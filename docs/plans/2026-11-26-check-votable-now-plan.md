# /check votableNow and reason: implementation plan

Spec: docs/specs/2026-11-26-check-votable-now-design.md (approved 2026-11-26)
Class: architectural
Repos and branches: /home/lucasmenendez/saas-backend/.worktrees/feature-saas-qbq-epic → feat/900-check-votable-now (base 1f7d66e1)

## Global Constraints
- Commit convention: conventional commits (`feat(csp): ...`, `test(csp): ...`), header ≤100 chars,
  no AI attribution or Co-Authored-By trailers. Every commit unsigned:
  `git -c commit.gpgsign=false commit -m "..." -- <paths>`. Never sign, never change git config.
- Never push; commit with explicit paths only (no `git add -A`). Never use bare `git stash`.
- Build output under `~/.cache/saas-backend/`, never /tmp.
- Do not dispatch subagents.
- Go conventions (AGENTS.md): `fmt.Errorf` not `errors.New`, lowercase log/error messages,
  gofumpt, line length 130, godoc on every exported entity starting with its name, quicktest
  matchers (`qt.HasLen`, `qt.IsNil`, `qt.IsTrue`...; `scripts/check-qt-patterns.sh` must pass).
- Comments: 1-3 lines, carry the why, not a restatement of the code.
- Unchanged behaviour: every signing endpoint (sign, sign-batch, blind-point, blind-sign) keeps its
  status codes and error messages exactly ("question is not open for voting", "process has not
  started yet", "process has ended"). `canVote`, `hasSigned`, `hasVoted`, `belongsToProcess` keep
  their current values.
- Tests: run unit tests only (`go test ./csp/handlers/`). The `./api` integration suite is left to
  CI; for api test changes only make sure they compile with `go vet ./api/`.
- Lint: `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=1f7d66e1 ./csp/... ./api/...`
  (the system golangci-lint cannot type-check this Go version).
- Swagger: `PATH=$(go env GOPATH)/bin:$PATH make swagger` after changing swag annotations or wire
  types; commit `docs/swagger.yaml` with the change.

## Review Focus
- `csp/handlers/processes.go:75-98`: `processEnded`, `unvotableElection` — the signing path's
  authorization must decide exactly as before.
- `csp/handlers/processes.go` `ProcessCheckHandler` (~:1008-1112): error mapping of the org-member
  and census lookups (not-found vs transient), the budget rule (blind `Used` vs ECDSA `TimesVoted`).
- `csp/handlers/cert_auth.go:183`: caller of `processEnded`, must keep its semantics.

## Task order and parallelism
| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 | — | csp/handlers/{processes.go,types.go,cert_auth.go,handlers_test.go}, docs/swagger.yaml | — |
| 2 | 1 | api/processes_csp_test.go | — |

## Tasks

### Task 1: reason logic, /check wiring and swagger
Model: standard   Repo: as above   Files: csp/handlers/types.go, csp/handlers/processes.go,
csp/handlers/cert_auth.go, csp/handlers/handlers_test.go, docs/swagger.yaml
Goal: spec §Design in full; AC1, AC2, AC4, AC5.

Step 1: types (`csp/handlers/types.go`). Add the `VoteReason` type and the ten constants exactly as
in spec §Types (godoc on the type; one short line per constant is enough). Add to
`ProcessQuestionStatus`, after `CanVote`:
```go
	// VotableNow is true when a sign request for this question, made now with this token, would
	// pass every authorization check the CSP runs; Reason says why not otherwise.
	VotableNow bool       `json:"votableNow"`
	Reason     VoteReason `json:"reason,omitempty"`
```
and rewrite `CanVote`'s doc (in the struct comment or as a field comment) to: eligible — in the
census, in the question's eligibility list, and the process has not ended; it ignores pause,
start date, weight and signature budget.

Step 2: pure functions (`csp/handlers/processes.go`), replacing `processEnded` / `unvotableElection`:
```go
// processEnded reports whether the process is past its end (plus endGrace), when it has one.
func processEnded(vp *db.VotingProcess, now time.Time) bool {
	return !vp.EndDate.IsZero() && !now.Before(vp.EndDate.Add(endGrace))
}

// processNotStarted reports whether the process start (minus startGrace) is still ahead.
func processNotStarted(vp *db.VotingProcess, now time.Time) bool {
	// publish always persists a StartDate, while EndDate is optional.
	return !vp.StartDate.IsZero() && now.Add(startGrace).Before(vp.StartDate)
}

// electionStateReason is the single rule for whether a question's election accepts a signature
// at now, shared by signing and /check. It returns "" when it does.
func electionStateReason(vp *db.VotingProcess, q *db.VotingProcessQuestion, now time.Time) VoteReason {
	switch {
	case q.Status == db.QuestionStatusCanceled:
		return VoteReasonCanceled
	case q.Status == db.QuestionStatusEnded, q.Status == db.QuestionStatusResults, processEnded(vp, now):
		return VoteReasonClosed
	case processNotStarted(vp, now):
		return VoteReasonNotStarted
	case q.Status != db.QuestionStatusReady:
		return VoteReasonPaused
	}
	return ""
}

// unvotableElection returns the 401 to write when the question's election does not currently
// accept a signature, or nil when it does. <keep the existing second paragraph about the CSP
// being the only place that can refuse on election state>
func unvotableElection(vp *db.VotingProcess, q *db.VotingProcessQuestion) *errors.Error {
	now := time.Now()
	if electionStateReason(vp, q, now) == "" {
		return nil
	}
	// messages keep their historical order (status, start, end), independent of /check's precedence
	switch {
	case q.Status != db.QuestionStatusReady:
		return errors.Ptr(errors.ErrUnauthorized.Withf("question is not open for voting"))
	case processNotStarted(vp, now):
		return errors.Ptr(errors.ErrUnauthorized.Withf("process has not started yet"))
	default:
		return errors.Ptr(errors.ErrUnauthorized.Withf("process has ended"))
	}
}

// voteReasonInput is what /check knows about one question for one voter.
type voteReasonInput struct {
	inCensus    bool
	eligible    bool
	zeroWeight  bool
	published   bool
	state       VoteReason // from electionStateReason
	budgetSpent bool
	unavailable bool
}

// voteReason applies /check's precedence: permanent reasons before temporary ones.
func voteReason(in voteReasonInput) VoteReason {
	switch {
	case !in.inCensus:
		return VoteReasonNotInCensus
	case !in.eligible:
		return VoteReasonNotEligible
	case in.zeroWeight:
		return VoteReasonZeroWeight
	case !in.published:
		return VoteReasonNotPublished
	case in.state == VoteReasonCanceled, in.state == VoteReasonClosed:
		return in.state
	case in.budgetSpent:
		return VoteReasonSignatureLimitReached
	case in.state != "":
		return in.state
	case in.unavailable:
		return VoteReasonUnavailable
	}
	return ""
}
```
Update `cert_auth.go:183` to `processEnded(vp, time.Now())` (same semantics).

Step 3: `ProcessCheckHandler` (`csp/handlers/processes.go`):
- `now := time.Now()` once at the top of the handler.
- Replace the swallowed `orgMember` block with:
  - `member, mErr := c.orgMember(vp.OrgAddress, auth)`;
  - `memberFound := mErr == nil`; if `mErr != nil && !errors.Is(mErr, db.ErrNotFound) &&
    !errors.Is(mErr, db.ErrInvalidData)` → `errors.ErrGenericInternalServerError.WithErr(mErr).Write(w)`
    and return;
  - when `memberFound`: load the census; on error `errors.ErrCensusNotFound.WithErr(err).Write(w)`
    and return; `weight` as today (`member.Weight` if weighted, else 1); `resp.Weight` set as today;
    `zeroWeight := census.Weighted && member.Weight == 0`; `anonymous := census.Anonymous`.
  - When not found, `resp.Weight` stays unset exactly as today.
- `CanVote` uses `processEnded(vp, now)`.
- After the CSP-row loop (rows known), compute per question:
  ```go
  row := rows[i]
  budgetSpent := row != nil && ((anonymous && row.Used) ||
      (!anonymous && row.TimesVoted >= db.MaxCSPSignaturesPerElection))
  ```
  and `Reason = voteReason(voteReasonInput{inCensus: resp.BelongsToProcess && memberFound,
  eligible: memberEligibleForQuestion(q, memberID), zeroWeight, published: len(q.UpstreamID) > 0,
  state: electionStateReason(vp, q, now), budgetSpent, unavailable: anonymous &&
  c.csp.BlindCSPDisabled()})`, `VotableNow = Reason == ""`. Do this before or after the
  vote-status goroutines, but never inside them (each goroutine writes only HasSigned/HasVoted).
- Warn once per call (a bool flag) when a published question has a status outside
  READY/PAUSED/ENDED/CANCELED/RESULTS: `log.Warnw("unexpected question status", "processId",
  oid.Hex(), "questionId", q.ID.Hex(), "status", q.Status)`.
- Swag description of the handler: append the meaning of `canVote`, `votableNow` and `reason`
  per spec §ProcessCheckHandler last bullet (unknown reason → generic message; absent
  `votableNow` from an older backend → unknown, not false; `sign-batch` or `blind-point` for an
  anonymous census; per-attempt outcomes still possible). Keep `@Description` lines ≤130 chars.

Step 4: unit tests (`csp/handlers/handlers_test.go`), replacing `TestUnvotableElectionEndDate`
with tests that use a fixed `now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)`:
- `TestElectionStateReason`: table over every status (`READY`, `PAUSED`, `ENDED`, `RESULTS`,
  `CANCELED`, `""`, `"BOGUS"`) × dates (none; `StartDate = now+startGrace` → votable;
  `now+startGrace+1ns` → not_started; `EndDate = now-endGrace` → closed; `now-endGrace+1ns` →
  open), with the expected reason per spec precedence (canceled beats closed beats not_started
  beats paused).
- `TestUnvotableElectionMatchesState`: for the same grid built relative to `time.Now()` with a
  margin of ≥1 s around each boundary (unvotableElection reads the clock itself), assert
  `(electionStateReason(vp,q,time.Now()) == "") == (unvotableElection(vp,q) == nil)`, and the three
  messages: paused+not-started → "question is not open for voting"; READY+not-started → "process
  has not started yet"; READY+ended → "process has ended". Compare a nil result with
  `qt.Equals, (*errors.Error)(nil)` (qt.IsNil rejects a typed nil).
- `TestVoteReason`: table covering each reason alone, `""` when all clear, and precedence cases:
  not eligible + canceled → not_eligible; not in census + not eligible → not_in_census; canceled +
  budgetSpent → canceled; paused + budgetSpent → signature_limit_reached; not_started +
  budgetSpent → signature_limit_reached; paused + unavailable → paused; unpublished + paused →
  not_published.
Run: `go test -count=1 -run 'TestElectionStateReason|TestUnvotableElection|TestVoteReason' ./csp/handlers/`
Expected: `ok`. Then `go test -count=1 ./csp/...` → `ok` (needs Docker; it is the unit-level
package suite, allowed).

Step 5: swagger and lint.
Run: `PATH=$(go env GOPATH)/bin:$PATH make swagger`; check `docs/swagger.yaml` has
`handlers.VoteReason` with the ten values (else add `enums:"..."` to the field and regenerate),
and `votableNow`/`reason` on `handlers.ProcessQuestionStatus`.
Run: `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=1f7d66e1 ./csp/...` → no issues;
`./scripts/check-qt-patterns.sh` → clean; `go vet ./...` → clean.

Step 6: commit
`git -c commit.gpgsign=false commit -m "feat(csp): /check reports per question votableNow and reason" -- csp/handlers/types.go csp/handlers/processes.go csp/handlers/cert_auth.go csp/handlers/handlers_test.go docs/swagger.yaml`

Interfaces this task provides: `handlers.VoteReason` + constants, `ProcessQuestionStatus.VotableNow`,
`ProcessQuestionStatus.Reason`.

### Task 2: integration tests
Model: standard   Repo: as above   Files: api/processes_csp_test.go
Goal: AC3.

Read `api/processes_csp_test.go` first and reuse its helpers (`publishCensusProcess`,
`publishProcessRequest`, `authProcessCSP`, `verifyProcessCSP`, `setupRelayVoting`,
`requestAndParse[T]`), and the pattern in `TestProcessCSPSignRefusedAfterEnd` of editing the
stored process/questions through `testDB` (`SetVotingProcess`, `SetQuestionStatus`,
`SetOrgMember`) rather than sending on-chain status txs: the CSP authorizes from stored state, so
this is what `/check` and signing read, and it keeps the test fast.

Add `TestProcessCSPCheckVotableNow` (subtests allowed, one process setup reused where state
changes don't interfere; otherwise a fresh setup per subtest) asserting, through
`POST /processes/{pid}/check`:
1. Two questions, question 2's stored status set to `PAUSED` (or published with
   `initialStatus: PAUSED` and question 1 set to `READY`): q1 `VotableNow == true`, `Reason == ""`;
   q2 `VotableNow == false`, `Reason == handlers.VoteReasonPaused`, `CanVote == true`. A
   `sign-batch` with only q1's ballot returns 200 with a signature.
2. A question set `CANCELED` → `canceled`, `CanVote == true`; set `ENDED` → `closed`,
   `CanVote == true`.
3. Eligibility subset excluding the voter → `not_eligible`, `CanVote == false`.
4. `StartDate` moved 1 h ahead → `not_started`, `CanVote == true`.
5. `EndDate` moved 2 min back (past endGrace) → `closed`, `CanVote == false`.
6. Weighted census: after authentication set the member's weight to 0 with `SetOrgMember` →
   every question `zero_weight`, `CanVote == true`.
7. Sign one question `db.MaxCSPSignaturesPerElection` times (same address) → that question
   `signature_limit_reached`, `CanVote == true`.
Do not modify existing assertions (`:207`, `:306` must stay).
Run: `go vet ./api/` → clean. Do not run the api suite locally (CI runs it).
Run: `./scripts/check-qt-patterns.sh` and
`~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=1f7d66e1 ./api/...` → clean.

Commit:
`git -c commit.gpgsign=false commit -m "test(csp): cover /check votableNow and reason end to end" -- api/processes_csp_test.go`

## Merge and release
1. Unit suites: `go test -count=1 ./csp/... ./db/...` → ok. `./api` runs in CI.
2. Gate audit on `1f7d66e1..HEAD`, then the 3-reviewer loop (implementation, user flow, security).
3. **Owner OK required before:** `gh stack submit` (push + open the PR against `main`),
   and any later push.
4. PR description: `Closes #900`, `Refs #899`, the five deviations, docs/SDK follow-ups pending
   (vocdoni.io `stage` "casting votes"; vocdoni-integrator-sdk `api-types`/react-providers).
