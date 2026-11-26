# Sign refusals carry a reason: design

Status: audited   Class: architectural (additive public API change on the voting path)
Owner decisions so far (2026-11-26):
- A refused batch reports every refused ballot, not only the first.
- Whole-request refusals carry `data.reason` too (`not_in_census`, `zero_weight`, `unavailable`).
- A ballot naming an election outside the process gets a new reason value, `unknown_election`.
- No status, error code, message or signing decision changes.

Source: epic vocdoni/saas-backend#899, section 2, second item. Builds on #903 (`/check` `votableNow` /
`reason`) and #904 (reason explains `canVote` first); stacked on #904.

## Problem

The signing endpoints refuse with a bare error and no machine-readable cause. A batch
(`sign-batch`, `blind-point`, `blind-sign`) stops at the first failing ballot with
`401 40001 "... at index i"` (`csp/handlers/processes.go`, `authorizeLoadedQuestion` callers at
`:637`, `:764`, `:870`); single `sign` does the same (`:392`). An app acting on a stale `/check`
(a question closed between the poll and the click) cannot tell the voter what happened, and has to
retry once per refused ballot to discover the rest.

## Goals

1. Every refusal of the authorization pass of `sign`, `sign-batch`, `blind-point` and `blind-sign`
   (before any signing is attempted) carries a `VoteReason`, using `/check`'s values and precedence.
2. A refused batch reports all its refused ballots at once.
3. No status code, error code, message or signing decision changes, for any request.

## Non-goals

- Per-attempt signing outcomes (`already_consumed`, `already_signing`, `address_mismatch`,
  `auth_invalid`, `sign_failed`, blind-round codes): unchanged. That is the per-item `code` of a 200
  batch response and, on single `sign`, the 401/500 that `signAndRespond` writes after authorization
  (`csp/handlers/sign.go:86-98`), which carries no `data`.
- Malformed-input refusals (400: repeated election, missing id, bad address, missing blinded message,
  wrong endpoint for the census mode, empty or oversized batch, unknown process id 400 40010): no reason.
- Token errors (`/check` already answers them), census-not-found 404, `maintenance` (#872).
- SDK, app and docs follow-ups.

## Design

### Reason values

Add `VoteReasonUnknownElection = "unknown_election"` to `handlers.VoteReason`, used only in refusals:
the ballot names an election id that resolves to no question of this process (unknown, unpublished, or
another process's). Not-found and wrong-process stay indistinguishable, as today. Constant comment:
`// VoteReasonUnknownElection means the ballot names no election of this process. Sign refusals only; /check never returns it.`

### Wire shape (the existing `data` field of the error body)

Types (`csp/handlers/types.go`): `SignRefusal{Index int; UpstreamID internal.HexBytes; Reason VoteReason}`
(json `index`, `upstreamId`, `reason`, none `omitempty`: index 0 must be present) and one data type,
`SignRefusalData{Reason VoteReason `json:"reason,omitempty"`; Ballots []SignRefusal `json:"ballots,omitempty"`}`:
`reason` is set on a whole-request refusal, `ballots` on a per-ballot one, never both. `index` is the
position in the request array (`ballots`, or `elections` on blind-point); single `sign` uses 0.
Swag: `@Failure 401 {object} errors.Error{data=handlers.SignRefusalData}` on the four endpoints, the same
on `@Failure 404` of the four (add the missing 404 line to blind-point and blind-sign) and on
`@Failure 400` of blind-point and blind-sign; each description says `data` is present only on the
refusals listed here.

Per-ballot refusal (401 40001, nothing signed):
```json
{"error":"question is not open for voting at index 0","code":40001,
 "data":{"ballots":[{"index":0,"upstreamId":"ab..","reason":"paused"},
                    {"index":2,"upstreamId":"cd..","reason":"not_eligible"}]}}
```
`ballots` lists every refused ballot in request order; `error` keeps today's text for the first one.
`upstreamId` echoes the id as sent; the response is bounded by the request caps (the batch cap, and the
router-wide body limit for single `sign`), and it is hex in JSON, so nothing is reflected unescaped.

Whole-request refusal, `data: {"reason": "<value>"}` on the existing error:
| Refusal (today) | `reason` |
|---|---|
| 401 "member is no longer part of the census" (`resolveSignContext`) | `not_in_census` |
| 404 `ErrUserNotFound` in `resolveSignContext`, only when the org-member lookup fails with `db.ErrNotFound` or `db.ErrInvalidData` | `not_in_census` |
| 401 40801 `ErrZeroWeightVoter` | `zero_weight` |
| 400 "anonymous voting is temporarily unavailable" (`blindCSPRefused`, blind-point / blind-sign) | `unavailable` |

`resolveSignContext` attaches the data itself: it calls `c.orgMember` and writes
`ErrUserNotFound.WithErr(err)`, adding `.WithData(...)` only for `db.ErrNotFound` / `db.ErrInvalidData`.
Any other lookup error keeps today's 404 with no data. `orgMemberFromAuth` and its other callers (auth
resend, weight) are not touched.

A whole-request refusal is decided before any ballot is looked at, so when several causes apply its
reason can differ from `/check`'s for the same voter (`/check` reports the most permanent cause first).
`unavailable` is answered before the token is read, as the refusal is today: it describes the server,
not the caller.

### Per-ballot reason

`now` is read once per request, in the handler (as `/check` does), and passed down.

`ballotRefusal(sc, question, now) VoteReason` returns `""` when the ballot is authorized, else:
1. `unknown_election`: `question == nil || question.ProcessID != sc.process.ID`;
2. otherwise `voteReason(voteReasonInput{inCensus: true, eligible: memberEligibleForQuestion(question,
   sc.memberID), processEnded: processEnded(sc.process, now), published: true,
   state: electionStateReason(sc.process, question, now)})`.

`voteReason` is `/check`'s own precedence function, so the two endpoints cannot drift. It is `""` exactly
when the member is eligible and `electionStateReason` is `""` (a process past its end always has a
non-empty state), which is today's signing decision.

`authorizeLoadedQuestion(sc, question, now) (internal.HexBytes, VoteReason, *errors.Error)` and
`unvotableElection(vp, q, now)`: `authorizeLoadedQuestion` first computes `reason := ballotRefusal(sc,
question, now)`; `reason == ""` returns the upstream id; otherwise it returns the reason plus today's
message, chosen in today's order (not found, then state via `unvotableElection`, then eligibility).
The reason and the error are both set or both empty. `authorizeQuestion` returns the same three values.
`TestUnvotableElectionMatchesState` is updated for the new `unvotableElection` signature. The reason
may name eligibility while the message names state when both apply; the reason is the contract.

### Batch flow

One pass over the ballots, as today. The first failing ballot still decides the response:
- if it is malformed, answer its 400 at once (unchanged);
- if it is unauthorized, record it (index, upstream id as sent, reason) and keep going only to collect
  the other unauthorized ballots: from then on a malformed ballot (repeated election, empty id, bad
  address, missing blinded message) is skipped, neither answered nor listed.
A refused ballot's own later checks (address, blinded message) are skipped too, as today.
After the pass, if any ballot was refused, answer the first one's 401 (today's message, `at index i`)
with `data.ballots`. Status, code and message are therefore the same as today for every request.

