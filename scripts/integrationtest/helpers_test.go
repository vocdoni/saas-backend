package integrationtest

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	qt "github.com/frankban/quicktest"
)

// scriptResult is the outcome of running one of the integration scripts.
type scriptResult struct {
	stdout string
	stderr string
	code   int
}

// runScript runs scripts/<name> with bash, feeding stdin and adding env to the current environment.
func runScript(c *qt.C, name, stdin string, env []string, args ...string) scriptResult {
	c.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join("..", name)}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else {
		c.Assert(err, qt.IsNil, qt.Commentf("running %s: %v", name, err))
	}
	return scriptResult{stdout: stdout.String(), stderr: stderr.String(), code: code}
}
