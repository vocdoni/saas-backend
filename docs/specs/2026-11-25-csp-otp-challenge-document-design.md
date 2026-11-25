# CSP OTP challenge as one document: design

Status: audited (2026-11-25, all 12 findings folded)   Class: architectural
Owner decisions so far (2026-11-25):
- All state of one OTP code lives in one Mongo document; every operation on it is one conditional update. No transactions.
- No persistent outbox. Compose the notification before claiming a send; roll the claim back when the push or the delivery fails.
- Sending to a different destination of the voter inside the cooldown sends there, bounded per destination, by the code's resend budget and by the daily cap.
- Shared attempts per code stay (accepted trade-off), documented in code, PR body and commit message.
- The rest of elboletaire's review of #737 (2026-11-25) is fixed in the same PR.

## Problem

saas-backend#737 lets a repeated step 0 reuse the voter's pending code by issuing extra tokens that point at the row holding the challenge (`CSPAuth.ChallengeID`, `db/csp.go:39`). The state of one code is then spread over several documents in `cspTokens` (the challenge row, one row per extra token) plus an in-memory notification queue, and every operation touching more than one of them has a gap. Review of #737 (elboletaire, 2026-11-25) found:

1. **Verify is two writes** (`db/csp.go:220-268`): `$unset secret` on the challenge row, then `verified=true` on the token row. If the second fails, the code is spent and no token is verified; retries hit `ErrTokenExpired` (`csp/auth.go:620`).
2. **A failed send keeps its claim** (`csp/auth.go:406-446`): `ClaimCSPAuthSend` sets `lastsentat` before `pushChallengeCode`; a push failure (queue full or closed, unbuildable message) is not rolled back, so the voter sits in cooldown for a code that never left. The same holds for a delivery the queue gives up on asynchronously: `countSentNotification` (`csp/csp.go:170`) ignores failures.
3. **Orphan extra token** (`csp/auth.go:81-88`): if the re-send fails after `SetCSPAuthForChallenge`, the extra token row and its slot in `tokens` stay.
4. **Wrong wait for a lost send race** (`csp/auth.go:356`): the loser computes the wait from the challenge read before the winner wrote `lastsentat`, so it answers `coolDownTime: 1`.
5. **Silent step 0 on a new destination** (`csp/auth.go:86`): inside the cooldown, step 0 for the same code but a different channel/address returns 200 and sends nothing.
6. `pushChallengeCode` takes 5 parameters (AGENTS.md: >3 → struct).
7. No test that a cooldown refusal in cert-auth step 0 leaves the certificate code unspent.

Also: the token view's `{"extratokens.id": token}` lookup (below) needs an index; `LastCSPAuth` is already served by the `{userid, bundleid, createdat: -1}` index from migration 0019 (`migrations/0019_census_revocation_indexes.go:30-37`).

## Goals

1. Every state change of one OTP code (issue token, claim attempt, verify, claim send, roll back send, drop token) is a single conditional update of one document.
2. A send that never reached the voter (push failed, or the queue gave up delivering it) gives back its cooldown, its resend unit and its daily-cap unit.
3. A voter switching to another of their destinations inside the cooldown gets the code there; alternating destinations cannot send more than today's budgets allow.
4. A request that loses a send race reports the real remaining wait.
5. The shared attempts-and-resends trade-off is explained where reviewers read it.
6. The token view's `extratokens.id` lookup uses an index.

## Non-goals

