# Certificate voter authentication: design

Status: draft   Class: architectural
Owner decisions so far (2026-11-11):
- D1 Architecture: a separate, stateless **certificate gateway** service terminates TLS and checks
  the client certificate with `github.com/vocdoni/cert-check`. It hands the proven identity to
  saas-backend server to server. saas-backend keeps its App Platform hosting.
- D2 The gateway lives in a **new dedicated repo** (`vocdoni/cert-gateway`).
- D3 Census model: **certificate plus optional 2FA**. The certificate proves the DNI/NIE. The
  census may still require an email or SMS OTP afterwards, through the existing challenge flow.
- D4 Accepted certificates at launch: **stable natural-person providers only**. These are FNMT,
  idCAT, ACCV, DIGITEL, Bewor and Sectigo. No DNIe, no representative or public-employee
  certificates, and no beta providers.
- D5 TLS: the gateway offers **TLS 1.3 and 1.2** (`certcheck.WithTLS13()`).

## Problem

Organisers can already make the member `nationalId` a voter login field
(`db/types.go:478-488`, `OrgMemberAuthFieldsNationalID`). The voter proves nothing, though: they
type the value into step 0 (`csp/handlers/auth.go:270`). A census whose only login field is
`nationalId` and has no 2FA is census type `auth` (`db/types.go:529-538`). Step 0 then mints an
already-verified token (`csp/auth.go:36-39`, `createAuthOnlyToken` `csp/auth.go:466`). Anyone who
knows or guesses a member's DNI can therefore vote as that member. Spanish organisations
(associations, cooperatives, political parties) want voters to prove their identity with the
official personal certificates they already hold.

saas-backend cannot ask for a client certificate where it runs today:
- It serves plain HTTP (`api/api.go:388-396`). TLS ends at the edge. DigitalOcean App Platform
  terminates TLS and offers no client-certificate, TLS-passthrough or TCP ingress.
- The `vocdoni.io` / `vocdoni.net` zones are proxied by Cloudflare. Cloudflare checks client
  certificates from your own CAs only on Enterprise, limits them to 5 CAs per account and does not
  check their revocation.

## Goals

1. An organiser can create a process whose census authenticates voters with a Spanish personal
   certificate. The certificate's DNI/NIE must match the member's `nationalId`. The census may add
   email or SMS 2FA.
2. A voter in such a census picks a certificate in the browser and, when the census uses 2FA,
   completes the OTP. They then vote through the existing sign and blind-sign flow, unchanged.
3. Typing a DNI never authenticates a voter in such a census.
4. Every certificate is verified fail-closed by cert-check: pinned anchors, a client
   authentication purpose, no test certificates and a revocation status of `good`.
5. `nationalId` matching is insensitive to formatting: case, spaces, dots, hyphens and missing
   leading zeros. This holds for typed logins and for certificate logins.
6. No DNI, name or certificate data appears in any log, URL or browser storage.

## Non-goals

- DNIe, representative, public-employee or beta providers. They can be added later through
  gateway configuration (D4).
- Matching on any certificate attribute other than the document number, such as name or birth
  date.
- Moving saas-backend off App Platform, or Cloudflare Enterprise mTLS.
- Certificate login for organiser (admin) accounts.
- Changes to saas-admin-dashboard (the backoffice). Showing the auth method there is a follow-up.
- Changes to `@vocdoni/react-providers`, `@vocdoni/api-client` or `@vocdoni/api-types`. The app
  passes the new step-0 fields through `auth0(participant)` as they are (see Current state).
- Alerting and monitoring dashboards beyond the logs and metrics named here.

## Current state

- **Census model** (`db/types.go:331-347`): `Type`, `Weighted`, `Anonymous`, `GroupID`,
  `AuthFields`, `TwoFaFields`. The census type comes from `TwoFaFields` (`GetCensusType`,
  `db/types.go:529-538`). The API input is `apicommon.CensusSpec` (`api/apicommon/voting_processes.go:20-45`).
  The census is built in `resolveOrCreateDefaultCensus` (`api/processes_census.go:37`). Census
  validation and the missing-data report are in `api/processes_census_validate.go:60-90`.
