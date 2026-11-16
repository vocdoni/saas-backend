# Certificate voter authentication: design

Status: approved (2026-11-16; audited 2026-11-11: 18 findings + delta audit 12 findings folded in)   Class: architectural
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
- D5 TLS: the gateway offers **TLS 1.3 and 1.2** (`certcheck.WithTLS13()`). Amended after the
  spec audit: there is no fallback between the two, so the gateway gets a `TLS13` switch and the
  dev test covers Windows key stores (see "Passive observers" and AC9).
- D8 (2026-11-16) Local development: the gateway gets a static-certificate mode and a test CA that
  is compiled only with the build tag `devca`, so the whole flow runs locally without an official
  certificate (C1 "Local development").
- D7 Hosting: **Railway in every environment**, through Railway's TCP proxy, which passes TLS
  untouched to the Go process. This is the setup cert-check's test UI already runs on
  (`testui/README.md` §4). Accepted trade-offs: a Railway-assigned port other than 443 in the URL,
  and ACME by DNS-01 with a Cloudflare token limited to a dedicated ACME zone. Hostnames: `cert-dev.vocdoni.net` (dev) and
  `cert.vocdoni.io` (prod). Staging uses the dev gateway, which routes each request to the dev or
  the stg backend by the `returnUrl` origin (C1 "Backends").
- D6 Pending-vote recovery: the backend returns a stable `voterRef`, and `@vocdoni/react-providers`
  gets a one-line additive change to use it (C2.5, C3).

## Problem

Organisers can already make the member `nationalId` a voter login field
(`db/types.go:386-404`, `OrgMemberAuthFieldsNationalID`). The voter proves nothing, though: they
type the value into step 0 (`csp/handlers/auth.go:270`). A census whose only login field is
`nationalId` and has no 2FA is census type `auth` (`db/types.go:451-460`). Step 0 then mints an
already-verified token (`csp/auth.go:36-39`, `createAuthOnlyToken` `csp/auth.go:466`). Anyone who
knows or guesses a member's DNI can therefore vote as that member. Spanish organisations
(associations, cooperatives, political parties) want voters to prove their identity with the
official personal certificates they already hold.

saas-backend cannot ask for a client certificate where it runs today:
- It serves plain HTTP (`api/api.go:393-407`). TLS ends at the edge. DigitalOcean App Platform
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
- Changes to `@vocdoni/api-client`. `@vocdoni/react-providers` gets one additive change (D6): in
  `auth0`, `setOwner(res.voterRef ?? await ownerHash(participant))`. `@vocdoni/api-types` adds the
  optional `certAuth`, `certCode`, `codeVerifier` and `voterRef` fields.
- Alerting and monitoring dashboards beyond the logs and metrics named here.

## Current state

- **Census model** (`db/types.go:253-269`): `Type`, `Weighted`, `Anonymous`, `GroupID`,
  `AuthFields`, `TwoFaFields`. The census type comes from `TwoFaFields` (`GetCensusType`,
  `db/types.go:451-460`). The API input is `apicommon.CensusSpec` (`api/apicommon/voting_processes.go:16-41`).
  The census is built in `resolveOrCreateDefaultCensus` (`api/processes_census.go:48`). Census
  validation and the missing-data report are in `api/processes_census_validate.go:60-99`.
- **Login hash.** `db.HashAuthTwoFaFields` (`db/types.go:486-509`) lowercases each auth-field
  value (`memberAuthFieldValue`, `db/census_missing_data.go:64-80`) and hashes the name→value map
  with unsalted SHA-256 (`internal.HashLoginFields`, `internal/utils.go:57`). Each participant
  stores `loginHash`, plus `loginHashEmail` and `loginHashPhone` when both 2FA fields are set
  (`calculateParticipantHashes`, `db/census_participant.go:860-875`). Each has a unique index per
  census. `migrations.hashMemberFields` (`migrations/login_hash.go:46`) must mirror the hash byte
  for byte; `db.TestRepairMatchesCanonicalHash` guards this.
- **Normalisation.** `OrgMember.Normalized` (`db/types.go:366-375`) only trims `NationalID`.
- **Voter flow.** The routes are at `api/routes.go:197-210` and the handlers in
  `csp/handlers/processes.go`.
  - Step 0, `authFirstStep` (`csp/handlers/auth.go:270-438`):
    1. Decode `AuthRequest` (`csp/handlers/types.go:16-28`).
    2. Refuse an anonymous census when anonymous voting is off.
    3. Build and normalise the input member.
    4. Check for missing login data.
    5. Claim the failed-attempt budget (`authAttemptKeys` `:193`, `ClaimAuthAttempt`).
    6. Look up the participant by login hash.
    7. Check the password.
    8. Run `determineContactMethod` (`:154-184`), which compares the submitted email or phone with
       the stored member.
    9. Check the weight.
    10. Call `csp.AuthToken`, which queues an OTP or mints a pre-verified token.
  - Step 1 (`authSecondStep`, `:451`) verifies the OTP or returns a pre-verified token.
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
voter browser                     cert-gateway (Railway)            saas-backend (App Platform)
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
  - **Auth listener** `AUTH_LISTEN` (default `:8443`), reached through the Railway TCP proxy at
    `https://<DOMAIN>:<port assigned to the TCP proxy>`. It serves TLS with
    `handler.TLSConfig(getCert)`, adding `certcheck.WithTLS13()` when `TLS13` is true. The
    `http.Server` has `Protocols` set to HTTP/1 only. It serves `/v1/auth` and nothing else.
  - **Main listener** `PORT` (`8080`, set as a service variable), plain HTTP with no public
    domain. It serves only `GET /readyz`: 200 once the providers are built, both listeners are up
    and the auth certificate is present and valid, else 503. Railway's healthcheck polls it from
    inside the platform. It is up from process start, so a missing certificate shows as 503, not
    as a crash.
  - The server certificate for `DOMAIN` comes from Let's Encrypt through **DNS-01 only**, using
    `github.com/caddyserver/certmagic` with the `libdns/cloudflare` provider, as cert-check's test
    UI does. HTTP-01 and TLS-ALPN-01 cannot reach a Railway-assigned port. The certificate and ACME
    account live in `DATA_DIR/certmagic` on a Railway volume, so redeploys reuse them. The auth
    listener starts once the certificate is ready, and a failed issuance is retried in the
    background with backoff. certmagic manages `DOMAIN` only, with no on-demand issuance.
  - The Cloudflare token is never zone-wide on `vocdoni.io` or `vocdoni.net`: both zones hold
    production records. `_acme-challenge.<DOMAIN>` is a CNAME to a TXT name in a dedicated
    Cloudflare zone that holds nothing else, `ACME_DNS_OVERRIDE_DOMAIN` is that full TXT record
    name (including its `_acme-challenge.` label), and the token has Zone:Read and Zone.DNS:Edit
    on that zone only. `ACME_DNS_OVERRIDE_DOMAIN` is required when `CLOUDFLARE_API_TOKEN` is set.
    Dev and prod use separate tokens. The owner enters each as a sealed Railway variable.
  - Every auth request is checked with `certcheck.CheckHost`; a mismatch answers 421. `CheckHost`
    strips the port from `Host`, so the Railway port in the URL is fine.
  - `http.Server`: `ReadHeaderTimeout` 10 s, `ReadTimeout` 15 s, `WriteTimeout` 60 s, `IdleTimeout`
    60 s, `MaxHeaderBytes` 16 KiB.