- Persistent outbox or crash-safe delivery: a code in the in-memory queue at restart is still lost (owner, 2026-11-25).
- Changing attempt sharing (stays per code), the send backoff (#893), error codes (#894), TTL cleanup of `cspTokens` (#895).
- Any change to `cspTokensStatus` / `CSPProcess` semantics (signing budgets, consumption, blind nonces): their rows are untouched. But every db-internal resolution of an auth token to its user — `fetchCSPAuthFromDB`, used by `ConsumeCSPProcess`, `ArmCSPProcessBlind`, `CSPProcess` and `IsCSPProcessConsumed`'s `UsedToken` re-check (`db/csp.go:412,492,349,381`) — adopts the token view (same `$or` lookup, same per-token `Verified`): an extra token has no `_id` document, so an `_id`-only lookup would refuse to sign for every voter verified through an extra token.
- API shape changes beyond what #737 already adds (`coolDownTime` on success, `Retry-After`).

## Current state

- `db.CSPAuth` (`db/csp.go:18-48`): one row per token in `cspTokens`; a row with empty `ChallengeID` holds its own challenge (`secret`, `attempts`, `resends`, `tokens`, `lastsentat`, `createdat`); extra tokens point at it.
- Readers of a token: `CSP.AuthToken`, `ChallengeSendRefusal`, `ClaimChallengeResend`, `ResendChallenge`, `ResendCoolDown`, `VerifyAuthToken` (`csp/auth.go`), the four signers in `csp/sign.go:62-244`, `csp/handlers/sign.go:36`, `csp/handlers/auth.go:510`, `csp/handlers/processes.go:349,973,1040,1213`. They read `Verified`, `UserID`, `AnchorID`, `Token`, and through `challengeOf` the challenge's `Secret`, `Attempts`, `CreatedAt`, `LastSent()`. Inside `db`, `fetchCSPAuthFromDB` (`_id` only, `db/csp.go:574`) resolves tokens for the `cspTokensStatus` operations.
- Writers: `SetCSPAuth`, `SetCSPAuthForChallenge`, `ClaimCSPAuthSend`, `VerifyCSPAuth`, `IncrementCSPAuthAttempts`, `ClaimCSPAuthResend`, `ReleaseCSPAuthResend`, `DeleteCSPAuth`, `DeleteCSPAuthByAnchor`. Census revocation deletes by `userid` (`db/census_revocation.go:431`); user erase and org teardown delete by `bundleid` (`DeleteCSPAuthByAnchor`, `db/user_erase.go:405`, `api/organizations_managed.go:564`, `db/csp.go:751`). Whole-document deletes stay correct: extra tokens die with their challenge.
- Auth-only tokens (`createAuthOnlyToken`, `csp/auth.go:711`): a row with no secret, verified at once (two writes today).
- The daily send cap is a counter in `cspCounters` (`db/csp_counters.go`), claimed before a send and released when nothing was queued; `ReleaseCSPCounter` targets the claim's own window.
- The queue (`csp/notifications/queue.go`) is in memory; each result (delivered or given up) is reported on `NotificationsSent` with `Success`, and `Meta` carries the `*NotificationChallenge` back.
- #737 is not deployed anywhere: no production row has `challengeid` or `tokens` as a counter.

## Design

### Document

`cspTokens` keeps one document per **challenge** (one OTP code, or one auth-only grant). `_id` stays the first token issued for it, so every existing row is already a valid document.

```go
type CSPAuth struct { // storage document; field names are the bson keys
    Token      internal.HexBytes    // _id: challenge id = first token
    UserID     internal.HexBytes    // userid
    AnchorID   internal.HexBytes    // bundleid
    CreatedAt  time.Time            // createdat: code expiry counts from here
    Secret     string               // secret, omitempty: unset once solved
    Attempts   int                  // attempts: per code
    Resends    int                  // resends: per code, every re-send of the code counts
    Tokens     []CSPAuthToken       // extratokens, omitempty: extra tokens answering this code
    Sends      map[string]time.Time // sends, omitempty: destination key -> last send time
    Verified   bool                 // verified
    VerifiedBy internal.HexBytes    // verifiedby, omitempty: the token that solved the code
    VerifiedAt time.Time            // verifiedat
    LastSentAt time.Time            // lastsentat, omitempty: read only, rows written before Sends
}
type CSPAuthToken struct { ID internal.HexBytes; CreatedAt time.Time } // id, createdat
```

`ChallengeID` and the counter `tokens int` go away (never deployed). The array's key is `extratokens`, not `tokens`: the previous code decodes `tokens` as an int, and an array there would fail the decode and brick that voter's step 0 after a rollback. `Tokens` holds at most `MaxChallengeTokens` extra tokens.

Destination key: lowercase hex of SHA-256 over `ctype + ":" + canonical(to)`, truncated to 16 bytes, where `canonical` lowercases emails (contact matching is already case-insensitive, `csp/handlers/auth.go:111`) and takes the normalized phone (`csp/handlers/auth.go:164`). Keying on the submitted spelling would let case-alternating one email bypass the per-destination cooldown. Delivery still uses `to` as submitted. The key names no contact data and is a valid field name.

### Token view

Callers keep working with a per-token view. `MongoStorage.CSPAuth(token)` and the db-internal `fetchCSPAuthFromDB` find the document with `{$or: [{_id: token}, {"extratokens.id": token}]}` and return a `CSPAuth` whose `Token` is the requested token and whose `Verified` is true only for the token that solved it (`verifiedby == token`, or `_id == token` when `verifiedby` is absent, for rows written before this change). The view also exposes the challenge id so the CSP layer can address the document. `challengeOf` disappears: the view is the challenge.

`ResendCoolDown` and `ChallengeSendRefusal` take the destination key: the wait reported (`coolDownTime`, `Retry-After`) is the wait of the destination the request named — `sends.{dest}`, falling back to `LastSent()` on legacy rows — and the cert-auth peek predicts the send `AuthToken` would attempt for that same destination. Handlers already hold it (`csp/handlers/auth.go:58-66` after step 0, `csp/handlers/processes.go:260-271`, `csp/handlers/cert_auth.go:385-393`).

### Operations (each one conditional update on one document)

| Operation | Filter (besides `_id`) | Update |
|---|---|---|
| New code | — | insert document with `sends.{dest}=now`, `resends=0` |
| Auth-only grant | — | insert with `verified=true`, `verifiedby=_id`, `verifiedat=now` (one write) |
| Extra token for pending code | `secret` exists, `attempts < Max`, `verified` false, `createdat > now-TTL`, `extratokens.{MaxChallengeTokens-1}` absent | `$push extratokens {id, now}` |
| Drop extra token (send failed) | `verified` false | `$pull extratokens {id}` |
| Claim attempt | `secret` exists, `attempts < Max` | `$inc attempts` |
| Verify | `secret` exists, `verified` false | `$unset secret`, `$set verified, verifiedby, verifiedat` |
| Claim send to dest | `secret` exists, `attempts < Max`, `createdat > now-TTL`, `resends < MaxChallengeResends`, `sends.{dest}` absent or `<= now-cooldown` (rows where the `sends` *field* is absent — legacy rows only: `lastsentat`/`createdat <= now-cooldown`) | `$set sends.{dest}=now`, `$inc resends` — `findOneAndUpdate`, returns the claim time |
| Roll back send | `sends.{dest} == claimTime` | `$unset sends.{dest}`, or `$set sends.{dest}=previous` when there was one — always the dest key, never the whole `sends` field, so an empty map stays and the row never looks legacy — `$inc resends -1` |

A request whose send claim does not match re-reads the document and classifies the refusal: cooldown on `sends.{dest}` → `coolDownTime` from the stored value (finding 4); resends exhausted → `ErrVerificationMaxAttempts`; secret gone, attempts spent or expired → `ErrTokenExpired`. On step 0 only, a cooldown or exhausted-resends refusal is not an error: the token is returned and nothing is sent (the voter already holds the code).

`auth/resend` runs the claim as one update — resend unit plus `sends.{dest}` for the *submitted* (normalized, not yet matched) contact — before checking the contact against the member. A wrong contact answers as today and keeps both spent (the probing bound, `csp/auth.go:224-228`). Only a claim that failed to send through no fault of the submission — push failure, async delivery failure, or losing the send race — is rolled back.

### Step 0

1. Read the voter's last document for (user, anchor) (indexed).
2. If its code is usable (secret, unexpired, attempts < Max, not verified) and has token room: push an extra token. If the voter's destination is due and the code has resends left, compose and claim a send to it (step-0 re-sends count as resends). If that send fails, drop the extra token and return the error. Otherwise return the token, sent or not (see refusal classification).
3. Otherwise mint a new code, subject to the cooldown since the latest send of the previous code (max over `sends`, falling back to `lastsentat`/`createdat`) and to the daily cap. A new code whose synchronous push fails is deleted (as today, `csp/auth.go:147`), with the daily unit released; one whose *async* delivery fails keeps its document (the voter holds its token) and gets the standard rollback, leaving `sends: {}`, so the next claim for any destination is immediately due.

`ChallengeSendRefusal` (cert-auth peek) mirrors steps 2-3 read-only, for the request's destination.

### Sending

`pushChallengeCode` takes one struct (`challengeSend`: user, anchor, challenge id, destination key, claim time, previous send time, daily-cap claim, code, expiry, delivery). Order: compose the message → claim the daily-cap unit → claim the send on the document → push. If the push fails: roll back the send claim and release the daily unit. The queue carries the `challengeSend` back in the result (`Meta` already carries the `*NotificationChallenge`); on `Success=false` the `NotificationsSent` consumer rolls back the send claim conditionally (`sends.{dest} == claimTime`, so a newer send is never undone) and releases the daily unit unconditionally — the failed send delivered nothing, whatever happened to the document since.

### Shared attempts and resends (documented trade-off)

A comment at `MaxChallengeAttempts` says, in 2-3 lines: attempts — and resends, which step-0 re-sends now also spend — are per code, not per token, because per-token budgets give 5 fresh guesses per step 0; whoever holds a voter's login data can therefore lock that voter's pending code or spend its resends, which no per-voter limit prevents (the same actor could already spend the daily cap); mitigation is #893. The PR body keeps a short "Accepted trade-off" section, and the commit message carries the same paragraph.

### Indexes

A new migration creates on `cspTokens`: `{"extratokens.id": 1}`, partial on `extratokens.id` existing. Down drops it. The `{userid, bundleid, createdat: -1}` and `{bundleid: 1}` indexes already exist (migration 0019) and are not touched.

### Alternatives considered

| Option | Why not |
|---|---|
| Mongo transactions | No session anywhere in the repo, tests run standalone `mongo:7`; still cannot include the in-memory queue. |
| Patch each gap with compensations, keep row-per-token | Leaves one code's state in N documents; each new feature reopens the same class of bug. |
| Persistent outbox | Only gains crash-safety for codes queued at restart (rare, recoverable by resend); out of scope by owner decision. |
| Hash of the last destination only | Alternating destinations would re-send on every request. |

## Compatibility and rollout

- Existing rows: single-token documents; `extratokens`, `sends`, `verifiedby` absent are handled (see token view and send claim). No data migration, only the index migration.
- API: unchanged beyond #737. Behaviour changes vs #737 as reviewed: step 0 to a new destination inside the cooldown sends; step-0 re-sends count against the code's 3 resends; failed sends give back cooldown and budgets.
- Rollback: reverting the PR before deploy is free. After deploy, old code ignores the unknown `extratokens`, `sends` and `verifiedby` fields and falls back to `lastsentat`/`createdat` for the cooldown; extra tokens stop resolving, so voters holding one repeat step 0 (15 min code lifetime bounds the disruption). No cleanup needed.

## Failure modes and security

- Two concurrent verifications: only one matches `secret exists, verified false`; the other gets `ErrTokenExpired`.
- A verify whose update fails leaves the code unsolved and retryable (attempt already counted).
- Concurrent extra tokens cannot exceed the cap (positional `$exists` filter).
- Rollback of a send only matches its own claim time, so it never undoes a later send; the daily unit of a failed send is always released.
- A wrong contact on `auth/resend` keeps its resend unit spent (contact probing stays bounded).
- Destination keys reveal no contact data and are case-canonical for email; the token view never exposes the secret (`json:"-"`) and never grants `Verified` to a token other than the one that solved the code.
- Delivery-failure rollback lets a voter retry at once; abuse stays bounded by the daily cap, which a failed send gives back only when nothing was delivered.
- Restart with codes in the queue: those sends stay claimed (non-goal); the voter waits one cooldown.

## Acceptance criteria

- [ ] AC1: verify is one update; a forced failure of the verify write leaves the code solvable (db test).
- [ ] AC2: push failure and an async `Success=false` result both restore `sends.{dest}`, `resends` and the daily unit (csp tests with an unbuildable message and a failing mail service).
- [ ] AC3: a failed step-0 re-send leaves no extra token (csp test).
- [ ] AC4: the loser of concurrent resends reports the real wait (csp test with 3 concurrent `ResendChallenge`).
- [ ] AC5: step 0 by email then by SMS inside the cooldown sends the SMS; alternating email/SMS sends at most once per destination per cooldown and at most `MaxChallengeResends` times per code (csp test).
- [ ] AC6: cert-auth step 0 refused by the cooldown leaves the certificate code unused (api test).
- [ ] AC7: existing rows (no `extratokens`/`sends`/`verifiedby`) verify, re-send and sign as before (db + csp tests on hand-inserted legacy documents).
- [ ] AC8: the migration creates the `extratokens.id` partial index; the `$or` token view is served by `_id` plus that index (db test on the index list).
- [ ] AC9: full `csp`, `db`, `api`, `errors` suites, golangci-lint and the qt check pass on z6; swagger unchanged or regenerated.
- [ ] AC10: a voter verified through an extra token signs (ECDSA and blind) and consumes the process; `IsCSPProcessConsumed` still enforces `ErrTokenNotVerified` when the consuming token's challenge was solved by a different token (api + db tests).
- [ ] AC11: the godoc at `MaxChallengeAttempts` names the shared attempts-and-resends trade-off and its bound; the PR body carries the "Accepted trade-off" section (reviewed by eye).
- [ ] AC12: email destination keys are case-insensitive: alternating the casing of one email inside the cooldown sends once (csp test).

## Constraints

- AGENTS.md conventions: `fmt.Errorf`, lowercase log messages, gofumpt, 130 cols, >3 params → struct, godoc on exports, quicktest matchers.
- Comments say what and why, never history.
- Never build or test on the laptop: z6 only.
- Conventional Commits, no AI attribution trailers.
- The PR keeps a curated history: the #737 commit, the success `coolDownTime`/`Retry-After` commit, and this redesign folded where it belongs.

## Open questions

None.
