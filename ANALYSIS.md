# Code Review & Analysis — Vocdoni SaaS Backend

**Date:** 2026-07-17
**Scope:** Full repository (`api/`, `db/`, `csp/`, `account/`, `subscriptions/`, `stripe/`,
`notifications/`, `objectstorage/`, `internal/`, `migrations/`, `cmd/`, infra, tests).
**Method:** Six parallel subsystem reviews reading source in full, plus `go build`, `go vet`, and
`golangci-lint` run locally. The highest-severity finding from each subsystem was independently
re-verified by reading the cited code and tracing its call sites.

## How to read this document

Each finding carries a **category** (bug / security / data-integrity / perf / structure / infra),
a **severity**, and a `file:line` anchor. A `(verified)` tag means I re-derived the finding myself
this session by reading the code and its callers; `(reported)` means it comes from a subsystem
review and is well-cited but I did not personally re-trace it. Where a finding's live impact is
softened by another condition, that caveat is stated inline rather than buried.

## Baseline (verified this session)

- `go build ./...` — **passes**.
- `go vet ./...` — **passes**, no diagnostics.
- `golangci-lint run` — **53 issues in 8 files**: 50 × `revive` (almost all `fmt.Println`/`fmt.Printf`
  in library code — see the leftover-debug-print theme), 3 × `staticcheck`. Notably
  `csp/signers/saltedkey/saltedkey.go:64` and `:120` (`esk.Private.D.Add(...)`) trip staticcheck —
  the salted-key code mutates a shared `big.Int` in place; correct today but flagged as aliasing-prone.
- CI (`.github/workflows/main.yml`) runs the full Docker-backed suite with coverage on every PR, but
  **the race detector runs only on `stage`/`release*` branches, never on PRs or `main`** — given the
  concurrency in the notification queue/breaker and the shared test chain, PR-introduced races land
  unchecked. Coverage upload is `continue-on-error`, so coverage regressions never fail CI.

---

## CRITICAL

### C1. Changing your account email permanently bricks the on-chain signing key of every org you created — `(verified)`
`api/users.go:465-509`, `account/signer.go:48-52`, signing sites `api/transaction.go:67,207`,
`api/process.go:736`, `api/process_vote.go:228`, `api/processes_publish.go:268,685`.

Org private keys are derived deterministically as `HashRaw(secret + creatorEmail + nonce)`
(`OrganizationSigner`, signer.go:49). `org.Address` (the on-chain account) is fixed at creation from
that key. `PUT /users/me` with a new email (`updateUserInfoHandler`) calls
`ReplaceCreatorEmail(old, new)`, which rewrites `creator` on **all** orgs the user created — but not
`org.Address`. Every signing path then re-derives the key from the new `org.Creator`, producing a
**different** private key whose address no longer matches `org.Address`. No code validates
`derivedSigner.Address() == org.Address`, so it fails only at chain level: after one email change,
publishing, status changes, vote relaying, and `/transactions` signing silently break for every
affected org, with no recovery except restoring the exact old email string.

**Fix:** decouple org signer identity from the mutable account email — store a stable per-org key
seed (or key id) at creation and derive from that, not from `creator`. As an immediate guard, block
email changes for users who created orgs, or verify `signer.Address() == org.Address` before signing
and refuse otherwise. This is the root symptom of the broader **email-keyed-identity** problem (see
Cross-cutting themes).

---

## HIGH

### H1. `BundleSignHandler` does not bind the auth token to the bundle; census-membership check is commented out — `(verified)` — security
`csp/handlers/handlers.go:377-448`. The three sibling handlers `BundleAuthResendHandler` (`:219`),
`UserWeightHandler` (`:483`), and `BundleCheckHandler` (`:580`) all enforce
`bytes.Equal(*bundleID, auth.BundleID)`. The **signing** endpoint — the one that actually mints
ballot signatures — does not, and the census-participant check is dead/commented (`:436-439`,
referencing a `checkCensusParticipant` method that does not exist anywhere). Consequence: a member of
org O who verifies a token for bundle A (census A) can `POST /process/bundle/B/sign` with that token
and sign a process in bundle B of the same org, **bypassing census-B eligibility**. Server-side
weight computation prevents weight inflation, but cross-census vote authorization within an org is a
genuine integrity hole. **Fix:** add the `bytes.Equal(*bundleID, auth.BundleID)` guard the siblings
have, and restore a real census-participant check.

