# Sign refusals carry a reason: implementation plan

Spec: docs/specs/2026-11-26-sign-refusal-reasons-design.md (approved 2026-11-26)
Class: architectural
Repos and branches: /home/lucasmenendez/saas-backend/.worktrees/feature-saas-qbq-epic → feat/899-sign-refusal-reasons (base main 19adf9df)

## Global Constraints
- Commits: conventional (`feat(csp): ...`, `test(csp): ...`), header ≤100 chars, no AI attribution or
  Co-Authored-By. Every commit unsigned: `git -c commit.gpgsign=false commit -m "..." -- <paths>`.
  Never sign, never change git config, never push, never rewrite history, never bare `git stash`.
- Commit with explicit paths only. Build output under `~/.cache/saas-backend/`, never /tmp.
- Do not dispatch subagents.
- Go conventions (AGENTS.md): `fmt.Errorf`, lowercase messages, gofumpt, lll 130, godoc on exported
  entities starting with their name, quicktest matchers (`scripts/check-qt-patterns.sh` must pass).
- Comments: 1-3 lines, the why.
- Unchanged for every request: HTTP status, error code, error message text, and whether anything is
  signed. Only `data` is added.
- Tests: unit (`go test ./csp/...`, Docker available). Do NOT run the `./api` suite (CI runs it);
  api test code must be traced by reading every helper it uses so it passes on first run.
- Lint: `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=19adf9df ./...`.
- Swagger: `PATH=$(go env GOPATH)/bin:$PATH make swagger` (reinstalls swag; needs network); commit
  `docs/swagger.yaml` with the change. If generation fails and deletes the file, restore it with
  `git checkout -- docs/swagger.yaml`.

## Review Focus
- `csp/handlers/processes.go` `authorizeLoadedQuestion` / `unvotableElection` / new `ballotRefusal`:
  refuse exactly when today's code refuses; messages unchanged.
- The four handlers' ballot loops (`ProcessSignBatchHandler`, `ProcessBlindPointHandler`,
  `ProcessBlindSignHandler`, `ProcessSignHandler` via `authorizeQuestion`): first failing ballot decides
  status/message; nothing signed on refusal; later malformed ballots skipped after a refusal.
- `resolveSignContext`: 404 data only for `db.ErrNotFound` / `db.ErrInvalidData`.

## Task order and parallelism
| Task | Depends on | Files | Parallel with |
|---|---|---|---|
| 1 | — | csp/handlers/{types.go,processes.go,handlers_test.go}, docs/swagger.yaml | — |
| 2 | 1 | api/ test files (new `api/processes_sign_refusal_test.go`) | — |

## Tasks

### Task 1: reasons in the authorization pass, the four handlers, swagger
Model: strong   Files: csp/handlers/types.go, csp/handlers/processes.go, csp/handlers/handlers_test.go,
docs/swagger.yaml
Goal: spec §Design in full; AC1, AC2b, AC3, AC4.

Step 1 (types.go): add `VoteReasonUnknownElection` with the spec's exact comment; add
```go
// SignRefusal is one ballot refused by a sign request's authorization pass.
type SignRefusal struct {
	Index      int               `json:"index"`
	UpstreamID internal.HexBytes `json:"upstreamId" swaggertype:"string" format:"hex" example:"deadbeef"`
	Reason     VoteReason        `json:"reason"`
}

// SignRefusalData is the data of a refused sign request: Reason for a whole-request refusal,
// Ballots for refused ballots, never both.
type SignRefusalData struct {
	Reason  VoteReason    `json:"reason,omitempty"`
	Ballots []SignRefusal `json:"ballots,omitempty"`
}
```

Step 2 (processes.go, authorization):
- `unvotableElection(vp, q, now time.Time)`: use the passed `now` instead of `time.Now()`.
- New `ballotRefusal(sc *signContext, question *db.VotingProcessQuestion, now time.Time) VoteReason`
  exactly as spec §Per-ballot reason (unknown_election, else `voteReason(voteReasonInput{inCensus: true,
  eligible: ..., processEnded: processEnded(sc.process, now), published: true, state:
  electionStateReason(sc.process, question, now)})`).
- `authorizeLoadedQuestion(sc, question, now) (internal.HexBytes, VoteReason, *errors.Error)`:
  `reason := ballotRefusal(...)`; `""` → `(question.UpstreamID, "", nil)`; else the reason plus today's
  message in today's order: nil/other-process → "election not found in process"; else if
  `unvotableElection(sc.process, question, now) != nil` → that error; else "member not eligible for this
  question". Both-or-neither invariant.
- `authorizeQuestion(sc, electionID, now)` returns the same three values (its empty-id 400 and DB 500
  return `nil, "", err`).

