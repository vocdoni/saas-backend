# Sign refusals carry a reason: design

Status: draft   Class: architectural (additive public API change on the voting path)
Owner decisions so far (2026-11-26):
- A refused batch reports every refused ballot, not only the first.
- Whole-request refusals carry `data.reason` too (`not_in_census`, `zero_weight`, `unavailable`).
- A ballot naming an election outside the process gets a new reason value, `unknown_election`.

Source: epic vocdoni/saas-backend#899, section 2, second item. Builds on #903 (`/check` `votableNow` /
`reason`) and #904 (reason explains `canVote` first), stacked on #904.

## Problem

The signing endpoints refuse with a bare error and no machine-readable cause. A batch
(`sign-batch`, `blind-point`, `blind-sign`) stops at the first unauthorized ballot with
`401 40001 "... at index i"` (`csp/handlers/processes.go`, `authorizeLoadedQuestion` callers at
`:637`, `:764`, `:870`); single `sign` does the same (`:392`). An app acting on a stale `/check`
(a question closed between the poll and the click) cannot tell the voter what happened, and has to
retry once per refused ballot to discover the rest.

## Goals

1. Every authorization refusal of `sign`, `sign-batch`, `blind-point` and `blind-sign` carries the
   `VoteReason` values that `/check` uses, so the app shows the same message whichever endpoint refused.
2. A refused batch reports all refused ballots at once.
3. No status code, error code, message or signing decision changes.

## Non-goals

- Per-attempt outcomes inside a 200 batch response (`already_consumed`, `already_signing`,
  `address_mismatch`, `auth_invalid`, `sign_failed`, blind-round codes): unchanged.
- Malformed-input refusals (400: repeated election, missing id, bad address, missing blinded
  message): no reason; they are client bugs.
- Token errors (`/check` already answers them), census-not-found 404, `maintenance` (#872).
- SDK, app and docs follow-ups.

## Design

### Reason values

Add one value to `handlers.VoteReason`: `VoteReasonUnknownElection = "unknown_election"`, used only in
refusals: the ballot names an election id that does not resolve to a question of this process
(unknown, unpublished, or another process's). Not-found and wrong-process stay indistinguishable, as
today. `/check` never returns it (its questions are by construction in the process).

### Wire shape (in the existing `data` field of the error body)

Per-ballot refusal (401 40001, nothing signed):
```json
{"error":"question is not open for voting at index 0","code":40001,
 "data":{"ballots":[{"index":0,"upstreamId":"ab..","reason":"paused"},
                    {"index":2,"upstreamId":"cd..","reason":"not_eligible"}]}}
```
`ballots` lists every refused ballot in request order; `error` keeps today's text for the first one.
Single `sign` uses the same shape with one entry, index 0.

Whole-request refusal: `data: {"reason": "<value>"}` on the existing error:
| Refusal (today) | `reason` |
|---|---|
| 401 "member is no longer part of the census" (`resolveSignContext`) | `not_in_census` |
| 404 `ErrUserNotFound` from the org-member lookup (`orgMemberFromAuth`) | `not_in_census` |
| 401 40801 `ErrZeroWeightVoter` | `zero_weight` |
| 400 "anonymous voting is temporarily unavailable" (`blindCSPRefused`, blind-point / blind-sign) | `unavailable` |

Types (`csp/handlers/types.go`): `SignRefusal{Index int; UpstreamID internal.HexBytes; Reason VoteReason}`
(json `index`, `upstreamId`, `reason`), `SignRefusalData{Ballots []SignRefusal}` and
`RequestRefusalData{Reason VoteReason}`, documented in swag on each endpoint's 401/400.

### Per-ballot reason

`ballotRefusal(sc, question) VoteReason` returns `""` when the ballot is authorized, else, in this
order (the same as `/check`'s rules 2 onward for what a ballot can hit):
1. `unknown_election`: `question == nil || question.ProcessID != sc.process.ID`;
2. `not_eligible`: `!memberEligibleForQuestion(question, sc.memberID)`;
3. `electionStateReason(sc.process, question, now)` (`canceled`, `closed`, `not_started`, `paused`).

`authorizeLoadedQuestion` keeps its return values and its 401 messages in today's order (state before
eligibility); it decides through `ballotRefusal` so the two cannot disagree on *whether* a ballot is
refused. The reason may name eligibility while the message names state when both apply; the reason
is the contract, the message is for humans.

### Batch flow

One pass over the ballots, as today, with one change: a malformed ballot still answers its 400 at
once; an unauthorized ballot is recorded (index, upstream id as sent, reason) and the pass continues.
After the pass, if any ballot was refused, answer the first one's 401 (today's message, `at index i`)
with `data.ballots`. A batch with both a refused and a later malformed ballot therefore answers the
400 (today it answered the 401): malformed input is a client bug and nothing is signed either way.

## Compatibility

Additive: same status, code and message; `data` is new on these errors. Clients that ignore `data`
see no change except the 400-over-401 precedence above. `unknown_election` is a new enum value;
clients already treat unknown reasons generically (documented in #903). Rollback is a revert.

## Failure modes and security

- No new information leaks: `unknown_election` covers not-found and other-process alike; the other
  reasons describe only the caller's own ballots, after the same token checks as today.
- The batch is still authorized as a unit before anything is signed; collecting all refusals adds no
  DB query (questions are already loaded into a map).
- Body size and ballot count caps unchanged.

## Acceptance criteria

- [ ] AC1: unit tests for `ballotRefusal` (every reason, precedence, nil/other-process question) and
  that `authorizeLoadedQuestion` refuses exactly when `ballotRefusal != ""`, with unchanged messages.
- [ ] AC2: integration (`api/`, runs in CI): sign-batch with a paused and a non-eligible ballot answers
  401 40001 with both in `data.ballots` and signs nothing; single sign of a paused question carries one
  entry; an unknown election id gives `unknown_election`; a removed member gets `data.reason:
  not_in_census`; zero weight `zero_weight`; blind-point with the switch off `unavailable`.
- [ ] AC3: `docs/swagger.yaml` regenerated with the types and the new enum value.
- [ ] AC4: lint, qt-patterns, gofumpt clean; `go test ./csp/...` passes.

## Constraints

Same as #903's spec: AGENTS.md Go conventions, comments 1-3 lines, unsigned conventional commits,
PR states the docs/SDK follow-ups as pending.

## Open questions

None.