### H2. `SetOrganization` update path can silently DELETE an existing organization — `(verified)` — data-integrity/bug
`db/organizations.go:104-112`. `SetOrganization` is used for both create and update. On any update,
the fetched org carries `Creator` (an email), so `addOrganizationToUser(org.Creator, ...)` re-runs;
if it returns an error — creator user's email no longer resolves (`CountDocuments == 0 → ErrNotFound`,
`db/users.go:43-44`) or any transient Mongo error — the "compensating rollback" runs
`DeleteOne({_id: org.Address})` and **destroys the whole organization**. Reachable from a routine
org-profile update or a Stripe webhook (`stripe/webhook.go:128`). **Fix:** never delete an existing
document as rollback for a create; scope the compensating delete to the create path only (e.g. only
when the upsert actually inserted), and don't treat a missing/transient creator lookup as fatal.

### H3. `HashSortedFields` / `HashVerificationCode` do not hash — stored "login hashes" embed recoverable plaintext PII — `(verified)` — security
`internal/utils.go:52-55, 114-116`. `sha256.New().Sum(b)` appends the digest of what was written to
the hasher (**nothing**, here) to `b` — it does not hash `b`. So `HashSortedFields` returns
`fmt.Append(nil, sortedData)` (the plaintext member fields: name/surname/nationalID/birthdate/email/
phone) followed by a fixed 32-byte constant (`SHA256("")`). These bytes are stored hex-encoded as
`loginHash`/`loginHashEmail` (`db/types.go`), so anyone with DB read access recovers every member's
identity fields verbatim. The code comment (utils.go:44-51) defends the construction on
back-compat grounds and **explicitly says not to "fix" it** — but never acknowledges it leaks
plaintext. `HashVerificationCode` has the identical defect (currently no callers — dead code).
**Fix:** the on-wire format can't change without a data migration, but it must become a real keyed
hash (HMAC with a server secret) on a versioned migration; at minimum document the exposure and lock
down DB access. Note: `HashPassword` (argon2id) is *correct* — this defect is isolated to the
login-hash lookup keys.

### H4. `publishCensusHandler` stores the wrong key as the census root — `(verified)` — bug
`api/census.go:260-263`. This endpoint sets `census.Published.Root = a.account.PubKey` (the
blockchain account signer), while **every other publish path** uses the CSP signer key
`a.csp.PubKey()` (census.go:380, process_bundles.go:107/155, processes_publish.go:273, process.go:742).
The CSP is what actually signs voter ballots, so the persisted root returned to SDK consumers does
not match the key that authorizes votes against that census. A `// TODO: use a different key` sits on
the offending line. **Fix:** use `a.csp.PubKey()` here to match the other five paths.

### H5. SMTP header injection via un-sanitized `Subject` — `(verified)` — security
`notifications/smtp/smtp.go:148`. `To`/`Cc`/`Reply-To` are validated through `mail.ParseAddress`
(rejects CRLF), but `Subject` is written raw. Subjects interpolate user/org-controlled fields:
support-ticket subject includes request-body `Title`/`Type` (`api/organizations.go:480`, only checked
non-empty); OTP/invite subjects include the org name. A `Title` containing `\r\n` injects arbitrary
headers (e.g. `Bcc:`) into mail sent to the support inbox and to voters. **Fix:** strip CR/LF from
`Subject` (and any header-bound value) and RFC 2047-encode non-ASCII subjects (see M-notifications).

### H6. Organization invitation codes are brute-forceable — short code, public endpoint, no attempt cap — `(reported)` — security
`api/organization_users.go:212-303`, `api/apicommon/const.go:30`. Invite codes are
`RandomHex(VerificationCodeLength)` with `VerificationCodeLength = 3` → 6 hex chars ≈ 2²⁴, valid 5
days, stored in **plaintext**. `POST /organizations/{address}/users/accept` is public and does a
bare global `InvitationByCode(code)` with **no per-invite attempt cap and no per-IP rate limit** —
unlike account-verification/reset codes of the same length, which are capped at 3 attempts. The
lookup isn't org-scoped, giving a birthday advantage across all pending invites platform-wide. A
successful guess grants an org role and, for a not-yet-registered invitee, creates a **verified**
account under the victim's email with the attacker's password. **Fix:** fold invites into the same
attempt-capped, sealed-code path the OTP hardening pass used; lengthen the code; scope lookups to the
org. This is the invitation-flow gap in the otherwise-completed OTP hardening (theme below).

