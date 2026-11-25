# CSP OTP challenge as one document: design

Status: audited (2026-11-25), revised with the owner   Class: architectural
Owner decisions (2026-11-25):
- All state of one OTP code lives in one Mongo document; every operation on it is one conditional update. No transactions.
- No persistent outbox. Compose the notification before claiming a send; roll the claim back when the push or the delivery fails.
- Sending to a different destination of the voter inside the cooldown sends there, bounded per destination, by the code's resend budget and by the daily cap.
- Shared attempts per code stay (accepted trade-off), documented in code, PR body and commit message.
- Codes in flight at deploy time need not survive it: they are few, and the voter asks again. Compatibility is measured against the schema in production (`lts`), never against #737's unreleased one; the code carries no legacy branches.
- No rollback to the previous code is planned; the design spends nothing on keeping one working.
- The rest of elboletaire's review of #737 (2026-11-25) is fixed in the same PR.

## Problem

saas-backend#737 lets a repeated step 0 reuse the voter's pending code by issuing extra tokens that point at the row holding the challenge. The state of one code is then spread over several documents in `cspTokens` plus an in-memory notification queue, and every operation touching more than one of them has a gap. Review of #737 (elboletaire, 2026-11-25) found:

1. **Verify is two writes**: `$unset secret` on the challenge row, then `verified=true` on the token row. If the second fails, the code is spent and no token is verified.
2. **A failed send keeps its claim**: the cooldown is claimed before the push and not rolled back when the push fails; a delivery the queue gives up on asynchronously is not rolled back either (`countSentNotification`, `csp/csp.go:170`, ignores failures).
3. **Orphan extra token**: if the re-send fails after the extra token was stored, it stays.
4. **Wrong wait for a lost send race**: the loser computes the wait from a stale read and answers `coolDownTime: 1`.
5. **Silent step 0 on a new destination**: inside the cooldown, step 0 for the same code but another channel/address returns 200 and sends nothing.
6. `pushChallengeCode` takes 5 parameters (AGENTS.md: >3 → struct).
7. No test that a cooldown refusal in cert-auth step 0 leaves the certificate code unspent.

## Goals

1. Every state change of one OTP code (issue token, claim attempt, verify, claim send, roll back send) is a single conditional update of one document.
2. A send that never reached the voter (push failed, or the queue gave up delivering it) gives back its cooldown, its resend unit and its daily-cap unit.
3. A voter switching to another of their destinations inside the cooldown gets the code there; alternating destinations cannot send more than the budgets allow.
4. A request that loses a send race reports the real remaining wait.
5. The shared attempts-and-resends trade-off is explained where reviewers read it.
6. One indexed lookup resolves any token, with no legacy code path.

## Non-goals

