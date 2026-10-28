package integrationtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// stickyPerms is the permission table the fake gh answers from.
const stickyPerms = "alice=write,bob=read,carol=maintain"

// stickyWriter, stickyMaintainer and stickyReader are logins in stickyPerms.
const (
	stickyWriter     = "alice"
	stickyMaintainer = "carol"
	stickyReader     = "bob"
)

// stickyFakeGh answers `gh api repos/<repo>/collaborators/<login>/permission` from FAKE_PERMS
// ("login=permission,..."), exits 1 for unknown logins, and appends each lookup to FAKE_GH_LOG.
const stickyFakeGh = `#!/usr/bin/env bash
case "$2" in
  repos/*/collaborators/*/permission)
    login="${2#*/collaborators/}"
    login="${login%/permission}"
    ;;
  *) echo "fake gh: unexpected $*" >&2; exit 2 ;;
esac
echo "$login" >> "$FAKE_GH_LOG"
for pair in ${FAKE_PERMS//,/ }; do
  if [ "${pair%%=*}" = "$login" ]; then
    echo "${pair#*=}"
    exit 0
  fi
done
echo "HTTP 404: Not Found" >&2
exit 1
`

// stickyUser and stickyComment mirror the fields of an issue comment that the script reads.
type stickyUser struct {
	Login string `json:"login"`
}

type stickyComment struct {
	Body string     `json:"body"`
	User stickyUser `json:"user"`
}

// stickyCmt builds a comment posted by login.
func stickyCmt(login, body string) stickyComment {
	return stickyComment{Body: body, User: stickyUser{Login: login}}
}

// stickyEnv returns the variables that put a fake gh first on PATH, set REPO to owner/repo and
// point the fake gh at a log file, together with the path of that log file.
func stickyEnv(t *testing.T) ([]string, string) {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "calls.log")
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(stickyFakeGh), 0o755); err != nil {
		t.Fatalf("writing fake gh: %v", err)
	}
	env := []string{
		"PATH=" + binDir + string(filepath.ListSeparator) + os.Getenv("PATH"),
		"REPO=owner/repo",
		"FAKE_PERMS=" + stickyPerms,
		"FAKE_GH_LOG=" + logPath,
	}
	return env, logPath
}

// stickyInput encodes comments as the JSON array the script reads on stdin.
func stickyInput(t *testing.T, comments ...stickyComment) string {
	t.Helper()
	if comments == nil {
		comments = []stickyComment{}
	}
	data, err := json.Marshal(comments)
	if err != nil {
		t.Fatalf("encoding comments: %v", err)
	}
	return string(data)
}

// stickyExpect asserts that comments derive the sticky ref node, with no stderr output.
func stickyExpect(t *testing.T, node string, comments ...stickyComment) {
	t.Helper()
	c := qt.New(t)
	env, _ := stickyEnv(t)
	res := runScript(c, "integration-sticky.sh", stickyInput(t, comments...), env)
	c.Assert(res.code, qt.Equals, 0, qt.Commentf("stderr: %s", res.stderr))
	c.Assert(res.stderr, qt.Equals, "")
	c.Assert(res.stdout, qt.Equals, fmt.Sprintf("vocdoni_node_ref=%s\n", node))
}

