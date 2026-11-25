# CSP OTP challenge as one document: implementation plan

Spec: docs/specs/2026-11-25-csp-otp-challenge-document-design.md (approved, 89e15f44)
Class: architectural
Repos and branches: /home/user/src/vd/saas-backend → work branch `csp-otp-doc` from `fix/csp-otp-reuse` (base 84106f75); spec and plan commits stay on `feat/csp-cooldown-hint`

Line anchors below are against 84106f75 (the working tree of `feat/csp-cooldown-hint` is identical for every code file).
The z6 checkout `~/scratch/saas-737` is synced to the task branch tip by the controller before each run.

## Issues found in the spec

Each one is resolved in the tasks below without changing the spec's intent; the owner may overrule any of them at plan review.

1. **Send order on `auth/resend`.** The spec orders every send "compose → daily → claim → push", but also requires `auth/resend` to claim (resend unit + `sends.{dest}`) *before* the contact check, and a wrong contact must spend the claim, not the daily cap. So on `auth/resend` the order is: claim → contact check (handler) → compose → daily → push, and a compose failure or a daily-cap refusal rolls the claim back (nothing was sent). Step 0 keeps the spec order.
2. **Daily-cap refusal of a step-0 re-send.** The spec lists only the cooldown and exhausted resends as quiet refusals on step 0. A spent daily cap is treated the same way (token returned, nothing sent), which is #737's behaviour and the existing test "a spent daily send budget still returns a token for the pending code".
3. **`ChallengeSendRefusal` destination.** The spec says it takes the destination key. Under the spec's own rules its answer cannot depend on it: reusing a usable code is never refused (the send is quiet), and a new code's cooldown runs from the latest send to *any* destination. An unused parameter also fails revive (`unused-parameter`). The signature stays `(uID, anchorID)`; `ResendCoolDown` does take the destination key.
4. **Rolling back a new code's send.** The spec's rollback always does `$inc resends -1`, but a new code's first send is the insert and spends no resend. `challengeSend` carries a `resend` flag; the rollback decrements only when it is set.
5. **Verifying a document with no secret.** The spec's verify filter requires `secret` to exist, but about 45 test fixtures (and `csp/sign_test.go`) use `SetCSPAuth(token, …, "")` + `VerifyCSPAuth(token)`. `VerifyCSPAuth` keeps today's rule: the `secret exists` filter applies when the read document has a secret; `verifiedby absent` always applies. Production auth-only grants use the new `SetVerifiedCSPAuth` (one insert).
6. **`MaxChallengeTokens` now counts the first token** (spec: "first included"), so a code takes 9 extra tokens instead of #737's 10. The constant's godoc and the cap test change accordingly.
7. **Synchronous push failure of a new code.** The spec deletes the document. The delete is conditional on `tokens.1` absent, so a token a concurrent step 0 pushed in the meantime is never deleted; such a document stays, with its send rolled back (immediately due).
8. **AC2 "push failure".** On step 0 composition precedes every claim, so an unbuildable message restores nothing because nothing was claimed. The restore path is exercised on `auth/resend` (claim first) with an unbuildable message; a real queue `Push` error goes through the same `rollbackSend`, and is not reproducible in tests without a hook.
9. **Empty contact on `auth/resend`.** There is no destination to claim, so it answers `ErrCSPAuthFailed` without spending anything (#737 spent one). Nothing can be probed with an empty contact.
10. **`TestProcessCSP` (api/processes_csp_test.go:188-190)** asserts that a wrong contact inside the cooldown answers 40103. Under per-destination cooldowns that destination is due, so the request spends a resend and answers 40802. The assertion is removed; the budget arithmetic further down still holds (see Task 3).
11. **Refusal precedence.** When both apply, the cooldown is reported before exhausted resends. This matches #737 ("inside the cooldown it spends nothing and fails with 40103").
12. **Migrated rows have no `sends`.** They get no new-code cooldown, and an immediate re-send on the first repeated step 0. This follows from "a missing `sends` is an empty map".

## Global Constraints

- Commit convention: Conventional Commits; no AI attribution and no `Co-Authored-By` trailers. Commit with explicit paths only (`git commit -m "…" -- <paths>`).
- Never push. Never build, test, vet or format on the laptop: every `go`/`golangci-lint`/`swag` command runs on z6 (`ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && …'`). Build output stays in that checkout and the Go caches on z6, never /tmp.
- While iterating, run only the targeted `-run` tests per task. The full suites, golangci-lint and `./scripts/check-qt-patterns.sh` run once, in Merge and release.
- AGENTS.md conventions:
  - Errors: `fmt.Errorf` (never `errors.New`), wrapped with context, inspected with `errors.Is`/`errors.As`.
  - Log messages start lowercase and use key/value pairs.
  - Formatting: gofumpt, 130 columns.
  - More than 3 params or results → a struct.
  - Godoc on every exported entity.
  - Tests use quicktest matchers (`qt.HasLen`, `qt.IsNil`, `qt.IsTrue`…), never `len()`+`qt.Equals`.
  - revive runs with all rules: no boolean flag parameters, no unused parameters or receivers, and name same-typed results.
- Comments say what and why, never history ("was", "previously", "moved from", "no longer").
- Unchanged behaviour:
  - `cspTokensStatus` / `CSPProcess` semantics (signing budgets, consumption, blind nonces).
  - The API shape beyond #737 (`coolDownTime` on success, `Retry-After`).
  - Error codes.
  - The per-code sharing of attempts.
  - Whole-document deletes by `userid` and `bundleid`.
- No legacy branches: the code handles only the migrated document shape.

## Review Focus

- **Only the token that solved the code signs.** `db.CSPAuth.view` (`Verified = verifiedby == token`), and every db-internal token resolution through `fetchCSPAuthFromDB` (db/csp.go:574): `CSPProcess` :339, `IsCSPProcessConsumed` :381, `ConsumeCSPProcess` :412, `ArmCSPProcessBlind` :492. The four signers check `Verified` at csp/sign.go:69,121,185,244, and the handlers at csp/handlers/processes.go:349,973,1040,1213.
- **The contact-probing bound on `auth/resend`.** In csp/handlers/processes.go:240-268, `ClaimChallengeResend` must claim the resend unit and `sends.{dest}` for the *submitted* contact before `determineContactMethod` runs. A wrong contact must leave both spent. Only compose, daily-cap, push or async-delivery failures may roll the claim back.
- **Conditional rollback by claim time.** `RollbackCSPAuthSend` matches `sends.{dest} == claimedAt` (ms-truncated on both sides), so it never undoes a later send, and it decrements `resends` only for a re-send. `settleNotification` rolls back only `Success == false` results.
- **The daily-cap release.** The unit is released exactly once per failed send:
  - `rollbackSend`, on push failure and on async failure;
  - `newChallenge` when the insert fails;
  - `resendCode` when the claim is refused.
  It is never released for a delivered send, or for a sync timeout (`errChallengeStillQueued`).
- **The unique partial index.** `migrations/0040_csp_challenge_documents.go`: unique on `tokens.id`, partial on `tokens.id` existing. The data `updateMany`s are idempotent, and `fetchCSPAuthFromDB` filters on `tokens.id`.
- **Verify as one update.** `VerifyCSPAuth` (db/csp.go:220-268 today has two writes) becomes one `UpdateOne` with filter `verifiedby absent` (+ `secret exists`); `$unset secret`, `$set verifiedby, verifiedat`.

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1. db: challenge document, token view, migration 40 | — | db/csp.go, db/errors.go, migrations/0040_csp_challenge_documents.go, db/csp_test.go, db/migration_csp_challenge_documents_test.go | — |
| 2. csp: sends claimed, rolled back and settled on the document | 1 | csp/auth.go, csp/csp.go, csp/notifications/challenge.go, csp/auth_test.go, csp/daily_limits_test.go, csp/challenge_document_test.go | — |
| 3. handlers: destination in step 0, resend and the cooldown hint; api tests; swagger | 2 | csp/handlers/auth.go, csp/handlers/processes.go, csp/handlers/cert_auth.go, docs/swagger.yaml, api/processes_csp_test.go, api/processes_blind_csp_test.go, api/cert_auth_step0_test.go | 4 |
| 4. Trade-off godoc | 2 | csp/csp.go | 3 |

Tasks 2 and 3 are not split further. After Task 1 the `csp` package does not compile until all of its callers move to the new db API; the same holds for `csp/handlers` and `api` after Task 2. Each task leaves its own package compiling and green.

AC → task:

| AC | Task(s) | Test |
|---|---|---|
| AC1 | 1 | `TestVerifyCSPAuthWriteFailure` |
| AC2 | 2 | `TestChallengeSendRollback` (async `Success=false`; unbuildable message on resend) |
| AC3 | 2 | `TestChallengeReuseFailedSendDropsToken` |
| AC4 | 2 | `TestChallengeResendRace` |
| AC5 | 2 | `TestChallengeSendPerDestination` |
| AC6 | 3 | `TestCertAuthStep0RetryLaterKeepsCode/2FA cooldown on a new code…` |
| AC7 | 1, 2 | `TestCSPChallengeDocumentsMigration` (db), `TestMigratedLTSChallenges` (csp) |
| AC8 | 1 | `TestCSPChallengeDocumentsMigration` (index list after up and down) |
| AC9 | Merge and release | full suites, lint, qt check, swagger |
| AC10 | 1, 3 | `TestCSPAuthTokensOfOneCode` (db), `TestProcessCSPSecondTokenSigns`, `TestProcessBlindCSPSecondTokenSigns` (api) |
| AC11 | 4, Merge and release | godoc at `MaxChallengeAttempts`, commit message, PR body |
| AC12 | 2 | `TestDestinationKey`, `TestChallengeSendPerDestination/email casing` |

## Tasks

### Task 1: db challenge document, token view and migration 40

Model: strong   Repo: /home/user/src/vd/saas-backend   Files: db/csp.go, db/errors.go, migrations/0040_csp_challenge_documents.go, db/csp_test.go, db/migration_csp_challenge_documents_test.go
Goal: one document per code, with every operation a single conditional update and every token lookup going through the `tokens.id` view (AC1, AC7 db part, AC8, AC10 db part).
Patch: .delivery/2026-11-25-csp-otp-challenge-document/patches/task-1.patch

Step 1: write the failing test

In db/csp_test.go:
- Delete `TestVerifyCSPAuthForChallenge` (:101-136), `TestSetCSPAuthForChallengeCap` (:138-165), `TestClaimReleaseCSPAuthResend` (:167-198) and `TestClaimCSPAuthSend` (:200-end of that func).
- Add the tests below.
- Add `context` and `go.mongodb.org/mongo-driver/v2/bson` to the imports.

`TestSetGetCSPAuth` stays as is: the "last token" view is the latest document's last token.

```go
// TestVerifyCSPAuthWriteFailure checks that verifying is one write: when it fails, the code keeps
// its secret and no token is verified, so the voter can still solve it.
func TestVerifyCSPAuthWriteFailure(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	setValidator := func(validator bson.M) {
		c.Assert(database.RunCommand(ctx, bson.D{
			{Key: "collMod", Value: "cspTokens"},
			{Key: "validator", Value: validator},
		}).Err(), qt.IsNil)
	}
	c.Cleanup(func() {
		setValidator(bson.M{})
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	// a validator refusing any verified challenge makes the verify write fail
	setValidator(bson.M{"verifiedby": bson.M{"$exists": false}})
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.ErrorIs, ErrStoreToken)
	token, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Secret, qt.Equals, "secret")
	c.Assert(token.VerifiedBy, qt.HasLen, 0)
	c.Assert(token.Verified, qt.IsFalse)

	// once writes go through again, the code is still solvable
	setValidator(bson.M{})
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.IsNil)
	token, err = testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Verified, qt.IsTrue)
	c.Assert(token.Secret, qt.Equals, "")
}

// TestCSPAuthTokensOfOneCode checks the token view: every token of a code resolves to its
// document, and only the token that solved the code is verified, for signing and consumption.
func TestCSPAuthTokensOfOneCode(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	first := testAuthToken
	second := internal.HexBytes(uuid.New().String())
	c.Assert(testDB.SetCSPAuth(first, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	add := func(token internal.HexBytes) bool {
		added, err := testDB.AddCSPAuthToken(CSPTokenRequest{
			ChallengeID: challenge.ID, Token: token, MaxTokens: 10, MaxAttempts: 5, TTL: time.Hour,
		})
		c.Assert(err, qt.IsNil)
		return added
	}
	c.Assert(add(second), qt.IsTrue)

	secondData, err := testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.ID, qt.DeepEquals, challenge.ID)
	c.Assert(secondData.Token, qt.DeepEquals, second)
	c.Assert(secondData.Secret, qt.Equals, "secret")
	c.Assert(secondData.Tokens, qt.HasLen, 2)

	// the second token solves the code: only it is verified, and the code is spent for the first
	c.Assert(testDB.VerifyCSPAuth(second), qt.IsNil)
	secondData, err = testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
	c.Assert(secondData.Verified, qt.IsTrue)
	firstData, err := testDB.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(firstData.Verified, qt.IsFalse)
	c.Assert(testDB.VerifyCSPAuth(first), qt.ErrorIs, ErrChallengeConsumed)

	// a process consumed through the solving token reads as verified...
	c.Assert(testDB.ConsumeCSPProcess(second, testCSPProcessID, testUserAddress), qt.IsNil)
	consumed, err := testDB.IsCSPProcessConsumed(testUserID, testCSPProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(consumed, qt.IsFalse)
	// ...and one consumed through the other token does not
	otherProcess := internal.HexBytes([]byte("otherProcessID"))
	c.Assert(testDB.ConsumeCSPProcess(first, otherProcess, testUserAddress), qt.IsNil)
	_, err = testDB.IsCSPProcessConsumed(testUserID, otherProcess)
	c.Assert(err, qt.ErrorIs, ErrTokenNotVerified)

	// a solved code takes no more tokens and keeps the one that solved it
	c.Assert(add(internal.HexBytes(uuid.New().String())), qt.IsFalse)
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, second), qt.IsNil)
	_, err = testDB.CSPAuth(second)
	c.Assert(err, qt.IsNil)
}

// TestAddCSPAuthTokenCap checks that a code takes at most MaxTokens tokens, the first included,
// and that a dropped token stops resolving.
func TestAddCSPAuthTokenCap(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const maxTokens = 3
	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	req := CSPTokenRequest{ChallengeID: challenge.ID, MaxTokens: maxTokens, MaxAttempts: 5, TTL: time.Hour}
	var last internal.HexBytes
	for range maxTokens - 1 {
		last = internal.HexBytes(uuid.New().String())
		req.Token = last
		added, err := testDB.AddCSPAuthToken(req)
		c.Assert(err, qt.IsNil)
		c.Assert(added, qt.IsTrue)
	}
	req.Token = internal.HexBytes(uuid.New().String())
	added, err := testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsFalse)

	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, last), qt.IsNil)
	_, err = testDB.CSPAuth(last)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	added, err = testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsTrue)

	// an expired code takes no token
	req.Token, req.TTL = internal.HexBytes(uuid.New().String()), 0
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, last), qt.IsNil)
	added, err = testDB.AddCSPAuthToken(req)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsFalse)
}

// TestClaimRollbackCSPAuthSend checks the per-destination send claim and its conditional rollback.
func TestClaimRollbackCSPAuthSend(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	_, _, err := testDB.ClaimCSPAuthSend(CSPSendClaimRequest{})
	c.Assert(err, qt.ErrorIs, ErrBadInputs)

	c.Assert(testDB.SetCSPAuth(testAuthToken, testUserID, testCSPBundleID, "secret"), qt.IsNil)
	challenge, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	req := CSPSendClaimRequest{
		ChallengeID: challenge.ID, Dest: "aa", CoolDown: time.Hour, TTL: time.Hour, MaxAttempts: 5, MaxResends: 2,
	}
	claim := func(r CSPSendClaimRequest) (CSPSendClaim, bool) {
		cl, ok, err := testDB.ClaimCSPAuthSend(r)
		c.Assert(err, qt.IsNil)
		return cl, ok
	}
	read := func() *CSPAuth {
		data, err := testDB.CSPAuth(testAuthToken)
		c.Assert(err, qt.IsNil)
		return data
	}

	// a destination never sent to is due, and has no previous send
	claimA, ok := claim(req)
	c.Assert(ok, qt.IsTrue)
	c.Assert(claimA.Previous.IsZero(), qt.IsTrue)
	// inside its cooldown it is refused; another destination is not
	_, ok = claim(req)
	c.Assert(ok, qt.IsFalse)
	reqB := req
	reqB.Dest = "bb"
	claimB, ok := claim(reqB)
	c.Assert(ok, qt.IsTrue)
	// the code's resends (2) are spent
	reqC := req
	reqC.Dest = "cc"
	_, ok = claim(reqC)
	c.Assert(ok, qt.IsFalse)
	data := read()
	c.Assert(data.Resends, qt.Equals, 2)
	c.Assert(data.Sends, qt.HasLen, 2)
	c.Assert(data.Sends["bb"].Equal(claimB.At), qt.IsTrue)

	// a rollback only matches its own claim time
	rollback := CSPSendRollback{ChallengeID: challenge.ID, Dest: "bb", ClaimedAt: claimB.At.Add(-time.Millisecond), Resend: true}
	rolled, err := testDB.RollbackCSPAuthSend(rollback)
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsFalse)
	rollback.ClaimedAt = claimB.At
	rolled, err = testDB.RollbackCSPAuthSend(rollback)
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsTrue)
	data = read()
	c.Assert(data.Resends, qt.Equals, 1)
	_, ok = data.Sends["bb"]
	c.Assert(ok, qt.IsFalse)

	// rolling back a later send to a destination restores its previous send time
	reqA := req
	reqA.CoolDown = 0
	claimA2, ok := claim(reqA)
	c.Assert(ok, qt.IsTrue)
	c.Assert(claimA2.Previous.Equal(claimA.At), qt.IsTrue)
	rolled, err = testDB.RollbackCSPAuthSend(CSPSendRollback{
		ChallengeID: challenge.ID, Dest: "aa", ClaimedAt: claimA2.At, Previous: claimA2.Previous, Resend: true,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(rolled, qt.IsTrue)
	data = read()
	c.Assert(data.Sends["aa"].Equal(claimA.At), qt.IsTrue)
	c.Assert(data.Resends, qt.Equals, 1)

	// a solved code takes no send
	c.Assert(testDB.VerifyCSPAuth(testAuthToken), qt.IsNil)
	_, ok = claim(reqB)
	c.Assert(ok, qt.IsFalse)
}

// TestNewCSPChallenge checks a new code's document: its first token, its first send, and that it
// is deleted only while no other token answers it.
func TestNewCSPChallenge(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	_, err := testDB.NewCSPChallenge(CSPChallengeRequest{})
	c.Assert(err, qt.ErrorIs, ErrBadInputs)
	challenge, err := testDB.NewCSPChallenge(CSPChallengeRequest{
		Token: testAuthToken, UserID: testUserID, AnchorID: testCSPBundleID, Secret: "secret", Dest: "aa",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(challenge.Token, qt.DeepEquals, testAuthToken)
	c.Assert(challenge.ID, qt.Not(qt.DeepEquals), testAuthToken)
	stored, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.ID, qt.DeepEquals, challenge.ID)
	c.Assert(stored.Resends, qt.Equals, 0)
	c.Assert(stored.Sends["aa"].Equal(stored.CreatedAt), qt.IsTrue)
	c.Assert(stored.LastSend().Equal(stored.CreatedAt), qt.IsTrue)

	added, err := testDB.AddCSPAuthToken(CSPTokenRequest{
		ChallengeID: challenge.ID, Token: invalidAuthToken, MaxTokens: 10, MaxAttempts: 5, TTL: time.Hour,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.IsTrue)
	deleted, err := testDB.DeleteCSPChallenge(challenge.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsFalse)
	c.Assert(testDB.RemoveCSPAuthToken(challenge.ID, invalidAuthToken), qt.IsNil)
	deleted, err = testDB.DeleteCSPChallenge(challenge.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.IsTrue)
	_, err = testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)

	// an auth-only grant is verified by its only token
	c.Assert(testDB.SetVerifiedCSPAuth(testAuthToken, testUserID, testCSPBundleID), qt.IsNil)
	grant, err := testDB.CSPAuth(testAuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(grant.Verified, qt.IsTrue)
	c.Assert(grant.Secret, qt.Equals, "")
}
```

New file db/migration_csp_challenge_documents_test.go:

```go
package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// cspTokenIDIndex is the index migration 40 creates on cspTokens.
const cspTokenIDIndex = "csp_tokens_token_id"

// TestCSPChallengeDocumentsMigration checks that migration 40 turns rows shaped as lts stores them
// (the token as _id, a verified boolean) into challenge documents that resolve, verify and keep
// their verification, that it is idempotent, and that it creates the unique partial token index
// and its down migration drops it.
func TestCSPChallengeDocumentsMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig := migrationByVersion(c, 40)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
		c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	})

	now := time.Now().Truncate(time.Millisecond)
	pending := internal.HexBytes(uuid.New().String())
	verified := internal.HexBytes(uuid.New().String())
	authOnly := internal.HexBytes(uuid.New().String())
	_, err := testDB.cspTokens.InsertMany(ctx, []any{
		bson.M{
			"_id": pending, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"secret": "secret", "attempts": 0, "verified": false, "verifiedat": time.Time{}, "resends": 0,
		},
		bson.M{
			"_id": verified, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"attempts": 1, "verified": true, "verifiedat": now, "resends": 0,
		},
		bson.M{
			"_id": authOnly, "userid": testUserID, "bundleid": testCSPBundleID, "createdat": now,
			"attempts": 0, "verified": true, "verifiedat": now,
		},
	})
	c.Assert(err, qt.IsNil)

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	// re-running it over migrated documents changes nothing
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	for _, tc := range []struct {
		token    internal.HexBytes
		verified bool
		secret   string
	}{
		{pending, false, "secret"},
		{verified, true, ""},
		{authOnly, true, ""},
	} {
		data, err := testDB.CSPAuth(tc.token)
		c.Assert(err, qt.IsNil)
		c.Assert(data.ID, qt.DeepEquals, tc.token)
		c.Assert(data.Tokens, qt.HasLen, 1)
		c.Assert(data.Tokens[0].ID, qt.DeepEquals, tc.token)
		c.Assert(data.Tokens[0].CreatedAt.Equal(now), qt.IsTrue)
		c.Assert(data.Verified, qt.Equals, tc.verified)
		c.Assert(data.Secret, qt.Equals, tc.secret)
		c.Assert(data.Sends, qt.HasLen, 0)
	}
	// the pending code still verifies, through its token
	c.Assert(testDB.VerifyCSPAuth(pending), qt.IsNil)
	data, err := testDB.CSPAuth(pending)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Verified, qt.IsTrue)

	// the token index: unique, and partial so rows without tokens never collide as nulls
	type indexSpec struct {
		Name    string `bson:"name"`
		Key     bson.D `bson:"key"`
		Unique  bool   `bson:"unique"`
		Partial bson.D `bson:"partialFilterExpression"`
	}
	tokenIndex := func() (indexSpec, bool) {
		cursor, err := testDB.cspTokens.Indexes().List(ctx)
		c.Assert(err, qt.IsNil)
		var indexes []indexSpec
		c.Assert(cursor.All(ctx, &indexes), qt.IsNil)
		for _, idx := range indexes {
			if idx.Name == cspTokenIDIndex {
				return idx, true
			}
		}
		return indexSpec{}, false
	}
	idx, ok := tokenIndex()
	c.Assert(ok, qt.IsTrue)
	c.Assert(idx.Key, qt.DeepEquals, bson.D{{Key: "tokens.id", Value: int32(1)}})
	c.Assert(idx.Unique, qt.IsTrue)
	c.Assert(idx.Partial, qt.DeepEquals, bson.D{{Key: "tokens.id", Value: bson.D{{Key: "$exists", Value: true}}}})

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	_, ok = tokenIndex()
	c.Assert(ok, qt.IsFalse)
	// dropping an index already gone must not fail
	c.Assert(mig.Down(ctx, database), qt.IsNil)
}
```

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestVerifyCSPAuthWriteFailure|TestCSPAuthTokensOfOneCode|TestAddCSPAuthTokenCap|TestClaimRollbackCSPAuthSend|TestNewCSPChallenge|TestCSPChallengeDocumentsMigration|TestSetGetCSPAuth" ./db/'`
Expected: build failure, `undefined: CSPTokenRequest` (and the other new names).

Step 2: implement

**db/errors.go:**
- Delete `ErrChallengeTokensExhausted` and its comment (:32-34).
- Reword the `ErrChallengeConsumed` comment: "ErrChallengeConsumed is returned when a token's OTP code was already solved, through it or another token of the same code."

**db/csp.go:**
- Add `bytes` to the imports.
- Replace lines 17-65 (the `CSPAuth` struct, `ChallengeToken`, `LastSent`) with:

```go
// CSPAuth is one OTP challenge in cspTokens: one code, or one auth-only grant, with every token
// answering it. Read through a token (CSPAuth, LastCSPAuth), it is that token's view: Token is the
// token it was read through, and Verified whether that token solved the code.
type CSPAuth struct {
	// ID is the document id, random. Nothing looks a token up by it.
	ID internal.HexBytes `json:"-" bson:"_id"`
	// Token is the token this view was read through. Not stored.
	Token internal.HexBytes `json:"token" bson:"-"`
	// UserID is the member ObjectID (hex) the token authenticates.
	UserID internal.HexBytes `json:"userID" bson:"userid"`
	// AnchorID is the token's anchor: the voting-process id (a process-bundle id on tokens issued
	// by the removed legacy bundle flow; the bson key keeps that name). It only binds the token
	// and gates the resend cooldown; per-election signing/consumption keys on the election id separately.
	AnchorID internal.HexBytes `json:"bundleID" bson:"bundleid"`
	// CreatedAt is when the code was issued; it expires from here.
	CreatedAt time.Time `json:"createdAt" bson:"createdat"`
	// Secret is the code's OTP secret, unset once the code is solved. It must never leave the
	// server, so it is excluded from JSON serialization.
	Secret string `json:"-" bson:"secret,omitempty"`
	// Attempts counts the verification attempts on the code, across all its tokens.
	Attempts int `json:"attempts" bson:"attempts"`
	// Resends counts the sends of the code after its first one, across all its tokens.
	Resends int `json:"resends" bson:"resends"`
	// Tokens are the tokens answering the code, the first included, oldest first.
	Tokens []CSPAuthToken `json:"-" bson:"tokens"`
	// Sends maps a destination key to when the code was last sent there; a destination missing
	// from it was never sent the code.
	Sends map[string]time.Time `json:"-" bson:"sends,omitempty"`
	// VerifiedBy is the token that solved the code: set means verified, and only that token signs.
	VerifiedBy internal.HexBytes `json:"-" bson:"verifiedby,omitempty"`
	// VerifiedAt is when the code was solved.
	VerifiedAt time.Time `json:"verifiedAt" bson:"verifiedat"`
	// Verified reports whether Token solved the code. Not stored.
	Verified bool `json:"verified" bson:"-"`
}

// CSPAuthToken is one token answering a challenge.
type CSPAuthToken struct {
	ID        internal.HexBytes `json:"-" bson:"id"`
	CreatedAt time.Time         `json:"-" bson:"createdat"`
}

// view sets the per-token fields of a challenge read through token.
func (a *CSPAuth) view(token internal.HexBytes) *CSPAuth {
	a.Token = token
	a.Verified = len(a.VerifiedBy) > 0 && bytes.Equal(a.VerifiedBy, token)
	return a
}

// LastSend returns when the code was last sent to any destination, zero if never.
func (a *CSPAuth) LastSend() time.Time {
	var last time.Time
	for _, at := range a.Sends {
		if at.After(last) {
			last = at
		}
	}
	return last
}

// CSPChallengeRequest is a new code for NewCSPChallenge.
type CSPChallengeRequest struct {
	// Token is the code's first token.
	Token    internal.HexBytes
	UserID   internal.HexBytes
	AnchorID internal.HexBytes
	// Secret is the code's OTP secret.
	Secret string
	// Dest is the destination key the code's first send goes to.
	Dest string
}

// CSPTokenRequest adds Token to the pending code of challenge ChallengeID (AddCSPAuthToken).
type CSPTokenRequest struct {
	ChallengeID internal.HexBytes
	Token       internal.HexBytes
	// MaxTokens caps the code's tokens, the first included.
	MaxTokens int
	// MaxAttempts is the attempt count that locks the code.
	MaxAttempts int
	// TTL is how long the code lives from its creation.
	TTL time.Duration
}

// CSPSendClaimRequest claims a re-send of the code of challenge ChallengeID to destination Dest
// (ClaimCSPAuthSend).
type CSPSendClaimRequest struct {
	ChallengeID internal.HexBytes
	Dest        string
	// CoolDown spaces the sends to one destination.
	CoolDown time.Duration
	// TTL is how long the code lives from its creation.
	TTL time.Duration
	// MaxAttempts is the attempt count that locks the code.
	MaxAttempts int
	// MaxResends caps the code's re-sends.
	MaxResends int
}

// CSPSendClaim is a send ClaimCSPAuthSend claimed.
type CSPSendClaim struct {
	// At is the send time the claim stored for the destination, truncated to the millisecond
	// Mongo stores, so a rollback can match it exactly.
	At time.Time
	// Previous is the destination's send time before the claim, zero when it had none.
	Previous time.Time
}

// CSPSendRollback undoes a send claimed on challenge ChallengeID (RollbackCSPAuthSend).
type CSPSendRollback struct {
	ChallengeID internal.HexBytes
	Dest        string
	// ClaimedAt is the send time the claim stored; a later send stored another and is left alone.
	ClaimedAt time.Time
	// Previous is restored as the destination's send time; zero removes the destination.
	Previous time.Time
	// Resend reports whether the claim spent one of the code's resends, given back too.
	Resend bool
}

// newChallengeDoc is a challenge document with a fresh random id and token as its only token.
func newChallengeDoc(token, userID, anchorID internal.HexBytes, now time.Time) *CSPAuth {
	return &CSPAuth{
		ID:        internal.HexBytes(internal.RandomBytes(16)),
		UserID:    userID,
		AnchorID:  anchorID,
		CreatedAt: now,
		Tokens:    []CSPAuthToken{{ID: token, CreatedAt: now}},
	}
}

// insertChallenge stores a new challenge document.
func (ms *MongoStorage) insertChallenge(doc *CSPAuth) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspTokens.InsertOne(ctx, doc); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// cspNow is the current time at the millisecond precision Mongo stores, so a time written and
// later matched exactly (a send claim) compares equal.
func cspNow() time.Time {
	return time.Now().Truncate(time.Millisecond)
}
```

- Replace `SetCSPAuth`, `SetCSPAuthForChallenge` and `ClaimCSPAuthSend` (:93-180) with:

```go
// SetCSPAuth stores a new challenge for a user and anchor whose only token is token, holding the
// OTP secret (none for a challenge verified without a code). It returns ErrBadInputs if the token,
// user ID or anchor ID are nil.
func (ms *MongoStorage) SetCSPAuth(token, userID, anchorID internal.HexBytes, secret string) error {
	if token == nil || userID == nil || anchorID == nil {
		return ErrBadInputs
	}
	doc := newChallengeDoc(token, userID, anchorID, cspNow())
	doc.Secret = secret
	return ms.insertChallenge(doc)
}

// SetVerifiedCSPAuth stores an auth-only grant: a challenge with no code, verified by its only token.
func (ms *MongoStorage) SetVerifiedCSPAuth(token, userID, anchorID internal.HexBytes) error {
	if token == nil || userID == nil || anchorID == nil {
		return ErrBadInputs
	}
	now := cspNow()
	doc := newChallengeDoc(token, userID, anchorID, now)
	doc.VerifiedBy = token
	doc.VerifiedAt = now
	return ms.insertChallenge(doc)
}

// NewCSPChallenge stores a new code with its first token and its first send, to Dest, both at the
// creation time. It returns the stored challenge viewed through req.Token.
func (ms *MongoStorage) NewCSPChallenge(req CSPChallengeRequest) (*CSPAuth, error) {
	if req.Token == nil || req.UserID == nil || req.AnchorID == nil || req.Secret == "" || req.Dest == "" {
		return nil, ErrBadInputs
	}
	now := cspNow()
	doc := newChallengeDoc(req.Token, req.UserID, req.AnchorID, now)
	doc.Secret = req.Secret
	doc.Sends = map[string]time.Time{req.Dest: now}
	if err := ms.insertChallenge(doc); err != nil {
		return nil, err
	}
	return doc.view(req.Token), nil
}

// AddCSPAuthToken adds a token to a pending code in one conditional update: the code must still
// have its secret, be unlocked, unverified and unexpired, and hold fewer than MaxTokens tokens. It
// reports whether the token was added.
func (ms *MongoStorage) AddCSPAuthToken(req CSPTokenRequest) (bool, error) {
	if req.ChallengeID == nil || req.Token == nil || req.MaxTokens < 1 {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := cspNow()
	res, err := ms.cspTokens.UpdateOne(ctx, bson.M{
		"_id":        req.ChallengeID,
		"secret":     bson.M{"$exists": true},
		"attempts":   bson.M{"$lt": req.MaxAttempts},
		"verifiedby": bson.M{"$exists": false},
		"createdat":  bson.M{"$gt": now.Add(-req.TTL)},
		// a token at the last allowed position means the code is full
		fmt.Sprintf("tokens.%d", req.MaxTokens-1): bson.M{"$exists": false},
	}, bson.M{"$push": bson.M{"tokens": CSPAuthToken{ID: req.Token, CreatedAt: now}}})
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	return res.MatchedCount == 1, nil
}

// RemoveCSPAuthToken drops a token from a code that is not verified, for a token whose send
// failed. It is a no-op once the code is verified, so it never drops the token that solved it.
func (ms *MongoStorage) RemoveCSPAuthToken(challengeID, token internal.HexBytes) error {
	if challengeID == nil || token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspTokens.UpdateOne(ctx,
		bson.M{"_id": challengeID, "verifiedby": bson.M{"$exists": false}},
		bson.M{"$pull": bson.M{"tokens": bson.M{"id": token}}}); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// ClaimCSPAuthSend claims a re-send of a pending code to one destination in one conditional
// update: the code must still have its secret, be unlocked and unexpired, have resends left, and
// not have been sent to that destination within the cooldown. The update stores the send time and
// spends a resend. claimed is false, with nothing written, when any condition fails; of concurrent
// claims for one destination exactly one succeeds.
func (ms *MongoStorage) ClaimCSPAuthSend(req CSPSendClaimRequest) (claim CSPSendClaim, claimed bool, err error) {
	if req.ChallengeID == nil || req.Dest == "" {
		return CSPSendClaim{}, false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := cspNow()
	field := "sends." + req.Dest
	filter := bson.M{
		"_id":       req.ChallengeID,
		"secret":    bson.M{"$exists": true},
		"attempts":  bson.M{"$lt": req.MaxAttempts},
		"createdat": bson.M{"$gt": now.Add(-req.TTL)},
		"resends":   bson.M{"$not": bson.M{"$gte": req.MaxResends}},
		"$or": bson.A{
			bson.M{field: bson.M{"$exists": false}},
			bson.M{field: bson.M{"$lte": now.Add(-req.CoolDown)}},
		},
	}
	update := bson.M{"$set": bson.M{field: now}, "$inc": bson.M{"resends": 1}}
	var before struct {
		Sends map[string]time.Time `bson:"sends"`
	}
	err = ms.cspTokens.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.Before).SetProjection(bson.M{field: 1})).Decode(&before)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return CSPSendClaim{}, false, nil
	}
	if err != nil {
		return CSPSendClaim{}, false, errors.Join(ErrStoreToken, err)
	}
	return CSPSendClaim{At: now, Previous: before.Sends[req.Dest]}, true, nil
}

// RollbackCSPAuthSend undoes a send claim whose message never reached the voter: it restores the
// destination's previous send time (or removes it) and, for a re-send, gives the resend back. It
// only matches while the destination still holds the claim's own send time, so it never undoes a
// later send; rolledBack is false when it did not match.
func (ms *MongoStorage) RollbackCSPAuthSend(r CSPSendRollback) (rolledBack bool, err error) {
	if r.ChallengeID == nil || r.Dest == "" || r.ClaimedAt.IsZero() {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	field := "sends." + r.Dest
	filter := bson.M{"_id": r.ChallengeID, field: r.ClaimedAt}
	update := bson.M{}
	if r.Previous.IsZero() {
		update["$unset"] = bson.M{field: ""}
	} else {
		update["$set"] = bson.M{field: r.Previous}
	}
	if r.Resend {
		filter["resends"] = bson.M{"$gt": 0}
		update["$inc"] = bson.M{"resends": -1}
	}
	res, err := ms.cspTokens.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	return res.MatchedCount == 1, nil
}
```

- `CSPAuth` (:182-192) stays as is: it resolves through `fetchCSPAuthFromDB`.
- In `LastCSPAuth` (:194-216), replace `return tokenData, nil` with:

```go
	// the view of the latest code is its newest token
	var last internal.HexBytes
	if n := len(tokenData.Tokens); n > 0 {
		last = tokenData.Tokens[n-1].ID
	}
	return tokenData.view(last), nil
```

and replace its godoc with "LastCSPAuth returns the user's latest challenge on the anchor, viewed through its newest token. It returns an error if the user ID or anchor ID are nil or there is no challenge."

- Replace `VerifyCSPAuth` (:218-268) with:

```go
// VerifyCSPAuth marks the token's code as solved by that token, in one update that also clears the
// code's secret, so the code cannot be solved again through any of its tokens. A code issued with
// a secret only verifies while it still has it. It returns ErrChallengeConsumed when the code is
// already solved, and ErrTokenNotFound when no code has the token.
func (ms *MongoStorage) VerifyCSPAuth(token internal.HexBytes) error {
	if token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": tokenData.ID, "verifiedby": bson.M{"$exists": false}}
	if tokenData.Secret != "" {
		filter["secret"] = bson.M{"$exists": true}
	}
	res, err := ms.cspTokens.UpdateOne(ctx, filter, bson.M{
		"$unset": bson.M{"secret": ""},
		"$set":   bson.M{"verifiedby": token, "verifiedat": time.Now()},
	})
	if err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	if res.MatchedCount == 0 {
		return ErrChallengeConsumed
	}
	return nil
}
```

- In `IncrementCSPAuthAttempts` (:270-301):
  - Change the filter to `bson.M{"tokens.id": token, "secret": bson.M{"$exists": true}, "attempts": bson.M{"$lt": maxAttempts}}`.
  - Replace the tail after the `MatchedCount == 1` check with:

```go
	// no document matched: the token is unknown, its code was solved meanwhile, or the cap is reached
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return false, err
	}
	if tokenData.Secret == "" {
		return false, ErrChallengeConsumed
	}
	return false, nil
```

  - Its godoc: replace "for the given token" with "for the code the given token answers, across all its tokens". Add: "It returns ErrChallengeConsumed when the code was solved meanwhile."
- Delete `ClaimCSPAuthResend` and `ReleaseCSPAuthResend` (:303-335).
- Replace `fetchCSPAuthFromDB` (:574-583) with:

```go
// fetchCSPAuthFromDB returns the challenge the token answers, viewed through the token. Every
// db-internal token resolution goes through it, so a token that did not solve its code never reads
// as verified.
func (ms *MongoStorage) fetchCSPAuthFromDB(ctx context.Context, token internal.HexBytes) (*CSPAuth, error) {
	tokenData := new(CSPAuth)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"tokens.id": token}).Decode(tokenData); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData.view(token), nil
}
```

- Replace `DeleteCSPAuth` (:678-691) with:

```go
// DeleteCSPChallenge removes a challenge whose first send never reached the queue, but only while
// its first token is its only one: a token a concurrent request added must keep resolving.
func (ms *MongoStorage) DeleteCSPChallenge(challengeID internal.HexBytes) (deleted bool, err error) {
	if challengeID == nil {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.cspTokens.DeleteOne(ctx, bson.M{"_id": challengeID, "tokens.1": bson.M{"$exists": false}})
	if err != nil {
		return false, fmt.Errorf("failed to delete CSP challenge: %w", err)
	}
	return res.DeletedCount == 1, nil
}
```

`IsCSPProcessConsumed`, `ConsumeCSPProcess`, `ArmCSPProcessBlind` and `CSPProcess` need no edit: they resolve through `fetchCSPAuthFromDB`, so `tokenData.Verified` is the view's.

**migrations/0040_csp_challenge_documents.go** (new):

```go
package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(40, "csp_challenge_documents", upCSPChallengeDocuments, downCSPChallengeDocuments)
}

// cspTokenIDIndexName names the unique index resolving a token to its challenge document, kept
// explicit so the down migration drops it deterministically.
const cspTokenIDIndexName = "csp_tokens_token_id"

// upCSPChallengeDocuments turns every cspTokens row into a challenge document: a row holding one
// code under its token gets that token as its only one, and a verified row is verified by it. The
// rows' verified field is left in place and ignored. Then it indexes the tokens: unique, so a token
// answers one code, and partial, so rows without tokens (inserted by an instance still running the
// previous code during a rolling deploy) never collide as nulls. Re-running it changes nothing.
func upCSPChallengeDocuments(ctx context.Context, database *mongo.Database) error {
	coll := database.Collection("cspTokens")
	if _, err := coll.UpdateMany(ctx,
		bson.M{"tokens": bson.M{"$exists": false}},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{
			"tokens": bson.A{bson.M{"id": "$_id", "createdat": "$createdat"}},
		}}}},
	); err != nil {
		return fmt.Errorf("failed to set csp challenge tokens: %w", err)
	}
	if _, err := coll.UpdateMany(ctx,
		bson.M{"verified": true, "verifiedby": bson.M{"$exists": false}},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{"verifiedby": "$_id"}}}},
	); err != nil {
		return fmt.Errorf("failed to set csp challenge verifiers: %w", err)
	}
	model := mongo.IndexModel{
		Keys: bson.D{{Key: "tokens.id", Value: 1}},
		Options: options.Index().
			SetName(cspTokenIDIndexName).
			SetUnique(true).
			SetPartialFilterExpression(bson.M{"tokens.id": bson.M{"$exists": true}}),
	}
	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create csp token id index: %w", err)
	}
	return nil
}

// downCSPChallengeDocuments drops the token index, which touches no document. It goes through
// replaceIndex, which tolerates an index already gone.
func downCSPChallengeDocuments(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("cspTokens"), []string{cspTokenIDIndexName}, nil); err != nil {
		return fmt.Errorf("failed to drop csp token id index: %w", err)
	}
	return nil
}
```

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestVerifyCSPAuthWriteFailure|TestCSPAuthTokensOfOneCode|TestAddCSPAuthTokenCap|TestClaimRollbackCSPAuthSend|TestNewCSPChallenge|TestCSPChallengeDocumentsMigration|TestSetGetCSPAuth|TestMigrations|TestCensusRevocationIndexesMigration|CSP" ./db/'`
Expected: `ok  github.com/vocdoni/saas-backend/db`. The `csp` package does not build at this point; that is expected until Task 2.

Step 3: commit
`git commit -m "fix(db): keep one csp otp code and all its tokens in one document" -- db/csp.go db/errors.go migrations/0040_csp_challenge_documents.go db/csp_test.go db/migration_csp_challenge_documents_test.go`

Interfaces this task provides to later tasks:
- `db.CSPAuth{ID, Token, UserID, AnchorID, CreatedAt, Secret, Attempts, Resends, Tokens []CSPAuthToken, Sends map[string]time.Time, VerifiedBy, VerifiedAt, Verified}` and `(*CSPAuth).LastSend() time.Time`.
- `(*MongoStorage).SetCSPAuth(token, userID, anchorID, secret) error` (unchanged signature).
- `SetVerifiedCSPAuth(token, userID, anchorID) error`.
- `NewCSPChallenge(CSPChallengeRequest) (*CSPAuth, error)`.
- `AddCSPAuthToken(CSPTokenRequest) (bool, error)`.
- `RemoveCSPAuthToken(challengeID, token) error`.
- `IncrementCSPAuthAttempts(token, max) (bool, error)`, which can now return `ErrChallengeConsumed`.
- `VerifyCSPAuth(token) error`.
- `ClaimCSPAuthSend(CSPSendClaimRequest) (CSPSendClaim, bool, error)`.
- `RollbackCSPAuthSend(CSPSendRollback) (bool, error)`.
- `DeleteCSPChallenge(id) (bool, error)`.
- Removed: `SetCSPAuthForChallenge`, `ClaimCSPAuthResend`, `ReleaseCSPAuthResend`, `DeleteCSPAuth`, `ChallengeToken`, `LastSent`, `ErrChallengeTokensExhausted`.

### Task 2: csp sends claimed, rolled back and settled on the document

Model: strong   Repo: /home/user/src/vd/saas-backend   Files: csp/auth.go, csp/csp.go, csp/notifications/challenge.go, csp/auth_test.go, csp/daily_limits_test.go, csp/challenge_document_test.go
Goal: step 0, resend and verify run on the challenge document. Each send composes first, claims, and is rolled back when it never reaches the voter, synchronously or through the queue result (AC2, AC3, AC4, AC5, AC7 csp part, AC12).
Patch: .delivery/2026-11-25-csp-otp-challenge-document/patches/task-2.patch

Step 1: write the failing test

Add this helper at the top level of csp/auth_test.go, after the imports:

```go
// resendTo runs the resend handler's sequence for a contact that matches the member: claim the
// resend for the destination, then send it.
func resendTo(c *CSP, token internal.HexBytes, to string, ctype notifications.ChallengeType) error {
	claim, err := c.ClaimChallengeResend(token, to, ctype)
	if err != nil {
		return err
	}
	return c.ResendChallenge(claim, apicommon.DefaultLang,
		notifications.OrganizationInfo{Address: testAddress.Address(), Name: testOrgName, Logo: testOrgLogo})
}
```

Update the existing csp tests to the new API:

- **csp/auth_test.go `TestResendChallenge` (:366-539).** Every `csp.ResendChallenge(token, to, ctype, lang, name, logo, addr)` becomes `resendTo(csp, token, to, ctype)` with the same token, `to` and `ctype`. In "resend limit", the `resend` closure becomes `return resendTo(csp, testToken, "resend-limit@test.com", notifications.EmailChallenge)`. The expected errors stay as they are:
  - empty token → `ErrInvalidAuthToken`;
  - empty destination or type → `errors.ErrInvalidData`;
  - not found → `ErrInvalidAuthToken`;
  - verified → `errors.ErrUserAlreadyVerified`;
  - empty secret → `ErrTokenExpired`.
- **csp/auth_test.go `TestAuthTokenResendAndVerifyFlow` (:579).** The `csp.ResendChallenge(token, testUserEmail, …)` call becomes `resendTo(csp, token, testUserEmail, notifications.EmailChallenge)`.
- **csp/auth_test.go `TestAuthTokenReusesPendingChallenge` (:679-1090):**
  - :727 `c.Assert(secondData.ChallengeID, qt.DeepEquals, first)` → `firstData, err := csp.Storage.CSPAuth(first)`, `c.Assert(err, qt.IsNil)`, `c.Assert(secondData.ID, qt.DeepEquals, firstData.ID)`, `c.Assert(secondData.Tokens, qt.HasLen, 2)`.
  - :746 `c.Assert(challenge.LastSentAt.IsZero(), qt.IsFalse)` → `c.Assert(challenge.Resends, qt.Equals, 1)`.
  - :750-781 ("ResendCoolDown reports…"): declare `dest := DestinationKey(notifications.EmailChallenge, email)` and pass it as the second argument of every `csp.ResendCoolDown(...)` call. Add at the end: `_, ok = csp.ResendCoolDown(first, DestinationKey(notifications.SMSChallenge, "+34600000000"))` followed by `c.Assert(ok, qt.IsTrue)` (a destination never sent to is due at once, `remaining == 0`).
  - :819 `err = csp.ResendChallenge(second, …)` → `err = resendTo(csp, second, email, notifications.EmailChallenge)`.
  - :845, :863, :886 `c.Assert(secondData.ChallengeID, qt.HasLen, 0)` → `c.Assert(secondData.Tokens, qt.HasLen, 1)` (a new code holds only its first token).
  - :899 the loop body `err = csp.ResendChallenge(token, email, …)` → `err = resendTo(csp, token, email, notifications.EmailChallenge)`.
  - :920-924 the `resend` closure becomes `return resendTo(csp, token, email, notifications.EmailChallenge)`.
  - Delete the subtest "a resend that loses the send race gives its resend back" (:1007-1028): the claim is now one update, and `TestChallengeResendRace` covers the race.
  - :1067 `for range MaxChallengeTokens {` → `for range MaxChallengeTokens - 1 {` (the first token counts).
  - :1083 `c.Assert(tokenData.ChallengeID, qt.HasLen, 0)` → `c.Assert(tokenData.Tokens, qt.HasLen, 1)`.
- **csp/daily_limits_test.go `TestDailyChallengeSendCap` (:50, :56).** `csp.ResendChallenge(token, dailyCapEmail, notifications.EmailChallenge, apicommon.DefaultLang, "", "", testAddress.Address())` → `resendTo(csp, token, dailyCapEmail, notifications.EmailChallenge)`, keeping the assertions.

New file csp/challenge_document_test.go:

```go
package csp

import (
	"context"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// newChallengeCSP returns a CSP on a fresh database with synchronous delivery, the given cooldown
// and mail service, an hour-long code TTL and the given daily cap (0: the default).
func newChallengeCSP(c *qt.C, coolDown time.Duration, mail bool, daily int) *CSP {
	c.Helper()
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	conf := &Config{
		DB:                       testDB,
		SMSService:               testSMSService,
		NotificationCoolDownTime: coolDown,
		NotificationTTL:          time.Hour,
		MaxDailyChallengeSends:   daily,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	}
	if mail {
		conf.MailService = testMailService
	}
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), conf)
	c.Assert(err, qt.IsNil)
	return csp
}

// stepZero runs AuthToken for the test user and anchor.
func stepZero(csp *CSP, to string, ctype notifications.ChallengeType) (internal.HexBytes, error) {
	return csp.AuthToken(testAnchorID, testUserID, to, ctype, apicommon.DefaultLang,
		testOrgName, testOrgLogo, testAddress.Address())
}

// eventually polls cond for up to 5 s: a delivery result is settled by the queue consumer, which
// runs after even a synchronous push has returned.
func eventually(c *qt.C, cond func() bool) {
	c.Helper()
	for range 50 {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.Fatalf("condition not met within 5s")
}

// dailySendsLeft reports whether the test user can still be sent a challenge today on the test
// anchor under a cap of max.
func dailySendsLeft(c *qt.C, csp *CSP, maxSends int) bool {
	reached, err := csp.Storage.CSPCounterReached(db.CSPCounterChallengeSends,
		counterKey(testUserID, testAnchorID), maxSends)
	c.Assert(err, qt.IsNil)
	return !reached
}

func TestDestinationKey(t *testing.T) {
	c := qt.New(t)
	key := DestinationKey(notifications.EmailChallenge, "Voter@Example.com")
	c.Assert(key, qt.Equals, DestinationKey(notifications.EmailChallenge, "voter@example.com"))
	c.Assert(key, qt.HasLen, 32)
	c.Assert(key, qt.Not(qt.Equals), DestinationKey(notifications.SMSChallenge, "voter@example.com"))
	c.Assert(DestinationKey(notifications.SMSChallenge, "+34600000001"), qt.Not(qt.Equals),
		DestinationKey(notifications.SMSChallenge, "+34600000002"))
}

// TestChallengeSendRollback checks that a send that never reached the voter gives back its claim on
// the destination, its resend and its daily-cap unit (AC2).
func TestChallengeSendRollback(t *testing.T) {
	c := qt.New(t)

	c.Run("async delivery failure", func(c *qt.C) {
		// no mail service: the queue gives every email up and reports Success=false
		csp := newChallengeCSP(c, time.Hour, false, 1)
		const email = "rollback-async@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		settled := func() bool {
			data, err := csp.Storage.CSPAuth(first)
			c.Assert(err, qt.IsNil)
			return len(data.Sends) == 0 && data.Resends == 0 && dailySendsLeft(c, csp, 1)
		}
		eventually(c, settled)

		// the destination is due at once: a repeated step 0 re-sends, which fails and is rolled
		// back the same way, resend included
		second, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(second, qt.Not(qt.DeepEquals), first)
		eventually(c, settled)
	})

	c.Run("resend whose message cannot be built", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, true, 2)
		const email = "rollback-compose@example.com"
		token, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		_ = fetchOTPCodeFromEmail(c, email)
		// the claim goes through for the submitted destination, then composing fails
		c.Assert(resendTo(csp, token, email, "bogus"), qt.ErrorIs, ErrNotificationFailure)
		data, err := csp.Storage.CSPAuth(token)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 0)
		c.Assert(data.Sends, qt.HasLen, 1)
		_, ok := data.Sends[DestinationKey("bogus", email)]
		c.Assert(ok, qt.IsFalse)
		// one of the two daily sends is left
		c.Assert(dailySendsLeft(c, csp, 2), qt.IsTrue)
	})
}

// TestChallengeReuseFailedSendDropsToken checks that a step 0 whose re-send of the pending code
// fails returns the error and leaves no extra token (AC3).
func TestChallengeReuseFailedSendDropsToken(t *testing.T) {
	c := qt.New(t)
	csp := newChallengeCSP(c, time.Hour, true, 0)
	const email = "reuse-fail@example.com"
	first, err := stepZero(csp, email, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, email)

	_, err = stepZero(csp, email, "bogus")
	c.Assert(err, qt.ErrorIs, ErrNotificationFailure)
	data, err := csp.Storage.CSPAuth(first)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Tokens, qt.HasLen, 1)
	c.Assert(data.Resends, qt.Equals, 0)
}

// TestChallengeResendRace checks that of concurrent resends to one destination exactly one sends,
// and the others report the wait left from the send that won (AC4).
func TestChallengeResendRace(t *testing.T) {
	c := qt.New(t)
	csp := newChallengeCSP(c, time.Hour, true, 0)
	const emailA, emailB = "race-a@example.com", "race-b@example.com"
	token, err := stepZero(csp, emailA, notifications.EmailChallenge)
	c.Assert(err, qt.IsNil)
	_ = fetchOTPCodeFromEmail(c, emailA)

	const racers = 3
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Go(func() { errs[i] = resendTo(csp, token, emailB, notifications.EmailChallenge) })
	}
	wg.Wait()
	sent := 0
	for _, err := range errs {
		if err == nil {
			sent++
			continue
		}
		c.Assert(err, qt.ErrorIs, errors.ErrAttemptCoolDownTime)
		var apiErr errors.Error
		c.Assert(errors.As(err, &apiErr), qt.IsTrue)
		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		coolDown, ok := data["coolDownTime"].(int64)
		c.Assert(ok, qt.IsTrue)
		c.Assert(coolDown > (59 * time.Minute).Milliseconds(), qt.IsTrue)
	}
	c.Assert(sent, qt.Equals, 1)
	_ = fetchOTPCodeFromEmail(c, emailB)
	data, err := csp.Storage.CSPAuth(token)
	c.Assert(err, qt.IsNil)
	c.Assert(data.Resends, qt.Equals, 1)
}

// TestChallengeSendPerDestination checks that a step 0 naming another destination of the voter
// inside the cooldown sends the code there, at most once per destination per cooldown and at most
// MaxChallengeResends times per code (AC5), and that the casing of an email is not another
// destination (AC12).
func TestChallengeSendPerDestination(t *testing.T) {
	c := qt.New(t)
	const phone = "+34600000077"
	smsDelivered := func() bool { return testSMSService.ConsumeSMS(phone) != nil }
	emailDelivered := func(email string) bool {
		_, err := testMailService.FindEmail(context.Background(), email)
		return err == nil
	}

	c.Run("once per destination per cooldown", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, true, 0)
		const email = "per-dest@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered(email), qt.IsTrue)
		_, err = stepZero(csp, phone, notifications.SMSChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(smsDelivered(), qt.IsTrue)
		for range 2 {
			_, err = stepZero(csp, email, notifications.EmailChallenge)
			c.Assert(err, qt.IsNil)
			c.Assert(emailDelivered(email), qt.IsFalse)
			_, err = stepZero(csp, phone, notifications.SMSChallenge)
			c.Assert(err, qt.IsNil)
			c.Assert(smsDelivered(), qt.IsFalse)
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 1)
		c.Assert(data.Sends, qt.HasLen, 2)
	})

	c.Run("alternating destinations spend the code's resends", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Millisecond, true, 0)
		const email = "alternate@example.com"
		first, err := stepZero(csp, email, notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered(email), qt.IsTrue)
		// SMS, email, SMS: the code's three resends; then nothing more is sent, the token still is
		for i, want := range []bool{true, true, true, false, false} {
			time.Sleep(10 * time.Millisecond)
			if i%2 == 0 {
				_, err = stepZero(csp, phone, notifications.SMSChallenge)
				c.Assert(err, qt.IsNil)
				c.Assert(smsDelivered(), qt.Equals, want)
			} else {
				_, err = stepZero(csp, email, notifications.EmailChallenge)
				c.Assert(err, qt.IsNil)
				c.Assert(emailDelivered(email), qt.Equals, want)
			}
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, MaxChallengeResends)
	})

	c.Run("email casing", func(c *qt.C) {
		csp := newChallengeCSP(c, time.Hour, true, 0)
		first, err := stepZero(csp, "casing@example.com", notifications.EmailChallenge)
		c.Assert(err, qt.IsNil)
		c.Assert(emailDelivered("casing@example.com"), qt.IsTrue)
		for _, spelling := range []string{"Casing@Example.com", "CASING@EXAMPLE.COM"} {
			_, err = stepZero(csp, spelling, notifications.EmailChallenge)
			c.Assert(err, qt.IsNil)
		}
		data, err := csp.Storage.CSPAuth(first)
		c.Assert(err, qt.IsNil)
		c.Assert(data.Resends, qt.Equals, 0)
		c.Assert(data.Sends, qt.HasLen, 1)
	})
}

// TestMigratedLTSChallenges checks that rows stored as lts stores them, once migrated, verify,
// re-send and sign (AC7).
func TestMigratedLTSChallenges(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	dbName := test.RandomDatabaseName()
	testDB, err := db.New(testMongoURI, dbName)
	c.Assert(err, qt.IsNil)
	csp, err := New(ctx, &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Hour,
		NotificationTTL:          time.Hour,
		RootKey:                  *testRootKey,
		SyncDelivery:             true,
	})
	c.Assert(err, qt.IsNil)

	database := testDB.DBClient.Database(dbName)
	now := time.Now().Truncate(time.Millisecond)
	secret := gotp.RandomSecret(16)
	pending, verified, authOnly := newAuthToken(), newAuthToken(), newAuthToken()
	pendingUser := internal.HexBytes([]byte("pendingUser"))
	verifiedUser := internal.HexBytes([]byte("verifiedUser"))
	authOnlyUser := internal.HexBytes([]byte("authOnlyUser"))
	_, err = database.Collection("cspTokens").InsertMany(ctx, []any{
		bson.M{
			"_id": pending, "userid": pendingUser, "bundleid": testAnchorID, "createdat": now,
			"secret": secret, "attempts": 0, "verified": false, "verifiedat": time.Time{}, "resends": 0,
		},
		bson.M{
			"_id": verified, "userid": verifiedUser, "bundleid": testAnchorID, "createdat": now,
			"attempts": 1, "verified": true, "verifiedat": now, "resends": 0,
		},
		bson.M{
			"_id": authOnly, "userid": authOnlyUser, "bundleid": testAnchorID, "createdat": now,
			"attempts": 0, "verified": true, "verifiedat": now,
		},
	})
	c.Assert(err, qt.IsNil)
	mig, ok := migrations.AsMap()[40]
	c.Assert(ok, qt.IsTrue)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	sign := func(token internal.HexBytes) {
		_, err := csp.Sign(token, testAddress, internal.HexBytes(util.RandomBytes(32)), testUserWeightBytes,
			signers.SignerTypeECDSASalted)
		c.Assert(err, qt.IsNil)
	}
	// the pending code re-sends, verifies and signs
	const email = "migrated-pending@example.com"
	c.Assert(resendTo(csp, pending, email, notifications.EmailChallenge), qt.IsNil)
	code := fetchOTPCodeFromEmail(c, email)
	c.Assert(code, qt.Equals, gotp.NewDefaultHOTP(secret).At(0))
	c.Assert(csp.VerifyAuthToken(pending, code), qt.IsNil)
	sign(pending)
	// verified and auth-only rows sign as they are
	sign(verified)
	sign(authOnly)
}
```

Notes for the implementer:
- `wg.Go` needs Go ≥1.25; the module already uses it at csp/auth_test.go:939.
- If `csp/signers` or `go.vocdoni.io/dvote/util` are imported under other paths in csp/sign_test.go, copy those import lines verbatim.
- Drop the stray `c.Assert(err, qt.IsNil)` in `newChallengeCSP` if the linter flags it.

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestDestinationKey|TestChallenge|TestMigratedLTSChallenges" ./csp/'`
Expected: build failure, `undefined: DestinationKey` and the wrong-signature errors for `ClaimChallengeResend`/`ResendChallenge`.

Step 2: implement

**csp/notifications/challenge.go:** add to `NotificationChallenge`, after `Success bool`:

```go
	// SendInfo is the sender's record of this send, carried back untouched on NotificationsSent
	// so the sender can settle the delivery result.
	SendInfo any
```

**csp/csp.go:**
- Replace the godoc of `MaxChallengeResends` (:25-27) with: "MaxChallengeResends is the number of times one challenge code can be sent again after its first send, through a repeated auth request or a resend request, across every authentication token answering it. Sends to one destination are also spaced by the notification cooldown, so this bounds the emails/SMS a member can be sent for one code."
- Replace the godoc of `MaxChallengeTokens` (:30-32) with: "MaxChallengeTokens is the number of authentication tokens one challenge code can be issued, its first included. Past it, a request gets a new code instead, subject to the notification cooldown and the daily send cap, so the tokens sharing a code stay bounded."
- In `New` (:194-254), delete the `go func() { … }()` block (:206-221). Then replace `return c, nil` at the end with:

```go
	go c.settleNotifications()
	go queue.Start()
	return c, nil
```

  and delete the original `go queue.Start()` (:222). Add after `countSentNotification`:

```go
// settleNotifications consumes the queue's delivery results until the service context ends.
func (c *CSP) settleNotifications() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case ch := <-c.notifyQueue.NotificationsSent:
			log.Debugw("notification pop from queue",
				"success", ch.Success,
				"type", ch.Type,
				"userID", ch.UserID,
				"anchorID", ch.AnchorID)
			c.settleNotification(ch)
		}
	}
}

// settleNotification accounts for a challenge the queue reports. A delivered one counts against
// the organization's sent counters. One given up on (expired, permanently failed, no service
// configured) gives back the claims of its send, so the voter can ask again at once and keeps the
// daily budget nothing was delivered for.
func (c *CSP) settleNotification(ch *notifications.NotificationChallenge) {
	countSentNotification(c.Storage, ch)
	if ch.Success {
		return
	}
	if send, ok := ch.SendInfo.(*challengeSend); ok {
		c.rollbackSend(send)
	}
}
```

(`c.notifyQueue` is the `*notifications.Queue` that `New` stores; if its field type is an interface without `NotificationsSent`, pass `queue` as a parameter instead: `go c.settleNotifications(queue)`.)

**csp/auth.go:**
- Keep the import block. Add `"crypto/sha256"`, `"encoding/hex"` and `"strings"`, and drop any import the compiler reports unused.
- Replace lines 20-505 (from `challengeDelivery` through `releaseChallengeSend`) with the code below. `counterLimitError` and `counterKey` (:507-524) stay.

```go
// challengeDelivery is where and how a challenge code is sent.
type challengeDelivery struct {
	to    string
	ctype notifications.ChallengeType
	lang  string
	org   notifications.OrganizationInfo
}

// dest is the destination key of the delivery (DestinationKey).
func (d challengeDelivery) dest() string {
	return DestinationKey(d.ctype, d.to)
}

// challengeCode is a code to deliver: whose it is and when it expires.
type challengeCode struct {
	userID    internal.HexBytes
	anchorID  internal.HexBytes
	code      string
	expiresAt time.Time
}

// challengeSend is one send of a challenge code: the composed message and the claims it holds,
// given back if the message never reaches the voter. The queue carries it back with the delivery
// result (NotificationChallenge.SendInfo).
type challengeSend struct {
	// challengeID is the challenge document the send was claimed on.
	challengeID internal.HexBytes
	// dest is the destination key the send was claimed for.
	dest string
	// claimedAt is the send time the claim stored; a rollback only matches it, so it never undoes a
	// later send.
	claimedAt time.Time
	// previous is the destination's send time before the claim, zero when it had none.
	previous time.Time
	// resend reports whether the claim spent one of the code's resends; a new code's first send
	// spends none.
	resend bool
	// daily is the unit of the member's daily send budget the send holds.
	daily db.CSPCounterClaim
	// ch is the composed message.
	ch *notifications.NotificationChallenge
}

// DestinationKey is the key under which a challenge records its sends to one destination: the
// lowercase hex of the first 16 bytes of SHA-256 over the challenge type, ":" and the destination.
// Contact matching ignores the case of emails, so they are lowercased first, or alternating the
// casing of one address would dodge its cooldown; phones arrive normalized. The key reveals no
// contact data.
func DestinationKey(ctype notifications.ChallengeType, to string) string {
	if ctype == notifications.EmailChallenge {
		to = strings.ToLower(to)
	}
	sum := sha256.Sum256([]byte(string(ctype) + ":" + to))
	return hex.EncodeToString(sum[:16])
}

// AuthToken method generates a new authentication token for a user, anchored to
// a voting process, and sends its challenge code to `to`.
//
// While the user's last code is still usable (unverified, not expired and not locked by failed
// attempts) and can take another token, it is reused rather than replaced: the new token answers
// the same code, so every code the user received keeps working. The code is re-sent to `to` unless
// it was sent there less than the notification cooldown ago, its resends are spent, or so is the
// daily send cap; in those cases the token is returned without sending anything, as the user
// already holds the code. Each call returns a distinct token, so whoever only knows the user's
// login data never holds the token the user verifies.
//
// Otherwise a new code is created, subject to the cooldown since the last code's latest send and
// to the daily send cap. It returns the token as HexBytes.
func (c *CSP) AuthToken(anchorID, uID internal.HexBytes, to string,
	ctype notifications.ChallengeType, lang string,
	orgName, orgLogo string, orgAddress common.Address,
) (internal.HexBytes, error) {
	// check the input parameters
	if len(anchorID) == 0 {
		return nil, ErrNoAnchorID
	}
	if len(uID) == 0 {
		return nil, ErrNoUserID
	}

	// For auth-only cases (no challenge type and no destination), create a pre-verified token
	if to == "" && ctype == "" {
		return c.createAuthOnlyToken(anchorID, uID)
	}
	delivery := challengeDelivery{
		to:    to,
		ctype: ctype,
		lang:  lang,
		org:   notifications.OrganizationInfo{Address: orgAddress, Name: orgName, Logo: orgLogo},
	}

	last, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && !errors.Is(err, db.ErrTokenNotFound) {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if last == nil {
		return c.newChallenge(uID, anchorID, delivery)
	}
	if c.challengeUsable(last) {
		token, err := c.reuseChallenge(last, delivery)
		if err != nil || token != nil {
			return token, err
		}
		// the code took no more tokens, or stopped being usable meanwhile: a new one follows
	}
	if remaining := c.coolDownSince(last.LastSend()); remaining > 0 {
		log.Warnw("cooldown time not reached",
			"userID", uID,
			"anchorID", anchorID,
			"challenge", last.ID)
		return nil, coolDownError(remaining)
	}
	return c.newChallenge(uID, anchorID, delivery)
}

// coolDownSince is how long the notification cooldown still runs after a send at sentAt; zero or
// less once it has passed, or when there was no send.
func (c *CSP) coolDownSince(sentAt time.Time) time.Duration {
	if sentAt.IsZero() {
		return 0
	}
	return c.notificationCoolDownTime - time.Since(sentAt)
}

// reuseChallenge issues a new token for the user's pending code and re-sends the code to the
// delivery's destination when that is due. It returns a nil token, and no error, when the code
// takes no more tokens or stopped being usable before the send was claimed, so the caller creates
// a new code instead. A send refused by the destination's cooldown, the code's resends or the
// daily cap still returns the token, as the user holds the code; any other failed send drops the
// token again.
func (c *CSP) reuseChallenge(challenge *db.CSPAuth, delivery challengeDelivery) (internal.HexBytes, error) {
	token := newAuthToken()
	added, err := c.Storage.AddCSPAuthToken(db.CSPTokenRequest{
		ChallengeID: challenge.ID,
		Token:       token,
		MaxTokens:   MaxChallengeTokens,
		MaxAttempts: MaxChallengeAttempts,
		TTL:         c.notificationTTL,
	})
	if err != nil {
		log.Warnw("error adding token to pending challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if !added {
		return nil, nil
	}
	if !c.sendDue(challenge, delivery.dest()) {
		return token, nil
	}
	err = c.resendCode(challenge, delivery)
	if err == nil || errors.Is(err, errors.ErrAttemptCoolDownTime) || errors.Is(err, errors.ErrVerificationMaxAttempts) {
		return token, nil
	}
	if dropErr := c.Storage.RemoveCSPAuthToken(challenge.ID, token); dropErr != nil {
		log.Warnw("error dropping token of unsent challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", dropErr)
	}
	if errors.Is(err, ErrTokenExpired) {
		return nil, nil
	}
	return nil, err
}

// sendDue reports whether challenge's code can be sent to dest now: it has resends left and was
// not sent there within the cooldown. ClaimCSPAuthSend decides; this only skips composing a send
// that would certainly be refused.
func (c *CSP) sendDue(challenge *db.CSPAuth, dest string) bool {
	return challenge.Resends < MaxChallengeResends && c.coolDownSince(challenge.Sends[dest]) <= 0
}

// resendCode re-sends challenge's code on a repeated auth request, in order: compose the message,
// claim a unit of the daily send cap, claim the send on the challenge, push. Each failed step
// gives back what the earlier ones claimed.
func (c *CSP) resendCode(challenge *db.CSPAuth, delivery challengeDelivery) error {
	ch, err := c.composeResend(challenge, delivery)
	if err != nil {
		return err
	}
	daily, err := c.claimDailyChallengeSend(challenge.UserID, challenge.AnchorID)
	if err != nil {
		return err
	}
	dest := delivery.dest()
	claim, err := c.claimSend(challenge, dest)
	if err != nil {
		c.releaseChallengeSend(daily)
		return err
	}
	_, err = c.pushChallengeCode(&challengeSend{
		challengeID: challenge.ID,
		dest:        dest,
		claimedAt:   claim.At,
		previous:    claim.Previous,
		resend:      true,
		daily:       daily,
		ch:          ch,
	})
	return err
}

// newChallenge creates a new code for the user and sends it, in order: compose the message, claim
// a unit of the daily send cap, store the code with its first send, push. A code that never
// reached the queue is deleted again.
func (c *CSP) newChallenge(uID, anchorID internal.HexBytes, delivery challengeDelivery) (internal.HexBytes, error) {
	token, secret, code := c.generateToken()
	ch, err := c.composeChallenge(challengeCode{
		userID:    uID,
		anchorID:  anchorID,
		code:      code,
		expiresAt: time.Now().Add(c.notificationTTL),
	}, delivery)
	if err != nil {
		return nil, err
	}
	// cap the challenges (new tokens + resends) this member can be sent for this process today:
	// the cooldown only spaces sends, so without a cap a member's contact could be flooded with
	// one email/SMS per cooldown period all day long
	daily, err := c.claimDailyChallengeSend(uID, anchorID)
	if err != nil {
		return nil, err
	}
	dest := delivery.dest()
	challenge, err := c.Storage.NewCSPChallenge(db.CSPChallengeRequest{
		Token:    token,
		UserID:   uID,
		AnchorID: anchorID,
		Secret:   secret,
		Dest:     dest,
	})
	if err != nil {
		log.Warnw("error storing new challenge",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		c.releaseChallengeSend(daily)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth token stored",
		"userID", uID,
		"anchorID", anchorID,
		"token", token)
	queued, err := c.pushChallengeCode(&challengeSend{
		challengeID: challenge.ID,
		dest:        dest,
		claimedAt:   challenge.Sends[dest],
		daily:       daily,
		ch:          ch,
	})
	if err != nil {
		// a code that never reached the queue must not look pending, or the next step 0 would
		// hand out a token for it without sending anything
		if !queued {
			if _, delErr := c.Storage.DeleteCSPChallenge(challenge.ID); delErr != nil {
				log.Warnw("error deleting unsent challenge", "userID", uID, "anchorID", anchorID, "error", delErr)
			}
		}
		return nil, err
	}
	return token, nil
}

// ChallengeSendRefusal returns, without spending anything, the refusal AuthToken would answer
// right now for the member on the anchor process: the cooldown since the last code's latest send
// (once that code cannot be reused), or the daily send cap. It is a peek, for a caller that must
// not spend something else (a certificate code) on a request AuthToken would refuse; AuthToken
// still enforces both. Reusing a usable code that can take another token is never refused,
// whichever destination the request names, so the peek needs none.
func (c *CSP) ChallengeSendRefusal(uID, anchorID internal.HexBytes) error {
	last, err := c.Storage.LastCSPAuth(uID, anchorID)
	if err != nil && !errors.Is(err, db.ErrTokenNotFound) {
		log.Warnw("error getting last token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return ErrStorageFailure
	}
	if last != nil {
		if c.challengeUsable(last) && len(last.Tokens) < MaxChallengeTokens {
			return nil
		}
		if remaining := c.coolDownSince(last.LastSend()); remaining > 0 {
			return coolDownError(remaining)
		}
	}
	reached, err := c.Storage.CSPCounterReached(db.CSPCounterChallengeSends, counterKey(uID, anchorID),
		c.maxDailyChallengeSends)
	if err != nil {
		log.Warnw("error reading daily challenge sends", "userID", uID, "anchorID", anchorID, "error", err)
		return ErrStorageFailure
	}
	if reached {
		window := db.CSPCounterWindow(db.CSPCounterChallengeSends)
		resetAt := time.Now().UTC().Truncate(window).Add(window)
		return counterLimitError(db.CSPCounterResult{ResetAt: resetAt}, "daily challenge limit reached, try again tomorrow")
	}
	return nil
}

// ResendClaim is a resend ClaimChallengeResend claimed: the send ResendChallenge makes once the
// caller has matched the contact against the member. A claim never passed to ResendChallenge
// stays spent.
type ResendClaim struct {
	challenge *db.CSPAuth
	to        string
	ctype     notifications.ChallengeType
	send      challengeSend
}

// ClaimChallengeResend claims, in one update of the code the token answers, one of the code's
// MaxChallengeResends resends and the send to the submitted destination (to, ctype). The caller
// claims before checking that destination against the member, so a wrong contact keeps both
// spent: otherwise a token holder could probe a member's other contact (say, the phone once they
// know the email) with unlimited resends. The resends are counted per code, so every token sharing
// it shares them. Inside the destination's cooldown it spends nothing and fails with
// ErrAttemptCoolDownTime carrying the remaining milliseconds; once the resends are spent it fails
// with ErrVerificationMaxAttempts.
func (c *CSP) ClaimChallengeResend(token internal.HexBytes, to string, ctype notifications.ChallengeType,
) (*ResendClaim, error) {
	if len(token) == 0 {
		return nil, ErrInvalidAuthToken
	}
	if to == "" || ctype == "" {
		return nil, errors.ErrInvalidData.Withf("missing challenge destination or type")
	}
	challenge, err := c.Storage.CSPAuth(token)
	if err != nil {
		if errors.Is(err, db.ErrTokenNotFound) {
			return nil, ErrInvalidAuthToken
		}
		log.Warnw("error getting token for resend",
			"token", token,
			"error", err)
		return nil, ErrStorageFailure
	}
	dest := DestinationKey(ctype, to)
	claim, err := c.claimSend(challenge, dest)
	if err != nil {
		return nil, err
	}
	return &ResendClaim{
		challenge: challenge,
		to:        to,
		ctype:     ctype,
		send: challengeSend{
			challengeID: challenge.ID,
			dest:        dest,
			claimedAt:   claim.At,
			previous:    claim.Previous,
			resend:      true,
		},
	}, nil
}

// ResendChallenge sends the code of a resend claimed with ClaimChallengeResend, counting it against
// the member's daily challenge budget. If the message cannot be composed or the daily budget is
// spent, nothing is sent and the claim is given back.
func (c *CSP) ResendChallenge(claim *ResendClaim, lang string, org notifications.OrganizationInfo) error {
	if claim == nil {
		return ErrInvalidAuthToken
	}
	send := &claim.send
	ch, err := c.composeResend(claim.challenge, challengeDelivery{to: claim.to, ctype: claim.ctype, lang: lang, org: org})
	if err != nil {
		c.rollbackSend(send)
		return err
	}
	if send.daily, err = c.claimDailyChallengeSend(claim.challenge.UserID, claim.challenge.AnchorID); err != nil {
		c.rollbackSend(send)
		return err
	}
	send.ch = ch
	_, err = c.pushChallengeCode(send)
	return err
}

// ResendCoolDown returns how long until the code the token answers can be sent again to the
// destination dest (DestinationKey), zero if it can be sent now. ok is false when there is no code
// to resend: an auth-only or verified token, or a code that is gone, solved, expired or locked. It
// is a hint for clients; the resend itself is still refused by the cooldown it reports.
func (c *CSP) ResendCoolDown(token internal.HexBytes, dest string) (remaining time.Duration, ok bool) {
	challenge, err := c.Storage.CSPAuth(token)
	if err != nil || challenge.Verified || !c.challengeUsable(challenge) {
		return 0, false
	}
	return max(c.coolDownSince(challenge.Sends[dest]), 0), true
}

// coolDownError is the ErrAttemptCoolDownTime answer carrying the milliseconds left, at least one.
func coolDownError(remaining time.Duration) errors.Error {
	remaining = max(remaining, time.Millisecond)
	return errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": remaining.Milliseconds()})
}

// claimSend claims a send of challenge's code to dest (ClaimCSPAuthSend). When the claim does not
// match it re-reads the challenge and returns why (sendRefusal), so a request that lost a send race
// reports the wait the winner stored; if nothing refuses the send any more, it claims once again.
func (c *CSP) claimSend(challenge *db.CSPAuth, dest string) (db.CSPSendClaim, error) {
	req := db.CSPSendClaimRequest{
		ChallengeID: challenge.ID,
		Dest:        dest,
		CoolDown:    c.notificationCoolDownTime,
		TTL:         c.notificationTTL,
		MaxAttempts: MaxChallengeAttempts,
		MaxResends:  MaxChallengeResends,
	}
	for range 2 {
		claim, claimed, err := c.Storage.ClaimCSPAuthSend(req)
		if err != nil {
			log.Warnw("error claiming challenge send",
				"userID", challenge.UserID,
				"anchorID", challenge.AnchorID,
				"challenge", challenge.ID,
				"error", err)
			return db.CSPSendClaim{}, ErrStorageFailure
		}
		if claimed {
			return claim, nil
		}
		fresh, err := c.Storage.CSPAuth(challenge.Token)
		if err != nil {
			if errors.Is(err, db.ErrTokenNotFound) {
				return db.CSPSendClaim{}, ErrTokenExpired
			}
			log.Warnw("error reading challenge after a refused send", "challenge", challenge.ID, "error", err)
			return db.CSPSendClaim{}, ErrStorageFailure
		}
		if refusal := c.sendRefusal(fresh, dest); refusal != nil {
			return db.CSPSendClaim{}, refusal
		}
	}
	return db.CSPSendClaim{}, ErrStorageFailure
}

// sendRefusal is why a send of challenge's code to dest cannot be claimed, read from a fresh copy
// of the challenge viewed through the requesting token; nil when nothing refuses it.
func (c *CSP) sendRefusal(challenge *db.CSPAuth, dest string) error {
	if challenge.Verified {
		// a verified token is always reported as such, regardless of age, so it never
		// masquerades as merely expired
		left := max(c.notificationTTL-time.Since(challenge.CreatedAt), 0)
		return errors.ErrUserAlreadyVerified.WithData(map[string]any{"coolDownTime": left.Seconds()})
	}
	// no secret (solved, or never had one), expired or locked: ErrTokenExpired prompts the client
	// to restart the OTP flow, and a code is never regenerated from an empty secret
	if !c.challengeUsable(challenge) {
		log.Warnw("send requested for token without usable challenge",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"token", challenge.Token,
			"attempts", challenge.Attempts)
		return ErrTokenExpired
	}
	if remaining := c.coolDownSince(challenge.Sends[dest]); remaining > 0 {
		return coolDownError(remaining)
	}
	if challenge.Resends >= MaxChallengeResends {
		return errors.ErrVerificationMaxAttempts.Withf("challenge resend limit reached, request a new token")
	}
	return nil
}

// challengeUsable reports whether the challenge's code can still be accepted: it has a secret (not
// yet solved), is not verified, not expired and not locked by attempts.
func (c *CSP) challengeUsable(challenge *db.CSPAuth) bool {
	return challenge.Secret != "" &&
		len(challenge.VerifiedBy) == 0 &&
		challenge.Attempts < MaxChallengeAttempts &&
		time.Since(challenge.CreatedAt) < c.notificationTTL
}

// composeResend composes the message carrying challenge's existing code, which expires with the
// code.
func (c *CSP) composeResend(challenge *db.CSPAuth, delivery challengeDelivery,
) (*notifications.NotificationChallenge, error) {
	code, err := c.regenerateTokenCode(challenge.Secret)
	if err != nil {
		log.Warnw("error regenerating token code",
			"userID", challenge.UserID,
			"anchorID", challenge.AnchorID,
			"challenge", challenge.ID,
			"error", err)
		return nil, ErrChallengeCodeFailure
	}
	return c.composeChallenge(challengeCode{
		userID:    challenge.UserID,
		anchorID:  challenge.AnchorID,
		code:      code,
		expiresAt: challenge.CreatedAt.Add(c.notificationTTL),
	}, delivery)
}

// composeChallenge composes the notification carrying a challenge code, advertising the time left
// until the code expires.
func (*CSP) composeChallenge(code challengeCode, delivery challengeDelivery,
) (*notifications.NotificationChallenge, error) {
	ch, err := notifications.NewNotificationChallenge(delivery.ctype, delivery.lang, code.userID, code.anchorID,
		delivery.to, code.code, delivery.org, time.Until(code.expiresAt).Round(time.Second).String())
	if err != nil {
		log.Warnw("error composing notification challenge",
			"userID", code.userID,
			"anchorID", code.anchorID,
			"error", err)
		return nil, ErrNotificationFailure
	}
	ch.ExpiresAt = code.expiresAt
	return ch, nil
}

// pushChallengeCode pushes the message of send to the queue, which carries send back with the
// delivery result so a delivery given up on gives back its claims (settleNotification). When the
// message never reached the queue, it gives them back itself and queued is false. A synchronous
// delivery that timed out is still queued and keeps them for the queue result to settle.
func (c *CSP) pushChallengeCode(send *challengeSend) (queued bool, err error) {
	send.ch.SendInfo = send
	err = c.pushChallenge(send.ch)
	if err == nil {
		return true, nil
	}
	log.Warnw("error pushing notification challenge",
		"userID", send.ch.UserID,
		"anchorID", send.ch.AnchorID,
		"error", err)
	if errors.Is(err, errChallengeStillQueued) {
		return true, ErrNotificationFailure
	}
	c.rollbackSend(send)
	return false, ErrNotificationFailure
}

// rollbackSend gives back the claims of a send that never reached the voter: the send on the
// challenge, unless a later send replaced it, and the daily-cap unit, always, as nothing was
// delivered for it.
func (c *CSP) rollbackSend(send *challengeSend) {
	if _, err := c.Storage.RollbackCSPAuthSend(db.CSPSendRollback{
		ChallengeID: send.challengeID,
		Dest:        send.dest,
		ClaimedAt:   send.claimedAt,
		Previous:    send.previous,
		Resend:      send.resend,
	}); err != nil {
		log.Warnw("error rolling back challenge send", "challenge", send.challengeID, "error", err)
	}
	c.releaseChallengeSend(send.daily)
}

// claimDailyChallengeSend claims one unit of the member's daily challenge-send budget for the
// anchor process, counting new tokens and resends together. The counter is Mongo-backed, so it
// survives service restarts. Fails closed on storage errors.
func (c *CSP) claimDailyChallengeSend(uID, anchorID internal.HexBytes) (db.CSPCounterClaim, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterChallengeSends,
		counterKey(uID, anchorID), c.maxDailyChallengeSends)
	if err != nil {
		log.Warnw("error claiming daily challenge send",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	if !res.Granted {
		return nil, counterLimitError(res, "daily challenge limit reached, try again tomorrow")
	}
	return res.Claim, nil
}

// releaseChallengeSend gives back a claimDailyChallengeSend unit for a send that delivered
// nothing. A nil claim is a no-op. Storage errors are logged: at worst the member loses one unit
// of today's budget.
func (c *CSP) releaseChallengeSend(claim db.CSPCounterClaim) {
	if err := c.Storage.ReleaseCSPCounter(claim); err != nil {
		log.Warnw("error releasing daily challenge send", "error", err)
	}
}
```

In `VerifyAuthToken` (:584-679), make these changes:
- Delete the `challenge, err := c.challengeOf(authTokenData)` block (:599-605).
- Replace `challenge.Secret` with `authTokenData.Secret` (:613 and :657).
- Replace `challenge.CreatedAt` with `authTokenData.CreatedAt` (:621).
- Replace `c.Storage.IncrementCSPAuthAttempts(challenge.Token, MaxChallengeAttempts)` with `c.Storage.IncrementCSPAuthAttempts(token, MaxChallengeAttempts)`, and in its log replace `"challenge", challenge.Token` with `"challenge", authTokenData.ID`.
- Right after the `IncrementCSPAuthAttempts` call, before the generic `if err != nil`, add:

```go
	if errors.Is(err, db.ErrChallengeConsumed) {
		// a concurrent verification solved the code first
		return ErrTokenExpired
	}
```

- Reword the comment above the secret check (:606-612). It should no longer mention legacy rows or separate rows. New text: "reject tokens whose code has no secret: auth-only grants, and codes already solved through this token or another one. An OTP derived from an empty secret would be trivially guessable. ErrTokenExpired prompts the client to restart the OTP flow rather than treating it as a hard failure."
- Reword the comment above the attempt claim: "claim one attempt on the code before comparing it: the conditional increment only succeeds while attempts are below the cap, so concurrent requests cannot all evaluate a guess against the same stale count. Attempts are counted per code, so issuing more tokens for it grants no more."

Replace `createAuthOnlyToken` (:709-748) with:

```go
// createAuthOnlyToken creates a pre-verified token for auth-only censuses, which need no challenge.
func (c *CSP) createAuthOnlyToken(anchorID, uID internal.HexBytes) (internal.HexBytes, error) {
	token := newAuthToken()
	if err := c.Storage.SetVerifiedCSPAuth(token, uID, anchorID); err != nil {
		log.Warnw("error setting new auth-only token",
			"userID", uID,
			"anchorID", anchorID,
			"error", err)
		return nil, ErrStorageFailure
	}
	log.Debugw("new auth-only token created and verified",
		"userID", uID,
		"anchorID", anchorID,
		"token", token)
	return token, nil
}
```

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestDestinationKey|TestChallenge|TestMigratedLTSChallenges|TestAuthToken|TestVerifyAuthToken|TestResendChallenge|TestDailyChallenge|TestSign|TestNotification" ./csp/'`
Expected: `ok  github.com/vocdoni/saas-backend/csp`. `csp/handlers` and `api` do not build until Task 3.

Step 3: commit
`git commit -m "fix(csp): claim, roll back and settle otp sends on the code document" -- csp/auth.go csp/csp.go csp/notifications/challenge.go csp/auth_test.go csp/daily_limits_test.go csp/challenge_document_test.go`

Interfaces this task provides to later tasks:
- `csp.DestinationKey(ctype notifications.ChallengeType, to string) string`.
- `(*CSP).ClaimChallengeResend(token, to, ctype) (*ResendClaim, error)`.
- `(*CSP).ResendChallenge(claim *ResendClaim, lang string, org notifications.OrganizationInfo) error`.
- `(*CSP).ResendCoolDown(token, dest string) (time.Duration, bool)`.
- `(*CSP).ChallengeSendRefusal(uID, anchorID) error` and `(*CSP).AuthToken(...)` keep their signatures.

### Task 3: handlers name the destination; api tests; swagger

Model: standard   Repo: /home/user/src/vd/saas-backend   Files: csp/handlers/auth.go, csp/handlers/processes.go, csp/handlers/cert_auth.go, docs/swagger.yaml, api/processes_csp_test.go, api/processes_blind_csp_test.go, api/cert_auth_step0_test.go
Goal: step 0, cert-auth step 0 and `auth/resend` pass the destination to the cooldown hint and the claim, keeping the contact-probing bound. Adds the api tests (AC6, AC10 api part).
Patch: .delivery/2026-11-25-csp-otp-challenge-document/patches/task-3.patch

Step 1: write the failing test

**api/processes_csp_test.go**
- In `TestProcessCSP`, replace lines 188-191 with the block below. The rest of the test stays: after the sleep, the wrong contact spends resend 1, the correct one resend 2, the other wrong one resend 3, and the last correct one answers 40017.

```go
	// inside the cooldown, a resend to the destination the code was sent to answers 40103 and
	// spends no resend
```

- Keep the `ErrAttemptCoolDownTime` assertion for `members[0].Email` that follows.
- Add:

```go
// TestProcessCSPSecondTokenSigns checks that of two tokens sharing one code, the one the voter
// solved the code with signs even though it was not the first issued, and the other is refused.
func TestProcessCSPSecondTokenSigns(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)

	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Questions = req.Questions[:1]
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID

	authReq := &handlers.AuthRequest{Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email}
	first := postProcessAuth0(t, pid, authReq)
	// a repeated step 0 returns a second token for the same pending code
	second := postProcessAuth0(t, pid, authReq)
	c.Assert(second, qt.Not(qt.DeepEquals), first)
	verifyProcessCSP(t, pid, members[0].Email, second)

	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	// the token the code was not solved with cannot sign
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.SignRequest{AuthToken: first, ProcessID: election, Payload: hex.EncodeToString(voter.Address().Bytes())},
		"processes", pid, "sign")
	testCSPSign(t, pid, second, election, voter.Address().Bytes())
}
```

**api/processes_blind_csp_test.go** — add:

```go
// TestProcessBlindCSPSecondTokenSigns checks the blind flow with two tokens sharing one code: the
// token the voter solved the code with blind-signs, the other is refused.
func TestProcessBlindCSPSecondTokenSigns(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)

	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	req.Census.Anonymous = true
	req.Questions = req.Questions[:1]
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint,
	)
	pid := created.ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID

	authReq := &handlers.AuthRequest{Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email}
	first := postProcessAuth0(t, pid, authReq)
	second := postProcessAuth0(t, pid, authReq)
	c.Assert(second, qt.Not(qt.DeepEquals), first)
	verifyProcessCSP(t, pid, members[0].Email, second)

	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.BlindPointRequest{AuthToken: first, Elections: []internal.HexBytes{election}},
		"processes", pid, "blind-point")
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	c.Assert(testBlindSignBallot(t, pid, second, election, voter.Address().Bytes()), qt.Not(qt.IsNil))
}
```

**api/cert_auth_step0_test.go** — append this subtest to `TestCertAuthStep0RetryLaterKeepsCode`, after "2FA within cooldown…". Add imports as needed: `time`, `github.com/vocdoni/saas-backend/csp/notifications` under the alias `cspnotifications` if `notifications` is taken, and `go.mongodb.org/mongo-driver/v2/bson` (already imported for `unused`).

```go
	t.Run("2FA cooldown on a new code: 40103, and the code stays unused", func(t *testing.T) {
		c := qt.New(t)
		login := func() *handlers.AuthRequest {
			verifier, challenge := testCertPKCE(t)
			return &handlers.AuthRequest{
				CertCode: newCertCode(t, withEmail, testDNI(41), challenge), CodeVerifier: verifier, Email: members[0].Email,
			}
		}
		tok := postProcessAuth0(t, withEmail, login())
		// lock the pending code so step 0 needs a new one, and date its last send far enough ahead
		// that the 1 s test cooldown cannot run out during the request
		dest := csp.DestinationKey(cspnotifications.EmailChallenge, members[0].Email)
		_, err := testDB.DBClient.Database(testDBName).Collection("cspTokens").UpdateOne(context.Background(),
			bson.M{"tokens.id": tok},
			bson.M{"$set": bson.M{"attempts": csp.MaxChallengeAttempts, "sends." + dest: time.Now().Add(time.Hour)}})
		c.Assert(err, qt.IsNil)
		req := login()
		apiErr := postProcessAuth0AndExpectError(t, withEmail, req)
		c.Assert(apiErr.Code, qt.Equals, errors.ErrAttemptCoolDownTime.Code)
		unused(c, req.CertCode)
	})
```

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestProcessCSP$|TestProcessCSPSecondTokenSigns|TestProcessBlindCSPSecondTokenSigns|TestCertAuthStep0RetryLaterKeepsCode" ./api/'`
Expected: build failure in `csp/handlers` (`ClaimChallengeResend`/`ResendChallenge`/`ResendCoolDown` argument mismatch).

Step 2: implement

**csp/handlers/auth.go**
- `handleAuthStep` (:57-59): `resp.CoolDownTime = c.resendCoolDown(res.token, res.dest)`.
- Replace `resendCoolDown` (:63-72) with:

```go
// resendCoolDown is AuthResponse.CoolDownTime for the token: the milliseconds until its code can be
// re-sent to the destination dest (csp.DestinationKey), or nil when there is no destination or no
// code to resend.
func (c *CSPHandlers) resendCoolDown(token internal.HexBytes, dest string) *int64 {
	if dest == "" {
		return nil
	}
	remaining, ok := c.csp.ResendCoolDown(token, dest)
	if !ok {
		return nil
	}
	ms := remaining.Milliseconds()
	return &ms
}
```

- `authFirstStep` (:327-328): replace the two last lines with `return c.typedAuthFirstStep(in)`.
- `firstStepResult` (:341-345):

```go
// firstStepResult is what step 0 answers: the token, the voterRef of a certificate census, and the
// destination key the code was sent to (empty for an auth-only census), for the cooldown hint.
type firstStepResult struct {
	token    internal.HexBytes
	voterRef string
	dest     string
}
```

- `typedAuthFirstStep` (:348):
  - The signature becomes `func (c *CSPHandlers) typedAuthFirstStep(in firstStepRequest) (res firstStepResult, err error)`.
  - Every `return nil, ` inside it (13 sites, :348-488) becomes `return firstStepResult{}, `.
  - Replace the final `return c.csp.AuthToken(...)` (:477-487) with:

```go
	// Generate the token
	token, err := c.csp.AuthToken(
		anchorID,
		uID,
		toDestinations,
		challengeType,
		lang,
		name,
		logo,
		org.Address,
	)
	if err != nil {
		return firstStepResult{}, err
	}
	res = firstStepResult{token: token}
	if toDestinations != "" {
		res.dest = csp.DestinationKey(challengeType, toDestinations)
	}
	return res, nil
```

- Add, after `determineContactMethod`:

```go
// submittedContact is the destination a resend request names, before it is checked against the
// member: the destination determineContactMethod answers when the contact matches. A missing
// contact answers like a wrong one; an auth-only census names none.
func submittedContact(census *db.Census, req *AuthResendRequest, phone *voterPhone,
) (string, notifications.ChallengeType, error) {
	email := func() (string, notifications.ChallengeType, error) {
		if req.Email == "" {
			return "", "", errors.ErrCSPAuthFailed
		}
		return req.Email, notifications.EmailChallenge, nil
	}
	sms := func() (string, notifications.ChallengeType, error) {
		if phone == nil {
			return "", "", errors.ErrCSPAuthFailed
		}
		return phone.normalized, notifications.SMSChallenge, nil
	}
	switch census.Type {
	case db.CensusTypeMail:
		return email()
	case db.CensusTypeSMS:
		return sms()
	case db.CensusTypeSMSorMail:
		if req.Email != "" {
			return email()
		}
		if req.Phone != "" {
			return sms()
		}
		return "", "", errors.ErrInvalidUserData.Withf("no valid contact method provided")
	case db.CensusTypeAuthOnly:
		return "", "", nil
	default:
		return "", "", errors.ErrNotSupported.Withf("invalid census type")
	}
}
```

**csp/handlers/cert_auth.go** (:405-410): after `token, err := c.csp.AuthToken(...)` and its error check, build the result with the destination:

```go
	res := firstStepResult{token: token, voterRef: c.csp.VoterRef(processID.Hex(), member.ID.Hex())}
	if toDestinations != "" {
		res.dest = csp.DestinationKey(challengeType, toDestinations)
	}
	return res, nil
```

The `ChallengeSendRefusal(uID, anchorID)` peek at :390-393 stays as is.

**csp/handlers/processes.go**
- `ProcessAuthResendHandler` (:247-272): replace from the comment "spend one of the token's resends…" to the end of the function with:

```go
	// the destination the request names, normalized but not yet matched against the member
	to, challengeType, err := submittedContact(census, &req, phone)
	if err != nil {
		writeResendError(w, err)
		return
	}
	// claim the resend for that destination before checking the contact, so that guessing a
	// member's contact through resends is bounded like the resends themselves: a wrong contact
	// keeps its claim spent
	claim, err := c.csp.ClaimChallengeResend(req.AuthToken, to, challengeType)
	if err != nil {
		writeResendError(w, err)
		return
	}
	if _, _, err := determineContactMethod(
		census, &AuthRequest{Email: req.Email, Phone: req.Phone}, phone, member,
	); err != nil {
		writeResendError(w, err)
		return
	}
	name, logo := orgNameAndLogo(org)
	org := notifications.OrganizationInfo{Address: org.Address, Name: name, Logo: logo}
	if err := c.csp.ResendChallenge(claim, lang, org); err != nil {
		writeResendError(w, err)
		return
	}
	apicommon.HTTPWriteJSON(w, &AuthResponse{
		AuthToken:    req.AuthToken,
		CoolDownTime: c.resendCoolDown(req.AuthToken, csp.DestinationKey(challengeType, to)),
	})
```

  If shadowing `org` trips revive, name the new variable `orgInfo`. `writeResendError` already writes an `errors.Error` as is, and falls back to `ErrUnauthorized`… only for the csp sentinels. That matches the previous `apiErr.Write` / `ErrUnauthorized.WithErr` split, because `determineContactMethod` and `submittedContact` only return `errors.Error` values. Import `github.com/vocdoni/saas-backend/csp/notifications` if processes.go does not import it yet.
- Swag text of `ProcessAuthResendHandler` (:173-179): replace the `@Description` lines from "A code is sent at most once per cooldown" to "spends one of them too." with:

```
//	@Description	A code is sent at most once per cooldown to each destination: inside it the request
//	@Description	fails with 40103, whose data.coolDownTime holds the milliseconds left, and spends no
//	@Description	resend. A code can be sent again at most 3 times, counted across every token sharing
//	@Description	it and including the re-sends of a repeated step 0. A contact that does not match the
//	@Description	member spends one of them too, and starts that contact's cooldown. A send that never
//	@Description	reaches the voter gives back its resend, its cooldown and its daily send.
```

- Swag text of `ProcessAuthHandler` (:113-117): replace the five lines from "While the voter's previous code is still usable" to "whose data.coolDownTime holds the milliseconds left." with:

```
//	@Description	While the voter's previous code is still usable (not verified, expired or locked by
//	@Description	failed attempts), a repeated step 0 returns a new token for that same code instead of
//	@Description	a new code. It re-sends the code to the contact named, unless that contact was sent it
//	@Description	less than the cooldown ago or the code's 3 re-sends are spent; the token is returned
//	@Description	either way. Otherwise a new code is sent, and a request inside the cooldown since the
//	@Description	previous code's last send fails with 40103, whose data.coolDownTime holds the
//	@Description	milliseconds left.
```

- Regenerate the spec: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && PATH=$(go env GOPATH)/bin:$PATH make swagger'`, then copy `docs/swagger.yaml` back from z6. The `swag` PATH gotcha is in AGENTS.md.

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -run "TestProcessCSP$|TestProcessCSPSecondTokenSigns|TestProcessBlindCSPSecondTokenSigns|TestCertAuthStep0|TestLang|TestProcessBlindCSPSwitchStopsAuthChallenges" ./api/ && go test -count=1 ./csp/handlers/'`
Expected: `ok  github.com/vocdoni/saas-backend/api` and `ok  github.com/vocdoni/saas-backend/csp/handlers`.

Step 3: commit
`git commit -m "fix(csp): name the destination in resend claims and cooldown hints" -- csp/handlers/auth.go csp/handlers/processes.go csp/handlers/cert_auth.go docs/swagger.yaml api/processes_csp_test.go api/processes_blind_csp_test.go api/cert_auth_step0_test.go`

Interfaces this task provides to later tasks: none.

### Task 4: shared attempts and resends trade-off

Model: fast   Repo: /home/user/src/vd/saas-backend   Files: csp/csp.go
Goal: the godoc at `MaxChallengeAttempts` names the shared attempts-and-resends trade-off and its bound (AC11).
Patch: .delivery/2026-11-25-csp-otp-challenge-document/patches/task-4.patch

Step 1: write the failing test
None: AC11 is reviewed by eye.

Step 2: implement
Replace csp/csp.go:21-23 with:

```go
// MaxChallengeAttempts is the maximum number of verification attempts one challenge code takes,
// across every authentication token answering it, before it is locked. Attempts, like resends
// (which repeated auth requests also spend), are per code rather than per token, since per-token
// budgets would give 5 fresh guesses with every auth request. So whoever holds a voter's login
// data can lock the voter's pending code or spend its resends, which no per-voter limit prevents
// (they could already spend the daily send cap); #893 mitigates it.
const MaxChallengeAttempts = 5
```

Run: `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go build ./csp/'`
Expected: no output.

Step 3: commit
`git commit -m "docs(csp): explain why otp attempts and resends are shared per code" -- csp/csp.go`

Interfaces this task provides to later tasks: none.

## Merge and release

1. Full suites on z6, once, on the tip of `csp-otp-doc`:
   - `ssh z6.vocdoni.net 'cd ~/scratch/saas-737 && go test -count=1 -failfast -timeout=45m ./...'` → every package `ok`. This covers the `csp`, `db`, `api`, `errors` and `migrations` suites (AC9).
   - `golangci-lint run` (v2.15.0) → no new issues.
   - `./scripts/check-qt-patterns.sh` → clean.
   - `go mod tidy && git diff --exit-code go.mod go.sum` → clean.
   - `make swagger` leaves `docs/swagger.yaml` unchanged from Task 3's commit.
2. Fold into #737's curated history. `fix/csp-otp-reuse` = `b20af158` (reuse a pending code) + `84106f75` (resend wait on success and `Retry-After`). The work commits sit on top of `84106f75`; let T be their tip. No interactive rebase is available, so:
   1. `git checkout -b otp-curated b20af158`
   2. `git diff 84106f75 T -- . ':!docs/plans' ':!docs/specs' > /tmp/otp-redesign.diff` (a scratch file outside the repo), then `git apply -3 /tmp/otp-redesign.diff`.
   3. Resolve the conflicts in the lines `84106f75` introduced (`resendCoolDown`, `ResendCoolDown`, `Retry-After`, `coolDownTime` on success). Keep the redesign and drop the hint, so the result is the reuse fix without the success hint.
   4. Run `git commit --amend` with `b20af158`'s message extended by the trade-off paragraph below. Also state that the code's state lives in one document, that sends roll back, and that sends are per destination.
   5. `git checkout T -- .` (the full final tree), then `git commit -C 84106f75`.
   6. Check: `git diff T HEAD -- . ':!docs/plans' ':!docs/specs'` is empty, and the commit-by-commit build passes on z6 (`git rebase --exec "go build ./..." 39d39504`, non-interactive).

   The commit trade-off paragraph (AC11):
   > Attempts and resends are counted per code, not per token: per-token budgets would hand out 5 fresh guesses with every step 0. Whoever holds a voter's login data can therefore lock the voter's pending code or spend its resends; no per-voter limit prevents it, and the same actor could already spend the voter's daily send cap. #893 (send backoff) mitigates it.
3. **Owner OK required before:**
   - `git push --force-with-lease origin otp-curated:fix/csp-otp-reuse`;
   - updating the #737 PR body;
   - posting anything on GitHub.
4. After the push: CI green (lint, qt check, tidy, tests), a reviewer's approval, then "Rebase and merge" into `main`. Promotion `main → stage → lts` is not ours.
5. PR body additions (keep it short, per the PR rules):
   - **Accepted trade-off** section (AC11):
     > ### Accepted trade-off
     > Attempts (5) and resends (3) are counted per code, not per token: per-token budgets would hand out 5 fresh guesses with every step 0. So whoever holds a voter's login data can lock the voter's pending code, or spend its resends, through tokens of their own. No per-voter limit prevents it, and the same actor could already spend the voter's daily send cap; the voter waits out the cooldown and gets a new code. #893 (send backoff) mitigates it.
   - Deploy note: migration 40 rewrites `cspTokens` in place and adds a unique partial index. A code an old instance issues during the rolling deploy is not found, and that voter repeats step 0. No rollback is planned.
   - Docs/SDK impact, still pending (to suggest to the owner, not to open unasked):
     - vocdoni.io `stage`, the CSP / casting-votes pages:
       - a repeated step 0 reuses the pending code and re-sends it to the contact named;
       - the cooldown is per contact;
       - step-0 re-sends count against the 3 resends;
       - failed sends give back the cooldown and budgets.
     - vocdoni-integrator-sdk: no type or endpoint change; `@vocdoni/api-voting` may show `coolDownTime` per contact.
6. Post-release checks (once on DEV/STG): run step 0 twice, then a resend to the other contact. Check the logs for `error rolling back challenge send` and `error pushing notification challenge`.