- **Providers.** `fnmt.New`, `idcat.New`, `accv.New`, `digitel.New`, `bewor.New`,
  `sectigo.New`. They share one `certcheck.NewRevocation` with `UserAgent: "vocdoni-cert-gateway/<version>"`.
  `NewHandler` fails at startup on any misconfiguration.
- **`GET /v1/auth`.** Query parameters:
  - `processId`: a Mongo ObjectID, exactly 24 lowercase hex characters (`^[0-9a-f]{24}$`).
  - `returnUrl`: an absolute `https` URL of at most 2048 bytes, with no userinfo, query or fragment.
    Its origin is exactly one of the origins listed in `BACKENDS`, and its path matches
    `^(/[a-z]{2,3}(-[A-Za-z0-9]{2,8})?)?/processes/<processId>/?$`, where `<processId>` is the
    `processId` parameter of the same request. The code is therefore only ever delivered to the
    voting page of that process, the one page that checks `state`.
  - `state`: base64url, 16–64 characters.
  - `codeChallenge`: base64url SHA-256, exactly 43 characters.
  - `lang`: optional, one of `es|ca|en`, default `es`.

  An invalid parameter gets a 400 HTML page, with no redirect, because the return URL cannot be
  trusted. Otherwise:
  1. Run `handler.Auth(r.WithContext(ctx))` with a 40 s deadline.
  2. On `*certcheck.Error`, redirect 303 to `returnUrl#certError=<code>&state=<state>`. Log
     `Diagnostics.LogAttrs()` plus `DiagnosticID`.
  3. On success, POST the assertion to the backend selected by the `returnUrl` origin (C2.4) with
     a 10 s deadline.
     - 201: redirect 303 to `returnUrl#certCode=<code>&state=<state>`.
     - A backend error code: redirect 303 to `returnUrl#certError=<backendCode>&state=<state>`.
       `backendCode` is one of `not_in_census`, `zero_weight`, `not_cert_census`,
       `anonymous_unavailable`, `process_not_open`, `rate_limited` or `backend_unavailable`
       (see the response contract in C2.4).

  The parameters travel in the **fragment**, so they never reach any server log or `Referer`.
- **Response hardening** on every response:
  - `Cache-Control: no-store`
  - `Referrer-Policy: no-referrer`
  - `X-Frame-Options: DENY`
  - `Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'`
- **Rate limit.** Two in-process token buckets on `/v1/auth`; an exceeded limit gets a 429 page.
  - Per client IP (`RemoteAddr`): 60 requests a minute with a burst of 30. Entries idle for 10
    minutes are evicted, and the map is capped at 100 000 entries (oldest evicted first).
  - Global: `RATE_LIMIT_GLOBAL_PER_MIN` (default 1200) requests a minute, counted only for
    requests that present a client certificate (`len(r.TLS.PeerCertificates) > 0`), after the
    per-IP bucket. It caps the load on the CAs' OCSP/CRL endpoints and on the backend. A request
    without a certificate never touches it.
  - The Railway TCP proxy's documentation does not say whether `RemoteAddr` is the client's
    address. At `LOG_LEVEL=debug` the gateway logs `remoteAddr` for each auth request, and the
    owner compares it in dev with their own public IP. If every request shows the proxy's
    address, the per-IP bucket is switched off by config (`RATE_LIMIT_PER_MIN=0`) and the global
    bucket stays.
