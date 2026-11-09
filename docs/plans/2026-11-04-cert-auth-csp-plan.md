# Certificate voter authentication in the CSP (SP2): implementation plan

Spec: `docs/specs/2026-11-04-cert-auth-csp-design.md` (approved by the owner 2026-11-04; audit
`.delivery/2026-11-04-cert-auth-csp-audit-spec.md`, all findings folded). Normative HTTP contract:
`contract-cert-auth.md` v1 with its 2026-11-04 errata (vocdoni-app worktree `certificats-digitals`,
`.delivery/certificats-digitals/contract-cert-auth.md`).
Class: architectural
Repos and branches: /Users/ferran/Repos/saas-backend → `feat/cert-auth-csp` (base: SP1 head after its rebase
onto origin/main; SP1 head at plan time: 145077b, before that rebase)

Verification: every code block below was built, vetted, linted (golangci-lint v2.15.0, gofumpt, the qt gate)
and tested in a scratch trial merge of 145077b into origin/main 91e4158 with SP1's nationalId migration
renumbered 34 → 36, which is what the coordinator announced for the real rebase (origin/main already registers
34 `integrator_flag` and 35 `payg_billing`). The tasks were replayed one by one in that order: each one
builds, vets and passes its own tests on top of the previous ones. The Docker suites named below also passed
there, together with the existing suites the changes touch (TestProcessCSP*, TestVotingProcessPublish,
TestCSPAuthNationalID*, TestProcessBlindCSP*, TestValidateProcessCensus*, TestProcessCensusMissingData,
TestProcessCensusPassword). Line numbers cite that trial merge: re-check them against the real base, and
rebase each diff hunk by its context, not its line number.

Decisions this plan takes where the spec does not match the code (none changes the contract):

- D1 Migration **37** `cert_auth_transactions` (spec: 0035, taken on origin/main); the publish guard checks
  `migrations.NationalIDNormalizationVersion`, which is 36 after SP1's rebase.
- D2 Feature-off code **40185** `ErrCertificateAuthDisabled` (40181 is now `ErrManagedOrgHasNoWallet`);
  40950 `ErrCertificateAuth` as specified. Re-check both are free when rebasing.
- D3 `issuer.name` uses the contract's display names (`"FNMT"`, `"idCAT"`), not cert-check's `Info().Name`
  (`"FNMT - Persona Física"`, `"idCAT Certificat"`) as spec section 7 says; other providers keep `Info().Name`.
- D4 Logs never carry a state or code, not even the 8-character prefix spec section 7 allows: the
  transaction's ObjectID is the correlation key (stricter Global Constraint).
- D5 The publish guard's 409 is `ErrDuplicateConflict` (40901, the codebase's generic 409) with the message
  "nationalId hash migration pending"; spec names no code and adds none.
- D6 Create on an unknown process answers 404 `ErrProcessNotFound`; unpublished or closed processes 409.
- D7 Certificate-census validation turns missing nationalIds into warnings in the API handler (it clears
  `missingData`): with `authFields` exactly `["nationalId"]` the only missing data is a missing nationalId,
  and SP1 already lists those members in `nationalIdWarnings`. No db change.
- D8 Redeem with a malformed body or code answers `attempt_expired` without touching storage; a malformed
  verifier consumes the code and answers `verifier_mismatch`, like any wrong verifier.
- D9 `db.ConsumeCertAuthState` takes the fresh code from certauth instead of generating it; the plain read
  that tells "spent" from "unknown" filters on the nonce too, so a state replayed on another nonce host reads
  as unknown (AC7).
- D10 The window helpers are exported as `handlers.ProcessPastEnd` / `handlers.ProcessBeforeStart`
  (revive's confusing-naming refuses `ProcessEnded` next to `processEnded`).
- D11 `github.com/pires/go-proxyproto` is pinned at v0.11.0 (the newest, v0.16.0, bumps golang.org/x/net).
- D12 The comma lists are plain string flags split by certauth's caller: viper splits a string slice read
  from an environment variable on spaces, not commas.
- D13 cert-check is a private module while saas-backend is public: CI, the Dockerfiles and docker compose get
  a `PRIVATE_MODULES_TOKEN` secret (Task 15). Pull requests from forks never receive it, and nobody without
  access can build the repository any more. Flagged for the owner (see Merge and release).

## Global Constraints

- Commit convention: Conventional Commits; no AI attribution, no `Co-Authored-By` or other trailers.
- Never push; commit with explicit paths only (`git commit -m "..." -- <paths>`), never `git add -A`.
- Go commands that resolve modules run with `GOPRIVATE=github.com/vocdoni/*` (cert-check is private).
  Build output and scratch files under `~/.cache/saas-backend/`, never `/tmp`.
- Docker is needed for every `./db/` and `./api/` test. The api package takes ~7 minutes in full: always
  narrow it with `-run`. Pure tests (`./certauth/...`, `./errors/`, `./cmd/service/`, `./migrations/`) need
  no Docker.
- Before each commit: `gofumpt -l` clean on the touched files, `go vet ./...` clean, `go mod tidy` leaves
  no diff, `./scripts/check-qt-patterns.sh` passes (it greps: never put `true`/`false` after `qt.Equals`, or
  `len(` before it, in one `c.Assert` line). golangci-lint v2.15.0 with the repo config must report nothing
  new in the touched files (lll 130, revive enable-all-rules).
- Repo conventions (AGENTS.md): `fmt.Errorf` with `%w`; lower-case structured log messages; godoc on every
  exported entity; quicktest matchers; error codes append-only, never renumbered; a new migration numbered
  above every registered one, integer literal version.
- Privacy: never log, store outside the ≤5-minute DER chain, or put in a URL a nationalId/DNI/NIE, a holder
  name, a certificate subject or chain, a state, a code or a verifier. cert-check diagnostics reach the logs
  only through `Diagnostics.LogAttrs()`; responses carry only contract section 8's fields, never
  `certcheck.Error.Detail` or a serialized `Diagnostics`.
- cert-check semantics are never reimplemented: verification is `Handler.Verify` only; the only local leaf
  parsing is the display helper (`subjectHolder`) and the hash of the raw DER for the budget key. Only
  `fnmt.New` and `idcat.New` are ever referenced (never `NewRepresentative`/`NewEmployee`).
- Do not touch SP1's frozen surfaces: `db.HashAuthTwoFaFields`, `internal.HashLoginFields`,
  `migrations/login_hash.go`, the normalisation call sites; SP2 calls `internal.NormalizeNationalID` and
  `CensusParticipantByLoginHash` as they are.
- `make swagger` after any change to a handler annotation, a route or an `apicommon` type, and commit the
  regenerated `docs/swagger.yaml` (Task 16). Gotcha: `scripts/generate-swagger.sh` deletes the file and calls
  `swag` without `$(go env GOPATH)/bin` on `PATH`; run it as `PATH=$(go env GOPATH)/bin:$PATH make swagger`.
- Feature off (`VOCDONI_CERTAUTHMODE` unset) leaves the binary's behaviour unchanged: no listener, the
  router's CORS middleware byte-identical, the two new routes answering `ErrCertificateAuthDisabled`, every
  existing suite passing unmodified. With the feature on, nothing changes for non-certificate censuses.
- The contract is normative: a field, status, reason, TTL or grammar that disagrees with it is a bug here.

## Review Focus

- Consume atomicity and TTL re-checks: `db/certauth_transactions.go:94` (`ConsumeCertAuthState`, filter on
  state + nonce + status + `createdAt`), `:122` (`ConsumeCertAuthCode`, `$unset chain`), `:137`
  (`FindOneAndUpdate` returning the pre-image); the disambiguating reads `:153`, `certauth/listener.go:230`
  (`expiredReturn`), `certauth/redeem.go:89` (`spentCode`).
- Budget keys and refunds: `certauth/redeem.go:150` (`verify`: per-DER unit before Verify, released only on
  retryable codes; per-verified-document unit after it), `:185` (`claimBudget` key
  `sha256(processID | subject)`); PKCE `:100` (`verifierMatches`, constant time) runs after the code consume.
- Header trust in cloudflare mode: `certauth/cloudflare.go:24` (`RequireAndVerifyClientCert` against the AOP
  CA), `:37` (`cloudflareChain`), `:89` (`peerFilter.Accept`); `certauth/config.go:196` (`validateFronts`:
  AOP CA required); `certauth/listener.go:99` (`wrapListener`).
- Per-SNI TLS: `certauth/listener.go:149` (`configForClient`: probe → no CertificateRequest, nonce host →
  cert-check's `TLSConfig`, else handshake error), `:134`, `certauth/service.go:95-96` (the two configs);
  PROXY policy `certauth/listener.go:112`; chain storage bounds `:273`.
- returnUrl allowlist: `certauth/create.go:28` (`returnPathRe`), `:103` (`checkReturnURL`);
  `certauth/config.go:271` (`parseOrigins`), `:304` (`checkDefaultReturn`); the 303 targets in
  `certauth/listener.go:197` (`serveAuthenticate`) are only a stored, validated returnUrl or the default.
- CORS dispatcher: `certauth/cors.go:18`; installed only with the feature on, `api/api.go:432` (else `:434`
  keeps today's middleware).
- Test-provider guard: `certauth/config.go:71` (allowlist), `:326` (`checkTestProviderAPI`), `:224`
  (`buildProviders`); `:75` (`providerConstructors`, natural persons only).
- Anonymous census path untouched: the blind public key choice still keys on `census.Anonymous` alone
  (`api/processes_publish.go:502`); the certificate refusal in `auth/0` sits before the anonymous check
  (`csp/handlers/auth.go:319`, `:322`); `db.ValidateCensusAuthMethod` (`db/census.go:50`) lets `Anonymous` and
  `Weighted` combine freely; the redeem token is the `auth/0` auth-only token (`certauth/redeem.go:203`).

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 Error codes and wire types | — | `errors/`, `api/apicommon/certauth.go` | 2, 3, 4, 15 |
| 2 Census authMethod and counters (db) | — | `db/census.go`, `db/errors.go`, `db/types.go`, `db/csp_counters.go` | 1, 3, 4, 15 |
| 3 Transaction store, migration 37 | — | `db/certauth_transactions.go`, `db/mongo.go`, `db/helpers.go`, `migrations/0037_*` | 1, 2, 4, 15 |
| 4 Synthetic test provider | — | `certauth/testprovider/`, `go.mod` | 1, 2, 3, 15 |
| 5 certauth configuration and New | 3, 4 | `certauth/config.go`, `service.go`, `random.go` | — |
| 6 Auth listener, origin mode, PROXY | 5 | `certauth/listener.go`, `go.mod` | 8, 11 |
| 7 Cloudflare mode | 6 | `certauth/cloudflare.go`, `certauth/listener.go` | 8, 11 |
| 8 Error mapping, holder helper, CSP exports | 1, 5 | `certauth/errors.go`, `subject.go`, `csp/handlers/` | 6, 7, 11 |
| 9 Create endpoint | 2, 6, 8 | `certauth/create.go` | 7, 11 |
| 10 Redeem endpoint | 9 | `certauth/redeem.go` | 11 |
| 11 CORS dispatcher | 5 | `certauth/cors.go` | 6–10 |
| 12 API wiring, flags, test harness | 1, 9, 10, 11 | `api/`, `cmd/service/`, `example.env` | 15 |
| 13 Census authMethod in the API | 2, 8, 12 | `api/`, `csp/handlers/auth.go` | 15 |
| 14 API end-to-end certificate tests | 13 | `api/certauth_flow_test.go` | 15, 17 |
| 15 Private module access in CI and Docker | 4 | `.github/workflows/`, `Dockerfile`, `dev.dockerfile`, `docker-compose.yml` | 1–14 |
| 16 Swagger | 13 | `docs/swagger.yaml` | 17 |
| 17 README deployment section | 12 | `README.md` | 14, 16 |

All tasks commit to the one branch in this order; "parallel" means they can be written independently.

Acceptance criteria → tasks: AC1 → 12 (routes, listener, CORS) and 13 (census refusal); AC2 → 2, 13;
AC3 → 13; AC4 → 2; AC5 → 8, 13; AC6 → 9 (unit) and 14 (Docker); AC7 → 6; AC8 → 3 (custody), 10 (unit),
14 (Docker); AC9 → 10, 14; AC10 → 8, 10; AC11 → 10, 14; AC12 → 10, 14; AC13 → 14; AC14 → 7;
AC15 → Merge and release (16 for swagger); AC16 → 5 (startup refusals), 6 (PROXY protocol).

## Tasks

### Task 1: Error codes and wire types
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `errors/errors_definition.go`, `errors/errors.go`,
`errors/certificate_test.go`, `api/apicommon/certauth.go`
Goal: the one certificate error code 40950 with a per-reason status, the plain feature-off error 40185, and the
request/response/error-data wire types of contract sections 3, 6, 8 and 9 (serves AC1, AC10).

Step 1: write the failing test
```go
package errors

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestCertificateAuthError pins the certificate sign-in envelope: one code for every reason, the
// reason's own HTTP status, the client-safe message as the error string (5xx included) and the
// data object verbatim.
func TestCertificateAuthError(t *testing.T) {
	c := qt.New(t)
	data := map[string]any{"reason": "revocation_unavailable", "retryable": true}

	e := CertificateAuthError(http.StatusServiceUnavailable, "revocation status is unavailable", data)
	code, body := writtenBody(t, e)
	c.Assert(code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(body["code"], qt.Equals, float64(40950))
	c.Assert(body["error"], qt.Equals, "revocation status is unavailable")
	c.Assert(body["data"], qt.DeepEquals, map[string]any{"reason": "revocation_unavailable", "retryable": true})
	c.Assert(e, qt.ErrorIs, ErrCertificateAuth)

	// the package-level definition is never modified by building a response from it
	c.Assert(ErrCertificateAuth.HTTPstatus, qt.Equals, http.StatusUnprocessableEntity)
	c.Assert(ErrCertificateAuth.Data, qt.IsNil)

	code, body = writtenBody(t, ErrCertificateAuthDisabled)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(body["code"], qt.Equals, float64(40185))
	c.Assert(body["data"], qt.IsNil)
}
```
Run: `go test -count=1 ./errors/`
Expected: `errors/certificate_test.go:17:7: undefined: CertificateAuthError`

Step 2: implement

`errors/errors_definition.go` (append after `ErrOrganizationAlreadySubscribed`, before the CSP block):
```diff
diff --git a/errors/errors_definition.go b/errors/errors_definition.go
index 31d32fe..273c76c 100644
--- a/errors/errors_definition.go
+++ b/errors/errors_definition.go
@@ -144,6 +144,15 @@ var (
 		Code: 40180, HTTPstatus: http.StatusConflict, LogLevel: "info",
 		Err: fmt.Errorf("organization already has a paid subscription, manage it from the billing portal"),
 	}
+	// ErrCertificateAuthDisabled answers the certificate sign-in endpoints, and refuses certificate
+	// censuses, on a deployment that runs without certificate authentication configured.
+	ErrCertificateAuthDisabled = Error{Code: 40185, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("certificate authentication is not enabled on this server"), LogLevel: "info"}
+
+	// ErrCertificateAuth is the one code of every certificate sign-in failure; clients key on
+	// data.reason, never on the code. The HTTP status varies per reason (see CertificateAuthError):
+	// contract-cert-auth.md section 8 fixes one code for every certificate reason, including the
+	// retryable 500/503 ones, a deliberate exception to the 4xxxx/5xxxx split above.
+	ErrCertificateAuth = Error{Code: 40950, HTTPstatus: http.StatusUnprocessableEntity, Err: fmt.Errorf("certificate authentication failed"), LogLevel: "info"}
 
 	// CSP errors (408)
 	ErrZeroWeightVoter = Error{Code: 40801, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("voter weight cannot be zero")}
```
`errors/errors.go`:
```diff
diff --git a/errors/errors.go b/errors/errors.go
index f66fb22..b5a6527 100644
--- a/errors/errors.go
+++ b/errors/errors.go
@@ -221,6 +221,16 @@ func (e Error) WithData(data any) Error {
 	return e
 }
 
+// CertificateAuthError is ErrCertificateAuth answering status, with message as the body's error
+// string and data as its data object. message must be client-safe: it is sent for 5xx statuses too.
+func CertificateAuthError(status int, message string, data any) Error {
+	e := ErrCertificateAuth
+	e.HTTPstatus = status
+	e.Err = fmt.Errorf("%s", message)
+	e.Data = data
+	return e
+}
+
 // As finds the first error in err's tree that matches target, and if one is found, sets
 // target to that error value and returns true. Otherwise, it returns false.
 //
```
`api/apicommon/certauth.go` (new):
```go
package apicommon

//revive:disable:max-public-structs

import "github.com/vocdoni/saas-backend/internal"

// Purposes of a certificate sign-in attempt (contract-cert-auth.md section 3).
const (
	// CertAuthPurposeVote is a normal sign-in: the redeem issues a CSP token while the process
	// accepts votes.
	CertAuthPurposeVote = "vote"
	// CertAuthPurposeCheck is a pre-check (an UPCOMING process, section 11): the redeem runs every
	// step but never issues a token.
	CertAuthPurposeCheck = "check"
)

// Backend reasons of a certificate sign-in failure (contract section 8). The certificate reasons
// are cert-check's own codes, passed through verbatim.
const (
	CertAuthReasonDNINotInCensus    = "dni_not_in_census"
	CertAuthReasonAttemptExpired    = "attempt_expired"
	CertAuthReasonCodeUsed          = "code_used"
	CertAuthReasonVerifierMismatch  = "verifier_mismatch"
	CertAuthReasonTooManyAttempts   = "too_many_attempts"
	CertAuthReasonCertificateCensus = "certificate_census"
	CertAuthReasonInternal          = "internal"
)

// CertAuthCreateRequest is the body of POST /processes/{processId}/auth/cert.
type CertAuthCreateRequest struct {
	// Challenge is base64url (no padding) of the SHA-256 of the verifier: exactly 43 characters.
	Challenge string `json:"challenge" example:"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"`
	// ChallengeMethod must be "S256".
	ChallengeMethod string `json:"challengeMethod" example:"S256"`
	// ReturnURL is where the auth listener sends the browser back: an allowlisted app origin with
	// an optional /:lang and /processes/{id} path, no query, fragment or userinfo.
	ReturnURL string `json:"returnUrl" example:"https://app.vocdoni.io/ca/processes/6650f0c0a1b2c3d4e5f60718"`
	// Purpose is "vote" or "check".
	Purpose string `json:"purpose" enums:"vote,check" example:"vote"`
}

// CertAuthCreateResponse is the 200 answer of POST /processes/{processId}/auth/cert.
type CertAuthCreateResponse struct {
	// State identifies the attempt: 32 random bytes, base64url. Single use, valid 15 minutes.
	State string `json:"state" example:"q3Jd0zR9xQe1v0bHkq2n8m1tY6cW4pLa7sFuEoZiXgA"`
	// AuthURL is the auth listener URL the browser navigates to, on a fresh per-attempt host.
	AuthURL string `json:"authUrl" example:"https://k7m2q9x4c1v8b3n6z5a0s2d4f6.cert.auth-dev.vocdoni.io/v1/authenticate?state=..."` //nolint:lll
	// ExpiresAt is when the state expires (RFC 3339, informative).
	ExpiresAt string `json:"expiresAt" example:"2026-11-13T09:15:00Z"`
	// ProbeURL is the auth listener's reachability probe, absent when the deployment cannot serve it.
	ProbeURL string `json:"probeUrl,omitempty" example:"https://probe.cert.auth-dev.vocdoni.io/ok"`
}

// CertAuthRedeemRequest is the body of POST /processes/{processId}/auth/cert/redeem.
type CertAuthRedeemRequest struct {
	// Code is the code the auth listener returned in the URL fragment.
	Code string `json:"code" example:"Zx8bQ2mVt1q9LkP0aR7sYc3dWn5uHe2oJi6gFb4xTz8"`
	// Verifier is the PKCE verifier whose S256 challenge the create carried.
	Verifier string `json:"verifier" example:"dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"`
}

// CertAuthHolder is the display identity of a certificate holder: never the full document number.
type CertAuthHolder struct {
	FullName string `json:"fullName" example:"Maria García López"`
	// DocumentType is "DNI" or "NIE".
	DocumentType string `json:"documentType" example:"DNI"`
	// DocumentEnding is the last 4 characters of the canonical 9-character number.
	DocumentEnding string `json:"documentEnding" example:"678Z"`
}

// CertAuthIssuer names the certificate provider of a failure, when it is known.
type CertAuthIssuer struct {
	ID   string `json:"id" example:"fnmt"`
	Name string `json:"name" example:"FNMT"`
}

// CertAuthRedeemResponse is the 200 answer of POST /processes/{processId}/auth/cert/redeem.
type CertAuthRedeemResponse struct {
	// Purpose echoes the create purpose.
	Purpose string `json:"purpose" example:"vote"`
	// AuthToken is the pre-verified CSP token, as an auth/0 token of an auth-only census; null for
	// a check, and for a vote while the process does not accept votes.
	AuthToken *internal.HexBytes `json:"authToken" swaggertype:"string" format:"hex" example:"8d1c5f3e0a2b4c6d8e0f1a2b3c4d5e6f"`
	// Weight is the voter's census weight (bare hex), omitted when AuthToken is null.
	Weight internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"01"`
	// VoterRef is opaque and stable for one participant of one process.
	VoterRef string `json:"voterRef" example:"Vt3nQ9xW2kLp7RfA0bYc1d"`
	// Holder is the certificate holder's display identity.
	Holder CertAuthHolder `json:"holder"`
	// Reference is the cert-check diagnostic id of this check (32 hex).
	Reference string `json:"reference" example:"3f9a07c2e41b8d5c2a6f90b3c7e14d28"`
}

// CertAuthErrorData is the data object of every certificate sign-in failure (error code 40950).
type CertAuthErrorData struct {
	Reason    string `json:"reason" example:"dni_not_in_census"`
	Retryable bool   `json:"retryable"`
	// Reference is the diagnostic id to quote to support (32 hex).
	Reference string          `json:"reference,omitempty" example:"3f9a07c2e41b8d5c2a6f90b3c7e14d28"`
	Holder    *CertAuthHolder `json:"holder,omitempty"`
	Issuer    *CertAuthIssuer `json:"issuer,omitempty"`
	// NotAfter is the certificate's expiry (RFC 3339), on expired and not_yet_valid.
	NotAfter string `json:"notAfter,omitempty" example:"2026-10-03T00:00:00Z"`
	// RetryAfter is the seconds until the attempt may be retried, on too_many_attempts.
	RetryAfter int64 `json:"retryAfter,omitempty" example:"1800"`
}
```
Run: `go test -count=1 ./errors/ && go build ./api/...`
Expected: `ok  	github.com/vocdoni/saas-backend/errors` (TestErrorCodesAreUnique and TestCertificateAuthError among them)

Step 3: commit
`git commit -m "feat(errors): add the certificate sign-in error codes and wire types" -- errors/errors_definition.go errors/errors.go errors/certificate_test.go api/apicommon/certauth.go`

Interfaces this task provides to later tasks: `errors.ErrCertificateAuth` (40950), `errors.ErrCertificateAuthDisabled`
(40185), `errors.CertificateAuthError(status int, message string, data any) errors.Error`;
`apicommon.CertAuthCreateRequest/Response`, `CertAuthRedeemRequest/Response`, `CertAuthHolder`, `CertAuthIssuer`,
`CertAuthErrorData`, the `CertAuthPurpose*` and `CertAuthReason*` constants.

### Task 2: Census authMethod and the certificate counters (db)
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `db/census.go`, `db/errors.go`, `db/types.go`,
`db/csp_counters.go`, `db/census_auth_method_test.go`
Goal: `db.Census.AuthMethod` with its feature-agnostic validation, and the two counter kinds with their windows
(serves AC2, AC4; the counters back AC6 and AC11).

Step 1: write the failing test
```go
package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestCertificateCensusAuthMethod pins the census authMethod rules: a certificate census logs in with
// exactly the nationalId and no 2FA field, an unknown method is refused, and the method round-trips
// through SetCensus.
func TestCertificateCensusAuthMethod(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	nationalID := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	email := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}
	for _, tc := range []struct {
		method CensusAuthMethod
		auth   OrgMemberAuthFields
		twoFa  OrgMemberTwoFaFields
		ok     bool
	}{
		{method: CensusAuthMethodCertificate, auth: nationalID, ok: true},
		{method: CensusAuthMethodCertificate, auth: nationalID, twoFa: email},
		{method: CensusAuthMethodCertificate, auth: OrgMemberAuthFields{OrgMemberAuthFieldsName}},
		{method: CensusAuthMethodCertificate, auth: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID, OrgMemberAuthFieldsName}},
		{method: CensusAuthMethodCertificate},
		{method: "fingerprint", auth: nationalID},
		{auth: nationalID, twoFa: email, ok: true},
	} {
		comment := qt.Commentf("method=%q auth=%v twoFa=%v", tc.method, tc.auth, tc.twoFa)
		err := ValidateCensusAuthMethod(tc.method, tc.auth, tc.twoFa)
		if tc.ok {
			c.Assert(err, qt.IsNil, comment)
			continue
		}
		c.Assert(err, qt.ErrorIs, ErrInvalidCensusAuthMethod, comment)
		_, err = testDB.SetCensus(&Census{
			OrgAddress: testOrgAddress, AuthMethod: tc.method, AuthFields: tc.auth, TwoFaFields: tc.twoFa,
		})
		c.Assert(err, qt.ErrorIs, ErrInvalidCensusAuthMethod, comment)
	}

	id, err := testDB.SetCensus(&Census{
		OrgAddress: testOrgAddress, AuthMethod: CensusAuthMethodCertificate, AuthFields: nationalID, Anonymous: true,
	})
	c.Assert(err, qt.IsNil)
	stored, err := testDB.Census(id)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethodCertificate)
	// a certificate census is auth-only: the blind path keys on Anonymous alone
	c.Assert(stored.Type, qt.Equals, CensusTypeAuthOnly)
	c.Assert(stored.Anonymous, qt.IsTrue)

	// a typed-fields census stores no authMethod at all
	id, err = testDB.SetCensus(&Census{OrgAddress: testOrgAddress, AuthFields: nationalID})
	c.Assert(err, qt.IsNil)
	stored, err = testDB.Census(id)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethod(""))
}

// TestCertAuthCounterWindows pins the windows of the certificate sign-in counters: creates per UTC
// minute, failed redeems per UTC hour.
func TestCertAuthCounterWindows(t *testing.T) {
	c := qt.New(t)
	c.Assert(CSPCounterWindow(CSPCounterCertAuthCreates), qt.Equals, time.Minute)
	c.Assert(CSPCounterWindow(CSPCounterCertAuthFailures), qt.Equals, time.Hour)
	now := time.Date(2026, 11, 4, 10, 15, 42, 0, time.UTC)
	creates := currentCSPCounterBucket(CSPCounterCertAuthCreates, []byte("ip|process"), now)
	c.Assert(creates.resetAt.Equal(time.Date(2026, 11, 4, 10, 16, 0, 0, time.UTC)), qt.IsTrue)
	c.Assert(creates.expiry.Equal(time.Date(2026, 11, 4, 10, 17, 0, 0, time.UTC)), qt.IsTrue)
}