- **Login hash.** `db.HashAuthTwoFaFields` (`db/types.go:564-587`) lowercases each auth-field
  value (`memberAuthFieldValue`, `db/census_missing_data.go:64-80`) and hashes the name→value map
  with unsalted SHA-256 (`internal.HashLoginFields`, `internal/utils.go:57`). Each participant
  stores `loginHash`, plus `loginHashEmail` and `loginHashPhone` when both 2FA fields are set
  (`calculateParticipantHashes`, `db/census_participant.go:860-875`). Each has a unique index per
  census. `migrations.hashMemberFields` (`migrations/login_hash.go:46`) must mirror the hash byte
  for byte; `db.TestRepairMatchesCanonicalHash` guards this.
- **Normalisation.** `OrgMember.Normalized` (`db/types.go:444-453`) only trims `NationalID`.
- **Voter flow.** The routes are at `api/routes.go:202-216` and the handlers in
  `csp/handlers/processes.go`.
  - Step 0, `authFirstStep` (`csp/handlers/auth.go:270-439`):
    1. Decode `AuthRequest` (`csp/handlers/types.go:16-28`).
    2. Refuse an anonymous census when anonymous voting is off.
    3. Build and normalise the input member.
    4. Check for missing login data.
    5. Claim the failed-attempt budget (`authAttemptKeys` `:193`, `ClaimAuthAttempt`).
    6. Look up the participant by login hash.
    7. Check the password.
    8. Run `determineContactMethod` (`:153-191`), which compares the submitted email or phone with
       the stored member.
    9. Check the weight.
    10. Call `csp.AuthToken`, which queues an OTP or mints a pre-verified token.
  - Step 1 (`authSecondStep`, `:447`) verifies the OTP or returns a pre-verified token.
- **Voter UI** (vocdoni-app, `src/components/Process/CSP/`). `Step0.tsx` renders
  `census.authFields`/`twoFaFields`. Calls go through `useElectionAuth().auth0(participant)` from
  `@vocdoni/react-providers` 4.x. It POSTs `participant` unchanged as the step-0 body and treats
  the census as auth-only when `twoFaFields` is empty. Tokens live only in memory. Process pages are
  server-routed (`clientRouting:false`), so a full-page return is safe. One build serves
  app.vocdoni.io, app-stg, app-dev and the custom organisation domains (`CUSTOM_ORGANIZATION_DOMAINS`).
- **Organiser UI.** `src/components/Process/Create/VoterAuthentication/` builds the `CensusSpec` in
  `buildCensusSpec` (`src/components/Process/Create/index.tsx:~476`). The draft round-trip is in
  `draft-mapping.ts:54-78,163`.
- **cert-check** is a Go 1.27 library in a private repo. `Handler.TLSConfig` asks for, but does not
  require, a client certificate. By default it uses TLS 1.2 only, ECDHE+AEAD, no session tickets and
  ALPN `http/1.1`. `Handler.Auth(r)` returns `Result.Identity.Document{Type, Number}`, with `Number`
  canonical: 9 uppercase characters with leading zeros kept. Every failure is a `*certcheck.Error`
  with one of 16 codes and a safe `Diagnostics.LogAttrs()`.

## Design

### Components and flow

```
voter browser                     cert-gateway (Droplet)            saas-backend (App Platform)
 │ 1 click "certificado"           │                                  │
 │──GET /v1/auth?processId&returnUrl&state&codeChallenge&lang──▶      │
 │◀═══ TLS handshake: CertificateRequest(CAs of D4), cert picker ═══▶│
 │                                 │ 2 cert-check Auth (chain, OCSP/CRL, DNI)
 │                                 │──3 POST /cert-auth/assertions (Ed25519-signed)──▶
 │                                 │     {processId, nationalId, codeChallenge, provider…}
 │                                 │◀── 201 {certCode, expiresAt} ─────│ 4 participant lookup,
 │◀─5 303 returnUrl#certCode=…&state=…                                  │   store assertion
 │ 6 POST /processes/{id}/auth/0 {certCode, codeVerifier[, email|phone]} ──────────────▶
 │◀──────── authToken (pre-verified, or pending OTP → existing step 1) ────────────────│
 │ 7 existing sign / blind-sign
```

### C1. cert-gateway (new repo `vocdoni/cert-gateway`)

