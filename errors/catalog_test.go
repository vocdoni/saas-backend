package errors_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal/errorcatalog"
)

// TestErrorCatalogUpToDate fails when docs/api.md is out of sync with errors_definition.go.
// Fix it by running make swagger.
func TestErrorCatalogUpToDate(t *testing.T) {
	c := qt.New(t)

	_, thisFile, _, _ := runtime.Caller(0)
	errorsDir := filepath.Dir(thisFile)

	entries, err := errorcatalog.ParseDir(errorsDir)
	c.Assert(err, qt.IsNil)

	generated := errorcatalog.RenderMarkdown(entries)

	catalogPath := filepath.Join(errorsDir, "..", "docs", "api.md")
	committed, err := os.ReadFile(catalogPath)
	c.Assert(err, qt.IsNil)

	c.Assert(generated, qt.Equals, string(committed),
		qt.Commentf("docs/api.md is stale — run make swagger to regenerate"))
}
