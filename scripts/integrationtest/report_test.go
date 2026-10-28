package integrationtest

import (
	"fmt"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

// reportVars are the variables integration-report.sh reads. reportRun sets every one of them,
// so nothing from the test runner's environment reaches the script.
var reportVars = []string{
	"EVENT", "REPO", "RUN_URL", "PARSED", "RESOLVE", "RESULT", "RESET", "STICKY",
	"CHECKOUT", "HEAD_SHA", "NODE_REF", "NODE_SHA", "FAILURE", "ERROR",
}

const (
	reportSHAApp   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	reportSHAMerge = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	reportSHANode  = "cccccccccccccccccccccccccccccccccccccccc"
	reportRunURL   = "https://github.com/vocdoni/saas-backend/actions/runs/1"
)

// reportRun runs integration-report.sh for a successful /integration comment run, with the
// variables in env overriding the defaults.
func reportRun(c *qt.C, env map[string]string) string {
	c.Helper()
	base := map[string]string{
		"EVENT":    "issue_comment",
		"REPO":     "vocdoni/saas-backend",
		"RUN_URL":  reportRunURL,
		"PARSED":   "true",
		"RESOLVE":  "success",
		"RESULT":   "success",
		"RESET":    "false",
		"STICKY":   "false",
		"CHECKOUT": reportSHAMerge,
		"HEAD_SHA": reportSHAApp,
		"NODE_REF": "",
		"NODE_SHA": "",
		"FAILURE":  "",
		"ERROR":    "",
	}
	var vars []string
	for _, k := range reportVars {
		v := base[k]
		if o, ok := env[k]; ok {
			v = o
		}
		vars = append(vars, k+"="+v)
	}
	res := runScript(c, "integration-report.sh", "", vars)
	c.Assert(res.stderr, qt.Equals, "")
	c.Assert(res.code, qt.Equals, 0)
	return res.stdout
}

// reportAppLine is the saas-backend line of a report for the merge of the test.
var reportAppLine = fmt.Sprintf("- saas-backend: merge of %s (%s)", reportSHAApp, reportSHAMerge)

func TestReportComment(t *testing.T) {
	t.Run("reports a successful /integration comment run", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{
			"NODE_REF": "1a2b3c4",
			"NODE_SHA": reportSHANode,
		})
		c.Assert(out, qt.Equals, strings.Join([]string{
			"✅ Integration run: **success**",
			"",
			reportAppLine,
			"- vocdoni-node: `1a2b3c4` → https://github.com/vocdoni/vocdoni-node/commit/" + reportSHANode,
			"- Run: " + reportRunURL,
			"",
			"One-shot run: pushes keep using the previous sticky ref, if any.",
			"",
		}, "\n"))
	})

	t.Run("links a bare PR number and says the refs are sticky", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{
			"NODE_REF": "#760",
			"NODE_SHA": "not-a-sha",
			"STICKY":   "true",
		})
		c.Assert(out, qt.Equals, strings.Join([]string{
			"✅ Integration run: **success**",
			"",
			reportAppLine,
			"- vocdoni-node: https://github.com/vocdoni/vocdoni-node/pull/760 → not built",
			"- Run: " + reportRunURL,
			"",
			"These refs are re-resolved on every push to this PR until `/integration reset`.",
			"",
		}, "\n"))
	})

	t.Run("says the vocdoni-node line is the go.mod version when unpinned", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{})
		c.Assert(out, qt.Contains, "- vocdoni-node: go.mod version\n")
	})

	t.Run("reports a failed run with its failure output in a collapsed block", func(t *testing.T) {
		c := qt.New(t)
		failure := strings.Join([]string{
			"  1) [chromium] › a.e2e.ts:1:1 › fails",
			"    Error: boom",
			"~~~~~",
			"   ~~~ stray",
			"after",
		}, "\n")
		out := reportRun(c, map[string]string{"RESULT": "failure", "FAILURE": failure})
		c.Assert(strings.HasPrefix(out, "❌ Integration run: **failure**\n"), qt.IsTrue)
		c.Assert(out, qt.Contains, strings.Join([]string{
			"",
			"<details><summary>Failure output</summary>",
			"",
			"~~~~~",
			"  1) [chromium] › a.e2e.ts:1:1 › fails",
			"    Error: boom",
			"after",
			"~~~~~",
			"</details>",
			"",
		}, "\n"))
		c.Assert(strings.Count(out, "~~~"), qt.Equals, 2)
	})

	t.Run("shows the parser error in a rejected reply", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{
			"PARSED":  "false",
			"RESULT":  "",
			"RESOLVE": "",
			"ERROR":   "ERROR: invalid ref 'https://x' for 'vocdoni-node'",
		})
		c.Assert(out, qt.Equals, strings.Join([]string{
			"❌ Integration run: **rejected**: `ERROR: invalid ref 'https://x' for 'vocdoni-node'`",
			"",
			"- Run: " + reportRunURL,
			"",
			"<details><summary>Failure output</summary>",
			"",
			"~~~~~",
			"ERROR: invalid ref 'https://x' for 'vocdoni-node'",
			"~~~~~",
			"</details>",
			"",
		}, "\n"))
	})

	t.Run("renders the error as inline code, with backticks removed and no mentions", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{"PARSED": "false", "RESULT": "", "RESOLVE": "", "ERROR": "bad `@user` ref"})
		c.Assert(strings.Split(out, "\n")[0], qt.Equals, "❌ Integration run: **rejected**: `bad @user ref`")
		resolveOut := reportRun(c, map[string]string{"RESOLVE": "failure", "ERROR": "cc @user `x`"})
		c.Assert(strings.Split(resolveOut, "\n")[0],
			qt.Equals, "❌ Integration run: **error** resolving the PR (failure): `cc @user x`")
	})

	t.Run("keeps the generic rejected reply when there is no parser error", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{"PARSED": "false", "RESULT": "", "RESOLVE": ""})
		c.Assert(out, qt.Equals, "❌ Integration run: **rejected**: the `/integration` command could not be parsed, "+
			"see the run log.\n")
	})

	t.Run("keeps the generic resolve error reply when there is no error text", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{"RESOLVE": "failure"})
		c.Assert(out, qt.Equals, "❌ Integration run: **error** resolving the PR (failure), see the run log; "+
			"comment again to retry.\n")
	})

	t.Run("reports a reset", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{"RESET": "true", "RESULT": ""})
		c.Assert(out, qt.Equals, strings.Join([]string{
			"Sticky integration ref cleared; pushes no longer run this workflow's tests.",
			"",
			"- Run: " + reportRunURL,
			"",
		}, "\n"))
	})

	t.Run("says an unresolved app merge", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{"CHECKOUT": "", "HEAD_SHA": ""})
		c.Assert(out, qt.Contains, "- saas-backend: not resolved\n")
	})
}