A single Go 1.27 binary, `cmd/cert-gateway`, with no database and no disk state apart from the
ACME cache.

- **Listeners.**
  - `:443` serves TLS with `handler.TLSConfig(acme.GetCertificate, certcheck.WithTLS13())`. The
    `http.Server` has `Protocols` set to HTTP/1 only.
  - `:80` serves only ACME HTTP-01 challenges (`autocert.Manager.HTTPHandler`) and redirects
    everything else to https.
  - The server certificate comes from Let's Encrypt via `golang.org/x/crypto/acme/autocert`. Its
    cache directory is `CACHE_DIR`. HTTP-01 is required because the cert-check TLS config
    advertises only `http/1.1` and so cannot answer TLS-ALPN-01.
- **Providers.** `fnmt.New`, `idcat.New`, `accv.New`, `digitel.New`, `bewor.New`,
  `sectigo.New`. They share one `certcheck.NewRevocation` with `UserAgent: "vocdoni-cert-gateway/<version>"`.
  `NewHandler` fails at startup on any misconfiguration.
- **`GET /v1/auth`.** Query parameters:
  - `processId`: 64 hex characters.
  - `returnUrl`: an absolute `https` URL whose origin is exactly one of `ALLOWED_RETURN_ORIGINS`,
    with no userinfo and no fragment. At most 2048 bytes.
  - `state`: base64url, 16–64 characters.
  - `codeChallenge`: base64url SHA-256, exactly 43 characters.
  - `lang`: optional, one of `es|ca|en`, default `es`.

  An invalid parameter gets a 400 HTML page, with no redirect, because the return URL cannot be
  trusted. Otherwise:
  1. Run `handler.Auth(r.WithContext(ctx))` with a 40 s deadline.
  2. On `*certcheck.Error`, redirect 303 to `returnUrl#certError=<code>&state=<state>`. Log
     `Diagnostics.LogAttrs()` plus `DiagnosticID`.
  3. On success, POST the assertion to saas-backend (C2.4) with a 10 s deadline.
     - 201: redirect 303 to `returnUrl#certCode=<code>&state=<state>`.
     - A backend error code: redirect 303 to `returnUrl#certError=<backendCode>&state=<state>`.
       `backendCode` is one of `not_in_census`, `zero_weight`, `not_cert_census`,
       `process_not_open`, `rate_limited` or `backend_unavailable`.

  The parameters travel in the **fragment**, so they never reach any server log or `Referer`.
- **`GET /healthz`** returns 200 once the handler is built.
- **Response hardening** on every response:
  - `Cache-Control: no-store`
  - `Referrer-Policy: no-referrer`
  - `X-Frame-Options: DENY`
  - `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`
- **Rate limit.** Per client IP, 20 requests to `/v1/auth` per minute, in a token bucket held in
  process. An exceeded limit gets a 429 page.
- **Request signing to the backend.**
  - Key: an Ed25519 private key from `SIGNING_KEY` (base64 of the 32-byte seed), with id `SIGNING_KEY_ID`.
  - Header: `X-Cert-Gateway-Signature: t=<unix seconds>,k=<keyId>,v1=<base64url(sig)>`.
  - The signature covers `"cert-gateway-v1\n" + t + "\n" + method + "\n" + path + "\n" + hex(sha256(body))`.
- **Configuration** (env):

  | Variable | Meaning |
  |---|---|
  | `DOMAIN` | the gateway's hostname |
  | `ACME_EMAIL` | contact email for Let's Encrypt |
  | `CACHE_DIR` | autocert cache directory |
  | `BACKEND_URL` | saas-backend base URL |
  | `SIGNING_KEY`, `SIGNING_KEY_ID` | request-signing key and its id |
  | `ALLOWED_RETURN_ORIGINS` | comma-separated origins `returnUrl` may use |
  | `RATE_LIMIT_PER_MIN` | default 20 |
  | `LOG_LEVEL` | log verbosity |

- **Logging.** The gateway never logs the document number, names, the certificate, `returnUrl`
  or `state`. It logs the outcome code, provider id, `DiagnosticID` and latency.
- **Pages.** The 400 and 429 HTML pages are static and localised (es, ca, en), with no
  JavaScript.