// TestCertificateCensusBuild pins how a certificate census is built (contract section 2): a member
// without a nationalId is skipped and left out of the size, while a present but unmatchable value
// (a wrong check letter) is a participant, counted, that the canonical twin number never matches.
func TestCertificateCensusBuild(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	addMember := func(nationalID string) string {
		id, err := testDB.SetOrgMember(testSalt, &OrgMember{OrgAddress: testOrgAddress, NationalID: nationalID})
		c.Assert(err, qt.IsNil)
		return id
	}
	separators := addMember("12.345.678-z")
	wrongLetter := addMember("87654321A") // canonical twin: 87654321X
	missing := addMember("")

	census := &Census{
		OrgAddress: testOrgAddress, AuthMethod: CensusAuthMethodCertificate,
		AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsNationalID},
	}
	_, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	size, skipped, err := testDB.PopulateMembersCensus(census, []string{separators, wrongLetter, missing})
	c.Assert(err, qt.IsNil)
	c.Assert(size, qt.Equals, int64(2))
	c.Assert(skipped, qt.HasLen, 1)
	c.Assert(skipped[0].Hex(), qt.Equals, missing)
	stored, err := testDB.Census(census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Size, qt.Equals, int64(2))
	c.Assert(stored.AuthMethod, qt.Equals, CensusAuthMethodCertificate)

	// what a certificate presents is always the canonical 9-character number
	lookup := func(number string) (*CensusParticipant, error) {
		return testDB.CensusParticipantByLoginHash(*stored, OrgMember{NationalID: number})
	}
	p, err := lookup("12345678Z")
	c.Assert(err, qt.IsNil)
	c.Assert(p.ParticipantID, qt.Equals, separators)
	_, err = lookup("87654321X")
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	// the member's own stored value is still matched by itself, as it always was
	p, err = lookup("87654321A")
	c.Assert(err, qt.IsNil)
	c.Assert(p.ParticipantID, qt.Equals, wrongLetter)
}
```
Run: `go vet ./db/`
Expected: `vet: db/census_auth_method_test.go:22:10: undefined: CensusAuthMethod`

Step 2: implement
```diff
diff --git a/db/errors.go b/db/errors.go
index 502b588..4901fee 100644
--- a/db/errors.go
+++ b/db/errors.go
@@ -35,6 +35,9 @@ var (
 	// ErrPasswordNeedsLookupField is returned for a census whose only login field is the member
 	// password: it never takes part in the login hash, so every member would hash alike.
 	ErrPasswordNeedsLookupField = fmt.Errorf("%w: %s", ErrInvalidData, PasswordNeedsLookupFieldMessage)
+	// ErrInvalidCensusAuthMethod is returned for an unknown census authMethod, or login fields the
+	// method cannot use (see ValidateCensusAuthMethod).
+	ErrInvalidCensusAuthMethod = fmt.Errorf("invalid census auth method")
 	// ErrAutoGroupCannotBeDeleted is returned when trying to delete the auto-generated "All members" group
 	ErrAutoGroupCannotBeDeleted = fmt.Errorf("auto-generated group cannot be deleted")
 	// ErrAutoGroupMembersCannotBeModified is returned when trying to manually add/remove members from the auto group
diff --git a/db/types.go b/db/types.go
index 24987be..c209de4 100644
--- a/db/types.go
+++ b/db/types.go
@@ -341,6 +341,9 @@ type Census struct {
 	Published   PublishedCensus      `json:"published" bson:"published"`
 	AuthFields  OrgMemberAuthFields  `json:"authFields" bson:"orgMemberAuthFields"`
 	TwoFaFields OrgMemberTwoFaFields `json:"twoFaFields" bson:"orgMemberTwoFaFields"`
+	// AuthMethod is how voters prove their login fields: empty for typed member details (auth/0),
+	// CensusAuthMethodCertificate for a digital certificate whose DNI/NIE matches the nationalId.
+	AuthMethod CensusAuthMethod `json:"authMethod,omitempty" bson:"authMethod,omitempty"`
 
 	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
 	UpdatedAt time.Time `json:"updatedAt" bson:"updatedAt"`
diff --git a/db/census.go b/db/census.go
index cb44239..a01d981 100644
--- a/db/census.go
+++ b/db/census.go
@@ -35,6 +35,35 @@ func ValidateCensusFields(authFields OrgMemberAuthFields, twoFaFields OrgMemberT
 	return nil
 }
 
+// CensusAuthMethod is how the voters of a census prove their login fields. The empty value is
+// today's typed member details.
+type CensusAuthMethod string
+
+// CensusAuthMethodCertificate is a census whose voters sign in only with a qualified digital
+// certificate (FNMT, idCAT) whose DNI/NIE matches the member's nationalId login hash.
+const CensusAuthMethodCertificate CensusAuthMethod = "certificate"
+
+// ValidateCensusAuthMethod checks the census login fields fit its auth method: a certificate census
+// logs in with exactly the nationalId and no 2FA field, and any other non-empty method is unknown.
+// Both refusals wrap ErrInvalidCensusAuthMethod. It knows nothing of whether the deployment runs
+// certificate authentication: that refusal belongs to the API.
+func ValidateCensusAuthMethod(
+	method CensusAuthMethod, authFields OrgMemberAuthFields, twoFaFields OrgMemberTwoFaFields,
+) error {
+	switch method {
+	case "":
+		return nil
+	case CensusAuthMethodCertificate:
+		if len(authFields) != 1 || authFields[0] != OrgMemberAuthFieldsNationalID || len(twoFaFields) > 0 {
+			return fmt.Errorf("%w: a certificate census needs authFields [\"nationalId\"] and no twoFaFields",
+				ErrInvalidCensusAuthMethod)
+		}
+		return nil
+	default:
+		return fmt.Errorf("%w: unknown authMethod %q", ErrInvalidCensusAuthMethod, method)
+	}
+}
+
 // SetCensus creates a new census for an organization
 // Returns the hex representation of the census
 func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
@@ -48,6 +77,9 @@ func (ms *MongoStorage) SetCensus(census *Census) (string, error) {
 	if err := ValidateCensusFields(census.AuthFields, census.TwoFaFields); err != nil {
 		return "", err
 	}
+	if err := ValidateCensusAuthMethod(census.AuthMethod, census.AuthFields, census.TwoFaFields); err != nil {
+		return "", err
+	}
 	// check that the org exists
 	_, err := ms.Organization(census.OrgAddress)
 	if err != nil {
diff --git a/db/csp_counters.go b/db/csp_counters.go
index 4638ba5..d9c09a7 100644
--- a/db/csp_counters.go
+++ b/db/csp_counters.go
@@ -26,6 +26,14 @@ const (
 	// login fields a request submits, without the contact) on one voting process, per UTC hour. It
 	// is the coarser bound on guessing one voter's contact with ever-changing values.
 	CSPCounterVoterAuthFailures = "voterAuthFailures"
+	// CSPCounterCertAuthCreates counts certificate sign-in attempts created from one client IP on
+	// one voting process, per UTC minute.
+	CSPCounterCertAuthCreates = "certAuthCreates"
+	// CSPCounterCertAuthFailures counts certificate redeems on one voting process, per UTC hour,
+	// under two kinds of key: the presented certificate (before verification) and the verified
+	// document number (after it). Only a verified identity reaches a document key, so a forged
+	// certificate naming a voter's document spends its own budget, never the voter's.
+	CSPCounterCertAuthFailures = "certAuthFailures"
 )
 
 // CSPCounterWindow returns the length of the window a counter kind resets after. Failed
@@ -33,10 +41,14 @@ const (
 // that voter's coarse budget, and an hour bounds such a lockout while still capping contact
 // guessing at 24 windows a day. Challenge sends cost money per unit, so their budget is daily.
 func CSPCounterWindow(kind string) time.Duration {
-	if kind == CSPCounterAuthFailures || kind == CSPCounterVoterAuthFailures {
+	switch kind {
+	case CSPCounterAuthFailures, CSPCounterVoterAuthFailures, CSPCounterCertAuthFailures:
 		return time.Hour
+	case CSPCounterCertAuthCreates:
+		return time.Minute
+	default:
+		return 24 * time.Hour
 	}
-	return 24 * time.Hour
 }
 
 // cspCounter is one counter document. The _id derives from (kind, key, window start), so every
```
Run (Docker: run by the implementer): `go test -count=1 -run 'TestCertificateCensusAuthMethod|TestCertAuthCounterWindows|TestCertificateCensusBuild' ./db/`
Expected: `ok  	github.com/vocdoni/saas-backend/db` (3 tests; passed in the trial merge)

Step 3: commit
`git commit -m "feat(db): add the census authMethod and the certificate sign-in counters" -- db/census.go db/errors.go db/types.go db/csp_counters.go db/census_auth_method_test.go`

Interfaces this task provides to later tasks: `db.CensusAuthMethod`, `db.CensusAuthMethodCertificate`,
`db.ValidateCensusAuthMethod(method, authFields, twoFaFields) error`, `db.ErrInvalidCensusAuthMethod`,
`db.Census.AuthMethod` (bson `authMethod,omitempty`), `db.CSPCounterCertAuthCreates` (per minute),
`db.CSPCounterCertAuthFailures` (per hour).

### Task 3: Transaction store and migration 37
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `db/certauth_transactions.go`, `db/certauth_transactions_test.go`,
`db/mongo.go`, `db/helpers.go`, `migrations/0037_cert_auth_transactions.go`
Goal: the `certAuthTransactions` collection with single-use, deadline-checked, atomic state and code consumes,
the chain removed at redeem, and its indexes (serves AC8 chain custody, AC7 and AC9 through certauth).

Step 1: write the failing test
```go
package db

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestCertAuthTransactionStore pins the certificate sign-in store: migration 37's indexes (unique
// state, unique sparse code, TTL on expiresAt), single-use state and code consumes that bind the
// state to its own host, re-check their own deadlines and let one of many concurrent consumes win,
// and chain custody ending at the code consume.
func TestCertAuthTransactionStore(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx := context.Background()

	specs, err := testDB.certAuthTransactions.Indexes().ListSpecifications(ctx)
	c.Assert(err, qt.IsNil)
	byName := map[string]bool{}
	for _, spec := range specs {
		byName[spec.Name] = true
		switch spec.Name {
		case "state_1":
			c.Assert(spec.Unique, qt.Not(qt.IsNil))
			c.Assert(*spec.Unique, qt.IsTrue)
		case "code_1":
			c.Assert(spec.Unique, qt.Not(qt.IsNil))
			c.Assert(*spec.Unique, qt.IsTrue)
			c.Assert(spec.Sparse, qt.Not(qt.IsNil))
			c.Assert(*spec.Sparse, qt.IsTrue)
		case "expiresAt_1":
			c.Assert(spec.ExpireAfterSeconds, qt.Not(qt.IsNil))
			c.Assert(*spec.ExpireAfterSeconds, qt.Equals, int32(0))
		default:
		}
	}
	for _, name := range []string{"state_1", "code_1", "expiresAt_1"} {
		c.Assert(byName[name], qt.IsTrue, qt.Commentf("index %s", name))
	}

	now := time.Now()
	newTx := func(state, nonce string, createdAt time.Time) *CertAuthTransaction {
		tx := &CertAuthTransaction{
			State: state, Nonce: nonce, Challenge: "challenge", ProcessID: bson.NewObjectID(),
			ReturnURL: "https://app.test/processes/0123456789abcdef01234567", Purpose: "vote",
			Status: CertAuthStatusCreated, CreatedAt: createdAt, ExpiresAt: createdAt.Add(15 * time.Minute),
		}
		c.Assert(testDB.InsertCertAuthTransaction(tx), qt.IsNil)
		return tx
	}
	consume := func(state, nonce, code string, chain [][]byte) (*CertAuthTransaction, error) {
		return testDB.ConsumeCertAuthState(CertAuthStateConsume{
			State: state, Nonce: nonce, Code: code, Chain: chain, ChainPresented: len(chain) > 0,
			CreatedAfter: time.Now().Add(-15 * time.Minute), Now: time.Now(), ExpiresAt: time.Now().Add(5 * time.Minute),
		})
	}

	c.Run("state", func(c *qt.C) {
		created := newTx("state-a", "nonce-a", now)
		// a state presented on another nonce host matches nothing, and leaves it usable
		_, err := consume("state-a", "nonce-b", "code-x", nil)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		_, err = testDB.CertAuthTransactionByState("state-a", "nonce-b")
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		chain := [][]byte{[]byte("leaf"), []byte("intermediate")}
		before, err := consume("state-a", "nonce-a", "code-a", chain)
		c.Assert(err, qt.IsNil)
		c.Assert(before.ID, qt.Equals, created.ID)
		c.Assert(before.Status, qt.Equals, CertAuthStatusCreated)
		c.Assert(before.ReturnURL, qt.Equals, created.ReturnURL)
		// single use
		_, err = consume("state-a", "nonce-a", "code-a2", chain)
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		after, err := testDB.CertAuthTransactionByState("state-a", "nonce-a")
		c.Assert(err, qt.IsNil)
		c.Assert(after.Status, qt.Equals, CertAuthStatusAuthenticated)
		c.Assert(after.Code, qt.Equals, "code-a")
		c.Assert(after.Chain, qt.DeepEquals, chain)
		c.Assert(after.ChainPresented, qt.IsTrue)
		// the expiry shrank to the code TTL, which also bounds the chain's custody
		c.Assert(after.ExpiresAt.Before(now.Add(6*time.Minute)), qt.IsTrue)

		// an expired state is refused even before the TTL monitor reaps it
		newTx("state-old", "nonce-old", now.Add(-16*time.Minute))
		_, err = consume("state-old", "nonce-old", "code-old", nil)
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		stale, err := testDB.CertAuthTransactionByState("state-old", "nonce-old")
		c.Assert(err, qt.IsNil)
		c.Assert(stale.Status, qt.Equals, CertAuthStatusCreated)

		// a duplicate state is refused by the unique index
		dup := &CertAuthTransaction{State: "state-a", Nonce: "nonce-z", ProcessID: bson.NewObjectID(), CreatedAt: now}
		c.Assert(testDB.InsertCertAuthTransaction(dup), qt.Not(qt.IsNil))
	})

	c.Run("concurrent state consumes", func(c *qt.C) {
		newTx("state-race", "nonce-race", now)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				if _, err := consume("state-race", "nonce-race", "code-race-"+string(rune('a'+i)), nil); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		c.Assert(wins.Load(), qt.Equals, int32(1))
	})

	c.Run("code", func(c *qt.C) {
		newTx("state-c", "nonce-c", now)
		chain := [][]byte{[]byte("leaf-c")}
		_, err := consume("state-c", "nonce-c", "code-c", chain)
		c.Assert(err, qt.IsNil)

		// an expired code is refused even before the TTL monitor reaps it
		_, err = testDB.ConsumeCertAuthCode("code-c", time.Now().Add(time.Minute))
		c.Assert(err, qt.ErrorIs, ErrNotFound)

		before, err := testDB.ConsumeCertAuthCode("code-c", time.Now().Add(-5*time.Minute))
		c.Assert(err, qt.IsNil)
		c.Assert(before.Status, qt.Equals, CertAuthStatusAuthenticated)
		c.Assert(before.Chain, qt.DeepEquals, chain)
		// single use, and the chain is gone from storage
		_, err = testDB.ConsumeCertAuthCode("code-c", time.Now().Add(-5*time.Minute))
		c.Assert(err, qt.ErrorIs, ErrNotFound)
		after, err := testDB.CertAuthTransactionByCode("code-c")
		c.Assert(err, qt.IsNil)
		c.Assert(after.Status, qt.Equals, CertAuthStatusConsumed)
		c.Assert(after.Chain, qt.HasLen, 0)
		raw := bson.M{}
		c.Assert(testDB.certAuthTransactions.FindOne(ctx, bson.M{"code": "code-c"}).Decode(&raw), qt.IsNil)
		_, hasChain := raw["chain"]
		c.Assert(hasChain, qt.IsFalse)

		_, err = testDB.CertAuthTransactionByCode("code-unknown")
		c.Assert(err, qt.ErrorIs, ErrNotFound)
	})

	c.Run("concurrent code consumes", func(c *qt.C) {
		newTx("state-cr", "nonce-cr", now)
		_, err := consume("state-cr", "nonce-cr", "code-cr", [][]byte{[]byte("leaf")})
		c.Assert(err, qt.IsNil)
		var wins atomic.Int32
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				if _, err := testDB.ConsumeCertAuthCode("code-cr", time.Now().Add(-5*time.Minute)); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		c.Assert(wins.Load(), qt.Equals, int32(1))
	})
}
```
Run: `go vet ./db/`
Expected: `vet: db/certauth_transactions_test.go:23:23: testDB.certAuthTransactions undefined (type *MongoStorage has no field or method certAuthTransactions)`

Step 2: implement

`db/mongo.go` and `db/helpers.go` (the blank lines keep gofmt from realigning the existing fields):
```diff
diff --git a/db/mongo.go b/db/mongo.go
index f45d2e5..979840a 100644
--- a/db/mongo.go
+++ b/db/mongo.go
@@ -68,6 +68,9 @@ type MongoStorage struct {
 	meteredVotes        *mongo.Collection
 	migrations          *mongo.Collection
 	counters            *mongo.Collection
+
+	// certAuthTransactions holds the certificate sign-in attempts (see CertAuthTransaction).
+	certAuthTransactions *mongo.Collection
 }
 
 type Options struct {
diff --git a/db/helpers.go b/db/helpers.go
index 7f91b42..32a95a9 100644
--- a/db/helpers.go
+++ b/db/helpers.go
@@ -41,6 +41,9 @@ func (ms *MongoStorage) collectionsMap() map[string]**mongo.Collection {
 		"meteredVotes":        &ms.meteredVotes,
 		"migrations":          &ms.migrations,
 		"counters":            &ms.counters,
+
+		// certificate sign-in attempts (indexed by migration 37)
+		"certAuthTransactions": &ms.certAuthTransactions,
 	}
 }
 
```
`migrations/0037_cert_auth_transactions.go` (new; first re-verify that 37 is above every version registered on
the base, `git grep -h "AddMigration(" migrations/`):
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
	AddMigration(37, "cert_auth_transactions", upCertAuthTransactions, downCertAuthTransactions)
}

// certAuthTransactionsIndexes are Mongo's default names of the indexes migration 37 creates, kept
// explicit so the down migration drops them deterministically.
var certAuthTransactionsIndexes = []string{"state_1", "code_1", "expiresAt_1"}

// upCertAuthTransactions indexes the certificate sign-in transactions: one attempt per state, one
// per code once the auth listener issued it (sparse: a created attempt has none), and a TTL on
// expiresAt that reaps every attempt, and the DER chain it may hold, once it expired. Creating the
// indexes creates the collection; running it again is a no-op.
func upCertAuthTransactions(ctx context.Context, database *mongo.Database) error {
	models := []mongo.IndexModel{
		{Keys: bson.D{{Key: "state", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "code", Value: 1}}, Options: options.Index().SetUnique(true).SetSparse(true)},
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	}
	if _, err := database.Collection("certAuthTransactions").Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("failed to create indexes on certAuthTransactions: %w", err)
	}
	return nil
}

// downCertAuthTransactions drops those indexes, tolerating them being already gone. The documents
// are short-lived attempts, so nothing else is undone.
func downCertAuthTransactions(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("certAuthTransactions"), certAuthTransactionsIndexes, nil); err != nil {
		return fmt.Errorf("failed to drop indexes on certAuthTransactions: %w", err)
	}
	return nil
}
```
`db/certauth_transactions.go` (new):
```go
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Statuses of a certificate sign-in transaction, in the only order it moves through them.
const (
	// CertAuthStatusCreated is an attempt the create endpoint stored, waiting for the auth listener.
	CertAuthStatusCreated = "created"
	// CertAuthStatusAuthenticated is an attempt the auth listener consumed: it holds a code and the
	// presented chain, waiting for the redeem.
	CertAuthStatusAuthenticated = "authenticated"
	// CertAuthStatusConsumed is an attempt whose code was redeemed. Its chain is gone.
	CertAuthStatusConsumed = "consumed"
)

// CertAuthTransaction is one certificate sign-in attempt (contract-cert-auth.md section 1). No field
// ever holds a DNI/NIE, a name or anything derived from the identity: the presented DER chain is its
// only personal data, removed at redeem and bounded by ExpiresAt otherwise.
type CertAuthTransaction struct {
	ID bson.ObjectID `bson:"_id"`
	// State is 32 random bytes, base64url (43 characters); unique.
	State string `bson:"state"`
	// Nonce is 16 random bytes, base32 lower case (26 characters): the label of the attempt's auth host.
	Nonce string `bson:"nonce"`
	// Challenge is the S256 PKCE challenge of the create request.
	Challenge string        `bson:"challenge"`
	ProcessID bson.ObjectID `bson:"processId"`
	// ReturnURL is the validated return URL of the create request.
	ReturnURL string `bson:"returnUrl"`
	// Purpose is "vote" or "check".
	Purpose string `bson:"purpose"`
	Status  string `bson:"status"`
	// Code is 32 random bytes, base64url, set by the auth listener; unique among the attempts that have one.
	Code string `bson:"code,omitempty"`
	// Chain is the presented DER certificates, leaf first. Empty with ChainPresented false means the
	// browser sent no certificate; empty with ChainPresented true means it sent one too large to keep.
	Chain          [][]byte `bson:"chain,omitempty"`
	ChainPresented bool     `bson:"chainPresented"`

	CreatedAt       time.Time `bson:"createdAt"`
	AuthenticatedAt time.Time `bson:"authenticatedAt,omitempty"`
	// ExpiresAt backs the TTL index: create + state TTL, shortened to authenticate + code TTL. The TTL
	// monitor only collects garbage: every consume re-checks its own deadline.
	ExpiresAt time.Time `bson:"expiresAt"`
}

// CertAuthStateConsume is what the auth listener stores when it consumes a state.
type CertAuthStateConsume struct {
	State string
	// Nonce is the label of the host the state was presented on: a state only matches on its own host.
	Nonce string
	// Code is the fresh code to issue.
	Code           string
	Chain          [][]byte
	ChainPresented bool
	// CreatedAfter is now minus the state TTL: an older attempt has expired.
	CreatedAfter time.Time
	// Now is the authentication time.
	Now time.Time
	// ExpiresAt is now plus the code TTL.
	ExpiresAt time.Time
}

// InsertCertAuthTransaction stores a new attempt, setting its id when it has none.
func (ms *MongoStorage) InsertCertAuthTransaction(tx *CertAuthTransaction) error {
	if tx == nil || tx.State == "" || tx.Nonce == "" || tx.ProcessID.IsZero() {
		return ErrBadInputs
	}
	if tx.ID.IsZero() {
		tx.ID = bson.NewObjectID()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.certAuthTransactions.InsertOne(ctx, tx); err != nil {
		return fmt.Errorf("failed to insert certificate auth transaction: %w", err)
	}
	return nil
}

// ConsumeCertAuthState atomically moves the created, unexpired attempt holding the state on the
// nonce's host to authenticated, storing the code and the chain, and shortening its expiry to the
// code TTL. It returns the attempt as it was before, or ErrNotFound when no attempt matches: the
// state is unknown, expired, already consumed or presented on another host. Of two concurrent
// consumes of one state exactly one succeeds.
func (ms *MongoStorage) ConsumeCertAuthState(in CertAuthStateConsume) (*CertAuthTransaction, error) {
	if in.State == "" || in.Nonce == "" || in.Code == "" {
		return nil, ErrBadInputs
	}
	filter := bson.M{
		"state":     in.State,
		"nonce":     in.Nonce,
		"status":    CertAuthStatusCreated,
		"createdAt": bson.M{"$gt": in.CreatedAfter},
	}
	set := bson.M{
		"status":          CertAuthStatusAuthenticated,
		"code":            in.Code,
		"chainPresented":  in.ChainPresented,
		"authenticatedAt": in.Now,
		"expiresAt":       in.ExpiresAt,
	}
	if len(in.Chain) > 0 {
		set["chain"] = in.Chain
	}
	return ms.consumeCertAuthTransaction(filter, bson.M{"$set": set})
}

// ConsumeCertAuthCode atomically moves the authenticated attempt holding the code, authenticated
// after authenticatedAfter (now minus the code TTL), to consumed and removes its chain. It returns
// the attempt as it was before, chain included, or ErrNotFound when no attempt matches: the code is
// unknown, expired or already redeemed (CertAuthTransactionByCode tells them apart). Of two
// concurrent redeems of one code exactly one succeeds.
func (ms *MongoStorage) ConsumeCertAuthCode(code string, authenticatedAfter time.Time) (*CertAuthTransaction, error) {
	if code == "" {
		return nil, ErrBadInputs
	}
	filter := bson.M{
		"code":            code,
		"status":          CertAuthStatusAuthenticated,
		"authenticatedAt": bson.M{"$gt": authenticatedAfter},
	}
	update := bson.M{"$set": bson.M{"status": CertAuthStatusConsumed}, "$unset": bson.M{"chain": ""}}
	return ms.consumeCertAuthTransaction(filter, update)
}

// consumeCertAuthTransaction applies update to the one attempt matching filter, returning its
// pre-image, or ErrNotFound.
func (ms *MongoStorage) consumeCertAuthTransaction(filter, update bson.M) (*CertAuthTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	opts := options.FindOneAndUpdate().SetReturnDocument(options.Before)
	tx := &CertAuthTransaction{}
	if err := ms.certAuthTransactions.FindOneAndUpdate(ctx, filter, update, opts).Decode(tx); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to consume certificate auth transaction: %w", err)
	}
	return tx, nil
}

// CertAuthTransactionByState returns the attempt holding the state on the nonce's host, whatever its
// status or age, or ErrNotFound.
func (ms *MongoStorage) CertAuthTransactionByState(state, nonce string) (*CertAuthTransaction, error) {
	if state == "" || nonce == "" {
		return nil, ErrBadInputs
	}
	return ms.certAuthTransaction(bson.M{"state": state, "nonce": nonce})
}

// CertAuthTransactionByCode returns the attempt holding the code, whatever its status or age, or
// ErrNotFound.
func (ms *MongoStorage) CertAuthTransactionByCode(code string) (*CertAuthTransaction, error) {
	if code == "" {
		return nil, ErrBadInputs
	}
	return ms.certAuthTransaction(bson.M{"code": code})
}

// certAuthTransaction reads the one attempt matching filter, or ErrNotFound.
func (ms *MongoStorage) certAuthTransaction(filter bson.M) (*CertAuthTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tx := &CertAuthTransaction{}
	if err := ms.certAuthTransactions.FindOne(ctx, filter).Decode(tx); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read certificate auth transaction: %w", err)
	}
	return tx, nil
}
```
Run: `go vet ./db/ && go test -count=1 -run TestMigrationVersionsAreUnique ./migrations/`
Expected: `ok  	github.com/vocdoni/saas-backend/migrations`
Run (Docker: run by the implementer): `go test -count=1 -run 'TestCertAuthTransactionStore|TestMigrationApplied' ./db/`
Expected: `ok  	github.com/vocdoni/saas-backend/db` (passed in the trial merge)

Step 3: commit
`git commit -m "feat(db): store certificate sign-in transactions (migration 37)" -- db/certauth_transactions.go db/certauth_transactions_test.go db/mongo.go db/helpers.go migrations/0037_cert_auth_transactions.go`

Interfaces this task provides to later tasks: `db.CertAuthTransaction`, `db.CertAuthStateConsume`, the
`db.CertAuthStatus*` constants, `(*MongoStorage).InsertCertAuthTransaction`, `ConsumeCertAuthState(in)`,
`CertAuthTransactionByState(state, nonce)`, `ConsumeCertAuthCode(code, authenticatedAfter)`,
`CertAuthTransactionByCode(code)`; all return `db.ErrNotFound` when nothing matches.

### Task 4: Synthetic test provider
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/testprovider/testprovider.go`,
`certauth/testprovider/pki.go`, `certauth/testprovider/testprovider_test.go`, `go.mod`, `go.sum`
Goal: a cert-check `Provider` double over an in-memory CA that mints natural-person certificates, with deterministic
error fixtures (OU `REVOKED`, `OUTAGE`), usable by tests and the SP6 e2e stack (spec section 8; serves AC7–AC14).

Step 1: write the failing test
```go
package testprovider

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	certcheck "github.com/vocdoni/cert-check"
)

func verifyErr(c *qt.C, h *certcheck.Handler, leaf *x509.Certificate) *certcheck.Error {
	c.Helper()
	_, err := h.Verify(context.Background(), []*x509.Certificate{leaf})
	e, ok := errors.AsType[*certcheck.Error](err)
	c.Assert(ok, qt.IsTrue, qt.Commentf("err: %v", err))
	return e
}

// TestProvider pins the synthetic provider: a cert-check Handler built with it accepts leaves of its
// CA as natural persons with their canonical DNI/NIE and holder name, and answers the deterministic
// error fixtures (revoked, outage, expired, forged, malformed document) as cert-check would.
func TestProvider(t *testing.T) {
	c := qt.New(t)
	pki, err := NewPKI()
	c.Assert(err, qt.IsNil)
	p, err := New(pki.CertPEM())
	c.Assert(err, qt.IsNil)
	h, err := certcheck.NewHandler([]certcheck.Provider{p})
	c.Assert(err, qt.IsNil)
	c.Assert(h.CertificateCheck(pki.CA.RawSubject), qt.IsTrue)

	dni, err := pki.Issue(Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	res, err := h.Verify(context.Background(), []*x509.Certificate{dni.Leaf})
	c.Assert(err, qt.IsNil)
	c.Assert(res.Provider.ID, qt.Equals, ID)
	c.Assert(res.Profile.HolderRole, qt.Equals, certcheck.RoleNaturalPerson)
	c.Assert(res.Identity.Document.Type, qt.Equals, certcheck.DocumentDNI)
	c.Assert(res.Identity.Document.Number, qt.Equals, "12345678Z")
	c.Assert(res.Identity.FullName, qt.Equals, "Maria García López")
	c.Assert(res.Diagnostics.DiagnosticID, qt.HasLen, 32)

	nie, err := pki.Issue(Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"})
	c.Assert(err, qt.IsNil)
	res, err = h.Verify(context.Background(), []*x509.Certificate{nie.Leaf})
	c.Assert(err, qt.IsNil)
	c.Assert(res.Identity.Document.Type, qt.Equals, certcheck.DocumentNIE)
	c.Assert(res.Identity.Document.Number, qt.Equals, "X1234567L")

	for _, tc := range []struct {
		name   string
		holder Holder
		code   certcheck.ErrorCode
	}{
		{"revoked", Holder{Document: "12345678Z", OU: OURevoked}, certcheck.CodeRevoked},
		{"outage", Holder{Document: "12345678Z", OU: OUOutage}, certcheck.CodeRevocationUnavailable},
		{"expired", Holder{Document: "12345678Z", NotAfter: time.Now().Add(-time.Hour)}, certcheck.CodeExpired},
		{"not yet valid", Holder{Document: "12345678Z", NotBefore: time.Now().Add(time.Hour)}, certcheck.CodeNotYetValid},
		{"wrong letter", Holder{Document: "12345678A"}, certcheck.CodeIdentityInvalid},
		{"not canonical", Holder{Document: "12.345.678-Z"}, certcheck.CodeIdentityInvalid},
	} {
		leaf, err := pki.Issue(tc.holder)
		c.Assert(err, qt.IsNil)
		e := verifyErr(c, h, leaf.Leaf)
		c.Assert(e.Code, qt.Equals, tc.code, qt.Commentf("%s", tc.name))
		c.Assert(e.Retryable, qt.Equals, tc.code == certcheck.CodeRevocationUnavailable, qt.Commentf("%s", tc.name))
		c.Assert(e.Diagnostics.DiagnosticID, qt.HasLen, 32)
	}

	// a forged certificate naming a real document chains to nothing the handler trusts
	forged, err := SelfSigned(Holder{Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	c.Assert(verifyErr(c, h, forged.Leaf).Code, qt.Equals, certcheck.CodeUntrustedIssuer)

	// the harness round-trips its CA through PEM
	keyPEM, err := pki.KeyPEM()
	c.Assert(err, qt.IsNil)
	loaded, err := LoadPKI(pki.CertPEM(), keyPEM)
	c.Assert(err, qt.IsNil)
	again, err := loaded.Issue(Holder{Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	_, err = h.Verify(context.Background(), []*x509.Certificate{again.Leaf})
	c.Assert(err, qt.IsNil)

	_, err = New([]byte("not a pem"))
	c.Assert(err, qt.Not(qt.IsNil))
}
```
Run: `go test -count=1 ./certauth/testprovider/`
Expected: `FAIL	github.com/vocdoni/saas-backend/certauth/testprovider [setup failed]` (no module provides cert-check yet)

Step 2: implement

Add the module: `GOPRIVATE=github.com/vocdoni/* go get github.com/vocdoni/cert-check@v0.1.1`, then (after the files
below exist) `go mod tidy`. Expected go.mod diff: `+	github.com/vocdoni/cert-check v0.1.1` in the main require
block, and its two go.sum lines.

`certauth/testprovider/testprovider.go` (new):
```go
// Package testprovider is a synthetic cert-check provider for integration tests and the e2e stack:
// cert-check rejects the specimens real CAs publish as test_certificate, so tests mint their own
// natural-person certificates from an in-memory CA (PKI) and the backend trusts that CA through
// Provider. Its key never lives in the backend: the harness holds the PKI, the backend only the
// CA certificate. Never enable it against a production API.
package testprovider

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/internal"
)

// ID is the provider ID of the synthetic provider.
const ID = "test"

// caName is the display name of the synthetic provider and the common name of its CA.
const caName = "Synthetic Test CA"

// Organizational units a leaf can carry to answer a deterministic error without any network.
const (
	// OURevoked makes Verify answer revoked.
	OURevoked = "REVOKED"
	// OUOutage makes Verify answer revocation_unavailable, as an OCSP/CRL outage does.
	OUOutage = "OUTAGE"
)

// documentPrefix is how the leaf subject serialNumber carries the document (ETSI EN 319 412-1).
const documentPrefix = "IDCES-"

var (
	oidGivenName = asn1.ObjectIdentifier{2, 5, 4, 42}
	oidSurname   = asn1.ObjectIdentifier{2, 5, 4, 4}
)

// Provider is the synthetic certcheck.Provider: it accepts natural-person leaves signed by its CA.
type Provider struct {
	ca  *x509.Certificate
	now func() time.Time
}

// New returns the provider of the CA certificate in caPEM (the first CERTIFICATE block).
func New(caPEM []byte) (*Provider, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("testprovider: no CERTIFICATE block in the CA PEM")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	if !ca.IsCA {
		return nil, fmt.Errorf("testprovider: the certificate is not a CA")
	}
	return NewWithCA(ca), nil
}

// NewWithCA returns the provider of ca.
func NewWithCA(ca *x509.Certificate) *Provider {
	return &Provider{ca: ca, now: time.Now}
}

// Info implements certcheck.Provider.
func (*Provider) Info() certcheck.ProviderInfo {
	return certcheck.ProviderInfo{
		ID: ID, Name: caName, Beta: true,
		Description: "Synthetic certificates for tests and the e2e stack",
	}
}

// CAs implements certcheck.Provider.
func (p *Provider) CAs() []*x509.Certificate { return []*x509.Certificate{p.ca} }

// Verify implements certcheck.Provider: the leaf must be signed by the CA and within its validity;
// the OUs REVOKED and OUTAGE answer revoked and revocation_unavailable; the subject serialNumber
// "IDCES-<DNI or NIE>" must hold a canonical number, and givenName/surname (or the CN) name the holder.
func (p *Provider) Verify(_ context.Context, chain []*x509.Certificate) (*certcheck.Result, error) {
	if len(chain) == 0 || chain[0] == nil {
		return nil, p.fail(nil, certcheck.StepBounds, certcheck.CodeNoCertificate, "no client certificate")
	}
	leaf := chain[0]
	if len(leaf.Raw) > certcheck.MaxCertificateDER {
		return nil, p.fail(nil, certcheck.StepBounds, certcheck.CodeInvalidChain, "leaf too large")
	}
	if err := leaf.CheckSignatureFrom(p.ca); err != nil {
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeUntrustedIssuer, "not signed by the synthetic CA")
	}
	now := p.now()
	switch {
	case now.Before(leaf.NotBefore):
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeNotYetValid, "not yet valid")
	case now.After(leaf.NotAfter):
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeExpired, "expired")
	}
	identity, ok := leafIdentity(leaf)
	if !ok {
		return nil, p.fail(leaf, certcheck.StepIdentity, certcheck.CodeIdentityInvalid, "no canonical IDCES document")
	}
	for _, ou := range leaf.Subject.OrganizationalUnit {
		switch ou {
		case OURevoked:
			return nil, p.fail(leaf, certcheck.StepRevocation, certcheck.CodeRevoked, "synthetic revocation")
		case OUOutage:
			return nil, p.fail(leaf, certcheck.StepRevocation, certcheck.CodeRevocationUnavailable, "synthetic outage")
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	return &certcheck.Result{
		Provider: certcheck.ProviderRef{ID: ID, Name: caName},
		Profile: certcheck.ProfileInfo{
			ID: "test-natural-person", Name: "Synthetic natural person", HolderRole: certcheck.RoleNaturalPerson,
		},
		Identity: identity,
		Certificate: certcheck.CertificateInfo{
			Subject: leaf.Subject.String(), Issuer: p.ca.Subject.CommonName, SerialHex: fmt.Sprintf("%x", leaf.SerialNumber),
			NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), SHA256: hex.EncodeToString(sum[:]),
		},
		ValidatedAt: now.UTC(),
		Diagnostics: certcheck.Diagnostics{
			DiagnosticID: certcheck.NewDiagnosticID(), Time: now.UTC(), Provider: ID,
			Certificate: certcheck.DescribeCertificate(leaf),
		},
	}, nil
}

// fail builds the *certcheck.Error of a failed step, with Diagnostics as cert-check fills them.
func (p *Provider) fail(leaf *x509.Certificate, step certcheck.Step, code certcheck.ErrorCode, detail string,
) *certcheck.Error {
	detail = "testprovider: " + detail
	d := certcheck.Diagnostics{
		DiagnosticID: certcheck.NewDiagnosticID(), Time: p.now().UTC(), Provider: ID,
		FailedStep: step, Code: code, TechnicalDetail: detail,
	}
	if leaf != nil {
		d.Certificate = certcheck.DescribeCertificate(leaf)
	}
	return &certcheck.Error{Code: code, Retryable: code.Retryable(), Detail: detail, Diagnostics: d}
}

// leafIdentity reads the holder identity from the leaf subject, reporting false unless its
// serialNumber is "IDCES-" and a canonical DNI or NIE.
func leafIdentity(leaf *x509.Certificate) (certcheck.Identity, bool) {
	raw := leaf.Subject.SerialNumber
	number, found := strings.CutPrefix(raw, documentPrefix)
	if !found {
		return certcheck.Identity{}, false
	}
	n := internal.NormalizeNationalID(number)
	if n.Class != internal.NationalIDCanonical || n.Canonical != number {
		return certcheck.Identity{}, false
	}
	docType := certcheck.DocumentDNI
	if n.Type == internal.NationalIDTypeNIE {
		docType = certcheck.DocumentNIE
	}
	given, surname := nameAttribute(leaf, oidGivenName), nameAttribute(leaf, oidSurname)
	fullName := strings.TrimSpace(given + " " + surname)
	if fullName == "" {
		fullName = leaf.Subject.CommonName
	}
	return certcheck.Identity{
		Document: certcheck.Document{
			Type: docType, Country: "ES", Number: n.Canonical, Raw: raw, Source: "subject.2.5.4.5",
		},
		GivenNames: given, Surnames: surname, FullName: fullName,
	}, true
}

// nameAttribute returns the first subject attribute of type oid, or "".
func nameAttribute(c *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, atv := range c.Subject.Names {
		if atv.Type.Equal(oid) {
			if s, ok := atv.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
```
`certauth/testprovider/pki.go` (new):
```go
package testprovider

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// PKI is an in-memory synthetic CA that mints the client certificates of the synthetic provider,
// and server certificates for test listeners. Tests create one with NewPKI; the e2e harness keeps
// one on disk with CertPEM/KeyPEM and LoadPKI.
type PKI struct {
	// CA is the CA certificate the backend trusts (New, NewWithCA).
	CA  *x509.Certificate
	key *ecdsa.PrivateKey
}

// Holder describes a client certificate to mint.
type Holder struct {
	GivenName string
	Surname   string
	// Document is the canonical DNI/NIE, written into the subject serialNumber as "IDCES-<Document>".
	Document string
	// OU, when set, is the subject organizational unit: OURevoked or OUOutage pick an error answer.
	OU string
	// NotBefore and NotAfter default to an hour ago and a year ahead.
	NotBefore time.Time
	NotAfter  time.Time
}

// NewPKI returns a fresh synthetic CA.
func NewPKI() (*PKI, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("testprovider: generating the CA key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: caName, Organization: []string{"Vocdoni test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("testprovider: creating the CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	return &PKI{CA: ca, key: key}, nil
}

// LoadPKI returns the PKI of a CA certificate and its EC private key, both PEM.
func LoadPKI(certPEM, keyPEM []byte) (*PKI, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("testprovider: loading the CA key pair: %w", err)
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("testprovider: the CA key is not an EC key")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	return &PKI{CA: ca, key: key}, nil
}

// CertPEM returns the CA certificate as PEM, what --certAuthTestProviderCA points at.
func (p *PKI) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.CA.Raw})
}

// KeyPEM returns the CA private key as PEM, for the harness only.
func (p *PKI) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(p.key)
	if err != nil {
		return nil, fmt.Errorf("testprovider: encoding the CA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// Issue mints a client certificate for h, signed by the CA.
func (p *PKI) Issue(h Holder) (*tls.Certificate, error) {
	return issue(h, p.CA, p.key)
}

// SelfSigned mints a client certificate for h signed by its own fresh key, not by any CA: a forged
// certificate that names a document without proving anything.
func SelfSigned(h Holder) (*tls.Certificate, error) {
	return issue(h, nil, nil)
}

// ServerCertificate mints a TLS server certificate for dnsNames (wildcards allowed), signed by the
// CA, as PEM certificate and key: what a test auth listener serves.
func (p *PKI) ServerCertificate(dnsNames ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: generating the server key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.CA, &key.PublicKey, p.key)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: creating the server certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: encoding the server key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// issue mints a client certificate for h, signed by parent and parentKey, or self-signed when
// parent is nil.
func issue(h Holder, parent *x509.Certificate, parentKey crypto.Signer) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("testprovider: generating the client key: %w", err)
	}
	notBefore, notAfter := h.NotBefore, h.NotAfter
	if notBefore.IsZero() {
		notBefore = time.Now().Add(-time.Hour)
	}
	if notAfter.IsZero() {
		notAfter = time.Now().AddDate(1, 0, 0)
	}
	subject := pkix.Name{
		CommonName:   fmt.Sprintf("%s %s - %s", h.Surname, h.GivenName, h.Document),
		SerialNumber: documentPrefix + h.Document,
		Country:      []string{"ES"},
		ExtraNames: []pkix.AttributeTypeAndValue{
			{Type: oidGivenName, Value: h.GivenName},
			{Type: oidSurname, Value: h.Surname},
		},
	}
	if h.OU != "" {
		subject.OrganizationalUnit = []string{h.OU}
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      subject,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, fmt.Errorf("testprovider: creating the client certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the client certificate: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// serial returns a random 128-bit certificate serial number.
func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err) // crypto/rand never fails since Go 1.24
	}
	return n
}
```
Run: `go mod tidy && git diff --exit-code go.mod go.sum >/dev/null || true; go test -count=1 ./certauth/testprovider/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth/testprovider` (TestProvider)

Step 3: commit
`git commit -m "feat(certauth): add the synthetic test certificate provider" -- certauth/testprovider/testprovider.go certauth/testprovider/pki.go certauth/testprovider/testprovider_test.go go.mod go.sum`

Interfaces this task provides to later tasks: `testprovider.New(caPEM) (*Provider, error)`, `NewWithCA`,
`testprovider.ID = "test"`, `OURevoked`, `OUOutage`; `testprovider.NewPKI()`, `LoadPKI(certPEM, keyPEM)`,
`(*PKI).CertPEM()`, `KeyPEM()`, `Issue(Holder) (*tls.Certificate, error)`, `ServerCertificate(dnsNames...)
(certPEM, keyPEM, err)`, `testprovider.SelfSigned(Holder)`, `testprovider.Holder{GivenName, Surname, Document, OU,
NotBefore, NotAfter}`.

### Task 5: certauth configuration, startup validation and provider composition
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/config.go`, `certauth/service.go`,
`certauth/random.go`, `certauth/helpers_test.go`, `certauth/config_test.go`
Goal: `certauth.Config` and `certauth.New`, failing loudly on every rule of spec section 1, and composing the
cert-check handler from FNMT natural person, idCAT and (guarded) the synthetic provider (serves AC16 startup part).

Step 1: write the failing test

`certauth/helpers_test.go` (new; the in-memory `Storage` all certauth unit tests run on):
```go
package certauth

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	testHost      = "auth.test"
	testOrigin    = "https://app.test"
	testReturnURL = testOrigin + "/ca/processes/0123456789abcdef01234567"
	testDefault   = testOrigin + "/"
)

// memStore is an in-memory Storage: one process with its census and members, the transactions
// and the counters, with the semantics of the Mongo implementation (single-use consumes that re-check
// their deadlines, a capped counter that can be released).
type memStore struct {
	mu        sync.Mutex
	txs       []*db.CertAuthTransaction
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	census    *db.Census
	members   map[string]*db.OrgMember // by canonical nationalId
	counters  map[string]int
	reset     time.Time
}

func newMemStore() *memStore {
	census := &db.Census{
		ID: bson.NewObjectID(), OrgAddress: common.Address{1}, AuthMethod: db.CensusAuthMethodCertificate,
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
	}
	return &memStore{
		vp: &db.VotingProcess{
			ID: bson.NewObjectID(), Published: true, CensusID: census.ID, OrgAddress: census.OrgAddress,
			StartDate: time.Now().Add(-time.Hour), EndDate: time.Now().Add(time.Hour),
		},
		questions: []db.VotingProcessQuestion{{Status: db.QuestionStatusReady}},
		census:    census,
		members:   map[string]*db.OrgMember{},
		counters:  map[string]int{},
		reset:     time.Now().Add(30 * time.Minute),
	}
}

// addMember adds a census member whose nationalId canonicalises to the given number.
func (m *memStore) addMember(nationalID string, weight uint64) *db.OrgMember {
	m.mu.Lock()
	defer m.mu.Unlock()
	member := &db.OrgMember{ID: bson.NewObjectID(), OrgAddress: m.census.OrgAddress, NationalID: nationalID, Weight: weight}
	m.members[internal.NormalizeNationalID(nationalID).Normalized] = member
	return member
}

func (m *memStore) InsertCertAuthTransaction(tx *db.CertAuthTransaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == tx.State {
			return fmt.Errorf("duplicate state")
		}
	}
	if tx.ID.IsZero() {
		tx.ID = bson.NewObjectID()
	}
	cp := *tx
	m.txs = append(m.txs, &cp)
	return nil
}

func (m *memStore) ConsumeCertAuthState(in db.CertAuthStateConsume) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == in.State && t.Nonce == in.Nonce && t.Status == db.CertAuthStatusCreated && t.CreatedAt.After(in.CreatedAfter) {
			before := *t
			t.Status, t.Code, t.Chain, t.ChainPresented = db.CertAuthStatusAuthenticated, in.Code, in.Chain, in.ChainPresented
			t.AuthenticatedAt, t.ExpiresAt = in.Now, in.ExpiresAt
			return &before, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) CertAuthTransactionByState(state, nonce string) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == state && t.Nonce == nonce {
			cp := *t
			return &cp, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) ConsumeCertAuthCode(code string, after time.Time) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.Code == code && t.Status == db.CertAuthStatusAuthenticated && t.AuthenticatedAt.After(after) {
			before := *t
			t.Status, t.Chain = db.CertAuthStatusConsumed, nil
			return &before, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) CertAuthTransactionByCode(code string) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.Code == code {
			cp := *t
			return &cp, nil
		}
	}
	return nil, db.ErrNotFound
}

// transaction returns the stored transaction holding the state.
func (m *memStore) transaction(state string) *db.CertAuthTransaction {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == state {
			cp := *t
			return &cp
		}
	}
	return nil
}

func (m *memStore) ProcessWithQuestions(id bson.ObjectID) (*db.VotingProcess, []db.VotingProcessQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.vp.ID {
		return nil, nil, db.ErrNotFound
	}
	vp := *m.vp
	return &vp, m.questions, nil
}

func (m *memStore) Census(string) (*db.Census, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *m.census
	return &c, nil
}

func (m *memStore) CensusParticipantByLoginHash(_ db.Census, member db.OrgMember) (*db.CensusParticipant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found, ok := m.members[internal.NormalizeNationalID(member.NationalID).Normalized]
	if !ok {
		return nil, db.ErrNotFound
	}
	return &db.CensusParticipant{ParticipantID: found.ID.Hex(), LoginHash: []byte("hash-" + found.ID.Hex())}, nil
}

func (m *memStore) OrgMember(_ common.Address, id string) (*db.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, member := range m.members {
		if member.ID.Hex() == id {
			return member, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) ClaimCSPCounter(kind string, key []byte, maxCount int) (db.CSPCounterResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxCount <= 0 {
		return db.CSPCounterResult{Granted: true}, nil
	}
	k := kind + "|" + string(key)
	if m.counters[k] >= maxCount {
		return db.CSPCounterResult{ResetAt: m.reset}, nil
	}
	m.counters[k]++
	return db.CSPCounterResult{Granted: true, Claim: db.CSPCounterClaim(k), ResetAt: m.reset}, nil
}

func (m *memStore) ReleaseCSPCounter(claim db.CSPCounterClaim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k := string(claim); m.counters[k] > 0 {
		m.counters[k]--
	}
	return nil
}

// fakeTokens issues sequential auth-only tokens and records whom it issued them to.
type fakeTokens struct {
	mu     sync.Mutex
	issued []string
}

func (f *fakeTokens) AuthToken(anchorID, uID internal.HexBytes, to string, ctype notifications.ChallengeType,
	_, _, _ string, _ common.Address,
) (internal.HexBytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(anchorID) == 0 || to != "" || ctype != "" {
		return nil, fmt.Errorf("not an auth-only token request")
	}
	f.issued = append(f.issued, uID.String())
	return internal.HexBytes{byte(len(f.issued))}, nil
}

func (f *fakeTokens) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issued)
}

// logEntry is one captured log line.
type logEntry struct {
	msg string
	kv  map[string]any
}

// captureLog records what the service logs.
type captureLog struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLog) add(msg string, keyvalues []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kv := map[string]any{}
	for i := 0; i+1 < len(keyvalues); i += 2 {
		kv[fmt.Sprint(keyvalues[i])] = keyvalues[i+1]
	}
	l.entries = append(l.entries, logEntry{msg: msg, kv: kv})
}

func (l *captureLog) Infow(msg string, keyvalues ...any) { l.add(msg, keyvalues) }
func (l *captureLog) Warnw(msg string, keyvalues ...any) { l.add(msg, keyvalues) }

func (l *captureLog) all() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logEntry(nil), l.entries...)
}

// testConfig is a valid origin-mode configuration for the PKI, its listener on port.
func testConfig(c *qt.C, pki *testprovider.PKI, port int) Config {
	certPEM, keyPEM, err := pki.ServerCertificate("*." + testHost)
	c.Assert(err, qt.IsNil)
	return Config{
		Mode: ModeOrigin, Host: testHost, ListenAddr: "127.0.0.1:" + strconv.Itoa(port), PublicPort: port,
		TLSCertPEM: certPEM, TLSKeyPEM: keyPEM, Providers: []string{"fnmt", "idcat"},
		AllowedOrigins: []string{testOrigin, "http://localhost:3000"}, DefaultReturn: testDefault,
		CreatesPerMinute: DefaultCreatesPerMinute, TestProvider: true, TestProviderCAPEM: pki.CertPEM(),
		VocdoniAPI: "http://127.0.0.1:9090/v2", RootKey: []byte("test-root-key"),
	}
}

// newTestService builds a service over a memStore with cfg adjusted by mutate, its logs captured.
// It serves nothing: newTestEnv adds the loopback auth listener.
func newTestService(c *qt.C, port int, mutate func(*Config)) (*Service, *memStore, *testprovider.PKI) {
	pki, err := testprovider.NewPKI()
	c.Assert(err, qt.IsNil)
	cfg := testConfig(c, pki, port)
	if mutate != nil {
		mutate(&cfg)
	}
	store := newMemStore()
	s, err := New(cfg, Deps{Storage: store, Tokens: &fakeTokens{}})
	c.Assert(err, qt.IsNil)
	s.log = &captureLog{}
	return s, store, pki
}
```
`certauth/config_test.go` (new):
```go
package certauth

import (
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
)

// TestCertAuthStartupValidation pins the startup rules (spec section 1, AC16): every misconfiguration
// refuses to start, and the PROXY protocol conveys the client IP only from the front CIDRs.
func TestCertAuthStartupValidation(t *testing.T) {
	c := qt.New(t)
	pki, err := testprovider.NewPKI()
	c.Assert(err, qt.IsNil)
	deps := Deps{Storage: newMemStore(), Tokens: &fakeTokens{}}
	otherCert, otherKey, err := pki.ServerCertificate("*.elsewhere.test")
	c.Assert(err, qt.IsNil)
	nonceOnly, nonceOnlyKey, err := pki.ServerCertificate(randomNonce() + "." + testHost)
	c.Assert(err, qt.IsNil)

	_, err = New(testConfig(c, pki, 443), deps)
	c.Assert(err, qt.IsNil)

	for name, mutate := range map[string]func(*Config){
		"unknown mode":                     func(cfg *Config) { cfg.Mode = "passthrough" },
		"host with port":                   func(cfg *Config) { cfg.Host = "auth.test:8443" },
		"upper case host":                  func(cfg *Config) { cfg.Host = "Auth.test" },
		"certificate for another domain":   func(cfg *Config) { cfg.TLSCertPEM, cfg.TLSKeyPEM = otherCert, otherKey },
		"certificate without the wildcard": func(cfg *Config) { cfg.TLSCertPEM, cfg.TLSKeyPEM = nonceOnly, nonceOnlyKey },
		"no certificate":                   func(cfg *Config) { cfg.TLSCertPEM = nil },
		"no allowed origin":                func(cfg *Config) { cfg.AllowedOrigins = nil },
		"http origin":                      func(cfg *Config) { cfg.AllowedOrigins = []string{"http://app.test"} },
		"origin with a path":               func(cfg *Config) { cfg.AllowedOrigins = []string{"https://app.test/"} },
		"upper case origin":                func(cfg *Config) { cfg.AllowedOrigins = []string{"https://App.test"} },
		"default return elsewhere":         func(cfg *Config) { cfg.DefaultReturn = "https://evil.test/" },
		"unknown provider":                 func(cfg *Config) { cfg.Providers = []string{"fnmt", "dnie"} },
		"representative provider":          func(cfg *Config) { cfg.Providers = []string{"fnmt-representative"} },
		"no provider":                      func(cfg *Config) { cfg.Providers, cfg.TestProvider = nil, false },
		"proxy protocol without fronts":    func(cfg *Config) { cfg.ProxyProtocol = true },
		"proxy protocol bad cidr": func(cfg *Config) {
			cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/33"}
		},
		"cloudflare without origin pull CA": func(cfg *Config) { cfg.Mode = ModeCloudflare },
		"proxy protocol in cloudflare mode": func(cfg *Config) {
			cfg.Mode, cfg.CFOriginPullCAPEM = ModeCloudflare, pki.CertPEM()
			cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/8"}
		},
		"test provider against production": func(cfg *Config) { cfg.VocdoniAPI = "https://api.vocdoni.io/v2" },
		"test provider against an unknown": func(cfg *Config) { cfg.VocdoniAPI = "https://api-lts.example.org/v2" },
		"test provider without CA":         func(cfg *Config) { cfg.TestProviderCAPEM = nil },
		"no root key":                      func(cfg *Config) { cfg.RootKey = nil },
	} {
		cfg := testConfig(c, pki, 443)
		mutate(&cfg)
		_, err := New(cfg, deps)
		c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("%s", name))
	}

	// accepted variations: localhost dev origins, an extra allowlisted test API, cloudflare with its CA
	for name, mutate := range map[string]func(*Config){
		"localhost origin": func(cfg *Config) { cfg.AllowedOrigins = append(cfg.AllowedOrigins, "http://127.0.0.1:5173") },
		"extra test api": func(cfg *Config) {
			cfg.VocdoniAPI, cfg.TestProviderAllowedAPIs = "https://api-e2e.internal/v2", []string{"api-e2e.internal"}
		},
		"dev api":    func(cfg *Config) { cfg.VocdoniAPI = "https://api-dev.vocdoni.net/v2" },
		"cloudflare": func(cfg *Config) { cfg.Mode, cfg.CFOriginPullCAPEM = ModeCloudflare, pki.CertPEM() },
	} {
		cfg := testConfig(c, pki, 443)
		mutate(&cfg)
		_, err := New(cfg, deps)
		c.Assert(err, qt.IsNil, qt.Commentf("%s", name))
	}
}
```
Run: `go test -count=1 -run TestCertAuthStartupValidation ./certauth/`
Expected: `certauth/helpers_test.go:263:59: undefined: Config`

Step 2: implement

`certauth/config.go` (new):
```go
// Package certauth signs voters in with a qualified digital certificate (FNMT natural person,
// idCAT) instead of typed member details. It owns the second, TLS listener the browser presents its
// certificate to, the per-attempt transaction flow (create, authenticate, redeem) of
// contract-cert-auth.md, and the error envelope of its failures. Verification is cert-check's; the
// match against the census is SP1's canonical nationalId login hash. A nil *Service means the
// deployment runs without the feature.
package certauth

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/cert-check/providers/fnmt"
	"github.com/vocdoni/cert-check/providers/idcat"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp"
)

// Modes of the auth listener, selected by --certAuthMode.
const (
	// ModeOrigin terminates TLS in this process with cert-check's TLS configuration.
	ModeOrigin = "origin"
	// ModeCloudflare trusts Cloudflare to terminate the browser's mTLS and forward the certificate
	// in headers. Not for production until its open blockers are verified (spec section 3b).
	ModeCloudflare = "cloudflare"
)

// Defaults of the configuration.
const (
	// DefaultListenAddr is the bind address of the auth listener.
	DefaultListenAddr = ":8443"
	// DefaultPublicPort is the port written into authUrl and probeUrl (omitted when 443).
	DefaultPublicPort = 443
	// DefaultCreatesPerMinute caps the attempts created per client IP and process per minute.
	DefaultCreatesPerMinute = 60
)

// Hosts, paths and protocol of the auth listener.
const (
	// nonceLen is the length of a nonce host label.
	nonceLen = 26
	// probeLabel is the first label of the probe host.
	probeLabel = "probe"
	// authenticatePath is the path of the authenticate endpoint on a nonce host.
	authenticatePath = "/v1/authenticate"
	// probePath is the path of the probe endpoint on the probe host.
	probePath = "/ok"
	// alpnHTTP1 is the only ALPN protocol the auth listener speaks: no HTTP/2 coalescing across hosts.
	alpnHTTP1 = "http/1.1"
)

// Lifetimes of an attempt (contract sections 3 and 4).
const (
	stateTTL = 15 * time.Minute
	codeTTL  = 5 * time.Minute
	// verifyTimeout bounds Handler.Verify (revocation): the main listener times out at 45 s and the
	// app waits 45 s.
	verifyTimeout = 40 * time.Second
)

// testProviderAPIHosts are the --vocdoniApi hosts the synthetic test provider may run against
// besides --certAuthTestProviderAllowedAPIs: an allowlist, so an unknown API fails closed.
var testProviderAPIHosts = []string{"localhost", "127.0.0.1", "api-dev.vocdoni.net", "api-stg.vocdoni.net"}

// providerConstructors are the cert-check providers a deployment may enable: natural persons only
// (owner decisions 1 and 4). The representative and employee constructors are never referenced.
var providerConstructors = map[string]func(...certcheck.Option) certcheck.Provider{
	"fnmt":  fnmt.New,
	"idcat": idcat.New,
}

// Config is the certificate sign-in configuration (the --certAuth* flags). PEM material is passed
// as bytes; cmd/service reads the files.
type Config struct {
	// Mode is ModeOrigin or ModeCloudflare.
	Mode string
	// Host is the auth domain suffix: attempts run on <nonce>.<Host>, the probe on probe.<Host>.
	Host string
	// ListenAddr is the bind address of the auth listener.
	ListenAddr string
	// PublicPort is the port of authUrl and probeUrl, omitted from them when 443.
	PublicPort int
	// TLSCertPEM and TLSKeyPEM are the auth listener's server certificate, covering *.<Host>.
	TLSCertPEM []byte
	TLSKeyPEM  []byte
	// Providers are the cert-check provider IDs to enable ("fnmt", "idcat").
	Providers []string
	// AllowedOrigins are the exact app origins a returnUrl, the CORS of the two JSON endpoints and
	// the probe accept. https, or http for localhost and 127.0.0.1 only.
	AllowedOrigins []string
	// DefaultReturn is where an unknown attempt is sent back to (an allowlisted origin's URL).
	DefaultReturn string
	// CreatesPerMinute caps the attempts created per client IP and process per minute; <= 0 disables it.
	CreatesPerMinute int
	// MaxHourlyFailures caps the redeems per presented certificate, and per verified document, on a
	// process per hour. 0 uses the CSP's default; a negative value disables the cap.
	MaxHourlyFailures int
	// TrustForwardedFor takes the client IP from the rightmost X-Forwarded-For entry: set it when the
	// main listener is only reachable through the trusted edge proxy (api.Config.OriginSecret).
	TrustForwardedFor bool
	// ProxyProtocol accepts PROXY protocol headers on the auth listener (origin mode), only from
	// the ProxyFrontCIDRs.
	ProxyProtocol   bool
	ProxyFrontCIDRs []string
	// CFOriginPullCAPEM is the CA of Cloudflare's Authenticated Origin Pulls client certificate
	// (cloudflare mode, required).
	CFOriginPullCAPEM []byte
	// CFAllowedCIDRs, when set, are the only peers the auth listener accepts in cloudflare mode.
	CFAllowedCIDRs []string
	// TestProvider adds the synthetic provider of TestProviderCAPEM, only against an allowlisted API.
	TestProvider            bool
	TestProviderCAPEM       []byte
	TestProviderAllowedAPIs []string
	// VocdoniAPI is the --vocdoniApi URL, checked against the test provider allowlist.
	VocdoniAPI string
	// RootKey is the service root key, from which the voterRef secret derives.
	RootKey []byte
}

// validated is what New derives from a valid Config.
type validated struct {
	serverCert *tls.Certificate
	origins    map[string]bool
	proxyNets  []*net.IPNet
	cfNets     []*net.IPNet
	cfCAs      *x509.CertPool
	providers  []certcheck.Provider
	maxFails   int
	warnings   []string
}

// validate checks every rule of spec section 1, failing on the first broken one.
func (cfg *Config) validate() (*validated, error) {
	v := &validated{}
	switch cfg.Mode {
	case ModeOrigin, ModeCloudflare:
	default:
		return nil, fmt.Errorf("certauth: unknown mode %q (origin or cloudflare)", cfg.Mode)
	}
	host := strings.ToLower(cfg.Host)
	if host != cfg.Host || !strings.Contains(host, ".") || strings.ContainsAny(host, ":/ *") {
		return nil, fmt.Errorf("certauth: host %q must be a lower case domain name without port", cfg.Host)
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	if cfg.PublicPort == 0 {
		cfg.PublicPort = DefaultPublicPort
	}
	if cfg.PublicPort < 1 || cfg.PublicPort > 65535 {
		return nil, fmt.Errorf("certauth: invalid public port %d", cfg.PublicPort)
	}
	cert, err := serverCertificate(cfg.TLSCertPEM, cfg.TLSKeyPEM, host)
	if err != nil {
		return nil, err
	}
	v.serverCert = cert
	if v.origins, err = parseOrigins(cfg.AllowedOrigins); err != nil {
		return nil, err
	}
	if err := checkDefaultReturn(cfg.DefaultReturn, v.origins); err != nil {
		return nil, err
	}
	if len(cfg.RootKey) == 0 {
		return nil, fmt.Errorf("certauth: the root key is required")
	}
	v.maxFails = cfg.MaxHourlyFailures // negative: ClaimCSPCounter disables the cap
	if v.maxFails == 0 {
		v.maxFails = csp.DefaultMaxHourlyAuthFailures
	}
	if err := cfg.validateFronts(v); err != nil {
		return nil, err
	}
	if v.providers, err = cfg.buildProviders(); err != nil {
		return nil, err
	}
	if cfg.TestProvider {
		v.warnings = append(v.warnings, "the synthetic test provider is enabled: never run this against production")
	}
	if cfg.Mode == ModeCloudflare {
		v.warnings = append(v.warnings, "cloudflare mode is not production ready: CA advertisement, HTTP/2 coalescing, "+
			"no-certificate forwarding and the mTLS hostname association are unverified (spec 3b)")
	}
	return v, nil
}

// validateFronts checks the PROXY protocol and cloudflare settings.
func (cfg *Config) validateFronts(v *validated) error {
	var err error
	if cfg.ProxyProtocol {
		if cfg.Mode != ModeOrigin {
			return fmt.Errorf("certauth: the PROXY protocol is only for origin mode")
		}
		if len(cfg.ProxyFrontCIDRs) == 0 {
			return fmt.Errorf("certauth: the PROXY protocol needs the front CIDRs allowed to send it")
		}
		if v.proxyNets, err = parseCIDRs(cfg.ProxyFrontCIDRs); err != nil {
			return err
		}
	}
	if cfg.Mode != ModeCloudflare {
		return nil
	}
	v.cfCAs = x509.NewCertPool()
	if !v.cfCAs.AppendCertsFromPEM(cfg.CFOriginPullCAPEM) {
		return fmt.Errorf("certauth: cloudflare mode needs the Authenticated Origin Pulls CA (PEM)")
	}
	if v.cfNets, err = parseCIDRs(cfg.CFAllowedCIDRs); err != nil {
		return err
	}
	return nil
}

// buildProviders constructs the configured cert-check providers, plus the synthetic one when it is
// enabled against an allowlisted API.
func (cfg *Config) buildProviders() ([]certcheck.Provider, error) {
	var providers []certcheck.Provider
	for _, id := range cfg.Providers {
		newProvider, ok := providerConstructors[id]
		if !ok {
			return nil, fmt.Errorf("certauth: unknown certificate provider %q (fnmt, idcat)", id)
		}
		providers = append(providers, newProvider())
	}
	if cfg.TestProvider {
		if err := checkTestProviderAPI(cfg.VocdoniAPI, cfg.TestProviderAllowedAPIs); err != nil {
			return nil, err
		}
		p, err := testprovider.New(cfg.TestProviderCAPEM)
		if err != nil {
			return nil, fmt.Errorf("certauth: test provider: %w", err)
		}
		providers = append(providers, p)
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("certauth: no certificate provider configured")
	}
	return providers, nil
}

// serverCertificate loads the auth listener certificate and checks it covers every host the
// listener answers on: a sample nonce host and the probe host.
func serverCertificate(certPEM, keyPEM []byte, host string) (*tls.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("certauth: loading the TLS certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("certauth: parsing the TLS certificate: %w", err)
	}
	for _, name := range []string{strings.Repeat("a", nonceLen) + "." + host, probeLabel + "." + host} {
		if err := leaf.VerifyHostname(name); err != nil {
			return nil, fmt.Errorf("certauth: the TLS certificate does not cover %s: %w", name, err)
		}
	}
	pair.Leaf = leaf
	return &pair, nil
}

// parseOrigins validates the allowed origins: exact scheme://host[:port], https, or http for a
// localhost or 127.0.0.1 host.
func parseOrigins(list []string) (map[string]bool, error) {
	if len(list) == 0 {
		return nil, fmt.Errorf("certauth: at least one allowed origin is required")
	}
	origins := make(map[string]bool, len(list))
	for _, o := range list {
		u, err := url.Parse(o)
		if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" ||
			o != strings.ToLower(u.Scheme+"://"+u.Host) {
			return nil, fmt.Errorf("certauth: allowed origin %q must be a lower case scheme://host[:port]", o)
		}
		if !secureScheme(u) {
			return nil, fmt.Errorf("certauth: allowed origin %q must be https (http only for localhost)", o)
		}
		origins[o] = true
	}
	return origins, nil
}

// secureScheme reports whether u is https, or http on localhost or 127.0.0.1.
func secureScheme(u *url.URL) bool {
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"
	default:
		return false
	}
}

// checkDefaultReturn requires the attempt_expired fallback to be an absolute URL on an allowed
// origin, so even that redirect never leaves the app.
func checkDefaultReturn(raw string, origins map[string]bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || !origins[u.Scheme+"://"+u.Host] {
		return fmt.Errorf("certauth: the default return %q must be a URL on an allowed origin", raw)
	}
	return nil
}

// parseCIDRs parses a list of CIDRs.
func parseCIDRs(list []string) ([]*net.IPNet, error) {
	nets := make([]*net.IPNet, 0, len(list))
	for _, s := range list {
		_, n, err := net.ParseCIDR(strings.TrimSpace(s))
		if err != nil {
			return nil, fmt.Errorf("certauth: invalid CIDR %q: %w", s, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// checkTestProviderAPI refuses the synthetic provider unless the API host is allowlisted.
func checkTestProviderAPI(apiURL string, extra []string) error {
	u, err := url.Parse(apiURL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("certauth: the test provider needs a valid --vocdoniApi URL, got %q", apiURL)
	}
	host := strings.ToLower(u.Hostname())
	if slices.Contains(testProviderAPIHosts, host) || slices.Contains(extra, host) {
		return nil
	}
	return fmt.Errorf("certauth: the test provider is refused against the API host %q", host)
}

// voterRefKey derives the voterRef HMAC key from the root key: stable across replicas and restarts.
func voterRefKey(rootKey []byte) []byte {
	sum := sha256.Sum256(append([]byte("vocdoni-certauth-voterref"), rootKey...))
	return sum[:]
}
```
`certauth/service.go` (new):
```go
package certauth

import (
	"crypto/tls"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// Storage is the persistence the certificate flow needs; *db.MongoStorage implements it.
type Storage interface {
	InsertCertAuthTransaction(tx *db.CertAuthTransaction) error
	ConsumeCertAuthState(in db.CertAuthStateConsume) (*db.CertAuthTransaction, error)
	CertAuthTransactionByState(state, nonce string) (*db.CertAuthTransaction, error)
	ConsumeCertAuthCode(code string, authenticatedAfter time.Time) (*db.CertAuthTransaction, error)
	CertAuthTransactionByCode(code string) (*db.CertAuthTransaction, error)
	ProcessWithQuestions(id bson.ObjectID) (*db.VotingProcess, []db.VotingProcessQuestion, error)
	Census(censusID string) (*db.Census, error)
	CensusParticipantByLoginHash(census db.Census, member db.OrgMember) (*db.CensusParticipant, error)
	OrgMember(orgAddress common.Address, id string) (*db.OrgMember, error)
	ClaimCSPCounter(kind string, key []byte, maxCount int) (db.CSPCounterResult, error)
	ReleaseCSPCounter(claim db.CSPCounterClaim) error
}

// TokenIssuer issues the pre-verified auth-only CSP token an auth/0 census issues; *csp.CSP
// implements it.
type TokenIssuer interface {
	AuthToken(anchorID, uID internal.HexBytes, to string, ctype notifications.ChallengeType, lang string,
		orgName, orgLogo string, orgAddress common.Address) (internal.HexBytes, error)
}

// Deps are the services the certificate flow runs on.
type Deps struct {
	Storage Storage
	Tokens  TokenIssuer
}

// logger is the structured log sink. Tests replace it to check what reaches the logs: never a
// DNI/NIE, a name, a certificate, a state, a code or a verifier.
type logger interface {
	Infow(msg string, keyvalues ...any)
	Warnw(msg string, keyvalues ...any)
}

// dvoteLogger logs through the service logger.
type dvoteLogger struct{}

func (dvoteLogger) Infow(msg string, keyvalues ...any) { log.Infow(msg, keyvalues...) }
func (dvoteLogger) Warnw(msg string, keyvalues ...any) { log.Warnw(msg, keyvalues...) }

// Service is the certificate sign-in feature: the auth listener and the create and redeem
// handlers. It is immutable after New and safe for concurrent use.
type Service struct {
	cfg     Config
	v       *validated
	store   Storage
	tokens  TokenIssuer
	handler *certcheck.Handler
	// probeTLS and authTLS are the per-SNI server configurations of origin mode.
	probeTLS *tls.Config
	authTLS  *tls.Config
	refKey   []byte
	log      logger
	now      func() time.Time
}

// New validates cfg (spec section 1: every rule fails loudly) and builds the service: the
// cert-check handler of the configured natural-person providers, the TLS configurations and the
// voterRef key. Call it only when the feature is configured; feature off is a nil *Service.
func New(cfg Config, deps Deps) (*Service, error) {
	if deps.Storage == nil || deps.Tokens == nil {
		return nil, fmt.Errorf("certauth: storage and token issuer are required")
	}
	v, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	handler, err := certcheck.NewHandler(v.providers)
	if err != nil {
		return nil, fmt.Errorf("certauth: building the certificate handler: %w", err)
	}
	s := &Service{
		cfg: cfg, v: v, store: deps.Storage, tokens: deps.Tokens, handler: handler,
		refKey: voterRefKey(cfg.RootKey), log: dvoteLogger{}, now: time.Now,
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return v.serverCert, nil }
	s.authTLS = handler.TLSConfig(getCert)
	s.probeTLS = &tls.Config{
		MinVersion: tls.VersionTLS12, NextProtos: []string{alpnHTTP1}, GetCertificate: getCert,
		ClientAuth: tls.NoClientCert, SessionTicketsDisabled: true,
	}
	for _, w := range v.warnings {
		s.log.Warnw("certificate sign-in: " + w)
	}
	ids := make([]string, 0, len(v.providers))
	for _, p := range v.providers {
		ids = append(ids, p.Info().ID)
	}
	s.log.Infow("certificate sign-in enabled", "mode", cfg.Mode, "host", cfg.Host, "listen", cfg.ListenAddr,
		"providers", ids)
	return s, nil
}

// AllowedOrigins returns the allowlisted app origins, sorted.
func (s *Service) AllowedOrigins() []string {
	origins := make([]string, 0, len(s.v.origins))
	for o := range s.v.origins {
		origins = append(origins, o)
	}
	slices.Sort(origins)
	return origins
}
```
`certauth/random.go` (new):
```go
package certauth

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/base64"
	"regexp"
	"strings"
)

// nonceEncoding writes the 16 random bytes of a nonce as a 26-character DNS label.
var nonceEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

var (
	// tokenRe is the grammar of a state, a code and an S256 challenge: 32 bytes, base64url, no padding.
	tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	// nonceRe is the grammar of a nonce host label.
	nonceRe = regexp.MustCompile(`^[a-z2-7]{26}$`)
	// verifierRe is the RFC 7636 code verifier grammar.
	verifierRe = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
)

// randomToken returns 32 random bytes as base64url without padding (43 characters): a state or a code.
func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails since Go 1.24
	return base64.RawURLEncoding.EncodeToString(b)
}

// randomNonce returns 16 random bytes as a lower case base32 label (26 characters), independent of
// any state: an observer of the DNS or the SNI cannot link the host to the redeem.
func randomNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToLower(nonceEncoding.EncodeToString(b))
}
```
Run: `go test -count=1 -run TestCertAuthStartupValidation ./certauth/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (TestCertAuthStartupValidation)

Step 3: commit
`git commit -m "feat(certauth): configure and validate certificate sign-in" -- certauth/config.go certauth/service.go certauth/random.go certauth/helpers_test.go certauth/config_test.go`

Interfaces this task provides to later tasks: `certauth.Config`, `ModeOrigin`, `ModeCloudflare`,
`DefaultListenAddr`, `DefaultPublicPort`, `DefaultCreatesPerMinute`; `certauth.Storage`, `TokenIssuer`, `Deps`,
`New(cfg Config, deps Deps) (*Service, error)`, `(*Service).AllowedOrigins() []string`; unexported:
`stateTTL`, `codeTTL`, `verifyTimeout`, `nonceLen`, `probeLabel`, `authenticatePath`, `probePath`, `alpnHTTP1`,
`validated` (`serverCert`, `origins`, `proxyNets`, `cfNets`, `cfCAs`, `providers`, `maxFails`), `secureScheme`,
`randomToken()`, `randomNonce()`, `tokenRe`, `nonceRe`, `verifierRe`, `Service.log` (test seam), `Service.now`;
test helpers `memStore`, `fakeTokens`, `captureLog`, `testConfig`, `newTestService`.

### Task 6: Auth listener, origin mode, with the probe and the PROXY protocol
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/listener.go`, `certauth/env_test.go`,
`certauth/listener_test.go`, `go.mod`, `go.sum`
Goal: the second listener: per-SNI TLS (probe without CertificateRequest, nonce hosts with cert-check's TLS 1.2
configuration), Host routing, `GET /ok`, `GET /v1/authenticate` with its bodiless 303s, chain storage bounds, and
PROXY headers honoured only from the front CIDRs (serves AC7, AC16 PROXY part).

Step 1: write the failing test

`certauth/env_test.go` (new):
```go
package certauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
)

// testEnv is a certificate service on a loopback auth listener over a memStore.
type testEnv struct {
	s      *Service
	store  *memStore
	tokens *fakeTokens
	logs   *captureLog
	pki    *testprovider.PKI
	port   int
}

// newTestEnv starts a service with cfg adjusted by mutate, serving the auth listener on loopback.
func newTestEnv(c *qt.C, mutate func(*Config)) *testEnv {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	c.Assert(err, qt.IsNil)
	port := ln.Addr().(*net.TCPAddr).Port
	s, store, pki := newTestService(c, port, mutate)
	go func() { _ = s.Serve(ln) }()
	c.Cleanup(func() { _ = ln.Close() })
	tokens, ok := s.tokens.(*fakeTokens)
	c.Assert(ok, qt.IsTrue)
	logs, ok := s.log.(*captureLog)
	c.Assert(ok, qt.IsTrue)
	return &testEnv{s: s, store: store, tokens: tokens, logs: logs, pki: pki, port: port}
}

// attempt stores a fresh created attempt on the env's process, as the create endpoint does, and
// returns its state and auth URL.
func (e *testEnv) attempt(c *qt.C, returnURL string) (state, authURL string) {
	state, nonce, now := randomToken(), randomNonce(), time.Now()
	c.Assert(e.store.InsertCertAuthTransaction(&db.CertAuthTransaction{
		State: state, Nonce: nonce, Challenge: "challenge", ProcessID: e.store.vp.ID, ReturnURL: returnURL,
		Purpose: "vote", Status: db.CertAuthStatusCreated, CreatedAt: now, ExpiresAt: now.Add(stateTTL),
	}), qt.IsNil)
	return state, e.authURL(nonce, state)
}

// portString is the listener port as text.
func (e *testEnv) portString() string { return strconv.Itoa(e.port) }

// authURL is the auth listener URL of a nonce host and a state.
func (e *testEnv) authURL(nonce, state string) string {
	return "https://" + nonce + "." + testHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + state
}

// clientOptions shape the TLS client of the auth listener.
type clientOptions struct {
	cert       *tls.Certificate // presented when the server asks; nil presents none
	serverName string           // overrides the SNI
	onRequest  func(*tls.CertificateRequestInfo)
	dial       func(ctx context.Context, network, addr string) (net.Conn, error)
}

// client returns an HTTP client of the auth listener that never follows redirects and dials
// loopback whatever the URL host.
func (e *testEnv) client(o clientOptions) *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(e.pki.CA)
	dial := o.dial
	if dial == nil {
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(e.port))
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext: dial, DisableKeepAlives: true,
			TLSClientConfig: &tls.Config{
				RootCAs: roots, ServerName: o.serverName, MinVersion: tls.VersionTLS12,
				GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
					if o.onRequest != nil {
						o.onRequest(cri)
					}
					if o.cert == nil {
						return &tls.Certificate{}, nil
					}
					return o.cert, nil
				},
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
}

// authenticate GETs authURL with the certificate (nil: none) and returns the response.
func (e *testEnv) authenticate(c *qt.C, authURL string, cert *tls.Certificate) *http.Response {
	resp, err := e.client(clientOptions{cert: cert}).Get(authURL)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// fragment returns the target and the parsed fragment of a 303.
func fragment(c *qt.C, resp *http.Response) (string, url.Values) {
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
	loc, err := url.Parse(resp.Header.Get("Location"))
	c.Assert(err, qt.IsNil)
	values, err := url.ParseQuery(loc.Fragment)
	c.Assert(err, qt.IsNil)
	loc.Fragment = ""
	return loc.String(), values
}

// issue mints a synthetic certificate.
func (e *testEnv) issue(c *qt.C, h testprovider.Holder) *tls.Certificate {
	cert, err := e.pki.Issue(h)
	c.Assert(err, qt.IsNil)
	return cert
}
```
`certauth/listener_test.go` (new):
```go
package certauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	proxyproto "github.com/pires/go-proxyproto"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
)

// TestCertAuthListener pins the auth listener (AC7): TLS 1.2, the providers' CAs in the
// CertificateRequest, ALPN http/1.1, no session resumption; the probe never asks for a certificate;
// a valid state answers the 303 with its code whether or not a certificate came; spent, expired,
// unknown and foreign-host states answer attempt_expired to the right return; unknown hosts 421.
func TestCertAuthListener(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	state, authURL := e.attempt(c, testReturnURL)
	nonceHost := e.store.transaction(state).Nonce + "." + testHost
	roots := x509.NewCertPool()
	roots.AddCert(e.pki.CA)
	addr := "127.0.0.1:" + strconv.Itoa(e.port)

	c.Run("handshake", func(c *qt.C) {
		var acceptable [][]byte
		cfg := &tls.Config{
			RootCAs: roots, ServerName: nonceHost, NextProtos: []string{"h2", "http/1.1"},
			ClientSessionCache: tls.NewLRUClientSessionCache(4), MinVersion: tls.VersionTLS12,
			GetClientCertificate: func(cri *tls.CertificateRequestInfo) (*tls.Certificate, error) {
				acceptable = cri.AcceptableCAs
				return &tls.Certificate{}, nil
			},
		}
		for i := range 2 {
			conn, err := tls.Dial("tcp", addr, cfg)
			c.Assert(err, qt.IsNil)
			state := conn.ConnectionState()
			c.Assert(state.Version, qt.Equals, uint16(tls.VersionTLS12))
			c.Assert(state.NegotiatedProtocol, qt.Equals, "http/1.1")
			// session tickets are off: the second connection is a full handshake, picker included
			c.Assert(state.DidResume, qt.IsFalse, qt.Commentf("connection %d", i))
			c.Assert(conn.Close(), qt.IsNil)
		}
		var want []string
		for _, p := range e.s.handler.Providers() {
			for _, ca := range p.CAs() {
				want = append(want, string(ca.RawSubject))
			}
		}
		got := make([]string, 0, len(acceptable))
		for _, name := range acceptable {
			got = append(got, string(name))
		}
		slices.Sort(want)
		slices.Sort(got)
		c.Assert(got, qt.DeepEquals, slices.Compact(want))
		c.Assert(slices.Contains(got, string(e.pki.CA.RawSubject)), qt.IsTrue)

		// the probe host never sends a CertificateRequest
		asked := false
		probeCfg := cfg.Clone()
		probeCfg.ServerName = "probe." + testHost
		probeCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			asked = true
			return &tls.Certificate{}, nil
		}
		conn, err := tls.Dial("tcp", addr, probeCfg)
		c.Assert(err, qt.IsNil)
		c.Assert(conn.Close(), qt.IsNil)
		c.Assert(asked, qt.IsFalse)

		// any other name fails the handshake
		other := cfg.Clone()
		other.ServerName = "www." + testHost
		_, err = tls.Dial("tcp", addr, other)
		c.Assert(err, qt.Not(qt.IsNil))
	})

	c.Run("authenticate", func(c *qt.C) {
		cert := e.issue(c, testprovider.Holder{GivenName: "Maria", Surname: "García", Document: "12345678Z"})
		for _, presented := range []*tls.Certificate{cert, nil} {
			state, authURL := e.attempt(c, testReturnURL)
			resp := e.authenticate(c, authURL, presented)
			target, frag := fragment(c, resp)
			c.Assert(target, qt.Equals, testReturnURL)
			c.Assert(frag.Get("state"), qt.Equals, state)
			c.Assert(frag.Get("code"), qt.Matches, `[A-Za-z0-9_-]{43}`)
			c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "no-store")
			c.Assert(resp.Header.Get("Referrer-Policy"), qt.Equals, "no-referrer")
			c.Assert(resp.Close, qt.IsTrue) // Connection: close
			body, err := io.ReadAll(resp.Body)
			c.Assert(err, qt.IsNil)
			c.Assert(body, qt.HasLen, 0)
			tx := e.store.transaction(state)
			c.Assert(tx.Status, qt.Equals, db.CertAuthStatusAuthenticated)
			c.Assert(tx.ChainPresented, qt.Equals, presented != nil)
			if presented != nil {
				c.Assert(tx.Chain, qt.DeepEquals, [][]byte{cert.Certificate[0]})
			}

			// the same state again is spent: back to its own return URL
			target, frag = fragment(c, e.authenticate(c, authURL, presented))
			c.Assert(target, qt.Equals, testReturnURL)
			c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")
		}

		// an expired state goes back to its own return URL too
		old := &db.CertAuthTransaction{
			State: randomToken(), Nonce: randomNonce(), Challenge: "c", ProcessID: e.store.vp.ID,
			ReturnURL: testOrigin + "/es", Purpose: "vote", Status: db.CertAuthStatusCreated,
			CreatedAt: time.Now().Add(-16 * time.Minute),
		}
		c.Assert(e.store.InsertCertAuthTransaction(old), qt.IsNil)
		oldURL := "https://" + old.Nonce + "." + testHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + old.State
		target, frag := fragment(c, e.authenticate(c, oldURL, nil))
		c.Assert(target, qt.Equals, testOrigin+"/es")
		c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")

		// an unknown state, a malformed one, and a state replayed on another nonce host are unknown:
		// the default return
		state, _ := e.attempt(c, testReturnURL)
		for _, u := range []string{
			"https://" + nonceHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=" + randomToken(),
			"https://" + nonceHost + ":" + strconv.Itoa(e.port) + authenticatePath + "?state=short",
			e.authURL(randomNonce(), state),
		} {
			target, frag := fragment(c, e.authenticate(c, u, nil))
			c.Assert(target, qt.Equals, testDefault, qt.Commentf("%s", u))
			c.Assert(frag.Get("error"), qt.Equals, "attempt_expired")
		}
		// the replay on another host did not spend the state
		c.Assert(e.store.transaction(state).Status, qt.Equals, db.CertAuthStatusCreated)
	})

	c.Run("probe", func(c *qt.C) {
		probeOK := `{"ok":true}`
		probe := "https://probe." + testHost + ":" + strconv.Itoa(e.port) + probePath
		for origin, allowed := range map[string]bool{testOrigin: true, "https://evil.test": false} {
			req, err := http.NewRequest(http.MethodGet, probe, nil)
			c.Assert(err, qt.IsNil)
			req.Header.Set("Origin", origin)
			resp, err := e.client(clientOptions{}).Do(req)
			c.Assert(err, qt.IsNil)
			body, err := io.ReadAll(resp.Body)
			c.Assert(err, qt.IsNil)
			c.Assert(resp.Body.Close(), qt.IsNil)
			c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
			c.Assert(string(body), qt.Equals, probeOK)
			c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "no-store")
			want := ""
			if allowed {
				want = origin
			}
			c.Assert(resp.Header.Get("Access-Control-Allow-Origin"), qt.Equals, want)
			c.Assert(resp.Header.Get("Access-Control-Allow-Credentials"), qt.Equals, "")
		}
	})

	c.Run("misdirected", func(c *qt.C) {
		// a Host that names nothing the listener serves, or another host than the SNI, is 421
		for _, host := range []string{"evil.test", randomNonce() + "." + testHost} {
			req, err := http.NewRequest(http.MethodGet, authURL, nil)
			c.Assert(err, qt.IsNil)
			req.Host = host
			resp, err := e.client(clientOptions{serverName: nonceHost}).Do(req)
			c.Assert(err, qt.IsNil)
			c.Assert(resp.Body.Close(), qt.IsNil)
			c.Assert(resp.StatusCode, qt.Equals, http.StatusMisdirectedRequest, qt.Commentf("host %s", host))
		}
	})
}

// TestBoundChain pins the storage bounds of a presented chain: within 10 certificates and 200 KiB
// it is kept, beyond them the leaf alone when it is within cert-check's size bound, else nothing.
func TestBoundChain(t *testing.T) {
	c := qt.New(t)
	leaf, small, big := []byte("leaf"), make([]byte, 1024), make([]byte, 17<<10)
	c.Assert(boundChain(nil), qt.IsNil)
	c.Assert(boundChain([][]byte{leaf, small}), qt.DeepEquals, [][]byte{leaf, small})
	long := [][]byte{leaf}
	for range 10 {
		long = append(long, small)
	}
	c.Assert(boundChain(long), qt.DeepEquals, [][]byte{leaf})
	heavy := [][]byte{leaf}
	for range 9 {
		heavy = append(heavy, make([]byte, 25<<10))
	}
	c.Assert(boundChain(heavy), qt.DeepEquals, [][]byte{leaf})
	c.Assert(boundChain(append([][]byte{big}, long[1:]...)), qt.IsNil)
}

// TestCertAuthStartupValidationProxyProtocol pins the PROXY protocol front (AC16): from a front
// CIDR the header's source is the client IP the logs record; from anyone else the connection is
// plain TCP, so a header fails the handshake and nobody else chooses the logged IP.
func TestCertAuthStartupValidationProxyProtocol(t *testing.T) {
	c := qt.New(t)
	conveyed := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 5555}
	withProxyHeader := func(e *testEnv) func(ctx context.Context, network, addr string) (net.Conn, error) {
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(e.port))
			if err != nil {
				return nil, err
			}
			header := &proxyproto.Header{
				Version: 2, Command: proxyproto.PROXY, TransportProtocol: proxyproto.TCPv4,
				SourceAddr: conveyed, DestinationAddr: conn.RemoteAddr(),
			}
			if _, err := header.WriteTo(conn); err != nil {
				return nil, err
			}
			return conn, nil
		}
	}
	lastClientIP := func(e *testEnv) any {
		var ip any
		for _, entry := range e.logs.all() {
			if entry.msg == "certificate attempt authenticated" {
				ip = entry.kv["clientIP"]
			}
		}
		return ip
	}

	// from a front CIDR the header is read and its source is the client IP the logs record
	front := newTestEnv(c, func(cfg *Config) {
		cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"127.0.0.0/8"}
	})
	_, authURL := front.attempt(c, testReturnURL)
	resp, err := front.client(clientOptions{dial: withProxyHeader(front)}).Get(authURL)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.Body.Close(), qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
	c.Assert(lastClientIP(front), qt.Equals, "203.0.113.7")
	// a plain connection from the front still works
	_, authURL = front.attempt(c, testReturnURL)
	_, frag := fragment(c, front.authenticate(c, authURL, nil))
	c.Assert(frag.Get("code"), qt.Not(qt.Equals), "")

	// from anywhere else the header is not read: it is plain TCP, so the handshake fails and
	// nobody else can choose the logged IP
	other := newTestEnv(c, func(cfg *Config) {
		cfg.ProxyProtocol, cfg.ProxyFrontCIDRs = true, []string{"10.0.0.0/8"}
	})
	_, authURL = other.attempt(c, testReturnURL)
	_, err = other.client(clientOptions{dial: withProxyHeader(other)}).Get(authURL)
	c.Assert(err, qt.Not(qt.IsNil))
	_, authURL = other.attempt(c, testReturnURL)
	_, frag = fragment(c, other.authenticate(c, authURL, nil))
	c.Assert(frag.Get("code"), qt.Not(qt.Equals), "")
	c.Assert(lastClientIP(other), qt.Equals, "127.0.0.1")
}
```
Run: `go test -count=1 -run 'TestCertAuthListener|TestBoundChain|TestCertAuthStartupValidation' ./certauth/`
Expected: `certauth/listener_test.go:16:2: missing go.sum entry for module providing package github.com/pires/go-proxyproto`

Step 2: implement

`GOPRIVATE=github.com/vocdoni/* go get github.com/pires/go-proxyproto@v0.11.0`, then `go mod tidy` once the file below
exists. Expected go.mod diff: `+	github.com/pires/go-proxyproto v0.11.0` in the main require block (do not take a
newer version: v0.16.0 bumps golang.org/x/net).

`certauth/listener.go` (new; origin mode only, Task 7 adds the cloudflare branches):
```go
package certauth

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	proxyproto "github.com/pires/go-proxyproto"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

const (
	// maxStoredChainCerts and maxStoredChainBytes bound the chain an attempt stores (spec 3, step 2).
	maxStoredChainCerts = certcheck.DefaultMaxChainLen
	maxStoredChainBytes = 200 << 10
	// listener socket deadlines: no request body is ever read.
	listenerReadHeaderTimeout = 10 * time.Second
	listenerReadTimeout       = 30 * time.Second
	listenerWriteTimeout      = 30 * time.Second
	// proxyHeaderTimeout bounds the wait for a PROXY protocol header.
	proxyHeaderTimeout = 10 * time.Second
)

// hostKind is what a host name of the auth listener addresses.
type hostKind int

const (
	hostUnknown hostKind = iota
	hostProbe
	hostAttempt
)

// errUnknownServerName refuses a handshake for a name the listener does not serve.
var errUnknownServerName = errors.New("certauth: unknown server name")

// classifyHost tells what a lower case host name (no port) addresses on the auth listener, and
// returns the nonce of an attempt host.
func (s *Service) classifyHost(host string) (hostKind, string) {
	label, rest, found := strings.Cut(host, ".")
	if !found || rest != s.cfg.Host {
		return hostUnknown, ""
	}
	switch {
	case label == probeLabel:
		return hostProbe, ""
	case nonceRe.MatchString(label):
		return hostAttempt, label
	default:
		return hostUnknown, ""
	}
}

// ListenAndServe serves the auth listener on the configured address until it fails.
func (s *Service) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("certauth: listening on %s: %w", s.cfg.ListenAddr, err)
	}
	return s.Serve(ln)
}

// Serve serves the auth listener on a TCP listener: the PROXY protocol when configured, then TLS,
// then HTTP/1.1 only.
func (s *Service) Serve(ln net.Listener) error {
	tlsLn := tls.NewListener(s.wrapListener(ln), s.listenerTLSConfig())
	srv := &http.Server{
		Handler:           s.listenerHandler(),
		ReadHeaderTimeout: listenerReadHeaderTimeout,
		ReadTimeout:       listenerReadTimeout,
		WriteTimeout:      listenerWriteTimeout,
		// handshake failures (scanners, cancelled pickers) are routine on a public port
		ErrorLog: stdlog.New(debugWriter{}, "", 0),
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	return srv.Serve(tlsLn)
}

// debugWriter sends the http.Server's own error lines to the debug log.
type debugWriter struct{}

func (debugWriter) Write(p []byte) (int, error) {
	log.Debugw("certificate auth listener", "event", string(bytes.TrimSpace(p)))
	return len(p), nil
}

// wrapListener applies the front of the mode: in origin mode the PROXY protocol, honoured only from
// the front CIDRs (any other peer is plain TCP).
func (s *Service) wrapListener(ln net.Listener) net.Listener {
	switch {
	case s.cfg.Mode == ModeOrigin && s.cfg.ProxyProtocol:
		return &proxyproto.Listener{Listener: ln, ConnPolicy: s.proxyPolicy, ReadHeaderTimeout: proxyHeaderTimeout}
	default:
		return ln
	}
}

// proxyPolicy reads a PROXY header only from a front CIDR: from anyone else the connection is plain
// TCP, so nobody else can choose the client IP the logs record.
func (s *Service) proxyPolicy(opts proxyproto.ConnPolicyOptions) (proxyproto.Policy, error) {
	if ipIn(opts.Upstream, s.v.proxyNets) {
		return proxyproto.USE, nil
	}
	return proxyproto.SKIP, nil
}

// ipIn reports whether a TCP address is inside one of the networks.
func ipIn(addr net.Addr, nets []*net.IPNet) bool {
	tcp, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	for _, n := range nets {
		if n.Contains(tcp.IP) {
			return true
		}
	}
	return false
}

// listenerTLSConfig is the TLS configuration of the mode.
func (s *Service) listenerTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{alpnHTTP1},
		GetCertificate:     func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.v.serverCert, nil },
		GetConfigForClient: s.configForClient,
	}
}

// configForClient selects the origin-mode configuration by SNI: the probe host never requests a
// client certificate (a background probe must not open the picker), an attempt host gets
// cert-check's configuration, and any other name fails the handshake.
func (s *Service) configForClient(hello *tls.ClientHelloInfo) (*tls.Config, error) {
	switch kind, _ := s.classifyHost(strings.ToLower(hello.ServerName)); kind {
	case hostProbe:
		return s.probeTLS, nil
	case hostAttempt:
		return s.authTLS, nil
	default:
		return nil, errUnknownServerName
	}
}

// listenerHandler routes by Host: the probe, an attempt, or 421 for anything else.
func (s *Service) listenerHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch kind, nonce := s.classifyHost(host); kind {
		case hostProbe:
			s.serveProbe(w, r)
		case hostAttempt:
			s.serveAuthenticate(w, r, nonce)
		default:
			http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		}
	})
}

// serveProbe answers GET /ok on the probe host (contract section 7).
func (s *Service) serveProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.Path != probePath {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Add("Vary", "Origin")
	if origin := r.Header.Get("Origin"); s.v.origins[origin] {
		h.Set("Access-Control-Allow-Origin", origin)
	}
	h.Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// serveAuthenticate is GET /v1/authenticate?state= on a nonce host (contract section 4): it stores
// the presented chain (or none) in the attempt and sends the browser straight back with a code. It
// never verifies anything and never answers anything but a 303 to an allowlisted URL.
func (s *Service) serveAuthenticate(w http.ResponseWriter, r *http.Request, nonce string) {
	if r.Method != http.MethodGet || r.URL.Path != authenticatePath {
		http.NotFound(w, r)
		return
	}
	if s.cfg.Mode == ModeOrigin && certcheck.CheckHost(r) != nil {
		http.Error(w, http.StatusText(http.StatusMisdirectedRequest), http.StatusMisdirectedRequest)
		return
	}
	state := r.URL.Query().Get("state")
	raw, presented := s.presentedChain(r)
	chain := boundChain(raw)
	if !tokenRe.MatchString(state) {
		s.redirect(w, s.cfg.DefaultReturn, url.Values{"error": {"attempt_expired"}})
		return
	}
	now := s.now()
	code := randomToken()
	tx, err := s.store.ConsumeCertAuthState(db.CertAuthStateConsume{
		State: state, Nonce: nonce, Code: code, Chain: chain, ChainPresented: presented,
		CreatedAfter: now.Add(-stateTTL), Now: now, ExpiresAt: now.Add(codeTTL),
	})
	if err != nil {
		s.redirect(w, s.expiredReturn(state, nonce, err), url.Values{"error": {"attempt_expired"}})
		return
	}
	s.log.Infow("certificate attempt authenticated", "transaction", tx.ID.Hex(), "processId", tx.ProcessID.Hex(),
		"certificatePresented", presented, "chainLength", len(chain), "clientIP", s.listenerClientIP(r))
	s.redirect(w, tx.ReturnURL, url.Values{"code": {code}, "state": {state}})
}

// expiredReturn is where an unusable state is sent back to: its own attempt's return URL when the
// attempt is still readable on this host, the default return otherwise.
func (s *Service) expiredReturn(state, nonce string, consumeErr error) string {
	if !errors.Is(consumeErr, db.ErrNotFound) {
		s.log.Warnw("could not consume a certificate attempt", "error", consumeErr)
		return s.cfg.DefaultReturn
	}
	tx, err := s.store.CertAuthTransactionByState(state, nonce)
	if err != nil {
		return s.cfg.DefaultReturn
	}
	return tx.ReturnURL
}

// redirect answers a bodiless 303 to target with the fragment, never cached, never leaking a
// referrer, and closing the connection so the next attempt opens a fresh handshake.
func (*Service) redirect(w http.ResponseWriter, target string, fragment url.Values) {
	h := w.Header()
	// the fragment values are base64url or fixed reason strings: Encode leaves them as they are
	h.Set("Location", target+"#"+fragment.Encode())
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Connection", "close")
	w.WriteHeader(http.StatusSeeOther)
}

// presentedChain returns the DER chain the browser presented, leaf first, and whether it presented
// one at all: the TLS peer certificates.
func (*Service) presentedChain(r *http.Request) ([][]byte, bool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return nil, false
	}
	chain := make([][]byte, 0, len(r.TLS.PeerCertificates))
	for _, c := range r.TLS.PeerCertificates {
		chain = append(chain, c.Raw)
	}
	return chain, true
}

// boundChain bounds what an attempt stores: a chain over maxStoredChainCerts certificates or
// maxStoredChainBytes keeps its leaf alone when the leaf is within cert-check's size bound (Verify
// judges the leaf against its pinned CAs), and nothing when it is not (redeem answers invalid_chain).
func boundChain(chain [][]byte) [][]byte {
	if len(chain) == 0 {
		return nil
	}
	total := 0
	for _, c := range chain {
		total += len(c)
	}
	if len(chain) <= maxStoredChainCerts && total <= maxStoredChainBytes {
		return chain
	}
	if len(chain[0]) <= certcheck.MaxCertificateDER {
		return chain[:1]
	}
	return nil
}

// listenerClientIP is the client IP the logs record: the TCP peer, the PROXY header's source when a
// front sent one.
func (*Service) listenerClientIP(r *http.Request) string {
	return clientIP(r.RemoteAddr)
}

// publicHost returns host with the public port appended when it is not 443.
func (s *Service) publicHost(host string) string {
	if s.cfg.PublicPort == DefaultPublicPort {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(s.cfg.PublicPort))
}

// clientIP returns the IP of the peer of a TCP address, or the address itself.
func clientIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
```
Run: `go mod tidy && go test -count=1 -race -run 'TestCertAuthListener|TestBoundChain|TestCertAuthStartupValidation' ./certauth/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (4 tests: TestCertAuthListener, TestBoundChain,
TestCertAuthStartupValidation, TestCertAuthStartupValidationProxyProtocol)

Step 3: commit
`git commit -m "feat(certauth): serve the origin-mode auth listener" -- certauth/listener.go certauth/env_test.go certauth/listener_test.go go.mod go.sum`

Interfaces this task provides to later tasks: `(*Service).ListenAndServe() error`, `Serve(ln net.Listener) error`;
unexported `classifyHost`, `wrapListener`, `listenerTLSConfig`, `presentedChain`, `listenerClientIP`, `boundChain`,
`ipIn`, `clientIP(addr)`, `publicHost(host)`; test helpers `testEnv`, `newTestEnv`, `attempt`, `authURL`,
`portString`, `client`, `clientOptions`, `authenticate`, `fragment`, `issue`.

### Task 7: Cloudflare mode
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/cloudflare.go`, `certauth/listener.go`,
`certauth/cloudflare_test.go`
Goal: Cloudflare-terminated mTLS: only a peer with the Authenticated Origin Pulls certificate (and, when set, an
allowlisted IP) connects; the RFC 9440 and legacy headers rebuild the chain origin mode stores; the client IP comes
from `CF-Connecting-IP` (serves AC14). Stays out of production (startup warning from Task 5).

Step 1: write the failing test
```go
package certauth

import (
	"encoding/base64"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
)

// TestCertAuthCloudflareMode pins cloudflare mode (AC14): only a peer presenting the Authenticated
// Origin Pulls certificate completes a handshake, only allowlisted peers connect when the list is
// set, and the forwarded RFC 9440 and legacy headers rebuild the chain the origin mode stores.
func TestCertAuthCloudflareMode(t *testing.T) {
	c := qt.New(t)
	aop, err := testprovider.NewPKI() // stands for Cloudflare's origin pull CA
	c.Assert(err, qt.IsNil)
	aopCert, err := aop.Issue(testprovider.Holder{Document: "00000000T"})
	c.Assert(err, qt.IsNil)
	cloudflare := func(cidrs ...string) func(*Config) {
		return func(cfg *Config) {
			cfg.Mode, cfg.CFOriginPullCAPEM, cfg.CFAllowedCIDRs = ModeCloudflare, aop.CertPEM(), cidrs
		}
	}
	e := newTestEnv(c, cloudflare())
	leaf := e.issue(c, testprovider.Holder{GivenName: "Maria", Surname: "García", Document: "12345678Z"})
	intermediate := e.pki.CA.Raw
	b64 := base64.StdEncoding.EncodeToString

	forward := func(env *testEnv, headers map[string]string) (*http.Response, string, error) {
		state, authURL := env.attempt(c, testReturnURL)
		req, err := http.NewRequest(http.MethodGet, authURL, nil)
		c.Assert(err, qt.IsNil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := env.client(clientOptions{cert: aopCert}).Do(req)
		if err == nil {
			c.Assert(resp.Body.Close(), qt.IsNil)
		}
		return resp, state, err
	}

	for name, tc := range map[string]struct {
		headers   map[string]string
		chain     [][]byte
		presented bool
	}{
		"rfc 9440": {
			headers: map[string]string{
				"Client-Cert":       ":" + b64(leaf.Certificate[0]) + ":",
				"Client-Cert-Chain": ":" + b64(intermediate) + ":, :" + b64(intermediate) + ":",
			},
			chain: [][]byte{leaf.Certificate[0], intermediate, intermediate}, presented: true,
		},
		"rfc 9440 leaf only": {
			headers: map[string]string{"Client-Cert": ":" + b64(leaf.Certificate[0]) + ":"},
			chain:   [][]byte{leaf.Certificate[0]}, presented: true,
		},
		"legacy": {
			headers: map[string]string{"Cf-Client-Cert-Der-Base64": b64(leaf.Certificate[0])},
			chain:   [][]byte{leaf.Certificate[0]}, presented: true,
		},
		"no certificate":     {headers: map[string]string{}},
		"undecodable":        {headers: map[string]string{"Client-Cert": "not a byte sequence"}, presented: true},
		"chain without leaf": {headers: map[string]string{"Client-Cert-Chain": ":" + b64(intermediate) + ":"}},
	} {
		resp, state, err := forward(e, tc.headers)
		c.Assert(err, qt.IsNil, qt.Commentf("%s", name))
		c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther, qt.Commentf("%s", name))
		tx := e.store.transaction(state)
		c.Assert(tx.ChainPresented, qt.Equals, tc.presented, qt.Commentf("%s", name))
		c.Assert(tx.Chain, qt.HasLen, len(tc.chain), qt.Commentf("%s", name))
		for i := range tc.chain {
			c.Assert(tx.Chain[i], qt.DeepEquals, tc.chain[i], qt.Commentf("%s: certificate %d", name, i))
		}
	}

	// the leaf the headers carry is stored exactly as origin mode stores the TLS peer certificate
	origin := newTestEnv(c, nil)
	state, authURL := origin.attempt(c, testReturnURL)
	origin.authenticate(c, authURL, leaf)
	c.Assert(origin.store.transaction(state).Chain, qt.DeepEquals, [][]byte{leaf.Certificate[0]})

	// without the origin pull certificate the handshake fails, so a forged header never arrives
	_, authURL = e.attempt(c, testReturnURL)
	req, err := http.NewRequest(http.MethodGet, authURL, nil)
	c.Assert(err, qt.IsNil)
	req.Header.Set("Client-Cert", ":"+b64(leaf.Certificate[0])+":")
	_, err = e.client(clientOptions{cert: leaf}).Do(req)
	c.Assert(err, qt.Not(qt.IsNil))
	_, err = e.client(clientOptions{}).Do(req)
	c.Assert(err, qt.Not(qt.IsNil))

	// with a peer allowlist, connections from elsewhere are dropped at accept
	_, _, err = forward(newTestEnv(c, cloudflare("10.0.0.0/8")), map[string]string{})
	c.Assert(err, qt.Not(qt.IsNil))
	resp, _, err := forward(newTestEnv(c, cloudflare("127.0.0.0/8")), map[string]string{})
	c.Assert(err, qt.IsNil)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusSeeOther)
}
```
Run: `go test -count=1 -run TestCertAuthCloudflareMode ./certauth/`
Expected: `--- FAIL: TestCertAuthCloudflareMode` (the origin-mode listener ignores the headers and needs no AOP certificate)

Step 2: implement

`certauth/cloudflare.go` (new):
```go
package certauth

import (
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
)

// Headers Cloudflare forwards the browser's client certificate in (spec section 3b).
const (
	// headerClientCert is the RFC 9440 leaf, a structured-field byte sequence (":<base64>:").
	headerClientCert = "Client-Cert"
	// headerClientCertChain is the RFC 9440 chain without the leaf, a list of byte sequences.
	headerClientCertChain = "Client-Cert-Chain"
	// headerCFClientCertDER is Cloudflare's legacy leaf header, plain base64 DER.
	headerCFClientCertDER = "Cf-Client-Cert-Der-Base64"
)

// cloudflareTLSConfig is the cloudflare-mode TLS configuration: only a peer presenting the zone's
// Authenticated Origin Pulls certificate completes a handshake. With the optional peer allowlist,
// that is what makes the forwarded certificate headers trustworthy.
func (s *Service) cloudflareTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{alpnHTTP1},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.v.serverCert, nil },
		ClientCAs:      s.v.cfCAs,
		ClientAuth:     tls.RequireAndVerifyClientCert,
	}
}

// cloudflareChain rebuilds the presented chain from the forwarded headers: the RFC 9440 leaf, or
// the legacy one, then the RFC 9440 chain. No leaf header means no certificate; a leaf header that
// does not decode is a certificate that was presented but cannot be kept (redeem: invalid_chain).
func cloudflareChain(h http.Header) ([][]byte, bool) {
	leafHeader, legacy := strings.TrimSpace(h.Get(headerClientCert)), false
	if leafHeader == "" {
		leafHeader, legacy = strings.TrimSpace(h.Get(headerCFClientCertDER)), true
	}
	if leafHeader == "" {
		return nil, false
	}
	var leaf []byte
	if legacy {
		leaf, _ = base64.StdEncoding.DecodeString(leafHeader)
	} else {
		leaf = sfByteSequence(leafHeader)
	}
	if len(leaf) == 0 {
		return nil, true
	}
	chain := [][]byte{leaf}
	for _, value := range h.Values(headerClientCertChain) {
		for item := range strings.SplitSeq(value, ",") {
			if der := sfByteSequence(strings.TrimSpace(item)); len(der) > 0 {
				chain = append(chain, der)
			}
		}
	}
	return chain, true
}

// sfByteSequence decodes an RFC 8941 byte sequence (":<base64>:"), or returns nil.
func sfByteSequence(item string) []byte {
	inner, ok := strings.CutPrefix(item, ":")
	if !ok {
		return nil
	}
	inner, ok = strings.CutSuffix(inner, ":")
	if !ok {
		return nil
	}
	der, err := base64.StdEncoding.DecodeString(inner)
	if err != nil {
		return nil
	}
	return der
}

// peerFilter drops, at accept time, every connection from a peer outside the allowed networks.
type peerFilter struct {
	net.Listener
	allowed []*net.IPNet
}

// Accept implements net.Listener.
func (f *peerFilter) Accept() (net.Conn, error) {
	for {
		conn, err := f.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if ipIn(conn.RemoteAddr(), f.allowed) {
			return conn, nil
		}
		_ = conn.Close()
	}
}
```
`certauth/listener.go`:
```diff
--- a/certauth/listener.go
+++ b/certauth/listener.go
@@ -69,8 +69,8 @@
 	return s.Serve(ln)
 }
 
-// Serve serves the auth listener on a TCP listener: the PROXY protocol when configured, then TLS,
-// then HTTP/1.1 only.
+// Serve serves the auth listener on a TCP listener: the PROXY protocol (origin mode) or the peer
+// filter (cloudflare mode) first, then TLS, then HTTP/1.1 only.
 func (s *Service) Serve(ln net.Listener) error {
 	tlsLn := tls.NewListener(s.wrapListener(ln), s.listenerTLSConfig())
 	srv := &http.Server{
@@ -95,11 +95,13 @@
 }
 
 // wrapListener applies the front of the mode: in origin mode the PROXY protocol, honoured only from
-// the front CIDRs (any other peer is plain TCP).
+// the front CIDRs (any other peer is plain TCP); in cloudflare mode the peer allowlist.
 func (s *Service) wrapListener(ln net.Listener) net.Listener {
 	switch {
 	case s.cfg.Mode == ModeOrigin && s.cfg.ProxyProtocol:
 		return &proxyproto.Listener{Listener: ln, ConnPolicy: s.proxyPolicy, ReadHeaderTimeout: proxyHeaderTimeout}
+	case s.cfg.Mode == ModeCloudflare && len(s.v.cfNets) > 0:
+		return &peerFilter{Listener: ln, allowed: s.v.cfNets}
 	default:
 		return ln
 	}
@@ -130,6 +132,9 @@
 
 // listenerTLSConfig is the TLS configuration of the mode.
 func (s *Service) listenerTLSConfig() *tls.Config {
+	if s.cfg.Mode == ModeCloudflare {
+		return s.cloudflareTLSConfig()
+	}
 	return &tls.Config{
 		MinVersion:         tls.VersionTLS12,
 		NextProtos:         []string{alpnHTTP1},
@@ -247,8 +252,11 @@
 }
 
 // presentedChain returns the DER chain the browser presented, leaf first, and whether it presented
-// one at all: the TLS peer certificates.
-func (*Service) presentedChain(r *http.Request) ([][]byte, bool) {
+// one at all: the TLS peer certificates in origin mode, the forwarded headers in cloudflare mode.
+func (s *Service) presentedChain(r *http.Request) ([][]byte, bool) {
+	if s.cfg.Mode == ModeCloudflare {
+		return cloudflareChain(r.Header)
+	}
 	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
 		return nil, false
 	}
@@ -279,9 +287,14 @@
 	return nil
 }
 
-// listenerClientIP is the client IP the logs record: the TCP peer, the PROXY header's source when a
-// front sent one.
-func (*Service) listenerClientIP(r *http.Request) string {
+// listenerClientIP is the client IP the logs record: the TCP peer (the PROXY header's source when a
+// front sent one), or Cloudflare's CF-Connecting-IP in cloudflare mode, where the peer is Cloudflare.
+func (s *Service) listenerClientIP(r *http.Request) string {
+	if s.cfg.Mode == ModeCloudflare {
+		if ip := net.ParseIP(r.Header.Get("CF-Connecting-IP")); ip != nil {
+			return ip.String()
+		}
+	}
 	return clientIP(r.RemoteAddr)
 }
 
```
Run: `go test -count=1 -race -run 'TestCertAuthCloudflareMode|TestCertAuthListener' ./certauth/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (2 tests)

Step 3: commit
`git commit -m "feat(certauth): add the cloudflare mode of the auth listener" -- certauth/cloudflare.go certauth/listener.go certauth/cloudflare_test.go`

Interfaces this task provides to later tasks: none beyond the mode itself (`cloudflareTLSConfig`, `cloudflareChain`,
`peerFilter` are internal).

### Task 8: Error mapping, holder display, CSP window exports
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/errors.go`, `certauth/subject.go`,
`certauth/errors_test.go`, `csp/handlers/certauth.go`, `csp/handlers/processes.go`
Goal: the single reason table of contract section 8 (cert-check codes verbatim, backend reasons, statuses,
`retryable`), issuer attribution, the display-only holder parsed from a failed leaf, and the exports certauth and
`auth/0` need from `csp/handlers` (serves AC10, AC5 `certificate_census`).

Step 1: write the failing test
```go
package certauth

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/handlers"
)

// errorBody is a decoded error envelope.
type errorBody struct {
	Error string                      `json:"error"`
	Code  int                         `json:"code"`
	Data  apicommon.CertAuthErrorData `json:"data"`
}

// writtenEnvelope writes an error and decodes its envelope, returning the raw body too.
func writtenEnvelope(c *qt.C, write func(http.ResponseWriter)) (int, string, errorBody) {
	rec := httptest.NewRecorder()
	write(rec)
	var parsed errorBody
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &parsed), qt.IsNil)
	return rec.Code, rec.Body.String(), parsed
}

// TestCertAuthErrorMapping pins the error envelope (AC10): every reason answers code 40950 with its
// HTTP status and the verbatim reason; retryable only for revocation_unavailable and internal; the
// conditional fields where the contract puts them; and never the cert-check Detail, a serialized
// Diagnostics or certificate material, in the body or in the logs.
func TestCertAuthErrorMapping(t *testing.T) {
	c := qt.New(t)

	// every cert-check code has a mapping, so none falls through to internal by accident
	for _, code := range certcheck.Codes {
		_, ok := reasons[string(code)]
		c.Assert(ok, qt.IsTrue, qt.Commentf("%s", code))
	}

	s, _, pki := newTestService(c, 443, nil)
	logs, ok := s.log.(*captureLog)
	c.Assert(ok, qt.IsTrue)
	verify := func(h testprovider.Holder) (*certcheck.Error, *x509.Certificate) {
		cert, err := pki.Issue(h)
		c.Assert(err, qt.IsNil)
		_, err = s.handler.Verify(t.Context(), []*x509.Certificate{cert.Leaf})
		ce, ok := err.(*certcheck.Error)
		c.Assert(ok, qt.IsTrue, qt.Commentf("err: %v", err))
		return ce, cert.Leaf
	}
	holder := testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"}
	expiredHolder := holder
	expiredHolder.NotAfter = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	revokedHolder := holder
	revokedHolder.OU = testprovider.OURevoked
	outageHolder := holder
	outageHolder.OU = testprovider.OUOutage

	for _, tc := range []struct {
		name      string
		holder    testprovider.Holder
		reason    string
		status    int
		withHold  bool
		notAfter  string
		retryable bool
	}{
		{"expired", expiredHolder, "expired", 422, true, "2026-10-03T00:00:00Z", false},
		{"revoked", revokedHolder, "revoked", 422, true, "", false},
		{"outage", outageHolder, "revocation_unavailable", 503, false, "", true},
	} {
		ce, leaf := verify(tc.holder)
		apiErr := s.verifyFailure(ce, leaf)
		status, raw, body := writtenEnvelope(c, apiErr.Write)
		comment := qt.Commentf("%s: %s", tc.name, raw)
		c.Assert(status, qt.Equals, tc.status, comment)
		c.Assert(body.Code, qt.Equals, 40950, comment)
		c.Assert(body.Data.Reason, qt.Equals, tc.reason, comment)
		c.Assert(body.Data.Retryable, qt.Equals, tc.retryable, comment)
		c.Assert(body.Data.Reference, qt.Equals, ce.Diagnostics.DiagnosticID, comment)
		c.Assert(body.Data.Issuer, qt.DeepEquals, &apicommon.CertAuthIssuer{ID: "test", Name: "Synthetic Test CA"}, comment)
		c.Assert(body.Data.NotAfter, qt.Equals, tc.notAfter, comment)
		if tc.withHold {
			c.Assert(body.Data.Holder, qt.DeepEquals, &apicommon.CertAuthHolder{
				FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
			}, comment)
		} else {
			c.Assert(body.Data.Holder, qt.IsNil, comment)
		}
		assertNoCertificateMaterial(c, raw, ce, leaf)
	}

	// unsupported_profile, as cert-check answers it for a representative certificate
	upErr := &certcheck.Error{
		Code: certcheck.CodeUnsupportedProfile, Detail: "representative OID 2.16.724.1.3.5.7",
		Diagnostics: certcheck.Diagnostics{DiagnosticID: certcheck.NewDiagnosticID(), TechnicalDetail: "profile"},
	}
	status, raw, body := writtenEnvelope(c, s.verifyFailure(upErr, nil).Write)
	c.Assert(status, qt.Equals, 422)
	c.Assert(body.Data.Reason, qt.Equals, "unsupported_profile")
	c.Assert(body.Data.Issuer, qt.IsNil)
	c.Assert(raw, qt.Not(qt.Contains), "representative OID")

	// a backend failure is internal, retryable, with a fresh reference the log line carries too
	internalErr := s.internalFailure("testing", http.ErrHandlerTimeout, "transaction", "tx")
	status, _, body = writtenEnvelope(c, internalErr.Write)
	c.Assert(status, qt.Equals, 500)
	c.Assert(body.Data.Reason, qt.Equals, "internal")
	c.Assert(body.Data.Retryable, qt.IsTrue)
	c.Assert(body.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
	logged := logs.all()
	last := logged[len(logged)-1]
	c.Assert(last.kv["reference"], qt.Equals, body.Data.Reference)

	// the auth/0 refusal of a certificate census shares the envelope
	status, _, body = writtenEnvelope(c, handlers.CertificateCensusError().Write)
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	c.Assert(body.Code, qt.Equals, 40950)
	c.Assert(body.Data, qt.DeepEquals, apicommon.CertAuthErrorData{Reason: "certificate_census"})
	want := certError(apicommon.CertAuthReasonCertificateCensus, apicommon.CertAuthErrorData{})
	c.Assert(handlers.CertificateCensusError().Error(), qt.Equals, want.Error())
}

// attrKeys lists the keys cert-check's LogAttrs emits for d.
func attrKeys(d certcheck.Diagnostics) []string {
	var keys []string
	for _, a := range d.LogAttrs() {
		keys = append(keys, a.Key)
	}
	return keys
}

// stringify renders a log value for the secret scan.
func stringify(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// assertNoCertificateMaterial checks a body carries nothing of the Diagnostics or the certificate.
func assertNoCertificateMaterial(c *qt.C, raw string, ce *certcheck.Error, leaf *x509.Certificate) {
	c.Helper()
	for _, banned := range []string{
		ce.Detail, "technicalDetail", "diagnostics", "IDCES", leaf.Subject.String(),
		base64.StdEncoding.EncodeToString(leaf.Raw)[:40],
	} {
		if banned != "" {
			c.Assert(raw, qt.Not(qt.Contains), banned)
		}
	}
}
```
Run: `go test -count=1 -run TestCertAuthErrorMapping ./certauth/`
Expected: `certauth/errors_test.go:44:12: undefined: reasons`

Step 2: implement

`certauth/errors.go` (new):
```go
package certauth

import (
	"crypto/x509"
	"net/http"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	apierrors "github.com/vocdoni/saas-backend/errors"
)

// unprocessable is the status of every non-retryable certificate rejection.
const unprocessable = http.StatusUnprocessableEntity

// reasonSpec is the HTTP status and client message of one data.reason.
type reasonSpec struct {
	status  int
	message string
}

// reasons maps every data.reason to its status and message (contract section 8): cert-check's codes,
// verbatim, then the backend's own. The messages are client-safe; clients key on the reason.
var reasons = map[string]reasonSpec{
	string(certcheck.CodeNoCertificate):               {unprocessable, "no certificate was presented"},
	string(certcheck.CodeProviderMismatch):            {unprocessable, "the certificate is not of the expected type"},
	string(certcheck.CodeUntrustedIssuer):             {unprocessable, "the certificate issuer is not accepted"},
	string(certcheck.CodeInvalidChain):                {unprocessable, "the certificate chain is not valid"},
	string(certcheck.CodeExpired):                     {unprocessable, "the certificate has expired"},
	string(certcheck.CodeNotYetValid):                 {unprocessable, "the certificate is not valid yet"},
	string(certcheck.CodeUnsupportedProfile):          {unprocessable, "this kind of certificate is not accepted"},
	string(certcheck.CodeUnsupportedPurpose):          {unprocessable, "the certificate cannot be used to sign in"},
	string(certcheck.CodeWeakAlgorithm):               {unprocessable, "the certificate uses a weak algorithm"},
	string(certcheck.CodeTestCertificate):             {unprocessable, "test certificates are not accepted"},
	string(certcheck.CodeIdentityInvalid):             {unprocessable, "the certificate identity is not valid"},
	string(certcheck.CodeIdentityUnsupportedDocument): {unprocessable, "the certificate document is not a DNI or NIE"},
	string(certcheck.CodeRevoked):                     {unprocessable, "the certificate has been revoked"},
	string(certcheck.CodeRevocationUnknown):           {unprocessable, "the certificate revocation status is unknown"},
	string(certcheck.CodeRevocationUnavailable):       {http.StatusServiceUnavailable, "revocation status is unavailable"},
	string(certcheck.CodeInternal):                    {http.StatusInternalServerError, "the certificate could not be checked"},
	apicommon.CertAuthReasonDNINotInCensus:            {http.StatusForbidden, "certificate is not in the census"},
	apicommon.CertAuthReasonAttemptExpired:            {http.StatusGone, "the sign-in attempt has expired"},
	apicommon.CertAuthReasonCodeUsed:                  {http.StatusConflict, "the sign-in code was already used"},
	apicommon.CertAuthReasonVerifierMismatch:          {http.StatusForbidden, "the sign-in verifier does not match"},
	apicommon.CertAuthReasonTooManyAttempts:           {http.StatusTooManyRequests, "too many sign-in attempts"},
	apicommon.CertAuthReasonCertificateCensus:         {http.StatusBadRequest, "this census signs in with a digital certificate"},
}

// issuerNames are the contract's display names of the providers (section 8); any other provider
// shows its own Info().Name.
var issuerNames = map[string]string{"fnmt": "FNMT", "idcat": "idCAT"}

// certError is the envelope of reason with data: code 40950, the reason's status and message, and
// retryable true only for revocation_unavailable and internal. An unknown reason is internal.
func certError(reason string, data apicommon.CertAuthErrorData) apierrors.Error {
	spec, ok := reasons[reason]
	if !ok {
		reason = apicommon.CertAuthReasonInternal
		spec = reasons[reason]
	}
	data.Reason = reason
	data.Retryable = certcheck.ErrorCode(reason).Retryable()
	return apierrors.CertificateAuthError(spec.status, spec.message, data)
}

// verifyFailure is the envelope of a cert-check failure of leaf: the verbatim code, the diagnostic
// reference, the issuer when one provider is attributable, and on expiry and revocation the holder
// read from the leaf subject and, for the validity failures, the certificate's notAfter. Never the
// Detail, the Diagnostics or the certificate itself.
func (s *Service) verifyFailure(e *certcheck.Error, leaf *x509.Certificate) apierrors.Error {
	data := apicommon.CertAuthErrorData{Reference: e.Diagnostics.DiagnosticID}
	if data.Reference == "" {
		data.Reference = certcheck.NewDiagnosticID()
	}
	if p := s.providerOf(leaf); p != nil {
		info := p.Info()
		name, ok := issuerNames[info.ID]
		if !ok {
			name = info.Name
		}
		data.Issuer = &apicommon.CertAuthIssuer{ID: info.ID, Name: name}
	}
	switch e.Code {
	case certcheck.CodeExpired, certcheck.CodeNotYetValid:
		if d := e.Diagnostics.Certificate; d != nil {
			data.NotAfter = d.NotAfter.UTC().Format(time.RFC3339)
		}
		data.Holder = subjectHolder(leaf)
	case certcheck.CodeRevoked:
		data.Holder = subjectHolder(leaf)
	default:
	}
	return certError(string(e.Code), data)
}

// internalFailure is the envelope of a backend failure: a fresh reference the log line carries too,
// so support can find it.
func (s *Service) internalFailure(what string, err error, kv ...any) apierrors.Error {
	ref := certcheck.NewDiagnosticID()
	s.log.Warnw("certificate sign-in failed: "+what, append([]any{"reference", ref, "error", err}, kv...)...)
	return certError(apicommon.CertAuthReasonInternal, apicommon.CertAuthErrorData{Reference: ref})
}

// retryAfter is the whole seconds until resetAt, at least one.
func retryAfter(now, resetAt time.Time) int64 {
	secs := int64(resetAt.Sub(now).Round(time.Second) / time.Second)
	return max(secs, 1)
}

// providerOf returns the provider advertising the leaf's issuer, attributing a failure to it, or
// nil when none (or more than one) does.
func (s *Service) providerOf(leaf *x509.Certificate) certcheck.Provider {
	if leaf == nil {
		return nil
	}
	var found certcheck.Provider
	for _, p := range s.handler.Providers() {
		for _, ca := range p.CAs() {
			if string(ca.RawSubject) == string(leaf.RawIssuer) {
				if found != nil && found.Info().ID != p.Info().ID {
					return nil
				}
				found = p
			}
		}
	}
	return found
}
```
`certauth/subject.go` (new):
```go
package certauth

import (
	"crypto/x509"
	"encoding/asn1"
	"strings"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	oidGivenName = asn1.ObjectIdentifier{2, 5, 4, 42}
	oidSurname   = asn1.ObjectIdentifier{2, 5, 4, 4}
)

// subjectDocumentPrefixes are the ETSI semantic identifiers a Spanish personal certificate writes
// its document with (subject serialNumber).
var subjectDocumentPrefixes = []string{"IDCES-", "PNOES-"}

// subjectHolder reads a display holder from a leaf subject, for the failures that show one
// (expired, not_yet_valid, revoked), or nil when it does not parse. The subject is unverified
// bytes: this is display only, never an authorization or budget input.
func subjectHolder(leaf *x509.Certificate) *apicommon.CertAuthHolder {
	if leaf == nil {
		return nil
	}
	serial := leaf.Subject.SerialNumber
	for _, prefix := range subjectDocumentPrefixes {
		serial = strings.TrimPrefix(serial, prefix)
	}
	n := internal.NormalizeNationalID(serial)
	if n.Canonical == "" {
		return nil
	}
	name := strings.TrimSpace(subjectAttribute(leaf, oidGivenName) + " " + subjectAttribute(leaf, oidSurname))
	if name == "" {
		name = strings.TrimSpace(leaf.Subject.CommonName)
	}
	if name == "" {
		return nil
	}
	return &apicommon.CertAuthHolder{
		FullName: name, DocumentType: string(n.Type), DocumentEnding: n.Canonical[len(n.Canonical)-4:],
	}
}

// resultHolder is the holder of a verified identity.
func resultHolder(id certcheck.Identity) apicommon.CertAuthHolder {
	number := id.Document.Number
	ending := number
	if len(number) > 4 {
		ending = number[len(number)-4:]
	}
	return apicommon.CertAuthHolder{FullName: id.FullName, DocumentType: string(id.Document.Type), DocumentEnding: ending}
}

// subjectAttribute returns the first subject attribute of type oid, or "".
func subjectAttribute(c *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, atv := range c.Subject.Names {
		if atv.Type.Equal(oid) {
			if s, ok := atv.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
```
`csp/handlers/certauth.go` (new):
```go
package handlers

import (
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// ProcessPastEnd reports whether the process is past its end (plus endGrace), when it has one: the
// window the signing endpoints enforce, exported for the certificate sign-in.
func ProcessPastEnd(vp *db.VotingProcess) bool { return processEnded(vp) }

// ProcessBeforeStart reports whether the process start is still more than startGrace ahead. A zero
// StartDate counts as started, as unvotableElection has it.
func ProcessBeforeStart(vp *db.VotingProcess) bool {
	return !vp.StartDate.IsZero() && time.Now().Add(startGrace).Before(vp.StartDate)
}

// CertificateCensusError is the auth/0 answer for a certificate census (contract section 2): its
// voters sign in only through the certificate endpoints, whose tokens are verified already.
func CertificateCensusError() errors.Error {
	return errors.CertificateAuthError(http.StatusBadRequest, "this census signs in with a digital certificate",
		apicommon.CertAuthErrorData{Reason: apicommon.CertAuthReasonCertificateCensus})
}
```
`csp/handlers/processes.go` (`unvotableElection` uses the shared start check; behaviour unchanged):
```diff
diff --git a/csp/handlers/processes.go b/csp/handlers/processes.go
index 1365d32..8dad2c0 100644
--- a/csp/handlers/processes.go
+++ b/csp/handlers/processes.go
@@ -88,7 +88,7 @@ func unvotableElection(vp *db.VotingProcess, q *db.VotingProcessQuestion) *error
 		return errors.Ptr(errors.ErrUnauthorized.Withf("question is not open for voting"))
 	}
 	// publish always persists a StartDate, while EndDate is optional.
-	if !vp.StartDate.IsZero() && time.Now().Add(startGrace).Before(vp.StartDate) {
+	if ProcessBeforeStart(vp) {
 		return errors.Ptr(errors.ErrUnauthorized.Withf("process has not started yet"))
 	}
 	if processEnded(vp) {
```
Run: `go test -count=1 -run TestCertAuthErrorMapping ./certauth/ && go vet ./csp/...`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (TestCertAuthErrorMapping)

Step 3: commit
`git commit -m "feat(certauth): map certificate failures to the error envelope" -- certauth/errors.go certauth/subject.go certauth/errors_test.go csp/handlers/certauth.go csp/handlers/processes.go`

Interfaces this task provides to later tasks: `handlers.ProcessPastEnd(vp) bool`, `handlers.ProcessBeforeStart(vp)
bool`, `handlers.CertificateCensusError() errors.Error`; unexported `certError(reason, data)`,
`(*Service).verifyFailure(e, leaf)`, `internalFailure(what, err, kv...)`, `providerOf(leaf)`, `retryAfter(now,
resetAt)`, `subjectHolder(leaf)`, `resultHolder(identity)`; test helpers `errorBody`, `writtenEnvelope`, `attrKeys`,
`stringify`.

### Task 9: Create endpoint
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/create.go`, `certauth/create_test.go`
Goal: `POST /processes/{processId}/auth/cert` (contract section 3): body grammar, the returnUrl allowlist, a
published process still taking sign-ins with a certificate census, the per-IP+process limit, a stored attempt with
independent state and nonce, and the `authUrl`/`probeUrl` answer (serves AC6 at unit level).

Step 1: write the failing test
```go
package certauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// pkce returns a fresh verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = randomToken()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// serveRoute sends a JSON body to handler mounted on pattern, as the main router does, from one
// client IP, and decodes the answer into out when it is not nil.
func serveRoute(c *qt.C, pattern string, handler http.HandlerFunc, path string, body, out any) int {
	data, err := json.Marshal(body)
	c.Assert(err, qt.IsNil)
	r := chi.NewRouter()
	r.Post(pattern, handler)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.RemoteAddr = "198.51.100.1:4000"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if out != nil {
		c.Assert(json.Unmarshal(rec.Body.Bytes(), out), qt.IsNil, qt.Commentf("body: %s", rec.Body))
	}
	return rec.Code
}

// post sends a create to the env's process path.
func (e *testEnv) post(c *qt.C, path string, body, out any) int {
	return serveRoute(c, "/processes/{processId}/auth/cert", e.s.Create, path, body, out)
}

// create runs a create for purpose and returns the answer and the verifier.
func (e *testEnv) create(c *qt.C, purpose string) (apicommon.CertAuthCreateResponse, string) {
	verifier, challenge := pkce()
	var resp apicommon.CertAuthCreateResponse
	code := e.post(c, "/processes/"+e.store.vp.ID.Hex()+"/auth/cert", apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testReturnURL, Purpose: purpose,
	}, &resp)
	c.Assert(code, qt.Equals, http.StatusOK)
	return resp, verifier
}

// TestCertAuthCreateValidation pins the create endpoint's checks against the in-memory store: the
// answer's shape, the body grammar, the returnUrl allowlist, the process and census state, and the
// per-IP limit.
func TestCertAuthCreateValidation(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	_, challenge := pkce()
	path := "/processes/" + e.store.vp.ID.Hex() + "/auth/cert"
	granted := 0 // the creates that spent a unit of the per-IP limit
	create := func(req apicommon.CertAuthCreateRequest) (int, errorBody) {
		var body errorBody
		status := e.post(c, path, req, &body)
		if status == http.StatusOK {
			granted++
		}
		return status, body
	}
	valid := apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testReturnURL, Purpose: "check",
	}

	// the answer: a stored attempt on a fresh nonce host, with the probe
	created, _ := e.create(c, "vote")
	granted++
	c.Assert(created.State, qt.Matches, `[A-Za-z0-9_-]{43}`)
	tx := e.store.transaction(created.State)
	c.Assert(tx.Status, qt.Equals, db.CertAuthStatusCreated)
	c.Assert(tx.Purpose, qt.Equals, "vote")
	c.Assert(created.AuthURL, qt.Equals, e.authURL(tx.Nonce, created.State))
	c.Assert(created.ProbeURL, qt.Equals, "https://probe."+testHost+":"+e.portString()+"/ok")
	expires, err := time.Parse(time.RFC3339, created.ExpiresAt)
	c.Assert(err, qt.IsNil)
	c.Assert(expires.Sub(tx.CreatedAt) > 14*time.Minute, qt.IsTrue)
	again, _ := e.create(c, "vote")
	granted++
	c.Assert(e.store.transaction(again.State).Nonce, qt.Not(qt.Equals), tx.Nonce)

	for _, u := range []string{
		testOrigin, testOrigin + "/", testOrigin + "/es", testOrigin + "/pt-br/",
		testOrigin + "/processes/0123456789abcdef01234567", "http://localhost:3000/ca/processes/0123456789abcdef01234567",
	} {
		req := valid
		req.ReturnURL = u
		status, _ := create(req)
		c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%s", u))
	}
	for _, u := range []string{
		"https://evil.test/", "https://app.test.evil.test/", testOrigin + "/?x=1", testOrigin + "/#frag",
		"https://user@app.test/", testOrigin + "/ca/processes/0123456789abcdef01234567/results",
		testOrigin + "/CA", testOrigin + "/processes/0123456789ABCDEF01234567", "http://app.test/",
		"//app.test/", "/processes/0123456789abcdef01234567", testOrigin + "/%2e%2e/", "javascript:alert(1)",
		testOrigin + "\\@evil.test", testOrigin + "/" + strings.Repeat("a", 3000),
	} {
		req := valid
		req.ReturnURL = u
		status, body := create(req)
		c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", u))
		c.Assert(body.Data.Reason, qt.Equals, "", qt.Commentf("%s", u))
	}
	for name, mutate := range map[string]func(*apicommon.CertAuthCreateRequest){
		"short challenge": func(r *apicommon.CertAuthCreateRequest) { r.Challenge = "abc" },
		"plain method":    func(r *apicommon.CertAuthCreateRequest) { r.ChallengeMethod = "plain" },
		"other purpose":   func(r *apicommon.CertAuthCreateRequest) { r.Purpose = "login" },
	} {
		req := valid
		mutate(&req)
		status, _ := create(req)
		c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", name))
	}

	// the process must take sign-ins, and its census sign in with a certificate
	e.store.questions = []db.VotingProcessQuestion{{Status: db.QuestionStatusEnded}, {Status: db.QuestionStatusResults}}
	status, _ := create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.questions = []db.VotingProcessQuestion{{Status: db.QuestionStatusPaused}}
	e.store.vp.EndDate = time.Now().Add(-2 * time.Minute)
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.vp.EndDate = time.Now().Add(time.Hour)
	e.store.vp.StartDate = time.Now().Add(24 * time.Hour) // upcoming is fine
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusOK)
	e.store.census.AuthMethod = ""
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	e.store.census.AuthMethod = db.CensusAuthMethodCertificate
	e.store.vp.Published = false
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.vp.Published = true
	status = e.post(c, "/processes/"+bson.NewObjectID().Hex()+"/auth/cert", valid, nil)
	c.Assert(status, qt.Equals, http.StatusNotFound)

	// the create limit: 60 per minute from one IP on one process; refused creates spent nothing
	for granted < DefaultCreatesPerMinute {
		status, _ := create(valid)
		c.Assert(status, qt.Equals, http.StatusOK)
	}
	status, body := create(valid)
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
	c.Assert(body.Code, qt.Equals, 40950)
	c.Assert(body.Data.Reason, qt.Equals, "too_many_attempts")
	c.Assert(body.Data.RetryAfter > 0, qt.IsTrue)
}
```
Run: `go test -count=1 -run TestCertAuthCreateValidation ./certauth/`
Expected: `certauth/create_test.go:47:63: e.s.Create undefined (type *Service has no field or method Create)`

Step 2: implement
```go
package certauth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	apierrors "github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// maxReturnURLLen bounds a returnUrl before it is parsed.
const maxReturnURLLen = 2048

// returnPathRe is the path a returnUrl may carry: the app root or a process page, with an optional
// /:lang (contract section 3).
var returnPathRe = regexp.MustCompile(`^(/[a-z]{2}(-[a-z]{2})?)?(/processes/[0-9a-f]{24})?/?$`)

// closedStatuses are the question statuses that take no more sign-ins.
var closedStatuses = map[string]bool{
	db.QuestionStatusEnded: true, db.QuestionStatusCanceled: true, db.QuestionStatusResults: true,
}

// Create is POST /processes/{processId}/auth/cert (contract section 3): it validates the attempt,
// spends one unit of the per-IP create limit and stores a fresh transaction, answering its state
// and the per-attempt auth URL. A validation failure is a plain standard error, the limit the
// too_many_attempts envelope.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	pid, err := bson.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		apierrors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}
	var req apicommon.CertAuthCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierrors.ErrMalformedBody.Write(w)
		return
	}
	if err := s.checkCreateRequest(&req); err != nil {
		apierrors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	if apiErr := s.checkProcess(pid); apiErr != nil {
		apiErr.Write(w)
		return
	}
	now := s.now()
	key := sha256.Sum256([]byte(s.requestClientIP(r) + "|" + pid.Hex()))
	limit, err := s.store.ClaimCSPCounter(db.CSPCounterCertAuthCreates, key[:], s.cfg.CreatesPerMinute)
	if err != nil {
		apierrors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !limit.Granted {
		certError(apicommon.CertAuthReasonTooManyAttempts,
			apicommon.CertAuthErrorData{RetryAfter: retryAfter(now, limit.ResetAt)}).Write(w)
		return
	}
	tx := &db.CertAuthTransaction{
		State: randomToken(), Nonce: randomNonce(), Challenge: req.Challenge, ProcessID: pid,
		ReturnURL: req.ReturnURL, Purpose: req.Purpose, Status: db.CertAuthStatusCreated,
		CreatedAt: now, ExpiresAt: now.Add(stateTTL),
	}
	if err := s.store.InsertCertAuthTransaction(tx); err != nil {
		apierrors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	authURL := url.URL{
		Scheme: "https", Host: s.publicHost(tx.Nonce + "." + s.cfg.Host), Path: authenticatePath,
		RawQuery: url.Values{"state": {tx.State}}.Encode(),
	}
	probeURL := url.URL{Scheme: "https", Host: s.publicHost(probeLabel + "." + s.cfg.Host), Path: probePath}
	apicommon.HTTPWriteJSON(w, &apicommon.CertAuthCreateResponse{
		State: tx.State, AuthURL: authURL.String(), ExpiresAt: tx.ExpiresAt.UTC().Format(time.RFC3339),
		ProbeURL: probeURL.String(),
	})
}

// checkCreateRequest validates the body fields.
func (s *Service) checkCreateRequest(req *apicommon.CertAuthCreateRequest) error {
	if !tokenRe.MatchString(req.Challenge) || req.ChallengeMethod != "S256" {
		return fmt.Errorf("challenge must be a 43-character S256 challenge")
	}
	if req.Purpose != apicommon.CertAuthPurposeVote && req.Purpose != apicommon.CertAuthPurposeCheck {
		return fmt.Errorf("purpose must be vote or check")
	}
	return s.checkReturnURL(req.ReturnURL)
}

// checkReturnURL accepts an absolute URL on an allowlisted origin whose path is the app root or a
// process page with an optional /:lang, without query, fragment or userinfo (contract section 3).
func (s *Service) checkReturnURL(raw string) error {
	invalid := fmt.Errorf("returnUrl is not an allowed app URL")
	if len(raw) > maxReturnURLLen || strings.ContainsAny(raw, "#?\\") {
		return invalid
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.RawPath != "" {
		return invalid
	}
	if !secureScheme(u) || !s.v.origins[u.Scheme+"://"+u.Host] || !returnPathRe.MatchString(u.Path) {
		return invalid
	}
	return nil
}

// checkProcess requires a published process that still takes sign-ins, with a certificate census.
// UPCOMING is allowed for both purposes: the redeem decides whether to issue a token.
func (s *Service) checkProcess(pid bson.ObjectID) *apierrors.Error {
	vp, questions, err := s.store.ProcessWithQuestions(pid)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apierrors.Ptr(apierrors.ErrProcessNotFound)
		}
		return apierrors.Ptr(apierrors.ErrGenericInternalServerError.WithErr(err))
	}
	if !vp.Published || !acceptsSignIns(vp, questions) {
		return apierrors.Ptr(apierrors.ErrDuplicateConflict.Withf("the process does not take sign-ins"))
	}
	census, err := s.store.Census(vp.CensusID.Hex())
	if err != nil {
		return apierrors.Ptr(apierrors.ErrGenericInternalServerError.WithErr(err))
	}
	if census.AuthMethod != db.CensusAuthMethodCertificate {
		return apierrors.Ptr(apierrors.ErrInvalidData.Withf("the process census does not sign in with a certificate"))
	}
	return nil
}

// acceptsSignIns reports whether a published process takes sign-ins: not past its end, and at least
// one question neither ENDED, CANCELED nor RESULTS.
func acceptsSignIns(vp *db.VotingProcess, questions []db.VotingProcessQuestion) bool {
	if handlers.ProcessPastEnd(vp) {
		return false
	}
	for i := range questions {
		if !closedStatuses[questions[i].Status] {
			return true
		}
	}
	return false
}

// requestClientIP is the client IP of a main-listener request: the TCP peer, or the rightmost
// X-Forwarded-For entry when the listener only takes requests through the trusted edge proxy.
func (s *Service) requestClientIP(r *http.Request) string {
	if s.cfg.TrustForwardedFor {
		if hops := r.Header.Values("X-Forwarded-For"); len(hops) > 0 {
			entries := strings.Split(hops[len(hops)-1], ",")
			if ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	return clientIP(r.RemoteAddr)
}
```
Run: `go test -count=1 -run TestCertAuthCreateValidation ./certauth/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (TestCertAuthCreateValidation)

Step 3: commit
`git commit -m "feat(certauth): create certificate sign-in attempts" -- certauth/create.go certauth/create_test.go`

Interfaces this task provides to later tasks: `(*Service).Create(w, r)` (reads chi's `processId`);
unexported `checkReturnURL`, `acceptsSignIns(vp, questions)`, `requestClientIP(r)`; test helpers `pkce`,
`serveRoute`, `(*testEnv).post`, `create`.

### Task 10: Redeem endpoint
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/redeem.go`, `certauth/redeem_test.go`
Goal: `POST /processes/{processId}/auth/cert/redeem` in contract order: consume the code (chain leaves storage),
PKCE, per-certificate budget, `Handler.Verify` (40 s), per-verified-document budget, natural-person check, canonical
census lookup, voterRef, holder, and the auth-only token only for an open vote (serves AC8, AC9, AC11, AC12 at unit
level and AC10's logging discipline).

Step 1: write the failing test
```go
package certauth

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// redeem posts a redeem, decoding a 200 into ok and anything else into failure.
func (e *testEnv) redeem(c *qt.C, code, verifier string) (int, *apicommon.CertAuthRedeemResponse, *errorBody) {
	data, err := json.Marshal(apicommon.CertAuthRedeemRequest{Code: code, Verifier: verifier})
	c.Assert(err, qt.IsNil)
	r := chi.NewRouter()
	r.Post("/processes/{processId}/auth/cert/redeem", e.s.Redeem)
	req := httptest.NewRequest(http.MethodPost, "/processes/"+e.store.vp.ID.Hex()+"/auth/cert/redeem",
		bytes.NewReader(data))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		ok := &apicommon.CertAuthRedeemResponse{}
		c.Assert(json.Unmarshal(rec.Body.Bytes(), ok), qt.IsNil)
		return rec.Code, ok, nil
	}
	failure := &errorBody{}
	c.Assert(json.Unmarshal(rec.Body.Bytes(), failure), qt.IsNil, qt.Commentf("body: %s", rec.Body))
	return rec.Code, nil, failure
}

// signIn runs create, authenticate with cert and returns the code and verifier to redeem.
func (e *testEnv) signIn(c *qt.C, purpose string, cert *tls.Certificate) (code, verifier string) {
	created, verifier := e.create(c, purpose)
	_, frag := fragment(c, e.authenticate(c, created.AuthURL, cert))
	c.Assert(frag.Get("state"), qt.Equals, created.State)
	return frag.Get("code"), verifier
}

var (
	maria = testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"}
	jordi = testprovider.Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"}
)

// TestCertAuthRedeemFlow runs create, authenticate and redeem against the in-memory store: the
// happy path for a DNI and an NIE, single use and PKCE, the census match, the two-stage budget and
// the check-only answers. The Docker suites in ./api repeat the essentials against Mongo and the CSP.
func TestCertAuthRedeemFlow(t *testing.T) {
	c := qt.New(t)

	c.Run("vote", func(c *qt.C) {
		e := newTestEnv(c, nil)
		member := e.store.addMember("12.345.678-z", 1)
		other := e.store.addMember("x1234567l", 1)
		code, verifier := e.signIn(c, "vote", e.issue(c, maria))
		status, ok, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		c.Assert(ok.Purpose, qt.Equals, "vote")
		c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
		c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{1})
		c.Assert(ok.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
			FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
		})
		c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
		c.Assert(ok.VoterRef, qt.Matches, `[A-Za-z0-9_-]{22}`)
		c.Assert(e.tokens.issued, qt.DeepEquals, []string{member.ID.Hex()})

		// the chain left storage with the code
		for _, tx := range e.store.txs {
			if tx.Code == code {
				c.Assert(tx.Status, qt.Equals, db.CertAuthStatusConsumed)
				c.Assert(tx.Chain, qt.HasLen, 0)
			}
		}

		// voterRef is stable for the member, and differs for another one
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		_, again, _ := e.redeem(c, code, verifier)
		c.Assert(again.VoterRef, qt.Equals, ok.VoterRef)
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		_, nie, _ := e.redeem(c, code, verifier)
		c.Assert(nie.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
			FullName: "Jordi Puig Serra", DocumentType: "NIE", DocumentEnding: "567L",
		})
		c.Assert(nie.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
		c.Assert(e.tokens.issued[2], qt.Equals, other.ID.Hex())
	})

	c.Run("single use and pkce", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 1)
		cert := e.issue(c, maria)
		code, verifier := e.signIn(c, "vote", cert)
		status, _, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		status, _, failure := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusConflict)
		c.Assert(failure.Data.Reason, qt.Equals, "code_used")

		// a wrong verifier burns the code
		code, verifier = e.signIn(c, "vote", cert)
		_, wrong := pkce()
		status, _, failure = e.redeem(c, code, wrong)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "verifier_mismatch")
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusConflict)
		c.Assert(failure.Data.Reason, qt.Equals, "code_used")

		// an expired code, an unknown one and a malformed one are attempt_expired
		code, verifier = e.signIn(c, "vote", cert)
		for _, tx := range e.store.txs {
			if tx.Code == code {
				tx.AuthenticatedAt = time.Now().Add(-6 * time.Minute)
			}
		}
		for _, candidate := range []string{code, randomToken(), "short"} {
			status, _, failure = e.redeem(c, candidate, verifier)
			c.Assert(status, qt.Equals, http.StatusGone)
			c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
		}

		// a code is bound to its process
		code, verifier = e.signIn(c, "vote", cert)
		e.store.txs[len(e.store.txs)-1].ProcessID = bson.NewObjectID()
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusGone)
		c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
	})

	c.Run("census", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("X1234567L", 0)
		code, verifier := e.signIn(c, "vote", e.issue(c, maria))
		status, _, failure := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Error, qt.Equals, "certificate is not in the census")
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		c.Assert(failure.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
		c.Assert(failure.Data.Holder.DocumentEnding, qt.Equals, "678Z")

		// a zero weight in a weighted census cannot vote either
		e.store.census.Weighted = true
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		c.Assert(e.tokens.count(), qt.Equals, 0)

		// presented, but too large to keep: invalid_chain, never no_certificate
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		for _, tx := range e.store.txs {
			if tx.Code == code {
				tx.Chain = nil
			}
		}
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
		c.Assert(failure.Data.Reason, qt.Equals, "invalid_chain")
	})

	c.Run("budget", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 1)
		redeemWith := func(cert any) (int, *errorBody) {
			var code, verifier string
			if holder, ok := cert.(testprovider.Holder); ok {
				code, verifier = e.signIn(c, "vote", e.issue(c, holder))
			} else {
				code, verifier = e.signIn(c, "vote", cert.(*tls.Certificate))
			}
			status, _, failure := e.redeem(c, code, verifier)
			return status, failure
		}

		// ten forged certificates naming Maria's document spend their own budgets, not hers
		for range 10 {
			forged, err := testprovider.SelfSigned(maria)
			c.Assert(err, qt.IsNil)
			status, failure := redeemWith(forged)
			c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
			c.Assert(failure.Data.Reason, qt.Equals, "untrusted_issuer")
		}
		status, _ := redeemWith(maria)
		c.Assert(status, qt.Equals, http.StatusOK)

		// a verified document not in the census is capped per document, whatever the certificate
		outsider := testprovider.Holder{GivenName: "Nobody", Surname: "Else", Document: "87654321X"}
		for range 10 {
			status, failure := redeemWith(outsider)
			c.Assert(status, qt.Equals, http.StatusForbidden)
			c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		}
		status, failure := redeemWith(outsider)
		c.Assert(status, qt.Equals, http.StatusTooManyRequests)
		c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")
		c.Assert(failure.Data.RetryAfter > 0, qt.IsTrue)
		// another document is unaffected
		status, _ = redeemWith(testprovider.Holder{GivenName: "Other", Surname: "One", Document: "11111111H"})
		c.Assert(status, qt.Equals, http.StatusForbidden)

		// one certificate failing verification is capped before Verify runs
		revoked := e.issue(c, testprovider.Holder{GivenName: "R", Surname: "R", Document: "22222222J", OU: testprovider.OURevoked})
		for range 10 {
			status, failure := redeemWith(revoked)
			c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
			c.Assert(failure.Data.Reason, qt.Equals, "revoked")
		}
		status, failure = redeemWith(revoked)
		c.Assert(status, qt.Equals, http.StatusTooManyRequests)
		c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")

		// revocation outages are never charged
		outage := e.issue(c, testprovider.Holder{GivenName: "O", Surname: "O", Document: "33333333P", OU: testprovider.OUOutage})
		for range 11 {
			status, failure := redeemWith(outage)
			c.Assert(status, qt.Equals, http.StatusServiceUnavailable)
			c.Assert(failure.Data.Reason, qt.Equals, "revocation_unavailable")
			c.Assert(failure.Data.Retryable, qt.IsTrue)
		}
	})

	c.Run("check only", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 3)
		e.store.census.Weighted = true
		e.store.vp.StartDate = time.Now().Add(time.Hour)
		for _, purpose := range []string{"check", "vote"} {
			code, verifier := e.signIn(c, purpose, e.issue(c, maria))
			status, ok, _ := e.redeem(c, code, verifier)
			c.Assert(status, qt.Equals, http.StatusOK)
			c.Assert(ok.Purpose, qt.Equals, purpose)
			c.Assert(ok.AuthToken, qt.IsNil)
			c.Assert(ok.Weight, qt.IsNil)
			c.Assert(ok.Holder.DocumentEnding, qt.Equals, "678Z")
			c.Assert(ok.VoterRef, qt.Not(qt.Equals), "")
			c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
		}
		// open: a check still opens no session, a vote does, with the member weight
		e.store.vp.StartDate = time.Now().Add(-time.Hour)
		code, verifier := e.signIn(c, "check", e.issue(c, maria))
		_, ok, _ := e.redeem(c, code, verifier)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(e.tokens.count(), qt.Equals, 0)
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		_, ok, _ = e.redeem(c, code, verifier)
		c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
		c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{3})
		// a vote created before the end and redeemed after it gets no token
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		e.store.vp.EndDate = time.Now().Add(-2 * time.Minute)
		status, ok, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(e.tokens.count(), qt.Equals, 1)
	})
}

// TestCertAuthErrorMappingLogs pins the logging discipline of the whole flow (AC10): a sign-in with
// no certificate answers a fresh reference although no Verify ran, and a successful and a failed
// sign-in log only cert-check's LogAttrs keys and the attempt ids, never the document, the holder's
// name, the certificate, the code or the verifier.
func TestCertAuthErrorMappingLogs(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	e.store.addMember("12.345.678-z", 1)

	code, verifier := e.signIn(c, "vote", nil)
	status, _, failure := e.redeem(c, code, verifier)
	c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
	c.Assert(failure.Data.Reason, qt.Equals, "no_certificate")
	c.Assert(failure.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
	c.Assert(failure.Data.Retryable, qt.IsFalse)

	cert := e.issue(c, maria)
	code, verifier = e.signIn(c, "vote", cert)
	status, _, _ = e.redeem(c, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK)
	revokedHolder := maria
	revokedHolder.OU = testprovider.OURevoked
	code2, verifier2 := e.signIn(c, "vote", e.issue(c, revokedHolder))
	status, _, _ = e.redeem(c, code2, verifier2)
	c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)

	full := certcheck.Diagnostics{TLS: &certcheck.TLSInfo{}, Certificate: &certcheck.CertificateDetails{}}
	allowed := slices.Concat(attrKeys(full),
		[]string{"transaction", "processId", "certificatePresented", "chainLength", "clientIP", "reference", "error"})
	secrets := []string{
		"12345678Z", "678Z", "Maria", "García", code, verifier, code2, verifier2, "IDCES",
		base64.StdEncoding.EncodeToString(cert.Certificate[0])[:40],
	}
	for _, entry := range e.logs.all() {
		for k, v := range entry.kv {
			c.Assert(slices.Contains(allowed, k), qt.IsTrue, qt.Commentf("log %q key %q", entry.msg, k))
			for _, secret := range secrets {
				c.Assert(strings.Contains(stringify(v), secret), qt.IsFalse, qt.Commentf("log %q key %q", entry.msg, k))
			}
		}
	}
}
```
Run: `go test -count=1 -run 'TestCertAuthRedeemFlow|TestCertAuthErrorMapping' ./certauth/`
Expected: `certauth/redeem_test.go:30:56: e.s.Redeem undefined (type *Service has no field or method Redeem)`

Step 2: implement
```go
package certauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	apierrors "github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// redemption is one redeem in progress: the attempt and what its steps derive.
type redemption struct {
	tx     *db.CertAuthTransaction
	vp     *db.VotingProcess
	census *db.Census
	chain  []*x509.Certificate
}

// Redeem is POST /processes/{processId}/auth/cert/redeem (contract section 6). Every failure is the
// certificate envelope. The steps run in the contract's order: consume the code (single use, the
// chain leaves storage with it), check the PKCE verifier, spend the presented certificate's budget,
// Verify, spend the verified document's budget, match the census, issue the token.
func (s *Service) Redeem(w http.ResponseWriter, r *http.Request) {
	res, apiErr := s.redeemCode(r)
	if apiErr != nil {
		apiErr.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
}

func (s *Service) redeemCode(r *http.Request) (*apicommon.CertAuthRedeemResponse, *apierrors.Error) {
	pid, err := bson.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	var req apicommon.CertAuthRedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !tokenRe.MatchString(req.Code) {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	now := s.now()
	tx, err := s.store.ConsumeCertAuthCode(req.Code, now.Add(-codeTTL))
	if err != nil {
		return nil, s.spentCode(req.Code, err)
	}
	if tx.ProcessID != pid {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	// the code is consumed whatever the verifier says: a stolen code is burnt by its first use
	if !verifierMatches(req.Verifier, tx.Challenge) {
		return nil, s.fail(apicommon.CertAuthReasonVerifierMismatch)
	}
	rd := &redemption{tx: tx}
	if apiErr := s.loadProcess(rd); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := s.parseChain(rd); apiErr != nil {
		return nil, apiErr
	}
	result, apiErr := s.verify(r.Context(), rd)
	if apiErr != nil {
		return nil, apiErr
	}
	return s.issue(rd, result)
}

// fail is the envelope of a backend reason without data.
func (*Service) fail(reason string) *apierrors.Error {
	return apierrors.Ptr(certError(reason, apicommon.CertAuthErrorData{}))
}

// spentCode tells an unusable code apart: redeemed already is code_used, anything else (expired,
// reaped, never issued) attempt_expired.
func (s *Service) spentCode(code string, consumeErr error) *apierrors.Error {
	if !errors.Is(consumeErr, db.ErrNotFound) {
		return apierrors.Ptr(s.internalFailure("consuming the code", consumeErr))
	}
	if tx, err := s.store.CertAuthTransactionByCode(code); err == nil && tx.Status == db.CertAuthStatusConsumed {
		return s.fail(apicommon.CertAuthReasonCodeUsed)
	}
	return s.fail(apicommon.CertAuthReasonAttemptExpired)
}

// verifierMatches checks the RFC 7636 S256 binding in constant time.
func verifierMatches(verifier, challenge string) bool {
	if !verifierRe.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// loadProcess reads the attempt's process and census.
func (s *Service) loadProcess(rd *redemption) *apierrors.Error {
	vp, _, err := s.store.ProcessWithQuestions(rd.tx.ProcessID)
	if err != nil {
		return apierrors.Ptr(s.internalFailure("reading the process", err, "transaction", rd.tx.ID.Hex()))
	}
	census, err := s.store.Census(vp.CensusID.Hex())
	if err != nil {
		return apierrors.Ptr(s.internalFailure("reading the census", err, "transaction", rd.tx.ID.Hex()))
	}
	rd.vp, rd.census = vp, census
	return nil
}

// parseChain turns the stored DER into certificates. No certificate is no_certificate; one that was
// presented but not kept, or does not parse, is invalid_chain; both with a fresh reference, since no
// Verify ran.
func (*Service) parseChain(rd *redemption) *apierrors.Error {
	if len(rd.tx.Chain) == 0 {
		reason := string(certcheck.CodeNoCertificate)
		if rd.tx.ChainPresented {
			reason = string(certcheck.CodeInvalidChain)
		}
		return apierrors.Ptr(certError(reason, apicommon.CertAuthErrorData{Reference: certcheck.NewDiagnosticID()}))
	}
	chain := make([]*x509.Certificate, 0, len(rd.tx.Chain))
	for _, der := range rd.tx.Chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return apierrors.Ptr(certError(string(certcheck.CodeInvalidChain),
				apicommon.CertAuthErrorData{Reference: certcheck.NewDiagnosticID()}))
		}
		chain = append(chain, c)
	}
	rd.chain = chain
	return nil
}

// verify runs the budgeted Verify. The unit spent before it is keyed on the presented leaf's DER,
// so a forged certificate naming someone's document spends only its own budget; an outage
// (revocation_unavailable, internal) gives that unit back. The unit spent after it is keyed on the
// verified document number. Both stay spent on every other outcome, success included.
func (s *Service) verify(ctx context.Context, rd *redemption) (*certcheck.Result, *apierrors.Error) {
	leafSum := sha256.Sum256(rd.chain[0].Raw)
	certClaim, apiErr := s.claimBudget(rd, leafSum[:])
	if apiErr != nil {
		return nil, apiErr
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	result, err := s.handler.Verify(vctx, rd.chain)
	if err != nil {
		e, ok := errors.AsType[*certcheck.Error](err)
		if !ok {
			e = &certcheck.Error{Code: certcheck.CodeInternal, Retryable: true}
		}
		if e.Code.Retryable() {
			if relErr := s.store.ReleaseCSPCounter(certClaim); relErr != nil {
				s.log.Warnw("could not release a certificate budget unit", "error", relErr)
			}
		}
		s.logOutcome(rd, e.Diagnostics)
		return nil, apierrors.Ptr(s.verifyFailure(e, rd.chain[0]))
	}
	s.logOutcome(rd, result.Diagnostics)
	if _, apiErr := s.claimBudget(rd, []byte(result.Identity.Document.Number)); apiErr != nil {
		return nil, apiErr
	}
	if result.Profile.HolderRole != certcheck.RoleNaturalPerson {
		return nil, apierrors.Ptr(certError(string(certcheck.CodeUnsupportedProfile),
			apicommon.CertAuthErrorData{Reference: result.Diagnostics.DiagnosticID}))
	}
	return result, nil
}

// claimBudget spends one unit of the hourly redeem budget of subject (a leaf DER hash or a verified
// document number) on the attempt's process.
func (s *Service) claimBudget(rd *redemption, subject []byte) (db.CSPCounterClaim, *apierrors.Error) {
	h := sha256.New()
	_, _ = h.Write([]byte(rd.tx.ProcessID.Hex()))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write(subject)
	res, err := s.store.ClaimCSPCounter(db.CSPCounterCertAuthFailures, h.Sum(nil), s.v.maxFails)
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("claiming the redeem budget", err, "transaction", rd.tx.ID.Hex()))
	}
	if !res.Granted {
		return nil, apierrors.Ptr(certError(apicommon.CertAuthReasonTooManyAttempts,
			apicommon.CertAuthErrorData{RetryAfter: retryAfter(s.now(), res.ResetAt)}))
	}
	return res.Claim, nil
}

// issue matches the verified document against the census and answers: the token when the attempt
// is a vote and the process accepts votes, a null token otherwise (contract section 11).
func (s *Service) issue(rd *redemption, result *certcheck.Result,
) (*apicommon.CertAuthRedeemResponse, *apierrors.Error) {
	holder := resultHolder(result.Identity)
	reference := result.Diagnostics.DiagnosticID
	notInCensus := apierrors.Ptr(certError(apicommon.CertAuthReasonDNINotInCensus,
		apicommon.CertAuthErrorData{Reference: reference, Holder: &holder}))
	// canonical already: cert-check reports the 9-character number, and the lookup hashes it
	// canonically (SP1), so a member stored as "12.345.678-z" matches
	input := db.OrgMember{NationalID: internal.NormalizeNationalID(result.Identity.Document.Number).Normalized}
	participant, err := s.store.CensusParticipantByLoginHash(*rd.census, input)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notInCensus
	}
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("looking up the census", err, "transaction", rd.tx.ID.Hex()))
	}
	member, err := s.store.OrgMember(rd.census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notInCensus
	}
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("reading the member", err, "transaction", rd.tx.ID.Hex()))
	}
	// by certificate, "cannot vote in this process" is the truthful answer for a zero weight too
	if rd.census.Weighted && member.Weight == 0 {
		return nil, notInCensus
	}
	resp := &apicommon.CertAuthRedeemResponse{
		Purpose: rd.tx.Purpose, VoterRef: s.voterRef(rd.tx.ProcessID, participant.LoginHash),
		Holder: holder, Reference: reference,
	}
	if rd.tx.Purpose != apicommon.CertAuthPurposeVote || !acceptsVotes(rd.vp) {
		return resp, nil
	}
	anchor := internal.HexBytes(rd.tx.ProcessID[:])
	token, err := s.tokens.AuthToken(anchor, internal.HexBytesFromString(member.ID.Hex()), "", "", "", "", "",
		common.Address{})
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("issuing the token", err, "transaction", rd.tx.ID.Hex()))
	}
	weight := uint64(1)
	if rd.census.Weighted {
		weight = member.Weight
	}
	resp.AuthToken, resp.Weight = &token, new(big.Int).SetUint64(weight).Bytes()
	return resp, nil
}

// acceptsVotes reports whether the process is open for votes now: started (a zero StartDate counts
// as started) and not ended.
func acceptsVotes(vp *db.VotingProcess) bool {
	return !handlers.ProcessPastEnd(vp) && !handlers.ProcessBeforeStart(vp)
}

// voterRef is the opaque, stable reference of one participant of one process: an HMAC under a key
// derived from the root key, never the document or a bare hash of it.
func (s *Service) voterRef(pid bson.ObjectID, loginHash []byte) string {
	mac := hmac.New(sha256.New, s.refKey)
	_, _ = mac.Write(pid[:])
	_, _ = mac.Write(loginHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// logOutcome logs a Verify outcome with cert-check's PII-safe attributes and the attempt's ids only.
func (s *Service) logOutcome(rd *redemption, d certcheck.Diagnostics) {
	kv := append([]any{"transaction", rd.tx.ID.Hex(), "processId", rd.tx.ProcessID.Hex()}, diagnosticKV(d)...)
	s.log.Infow("certificate verified", kv...)
}

// diagnosticKV turns cert-check's PII-safe log attributes into key/value pairs for the logger.
func diagnosticKV(d certcheck.Diagnostics) []any {
	attrs := d.LogAttrs()
	kv := make([]any, 0, 2*len(attrs))
	for _, a := range attrs {
		kv = append(kv, a.Key, a.Value.Resolve().Any())
	}
	return kv
}
```
Run: `go test -count=1 -race ./certauth/...`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` and `ok  	github.com/vocdoni/saas-backend/certauth/testprovider`

Step 3: commit
`git commit -m "feat(certauth): redeem certificate sign-in codes" -- certauth/redeem.go certauth/redeem_test.go`

Interfaces this task provides to later tasks: `(*Service).Redeem(w, r)`; unexported `verifierMatches`,
`acceptsVotes(vp)`, `voterRef(pid, loginHash)`, `diagnosticKV(d)`.

### Task 11: CORS dispatcher
Model: opus   Repo: /Users/ferran/Repos/saas-backend   Files: `certauth/cors.go`, `certauth/cors_test.go`
Goal: contract section 13 as written: the two JSON endpoints answer preflights and requests for allowlisted origins
only, no credentials; every other path gets exactly the general middleware's answer (serves AC1's "unchanged" for
CORS with the feature on; the off case is Task 12).

Step 1: write the failing test
```go
package certauth

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/cors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// generalCORS is the main router's CORS as api.initRouter configures it.
func generalCORS() func(http.Handler) http.Handler {
	return cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300,
	}).Handler
}

// TestCertAuthCORS pins the CORS dispatcher (contract section 13): the two certificate endpoints
// answer preflights and requests for the allowlisted origins only, never with credentials, and every
// other path gets exactly what the general middleware answers alone.
func TestCertAuthCORS(t *testing.T) {
	c := qt.New(t)
	s, _, _ := newTestService(c, 443, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	dispatched := s.CORS(generalCORS())(ok)
	general := generalCORS()(ok)
	pid := bson.NewObjectID().Hex()

	serve := func(h http.Handler, method, path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Origin", origin)
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "content-type")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{"/processes/" + pid + "/auth/cert", "/processes/" + pid + "/auth/cert/redeem"} {
		for _, method := range []string{http.MethodOptions, http.MethodPost} {
			allowed := serve(dispatched, method, path, testOrigin)
			c.Assert(allowed.Header().Get("Access-Control-Allow-Origin"), qt.Equals, testOrigin, qt.Commentf("%s %s", method, path))
			c.Assert(allowed.Header().Get("Access-Control-Allow-Credentials"), qt.Equals, "")
			foreign := serve(dispatched, method, path, "https://evil.test")
			c.Assert(foreign.Header().Get("Access-Control-Allow-Origin"), qt.Equals, "", qt.Commentf("%s %s", method, path))
			c.Assert(foreign.Header().Get("Access-Control-Allow-Credentials"), qt.Equals, "")
		}
	}

	// every other path, including the typed auth steps and look-alike paths, is untouched
	for _, path := range []string{
		"/processes/" + pid + "/auth/0", "/processes/" + pid + "/auth/cert/other", "/processes/" + pid + "/auth/certs",
		"/processes/" + pid, "/ping",
	} {
		for _, method := range []string{http.MethodOptions, http.MethodPost, http.MethodGet} {
			got, want := serve(dispatched, method, path, "https://evil.test"), serve(general, method, path, "https://evil.test")
			c.Assert(got.Code, qt.Equals, want.Code, qt.Commentf("%s %s", method, path))
			c.Assert(got.Header(), qt.DeepEquals, want.Header(), qt.Commentf("%s %s", method, path))
		}
	}
}
```
Run: `go test -count=1 -run TestCertAuthCORS ./certauth/`
Expected: `certauth/cors_test.go:31:18: s.CORS undefined (type *Service has no field or method CORS)`

Step 2: implement
```go
package certauth

import (
	"net/http"
	"regexp"

	"github.com/go-chi/cors"
)

// jsonEndpointRe matches the paths of the two main-listener certificate endpoints.
var jsonEndpointRe = regexp.MustCompile(`^/processes/[0-9a-f]{24}/auth/cert(/redeem)?$`)

// CORS returns the main router's CORS middleware with the feature on: the two certificate JSON
// endpoints answer preflights and requests for the allowlisted app origins only, without
// credentials (contract section 13); every other path goes through general, the router's CORS
// as it is with the feature off. chi cannot swap a middleware per route, so the one CORS slot
// dispatches by path.
func (s *Service) CORS(general func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	cert := cors.New(cors.Options{
		AllowedOrigins:   s.AllowedOrigins(),
		AllowedMethods:   []string{http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}).Handler
	return func(next http.Handler) http.Handler {
		generalNext, certNext := general(next), cert(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if jsonEndpointRe.MatchString(r.URL.Path) {
				certNext.ServeHTTP(w, r)
				return
			}
			generalNext.ServeHTTP(w, r)
		})
	}
}
```
Run: `go test -count=1 -run TestCertAuthCORS ./certauth/`
Expected: `ok  	github.com/vocdoni/saas-backend/certauth` (TestCertAuthCORS)

Step 3: commit
`git commit -m "feat(certauth): restrict CORS on the certificate endpoints" -- certauth/cors.go certauth/cors_test.go`

Interfaces this task provides to later tasks: `(*Service).CORS(general func(http.Handler) http.Handler)
func(http.Handler) http.Handler`.

### Task 12: API wiring, service flags and the certificate test API
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `api/api.go`, `api/routes.go`, `api/certauth.go`,
`api/api_test.go`, `api/certauth_test.go`, `cmd/service/main.go`, `cmd/service/certauth.go`,
`cmd/service/certauth_test.go`, `example.env`
Goal: `api.Config.CertAuth` (nil = off), the listener started by `Start`, the two routes with swag annotations, the
CORS dispatcher installed only with the feature on, the `--certAuth*` flags read only when a mode is set, and a
second, certificate-enabled API in the api test harness (serves AC1).

Step 1: write the failing test

`api/certauth_test.go` (new; `useCertAPI` swaps `testPort`, safe because the package never runs tests in parallel):
```go
package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	// testCertAuthHost is the auth domain of the certificate-enabled test API.
	testCertAuthHost = "auth.test"
	// testCertAppOrigin is its only allowed app origin.
	testCertAppOrigin = "https://app.test"
)

var (
	// testCertPort is the port of the certificate-enabled API, testCertListenerPort its auth listener.
	testCertPort, testCertListenerPort int
	// testCertAPI is the API with certificate sign-in on; testAPI stays without it.
	testCertAPI *API
	// testCertPKI is the synthetic CA the certificate-enabled API trusts: tests mint voters' certificates
	// with it, and it signs the auth listener's server certificate.
	testCertPKI *testprovider.PKI
)

// startCertAuthAPI starts a second API sharing conf's database, CSP and chain, with certificate
// sign-in on: origin mode on a loopback auth listener, the synthetic provider next to FNMT and idCAT.
func startCertAuthAPI(ctx context.Context, conf Config, rootKey internal.HexBytes) error {
	testCertPort = 30000 + internal.RandomInt(5000)
	testCertListenerPort = 35000 + internal.RandomInt(5000)
	var err error
	if testCertPKI, err = testprovider.NewPKI(); err != nil {
		return err
	}
	certPEM, keyPEM, err := testCertPKI.ServerCertificate("*." + testCertAuthHost)
	if err != nil {
		return err
	}
	service, err := certauth.New(certauth.Config{
		Mode: certauth.ModeOrigin, Host: testCertAuthHost,
		ListenAddr: "127.0.0.1:" + strconv.Itoa(testCertListenerPort), PublicPort: testCertListenerPort,
		TLSCertPEM: certPEM, TLSKeyPEM: keyPEM, Providers: []string{"fnmt", "idcat"},
		AllowedOrigins: []string{testCertAppOrigin}, DefaultReturn: testCertAppOrigin + "/",
		CreatesPerMinute: certauth.DefaultCreatesPerMinute,
		TestProvider:     true, TestProviderCAPEM: testCertPKI.CertPEM(), VocdoniAPI: testAPIEndpoint,
		RootKey: rootKey,
	}, certauth.Deps{Storage: conf.DB, Tokens: conf.CSP})
	if err != nil {
		return err
	}
	conf.Port, conf.CertAuth = testCertPort, service
	testCertAPI = New(ctx, &conf)
	testCertAPI.Start()
	return pingAPI(fmt.Sprintf("http://%s:%d%s", testHost, testCertPort, pingEndpoint), 5)
}

// useCertAPI points every request helper at the certificate-enabled API until the test ends. The
// package's tests never run in parallel, which is what makes swapping testPort safe.
func useCertAPI(t *testing.T) {
	t.Helper()
	saved := testPort
	testPort = testCertPort
	t.Cleanup(func() { testPort = saved })
}

// TestCertAuthDisabled pins feature off (AC1): the API without certificate sign-in configured runs
// no auth listener, and both certificate endpoints answer the plain ErrCertificateAuthDisabled.
func TestCertAuthDisabled(t *testing.T) {
	c := qt.New(t)
	c.Assert(testAPI.certAuth, qt.IsNil)
	pid := bson.NewObjectID().Hex()
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, "",
		&apicommon.CertAuthCreateRequest{}, "processes", pid, "auth", "cert")
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, "",
		&apicommon.CertAuthRedeemRequest{}, "processes", pid, "auth", "cert", "redeem")
	// the typed auth steps still route as before
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "", nil, "processes", pid, "auth", "7")
	// and the CORS stays the permissive one, for the certificate paths too
	req, err := http.NewRequest(http.MethodOptions, testURL("/processes/"+pid+"/auth/cert"), nil)
	c.Assert(err, qt.IsNil)
	req.Header.Set("Origin", "https://anywhere.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.Body.Close(), qt.IsNil)
	c.Assert(resp.Header.Get("Access-Control-Allow-Origin"), qt.Equals, "*")
}
```
`api/api_test.go` (TestMain keeps its API and starts the second one):
```diff
diff --git a/api/api_test.go b/api/api_test.go
index e1b4287..4695edc 100644
--- a/api/api_test.go
+++ b/api/api_test.go
@@ -407,7 +407,7 @@ func TestMain(m *testing.M) {
 	}
 
 	// start the API
-	testAPI = New(ctx, &Config{
+	apiConf := &Config{
 		Host:                testHost,
 		Port:                testPort,
 		Secret:              testSecret,
@@ -432,13 +432,18 @@ func TestMain(m *testing.M) {
 			}
 			return testAPI.lookupVoteOnChain(ctx, nullifier)
 		},
-	})
+	}
+	testAPI = New(ctx, apiConf)
 	testAPI.Start()
 	// wait for the API to start
 	if err := pingAPI(testURL(pingEndpoint), 5); err != nil {
 		panic(err)
 	}
 	log.Infow("API server started", "host", testHost, "port", testPort)
+	// a second API on the same database and CSP with certificate sign-in on (see useCertAPI)
+	if err := startCertAuthAPI(ctx, *apiConf, *rootKey); err != nil {
+		panic(err)
+	}
 	// run the tests
 	m.Run()
 }
```
`cmd/service/certauth_test.go` (new):
```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// TestCertAuthConfig pins how the --certAuth* configuration is read: comma lists from the
// environment, the defaults, and the PEM files loaded by path.
func TestCertAuthConfig(t *testing.T) {
	c := qt.New(t)
	dir := t.TempDir()
	certFile := filepath.Join(dir, "cert.pem")
	c.Assert(os.WriteFile(certFile, []byte("CERT"), 0o600), qt.IsNil)
	t.Setenv("VOCDONI_CERTAUTHMODE", "origin")
	t.Setenv("VOCDONI_CERTAUTHHOST", "cert.auth-dev.vocdoni.io")
	t.Setenv("VOCDONI_CERTAUTHALLOWEDORIGINS", "https://app-dev.vocdoni.io, http://localhost:3000,")
	t.Setenv("VOCDONI_CERTAUTHTLSCERT", certFile)

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	saved := flag.CommandLine
	flag.CommandLine = flags
	defer func() { flag.CommandLine = saved }()
	defineCertAuthFlags()
	v := viper.New()
	c.Assert(initViper(v, flags), qt.IsNil)

	cfg, err := certAuthConfig(v)
	c.Assert(err, qt.IsNil)
	c.Assert(cfg.Mode, qt.Equals, "origin")
	c.Assert(cfg.Host, qt.Equals, "cert.auth-dev.vocdoni.io")
	c.Assert(cfg.AllowedOrigins, qt.DeepEquals, []string{"https://app-dev.vocdoni.io", "http://localhost:3000"})
	c.Assert(cfg.Providers, qt.DeepEquals, []string{"fnmt", "idcat"})
	c.Assert(cfg.ListenAddr, qt.Equals, ":8443")
	c.Assert(cfg.PublicPort, qt.Equals, 443)
	c.Assert(cfg.CreatesPerMinute, qt.Equals, 60)
	c.Assert(string(cfg.TLSCertPEM), qt.Equals, "CERT")
	c.Assert(cfg.TLSKeyPEM, qt.IsNil)

	t.Setenv("VOCDONI_CERTAUTHTLSKEY", filepath.Join(dir, "missing.pem"))
	_, err = certAuthConfig(v)
	c.Assert(err, qt.ErrorMatches, "reading certAuthTLSKey: .*")
}
```
Run: `go vet ./api/ ./cmd/service/`
Expected: `vet: api/certauth_test.go:61:18: conf.CertAuth undefined (type Config has no field or method CertAuth)`

Step 2: implement

`api/api.go`:
```diff
diff --git a/api/api.go b/api/api.go
index 68de721..cfacb6e 100644
--- a/api/api.go
+++ b/api/api.go
@@ -70,6 +70,7 @@ import (
 	"github.com/hashicorp/golang-lru/v2/expirable"
 	"github.com/vocdoni/saas-backend/account"
 	"github.com/vocdoni/saas-backend/api/apicommon"
+	"github.com/vocdoni/saas-backend/certauth"
 	"github.com/vocdoni/saas-backend/csp"
 	"github.com/vocdoni/saas-backend/csp/handlers"
 	"github.com/vocdoni/saas-backend/db"
@@ -133,6 +134,10 @@ type Config struct {
 	// ask the chain whether a vote is on chain. Nil (production) uses Account, bounded by the
 	// lookup slots shared with POST /votes/verify. Tests set it to inject lookup failures.
 	VoteOnChainLookup handlers.VoteOnChainLookup
+	// CertAuth is the certificate sign-in feature (its auth listener and the two JSON endpoints).
+	// Nil when it is not configured: the endpoints answer ErrCertificateAuthDisabled, certificate
+	// censuses are refused, and nothing else changes.
+	CertAuth *certauth.Service
 }
 
 // StatusEnqueuer hands question status reconciliations to the background syncer. It is satisfied by
@@ -219,6 +224,8 @@ type API struct {
 	electionReads *electionReads
 	// voteOnChain tells whether a vote is on chain (see Config.VoteOnChainLookup).
 	voteOnChain handlers.VoteOnChainLookup
+	// certAuth is the certificate sign-in feature, nil when it is off (see Config.CertAuth).
+	certAuth *certauth.Service
 	// draftLocks makes the draft cap check (OrgCanCreateVotingProcessDraft) and the draft write it
 	// allows one step per organization, so parallel creations cannot pass the same count.
 	// ponytail: in-memory, like orgTxLocks — one API instance; a DB-side conditional write if that changes
@@ -331,6 +338,7 @@ func New(ctx context.Context, conf *Config) *API {
 		return a.account.Election(id)
 	})
 	a.voteOnChain = conf.VoteOnChainLookup
+	a.certAuth = conf.CertAuth
 	if a.voteOnChain == nil {
 		a.voteOnChain = a.lookupVoteOnChain
 	}
@@ -397,6 +405,13 @@ func (a *API) Start() {
 			log.Fatalf("failed to start the API server: %v", err) //revive:disable:deep-exit
 		}
 	}()
+	if a.certAuth != nil {
+		go func() {
+			if err := a.certAuth.ListenAndServe(); err != nil {
+				log.Fatalf("failed to start the certificate auth listener: %v", err) //revive:disable:deep-exit
+			}
+		}()
+	}
 }
 
 // router creates the router with all the routes and middleware.
@@ -405,13 +420,19 @@ func (a *API) Start() {
 func (a *API) initRouter() http.Handler {
 	// Create the router with a basic middleware stack
 	r := chi.NewRouter()
-	r.Use(cors.New(cors.Options{
+	generalCORS := cors.New(cors.Options{
 		AllowedOrigins:   []string{"*"},
 		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
 		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
 		AllowCredentials: true,
 		MaxAge:           300, // Maximum value not ignored by any of major browsers
-	}).Handler)
+	}).Handler
+	if a.certAuth != nil {
+		// the certificate endpoints answer the allowlisted app origins only, without credentials
+		r.Use(a.certAuth.CORS(generalCORS))
+	} else {
+		r.Use(generalCORS)
+	}
 	r.Use(middleware.Logger)
 	r.Use(middleware.Recoverer)
 	if a.originSecret != "" {
@@ -578,6 +599,8 @@ func (a *API) initRouter() http.Handler {
 		handle(r, http.MethodGet, processesResultsEndpoint, a.votingProcessResultsHandler)
 		handle(r, http.MethodPost, processesCheckEndpoint, cspHandlers.ProcessCheckHandler)
 		handle(r, http.MethodPost, processesAuthEndpoint, cspHandlers.ProcessAuthHandler)
+		handle(r, http.MethodPost, processesAuthCertEndpoint, a.certAuthCreateHandler)
+		handle(r, http.MethodPost, processesAuthCertRedeemEndpoint, a.certAuthRedeemHandler)
 		handle(r, http.MethodPost, processesAuthResendEndpoint, cspHandlers.ProcessAuthResendHandler)
 		handle(r, http.MethodPost, processesSignEndpoint, cspHandlers.ProcessSignHandler)
 		handle(r, http.MethodPost, processesSignBatchEndpoint, cspHandlers.ProcessSignBatchHandler)
```
`api/routes.go`:
```diff
diff --git a/api/routes.go b/api/routes.go
index 1dcd16c..6b2611e 100644
--- a/api/routes.go
+++ b/api/routes.go
@@ -203,7 +203,12 @@ const (
 	// CSP voter routes for a voting process (public)
 	processesAuthEndpoint       = "/processes/{processId}/auth/{step}"
 	processesAuthResendEndpoint = "/processes/{processId}/auth/resend"
-	processesSignEndpoint       = "/processes/{processId}/sign"
+	// POST /processes/{processId}/auth/cert creates a certificate sign-in attempt, and
+	// .../auth/cert/redeem trades its code for the CSP token (public; certauth). chi prefers the
+	// static segment, so they coexist with auth/{step}.
+	processesAuthCertEndpoint       = "/processes/{processId}/auth/cert"
+	processesAuthCertRedeemEndpoint = "/processes/{processId}/auth/cert/redeem"
+	processesSignEndpoint           = "/processes/{processId}/sign"
 	// POST /processes/{processId}/sign-batch to sign every question's ballot in one call, so a
 	// voter casting a multi-question process does not need one round trip per question.
 	// Authorization is all or nothing — a rejected batch signs nothing — but the signing that
```
`api/certauth.go` (new):
```go
package api

import (
	"net/http"

	"github.com/vocdoni/saas-backend/errors"
)

// certAuthCreateHandler godoc
//
//	@Summary		Create a certificate sign-in attempt
//	@Description	Starts a digital-certificate sign-in (FNMT natural person or idCAT) for a voter of a
//	@Description	process whose census has `authMethod: "certificate"`. Public, no Authorization header.
//	@Description	The body carries a PKCE S256 `challenge` (43 characters), `challengeMethod` "S256", the
//	@Description	`returnUrl` to come back to (an allowlisted app origin, path `/`, `/:lang`,
//	@Description	`/processes/{id}` or `/:lang/processes/{id}`, no query, fragment or userinfo) and the
//	@Description	`purpose` ("vote", or "check" for a pre-check of an UPCOMING process).
//	@Description
//	@Description	The answer's `authUrl` is on the separate auth listener, on a fresh host per attempt
//	@Description	(`https://<nonce>.<certAuthHost>/v1/authenticate?state=...`, TLS 1.2, client certificate
//	@Description	requested): the browser navigates there, presents its certificate, and is sent back with
//	@Description	`303 <returnUrl>#code=<code>&state=<state>` (or `#error=attempt_expired`), never
//	@Description	verifying anything there. `probeUrl` (`https://probe.<certAuthHost>/ok`, no client
//	@Description	certificate requested) lets the app detect networks that block the auth listener.
//	@Description	The state is single use and valid 15 minutes.
//	@Description
//	@Description	Validation failures are plain errors (400; 404 unknown process; 409 a process that is not
//	@Description	published or no longer takes sign-ins; 400 a census that is not a certificate census).
//	@Description	Over the per-IP and process limit (60 per minute by default) it answers 429, code 40950,
//	@Description	with `data.reason` "too_many_attempts" and `data.retryAfter`. Without certificate sign-in
//	@Description	configured on the server it answers 400, code 40185.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CertAuthCreateRequest	true	"PKCE challenge, return URL and purpose"
//	@Success		200			{object}	apicommon.CertAuthCreateResponse
//	@Failure		400			{object}	errors.Error	"Invalid request, not a certificate census, or certificate sign-in not enabled (40185)"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		409			{object}	errors.Error	"Process not published, or no longer taking sign-ins"
//	@Failure		429			{object}	errors.Error	"Too many attempts (40950, data.reason too_many_attempts)"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/auth/cert [post]
func (a *API) certAuthCreateHandler(w http.ResponseWriter, r *http.Request) {
	if a.certAuth == nil {
		errors.ErrCertificateAuthDisabled.Write(w)
		return
	}
	a.certAuth.Create(w, r)
}

// certAuthRedeemHandler godoc
//
//	@Summary		Redeem a certificate sign-in code
//	@Description	Trades the `code` the auth listener returned in the URL fragment, with the PKCE
//	@Description	`verifier`, for the voter's CSP token. Public, no Authorization header. The code is single
//	@Description	use and valid 5 minutes, and is consumed by the first redeem whatever its outcome. The
//	@Description	server verifies the presented certificate (chain, profile, revocation: up to ~40 s),
//	@Description	matches its DNI/NIE against the census `nationalId` and answers the `holder` (full name,
//	@Description	document type, last 4 characters), an opaque `voterRef`, the diagnostic `reference`, and
//	@Description	`authToken`: the same pre-verified token an auth/0 auth-only census issues, usable with
//	@Description	check, sign-batch, blind-point, blind-sign and sign-info. `authToken` is null (and
//	@Description	`weight` absent) for a "check" attempt and for a "vote" while the process does not
//	@Description	accept votes.
//	@Description
//	@Description	Every failure is code 40950 with `data.reason`: the cert-check codes verbatim
//	@Description	(no_certificate, provider_mismatch, untrusted_issuer, invalid_chain, expired,
//	@Description	not_yet_valid, unsupported_profile, unsupported_purpose, weak_algorithm, test_certificate,
//	@Description	identity_invalid, identity_unsupported_document, revoked, revocation_unknown: 422;
//	@Description	revocation_unavailable: 503; internal: 500) and dni_not_in_census (403),
//	@Description	attempt_expired (410), code_used (409), verifier_mismatch (403), too_many_attempts (429).
//	@Description	`data` also carries `retryable` (true only for revocation_unavailable and internal), and
//	@Description	when they apply `reference`, `holder`, `issuer`, `notAfter` and `retryAfter`. Failed
//	@Description	attempts are capped per hour per presented certificate and per verified document;
//	@Description	revocation outages are never charged. Without certificate sign-in configured on the
//	@Description	server it answers 400, code 40185.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CertAuthRedeemRequest	true	"Code and PKCE verifier"
//	@Success		200			{object}	apicommon.CertAuthRedeemResponse
//	@Failure		400			{object}	errors.Error	"Certificate sign-in not enabled (40185)"
//	@Failure		403			{object}	errors.Error	"dni_not_in_census or verifier_mismatch (40950)"
//	@Failure		409			{object}	errors.Error	"code_used (40950)"
//	@Failure		410			{object}	errors.Error	"attempt_expired (40950)"
//	@Failure		422			{object}	errors.Error	"Certificate rejected, data.reason is the cert-check code (40950)"
//	@Failure		429			{object}	errors.Error	"too_many_attempts (40950)"
//	@Failure		500			{object}	errors.Error	"internal, retryable (40950)"
//	@Failure		503			{object}	errors.Error	"revocation_unavailable, retryable (40950)"
//	@Router			/processes/{processId}/auth/cert/redeem [post]
func (a *API) certAuthRedeemHandler(w http.ResponseWriter, r *http.Request) {
	if a.certAuth == nil {
		errors.ErrCertificateAuthDisabled.Write(w)
		return
	}
	a.certAuth.Redeem(w, r)
}
```
`cmd/service/certauth.go` (new):
```go
package main

import (
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/certauth"
)

// defineCertAuthFlags defines the --certAuth* flags (VOCDONI_CERTAUTH* variables). Certificate
// sign-in is off unless certAuthMode is set, and then none of the others is read.
func defineCertAuthFlags() {
	flag.String("certAuthMode", "", "certificate sign-in: empty (off), origin or cloudflare")
	flag.String("certAuthHost", "", "certificate auth domain: attempts run on <nonce>.<host>, the probe on probe.<host>")
	flag.String("certAuthListenAddr", certauth.DefaultListenAddr, "bind address of the certificate auth listener")
	flag.Int("certAuthPublicPort", certauth.DefaultPublicPort, "public port of the auth listener URLs (omitted when 443)")
	flag.String("certAuthTLSCert", "", "PEM file of the auth listener certificate, covering *.<certAuthHost>")
	flag.String("certAuthTLSKey", "", "PEM file of the auth listener certificate key")
	flag.String("certAuthProviders", "fnmt,idcat", "comma list of certificate providers (fnmt, idcat)")
	flag.String("certAuthAllowedOrigins", "", "comma list of the app origins a returnUrl, the CORS and the probe allow")
	flag.String("certAuthDefaultReturn", "", "app URL an expired certificate attempt is sent back to")
	flag.Int("certAuthCreatesPerMinute", certauth.DefaultCreatesPerMinute,
		"certificate attempts per client IP and process per minute (<= 0 = unlimited)")
	flag.Bool("certAuthProxyProtocol", false, "accept PROXY protocol headers from certAuthProxyFrontIPs (origin mode)")
	flag.String("certAuthProxyFrontIPs", "", "comma list of the CIDRs allowed to send PROXY protocol headers")
	flag.String("certAuthCFOriginPullCA", "", "PEM file of the Cloudflare Authenticated Origin Pulls CA (cloudflare mode)")
	flag.String("certAuthCFAllowedIPs", "", "comma list of the CIDRs the auth listener accepts in cloudflare mode")
	flag.Bool("certAuthTestProvider", false, "enable the synthetic certificate provider (dev and e2e only)")
	flag.String("certAuthTestProviderCA", "", "PEM file of the synthetic certificate provider CA")
	flag.String("certAuthTestProviderAllowedAPIs", "", "comma list of extra --vocdoniApi hosts the synthetic provider allows")
}

// certAuthConfig reads the --certAuth* configuration and its PEM files. certauth.New validates it.
func certAuthConfig(v *viper.Viper) (certauth.Config, error) {
	cfg := certauth.Config{
		Mode:                    v.GetString("certAuthMode"),
		Host:                    v.GetString("certAuthHost"),
		ListenAddr:              v.GetString("certAuthListenAddr"),
		PublicPort:              v.GetInt("certAuthPublicPort"),
		Providers:               splitList(v.GetString("certAuthProviders")),
		AllowedOrigins:          splitList(v.GetString("certAuthAllowedOrigins")),
		DefaultReturn:           v.GetString("certAuthDefaultReturn"),
		CreatesPerMinute:        v.GetInt("certAuthCreatesPerMinute"),
		ProxyProtocol:           v.GetBool("certAuthProxyProtocol"),
		ProxyFrontCIDRs:         splitList(v.GetString("certAuthProxyFrontIPs")),
		CFAllowedCIDRs:          splitList(v.GetString("certAuthCFAllowedIPs")),
		TestProvider:            v.GetBool("certAuthTestProvider"),
		TestProviderAllowedAPIs: splitList(v.GetString("certAuthTestProviderAllowedAPIs")),
	}
	var err error
	for _, f := range []struct {
		key string
		dst *[]byte
	}{
		{"certAuthTLSCert", &cfg.TLSCertPEM},
		{"certAuthTLSKey", &cfg.TLSKeyPEM},
		{"certAuthCFOriginPullCA", &cfg.CFOriginPullCAPEM},
		{"certAuthTestProviderCA", &cfg.TestProviderCAPEM},
	} {
		path := v.GetString(f.key)
		if path == "" {
			continue
		}
		if *f.dst, err = os.ReadFile(path); err != nil {
			return certauth.Config{}, fmt.Errorf("reading %s: %w", f.key, err)
		}
	}
	return cfg, nil
}

// splitList splits a comma list, trimming spaces and dropping empty items. Viper splits a string
// slice from an environment variable on spaces, not commas, so the lists are plain strings.
func splitList(s string) []string {
	var out []string
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
```
`cmd/service/main.go`:
```diff
diff --git a/cmd/service/main.go b/cmd/service/main.go
index f5792df..cc03a86 100644
--- a/cmd/service/main.go
+++ b/cmd/service/main.go
@@ -15,6 +15,7 @@ import (
 	"github.com/spf13/viper"
 	"github.com/vocdoni/saas-backend/account"
 	"github.com/vocdoni/saas-backend/api"
+	"github.com/vocdoni/saas-backend/certauth"
 	"github.com/vocdoni/saas-backend/csp"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
@@ -93,6 +94,7 @@ func main() {
 	flag.Duration("statusSyncInterval", 60*time.Second, "poll cadence for confirm retries + read freshness window (0=60s)")
 	flag.Duration("statusSyncConfirmTimeout", 5*time.Minute, "max wait for on-chain confirmation before reconciling (0=5m)")
 	flag.Bool(disableBlindCSPKey, false, "temporarily stop offering anonymous voting (no new anonymous processes, no blind signing)")
+	defineCertAuthFlags()
 	// parse flags
 	flag.Parse()
 	// initialize Viper
@@ -277,6 +279,19 @@ func main() {
 		log.Fatalf("could not create the CSP service: %v", err)
 		return
 	}
+	// certificate voter sign-in: only when a mode is set; otherwise nothing of it is read or started
+	if viper.GetString("certAuthMode") != "" {
+		certConf, err := certAuthConfig(viper.GetViper())
+		if err != nil {
+			log.Fatalf("could not configure certificate sign-in: %v", err)
+		}
+		certConf.VocdoniAPI, certConf.RootKey = apiEndpoint, bPrivKey
+		certConf.TrustForwardedFor = apiConf.OriginSecret != ""
+		certConf.MaxHourlyFailures = viper.GetInt("cspMaxHourlyAuthFailures")
+		if apiConf.CertAuth, err = certauth.New(certConf, certauth.Deps{Storage: database, Tokens: apiConf.CSP}); err != nil {
+			log.Fatalf("could not configure certificate sign-in: %v", err)
+		}
+	}
 	// background worker: reconcile a published question's status with the chain on demand (a status
 	// change through the API, or a read of the process/question). Enqueues from the API handlers flow
 	// through apiConf.StatusSyncer.
```
`example.env`:
```diff
diff --git a/example.env b/example.env
index c5865c5..3d696bd 100644
--- a/example.env
+++ b/example.env
@@ -72,3 +72,23 @@ VOCDONI_STATUSSYNCCONFIRMTIMEOUT=5m
 # created, updated or published (unless already partially on chain), blind signing is refused and plans
 # report anonymous voting as unavailable (default false).
 # VOCDONI_DISABLE_BLINDCSP=false
+
+# Certificate voter sign-in (FNMT natural person, idCAT). Off unless VOCDONI_CERTAUTHMODE is set;
+# then the other values are required as described in the README ("Certificate voter sign-in").
+# VOCDONI_CERTAUTHMODE=origin
+# VOCDONI_CERTAUTHHOST=cert.auth-dev.vocdoni.io
+# VOCDONI_CERTAUTHLISTENADDR=:8443
+# VOCDONI_CERTAUTHPUBLICPORT=443
+# VOCDONI_CERTAUTHTLSCERT=/run/secrets/certauth/fullchain.pem
+# VOCDONI_CERTAUTHTLSKEY=/run/secrets/certauth/privkey.pem
+# VOCDONI_CERTAUTHPROVIDERS=fnmt,idcat
+# VOCDONI_CERTAUTHALLOWEDORIGINS=https://app-dev.vocdoni.io
+# VOCDONI_CERTAUTHDEFAULTRETURN=https://app-dev.vocdoni.io/
+# VOCDONI_CERTAUTHCREATESPERMINUTE=60
+# VOCDONI_CERTAUTHPROXYPROTOCOL=false
+# VOCDONI_CERTAUTHPROXYFRONTIPS=
+# VOCDONI_CERTAUTHCFORIGINPULLCA=
+# VOCDONI_CERTAUTHCFALLOWEDIPS=
+# VOCDONI_CERTAUTHTESTPROVIDER=false
+# VOCDONI_CERTAUTHTESTPROVIDERCA=
+# VOCDONI_CERTAUTHTESTPROVIDERALLOWEDAPIS=
```
Run: `go vet ./... && go test -count=1 ./cmd/service/`
Expected: `ok  	github.com/vocdoni/saas-backend/cmd/service` (TestDisableBlindCSPEnvName, TestCertAuthConfig)
Run (Docker: run by the implementer): `go test -count=1 -run '^TestCertAuthDisabled$' ./api/`
Expected: `ok  	github.com/vocdoni/saas-backend/api` (passed in the trial merge)

Step 3: commit
`git commit -m "feat(api): wire certificate sign-in into the service" -- api/api.go api/routes.go api/certauth.go api/api_test.go api/certauth_test.go cmd/service/main.go cmd/service/certauth.go cmd/service/certauth_test.go example.env`

Interfaces this task provides to later tasks: `api.Config.CertAuth`, `API.certAuth`, routes
`processesAuthCertEndpoint`, `processesAuthCertRedeemEndpoint`; test harness `testCertAPI`, `testCertPort`,
`testCertListenerPort`, `testCertPKI`, `testCertAuthHost`, `testCertAppOrigin`, `startCertAuthAPI`, `useCertAPI(t)`.

### Task 13: Certificate censuses in the API
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `api/apicommon/voting_processes.go`,
`api/processes_census.go`, `api/processes_census_validate.go`, `api/processes_publish.go`, `csp/handlers/auth.go`,
`api/certauth_census_test.go`
Goal: `CensusSpec.authMethod` on create, draft update, validation and every read; feature-off refusal in the API
layer; certificate-census validation semantics (warnings, duplicates still block); the publish guard (feature off,
SP1 migration pending); `auth/0` refusing certificate censuses (serves AC1 census half, AC2, AC3, AC5).

Step 1: write the failing test
```go
package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// certCensus is a certificate census over the given members.
func certCensus(memberIDs ...string) apicommon.CensusSpec {
	return apicommon.CensusSpec{
		AuthMethod: db.CensusAuthMethodCertificate,
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		MemberIDs:  memberIDs,
	}
}

// certVoters creates an organization with one member per nationalId, as stored (weights 1, 2...),
// and returns the admin token, the organization and the member ids in order.
func certVoters(t *testing.T, nationalIDs ...string) (string, common.Address, []string) {
	t.Helper()
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := make([]apicommon.OrgMember, len(nationalIDs))
	for i, nid := range nationalIDs {
		members[i] = apicommon.OrgMember{
			MemberNumber: fmt.Sprintf("C%d-%d", internal.RandomInt(1000000), i), NationalID: nid,
			Weight: strconv.Itoa(i + 1),
		}
	}
	stored := postOrgMembers(t, token, orgAddress, members...)
	byNumber := make(map[string]string, len(stored))
	for _, m := range stored {
		byNumber[m.MemberNumber] = m.ID
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = byNumber[m.MemberNumber]
	}
	return token, orgAddress, ids
}

// TestCertAuthDisabledCensus pins the census half of feature off (AC1): without certificate sign-in
// configured, a certificate census is refused on create and validation with ErrCertificateAuthDisabled,
// before the db layer (which knows nothing of the feature) sees it.
func TestCertAuthDisabledCensus(t *testing.T) {
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress)
	req.Census = certCensus(ids...)
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, token, req, processesCreateEndpoint)
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)
}

// TestCertificateCensusSpec pins the census authMethod round trip (AC2): accepted on create, draft
// update and validation with authFields ["nationalId"] and no 2FA, echoed on the public process read
// and the list; any other field combination and unknown methods answer 400.
func TestCertificateCensusSpec(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")

	req := minimalVotingProcessRequest(orgAddress)
	req.Census = certCensus(ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	c.Assert(got.Census.AuthFields, qt.DeepEquals, db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID})

	// a draft update keeps it, and can drop it again
	req.Census.Anonymous = true
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, req, "processes", pid)
	got = requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	c.Assert(got.Census.Anonymous, qt.IsTrue)

	// published, the public read and the list echo it too
	pid, _ = publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	public := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", pid)
	c.Assert(public.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
	list := requestAndParse[apicommon.VotingProcessListResponse](t, http.MethodGet, token, nil,
		processesCreateEndpoint+"?orgAddress="+orgAddress.String())
	found := false
	for _, p := range list.Processes {
		if p.ID == pid {
			found = true
			c.Assert(p.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)
		}
	}
	c.Assert(found, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)

	for name, census := range map[string]apicommon.CensusSpec{
		"with 2fa": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
			TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		},
		"other field": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		},
		"extra field": {
			AuthMethod: db.CensusAuthMethodCertificate, MemberIDs: ids,
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID, db.OrgMemberAuthFieldsName},
		},
		"unknown method": {
			AuthMethod: "fingerprint", MemberIDs: ids, AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		},
	} {
		bad := minimalVotingProcessRequest(orgAddress)
		bad.Census = census
		c.Assert(requestAndExpectError(t, http.MethodPost, token, bad, processesCreateEndpoint).Code,
			qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("%s", name))
		c.Assert(requestAndExpectError(t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: census},
			processesCensusValidateEndpoint).Code, qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("%s", name))
	}
}

// TestValidateCertificateCensus pins the validation of a certificate census (AC3): members whose
// nationalId is missing, has a wrong letter, is another tax id or is not a DNI/NIE are warnings only
// (200, never missingData); canonical duplicates still answer 400 naming both members.
func TestValidateCertificateCensus(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z", "", "12345678A", "B12345678", "passport-1")

	resp := requestAndParse[apicommon.ValidateProcessCensusResponse](t, http.MethodPost, token,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(ids...)},
		processesCensusValidateEndpoint)
	classes := map[string]string{}
	for _, w := range resp.NationalIDWarnings {
		classes[w.MemberID] = w.Class
	}
	c.Assert(classes, qt.DeepEquals, map[string]string{
		ids[1]: "missing", ids[2]: "letter_mismatch", ids[3]: "unsupported_document", ids[4]: "invalid",
	})

	// the same members on a typed nationalId census: the missing one blocks, as before
	_, status := testRequest(t, http.MethodPost, token, &apicommon.ValidateProcessCensusRequest{
		OrgAddress: orgAddress.Bytes(),
		Census:     apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: ids},
	}, processesCensusValidateEndpoint)
	c.Assert(status, qt.Equals, http.StatusBadRequest)

	// canonical twins block, naming both, with the duplicate warnings
	token2, org2, twinIDs := certVoters(t, "12345678Z", "12.345.678-z")
	body, status := testRequest(t, http.MethodPost, token2,
		&apicommon.ValidateProcessCensusRequest{OrgAddress: org2.Bytes(), Census: certCensus(twinIDs...)},
		processesCensusValidateEndpoint)
	c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", body))
	c.Assert(string(body), qt.Contains, twinIDs[0])
	c.Assert(string(body), qt.Contains, twinIDs[1])
	c.Assert(string(body), qt.Contains, "duplicate_after_normalization")
}

// TestCertificateCensusPublishGuard pins the publish guard (AC5): a certificate census does not
// publish on a ledger without SP1's nationalId migration (409), and publishes once it ran.
func TestCertificateCensusPublishGuard(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Census = certCensus(ids...)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	payIfPriced(t, token, pid)

	ledger := testDB.DBClient.Database(testDBName).Collection("migrations")
	version := migrations.NationalIDNormalizationVersion
	_, err := ledger.DeleteMany(context.Background(), bson.M{"version": bson.M{"$gte": version}})
	c.Assert(err, qt.IsNil)
	failure := requestAndExpectError(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(failure.Code, qt.Equals, errors.ErrDuplicateConflict.Code)
	c.Assert(failure.Error(), qt.Contains, "nationalId hash migration pending")

	c.Assert(testDB.RunMigrationsUp(), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	// a certificate draft on a deployment without the feature cannot publish either
	draft := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
		processesCreateEndpoint).ProcessID
	payIfPriced(t, token, draft)
	testPort = testAPI.port // the feature-off API; useCertAPI's cleanup restores it after the test
	failure = requestAndExpectError(t, http.MethodPost, token, nil, "processes", draft, "publish")
	c.Assert(failure.Code, qt.Equals, errors.ErrCertificateAuthDisabled.Code)
}

// TestAuthZeroCertificateCensus pins that a certificate census never signs in with typed details
// (AC5): auth/0 answers the certificate envelope, reason certificate_census.
func TestAuthZeroCertificateCensus(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	failure := postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{NationalID: "12345678Z"})
	c.Assert(failure.Code, qt.Equals, errors.ErrCertificateAuth.Code)
	data, ok := failure.Data.(map[string]any)
	c.Assert(ok, qt.IsTrue)
	c.Assert(data["reason"], qt.Equals, "certificate_census")
	retryable, isBool := data["retryable"].(bool)
	c.Assert(isBool, qt.IsTrue)
	c.Assert(retryable, qt.IsFalse)
}
```
Run: `go vet ./api/`
Expected: `vet: api/certauth_census_test.go:24:3: unknown field AuthMethod in struct literal of type apicommon.CensusSpec`

Step 2: implement
```diff
diff --git a/api/apicommon/voting_processes.go b/api/apicommon/voting_processes.go
index e26e2d8..edcb8a1 100644
--- a/api/apicommon/voting_processes.go
+++ b/api/apicommon/voting_processes.go
@@ -24,6 +24,10 @@ type CensusSpec struct {
 	// one other auth field or a 2FA field.
 	AuthFields  db.OrgMemberAuthFields  `json:"authFields,omitempty"`
 	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
+	// AuthMethod "certificate" makes voters sign in only with a digital certificate (FNMT, idCAT)
+	// whose DNI/NIE matches their nationalId: authFields must be exactly ["nationalId"] and
+	// twoFaFields empty. Absent: today's typed member details. Echoed on process reads.
+	AuthMethod db.CensusAuthMethod `json:"authMethod,omitempty" enums:"certificate"`
 	// GroupID is the org member group the census was built from. Round-trips: it is echoed back on
 	// process reads, so a client can restore the group a draft targeted. Absent (not a zero id) when
 	// the census is organization-wide.
@@ -459,6 +463,7 @@ func PublicQuestionResponseFromDB(q *db.VotingProcessQuestion, census *db.Census
 			Anonymous:   census.Anonymous,
 			AuthFields:  census.AuthFields,
 			TwoFaFields: census.TwoFaFields,
+			AuthMethod:  census.AuthMethod,
 		}
 	}
 	return resp
@@ -505,6 +510,7 @@ func VotingProcessResponseFromDB(
 			Anonymous:   census.Anonymous,
 			AuthFields:  census.AuthFields,
 			TwoFaFields: census.TwoFaFields,
+			AuthMethod:  census.AuthMethod,
 			Size:        census.Size,
 		}
 		// guard the zero id so an organization-wide census reports no group at all: omitempty keys off
diff --git a/api/processes_census.go b/api/processes_census.go
index 0c20406..9f6f988 100644
--- a/api/processes_census.go
+++ b/api/processes_census.go
@@ -10,6 +10,7 @@ import (
 	"github.com/vocdoni/saas-backend/csp"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/errors"
+	"github.com/vocdoni/saas-backend/migrations"
 	"go.mongodb.org/mongo-driver/v2/bson"
 )
 
@@ -40,6 +41,42 @@ func (a *API) anonymousCensusRefused(anonymous bool) bool {
 	return anonymous && a.csp.BlindCSPDisabled()
 }
 
+// censusAuthMethodError refuses a census spec whose authMethod this deployment or its login fields
+// cannot serve: a certificate census without certificate sign-in configured (the db layer knows
+// nothing of the feature), or login fields the method does not fit. Nil for any other census.
+func (a *API) censusAuthMethodError(spec apicommon.CensusSpec) error {
+	if spec.AuthMethod == db.CensusAuthMethodCertificate && a.certAuth == nil {
+		return errors.ErrCertificateAuthDisabled
+	}
+	if err := db.ValidateCensusAuthMethod(spec.AuthMethod, spec.AuthFields, spec.TwoFaFields); err != nil {
+		return errors.ErrInvalidData.WithErr(err)
+	}
+	return nil
+}
+
+// certificateCensusPublishBlock is why a certificate census cannot publish yet, or "" and nil: the
+// deployment runs without certificate sign-in, or SP1's nationalId hash migration has not run, so
+// canonical certificate numbers would not match the stored login hashes.
+func (a *API) certificateCensusPublishBlock(census *db.Census) (string, error) {
+	if census.AuthMethod != db.CensusAuthMethodCertificate {
+		return "", nil
+	}
+	if a.certAuth == nil {
+		return "certificate authentication is not enabled on this server", errors.ErrCertificateAuthDisabled
+	}
+	applied, err := a.db.MigrationApplied(migrations.NationalIDNormalizationVersion)
+	if err != nil {
+		return "could not check the nationalId hash migration", errors.ErrGenericInternalServerError.WithErr(err)
+	}
+	if !applied {
+		return nationalIDMigrationPending, errors.ErrDuplicateConflict.With(nationalIDMigrationPending)
+	}
+	return "", nil
+}
+
+// nationalIDMigrationPending is the publish refusal of a certificate census before migration 36.
+const nationalIDMigrationPending = "nationalId hash migration pending"
+
 // resolveOrCreateDefaultCensus materializes the inline census spec of a voting process into
 // a db.Census (auth/2FA policy + participants) and returns it. The census type is inferred
 // from the 2FA fields (SetCensus does this). The census is created unpublished; publishing happens at
@@ -51,12 +88,16 @@ func (a *API) resolveOrCreateDefaultCensus(
 	if a.anonymousCensusRefused(spec.Anonymous) {
 		return nil, nil, errors.ErrInvalidData.With(anonymousVotingUnavailable)
 	}
+	if err := a.censusAuthMethodError(spec); err != nil {
+		return nil, nil, err
+	}
 	census := &db.Census{
 		OrgAddress:  orgAddress,
 		Weighted:    spec.Weighted,
 		Anonymous:   spec.Anonymous,
 		AuthFields:  spec.AuthFields,
 		TwoFaFields: spec.TwoFaFields,
+		AuthMethod:  spec.AuthMethod,
 		CreatedAt:   time.Now(),
 	}
 	censusID, err := a.db.SetCensus(census)
@@ -65,6 +106,9 @@ func (a *API) resolveOrCreateDefaultCensus(
 		if errors.Is(err, db.ErrPasswordNeedsLookupField) {
 			return nil, nil, errors.ErrInvalidData.With(db.PasswordNeedsLookupFieldMessage)
 		}
+		if errors.Is(err, db.ErrInvalidCensusAuthMethod) {
+			return nil, nil, errors.ErrInvalidData.WithErr(err)
+		}
 		return nil, nil, fmt.Errorf("failed to create census: %w", err)
 	}
 	// self-clean: if any step below fails, delete the census we just created so a failed
diff --git a/api/processes_census_validate.go b/api/processes_census_validate.go
index fa97714..8873445 100644
--- a/api/processes_census_validate.go
+++ b/api/processes_census_validate.go
@@ -8,6 +8,7 @@ import (
 	"github.com/vocdoni/saas-backend/api/apicommon"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/errors"
+	"go.mongodb.org/mongo-driver/v2/bson"
 )
 
 // validateProcessCensusHandler godoc
@@ -76,6 +77,10 @@ func (a *API) validateProcessCensusHandler(w http.ResponseWriter, r *http.Reques
 		errors.ErrInvalidData.With(db.PasswordNeedsLookupFieldMessage).Write(w)
 		return
 	}
+	if err := a.censusAuthMethodError(census); err != nil {
+		writeSubscriptionError(w, err)
+		return
+	}
 
 	// select the member set to validate: a group, an explicit subset, or (default) the whole org.
 	var results *db.OrgMemberAggregationResults
@@ -97,6 +102,11 @@ func (a *API) validateProcessCensusHandler(w http.ResponseWriter, r *http.Reques
 		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
 		return
 	}
+	// a certificate census logs in with the nationalId alone, so its missing data is a missing
+	// nationalId: a warning like any value no certificate can match, never a refusal
+	if census.AuthMethod == db.CensusAuthMethodCertificate {
+		results.MissingData = []bson.ObjectID{}
+	}
 	if len(results.Duplicates) > 0 || len(results.MissingData) > 0 || len(results.NotFound) > 0 {
 		errors.ErrInvalidData.WithData(results).Write(w)
 		return
diff --git a/api/processes_publish.go b/api/processes_publish.go
index b0a5eef..d097386 100644
--- a/api/processes_publish.go
+++ b/api/processes_publish.go
@@ -173,13 +173,19 @@ func (a *API) publishPreflightProblems(t publishTarget) (problems []string, ques
 	if census == nil {
 		return problems, questionsMismatch, nil // the checks below need the census
 	}
+	// a certificate census needs the feature on and SP1's nationalId migration applied; publish
+	// answers its own refusal (400 40185, or 409) rather than a 400 listing it
+	if problem, blocked := a.certificateCensusPublishBlock(census); blocked != nil {
+		problems = append(problems, problem)
+		denied = blocked
+	}
 	// a resume must still mint the remaining questions of a partially published anonymous process
 	if a.anonymousCensusRefused(census.Anonymous) && !anyMined(questions) {
 		problems = append(problems, anonymousVotingUnavailable)
 	}
 	orgDoc, err := a.db.Organization(vp.OrgAddress)
 	if err != nil {
-		return append(problems, "organization not found"), questionsMismatch, nil
+		return append(problems, "organization not found"), questionsMismatch, denied
 	}
 	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
 		problems = append(problems, "publishing requires the admin role")
@@ -195,7 +201,7 @@ func (a *API) publishPreflightProblems(t publishTarget) (problems []string, ques
 	// the checks below but fail in the worker (electionParamsForQuestion). Reject it here.
 	if maxSize == 0 {
 		problems = append(problems, "census has no members")
-		return problems, questionsMismatch, nil
+		return problems, questionsMismatch, denied
 	}
 	start := vp.StartDate
 	if start.IsZero() || start.Before(time.Now()) {
@@ -207,7 +213,9 @@ func (a *API) publishPreflightProblems(t publishTarget) (problems []string, ques
 	}
 	if err := a.subscriptions.OrgCanPublishProcess(orgDoc, durationSeconds); err != nil {
 		problems = append(problems, err.Error())
-		denied = err
+		if denied == nil {
+			denied = err
+		}
 	}
 	return problems, questionsMismatch, denied
 }
diff --git a/csp/handlers/auth.go b/csp/handlers/auth.go
index d7a3266..463270b 100644
--- a/csp/handlers/auth.go
+++ b/csp/handlers/auth.go
@@ -314,6 +314,10 @@ func (c *CSPHandlers) authFirstStep(
 		}
 		return nil, errors.ErrGenericInternalServerError.WithErr(err)
 	}
+	// a certificate census signs in only through the certificate endpoints (contract section 2)
+	if census.AuthMethod == db.CensusAuthMethodCertificate {
+		return nil, CertificateCensusError()
+	}
 	// before any budget is claimed or challenge sent: the token could not be blind-signed anyway
 	if c.anonymousCensusRefused(census) {
 		return nil, errors.ErrInvalidData.With(csp.AnonymousVotingUnavailable)
```
Run: `go vet ./...`
Expected: no output
Run (Docker: run by the implementer): `go test -count=1 -run '^(TestCertAuthDisabledCensus|TestCertificateCensusSpec|TestValidateCertificateCensus|TestCertificateCensusPublishGuard|TestAuthZeroCertificateCensus|TestValidateProcessCensus.*|TestProcessCensusMissingData|TestCSPAuthNationalID.*|TestVotingProcessPublish|TestProcessBlindCSPSwitch.*)$' ./api/`
Expected: `ok  	github.com/vocdoni/saas-backend/api` (passed in the trial merge)

Step 3: commit
`git commit -m "feat(processes): add certificate censuses" -- api/apicommon/voting_processes.go api/processes_census.go api/processes_census_validate.go api/processes_publish.go csp/handlers/auth.go api/certauth_census_test.go`

Interfaces this task provides to later tasks: `apicommon.CensusSpec.AuthMethod`; `(*API).censusAuthMethodError(spec)`,
`(*API).certificateCensusPublishBlock(census) (problem string, err error)`; test helpers `certCensus(ids...)`,
`certVoters(t, nationalIDs...) (token, org, ids)`.

### Task 14: End-to-end certificate sign-in against the stack
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `api/certauth_flow_test.go`
Goal: the Docker acceptance suites through the real Mongo, CSP and chain: create (AC6), redeem and token use (AC8),
single use and PKCE (AC9), the two-stage budget (AC11), check-only (AC12), the anonymous census (AC13). No
production code: the behaviour exists since Task 13, so these pass as written; any failure is a bug to fix in the
task that owns the code, never by loosening the test.

Step 1: write the tests
```go
package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// certPKCE returns a fresh verifier and its S256 challenge.
func certPKCE() (verifier, challenge string) {
	verifier = internal.RandomHex(32) // 64 characters of the verifier grammar
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// certCreate starts an attempt on pid and returns the answer and its verifier.
func certCreate(t *testing.T, pid, purpose string) (apicommon.CertAuthCreateResponse, string) {
	t.Helper()
	verifier, challenge := certPKCE()
	created := requestAndParse[apicommon.CertAuthCreateResponse](t, http.MethodPost, "",
		&apicommon.CertAuthCreateRequest{
			Challenge: challenge, ChallengeMethod: "S256", Purpose: purpose,
			ReturnURL: testCertAppOrigin + "/ca/processes/" + pid,
		}, "processes", pid, "auth", "cert")
	return created, verifier
}

// certAuthenticate navigates to authURL on the auth listener presenting cert (nil: none) and returns
// the 303's target and fragment.
func certAuthenticate(t *testing.T, authURL string, cert *tls.Certificate) (string, url.Values) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(testCertPKI.CA)
	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(testCertListenerPort))
			},
			TLSClientConfig: &tls.Config{
				RootCAs: roots, MinVersion: tls.VersionTLS12,
				GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					if cert == nil {
						return &tls.Certificate{}, nil
					}
					return cert, nil
				},
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	resp, err := client.Get(authURL)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, resp.Body.Close(), qt.IsNil)
	qt.Assert(t, resp.StatusCode, qt.Equals, http.StatusSeeOther)
	loc, err := url.Parse(resp.Header.Get("Location"))
	qt.Assert(t, err, qt.IsNil)
	frag, err := url.ParseQuery(loc.Fragment)
	qt.Assert(t, err, qt.IsNil)
	loc.Fragment = ""
	return loc.String(), frag
}

// certSignIn runs create and authenticate with cert, returning the code and verifier to redeem.
func certSignIn(t *testing.T, pid, purpose string, cert *tls.Certificate) (code, verifier string) {
	t.Helper()
	created, verifier := certCreate(t, pid, purpose)
	_, frag := certAuthenticate(t, created.AuthURL, cert)
	qt.Assert(t, frag.Get("state"), qt.Equals, created.State)
	return frag.Get("code"), verifier
}

// certRedeem redeems a code, returning the status and either the 200 answer or the error envelope.
func certRedeem(t *testing.T, pid, code, verifier string,
) (int, *apicommon.CertAuthRedeemResponse, *certErrorBody) {
	t.Helper()
	body, status := testRequest(t, http.MethodPost, "",
		&apicommon.CertAuthRedeemRequest{Code: code, Verifier: verifier}, "processes", pid, "auth", "cert", "redeem")
	if status == http.StatusOK {
		resp := &apicommon.CertAuthRedeemResponse{}
		qt.Assert(t, json.Unmarshal(body, resp), qt.IsNil)
		return status, resp, nil
	}
	failure := &certErrorBody{}
	qt.Assert(t, json.Unmarshal(body, failure), qt.IsNil, qt.Commentf("body: %s", body))
	return status, nil, failure
}

// certErrorBody is a decoded certificate error envelope.
type certErrorBody struct {
	Error string                      `json:"error"`
	Code  int                         `json:"code"`
	Data  apicommon.CertAuthErrorData `json:"data"`
}

// issueCert mints a synthetic certificate for document.
func issueCert(t *testing.T, document string) *tls.Certificate {
	t.Helper()
	cert, err := testCertPKI.Issue(testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: document})
	qt.Assert(t, err, qt.IsNil)
	return cert
}

// TestCertAuthCreate pins the create endpoint against the real stack (AC6): the 200 shape, the
// returnUrl allowlist, 409 on an ended process, 400 on a typed census, and the per-IP limit.
func TestCertAuthCreate(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)

	before := time.Now()
	created, _ := certCreate(t, pid, "vote")
	c.Assert(created.State, qt.Matches, `[A-Za-z0-9_-]{43}`)
	authURL, err := url.Parse(created.AuthURL)
	c.Assert(err, qt.IsNil)
	c.Assert(authURL.Scheme, qt.Equals, "https")
	c.Assert(authURL.Hostname(), qt.Matches, `[a-z2-7]{26}\.`+strings.ReplaceAll(testCertAuthHost, ".", `\.`))
	c.Assert(authURL.Path, qt.Equals, "/v1/authenticate")
	c.Assert(authURL.Query().Get("state"), qt.Equals, created.State)
	c.Assert(strings.HasPrefix(authURL.Hostname(), created.State[:26]), qt.IsFalse) // independent of the state
	expires, err := time.Parse(time.RFC3339, created.ExpiresAt)
	c.Assert(err, qt.IsNil)
	c.Assert(expires.Sub(before) > 14*time.Minute && expires.Sub(before) < 16*time.Minute, qt.IsTrue)
	c.Assert(created.ProbeURL, qt.Equals, "https://probe."+testCertAuthHost+":"+strconv.Itoa(testCertListenerPort)+"/ok")
	again, _ := certCreate(t, pid, "vote")
	c.Assert(again.AuthURL, qt.Not(qt.Equals), created.AuthURL) // a fresh host per attempt

	_, challenge := certPKCE()
	for _, tc := range []struct {
		returnURL string
		ok        bool
	}{
		{testCertAppOrigin + "/", true},
		{testCertAppOrigin + "/es", true},
		{testCertAppOrigin + "/processes/" + pid, true},
		{"https://evil.test/processes/" + pid, false},
		{testCertAppOrigin + "/processes/" + pid + "?x=1", false},
		{testCertAppOrigin + "/processes/" + pid + "#x", false},
		{"https://user@app.test/", false},
		{testCertAppOrigin + "/processes/" + pid + "/results", false},
		{"http://app.test/", false},
	} {
		_, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
			Challenge: challenge, ChallengeMethod: "S256", ReturnURL: tc.returnURL, Purpose: "vote",
		}, "processes", pid, "auth", "cert")
		want := http.StatusBadRequest
		if tc.ok {
			want = http.StatusOK
		}
		c.Assert(status, qt.Equals, want, qt.Commentf("%s", tc.returnURL))
	}

	// a typed census is a 400, an ended process a 409
	typedPid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: ids}, 1)
	_, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "vote",
	}, "processes", typedPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	endedPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	endCertProcess(t, endedPid)
	_, status = testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "vote",
	}, "processes", endedPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusConflict)

	// the 61st create in one UTC minute from one IP answers 429 (stay clear of a minute boundary)
	limitPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	if s := time.Now().UTC().Second(); s > 40 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
	for range 60 {
		certCreate(t, limitPid, "check")
	}
	body, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "check",
	}, "processes", limitPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusTooManyRequests, qt.Commentf("%s", body))
	c.Assert(string(body), qt.Contains, `"reason":"too_many_attempts"`)
	c.Assert(string(body), qt.Contains, `"retryAfter":`)
}

// TestCertAuthRedeem pins the happy path against the real stack (AC8): a member stored as
// "12.345.678-z" redeems a certificate for 12345678Z into an auth-only token that works with check,
// sign-batch and sign-info like an auth/0 one; holder, weight, voterRef and reference are right for
// a DNI and an NIE, and the transaction's chain is gone.
func TestCertAuthRedeem(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12.345.678-z", "x1234567l")
	census := certCensus(ids...)
	census.Weighted = true
	pid, elections := publishCensusProcess(t, token, orgAddress, census, 2)

	code, verifier := certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	status, ok, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
	c.Assert(ok.Purpose, qt.Equals, "vote")
	c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
	c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{1})
	c.Assert(ok.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
		FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
	})
	c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
	tx, err := testDB.CertAuthTransactionByCode(code)
	c.Assert(err, qt.IsNil)
	c.Assert(tx.Status, qt.Equals, db.CertAuthStatusConsumed)
	c.Assert(tx.Chain, qt.HasLen, 0)

	// the token is an auth-only token like auth/0's: check, sign-batch and sign-info take it
	tok := *ok.AuthToken
	auth, err := testDB.CSPAuth(tok)
	c.Assert(err, qt.IsNil)
	c.Assert(auth.Verified, qt.IsTrue)
	c.Assert(auth.UserID.String(), qt.Equals, ids[0])
	check := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok}, "processes", pid, "check")
	c.Assert(check.BelongsToProcess, qt.IsTrue)
	c.Assert(check.Weight, qt.DeepEquals, ok.Weight)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	batch := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", &handlers.SignBatchRequest{
		AuthToken: tok, Ballots: []handlers.SignBatchBallot{
			{UpstreamID: elections[0], Address: voter.Address().Bytes()},
			{UpstreamID: elections[1], Address: voter.Address().Bytes()},
		},
	}, "processes", pid, "sign-batch")
	c.Assert(batch.Signatures, qt.HasLen, 2)
	for _, s := range batch.Signatures {
		c.Assert(s.Code, qt.Equals, "", qt.Commentf("%s", s.Error))
		c.Assert(hex.EncodeToString(s.Weight), qt.Equals, "01")
	}
	info := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: tok}, "processes", pid, "sign-info")
	c.Assert(info.Consumed, qt.HasLen, 2)

	// voterRef is stable for one member, and different for another member and another process
	code, verifier = certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	_, again, _ := certRedeem(t, pid, code, verifier)
	c.Assert(again.VoterRef, qt.Equals, ok.VoterRef)
	nieCert, err := testCertPKI.Issue(testprovider.Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"})
	c.Assert(err, qt.IsNil)
	code, verifier = certSignIn(t, pid, "vote", nieCert)
	_, nie, _ := certRedeem(t, pid, code, verifier)
	c.Assert(nie.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
		FullName: "Jordi Puig Serra", DocumentType: "NIE", DocumentEnding: "567L",
	})
	c.Assert(nie.Weight, qt.DeepEquals, internal.HexBytes{2})
	c.Assert(nie.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
	otherPid, _ := publishCensusProcess(t, token, orgAddress, census, 1)
	code, verifier = certSignIn(t, otherPid, "vote", issueCert(t, "12345678Z"))
	_, other, _ := certRedeem(t, otherPid, code, verifier)
	c.Assert(other.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
}

// TestCertAuthRedeemSingleUse pins single use and PKCE against the real stack (AC9).
func TestCertAuthRedeemSingleUse(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	otherPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	cert := issueCert(t, "12345678Z")

	code, verifier := certSignIn(t, pid, "vote", cert)
	status, _, _ := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK)
	status, _, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusConflict)
	c.Assert(failure.Data.Reason, qt.Equals, "code_used")

	code, verifier = certSignIn(t, pid, "vote", cert)
	wrong, _ := certPKCE()
	status, _, failure = certRedeem(t, pid, code, wrong)
	c.Assert(status, qt.Equals, http.StatusForbidden)
	c.Assert(failure.Data.Reason, qt.Equals, "verifier_mismatch")
	status, _, failure = certRedeem(t, pid, code, verifier) // consumed by the mismatch
	c.Assert(status, qt.Equals, http.StatusConflict)
	c.Assert(failure.Data.Reason, qt.Equals, "code_used")

	// past the code TTL (the transaction aged by hand, as the TTL monitor may lag)
	code, verifier = certSignIn(t, pid, "vote", cert)
	ageCertAuthCode(t, code, 6*time.Minute)
	status, _, failure = certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusGone)
	c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")

	// a code is bound to its process
	code, verifier = certSignIn(t, pid, "vote", cert)
	status, _, failure = certRedeem(t, otherPid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusGone)
	c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
}

// TestCertAuthRedeemBudget pins the two-stage failed-attempt budget against the real stack (AC11).
func TestCertAuthRedeemBudget(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	redeemWith := func(cert any) (int, *certErrorBody) {
		var code, verifier string
		if document, ok := cert.(string); ok {
			code, verifier = certSignIn(t, pid, "vote", issueCert(t, document))
		} else {
			forged, err := testprovider.SelfSigned(cert.(testprovider.Holder))
			c.Assert(err, qt.IsNil)
			code, verifier = certSignIn(t, pid, "vote", forged)
		}
		status, _, failure := certRedeem(t, pid, code, verifier)
		return status, failure
	}

	// ten self-signed certificates naming the member's document do not lock the member out
	for range 10 {
		status, failure := redeemWith(testprovider.Holder{GivenName: "X", Surname: "X", Document: "12345678Z"})
		c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
		c.Assert(failure.Data.Reason, qt.Equals, "untrusted_issuer")
	}
	status, _ := redeemWith("12345678Z")
	c.Assert(status, qt.Equals, http.StatusOK)

	// a verified document outside the census is capped at 10 per hour, another one is not
	for range 10 {
		status, failure := redeemWith("87654321X")
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
	}
	status, failure := redeemWith("87654321X")
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
	c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")
	c.Assert(failure.Data.RetryAfter > 0, qt.IsTrue)
	status, _ = redeemWith("11111111H")
	c.Assert(status, qt.Equals, http.StatusForbidden)

	// revocation outages are never charged: the same certificate keeps answering 503, not 429
	outage, err := testCertPKI.Issue(testprovider.Holder{
		GivenName: "O", Surname: "O", Document: "12345678Z",
		OU: testprovider.OUOutage,
	})
	c.Assert(err, qt.IsNil)
	for range 11 {
		code, verifier := certSignIn(t, pid, "vote", outage)
		status, _, failure := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusServiceUnavailable)
		c.Assert(failure.Data.Reason, qt.Equals, "revocation_unavailable")
	}
	status, _ = redeemWith("12345678Z")
	c.Assert(status, qt.Equals, http.StatusOK)
}

// TestCertAuthCheckOnly pins the check-only redeems against the real stack (AC12): an UPCOMING
// process answers a null token, no weight and no CSP token, for both purposes, and spends budget.
func TestCertAuthCheckOnly(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress) // starts in an hour
	req.Census = certCensus(ids...)
	pid, _ := publishProcessRequest(t, token, req)

	for _, purpose := range []string{"check", "vote"} {
		code, verifier := certSignIn(t, pid, purpose, issueCert(t, "12345678Z"))
		status, ok, failure := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
		c.Assert(ok.Purpose, qt.Equals, purpose)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(ok.Weight, qt.IsNil)
		c.Assert(ok.Holder.DocumentEnding, qt.Equals, "678Z")
		c.Assert(ok.VoterRef, qt.Not(qt.Equals), "")
		c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
	}
	oid := objectID(c, pid)
	_, err := testDB.LastCSPAuth(internal.HexBytesFromString(ids[0]), internal.HexBytes(oid[:]))
	c.Assert(err, qt.ErrorIs, db.ErrTokenNotFound)

	// check redeems spend the budget like votes do: ten unknown documents, then 429
	for range 10 {
		code, verifier := certSignIn(t, pid, "check", issueCert(t, "87654321X"))
		status, _, _ := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
	}
	code, verifier := certSignIn(t, pid, "check", issueCert(t, "87654321X"))
	status, _, _ := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
}

// TestCertAuthBlindCSP pins the anonymous certificate census (AC13): it publishes with the blind key
// and the redeemed token runs blind-point and blind-sign into a vote cast on chain.
func TestCertAuthBlindCSP(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	census := certCensus(ids...)
	census.Anonymous = true
	pid, elections := publishCensusProcess(t, token, orgAddress, census, 1)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", pid)
	c.Assert(got.Census.Anonymous, qt.IsTrue)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)

	code, verifier := certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	status, ok, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	proof := testBlindSignBallot(t, pid, *ok.AuthToken, elections[0], voter.Address().Bytes())
	nullifier := testCastVote(t, testNewVocdoniClient(t), &voter, elections[0], proof, []byte(`{"votes":[0]}`))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)
}

// endCertProcess marks every question of a published process ENDED in storage.
func endCertProcess(t *testing.T, pid string) {
	t.Helper()
	questions, err := testDB.QuestionsByProcess(objectID(qt.New(t), pid))
	qt.Assert(t, err, qt.IsNil)
	for _, q := range questions {
		qt.Assert(t, testDB.SetQuestionStatus(q.ID, db.QuestionStatusEnded), qt.IsNil)
	}
}

// ageCertAuthCode moves a code's authentication back by age, as if it had waited that long.
func ageCertAuthCode(t *testing.T, code string, age time.Duration) {
	t.Helper()
	_, err := testDB.DBClient.Database(testDBName).Collection("certAuthTransactions").UpdateOne(context.Background(),
		bson.M{"code": code}, bson.M{"$set": bson.M{"authenticatedAt": time.Now().Add(-age)}})
	qt.Assert(t, err, qt.IsNil)
}
```
Run: `go vet ./api/`
Expected: no output

Step 2: implement — nothing. Run the suite.
Run (Docker: run by the implementer): `go test -count=1 -timeout 30m -run '^(TestCertAuth.*|TestCertificateCensus.*|TestValidateCertificateCensus|TestAuthZeroCertificateCensus)$' ./api/`
Expected: `ok  	github.com/vocdoni/saas-backend/api` (12 tests, about 4 minutes; passed in the trial merge). TestCertAuthCreate
waits up to 20 s to stay clear of a UTC minute boundary before its 61 creates.

Step 3: commit
`git commit -m "test(api): cover certificate sign-in end to end" -- api/certauth_flow_test.go`

Interfaces this task provides to later tasks: none.

### Task 15: Private module access in CI and Docker
Model: sonnet   Repo: /Users/ferran/Repos/saas-backend   Files: `.github/workflows/main.yml`, `.github/workflows/swag.yml`,
`.github/workflows/integration.yml`, `Dockerfile`, `dev.dockerfile`, `docker-compose.yml`
Goal: every build that resolves modules can fetch the private `github.com/vocdoni/cert-check` with a
`PRIVATE_MODULES_TOKEN` secret, without the token ever reaching a runner's git config or an image layer (spec
Deployment item 4). The secret itself is an ops step needing the owner's OK (Merge and release).

Step 1: no test (configuration). Validate the YAML instead after Step 2.

Step 2: implement
```diff
diff --git a/.github/workflows/main.yml b/.github/workflows/main.yml
index 53dcebb..f2ddc57 100644
--- a/.github/workflows/main.yml
+++ b/.github/workflows/main.yml
@@ -13,6 +13,13 @@ permissions:
 jobs:
   lint:
     runs-on: ${{ github.event.repository.private && 'self-hosted' || 'ubuntu-latest' }}
+    # the private cert-check module: a job-scoped git rewrite with the token, never written to the
+    # runner's git config (the runners may be self-hosted and shared)
+    env:
+      GOPRIVATE: github.com/vocdoni/cert-check
+      GIT_CONFIG_COUNT: 1
+      GIT_CONFIG_KEY_0: url.https://x-access-token:${{ secrets.PRIVATE_MODULES_TOKEN }}@github.com/vocdoni/cert-check.insteadOf
+      GIT_CONFIG_VALUE_0: https://github.com/vocdoni/cert-check
     steps:
       - uses: actions/checkout@v7
       - uses: actions/setup-go@v7
@@ -29,6 +36,13 @@ jobs:
 
   test:
     runs-on: ${{ github.event.repository.private && 'self-hosted' || 'ubuntu-latest' }}
+    # the private cert-check module: a job-scoped git rewrite with the token, never written to the
+    # runner's git config (the runners may be self-hosted and shared)
+    env:
+      GOPRIVATE: github.com/vocdoni/cert-check
+      GIT_CONFIG_COUNT: 1
+      GIT_CONFIG_KEY_0: url.https://x-access-token:${{ secrets.PRIVATE_MODULES_TOKEN }}@github.com/vocdoni/cert-check.insteadOf
+      GIT_CONFIG_VALUE_0: https://github.com/vocdoni/cert-check
     steps:
       - uses: actions/checkout@v7
       - uses: actions/setup-go@v7
@@ -97,6 +111,8 @@ jobs:
           file: ./Dockerfile
           build-args: |
             GOLANG_VERSION=${{ steps.goversion.outputs.version }}
+          secrets: |
+            PRIVATE_MODULES_TOKEN=${{ secrets.PRIVATE_MODULES_TOKEN }}
           platforms: linux/amd64
           push: true
           tags: |
diff --git a/.github/workflows/swag.yml b/.github/workflows/swag.yml
index 95e60d0..2ade864 100644
--- a/.github/workflows/swag.yml
+++ b/.github/workflows/swag.yml
@@ -15,6 +15,13 @@ jobs:
   api-swagger:
     name: Generate swagger.yaml
     runs-on: ${{ github.event.repository.private && 'self-hosted' || 'ubuntu-latest' }}
+    # the private cert-check module: a job-scoped git rewrite with the token, never written to the
+    # runner's git config (the runners may be self-hosted and shared)
+    env:
+      GOPRIVATE: github.com/vocdoni/cert-check
+      GIT_CONFIG_COUNT: 1
+      GIT_CONFIG_KEY_0: url.https://x-access-token:${{ secrets.PRIVATE_MODULES_TOKEN }}@github.com/vocdoni/cert-check.insteadOf
+      GIT_CONFIG_VALUE_0: https://github.com/vocdoni/cert-check
     steps:
       - uses: actions/checkout@v7
       - uses: actions/setup-go@v7
diff --git a/.github/workflows/integration.yml b/.github/workflows/integration.yml
index bc6d7fe..6c8b804 100644
--- a/.github/workflows/integration.yml
+++ b/.github/workflows/integration.yml
@@ -178,6 +178,11 @@ jobs:
       # A pinned vocdoni-node may raise the go directive in go.mod, so the toolchain
       # must be allowed to download. Otherwise the go.mod version is used as is.
       GOTOOLCHAIN: auto
+      # the private cert-check module, as in main.yml
+      GOPRIVATE: github.com/vocdoni/cert-check
+      GIT_CONFIG_COUNT: 1
+      GIT_CONFIG_KEY_0: url.https://x-access-token:${{ secrets.PRIVATE_MODULES_TOKEN }}@github.com/vocdoni/cert-check.insteadOf
+      GIT_CONFIG_VALUE_0: https://github.com/vocdoni/cert-check
     steps:
       - name: Checkout
         uses: actions/checkout@v5
diff --git a/Dockerfile b/Dockerfile
index 8fd3d5b..26610e3 100644
--- a/Dockerfile
+++ b/Dockerfile
@@ -5,7 +5,14 @@ WORKDIR /src
 ENV CGO_ENABLED=1
 RUN go env -w GOCACHE=/go-cache
 COPY . .
-RUN --mount=type=cache,target=/go-cache go mod download
+# the private cert-check module, through a build secret of the same name as the CI secret and the
+# Railway variable; the token never lands in a layer
+ENV GOPRIVATE=github.com/vocdoni/cert-check
+RUN --mount=type=cache,target=/go-cache --mount=type=secret,id=PRIVATE_MODULES_TOKEN \
+    GIT_CONFIG_COUNT=1 \
+    GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/PRIVATE_MODULES_TOKEN)@github.com/vocdoni/cert-check.insteadOf" \
+    GIT_CONFIG_VALUE_0=https://github.com/vocdoni/cert-check \
+    go mod download
 RUN --mount=type=cache,target=/go-cache go run scripts/circuits/main.go
 RUN --mount=type=cache,target=/go-cache go build -o=backend -ldflags="-s -w" cmd/service/main.go
 
diff --git a/dev.dockerfile b/dev.dockerfile
index 823e62c..fc8b786 100644
--- a/dev.dockerfile
+++ b/dev.dockerfile
@@ -13,7 +13,13 @@ RUN apt-get update && \
     rm -rf /var/lib/apt/lists/*
 
 COPY go.mod go.sum ./
-RUN --mount=type=cache,target=/go-cache go mod download
+# the private cert-check module: docker compose passes PRIVATE_MODULES_TOKEN as a build secret
+ENV GOPRIVATE=github.com/vocdoni/cert-check
+RUN --mount=type=cache,target=/go-cache --mount=type=secret,id=PRIVATE_MODULES_TOKEN \
+    GIT_CONFIG_COUNT=1 \
+    GIT_CONFIG_KEY_0="url.https://x-access-token:$(cat /run/secrets/PRIVATE_MODULES_TOKEN)@github.com/vocdoni/cert-check.insteadOf" \
+    GIT_CONFIG_VALUE_0=https://github.com/vocdoni/cert-check \
+    go mod download
 
 COPY . .
 RUN go run scripts/circuits/main.go
diff --git a/docker-compose.yml b/docker-compose.yml
index 62c3658..34e6963 100644
--- a/docker-compose.yml
+++ b/docker-compose.yml
@@ -10,6 +10,7 @@ services:
     build:
       context: ./
       dockerfile: dev.dockerfile
+      secrets: [PRIVATE_MODULES_TOKEN]
       target: api
     image: saas-backend-api
 
@@ -68,6 +69,7 @@ services:
     build:
       context: ./
       dockerfile: dev.dockerfile
+      secrets: [PRIVATE_MODULES_TOKEN]
       target: defaultplan
     image: saas-backend-defaultplan
     env_file:
@@ -84,6 +86,7 @@ services:
     build:
       context: ./
       dockerfile: dev.dockerfile
+      secrets: [PRIVATE_MODULES_TOKEN]
       target: localsmtp
     image: saas-backend-localsmtp
     env_file:
@@ -130,6 +133,7 @@ services:
     build:
       context: ./
       dockerfile: dev.dockerfile
+      secrets: [PRIVATE_MODULES_TOKEN]
       target: fundaccount
     image: saas-backend-fundaccount
     env_file:
@@ -141,3 +145,9 @@ services:
 volumes:
   mongodb: {}
   vocone_run: {}
+
+# the private cert-check module: export PRIVATE_MODULES_TOKEN (a GitHub token that can read
+# vocdoni/cert-check) before building
+secrets:
+  PRIVATE_MODULES_TOKEN:
+    environment: PRIVATE_MODULES_TOKEN
```
Run: `ruby -e 'require "yaml"; %w[main swag integration].each { |f| YAML.load_file(".github/workflows/#{f}.yml") }; YAML.load_file("docker-compose.yml"); puts "ok"'`
Expected: `ok`
Run (Docker: run by the implementer, with a token that can read vocdoni/cert-check in `GH_TOKEN`):
`docker build --target builder --build-arg GOLANG_VERSION=1.27.3 --secret id=PRIVATE_MODULES_TOKEN,env=GH_TOKEN -t saas-backend-sp2-check .`
Expected: the `go mod download` step succeeds; without the secret it fails resolving `github.com/vocdoni/cert-check`

Step 3: commit
`git commit -m "ci: give builds access to the private cert-check module" -- .github/workflows/main.yml .github/workflows/swag.yml .github/workflows/integration.yml Dockerfile dev.dockerfile docker-compose.yml`

Interfaces this task provides to later tasks: the `PRIVATE_MODULES_TOKEN` secret name (CI secret, Docker build
secret id, Railway variable, docker compose environment variable).

### Task 16: Swagger
Model: haiku   Repo: /Users/ferran/Repos/saas-backend   Files: `docs/swagger.yaml`
Goal: the published spec documents the two endpoints, `CensusSpec.authMethod`, the certificate types and the codes
40950 and 40185 (serves AC15).

Step 1: no test (generated file).

Step 2: implement
Run: `PATH=$(go env GOPATH)/bin:$PATH make swagger && git status --short`
Expected: `Swagger documentation generated successfully at docs/swagger.yaml`, and `git status` lists only
` M docs/swagger.yaml` (if `swag fmt` reformats a Go annotation, commit that file in this task too).
Run: `grep -c "/processes/{processId}/auth/cert" docs/swagger.yaml && grep -n "authMethod:" docs/swagger.yaml`
Expected: `2`, and at least one `authMethod:` property under `apicommon.CensusSpec`

Step 3: commit
`git commit -m "docs(swagger): regenerate after certificate sign-in" -- docs/swagger.yaml`

Interfaces this task provides to later tasks: none.

### Task 17: README deployment section
Model: haiku   Repo: /Users/ferran/Repos/saas-backend   Files: `README.md`
Goal: the operator-facing summary of spec "Compatibility and rollout" (variables, origin-mode deployment, cloudflare
mode's production bar, rollback), the source SP6's runbook starts from.

Step 1: no test (documentation).

Step 2: implement (insert before `## CI`):
```diff
diff --git a/README.md b/README.md
index d53ed23..2f3e496 100644
--- a/README.md
+++ b/README.md
@@ -228,6 +228,57 @@ docker compose down -v
 ```
 
 
+## Certificate voter sign-in
+
+Optional. A census with `authMethod: "certificate"` signs its voters in with a qualified Spanish
+certificate (FNMT natural person, idCAT) whose DNI/NIE matches the member's `nationalId`. The
+browser presents the certificate to a second, TLS listener on a fresh host per attempt
+(`<nonce>.<certAuthHost>`), which sends it straight back to the app with a code; the app redeems
+the code on the main API (`POST /processes/{processId}/auth/cert/redeem`), where the certificate
+is verified (chain, profile, revocation) with the private `github.com/vocdoni/cert-check` module.
+The HTTP contract and the design are in `docs/specs/2026-11-04-cert-auth-csp-design.md`.
+
+Everything is off unless `VOCDONI_CERTAUTHMODE` is set: no listener starts, the two endpoints
+answer 40185 and certificate censuses are refused. With it set, the service refuses to start on
+any inconsistent value (see `example.env` for the variables).
+
+| Variable | Meaning |
+|---|---|
+| `VOCDONI_CERTAUTHMODE` | `origin` (this process terminates TLS) or `cloudflare` (not for production yet, see below) |
+| `VOCDONI_CERTAUTHHOST` | auth domain, e.g. `cert.auth-dev.vocdoni.io` |
+| `VOCDONI_CERTAUTHLISTENADDR` / `VOCDONI_CERTAUTHPUBLICPORT` | bind address (`:8443`) and the public port of the URLs (`443`) |
+| `VOCDONI_CERTAUTHTLSCERT` / `VOCDONI_CERTAUTHTLSKEY` | PEM files of a certificate covering `*.<host>`; loaded at startup, so a renewal needs a restart |
+| `VOCDONI_CERTAUTHPROVIDERS` | `fnmt,idcat` |
+| `VOCDONI_CERTAUTHALLOWEDORIGINS` | exact app origins (https; http only for localhost) allowed as return URLs, CORS and probe origins |
+| `VOCDONI_CERTAUTHDEFAULTRETURN` | app URL an expired attempt returns to |
+| `VOCDONI_CERTAUTHCREATESPERMINUTE` | attempts per client IP and process per minute (60) |
+| `VOCDONI_CERTAUTHPROXYPROTOCOL` / `VOCDONI_CERTAUTHPROXYFRONTIPS` | PROXY protocol from a TCP front, honoured only from those CIDRs |
+| `VOCDONI_CERTAUTHCFORIGINPULLCA` / `VOCDONI_CERTAUTHCFALLOWEDIPS` | cloudflare mode: the Authenticated Origin Pulls CA, and optional peer CIDRs |
+| `VOCDONI_CERTAUTHTESTPROVIDER` / `VOCDONI_CERTAUTHTESTPROVIDERCA` | synthetic test CA, refused unless `--vocdoniApi` is localhost, api-dev, api-stg or listed in `VOCDONI_CERTAUTHTESTPROVIDERALLOWEDAPIS` |
+
+Behind a proxy, also set `VOCDONI_ORIGINSECRET`: the create limit then keys on the rightmost
+`X-Forwarded-For` entry, otherwise every voter shares the proxy's address.
+
+Deploying origin mode:
+
+1. DNS: a wildcard `*.<certAuthHost>` record (which covers `probe.<certAuthHost>`) to the auth
+   listener's dedicated IP, DNS-only (grey cloud) or through a TCP passthrough front (Cloudflare
+   Spectrum, a dedicated-IP forwarder; enable the PROXY protocol for it).
+2. A wildcard certificate for `*.<certAuthHost>` (DNS-01), mounted as files.
+3. The public port must be 443 (corporate proxies only allow it): the front forwards 443 to
+   `VOCDONI_CERTAUTHLISTENADDR`.
+4. Builds need access to the private `github.com/vocdoni/cert-check` module: `GOPRIVATE` plus a
+   token (`PRIVATE_MODULES_TOKEN`) in CI and as the Docker build secret of the same name.
+5. Nothing changes for the main API, including behind Cloudflare.
+
+Cloudflare mode (Cloudflare terminates the mTLS and forwards the certificate in headers) is
+implemented but stays out of every deployed environment until its open questions (CA names in the
+CertificateRequest, HTTP/2 coalescing across hosts, forwarding requests without a certificate,
+the mTLS hostname association) are verified.
+
+Rollback: unset `VOCDONI_CERTAUTHMODE` and redeploy. A published certificate census then has no way
+to sign voters in, so do not roll back during a live certificate vote.
+
 ## CI
 
 ### Testing against an unmerged vocdoni-node
```
Run: `grep -n "^## Certificate voter sign-in" README.md`
Expected: one line, just before `## CI`

Step 3: commit
`git commit -m "docs(readme): document certificate sign-in deployment" -- README.md`

Interfaces this task provides to later tasks: none.

## Merge and release

1. Full suites, on the branch rebased onto SP1's final head (Docker):
   - `GOPRIVATE=github.com/vocdoni/* go test -failfast -timeout=40m ./...` → every package `ok` (the api package
     takes the longest; it now also starts the certificate-enabled test API).
   - `make lint` (golangci-lint v2.15.0) → no new issues in the touched files; `./scripts/check-qt-patterns.sh` →
     `No qt anti-patterns found!`; `go mod tidy` → no diff; `PATH=$(go env GOPATH)/bin:$PATH make swagger` → no diff.
   - Re-verify before merging: migration 37 and codes 40185/40950 still free on origin/main and origin/lts
     (`git grep -h "AddMigration(" origin/main -- migrations/`, `git grep -n "4018[5-9]\|4095" origin/main -- errors/`).
2. Merge order: SP1 (`feat/nationalid-normalization` → `main`) first, with migration 36 applied wherever SP2 goes.
   Then one PR `feat/cert-auth-csp` → `main`, opened stacked on the SP1 PR (base `feat/nationalid-normalization`
   until SP1 merges, then retargeted to `main`), title `feat(csp): certificate voter sign-in`, body `Refs
   vocdoni/vocdoni-app#1963`. Description per the owner's convention: opener line `{model} here, controlled by
   ferranrego.`, under ~15 lines, no emojis, no attribution footer: what and why, decisions D1–D13 that a
   reviewer must know (D2, D5, D13 at least), how it was tested, and the docs/SDK impact as suggested follow-ups
   (vocdoni.io `stage`: census `authMethod`, the two endpoints, error 40950 and its reasons; integrator-sdk SP3:
   `createCertAuth`/`redeemCertAuth`, `@vocdoni/api-types` for the new types). Promotion `main` → `stage` → `lts`
   follows the repo's normal flow.
3. Release: no version bump or tag (the service ships through the `main` → `stage` → `lts` promotion PRs). The
   feature ships off: no environment changes behaviour until it sets `VOCDONI_CERTAUTHMODE`.
4. **Owner OK required before:**
   - pushing `feat/cert-auth-csp`;
   - opening the PR (and every later PR, including promotions);
   - creating the `PRIVATE_MODULES_TOKEN` secret (GitHub Actions, Railway build variable) — and deciding D13: a
     public repository that depends on a private module cannot be built by outside contributors or fork PRs;
     the alternatives are publishing cert-check or a build tag that leaves `certauth` out;
   - DNS: the wildcard `*.cert.auth-<env>.vocdoni.io` record (covering `probe.`), DNS-only or Spectrum (open
     question Q1);
   - the DNS-01 wildcard certificate for `*.cert.auth-<env>.vocdoni.io` and how its PEMs reach the container (Q3);
   - the dedicated IP / TCP front on public port 443 (and `VOCDONI_CERTAUTHPROXYPROTOCOL` +
     `VOCDONI_CERTAUTHPROXYFRONTIPS` if the front sends PROXY headers);
   - setting the `VOCDONI_CERTAUTH*` variables in each environment (dev first), with `VOCDONI_ORIGINSECRET` set
     behind any proxy;
   - any use of `VOCDONI_CERTAUTHMODE=cloudflare` (not allowed in any deployed environment until Q2 is verified);
   - enabling `VOCDONI_CERTAUTHTESTPROVIDER` anywhere (dev/e2e only; it refuses production APIs itself).
5. Post-release checks: with the mode unset, the service starts as before and `POST /processes/{id}/auth/cert`
   answers 400 code 40185. With the mode set in dev: the startup log shows `certificate sign-in enabled` with
   providers `fnmt,idcat`; `curl -s https://probe.<host>/ok` answers `{"ok":true}`; `openssl s_client -connect
   <ip>:443 -servername probe.<host>` shows no "Acceptable client certificate CA names", while `-servername
   <26 base32>.<host>` shows the FNMT/idCAT CA names and `Protocol  : TLSv1.2`; a real FNMT or idCAT sign-in by the
   owner on a dev certificate census, then SP6's Playwright run against the synthetic provider.