- **Backends.** One gateway can serve several backends, so the dev gateway serves both dev and stg.
  `BACKENDS` is a JSON array, read once at startup:

  ```json
  [{"name":"dev","url":"https://saas-api-dev.vocdoni.net","keyId":"dev-1","keyEnv":"SIGNING_KEY_DEV",
    "origins":["https://app-dev.vocdoni.io"]},
   {"name":"stg","url":"https://saas-api-stg.vocdoni.net","keyId":"stg-1","keyEnv":"SIGNING_KEY_STG",
    "origins":["https://app-stg.vocdoni.io"]}]
  ```
  - `BACKENDS` holds no secret and is a plain variable. `keyEnv` names a sealed variable whose
    value is the base64 of the 32-byte Ed25519 seed.
  - The `returnUrl` origin selects the backend. Origins are compared in canonical form:
    `https://` plus the lowercase host, with no path, no trailing slash and no default port. An
    origin in `BACKENDS` that is not already in that form fails startup, and the origin of
    `returnUrl` is canonicalised the same way before the lookup.
  - Startup fails, naming the entry and never printing a key, on: invalid JSON or an unknown
    field; an empty list; an entry with an empty `name`, `keyId` or `origins`; a duplicate `name`,
    `keyId`, `url`, `keyEnv`, key value or origin; a `url` that is not `https` or has a path,
    query or userinfo; a `keyEnv` variable that is unset or does not decode to 32 bytes.
  - The set of allowed return origins is the union of every entry's `origins`. There is no separate
    allowlist variable.
  - The production gateway has a single entry for the prod backend, listing `https://app.vocdoni.io`
    and every custom organisation domain.
  - Each backend has its own key pair. A key is never configured on two backends, so a request
    signed for one is rejected by the other.
  - Logs and metrics carry the backend `name`.
- **Request to the backend.**
  - The entry's `url` is the backend's public hostname behind Cloudflare, so the edge adds
    `X-Origin-Secret` as it does for every client. The gateway never holds the origin secret. The
    request URL is `url + "/cert-auth/assertions"`.
  - Key: the Ed25519 seed in the entry's `keyEnv` variable, with id `keyId`.
  - Header: `X-Cert-Gateway-Signature: t=<unix seconds>,k=<keyId>,v1=<base64url(sig), no padding>`.
  - The signature covers the bytes of
    `"cert-gateway-v1\n" + t + "\n" + "POST" + "\n" + "/cert-auth/assertions" + "\n" + hex(sha256(body))`.
    Method and path are these literals on both sides: the backend does not read them from the
    request, so a path prefix or rewrite at the edge cannot break verification. `body` is the raw
    request body; the backend hashes the bytes it read (bounded at 4 KiB) before decoding JSON.
  - `Content-Type: application/json`. `User-Agent: vocdoni-cert-gateway/<version>`.
- **Backend check at startup.** For each `BACKENDS` entry the gateway sends one unsigned
  `POST url + "/cert-auth/assertions"` with an empty body and a 10 s deadline, and logs
  `backend check` with the entry `name` and the HTTP status: info for 401 or 404, error for
  anything else or a transport failure. It does not block startup or `/readyz`.
- **Configuration** (env):

  | Variable | Meaning |
  |---|---|
  | `DOMAIN` | the gateway's hostname |
  | `AUTH_LISTEN` | auth listener address, default `:8443` |
  | `PORT` | main listener port, `8080` |
  | `ACME_EMAIL` | contact email for Let's Encrypt |
  | `ACME_CA` | `production` or `staging` (first issuance uses `staging`) |
  | `DATA_DIR` | volume mount, default `/data` |
  | `CLOUDFLARE_API_TOKEN` | DNS-01 token for the dedicated ACME zone only (sealed) |
  | `ACME_DNS_OVERRIDE_DOMAIN` | required: full TXT record name in the dedicated ACME zone that `_acme-challenge.<DOMAIN>` is a CNAME to |
  | `BACKENDS` | JSON array of backends: name, url, keyId, keyEnv and return origins (not secret) |
  | `SIGNING_KEY_<NAME>` | one per backend, named by its `keyEnv`: base64 of the Ed25519 seed (sealed) |
  | `RATE_LIMIT_PER_MIN` | default 60; `0` turns the per-IP bucket off |
  | `RATE_LIMIT_GLOBAL_PER_MIN` | default 1200 |
  | `TLS13` | `true` (default) offers TLS 1.3 and 1.2; `false` offers 1.2 only. A restart applies it. |
  | `LOG_LEVEL` | log verbosity; `debug` adds `remoteAddr` per auth request |

- **Logging.** The gateway never logs the document number, names, the certificate, `returnUrl`
  or `state`. It logs the outcome code, provider id, backend name, `DiagnosticID` and latency.
- **Pages.** The 400 and 429 HTML pages are static and localised (es, ca, en), with no
  JavaScript.
- **Deployment.**
  - Host: Railway, one project per environment (`cert-gateway-dev`, `cert-gateway-prod`), each with
    one service: 1 replica, a volume at `/data`, a TCP proxy to `AUTH_LISTEN`, no public HTTP
    domain, `PORT=8080`, healthcheck `/readyz` with a 600 s timeout, restart on failure (10
    retries), Dockerfile builder, and wait for CI. The settings follow cert-check's test UI
    (`testui/README.md` §4 "Railway service settings"). Railway mounts volumes as root, so the
    container runs as root (`RAILWAY_RUN_UID=0`), as the test UI does.
  - Region: Railway's EU West region (Amsterdam) for both services. The service is never moved
    to a region outside the EU.
  - Deploy trigger: the dev service tracks `main`. The prod service tracks the branch `release`,
    which only the owner advances, by fast-forward to a commit of `main` that dev has run.
  - Private module: the Dockerfile's build stage declares `ARG CERT_CHECK_READ_TOKEN` and uses it
    to fetch `github.com/vocdoni/cert-check` (`GOPRIVATE`, a git `insteadOf` URL). The value is a
    sealed service variable holding a read-only token for that one repository. The final stage
    does not contain it.
  - Expected cost: about 50–100 MB of RAM and almost no CPU, so roughly $1–2 a month of Railway
    usage per environment, usually inside the plan's included usage.
  - Hostnames are DNS-only (grey-cloud) CNAME records on Cloudflare pointing at the Railway TCP
    proxy hostname: `cert-dev.vocdoni.net` (dev) and `cert.vocdoni.io` (prod). The public URL
    carries the Railway-assigned port, for example `https://cert.vocdoni.io:15140`.
    `CERT_GATEWAY_URL` in vocdoni-app includes that port. If the TCP proxy is ever recreated, the
    port and possibly the proxy hostname change: `CERT_GATEWAY_URL` and the CNAME are updated in
    the same change.
  - Staging has no gateway of its own: app-stg's `CERT_GATEWAY_URL` points at `cert-dev.vocdoni.net`,
    whose `BACKENDS` has a `stg` entry, and the stg backend holds the stg public key.
