package errors

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"strings"
	"testing"
)

// TestErrorCodesAreUnique parses the current package's source files,
// finds all vars initialized with an Error{...} composite literal,
// pulls out the Code field, and fails if there are duplicates.
func TestErrorCodesAreUnique(t *testing.T) {
	// Reflection can’t list all package-level vars,
	// so the only way is to scan the package’s AST

	fset := token.NewFileSet()

	// Parse all non-test .go files in this directory
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		name := info.Name()
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse dir: %v", err)
	}

	// Find the current package (named "errors")
	pkg, ok := pkgs["errors"]
	if !ok {
		t.Fatalf("package 'errors' not found; got: %v", keys(pkgs))
	}

	type occ struct {
		varName string
		pos     token.Position
	}
	byCode := map[int][]occ{}

	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			gd, ok := n.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				return true
			}

			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				// We expect Name = Value pairs.
				for i, name := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					cl, ok := vs.Values[i].(*ast.CompositeLit)
					if !ok {
						continue
					}
					// Only consider composite literals of type Error (or pkg-qualified ...Error)
					if !isErrorComposite(cl) {
						continue
					}

					// Find Code: <int> inside the literal.
					if code, ok := extractCodeField(cl); ok {
						byCode[code] = append(byCode[code], occ{
							varName: name.Name,
							pos:     fset.Position(name.Pos()),
						})
					}
				}
			}
			return true
		})
	}

	var dups []string
	for code, occs := range byCode {
		if len(occs) > 1 {
			var refs []string
			for _, o := range occs {
				refs = append(refs, o.varName+"@"+o.pos.String())
			}
			dups = append(dups, strconv.Itoa(code)+": "+strings.Join(refs, ", "))
		}
	}
	if len(dups) > 0 {
		t.Fatalf("duplicate Error.Code values found:\n  %s", strings.Join(dups, "\n  "))
	}
}

// isErrorComposite returns true if the composite literal's type is named "Error"
// (either unqualified or selector-qualified, e.g., errors.Error).
func isErrorComposite(cl *ast.CompositeLit) bool {
	switch t := cl.Type.(type) {
	case *ast.Ident:
		return t.Name == "Error"
	case *ast.SelectorExpr:
		// e.g., somepkg.Error
		return t.Sel.Name == "Error"
	default:
		return false
	}
}

// extractCodeField looks for a "Code: <int>" entry in the composite literal.
func extractCodeField(cl *ast.CompositeLit) (int, bool) {
	for _, elt := range cl.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		keyIdent, ok := kv.Key.(*ast.Ident)
		if !ok || keyIdent.Name != "Code" {
			continue
		}
		if v, ok := kv.Value.(*ast.BasicLit); ok {
			if v.Kind == token.INT {
				// Accept 10, 0x..., with underscores.
				txt := strings.ReplaceAll(v.Value, "_", "")
				n, err := strconv.ParseInt(txt, 0, 32)
				if err == nil {
					return int(n), true
				}
			}
		}
	}
	return 0, false
}

func keys[M ~map[K]V, K comparable, V any](m M) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