### H7. Bulk-insert error paths nil-deref and crash the process — `(reported)` — bug
`db/org_members.go:245-253` (`InsertMany`) and `db/census_participant.go:726-727` (`BulkWrite`). On a
non-write error (context deadline — the batch ctx is only 20s — or network failure) the MongoDB
driver returns `result == nil`, and the code immediately reads `result.InsertedIDs` /
`results.UpsertedCount` → panic. `org_members` runs this inside a bare goroutine
(`addOrgMemberBatches`), so a slow DB during a CSV import **crashes the whole service**. (Contrast
`processBatch` at census_participant.go:364, which checks `err` first.) **Fix:** check `err` before
dereferencing the result on both paths; don't panic from a detached goroutine.

### H8. Public unauthenticated endpoint leaks Stripe billing email + internal usage counters — `(reported)` — security
`api/organizations.go:207-216` (public group) → `apicommon.OrganizationFromDB` → `types.go:894-901`.
`GET /organizations/{address}` returns the full `OrganizationInfo`, including `Subscription.Email`
(the Stripe customer billing email) and `Counters` (SentSMS/SentEmails/SentVotes/Users/Processes).
Org addresses are public on-chain, so this exposes PII and business metrics to anyone. An
admin-only `/organizations/{address}/subscription` endpoint exists precisely for this data.
**Fix:** strip `Subscription`/`Counters` from the public projection.

---

## MEDIUM

### Auth & identity
- **M1. No session invalidation** — `(reported)` — `api/auth.go:54-69`, `api/users.go:679-760`,
  `jwtExpiration = 360h`. Password change/reset does not revoke outstanding JWTs (stateless, keyed on
  email), and `POST /auth/refresh` lets any valid token mint a fresh 15-day token indefinitely — no
  absolute session lifetime, no token version/nonce in the user record. A stolen session outlives a
  password reset by up to 15 days. **Fix:** add a per-user token version claim bumped on
  password/email change; cap absolute session age.
- **M2. Single hardcoded global password salt** — `(reported)` — `api/api.go:75`
  (`passwordSalt = "vocdoni365"`), `internal/utils.go:102`. Argon2id params are good, but one static
  salt means identical passwords → identical hashes (bulk cracking, reuse detection across accounts).
  **Fix:** per-user random salt stored alongside the hash.
- **M3. API keys are not bound to their org** — `(reported)` — `api/middlewares.go:107-123`. Only the
  three `/integrator` endpoints consult `key.OrgAddress`; every other endpoint authorizes via the
  creator's `HasRoleFor(org.Address, …)`, so a key minted for integrator org A can act on **any** org
  where the creating admin holds a role. Broader than the documented "managed organizations" scope.
- **M4. Unverified-account email flooding** — `(reported)` — `api/users.go:356-376, 602-624`. The
  verified-user recovery branch enforces `otpCooldown`, but the **unverified** recovery branch and the
  post-expiry resend branch reissue a code on every call with no cooldown and no cap (each reissue
  also resets the attempt counter). CSP's `ResendChallenge` (`csp/auth.go:103-196`) has the same
  missing-cooldown issue for SMS/email. **Fix:** apply the existing cooldown to all code-issuing paths.

