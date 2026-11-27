// Package errorcatalog parses the errors package source and renders the API error catalog.
// Used by scripts/gen-error-codes to produce docs/api.md and by the staleness test.
package errorcatalog

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Entry is a single API error code entry.
type Entry struct {
	GoName     string
	Code       int
	HTTPStatus int
	Message    string
}

// ParseDir parses all non-test .go files in dir and returns all Error entries sorted by Code.
// It returns an error if any Error{} composite literal cannot be fully resolved — missing or
// non-integer Code, unrecognised http.Status* constant, or non-literal Err message.
func ParseDir(dir string) ([]Entry, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	fset := token.NewFileSet()
	var entries []Entry

	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fullPath := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, fullPath, nil, 0)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}

		var visitErr error
		ast.Inspect(f, func(n ast.Node) bool {
			if visitErr != nil {
				return false
			}
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, varName := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					cl, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok || !isErrorType(cl) {
						continue
					}
					e, err := extractEntry(varName.Name, cl)
					if err != nil {
						visitErr = fmt.Errorf("%s: %w", name, err)
						return false
					}
					entries = append(entries, e)
				}
			}
			return true
		})
		if visitErr != nil {
			return nil, visitErr
		}
	}

	slices.SortFunc(entries, func(a, b Entry) int { return cmp.Compare(a.Code, b.Code) })
	return entries, nil
}

// RenderMarkdown renders the error catalog as a markdown table for the swagger API description.
func RenderMarkdown(entries []Entry) string {
	var sb strings.Builder
	sb.WriteString("API for Vocdoni SaaS Backend.\n\n")
	sb.WriteString("Errors are returned as JSON: `{\"error\":\"message\",\"code\":4xxxx}`.\n")
	sb.WriteString("Codes 4xxxx are client errors (HTTP 4xx); codes 5xxxx are server errors (HTTP 5xx).\n\n")
	sb.WriteString("## Error Codes\n\n")
	sb.WriteString("| Code | HTTP Status | Message | Go name |\n")
	sb.WriteString("|------|-------------|---------|--------|\n")
	for _, e := range entries {
		status := fmt.Sprintf("%d %s", e.HTTPStatus, http.StatusText(e.HTTPStatus))
		fmt.Fprintf(&sb, "| %d | %s | %s | `%s` |\n", e.Code, status, e.Message, e.GoName)
	}
	return sb.String()
}

// isErrorType returns true if the composite literal's type is named "Error".
func isErrorType(cl *ast.CompositeLit) bool {
	switch t := cl.Type.(type) {
	case *ast.Ident:
		return t.Name == "Error"
	case *ast.SelectorExpr:
		return t.Sel.Name == "Error"
	default:
		return false
	}
}

// extractEntry extracts a catalog entry from an Error{} composite literal.
// It returns an error (naming the variable) if any required field cannot be resolved,
// so callers never silently drop entries.
func extractEntry(varName string, cl *ast.CompositeLit) (Entry, error) {
	var e Entry
	e.GoName = varName
	codeOK, statusOK, msgOK := false, false, false

	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Code":
			lit, ok := kv.Value.(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return Entry{}, fmt.Errorf("%s: Code is not an integer literal", varName)
			}
			txt := strings.ReplaceAll(lit.Value, "_", "")
			n, err := strconv.ParseInt(txt, 0, 32)
			if err != nil {
				return Entry{}, fmt.Errorf("%s: Code %q: %w", varName, lit.Value, err)
			}
			e.Code = int(n)
			codeOK = true

		case "HTTPstatus":
			sel, ok := kv.Value.(*ast.SelectorExpr)
			if !ok {
				return Entry{}, fmt.Errorf("%s: HTTPstatus is not a selector expression", varName)
			}
			v, ok := allHTTPStatuses[sel.Sel.Name]
			if !ok {
				return Entry{}, fmt.Errorf("%s: unknown http status %q", varName, sel.Sel.Name)
			}
			e.HTTPStatus = v
			statusOK = true

		case "Err":
			// Must be fmt.Errorf("literal string").
			call, ok := kv.Value.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return Entry{}, fmt.Errorf("%s: Err field must be fmt.Errorf(\"literal\")", varName)
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return Entry{}, fmt.Errorf("%s: Err field message is not a string literal in fmt.Errorf", varName)
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return Entry{}, fmt.Errorf("%s: Err unquoting message: %w", varName, err)
			}
			e.Message = s
			msgOK = true

		default:
			// LogLevel and Data fields are not needed for the catalog.
		}
	}

	switch {
	case !codeOK:
		return Entry{}, fmt.Errorf("%s: missing Code field", varName)
	case !statusOK:
		return Entry{}, fmt.Errorf("%s: missing HTTPstatus field", varName)
	case !msgOK:
		return Entry{}, fmt.Errorf("%s: missing Err field", varName)
	}
	return e, nil
}

