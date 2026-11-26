# /check votableNow and reason: design

Status: audited   Class: architectural (additive public API change)
Owner decisions so far:
- 2026-11-26 implement #900 with the deviations below, explained in the PR (not on the issue first).
- 2026-11-26 the service-wide anonymous-voting switch is reported as a 10th reason, `unavailable`.

Source: vocdoni/saas-backend#900 (part of epic #899). This spec restates #900 and records where it
deviates from it (§Deviations from #900). Client context: vocdoni-app #2070 (question-by-question
room, re-reads `/check` on focus, reconnect, status change and every cast) and #2075 (voter page
follows the open questions; today it sends placeholders for non-eligible questions and expects the
CSP refusal).

## Problem

`POST /processes/{processId}/check` returns, per question, `canVote`, which only covers census
membership, the question's eligibility list and process end (`csp/handlers/processes.go:1067`).
A paused, not-yet-started or unpublished question reports `canVote: true`, yet the CSP refuses to
sign it with a bare `401 40001` (`unvotableElection`, `csp/handlers/processes.go:86`). Clients
(Vocdoni App, the integrator SDK and its recipes) re-derive the backend's rule from a separate,
possibly stale read and cannot tell the voter why a question can't be voted.

## Goals

1. `/check` tells, per question, whether a sign request made now with this token would pass every
   authorization check the CSP runs (`votableNow`), and if not, why (`reason`, closed enum).
2. `/check` and the signing path decide election state in one place, so they cannot drift.
3. `canVote` keeps its current value; its meaning (eligibility) is documented and pinned by tests.

## Non-goals