- **Deployment.**
  - Host: one DigitalOcean Basic Droplet in fra1 (1 GiB, ~$6/month), running the
    `ghcr.io/vocdoni/cert-gateway` image with `--restart=always`.
  - Firewall: inbound 80 and 443 from anywhere, 22 from admin IPs only.
  - Time is synced with NTP.
  - Hostnames are DNS-only (grey-cloud) A records on Cloudflare: `cert.vocdoni.io` (prod) and
    `cert-stg.vocdoni.io` (stg). Dev reuses stg.
- **CI.** Run `go test -race ./...` and golangci-lint. The image is built on tag `v*`, with
  `GOPRIVATE=github.com/vocdoni/cert-check` and a read token for the private cert-check module.

### C2. saas-backend

**C2.1 Census field.**
- `db.Census` and `apicommon.CensusSpec` / `OrganizationCensus` gain
  `CertAuth bool \`json:"certAuth,omitempty" bson:"certAuth,omitempty"\``.
- Validation in `processes_census_validate.go` and at process create and update:
  - `CertAuth` requires `AuthFields` to be exactly `["nationalId"]`. `TwoFaFields` may be any
    allowed subset, including empty. `Weighted` and `Anonymous` are allowed.
  - `CertAuth` is refused with `ErrNotSupported` when the backend has no gateway keys configured
    (C2.4).
- `CertAuth` is echoed on process reads, so the voter UI can see it.
- `CertAuth` is immutable once the process is published, like the other census fields.

**C2.2 Canonical national ID.**
- New `internal.CanonicalNationalID(s string) (canonical string, ok bool)`:
  1. Uppercase the value and remove spaces, `.` and `-`.
  2. If the result matches `^[0-9]{1,8}[A-Z]$` and the check letter is valid, left-pad the digits
     to 8 and return the DNI with `ok=true`.
  3. If it matches `^[XYZ][0-9]{1,7}[A-Z]$` and the check letter is valid, left-pad to 7 digits
     and return the NIE with `ok=true`.
  4. Otherwise return the trimmed input unchanged with `ok=false`.
- `memberAuthFieldValue` returns `CanonicalNationalID(m.NationalID)` for `nationalId`, so the
  hash lowercases the canonical form. The stored and displayed `NationalID` is not rewritten.
- `migrations.hashMemberFields` gets the same change. `TestRepairMatchesCanonicalHash` keeps
  guarding the parity.

**C2.3 Login hash of a cert census.**
- For a census with `CertAuth`, the participant's `loginHash` is
  `HashAuthTwoFaFields(member, ["nationalId"], nil)`. No 2FA field enters the hash, and no
  `loginHashEmail` or `loginHashPhone` is stored.
- As a result one canonical DNI is one participant per census, and the census duplicate check
  reports duplicates by DNI.
- A single helper decides which fields are hashed for a census:
  `func (c Census) LoginHashFields() (OrgMemberAuthFields, OrgMemberTwoFaFields)`. Every hash site
  uses it: `calculateParticipantHashes`, the duplicate and missing-data aggregation, and the repair
  tooling.
- For a cert census, the missing-data and validation report adds `invalidNationalId`: the members
  whose `nationalId` gives `ok=false`. These members cannot log in with a certificate.

**C2.4 Assertion endpoint** (internal, gateway only).
- Route: `POST /cert-auth/assertions`. The body is at most 4 KiB:

  ```json
  {"processId":"<hex64>","nationalId":"<9 chars>","documentType":"DNI|NIE",
   "provider":"fnmt","profile":"fnmt-citizen-g2","certSha256":"<hex64>",
   "codeChallenge":"<b64url 43>"}
  ```
- **Authentication.**
  1. Verify `X-Cert-Gateway-Signature` against `CERT_GATEWAY_PUBLIC_KEYS`, a comma-separated list
     of `keyId:base64(pubkey)` entries so keys can be rotated.
  2. Require `|now − t| ≤ 60 s`.
  3. Compare in constant time.
  4. Without keys configured, the route answers 404 (feature off).