### Data integrity & concurrency (single-instance assumptions)
A recurring theme: check-then-act flows are guarded only by the in-process `keysLock` mutex, so they
are unsafe across replicas, and `keysLock` is applied inconsistently (many reads take it, many writes
don't).
- **M5. `dynamicUpdateDocument` zero-value drop is a systemic footgun** — `(reported)` —
  `db/helpers.go:57-90`. Fields equal to their zero value are dropped from `$set`, so `SetCensus` can
  never write `Size: 0`, `SetOrgMember` can't clear email/phone, `SetUser` can't set `Verified:false`
  or persist `TokensRemaining: 0`. Only `active`/`weight` are special-cased. Also `SetOrganization`'s
  full-snapshot `$set` clobbers concurrently `$inc`-ed `counters`/`subscription` subdocs with a stale
  snapshot (`db/organizations.go:90-96`), silently regressing quota/billing counters even on one
  instance. **Fix:** switch these updates to field-scoped `$set`/`$inc` and stop round-tripping whole
  subdocuments.
- **M6. `SetUser` can wipe org memberships** — `(reported)` — `db/users.go:134-151`. A non-nil empty
  `organizations` slice is not the reflect zero value, so it lands in `$set` and erases all roles for
  any caller that built the user without loading its orgs.
- **M7. Quota counters are check-then-`$inc` under in-process lock only** — `(reported)` —
  `db/organizations.go:359-468` (`IncrementOrganizationUsersCounter`, `ReserveManagedPublish`, …) and
  `db/csp.go:220-277` (`ConsumeCSPProcess`, also flagged as csp M3). Multi-replica deployments can
  exceed caps / grant one extra vote overwrite. The correct pattern already exists in the codebase
  (`IncrementCSPAuthAttempts`, `ClaimProcessForPublish` use conditional single-op updates) and should
  be copied. **Fix:** conditional atomic updates (`counters.X: {$lt: limit}` + `$inc`).
- **M8. Lost-update races on full-array `$set`** — `(reported)` — `db/process_bundles.go:278-317`
  (`AddProcessesToBundle` reads before locking, `$set`s whole array → use `$addToSet`),
  `db/org_members_groups.go:129-225`.
- **M9. Non-atomic multi-step flows without transactions** — `(reported)` —
  `db/census.go:66-135` (`PopulateGroupCensus` upserts census *last*, leaving dangling group refs on
  mid-way failure), `db/org_members.go:566-607` (delete-then-patch-groups).
- **M10. Migration runner has no lock and non-atomic apply/record** — `(reported)` —
  `db/migrations.go:22-66`. Multiple replicas starting together all run `RunMigrationsUp` (no
  lease/lock doc), and a crash between `Up()` and the record `InsertOne` re-runs the migration.
  Correctness rests on every migration being idempotent by convention.

### Stripe / billing
- **M11. Subscription webhook returns an error on legitimate customer metadata** — `(verified,
  live-impact caveated)` — `stripe/webhook.go:133-136`. The check is inverted: it errors whenever the
  customer *already has* an `address` in metadata (the normal case after the first event), so the
  event is never marked processed and Stripe retries forever. **Caveat:** likely masked today because
  webhook payloads don't expand the `customer` object, leaving `Metadata` nil (see M12) — but the
  logic is wrong as written and will fire the moment the customer is expanded. **Fix:** only error
  when a non-empty existing address *differs* from `OrgAddress`.
- **M12. Subscription `Customer` fields unreliable / nil-deref risk** — `(reported)` —
  `stripe/webhook.go:125,134,303`. Event payloads don't expand `customer`, so `Customer.Email` is
  `""` (subscription email silently lost) and `Customer.Metadata` is nil; a null `customer` panics at
  line 125. **Fix:** fetch/expand the customer, or guard for nil.
- **M13. In-memory, unbounded, per-instance webhook idempotency store** — `(reported)` —
  `stripe/service.go:22`, `stripe/webhook.go:44-55`. `processedEvents sync.Map` is never evicted
  (grows for process lifetime) and is process-local, so with >1 replica the same event is processed
  twice. Check-then-store is also non-atomic. **Fix:** persisted/TTL'd dedup keyed on event ID.
- **M14. Stripe degraded-mode nil-deref** — `(reported)` — `api/stripe_handlers.go:157-176, 190-222`.
  If `InitializeStripeService` fails at boot, routes are still registered; `HandleWebhook` and
  `CreateSubscriptionCheckout` guard `h == nil`, but `GetCheckoutSession` and
  `CreateSubscriptionPortalSession` do not → panic on every call.

### Object storage & notifications
- **M15. Unbounded upload allocation + short read** — `(reported)` —
  `objectstorage/objectstorage.go:166-172`. `make([]byte, size)` uses the attacker-controlled
  multipart `Size` header (memory-exhaustion vector) and a single `data.Read(buff)` can short-read.
  The handler sets only a 32 MB in-memory parse threshold, no total-request cap. **Fix:**
  `http.MaxBytesReader` + `io.ReadFull`/`io.LimitReader`.
- **M16. Missing `nosniff` on public inline object download** — `(reported)` —
  `objectstorage/handlers.go:159-162`. Public route serves user content `inline` with no
  `X-Content-Type-Options: nosniff`. **Fix:** add the header (cheap defense against polyglot/MIME-sniff).
- **M17. Non-ASCII email subject / Content-Transfer-Encoding bugs** — `(reported)` —
  `notifications/smtp/smtp.go:148,162-173`. Localized subjects (`Código…`, `Invitación…`) are emitted
  as raw UTF-8 in an ASCII-only header, and both MIME parts declare `7bit` while bodies are 8-bit
  UTF-8. Strict relays may reject/mangle. **Fix:** RFC 2047 encoded-words for subject; quoted-printable
  or `8bit` for bodies.
- **M18. Phone normalization drops leading zeros** — `(reported)` — `internal/utils.go:74`. Building
  E.164 as `fmt.Sprintf("+%d%d", cc, GetNationalNumber())` loses leading zeros in national numbers
  that retain them, diverging from canonical E.164 and causing 2FA lookup mismatches. **Fix:**
  `phonenumbers.Format(pn, phonenumbers.E164)`.

### API handler bugs
- **M19. Missing `return` after pagination-param error** — `(verified)` — `api/org_members.go:120-123`.
  On `?page=abc`, the handler writes a 400 then **continues** with zero-value params, runs a DB query
  with limit 0, and `calculatePagination` writes a second/third response body. Every other paginated
  handler returns here. **Fix:** add the `return`.
- **M20. `createProcessHandler` rejects the documented census-only creation** — `(reported)` —
  `api/process.go:65`. `HexBytes.Equals(nil)` is true for an empty address, so a request carrying only
  `censusId` is rejected by the early guard even though the code below (and the godoc) supports
  resolving the org from the census. **Fix:** correct the guard so the census-only branch is reachable.
- **M21. Unauthenticated public fan-out to N chain / N DB calls** — `(reported)` —
  `api/processes.go:566-599` (`GET /processes/{id}/results` → one Vochain `Election()` per question,
  up to 100, no cache, no auth), `csp/handlers/processes.go:293-313` (`POST /processes/{id}/check`
  → N+1 Mongo). An unauthenticated caller can force ~100 chain round-trips per request. **Fix:**
  cache results / batch the lookups / bound the fan-out.
- **M22. User-controlled `$regex` in member search** — `(reported)` — `db/org_members.go:509-517`,
  fed verbatim from the query string (`api/org_members.go:115`). Unescaped, applied to six fields —
  malformed patterns → 500s, catastrophic patterns → ReDoS. **Fix:** `regexp.QuoteMeta` or an
  anchored text index.

---

## LOW (condensed)

**Correctness / robustness**
- `db/organizations.go:104` creator path adds duplicate org entries when the same org is re-added with
  a different role (`$addToSet` on the whole `{_id, role}` doc); `RoleFor` then returns whichever comes
  first (`db/users.go:47-54`).
- `migrations/0002_initial_indexes.go:117` indexes `hashedPhone`, a field that doesn't exist (bson tag
  is `phone`, `db/types.go:257`) — dead index; phone lookups fall back to a broad scan. 0004 fixed the
  analogous `nationalID` typo but not this one.