- Reasons in `sign-batch` / `blind-point` refusals (separate sub-issue of #899).
- `maintenance` reason (#872).
- Changing `canVote` as #787 proposes: the app and SDK 4.2.0 read it as "this question is for you".
- SDK, app and vocdoni.io docs follow-ups (recorded as pending in the PR).
- Any change to the signing path's behaviour, status codes or error messages.

## Current state

- `unvotableElection(vp, q)` (`processes.go:86`): non-`READY` status → 401 "question is not open
  for voting"; `now + startGrace < StartDate` → 401 "process has not started yet";
  `processEnded(vp)` (EndDate + `endGrace` passed) → 401 "process has ended". Checked in that order.
  Both read `time.Now()` internally.
- `authorizeLoadedQuestion` (`processes.go:414`): question must belong to the process, then
  `unvotableElection`, then `memberEligibleForQuestion`.
- `resolveSignContext` (`processes.go:330`): verified token anchored to the process; census
  participant exists (else 401 "member is no longer part of the census"); org member exists (else
  404 `ErrUserNotFound`); census loads (else 404 `ErrCensusNotFound`); weighted census with
  `member.Weight == 0` → `ErrZeroWeightVoter`.
- Signature budget, per voter and election (`db/csp.go`):
  - ECDSA: refused once `TimesVoted >= db.MaxCSPSignaturesPerElection` (10; `db/csp.go:254`,
    `:288`), answered `already_consumed`.
  - Blind (anonymous census): exactly one signature; refused once the row is `Used`
    (`db/csp.go:363`, `:381`, `:426`).
- Blind rounds refuse first thing while anonymous voting is switched off service-wide
  (`blindCSPRefused`, `processes.go:819`; 400 "anonymous voting is temporarily unavailable").
- `ProcessCheckHandler` (`processes.go:1008`): loads the census participant (sets
  `BelongsToProcess`), the org member and the census (every org-member or census error is
  swallowed, weight defaults to 1), all questions, and the CSP row of each published question.
- Question statuses (`db/const.go:44-48`): `READY`, `PAUSED`, `ENDED`, `CANCELED`, `RESULTS`; an
  unpublished question has an empty `UpstreamID` (and normally an empty `Status`).

## Design

### Types (`csp/handlers/types.go`)

```go
// VoteReason says why a question cannot be voted right now.
type VoteReason string

const (
	VoteReasonNotInCensus           VoteReason = "not_in_census"
	VoteReasonNotEligible           VoteReason = "not_eligible"
	VoteReasonZeroWeight            VoteReason = "zero_weight"
	VoteReasonNotPublished          VoteReason = "not_published"
	VoteReasonCanceled              VoteReason = "canceled"
	VoteReasonClosed                VoteReason = "closed"
	VoteReasonSignatureLimitReached VoteReason = "signature_limit_reached"
	VoteReasonNotStarted            VoteReason = "not_started"
	VoteReasonPaused                VoteReason = "paused"
	VoteReasonUnavailable           VoteReason = "unavailable"
)
```

`ProcessQuestionStatus` gains `VotableNow bool \`json:"votableNow"\`` and
`Reason VoteReason \`json:"reason,omitempty"\``. No extra tag: swag derives the enum from the typed
constants. AC4 checks that `docs/swagger.yaml` lists the ten values for `handlers.VoteReason`; if
it does not, add `enums:"not_in_census,..."` to the field.

### Reasons and precedence

When several apply, the first in this table wins: permanent reasons before temporary ones.

| # | `reason` | Condition |
|---|---|---|
| 1 | `not_in_census` | census participant not found, or org member lookup returns `db.ErrNotFound` / `db.ErrInvalidData` |
| 2 | `not_eligible` | `!memberEligibleForQuestion(q, memberID)` |
| 3 | `zero_weight` | `census.Weighted && member.Weight == 0` |
| 4 | `not_published` | `len(q.UpstreamID) == 0` |
| 5 | `canceled` | `q.Status == CANCELED` |
| 6 | `closed` | `q.Status` is `ENDED` or `RESULTS`, or `processEnded(vp, now)` |
| 7 | `signature_limit_reached` | the voter's CSP row for the question exists and: anonymous census → `row.Used`; otherwise `row.TimesVoted >= db.MaxCSPSignaturesPerElection` |
| 8 | `not_started` | `!vp.StartDate.IsZero() && now + startGrace < vp.StartDate` |
| 9 | `paused` | `q.Status == PAUSED`, or any other non-`READY` status |
| 10 | `unavailable` | `census.Anonymous && c.csp.BlindCSPDisabled()` |

`votableNow = reason == ""`. `canVote` is computed exactly as today (with the shared `now`).

### One source of truth for election state

`electionStateReason(vp, q, now) VoteReason` is a pure function returning `canceled`, `closed`,
`not_started`, `paused` or `""`, in that precedence. `processEnded` gains the same `now`
parameter. `unvotableElection(vp, q)` takes `now := time.Now()` once, returns nil when
`electionStateReason(vp, q, now) == ""`, and otherwise picks its message in today's order
(non-`READY` → "question is not open for voting"; not started → "process has not started yet";
else "process has ended"). Whether a question is votable is decided in one place; only the message
order differs.

A second pure function composes the table and is what AC1 tests: `voteReason(in voteReasonInput)
VoteReason`, with fields `inCensus`, `eligible`, `zeroWeight`, `published`, `state VoteReason`
(from `electionStateReason`), `budgetSpent`, `unavailable`. Order: rules 1-4; then `state` if it
is `canceled` or `closed`; then `budgetSpent`; then the remaining `state`; then `unavailable`.

### `ProcessCheckHandler`

- Take `now := time.Now()` once and use it for `canVote` and every question's reason.
- Keep the loaded org member and census. `orgMember` failing with `db.ErrNotFound` or
  `db.ErrInvalidData` → `not_in_census` for every question (`belongsToProcess` and `canVote` keep
  today's values, so `belongsToProcess: true` with `reason: not_in_census` is possible and
  intended). Any other `orgMember` error → 500 `ErrGenericInternalServerError`, as the
  `CensusParticipant` lookup already does. The census is loaded only when the org member was
  found; a census load error then answers `ErrCensusNotFound.WithErr(err)` (404), as
  `resolveSignContext` does. `weight` keeps today's value otherwise.
- Compute each question's reason after its CSP row is fetched (rule 7 needs it). No new DB query.
- A non-`READY`, non-`PAUSED`, non-terminal status on a published question is logged at warn at
  most once per `/check` call (with `processId` and the status), by the handler, not by
  `electionStateReason`.
- Update `canVote`'s doc comment and the handler's swag description:
  - `canVote` = eligible: in the census, in the question's eligibility list, and the process has
    not ended. It ignores pause, start date, weight and signature budget.
  - `votableNow` = a sign request now (to `sign-batch`, or `blind-point` for an anonymous census)
    would pass authorization; it can still fail for per-attempt outcomes (`already_signing`,
    `address_mismatch`, `auth_invalid`, blind-round errors).
  - Clients treat an unknown `reason` generically, and an absent `votableNow` (older backend) as
    unknown, never as `false`.
  - Run `make swagger`.

### Deviations from #900

1. Precedence: #900 orders `not_started` before `canceled`/`closed` and `signature_limit_reached`
   last, contradicting its own "most permanent first" rule (a canceled question before its start
   date would say "starts at 18:00"; a voter out of signatures would wait for a paused question to
   open just to be refused). Moved `canceled`, `closed`, `signature_limit_reached` ahead of
   `not_started`, `paused`.
2. `unvotableElection` keeps its message order instead of following the new precedence, so the
   signing path's messages don't change; it still delegates the votable/not decision to
   `electionStateReason`.
3. `/check` stops swallowing org-member and census lookup errors (500 / 404), so a DB blip is not
   reported as `not_in_census` or a wrong `zero_weight`.
4. `signature_limit_reached` for an anonymous census means the single blind signature was issued
   (`row.Used`), not 10.
5. New reason `unavailable` for the service-wide anonymous-voting switch.

### Alternatives considered

| Option | Why not |
|---|---|
| Change `canVote` to mean "votable now" (#787) | Breaks the app/SDK 4.2.0 voter room, which reads `canVote` as eligibility |
| Return only `votableNow`, no reason | The app still could not explain the refusal |
| Leave the anonymous switch as a documented gap until #872 | Owner chose correctness: one line, no query |

## Compatibility and rollout

Additive: two new response fields; `reason` omitted when `votableNow` is true. `canVote`,
`hasSigned`, `hasVoted` unchanged. Signing endpoints unchanged. No schema, migration or config
change. Rollback is a revert. Rollout: backend reaches `lts` before any client release that
requires the fields; clients treat an absent `votableNow` as unknown and fall back to their
current rule.

## Failure modes and security

- `/check` already requires a verified token anchored to the process; the new fields expose only
  the caller's own state, and rules 1-2 win so no election state leaks to a voter outside the
  census or the eligibility subset.
- DB errors on the member/census lookups are errors, not reasons (see §ProcessCheckHandler).
- `/check` adds no DB query; the warn log is bounded to one line per call.
- Clock: one `now` per request; start/end grace are the constants the signer uses.

## Acceptance criteria

- [ ] AC1: unit table for `voteReason` and `electionStateReason` over every question status,
  start/end grace boundaries (fixed `now`, exact boundaries ±1 ns), empty `UpstreamID`, blind vs
  ECDSA budget, `unavailable`, and precedence cases (`csp/handlers/handlers_test.go`).
- [ ] AC2: unit equivalence `(electionStateReason(vp,q,now) == "") == (unvotableElection(vp,q) == nil)`
  over every status × boundary combination, plus the three messages unchanged.
- [ ] AC3: integration (`api/processes_csp_test.go`): two questions published `PAUSED`, open
  question 1 → q1 `votableNow: true`, q2 `reason: paused`, `canVote: true`; a `sign-batch` with
  only `votableNow` questions succeeds; canceled → `canceled`; ended → `closed`; eligibility
  subset → `not_eligible` with `canVote: false`; future start → `not_started`; weight set to 0
  after auth → `zero_weight`; 10 signatures → `signature_limit_reached`. `canVote` stays `true` in
  the paused, canceled, ended-by-status, not-started, zero-weight and signature-limit cases.
  Existing `canVote` assertions unchanged. (Run by CI.)
- [ ] AC4: `docs/swagger.yaml` regenerated with the enum and new descriptions.
- [ ] AC5: `golangci-lint --new-from-rev` and `scripts/check-qt-patterns.sh` clean.

## Constraints

- Go conventions from AGENTS.md: `fmt.Errorf`, lowercase messages, gofumpt, lll 130, godoc on
  exported entities, quicktest matchers.
- Comments 1-3 lines carrying the why (vocdoni-comments).
- Commits: conventional, unsigned (`git -c commit.gpgsign=false`), no AI trailers.
- PR description states the vocdoni.io (`stage`) and vocdoni-integrator-sdk follow-ups as pending.

## Open questions

None.