- **Steps.** Each failure maps to a code from C1:
  1. Load the process. It must be published and open for voting, else `process_not_open`.
  2. Its census must have `CertAuth`, else `not_cert_census`.
  3. Refuse an anonymous census exactly as `anonymousCensusRefused` does.
  4. Compute `CanonicalNationalID(nationalId)`. It must give `ok=true`, else 400.
  5. Look up the participant by `HashAuthTwoFaFields({NationalID}, ["nationalId"], nil)`, else
     404 `not_in_census`.
  6. Check the weight, else `zero_weight`.
  7. Spend the rate budget: at most 10 assertions per participant per process per hour, else 429
     `rate_limited`.
  8. Create the assertion and answer `201 {"certCode":"<b64url 32 random bytes>","expiresAt":…}`.
- **Storage.** New collection `certAuthAssertions`:

  ```
  {_id, codeHash: sha256(code), processId, censusId, participantId, codeChallenge,
   provider, certSha256, createdAt, expiresAt, usedAt?}
  ```
  - Indexes: unique on `codeHash`, and a TTL index on `expiresAt` (`expireAfterSeconds: 0`).
  - The TTL is 5 minutes.
  - No document number is stored.
- **Logs.** Outcome, provider, process id and participant id only.

**C2.5 Step 0 with a certificate code.**
- `AuthRequest` gains `CertCode string \`json:"certCode,omitempty"\`` and
  `CodeVerifier string \`json:"codeVerifier,omitempty"\``.
- **Census with `CertAuth`.**
  1. The request must carry `certCode` and `codeVerifier` (43–128 characters). The typed login
     fields must be empty, else `ErrInvalidUserData`.
  2. Consume the assertion atomically:
     `FindOneAndUpdate({codeHash, processId, usedAt: null, expiresAt > now}, {$set: {usedAt: now}})`.
  3. Check `base64url(sha256(codeVerifier)) == codeChallenge` in constant time.
  4. Any failure answers `ErrCSPAuthFailed`.
  5. Load the `OrgMember` by `participantId`, then continue the existing flow from
     `determineContactMethod`: the submitted email or phone against the stored member, the weight,
     then `csp.AuthToken`.
  6. The failed-attempt budget key is the participant's `loginHash`. It is claimed before the
     contact comparison, as today.
  7. A consumed code is spent even if the contact check then fails. The voter goes through the
     certificate step again.
- **Census without `CertAuth`.** A request carrying `certCode` is refused with `ErrInvalidUserData`.
- **Unchanged.** Step 1, resend, sign and blind-sign do not change. The token is bound to the
  member id and process exactly as today.

**C2.6 Migration.**
- A registered migration rehashes participants for the new canonical `nationalId`. It covers every
  census whose `AuthFields` contains `nationalId`, and only participants whose canonical value
  differs from the lowercased trimmed value.
- A participant whose new hash collides with another participant of the same census keeps its old
  hashes and is counted and logged by participant id. The migration reports how many it rehashed
  and how many collided.
- It is idempotent.
- Version numbering follows the lts/main ledger rule (`migrations/repair_login_hashes.go:16-26`).
  The plan fixes the number against `origin/main` and `origin/lts`.

**C2.7 Docs.** Swagger annotations cover the new fields and route. `example.env` documents
`CERT_GATEWAY_PUBLIC_KEYS`.

### C3. vocdoni-app

- **Organiser side.**
  - VoterAuthentication gets a new credential option, "Certificado digital". Selecting it fixes the
    credentials to `["nationalId"]`, disables the other credential choices and keeps the existing
    2FA choices.
  - `buildCensusSpec` sends `certAuth: true`, and the draft mapping round-trips it.
  - The census validation step shows the `invalidNationalId` count.
- **Voter side**, when `election.census.certAuth`:
  1. Step 0 shows a single "Acceder con certificado digital" button instead of the login fields.
  2. On click, generate `state` (16 random bytes) and `codeVerifier` (32 random bytes, base64url).
     Store both in `sessionStorage` under `certAuth:<processId>`, then navigate (top level) to
     `${CERT_GATEWAY_URL}/v1/auth` with
     `returnUrl = location.origin + location.pathname`.
  3. On page load with `#certCode`/`#certError`, compare `state` with the stored one, then strip
     the fragment (`history.replaceState`) and delete the `sessionStorage` entry.
     - For a census without 2FA, call `auth0({certCode, codeVerifier})`.
     - For a census with 2FA, show the existing email or phone input, call
       `auth0({certCode, codeVerifier, email|phone})`, then the existing Step1.
  4. Show `#certError` codes as localised messages.