## Compatibility

Additive: same status, code and message for every request; `data` is new on these errors.
`unknown_election` is a new enum value; clients already treat unknown reasons generically (documented
in #903). Rollback is a revert.

## Failure modes and security

- No new information leaks: `unknown_election` covers not-found and other-process alike; the other
  reasons describe only the caller's own ballots, after the same token checks as today.
- A transient org-member lookup failure is never labelled `not_in_census` (404 without data).
- The batch is still authorized as a unit before anything is signed; collecting all refusals adds no
  DB query (questions are already loaded into a map). Body and ballot caps unchanged.

## Acceptance criteria

- [ ] AC1: unit tests for `ballotRefusal` (every reason, precedence matching `voteReason`, nil and
  other-process question) and that `authorizeLoadedQuestion` refuses exactly when its reason is set,
  with unchanged messages; one fixed `now`.
- [ ] AC2: integration (`api/`, runs in CI), reading the body with
  `requestAndParseWithAssertCode[struct{Code int; Data handlers.SignRefusalData}]`:
  - sign-batch with a paused and a non-eligible ballot (and a votable one between them): 401 40001,
    `data.ballots` has exactly the two, with their request indexes and upstream ids; a following
    sign-batch of the votable ballot alone succeeds (nothing was consumed);
  - the same for blind-point and blind-sign (`data.ballots`, no point issued / no slot consumed);
  - single sign of a paused question: one entry, index 0;
  - an election id of another process and a random id both give `unknown_election`;
  - a member removed from the census: 401 40001, `data.reason: not_in_census`; zero weight: 401 40801,
    `zero_weight`; blind-point and blind-sign with the switch off: 400, `unavailable`;
  - a refused ballot followed by a malformed one answers the 401; a malformed ballot first answers
    its 400 with no `data`.
- [ ] AC2b: `csp/handlers` test: a token whose org member is gone while the census participant remains
  answers 404 40018 with `data.reason: not_in_census`.
- [ ] AC3: `docs/swagger.yaml` regenerated with `handlers.SignRefusalData`, `handlers.SignRefusal` and
  the new enum value.
- [ ] AC4: lint, qt-patterns, gofumpt clean; `go test ./csp/...` passes.

## Constraints

Same as #903's spec: AGENTS.md Go conventions, comments 1-3 lines, unsigned conventional commits,
PR states the docs/SDK follow-ups as pending.

## Open questions

None.