- **CI.** Run `go test -race ./...`, golangci-lint and a Docker build, with
  `GOPRIVATE=github.com/vocdoni/cert-check` and a read token for the private cert-check module.
  CI publishes no image: Railway builds the Dockerfile.
- **Local development (D8).** Everything runs on one machine, with no cloud service.
  - **Static server certificate.** `TLS_CERT_FILE` and `TLS_KEY_FILE` replace ACME, for example
    with a mkcert certificate for `cert.localtest.me`. They are mutually exclusive with
    `CLOUDFLARE_API_TOKEN` and `ACME_DNS_OVERRIDE_DOMAIN`, and startup fails if both sets are
    given. This mode is in every build, so the production image can also run without ACME.
  - **Test CA, build tag `devca` only.** `internal/devca` implements a `certcheck.Provider`
    (id `devca`) that accepts client certificates issued by a local CA whose certificate is in
    `DEVCA_CERT_FILE`.
    - The provider skips revocation and reads the DNI/NIE from the subject `serialNumber`
      (`IDCES-<number>`), applying the same canonical form as cert-check.
    - It is added to the handler only when the binary is built with `-tags devca` **and**
      `DEVCA_CERT_FILE` is set.
    - The Dockerfile and every image build never pass the tag. CI runs the untagged suite and, as
      a separate test-only step, `go vet -tags devca ./... && go test -race -tags devca ./...`,
      which builds no image (owner, 2026-11-16). A test builds the binary without the tag and
      checks that a `devca` client certificate gets `untrusted_issuer` and that `devca` is not
      among the provider ids.
    - A `devca` binary logs a warning at startup and answers `X-Cert-Gateway-Dev: devca` on every
      response.
  - **Scripts.** `scripts/devca` creates the local CA (once, under `~/.cache/cert-gateway/devca`,
    never in the repo). Given a DNI or NIE and a name, it issues a client certificate as a
    PKCS#12 file with a printed password, ready to import into a browser.
  - **Stack.** `make dev` or a `docker-compose.dev.yml` in the cert-gateway repo starts Mongo, a
    local saas-backend with test gateway keys, the gateway (`devca`, static certificate, `BACKENDS`
    pointing at the local backend with origin `http://localhost:3000`) and documents running
    vocdoni-app with `CERT_GATEWAY_URL=https://cert.localtest.me:8443`.
    - The `https`-only rules get two exceptions, both only in a `devca` binary (owner, 2026-11-16):
      an origin `http://localhost:<port>`, and a backend `url` `http://localhost:<port>` (the local
      saas-backend serves plain HTTP). A binary without the tag rejects both at startup.
    - The README walks through: create the CA, issue a certificate, import it, create a member with
      that DNI and a cert census, then vote.
  - A real certificate (FNMT, idCAT…) also works locally against a binary without `devca`. It
    needs internet for OCSP/CRL and the DNI in the local memberbase.

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
  1. Uppercase the value (`strings.ToUpper`) and remove every rune for which `unicode.IsSpace` is
     true, plus `.`, `-`, `_` and `/`.
  2. If the result matches `^0*([0-9]{1,8})([A-Z])$` and the letter is the check letter of the
     number, return the digits left-padded to 8 plus the letter, `ok=true`.
  3. If it matches `^([XYZ])0*([0-9]{1,7})([A-Z])$` and the letter is the check letter (X=0, Y=1,
     Z=2 prefixed), return the prefix, the digits left-padded to 7 and the letter, `ok=true`.
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
- A single helper decides which fields are **hashed** for a census:
  `func (c Census) LoginHashFields() (OrgMemberAuthFields, OrgMemberTwoFaFields)`. For `CertAuth` it
  returns `(["nationalId"], nil)`; otherwise `(c.AuthFields, c.TwoFaFields)`.
  `calculateParticipantHashes` is its only caller, and every other hash site already goes through
  `calculateParticipantHashes` (`db/census_missing_data.go:139`, `db/census_participant.go:184,565`,
  `db/org_members.go:625`).
- **Missing data is not hashing.** `MissingLoginData` keeps receiving `census.AuthFields` and
  `census.TwoFaFields`, so a member with no 2FA channel is still left out of a cert census with 2FA.
- The validation path takes the flag too: `CheckGroupMembersFields`, `CheckMembersFields` and
  `aggregateMemberFields` gain a `certAuth bool` parameter (they take field lists today, not a census).
- The repair tooling mirrors the helper: `migrations.censusHashDoc` gains
  `CertAuth bool \`bson:"certAuth"\``, and `computeParticipantHashes` hashes `(["nationalId"], nil)`
  and emits no email or phone hash when it is set. `TestRepairMatchesCanonicalHash` gains cert-census
  cases (no 2FA, email, email+phone) comparing `computeParticipantHashes` with
  `calculateParticipantHashes`.
- For a cert census, a member whose `nationalId` gives `ok=false` is treated exactly like a member
  with missing login data: it could never log in. `MissingLoginData` gains that test when called for
  a cert census, so such members are listed in the existing `missingData` array, make
  `POST /processes/census/validation` answer 400, and are left out of the census and returned in the
  `missing` list at create, update and `PUT /processes/{id}/census`. No new response field is added.