- **Configuration and strings.** `CERT_GATEWAY_URL` is a new env var. The option is hidden when it
  is unset. Strings go in en, es and ca. The other locales take English through the existing
  extraction.
- **Gateway allowlist.** The gateway's `ALLOWED_RETURN_ORIGINS` must list every app origin and
  every custom organisation domain that serves voting pages.

### Alternatives considered

| Option | Why not |
|---|---|
| Certificate check inside saas-backend, moving it to a Droplet/DOKS with an LB in TLS passthrough | Migrates the production service's hosting for one feature; the LB costs $15+/month (D1) |
| Cloudflare Enterprise mTLS forwarding `Client-Cert` to App Platform | Enterprise only; 5 CAs per account; no revocation check for BYO CAs; whether it advertises the CA list and works with smart-card signatures is undocumented (D1) |
| Gateway signs an attestation that travels through the browser to the backend | Puts the DNI in the browser and in URLs; the server-to-server call keeps it between the two services |
| Gateway redirects with the auth token itself | A token in a URL is a bearer credential. The single-use code plus the PKCE verifier makes a leaked or misdirected code useless |
| Gateway code inside saas-backend (`cmd/`) or inside cert-check | Owner chose a dedicated repo (D2) |
| Certificate-only census with no 2FA option | Owner chose optional 2FA (D3) |
| Rewriting stored `nationalId` values to the canonical form | Changes what organisers see and export; folding at hash time follows the existing rule in `HashAuthTwoFaFields` |

## Compatibility and rollout

- **API.** `certAuth`, `certCode` and `codeVerifier` are additive and omitted when empty. Old
  clients never send them and old censuses lack the field, so they behave as today.
- **Login hashes.** After C2.6, typed logins match more leniently, because both sides are
  canonicalised. A member stored as `1234567z` now logs in typing `01234567-Z`. Values that are not
  a valid DNI or NIE hash exactly as before.
- **Rollout order:**
  1. saas-backend with the feature off (no keys), migration run on stg.
  2. cert-gateway on the stg Droplet.
  3. Backend stg keys.
  4. vocdoni-app stg with `CERT_GATEWAY_URL`.
  5. End-to-end test with real certificates, run manually by the owner.
  6. The same steps in production.
  7. Every deploy and DNS change waits for the owner's OK.
- **Rollback.**
  - Unset `CERT_GATEWAY_PUBLIC_KEYS`. The assertion route returns 404 and new cert censuses are
    refused. Existing cert censuses can no longer authenticate, so the owner decides this during
    open votes.
  - Rolling the backend back past C2.2 makes typed logins of non-canonical stored IDs fail until
    the repair tooling is run with the old hash. The plan documents that command.
  - The gateway has no state, so it can be stopped at any time.

## Failure modes and security

- **Trust boundary.** The gateway is trusted to assert a DNI. A compromised gateway or signing
  key can mint assertions for any DNI in any cert census. Mitigations:
  - The key lives only on the Droplet.
  - The backend accepts several keys, so a key can be rotated without downtime.
  - The Droplet runs only the container: SSH from admin IPs, unattended upgrades.
  - Assertions are logged per participant, so they can be audited.
  - Accepted residual risk: this is the same trust a CSP already holds.
- **Code theft.** The `returnUrl` allowlist stops open redirects. The code travels in the
  fragment, is single-use and expires in 5 minutes. PKCE binds it to the browser that started the
  flow, so a code that leaks through history, an extension or a misdirect cannot be redeemed
  without the `codeVerifier` in that tab's `sessionStorage`. `state` blocks login CSRF, where a
  victim's tab would be fed the attacker's code.
- **Replay to the backend.** The signed request carries a timestamp valid for 60 s and travels
  over TLS. A replay can only create a new code, which goes back to the replayer and is useless
  without the verifier.
- **Passive observers.** TLS 1.3 encrypts the client certificate. A 1.2 fallback exposes it
  (D5).
- **Untrusted input.** cert-check bounds the chain and certificate sizes. The gateway bounds and
  validates its query parameters, and the backend bounds the body and validates every field. A
  revocation lookup that is unavailable fails closed with a retryable error, and the UI says "try
  later".
