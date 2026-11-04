# Certificate voter authentication in the CSP (SP2): design

Status: approved (2026-11-04)   Class: architectural
Audit: `.delivery/2026-11-04-cert-auth-csp-audit-spec.md` (APPROVE WITH CHANGES, 1C/4I/11M); all
16 findings folded in. Contract errata landed in `contract-cert-auth.md`: F1 (§6/§13 budget
staging) and F13 (§11 vote-after-end). F4 needed no erratum: §13's CORS rule is implemented
as written (§4 below).
Owner decisions so far:
- 2026-11-03 (umbrella plan §12 + progress.md): providers at launch FNMT natural person + idCAT
  only; natural-person profiles only, enforced by handler composition (no representative/employee
  constructors); certificate-only census, no fallback method; every plan (no feature gating by
  subscription); identified display = holder full name + document type + masked number; auth
  listener on port 443; instant redirect with the DER chain stored ≤5 minutes and `Handler.Verify`
  at redeem; per-attempt random nonce subdomain.
- 2026-11-03 (dev team, via owner): count on Cloudflare being in the middle, but also support the
  server itself serving TLS; env vars select the mode. Two modes: "origin" (Go terminates TLS)
  and "cloudflare" (Cloudflare terminates mTLS and forwards the client certificate in headers).
- 2026-11-04 (SP1 amendment, not to be relitigated): member data is never modified; DNI/NIE
  normalisation lives only in login-hash computation and comparison; duplicates after
  normalisation block; census validation reports JSON warnings.
- 2026-11-04 (app contract): billing unchanged — members who cannot sign in still count in the
  billed census (ruling A5).

The HTTP contract this design implements **exactly** is
`contract-cert-auth.md` v1 (2026-11-04), kept in the app repo's delivery folder
(`.delivery/certificats-digitals/contract-cert-auth.md` on the `certificats-digitals` worktree).
The app UI is already built against it; any divergence goes through that file first. Section
references like "contract §6" below point at it.

## Problem

Organizers can only authenticate voters by typed member details (auth/2FA fields). The approved
umbrella program adds "Digital certificate (DNI/NIE)": the voter presents a qualified Spanish
certificate (FNMT natural person or idCAT) over TLS, the backend verifies it with the private
`cert-check` module and matches its DNI/NIE against the member's `nationalId` login hash.

Today the backend has nothing of this (verified on `feat/nationalid-normalization`):

- One plain-HTTP listener (`api/api.go:333`, `http.Server` + `ListenAndServe`); no TLS, no client
  certificate handling anywhere.
- Voter auth is `POST /processes/{processId}/auth/{step}` (`api/routes.go:186`) with typed fields
  (`csp/handlers/auth.go`); a census is defined by `AuthFields`/`TwoFaFields` only
  (`db/types.go:314`, `Census`), with no notion of *how* the fields are proven.
- No transaction/state store for a redirect flow, no nonce-host routing, no
  certificate-specific errors.

SP1 (merged first; spec `docs/specs/2026-11-03-nationalid-normalization-design.md`, approved as
amended) already provides the matching substrate: `internal.NormalizeNationalID`
(`internal/nationalid.go`), canonical nationalId login hashes via
`db.calculateParticipantHashes` (`db/census_participant.go:904`) feeding
`CensusParticipantByLoginHash` (`db/census_participant.go:172`), migration 0036 recomputing stored
participant hashes, `MongoStorage.MigrationApplied` (`db/migrations.go:246`) for the publish
guard, and the `nationalIdWarnings` JSON on census validation. SP2 builds only the certificate
flow on top; it adds no normalisation logic of its own.

## Goals

1. A voter on a certificate census authenticates with 2 clicks: button → native browser picker →
   back on the ballot, identified, holding the same pre-verified auth-only CSP token an `auth/0`
   census issues — so `check`, `sign-batch`, `blind-point`, `blind-sign`, `sign-info` and the
   whole vote pipeline, anonymous (blind CSP) censuses included, work unchanged.
2. The exact HTTP contract of `contract-cert-auth.md` v1 as amended by the 2026-11-04 errata
   (audit F1, F13): census `authMethod: "certificate"`,
   `POST /processes/{id}/auth/cert`, the auth listener's `GET /v1/authenticate`, the probe host,
   `POST /processes/{id}/auth/cert/redeem`, the error envelope with `data.reason`, holder display
   fields, the reference code, and check-only redeems for UPCOMING processes.
3. Two deployment modes behind env vars: `origin` (Go terminates TLS with cert-check's
   `TLSConfig`; Cloudflare DNS-only or TCP passthrough such as Spectrum) and `cloudflare`
   (Cloudflare terminates mTLS with a BYO CA and forwards the client certificate in headers).
   Origin mode ships first; cloudflare mode is fully specified and implemented but stays out of
   production until its blockers are verified (open questions Q1–Q2).
4. Feature off when the config is absent: the binary starts and behaves exactly as today — no
   listener, no new routes answering anything but an explicit "not enabled" error, no new
   startup requirements.
5. Security invariants of contract §13 (as amended by erratum F1): single-use state and code,
   PKCE challenge binding, returnUrl allowlist, rate limits and a two-stage failed-attempt budget
   (per presented certificate before Verify, per verified canonical document after it; revocation
   outages excluded), DER chain custody ≤5 minutes, no PII in logs or URLs, diagnostics shown
   only to the voter.

## Non-goals

- Normalisation, hash migration, warning classes, `scripts/repairlogins`: SP1 (this design calls
  its functions and adds no second normaliser).
