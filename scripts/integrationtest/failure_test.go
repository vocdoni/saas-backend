package integrationtest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
)

// failureRun runs integration-failure.sh on a log with the given content. A nil log leaves the
// log argument out, and an absent log passes a path that does not exist.
func failureRun(c *qt.C, step string, log *string) scriptResult {
	c.Helper()
	args := []string{step}
	if log != nil {
		path := filepath.Join(c.TempDir(), "step.log")
		c.Assert(os.WriteFile(path, []byte(*log), 0o600), qt.IsNil)
		args = append(args, path)
	}
	return runScript(c, "integration-failure.sh", "", nil, args...)
}

// failureLines returns n lines of the form "line <i>", joined by newlines.
func failureLines(n int) string {
	lines := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	return strings.Join(lines, "\n")
}

// goFailLog is a `go test` run where TestOne fails with a subtest, with unindented log lines
// between the failure blocks.
const goFailLog = `=== RUN   TestOne
unrelated log line
--- FAIL: TestOne (0.00s)
    --- FAIL: TestOne/sub (0.00s)
        sub_test.go:20: sub detail
    one_test.go:10: expected 1, got 2
unrelated log line after the block
--- PASS: TestTwo (0.00s)
FAIL
FAIL	example.com/pkg	0.5s
`

func TestFailureGoTest(t *testing.T) {
	t.Run("keeps the --- FAIL blocks and the summary lines, drops other log lines", func(t *testing.T) {
		c := qt.New(t)
		log := goFailLog
		res := failureRun(c, "Run Go test", &log)
		c.Assert(res.code, qt.Equals, 0)
		c.Assert(res.stderr, qt.Equals, "")
		c.Assert(res.stdout, qt.Equals, "Run Go test\n"+
			"--- FAIL: TestOne (0.00s)\n"+
			"    --- FAIL: TestOne/sub (0.00s)\n"+
			"        sub_test.go:20: sub detail\n"+
			"    one_test.go:10: expected 1, got 2\n"+
			"FAIL\n"+
			"FAIL\texample.com/pkg\t0.5s\n")
	})

	t.Run("keeps a panic line", func(t *testing.T) {
		c := qt.New(t)
		log := "=== RUN   TestBoom\npanic: boom [recovered]\n\tpanic: boom\nFAIL\texample.com/pkg\t0.1s\n"
		res := failureRun(c, "Run Go test", &log)
		c.Assert(res.stdout, qt.Contains, "panic: boom [recovered]\n")
		c.Assert(res.stdout, qt.Contains, "FAIL\texample.com/pkg\t0.1s\n")
	})

	t.Run("falls back to the last 60 lines without a --- FAIL line", func(t *testing.T) {
		c := qt.New(t)
		log := failureLines(80) + "\n"
		res := failureRun(c, "Run Go test", &log)
		c.Assert(res.stdout, qt.Equals, "Run Go test\n"+strings.Join(strings.Split(failureLines(80), "\n")[20:], "\n")+"\n")
	})

	t.Run("a build error with no test output is shown in full when short", func(t *testing.T) {
		c := qt.New(t)
		log := "# example.com/pkg\n./main.go:3:2: undefined: x\nFAIL\texample.com/pkg [build failed]\n"
		res := failureRun(c, "Run Go test", &log)
		c.Assert(res.stdout, qt.Equals, "Run Go test\n"+log)
	})
}

func TestFailureOtherSteps(t *testing.T) {
	t.Run("another step gets the last 60 lines, even when it has --- FAIL lines", func(t *testing.T) {
		c := qt.New(t)
		log := "--- FAIL: TestX (0.00s)\n    x_test.go:1: nope\n" + failureLines(80) + "\n"
		res := failureRun(c, "Pin vocdoni-node", &log)
		c.Assert(res.stdout, qt.Equals, "Pin vocdoni-node\n"+
			strings.Join(strings.Split(failureLines(80), "\n")[20:], "\n")+"\n")
		c.Assert(res.stdout, qt.Not(qt.Contains), "--- FAIL: TestX")
	})
}