func TestStickyDerivation(t *testing.T) {
	t.Run("an empty array has no sticky state", func(t *testing.T) {
		stickyExpect(t, "")
	})

	t.Run("a sticky comment sets the state", func(t *testing.T) {
		stickyExpect(t, "#760", stickyCmt(stickyWriter, "/integration vocdoni-node=#760"))
	})

	t.Run("a bare PR number comment sets the state as #N", func(t *testing.T) {
		stickyExpect(t, "#760", stickyCmt(stickyWriter, "/integration vocdoni-node=760"))
	})

	t.Run("a SHA-only comment does not change the state", func(t *testing.T) {
		stickyExpect(t, "#760",
			stickyCmt(stickyWriter, "/integration vocdoni-node=#760"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=1a2b3c4"),
		)
	})

	t.Run("reset clears the state", func(t *testing.T) {
		stickyExpect(t, "",
			stickyCmt(stickyWriter, "/integration vocdoni-node=#760"),
			stickyCmt(stickyWriter, "/integration reset"),
		)
	})

	t.Run("a later sticky comment replaces the earlier one", func(t *testing.T) {
		stickyExpect(t, "#2",
			stickyCmt(stickyWriter, "/integration vocdoni-node=feat/a"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=#2"),
		)
	})

	t.Run("a writer sticky comment counts", func(t *testing.T) {
		stickyExpect(t, "feat/a", stickyCmt(stickyWriter, "/integration vocdoni-node=feat/a"))
	})

	t.Run("a maintain sticky comment counts", func(t *testing.T) {
		stickyExpect(t, "feat/a", stickyCmt(stickyMaintainer, "/integration vocdoni-node=feat/a"))
	})

	t.Run("a read-only user sticky comment is ignored", func(t *testing.T) {
		stickyExpect(t, "", stickyCmt(stickyReader, "/integration vocdoni-node=feat/b"))
	})

	t.Run("a read-only user reset is ignored", func(t *testing.T) {
		stickyExpect(t, "#760",
			stickyCmt(stickyWriter, "/integration vocdoni-node=#760"),
			stickyCmt(stickyReader, "/integration reset"),
		)
	})

	t.Run("an unknown login is ignored with a warning, not a failure", func(t *testing.T) {
		c := qt.New(t)
		env, _ := stickyEnv(t)
		res := runScript(c, "integration-sticky.sh", stickyInput(t,
			stickyCmt("mallory", "/integration vocdoni-node=feat/a"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=#760"),
		), env)
		c.Assert(res.code, qt.Equals, 0, qt.Commentf("stderr: %s", res.stderr))
		c.Assert(res.stderr, qt.Contains, "::warning::could not check mallory's permission")
		c.Assert(res.stdout, qt.Equals, "vocdoni_node_ref=#760\n")
	})

	t.Run("each login is looked up once", func(t *testing.T) {
		c := qt.New(t)
		env, logPath := stickyEnv(t)
		res := runScript(c, "integration-sticky.sh", stickyInput(t,
			stickyCmt(stickyWriter, "/integration vocdoni-node=feat/a"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=feat/b"),
			stickyCmt(stickyReader, "just chatting"),
		), env)
		c.Assert(res.code, qt.Equals, 0, qt.Commentf("stderr: %s", res.stderr))
		c.Assert(res.stdout, qt.Equals, "vocdoni_node_ref=feat/b\n")
		data, err := os.ReadFile(logPath)
		c.Assert(err, qt.IsNil)
		c.Assert(strings.Split(strings.TrimSpace(string(data)), "\n"), qt.DeepEquals, []string{stickyWriter})
	})

	t.Run("usage is printed and the script fails when REPO is unset", func(t *testing.T) {
		c := qt.New(t)
		env, _ := stickyEnv(t)
		res := runScript(c, "integration-sticky.sh", "[]", append(env, "REPO="))
		c.Assert(res.code, qt.Equals, 1)
		c.Assert(res.stderr, qt.Contains, "usage: REPO=owner/repo")
	})

	t.Run("malformed /integration comments are ignored", func(t *testing.T) {
		stickyExpect(t, "#760",
			stickyCmt(stickyWriter, "/integration vocdoni-node=#760"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=#abc"),
			stickyCmt(stickyWriter, "/integration vocdoni-node=#1 vocdoni-node=#2"),
			stickyCmt(stickyWriter, "/integration reset now"),
			stickyCmt(stickyWriter, "just chatting"),
		)
	})

	t.Run("only the first line of a multi-line body is read", func(t *testing.T) {
		stickyExpect(t, "feat/a", stickyCmt(stickyWriter,
			"/integration vocdoni-node=feat/a\n\nSome notes with `backticks` and \"quotes\",\n/integration reset",
		))
	})

	t.Run("a valid first line followed by a body over 70000 bytes is accepted", func(t *testing.T) {
		body := "/integration vocdoni-node=#760\n" + strings.Repeat("x", 70000)
		stickyExpect(t, "#760", stickyCmt(stickyWriter, body))
	})
}