- SDK surface (`createCertAuth`/`redeemCertAuth`): SP3. App UI: SP4/SP5.
- Playwright e2e against the disposable stack, the deployment runbook as a document, and docs:
  SP6. SP2 provides what they need (synthetic test provider, deployment section below as the
  runbook's source).
- Providers beyond `fnmt.New()` and `idcat.New()` (owner decision 1); representative/employee
  profiles (owner decision 4 — enforced by never constructing them, so cert-check itself answers
  `unsupported_profile`).
- Plan/feature gating by subscription (owner decision 3: every plan). Billing changes (ruling A5:
  none).
- Per-organization custom return-domains stored in the DB: the returnUrl allowlist is static
  configuration; ops add custom org domains to the env list.
- TLS 1.3 (`certcheck.WithTLS13`): revisit after SP0's FNMT/idCAT TLS 1.3 check; not in SP2.

## Current state

Verified on branch `feat/nationalid-normalization` at 09c6585 (SP1, merging first; line numbers
re-checked against that head per audit F11 — re-verify once more on the final rebase):

- **Routing**: `api/api.go:473` opens the public chi group (body limit, no JWT); CSP voter
  endpoints are registered there (`api/api.go:503–519`), including
  `processesAuthEndpoint = "/processes/{processId}/auth/{step}"` (`api/routes.go:186`) →
  `cspHandlers.ProcessAuthHandler`. chi prefers static segments, so `auth/cert` and
  `auth/cert/redeem` can coexist with `auth/{step}` (which only accepts steps `0`/`1`,
  `csp/handlers/auth.go:26`).
- **CORS**: one global middleware, `AllowedOrigins: ["*"]` with `AllowCredentials: true`
  (`api/api.go:353–358`), router-wide; it answers preflights before any inner handler. chi cannot
  swap middleware per route, but the middleware function itself can dispatch by path (§4).
- **Auth-only tokens**: `csp.CSP.AuthToken(anchorID, uID, to, ctype, ...)` with `to == "" &&
  ctype == ""` creates a pre-verified token via `createAuthOnlyToken` (`csp/auth.go:38`,
  `csp/auth.go:466`): stored in Mongo, immediately verified, usable by every signing endpoint.
- **Election window**: `startGrace`/`endGrace`, `processEnded` and `unvotableElection`
  (`csp/handlers/processes.go:64–98`; the `:90` comment records that "publish always persists a
  StartDate") gate signing per question; `db.VotingProcess` (`db/types.go:801`) holds
  `Published`, `StartDate`, `EndDate`; question statuses are
  `READY|PAUSED|ENDED|CANCELED|RESULTS` (`db/processes_questions.go:314–317`).
- **Census**: `db.Census` (`db/types.go:314`) with `Type` derived from `TwoFaFields`
  (`GetCensusType`, `db/types.go:516`); `db.ValidateCensusFields` (`db/census.go:22`);
  `populateCensus` (`db/census.go:123`) classifies members (`classifyCensusMembers`,
  `db/census_missing_data.go:126`), **blocks on duplicate login hashes** (`CensusMembersError`,
  `db/census.go:172`), inserts `members.complete` as participants and sets
  `census.Size = insertedCount` (`db/census.go:186–190`) — members with missing auth data are
  skipped, not blocking, at build time. `apicommon.CensusSpec`
  (`api/apicommon/voting_processes.go:16`) is the wire shape, echoed on process reads
  (`voting_processes.go:333,378`). Census validation endpoint:
  `api/processes_census_validate.go:42` (`POST /processes/census/validation`), 200 body
  `ValidateProcessCensusResponse{NationalIDWarnings}` (SP1).
- **Publish**: `publishPreflightProblems` (`api/processes_publish.go:109`) is the pre-publish
  gate; the blind public key selection for `Anonymous` censuses is at
  `api/processes_publish.go:369`.
- **Failed-attempt budget machinery**: Mongo-backed windowed counters with claim and refund —
  `ClaimCSPCounter` / `ReleaseCSPCounter` / `CSPCounterReached` (`db/csp_counters.go:104,144,159`),
  kinds + windows in `db/csp_counters.go:17–41`, TTL-indexed (`migrations/0029_csp_counters_ttl_index.go`).
- **Errors**: typed `errors.Error{Code, HTTPstatus, Err, Data}` (`errors/errors.go`), JSON
  `{error, code, data}`; codes are append-only, never reused (`errors/errors_definition.go`
  header). `WithData` (`errors/errors.go:214`) carries structured data.
- **Config**: Viper with `VOCDONI_` prefix; flags in `cmd/service/main.go:33+`; optional
  services enable on config presence. `api.Config` (`api/api.go:89`) wires components.
- **Mongo**: collections are fields on `MongoStorage` initialized in `db/mongo.go:46–62`; TTL
  indexes exist (migrations 0002, 0029). Migration ledger records versions only; highest
  registered after SP1 is **0036** on this branch (`migrations.NationalIDNormalizationVersion`).
- **cert-check** (read-only clone `~/.cache/vocdoni-app/ext/cert-check`, private module, Go 1.27,
  saas-backend is on Go 1.27.3): `NewHandler([]Provider{...})` → immutable `Handler`;
  `h.TLSConfig(getCert)` = TLS 1.2 only, ECDHE+AEAD, `RequestClientCert`, CAs advertised in the
  CertificateRequest, session tickets off, ALPN http/1.1 only; `h.Auth(r)` /
  `h.Verify(ctx, chain)` (chain[0] = leaf, full chain/profile/revocation validation — "never
  trust `r.TLS.PeerCertificates` on its own"); `CheckHost(r)` (Host == TLS server name);
  `Error{Code, Retryable, Detail, Diagnostics}` with the 16 codes of its README;
  `Diagnostics.DiagnosticID` (32 hex, random), `Diagnostics.LogAttrs()` (PII-safe),
  `NewDiagnosticID`/`DescribeCertificate` exported; `Provider` is a 3-method interface
  (`provider.go:12`) so test doubles are supported; `Result.Identity.Document.Number` is 9
  uppercase chars, zeros kept. `Handler.Providers()`, `ClientCAs()`, `CertificateCheck(subject)`
  exist for issuer attribution.

## Design

New package **`certauth/`** owns everything certificate-specific: config, the second listener,
host routing, the transaction store access, the handler logic for the three endpoints, error
mapping and the synthetic test provider (subpackage `certauth/testprovider`). `api` wires it like
it wires the CSP: `api.Config` gains `CertAuth *certauth.Service` (nil = feature off) and
registers the two main-listener routes delegating to it. `db` gains the transaction collection
and its accessors (same layering as every other collection).

### 1. Configuration (feature off when absent)

Flags on `cmd/service/main.go` (Viper ⇄ `VOCDONI_` env), all read into a
`certauth.Config`:

| Flag / env | Meaning | Default |
|---|---|---|
| `--certAuthMode` / `VOCDONI_CERTAUTHMODE` | `""` (off), `origin`, `cloudflare` | `""` |
| `--certAuthHost` / `VOCDONI_CERTAUTHHOST` | public auth domain suffix, e.g. `cert.auth-dev.vocdoni.io`; authUrl hosts are `<nonce>.<host>`, probe is `probe.<host>` | — (required when mode set) |
| `--certAuthListenAddr` / `VOCDONI_CERTAUTHLISTENADDR` | bind address of the auth listener | `:8443` |
| `--certAuthPublicPort` / `VOCDONI_CERTAUTHPUBLICPORT` | port written into `authUrl`/`probeUrl`; omitted from the URL when 443 | `443` |
| `--certAuthTLSCert`, `--certAuthTLSKey` | PEM files, the auth listener's server certificate: origin mode a wildcard `*.<certAuthHost>` certificate; cloudflare mode any certificate the Cloudflare origin pull accepts, e.g. a Cloudflare Origin CA certificate covering `*.<certAuthHost>` | — (required in both modes) |
| `--certAuthProviders` | comma list of cert-check provider IDs | `fnmt,idcat` |
| `--certAuthAllowedOrigins` | comma list of exact origins for returnUrl, redeem/create CORS and the probe | — (required when mode set) |
| `--certAuthDefaultReturn` | absolute https URL, the `attempt_expired` fallback redirect (contract §4) | — (required when mode set) |
| `--certAuthCreatesPerMinute` | create rate-limit cap per IP+process (≤0 disables, matching `ClaimCSPCounter`'s contract) | `60` |
| `--certAuthProxyProtocol` | accept PROXY protocol v1/v2 on the auth listener (origin mode behind Spectrum / a TCP front) | `false` |
| `--certAuthProxyFrontIPs` | comma list of CIDRs allowed to send PROXY protocol headers (origin mode; required when `certAuthProxyProtocol` is set) | — |
| `--certAuthCFOriginPullCA` | PEM file with the CA of the Authenticated Origin Pulls client certificate (cloudflare mode; required) | — |
| `--certAuthCFAllowedIPs` | comma list of CIDRs the auth listener accepts in cloudflare mode (empty = no IP filter) | — |
| `--certAuthTestProvider` | enable the synthetic test provider (dev/e2e only) | `false` |
| `--certAuthTestProviderCA` | PEM file with the synthetic CA certificate (its key stays with the test harness) | — |
| `--certAuthTestProviderAllowedAPIs` | extra `--vocdoniApi` hosts the test provider may run against (see rules) | — |

Rules, enforced at startup (fail loudly, like `NewHandler` does):

- `certAuthMode` empty → `certauth.New` is not called, `Config.CertAuth` is nil, no listener
  starts, no flag below is read. The three new routes still exist but answer
  `400 ErrCertificateAuthDisabled` (a plain standard error); census `authMethod: "certificate"`
  is refused on create/update/validate with the same error, so no un-runnable census can be
  created on a deployment without the feature. **The feature-off check is API-layer** (audit F9):
  the create/update/validate handlers test `a.certAuth == nil` before calling db validation — the
  db layer stays feature-agnostic and never sees `api.Config`. Publishing a *pre-existing*
  certificate-census draft while the feature is off is refused by `publishPreflightProblems` with
  the same `ErrCertificateAuthDisabled` (clearer than letting the publish-time census rebuild
  fail opaquely). Everything else is byte-identical to today.
- Allowed origins must be well-formed `https` origins; `http` is accepted only for
  `localhost`/`127.0.0.1` origins (the operator opts into dev by listing them — no separate
  dev-mode flag).
- Both modes require cert/key files that parse and cover `*.<certAuthHost>` (checked with
  `x509.Certificate.VerifyHostname` on a sample nonce host at startup) — in cloudflare mode the
  certificate only has to satisfy the origin pull (a Cloudflare Origin CA certificate is fine),
  but the hostname check still applies.
- Mode `cloudflare` additionally requires the AOP CA file; starting in this mode logs a prominent
  warning naming the unverified blockers (§3b) until SP6 clears them.
- `--certAuthProxyProtocol` requires `--certAuthProxyFrontIPs` (fail at startup otherwise): PROXY
  headers are honoured only from those CIDRs (audit F14).
- `certAuthTestProvider` refuses to enable unless the `--vocdoniApi` host is on an **allowlist**
  (audit F8 — a deny-known-prod check fails open): `localhost`, `127.0.0.1`, the dev/stage hosts
  `api-dev.vocdoni.net` and `api-stg.vocdoni.net`, or a host listed in
  `--certAuthTestProviderAllowedAPIs`. Anything else refuses at startup. When enabled it always
  logs a warning banner and adds the synthetic provider *in addition to* the configured ones.

### 2. Transaction store (Mongo)

New collection `certAuthTransactions` (field on `MongoStorage`, `db/mongo.go`), one document per
attempt:

```go
// db.CertAuthTransaction is one certificate sign-in attempt (contract §1).
type CertAuthTransaction struct {
    ID        bson.ObjectID `bson:"_id"`
    State     string        `bson:"state"`           // 32 random bytes, base64url (43 chars); unique index
    Nonce     string        `bson:"nonce"`           // 16 random bytes, base32 lower (26 chars); the authUrl host label
    Challenge string        `bson:"challenge"`       // S256 challenge from create, 43 chars
    ProcessID bson.ObjectID `bson:"processId"`
    ReturnURL string        `bson:"returnUrl"`       // validated at create
    Purpose   string        `bson:"purpose"`         // "vote" | "check"
    Status    string        `bson:"status"`          // "created" | "authenticated" | "consumed"
    Code      string        `bson:"code,omitempty"`  // 32 random bytes, base64url; unique sparse index
    Chain     [][]byte      `bson:"chain,omitempty"` // DER certificates, leaf first; empty = "no certificate"
    ChainPresented bool     `bson:"chainPresented"`  // distinguishes "no certificate" from "not yet authenticated"
    CreatedAt       time.Time `bson:"createdAt"`
    AuthenticatedAt time.Time `bson:"authenticatedAt,omitempty"`
    ExpiresAt       time.Time `bson:"expiresAt"`     // TTL index (expireAfterSeconds: 0)
}
```

- **TTLs** (constants in `certauth`): `stateTTL = 15 * time.Minute`, `codeTTL = 5 * time.Minute`
  (contract §3/§4). At create `ExpiresAt = CreatedAt + stateTTL`. The authenticate consume
  *shortens* it: `ExpiresAt = now + codeTTL`, which also bounds the DER chain's custody — the
  document (chain included) is reaped ≤5 min after authenticate plus the TTL monitor's ~60 s lag.
  Mongo TTL is garbage collection only: every read re-checks the relevant deadline explicitly
  (`CreatedAt+stateTTL` at authenticate, `AuthenticatedAt+codeTTL` at redeem), so a not-yet-reaped
  expired document behaves as expired.
- **Single use, atomically**: `db.ConsumeCertAuthState(state, nonce, chain)` is one
  `FindOneAndUpdate{state, status: "created", createdAt > now-stateTTL, nonce}` →
  `$set{status: "authenticated", code, chain, chainPresented, authenticatedAt, expiresAt}`
  returning the previous document; no match = unknown/expired/consumed (the handler distinguishes
  "known but spent/expired" — redirect to its stored returnUrl — from "unknown" — redirect to
  `certAuthDefaultReturn` — with a second plain read). `db.ConsumeCertAuthCode(code)` is the same
  pattern on `{code, status: "authenticated", authenticatedAt > now-codeTTL}` →
  `$set{status: "consumed"}, $unset{chain}` returning the pre-image (the chain the redeem
  verifies). The `$unset` makes chain custody end at redeem regardless of TTL. Its "no match" is
  disambiguated with a second plain read, exactly like the state path (audit F10): a document with
  `status: "consumed"` → `code_used` (409); anything else — expired, TTL-reaped or unknown —
  → `attempt_expired` (410).
- Migration **0037** (`migrations/0037_cert_auth_transactions.go`; number re-verified against
  `main` and `origin/lts` on rebase, after SP1's 0036) creates the unique index on `state`, the
  unique sparse index on `code`, and the TTL index on `expiresAt`. `down` drops them.
- No document field ever holds a DNI/NIE, a name, or anything derived from the identity; the
  chain is the only personal data and lives ≤5 min.

### 3. The auth listener

One `http.Server` built by `certauth.Service.Listener()`, started by `api.Start` next to the main
server when the feature is on. Shared pieces (both modes): a host-routing `http.Handler` that
dispatches on `r.Host` (port stripped) —

- `probe.<certAuthHost>` → `GET /ok`: answers `200 {"ok":true}` with `Cache-Control: no-store`
  and, when the request's `Origin` is in the allowlist, `Access-Control-Allow-Origin: <origin>`
  (+ `Vary: Origin`; no credentials header). Anything else on this host: 404.
- `<26 base32 chars>.<certAuthHost>` → `GET /v1/authenticate` (below). Anything else: 404.
- any other host → `421 Misdirected Request`.

`ReadHeaderTimeout: 10 * time.Second`, `ReadTimeout`/`WriteTimeout` 30 s, `MaxHeaderBytes`
default; no request bodies are read.

**`GET /v1/authenticate?state=<state>`** (contract §4), identical logic in both modes once the
chain is in hand:

1. In origin mode, `certcheck.CheckHost(r)` first (Host must equal the TLS server name; a
   mismatch → 421). Extract the nonce from the Host's first label.
2. Obtain the presented chain: origin mode `r.TLS.PeerCertificates` (possession already proven by
   the completed handshake's CertificateVerify); cloudflare mode from the forwarded headers (§3b).
   An empty chain is a valid outcome ("no certificate": `ChainPresented = false`). Storage bounds
   (audit F5, both modes): when the presented chain exceeds >10 certificates or >200 KiB total,
   store the leaf alone when it is ≤16 KiB (cert-check's per-certificate bound) and drop the rest
   — `Handler.Verify` judges the leaf-only chain against its pinned anchors and intermediates; a
   leaf over 16 KiB stores as presented-but-empty (`ChainPresented: true`, empty `Chain`) and
   redeem answers `invalid_chain` with a fresh diagnostic, never `no_certificate`. The 303 is
   unconditional either way.
3. `ConsumeCertAuthState(state, nonce, chain)`: the nonce in the filter binds the attempt to its
   own host, so a URL replayed on a different nonce host is "unknown state". On success, answer
   immediately — no verification, no OCSP, no body:
   `303 Location: <returnUrl>#code=<code>&state=<state>`, headers `Cache-Control: no-store`,
   `Referrer-Policy: no-referrer`, `Connection: close`.
4. Unknown, expired or consumed state: `303 <storedReturnUrl-or-certAuthDefaultReturn>#error=attempt_expired`,
   same headers. Never a bare text or HTML page.

`code` is 32 random bytes base64url from `crypto/rand`, generated inside the consume call.

#### 3a. Mode `origin` — Go terminates TLS

- `srv.TLSConfig` is a parent `tls.Config{MinVersion: tls.VersionTLS12,
  NextProtos: ["http/1.1"], GetCertificate: getCert}` whose `GetConfigForClient` selects by
  lowercased SNI (audit F2): `probe.<certAuthHost>` → a plain config (same `getCert`,
  `ClientAuth: tls.NoClientCert`, TLS 1.2 floor) so a probe handshake **never sends a
  CertificateRequest** (contract §7 — otherwise the background 3 s health check could surface the
  native picker); `<26 base32>.<certAuthHost>` → `handler.TLSConfig(getCert)` where `handler` is
  the `certcheck.NewHandler` built at startup from the configured providers (default
  `[]certcheck.Provider{fnmt.New(), idcat.New()}` — never `fnmt.NewRepresentative()` or
  `fnmt.NewEmployee()`, owner decision 4) plus the synthetic provider when enabled; any other SNI
  → handshake error, mirroring cert-check's testui (`testui/internal/server/tlsconfig.go`,
  per-SNI `GetConfigForClient`). `getCert` returns the wildcard certificate loaded from
  `--certAuthTLSCert/Key` at startup (renewal = restart; noted in Deployment).
- `Protocols.SetHTTP1(true)` (HTTP/1.1 only), exactly as cert-check's quick start: with session
  tickets off and TLS 1.2 this guarantees a fresh handshake — and therefore the browser's picker —
  per nonce host.
- When `--certAuthProxyProtocol` is set, the `net.Listener` is wrapped with
  `github.com/pires/go-proxyproto` (new dependency, origin mode only) so a TCP-passthrough front
  (Spectrum, a dedicated-IP forwarder) can convey the client IP; it is used for logging only. The
  wrapper sets a `Policy` accepting PROXY headers only from `--certAuthProxyFrontIPs` (audit F14
  — otherwise anyone can spoof logged client IPs); connections from other sources are treated as
  plain TCP.
- What this mode preserves (all unconditional, from cert-check's `TLSConfig`): CA names in the
  CertificateRequest → the browser's picker shows only FNMT/idCAT certificates; TLS 1.2 ceiling
  (DNIe-class and legacy CAPI keys could sign — relevant if providers grow); private-key
  possession proven at the origin; session tickets off; HTTP/1.1 only (no HTTP/2 coalescing
  across nonce hosts); full revocation at redeem.
- What it loses: Cloudflare WAF/DDoS/rate limiting on the auth listener when the wildcard record
  is DNS-only (Spectrum restores L3/L4 protection); the wildcard server certificate is
  self-managed (DNS-01).

#### 3b. Mode `cloudflare` — Cloudflare terminates mTLS (implemented, not production until verified)

Cloudflare proxies `*.<certAuthHost>` (orange cloud), terminates the browser's mTLS with the
FNMT/idCAT roots uploaded as BYO CAs, and forwards to the origin. The auth listener then:

- Serves TLS itself with `tls.Config{ClientCAs: <AOP CA from --certAuthCFOriginPullCA>,
  ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}` — only a peer
  presenting Cloudflare's Authenticated Origin Pulls certificate (zone-level or per-hostname
  upload, never the shared global one) completes a connection. When `--certAuthCFAllowedIPs` is
  set, connections from other IPs are dropped at accept time. Both checks together are the
  header-trust boundary: a forged `Client-Cert` header from anyone who is not our Cloudflare zone
  never reaches the handler.
- Reconstructs the chain for storage: leaf from the RFC 9440 `Client-Cert` structured-field byte
  sequence, falling back to the legacy `Cf-Client-Cert-Der-Base64`; intermediates from
  `Client-Cert-Chain` (leaf excluded, per RFC 9440). Absent leaf header = "no certificate".
  Oversize leaves are **not detectable at the origin** (audit F5):
  `cf.tls_client_auth.cert_rfc9440_too_large` is a WAF expression field, not a forwarded header —
  an over-10 KiB leaf arrives as an absent `Client-Cert` header and redeems as `no_certificate`.
  Accepted cloudflare-mode limitation (real FNMT/idCAT leaves are a few KiB).
- Everything downstream (transaction consume, 303, redeem `Handler.Verify` on the stored chain)
  is shared code. Cloudflare does **not** check revocation for BYO CAs; our Verify-at-redeem does,
  unchanged.

What this mode loses, stated so nobody is surprised (sources: `maps/cloudflare.md`):

- **CA advertisement for picker filtering** — whether Cloudflare puts the BYO CA names in the TLS
  `CertificateRequest.certificate_authorities` is UNVERIFIED; if it does not, voters see *every*
  installed certificate in the picker.
- **TLS 1.2 ceiling** — only zone-wide `tls_1_3: off`; needs a dedicated zone for the auth
  domain, or TLS 1.3 stays on (FNMT/idCAT software keys may cope; unverified).
- **Possession proof at the origin** — Cloudflare proves the CertificateVerify at its edge; the
  origin sees a DER blob in a header and trusts AOP+IP that Cloudflare checked possession. A gap
  in the zone's mTLS/WAF configuration would let a valid-but-stolen certificate blob through
  `Handler.Verify` (chain and revocation pass without possession). Accepted trust boundary, per
  the dev team's constraint.
- **Session-ticket and HTTP/2 control** — the edge owns resumption; HTTP/2 coalescing can reuse
  one connection across nonce hosts under the wildcard edge certificate, silently skipping the
  per-attempt picker. Mitigation (zone-level HTTP/2 off) is UNVERIFIED in effect.
- **Unverified blockers before production** (open question Q2): CA names in CertificateRequest;
  HTTP/2 coalescing across nonce hosts with H2 disabled; a WAF/mTLS configuration that *forwards*
  no-cert requests instead of blocking them (the contract's `no_certificate` UX requires our 303,
  not a Cloudflare block page) while still requesting a certificate; the mTLS hostname
  association covering every nonce subdomain while excluding `probe.` (audit F16); Enterprise
  plan with API Shield mTLS (BYO CA, max 5 roots) on the zone.

### 4. `POST /processes/{processId}/auth/cert` — create (contract §3)

Main listener, public group, no `Authorization`. New route constants
`processesAuthCertEndpoint` / `processesAuthCertRedeemEndpoint` in `api/routes.go`, registered
next to `processesAuthEndpoint` (`api/api.go:512`), handlers delegating to
`certauth.Service.Create` / `Redeem`.

Validation, in order (any failure before the rate limit is a plain 400 standard error, no
`data.reason`, per contract §3):

1. Body: `challenge` exactly 43 chars of `[A-Za-z0-9_-]`; `challengeMethod == "S256"`;
   `purpose ∈ {"vote", "check"}`.
2. `returnUrl`: parses as absolute URL; scheme `https` (or `http` with a `localhost`/`127.0.0.1`
   host); no query, no fragment, no userinfo; origin ∈ `certAuthAllowedOrigins` (exact string
   match on scheme://host[:port]); path matches
   `^(/[a-z]{2}(-[a-z]{2})?)?(/processes/[0-9a-f]{24})?/?$`.
3. Process exists (`votingProcesses` by id), `Published == true`, and accepts attempts:
   not `processEnded(vp)` (`csp/handlers/processes.go:76`) and at least one question's status is
   outside `{ENDED, CANCELED, RESULTS}` — else `409` standard error (this is the contract's "not
   CANCELED, ENDED or RESULTS"; UPCOMING — `StartDate` in the future — is allowed for both
   purposes, redeem decides token issuance, §6).
4. Census (`vp.CensusID`) has `AuthMethod == "certificate"` — else `400` standard error.
5. Rate limit: one unit of a new counter kind `db.CSPCounterCertAuthCreates`
   (`CSPCounterWindow` gains a `time.Minute` case for it) keyed
   `sha256(clientIP | processID)`, cap `--certAuthCreatesPerMinute` (default 60; ≤0 disables,
   matching `ClaimCSPCounter`'s contract — audit F12: the cap is configurable because a
   CGNAT/venue crowd shares one IP and a 500-voter assembly can brush 60/min). Refused →
   `429` cert-error envelope `data.reason = "too_many_attempts"`,
   `data.retryable = false`, `data.retryAfter = seconds until ResetAt`. Client IP from
   `RemoteAddr`; when the deployment fronts the main listener with the trusted edge proxy
   (`OriginSecret` set, `api/api.go:114`), the rightmost `X-Forwarded-For` entry is used instead.
   Deployment rule (F12): behind any proxy, `OriginSecret` must be set, or `RemoteAddr` is the
   proxy and every voter shares one create bucket.

Then: generate `state` (32 B base64url) and `nonce` (16 B base32 lower, 26 chars, independent of
state), insert the transaction, answer 200:

```json
{"state": "...", "authUrl": "https://<nonce>.<certAuthHost>[:<publicPort≠443>]/v1/authenticate?state=<state>",
 "expiresAt": "<createdAt+15m RFC3339>", "probeUrl": "https://probe.<certAuthHost>[:...]/ok"}
```

`probeUrl` is present in both modes (in cloudflare mode its host must be excluded from the mTLS
association — deployment note); a future mode that cannot serve it omits the field, as the
contract allows.

CORS on this endpoint and redeem — contract §13 implemented as written (audit F4; chi cannot swap
middleware per route, but the global middleware can dispatch by path): the single `r.Use` CORS
slot (`api/api.go:353`) becomes a path dispatcher installed when the feature is on. Requests whose
path matches `^/processes/[0-9a-f]{24}/auth/cert(/redeem)?$` go through a dedicated
`cors.New{AllowedOrigins: certAuthAllowedOrigins, AllowedMethods: [POST, OPTIONS],
AllowedHeaders: [Accept, Content-Type], AllowCredentials: false}` handler — preflights and actual
responses both answer allowlisted origins only, no credentials; every other path keeps today's
permissive config byte-identically (feature off: the dispatcher is not installed at all). The
returnUrl allowlist and PKCE binding remain the primary controls; this makes §13's CORS line a
tested guarantee rather than a softened aside.

### 5. `POST /processes/{processId}/auth/cert/redeem` (contract §6)

Steps, exactly in contract order; every failure is the cert-error envelope (§7):

1. Body: `code`, `verifier` (`[A-Za-z0-9._~-]{43,128}` — RFC 7636 verifier grammar).
2. `tx := ConsumeCertAuthCode(code)`: unknown/expired → `attempt_expired` (410); already consumed
   → `code_used` (409). `tx.ProcessID` must equal the path `processId` (else `attempt_expired` —
   a code is bound to its process).
3. PKCE: `base64url(sha256(verifier)) != tx.Challenge` (constant-time compare) →
   `verifier_mismatch` (403). The code is already consumed — by design (contract).
4. `ChainPresented == false` → `no_certificate` (422); `ChainPresented == true` with an empty
   `Chain` (the F5 oversize-leaf case) → `invalid_chain` (422). Both carry a fresh cert-check
   style diagnostic: the service builds `certcheck.Error`-equivalent data via `NewDiagnosticID`
   so the reference code exists even without a Verify run.
5. Parse `chain[0]` with `x509.ParseCertificate`; best-effort extract the holder's identity from
   the subject (givenName/surname/CN, serialNumber attribute, e.g. `IDCES-12345678Z`) with a
   small `certauth.subjectIdentity` helper used **only for display**, never for budget keys or
   authorization. Budget, claimed in two stages so a forged subject can never spend a real
   voter's budget (audit F1 — the leaf subject is attacker-controlled until Verify passes; the
   old single pre-Verify document key let 10 self-signed certificates naming a victim's DNI lock
   that voter out hourly for the whole vote):
   - **Before Verify**: claim one unit of kind `db.CSPCounterCertAuthFailures` (hourly window)
     keyed `sha256(processID | sha256(leafDER))` — per presented certificate — cap
     `maxHourlyAuthFailures` (the CSP's existing default, 10). Refused → `too_many_attempts`
     (429, `retryAfter`).
   - **After a Verify that returns a `Result`** (step 6): claim one unit of the same kind keyed
     `sha256(processID | canonicalDocument)` with the **verified**
     `res.Identity.Document.Number`, same cap. Refused → `too_many_attempts`. This is the key
     contract §13 names (as amended by erratum F1); it binds only identities proven by possession
     and chain validation, so `dni_not_in_census` probing stays capped per document while a
     self-signed certificate naming a foreign document only ever spends its own DER's budget.
   Check-only redeems spend the units too (contract §11).
6. `Handler.Verify(ctx, tx.Chain-as-x509)` with `context.WithTimeout(r.Context(), 40*time.Second)`
   (the main listener's 45 s global timeout, `api/api.go:367`, leaves headroom; the app waits
   45 s). On `*certcheck.Error`: map per §7. `revocation_unavailable` and `internal` release the
   per-certificate unit (`ReleaseCSPCounter`) — outages are never charged (contract §8); no
   document unit exists yet on those paths (no `Result`), so there is nothing else to release.
   On success, claim the document unit (step 5, stage two).
7. On success, enforce natural person defensively: `res.Profile.HolderRole !=
   certcheck.RoleNaturalPerson` → `unsupported_profile` (belt-and-braces; the handler composition
   already guarantees it).
8. `r := internal.NormalizeNationalID(res.Identity.Document.Number)`; use `r.Normalized`
   (idempotent — cert output is already canonical). Build
   `inputMember := db.OrgMember{NationalID: r.Normalized}` (by value — the lookup takes
   `OrgMember`, not a pointer; audit F11) and look up
   `CensusParticipantByLoginHash(census, inputMember)` (canonical hashing inside, SP1 §2/§3).
   `ErrNotFound` → `dni_not_in_census` (403) with the holder block and the successful Verify's
   `DiagnosticID` as `reference`. A zero-weight participant in a weighted census (the `auth/0`
   guard, `csp/handlers/auth.go:444`) also answers `dni_not_in_census`: by certificate, "cannot
   vote in this process" is the truthful rendering, and the holder block makes it actionable.
9. Token: `voteOpen := !processEnded(vp) && !(!vp.StartDate.IsZero() &&
   time.Now().Add(startGrace).Before(vp.StartDate))` — the same zero-StartDate semantics as
   `unvotableElection` (audit F6: a zero StartDate counts as *started*, matching
   `csp/handlers/processes.go:90–92`; publish always persists one, but the invariant must not
   silently flip to null tokens if that ever relaxes).
   When `tx.Purpose == "vote"` and `voteOpen`: `authToken, _ = csp.AuthToken(processID, memberID,
   "", "", ...)` (the existing auth-only pre-verified path, `csp/auth.go:38`) and
   `weight` = the participant member's weight as `internal.HexBytes` (big-endian `big.Int`
   bytes; weight 1 for non-weighted censuses) — the same wire encoding as `AuthResponse.Weight`
   (`csp/handlers/types.go:44`), which marshals as bare hex without a `0x` prefix
   (`internal/hexbytes.go:85`, e.g. `"01"`, `"2a"`), matching the contract's example.
   When `purpose == "check"`, when `purpose == "vote"` on a not-yet-open process, **and when
   `purpose == "vote"` on a process that ended between create and redeem** (audit F13, contract
   §11 erratum: same null-token shape, no new reason — the app renders from process status):
   `authToken: null`, no `weight`, no CSP token created (contract §11).
10. Respond 200: `purpose` (echo), `authToken`, `weight`, `voterRef`, `holder`, `reference`
    (= `res.Diagnostics.DiagnosticID`). The chain is already gone (step 2's `$unset`).

**`voterRef`**: `base64url(HMAC-SHA256(k, processID || participantLoginHash)[:16])` where
`k = SHA-256("vocdoni-certauth-voterref" || csp.RootKey)` derived once at startup — stable per
participant-per-process across replicas and restarts, not reversible without the server secret,
never a DNI or a bare hash of one (contract §6).

**`holder`**: `fullName = res.Identity.FullName`; `documentType` `"DNI"`/`"NIE"` from
`res.Identity.Document.Type`; `documentEnding` = last 4 chars of the canonical 9-char number. On
failures `expired`, `not_yet_valid`, `revoked`, the holder is filled best-effort from the parsed
leaf subject (step 5's helper: givenName/surname/CN for the name, serialNumber for the document)
and omitted when parsing fails — "when the identity parsed" (contract §8).

**Budget outcome accounting** (audit F1): both units — the per-certificate one from step 5 and
the per-document one from step 6 — stay spent on every outcome, success included, mirroring
`auth/0`'s spend-per-attempt; the only release is the per-certificate unit on the retryable pair
(`revocation_unavailable`, `internal`), where no document unit was ever claimed. This mirrors the
typed-auth two-key defence (submission vs voter keys, `db/csp_counters.go:21–34`): the
attacker-reachable key is the certificate they actually hold, never a key they can point at
someone else.

### 6. Census `authMethod: "certificate"` (contract §2)

- `db.Census` gains `AuthMethod CensusAuthMethod` (`bson:"authMethod,omitempty"`), type
  `CensusAuthMethod string` with the single constant `CensusAuthMethodCertificate =
  "certificate"`; empty means today's typed-fields census. `apicommon.CensusSpec` gains
  `AuthMethod string json:"authMethod,omitempty"`, carried through create
  (`POST /processes`), draft update (`PUT /processes/{id}`), validation
  (`POST /processes/census/validation`) and echoed on every process read (both `CensusSpec`
  constructions, `api/apicommon/voting_processes.go:333,378`).
- Validation (one place, `db.ValidateCensusFields` grows an `authMethod` parameter or a sibling
  `ValidateCensusSpec` called from `db/census.go:48` and the validation handler): when
  `authMethod == "certificate"`, `AuthFields` must be exactly `["nationalId"]` and `TwoFaFields`
  empty — anything else `400` standard error. Unknown `authMethod` values: `400`. `Anonymous` and
  `Weighted` combine freely (no new interaction: the census stays `CensusTypeAuthOnly` via
  `GetCensusType`, and the blind path keys on `Anonymous` only, `api/processes_publish.go:369`).
  When the feature is off, the refusal happens one layer up, in the API handlers (§1, audit F9);
  the db validation itself is feature-agnostic.
- **Census validation semantics** (SP1 pipeline, adjusted only for this census kind): members
  whose nationalId classifies `missing`, `letter_mismatch`, `unsupported_document` or `invalid`
  are **not** listed in `missingData` and do not 400 — they appear only in `nationalIdWarnings`
  (their classes are already produced by SP1). Implementation: the missing-data classification
  (`CheckGroupMembersFields` / `classifyCensusMembers` feeding the validation result) treats an
  empty `nationalId` as a warning instead of `missingData` **only when
  `authMethod == "certificate"`** (for typed censuses an empty login field still blocks, SP1
  semantics unchanged). Duplicates after normalisation keep blocking: `400` with
  `data.duplicates` (both members of each group) plus the matching
  `duplicate_after_normalization` warnings — that is SP1's §4 behaviour untouched. Clean census:
  `200 {"nationalIdWarnings": [...]}` (empty array when clean).
- **Census build** (draft create/update and the publish-time rebuild): never fails on the
  non-blocking classes. Members with a **missing** nationalId are skipped by the existing
  complete/missing classification in `populateCensus` (`db/census.go:123`,
  `classifyCensusMembers` at `db/census_missing_data.go:126`) — they get no participant, **so
  they are excluded from the stored `census.Size`** (`db/census.go:186–190`,
  `Size = insertedCount`). Members with a present-but-unmatchable value (`letter_mismatch`,
  `unsupported_document`, `invalid`) are inserted as participants with their raw-value hashes —
  SP1 guarantees a certificate's canonical number can never hash-match them — and **count in
  `Size`**. This is today's build behaviour; SP2 changes nothing in the build path and records
  the Size effect here as the contract requires. Billing is untouched either way (ruling A5: the
  billed census is the chosen group).
- **`auth/0` refusal**: `authFirstStep` (`csp/handlers/auth.go:297`), right after loading the
  census, answers the cert envelope `400` with `data.reason = "certificate_census"`,
  `data.retryable = false` when `census.AuthMethod == "certificate"`. Step 1 is unchanged: the
  only tokens that exist for such a process come from redeem and are already verified.
- **Publish guard**: `publishPreflightProblems` (`api/processes_publish.go:109`) — when the
  process census has `AuthMethod == "certificate"` and
  `!ms.MigrationApplied(migrations.NationalIDNormalizationVersion)`, publishing is refused with
  `409` (standard error, message "nationalId hash migration pending"). This closes the
  pre-migration deploy window of SP1 §6.

### 7. Error envelope and mapping (contract §8)

One new code in `errors/errors_definition.go`:

```go
// ErrCertificateAuth is the one code of every certificate sign-in failure; clients key on
// data.reason, never on the code (contract §8). The HTTP status varies per reason —
// contract-cert-auth.md §8 fixes one code for every certificate reason, including the 500/503
// retryable ones, a deliberate exception to this file's 4xxxx/5xxxx split (audit F15).
ErrCertificateAuth = Error{Code: 40950, HTTPstatus: http.StatusUnprocessableEntity,
    Err: fmt.Errorf("certificate authentication failed"), LogLevel: "info"}
```

`certauth` builds each response as a copy with the reason's HTTP status, `Err` set to the
contract's `error` string, and `Data` = the §8 `data` object (`reason`, `retryable`, and
conditionally `reference`, `holder`, `issuer`, `notAfter`, `retryAfter`). 40950 is appended per
the errors-file conventions (never reuse, never renumber). The feature-off error is a separate
plain `ErrCertificateAuthDisabled` (next free 4xx code, 400) with no `data`.

Mapping table (single source: a `map[certcheck.Code]reasonSpec` in `certauth/errors.go`;
cert-check codes pass through **verbatim** as `data.reason`):

| `data.reason` | HTTP | Source | Extra `data` |
|---|---|---|---|
| the 14 non-retryable cert-check codes (`no_certificate` … `revocation_unknown`) | 422 | `Handler.Verify` | `reference` (Diagnostics.DiagnosticID); `issuer` when attributable; `holder` on `expired`/`not_yet_valid`/`revoked` when the leaf subject parses; `notAfter` on `expired`/`not_yet_valid` (Diagnostics.Certificate) |
| `revocation_unavailable` | 503 | Verify, retryable | `reference`; budget unit released |
| `internal` | 500 | Verify or backend | `retryable: true`; `reference` = Verify's DiagnosticID, or a fresh `certcheck.NewDiagnosticID()` for backend-internal failures, logged with the error |
| `dni_not_in_census` | 403 | backend | `reference` (the successful Verify's DiagnosticID), `holder` |
| `attempt_expired` | 410 | backend | — |
| `code_used` | 409 | backend | — |
| `verifier_mismatch` | 403 | backend | — |
| `too_many_attempts` | 429 | backend (create limit, redeem budget) | `retryAfter` (int seconds) |
| `certificate_census` | 400 | backend (`auth/0` on a cert census) | — |

`retryable` is `true` only for `revocation_unavailable` and `internal`. **Issuer attribution**:
match the leaf's `RawIssuer` against each configured provider's `CAs()` subjects
(`Handler.Providers()`); emit `{"id": Info().ID, "name": Info().Name}` when exactly one provider
matches, omit otherwise.

**Diagnostics discipline**: the response carries only the fields above — never
`certcheck.Error.Detail`, never a serialized `Diagnostics`, never the certificate. Logging goes
through `slog`-style structured attrs from `Diagnostics.LogAttrs()` plus
`processId`/`state`-prefix only; the backend never logs a DNI/NIE, a name, a subject, a chain or
a returnUrl fragment. (`state` and `code` are random secrets: log at most their first 8 chars for
correlation.)

### 8. Synthetic test provider (`certauth/testprovider`)

A `certcheck.Provider` double for integration tests and the SP6 e2e stack — cert-check's own
README prescribes exactly this (real CA-published specimens are rejected as `test_certificate`):

- `Info()` → `{ID: "test", Name: "Synthetic Test CA", Beta: true}`.
- `CAs()` → the CA certificate loaded from `--certAuthTestProviderCA` (or generated in-memory by
  the test helper); the CA key never lives in the backend — the harness (tests, Playwright) mints
  client certificates with it.
- `Verify(ctx, chain)` → checks the leaf is signed by the CA and within validity, then builds a
  `certcheck.Result` whose `Identity` comes from the leaf subject (`serialNumber` `IDCES-<n>` →
  document via `internal.NormalizeNationalID` classification for DNI/NIE typing, CN/givenName/
  surname → names), `HolderRole: RoleNaturalPerson`, a fresh `DiagnosticID`. Leaves with subject
  OU `REVOKED` answer `revoked`, OU `OUTAGE` answer `revocation_unavailable` — deterministic
  error-path fixtures with no network.
- Enabled only by `--certAuthTestProvider` under §1's guard. Unit/integration tests construct it
  directly (no flag) with an in-memory PKI helper in `certauth/testprovider` (exported, no
  `_test.go` so the e2e stack binary can use it).

### 9. Swagger

`make swagger` after the API changes. The two main-listener endpoints get full swag annotations
(request/response types in `apicommon`: `CertAuthCreateRequest/Response`,
`CertAuthRedeemRequest/Response`, `CertAuthHolder`); the census `authMethod` field is documented
on `CensusSpec`; the auth listener is a separate server and is documented in the create
endpoint's `@Description` (the authUrl host is not part of this API's base path), not as a
swagger route. Per AGENTS.md, the PR description records the vocdoni.io and integrator-sdk
follow-ups (new endpoints, `authMethod`, the error envelope) as suggested issues.

### Alternatives considered

| Option | Why not |
|---|---|
| Verify on the auth listener behind an interstitial (no server-side chain custody) | Rejected by owner decision 7: up to 40 s on a foreign host is the top abandonment point; errors must render in the app. |
| Reuse `auth/{step}` with a new step for certificates | The flow is a redirect dance with its own state machine, listener and error envelope; shoehorning it into `AuthRequest/AuthResponse` would leak cert semantics into every typed-auth path. Dedicated routes match the contract. |
| Store transactions in memory | Multiple replicas serve the main API and the auth listener; state created on one replica must be consumable on another. Mongo with TTL indexes is the existing pattern (csp counters, tokens). |
| Derive the nonce from the state | Contract forbids it (§3): an on-path observer of DNS/SNI could link the cleartext hostname to a later redeem. Independent randomness costs nothing. |
| Key the redeem budget on IP | CGNAT/venue Wi-Fi punishes crowds (umbrella §2); the verified canonical document is the enumeration target, so it is the right post-Verify key, with the per-DER key bounding pre-Verify cost (audit F1). |
| Key the pre-Verify budget on the unverified leaf-subject document | Rejected (audit F1, Critical): the subject is attacker-controlled; 10 forged self-signed certificates naming a victim's DNI would lock the real voter out hourly — the inverse of the typed-auth submission/voter split. |
| Charge revocation outages to the budget | A CA outage would lock every affected voter out for an hour through no fault of theirs; contract §8 excludes it. |
| A separate cert-check microservice | Rejected in the umbrella plan (cross-origin cookie redeem = third-party cookie death); embedding keeps one trust boundary and reuses the CSP token store. |
| Gate cloudflare mode behind an extra "ack" env var | Selecting `certAuthMode=cloudflare` is already an explicit operator act; a startup warning naming the unverified blockers is enough, and SP6's runbook controls production. |
| Refuse `authMethod: "certificate"` only at publish when the feature is off | Would let organizers build drafts that can never run on that deployment; refusing at create/validate fails earlier with the same code path. |

## Compatibility and rollout

- **Wire changes** (vs the SP1 branch): `CensusSpec.authMethod` (optional, echoed on reads); two
  new public endpoints; error code 40950 (+ the feature-off code); no existing field or status
  changes. Old clients are unaffected: they never send `authMethod` and never call the new
  routes. `make swagger` regenerates the spec; PR description records vocdoni.io/integrator-sdk
  impact per AGENTS.md.
- **Ordering**: SP1 merges and migrates first (0036); SP2's migration is 0037 (indexes only, no
  data). The publish guard (§6) makes a deploy that reorders them safe: certificate censuses
  cannot publish until `MigrationApplied(36)`.
- **Feature flag rollout**: deploy with `VOCDONI_CERTAUTHMODE` unset → inert. Enable per
  environment by setting the mode + host + TLS/allowlist vars. No restart-ordering constraints
  with other replicas: a replica without the config simply refuses cert-census writes, so flip
  the env on all replicas in one deploy.
- **Deployment (origin mode — the SP6 runbook's source of truth)**:
  1. DNS: wildcard record `*.cert.auth-<env>.vocdoni.io` → the auth listener's dedicated IP,
     **DNS-only (grey cloud)** or a Spectrum TCP-passthrough application; plus
     `probe.cert.auth-<env>...` covered by the same wildcard.
  2. Wildcard server certificate for `*.cert.auth-<env>.vocdoni.io` via DNS-01 (the deployed
     cert-check testui already runs this exact setup); mounted as files; **renewal requires a
     service restart** (the cert is loaded at startup).
  3. Port: the public port is 443 (owner decision 6; corporate proxies allow CONNECT only to
     443). Railway's TCP proxy assigns a random high port, so 443 needs the dedicated-IP TCP
     front (PROXY protocol supported via `--certAuthProxyProtocol`) or Spectrum. The preflight
     probe ships regardless and is the app's detector for blocked networks.
  4. Build: `GOPRIVATE=github.com/vocdoni/cert-check` plus module access (deploy token / ssh) in
     CI, the Dockerfile build stage and Railway. cert-check needs Go ≥1.27 (we are on 1.27.3).
  5. Cloudflare on the main API zone is unaffected: the new endpoints ride the existing listener.
- **Deployment (cloudflare mode)**: proxied wildcard record, BYO CA upload (FNMT + idCAT roots,
  ≤5), mTLS hostname association covering every nonce host but **not** `probe.` (granularity
  itself unverified — Q2/F16), forwarding (not blocking) configuration, zone-wide `tls_1_3: off`
  and HTTP/2 off (dedicated zone recommended), AOP zone or per-hostname certificate uploaded and
  its CA configured at the origin, CF IP allowlist, and the origin's own TLS cert/key (a
  Cloudflare Origin CA certificate covering `*.<certAuthHost>` suffices — audit F3). Blocked on
  open questions Q1–Q2; excluded from the production runbook until SP6 verifies.
- **Rollback**: unset `VOCDONI_CERTAUTHMODE` and redeploy — the feature turns off whole;
  migration 0037 holds no data worth undoing (transactions expire in ≤15 min by TTL). Published
  certificate censuses then have **no way to authenticate voters** (`auth/0` still refuses them):
  rollback during a live certificate vote is an outage for that vote, by design — note it in the
  runbook. Rolling back past SP1 follows SP1's own rollback section.
- **Old replicas during the enabling deploy**: a replica without the new binary 404s the new
  routes (standard router behaviour) and ignores `authMethod` on census writes (unknown JSON
  field) — so do not create certificate censuses until the deploy completes; the app gates on
  SP3/SP5 anyway.

## Failure modes and security

- **Untrusted input**: `state`, `code`, `verifier`, `returnUrl`, `challenge` are
  voter/attacker-controlled; all are length- and alphabet-validated before any storage access.
  The DER chain is attacker-supplied bytes: it is only ever parsed by `x509.ParseCertificate`
  and `Handler.Verify` (cert-check bounds chain length ≤10 and cert size ≤16 KiB); the listener
  additionally bounds storage to ≤10 certs / ≤200 KiB with the defined overflow behaviour of §3
  step 2 (leaf-only storage, or presented-but-empty → `invalid_chain`; audit F5), so a hostile
  client can neither bloat the collection nor turn an oversize chain into a misleading
  `no_certificate`.
- **CSRF/injection into the flow**: the code travels in a URL fragment (never sent to servers,
  stripped by the app pre-hydration); redeem requires the verifier, which never leaves the
  voter's browser — a stolen `code` alone is useless (`verifier_mismatch`, and the theft burns
  the code). An attacker-initiated create for a victim's return (login CSRF) yields a session
  bound to the *attacker's* own certificate at most — and the app displays the holder name (V5),
  making it self-evident.
- **Open redirect**: impossible by construction — the 303 target is always the transaction's
  validated returnUrl or `certAuthDefaultReturn`; `attempt_expired` redirects only to those
  (contract §13).
- **Replay/races**: state and code consumes are single `FindOneAndUpdate`s; two concurrent
  authenticates or redeems — one wins, the other sees spent state/code. The budget claim is the
  existing atomic conditional increment (`db/csp_counters.go:104`).
- **Partial failure**: a crash between state consume and the 303 leaves an authenticated,
  never-redeemed transaction — TTL reaps it in ≤5 min + lag; the voter retries from V1 (fresh
  host, fresh picker). A crash after code consume before response: the app's retry hits
  `code_used` → V1 resume (contract treats it as a non-error).
- **Revocation outages**: `revocation_unavailable` (503, retryable, budget refunded). cert-check
  fails closed on stale/unknown evidence; the backend adds nothing permissive.
- **Clock skew**: all TTL comparisons use server time on server-written timestamps; the only
  client-visible time is `expiresAt` (informative).
- **Privacy**: the DER chain (which names the holder) exists server-side ≤5 min and is `$unset`
  at redeem; no identity value is logged (LogAttrs only), stored in warnings, or placed in URLs;
  TLS 1.2's cleartext client certificate on the wire is the accepted umbrella deviation (§8 of
  the plan); the nonce host in DNS/SNI reveals only "someone authenticates against the auth
  domain".
- **Cloudflare-mode header forgery**: bounded by AOP (`RequireAndVerifyClientCert` against the
  zone's own AOP CA) + optional IP allowlist; the residual trust in Cloudflare's possession
  check is the documented, dev-team-accepted boundary (§3b).
- **Blind CSP unlinkability**: unchanged — the certificate replaces step 0 only; the CSP learns
  the member authenticated (as with email/SMS today) and the blind signature still prevents
  linking any ballot to them.
- **DoS and targeted lockout**: create is rate-limited per IP+process (configurable cap); the
  auth listener does no expensive work before the single indexed consume; Verify (the expensive
  step, ≤40 s) runs only after a valid code+verifier pair and under the per-certificate budget —
  keyed on the DER actually presented, so varying the certificate costs the attacker their own
  budget and a forged subject can never exhaust a real voter's document key (audit F1); the
  per-document key engages only after Verify proves possession. The listener shares the process with the API
  — a sustained TLS-handshake flood on a DNS-only host is the accepted origin-mode exposure
  (Spectrum is the paid answer; open question Q1).

## Acceptance criteria

All (D) criteria need Docker (`testcontainers` Mongo + Voconed). Integration tests use the
synthetic test provider and a loopback TLS auth listener on a random port with a `localhost`
SAN certificate; nonce hosts are exercised with `tls.Dial` + explicit `ServerName`/Host.

- [ ] AC1 (D): with `certAuthMode` unset, the service starts with no auth listener; the three
  new behaviours answer `ErrCertificateAuthDisabled` (`POST .../auth/cert`, `.../redeem`, census
  `authMethod: "certificate"` on create/validate), and every pre-existing API suite passes
  unmodified — `go test -run TestCertAuthDisabled ./api/` plus the full suite under AC15.
- [ ] AC2 (D): census `authMethod: "certificate"` round-trips: accepted on create/update/validate
  with `authFields ["nationalId"]` + empty `twoFaFields`, echoed on `GET /processes/{id}`
  (public) and the list; any other field combination, and unknown authMethod values, answer 400
  — `go test -run TestCertificateCensusSpec ./api/`.
- [ ] AC3 (D): validation of a certificate census: members with `missing`/`letter_mismatch`/
  `unsupported_document`/`invalid` nationalIds appear only in `nationalIdWarnings` (never in
  `missingData`, response 200); canonical duplicates answer 400 with both members in
  `data.duplicates` and `duplicate_after_normalization` warnings —
  `go test -run TestValidateCertificateCensus ./api/`.
- [ ] AC4 (D): census build: a missing-nationalId member is skipped (not a participant,
  excluded from `census.Size`); a `letter_mismatch` member is inserted and counted, and a
  certificate with the canonical twin number does not match it —
  `go test -run TestCertificateCensusBuild ./db/`.
- [ ] AC5 (D): publish guard: publishing a certificate census on a ledger without 0036 answers
  409; after `RunMigrationsUp` it publishes; `auth/0` on a certificate census answers 400 with
  `data.reason = "certificate_census"` — `go test -run TestCertificateCensusPublishGuard ./api/`
  and `go test -run TestAuthZeroCertificateCensus ./api/`.
- [ ] AC6 (D): create endpoint: 200 shape (`state` 43 chars, `authUrl` host = fresh 26-char
  base32 nonce under the configured host, `expiresAt` ≈ +15 min, `probeUrl`); returnUrl matrix
  (allowlisted origin + `/:lang`/process paths pass; foreign origin, query, fragment, userinfo,
  deep path, plain http on non-localhost fail 400); 409 on an ENDED process; 400 on a
  non-certificate census; 61st create in a minute from one IP answers 429 with
  `reason too_many_attempts` and `retryAfter` — `go test -run TestCertAuthCreate ./api/`.
- [ ] AC7: auth listener (loopback TLS, no Docker): negotiated version is TLS 1.2; the
  CertificateRequest advertises exactly the providers' CAs; ALPN is http/1.1; session tickets
  are off; a valid state answers 303 to `<returnUrl>#code=...&state=...` with
  `Cache-Control: no-store`, `Referrer-Policy: no-referrer`, `Connection: close`, both with and
  without a client certificate; the same state twice, an expired state and an unknown state
  answer 303 `#error=attempt_expired` (stored returnUrl vs default return respectively); a
  state presented on the wrong nonce host answers as unknown; `probe.<host>/ok` answers 200
  JSON with the allowlisted origin echoed in `Access-Control-Allow-Origin`; a handshake to
  `probe.<host>` sends **no** CertificateRequest (the client's `GetClientCertificate` callback is
  never invoked) while a nonce host's handshake does (audit F2); an unknown host
  answers 421 — `go test -run TestCertAuthListener ./certauth/`.
- [ ] AC8 (D): redeem happy path: member stored as `12.345.678-z` in a certificate census,
  synthetic certificate for canonical `12345678Z` → 200 with `purpose "vote"`, a non-null
  `authToken` that then passes `check`, `sign-batch` and `sign-info` exactly like an `auth/0`
  token; `weight` matches the member weight encoding; `holder`
  `{fullName, documentType, documentEnding}` correct for a DNI and an NIE fixture; `voterRef`
  stable across two attempts of the same member and different across members/processes;
  `reference` is 32 hex; the transaction's chain is absent after redeem —
  `go test -run TestCertAuthRedeem ./api/` and chain custody asserted in
  `go test -run TestCertAuthTransactionStore ./db/` (TTL index present, `$unset` on consume).
- [ ] AC9 (D): single-use and PKCE: second redeem of a code → 409 `code_used`; wrong verifier →
  403 `verifier_mismatch` and the code is consumed; redeem after codeTTL → 410
  `attempt_expired`; code redeemed against a different processId → `attempt_expired` —
  `go test -run TestCertAuthRedeemSingleUse ./api/`.
- [ ] AC10: error mapping: for each synthetic failure (`no_certificate`, `revoked`, `expired`
  with `holder`+`notAfter`+`issuer`, `unsupported_profile`, `revocation_unavailable` 503
  retryable, backend `internal` 500 with fresh reference) the envelope carries code 40950, the
  verbatim `data.reason`, and no Diagnostics/Detail/certificate material anywhere in the body;
  log output contains only LogAttrs keys — `go test -run TestCertAuthErrorMapping ./certauth/`.
- [ ] AC11 (D): budget: 10 failed redeems for one canonical document on one process → 429
  `too_many_attempts` with `retryAfter`; a different document on the same process is unaffected;
  `revocation_unavailable` outcomes do not consume budget (10 outages, then a normal attempt
  still runs); and ten failed redeems with **self-signed chains whose subject names document X**
  do not block a later genuine redeem for X — they spent per-DER keys, not X's document key
  (audit F1) — `go test -run TestCertAuthRedeemBudget ./api/`.
- [ ] AC12 (D): check-only: `purpose "check"` on an UPCOMING process redeems 200 with
  `authToken null`, no `weight`, holder + voterRef + reference present, and no CSP token in
  storage; `purpose "vote"` on UPCOMING answers the same with `purpose "vote"`; check redeems
  spend the budget — `go test -run TestCertAuthCheckOnly ./api/`.
- [ ] AC13 (D): anonymous census: a certificate census with `anonymous: true` publishes with the
  blind key and the redeemed token completes `blind-point` + `blind-sign` (the existing blind
  flow assertions of `TestProcessBlindCSP` adapted) —
  `go test -run TestCertAuthBlindCSP ./api/`.
- [ ] AC14: cloudflare mode: the listener accepts only connections presenting the AOP client
  certificate (others fail the handshake) and, when configured, only allowlisted IPs; RFC 9440
  `Client-Cert`/`Client-Cert-Chain` and legacy `Cf-Client-Cert-Der-Base64` reconstruct the same
  chain the origin-mode path stores; absent headers store "no certificate" —
  `go test -run TestCertAuthCloudflareMode ./certauth/`.
- [ ] AC15 (D): whole-repo gates: `go test -failfast -timeout=40m ./...`, `make lint`,
  `./scripts/check-qt-patterns.sh`, and `make swagger` committed (the spec documents the two
  endpoints, `authMethod`, and code 40950).
- [ ] AC16: startup validation and front plumbing (audit F8/F14): with `certAuthProxyProtocol` +
  `certAuthProxyFrontIPs`, a connection carrying a PROXY v2 header from an allowed CIDR logs the
  conveyed IP and one from elsewhere is treated as plain TCP; startup refuses a certificate not
  covering `*.<host>`, a non-https non-localhost allowed origin, `certAuthProxyProtocol` without
  front CIDRs, and the test provider against a `--vocdoniApi` host outside the allowlist —
  `go test -run TestCertAuthStartupValidation ./certauth/`.

## Constraints

- Repo conventions (AGENTS.md): `fmt.Errorf` + `%w`, lowercase structured log messages, gofumpt,
  lll 130, godoc on exported entities, quicktest matchers, Conventional Commit titles,
  `make swagger` after handler/type changes, suggest vocdoni.io + integrator-sdk follow-ups in
  the PR description.
- The contract file is normative: any field, status, reason string, TTL or grammar here that
  disagrees with `contract-cert-auth.md` v1 is a bug in this spec, and changes go through the
  contract first.
- Never log, store (outside the ≤5-min chain) or put in a URL any DNI/NIE, holder name,
  certificate subject or chain; diagnostics only via `LogAttrs()`; responses carry only §7's
  fields.
- Do not touch SP1's frozen surfaces: `db.HashAuthTwoFaFields`, `internal.HashLoginFields`,
  `migrations/login_hash.go`, and the normalisation call sites — SP2 consumes
  `NormalizeNationalID` and `CensusParticipantByLoginHash` as-is and adds no second normaliser.
- cert-check is imported (GOPRIVATE), pinned to a tagged release; its semantics are never
  reimplemented (the only local parsing of a leaf is the best-effort display helper — never used
  for authorization or budget keys, audit F1 — plus the hash of its raw DER for the
  per-certificate budget key). The handler is built once at startup; representative/employee
  constructors are never referenced.
- New migration numbered above the highest registered on both `main` and `origin/lts` at merge
  time (0037 today; re-verify on rebase); never renumber after shipping.
- Error codes are append-only; 40950 (and the feature-off code) never change meaning.
- Existing endpoints' observable behaviour is unchanged when the feature is off, and unchanged
  for non-certificate censuses when it is on.

## Open questions

(For the owner / dev team; implementation can start on origin mode while these are pending.)

1. **Q1 — Auth-host exposure (dev team, revisits owner decision 6)**: is a DNS-only (grey-cloud)
   wildcard record for `*.cert.auth-<env>.vocdoni.io`, pointing at a dedicated IP that serves
   port 443 directly, acceptable for dev/stage and production — or must all traffic stay behind
   Cloudflare? Recommendation: grey-cloud + dedicated IPv4 on 443 for dev/stage now (it is what
   the cert-check testui already runs); decide between Spectrum (Enterprise add-on, keeps L3/L4
   DDoS cover, PROXY protocol supported by `--certAuthProxyProtocol`) and staying grey-cloud for
   production when Q2's plan question is answered.
2. **Q2 — Cloudflare mode preconditions (dev team, with the owner on budget)**: (a) is the zone
   (or a dedicated auth zone) on Enterprise with API Shield mTLS (BYO CA)? (b) live-verify on a
   test zone: CA names present in the edge's CertificateRequest (picker filtering); no
   coalesced-connection picker skip across nonce hosts with HTTP/2 off; an mTLS/WAF configuration
   that requests the certificate but forwards no-cert requests to the origin (our 303
   `no_certificate` UX) instead of serving a Cloudflare block page; and that the mTLS hostname
   association covers every `<nonce>.<host>` (wildcard or rule-based — the docs state wildcard
   origin-server hostnames are unsupported in the association context, `maps/cloudflare.md` §1)
   while excluding `probe.<host>`, with the edge requesting a certificate on each new nonce
   subdomain (audit F16). Recommendation: ship SP2 with
   cloudflare mode implemented and tested at the unit level (AC14) but keep
   `certAuthMode=cloudflare` out of every deployed environment until (a) and (b) are confirmed;
   fold the verification into SP6's runbook work.
3. **Q3 — Dedicated IPv4 and TLS certificate ownership (dev team/ops)**: who provisions the
   dedicated IP / TCP front and the DNS-01 wildcard certificate per environment, and through
   which secret store do the PEM files reach the container? Recommendation: same mechanism as
   the deployed testui (Railway TCP front + DNS-01 via the existing Cloudflare API token), PEMs
   as mounted secrets; document in the SP6 runbook.