**C2.4 Assertion endpoint** (internal, gateway only).
- Route: `POST /cert-auth/assertions`. The body is at most 4 KiB:

  ```json
  {"processId":"<hex24>","nationalId":"<9 chars>","documentType":"DNI|NIE",
   "provider":"fnmt","profile":"fnmt-citizen-g2","certSha256":"<hex64>",
   "codeChallenge":"<b64url 43>"}
  ```
- **Authentication.**
  1. Verify `X-Cert-Gateway-Signature` against `CERT_GATEWAY_PUBLIC_KEYS`, a comma-separated list
     of `keyId:base64(pubkey)` entries so keys can be rotated.
  2. Require `|now − t| ≤ 60 s`.
  3. Compare in constant time.
  4. Without keys configured, the route answers 404 (feature off).
- **Steps.** Each failure maps to a code of the response contract below:
  0. Parse `processId` with `bson.ObjectIDFromHex`, else 400.
  1. Load the process. It must exist and be published (`vp.Published`, the same test step 0
     applies), else `process_not_open`.
  2. Its census must have `CertAuth`, else `not_cert_census`.
  3. Refuse an anonymous census exactly as `anonymousCensusRefused` does.
  4. Compute `CanonicalNationalID(nationalId)`. It must give `ok=true`, else 400.
  5. Look up the participant by `HashAuthTwoFaFields({NationalID}, ["nationalId"], nil)`, else
     404 `not_in_census`.
  6. Check the weight, else `zero_weight`.
  7. Spend the rate budget: at most 10 assertions per participant per process per hour, else 429
     `rate_limited`. The budget is a `ClaimCSPCounter` counter keyed by process and participant id,
     like the step-0 budgets.
  8. Create the assertion and answer `201 {"certCode":"<b64url 32 random bytes>","expiresAt":…}`.
- **Response contract.** Success is `201` with `{"certCode":"…","expiresAt":"<RFC 3339 UTC>"}`.
  Every failure is JSON `{"error":"<code>"}` with these statuses and no other body fields:

  | Status | `error` | When |
  |---|---|---|
  | 400 | `bad_request` | malformed body, bad `processId`, `nationalId` with `ok=false` |
  | 401 | `bad_signature` | missing, malformed, stale, unknown-key or invalid signature |
  | 404 | (plain 404, no body) | no keys configured |
  | 404 | `process_not_open` | process unknown or `!vp.Published` |
  | 409 | `not_cert_census` | census without `CertAuth` |
  | 409 | `anonymous_unavailable` | anonymous census while blind CSP is off |
  | 404 | `not_in_census` | no participant for the DNI |
  | 403 | `zero_weight` | weighted census, member weight 0 |
  | 429 | `rate_limited` | assertion budget spent |

  The gateway forwards to the browser only `process_not_open`, `not_cert_census`,
  `anonymous_unavailable`, `not_in_census`, `zero_weight` and `rate_limited`. Anything else —
  another code, a non-JSON body, a 5xx, a timeout — becomes `backend_unavailable`, and the gateway
  logs the status.
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
  1. Shape check, before anything is consumed: the request must carry `certCode` (43 characters,
     base64url) and `codeVerifier` (43–128 characters, base64url), and `name`, `surname`,
     `memberNumber`, `nationalId`, `birthDate` and `password` must be empty. Otherwise
     `ErrInvalidUserData`, and no code is consumed.
  2. Consume the assertion atomically:
     `FindOneAndUpdate({codeHash, processId, usedAt: null, expiresAt > now}, {$set: {usedAt: now}})`.
  3. Check `base64url(sha256(codeVerifier)) == codeChallenge` in constant time.
  4. A code that is unknown, expired, used or of another process, or a verifier that does not match,
     answers the bare `ErrCSPAuthFailed`.
  5. Load the `OrgMember` by `participantId`, then continue the existing flow from
     `determineContactMethod`: the submitted email or phone against the stored member, the weight,
     then `csp.AuthToken`.
  6. The failed-attempt budget key is the participant's `loginHash`. It is claimed before the
     contact comparison, as today.
  7. A consumed code is spent even if the contact check then fails. The voter goes through the
     certificate step again.
  8. The step-0 response of a cert census carries `voterRef` (D6): lowercase hex of
     `sha256("vocdoni-voter-ref:" + processId hex + ":" + participantId hex)`. It is the same on
     every login of one voter to one process, so the client can recognise that voter's pending
     votes. `AuthResponse` gains `VoterRef string \`json:"voterRef,omitempty"\``.
- **Census without `CertAuth`.** A request carrying `certCode` is refused with `ErrInvalidUserData`.
- **Unchanged.** Step 1, resend, sign and blind-sign do not change. The token is bound to the
  member id and process exactly as today.

**C2.6 Migration.**
- A registered migration calls `RepairLoginHashes(ctx, db, RepairOptions{Apply: true})` and logs its
  report, exactly as migration 0025 does. With `hashMemberFields` canonicalising `nationalId`
  (C2.2), that rewrites the participants whose hash changed and nothing else. No new selection or
  collision code is written.
- Collisions follow the existing rule: rows of one census that share a recomputed hash are all
  skipped and listed in `RepairReport.SkippedMembers`. They are members stored twice under two
  spellings of one DNI. They keep their old hashes, but a login always hashes the canonical form,
  so of such twins only one stored in canonical form still logs in; the others cannot (typed or by
  certificate) until the organiser removes the duplicate and `scripts/repairlogins -apply` rehashes
  the row that is left. The migration log line carries the count, and the duplicates of open
  processes are removed before each deploy (corrected after the plan pre-flight audit).
- It is idempotent (`RepairLoginHashes` is). Its `Down` is a no-op, as 0025's is: the old hashes
  come back by running the previous release's `scripts/repairlogins -apply`.