Step 3 (processes.go, handlers): each handler takes `now := time.Now()` once after parsing.
- Batch loops (sign-batch, blind-point, blind-sign): keep the loop and all its 400 checks. Add
  `var refusals []SignRefusal; var firstErr *errors.Error`. Inside the loop: if `firstErr != nil`
  (a refusal already happened), malformed checks `continue` silently instead of writing; the
  authorization call on a refused ballot appends `SignRefusal{Index: i, UpstreamID: <id as sent>,
  Reason: reason}`, stores `firstErr = sErr.Withf("at index %d", i)` if first, and `continue`s
  (skipping that ballot's own address / blinded-message checks, as today). Before any refusal, a
  malformed ballot writes its 400 and returns, as today. After the loop: if `len(refusals) > 0`,
  `firstErr.WithData(SignRefusalData{Ballots: refusals}).Write(w); return`. Note `Withf` and `WithData`
  return values (Error is a value type); check how `errors.Ptr` / `*errors.Error` are used and keep
  types consistent.
- Single `sign`: on a refused ballot write
  `sErr.WithData(SignRefusalData{Ballots: []SignRefusal{{Index: 0, UpstreamID: req.ProcessID, Reason: reason}}})`.
- `blindCSPRefused`: add `.WithData(SignRefusalData{Reason: VoteReasonUnavailable})`.
- `resolveSignContext`: the "member is no longer part of the census" 401 gets `Reason: not_in_census`;
  `ErrZeroWeightVoter` gets `zero_weight`; replace `c.orgMemberFromAuth(w, ...)` there with
  `c.orgMember(...)` and write `errors.ErrUserNotFound.WithErr(err)` — with
  `.WithData(SignRefusalData{Reason: VoteReasonNotInCensus})` only when
  `errors.Is(err, db.ErrNotFound) || errors.Is(err, db.ErrInvalidData)`. Keep the public message
  identical to what `orgMemberFromAuth` writes today (read it first). Do not touch `orgMemberFromAuth`.
- Swag on the four handlers per spec §Wire shape (`errors.Error{data=handlers.SignRefusalData}` on 401
  and 404; on 400 for blind-point and blind-sign; add the missing 404 lines), one description line each
  saying when `data` is present. Lines ≤130.

Step 4 (handlers_test.go):
- Update `TestUnvotableElectionMatchesState` and its message table for the `now` parameter (pass
  `time.Now()` at the call; keep the margins).
- `TestBallotRefusal`: fixed `now`; table over nil question, other-process question, not eligible, each
  state (canceled, ended status, results, paused, not started, process ended), precedence (not eligible +
  paused → not_eligible; canceled + process ended → closed), votable → "".
- `TestAuthorizeLoadedQuestionMatchesReason`: over the same table, assert `(err == nil) == (reason == "")`
  (write as `ok := err == (*errors.Error)(nil)` then `qt.Equals`), upstream id returned only when
  authorized, and the message for: not found, paused, not eligible (eligible+paused gives the state
  message, unchanged order).
- AC2b: a `csp/handlers` test against the test DB (see `vote_status_test.go` / `main_test.go` fixtures
  and `router.Post` usage) where the census participant exists but the org member does not: sign-batch
  answers 404 with `data.reason == not_in_census`. If the fixtures make this disproportionately hard,
  stop and report NEEDS_CONTEXT with what is missing instead of skipping it.

Step 5: `go test -count=1 ./csp/...` ok; swagger regen and check `handlers.SignRefusalData`,
`handlers.SignRefusal` and `unknown_election` appear; lint, qt-patterns, gofumpt clean; `go vet ./...`.

Step 6: commit `feat(csp): sign refusals report a reason per ballot and per request` with the four files.

### Task 2: integration tests
Model: standard   Files: new `api/processes_sign_refusal_test.go` (helpers in that file only)
Goal: AC2.

Reuse `setupRelayVoting`, the eligibility / status / date setters used by `TestProcessCSPCheckVotableNow`
(`api/processes_csp_test.go`), the blind fixtures in `api/processes_blind_csp_test.go` and
`api/processes_blind_csp_switch_test.go` (`setDisableBlindCSP`), the weighted-census setup and PUT
members upsert from the zero-weight subtest, and `testDB.DelCensusParticipant` for the removed member.
Read each before use. Decode refusals with
`requestAndParseWithAssertCode[struct{Code int `json:"code"`; Data handlers.SignRefusalData `json:"data"`}]`
(check its signature at `api/api_test.go:771`). Cases exactly as spec AC2. For "nothing consumed",
re-sign the votable ballot afterwards and assert success. Verify: `go vet ./api/`, qt-patterns, lint,
gofumpt. Commit `test(csp): cover reasons on sign refusals end to end`.

## Merge and release
1. Unit suites `go test -count=1 ./csp/...` ok; `./api` runs in CI.
2. Gate audit on `19adf9df..HEAD`.
3. PR description: `Refs #899`, sub-issue for this item opened just before the PR (`Closes #N`),
   docs/SDK follow-ups pending. **Owner OK before** `gh stack submit` and any later push.
4. Read CI; fix on the branch with new commits; push only with owner OK.
5. **Owner OK before** merge. Release is the owner's (`main → stage → lts`).