func TestFailureStripping(t *testing.T) {
	t.Run("strips ANSI colour codes and GitHub workflow command lines", func(t *testing.T) {
		c := qt.New(t)
		log := "##[group]Run go test\n\x1b[31m--- FAIL: TestA\x1b[0m (0.00s)\r\n" +
			"    a_test.go:1: boom\n::error::something\n##[error]Process completed with exit code 1.\n"
		res := failureRun(c, "Run Go test", &log)
		c.Assert(res.stdout, qt.Equals, "Run Go test\n--- FAIL: TestA (0.00s)\n    a_test.go:1: boom\n")
	})
}

func TestFailureCap(t *testing.T) {
	t.Run("caps the excerpt at 30000 bytes and keeps the end", func(t *testing.T) {
		c := qt.New(t)
		// Sixty 1000-byte lines: the tail alone is over the cap.
		lines := make([]string, 0, 2000)
		for i := range 2000 {
			lines = append(lines, strconv.Itoa(i)+" "+strings.Repeat("x", 1000))
		}
		log := strings.Join(lines, "\n") + "\n"
		res := failureRun(c, "Pin vocdoni-node", &log)
		c.Assert(len(res.stdout) <= 30000, qt.IsTrue)
		c.Assert(strings.HasPrefix(res.stdout, "Pin vocdoni-node\n…(truncated)\n"), qt.IsTrue)
		c.Assert(strings.HasSuffix(strings.TrimRight(res.stdout, "\n"), "1999 "+strings.Repeat("x", 1000)), qt.IsTrue)
	})

	t.Run("keeps the capped excerpt valid UTF-8 when the cut lands inside a character", func(t *testing.T) {
		c := qt.New(t)
		lines := make([]string, 60)
		for i := range lines {
			lines[i] = strings.Repeat("€", 1000)
		}
		log := strings.Join(lines, "\n") + "\n"
		res := failureRun(c, "Pin vocdoni-node", &log)
		c.Assert(res.code, qt.Equals, 0)
		c.Assert(len(res.stdout) <= 30000, qt.IsTrue)
		c.Assert(utf8.ValidString(res.stdout), qt.IsTrue)
		c.Assert(strings.HasPrefix(res.stdout, "Pin vocdoni-node\n…(truncated)\n"), qt.IsTrue)
		c.Assert(strings.HasSuffix(strings.TrimRight(res.stdout, "\n"), strings.Repeat("€", 1000)), qt.IsTrue)
	})

	t.Run("keeps a capped single-line excerpt valid UTF-8", func(t *testing.T) {
		c := qt.New(t)
		// One 60000-byte line: there is no line break to drop the partial character with.
		log := strings.Repeat("€", 20000) + "\n"
		res := failureRun(c, "Pin vocdoni-node", &log)
		c.Assert(res.code, qt.Equals, 0)
		c.Assert(len(res.stdout) <= 30000, qt.IsTrue)
		c.Assert(utf8.ValidString(res.stdout), qt.IsTrue)
		c.Assert(strings.HasPrefix(res.stdout, "Pin vocdoni-node\n…(truncated)\n€"), qt.IsTrue)
	})
}

func TestFailureMissingLog(t *testing.T) {
	t.Run("says so when the log file does not exist", func(t *testing.T) {
		c := qt.New(t)
		res := runScript(c, "integration-failure.sh", "", nil, "Start stack", filepath.Join(c.TempDir(), "absent.log"))
		c.Assert(res.code, qt.Equals, 0)
		c.Assert(res.stdout, qt.Equals, "Start stack\nno log was captured for this step.\n")
	})

	t.Run("says so when the log is empty", func(t *testing.T) {
		c := qt.New(t)
		empty := ""
		res := failureRun(c, "Start stack", &empty)
		c.Assert(res.stdout, qt.Equals, "Start stack\nno log was captured for this step.\n")
	})

	t.Run("says so when no log argument is given", func(t *testing.T) {
		c := qt.New(t)
		res := failureRun(c, "Start stack", nil)
		c.Assert(res.stdout, qt.Equals, "Start stack\nno log was captured for this step.\n")
	})
}