- `db/verifications.go:52` stores one verification doc per user (`_id = userID`, `ReplaceOne`), so a
  password-reset code destroys a pending account-verification code and vice versa; no TTL index on
  `verifications.expiration` (invites have one), so expired codes accumulate.
- `buildLoginResponse` discards the token-encode error (`api/helpers.go:72`) → 200 with empty token.
- Dev-mode (no SMTP) registration 500s because `SealToken("", …)` errors on an empty code
  (`api/helpers.go:121-141` vs `internal/utils.go:134`), contradicting the mock-verify handling.
- Weaker password policy on invite accept (non-empty only, no 8-char min — `api/organization_users.go:260`).
- Email change to an existing email returns 500 not 409 (`db/users.go:148` doesn't map dup-key), and
  orphans API keys keyed on the old email.
- `PUT /organizations/{address}` flips an active org to inactive when `active` is omitted (`bool`
  zero-value; needs `*bool`) — `api/organizations.go:285`.
- `SetProcess` returns `NilObjectID` on the update path and the handler echoes it to the client
  (`db/process.go:65`, `api/process.go:120`).
- Emails not normalized in the accounts API (`Foo@x` ≠ `foo@x`; `NormalizeEmail` exists but only CSP
  uses it).
- `account/process.go:129` single-publish nonce race; `account/tx_funder.go:77-122` funds NewProcess
  without validating the payload entity against `targetAddr` (defense-in-depth gap).
- Signing lock leaks on post-lock error paths in `csp/sign.go:26-27,79` (deferred `unlock(nil, …)`
  hashes a different key than the lock) — latent, currently unreachable via HTTP.

**Security (low)**
- CORS `AllowedOrigins: ["*"]` with `AllowCredentials: true` (`api/api.go:242`) — inert with Bearer
  auth, a footgun if cookies are ever added.
- Inconsistent user-enumeration posture: register/verify/reset leak account existence while recovery
  deliberately doesn't (`api/users.go:71,222,694`); login skips argon2 for nonexistent users (timing).
- `db` structs expose password/hash fields via json tags (`OrgMember.HashedPass json:"pass"`,
  `User.Password json:"password"`) — safe only because `apicommon` converts; any handler returning a
  db struct verbatim leaks argon2 hashes.
- `ConsumedAddressHandler` lacks bundle binding (`csp/handlers/handlers.go:658`) — exposes only the
  token's own user's data, so minimal impact.
- CSP overwrite budget is a hardcoded `MaxVoteOverwritesPerProcess = 10` (`db/const.go:27`) ignoring
  the election's configured `MaxVoteOverwrites` — signing-oracle looseness, bounded on-chain.

**Infra**
- `Dockerfile` runs as root (no `USER`), `main.go` binds `0.0.0.0`. Add a non-root user.
- `.golangci.yml` disables strong `revive` rules (`exported`, `use-errors-new`, `add-constant`,
  complexity/length) "until fixing issues" — re-enable incrementally; `use-errors-new` being off is
  why some of these issues aren't caught.
- `stripe/locks.go:28` panics on an unexpected type in the payment path (programmer-error only).

---

## Cross-cutting themes

1. **Email-keyed identity is the deepest structural risk.** The user email is simultaneously the JWT
   subject, the org signer-derivation input (C1), and the API-key ownership key (M3). This one design
   choice produces the critical signer-bricking bug and a cluster of email-change fragilities
   (orphaned API keys, 500-on-collision, session non-revocation). Introducing a stable internal user
   id / org key id and deriving everything from that would retire an entire class of bugs.

2. **Single-instance assumptions throughout the storage layer.** Quota counters, publish reservation,
   bundle updates, CSP consumption, and the migration runner all rely on the in-process `keysLock`
   mutex and read-modify-write, which is unsafe across replicas — and `keysLock` is applied
   inconsistently. The codebase already contains the correct atomic pattern
   (`ClaimProcessForPublish`, `IncrementCSPAuthAttempts`); the fix is to propagate it. If the service
   is ever run with >1 replica, these become live data-integrity bugs.

3. **The OTP-hardening pass didn't reach the invitation flow.** Recent commits capped
   verification/reset code guesses, but invites (H6) kept the short plaintext code, public endpoint,
   and no attempt cap — and two email-reissue paths (M4) still lack the cooldown their siblings have.

4. **Public-surface data hygiene.** `GET /organizations/{address}` (H8) over-exposes billing email
   and usage counters; several publish/results endpoints (M21) allow unauthenticated fan-out to the
   chain. The authenticated equivalents already exist; the public projections just need trimming.

5. **Leftover debug prints in library code.** `fmt.Println`/`fmt.Printf` swallow errors in hot paths:
   `csp/sign.go:120` (storage error in the sign path), `db/organizations.go:323`
   (`UpdateOrganizationMeta`), `db/process_bundles.go:126,158,190,236`,
   `migrations/0009_*.go`. These are the bulk of the 50 revive lint issues and should be `log.*`.

---

## Test coverage

Genuinely strong integration suite by Go-backend standards (~250 test functions, real
Mongo/MailHog/in-process Voconed, negative-path coverage including the OTP brute-force lockout
regression). `check-qt-patterns.sh` passes clean and runs in CI. The material gaps cluster in one
place — **the billing surface** — plus migration data-correctness:

**Highest-risk untested areas (ranked):**
1. **Stripe checkout/portal endpoints — zero coverage** (`api/stripe_handlers.go:93,157,188`). Money
   entry points; permission/plan/error paths unverified. `stripe/client.go` has no mock seam.
2. **Webhook signature verification bypassed by tests** — `stripe_webhook_test.go` calls
   `HandleEvent` directly, skipping `ConstructEvent`. A regression accepting forged webhooks (which
   can change an org's plan) would not be caught. Product-update sync subtests are commented out
   (`:267-304`, `// TODO: needs refactoring`).
3. **Migrations only smoke-tested on near-empty DBs.** `0008` (unique login-hash indexes, **no dedupe
   step** — will hard-fail on any census with duplicate hashes), `0014` (rewrites every org's plan id,
   drops plan docs, env-var-dependent product id that falls back to a **sandbox** id), and `0011`
   (auto-group backfill) have no test against realistic data.
4. **Object-storage HTTP handlers** (upload/download) — no HTTP test (multipart, size limits, auth).
5. **Legacy `BundleAuthResendHandler`**, **publish-lock steal/expiry semantics**,
   **`account/price.go` (election pricing → billing)**, and **CSP `validateAuthRequest`**
   (`// TODO: Add correct validations`) are untested.

**Quality issues:** 32 `time.Sleep`-based synchronizations (flakiness/slowness; some sleep real
cooldowns); **207 subtests discard their `*testing.T`** (`t.Run("…", func(_ *testing.T)`) so failures
mis-attribute and `-run Test/Sub` is meaningless; ~90 call sites assert status code only, discarding
the body; `testRequest` nil-derefs on connection error instead of failing cleanly; no `t.Parallel()`
anywhere; race detector not run on PRs (baseline above).

---

## Prioritized recommendations

**Fix now (correctness / security / data-loss):**
1. C1 — sever org signer derivation from the mutable email (or block email change for org creators).
2. H1 — add the bundle-binding check to `BundleSignHandler`.
3. H2 — remove the destructive compensating delete from the `SetOrganization` update path.
4. H4 — use `a.csp.PubKey()` in `publishCensusHandler`.
5. H5 — strip CRLF from the SMTP `Subject`.
6. H7 — nil-check bulk-insert results before deref; don't panic from detached goroutines.
7. M19 — add the missing `return` in `organizationMembersHandler`.

**Fix soon (security hardening / integrity):**
8. H3 — plan a migration to real keyed login hashes; document the current exposure meanwhile.
9. H6 + M4 — bring invites and all code-reissue paths under the attempt-cap + cooldown scheme.
10. H8 — trim `Subscription`/`Counters` from the public org projection.
11. M5–M10 — move counters/publish/consume/bundle updates and the migration runner to atomic ops
    before any multi-replica deployment; test `0008`/`0014` against dirty data.
12. M11–M14 — fix the inverted webhook check, expand the customer object, persist idempotency, and
    add the two missing Stripe nil guards; add HTTP-level tests for checkout/portal/webhook signature.

**Structural / hygiene (retire bug classes):**
13. Introduce a stable internal user/org id and a `requireOrgRole()` middleware — removes ~300 lines
    of copy-pasted role-check boilerplate and the sibling-endpoint drift that produced H4, M20, and
    the `Active` zero-value flip.
14. Replace the leftover `fmt.Print*` calls with structured logging; re-enable disabled lint rules
    incrementally; run the race detector on PRs.

---

*Findings tagged `(verified)` were re-derived from source this session (C1, H1–H5, M11, M19 and the
build/lint baseline). Items tagged `(reported)` come from full-file subsystem reviews with cited
`file:line` anchors but were not personally re-traced; they are high-confidence but worth confirming
before large refactors. The single most consequential claim — C1 — was verified end-to-end through
the derivation formula, the email-change handler, and all seven signing call sites.*