- **Availability.** The Droplet IP is exposed, with no Cloudflare in front: there is the
  per-IP rate limit, and the IP can be changed. Gateway downtime only blocks cert censuses. If the
  backend is unreachable, the gateway answers `backend_unavailable`.
- **Concurrency.** Assertions are consumed with one atomic `FindOneAndUpdate`, so two redemptions
  of one code cannot both succeed. Step-0 budgets keep their existing claim and release semantics.
- **Privacy.** The DNI exists only in the gateway's memory, in the signed request body and in the
  backend's lookup. It is never stored in assertions, never logged and never placed in URLs or
  browser storage.

## Acceptance criteria

- [ ] AC1 `internal.CanonicalNationalID` table test: DNI and NIE with case, spaces, dots and
  hyphens, short digits, an invalid letter, a foreign ID and the empty string.
- [ ] AC2 Hash parity: `TestRepairMatchesCanonicalHash` passes with the canonical `nationalId`. A
  new test shows a typed `1234567-z` matching a stored `01234567Z`.
- [ ] AC3 Census validation tests:
  - `certAuth` with `authFields≠[nationalId]` is rejected.
  - `certAuth` with the feature off is rejected.
  - `certAuth` with each 2FA subset is accepted.
  - `invalidNationalId` is reported.
- [ ] AC4 Assertion endpoint tests:
  - Valid signature leads to 201.
  - Bad or old signature, an unknown key and the feature off are each rejected.
  - Each error code: `not_cert_census`, `not_in_census`, `zero_weight`, `process_not_open`,
    `rate_limited`.
  - No DNI is present in the stored document.
- [ ] AC5 Step-0 tests:
  - Cert census without 2FA: `{certCode, codeVerifier}` gives a verified token, then sign
    succeeds.
  - Cert census with email 2FA: an OTP is queued, then step 1 succeeds.
  - Each of these fails with `ErrCSPAuthFailed`: wrong verifier, expired code, reused code, a code
    from another process, and typed login fields on a cert census.
  - `certCode` on a non-cert census is rejected.
  - Two concurrent redemptions give exactly one success.
- [ ] AC6 Migration test: non-canonical IDs are rehashed, collisions keep their old hashes and are
  counted, and a second run changes nothing.
- [ ] AC7 Gateway tests, using a fake `certcheck.Provider` and an `httptest` TLS server with
  client certificates:
  - Valid certificate leads to a signed POST, then a 303 to `returnUrl#certCode`.
  - Each `certcheck` error code leads to `#certError`.
  - A disallowed `returnUrl` or a bad parameter gets a 400 with no redirect.
  - The signature verifies against the backend's verifier, using shared test vectors.
  - Over the limit gets a 429.
  - A captured log contains no document number.
- [ ] AC8 vocdoni-app tests:
  - The organiser option builds `certAuth:true` with `["nationalId"]` and round-trips the draft.
  - The voter flow stores and checks `state`, strips the fragment, and calls `auth0` with
    `certCode`/`codeVerifier` (plus contact when 2FA).
  - Error codes render localised messages.
- [ ] AC9 Manual end-to-end on stg with at least one real FNMT and one idCAT certificate, run by
  the owner: auth-only and email-2FA censuses, plus a certificate whose DNI is outside the census.

## Constraints

- Go 1.27 (saas-backend `go 1.27.3`). Use the stdlib `crypto/ed25519` and `crypto/sha256` only:
  no new backend dependency. saas-backend does **not** import cert-check.
- Commits: conventional `type(scope): lowercase summary`, with no AI attribution or co-author
  trailers. Commits are never signed: always run `git -c commit.gpgsign=false commit …`.
- No PII (DNI, names, certificate subject or serial, email, phone) in logs, errors, URLs or browser
  storage.
- Feature branches: `feat/cert-auth` in saas-backend and vocdoni-app, `main` in the new
  cert-gateway repo. No pushes, tags, deploys or DNS changes without the owner's explicit OK.
- Tests need Docker (Mongo testcontainers) as the repo already does.

## Open questions

None. Hostnames (`cert.vocdoni.io` and `cert-stg.vocdoni.io`) and the Droplet size are proposed
defaults that the owner confirms when approving the spec.
