# /check votableNow and reason: design

Status: draft   Class: architectural (additive public API change)
Owner decisions so far: 2026-11-26 implement #900 with the amendments below, explained in the PR
(not on the issue first).

Source: vocdoni/saas-backend#900 (part of epic #899). This spec restates #900 and records where it
deviates from it (§Deviations from #900).

## Problem

`POST /processes/{processId}/check` returns, per question, `canVote`, which only covers census
membership, the question's eligibility list and process end (`csp/handlers/processes.go:1067`).
A paused, not-yet-started or unpublished question reports `canVote: true`, yet the CSP refuses to
sign it with a bare `401 40001` (`unvotableElection`, `csp/handlers/processes.go:86`). Clients
(Vocdoni App, the integrator SDK and its recipes) re-derive the backend's rule from a separate,
possibly stale read and cannot tell the voter why a question can't be voted. Question-by-question
assemblies (questions published paused and opened one at a time) hit this on every question.

## Goals

1. `/check` tells, per question, whether a sign request made now with this token would pass every
   authorization check the CSP runs (`votableNow`), and if not, why (`reason`, closed enum).
2. `/check` and the signing path share one rule for election state, so they cannot drift.
3. `canVote` keeps its current value; its meaning (eligibility) is documented and pinned by tests.

## Non-goals

- Reasons in `sign-batch` / `blind-point` refusals (separate sub-issue of #899).
- `maintenance` reason (#872).
- Changing `canVote` as #787 proposes: the app and SDK 4.2.0 read it as "this question is for you".
- SDK, app and vocdoni.io docs follow-ups.
- Any change to the signing path's behaviour, status codes or error messages.

## Current state

- `unvotableElection(vp, q)` (`processes.go:86`): non-`READY` status → 401 "question is not open
  for voting"; `now + startGrace < StartDate` → 401 "process has not started yet";
  `processEnded(vp)` (EndDate + `endGrace` passed) → 401 "process has ended". Checked in that order.
- `authorizeLoadedQuestion` (`processes.go:414`): question must belong to the process, then
  `unvotableElection`, then `memberEligibleForQuestion`.
- `resolveSignContext` (`processes.go:330`): verified token anchored to the process; census
  participant exists (else 401 "member is no longer part of the census"); org member exists (else
  `ErrUserNotFound`); census loads (else `ErrCensusNotFound`); weighted census with
  `member.Weight == 0` → `ErrZeroWeightVoter`.
- Signature budget: a voter's CSP row per election counts `TimesVoted`; at
  `db.MaxCSPSignaturesPerElection` (10, `db/const.go:33`) signing answers `already_consumed`.
- `ProcessCheckHandler` (`processes.go:1008`): loads the census participant (sets
  `BelongsToProcess`), the org member and the census (a census load error is silently ignored,
  weight defaults to 1), all questions, and the CSP row of each published question (needed for
  `hasSigned` / `hasVoted`).
- Question statuses (`db/const.go:44-48`): `READY`, `PAUSED`, `ENDED`, `CANCELED`, `RESULTS`; an
  unpublished question has an empty `Status` and an empty `UpstreamID`.

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
)
```

`ProcessQuestionStatus` gains `VotableNow bool \`json:"votableNow"\`` and
`Reason VoteReason \`json:"reason,omitempty"\`` with a swag `Enums(...)` tag listing the values.

### Reasons and precedence

When several apply, the first in this table wins: permanent reasons before temporary ones.

| # | `reason` | Condition |
|---|---|---|
| 1 | `not_in_census` | census participant not found, or org member not found |
| 2 | `not_eligible` | `!memberEligibleForQuestion(q, memberID)` |
| 3 | `zero_weight` | `census.Weighted && member.Weight == 0` |
| 4 | `not_published` | `len(q.UpstreamID) == 0` |
| 5 | `canceled` | `q.Status == CANCELED` |
| 6 | `closed` | `q.Status` is `ENDED` or `RESULTS`, or `processEnded(vp)` |
| 7 | `signature_limit_reached` | the voter's CSP row for the question has `TimesVoted >= db.MaxCSPSignaturesPerElection` |
| 8 | `not_started` | `!vp.StartDate.IsZero() && now + startGrace < vp.StartDate` |
| 9 | `paused` | `q.Status == PAUSED`, or any other non-`READY` status (logged as a warning) |

`votableNow = reason == ""`. `canVote` is computed exactly as today.

### One source of truth for election state

A new `electionStateReason(vp, q) VoteReason` returns `canceled`, `closed`, `not_started`, `paused`
or `""`, in that precedence. `unvotableElection` becomes a wrapper over the same predicates that
keeps its current check order and messages (status first, then start, then end), so the signing
path's observable behaviour is unchanged. Both must agree on *whether* a question is votable; only
the reported reason/message ordering differs. A unit test asserts
`(electionStateReason(vp,q) == "") == (unvotableElection(vp,q) == nil)` over every status ×
start/end boundary combination.

### `ProcessCheckHandler`

- Keep the loaded org member and census. A missing org member → `not_in_census` for every question.
  A census load error is a 500 (no longer ignored), since `zero_weight` depends on it.
- Compute each question's reason after its CSP row is fetched (rule 7 needs `TimesVoted`). No new
  DB queries are added beyond what the handler already runs.
- Update `canVote`'s doc comment and the handler's swag description: `canVote` = eligible (in the
  census, in the question's eligibility list, process not ended), ignoring pause, start date,
  weight and signature budget; `votableNow` = a sign request now would pass authorization, still
  subject to per-attempt outcomes (`already_signing`, `address_mismatch`); clients must treat an
  unknown `reason` generically. Run `make swagger`.

### Deviations from #900

1. Precedence: #900 orders `not_started` before `canceled`/`closed` and `signature_limit_reached`
   last, contradicting its own "most permanent first" rule (a canceled question before its start
   date would say "starts at 18:00"; a voter out of signatures would wait for a paused question to
   open just to be refused). Moved `canceled`, `closed`, `signature_limit_reached` ahead of
   `not_started`, `paused`.
2. #900 says to make `unvotableElection` a thin wrapper over the reason function while keeping its
   messages. With the new order a paused question before its start date would change message, so
   the wrapper keeps its own check order instead; equivalence is pinned by a test.
3. `/check` stops ignoring a census load error (500), required to report `zero_weight` correctly.

### Alternatives considered

| Option | Why not |
|---|---|
| Change `canVote` to mean "votable now" (#787) | Breaks the app/SDK 4.2.0 voter room, which reads `canVote` as eligibility |
| Return only `votableNow`, no reason | The app still could not explain the refusal |

## Compatibility and rollout

Additive: two new response fields; `reason` omitted when `votableNow` is true. `canVote`,
`hasSigned`, `hasVoted` unchanged. Signing endpoints unchanged. No schema, migration or config
change. Rollback is a revert.

## Failure modes and security

- `/check` already requires a verified token anchored to the process; the new fields reveal nothing
  the token holder can't learn by attempting to sign. `not_in_census` for a missing org member
  mirrors the signing refusal.
- `/check` is polled every ~10 s by every connected voter; the change adds no DB query.
- Clock: start/end grace are the same constants the signer uses.

## Acceptance criteria

- [ ] AC1: unit table over every question status, start/end grace boundaries, empty `UpstreamID`,
  precedence cases (`csp/handlers/handlers_test.go`).
- [ ] AC2: unit equivalence test between `electionStateReason` and `unvotableElection`.
- [ ] AC3: integration (`api/processes_csp_test.go`): two questions published `PAUSED`, open
  question 1 → q1 `votableNow: true`, q2 `reason: paused`, `canVote: true`; a `sign-batch` with
  only `votableNow` questions succeeds; canceled → `canceled`; ended → `closed`; eligibility
  subset → `not_eligible` with `canVote: false`; future start → `not_started`; weight set to 0
  after auth → `zero_weight`; 10 signatures → `signature_limit_reached`. Existing `canVote`
  assertions unchanged. (Run by CI; locally only the narrowed `-run` if needed.)
- [ ] AC4: `docs/swagger.yaml` regenerated with the enum and new descriptions.
- [ ] AC5: `golangci-lint --new-from-rev` and `scripts/check-qt-patterns.sh` clean.

## Constraints

- Go conventions from AGENTS.md: `fmt.Errorf`, lowercase messages, gofumpt, lll 130, godoc on
  exported entities, quicktest matchers.
- Comments 1-3 lines carrying the why (vocdoni-comments).
- Commits: conventional, unsigned (`git -c commit.gpgsign=false`), no AI trailers.

## Open questions

None.
