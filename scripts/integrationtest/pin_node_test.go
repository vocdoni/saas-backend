package integrationtest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// pinNodeRun runs integration-pin-node.sh with the given arguments. Only offline paths may be
// exercised with it: every call must fail before the script reaches the GitHub API.
func pinNodeRun(c *qt.C, args ...string) scriptResult {
	c.Helper()
	return runScript(c, "integration-pin-node.sh", "", nil, args...)
}

func TestPinNodeWrongArgc(t *testing.T) {
	c := qt.New(t)
	for _, args := range [][]string{
		{},
		{"main"},
		{"main", t.TempDir(), "extra"},
	} {
		c.Run(fmt.Sprintf("%d args", len(args)), func(c *qt.C) {
			res := pinNodeRun(c, args...)
			c.Assert(res.code, qt.Equals, 1)
			c.Assert(res.stdout, qt.Equals, "")
			c.Assert(res.stderr, qt.Contains, "usage:")
		})
	}
}

func TestPinNodeInvalidRef(t *testing.T) {
	c := qt.New(t)
	for _, ref := range []string{
		"-rf",
		"a..b",
		"a;b",
		"#",
		"#abc",
		"#1234567890",
		strings.Repeat("a", 101),
		"",
	} {
		c.Run(fmt.Sprintf("ref=%q", ref), func(c *qt.C) {
			res := pinNodeRun(c, ref, t.TempDir())
			c.Assert(res.code, qt.Equals, 1)
			c.Assert(res.stdout, qt.Equals, "")
			c.Assert(strings.HasPrefix(res.stderr, "ERROR: "), qt.IsTrue, qt.Commentf("stderr: %s", res.stderr))
		})
	}
}

func TestPinNodeNonEmptyCheckoutDir(t *testing.T) {
	c := qt.New(t)
	dir := t.TempDir()
	c.Assert(os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("x"), 0o600), qt.IsNil)

	res := pinNodeRun(c, "main", dir)
	c.Assert(res.code, qt.Equals, 1)
	c.Assert(res.stdout, qt.Equals, "")
	c.Assert(res.stderr, qt.Contains, "not empty")
}