// allHTTPStatuses maps every net/http Status* constant name to its integer value.
// Values are taken directly from net/http so they stay correct across Go versions.
// If an Error{} in errors_definition.go uses a constant missing from this map,
// ParseDir returns an error naming the variable — add the missing constant here.
var allHTTPStatuses = map[string]int{
	// 1xx
	"StatusContinue":           http.StatusContinue,
	"StatusSwitchingProtocols": http.StatusSwitchingProtocols,
	"StatusProcessing":         http.StatusProcessing,
	"StatusEarlyHints":         http.StatusEarlyHints,
	// 2xx
	"StatusOK":                   http.StatusOK,
	"StatusCreated":              http.StatusCreated,
	"StatusAccepted":             http.StatusAccepted,
	"StatusNonAuthoritativeInfo": http.StatusNonAuthoritativeInfo,
	"StatusNoContent":            http.StatusNoContent,
	"StatusResetContent":         http.StatusResetContent,
	"StatusPartialContent":       http.StatusPartialContent,
	"StatusMultiStatus":          http.StatusMultiStatus,
	"StatusAlreadyReported":      http.StatusAlreadyReported,
	"StatusIMUsed":               http.StatusIMUsed,
	// 3xx
	"StatusMultipleChoices":   http.StatusMultipleChoices,
	"StatusMovedPermanently":  http.StatusMovedPermanently,
	"StatusFound":             http.StatusFound,
	"StatusSeeOther":          http.StatusSeeOther,
	"StatusNotModified":       http.StatusNotModified,
	"StatusUseProxy":          http.StatusUseProxy,
	"StatusTemporaryRedirect": http.StatusTemporaryRedirect,
	"StatusPermanentRedirect": http.StatusPermanentRedirect,
	// 4xx
	"StatusBadRequest":                   http.StatusBadRequest,
	"StatusUnauthorized":                 http.StatusUnauthorized,
	"StatusPaymentRequired":              http.StatusPaymentRequired,
	"StatusForbidden":                    http.StatusForbidden,
	"StatusNotFound":                     http.StatusNotFound,
	"StatusMethodNotAllowed":             http.StatusMethodNotAllowed,
	"StatusNotAcceptable":                http.StatusNotAcceptable,
	"StatusProxyAuthRequired":            http.StatusProxyAuthRequired,
	"StatusRequestTimeout":               http.StatusRequestTimeout,
	"StatusConflict":                     http.StatusConflict,
	"StatusGone":                         http.StatusGone,
	"StatusLengthRequired":               http.StatusLengthRequired,
	"StatusPreconditionFailed":           http.StatusPreconditionFailed,
	"StatusRequestEntityTooLarge":        http.StatusRequestEntityTooLarge,
	"StatusRequestURITooLong":            http.StatusRequestURITooLong,
	"StatusUnsupportedMediaType":         http.StatusUnsupportedMediaType,
	"StatusRequestedRangeNotSatisfiable": http.StatusRequestedRangeNotSatisfiable,
	"StatusExpectationFailed":            http.StatusExpectationFailed,
	"StatusTeapot":                       http.StatusTeapot,
	"StatusMisdirectedRequest":           http.StatusMisdirectedRequest,
	"StatusUnprocessableEntity":          http.StatusUnprocessableEntity,
	"StatusLocked":                       http.StatusLocked,
	"StatusFailedDependency":             http.StatusFailedDependency,
	"StatusTooEarly":                     http.StatusTooEarly,
	"StatusUpgradeRequired":              http.StatusUpgradeRequired,
	"StatusPreconditionRequired":         http.StatusPreconditionRequired,
	"StatusTooManyRequests":              http.StatusTooManyRequests,
	"StatusRequestHeaderFieldsTooLarge":  http.StatusRequestHeaderFieldsTooLarge,
	"StatusUnavailableForLegalReasons":   http.StatusUnavailableForLegalReasons,
	// 5xx
	"StatusInternalServerError":           http.StatusInternalServerError,
	"StatusNotImplemented":                http.StatusNotImplemented,
	"StatusBadGateway":                    http.StatusBadGateway,
	"StatusServiceUnavailable":            http.StatusServiceUnavailable,
	"StatusGatewayTimeout":                http.StatusGatewayTimeout,
	"StatusHTTPVersionNotSupported":       http.StatusHTTPVersionNotSupported,
	"StatusVariantAlsoNegotiates":         http.StatusVariantAlsoNegotiates,
	"StatusInsufficientStorage":           http.StatusInsufficientStorage,
	"StatusLoopDetected":                  http.StatusLoopDetected,
	"StatusNotExtended":                   http.StatusNotExtended,
	"StatusNetworkAuthenticationRequired": http.StatusNetworkAuthenticationRequired,
}
