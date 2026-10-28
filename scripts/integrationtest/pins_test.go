package integrationtest

import (
	"fmt"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// pinsRun feeds input to integration-pins.sh on stdin.
func pinsRun(c *qt.C, input string) scriptResult {
	c.Helper()
	return runScript(c, "integration-pins.sh", input, nil)
}

// pinsExpectOK asserts that input parses to the given ref, sticky and reset values.
func pinsExpectOK(t *testing.T, input, node string, sticky, reset bool) {
	t.Helper()
	c := qt.New(t)
	res := pinsRun(c, input)
	c.Assert(res.code, qt.Equals, 0, qt.Commentf("stderr: %s", res.stderr))
	c.Assert(res.stderr, qt.Equals, "")
	c.Assert(res.stdout, qt.Equals, fmt.Sprintf("vocdoni_node_ref=%s\nsticky=%t\nreset=%t\n", node, sticky, reset))
}

// pinsExpectError asserts that input is rejected with an ERROR line on stderr and nothing on stdout.
func pinsExpectError(t *testing.T, input string) {
	t.Helper()
	c := qt.New(t)
	res := pinsRun(c, input)
	c.Assert(res.code, qt.Equals, 1)
	c.Assert(res.stdout, qt.Equals, "")
	c.Assert(res.stderr, qt.Matches, "ERROR: [^\n]+\n")
}

func TestPinsAccepted(t *testing.T) {
	t.Run("bare command pins nothing and is one-shot", func(t *testing.T) {
		pinsExpectOK(t, "/integration", "", false, false)
		pinsExpectOK(t, "/integration\n", "", false, false)
	})

	t.Run("SHA-only refs are one-shot", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=1a2b3c4", "1a2b3c4", false, false)
		sha := strings.Repeat("a", 40)
		pinsExpectOK(t, "/integration vocdoni-node="+sha, sha, false, false)
		sha = "abcdef0123456789abcdef0123456789abcdef01"
		pinsExpectOK(t, "/integration vocdoni-node="+sha, sha, false, false)
	})

	t.Run("branch refs are sticky", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=abc", "abc", true, false)
		pinsExpectOK(t, "/integration vocdoni-node=release/1.0.0", "release/1.0.0", true, false)
		pinsExpectOK(t, "/integration vocdoni-node=feat/x.y", "feat/x.y", true, false)
		pinsExpectOK(t, "/integration vocdoni-node=pull/123/head", "pull/123/head", true, false)
	})

	t.Run("a #PR ref is accepted and sticky", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=#1", "#1", true, false)
		pinsExpectOK(t, "/integration vocdoni-node=#999999999", "#999999999", true, false)
	})

	t.Run("a bare PR number is the same as #N and sticky", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=1", "#1", true, false)
		pinsExpectOK(t, "/integration vocdoni-node=999999", "#999999", true, false)
	})

	t.Run("a bare PR number keeps its leading zeros", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=0760", "#0760", true, false)
	})

	t.Run("a bare number of 7 digits is a SHA, one-shot", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=1234567", "1234567", false, false)
	})

	t.Run("a ref of exactly 100 characters is accepted", func(t *testing.T) {
		ref := strings.Repeat("a", 100)
		pinsExpectOK(t, "/integration vocdoni-node="+ref, ref, true, false)
	})

	t.Run("only the first line is read", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=abc\nvocdoni-node=ignored\n/integration bogus!", "abc", true, false)
	})

	t.Run("a CRLF line ending is stripped", func(t *testing.T) {
		pinsExpectOK(t, "/integration vocdoni-node=1a2b3c4\r\nmore text\r\n", "1a2b3c4", false, false)
	})

	t.Run("extra whitespace and tabs are tolerated", func(t *testing.T) {
		pinsExpectOK(t, "/integration   vocdoni-node=1a2b3c4  ", "1a2b3c4", false, false)
		pinsExpectOK(t, "/integration\tvocdoni-node=1a2b3c4", "1a2b3c4", false, false)
	})

	t.Run("reset clears the sticky state", func(t *testing.T) {
		pinsExpectOK(t, "/integration reset", "", false, true)
		pinsExpectOK(t, "/integration reset\nmore text", "", false, true)
	})
}

func TestPinsRejected(t *testing.T) {
	t.Run("anything that is not the /integration command is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integrationx vocdoni-node=abc")
		pinsExpectError(t, "hello")
		pinsExpectError(t, "")
	})

	t.Run("an unknown key is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni=abc")
		pinsExpectError(t, "/integration vocdoni-node2=abc")
		pinsExpectError(t, "/integration saas-backend=abc")
		pinsExpectError(t, "/integration integrator-sdk=main")
	})

	t.Run("a token without = is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node")
	})

	t.Run("a key given twice is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=a vocdoni-node=b")
		pinsExpectError(t, "/integration vocdoni-node=a vocdoni-node=a")
	})

	t.Run("an empty ref is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=")
		c := qt.New(t)
		c.Assert(pinsRun(c, "/integration vocdoni-node=").stderr, qt.Contains, "empty ref")
	})

	t.Run("a ref containing .. is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=a..b")
		pinsExpectError(t, "/integration vocdoni-node=v1..v2")
	})

	t.Run("a ref starting with - is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=-rf")
		pinsExpectError(t, "/integration vocdoni-node=--upload-pack=x")
	})

	t.Run("a ref longer than 100 characters is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node="+strings.Repeat("a", 101))
	})

	t.Run("an uppercase key is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration Vocdoni-Node=abc")
	})

	t.Run("an = inside a ref is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=a=b")
	})

	t.Run("shell metacharacters are rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=a;b")
		pinsExpectError(t, "/integration vocdoni-node=$(id)")
	})

	t.Run("malformed #PR refs are rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration vocdoni-node=#")
		pinsExpectError(t, "/integration vocdoni-node=#abc")
		pinsExpectError(t, "/integration vocdoni-node=#1/2")
		pinsExpectError(t, "/integration vocdoni-node=#1234567890")
	})

	t.Run("reset with any other token is rejected", func(t *testing.T) {
		pinsExpectError(t, "/integration reset vocdoni-node=abc")
		pinsExpectError(t, "/integration reset reset")
		pinsExpectError(t, "/integration reset foo")
	})
}