- Persistent outbox or crash-safe delivery: a code in the in-memory queue at restart is lost (owner, 2026-11-25).
- Keeping codes in flight across the deploy, or any rollback path to the previous code (owner, 2026-11-25).
- Changing attempt sharing (stays per code), the send backoff (#893), error codes (#894), TTL cleanup of `cspTokens` (#895).
- Any change to `cspTokensStatus` / `CSPProcess` semantics (signing budgets, consumption, blind nonces): their rows are untouched. But every db-internal resolution of an auth token — `fetchCSPAuthFromDB`, used by `ConsumeCSPProcess`, `ArmCSPProcessBlind`, `CSPProcess` and `IsCSPProcessConsumed`'s `UsedToken` re-check (`db/csp.go:412,492,349,381`) — adopts the token view, or every voter verified through a token other than the first would be refused at signing.
- API shape changes beyond what #737 already adds (`coolDownTime` on success, `Retry-After`).

## Current state

- Production (`lts`, same as `main` for this collection): one `cspTokens` row per token, each with its own code: `{_id: token, userid, bundleid, createdat, secret, attempts, verified, verifiedat, resends}`. Step 0 refuses within 60 s of the last row's `createdat`, else claims the daily cap, mints a row and pushes; 5 attempts and 3 resends per token; 15 min expiry from `createdat`; `auth/resend` has no cooldown, only the resend budget and the daily cap. A repeated step 0 sends a new code, so the code the voter already received no longer matches the token on screen — the product bug #737 fixes.
- #737 as reviewed (unreleased): extra rows pointing at the challenge row (`challengeid`), a `tokens` counter, `lastsentat`. All three are replaced by this design.
- Readers of a token: `CSP.AuthToken`, `ChallengeSendRefusal`, `ClaimChallengeResend`, `ResendChallenge`, `ResendCoolDown`, `VerifyAuthToken` (`csp/auth.go`), the four signers in `csp/sign.go`, `csp/handlers/sign.go`, `csp/handlers/auth.go`, `csp/handlers/processes.go`; inside `db`, `fetchCSPAuthFromDB` (`_id` only, `db/csp.go:574`).
- Deletes: census revocation by `userid` (`db/census_revocation.go:431`); user erase and org teardown by `bundleid` (`db/user_erase.go:405`, `api/organizations_managed.go:564`, `db/csp.go:751`). Whole-document deletes stay correct: all tokens of a code die with it.
- Auth-only tokens (`createAuthOnlyToken`): a row with no secret, verified at once.
- The daily send cap is a counter in `cspCounters` (`db/csp_counters.go`), claimed before a send and released when nothing was queued.
- The queue (`csp/notifications/queue.go`) is in memory; each result (delivered or given up) is reported on `NotificationsSent` with `Success`, and `Meta` carries the `*NotificationChallenge` back.

## Design

### Document

`cspTokens` holds one document per **challenge** (one OTP code, or one auth-only grant).

```go
type CSPAuth struct { // storage document; field names are the bson keys
    ID         internal.HexBytes    // _id: random document id; never a token, never used to look one up
    UserID     internal.HexBytes    // userid
    AnchorID   internal.HexBytes    // bundleid
    CreatedAt  time.Time            // createdat: code expiry counts from here
    Secret     string               // secret, omitempty: unset once solved
    Attempts   int                  // attempts: per code
    Resends    int                  // resends: per code, every re-send of the code counts
    Tokens     []CSPAuthToken       // tokens: every token answering this code, the first included
    Sends      map[string]time.Time // sends, omitempty: destination key -> last send time
    VerifiedBy internal.HexBytes    // verifiedby, omitempty: the token that solved the code; set = verified
    VerifiedAt time.Time            // verifiedat
}
type CSPAuthToken struct { ID internal.HexBytes; CreatedAt time.Time } // id, createdat
```

- `Tokens` holds at most `MaxChallengeTokens` (10) tokens.
- There is no `verified` boolean: `verifiedby` set means verified, and it names the one token that may sign. Another token of the same code — which anyone holding the voter's login data can obtain with their own step 0 — never becomes verified by the voter solving the code.
- `_id` is a fresh random id; rows migrated from `lts` keep theirs (which happens to equal their first token) and nothing relies on it.
- A missing `sends` is an empty map: no destination has been sent the code yet.

Destination key: lowercase hex of SHA-256 over `ctype + ":" + canonical(to)`, truncated to 16 bytes, where `canonical` lowercases emails (contact matching is already case-insensitive, `csp/handlers/auth.go:111`) and takes the normalized phone (`csp/handlers/auth.go:164`). Keying on the submitted spelling would let case-alternating one email bypass the per-destination cooldown. Delivery still uses `to` as submitted. The key names no contact data and is a valid field name.

### Token view

Callers keep a per-token view. `MongoStorage.CSPAuth(token)` and the db-internal `fetchCSPAuthFromDB` find the document with `{"tokens.id": token}` and return a `CSPAuth` view whose `Token` is the requested token and whose `Verified` is `verifiedby == token`. The view exposes the document id so the CSP layer can address it. `challengeOf` disappears: the view is the challenge.

`ResendCoolDown` and `ChallengeSendRefusal` take the destination key: the wait reported (`coolDownTime`, `Retry-After`) is the wait of the destination the request named, and the cert-auth peek predicts the send `AuthToken` would attempt for that destination. Handlers already hold it (`csp/handlers/auth.go:58-66` after step 0, `csp/handlers/processes.go:260-271`, `csp/handlers/cert_auth.go:385-393`).

### Operations (each one conditional update on one document)

| Operation | Filter (besides the document) | Update |
|---|---|---|
| New code | — | insert with `tokens=[{first, now}]`, `sends.{dest}=now`, `resends=0` |
| Auth-only grant | — | insert with `tokens=[{first, now}]`, `verifiedby=first`, `verifiedat=now` |
| Extra token for pending code | `secret` exists, `attempts < Max`, `verifiedby` absent, `createdat > now-TTL`, `tokens.{MaxChallengeTokens-1}` absent | `$push tokens {id, now}` |
| Drop extra token (send failed) | `verifiedby` absent | `$pull tokens {id}` |
| Claim attempt | `secret` exists, `attempts < Max` | `$inc attempts` |
| Verify | `secret` exists, `verifiedby` absent | `$unset secret`, `$set verifiedby, verifiedat` |
| Claim send to dest | `secret` exists, `attempts < Max`, `createdat > now-TTL`, `resends < MaxChallengeResends`, `sends.{dest}` absent or `<= now-cooldown` | `$set sends.{dest}=now`, `$inc resends` — `findOneAndUpdate`, returns the claim time |
| Roll back send | `sends.{dest} == claimTime` | `$set sends.{dest}=previous`, or `$unset sends.{dest}` when there was none; `$inc resends -1` |

A request whose send claim does not match re-reads the document and classifies the refusal: cooldown on `sends.{dest}` → `coolDownTime` from the stored value (finding 4); resends exhausted → `ErrVerificationMaxAttempts`; secret gone, attempts spent or expired → `ErrTokenExpired`. On step 0 only, a cooldown or exhausted-resends refusal is not an error: the token is returned and nothing is sent (the voter already holds the code).

`auth/resend` runs the claim as one update — resend unit plus `sends.{dest}` for the *submitted* (normalized, not yet matched) contact — before checking the contact against the member. A wrong contact answers as today and keeps both spent (the probing bound). Only a claim that failed to send through no fault of the submission — push failure, async delivery failure, or losing the send race — is rolled back.

### Step 0

1. Read the voter's last document for (user, anchor) (indexed `{userid, bundleid, createdat: -1}`, migration 0019).
2. If its code is usable (secret, unexpired, attempts < Max, not verified) and has token room: push a token. If the voter's destination is due and the code has resends left, compose and claim a send to it (step-0 re-sends count as resends). If that send fails, drop the token and return the error. Otherwise return the token, sent or not (see refusal classification).
3. Otherwise mint a new code, subject to the cooldown since the latest send of the previous code (max over its `sends`) and to the daily cap. A new code whose synchronous push fails is deleted, with the daily unit released; one whose *async* delivery fails keeps its document (the voter holds its token) and gets the standard rollback, so the next claim for any destination is immediately due.

`ChallengeSendRefusal` (cert-auth peek) mirrors steps 2-3 read-only, for the request's destination.

### Sending

`pushChallengeCode` takes one struct (`challengeSend`: user, anchor, document id, destination key, claim time, previous send time, daily-cap claim, code, expiry, delivery). Order: compose the message → claim the daily-cap unit → claim the send on the document → push. If the push fails: roll back the send claim and release the daily unit. The queue carries the `challengeSend` back in the result (`Meta` already carries the `*NotificationChallenge`); on `Success=false` the `NotificationsSent` consumer rolls back the send claim conditionally (`sends.{dest} == claimTime`, so a newer send is never undone) and releases the daily unit unconditionally — the failed send delivered nothing, whatever happened to the document since.

### Shared attempts and resends (documented trade-off)

A comment at `MaxChallengeAttempts` says, in 2-3 lines: attempts — and resends, which step-0 re-sends also spend — are per code, not per token, because per-token budgets give 5 fresh guesses per step 0; whoever holds a voter's login data can therefore lock that voter's pending code or spend its resends, which no per-voter limit prevents (the same actor could already spend the daily cap); mitigation is #893. The PR body keeps a short "Accepted trade-off" section, and the commit message carries the same paragraph.

### Migration

One migration on `cspTokens`:

- Up: `updateMany` with a pipeline setting `tokens: [{id: "$_id", createdat: "$createdat"}]` on rows without `tokens`, and `verifiedby: "$_id"` on rows with `verified: true`; then create the unique index `{"tokens.id": 1}`, partial on `tokens.id` existing (rows an old instance inserts during a rolling deploy lack it and must not collide as nulls; those voters repeat step 0). The `verified` field of existing rows is left in place and ignored.
- Down: drop the index (the migration framework requires a down; no rollback to the previous code is planned).

### Alternatives considered

| Option | Why not |
|---|---|
| Mongo transactions | No session anywhere in the repo, tests run standalone `mongo:7`; still cannot include the in-memory queue. |
| Patch each gap with compensations, keep row-per-token | Leaves one code's state in N documents; each new feature reopens the same class of bug. |
| `_id` = first token plus a list of extra tokens, no migration | Only keeps codes in flight across the deploy; costs a `$or` lookup and legacy branches in every operation. |
| Persistent outbox | Only gains crash-safety for codes queued at restart (rare, recoverable by resend); out of scope by owner decision. |
| Hash of the last destination only | Alternating destinations would re-send on every request. |

## Compatibility and rollout

- Deploy: the migration normalizes every existing row, so the code has one shape to handle. A code in flight keeps working (its row gains `tokens`); one whose row an old instance writes during the rolling deploy is not found and the voter repeats step 0.
- API: unchanged beyond #737. Behaviour changes vs `lts`: a repeated step 0 reuses the pending code; step 0 to a new destination inside the cooldown sends; step-0 re-sends count against the code's 3 resends; `auth/resend` gets the per-destination cooldown; failed sends give back cooldown and budgets.
- Rollback: not planned (owner, 2026-11-25). Reverting the PR before deploy is free; after deploy, fixes go forward.

## Failure modes and security

- Two concurrent verifications: only one matches `secret exists, verifiedby absent`; the other gets `ErrTokenExpired`.
- A verify whose update fails leaves the code unsolved and retryable (attempt already counted).
- Concurrent extra tokens cannot exceed the cap (positional `$exists` filter); the unique index keeps a token in one document.
- Only the token that solved the code can sign; tokens obtained by someone else with the voter's login data never inherit the verification.
- Rollback of a send only matches its own claim time, so it never undoes a later send; the daily unit of a failed send is always released.
- A wrong contact on `auth/resend` keeps its resend unit spent (contact probing stays bounded).
- Destination keys reveal no contact data and are case-canonical for email; the token view never exposes the secret (`json:"-"`).
- Delivery-failure rollback lets a voter retry at once; abuse stays bounded by the daily cap, which a failed send gives back only when nothing was delivered.
- Restart with codes in the queue: those sends stay claimed (non-goal); the voter waits one cooldown.

## Acceptance criteria

- [ ] AC1: verify is one update; a forced failure of the verify write leaves the code solvable (db test).
- [ ] AC2: push failure and an async `Success=false` result both restore `sends.{dest}`, `resends` and the daily unit (csp tests with an unbuildable message and a failing mail service).
- [ ] AC3: a failed step-0 re-send leaves no extra token (csp test).
- [ ] AC4: the loser of concurrent resends reports the real wait (csp test with 3 concurrent `ResendChallenge`).
- [ ] AC5: step 0 by email then by SMS inside the cooldown sends the SMS; alternating email/SMS sends at most once per destination per cooldown and at most `MaxChallengeResends` times per code (csp test).
- [ ] AC6: cert-auth step 0 refused by the cooldown leaves the certificate code unused (api test).
- [ ] AC7: the migration turns hand-inserted `lts`-shaped rows (pending, verified, auth-only) into documents that verify, re-send and sign (migration + csp tests).
- [ ] AC8: the unique partial index on `tokens.id` exists after up and is gone after down; token lookups use it (db test on the index list).
- [ ] AC9: full `csp`, `db`, `api`, `errors`, `migrations` suites, golangci-lint and the qt check pass on z6; swagger unchanged or regenerated.
- [ ] AC10: a voter verified through a token other than the first signs (ECDSA and blind) and consumes the process; a token of the same code that did not solve it is refused (`ErrTokenNotVerified`), including in `IsCSPProcessConsumed` (api + db tests).
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