- Before the deploy, the owner runs `scripts/repairlogins` (dry run) built from this branch against
  dev, stg and then prod and reads the collision count. The migration runs in every environment,
  because it changes typed `nationalId` logins too.
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
  - For a cert census, the existing missing-data alert reads "missing or invalid DNI/NIE".
- **Voter side**, when `election.census.certAuth`:
  1. Step 0 shows a single "Acceder con certificado digital" button instead of the login fields.
  2. On click, generate `state` (16 random bytes) and `codeVerifier` (32 random bytes, base64url).
     Store both in `sessionStorage` under `certAuth:<processId>`, then navigate (top level) to
     `${CERT_GATEWAY_URL}/v1/auth` with
     `returnUrl = location.origin + localizedProcessPath(processId)` (the `/[lang/]processes/<id>`
     route, also when the voter started from a home-process root page).
  3. On page load with `#certCode`/`#certError`, compare `state` with the stored one, then strip
     the fragment (`history.replaceState`) and delete the `sessionStorage` entry. If `state` does
     not match, or nothing is stored, strip the fragment, show the generic error and call nothing.
     The stored entry is deleted only on a match.
     - For a census without 2FA, call `auth0({certCode, codeVerifier})`.
     - For a census with 2FA, show the existing email or phone input, call
       `auth0({certCode, codeVerifier, email|phone})`, then the existing Step1.
  4. Show `#certError` codes as localised messages (see "Sticky certificate choice").
  5. The terms-and-privacy checkbox stays above the certificate button and is required before the
     redirect.
  6. The certificate path never calls `pushCrispUserFields`.
  6b. Under the certificate button, a short note says that if the certificate page does not load,
     the voter should try from another network, such as mobile data (see "Non-standard port").
  7. `feat/cert-auth` in vocdoni-app is cut from `origin/develop` (react-providers 4.x), and bumps
     react-providers to the release carrying the D6 change.
- **react-providers** (D6): `auth0` sets `owner` to `res.voterRef` when present, else to
  `ownerHash(participant)` as today. Released before the app.
- **Configuration and strings.** `CERT_GATEWAY_URL` is a new env var. The option is hidden when it
  is unset. Strings go in en, es and ca. The other locales take English through the existing
  extraction.
- **Gateway allowlist.** The gateway's `BACKENDS` origins must list every app origin and every
  custom organisation domain that serves voting pages, each under the backend it talks to.

### Alternatives considered

| Option | Why not |
|---|---|
| Certificate check inside saas-backend, moving it to a Droplet/DOKS with an LB in TLS passthrough | Migrates the production service's hosting for one feature; the LB costs $15+/month (D1) |
| Cloudflare Enterprise mTLS forwarding `Client-Cert` to App Platform | Enterprise only; 5 CAs per account; no revocation check for BYO CAs; whether it advertises the CA list and works with smart-card signatures is undocumented (D1) |
| Gateway signs an attestation that travels through the browser to the backend | Puts the DNI in the browser and in URLs; the server-to-server call keeps it between the two services |
| Gateway redirects with the auth token itself | A token in a URL is a bearer credential. The single-use code plus the PKCE verifier makes a leaked or misdirected code useless |
| Gateway on a DigitalOcean Droplet (port 443, ACME HTTP-01, ~$6/month per environment) | Owner chose Railway in every environment (D7): no VM to maintain, same setup as cert-check's test UI |
| Gateway code inside saas-backend (`cmd/`) or inside cert-check | Owner chose a dedicated repo (D2) |
| Certificate-only census with no 2FA option | Owner chose optional 2FA (D3) |
| Rewriting stored `nationalId` values to the canonical form | Changes what organisers see and export; folding at hash time follows the existing rule in `HashAuthTwoFaFields` |

## Compatibility and rollout

- **API.** `certAuth`, `certCode` and `codeVerifier` are additive and omitted when empty. Old
  clients never send them and old censuses lack the field, so they behave as today.
- **Login hashes.** After C2.6, typed logins match more leniently, because both sides are
  canonicalised. A member stored as `1234567l` now logs in typing `01234567-L`. Values that are not
  a valid DNI or NIE hash exactly as before.
- **Rollout order:**
  1. saas-backend with the feature off (no keys), migration run on dev.
  2. cert-gateway on Railway dev. Before the first issuance:
     - Check that no CAA record on the hostname's zone excludes `letsencrypt.org`.
     - Check that the assigned port is not on Chromium's restricted-port list or Firefox's banned
       list. If it is, recreate the proxy.

     Issue first with `ACME_CA=staging`, then `production`.
  3. Backend dev keys.
  4. Read the gateway's `backend check` log line (`railway logs`). The status must be 401 (bad
     signature): not 403 (origin secret or a Cloudflare WAF/bot challenge) and not 404 (keys not
     set).
     - If it is 403, add a Cloudflare WAF custom rule with action Skip (managed rules, rate
       limiting rules and Super Bot Fight Mode). It matches requests whose path is
       `/cert-auth/assertions` and that carry `X-Cert-Gateway-Signature`. Then redeploy the gateway
       and read the line again.
     - Railway egress IPs are shared and not fixed, so the rule cannot rely on the source IP. The
       signature is what authenticates.
     - If the zone runs the free Bot Fight Mode, a skip rule does not apply to it: stop and ask the
       owner.
  5. In dev, set `LOG_LEVEL=debug`, make two auth requests from two networks and compare the
     logged `remoteAddr` values with the clients' public IPs. If they are not the clients'
     addresses, set `RATE_LIMIT_PER_MIN=0` on every gateway. Reset `LOG_LEVEL` to `info`.
  6. react-providers release with the D6 change (and api-types fields).
  7. vocdoni-app dev with `CERT_GATEWAY_URL`.
  8. End-to-end test with real certificates, run manually by the owner (AC9).
  9. Staging: migration, stg keys on the stg backend, the `stg` entry in the dev gateway's
     `BACKENDS`, the `backend check` line for `stg` showing 401, then app-stg with
     `CERT_GATEWAY_URL=https://cert-dev.vocdoni.net:<port>`.
  10. Production: the same steps on its own gateway (`cert.vocdoni.io`), deployed by the owner
      advancing the `release` branch.
  11. Every deploy and DNS change waits for the owner's OK.