func TestReportPushRun(t *testing.T) {
	t.Run("titles a push run and says where its refs come from", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{
			"EVENT":    "pull_request",
			"NODE_REF": "#760",
			"NODE_SHA": reportSHANode,
		})
		c.Assert(out, qt.Equals, strings.Join([]string{
			"✅ Integration run on push: **success**",
			"",
			reportAppLine,
			"- vocdoni-node: https://github.com/vocdoni/vocdoni-node/pull/760 → " +
				"https://github.com/vocdoni/vocdoni-node/commit/" + reportSHANode,
			"- Run: " + reportRunURL,
			"",
			"The ref comes from this PR's sticky `/integration` comment, re-resolved on this push.",
			"",
		}, "\n"))
	})

	t.Run("includes the failure output of a failed push run", func(t *testing.T) {
		c := qt.New(t)
		out := reportRun(c, map[string]string{
			"EVENT":    "pull_request",
			"RESULT":   "failure",
			"NODE_REF": "feat/x",
			"FAILURE":  "--- FAIL: TestX (0.00s)\n    x_test.go:1: boom",
		})
		c.Assert(strings.HasPrefix(out, "❌ Integration run on push: **failure**\n"), qt.IsTrue)
		c.Assert(out, qt.Contains, "- vocdoni-node: `feat/x` → not built\n")
		c.Assert(out, qt.Contains, "<summary>Failure output</summary>")
		c.Assert(out, qt.Contains, "    x_test.go:1: boom\n~~~~~\n</details>\n")
	})
}