- **Rollback.**
  - Unset `CERT_GATEWAY_PUBLIC_KEYS`. The assertion route returns 404 and new cert censuses are
    refused. Existing cert censuses can no longer authenticate, so the owner decides this during
    open votes.
  - Rolling the backend back past C2.2 makes typed logins of non-canonical stored IDs fail until
    the previous release's `scripts/repairlogins -apply` is run. A rolled-back backend starts
    against a newer migration ledger (`db/migrations.go:87-125` only applies versions above the
    stored one), which is safe.
  - The gateway has no state, so it can be stopped at any time.

## Failure modes and security

- **Trust boundary.** The gateway is trusted to assert a DNI. A compromised gateway or signing
  key can mint assertions for any DNI in any cert census. Mitigations:
  - Each signing key lives only in a sealed variable of the Railway service. The dev gateway holds
    the dev and the stg key, so the stg backend trusts whatever `main` deploys to dev.
  - The backend accepts several keys, so a key can be rotated without downtime.
  - Access to each Railway project is limited to the people who operate it. The Cloudflare DNS
    token can write only to the dedicated ACME zone, so a leak cannot change any record of
    `vocdoni.io` or `vocdoni.net`.
  - Assertions are logged per participant, so they can be audited.
  - Accepted residual risk: this is the same trust a CSP already holds.
- **Code theft.** The `returnUrl` allowlist stops open redirects. The code travels in the
  fragment, is single-use and expires in 5 minutes. PKCE binds the code to the browser that started
  the flow. A flow started by an attacker is not bound to the victim, so the defence there is the
  `returnUrl` path rule: the code only reaches the voting page, which discards it because `state` is
  not the one it stored. `state` also blocks login CSRF, where a victim's tab would be fed the
  attacker's code.
- **Sticky certificate choice.** Browsers remember the chosen certificate, or the choice of none,
  for the gateway's host until they are restarted. For every non-retryable code (`no_certificate`,
  `not_in_census`, `zero_weight`, `not_cert_census`, `anonymous_unavailable`, `process_not_open` and
  every cert-check code except `revocation_unavailable` and `internal`: `provider_mismatch`,
  `untrusted_issuer`, `invalid_chain`, `expired`, `not_yet_valid`, `unsupported_profile`,
  `unsupported_purpose`, `weak_algorithm`, `test_certificate`, `identity_invalid`,
  `identity_unsupported_document`, `revoked`, `revocation_unknown`) the voter UI says, in the
  voter's language, that to use a different certificate they must close every window of the
  browser and open the voting link again (owner, 2026-11-16). The retry button stays, for the retryable codes
  (`revocation_unavailable`, `internal`, `backend_unavailable`, `rate_limited`).
  Not done for now: a fresh hostname per attempt would avoid the restart. DNS-01 makes a wildcard
  certificate possible, so this is a later option if support load shows it is needed.
- **Replay to the backend.** The signed request carries a timestamp valid for 60 s and travels
  over TLS. A replay can only create a new code, which goes back to the replayer and is useless
  without the verifier.
- **Passive observers.** TLS 1.3 encrypts the client certificate; TLS 1.2 sends it in clear. There
  is no fallback between them: a browser that negotiates 1.3 with a key that cannot sign RSA-PSS
  fails the handshake and cannot authenticate. `TLS13=false` is the switch if AC9 or production
  shows such keys.
- **Untrusted input.** cert-check bounds the chain and certificate sizes. The gateway bounds and
  validates its query parameters, and the backend bounds the body and validates every field. A
  revocation lookup that is unavailable fails closed with a retryable error, and the UI says "try
  later".
- **Availability.** The gateway sits behind Railway's TCP proxy with no Cloudflare in front: the
  rate limits apply. Accepted residual risk: Railway offers no filtering in front of the TCP proxy,
  and with the per-IP bucket off a client that holds any certificate can spend the global bucket
  and block certificate logins until it stops.
  A redeploy stops the gateway for a short time, because a Railway service with a volume cannot
  run two deployments at once. Variable changes redeploy, so `BACKENDS` and other variables are
  not changed on the production gateway while a certificate vote is open.
  Gateway downtime only blocks cert censuses. If the backend is unreachable, the
  gateway answers `backend_unavailable`.
- **Non-standard port (D7).** The auth URL uses a Railway-assigned port other than 443. Networks
  that only allow outbound 80/443 (some corporate, hotel or university networks) cannot reach it.
  The voter then sees a browser connection error that the app cannot detect. Mitigation: the voter
  UI's certificate step tells the voter, in their language, that if the page does not load they
  should try from another network (for example mobile data). Moving to a 443 host (a Droplet or a
  Railway option for 443 TCP, if one appears) needs only a new `CERT_GATEWAY_URL` and DNS record.
- **Concurrency.** Assertions are consumed with one atomic `FindOneAndUpdate`, so two redemptions
  of one code cannot both succeed. Step-0 budgets keep their existing claim and release semantics.
- **Privacy.** The DNI exists only in the gateway's memory, in the signed request body and in the
  backend's lookup. It is never stored in assertions, never logged and never placed in URLs or
  browser storage. Railway is the processor that hosts the gateway, in the EU region. It sees the
  TLS stream, which with TLS 1.2 includes the client certificate.

## Acceptance criteria

- [ ] AC1 `internal.CanonicalNationalID` table test: DNI and NIE with case, spaces, tab and U+00A0
  separators, dots and hyphens, short digits, `001234567L` (and `001234567Z` as an invalid letter), `X01234567L`, an invalid letter, a
  foreign ID and the empty string.
- [ ] AC2 Hash parity: `TestRepairMatchesCanonicalHash` passes with the canonical `nationalId`,
  including cert-census cases (no 2FA, email, email+phone). A new test shows a typed `1234567-l`
  matching a stored `01234567L`. For a cert census with email+phone 2FA, a dry run of
  `RepairLoginHashes` reports zero rows to rehash.
- [ ] AC3 Census validation tests:
  - `certAuth` with `authFields≠[nationalId]` is rejected.
  - `certAuth` with the feature off is rejected.
  - `certAuth` with each 2FA subset is accepted.
  - A member with an invalid DNI is reported in `missingData` and left out of the census.
  - A member without an email is left out of a cert census with email 2FA.
- [ ] AC4 Assertion endpoint tests:
  - Valid signature leads to 201.
  - Bad or old signature, an unknown key and the feature off are each rejected.
  - Each row of the response contract, with its exact status and body.
  - No DNI is present in the stored document.
- [ ] AC5 Step-0 tests:
  - Cert census without 2FA: `{certCode, codeVerifier}` gives a verified token, then sign
    succeeds.
  - Cert census with email 2FA: an OTP is queued, then step 1 succeeds.
  - Each of these fails with `ErrCSPAuthFailed`: wrong verifier, expired code, reused code and a
    code from another process.
  - Typed login fields, or no `certCode`, on a cert census are rejected with `ErrInvalidUserData`
    and the code stays unused.
  - `certCode` on a non-cert census is rejected.
  - Two concurrent redemptions give exactly one success.
  - Two certificate logins of one voter return the same `voterRef`; two voters differ.
- [ ] AC6 Migration test: non-canonical IDs are rehashed, collisions keep their old hashes and are
  counted, and a second run changes nothing.
- [ ] AC7 Gateway tests, using a fake `certcheck.Provider` and an `httptest` TLS server with
  client certificates:
  - Valid certificate leads to a signed POST, then a 303 to `returnUrl#certCode`.
  - Each `certcheck` error code leads to `#certError`.
  - A disallowed `returnUrl` or a bad parameter gets a 400 with no redirect. So does a `returnUrl`
    on an allowed origin with any other path, a query, or another process id.
  - An SNI name other than `DOMAIN` gets no certificate and triggers no ACME order (certmagic
    is configured for `DOMAIN` only, with no on-demand issuance).
  - `/readyz` answers 503 until the auth certificate is present and valid, then 200.
  - A `Host` that differs from the SNI name gets a 421.
  - The signature verifies against the backend's verifier, using shared test vectors.
  - With two `BACKENDS` entries, a `returnUrl` on each origin posts to its own backend with its own
    key. A configuration with a duplicate origin or name fails at startup.
  - Over the per-IP limit gets a 429. With `RATE_LIMIT_PER_MIN=0` the per-IP bucket never
    refuses. Over the global limit, a request with a client certificate gets a 429 and a request
    without one still gets its `no_certificate` redirect.
  - Each startup failure of `BACKENDS` listed in C1 fails with a message that names the entry and
    contains no key material.
  - A `returnUrl` whose origin differs from a configured one only by host case or `:443` selects
    that backend.
  - The startup `backend check` logs the entry name and status for 401, 404, 403 and a transport
    error, and never blocks `/readyz`.
  - A captured log contains no document number.
- [ ] AC8 vocdoni-app tests:
  - The organiser option builds `certAuth:true` with `["nationalId"]` and round-trips the draft.
  - The voter flow stores and checks `state`, strips the fragment, and calls `auth0` with
    `certCode`/`codeVerifier` (plus contact when 2FA).
  - Error codes render localised messages. Each non-retryable code renders the "close the
    browser" instruction; each retryable code renders the retry button.
  - A `state` mismatch strips the fragment and calls nothing.
  - After a reload and a second certificate login in the same tab, a pending vote saved before the
    reload is re-sent (react-providers test with `voterRef`).
- [ ] AC9 Manual end-to-end on dev (`cert-dev.vocdoni.net`), run by the owner, with `TLS13=true`: an FNMT and an idCAT
  software certificate, each on Windows (Chrome or Edge, certificate in the Windows store), on
  Firefox (its own store) and on macOS or Android. Auth-only and email-2FA censuses, plus a
  certificate whose DNI is outside the census. Cancel the picker once and confirm the message
  shown. Any handshake failure is recorded with the gateway's TLS log line; if one occurs,
  production starts with `TLS13=false`.
- [ ] AC10 Gateway: a test builds the production handler and asserts its provider ids are exactly
  `fnmt, idcat, accv, digitel, bewor, sectigo`, and that none has `Info().Beta`.
- [ ] AC11 No PII: a backend test captures the log of a full assertion + step-0 run and finds no
  document number, name, email or phone; a vocdoni-app test asserts `sessionStorage` holds only
  `state` and `codeVerifier` during the flow and that the cert path never calls
  `pushCrispUserFields`.
- [ ] AC12 Local development:
  - A binary built without `-tags devca` rejects a `devca` client certificate with
    `untrusted_issuer`, lists no `devca` provider, and refuses an `http://localhost` origin in
    `BACKENDS`.
  - With `-tags devca` and `DEVCA_CERT_FILE`, a certificate from `scripts/devca` for a member's DNI
    completes the flow against a local backend in a test.
  - `make dev` (or the compose file) starts the stack, and the README walkthrough ends with a cast
    vote. The owner checks this manually once.

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

None. Hosting and hostnames are owner decisions (D7); staging shares the dev gateway.
