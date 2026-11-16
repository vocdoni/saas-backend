# Certificate voter authentication: implementation plan

Spec: docs/specs/2026-11-11-certificate-voter-auth-design.md (saas-backend, branch feat/cert-auth; approved 2026-11-16, D1–D8)
Class: architectural
Repos and branches:
- saas-backend: /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth → feat/cert-auth (base origin/main 05c19b9; spec commits on top)
- cert-gateway (new): /home/lucasmenendez/cert-gateway → main (no base; Task 9 runs `git init -b main`)
- vocdoni-integrator-sdk: /home/lucasmenendez/vocdoni-integrator-sdk → feat/cert-auth (base origin/main 2be5f7e; Task 15 creates the branch)
- vocdoni-app: /home/lucasmenendez/vocdoni-app → feat/cert-auth (base origin/develop 6daf44fb; Task 16 creates the branch; the local `develop` is behind, never build on it)

## Spec issues found

1. **AC2 and "Compatibility" use an invalid DNI.** The examples `1234567-z` ↔ `01234567Z` and `1234567z` ↔ `01234567-Z`
   use the number 1234567 with the letter Z, but under C2.2's own rule its check letter is L (1234567 mod 23 = 19 → `L`).
   `CanonicalNationalID("01234567Z")` therefore returns `ok=false`, and a test written as AC2 says would fail. Evidence:
   `python3 -c "print('TRWAGMYFPDXBNJZSQVHLCKE'[1234567%23])"` prints `L`. The plan keeps the intent with the valid
   spelling: typed `1234567-l` matches stored `01234567L` (Task 1). AC1's `001234567Z` is tested as an invalid-letter
   case (`ok=false`) and `001234567L` as the extra-leading-zero case (`01234567L`). No behaviour changes.
2. **Local development cannot post to an `http` backend.** C1 "Backends" failed startup on a `url` that is not `https`,
   and D8's only exception was for `origins`, while the local saas-backend serves plain HTTP on :8080. Resolved by owner
   ruling (2026-11-16; spec D8 "Stack" amended at 748a3d7): a devca binary also accepts a backend `url` of exactly
   `http://localhost:<port>`, through the same `allowLocalhost` switch (`const devCA`, set by the build tag) as the
   origin exception, and a binary without the tag rejects it at startup (Task 10 `TestLocalhostBackendURL`, Task 12
   `TestProductionBuildHasNoDevCA`, Task 13 `TestDevCABuildConfig`). The compose gateway runs with
   `network_mode: "service:api"`, so the backend really is at `http://localhost:8080` (Task 14). No Caddy, no mkcert
   certificate for the backend.
3. **Variable name.** The spec names the backend variable `CERT_GATEWAY_PUBLIC_KEYS`; saas-backend reads every setting
   through Viper with the `VOCDONI_` prefix (`cmd/service/main.go:38-42`). The plan uses flag `cert_gateway_public_keys`,
   read from `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS`, exactly as `disable_blindcsp` ⇄ `VOCDONI_DISABLE_BLINDCSP`
   (`cmd/service/main.go:32-34`), and documents that name in `example.env` and the gateway README.
4. **"Compare in constant time" (C2.4 step 3).** An Ed25519 signature is verified, not compared; there is no secret to
   leak. The plan uses `ed25519.Verify`; the constant-time comparison applies where a secret-derived value is compared,
   the PKCE check of C2.5 (`subtle.ConstantTimeCompare`).
5. **AC12's devca tests in CI.** C1 said "The Dockerfile and CI never pass the tag". Resolved by owner ruling
   (2026-11-16; spec C1 amended at 748a3d7): CI runs the untagged suite (AC12's "built without the tag" checks) and, as
   a separate test-only step, `go vet -tags devca ./... && go test -race -tags devca ./...` (Task 13's end-to-end test
   included); it builds no image. The Dockerfile and every image build stay untagged (Task 14).
6. **C2.6 "They keep logging in with the exact spelling stored" is wrong after C2.2.** A login hashes the
   canonical form, so a skipped member stored under a non-canonical spelling cannot log in at all. The plan
   documents the real effect (Task 3, "Merge and release" 4.9). No code changes. Resolved: spec C2.6 amended at
   748a3d7.

## Details the spec leaves to the plan (resolved from the code)

- **Migrations.** origin/main's highest migration is 0036, origin/lts's is 0033 (`git ls-tree origin/main migrations/`,
  `git ls-tree origin/lts migrations/`; no remote branch adds 0037+). New migrations go on main above 0036, which keeps lts
  below main as `migrations/repair_login_hashes.go:16-26` requires: **0037 `canonical_national_id_hashes`** (C2.6) and
  **0038 `cert_auth_assertions_indexes`** (C2.4's unique `codeHash` and TTL `expiresAt` indexes; every index in this repo is
  created by a migration, e.g. `migrations/0029_csp_counters_ttl_index.go`).
- **"MissingLoginData gains that test when called for a cert census"** is `func (c *Census) MemberMissingLoginData(m
  *OrgMember) bool` in `db/census_missing_data.go`: `m.MissingLoginData(c.AuthFields, c.TwoFaFields)` plus, for `CertAuth`,
  `!ok` of `internal.CanonicalNationalID`. The three census call sites (`db/census_missing_data.go:135`,
  `db/census_participant.go:546`, `db/org_members.go:613`) call it; `MissingLoginData`'s signature does not change (its
  fourth caller, typed step 0 at `csp/handlers/auth.go:358`, never handles a cert census).
- **`CheckGroupMembersFields`, `CheckMembersFields`, `aggregateMemberFields`** gain a trailing `certAuth bool` parameter, as
  the spec says; existing test calls pass `false`.
- **Assertion route.** Constant `certAuthAssertionsEndpoint = "/cert-auth/assertions"` in `api/routes.go`, registered in
  the **public** chi group (`api/api.go:540`) as `cspHandlers.CertAssertionHandler`. It keeps every global middleware:
  `requireOriginSecret` (the gateway calls the public Cloudflare hostname, which adds `X-Origin-Secret`), throttling, the
  45 s timeout, and the public group's `limitBody`; the handler bounds its own body to 4 KiB with `http.MaxBytesReader`
  and authenticates with the signature (no JWT). The handler lives in `csp/handlers/cert_auth.go` (it needs the CSP, the
  storage and `anonymousCensusRefused`).
- **Keys at runtime.** `csp.Config.CertGatewayKeys` (parsed in `main.go` with `csp.ParseCertGatewayKeys`) is stored in an
  `atomic.Pointer` on `csp.CSP` with `CertGatewayKeys()`, `SetCertGatewayKeys()` and `CertAuthEnabled()`, mirroring
  `BlindCSPDisabled`/`SetBlindCSPDisabled` (`csp/csp.go:122-130`), so API tests can turn the feature off and on.
- **Rate counter.** New kind `db.CSPCounterCertAssertions = "certAssertions"` with an hourly window
  (`db.CSPCounterWindow`), claimed by `(*csp.CSP).ClaimCertAssertion(processID, memberID)` with the key
  `counterKey(memberID, processID)` and cap `csp.MaxHourlyCertAssertions = 10`, through `ClaimCSPCounter`.
- **Step 0 split.** `authFirstStep` returns `(token, voterRef, err)`; after the census and organisation are loaded it calls
  `certAuthFirstStep` for a `CertAuth` census, refuses a `certCode` on any other census, and otherwise runs the existing
  body, moved unchanged into `typedAuthFirstStep`.
- **Gateway:** global bucket burst = per-minute / 10 (six seconds' worth; 120 at the default); `ACME_CA` has no default
  and is required in ACME mode; the auth listener serves `/v1/auth` only, other paths 404, other methods 405; a backend 5xx
  carries no JSON body. A dev image `dev/Dockerfile.devca` (never deployed) builds the devca binary for compose; the
  production `Dockerfile` never passes the tag.
- **vocdoni-app:** the certificate flow state lives in `CspAuthProvider` (`certReturn`); the gateway answer is handled once
  per session by `CertAuthReturnDialog`, mounted in `CspAuthSession` (several Identify buttons share one session, so the
  modal itself cannot own it). The organiser option is a `certAuth` checkbox card in `VoterAuthForm`.

## Global Constraints

- **Never sign commits.** Every commit, merge, cherry-pick, revert or rebase runs with `git -c commit.gpgsign=false …`;
  `~/.gitconfig` sets `commit.gpgsign=true`, so never rely on the config and never change it. Commit with explicit paths, staging them first (a new file is not known to git until it is added; never use
  `git add -A` or `git add .`):
  `git add -- <paths> && git -c commit.gpgsign=false commit -m "<type(scope): lowercase summary>" -- <paths>`.
- Conventional commits, `type(scope): lowercase summary`; no AI attribution, no `Co-Authored-By` trailers.
- No pushes, tags, deploys, DNS changes, GitHub repository creation, npm publishing, Railway changes or key generation for
  real environments. Those are owner-OK steps in "Merge and release".
- No PII (DNI/NIE, names, certificate subject or serial, email, phone) in logs, error messages, URLs or browser storage. The
  only browser storage the flow adds is `sessionStorage["certAuth:<processId>"] = {state, codeVerifier}`.
- Build output and scratch files under `~/.cache/<project>/` (e.g. `~/.cache/cert-gateway/bin`), never `/tmp`, never in a
  repository. Go tests use `t.TempDir()`, which is removed when the test ends; run them with
  `TMPDIR=$HOME/.cache/<project>/tmp` (create it first) so nothing lands in `/tmp`.
- saas-backend never imports `github.com/vocdoni/cert-check`, and adds no dependency (stdlib `crypto/ed25519`,
  `crypto/sha256` only).
- cert-gateway needs the private module: `export GOPRIVATE=github.com/vocdoni/cert-check` and git credentials that can read
  it, for every `go` command in that repo.
- Toolchains: Go 1.27 (`go 1.27.3` saas-backend, `go 1.27` cert-gateway); node 22/24 with pnpm (sdk: pnpm 11, app: pnpm
  10). saas-backend tests need Docker (testcontainers Mongo + vocone); the `api` package is slow, so always narrow with
  `-run` while iterating.
- saas-backend rules (AGENTS.md): errors with `fmt.Errorf` and `%w`, lowercase log/error messages, structured logs,
  `gofumpt`, line length 130, quicktest matchers (`./scripts/check-qt-patterns.sh`), run `make swagger` after API changes
  (with `$(go env GOPATH)/bin` on `PATH`).
- vocdoni-app rules (AGENTS.md): no hard-coded user-facing strings (i18next with `defaultValue`), theme tokens only (no raw
  colours or px), Prettier style (no semicolons, single quotes), `pnpm lint` (tsc + prettier + knip) and `pnpm test`.
- integrator-sdk rules (AGENTS.md): changes to publishable packages carry a changeset; never edit versions or changelogs by
  hand; public API changes update the skill (`skills/vocdoni-integrator-sdk/`).
- Behaviour that must not change: typed logins of non-cert censuses (except the intended leniency of a canonical
  DNI/NIE), step 1, resend, sign, blind-sign, the token binding to member and process, and every existing API response
  shape (new fields are additive and `omitempty`).
- Shared signature test vectors (defined in Task 4, reused verbatim in Task 9): seed = bytes `0x00..0x1f`
  (`AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=`), public key `A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=`,
  keyId `test-1`, t `1767225600`, the body and header in the tests. Never use them outside tests and local development.
- Scratch verification of this plan ran in `/home/lucasmenendez/.cache/saas-cert/scratch/` (detached worktrees, since
  removed); implementers work in the repos above.

## Review Focus

- Signature verification: `csp/cert_gateway.go:71` (`VerifyCertGatewaySignature`: header parse, ±60 s, key lookup,
  `ed25519.Verify` over the literal method/path and `hex(sha256(body))`) and `csp/cert_gateway.go:37` (key parsing).
- Assertion endpoint: `csp/handlers/cert_auth.go:128-251` (order: feature-off 404 → body bound → signature → JSON →
  process published → `CertAuth` → anonymous → canonical DNI → participant → weight → rate budget → store); the response
  contract statuses; no DNI in logs or storage.
- Assertion storage and consumption: `db/cert_auth_assertions.go:51` (atomic `FindOneAndUpdate` filter `codeHash`,
  `processId`, `usedAt: null`, `expiresAt > now`), migration `migrations/0038_cert_auth_assertions_indexes.go`.
- Step 0 with a code: `csp/handlers/cert_auth.go:257-351` (shape check before consumption, PKCE in constant time, census
  match, budget keyed by the participant's `loginHash`, contact check, weight, `voterRef`), and the split in
  `csp/handlers/auth.go:306-327`.
- Hash parity: `internal/national_id.go:23`, `db/census_missing_data.go:67,88`, `db/types.go:471`,
  `db/census_participant.go:860`, mirrored by `migrations/login_hash.go:57` and `migrations/repair_login_hashes.go:418`;
  migration `migrations/0037_canonical_national_id_hashes.go` (version number per the ledger rule).
- Census validation: `api/processes_census.go:47` (`certAuthSpecError`), `api/processes_census_validate.go`.
- Gateway auth handler: `internal/gateway/gateway.go:109` (parameter and `returnUrl` validation), `:147` (order: path,
  method, `CheckHost`, per-IP, params, global bucket), `:188` (cert-check, logging), `:235` (backend post, answer
  mapping), `:217` (fragment redirect); `internal/gateway/config.go:145,215` (BACKENDS validation, canonical origins);
  `internal/gateway/signing.go:31`; `internal/certs/certs.go:192` (SNI guard before certmagic).
- devca isolation: `internal/devca/devca.go` (build tag), `cmd/cert-gateway/providers_prod.go` / `providers_devca.go`.
- Voter flow: `src/components/Process/CSP/cert-auth.ts:57,87` (state/verifier storage, fragment parsing, state check),
  `src/components/Process/CSP/CertAuth.tsx:42,208,284`, `src/components/Process/CSP/CSPStepsProvider.tsx:73`.
- react-providers owner binding: `packages/react-providers/src/election/use-election-auth.ts:168`.

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 Canonical national ID + hash parity | – | saas-backend `internal/`, `db/`, `migrations/` | 9–14 |
| 2 Census `CertAuth`, `LoginHashFields`, missing data | 1 | saas-backend `db/`, `migrations/`, `api/processes_census_validate.go` | 9–14 |
| 3 Migration 0037 | 1, 2 | saas-backend `migrations/`, `db/` test | 9–14 |
| 4 Gateway signature verification + keys (shared vectors) | – | saas-backend `csp/`, `cmd/service/`, `example.env` | 9–14 |
| 5 API census field and validation | 2, 4 | saas-backend `api/` | 9–14 |
| 6 Assertion storage, counter, endpoint | 4, 5 | saas-backend `db/`, `migrations/`, `csp/`, `api/` | 9–14 |
| 7 Step 0 with certCode, PKCE, voterRef | 6 | saas-backend `csp/handlers/`, `api/` test | 9–14 |
| 8 Swagger, full suite, lint | 1–7 | saas-backend `docs/swagger.yaml` | 9–14 |
| 9 cert-gateway repo + signing (shared vectors) | 4 (vectors) | cert-gateway | 1–3, 5–8 |
| 10 Gateway configuration | 9 | cert-gateway `internal/gateway/config*` | 1–8 |
| 11 Auth handler, limits, pages, server, /readyz | 10 | cert-gateway `internal/gateway`, `internal/testpki` | 1–8 |
| 12 Certificates (certmagic/static) + main | 11 | cert-gateway `internal/certs`, `cmd/` | 1–8 |
| 13 devca provider, script, tagged test | 12 | cert-gateway `internal/devca`, `cmd/`, `scripts/` | 1–8 |
| 14 Dockerfile, CI, compose, Makefile, README | 13 | cert-gateway | 1–8 |
| 15 SDK types + react-providers voterRef + changeset | 7 (API shape) | vocdoni-integrator-sdk | 8–14 |
| 16 App: bump SDK packages | 15 **and its npm release (owner OK)** | vocdoni-app `package.json`, lockfile | 1–14 |
| 17 App: env + organiser option | 16 | vocdoni-app `src/app-env*`, `src/components/Process/Create/`, `.env.example`, `README.md` | – |
| 18 App: voter certificate flow | 16, 17 (env) | vocdoni-app `src/components/Process/CSP/` | – |
| 19 App: i18n en/es/ca (+ English elsewhere) | 17, 18 | vocdoni-app `src/i18n/locales/` | – |

Tasks of one repository share one working tree: run them one at a time, in number order.

Parts A (saas-backend) and B (cert-gateway) touch disjoint repos and run in parallel once Task 4 has fixed the shared
vectors. Part C waits for the backend wire shape (Task 7). Part D waits for the npm release of Part C, which needs the
owner's OK (see "Merge and release"); its tasks need the new `@vocdoni/api-types` to type-check.

## Tasks

## Part A — saas-backend (C2)

### Task 1: Canonical national ID and hash parity
Model: strong   Repo: saas-backend (`/home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth`, branch feat/cert-auth)   Files: `internal/national_id.go` (new), `internal/national_id_test.go` (new), `db/census_missing_data.go`, `db/types.go`, `migrations/login_hash.go`, `db/national_id_hash_test.go` (new)
Goal: C2.2 — `internal.CanonicalNationalID` and canonical hashing of `nationalId` on both the login path and the repair tooling (AC1; AC2's typed-vs-stored and parity parts).

Step 0: check the branch: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && git status -sb` → `## feat/cert-auth` and a clean tree.

Step 1: write the failing tests
Create `internal/national_id_test.go`:

```go
package internal

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestCanonicalNationalID(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		ok             bool
	}{
		{"canonical DNI", "12345678Z", "12345678Z", true},
		{"lowercase letter", "12345678z", "12345678Z", true},
		{"spaces", " 1234 5678 z ", "12345678Z", true},
		{"tab and no-break space", "12345678\tZ\u00a0", "12345678Z", true},
		{"dots and hyphen", "12.345.678-Z", "12345678Z", true},
		{"underscore and slash", "12_345/678Z", "12345678Z", true},
		{"short digits padded", "1234567L", "01234567L", true},
		{"short digits with separator", "1234567-l", "01234567L", true},
		{"extra leading zero", "001234567L", "01234567L", true},
		{"extra leading zero, wrong letter", "001234567Z", "001234567Z", false},
		{"one digit", "1R", "00000001R", true},
		{"NIE", "X1234567L", "X1234567L", true},
		{"NIE extra leading zero", "X01234567L", "X1234567L", true},
		{"NIE short digits", "y-123-e", "Y0000123E", true},
		{"NIE with Z prefix", "Z1234567R", "Z1234567R", true},
		{"invalid letter", "12345678A", "12345678A", false},
		{"invalid NIE letter", "X1234567A", "X1234567A", false},
		{"too many digits", "123456789Z", "123456789Z", false},
		{"foreign ID", " P1234567 ", "P1234567", false},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := CanonicalNationalID(tc.in)
			qt.Assert(t, got, qt.Equals, tc.want)
			qt.Assert(t, ok, qt.Equals, tc.ok)
		})
	}
}
```
Create `db/national_id_hash_test.go`:

```go
package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestNationalIDLoginHashIsCanonical: a typed national ID matches the stored one whatever its
// formatting, as long as both are a valid DNI or NIE; anything else still hashes as typed.
func TestNationalIDLoginHashIsCanonical(t *testing.T) {
	c := qt.New(t)
	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	hash := func(id string) []byte { return HashAuthTwoFaFields(OrgMember{NationalID: id}, auth, nil) }

	c.Assert(hash("1234567-l"), qt.DeepEquals, hash("01234567L"))
	c.Assert(hash(" x-01234567 l"), qt.DeepEquals, hash("X1234567L"))
	c.Assert(hash("12.345.678-z"), qt.DeepEquals, hash("12345678Z"))
	// not a valid DNI/NIE: compared as before, case-insensitively only
	c.Assert(hash("DNI002"), qt.DeepEquals, hash("dni002"))
	c.Assert(hash("1234567-z"), qt.Not(qt.DeepEquals), hash("01234567Z"))
}

// TestRepairCanonicalizesNationalID: the repair tooling hashes the national ID exactly as the
// login path does, so a participant stored under a non-canonical spelling is rehashed to the hash a
// canonical login computes.
func TestRepairCanonicalizesNationalID(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, CreatedAt: time.Now()}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	member := &OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: "1234567-l", CreatedAt: time.Now()}
	seedLegacyParticipant(t, censusID, member, unfoldedHash(*member, auth, nil))

	report := runRepair(t, migrations.RepairOptions{Apply: true})
	c.Assert(report.ParticipantsRehashed, qt.Equals, 1)

	var got CensusParticipant
	c.Assert(testDB.censusParticipants.FindOne(context.Background(),
		bson.M{"participantID": member.ID.Hex(), "censusId": censusID}).Decode(&got), qt.IsNil)
	c.Assert(got.LoginHash, qt.DeepEquals, HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, auth, nil))
	// the stored value is not rewritten
	stored, err := testDB.OrgMember(testOrgAddress, member.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(stored.NationalID, qt.Equals, "1234567-l")
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./internal/ -run TestCanonicalNationalID`
Expected: `FAIL	github.com/vocdoni/saas-backend/internal [build failed]` (`undefined: CanonicalNationalID`)

Step 2: implement
Create `internal/national_id.go`:

```go
package internal

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// nationalIDCheckLetters are the DNI/NIE check letters, indexed by the number modulo 23.
const nationalIDCheckLetters = "TRWAGMYFPDXBNJZSQVHLCKE"

var (
	dniPattern = regexp.MustCompile(`^0*([0-9]{1,8})([A-Z])$`)
	niePattern = regexp.MustCompile(`^([XYZ])0*([0-9]{1,7})([A-Z])$`)
)

// CanonicalNationalID returns the canonical form of a Spanish DNI or NIE: uppercase, without
// separators, the DNI number left-padded to 8 digits and the NIE number to 7, as cert-check reports
// the document number of a certificate. It ignores case, whitespace (unicode.IsSpace), '.', '-', '_'
// and '/', and missing or extra leading zeros. ok is false, and the input is returned trimmed but
// otherwise unchanged, when the value is not a DNI or NIE with a valid check letter.
func CanonicalNationalID(s string) (canonical string, ok bool) {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune(".-_/", r) {
			return -1
		}
		return r
	}, strings.ToUpper(s))
	if m := dniPattern.FindStringSubmatch(cleaned); m != nil {
		if checkLetterMatches(m[1], m[2]) {
			return leftPad(m[1], 8) + m[2], true
		}
	} else if m := niePattern.FindStringSubmatch(cleaned); m != nil {
		if checkLetterMatches(strconv.Itoa(strings.IndexByte("XYZ", m[1][0]))+leftPad(m[2], 7), m[3]) {
			return m[1] + leftPad(m[2], 7) + m[3], true
		}
	}
	return strings.TrimSpace(s), false
}

// checkLetterMatches reports whether letter is the check letter of the decimal number digits.
func checkLetterMatches(digits, letter string) bool {
	n, err := strconv.Atoi(digits)
	return err == nil && nationalIDCheckLetters[n%23] == letter[0]
}

// leftPad pads digits with zeros on the left up to width.
func leftPad(digits string, width int) string {
	return strings.Repeat("0", width-len(digits)) + digits
}
```
Edit `db/census_missing_data.go`, replace:

```go
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
```

with:

```go
	"strings"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
```
Edit `db/census_missing_data.go`, replace:

```go
	case OrgMemberAuthFieldsNationalID:
		return m.NationalID, true
```

with:

```go
	case OrgMemberAuthFieldsNationalID:
		// a valid DNI/NIE is compared in its canonical form, so its formatting does not matter
		nationalID, _ := internal.CanonicalNationalID(m.NationalID)
		return nationalID, true
```
Edit `db/types.go`, replace:

```go
// The password auth field is not part of the hash (memberAuthFieldValue reports it unknown): it is
```

with:

```go
// The national ID is hashed in its canonical DNI/NIE form when it is a valid one (see
// memberAuthFieldValue and internal.CanonicalNationalID), so its formatting does not matter.
//
// The password auth field is not part of the hash (memberAuthFieldValue reports it unknown): it is
```
Edit `migrations/login_hash.go`, replace:

```go
		case "nationalId":
			fields[field] = strings.ToLower(m.NationalID)
```

with:

```go
		case "nationalId":
			nationalID, _ := internal.CanonicalNationalID(m.NationalID)
			fields[field] = strings.ToLower(nationalID)
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./internal/ -run TestCanonicalNationalID && go test ./db/ -count=1 -run 'TestNationalIDLoginHashIsCanonical|TestRepairCanonicalizesNationalID|TestRepair' && go test ./db/ ./migrations/ -count=1`
Expected: `ok  	github.com/vocdoni/saas-backend/internal`, then `ok  	github.com/vocdoni/saas-backend/db` (twice) and `ok  	github.com/vocdoni/saas-backend/migrations` (`TestRepairMatchesCanonicalHash` keeps passing: its fixtures use non-DNI values such as `DnI001`).

Step 3: commit
`git add -- internal/national_id.go internal/national_id_test.go db/census_missing_data.go db/types.go migrations/login_hash.go db/national_id_hash_test.go && git -c commit.gpgsign=false commit -m "feat(csp): hash the national id in its canonical dni/nie form" -- internal/national_id.go internal/national_id_test.go db/census_missing_data.go db/types.go migrations/login_hash.go db/national_id_hash_test.go`

Interfaces this task provides to later tasks: `internal.CanonicalNationalID(s string) (canonical string, ok bool)`; `db.HashAuthTwoFaFields` and `migrations.hashMemberFields` hash the canonical `nationalId`.

### Task 2: Census `CertAuth`, `LoginHashFields` and missing data for a cert census
Model: strong   Repo: saas-backend   Files: `db/types.go`, `db/census_participant.go`, `db/census_missing_data.go`, `db/org_members.go`, `db/org_members_groups.go`, `api/processes_census_validate.go`, `migrations/login_hash.go`, `migrations/repair_login_hashes.go`, `db/census_missing_data_test.go`, `db/org_members_groups_test.go`, `db/census_cert_auth_test.go` (new)
Goal: C2.1 (db field), C2.3 — a cert census hashes `(["nationalId"], nil)` everywhere, the repair tooling mirrors it, and an invalid DNI/NIE is missing data (AC2 cert-census parity and dry run; AC3's db half).

Step 1: write the failing test
Create `db/census_cert_auth_test.go`:

```go
package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	certAuthFields = OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	certTwoFaCases = []OrgMemberTwoFaFields{
		{},
		{OrgMemberTwoFaFieldEmail},
		{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone},
	}
)

// TestCertCensusLoginHashFields: a certificate census hashes the national ID alone, whatever its
// 2FA fields, and stores no email or phone variant.
func TestCertCensusLoginHashFields(t *testing.T) {
	c := qt.New(t)
	member := OrgMember{NationalID: "1234567-l", Email: "a@example.com", Phone: HashedPhone("hashed")}
	for _, twoFa := range certTwoFaCases {
		census := Census{AuthFields: certAuthFields, TwoFaFields: twoFa, CertAuth: true}
		auth, gotTwoFa := census.LoginHashFields()
		c.Assert(auth, qt.DeepEquals, certAuthFields)
		c.Assert(gotTwoFa, qt.IsNil)
		c.Assert(calculateParticipantHashes(census, member), qt.DeepEquals, map[string][]byte{
			"loginHash": HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, certAuthFields, nil),
		})
	}
	// a census without CertAuth is unchanged
	plain := Census{AuthFields: certAuthFields, TwoFaFields: certTwoFaCases[2]}
	auth, twoFa := plain.LoginHashFields()
	c.Assert(auth, qt.DeepEquals, plain.AuthFields)
	c.Assert(twoFa, qt.DeepEquals, plain.TwoFaFields)
}

// TestRepairMatchesCanonicalHashCertCensus extends TestRepairMatchesCanonicalHash to certificate
// censuses: the repair tooling's computeParticipantHashes and calculateParticipantHashes agree, and
// a census built by the service needs no repair.
func TestRepairMatchesCanonicalHashCertCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	org := &Organization{Address: testOrgAddress, CreatedAt: time.Now(), Country: "ES"}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	phone, err := NewHashedPhone(testPlaintextPhone, org)
	c.Assert(err, qt.IsNil)

	type seeded struct {
		memberID bson.ObjectID
		censusID string
		census   Census
	}
	var all []seeded
	for i, twoFa := range certTwoFaCases {
		census := &Census{
			OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: twoFa,
			CertAuth: true, CreatedAt: time.Now(),
		}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		member := &OrgMember{
			ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: []string{"1234567-l", "x-1234567 l", "12.345.678-z"}[i],
			Email: "person@example.com", Phone: phone, CreatedAt: time.Now(),
		}
		// stored as the code before this change would have: the raw value, every 2FA field hashed too
		seedLegacyParticipant(t, censusID, member, unfoldedHash(*member, certAuthFields, twoFa))
		all = append(all, seeded{memberID: member.ID, censusID: censusID, census: *census})
	}

	report := runRepair(t, migrations.RepairOptions{Apply: true})
	c.Assert(report.ParticipantsRehashed, qt.Equals, len(certTwoFaCases))

	for _, s := range all {
		stored, err := testDB.OrgMember(testOrgAddress, s.memberID.Hex())
		c.Assert(err, qt.IsNil)
		var got CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(context.Background(),
			bson.M{"participantID": s.memberID.Hex(), "censusId": s.censusID}).Decode(&got), qt.IsNil)
		want := calculateParticipantHashes(s.census, *stored)
		c.Assert(got.LoginHash, qt.DeepEquals, want["loginHash"], qt.Commentf("twoFa=%v", s.census.TwoFaFields))
		c.Assert(got.LoginHashEmail, qt.HasLen, 0)
		c.Assert(got.LoginHashPhone, qt.HasLen, 0)
		found, err := testDB.CensusParticipantByLoginHash(s.census, *stored)
		c.Assert(err, qt.IsNil)
		c.Assert(found.ParticipantID, qt.Equals, s.memberID.Hex())
	}

	// a cert census with email+phone 2FA built by the service: a dry run finds nothing to rehash
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(org), qt.IsNil)
	census := &Census{
		OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: certTwoFaCases[2],
		CertAuth: true, CreatedAt: time.Now(),
	}
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	var ids []string
	for _, id := range []string{"12345678Z", "1234567-l"} {
		member := &OrgMember{OrgAddress: testOrgAddress, NationalID: id, Email: "p@example.com", PlaintextPhone: testPlaintextPhone}
		memberID, err := testDB.SetOrgMember("test_salt", member)
		c.Assert(err, qt.IsNil)
		ids = append(ids, memberID)
	}
	added, missing, err := testDB.PopulateMembersCensus(census, ids)
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, int64(2))
	c.Assert(missing, qt.HasLen, 0)
	dry := runRepair(t, migrations.RepairOptions{})
	c.Assert(dry.ParticipantsScanned, qt.Equals, 2)
	c.Assert(dry.ParticipantsRehashed, qt.Equals, 0)
}

// TestCertCensusMissingData: in a certificate census a member whose national ID is not a valid DNI
// or NIE is missing data, like a member without one; the 2FA channel is still required; and members
// whose national IDs differ only in formatting are duplicates.
func TestCertCensusMissingData(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)

	add := func(nationalID, email string) string {
		id, err := testDB.SetOrgMember("test_salt", &OrgMember{OrgAddress: testOrgAddress, NationalID: nationalID, Email: email})
		c.Assert(err, qt.IsNil)
		return id
	}
	valid := add("X1234567L", "valid@example.com")
	invalid := add("12345678A", "invalid@example.com")
	foreign := add("P1234567", "foreign@example.com")
	noEmail := add("Y0000123E", "")
	twinA := add("12345678Z", "a@example.com")
	twinB := add("12.345.678-z", "b@example.com")
	ids := []string{valid, invalid, foreign, noEmail, twinA, twinB}
	emailOnly := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}

	hexes := func(oids []bson.ObjectID) []string {
		out := make([]string, 0, len(oids))
		for _, id := range oids {
			out = append(out, id.Hex())
		}
		return out
	}

	res, err := testDB.CheckMembersFields(testOrgAddress, ids, certAuthFields, emailOnly, true)
	c.Assert(err, qt.IsNil)
	c.Assert(hexes(res.MissingData), qt.ContentEquals, []string{invalid, foreign, noEmail})
	c.Assert(hexes(res.Duplicates), qt.ContentEquals, []string{twinA, twinB})
	c.Assert(hexes(res.Members), qt.DeepEquals, []string{valid})

	// the same members in a census without CertAuth: any national ID counts, and the formatting
	// twins are duplicates there too now that the hash is canonical
	res, err = testDB.CheckMembersFields(testOrgAddress, ids, certAuthFields, emailOnly, false)
	c.Assert(err, qt.IsNil)
	c.Assert(hexes(res.MissingData), qt.DeepEquals, []string{noEmail})

	// the census build leaves the invalid ones out and reports them
	census := &Census{OrgAddress: testOrgAddress, AuthFields: certAuthFields, TwoFaFields: emailOnly, CertAuth: true}
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	added, missing, err := testDB.PopulateMembersCensus(census, []string{valid, invalid, foreign, noEmail})
	c.Assert(err, qt.IsNil)
	c.Assert(added, qt.Equals, int64(1))
	c.Assert(hexes(missing), qt.ContentEquals, []string{invalid, foreign, noEmail})
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./db/ -run TestCert`
Expected: `FAIL	github.com/vocdoni/saas-backend/db [build failed]` (`unknown field CertAuth in struct literal of type Census`)

Step 2: implement
Edit `db/types.go`, replace:

```go
	AuthFields  OrgMemberAuthFields  `json:"authFields" bson:"orgMemberAuthFields"`
	TwoFaFields OrgMemberTwoFaFields `json:"twoFaFields" bson:"orgMemberTwoFaFields"`

	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
```

with:

```go
	AuthFields  OrgMemberAuthFields  `json:"authFields" bson:"orgMemberAuthFields"`
	TwoFaFields OrgMemberTwoFaFields `json:"twoFaFields" bson:"orgMemberTwoFaFields"`
	// CertAuth makes voters prove their nationalId (the only auth field such a census has) with a
	// Spanish personal certificate, through the certificate gateway, instead of typing it. The
	// census may still require an email or SMS code afterwards (TwoFaFields).
	CertAuth bool `json:"certAuth,omitempty" bson:"certAuth,omitempty"`

	CreatedAt time.Time `json:"createdAt" bson:"createdAt"`
```
Edit `db/types.go`, replace:

```go
// HashAuthTwoFaFields helper function receives as input the data of a member and
```

with:

```go
// LoginHashFields returns the fields a participant's login hashes are computed from. A certificate
// census hashes the national ID alone: the certificate proves it, any 2FA contact is checked
// against the member afterwards, and one canonical DNI is therefore one participant. Every other
// census hashes its auth and 2FA fields. calculateParticipantHashes is its only caller, and
// migrations.computeParticipantHashes mirrors it.
func (c Census) LoginHashFields() (OrgMemberAuthFields, OrgMemberTwoFaFields) {
	if c.CertAuth {
		return OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}, nil
	}
	return c.AuthFields, c.TwoFaFields
}

// HashAuthTwoFaFields helper function receives as input the data of a member and
```
Edit `db/census_participant.go`, replace:

```go
func calculateParticipantHashes(census Census, member OrgMember) map[string][]byte {
	hashes := map[string][]byte{
		"loginHash": HashAuthTwoFaFields(member, census.AuthFields, census.TwoFaFields),
	}
	// a whitespace-only email is no channel, as MissingLoginData has it
	if len(census.TwoFaFields) == 2 && strings.TrimSpace(member.Email) != "" {
		hashes["loginHashEmail"] = HashAuthTwoFaFields(member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
	}
	if len(census.TwoFaFields) == 2 && !member.Phone.IsEmpty() {
		hashes["loginHashPhone"] = HashAuthTwoFaFields(member, census.AuthFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone})
	}
	return hashes
}
```

with:

```go
func calculateParticipantHashes(census Census, member OrgMember) map[string][]byte {
	authFields, twoFaFields := census.LoginHashFields()
	hashes := map[string][]byte{
		"loginHash": HashAuthTwoFaFields(member, authFields, twoFaFields),
	}
	// a whitespace-only email is no channel, as MissingLoginData has it
	if len(twoFaFields) == 2 && strings.TrimSpace(member.Email) != "" {
		hashes["loginHashEmail"] = HashAuthTwoFaFields(member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail})
	}
	if len(twoFaFields) == 2 && !member.Phone.IsEmpty() {
		hashes["loginHashPhone"] = HashAuthTwoFaFields(member, authFields, OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone})
	}
	return hashes
}
```
Edit `db/census_participant.go`, replace:

```go
		if member.MissingLoginData(census.AuthFields, census.TwoFaFields) {
			memberErrors
```

with:

```go
		if census.MemberMissingLoginData(member) {
			memberErrors
```
Edit `db/org_members.go`, replace:

```go
		if member.MissingLoginData(census.AuthFields, census.TwoFaFields) {
```

with:

```go
		if census.MemberMissingLoginData(member) {
```
Edit `db/census_missing_data.go`, replace:

```go
		if m.MissingLoginData(census.AuthFields, census.TwoFaFields) {
```

with:

```go
		if census.MemberMissingLoginData(m) {
```
Edit `db/census_missing_data.go`, replace:

```go
// memberAuthFieldValue returns the value of an auth field of the member, and false for a field it
```

with:

```go
// MemberMissingLoginData reports whether the member lacks the data to log in to the census: what
// MissingLoginData reports for its auth and 2FA fields, and, for a certificate census, a national ID
// that is not a valid DNI or NIE, which no certificate could ever match. Such members are left out of
// the census and reported as missing data.
func (c *Census) MemberMissingLoginData(m *OrgMember) bool {
	if m.MissingLoginData(c.AuthFields, c.TwoFaFields) {
		return true
	}
	if c.CertAuth {
		_, ok := internal.CanonicalNationalID(m.NationalID)
		return !ok
	}
	return false
}

// memberAuthFieldValue returns the value of an auth field of the member, and false for a field it
```
Edit `db/org_members_groups.go`, replace:

```go
// Both authFields and twoFaFields are checked for missing data.
func (ms *MongoStorage) CheckGroupMembersFields(
	orgAddress common.Address,
	groupID string,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
) (*OrgMemberAggregationResults, error) {
```

with:

```go
// Both authFields and twoFaFields are checked for missing data. certAuth checks them for a
// certificate census, where a national ID that is not a valid DNI or NIE also counts as missing data
// and only the national ID is hashed (Census.LoginHashFields).
func (ms *MongoStorage) CheckGroupMembersFields(
	orgAddress common.Address,
	groupID string,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
	certAuth bool,
) (*OrgMemberAggregationResults, error) {
```
Edit `db/org_members_groups.go`, replace:

```go
	return aggregateMemberFields(ctx, cur, authFields, twoFaFields)
}
```

with:

```go
	return aggregateMemberFields(ctx, cur, authFields, twoFaFields, certAuth)
}
```
Edit `db/org_members_groups.go`, replace:

```go
// same aggregation logic.
func (ms *MongoStorage) CheckMembersFields(
	orgAddress common.Address,
	memberIDs []string,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
) (*OrgMemberAggregationResults, error) {
```

with:

```go
// same aggregation logic.
func (ms *MongoStorage) CheckMembersFields(
	orgAddress common.Address,
	memberIDs []string,
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
	certAuth bool,
) (*OrgMemberAggregationResults, error) {
```
Edit `db/org_members_groups.go`, replace:

```go
	results, err := aggregateMemberFields(ctx, cur, authFields, twoFaFields)
```

with:

```go
	results, err := aggregateMemberFields(ctx, cur, authFields, twoFaFields, certAuth)
```
Edit `db/org_members_groups.go`, replace:

```go
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
) (*OrgMemberAggregationResults, error) {
	cm, err := classifyCensusMembers(ctx, cur, Census{AuthFields: authFields, TwoFaFields: twoFaFields}, nil)
```

with:

```go
	authFields OrgMemberAuthFields,
	twoFaFields OrgMemberTwoFaFields,
	certAuth bool,
) (*OrgMemberAggregationResults, error) {
	census := Census{AuthFields: authFields, TwoFaFields: twoFaFields, CertAuth: certAuth}
	cm, err := classifyCensusMembers(ctx, cur, census, nil)
```
Edit `api/processes_census_validate.go`: append `, false` as the last argument of the three `CheckGroupMembersFields` /
`CheckMembersFields` calls (the API passes the spec's flag from Task 5 on):

```go
	case census.GroupID != "":
		results, err = a.db.CheckGroupMembersFields(orgAddr, census.GroupID, census.AuthFields, census.TwoFaFields, false)
	case len(census.MemberIDs) > 0:
		results, err = a.db.CheckMembersFields(orgAddr, census.MemberIDs, census.AuthFields, census.TwoFaFields, false)
	default:
		results, err = a.db.CheckGroupMembersFields(orgAddr, "", census.AuthFields, census.TwoFaFields, false)
```

Edit `migrations/login_hash.go`, replace:

```go
	ID          bson.ObjectID `bson:"_id"`
	AuthFields  []string      `bson:"orgMemberAuthFields"`
	TwoFaFields []string      `bson:"orgMemberTwoFaFields"`
}
```

with:

```go
	ID          bson.ObjectID `bson:"_id"`
	AuthFields  []string      `bson:"orgMemberAuthFields"`
	TwoFaFields []string      `bson:"orgMemberTwoFaFields"`
	CertAuth    bool          `bson:"certAuth"`
}
```
Edit `migrations/repair_login_hashes.go`, replace:

```go
// computeParticipantHashes mirrors db.calculateParticipantHashesBson: it produces
// exactly the hash fields that were originally stored for a participant, so the
// repair never adds or removes keys.
func computeParticipantHashes(m memberHashDoc, c censusHashDoc) participantHashSet {
	set := participantHashSet{
		LoginHash: hashMemberFields(m, c.AuthFields, c.TwoFaFields),
	}
	if len(c.TwoFaFields) == 2 && strings.TrimSpace(m.Email) != "" {
		set.LoginHashEmail = hashMemberFields(m, c.AuthFields, []string{"email"}) //nolint:goconst
	}
	if len(c.TwoFaFields) == 2 && len(m.Phone) > 0 {
		set.LoginHashPhone = hashMemberFields(m, c.AuthFields, []string{"phone"})
	}
	return set
}
```

with:

```go
// computeParticipantHashes mirrors db.calculateParticipantHashesBson: it produces
// exactly the hash fields that were originally stored for a participant, so the
// repair never adds or removes keys. Like db.Census.LoginHashFields, a certificate
// census hashes the national ID alone and stores no email or phone variant.
func computeParticipantHashes(m memberHashDoc, c censusHashDoc) participantHashSet {
	authFields, twoFaFields := c.AuthFields, c.TwoFaFields
	if c.CertAuth {
		authFields, twoFaFields = []string{"nationalId"}, nil
	}
	set := participantHashSet{
		LoginHash: hashMemberFields(m, authFields, twoFaFields),
	}
	if len(twoFaFields) == 2 && strings.TrimSpace(m.Email) != "" {
		set.LoginHashEmail = hashMemberFields(m, authFields, []string{"email"}) //nolint:goconst
	}
	if len(twoFaFields) == 2 && len(m.Phone) > 0 {
		set.LoginHashPhone = hashMemberFields(m, authFields, []string{"phone"})
	}
	return set
}
```
Update the existing test calls to the new signatures (verified to touch exactly 20 single-line calls), then the two
multi-line ones:

```sh
sed -i -E '/testDB\.Check(Group)?MembersFields\(.*\)$/ s/\)$/, false)/' db/census_missing_data_test.go db/org_members_groups_test.go
```

Edit `db/census_missing_data_test.go`, replace:

```go
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname}, emailOnly)
```

with:

```go
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname}, emailOnly, false)
```
Edit `db/org_members_groups_test.go`, replace:

```go
			OrgMemberAuthFields{},
			OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone},
		)
```

with:

```go
			OrgMemberAuthFields{},
			OrgMemberTwoFaFields{OrgMemberTwoFaFieldPhone},
			false,
		)
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go vet ./... && go test ./db/ -count=1 -run 'TestCert|TestRepair|TestNationalID' -v 2>&1 | grep -E '^(--- |ok|FAIL)' && go test ./db/ ./migrations/ ./internal/ -count=1`
Expected: `--- PASS: TestCertCensusLoginHashFields`, `--- PASS: TestRepairMatchesCanonicalHashCertCensus`, `--- PASS: TestCertCensusMissingData`, then `ok` for `db`, `migrations` and `internal`.

Step 3: commit
`git add -- db/types.go db/census_participant.go db/census_missing_data.go db/org_members.go db/org_members_groups.go api/processes_census_validate.go migrations/login_hash.go migrations/repair_login_hashes.go db/census_missing_data_test.go db/org_members_groups_test.go db/census_cert_auth_test.go && git -c commit.gpgsign=false commit -m "feat(csp): hash a certificate census on the national id alone" -- db/types.go db/census_participant.go db/census_missing_data.go db/org_members.go db/org_members_groups.go api/processes_census_validate.go migrations/login_hash.go migrations/repair_login_hashes.go db/census_missing_data_test.go db/org_members_groups_test.go db/census_cert_auth_test.go`

Interfaces this task provides to later tasks: `db.Census.CertAuth bool` (bson `certAuth`); `func (c Census) LoginHashFields() (OrgMemberAuthFields, OrgMemberTwoFaFields)`; `func (c *Census) MemberMissingLoginData(m *OrgMember) bool`; `CheckGroupMembersFields(org, groupID, auth, twoFa, certAuth bool)`, `CheckMembersFields(org, ids, auth, twoFa, certAuth bool)`.

### Task 3: Migration 0037 rehashes canonical national IDs
Model: strong   Repo: saas-backend   Files: `migrations/0037_canonical_national_id_hashes.go` (new), `db/migration_canonical_national_id_test.go` (new)
Goal: C2.6 — a registered migration runs `RepairLoginHashes(Apply: true)` and logs its report, as 0025 does; version 37 per the lts/main ledger rule (main ends at 0036, lts at 0033) (AC6).

Step 1: write the failing test
Create `db/migration_canonical_national_id_test.go`:

```go
package db

import (
	"context"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestMigrationCanonicalNationalIDHashes: migration 37 rehashes participants stored under a
// non-canonical national ID, leaves (and counts) the members stored twice under two spellings of one
// DNI with their old hashes, and a second run changes nothing.
func TestMigrationCanonicalNationalIDHashes(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	mig := migrationByVersion(c, 37)

	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, CreatedAt: time.Now()}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// legacyHash is what the previous release stored: the lowercased value as written
	legacyHash := func(m OrgMember) []byte {
		return internal.HashLoginFields(map[string]string{"nationalId": strings.ToLower(m.NationalID)})
	}
	seed := func(nationalID string) (*OrgMember, []byte) {
		m := &OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: nationalID, CreatedAt: time.Now()}
		h := legacyHash(*m)
		seedLegacyParticipant(t, censusID, m, h)
		return m, h
	}
	rehashed, _ := seed("1234567-l")
	canonical, canonicalHash := seed("12345678Z")
	foreign, foreignHash := seed("P1234567")
	twinA, twinAHash := seed("X1234567L")
	twinB, twinBHash := seed("x-01234567-l")

	dry := runRepair(t, migrations.RepairOptions{})
	c.Assert(dry.ParticipantsRehashed, qt.Equals, 1)
	c.Assert(dry.ParticipantsSkipped, qt.Equals, 2)
	c.Assert(dry.SkippedMembers, qt.ContentEquals, []migrations.SkippedMember{
		{MemberID: twinA.ID.Hex(), CensusID: censusID}, {MemberID: twinB.ID.Hex(), CensusID: censusID},
	})

	stored := func(m *OrgMember) []byte {
		var p CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": m.ID.Hex(), "censusId": censusID}).Decode(&p), qt.IsNil)
		return p.LoginHash
	}
	for range 2 { // the second run must change nothing
		c.Assert(mig.Up(ctx, database), qt.IsNil)
		c.Assert(stored(rehashed), qt.DeepEquals, HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, auth, nil))
		c.Assert(stored(canonical), qt.DeepEquals, canonicalHash)
		c.Assert(stored(foreign), qt.DeepEquals, foreignHash)
		c.Assert(stored(twinA), qt.DeepEquals, twinAHash)
		c.Assert(stored(twinB), qt.DeepEquals, twinBHash)
	}
	again := runRepair(t, migrations.RepairOptions{})
	c.Assert(again.ParticipantsRehashed, qt.Equals, 0)
	c.Assert(again.ParticipantsSkipped, qt.Equals, 2)
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./db/ -count=1 -run TestMigrationCanonicalNationalIDHashes`
Expected: `--- FAIL: TestMigrationCanonicalNationalIDHashes` at `migrationByVersion(c, 37)` (`got: false, want: true`: version 37 is not registered)

Step 2: implement
Create `migrations/0037_canonical_national_id_hashes.go`:

```go
package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(37, "canonical_national_id_hashes", upCanonicalNationalIDHashes, downCanonicalNationalIDHashes)
}

// upCanonicalNationalIDHashes rehashes the census participants whose login hash includes a national
// ID that is a valid DNI or NIE written in a non-canonical form, now that hashMemberFields hashes it
// canonically (internal.CanonicalNationalID). It is RepairLoginHashes applied, as in migration 0025:
// rows of one census that would share a recomputed hash (a member stored twice under two spellings
// of one DNI) are all skipped and keep their old hashes. A login always hashes the canonical form, so
// of such twins only one stored in canonical form still logs in; the others cannot until the organiser
// removes the duplicate and scripts/repairlogins -apply rehashes the row that is left.
func upCanonicalNationalIDHashes(ctx context.Context, database *mongo.Database) error {
	report, err := RepairLoginHashes(ctx, database, RepairOptions{Apply: true})
	if err != nil {
		return fmt.Errorf("rehashing canonical national id login hashes: %w", err)
	}
	log.Infow("rehashed census participant login hashes with canonical national ids",
		"scanned", report.ParticipantsScanned, "rehashed", report.ParticipantsRehashed,
		"skipped", report.ParticipantsSkipped, "orphans", report.OrphanParticipants)
	return nil
}

// downCanonicalNationalIDHashes is a no-op: the previous hashes are recomputed by running the
// previous release's scripts/repairlogins with -apply against the database.
func downCanonicalNationalIDHashes(_ context.Context, _ *mongo.Database) error {
	return nil
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./db/ -count=1 -run 'TestMigrationCanonicalNationalIDHashes|TestMigrations$' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: `--- PASS: TestMigrationCanonicalNationalIDHashes`, `--- PASS: TestMigrations`, `ok  	github.com/vocdoni/saas-backend/db`

Step 3: commit
`git add -- migrations/0037_canonical_national_id_hashes.go db/migration_canonical_national_id_test.go && git -c commit.gpgsign=false commit -m "feat(migrations): rehash login hashes with canonical national ids" -- migrations/0037_canonical_national_id_hashes.go db/migration_canonical_national_id_test.go`

Interfaces this task provides to later tasks: migration version 37 (`canonical_national_id_hashes`); the next free version on main is 38.

### Task 4: Certificate gateway signature verification and keys (shared test vectors)
Model: strong   Repo: saas-backend   Files: `csp/cert_gateway.go` (new), `csp/cert_gateway_test.go` (new), `csp/csp.go`, `cmd/service/main.go`, `cmd/service/main_test.go`, `example.env`
Goal: C2.4 "Authentication" — parse `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS`, verify `X-Cert-Gateway-Signature` over the literal method/path and the body hash within ±60 s; the shared vectors (AC4 signature part; AC7 "signature verifies against the backend's verifier").

The vectors below are the single source of truth: Task 9 copies the same constants verbatim into cert-gateway.

Step 1: write the failing test
Create `csp/cert_gateway_test.go`:

```go
package csp

import (
	"errors"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// Shared certificate gateway signature test vectors. cert-gateway's internal/signing tests use the
// same values: the seed is bytes 0x00..0x1f, its public key is vectorPublicKey.
const (
	vectorSeed      = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	vectorPublicKey = "A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg="
	vectorKeyID     = "test-1"
	vectorTime      = int64(1767225600) // 2026-01-01T00:00:00Z
	vectorBody      = `{"processId":"0123456789abcdef01234567","nationalId":"12345678Z","documentType":"DNI",` +
		`"provider":"fnmt","profile":"fnmt-citizen-g2",` +
		`"certSha256":"abababababababababababababababababababababababababababababababab",` +
		`"codeChallenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}`
	vectorMessage = "cert-gateway-v1\n1767225600\nPOST\n/cert-auth/assertions\n" +
		"4c6ce4c5f31980d9aa0819615924fc66ab54794461d5a4602fda03144362a1ff"
	vectorHeader = "t=1767225600,k=test-1,v1=" +
		"MIU3KNz-CoWDXqKlIFuxleuc_y5cG83YtE6v84OAb9mfZRp4jDnnPXOSTaVn666rdPe7SERtWnL2uGi6qH9QAg"
)

func TestCertGatewaySignatureVectors(t *testing.T) {
	c := qt.New(t)
	// a second, rotated-out key next to the vector key
	keys, err := ParseCertGatewayKeys("old-1:11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=, " + vectorKeyID + ":" + vectorPublicKey)
	c.Assert(err, qt.IsNil)
	c.Assert(keys, qt.HasLen, 2)
	at := time.Unix(vectorTime, 0)

	c.Assert(string(CertGatewaySignedMessage(vectorTime, []byte(vectorBody))), qt.Equals, vectorMessage)
	c.Assert(VerifyCertGatewaySignature(keys, vectorHeader, []byte(vectorBody), at), qt.IsNil)
	c.Assert(VerifyCertGatewaySignature(keys, vectorHeader, []byte(vectorBody), at.Add(60*time.Second)), qt.IsNil)
	c.Assert(VerifyCertGatewaySignature(keys, vectorHeader, []byte(vectorBody), at.Add(-60*time.Second)), qt.IsNil)

	for name, tc := range map[string]struct {
		header, body string
		at           time.Time
	}{
		"empty header":    {"", vectorBody, at},
		"missing v1":      {"t=1767225600,k=test-1", vectorBody, at},
		"duplicate field": {vectorHeader + ",k=test-1", vectorBody, at},
		"unknown key":     {"t=1767225600,k=test-2,v1=" + vectorHeader[len("t=1767225600,k=test-1,v1="):], vectorBody, at},
		"stale":           {vectorHeader, vectorBody, at.Add(61 * time.Second)},
		"future":          {vectorHeader, vectorBody, at.Add(-61 * time.Second)},
		"padded":          {vectorHeader + "==", vectorBody, at},
		"other body":      {vectorHeader, vectorBody + " ", at},
		"other time":      {"t=1767225601" + vectorHeader[len("t=1767225600"):], vectorBody, at},
	} {
		err := VerifyCertGatewaySignature(keys, tc.header, []byte(tc.body), tc.at)
		c.Assert(errors.Is(err, ErrCertGatewaySignature), qt.IsTrue, qt.Commentf("%s: %v", name, err))
	}
}

func TestParseCertGatewayKeys(t *testing.T) {
	c := qt.New(t)
	keys, err := ParseCertGatewayKeys("  ")
	c.Assert(err, qt.IsNil)
	c.Assert(keys, qt.HasLen, 0)
	for _, bad := range []string{
		"test-1",              // no key
		":" + vectorPublicKey, // no id
		"test-1:not-base64!",  // not base64
		"test-1:AAEC",         // wrong length
		"test-1:" + vectorPublicKey + ",test-1:" + vectorPublicKey, // duplicate id
		"test=1:" + vectorPublicKey, // '=' breaks the signature header
	} {
		_, err := ParseCertGatewayKeys(bad)
		c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("%q", bad))
	}
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./csp/ -run 'TestCertGateway|TestParseCertGatewayKeys'`
Expected: `FAIL	github.com/vocdoni/saas-backend/csp [build failed]` (`undefined: ParseCertGatewayKeys`)

Step 2: implement
Create `csp/cert_gateway.go` (Task 6 appends the rate budget):

```go
package csp

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CertGatewaySignatureHeader carries the certificate gateway's signature of an assertion request.
const CertGatewaySignatureHeader = "X-Cert-Gateway-Signature"

// CertGatewaySignatureMaxSkew is how far the signature timestamp may be from the server clock.
const CertGatewaySignatureMaxSkew = 60 * time.Second

// certGatewaySignedPath is the request path the signature covers. It is a literal on both sides,
// never read from the request, so a path prefix or rewrite at the edge cannot break verification.
const certGatewaySignedPath = "/cert-auth/assertions"

// ErrCertGatewaySignature is returned for a missing, malformed, stale, unknown-key or invalid
// certificate gateway signature.
var ErrCertGatewaySignature = fmt.Errorf("invalid certificate gateway signature")

// CertGatewayKeys maps a certificate gateway key id to its Ed25519 public key. Several keys can be
// configured at once, so a key can be rotated without downtime.
type CertGatewayKeys map[string]ed25519.PublicKey

// ParseCertGatewayKeys parses CERT_GATEWAY_PUBLIC_KEYS: a comma-separated list of
// keyId:base64(public key) entries. An empty value is no keys (the feature is off).
func ParseCertGatewayKeys(s string) (CertGatewayKeys, error) {
	keys := CertGatewayKeys{}
	if strings.TrimSpace(s) == "" {
		return keys, nil
	}
	for entry := range strings.SplitSeq(s, ",") {
		id, encoded, found := strings.Cut(strings.TrimSpace(entry), ":")
		if !found || id == "" || strings.ContainsAny(id, ",= ") {
			return nil, fmt.Errorf("certificate gateway key %q: want keyId:base64(publicKey)", id)
		}
		if _, dup := keys[id]; dup {
			return nil, fmt.Errorf("certificate gateway key %q: duplicate key id", id)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("certificate gateway key %q: not a base64 Ed25519 public key", id)
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	return keys, nil
}

// CertGatewaySignedMessage returns the bytes the certificate gateway signs for an assertion
// request body sent at unix time t.
func CertGatewaySignedMessage(t int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("cert-gateway-v1\n" + strconv.FormatInt(t, 10) + "\nPOST\n" + certGatewaySignedPath + "\n" +
		hex.EncodeToString(sum[:]))
}

// VerifyCertGatewaySignature verifies header, the X-Cert-Gateway-Signature value
// "t=<unix seconds>,k=<keyId>,v1=<base64url(signature), no padding>", over body, the raw request
// body, with the key named by k. The timestamp must be within CertGatewaySignatureMaxSkew of now.
// Every failure wraps ErrCertGatewaySignature.
func VerifyCertGatewaySignature(keys CertGatewayKeys, header string, body []byte, now time.Time) error {
	fields := map[string]string{}
	for part := range strings.SplitSeq(header, ",") {
		name, value, found := strings.Cut(part, "=")
		if _, dup := fields[name]; !found || dup {
			return fmt.Errorf("%w: malformed header", ErrCertGatewaySignature)
		}
		fields[name] = value
	}
	if len(fields) != 3 {
		return fmt.Errorf("%w: malformed header", ErrCertGatewaySignature)
	}
	t, err := strconv.ParseInt(fields["t"], 10, 64)
	if err != nil {
		return fmt.Errorf("%w: malformed timestamp", ErrCertGatewaySignature)
	}
	if skew := now.Sub(time.Unix(t, 0)); skew > CertGatewaySignatureMaxSkew || skew < -CertGatewaySignatureMaxSkew {
		return fmt.Errorf("%w: stale timestamp", ErrCertGatewaySignature)
	}
	key, ok := keys[fields["k"]]
	if !ok {
		return fmt.Errorf("%w: unknown key", ErrCertGatewaySignature)
	}
	sig, err := base64.RawURLEncoding.DecodeString(fields["v1"])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: malformed signature", ErrCertGatewaySignature)
	}
	if !ed25519.Verify(key, CertGatewaySignedMessage(t, body), sig) {
		return fmt.Errorf("%w: signature mismatch", ErrCertGatewaySignature)
	}
	return nil
}
```
Edit `csp/csp.go`, replace:

```go
	// DisableBlindCSP temporarily stops offering anonymous voting: no blind signing, and no new
	// anonymous processes (the API reads it through BlindCSPDisabled).
	DisableBlindCSP bool
}
```

with:

```go
	// DisableBlindCSP temporarily stops offering anonymous voting: no blind signing, and no new
	// anonymous processes (the API reads it through BlindCSPDisabled).
	DisableBlindCSP bool
	// CertGatewayKeys are the certificate gateway public keys (CERT_GATEWAY_PUBLIC_KEYS). Empty turns
	// certificate voter authentication off: the assertion route answers 404 and certificate
	// censuses are refused (see CertAuthEnabled).
	CertGatewayKeys CertGatewayKeys
}
```
Edit `csp/csp.go`, replace:

```go
	notifySync               bool
	blindCSPDisabled         atomic.Bool
}
```

with:

```go
	notifySync               bool
	blindCSPDisabled         atomic.Bool
	certGatewayKeys          atomic.Pointer[CertGatewayKeys]
}

// CertGatewayKeys returns the configured certificate gateway public keys; empty when certificate
// voter authentication is off.
func (c *CSP) CertGatewayKeys() CertGatewayKeys {
	if keys := c.certGatewayKeys.Load(); keys != nil {
		return *keys
	}
	return nil
}

// SetCertGatewayKeys replaces the certificate gateway public keys at runtime.
func (c *CSP) SetCertGatewayKeys(keys CertGatewayKeys) {
	c.certGatewayKeys.Store(&keys)
}

// CertAuthEnabled reports whether certificate voter authentication is on: at least one
// certificate gateway key is configured.
func (c *CSP) CertAuthEnabled() bool {
	return len(c.CertGatewayKeys()) > 0
}
```
Edit `csp/csp.go`, replace:

```go
	c.blindCSPDisabled.Store(config.DisableBlindCSP)
	return c, nil
```

with:

```go
	c.blindCSPDisabled.Store(config.DisableBlindCSP)
	c.SetCertGatewayKeys(config.CertGatewayKeys)
	return c, nil
```
Edit `cmd/service/main.go`, replace:

```go
const disableBlindCSPKey = "disable_blindcsp"
```

with:

```go
const disableBlindCSPKey = "disable_blindcsp"

// certGatewayPublicKeysKey is the flag of the certificate gateway public keys, read from
// VOCDONI_CERT_GATEWAY_PUBLIC_KEYS like disableBlindCSPKey.
const certGatewayPublicKeysKey = "cert_gateway_public_keys"
```
Edit `cmd/service/main.go`, replace:

```go
	flag.Bool(disableBlindCSPKey, false, "temporarily stop offering anonymous voting (no new anonymous processes, no blind signing)")
```

with:

```go
	flag.Bool(disableBlindCSPKey, false, "temporarily stop offering anonymous voting (no new anonymous processes, no blind signing)")
	flag.String(certGatewayPublicKeysKey, "",
		"certificate gateway public keys, keyId:base64(ed25519 public key) comma-separated; empty turns certificate voter auth off")
```
Edit `cmd/service/main.go`, replace:

```go
	cspConf := &csp.Config{
```

with:

```go
	certGatewayKeys, err := csp.ParseCertGatewayKeys(viper.GetString(certGatewayPublicKeysKey))
	if err != nil {
		log.Fatalf("invalid %s: %v", certGatewayPublicKeysKey, err)
	}
	cspConf := &csp.Config{
```
Edit `cmd/service/main.go`, replace:

```go
		DisableBlindCSP:                viper.GetBool(disableBlindCSPKey),
	}
```

with:

```go
		DisableBlindCSP:                viper.GetBool(disableBlindCSPKey),
		CertGatewayKeys:                certGatewayKeys,
	}
```
Append to `cmd/service/main_test.go`:

```go

func TestCertGatewayPublicKeysEnvName(t *testing.T) {
	t.Setenv("VOCDONI_CERT_GATEWAY_PUBLIC_KEYS", "dev-1:A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=")
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.String(certGatewayPublicKeysKey, "", "")
	v := viper.New()
	qt.Assert(t, initViper(v, flags), qt.IsNil)
	qt.Assert(t, v.GetString(certGatewayPublicKeysKey), qt.Equals, "dev-1:A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=")
}
```

Append to `example.env`:

```sh

# Certificate voter authentication: the certificate gateway public keys, as comma-separated
# keyId:base64(Ed25519 public key) entries (several at once to rotate a key without downtime).
# Empty or unset turns the feature off: POST /cert-auth/assertions answers 404 and censuses with
# certAuth are refused.
# VOCDONI_CERT_GATEWAY_PUBLIC_KEYS=dev-1:BASE64PUBLICKEY=
```

Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./csp/ -count=1 -run 'TestCertGateway|TestParseCertGatewayKeys' -v 2>&1 | grep -E '^(--- |ok)' && go test ./cmd/service/ -count=1 && go build ./...`
Expected: `--- PASS: TestCertGatewaySignatureVectors`, `--- PASS: TestParseCertGatewayKeys`, `ok  	github.com/vocdoni/saas-backend/csp`, `ok  	github.com/vocdoni/saas-backend/cmd/service`, and a clean build.

Step 3: commit
`git add -- csp/cert_gateway.go csp/cert_gateway_test.go csp/csp.go cmd/service/main.go cmd/service/main_test.go example.env && git -c commit.gpgsign=false commit -m "feat(csp): verify certificate gateway signatures" -- csp/cert_gateway.go csp/cert_gateway_test.go csp/csp.go cmd/service/main.go cmd/service/main_test.go example.env`

Interfaces this task provides to later tasks: `csp.CertGatewayKeys` (`map[string]ed25519.PublicKey`), `csp.ParseCertGatewayKeys(string)`, `csp.CertGatewaySignedMessage(t int64, body []byte) []byte`, `csp.VerifyCertGatewaySignature(keys, header, body, now) error` (wraps `csp.ErrCertGatewaySignature`), `csp.CertGatewaySignatureHeader`, `(*csp.CSP).CertGatewayKeys()`, `SetCertGatewayKeys(keys)`, `CertAuthEnabled() bool`; the shared vector constants.

### Task 5: API census field, validation and echo
Model: standard   Repo: saas-backend   Files: `api/apicommon/voting_processes.go`, `api/apicommon/types.go`, `api/processes_census.go`, `api/processes_census_validate.go`, `api/processes_cert_census_test.go` (new)
Goal: C2.1 — `certAuth` on `CensusSpec`/`OrganizationCensus`, refused without keys (40025) or with authFields other than `["nationalId"]` (40037), any 2FA subset allowed, echoed on reads; the validation endpoint reports invalid DNIs (AC3).

Step 1: write the failing test
Create `api/processes_cert_census_test.go`:

```go
package api

import (
	"crypto/ed25519"
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// testCertGatewayKeyID and testCertGatewaySeed are the shared signature test vector key (seed bytes
// 0x00..0x1f): csp.TestCertGatewaySignatureVectors and cert-gateway's tests use it too.
const testCertGatewayKeyID = "test-1"

var testCertGatewayKey = ed25519.NewKeyFromSeed([]byte{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
	16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31,
})

// setCertGatewayKeys sets the certificate gateway keys of the shared test CSP for the rest of the
// test; nil turns certificate auth off. It mutates shared testAPI state, like setDisableBlindCSP.
func setCertGatewayKeys(t *testing.T, keys csp.CertGatewayKeys) {
	t.Helper()
	previous := testAPI.csp.CertGatewayKeys()
	testAPI.csp.SetCertGatewayKeys(keys)
	t.Cleanup(func() { testAPI.csp.SetCertGatewayKeys(previous) })
}

// enableCertAuth turns certificate auth on with the test vector key.
func enableCertAuth(t *testing.T) {
	t.Helper()
	setCertGatewayKeys(t, csp.CertGatewayKeys{
		testCertGatewayKeyID: testCertGatewayKey.Public().(ed25519.PublicKey),
	})
}

// testDNI returns a valid DNI (8 digits and their check letter) for n.
func testDNI(n int) string {
	return fmt.Sprintf("%08d%c", n, "TRWAGMYFPDXBNJZSQVHLCKE"[n%23])
}

// certCensus is a certificate census spec over the given members.
func certCensus(twoFa db.OrgMemberTwoFaFields, memberIDs ...string) apicommon.CensusSpec {
	return apicommon.CensusSpec{
		AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
		TwoFaFields: twoFa,
		CertAuth:    true,
		MemberIDs:   memberIDs,
	}
}

// TestCertCensusValidation (AC3): a certificate census needs authFields exactly ["nationalId"] and
// the feature on; any 2FA subset works; members with an invalid DNI, or without the 2FA channel,
// are reported as missing data and left out of the census; certAuth is echoed on reads.
func TestCertCensusValidation(t *testing.T) {
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "C-1", NationalID: testDNI(1), Email: "c1@example.com", Phone: "+34600000001"},
		apicommon.OrgMember{MemberNumber: "C-2", NationalID: "1234567-l", Email: "c2@example.com", Phone: "+34600000002"},
		apicommon.OrgMember{MemberNumber: "C-3", NationalID: "12345678A", Email: "c3@example.com", Phone: "+34600000003"},
		apicommon.OrgMember{MemberNumber: "C-4", NationalID: testDNI(4), Phone: "+34600000004"},
	)
	ids := memberIDs(members)
	valid, invalidDNI, noEmail := ids[:2], ids[2], ids[3]

	validate := func(spec apicommon.CensusSpec) ([]byte, int) {
		return testRequest(t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec},
			processesCensusValidateEndpoint)
	}

	t.Run("feature off", func(t *testing.T) {
		setCertGatewayKeys(t, nil)
		requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token,
			&apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: certCensus(nil, valid...)},
			processesCensusValidateEndpoint)
		req := minimalVotingProcessRequest(orgAddress)
		req.Census = certCensus(nil, valid...)
		requestAndAssertError(errors.ErrNotSupported, t, http.MethodPost, token, req, processesCreateEndpoint)
	})

	enableCertAuth(t)

	t.Run("authFields other than [nationalId]", func(t *testing.T) {
		c := qt.New(t)
		for _, auth := range []db.OrgMemberAuthFields{
			nil,
			{db.OrgMemberAuthFieldsMemberNumber},
			{db.OrgMemberAuthFieldsNationalID, db.OrgMemberAuthFieldsName},
		} {
			spec := certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, valid...)
			spec.AuthFields = auth
			body, code := validate(spec)
			c.Assert(code, qt.Equals, http.StatusBadRequest, qt.Commentf("auth=%v: %s", auth, body))
			c.Assert(string(body), qt.Contains, fmt.Sprintf(`"code":%d`, errors.ErrInvalidData.Code))
			req := minimalVotingProcessRequest(orgAddress)
			req.Census = spec
			requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token, req, processesCreateEndpoint)
		}
	})

	t.Run("every 2FA subset", func(t *testing.T) {
		c := qt.New(t)
		for _, twoFa := range []db.OrgMemberTwoFaFields{
			nil,
			{db.OrgMemberTwoFaFieldEmail},
			{db.OrgMemberTwoFaFieldPhone},
			{db.OrgMemberTwoFaFieldEmail, db.OrgMemberTwoFaFieldPhone},
		} {
			body, code := validate(certCensus(twoFa, valid...))
			c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("twoFa=%v: %s", twoFa, body))
		}
	})

	t.Run("invalid DNI and missing 2FA channel are missing data", func(t *testing.T) {
		c := qt.New(t)
		apiErr := requestAndExpectError(t, http.MethodPost, token, &apicommon.ValidateProcessCensusRequest{
			OrgAddress: orgAddress.Bytes(),
			Census:     certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...),
		}, processesCensusValidateEndpoint)
		c.Assert(apiErr.Code, qt.Equals, errors.ErrInvalidData.Code)
		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue, qt.Commentf("data: %v", apiErr.Data))
		c.Assert(data["missingData"], qt.ContentEquals, []any{invalidDNI, noEmail})

		req := minimalVotingProcessRequest(orgAddress)
		req.Census = certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...)
		created := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, req,
			processesCreateEndpoint)
		c.Assert(created.MissingData, qt.ContentEquals, []string{invalidDNI, noEmail})
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", created.ProcessID)
		c.Assert(got.Census.CertAuth, qt.IsTrue)
		c.Assert(got.Census.Size, qt.Equals, int64(len(valid)))
	})
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./api/ -count=1 -run TestCertCensusValidation`
Expected: `FAIL	github.com/vocdoni/saas-backend/api [build failed]` (`unknown field CertAuth in struct literal of type apicommon.CensusSpec`)

Step 2: implement
Edit `api/apicommon/voting_processes.go`, replace:

```go
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
	// GroupID is the org member group
```

with:

```go
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
	// CertAuth makes voters prove their national ID with a Spanish personal certificate, through the
	// certificate gateway, instead of typing it. It requires authFields to be exactly ["nationalId"];
	// twoFaFields may add an email or SMS code. Refused (40025) while the service has no certificate
	// gateway keys configured. Members whose nationalId is not a valid DNI or NIE are reported as
	// missing data and left out of the census. Echoed on process reads.
	CertAuth bool `json:"certAuth,omitempty"`
	// GroupID is the org member group
```
Edit `api/apicommon/voting_processes.go`, replace:

```go
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
			Anonymous:   census.Anonymous,
			AuthFields:  census.AuthFields,
			TwoFaFields: census.TwoFaFields,
		}
```

with:

```go
		resp.Census = CensusSpec{
			Weighted:    census.Weighted,
			Anonymous:   census.Anonymous,
			AuthFields:  census.AuthFields,
			TwoFaFields: census.TwoFaFields,
			CertAuth:    census.CertAuth,
		}
```
Edit `api/apicommon/voting_processes.go`, replace:

```go
			TwoFaFields: census.TwoFaFields,
			Size:        census.Size,
		}
```

with:

```go
			TwoFaFields: census.TwoFaFields,
			CertAuth:    census.CertAuth,
			Size:        census.Size,
		}
```
Edit `api/apicommon/types.go`, replace:

```go
	// Optional for defining which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`
}
```

with:

```go
	// Optional for defining which member data should be used for two-factor authentication
	TwoFaFields db.OrgMemberTwoFaFields `json:"twoFaFields,omitempty"`

	// CertAuth reports a census whose voters prove their national ID with a personal certificate
	CertAuth bool `json:"certAuth,omitempty"`
}
```
Edit `api/apicommon/types.go`, replace:

```go
		AuthFields:  census.AuthFields,
		TwoFaFields: census.TwoFaFields,
	}
	// guard the zero id so an organization-wide census reports no group at all: omitempty keys off the
```

with:

```go
		AuthFields:  census.AuthFields,
		TwoFaFields: census.TwoFaFields,
		CertAuth:    census.CertAuth,
	}
	// guard the zero id so an organization-wide census reports no group at all: omitempty keys off the
```
Edit `api/processes_census.go`, replace:

```go
import (
	"fmt"
	"strings"
```

with:

```go
import (
	"fmt"
	"slices"
	"strings"
```
Edit `api/processes_census.go`, replace:

```go
// resolveOrCreateDefaultCensus materializes the inline census spec of a voting process into
```

with:

```go
// certAuthSpecError returns the error for a census spec whose certAuth cannot be used, or nil:
// certificate censuses are refused while no certificate gateway key is configured, and they need
// authFields to be exactly ["nationalId"], the field a certificate proves. Any 2FA subset is fine.
func (a *API) certAuthSpecError(spec apicommon.CensusSpec) error {
	if !spec.CertAuth {
		return nil
	}
	if !a.csp.CertAuthEnabled() {
		return errors.ErrNotSupported.Withf("certificate voter authentication is not enabled")
	}
	if !slices.Equal(spec.AuthFields, db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}) {
		return errors.ErrInvalidData.Withf(`certAuth requires authFields to be exactly ["nationalId"]`)
	}
	return nil
}

// resolveOrCreateDefaultCensus materializes the inline census spec of a voting process into
```
Edit `api/processes_census.go`, replace:

```go
	if a.anonymousCensusRefused(spec.Anonymous) {
		return nil, nil, errors.ErrInvalidData.With(anonymousVotingUnavailable)
	}
	census := &db.Census{
		OrgAddress:  orgAddress,
		Weighted:    spec.Weighted,
		Anonymous:   spec.Anonymous,
		AuthFields:  spec.AuthFields,
		TwoFaFields: spec.TwoFaFields,
		CreatedAt:   time.Now(),
	}
```

with:

```go
	if a.anonymousCensusRefused(spec.Anonymous) {
		return nil, nil, errors.ErrInvalidData.With(anonymousVotingUnavailable)
	}
	if err := a.certAuthSpecError(spec); err != nil {
		return nil, nil, err
	}
	census := &db.Census{
		OrgAddress:  orgAddress,
		Weighted:    spec.Weighted,
		Anonymous:   spec.Anonymous,
		AuthFields:  spec.AuthFields,
		TwoFaFields: spec.TwoFaFields,
		CertAuth:    spec.CertAuth,
		CreatedAt:   time.Now(),
	}
```
Edit `api/processes_census_validate.go`, replace:

```go
//	@Description	temporarily unavailable".
```

with:

```go
//	@Description	temporarily unavailable".
//	@Description	A `certAuth: true` spec needs authFields exactly ["nationalId"] (else 400, 40037) and a service with
//	@Description	certificate gateway keys (else 400, 40025); members whose nationalId is not a valid DNI or NIE are
//	@Description	reported in missingData.
```
Edit `api/processes_census_validate.go`, replace:

```go
	if err := db.ValidateCensusFields(census.AuthFields, census.TwoFaFields); err != nil {
		errors.ErrInvalidData.With(db.PasswordNeedsLookupFieldMessage).Write(w)
		return
	}
```

with:

```go
	if err := db.ValidateCensusFields(census.AuthFields, census.TwoFaFields); err != nil {
		errors.ErrInvalidData.With(db.PasswordNeedsLookupFieldMessage).Write(w)
		return
	}
	if err := a.certAuthSpecError(census); err != nil {
		writeAPIError(w, err)
		return
	}
```
Edit `api/processes_census_validate.go`, replace:

```go
	switch {
	case census.GroupID != "":
		results, err = a.db.CheckGroupMembersFields(orgAddr, census.GroupID, census.AuthFields, census.TwoFaFields, false)
	case len(census.MemberIDs) > 0:
		results, err = a.db.CheckMembersFields(orgAddr, census.MemberIDs, census.AuthFields, census.TwoFaFields, false)
	default:
		results, err = a.db.CheckGroupMembersFields(orgAddr, "", census.AuthFields, census.TwoFaFields, false)
	}
```

with:

```go
	auth, twoFa, certAuth := census.AuthFields, census.TwoFaFields, census.CertAuth
	switch {
	case census.GroupID != "":
		results, err = a.db.CheckGroupMembersFields(orgAddr, census.GroupID, auth, twoFa, certAuth)
	case len(census.MemberIDs) > 0:
		results, err = a.db.CheckMembersFields(orgAddr, census.MemberIDs, auth, twoFa, certAuth)
	default:
		results, err = a.db.CheckGroupMembersFields(orgAddr, "", auth, twoFa, certAuth)
	}
```
`CertAuth` stays immutable once published with no extra code: a published process refuses a draft update
(`api/processes.go`, `if vp.Published`), and `PUT /processes/{id}/census` adds participants to the stored census through
`MemberMissingLoginData`/`calculateParticipantHashes`, which read the stored flag.

Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./api/ -count=1 -run TestCertCensusValidation -v 2>&1 | grep -E '^\s*--- |^(ok|FAIL)'`
Expected: `--- PASS: TestCertCensusValidation` with its four subtests, `ok  	github.com/vocdoni/saas-backend/api`

Step 3: commit
`git add -- api/apicommon/voting_processes.go api/apicommon/types.go api/processes_census.go api/processes_census_validate.go api/processes_cert_census_test.go && git -c commit.gpgsign=false commit -m "feat(processes): add certificate censuses" -- api/apicommon/voting_processes.go api/apicommon/types.go api/processes_census.go api/processes_census_validate.go api/processes_cert_census_test.go`

Interfaces this task provides to later tasks: `apicommon.CensusSpec.CertAuth` (`json:"certAuth,omitempty"`); test helpers in package `api`: `testCertGatewayKeyID`, `testCertGatewayKey` (vector key), `setCertGatewayKeys(t, keys)`, `enableCertAuth(t)`, `testDNI(n int) string`, `certCensus(twoFa, memberIDs...) apicommon.CensusSpec`.

### Task 6: Assertion storage, rate counter and `POST /cert-auth/assertions`
Model: strong   Repo: saas-backend   Files: `db/cert_auth_assertions.go` (new), `db/cert_auth_assertions_test.go` (new), `db/mongo.go`, `db/helpers.go`, `db/csp_counters.go`, `migrations/0038_cert_auth_assertions_indexes.go` (new), `csp/cert_gateway.go`, `csp/handlers/cert_auth.go` (new), `api/routes.go`, `api/api.go`, `api/cert_assertions_test.go` (new)
Goal: C2.4 — the signed assertion endpoint with its exact response contract, the `certAuthAssertions` collection (unique `codeHash`, TTL `expiresAt`, 5-minute codes, no document number) and the 10-per-hour budget (AC4; concurrency half of AC5 at the storage level).

Step 1: write the failing tests
Create `db/cert_auth_assertions_test.go`:

```go
package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestCertAuthAssertions(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	now := time.Now().UTC().Truncate(time.Millisecond)
	processID := bson.NewObjectID()

	store := func(code string, expiresAt time.Time) []byte {
		hash := sha256.Sum256([]byte(code))
		c.Assert(testDB.CreateCertAuthAssertion(&CertAuthAssertion{
			CodeHash: hash[:], ProcessID: processID, CensusID: "census", ParticipantID: "participant",
			CodeChallenge: "challenge", Provider: "fnmt", CertSHA256: "ab", CreatedAt: now, ExpiresAt: expiresAt,
		}), qt.IsNil)
		return hash[:]
	}

	t.Run("consumed once", func(t *testing.T) {
		c := qt.New(t)
		hash := store("code-1", now.Add(5*time.Minute))
		got, err := testDB.ConsumeCertAuthAssertion(hash, processID, now)
		c.Assert(err, qt.IsNil)
		c.Assert(got.ParticipantID, qt.Equals, "participant")
		c.Assert(got.CodeChallenge, qt.Equals, "challenge")
		c.Assert(got.UsedAt, qt.Not(qt.IsNil))
		_, err = testDB.ConsumeCertAuthAssertion(hash, processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
	})

	t.Run("expired, unknown or of another process", func(t *testing.T) {
		c := qt.New(t)
		expired := store("code-2", now.Add(-time.Second))
		_, err := testDB.ConsumeCertAuthAssertion(expired, processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
		unknown := sha256.Sum256([]byte("nope"))
		_, err = testDB.ConsumeCertAuthAssertion(unknown[:], processID, now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
		other := store("code-3", now.Add(5*time.Minute))
		_, err = testDB.ConsumeCertAuthAssertion(other, bson.NewObjectID(), now)
		c.Assert(errors.Is(err, ErrNotFound), qt.IsTrue)
	})

	t.Run("concurrent redemptions: exactly one wins", func(t *testing.T) {
		c := qt.New(t)
		hash := store("code-4", now.Add(5*time.Minute))
		var wg sync.WaitGroup
		var mu sync.Mutex
		wins := 0
		for range 8 {
			wg.Go(func() {
				if _, err := testDB.ConsumeCertAuthAssertion(hash, processID, now); err == nil {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		c.Assert(wins, qt.Equals, 1)
	})

	t.Run("no document number is stored, code hash unique, TTL on expiresAt", func(t *testing.T) {
		c := qt.New(t)
		ctx := context.Background()
		var raw bson.M
		c.Assert(testDB.certAuthAssertions.FindOne(ctx, bson.M{}).Decode(&raw), qt.IsNil)
		for _, field := range []string{"nationalId", "documentType", "profile"} {
			_, present := raw[field]
			c.Assert(present, qt.IsFalse, qt.Commentf("%s stored", field))
		}
		err := testDB.CreateCertAuthAssertion(&CertAuthAssertion{
			CodeHash: raw["codeHash"].(bson.Binary).Data, ProcessID: processID, ParticipantID: "p", ExpiresAt: now,
		})
		c.Assert(err, qt.Not(qt.IsNil))
		specs, err := testDB.certAuthAssertions.Indexes().ListSpecifications(ctx)
		c.Assert(err, qt.IsNil)
		ttl := false
		for _, spec := range specs {
			if spec.Name == "expiresAt_1" && spec.ExpireAfterSeconds != nil && *spec.ExpireAfterSeconds == 0 {
				ttl = true
			}
		}
		c.Assert(ttl, qt.IsTrue)
	})
}
```
Create `api/cert_assertions_test.go`:

```go
package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// testCertPKCE returns a fresh PKCE verifier and its S256 challenge.
func testCertPKCE(t *testing.T) (verifier, challenge string) {
	t.Helper()
	random := make([]byte, 32)
	_, err := rand.Read(random)
	qt.Assert(t, err, qt.IsNil)
	verifier = base64.RawURLEncoding.EncodeToString(random)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// newCertAssertionRequest is the assertion the gateway posts for a certificate of nationalID.
func newCertAssertionRequest(pid, nationalID, challenge string) *handlers.CertAssertionRequest {
	return &handlers.CertAssertionRequest{
		ProcessID: pid, NationalID: nationalID, DocumentType: "DNI", Provider: "fnmt",
		Profile: "fnmt-citizen-g2", CertSHA256: fmt.Sprintf("%064x", 0xab), CodeChallenge: challenge,
	}
}

// postCertAssertionRaw posts body to the assertion route with signatureHeader, returning the
// answer's status and body.
func postCertAssertionRaw(t *testing.T, body []byte, signatureHeader string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, testURL(certAuthAssertionsEndpoint), bytes.NewReader(body))
	qt.Assert(t, err, qt.IsNil)
	req.Header.Set("Content-Type", "application/json")
	if signatureHeader != "" {
		req.Header.Set(csp.CertGatewaySignatureHeader, signatureHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	qt.Assert(t, err, qt.IsNil)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	qt.Assert(t, err, qt.IsNil)
	return resp.StatusCode, data
}

// signCertAssertion returns the X-Cert-Gateway-Signature of body at time at with key keyID.
func signCertAssertion(key ed25519.PrivateKey, keyID string, body []byte, at time.Time) string {
	sig := ed25519.Sign(key, csp.CertGatewaySignedMessage(at.Unix(), body))
	return "t=" + strconv.FormatInt(at.Unix(), 10) + ",k=" + keyID + ",v1=" + base64.RawURLEncoding.EncodeToString(sig)
}

// postCertAssertion signs req with the test vector key and posts it.
func postCertAssertion(t *testing.T, req *handlers.CertAssertionRequest) (int, []byte) {
	t.Helper()
	body, err := json.Marshal(req)
	qt.Assert(t, err, qt.IsNil)
	return postCertAssertionRaw(t, body, signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now()))
}

// newCertCode gets a certificate code for nationalID on process pid, bound to challenge.
func newCertCode(t *testing.T, pid, nationalID, challenge string) string {
	t.Helper()
	status, body := postCertAssertion(t, newCertAssertionRequest(pid, nationalID, challenge))
	qt.Assert(t, status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", body))
	var resp handlers.CertAssertionResponse
	qt.Assert(t, json.Unmarshal(body, &resp), qt.IsNil)
	return resp.CertCode
}

// TestCertAssertions (AC4) covers POST /cert-auth/assertions: the signature, and each row of the
// response contract with its exact status and body.
func TestCertAssertions(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "A-1", Name: "Alice", NationalID: "1234567-l", Weight: "2"},
		apicommon.OrgMember{MemberNumber: "A-2", Name: "Bob", NationalID: testDNI(2), Weight: "0"},
		apicommon.OrgMember{MemberNumber: "A-3", Name: "Carol", NationalID: testDNI(3), Weight: "1"},
	)
	ids := memberIDs(members)
	spec := certCensus(nil, ids...)
	spec.Weighted = true
	pid, _ := publishCensusProcess(t, token, orgAddress, spec, 1)
	_, challenge := testCertPKCE(t)

	expect := func(t *testing.T, status int, body []byte, wantStatus int, wantCode string) {
		t.Helper()
		qt.Assert(t, status, qt.Equals, wantStatus, qt.Commentf("response: %s", body))
		qt.Assert(t, string(body), qt.Equals, `{"error":"`+wantCode+`"}`+"\n")
	}

	t.Run("a valid signature creates a code, stored without the DNI", func(t *testing.T) {
		c := qt.New(t)
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, "01234567L", challenge))
		c.Assert(status, qt.Equals, http.StatusCreated, qt.Commentf("response: %s", body))
		var resp handlers.CertAssertionResponse
		c.Assert(json.Unmarshal(body, &resp), qt.IsNil)
		c.Assert(resp.CertCode, qt.Matches, `[A-Za-z0-9_-]{43}`)
		expiresAt, err := time.Parse(time.RFC3339, resp.ExpiresAt)
		c.Assert(err, qt.IsNil)
		c.Assert(resp.ExpiresAt, qt.Matches, `.*Z`)
		c.Assert(time.Until(expiresAt) > 4*time.Minute && time.Until(expiresAt) <= 5*time.Minute, qt.IsTrue)

		codeHash := sha256.Sum256([]byte(resp.CertCode))
		var stored bson.M
		c.Assert(testDB.DBClient.Database(testDBName).Collection("certAuthAssertions").
			FindOne(context.Background(), bson.M{"codeHash": codeHash[:]}).Decode(&stored), qt.IsNil)
		c.Assert(stored["participantId"], qt.Equals, ids[0])
		raw, err := json.Marshal(stored)
		c.Assert(err, qt.IsNil)
		c.Assert(string(raw), qt.Not(qt.Contains), "1234567")
	})

	t.Run("bad signatures", func(t *testing.T) {
		body, err := json.Marshal(newCertAssertionRequest(pid, testDNI(3), challenge))
		qt.Assert(t, err, qt.IsNil)
		otherKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
		for name, header := range map[string]string{
			"missing":     "",
			"malformed":   "v1=abc",
			"stale":       signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now().Add(-2*time.Minute)),
			"unknown key": signCertAssertion(testCertGatewayKey, "test-2", body, time.Now()),
			"wrong key":   signCertAssertion(otherKey, testCertGatewayKeyID, body, time.Now()),
		} {
			t.Run(name, func(t *testing.T) {
				status, resp := postCertAssertionRaw(t, body, header)
				expect(t, status, resp, http.StatusUnauthorized, "bad_signature")
			})
		}
	})

	t.Run("malformed requests", func(t *testing.T) {
		for _, req := range []*handlers.CertAssertionRequest{
			newCertAssertionRequest("not-an-id", testDNI(3), challenge),
			newCertAssertionRequest(bson.NewObjectID().Hex()[:23]+"G", testDNI(3), challenge),
			newCertAssertionRequest(pid, "12345678A", challenge), // invalid DNI
			newCertAssertionRequest(pid, testDNI(3), "short"),
		} {
			status, body := postCertAssertion(t, req)
			expect(t, status, body, http.StatusBadRequest, "bad_request")
		}
		body := []byte(`{"processId":`)
		status, resp := postCertAssertionRaw(t, body, signCertAssertion(testCertGatewayKey, testCertGatewayKeyID, body, time.Now()))
		expect(t, status, resp, http.StatusBadRequest, "bad_request")
	})

	t.Run("process unknown or not published", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(bson.NewObjectID().Hex(), testDNI(3), challenge))
		expect(t, status, body, http.StatusNotFound, "process_not_open")
		draft := minimalVotingProcessRequest(orgAddress)
		draft.Census = certCensus(nil, ids...)
		draftID := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token, draft,
			processesCreateEndpoint).ProcessID
		status, body = postCertAssertion(t, newCertAssertionRequest(draftID, testDNI(3), challenge))
		expect(t, status, body, http.StatusNotFound, "process_not_open")
	})

	t.Run("census without certAuth", func(t *testing.T) {
		typed, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}, MemberIDs: ids,
		}, 1)
		status, body := postCertAssertion(t, newCertAssertionRequest(typed, testDNI(3), challenge))
		expect(t, status, body, http.StatusConflict, "not_cert_census")
	})

	t.Run("anonymous census while anonymous voting is off", func(t *testing.T) {
		anonymous := certCensus(nil, ids...)
		anonymous.Anonymous = true
		anonPID, _ := publishCensusProcess(t, token, orgAddress, anonymous, 1)
		setDisableBlindCSP(t, true)
		status, body := postCertAssertion(t, newCertAssertionRequest(anonPID, testDNI(3), challenge))
		expect(t, status, body, http.StatusConflict, "anonymous_unavailable")
	})

	t.Run("not in census", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(9), challenge))
		expect(t, status, body, http.StatusNotFound, "not_in_census")
	})

	t.Run("zero weight", func(t *testing.T) {
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(2), challenge))
		expect(t, status, body, http.StatusForbidden, "zero_weight")
	})

	t.Run("rate limited after 10 an hour", func(t *testing.T) {
		for range csp.MaxHourlyCertAssertions {
			newCertCode(t, pid, testDNI(3), challenge)
		}
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(3), challenge))
		expect(t, status, body, http.StatusTooManyRequests, "rate_limited")
	})

	t.Run("feature off: a plain 404", func(t *testing.T) {
		setCertGatewayKeys(t, nil)
		status, body := postCertAssertion(t, newCertAssertionRequest(pid, testDNI(3), challenge))
		qt.Assert(t, status, qt.Equals, http.StatusNotFound)
		qt.Assert(t, body, qt.HasLen, 0)
	})
	c.Assert(testAPI.csp.CertAuthEnabled(), qt.IsTrue)
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./db/ -count=1 -run TestCertAuthAssertions`
Expected: `FAIL	github.com/vocdoni/saas-backend/db [build failed]` (`undefined: CertAuthAssertion`)

Step 2: implement
Create `db/cert_auth_assertions.go`:

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

// CertAuthAssertion is a certificate gateway's proof that a voter holds a certificate for a census
// participant's national ID, redeemable once at step 0 of that process with the code it was issued
// under and the PKCE verifier of CodeChallenge. It stores no document number. A TTL index on
// ExpiresAt removes it.
type CertAuthAssertion struct {
	ID            bson.ObjectID `bson:"_id"`
	CodeHash      []byte        `bson:"codeHash"` // sha256 of the code; the code itself is never stored
	ProcessID     bson.ObjectID `bson:"processId"`
	CensusID      string        `bson:"censusId"`
	ParticipantID string        `bson:"participantId"`
	CodeChallenge string        `bson:"codeChallenge"` // base64url(sha256(codeVerifier))
	Provider      string        `bson:"provider"`
	CertSHA256    string        `bson:"certSha256"`
	CreatedAt     time.Time     `bson:"createdAt"`
	ExpiresAt     time.Time     `bson:"expiresAt"`
	UsedAt        *time.Time    `bson:"usedAt,omitempty"`
}

// CreateCertAuthAssertion stores a new assertion.
func (ms *MongoStorage) CreateCertAuthAssertion(a *CertAuthAssertion) error {
	if len(a.CodeHash) == 0 || a.ProcessID.IsZero() || a.ParticipantID == "" {
		return ErrInvalidData
	}
	if a.ID.IsZero() {
		a.ID = bson.NewObjectID()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.certAuthAssertions.InsertOne(ctx, a); err != nil {
		return fmt.Errorf("failed to store cert auth assertion: %w", err)
	}
	return nil
}

// ConsumeCertAuthAssertion atomically marks as used, and returns, the unused and unexpired
// assertion of the process with the given code hash. Two concurrent calls never both get it. It
// returns ErrNotFound for a code that is unknown, used, expired or of another process.
func (ms *MongoStorage) ConsumeCertAuthAssertion(codeHash []byte, processID bson.ObjectID, now time.Time) (
	*CertAuthAssertion, error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"codeHash":  codeHash,
		"processId": processID,
		"usedAt":    nil,
		"expiresAt": bson.M{"$gt": now},
	}
	update := bson.M{"$set": bson.M{"usedAt": now}}
	a := &CertAuthAssertion{}
	err := ms.certAuthAssertions.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to consume cert auth assertion: %w", err)
	}
	return a, nil
}
```
Edit `db/mongo.go`, replace:

```go
	cspCounters         *mongo.Collection
	jobs                *mongo.Collection
```

with:

```go
	cspCounters         *mongo.Collection
	certAuthAssertions  *mongo.Collection
	jobs                *mongo.Collection
```
Edit `db/helpers.go`, replace:

```go
		"cspCounters":         &ms.cspCounters,
```

with:

```go
		"cspCounters":         &ms.cspCounters,
		"certAuthAssertions":  &ms.certAuthAssertions,
```
Edit `db/csp_counters.go`, replace:

```go
	CSPCounterVoterAuthFailures = "voterAuthFailures"
)
```

with:

```go
	CSPCounterVoterAuthFailures = "voterAuthFailures"
	// CSPCounterCertAssertions counts the certificate assertions the certificate gateway created for
	// one census participant on one voting process, per UTC hour.
	CSPCounterCertAssertions = "certAssertions"
)
```
Edit `db/csp_counters.go`, replace:

```go
	if kind == CSPCounterAuthFailures || kind == CSPCounterVoterAuthFailures {
```

with:

```go
	if kind == CSPCounterAuthFailures || kind == CSPCounterVoterAuthFailures || kind == CSPCounterCertAssertions {
```
Create `migrations/0038_cert_auth_assertions_indexes.go`:

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
	AddMigration(38, "cert_auth_assertions_indexes", upCertAuthAssertionsIndexes, downCertAuthAssertionsIndexes)
}

// certAuthAssertionsIndexNames are Mongo's default names of the indexes below, kept explicit so the
// down migration drops them deterministically.
var certAuthAssertionsIndexNames = []string{"codeHash_1", "expiresAt_1"}

// upCertAuthAssertionsIndexes indexes the certificate auth assertions: a unique code hash, which
// step 0 looks the assertion up by, and a TTL on expiresAt so expired assertions are removed.
func upCertAuthAssertionsIndexes(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection("certAuthAssertions").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "codeHash", Value: 1}}, Options: options.Index().SetUnique(true)},
		{Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	})
	if err != nil {
		return fmt.Errorf("failed to create certAuthAssertions indexes: %w", err)
	}
	return nil
}

// downCertAuthAssertionsIndexes drops those indexes, tolerating them being already gone.
func downCertAuthAssertionsIndexes(ctx context.Context, database *mongo.Database) error {
	if err := replaceIndex(ctx, database.Collection("certAuthAssertions"), certAuthAssertionsIndexNames, nil); err != nil {
		return fmt.Errorf("failed to drop certAuthAssertions indexes: %w", err)
	}
	return nil
}
```
Edit `csp/cert_gateway.go`, replace:

```go
	"strings"
	"time"
)
```

with:

```go
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)
```
Append to `csp/cert_gateway.go`:

```go

// MaxHourlyCertAssertions caps the certificate assertions created for one census participant on
// one voting process per UTC hour.
const MaxHourlyCertAssertions = 10

// ClaimCertAssertion spends one unit of the participant's hourly certificate assertion budget on
// the process, and reports false once it is spent.
func (c *CSP) ClaimCertAssertion(processID, participantID internal.HexBytes) (bool, error) {
	res, err := c.Storage.ClaimCSPCounter(db.CSPCounterCertAssertions,
		counterKey(participantID, processID), MaxHourlyCertAssertions)
	if err != nil {
		return false, fmt.Errorf("claiming cert assertion: %w", err)
	}
	return res.Granted, nil
}
```
Create `csp/handlers/cert_auth.go` (Task 7 appends the step-0 half):

```go
package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// certAssertionMaxBody bounds the assertion request body. The gateway signs the raw body, so it is
// read (and hashed) whole before being decoded.
const certAssertionMaxBody = 4 << 10

// certAssertionTTL is how long a certificate code can be redeemed at step 0.
const certAssertionTTL = 5 * time.Minute

// Error codes of POST /cert-auth/assertions. The gateway forwards to the browser only the
// voter-facing ones (process_not_open, not_cert_census, anonymous_unavailable, not_in_census,
// zero_weight, rate_limited); anything else becomes backend_unavailable there.
const (
	certErrBadRequest    = "bad_request"
	certErrBadSignature  = "bad_signature"
	certErrProcessClosed = "process_not_open"
	certErrNotCertCensus = "not_cert_census"
	certErrAnonymous     = "anonymous_unavailable"
	certErrNotInCensus   = "not_in_census"
	certErrZeroWeight    = "zero_weight"
	certErrRateLimited   = "rate_limited"
)

var (
	base64URL43 = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CertAssertionRequest is the body the certificate gateway signs and posts to
// POST /cert-auth/assertions once it has verified a voter's certificate.
type CertAssertionRequest struct {
	// ProcessID is the voting process id, 24 lowercase hex characters.
	ProcessID string `json:"processId"`
	// NationalID is the certificate's DNI or NIE, 9 characters as cert-check reports it.
	NationalID string `json:"nationalId"`
	// DocumentType is DNI or NIE.
	DocumentType string `json:"documentType"`
	// Provider and Profile are the cert-check provider and profile that accepted the certificate.
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
	// CertSHA256 is the lowercase hex SHA-256 of the certificate.
	CertSHA256 string `json:"certSha256"`
	// CodeChallenge is base64url(sha256(codeVerifier)), unpadded, 43 characters (PKCE S256).
	CodeChallenge string `json:"codeChallenge"`
}

// CertAssertionResponse is the 201 answer of POST /cert-auth/assertions.
type CertAssertionResponse struct {
	// CertCode is the single-use code the voter redeems at step 0 with the PKCE verifier.
	CertCode string `json:"certCode"`
	// ExpiresAt is when the code expires, RFC 3339 UTC.
	ExpiresAt string `json:"expiresAt"`
}

// CertAssertionError is every failure answer of POST /cert-auth/assertions.
type CertAssertionError struct {
	Error string `json:"error"`
}

// valid reports whether every field of the request is well formed.
func (req *CertAssertionRequest) valid() bool {
	return (req.DocumentType == "DNI" || req.DocumentType == "NIE") &&
		req.Provider != "" && len(req.Provider) <= 64 && len(req.Profile) <= 64 &&
		hex64.MatchString(req.CertSHA256) && base64URL43.MatchString(req.CodeChallenge)
}

// writeCertAssertionJSON writes an answer of POST /cert-auth/assertions.
func writeCertAssertionJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Warnw("failed to write cert assertion answer", "error", err)
	}
}

// writeCertAssertionError writes a failure answer and logs its outcome. The fields logged are ids
// only: never the national ID.
func writeCertAssertionError(w http.ResponseWriter, status int, code string, logFields ...any) {
	log.Infow("cert assertion refused", append([]any{"outcome", code}, logFields...)...)
	writeCertAssertionJSON(w, status, CertAssertionError{Error: code})
}

// CertAssertionHandler godoc
//
//	@Summary		Create a certificate auth assertion (certificate gateway only)
//	@Description	Called by the certificate gateway, server to server, once it has verified a voter's
//	@Description	certificate. The body is signed with the gateway's Ed25519 key in the X-Cert-Gateway-Signature
//	@Description	header: t=<unix seconds>,k=<keyId>,v1=<base64url signature> over
//	@Description	"cert-gateway-v1\n" + t + "\nPOST\n/cert-auth/assertions\n" + hex(sha256(body)), within 60 s of
//	@Description	the server clock. On success it stores a single-use code, valid for 5 minutes, that the voter
//	@Description	redeems at POST /processes/{processId}/auth/0 with the PKCE verifier of codeChallenge. At most
//	@Description	10 assertions per census participant per process per hour.
//	@Description	Without certificate gateway keys configured the route answers a plain 404.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			X-Cert-Gateway-Signature	header		string							true	"Gateway signature"
//	@Param			request						body		handlers.CertAssertionRequest	true	"Verified certificate identity"
//	@Success		201							{object}	handlers.CertAssertionResponse
//	@Failure		400							{object}	handlers.CertAssertionError	"bad_request"
//	@Failure		401							{object}	handlers.CertAssertionError	"bad_signature"
//	@Failure		403							{object}	handlers.CertAssertionError	"zero_weight"
//	@Failure		404							{object}	handlers.CertAssertionError	"process_not_open, not_in_census; or no body when the feature is off"
//	@Failure		409							{object}	handlers.CertAssertionError	"not_cert_census, anonymous_unavailable"
//	@Failure		429							{object}	handlers.CertAssertionError	"rate_limited"
//	@Router			/cert-auth/assertions [post]
func (c *CSPHandlers) CertAssertionHandler(w http.ResponseWriter, r *http.Request) {
	keys := c.csp.CertGatewayKeys()
	if len(keys) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, certAssertionMaxBody))
	if err != nil {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	header := r.Header.Get(csp.CertGatewaySignatureHeader)
	if err := csp.VerifyCertGatewaySignature(keys, header, body, time.Now()); err != nil {
		writeCertAssertionError(w, http.StatusUnauthorized, certErrBadSignature, "reason", err.Error())
		return
	}
	var req CertAssertionRequest
	if err := json.Unmarshal(body, &req); err != nil || !req.valid() {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	processID, err := bson.ObjectIDFromHex(req.ProcessID)
	if err != nil || processID.Hex() != req.ProcessID {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	logFields := []any{"provider", req.Provider, "processId", req.ProcessID}

	vp, err := c.mainDB.VotingProcess(processID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && !vp.Published) {
		writeCertAssertionError(w, http.StatusNotFound, certErrProcessClosed, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if !census.CertAuth {
		writeCertAssertionError(w, http.StatusConflict, certErrNotCertCensus, logFields...)
		return
	}
	if c.anonymousCensusRefused(census) {
		writeCertAssertionError(w, http.StatusConflict, certErrAnonymous, logFields...)
		return
	}
	nationalID, ok := internal.CanonicalNationalID(req.NationalID)
	if !ok {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest, logFields...)
		return
	}
	participant, err := c.mainDB.CensusParticipantByLoginHash(*census, db.OrgMember{NationalID: nationalID})
	if errors.Is(err, db.ErrNotFound) {
		writeCertAssertionError(w, http.StatusNotFound, certErrNotInCensus, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	logFields = append(logFields, "participantId", participant.ParticipantID)
	member, err := c.mainDB.OrgMember(census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		writeCertAssertionError(w, http.StatusNotFound, certErrNotInCensus, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if census.Weighted && member.Weight == 0 {
		writeCertAssertionError(w, http.StatusForbidden, certErrZeroWeight, logFields...)
		return
	}
	granted, err := c.csp.ClaimCertAssertion(internal.HexBytes(processID[:]), internal.HexBytes(member.ID[:]))
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if !granted {
		writeCertAssertionError(w, http.StatusTooManyRequests, certErrRateLimited, logFields...)
		return
	}

	code := make([]byte, 32)
	if _, err := rand.Read(code); err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	certCode := base64.RawURLEncoding.EncodeToString(code)
	codeHash := sha256.Sum256([]byte(certCode))
	now := time.Now().UTC()
	assertion := &db.CertAuthAssertion{
		CodeHash:      codeHash[:],
		ProcessID:     processID,
		CensusID:      census.ID.Hex(),
		ParticipantID: participant.ParticipantID,
		CodeChallenge: req.CodeChallenge,
		Provider:      req.Provider,
		CertSHA256:    req.CertSHA256,
		CreatedAt:     now,
		ExpiresAt:     now.Add(certAssertionTTL),
	}
	if err := c.mainDB.CreateCertAuthAssertion(assertion); err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	log.Infow("cert assertion created", append([]any{"outcome", "created"}, logFields...)...)
	writeCertAssertionJSON(w, http.StatusCreated, &CertAssertionResponse{
		CertCode:  certCode,
		ExpiresAt: assertion.ExpiresAt.Format(time.RFC3339),
	})
}

// certAssertionFailed answers an internal failure: a plain 500, which the gateway turns into
// backend_unavailable.
func (*CSPHandlers) certAssertionFailed(w http.ResponseWriter, err error, logFields []any) {
	log.Warnw("cert assertion failed", append([]any{"error", err.Error()}, logFields...)...)
	w.WriteHeader(http.StatusInternalServerError)
}
```
Edit `api/routes.go`, replace:

```go
	processesWeightEndpoint     = "/processes/{processId}/weight"
)
```

with:

```go
	processesWeightEndpoint     = "/processes/{processId}/weight"
	// POST /cert-auth/assertions — the certificate gateway, server to server and signed, hands over
	// a voter's verified national ID and gets a single-use code for step 0 (404 while no gateway key
	// is configured)
	certAuthAssertionsEndpoint = "/cert-auth/assertions"
)
```
Edit `api/api.go`, replace:

```go
		handle(r, http.MethodPost, processesSignInfoEndpoint, cspHandlers.ProcessSignInfoHandler)
	})
```

with:

```go
		handle(r, http.MethodPost, processesSignInfoEndpoint, cspHandlers.ProcessSignInfoHandler)
		handle(r, http.MethodPost, certAuthAssertionsEndpoint, cspHandlers.CertAssertionHandler)
	})
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./db/ -count=1 -run 'TestCertAuthAssertions|TestMigrations$' && go test ./api/ -count=1 -run 'TestCertAssertions|TestCertCensusValidation' -v 2>&1 | grep -E '^\s*--- |^(ok|FAIL)'`
Expected: `ok  	github.com/vocdoni/saas-backend/db`; `--- PASS: TestCertAssertions` with subtests `a_valid_signature_creates_a_code,_stored_without_the_DNI`, `bad_signatures` (5), `malformed_requests`, `process_unknown_or_not_published`, `census_without_certAuth`, `anonymous_census_while_anonymous_voting_is_off`, `not_in_census`, `zero_weight`, `rate_limited_after_10_an_hour`, `feature_off:_a_plain_404`; `ok  	github.com/vocdoni/saas-backend/api`

Step 3: commit
`git add -- db/cert_auth_assertions.go db/cert_auth_assertions_test.go db/mongo.go db/helpers.go db/csp_counters.go migrations/0038_cert_auth_assertions_indexes.go csp/cert_gateway.go csp/handlers/cert_auth.go api/routes.go api/api.go api/cert_assertions_test.go && git -c commit.gpgsign=false commit -m "feat(csp): accept signed certificate assertions from the gateway" -- db/cert_auth_assertions.go db/cert_auth_assertions_test.go db/mongo.go db/helpers.go db/csp_counters.go migrations/0038_cert_auth_assertions_indexes.go csp/cert_gateway.go csp/handlers/cert_auth.go api/routes.go api/api.go api/cert_assertions_test.go`

Interfaces this task provides to later tasks: `db.CertAuthAssertion`, `(*MongoStorage).CreateCertAuthAssertion(*CertAuthAssertion) error`, `(*MongoStorage).ConsumeCertAuthAssertion(codeHash []byte, processID bson.ObjectID, now time.Time) (*CertAuthAssertion, error)` (`db.ErrNotFound` for unknown/used/expired/foreign); `db.CSPCounterCertAssertions`; `(*csp.CSP).ClaimCertAssertion(processID, memberID internal.HexBytes) (bool, error)`, `csp.MaxHourlyCertAssertions`; `handlers.CertAssertionRequest`, `CertAssertionResponse`, `CertAssertionError`, `base64URL43`; migration version 38; API test helpers `testCertPKCE`, `newCertAssertionRequest`, `postCertAssertionRaw`, `signCertAssertion`, `postCertAssertion`, `newCertCode(t, pid, nationalID, challenge) string`.

### Task 7: Step 0 with a certificate code (PKCE) and `voterRef`
Model: strong   Repo: saas-backend   Files: `csp/handlers/types.go`, `csp/handlers/auth.go`, `csp/handlers/auth_test.go`, `csp/handlers/cert_auth.go`, `csp/handlers/processes.go`, `api/cert_auth_step0_test.go` (new)
Goal: C2.5 — a cert census takes `{certCode, codeVerifier[, email|phone]}` only, consumes the assertion atomically, checks PKCE in constant time, then runs the existing contact/weight/`AuthToken` flow and returns `voterRef`; any other census refuses `certCode` (AC5, AC11).

Step 1: write the failing test
Create `api/cert_auth_step0_test.go`:

```go
package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

// certStep0 redeems a certificate code at step 0 and returns the raw answer.
func certStep0(t *testing.T, pid string, req *handlers.AuthRequest) ([]byte, int) {
	t.Helper()
	return testRequest(t, http.MethodPost, "", req, processAuthURL(pid, "0"))
}

// captureAPILogs runs fn with the global logger writing to a file, and returns what it logged.
func captureAPILogs(t *testing.T, fn func()) string {
	t.Helper()
	prev := *log.Logger()
	t.Cleanup(func() { *log.Logger() = prev })
	logFile := filepath.Join(t.TempDir(), "api.log")
	f, err := os.Create(logFile)
	qt.Assert(t, err, qt.IsNil)
	t.Cleanup(func() { _ = f.Close() })
	stdout := os.Stdout
	os.Stdout = f
	log.Init("debug", "stdout", nil)
	os.Stdout = stdout
	fn()
	out, err := os.ReadFile(logFile)
	qt.Assert(t, err, qt.IsNil)
	return string(out)
}

// TestCertAuthStep0 (AC5, AC11) covers step 0 with a certificate code.
func TestCertAuthStep0(t *testing.T) {
	c := qt.New(t)
	enableCertAuth(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{
			MemberNumber: "S-1", Name: "Alice", Surname: "Uno", NationalID: "1234567-l",
			Email: "alice-cert@example.com", Phone: "+34611111111",
		},
		apicommon.OrgMember{
			MemberNumber: "S-2", Name: "Bob", Surname: "Dos", NationalID: testDNI(2),
			Email: "bob-cert@example.com", Phone: "+34622222222",
		},
	)
	ids := memberIDs(members)
	alice, bob := "01234567L", testDNI(2)
	authOnly, authOnlyElections := publishCensusProcess(t, token, orgAddress, certCensus(nil, ids...), 1)
	withEmail, _ := publishCensusProcess(t, token, orgAddress,
		certCensus(db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}, ids...), 1)
	typed, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}, MemberIDs: ids,
	}, 1)

	login := func(t *testing.T, pid, nationalID string, contact *handlers.AuthRequest) handlers.AuthResponse {
		t.Helper()
		verifier, challenge := testCertPKCE(t)
		req := &handlers.AuthRequest{CertCode: newCertCode(t, pid, nationalID, challenge), CodeVerifier: verifier}
		if contact != nil {
			req.Email, req.Phone = contact.Email, contact.Phone
		}
		body, code := certStep0(t, pid, req)
		qt.Assert(t, code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", body))
		var resp handlers.AuthResponse
		qt.Assert(t, json.Unmarshal(body, &resp), qt.IsNil)
		qt.Assert(t, resp.AuthToken, qt.Not(qt.HasLen), 0)
		return resp
	}

	t.Run("auth-only: a verified token that signs, and a stable voterRef", func(t *testing.T) {
		c := qt.New(t)
		first := login(t, authOnly, alice, nil)
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		testCSPSign(t, authOnly, first.AuthToken, authOnlyElections[0], voter.Address().Bytes())

		c.Assert(first.VoterRef, qt.Matches, `[0-9a-f]{64}`)
		sum := sha256.Sum256([]byte("vocdoni-voter-ref:" + authOnly + ":" + ids[0]))
		c.Assert(first.VoterRef, qt.Equals, hex.EncodeToString(sum[:]))
		c.Assert(login(t, authOnly, alice, nil).VoterRef, qt.Equals, first.VoterRef)
		c.Assert(login(t, authOnly, bob, nil).VoterRef, qt.Not(qt.Equals), first.VoterRef)
	})

	t.Run("email 2FA: an OTP, then step 1", func(t *testing.T) {
		resp := login(t, withEmail, bob, &handlers.AuthRequest{Email: members[1].Email})
		verifyProcessCSP(t, withEmail, members[1].Email, resp.AuthToken)
	})

	t.Run("bad codes and verifiers answer 40802", func(t *testing.T) {
		c := qt.New(t)
		verifier, challenge := testCertPKCE(t)
		failed := func(req *handlers.AuthRequest, pid string) {
			t.Helper()
			err := postProcessAuth0AndExpectError(t, pid, req)
			c.Assert(err.Code, qt.Equals, errors.ErrCSPAuthFailed.Code)
		}
		otherVerifier, _ := testCertPKCE(t)
		failed(&handlers.AuthRequest{CertCode: newCertCode(t, authOnly, alice, challenge), CodeVerifier: otherVerifier}, authOnly)

		reused := newCertCode(t, authOnly, alice, challenge)
		postProcessAuth0(t, authOnly, &handlers.AuthRequest{CertCode: reused, CodeVerifier: verifier})
		failed(&handlers.AuthRequest{CertCode: reused, CodeVerifier: verifier}, authOnly)

		expired := newCertCode(t, authOnly, alice, challenge)
		codeHash := sha256.Sum256([]byte(expired))
		_, err := testDB.DBClient.Database(testDBName).Collection("certAuthAssertions").UpdateOne(context.Background(),
			bson.M{"codeHash": codeHash[:]}, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Second)}})
		c.Assert(err, qt.IsNil)
		failed(&handlers.AuthRequest{CertCode: expired, CodeVerifier: verifier}, authOnly)

		otherProcess := newCertCode(t, withEmail, alice, challenge)
		failed(&handlers.AuthRequest{CertCode: otherProcess, CodeVerifier: verifier}, authOnly)

		wrongContact := newCertCode(t, withEmail, alice, challenge)
		failed(&handlers.AuthRequest{CertCode: wrongContact, CodeVerifier: verifier, Email: "other@example.com"}, withEmail)
		failed(&handlers.AuthRequest{CertCode: wrongContact, CodeVerifier: verifier, Email: members[0].Email}, withEmail)
	})

	t.Run("typed fields or no code on a cert census: 40005, and the code stays unused", func(t *testing.T) {
		c := qt.New(t)
		verifier, challenge := testCertPKCE(t)
		code := newCertCode(t, authOnly, alice, challenge)
		for _, req := range []*handlers.AuthRequest{
			{CertCode: code, CodeVerifier: verifier, NationalID: alice},
			{CertCode: code, CodeVerifier: verifier, Name: "Alice"},
			{CertCode: code},
			{CodeVerifier: verifier},
			{NationalID: alice},
		} {
			err := postProcessAuth0AndExpectError(t, authOnly, req)
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
		}
		postProcessAuth0(t, authOnly, &handlers.AuthRequest{CertCode: code, CodeVerifier: verifier})
	})

	t.Run("a code on a census without certAuth: 40005", func(t *testing.T) {
		verifier, challenge := testCertPKCE(t)
		code := newCertCode(t, authOnly, alice, challenge)
		err := postProcessAuth0AndExpectError(t, typed, &handlers.AuthRequest{CertCode: code, CodeVerifier: verifier})
		qt.Assert(t, err.Code, qt.Equals, errors.ErrInvalidUserData.Code)
	})

	t.Run("two concurrent redemptions: exactly one succeeds", func(t *testing.T) {
		verifier, challenge := testCertPKCE(t)
		req := &handlers.AuthRequest{CertCode: newCertCode(t, authOnly, bob, challenge), CodeVerifier: verifier}
		var wg sync.WaitGroup
		codes := make([]int, 2)
		for i := range codes {
			wg.Go(func() { _, codes[i] = certStep0(t, authOnly, req) })
		}
		wg.Wait()
		qt.Assert(t, codes, qt.ContentEquals, []int{http.StatusOK, errors.ErrCSPAuthFailed.HTTPstatus})
	})

	t.Run("no PII in the logs of an assertion and a step 0", func(t *testing.T) {
		c := qt.New(t)
		out := captureAPILogs(t, func() {
			login(t, withEmail, alice, &handlers.AuthRequest{Email: members[0].Email})
		})
		c.Assert(out, qt.Contains, "cert assertion created")
		for _, pii := range []string{"1234567", "Alice", "Uno", members[0].Email, "611111111"} {
			c.Assert(out, qt.Not(qt.Contains), pii)
		}
	})
	c.Assert(testAPI.csp.CertAuthEnabled(), qt.IsTrue)
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test ./api/ -count=1 -run TestCertAuthStep0`
Expected: `FAIL	github.com/vocdoni/saas-backend/api [build failed]` (`unknown field CertCode in struct literal of type handlers.AuthRequest`)

Step 2: implement
Edit `csp/handlers/types.go`, replace:

```go
	Password string `json:"password,omitempty"`
}

// AuthResendRequest defines the payload for the 2FA resend request.
```

with:

```go
	Password string `json:"password,omitempty"`
	// CertCode is the single-use code the certificate gateway returned to the voting page, for a
	// census with certAuth (43 base64url characters). It replaces every typed login field.
	CertCode string `json:"certCode,omitempty"`
	// CodeVerifier is the PKCE verifier (43 to 128 base64url characters) whose
	// base64url(sha256(...)) the voting page sent to the certificate gateway as codeChallenge.
	CodeVerifier string `json:"codeVerifier,omitempty"`
}

// AuthResendRequest defines the payload for the 2FA resend request.
```
Edit `csp/handlers/types.go`, replace:

```go
	Weight    internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
}

// AuthRequest defines the payload for finishing the authentication process.
```

with:

```go
	Weight    internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
	// VoterRef identifies the voter of a certificate census on the process, the same on every login:
	// lowercase hex of sha256("vocdoni-voter-ref:" + process id + ":" + member id). Step 0 only.
	VoterRef string `json:"voterRef,omitempty"`
}

// AuthRequest defines the payload for finishing the authentication process.
```
Apply this diff to `csp/handlers/auth.go` (`git apply`); the rest of the old `authFirstStep` body, from
`lang := apicommon.NotificationLang(r.Context(), org)` to its end, stays byte for byte as the body of
`typedAuthFirstStep`:

```diff
diff --git a/csp/handlers/auth.go b/csp/handlers/auth.go
index d2a6b74..cedd9cb 100644
--- a/csp/handlers/auth.go
+++ b/csp/handlers/auth.go
@@ -35,10 +35,11 @@ func (c *CSPHandlers) handleAuthStep(w http.ResponseWriter, r *http.Request,
 	step int, anchorID internal.HexBytes, censusID string,
 ) {
 	var authToken internal.HexBytes
+	var voterRef string
 	var err error
 
 	if step == 0 {
-		authToken, err = c.authFirstStep(r, anchorID, censusID)
+		authToken, voterRef, err = c.authFirstStep(r, anchorID, censusID)
 	} else {
 		authToken, err = c.authSecondStep(r)
 	}
@@ -53,7 +54,7 @@ func (c *CSPHandlers) handleAuthStep(w http.ResponseWriter, r *http.Request,
 		return
 	}
 
-	apicommon.HTTPWriteJSON(w, &AuthResponse{AuthToken: authToken})
+	apicommon.HTTPWriteJSON(w, &AuthResponse{AuthToken: authToken, VoterRef: voterRef})
 }
 
 // validateContactInfo checks if at least one contact method is provided
@@ -267,36 +268,59 @@ func passwordCheckFailed(err error) error {
 // unknown participant pays for a password check too (see unknownParticipant), so the two cannot be
 // told apart by their timing either. When no argon2 slot frees up in time, both answer
 // ErrServerBusy, which spends no budget.
+//
+// A census with CertAuth is authenticated with a certificate code instead (certAuthFirstStep), which
+// also returns the voter's voterRef; any other census refuses a request carrying one.
 func (c *CSPHandlers) authFirstStep(
 	r *http.Request,
 	anchorID internal.HexBytes,
 	censusID string,
-) (token internal.HexBytes, err error) {
+) (token internal.HexBytes, voterRef string, err error) {
 	// Parse request
 	var req AuthRequest
 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
-		return nil, errors.ErrMalformedBody.Withf("invalid JSON request")
+		return nil, "", errors.ErrMalformedBody.Withf("invalid JSON request")
 	}
 
 	// Get census and org information first (needed for validation)
 	census, err := c.mainDB.Census(censusID)
 	if err != nil {
 		if err == db.ErrNotFound {
-			return nil, errors.ErrCensusNotFound
+			return nil, "", errors.ErrCensusNotFound
 		}
-		return nil, errors.ErrGenericInternalServerError.WithErr(err)
+		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
 	}
 	// before any budget is claimed or challenge sent: the token could not be blind-signed anyway
 	if c.anonymousCensusRefused(census) {
-		return nil, errors.ErrInvalidData.With(csp.AnonymousVotingUnavailable)
+		return nil, "", errors.ErrInvalidData.With(csp.AnonymousVotingUnavailable)
 	}
 
 	org, err := c.mainDB.Organization(census.OrgAddress)
 	if err != nil {
 		if err == db.ErrNotFound {
-			return nil, errors.ErrOrganizationNotFound
+			return nil, "", errors.ErrOrganizationNotFound
 		}
-		return nil, errors.ErrGenericInternalServerError.WithErr(err)
+		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
+	}
+
+	if census.CertAuth {
+		return c.certAuthFirstStep(r, &req, census, org, anchorID)
 	}
+	if req.CertCode != "" {
+		return nil, "", errors.ErrInvalidUserData.Withf("this census does not use certificate authentication")
+	}
+	token, err = c.typedAuthFirstStep(r, &req, census, org, anchorID)
+	return token, "", err
+}
+
+// typedAuthFirstStep is step 0 of a census whose voters type their login fields (see authFirstStep).
+func (c *CSPHandlers) typedAuthFirstStep(
+	r *http.Request,
+	in *AuthRequest,
+	census *db.Census,
+	org *db.Organization,
+	anchorID internal.HexBytes,
+) (token internal.HexBytes, err error) {
+	req := *in
 
 	lang := apicommon.NotificationLang(r.Context(), org)
```

Apply to `csp/handlers/auth_test.go`:

```diff
diff --git a/csp/handlers/auth_test.go b/csp/handlers/auth_test.go
index f1ffe3f..106f9f1 100644
--- a/csp/handlers/auth_test.go
+++ b/csp/handlers/auth_test.go
@@ -68,7 +68,7 @@ func TestAuthFirstStepUnknownParticipantPasswordWork(t *testing.T) {
 		body, err := json.Marshal(&AuthRequest{MemberNumber: memberNumber, Password: password})
 		c.Assert(err, qt.IsNil)
 		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
-		_, err = h.authFirstStep(r, internal.HexBytes{0x01}, censusID)
+		_, _, err = h.authFirstStep(r, internal.HexBytes{0x01}, censusID)
 		apiErr, ok := err.(errors.Error)
 		c.Assert(ok, qt.IsTrue, qt.Commentf("error: %v", err))
 		return apiErr.Code, checks
```

Apply to `csp/handlers/processes.go` (swagger description of `ProcessAuthHandler`):

```diff
diff --git a/csp/handlers/processes.go b/csp/handlers/processes.go
index 1365d32..359bc28 100644
--- a/csp/handlers/processes.go
+++ b/csp/handlers/processes.go
@@ -122,6 +122,10 @@ func unvotableElection(vp *db.VotingProcess, q *db.VotingProcessQuestion) *error
 //	@Description	40017 with data.retryAfter.
 //	@Description	Step 0 for an anonymous census is refused (400, 40037, "anonymous voting is temporarily
 //	@Description	unavailable") while the service has anonymous voting off.
+//	@Description	For a census with certAuth, step 0 takes certCode and codeVerifier (plus the email or phone when
+//	@Description	the census has 2FA) and no typed login field, else 40005; an unknown, expired, used or foreign
+//	@Description	code, or a wrong verifier, answers 40802 and the code is spent. The answer carries voterRef. A
+//	@Description	certCode sent to any other census answers 40005.
 //	@Tags			processes
 //	@Accept			json
 //	@Produce		json
```
Edit `csp/handlers/cert_auth.go`, replace:

```go
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
```

with:

```go
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
```
Edit `csp/handlers/cert_auth.go`, replace:

```go
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
```

with:

```go
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
```
Append to `csp/handlers/cert_auth.go`:

```go

// base64URLVerifier is a PKCE code verifier: 43 to 128 base64url characters.
var base64URLVerifier = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

// voterRef is the stable reference of a voter of a certificate census on a process (AuthResponse).
func voterRef(processID, memberID string) string {
	sum := sha256.Sum256([]byte("vocdoni-voter-ref:" + processID + ":" + memberID))
	return hex.EncodeToString(sum[:])
}

// certAuthFirstStep is step 0 of a census with CertAuth. The request carries the certificate code
// the gateway returned to the voting page and the PKCE verifier of its code challenge, plus the
// email or phone when the census has 2FA, and no typed login field.
//
// The request is checked in full before the code is consumed, so a malformed one spends nothing.
// The code is then consumed atomically, and is spent from then on whatever the outcome: a
// verifier, contact or weight that fails sends the voter through the certificate again. An unknown,
// expired, used or foreign code and a wrong verifier all answer the bare ErrCSPAuthFailed. The
// failed-attempt budget is the participant's (its login hash), claimed before the contact check as
// in the typed flow. The rest is the typed flow's: contact check, weight, then csp.AuthToken.
func (c *CSPHandlers) certAuthFirstStep(
	r *http.Request,
	req *AuthRequest,
	census *db.Census,
	org *db.Organization,
	anchorID internal.HexBytes,
) (token internal.HexBytes, ref string, err error) {
	if !base64URL43.MatchString(req.CertCode) || !base64URLVerifier.MatchString(req.CodeVerifier) ||
		req.Name != "" || req.Surname != "" || req.MemberNumber != "" || req.NationalID != "" ||
		req.BirthDate != "" || req.Password != "" {
		return nil, "", errors.ErrInvalidUserData.Withf("a certificate login takes certCode and codeVerifier only")
	}
	if err := validateAuthRequest(req, census); err != nil {
		return nil, "", err
	}
	var phone *voterPhone
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) {
		if phone, err = parseVoterPhone(r.Context(), req.Phone, org); err != nil {
			return nil, "", phoneParseFailed(err)
		}
	}
	lang := apicommon.NotificationLang(r.Context(), org)

	var processID bson.ObjectID
	copy(processID[:], anchorID)
	codeHash := sha256.Sum256([]byte(req.CertCode))
	assertion, err := c.mainDB.ConsumeCertAuthAssertion(codeHash[:], processID, time.Now())
	if errors.Is(err, db.ErrNotFound) {
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	verifierHash := sha256.Sum256([]byte(req.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(assertion.CodeChallenge)) != 1 ||
		assertion.CensusID != census.ID.Hex() {
		return nil, "", errors.ErrCSPAuthFailed
	}

	participant, err := c.mainDB.CensusParticipant(census.ID.Hex(), assertion.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	claim, err := c.csp.ClaimAuthAttempt(anchorID, participant.LoginHash, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if !errors.Is(err, errors.ErrCSPAuthFailed) {
			c.csp.ReleaseAuthAttempt(claim)
		}
	}()

	member, err := c.mainDB.OrgMember(census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("census participant without org member", "participantID", participant.ParticipantID)
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	toDestinations, challengeType, err := determineContactMethod(census, req, phone, member)
	if err != nil {
		return nil, "", err
	}
	if census.Weighted && member.Weight == 0 {
		return nil, "", errors.ErrZeroWeightVoter
	}
	name, logo := orgNameAndLogo(org)
	token, err = c.csp.AuthToken(anchorID, internal.HexBytesFromString(member.ID.Hex()), toDestinations,
		challengeType, lang, name, logo, org.Address)
	if err != nil {
		return nil, "", err
	}
	return token, voterRef(processID.Hex(), member.ID.Hex()), nil
}
```
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && gofumpt -l csp api; go vet ./csp/... ./api/ && go test ./csp/... -count=1 && go test ./api/ -count=1 -run 'TestCert|TestProcessCSP$|TestCSPAuth' -v 2>&1 | grep -E '^--- |^(ok|FAIL)'`
Expected: no gofumpt output; `ok` for `csp` and `csp/handlers`; `--- PASS: TestCertAuthStep0` (subtests: auth-only token that signs and stable voterRef, email 2FA then step 1, bad codes and verifiers 40802, typed fields 40005 with the code unused, code on a non-cert census 40005, two concurrent redemptions one success, no PII in the logs), `--- PASS: TestCertAssertions`, `--- PASS: TestCertCensusValidation`, the existing `TestProcessCSP`/`TestCSPAuth*` still `PASS`, `ok  	github.com/vocdoni/saas-backend/api`.

Step 3: commit
`git add -- csp/handlers/types.go csp/handlers/auth.go csp/handlers/auth_test.go csp/handlers/cert_auth.go csp/handlers/processes.go api/cert_auth_step0_test.go && git -c commit.gpgsign=false commit -m "feat(csp): authenticate certificate census voters with a gateway code" -- csp/handlers/types.go csp/handlers/auth.go csp/handlers/auth_test.go csp/handlers/cert_auth.go csp/handlers/processes.go api/cert_auth_step0_test.go`

Interfaces this task provides to later tasks: step-0 body fields `certCode`, `codeVerifier`; response field `voterRef` (lowercase hex sha256 of `"vocdoni-voter-ref:" + processId + ":" + memberId`); error codes 40005 (shape) and 40802 (bad code/verifier).

### Task 8: Swagger, full backend suite and lint
Model: fast   Repo: saas-backend   Files: `docs/swagger.yaml`
Goal: C2.7 — the swagger spec covers the new fields and route; the branch is green.

Step 1: regenerate (needs `swag` v1.16.5 on `PATH`; the script installs it into `$(go env GOPATH)/bin`)
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && PATH="$(go env GOPATH)/bin:$PATH" make swagger && git diff --stat docs/swagger.yaml`
Expected: `Swagger documentation generated successfully at docs/swagger.yaml` and about 135 added lines (`/cert-auth/assertions`, `handlers.CertAssertionRequest`, `certAuth`, `certCode`, `codeVerifier`, `voterRef`). `git status` lists no other changed file.

Step 2: full checks
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && go test -failfast -timeout=45m ./... -count=1 2>&1 | grep -E '^(ok|FAIL|---)' | grep -v '^--- PASS'`
Expected: `ok` for every package (the `api` package takes about 6–7 minutes), no `FAIL`.
Run: `cd /home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && golangci-lint run --new-from-rev=origin/main ./... && ./scripts/check-qt-patterns.sh && go mod tidy -diff`
Expected: `0 issues.`, `No qt anti-patterns found!`, and no tidy diff. (golangci-lint must be v2.15.0 built with Go ≥ 1.27; one built with Go 1.26 refuses to load the config — install with `GOBIN=~/.cache/saas-cert/bin go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`.)

Step 3: commit
`git -c commit.gpgsign=false commit -m "docs(swagger): document certificate voter authentication" -- docs/swagger.yaml`

Interfaces this task provides to later tasks: the backend wire contract Part B and C build on (assertion route, step-0 fields).


## Part B — cert-gateway (C1, D8)

### Task 9: cert-gateway repository and request signing (shared vectors)
Model: strong   Repo: cert-gateway (`/home/lucasmenendez/cert-gateway`, new, branch main)   Files: `go.mod`, `.gitignore`, `internal/gateway/signing.go`, `internal/gateway/signing_test.go`
Goal: C1 "Request to the backend" — the Ed25519 signature over `"cert-gateway-v1\n" + t + "\nPOST\n/cert-auth/assertions\n" + hex(sha256(body))`, checked against Task 4's vectors verbatim (AC7 shared vectors).

Step 0: create the repository (local only; the GitHub repository is an owner-OK step)
Run: `mkdir -p /home/lucasmenendez/cert-gateway && cd /home/lucasmenendez/cert-gateway && git init -b main`
Expected: `Initialized empty Git repository in /home/lucasmenendez/cert-gateway/.git/`

Create `go.mod`:

```go
module github.com/vocdoni/saas-cert-gateway

go 1.27
```

Create `.gitignore`:

```text
# devca files and certificates never go in the repository (they live under ~/.cache/cert-gateway)
*.pem
*.key
*.p12
```
Step 1: write the failing test

Create `internal/gateway/signing_test.go`:

```go
package gateway

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
)

// Shared certificate gateway signature test vectors, verbatim from saas-backend's
// csp/cert_gateway_test.go: the backend verifies exactly these.
const (
	vectorSeed      = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	vectorPublicKey = "A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg="
	vectorKeyID     = "test-1"
	vectorTime      = int64(1767225600) // 2026-01-01T00:00:00Z
	vectorBody      = `{"processId":"0123456789abcdef01234567","nationalId":"12345678Z","documentType":"DNI",` +
		`"provider":"fnmt","profile":"fnmt-citizen-g2",` +
		`"certSha256":"abababababababababababababababababababababababababababababababab",` +
		`"codeChallenge":"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}`
	vectorMessage = "cert-gateway-v1\n1767225600\nPOST\n/cert-auth/assertions\n" +
		"4c6ce4c5f31980d9aa0819615924fc66ab54794461d5a4602fda03144362a1ff"
	vectorHeader = "t=1767225600,k=test-1,v1=" +
		"MIU3KNz-CoWDXqKlIFuxleuc_y5cG83YtE6v84OAb9mfZRp4jDnnPXOSTaVn666rdPe7SERtWnL2uGi6qH9QAg"
)

// vectorKey is the private key of the vector seed.
func vectorKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	seed, err := base64.StdEncoding.DecodeString(vectorSeed)
	if err != nil {
		t.Fatal(err)
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestSignatureVectors(t *testing.T) {
	key := vectorKey(t)
	if got := base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)); got != vectorPublicKey {
		t.Fatalf("public key %s, want %s", got, vectorPublicKey)
	}
	if got := string(SignedMessage(vectorTime, []byte(vectorBody))); got != vectorMessage {
		t.Fatalf("message %q, want %q", got, vectorMessage)
	}
	if got := Sign(key, vectorKeyID, vectorTime, []byte(vectorBody)); got != vectorHeader {
		t.Fatalf("header %s, want %s", got, vectorHeader)
	}
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test ./internal/gateway/`
Expected: `FAIL	github.com/vocdoni/saas-cert-gateway/internal/gateway [build failed]` (`undefined: SignedMessage`)

Step 2: implement
Create `internal/gateway/signing.go`:

```go
// Package gateway is the certificate gateway: it verifies a voter's Spanish personal certificate
// with cert-check, posts the proven identity to the backend that serves the voting page, signed,
// and redirects the browser back with a single-use code.
package gateway

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strconv"
)

// AssertionPath is the backend route the gateway posts assertions to. The signature covers it as a
// literal, never read from a request.
const AssertionPath = "/cert-auth/assertions"

// SignatureHeader carries the gateway's signature of an assertion request.
const SignatureHeader = "X-Cert-Gateway-Signature"

// SignedMessage returns the bytes signed for an assertion request body sent at unix time t:
// "cert-gateway-v1\n" + t + "\nPOST\n/cert-auth/assertions\n" + hex(sha256(body)).
func SignedMessage(t int64, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte("cert-gateway-v1\n" + strconv.FormatInt(t, 10) + "\nPOST\n" + AssertionPath + "\n" +
		hex.EncodeToString(sum[:]))
}

// Sign returns the X-Cert-Gateway-Signature value of body at unix time t with key, named keyID:
// "t=<unix seconds>,k=<keyId>,v1=<base64url(signature), no padding>".
func Sign(key ed25519.PrivateKey, keyID string, t int64, body []byte) string {
	sig := ed25519.Sign(key, SignedMessage(t, body))
	return "t=" + strconv.FormatInt(t, 10) + ",k=" + keyID + ",v1=" + base64.RawURLEncoding.EncodeToString(sig)
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go vet ./... && go test ./internal/gateway/ -run TestSignatureVectors -v 2>&1 | grep -E '^(--- |ok)'`
Expected: `--- PASS: TestSignatureVectors`, `ok  	github.com/vocdoni/saas-cert-gateway/internal/gateway`

Step 3: commit
`git add -- go.mod .gitignore internal/gateway/signing.go internal/gateway/signing_test.go && git -c commit.gpgsign=false commit -m "feat(gateway): sign backend assertions with ed25519" -- go.mod .gitignore internal/gateway/signing.go internal/gateway/signing_test.go`

Interfaces this task provides to later tasks: package `gateway` with `AssertionPath`, `SignatureHeader`, `SignedMessage(t int64, body []byte) []byte`, `Sign(key ed25519.PrivateKey, keyID string, t int64, body []byte) string`; test helpers `vectorSeed`, `vectorKey(t)`, `vectorKeyID`.

### Task 10: Gateway configuration and BACKENDS validation
Model: standard   Repo: cert-gateway   Files: `internal/gateway/config.go`, `internal/gateway/config_test.go`
Goal: C1 "Configuration", "Backends", D8 static mode — every variable, canonical origins, and each BACKENDS startup failure naming the entry without key material (AC7 config bullets; AC12 "refuses an http://localhost origin"). D8's two devca exceptions (owner, 2026-11-16), both through `allowLocalhost`, which only a devca build passes: an origin `http://localhost:<port>` and a backend `url` `http://localhost:<port>`; a binary without the tag rejects both at startup.

Step 1: write the failing test
Create `internal/gateway/config_test.go`:

```go
package gateway

import (
	"encoding/base64"
	"strings"
	"testing"
)

// testEnvVars is a valid environment: two backends, static certificate.
func testEnvVars() map[string]string {
	return map[string]string{
		"DOMAIN":          "Cert-Dev.vocdoni.net",
		"TLS_CERT_FILE":   "/certs/cert.pem",
		"TLS_KEY_FILE":    "/certs/key.pem",
		"SIGNING_KEY_DEV": vectorSeed,
		"SIGNING_KEY_STG": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32))),
		"BACKENDS": `[{"name":"dev","url":"https://saas-api-dev.vocdoni.net","keyId":"dev-1","keyEnv":"SIGNING_KEY_DEV",` +
			`"origins":["https://app-dev.vocdoni.io"]},` +
			`{"name":"stg","url":"https://saas-api-stg.vocdoni.net","keyId":"stg-1","keyEnv":"SIGNING_KEY_STG",` +
			`"origins":["https://app-stg.vocdoni.io","https://vote.example.org"]}]`,
	}
}

func load(vars map[string]string, allowLocalhost bool) (Config, error) {
	return LoadConfig(func(k string) string { return vars[k] }, allowLocalhost)
}

func TestLoadConfig(t *testing.T) {
	cfg, err := load(testEnvVars(), false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "cert-dev.vocdoni.net" || cfg.AuthListen != ":8443" || cfg.Port != 8080 || cfg.DataDir != "/data" ||
		cfg.CertMode != CertStatic || !cfg.TLS13 || cfg.RateLimitPerMin != 60 || cfg.RateLimitGlobalPerMin != 1200 {
		t.Fatalf("defaults: %+v", cfg)
	}
	if len(cfg.Backends) != 2 || cfg.Backends[1].Origins[1] != "https://vote.example.org" || cfg.Backends[0].KeyID != "dev-1" {
		t.Fatalf("backends: %+v", cfg.Backends)
	}

	acme := testEnvVars()
	delete(acme, "TLS_CERT_FILE")
	delete(acme, "TLS_KEY_FILE")
	for k, v := range map[string]string{
		"ACME_CA": "staging", "ACME_EMAIL": "ops@example.org", "CLOUDFLARE_API_TOKEN": "token",
		"ACME_DNS_OVERRIDE_DOMAIN": "_acme-challenge.cert-dev.acme-zone.example", "TLS13": "false", "RATE_LIMIT_PER_MIN": "0",
	} {
		acme[k] = v
	}
	cfg, err = load(acme, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CertMode != CertACME || !cfg.ACMEStaging || cfg.TLS13 || cfg.RateLimitPerMin != 0 {
		t.Fatalf("acme: %+v", cfg)
	}
}

func TestLoadConfigFailures(t *testing.T) {
	seed := testEnvVars()["SIGNING_KEY_STG"]
	backends := func(entries ...string) string { return "[" + strings.Join(entries, ",") + "]" }
	dev := `{"name":"dev","url":"https://saas-api-dev.vocdoni.net","keyId":"dev-1","keyEnv":"SIGNING_KEY_DEV","origins":["https://app-dev.vocdoni.io"]}`
	entry := func(name, url, keyID, keyEnv, origin string) string {
		return `{"name":"` + name + `","url":"` + url + `","keyId":"` + keyID + `","keyEnv":"` + keyEnv +
			`","origins":["` + origin + `"]}`
	}
	stg := func(mut func(f map[string]string)) string {
		f := map[string]string{
			"name": "stg", "url": "https://saas-api-stg.vocdoni.net", "keyId": "stg-1",
			"keyEnv": "SIGNING_KEY_STG", "origin": "https://app-stg.vocdoni.io",
		}
		mut(f)
		return entry(f["name"], f["url"], f["keyId"], f["keyEnv"], f["origin"])
	}
	for name, tc := range map[string]struct {
		vars    map[string]string
		wantMsg string
	}{
		"invalid JSON":     {map[string]string{"BACKENDS": "[{"}, "invalid JSON"},
		"unknown field":    {map[string]string{"BACKENDS": backends(strings.Replace(dev, `"name"`, `"extra":1,"name"`, 1))}, "invalid JSON"},
		"empty list":       {map[string]string{"BACKENDS": "[]"}, "empty list"},
		"empty name":       {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["name"] = "" }))}, `[1] ""`},
		"empty keyId":      {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["keyId"] = "" }))}, `[1] "stg"`},
		"empty origins":    {map[string]string{"BACKENDS": backends(strings.Replace(dev, `"https://app-dev.vocdoni.io"`, "", 1))}, `[0] "dev"`},
		"duplicate name":   {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["name"] = "dev" }))}, "duplicate name"},
		"duplicate keyId":  {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["keyId"] = "dev-1" }))}, "duplicate keyId"},
		"duplicate url":    {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "https://saas-api-dev.vocdoni.net" }))}, "duplicate url"},
		"duplicate keyEnv": {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["keyEnv"] = "SIGNING_KEY_DEV" }))}, "duplicate keyEnv"},
		"duplicate key":    {map[string]string{"SIGNING_KEY_STG": vectorSeed}, "duplicate key"},
		"duplicate origin": {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "https://app-dev.vocdoni.io" }))}, "duplicate origin"},
		"http url":         {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "http://saas-api-stg.vocdoni.net" }))}, "url must be"},
		"localhost url":    {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "http://localhost:8080" }))}, "url must be"},
		"keyId comma":      {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["keyId"] = "stg,1" }))}, "keyId must not"},
		"keyId equals":     {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["keyId"] = "stg=1" }))}, "keyId must not"},
		"url with path":    {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "https://saas-api-stg.vocdoni.net/v1" }))}, "url must be"},
		"url with query":   {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "https://saas-api-stg.vocdoni.net?x=1" }))}, "url must be"},
		"url with user":    {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["url"] = "https://u:p@saas-api-stg.vocdoni.net" }))}, "url must be"},
		"unset key":        {map[string]string{"SIGNING_KEY_STG": ""}, "SIGNING_KEY_STG is unset"},
		"short key":        {map[string]string{"SIGNING_KEY_STG": base64.StdEncoding.EncodeToString([]byte("short"))}, "SIGNING_KEY_STG is unset"},
		"origin path":      {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "https://app-stg.vocdoni.io/es" }))}, "is not https://"},
		"origin slash":     {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "https://app-stg.vocdoni.io/" }))}, "is not https://"},
		"origin case":      {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "https://App-stg.vocdoni.io" }))}, "is not https://"},
		"origin port 443":  {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "https://app-stg.vocdoni.io:443" }))}, "is not https://"},
		"localhost origin": {map[string]string{"BACKENDS": backends(dev, stg(func(f map[string]string) { f["origin"] = "http://localhost:3000" }))}, "is not https://"},
		"no domain":        {map[string]string{"DOMAIN": ""}, "DOMAIN"},
		"half static":      {map[string]string{"TLS_KEY_FILE": ""}, "go together"},
		"static and acme":  {map[string]string{"CLOUDFLARE_API_TOKEN": "token"}, "exclusive"},
		"bad TLS13":        {map[string]string{"TLS13": "yes"}, "TLS13"},
		"bad rate":         {map[string]string{"RATE_LIMIT_PER_MIN": "-1"}, "RATE_LIMIT_PER_MIN"},
	} {
		vars := testEnvVars()
		for k, v := range tc.vars {
			vars[k] = v
		}
		_, err := load(vars, false)
		if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
			t.Errorf("%s: error %v, want one containing %q", name, err, tc.wantMsg)
			continue
		}
		for _, secret := range []string{vectorSeed, seed} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error prints key material: %v", name, err)
			}
		}
	}

	acme := testEnvVars()
	delete(acme, "TLS_CERT_FILE")
	delete(acme, "TLS_KEY_FILE")
	acme["ACME_CA"] = "production"
	acme["ACME_EMAIL"] = "ops@example.org"
	acme["CLOUDFLARE_API_TOKEN"] = "token"
	if _, err := load(acme, false); err == nil || !strings.Contains(err.Error(), "ACME_DNS_OVERRIDE_DOMAIN") {
		t.Errorf("a token without ACME_DNS_OVERRIDE_DOMAIN: %v", err)
	}
}

// TestLocalhostBackendURL: only a devca build (allowLocalhost) accepts a backend url, and then
// exactly http://localhost:<port>.
func TestLocalhostBackendURL(t *testing.T) {
	const local = `[{"name":"local","url":"http://localhost:8080","keyId":"local-1","keyEnv":"SIGNING_KEY_DEV",` +
		`"origins":["https://app-dev.vocdoni.io"]}]`
	vars := testEnvVars()
	vars["BACKENDS"] = local
	if _, err := load(vars, false); err == nil || !strings.Contains(err.Error(), "url must be") {
		t.Errorf("without devca: %v, want a url error", err)
	}
	cfg, err := load(vars, true)
	if err != nil || cfg.Backends[0].URL != "http://localhost:8080" {
		t.Fatalf("devca: %+v, %v", cfg.Backends, err)
	}
	for _, url := range []string{
		"http://localhost", "http://localhost:8080/", "http://localhost:8080/v1", "http://localhost:8080?x=1",
		"http://LOCALHOST:8080", "http://127.0.0.1:8080", "http://api:8080", "http://u@localhost:8080",
		"http://localhost:99999",
	} {
		vars["BACKENDS"] = strings.Replace(local, "http://localhost:8080", url, 1)
		if _, err := load(vars, true); err == nil || !strings.Contains(err.Error(), "url must be") {
			t.Errorf("devca %s: %v, want a url error", url, err)
		}
	}
}

func TestCanonicalOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://App.Vocdoni.io/es/processes/x": "https://app.vocdoni.io",
		"https://app.vocdoni.io:443/":           "https://app.vocdoni.io",
		"https://app.vocdoni.io:8443":           "https://app.vocdoni.io:8443",
		"http://app.vocdoni.io":                 "",
		"http://localhost:3000/processes/x":     "",
		"ftp://app.vocdoni.io":                  "",
		"https://u@app.vocdoni.io":              "",
		"/processes/x":                          "",
	} {
		got, ok := CanonicalOrigin(in, false)
		if got != want || ok != (want != "") {
			t.Errorf("CanonicalOrigin(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if got, ok := CanonicalOrigin("http://localhost:3000/processes/x", true); !ok || got != "http://localhost:3000" {
		t.Errorf("devca localhost: %q, %v", got, ok)
	}
	if _, ok := CanonicalOrigin("http://localhost/processes/x", true); ok {
		t.Error("devca localhost without a port accepted")
	}
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test ./internal/gateway/ -run 'Config|Canonical'`
Expected: `FAIL	github.com/vocdoni/saas-cert-gateway/internal/gateway [build failed]` (`undefined: LoadConfig`)

Step 2: implement
Create `internal/gateway/config.go`:

```go
package gateway

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Backend is one saas-backend the gateway serves: the voting pages on its Origins get codes from
// URL, signed with Key (id KeyID).
type Backend struct {
	Name    string
	URL     string // https://host, no path (or http://localhost:<port> in a devca build)
	KeyID   string
	Key     ed25519.PrivateKey
	Origins []string // canonical form, see CanonicalOrigin
}

// CertMode selects where the auth listener's certificate comes from.
type CertMode int

// Certificate modes.
const (
	CertACME   CertMode = iota // Let's Encrypt through DNS-01 (certmagic)
	CertStatic                 // TLS_CERT_FILE and TLS_KEY_FILE
)

// Config is the gateway configuration, read from the environment by LoadConfig.
type Config struct {
	Domain                string
	AuthListen            string
	Port                  int
	CertMode              CertMode
	ACMEEmail             string
	ACMEStaging           bool
	DataDir               string
	CloudflareToken       string
	ACMEDNSOverrideDomain string
	TLSCertFile           string
	TLSKeyFile            string
	Backends              []Backend
	RateLimitPerMin       int // 0 turns the per-IP bucket off
	RateLimitGlobalPerMin int
	TLS13                 bool
	LogLevel              slog.Level
	DevCACertFile         string // only read by a devca build
}

// LoadConfig reads the configuration from getenv. allowLocalhost lets BACKENDS list
// http://localhost:<port> origins and backend urls; only a devca build passes true. Errors name the
// variable or the BACKENDS entry and never contain key material.
func LoadConfig(getenv func(string) string, allowLocalhost bool) (Config, error) {
	cfg := Config{
		Domain:                strings.ToLower(strings.TrimSpace(getenv("DOMAIN"))),
		AuthListen:            withDefault(getenv("AUTH_LISTEN"), ":8443"),
		ACMEEmail:             getenv("ACME_EMAIL"),
		DataDir:               withDefault(getenv("DATA_DIR"), "/data"),
		CloudflareToken:       getenv("CLOUDFLARE_API_TOKEN"),
		ACMEDNSOverrideDomain: getenv("ACME_DNS_OVERRIDE_DOMAIN"),
		TLSCertFile:           getenv("TLS_CERT_FILE"),
		TLSKeyFile:            getenv("TLS_KEY_FILE"),
		DevCACertFile:         getenv("DEVCA_CERT_FILE"),
	}
	if cfg.Domain == "" {
		return Config{}, errors.New("DOMAIN is required")
	}
	var err error
	if cfg.Port, err = intVar(getenv, "PORT", 8080); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitPerMin, err = intVar(getenv, "RATE_LIMIT_PER_MIN", 60); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitGlobalPerMin, err = intVar(getenv, "RATE_LIMIT_GLOBAL_PER_MIN", 1200); err != nil {
		return Config{}, err
	}
	if cfg.RateLimitGlobalPerMin < 1 {
		return Config{}, errors.New("RATE_LIMIT_GLOBAL_PER_MIN must be at least 1")
	}
	switch v := getenv("TLS13"); v {
	case "", "true":
		cfg.TLS13 = true
	case "false":
	default:
		return Config{}, fmt.Errorf("TLS13 must be true or false, got %q", v)
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(withDefault(getenv("LOG_LEVEL"), "info"))); err != nil {
		return Config{}, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	if err := cfg.loadCertMode(getenv); err != nil {
		return Config{}, err
	}
	if cfg.Backends, err = ParseBackends(getenv("BACKENDS"), getenv, allowLocalhost); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// loadCertMode selects the static certificate when TLS_CERT_FILE or TLS_KEY_FILE is set, ACME
// otherwise, and checks the variables each needs.
func (cfg *Config) loadCertMode(getenv func(string) string) error {
	if cfg.TLSCertFile != "" || cfg.TLSKeyFile != "" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			return errors.New("TLS_CERT_FILE and TLS_KEY_FILE go together")
		}
		if cfg.CloudflareToken != "" || cfg.ACMEDNSOverrideDomain != "" {
			return errors.New("TLS_CERT_FILE/TLS_KEY_FILE and CLOUDFLARE_API_TOKEN/ACME_DNS_OVERRIDE_DOMAIN are exclusive")
		}
		cfg.CertMode = CertStatic
		return nil
	}
	cfg.CertMode = CertACME
	switch getenv("ACME_CA") {
	case "production":
	case "staging":
		cfg.ACMEStaging = true
	default:
		return errors.New("ACME_CA must be production or staging")
	}
	if cfg.CloudflareToken == "" || cfg.ACMEDNSOverrideDomain == "" || cfg.ACMEEmail == "" {
		return errors.New("ACME needs CLOUDFLARE_API_TOKEN, ACME_DNS_OVERRIDE_DOMAIN and ACME_EMAIL " +
			"(or TLS_CERT_FILE and TLS_KEY_FILE for a static certificate)")
	}
	return nil
}

// backendEntry is one element of the BACKENDS JSON array.
type backendEntry struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	KeyID   string   `json:"keyId"`
	KeyEnv  string   `json:"keyEnv"`
	Origins []string `json:"origins"`
}

// ParseBackends parses BACKENDS, reading each entry's signing key from the variable its keyEnv
// names (base64 of the 32-byte Ed25519 seed).
func ParseBackends(raw string, getenv func(string) string, allowLocalhost bool) ([]Backend, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var entries []backendEntry
	if err := dec.Decode(&entries); err != nil {
		return nil, fmt.Errorf("BACKENDS: invalid JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("BACKENDS: trailing data after the array")
	}
	if len(entries) == 0 {
		return nil, errors.New("BACKENDS: empty list")
	}
	seen := map[string]string{} // "<field>:<value>" -> entry that holds it
	claim := func(field, value, entry string) error {
		key := field + ":" + value
		if other, dup := seen[key]; dup {
			return fmt.Errorf("BACKENDS %s: duplicate %s (also in %s)", entry, field, other)
		}
		seen[key] = entry
		return nil
	}
	backends := make([]Backend, 0, len(entries))
	for i, e := range entries {
		entry := fmt.Sprintf("[%d] %q", i, e.Name)
		if e.Name == "" || e.KeyID == "" || len(e.Origins) == 0 || e.KeyEnv == "" {
			return nil, fmt.Errorf("BACKENDS %s: name, keyId, keyEnv and origins are required", entry)
		}
		if strings.ContainsAny(e.KeyID, ",=: ") {
			return nil, fmt.Errorf("BACKENDS %s: keyId must not contain ',', '=', ':' or spaces", entry)
		}
		u, err := url.Parse(e.URL)
		// a devca build also accepts exactly http://localhost:<port> (the local saas-backend serves plain HTTP)
		local, _ := CanonicalOrigin(e.URL, allowLocalhost)
		localHTTP := strings.HasPrefix(e.URL, "http://") && local == e.URL
		if err != nil || (u.Scheme != "https" && !localHTTP) || u.Host == "" || u.User != nil ||
			(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, fmt.Errorf("BACKENDS %s: url must be https://host with no path, query or userinfo", entry)
		}
		seed, err := base64.StdEncoding.DecodeString(getenv(e.KeyEnv))
		if err != nil || len(seed) != ed25519.SeedSize {
			return nil, fmt.Errorf("BACKENDS %s: %s is unset or not the base64 of a 32-byte Ed25519 seed", entry, e.KeyEnv)
		}
		b := Backend{Name: e.Name, URL: u.Scheme + "://" + u.Host, KeyID: e.KeyID, Key: ed25519.NewKeyFromSeed(seed)}
		for _, check := range [][2]string{
			{"name", e.Name}, {"keyId", e.KeyID}, {"url", b.URL}, {"keyEnv", e.KeyEnv}, {"key", string(seed)},
		} {
			if err := claim(check[0], check[1], entry); err != nil {
				return nil, err
			}
		}
		for _, origin := range e.Origins {
			canonical, ok := CanonicalOrigin(origin, allowLocalhost)
			if !ok || canonical != origin {
				return nil, fmt.Errorf("BACKENDS %s: origin %q is not https://<lowercase host> with no path, "+
					"trailing slash or default port", entry, origin)
			}
			if err := claim("origin", origin, entry); err != nil {
				return nil, err
			}
			b.Origins = append(b.Origins, origin)
		}
		backends = append(backends, b)
	}
	return backends, nil
}

// CanonicalOrigin returns the canonical origin of an absolute URL: "https://" plus the lowercase
// host, with no path and no default port. With allowLocalhost, http://localhost:<port> is accepted
// too. ok is false for anything else.
func CanonicalOrigin(raw string, allowLocalhost bool) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	switch {
	case u.Scheme == "https":
		if port == "443" {
			port = ""
		}
	case u.Scheme == "http" && allowLocalhost && host == "localhost" && port != "":
	default:
		return "", false
	}
	origin := u.Scheme + "://" + host
	if port != "" {
		if _, err := strconv.ParseUint(port, 10, 16); err != nil {
			return "", false
		}
		origin = u.Scheme + "://" + net.JoinHostPort(host, port)
	}
	return origin, true
}

func withDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intVar(getenv func(string) string, name string, def int) (int, error) {
	v := getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %q", name, v)
	}
	return n, nil
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test ./internal/gateway/ -run 'Config|Canonical' -v 2>&1 | grep -E '^(--- |ok)'`
Expected: `--- PASS: TestLoadConfig`, `--- PASS: TestLoadConfigFailures`, `--- PASS: TestLocalhostBackendURL`, `--- PASS: TestCanonicalOrigin`, `ok`

Step 3: commit
`git add -- internal/gateway/config.go internal/gateway/config_test.go && git -c commit.gpgsign=false commit -m "feat(gateway): read and validate the configuration" -- internal/gateway/config.go internal/gateway/config_test.go`

Interfaces this task provides to later tasks: `gateway.Config`, `gateway.Backend{Name, URL, KeyID, Key, Origins}`, `gateway.CertMode` (`CertACME`, `CertStatic`), `gateway.LoadConfig(getenv, allowLocalhost bool) (Config, error)`, `gateway.ParseBackends(raw, getenv, allowLocalhost)`, `gateway.CanonicalOrigin(raw string, allowLocalhost bool) (string, bool)`.

### Task 11: Auth handler, rate limits, pages, servers and /readyz
Model: strong   Repo: cert-gateway   Files: `internal/gateway/gateway.go`, `internal/gateway/pages.go`, `internal/gateway/ratelimit.go`, `internal/gateway/ratelimit_test.go`, `internal/gateway/providers.go`, `internal/gateway/providers_test.go`, `internal/gateway/server.go`, `internal/gateway/gateway_test.go`, `internal/gateway/server_test.go`, `internal/testpki/testpki.go`, `go.mod`, `go.sum`
Goal: C1 `GET /v1/auth`, listeners, response hardening, rate limits, startup backend check, logging, pages (AC7 except the certmagic SNI bullet, which is Task 12; AC10).

Step 0: add the dependencies (private module)
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go get github.com/vocdoni/cert-check@v0.1.3 golang.org/x/time@v0.16.0`
Expected: `go: added github.com/vocdoni/cert-check v0.1.3` and `go: added golang.org/x/time v0.16.0`

Step 1: write the failing tests
Create `internal/testpki/testpki.go`:

```go
// Package testpki issues throwaway certificates for tests.
package testpki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"math/big"
	"testing"
	"time"
)

// OIDSerialNumber is the subject serialNumber attribute (2.5.4.5).
var OIDSerialNumber = asn1.ObjectIdentifier{2, 5, 4, 5}

// CA is a test certificate authority.
type CA struct {
	Cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// Pool returns a pool holding the CA certificate.
func (ca *CA) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// NewCA creates a CA named name.
func NewCA(t testing.TB, name string) *CA {
	t.Helper()
	key := newKey(t)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name, Organization: []string{"test"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &CA{Cert: cert, key: key}
}

// Server issues a TLS server certificate for host.
func (ca *CA) Server(t testing.TB, host string) tls.Certificate {
	t.Helper()
	return ca.issue(t, &x509.Certificate{
		Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
}

// Client issues a client authentication certificate whose subject serialNumber is serialNumber.
func (ca *CA) Client(t testing.TB, serialNumber string) tls.Certificate {
	t.Helper()
	subject := pkix.Name{CommonName: "TEST PERSON", ExtraNames: []pkix.AttributeTypeAndValue{
		{Type: OIDSerialNumber, Value: serialNumber},
	}}
	return ca.issue(t, &x509.Certificate{
		Subject: subject, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

func (ca *CA) issue(t testing.TB, tmpl *x509.Certificate) tls.Certificate {
	t.Helper()
	key := newKey(t)
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl.SerialNumber = serial
	tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
```
Create `internal/gateway/ratelimit_test.go`:

```go
package gateway

import (
	"fmt"
	"testing"
	"time"
)

func TestIPLimiter(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := newIPLimiter(60) // burst 30, one a second
	for i := range 30 {
		if !l.allow("192.0.2.1", now) {
			t.Fatalf("request %d refused within the burst", i)
		}
	}
	if l.allow("192.0.2.1", now) {
		t.Fatal("request over the burst allowed")
	}
	if !l.allow("192.0.2.2", now) {
		t.Fatal("another IP shares the bucket")
	}
	if !l.allow("192.0.2.1", now.Add(time.Second)) {
		t.Fatal("bucket did not refill")
	}

	// idle buckets are evicted, and the map is capped (least recently used first)
	if l.allow("192.0.2.3", now.Add(11*time.Minute)); len(l.entries) != 1 {
		t.Fatalf("%d entries after the idle eviction, want 1", len(l.entries))
	}
	l.max = 3
	for i := range 5 {
		l.allow(fmt.Sprintf("198.51.100.%d", i), now.Add(11*time.Minute))
	}
	if len(l.entries) != 3 {
		t.Fatalf("%d entries, want the cap of 3", len(l.entries))
	}
	if _, kept := l.entries["198.51.100.0"]; kept {
		t.Fatal("the least recently used entry was not evicted")
	}

	off := newIPLimiter(0)
	for range 1000 {
		if !off.allow("192.0.2.1", now) {
			t.Fatal("RATE_LIMIT_PER_MIN=0 refused a request")
		}
	}
}
```
Create `internal/gateway/providers_test.go`:

```go
package gateway

import (
	"slices"
	"testing"

	certcheck "github.com/vocdoni/cert-check"
)

// TestProductionProviders (AC10): the production handler accepts exactly the stable
// natural-person providers, none of them beta.
func TestProductionProviders(t *testing.T) {
	h, err := certcheck.NewHandler(ProductionProviders())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range h.Providers() {
		ids = append(ids, p.Info().ID)
		if p.Info().Beta {
			t.Errorf("provider %s is beta", p.Info().ID)
		}
	}
	if want := []string{"fnmt", "idcat", "accv", "digitel", "bewor", "sectigo"}; !slices.Equal(ids, want) {
		t.Fatalf("providers %v, want %v", ids, want)
	}
}
```
Create `internal/gateway/gateway_test.go`:

```go
package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/testpki"
)

const (
	testDomain    = "cert.test"
	testDevOrigin = "https://app-dev.example"
	testStgOrigin = "https://app-stg.example"
	testProcess   = "0123456789abcdef01234567"
	testState     = "c3RhdGUtc3RhdGUtc3RhdGU"
	testChallenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	testDNI       = "12345678Z"
)

// fakeProvider is a certcheck.Provider for tests: it trusts its CA and answers what result/err say.
type fakeProvider struct {
	ca  *x509.Certificate
	mu  sync.Mutex
	err *certcheck.Error
}

func (*fakeProvider) Info() certcheck.ProviderInfo {
	return certcheck.ProviderInfo{ID: "fake", Name: "Fake"}
}
func (p *fakeProvider) CAs() []*x509.Certificate { return []*x509.Certificate{p.ca} }
func (p *fakeProvider) fail(code certcheck.ErrorCode) {
	p.mu.Lock()
	p.err = errCode(code)
	p.mu.Unlock()
}

func errCode(code certcheck.ErrorCode) *certcheck.Error {
	if code == "" {
		return nil
	}
	return &certcheck.Error{Code: code, Diagnostics: certcheck.Diagnostics{DiagnosticID: "diag-1", Code: code, Provider: "fake"}}
}

func (p *fakeProvider) Verify(_ context.Context, chain []*x509.Certificate) (*certcheck.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return nil, p.err
	}
	if len(chain) == 0 {
		return nil, errCode(certcheck.CodeNoCertificate)
	}
	return &certcheck.Result{
		Provider:    certcheck.ProviderRef{ID: "fake", Name: "Fake"},
		Profile:     certcheck.ProfileInfo{ID: "fake-citizen"},
		Identity:    certcheck.Identity{Document: certcheck.Document{Type: certcheck.DocumentDNI, Number: testDNI}, FullName: "TEST PERSON"},
		Certificate: certcheck.CertificateInfo{SHA256: strings.Repeat("AB", 32)},
		Diagnostics: certcheck.Diagnostics{DiagnosticID: "diag-ok"},
	}, nil
}

// fakeBackend is a saas-backend assertion route: it verifies the signature with pub and answers
// status and body, recording what it received.
type fakeBackend struct {
	*httptest.Server
	pub    ed25519.PublicKey
	mu     sync.Mutex
	status int
	body   string
	got    []map[string]string
	sigOK  []bool
	agents []string
}

func newFakeBackend(t *testing.T, pub ed25519.PublicKey) *fakeBackend {
	b := &fakeBackend{pub: pub, status: http.StatusCreated, body: `{"certCode":"` + strings.Repeat("C", 43) + `","expiresAt":"x"}`}
	b.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var fields map[string]string
		_ = json.Unmarshal(raw, &fields)
		b.mu.Lock()
		defer b.mu.Unlock()
		b.got = append(b.got, fields)
		b.sigOK = append(b.sigOK, r.URL.Path == AssertionPath && verify(b.pub, r.Header.Get(SignatureHeader), raw))
		b.agents = append(b.agents, r.Header.Get("User-Agent"))
		w.WriteHeader(b.status)
		_, _ = io.WriteString(w, b.body)
	}))
	t.Cleanup(b.Close)
	return b
}

func (b *fakeBackend) answer(status int, body string) {
	b.mu.Lock()
	b.status, b.body = status, body
	b.mu.Unlock()
}

func (b *fakeBackend) calls() int { b.mu.Lock(); defer b.mu.Unlock(); return len(b.got) }

// verify mirrors saas-backend's csp.VerifyCertGatewaySignature for the tests.
func verify(pub ed25519.PublicKey, header string, body []byte) bool {
	fields := map[string]string{}
	for part := range strings.SplitSeq(header, ",") {
		k, v, _ := strings.Cut(part, "=")
		fields[k] = v
	}
	t, err := strconv.ParseInt(fields["t"], 10, 64)
	if err != nil || time.Since(time.Unix(t, 0)).Abs() > time.Minute {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(fields["v1"])
	return err == nil && ed25519.Verify(pub, SignedMessage(t, body), sig)
}

// testEnv is a running gateway: both listeners, two backends (dev, stg), one fake provider.
type testEnv struct {
	t        *testing.T
	provider *fakeProvider
	dev, stg *fakeBackend
	authAddr string
	mainAddr string
	ca       *testpki.CA
	client   tls.Certificate
	logs     *syncBuffer
	certs    *fakeCerts
	gateway  *Gateway
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.buf.String() }

// fakeCerts is a CertSource serving a static certificate for testDomain, ready when ready is set.
type fakeCerts struct {
	cert  tls.Certificate
	ready atomic.Bool
}

func (c *fakeCerts) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if hello.ServerName != testDomain {
		return nil, errors.New("unknown server name")
	}
	return &c.cert, nil
}
func (c *fakeCerts) Ready() bool { return c.ready.Load() }

func startEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	ca := testpki.NewCA(t, "gateway test CA")
	e := &testEnv{
		t: t, ca: ca, provider: &fakeProvider{ca: ca.Cert}, client: ca.Client(t, "IDCES-"+testDNI),
		logs: &syncBuffer{}, certs: &fakeCerts{cert: ca.Server(t, testDomain)},
	}
	e.certs.ready.Store(true)
	devKey, stgKey := vectorKey(t), ed25519.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	e.dev = newFakeBackend(t, devKey.Public().(ed25519.PublicKey))
	e.stg = newFakeBackend(t, stgKey.Public().(ed25519.PublicKey))
	cfg := Config{
		Domain: testDomain, RateLimitPerMin: 1000, RateLimitGlobalPerMin: 1000, TLS13: true,
		Backends: []Backend{
			{Name: "dev", URL: e.dev.URL, KeyID: "dev-1", Key: devKey, Origins: []string{testDevOrigin}},
			{Name: "stg", URL: e.stg.URL, KeyID: "stg-1", Key: stgKey, Origins: []string{testStgOrigin}},
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	handler, err := certcheck.NewHandler([]certcheck.Provider{e.provider})
	if err != nil {
		t.Fatal(err)
	}
	backendRoots := x509.NewCertPool()
	backendRoots.AddCert(e.dev.Certificate())
	backendRoots.AddCert(e.stg.Certificate())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: backendRoots}}}
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	g := New(Deps{Config: cfg, Handler: handler, Client: client, Logger: logger, Version: "test"})
	e.gateway = g
	srv := NewServer(g, e.certs)
	mainLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	authLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.mainAddr, e.authAddr = mainLn.Addr().String(), authLn.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, mainLn, authLn) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	})
	e.waitReady()
	return e
}

func (e *testEnv) waitReady() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get("http://" + e.mainAddr + "/readyz"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Fatal("gateway not ready")
}

// httpClient talks to the auth listener as a browser holding cert (nil: none) would, never
// following redirects.
func (e *testEnv) httpClient(cert *tls.Certificate) *http.Client {
	cfg := &tls.Config{RootCAs: e.ca.Pool()}
	if cert != nil {
		cfg.Certificates = []tls.Certificate{*cert}
	}
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: cfg, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", e.authAddr)
		}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func authQuery(returnURL string) url.Values {
	return url.Values{
		"processId": {testProcess}, "returnUrl": {returnURL}, "state": {testState},
		"codeChallenge": {testChallenge}, "lang": {"en"},
	}
}

func devReturnURL() string { return testDevOrigin + "/es/processes/" + testProcess }

// get requests /v1/auth with q, with the client certificate unless noCert.
func (e *testEnv) get(q url.Values, noCert bool) *http.Response {
	e.t.Helper()
	cert := &e.client
	if noCert {
		cert = nil
	}
	_, port, _ := net.SplitHostPort(e.authAddr)
	resp, err := e.httpClient(cert).Get("https://" + testDomain + ":" + port + "/v1/auth?" + q.Encode())
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func assertRedirect(t *testing.T, resp *http.Response, returnURL, fragment string) {
	t.Helper()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d (%s), want 303", resp.StatusCode, body)
	}
	if got, want := resp.Header.Get("Location"), returnURL+"#"+fragment+"&state="+testState; got != want {
		t.Fatalf("Location %q, want %q", got, want)
	}
}

func assertHardened(t *testing.T, h http.Header) {
	t.Helper()
	for name, want := range map[string]string{
		"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Frame-Options": "DENY",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
	} {
		if got := h.Get(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestAuthSuccess(t *testing.T) {
	e := startEnv(t, nil)
	resp := e.get(authQuery(devReturnURL()), false)
	assertRedirect(t, resp, devReturnURL(), "certCode="+strings.Repeat("C", 43))
	assertHardened(t, resp.Header)
	if e.dev.calls() != 1 || e.stg.calls() != 0 {
		t.Fatalf("dev got %d calls, stg %d; want 1, 0", e.dev.calls(), e.stg.calls())
	}
	want := map[string]string{
		"processId": testProcess, "nationalId": testDNI, "documentType": "DNI", "provider": "fake",
		"profile": "fake-citizen", "certSha256": strings.Repeat("ab", 32), "codeChallenge": testChallenge,
	}
	if got := e.dev.got[0]; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("backend got %v, want %v", got, want)
	}
	if !e.dev.sigOK[0] || e.dev.agents[0] != "vocdoni-cert-gateway/test" {
		t.Fatalf("signature ok=%v, user agent %q", e.dev.sigOK[0], e.dev.agents[0])
	}
	// the log has the outcome, never the document number, the name, the return URL or the state
	logs := e.logs.String()
	for _, secret := range []string{testDNI, "TEST PERSON", "app-dev.example", testState} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q:\n%s", secret, logs)
		}
	}
	if !strings.Contains(logs, `"outcome":"created"`) || !strings.Contains(logs, `"backend":"dev"`) {
		t.Errorf("log lacks the outcome or backend:\n%s", logs)
	}
}

// TestAuthTwoBackends: each origin posts to its own backend with its own key, and an origin that
// differs only by host case or :443 selects the same backend.
func TestAuthTwoBackends(t *testing.T) {
	e := startEnv(t, nil)
	stgReturn := testStgOrigin + "/processes/" + testProcess
	assertRedirect(t, e.get(authQuery(stgReturn), false), stgReturn, "certCode="+strings.Repeat("C", 43))
	if e.stg.calls() != 1 || !e.stg.sigOK[0] || e.dev.calls() != 0 {
		t.Fatalf("stg calls %d (signature ok %v), dev calls %d", e.stg.calls(), e.stg.sigOK, e.dev.calls())
	}
	for _, returnURL := range []string{
		"https://APP-DEV.example/processes/" + testProcess,
		"https://app-dev.example:443/ca/processes/" + testProcess + "/",
	} {
		assertRedirect(t, e.get(authQuery(returnURL), false), returnURL, "certCode="+strings.Repeat("C", 43))
	}
	if e.dev.calls() != 2 {
		t.Fatalf("dev calls %d, want 2", e.dev.calls())
	}
	// a request signed for one backend is rejected by the other: their keys differ
	if verify(e.stg.pub, Sign(vectorKey(t), "dev-1", time.Now().Unix(), []byte("{}")), []byte("{}")) {
		t.Fatal("the dev key verifies on the stg backend")
	}
}

func TestAuthCertcheckErrors(t *testing.T) {
	e := startEnv(t, nil)
	for _, code := range certcheck.Codes {
		e.provider.fail(code)
		assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certError="+string(code))
	}
	if e.dev.calls() != 0 {
		t.Fatal("a rejected certificate reached the backend")
	}
	e.provider.fail("")
	assertRedirect(t, e.get(authQuery(devReturnURL()), true), devReturnURL(), "certError=no_certificate")
}

func TestAuthBackendAnswers(t *testing.T) {
	e := startEnv(t, nil)
	for code := range forwardedBackendErrors {
		e.dev.answer(http.StatusConflict, `{"error":"`+code+`"}`)
		assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certError="+code)
	}
	for _, a := range []struct {
		status int
		body   string
	}{
		{http.StatusBadRequest, `{"error":"bad_request"}`},
		{http.StatusUnauthorized, `{"error":"bad_signature"}`},
		{http.StatusNotFound, ""},
		{http.StatusInternalServerError, `{"error":"not_in_census"}`},
		{http.StatusForbidden, `<html>Cloudflare</html>`},
		{http.StatusCreated, `{"certCode":"short"}`},
	} {
		e.dev.answer(a.status, a.body)
		assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certError=backend_unavailable")
	}
	e.dev.Close()
	assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certError=backend_unavailable")
	if !strings.Contains(e.logs.String(), `"status":403`) {
		t.Error("the backend status is not logged")
	}
}

func TestAuthBadParameters(t *testing.T) {
	e := startEnv(t, nil)
	base := authQuery(devReturnURL())
	with := func(name, value string) url.Values {
		q := url.Values{}
		for k, v := range base {
			q[k] = append([]string(nil), v...)
		}
		if value == "" {
			q.Del(name)
		} else {
			q.Set(name, value)
		}
		return q
	}
	dup := with("state", testState)
	dup.Add("state", testState)
	for name, q := range map[string]url.Values{
		"no processId":            with("processId", ""),
		"uppercase processId":     with("processId", strings.ToUpper(testProcess)),
		"short state":             with("state", "abc"),
		"state with padding":      with("state", testState+"="),
		"short challenge":         with("codeChallenge", testChallenge[1:]),
		"unknown lang":            with("lang", "fr"),
		"duplicate parameter":     dup,
		"http returnUrl":          with("returnUrl", "http://app-dev.example/processes/"+testProcess),
		"unknown origin":          with("returnUrl", "https://evil.example/processes/"+testProcess),
		"localhost origin":        with("returnUrl", "http://localhost:3000/processes/"+testProcess),
		"query":                   with("returnUrl", devReturnURL()+"?x=1"),
		"empty query":             with("returnUrl", devReturnURL()+"?"),
		"fragment":                with("returnUrl", devReturnURL()+"#x"),
		"userinfo":                with("returnUrl", "https://u@app-dev.example/processes/"+testProcess),
		"other path":              with("returnUrl", testDevOrigin+"/organization/"+testProcess),
		"other process":           with("returnUrl", testDevOrigin+"/processes/"+strings.Repeat("a", 24)),
		"encoded slash":           with("returnUrl", testDevOrigin+"/es%2Fprocesses/"+testProcess),
		"long returnUrl":          with("returnUrl", testDevOrigin+"/"+strings.Repeat("a", 2048)),
		"returnUrl with dot path": with("returnUrl", testDevOrigin+"/../processes/"+testProcess),
	} {
		resp := e.get(q, false)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: status %d, Location %q; want 400 and no redirect", name, resp.StatusCode, resp.Header.Get("Location"))
		}
		if !bytes.Contains(body, []byte("<html")) || bytes.Contains(body, []byte("<script")) {
			t.Errorf("%s: not a static HTML page: %s", name, body)
		}
		assertHardened(t, resp.Header)
	}
	if e.dev.calls() != 0 {
		t.Fatal("a bad request reached the backend")
	}
	catalan := with("processId", "")
	catalan.Set("lang", "ca")
	if body, _ := io.ReadAll(e.get(catalan, false).Body); !bytes.Contains(body, []byte("Enllaç no vàlid")) {
		t.Errorf("the page is not in the requested language: %s", body)
	}
}
```
Create `internal/gateway/server_test.go`:

```go
package gateway

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/testpki"
)

func TestHostMismatch(t *testing.T) {
	e := startEnv(t, nil)
	_, port, _ := net.SplitHostPort(e.authAddr)
	req, err := http.NewRequest(http.MethodGet, "https://"+testDomain+":"+port+"/v1/auth?"+authQuery(devReturnURL()).Encode(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "other.test:" + port
	resp, err := e.httpClient(&e.client).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("status %d, want 421", resp.StatusCode)
	}
	assertHardened(t, resp.Header)
}

func TestOtherServerNameGetsNoCertificate(t *testing.T) {
	e := startEnv(t, nil)
	conn, err := tls.Dial("tcp", e.authAddr, &tls.Config{ServerName: "other.test", RootCAs: e.ca.Pool()})
	if err == nil {
		_ = conn.Close()
		t.Fatal("handshake with another server name succeeded")
	}
}

func TestReadyz(t *testing.T) {
	e := startEnv(t, nil)
	get := func() *http.Response {
		resp, err := http.Get("http://" + e.mainAddr + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	resp := get()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	assertHardened(t, resp.Header)
	e.certs.ready.Store(false) // the certificate expired or is gone
	if code := get().StatusCode; code != http.StatusServiceUnavailable {
		t.Fatalf("status %d without a valid certificate, want 503", code)
	}
	if resp, err := http.Get("http://" + e.mainAddr + "/v1/auth"); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("the main listener serves /v1/auth: %d", resp.StatusCode)
		}
	}
}

// TestReadyzWaitsForTheCertificate: the main listener answers 503 from the start, and the auth
// listener starts once the certificate is ready.
func TestReadyzWaitsForTheCertificate(t *testing.T) {
	certs := &fakeCerts{}
	srv := NewServer(New(Deps{
		Config: Config{Domain: testDomain, RateLimitGlobalPerMin: 1}, Handler: testHandler(t),
		Logger: slog.New(slog.DiscardHandler),
	}), certs)
	mainLn, _ := net.Listen("tcp", "127.0.0.1:0")
	authLn, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, mainLn, authLn) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	status := func() int {
		for range 50 {
			if resp, err := http.Get("http://" + mainLn.Addr().String() + "/readyz"); err == nil {
				_ = resp.Body.Close()
				return resp.StatusCode
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("main listener down")
		return 0
	}
	if code := status(); code != http.StatusServiceUnavailable {
		t.Fatalf("status %d before the certificate, want 503", code)
	}
	certs.ready.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for status() != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("not ready once the certificate is")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestRateLimits(t *testing.T) {
	t.Run("per IP", func(t *testing.T) {
		e := startEnv(t, func(c *Config) { c.RateLimitPerMin = 2 }) // burst 1
		assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certCode="+strings.Repeat("C", 43))
		resp := e.get(authQuery(devReturnURL()), false)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d over the per-IP limit, want 429", resp.StatusCode)
		}
		assertHardened(t, resp.Header)
	})
	t.Run("per IP off", func(t *testing.T) {
		e := startEnv(t, func(c *Config) { c.RateLimitPerMin = 0 })
		for range 40 {
			if resp := e.get(authQuery(devReturnURL()), true); resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status %d with the per-IP bucket off", resp.StatusCode)
			}
		}
	})
	t.Run("global, certificate requests only", func(t *testing.T) {
		e := startEnv(t, func(c *Config) { c.RateLimitGlobalPerMin = 1 }) // burst 1
		assertRedirect(t, e.get(authQuery(devReturnURL()), false), devReturnURL(), "certCode="+strings.Repeat("C", 43))
		resp := e.get(authQuery(devReturnURL()), false)
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("status %d over the global limit, want 429", resp.StatusCode)
		}
		assertHardened(t, resp.Header)
		assertRedirect(t, e.get(authQuery(devReturnURL()), true), devReturnURL(), "certError=no_certificate")
	})
}

func TestCheckBackends(t *testing.T) {
	var logs bytes.Buffer
	backend := func(status int) string {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body, _ := io.ReadAll(r.Body); len(body) != 0 || r.Header.Get(SignatureHeader) != "" || r.URL.Path != AssertionPath {
				t.Errorf("check request: path %s, body %q, signature %q", r.URL.Path, body, r.Header.Get(SignatureHeader))
			}
			w.WriteHeader(status)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	closed := httptest.NewTLSServer(http.NotFoundHandler())
	closed.Close()
	g := New(Deps{
		Config: Config{RateLimitGlobalPerMin: 1, Backends: []Backend{
			{Name: "keys-set", URL: backend(http.StatusUnauthorized)},
			{Name: "keys-unset", URL: backend(http.StatusNotFound)},
			{Name: "waf", URL: backend(http.StatusForbidden)},
			{Name: "down", URL: closed.URL},
		}},
		Client: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}, //nolint:gosec // test servers
		Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
	})
	g.CheckBackends(context.Background())
	out := logs.String()
	for _, want := range []string{
		`"level":"INFO","msg":"backend check","backend":"keys-set","status":401`,
		`"level":"INFO","msg":"backend check","backend":"keys-unset","status":404`,
		`"level":"ERROR","msg":"backend check","backend":"waf","status":403`,
		`"level":"ERROR","msg":"backend check","backend":"down","error":`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
}

// testHandler is a certcheck handler with one fake provider.
func testHandler(t *testing.T) *certcheck.Handler {
	t.Helper()
	h, err := certcheck.NewHandler([]certcheck.Provider{&fakeProvider{ca: testpki.NewCA(t, "handler CA").Cert}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test ./internal/gateway/`
Expected: `FAIL	github.com/vocdoni/saas-cert-gateway/internal/gateway [build failed]` (`undefined: New`, `undefined: newIPLimiter`, …)

Step 2: implement
Create `internal/gateway/ratelimit.go`:

```go
package gateway

import (
	"container/list"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const (
	// ipIdleEviction is how long a client IP's bucket is kept without requests.
	ipIdleEviction = 10 * time.Minute
	// ipMaxEntries caps the per-IP buckets; the least recently used is evicted first.
	ipMaxEntries = 100_000
)

// ipLimiter is the per client IP token bucket of /v1/auth: perMin requests a minute with a burst of
// perMin/2. A nil *ipLimiter (RATE_LIMIT_PER_MIN=0) allows everything.
type ipLimiter struct {
	mu      sync.Mutex
	limit   rate.Limit
	burst   int
	max     int
	idle    time.Duration
	entries map[string]*list.Element // of *ipEntry
	lru     *list.List               // front = most recently used
}

type ipEntry struct {
	ip       string
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPLimiter(perMin int) *ipLimiter {
	if perMin <= 0 {
		return nil
	}
	return &ipLimiter{
		limit: rate.Limit(float64(perMin) / 60), burst: max(1, perMin/2),
		max: ipMaxEntries, idle: ipIdleEviction, entries: map[string]*list.Element{}, lru: list.New(),
	}
}

// allow spends one token of ip's bucket at now, and reports whether there was one.
func (l *ipLimiter) allow(ip string, now time.Time) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// drop the buckets idle for too long, oldest first
	for back := l.lru.Back(); back != nil && now.Sub(back.Value.(*ipEntry).lastSeen) > l.idle; back = l.lru.Back() {
		delete(l.entries, back.Value.(*ipEntry).ip)
		l.lru.Remove(back)
	}
	el, ok := l.entries[ip]
	if !ok {
		if l.lru.Len() >= l.max {
			oldest := l.lru.Back()
			delete(l.entries, oldest.Value.(*ipEntry).ip)
			l.lru.Remove(oldest)
		}
		el = l.lru.PushFront(&ipEntry{ip: ip, limiter: rate.NewLimiter(l.limit, l.burst)})
		l.entries[ip] = el
	}
	l.lru.MoveToFront(el)
	e := el.Value.(*ipEntry)
	e.lastSeen = now
	return e.limiter.AllowN(now, 1)
}

// newGlobalLimiter is the bucket shared by every request that presents a client certificate:
// perMin requests a minute, with a burst of a tenth of that (six seconds' worth).
func newGlobalLimiter(perMin int) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(float64(perMin)/60), max(1, perMin/10))
}
```
Create `internal/gateway/pages.go`:

```go
package gateway

import (
	"html/template"
	"log/slog"
	"net/http"
)

// pageLangs are the languages of the gateway's pages; defaultLang is used when the request names
// none (or an invalid one).
var pageLangs = map[string]bool{"es": true, "ca": true, "en": true}

const defaultLang = "es"

// pageTexts holds the static texts of the error pages, by status and language.
var pageTexts = map[int]map[string][2]string{
	http.StatusBadRequest: {
		"es": {"Enlace no válido", "El enlace de acceso con certificado no es válido. Vuelve a la página de la votación y pulsa de nuevo «Acceder con certificado digital»."},
		"ca": {"Enllaç no vàlid", "L'enllaç d'accés amb certificat no és vàlid. Torna a la pàgina de la votació i prem de nou «Accedeix amb certificat digital»."},
		"en": {"Invalid link", "The certificate sign-in link is not valid. Go back to the voting page and click “Sign in with a digital certificate” again."},
	},
	http.StatusTooManyRequests: {
		"es": {"Demasiados intentos", "Se han recibido demasiados intentos. Espera un minuto y vuelve a intentarlo desde la página de la votación."},
		"ca": {"Massa intents", "S'han rebut massa intents. Espera un minut i torna-ho a provar des de la pàgina de la votació."},
		"en": {"Too many attempts", "Too many attempts were received. Wait a minute and try again from the voting page."},
	},
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="{{.Lang}}"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5;color:#222}</style>
</head><body><h1>{{.Title}}</h1><p>{{.Body}}</p></body></html>
`))

// writePage writes the static, localised page of status (400 or 429). No JavaScript, no external
// resource, nothing taken from the request but the language.
func writePage(w http.ResponseWriter, status int, lang string, log *slog.Logger) {
	if !pageLangs[lang] {
		lang = defaultLang
	}
	text := pageTexts[status][lang]
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := pageTemplate.Execute(w, struct{ Lang, Title, Body string }{lang, text[0], text[1]}); err != nil {
		log.Warn("writing page", "status", status, "error", err.Error())
	}
}

// setHardeningHeaders sets the headers every response carries.
func setHardeningHeaders(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
}
```
Create `internal/gateway/providers.go`:

```go
package gateway

import (
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/cert-check/providers/accv"
	"github.com/vocdoni/cert-check/providers/bewor"
	"github.com/vocdoni/cert-check/providers/digitel"
	"github.com/vocdoni/cert-check/providers/fnmt"
	"github.com/vocdoni/cert-check/providers/idcat"
	"github.com/vocdoni/cert-check/providers/sectigo"
)

// ProductionProviders returns the accepted certificate types: the stable natural-person providers
// FNMT, idCAT, ACCV, DIGITEL, Bewor and Sectigo. No DNIe, representative, public-employee or beta
// provider.
func ProductionProviders(opts ...certcheck.Option) []certcheck.Provider {
	return []certcheck.Provider{
		fnmt.New(opts...), idcat.New(opts...), accv.New(opts...),
		digitel.New(opts...), bewor.New(opts...), sectigo.New(opts...),
	}
}
```
Create `internal/gateway/gateway.go`:

```go
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"golang.org/x/time/rate"
)

// Request deadlines: the certificate check (chain and OCSP/CRL) and the backend call.
const (
	authTimeout    = 40 * time.Second
	backendTimeout = 10 * time.Second
	// maxReturnURL bounds the returnUrl parameter.
	maxReturnURL = 2048
	// maxBackendBody bounds what is read of a backend answer.
	maxBackendBody = 4 << 10
)

var (
	processIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)
	statePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	b64url43Pattern  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	// returnPathPattern is the voting page path: /[lang/]processes/<processId>[/]
	returnPathPattern = regexp.MustCompile(`^(/[a-z]{2,3}(-[A-Za-z0-9]{2,8})?)?/processes/([0-9a-f]{24})/?$`)
)

// forwardedBackendErrors are the backend error codes shown to the voter as they are; any other
// backend answer becomes backend_unavailable.
var forwardedBackendErrors = map[string]bool{
	"process_not_open": true, "not_cert_census": true, "anonymous_unavailable": true,
	"not_in_census": true, "zero_weight": true, "rate_limited": true,
}

// codeBackendUnavailable is the voter-facing code of any backend failure.
const codeBackendUnavailable = "backend_unavailable"

// Deps are the Gateway's collaborators.
type Deps struct {
	Config  Config
	Handler *certcheck.Handler
	// Client posts to the backends; nil uses one that never follows redirects.
	Client  *http.Client
	Logger  *slog.Logger
	Version string
	// DevCA marks a devca build: every response carries X-Cert-Gateway-Dev: devca.
	DevCA bool
	// Now is the clock of the signatures and rate limits; nil uses time.Now.
	Now func() time.Time
}

// Gateway serves GET /v1/auth on the auth listener.
type Gateway struct {
	cfg       Config
	handler   *certcheck.Handler
	client    *http.Client
	log       *slog.Logger
	userAgent string
	devCA     bool
	now       func() time.Time
	byOrigin  map[string]*Backend
	perIP     *ipLimiter
	global    *rate.Limiter
}

// New builds the Gateway.
func New(d Deps) *Gateway {
	g := &Gateway{
		cfg: d.Config, handler: d.Handler, client: d.Client, log: d.Logger, devCA: d.DevCA, now: d.Now,
		userAgent: "vocdoni-cert-gateway/" + d.Version, byOrigin: map[string]*Backend{},
		perIP: newIPLimiter(d.Config.RateLimitPerMin), global: newGlobalLimiter(d.Config.RateLimitGlobalPerMin),
	}
	if g.client == nil {
		g.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if g.now == nil {
		g.now = time.Now
	}
	for i := range g.cfg.Backends {
		for _, origin := range g.cfg.Backends[i].Origins {
			g.byOrigin[origin] = &g.cfg.Backends[i]
		}
	}
	return g
}

// authParams are the validated query parameters of /v1/auth.
type authParams struct {
	processID string
	returnURL string
	state     string
	challenge string
	lang      string
	backend   *Backend
}

// parseParams validates the query parameters; ok is false for any invalid one.
func (g *Gateway) parseParams(q url.Values) (authParams, bool) {
	for name, values := range q {
		if len(values) != 1 && (name == "processId" || name == "returnUrl" || name == "state" ||
			name == "codeChallenge" || name == "lang") {
			return authParams{}, false
		}
	}
	p := authParams{
		processID: q.Get("processId"), returnURL: q.Get("returnUrl"), state: q.Get("state"),
		challenge: q.Get("codeChallenge"), lang: q.Get("lang"),
	}
	if p.lang == "" {
		p.lang = defaultLang
	}
	if !processIDPattern.MatchString(p.processID) || !statePattern.MatchString(p.state) ||
		!b64url43Pattern.MatchString(p.challenge) || !pageLangs[p.lang] || len(p.returnURL) > maxReturnURL {
		return authParams{}, false
	}
	u, err := url.Parse(p.returnURL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		strings.Contains(p.returnURL, "#") {
		return authParams{}, false
	}
	origin, ok := CanonicalOrigin(p.returnURL, g.devCA)
	if !ok {
		return authParams{}, false
	}
	if p.backend = g.byOrigin[origin]; p.backend == nil {
		return authParams{}, false
	}
	m := returnPathPattern.FindStringSubmatch(u.EscapedPath())
	if m == nil || m[3] != p.processID {
		return authParams{}, false
	}
	return p, true
}

// ServeHTTP serves the auth listener: GET /v1/auth and nothing else.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setHardeningHeaders(w.Header())
	if g.devCA {
		w.Header().Set("X-Cert-Gateway-Dev", "devca")
	}
	if r.URL.Path != "/v1/auth" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := certcheck.CheckHost(r); err != nil {
		http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
		return
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	g.log.Debug("auth request", "remoteAddr", ip)
	lang := r.URL.Query().Get("lang")
	if !g.perIP.allow(ip, g.now()) {
		writePage(w, http.StatusTooManyRequests, lang, g.log)
		return
	}
	params, ok := g.parseParams(r.URL.Query())
	if !ok {
		writePage(w, http.StatusBadRequest, lang, g.log)
		return
	}
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 && !g.global.AllowN(g.now(), 1) {
		writePage(w, http.StatusTooManyRequests, params.lang, g.log)
		return
	}
	g.auth(w, r, params)
}

// auth verifies the certificate, posts the assertion and redirects back to the voting page.
func (g *Gateway) auth(w http.ResponseWriter, r *http.Request, p authParams) {
	start := g.now()
	ctx, cancel := context.WithTimeout(r.Context(), authTimeout)
	defer cancel()
	res, err := g.handler.Auth(r.WithContext(ctx))
	if err != nil {
		code := string(certcheck.CodeInternal)
		attrs := []slog.Attr{slog.String("backend", p.backend.Name)}
		if e, ok := errors.AsType[*certcheck.Error](err); ok {
			code = string(e.Code)
			attrs = append(attrs, e.Diagnostics.LogAttrs()...)
		}
		attrs = append(attrs, slog.String("outcome", code), slog.Int64("latencyMs", g.now().Sub(start).Milliseconds()))
		g.log.LogAttrs(r.Context(), slog.LevelInfo, "certificate rejected", attrs...)
		g.redirect(w, p, "certError", code)
		return
	}
	certCode, outcome := g.postAssertion(r.Context(), p, res)
	g.log.Info("certificate accepted", "outcome", outcome, "provider", res.Provider.ID, "backend", p.backend.Name,
		"diagnosticId", res.Diagnostics.DiagnosticID, "latencyMs", g.now().Sub(start).Milliseconds())
	if certCode == "" {
		g.redirect(w, p, "certError", outcome)
		return
	}
	g.redirect(w, p, "certCode", certCode)
}

// redirect answers 303 to the voting page with the outcome in the fragment, so it never reaches a
// server log or a Referer.
func (*Gateway) redirect(w http.ResponseWriter, p authParams, key, value string) {
	w.Header().Set("Location", p.returnURL+"#"+key+"="+url.QueryEscape(value)+"&state="+p.state)
	w.WriteHeader(http.StatusSeeOther)
}

// assertionRequest is the body posted to the backend's /cert-auth/assertions.
type assertionRequest struct {
	ProcessID     string `json:"processId"`
	NationalID    string `json:"nationalId"`
	DocumentType  string `json:"documentType"`
	Provider      string `json:"provider"`
	Profile       string `json:"profile"`
	CertSHA256    string `json:"certSha256"`
	CodeChallenge string `json:"codeChallenge"`
}

// postAssertion posts the verified identity to the backend of the voting page and returns the
// certificate code, or "" and the voter-facing error code. outcome is "created" on success.
func (g *Gateway) postAssertion(ctx context.Context, p authParams, res *certcheck.Result) (certCode, outcome string) {
	body, err := json.Marshal(assertionRequest{
		ProcessID: p.processID, NationalID: res.Identity.Document.Number,
		DocumentType: string(res.Identity.Document.Type), Provider: res.Provider.ID, Profile: res.Profile.ID,
		CertSHA256: strings.ToLower(res.Certificate.SHA256), CodeChallenge: p.challenge,
	})
	if err != nil {
		g.log.Error("encoding assertion", "backend", p.backend.Name, "error", err.Error())
		return "", codeBackendUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, backendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.backend.URL+AssertionPath, bytes.NewReader(body))
	if err != nil {
		g.log.Error("building assertion request", "backend", p.backend.Name, "error", err.Error())
		return "", codeBackendUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set(SignatureHeader, Sign(p.backend.Key, p.backend.KeyID, g.now().Unix(), body))
	resp, err := g.client.Do(req)
	if err != nil {
		// the error names the URL only: the body never reaches the log
		g.log.Error("posting assertion", "backend", p.backend.Name, "error", err.Error())
		return "", codeBackendUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	var answer struct {
		CertCode string `json:"certCode"`
		Error    string `json:"error"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBackendBody))
	if err == nil {
		err = json.Unmarshal(raw, &answer)
	}
	switch {
	case err == nil && resp.StatusCode == http.StatusCreated && b64url43Pattern.MatchString(answer.CertCode):
		return answer.CertCode, "created"
	case err == nil && resp.StatusCode >= 400 && resp.StatusCode < 500 && forwardedBackendErrors[answer.Error]:
		return "", answer.Error
	default:
		g.log.Error("unexpected backend answer", "backend", p.backend.Name, "status", resp.StatusCode)
		return "", codeBackendUnavailable
	}
}
```
Create `internal/gateway/server.go`:

```go
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	certcheck "github.com/vocdoni/cert-check"
)

// CertSource provides the auth listener's certificate (internal/certs.Manager).
type CertSource interface {
	GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error)
	Ready() bool
}

const (
	// readyPoll is how often Run checks CertSource.Ready before serving the auth listener.
	readyPoll = 500 * time.Millisecond
	// shutdownTimeout bounds the graceful shutdown of both listeners.
	shutdownTimeout = 15 * time.Second
)

// Server runs the main listener (GET /readyz only, plain HTTP) and the auth listener (TLS with
// the certificate request of the handler's CAs, serving the Gateway).
type Server struct {
	gateway *Gateway
	certs   CertSource
	authTLS *tls.Config
	log     *slog.Logger
	mainUp  atomic.Bool
	authUp  atomic.Bool
}

// NewServer builds the Server of g, its auth certificate coming from certs.
func NewServer(g *Gateway, certs CertSource) *Server {
	var opts []certcheck.TLSOption
	if g.cfg.TLS13 {
		opts = append(opts, certcheck.WithTLS13())
	}
	return &Server{gateway: g, certs: certs, authTLS: g.handler.TLSConfig(certs.GetCertificate, opts...), log: g.log}
}

// MainHandler serves GET /readyz: 200 once both listeners are up and the auth certificate is
// present and valid, 503 before.
func (s *Server) MainHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		setHardeningHeaders(w.Header())
		if s.gateway.devCA {
			w.Header().Set("X-Cert-Gateway-Dev", "devca")
		}
		if s.mainUp.Load() && s.authUp.Load() && s.certs.Ready() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	return mux
}

// newHTTPServer returns an http.Server with the gateway's timeouts and HTTP/1 only.
func newHTTPServer(h http.Handler, log *slog.Logger) *http.Server {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		// TLS handshake failures land here: the "TLS log line" of a failed certificate login
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	srv.Protocols = new(http.Protocols)
	srv.Protocols.SetHTTP1(true)
	return srv
}

// Run serves both listeners until ctx ends, then shuts them down. The main listener serves at
// once; the auth listener once the certificate is ready.
func (s *Server) Run(ctx context.Context, mainLn, authLn net.Listener) error {
	mainSrv := newHTTPServer(s.MainHandler(), s.log)
	authSrv := newHTTPServer(s.gateway, s.log)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		s.mainUp.Store(true)
		if err := mainSrv.Serve(mainLn); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	})
	wg.Go(func() {
		t := time.NewTicker(readyPoll)
		defer t.Stop()
		for !s.certs.Ready() {
			select {
			case <-ctx.Done():
				_ = authLn.Close() // never served
				return
			case <-t.C:
			}
		}
		s.authUp.Store(true)
		if err := authSrv.Serve(tls.NewListener(authLn, s.authTLS)); !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	})
	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	s.mainUp.Store(false)
	s.authUp.Store(false)
	shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer stop()
	shutdownErr := errors.Join(mainSrv.Shutdown(shutdownCtx), authSrv.Shutdown(shutdownCtx))
	cancel()
	wg.Wait()
	return errors.Join(runErr, shutdownErr)
}

// CheckBackends sends each backend one unsigned, empty POST to the assertion route and logs
// "backend check" with its name and status: info for 401 (keys set, signature refused) or 404
// (keys not set), error for anything else (403: origin secret or a WAF challenge) or a transport
// failure. It never blocks readiness: Run does not wait for it.
func (g *Gateway) CheckBackends(ctx context.Context) {
	var wg sync.WaitGroup
	for i := range g.cfg.Backends {
		b := &g.cfg.Backends[i]
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, backendTimeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+AssertionPath, http.NoBody)
			if err != nil {
				g.log.Error("backend check", "backend", b.Name, "error", err.Error())
				return
			}
			req.Header.Set("User-Agent", g.userAgent)
			resp, err := g.client.Do(req)
			if err != nil {
				g.log.Error("backend check", "backend", b.Name, "error", err.Error())
				return
			}
			_ = resp.Body.Close()
			level := slog.LevelError
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
				level = slog.LevelInfo
			}
			g.log.Log(ctx, level, "backend check", "backend", b.Name, "status", resp.StatusCode)
		})
	}
	wg.Wait()
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go mod tidy && gofumpt -l . ; go vet ./... && go test -race -count=1 ./... -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: no gofumpt output; `--- PASS` for `TestAuthSuccess`, `TestAuthTwoBackends`, `TestAuthCertcheckErrors`, `TestAuthBackendAnswers`, `TestAuthBadParameters`, `TestProductionProviders`, `TestIPLimiter`, `TestHostMismatch`, `TestOtherServerNameGetsNoCertificate`, `TestReadyz`, `TestReadyzWaitsForTheCertificate`, `TestRateLimits`, `TestCheckBackends`, `TestSignatureVectors`, `TestLoadConfig`, `TestLoadConfigFailures`, `TestLocalhostBackendURL`, `TestCanonicalOrigin`; `ok  	github.com/vocdoni/saas-cert-gateway/internal/gateway`
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && golangci-lint run ./...` → `0 issues.`

Step 3: commit
`git add -- go.mod go.sum internal/gateway/gateway.go internal/gateway/pages.go internal/gateway/ratelimit.go internal/gateway/ratelimit_test.go internal/gateway/providers.go internal/gateway/providers_test.go internal/gateway/server.go internal/gateway/gateway_test.go internal/gateway/server_test.go internal/testpki/testpki.go && git -c commit.gpgsign=false commit -m "feat(gateway): verify certificates and hand the identity to the backend" -- go.mod go.sum internal/gateway/gateway.go internal/gateway/pages.go internal/gateway/ratelimit.go internal/gateway/ratelimit_test.go internal/gateway/providers.go internal/gateway/providers_test.go internal/gateway/server.go internal/gateway/gateway_test.go internal/gateway/server_test.go internal/testpki/testpki.go`

Interfaces this task provides to later tasks: `gateway.Deps{Config, Handler, Client, Logger, Version, DevCA, Now}`, `gateway.New(Deps) *Gateway` (an `http.Handler`), `(*Gateway).CheckBackends(ctx)`, `gateway.CertSource` (`GetCertificate`, `Ready`), `gateway.NewServer(g, certs) *Server`, `(*Server).Run(ctx, mainLn, authLn) error`, `(*Server).MainHandler()`, `gateway.ProductionProviders(opts ...certcheck.Option) []certcheck.Provider`; package `testpki` (`NewCA`, `(*CA).Server`, `(*CA).Client`, `(*CA).Pool`).

### Task 12: Auth certificate (certmagic DNS-01 or static) and `main`
Model: standard   Repo: cert-gateway   Files: `internal/certs/certs.go`, `internal/certs/certs_test.go`, `cmd/cert-gateway/main.go`, `cmd/cert-gateway/providers_prod.go`, `cmd/cert-gateway/main_test.go`, `go.mod`, `go.sum`
Goal: C1 listeners/ACME (DNS-01 only, `DOMAIN` only, no on-demand, storage in `DATA_DIR/certmagic`, background retry with backoff), D8 static mode, wiring (AC7 "SNI other than DOMAIN gets no certificate and triggers no ACME order"; AC12 untagged checks).

Step 0: add the dependencies
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go get github.com/caddyserver/certmagic@v0.25.6 github.com/libdns/cloudflare@v0.2.2 go.uber.org/zap@v1.28.0`
Expected: `go: added github.com/caddyserver/certmagic v0.25.6`, `go: added github.com/libdns/cloudflare v0.2.2`, `go: added go.uber.org/zap v1.28.0` (the versions cert-check's testui pins)

Step 1: write the failing tests
Create `internal/certs/certs_test.go`:

```go
package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

func selfSigned(t *testing.T, name string, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func TestStaticManager(t *testing.T) {
	m := NewStatic("cert.localtest.me", selfSigned(t, "cert.localtest.me", time.Now().Add(time.Hour)))
	if !m.Ready() {
		t.Fatal("a valid static certificate is not ready")
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "CERT.localtest.me"}); err != nil {
		t.Fatalf("domain refused: %v", err)
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.localtest.me"}); err == nil {
		t.Fatal("another server name got a certificate")
	}
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	expired := NewStatic("cert.localtest.me", selfSigned(t, "cert.localtest.me", time.Now().Add(-time.Minute)))
	if expired.Ready() {
		t.Fatal("an expired certificate is ready")
	}
}

// TestACMEOtherNameNoOrder: a server name other than the domain never reaches certmagic (cfg is nil
// here: reaching it would panic), so it cannot trigger an ACME order.
func TestACMEOtherNameNoOrder(t *testing.T) {
	m := &Manager{domain: "cert-dev.vocdoni.net"}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "attacker.example"}); err == nil {
		t.Fatal("another server name got a certificate")
	}
	if m.Ready() {
		t.Fatal("ready before any certificate")
	}
}

// TestRunRetriesWithBackoff: a failed issuance is retried in the background until it succeeds.
func TestRunRetriesWithBackoff(t *testing.T) {
	calls := 0
	m := &Manager{
		domain: "cert.example", cfg: &certmagic.Config{}, log: slog.New(slog.DiscardHandler),
		backoff: []time.Duration{time.Millisecond}, attempt: time.Second,
	}
	m.manage = func(_ context.Context, names []string) error {
		if len(names) != 1 || names[0] != "cert.example" {
			t.Errorf("managed names %v, want the domain only", names)
		}
		if calls++; calls < 3 {
			return errors.New("dns not ready")
		}
		return nil
	}
	if err := m.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !m.ready.Load() {
		t.Fatalf("%d attempts, ready=%v; want 3, true", calls, m.ready.Load())
	}
}

func TestNewACMEManagesTheDomainOnly(t *testing.T) {
	m, err := NewACME(Options{
		Domain: "cert.example", DataDir: t.TempDir(), Email: "ops@example.org",
		CloudflareToken: "token", OverrideDomain: "_acme-challenge.cert.acme.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if m.cfg.OnDemand != nil || m.Ready() {
		t.Fatalf("on-demand=%v ready=%v, want nil and false", m.cfg.OnDemand, m.Ready())
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example"}); err == nil {
		t.Fatal("another server name got a certificate")
	}
}
```
Create `cmd/cert-gateway/main_test.go`:

```go
//go:build !devca

package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/gateway"
	"github.com/vocdoni/saas-cert-gateway/internal/testpki"
)

// TestProductionBuildHasNoDevCA (AC12): a binary built without -tags devca lists no devca
// provider even with DEVCA_CERT_FILE set, rejects a test CA certificate with untrusted_issuer and
// refuses an http://localhost origin and an http://localhost backend url in BACKENDS.
func TestProductionBuildHasNoDevCA(t *testing.T) {
	if devCA {
		t.Fatal("devCA is set in a build without the devca tag")
	}
	ca := testpki.NewCA(t, "cert-gateway dev CA")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	providers, err := withDevProviders(gateway.ProductionProviders(), gateway.Config{DevCACertFile: caFile},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := certcheck.NewHandler(providers)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range handler.Providers() {
		ids = append(ids, p.Info().ID)
	}
	if slices.Contains(ids, "devca") {
		t.Fatalf("providers %v include devca", ids)
	}
	leaf := ca.Client(t, "IDCES-12345678Z").Leaf
	_, err = handler.Verify(t.Context(), []*x509.Certificate{leaf})
	if e, ok := errors.AsType[*certcheck.Error](err); !ok || e.Code != certcheck.CodeUntrustedIssuer {
		t.Fatalf("a test CA certificate got %v, want untrusted_issuer", err)
	}

	env := map[string]string{
		"DOMAIN": "cert.localtest.me", "TLS_CERT_FILE": "c", "TLS_KEY_FILE": "k",
		"SIGNING_KEY_LOCAL": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"BACKENDS": `[{"name":"local","url":"https://localhost:8080","keyId":"local-1","keyEnv":"SIGNING_KEY_LOCAL",` +
			`"origins":["http://localhost:3000"]}]`,
	}
	_, err = gateway.LoadConfig(func(k string) string { return env[k] }, devCA)
	if err == nil || !strings.Contains(err.Error(), "http://localhost:3000") {
		t.Fatalf("an http://localhost origin got %v, want a startup error", err)
	}
	env["BACKENDS"] = `[{"name":"local","url":"http://localhost:8080","keyId":"local-1","keyEnv":"SIGNING_KEY_LOCAL",` +
		`"origins":["https://app.localtest.me"]}]`
	_, err = gateway.LoadConfig(func(k string) string { return env[k] }, devCA)
	if err == nil || !strings.Contains(err.Error(), "url must be") {
		t.Fatalf("an http://localhost backend url got %v, want a startup error", err)
	}
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test ./internal/certs/ ./cmd/...`
Expected: `FAIL	github.com/vocdoni/saas-cert-gateway/internal/certs [build failed]` (`undefined: NewStatic`) and `FAIL	github.com/vocdoni/saas-cert-gateway/cmd/cert-gateway [build failed]` (`undefined: devCA`)

Step 2: implement
Create `internal/certs/certs.go`:

```go
// Package certs provides the auth listener's certificate for DOMAIN only: Let's Encrypt through
// certmagic with the Cloudflare DNS-01 solver (HTTP-01 and TLS-ALPN-01 off, no on-demand
// issuance), stored under DATA_DIR/certmagic, or a static certificate. It follows cert-check's
// testui/internal/acme.
package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/libdns/cloudflare"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Options configures NewACME.
type Options struct {
	Domain          string // the only name managed
	DataDir         string // storage at <DataDir>/certmagic
	Email           string
	Staging         bool
	CloudflareToken string
	OverrideDomain  string // full TXT record name in the dedicated ACME zone
	Logger          *slog.Logger
	LogOutput       io.Writer // certmagic's JSON log lines; nil discards them
}

// Manager serves the auth certificate and reports readiness.
type Manager struct {
	domain    string
	cfg       *certmagic.Config // nil in static mode
	cache     *certmagic.Cache  // nil in static mode
	static    *tls.Certificate
	ready     atomic.Bool
	log       *slog.Logger
	manage    func(context.Context, []string) error // one obtain attempt (cfg.ManageSync); tests replace it
	backoff   []time.Duration
	attempt   time.Duration
	closeOnce sync.Once
}

// Backoff between failed obtain attempts (Let's Encrypt allows 5 failed validations per
// identifier per hour); the process stays up and not ready.
var obtainBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, time.Hour}

const (
	obtainAttemptTimeout = 5 * time.Minute
	cloudflareTimeout    = 30 * time.Second
)

// NewACME builds a certmagic-backed manager for o.Domain. Run obtains the certificate; Close
// stops certmagic's renewal.
func NewACME(o Options) (*Manager, error) {
	if o.Domain == "" || o.DataDir == "" || o.Email == "" || o.CloudflareToken == "" || o.OverrideDomain == "" {
		return nil, errors.New("certs: Domain, DataDir, Email, CloudflareToken and OverrideDomain are required")
	}
	storage := filepath.Join(o.DataDir, "certmagic")
	if err := probeStorage(storage); err != nil {
		return nil, err
	}
	m := &Manager{domain: strings.ToLower(o.Domain), log: o.Logger}
	if m.log == nil {
		m.log = slog.Default()
	}
	zl := zapLogger(o.LogOutput, m.log)
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil },
		Logger:           zl,
	})
	cfg = certmagic.New(cache, certmagic.Config{
		Storage: &certmagic.FileStorage{Path: storage},
		OCSP:    certmagic.OCSPConfig{DisableStapling: true},
		Logger:  zl,
	})
	ca := certmagic.LetsEncryptProductionCA
	if o.Staging {
		ca = certmagic.LetsEncryptStagingCA
	}
	cfg.Issuers = []certmagic.Issuer{certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:                      ca,
		Email:                   o.Email,
		Agreed:                  true,
		DisableHTTPChallenge:    true,
		DisableTLSALPNChallenge: true,
		Logger:                  zl,
		DNS01Solver: &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{
			DNSProvider:    &cloudflare.Provider{APIToken: o.CloudflareToken, HTTPClient: &http.Client{Timeout: cloudflareTimeout}},
			OverrideDomain: o.OverrideDomain,
			Logger:         zl,
		}},
	})}
	m.cfg, m.cache, m.manage, m.backoff, m.attempt = cfg, cache, cfg.ManageSync, obtainBackoff, obtainAttemptTimeout
	return m, nil
}

// zapLogger returns certmagic's logger: JSON lines on w at the levels log enables.
func zapLogger(w io.Writer, log *slog.Logger) *zap.Logger {
	if w == nil {
		return zap.NewNop()
	}
	enc := zap.NewProductionEncoderConfig()
	enc.TimeKey, enc.EncodeTime, enc.EncodeLevel = "time", zapcore.RFC3339NanoTimeEncoder, zapcore.CapitalLevelEncoder
	enabled := zap.LevelEnablerFunc(func(l zapcore.Level) bool {
		return log.Enabled(context.Background(), slog.Level(4*int(l)))
	})
	return zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(enc), zapcore.Lock(zapcore.AddSync(w)), enabled))
}

// probeStorage creates dir and writes a file in it, so an unwritable volume fails startup.
func probeStorage(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("certs: storage %s: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".probe-*")
	if err != nil {
		return fmt.Errorf("certs: storage %s is not writable: %w", dir, err)
	}
	_, werr := f.Write([]byte("probe"))
	if err := errors.Join(werr, f.Close(), os.Remove(f.Name())); err != nil {
		return fmt.Errorf("certs: storage %s is not writable: %w", dir, err)
	}
	return nil
}

// LoadStatic serves the PEM certificate chain and key for domain (TLS_CERT_FILE, TLS_KEY_FILE).
func LoadStatic(domain, certFile, keyFile string) (*Manager, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("certs: static certificate: %w", err)
	}
	return NewStatic(domain, c), nil
}

// NewStatic serves a fixed certificate for domain.
func NewStatic(domain string, cert tls.Certificate) *Manager {
	m := &Manager{domain: strings.ToLower(domain), static: &cert, log: slog.Default()}
	m.ready.Store(true)
	return m
}

// Run obtains (or loads from storage) the certificate, retrying with backoff until it succeeds or
// ctx ends; certmagic then renews it in the background. A static manager returns at once.
func (m *Manager) Run(ctx context.Context) error {
	if m.cfg == nil {
		return nil
	}
	for i := 0; ; i++ {
		attemptCtx, cancel := context.WithTimeout(ctx, m.attempt)
		err := m.manage(attemptCtx, []string{m.domain})
		cancel()
		if err == nil {
			m.ready.Store(true)
			m.log.Info("auth certificate ready", "name", m.domain)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wait := m.backoff[min(i, len(m.backoff)-1)]
		m.log.Error("auth certificate not obtained; retrying", "name", m.domain, "error", err.Error(), "retryIn", wait.String())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// Close stops certmagic's background renewal; a no-op for a static manager.
func (m *Manager) Close() {
	if m.cache != nil {
		m.closeOnce.Do(m.cache.Stop)
	}
}

// GetCertificate is the tls.Config hook. A server name other than the domain gets no certificate
// and never reaches certmagic, so it can trigger no ACME order.
func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if !strings.EqualFold(hello.ServerName, m.domain) {
		return nil, fmt.Errorf("certs: no certificate for server name %q", hello.ServerName[:min(len(hello.ServerName), 64)])
	}
	if m.static != nil {
		return m.static, nil
	}
	return m.cfg.GetCertificate(hello)
}

// Ready reports whether a currently valid certificate for the domain is loaded.
func (m *Manager) Ready() bool {
	if !m.ready.Load() {
		return false
	}
	now := time.Now()
	if m.static != nil {
		return validAt(m.static, now)
	}
	for _, c := range m.cache.AllMatchingCertificates(m.domain) {
		if validAt(&c.Certificate, now) {
			return true
		}
	}
	return false
}

// validAt reports whether c's leaf is valid at now.
func validAt(c *tls.Certificate, now time.Time) bool {
	leaf := c.Leaf
	if leaf == nil {
		if len(c.Certificate) == 0 {
			return false
		}
		var err error
		if leaf, err = x509.ParseCertificate(c.Certificate[0]); err != nil {
			return false
		}
	}
	return !now.Before(leaf.NotBefore) && now.Before(leaf.NotAfter)
}
```
Create `cmd/cert-gateway/main.go`:

```go
// Command cert-gateway verifies voters' Spanish personal certificates for saas-backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/certs"
	"github.com/vocdoni/saas-cert-gateway/internal/gateway"
)

// version is set with -ldflags "-X main.version=<commit>".
var version = "dev"

func main() {
	if err := run(os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "cert-gateway:", err)
		os.Exit(1)
	}
}

// run wires and serves the gateway until SIGINT or SIGTERM.
func run(getenv func(string) string) error {
	cfg, err := gateway.LoadConfig(getenv, devCA)
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rev, err := certcheck.NewRevocation(certcheck.RevocationOptions{UserAgent: "vocdoni-cert-gateway/" + version, Logger: log})
	if err != nil {
		return err
	}
	opts := []certcheck.Option{certcheck.WithRevocation(rev), certcheck.WithLogger(log), certcheck.WithVersion(version)}
	providers, err := withDevProviders(gateway.ProductionProviders(opts...), cfg, log)
	if err != nil {
		return err
	}
	handler, err := certcheck.NewHandler(providers, certcheck.WithVersion(version))
	if err != nil {
		return err
	}

	var cm *certs.Manager
	if cfg.CertMode == gateway.CertStatic {
		cm, err = certs.LoadStatic(cfg.Domain, cfg.TLSCertFile, cfg.TLSKeyFile)
	} else {
		cm, err = certs.NewACME(certs.Options{
			Domain: cfg.Domain, DataDir: cfg.DataDir, Email: cfg.ACMEEmail,
			Staging: cfg.ACMEStaging, CloudflareToken: cfg.CloudflareToken, OverrideDomain: cfg.ACMEDNSOverrideDomain,
			Logger: log, LogOutput: os.Stdout,
		})
	}
	if err != nil {
		return err
	}
	defer cm.Close()

	g := gateway.New(gateway.Deps{Config: cfg, Handler: handler, Logger: log, Version: version, DevCA: devCA})
	srv := gateway.NewServer(g, cm)
	mainLn, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.Port))
	if err != nil {
		return err
	}
	authLn, err := net.Listen("tcp", cfg.AuthListen)
	if err != nil {
		_ = mainLn.Close()
		return err
	}
	log.Info("cert-gateway starting", "version", version, "domain", cfg.Domain, "providers", len(providers),
		"backends", len(cfg.Backends), "tls13", cfg.TLS13)
	var wg sync.WaitGroup
	wg.Go(func() { g.CheckBackends(ctx) })
	wg.Go(func() {
		if err := cm.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("certificate manager stopped", "error", err.Error())
		}
	})
	err = srv.Run(ctx, mainLn, authLn)
	stop()
	wg.Wait()
	return err
}
```
Create `cmd/cert-gateway/providers_prod.go`:

```go
//go:build !devca

package main

import (
	"log/slog"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/gateway"
)

// devCA reports a devca build; this one is not.
const devCA = false

// withDevProviders returns the production providers unchanged.
func withDevProviders(providers []certcheck.Provider, _ gateway.Config, _ *slog.Logger) ([]certcheck.Provider, error) {
	return providers, nil
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go mod tidy && go vet ./... && go test -race -count=1 ./... && go build -trimpath -o ~/.cache/cert-gateway/bin/cert-gateway ./cmd/cert-gateway && golangci-lint run ./...`
Expected: `ok` for `cmd/cert-gateway`, `internal/certs`, `internal/gateway`; the binary at `~/.cache/cert-gateway/bin/cert-gateway`; `0 issues.`

Step 3: commit
`git add -- go.mod go.sum internal/certs/certs.go internal/certs/certs_test.go cmd/cert-gateway/main.go cmd/cert-gateway/providers_prod.go cmd/cert-gateway/main_test.go && git -c commit.gpgsign=false commit -m "feat(gateway): obtain the auth certificate and wire the service" -- go.mod go.sum internal/certs/certs.go internal/certs/certs_test.go cmd/cert-gateway/main.go cmd/cert-gateway/providers_prod.go cmd/cert-gateway/main_test.go`

Interfaces this task provides to later tasks: `certs.NewACME(Options)`, `certs.LoadStatic(domain, certFile, keyFile)`, `certs.NewStatic(domain, tls.Certificate)`, `(*certs.Manager).Run/Close/GetCertificate/Ready`; in `package main`: `const devCA bool`, `withDevProviders(providers, cfg, log) ([]certcheck.Provider, error)`.

### Task 13: devca test CA (build tag), `scripts/devca` and the tagged end-to-end test
Model: standard   Repo: cert-gateway   Files: `internal/devca/devca.go`, `cmd/cert-gateway/providers_devca.go`, `cmd/cert-gateway/devca_test.go`, `scripts/devca`
Goal: D8 — a `devca` provider compiled only with `-tags devca` and only added when `DEVCA_CERT_FILE` is set, a warning and `X-Cert-Gateway-Dev: devca` in such builds, and a script that issues PKCS#12 client certificates under `~/.cache/cert-gateway/devca` (AC12).

Step 1: write the failing test (needs `openssl` on `PATH`)
Create `cmd/cert-gateway/devca_test.go`:

```go
//go:build devca

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/certs"
	"github.com/vocdoni/saas-cert-gateway/internal/gateway"
	"github.com/vocdoni/saas-cert-gateway/internal/testpki"
)

// TestDevCAFlow (AC12): with -tags devca and DEVCA_CERT_FILE, a certificate issued by
// scripts/devca for a member's DNI completes the flow: the gateway posts a signed assertion for that
// DNI to a local backend and redirects to the http://localhost voting page with the code.
func TestDevCAFlow(t *testing.T) {
	if !devCA {
		t.Fatal("devCA is not set in a devca build")
	}
	dir := t.TempDir()
	script := func(args ...string) {
		cmd := exec.Command("../../scripts/devca", args...)
		cmd.Env = append(os.Environ(), "DEVCA_DIR="+dir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("scripts/devca %v: %v\n%s", args, err, out)
		}
	}
	script("init")
	script("issue", "12345678z", "Ana", "Pérez Gómez")
	client, err := tls.LoadX509KeyPair(filepath.Join(dir, "12345678Z.pem"), filepath.Join(dir, "12345678Z.key"))
	if err != nil {
		t.Fatal(err)
	}

	// the local backend: checks the signature with the test vector key and answers a code
	seed, _ := base64.StdEncoding.DecodeString("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=")
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	got := make(chan map[string]string, 1)
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		header := r.Header.Get(gateway.SignatureHeader)
		parts := strings.Split(header, ",")
		var ts int64
		var sig []byte
		for _, p := range parts {
			k, v, _ := strings.Cut(p, "=")
			switch k {
			case "t":
				ts, _ = strconv.ParseInt(v, 10, 64)
			case "v1":
				sig, _ = base64.RawURLEncoding.DecodeString(v)
			}
		}
		if !ed25519.Verify(pub, gateway.SignedMessage(ts, body), sig) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var fields map[string]string
		_ = json.Unmarshal(body, &fields)
		got <- fields
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"certCode":"`+strings.Repeat("D", 43)+`","expiresAt":"x"}`)
	}))
	defer backend.Close()

	env := map[string]string{
		"DOMAIN": "cert.localtest.me", "TLS_CERT_FILE": "unused", "TLS_KEY_FILE": "unused",
		"DEVCA_CERT_FILE": filepath.Join(dir, "ca.pem"), "SIGNING_KEY_LOCAL": base64.StdEncoding.EncodeToString(seed),
		"BACKENDS": `[{"name":"local","url":"` + backend.URL + `","keyId":"local-1","keyEnv":"SIGNING_KEY_LOCAL",` +
			`"origins":["http://localhost:3000"]}]`,
	}
	cfg, err := gateway.LoadConfig(func(k string) string { return env[k] }, devCA)
	if err != nil {
		t.Fatal(err)
	}
	providers, err := withDevProviders(gateway.ProductionProviders(), cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	handler, err := certcheck.NewHandler(providers)
	if err != nil {
		t.Fatal(err)
	}
	serverCA := testpki.NewCA(t, "local server CA")
	cm := certs.NewStatic(cfg.Domain, serverCA.Server(t, cfg.Domain))
	g := gateway.New(gateway.Deps{
		Config: cfg, Handler: handler, Client: backend.Client(), Logger: slog.New(slog.DiscardHandler),
		Version: "test", DevCA: devCA,
	})
	mainLn, _ := net.Listen("tcp", "127.0.0.1:0")
	authLn, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- gateway.NewServer(g, cm).Run(ctx, mainLn, authLn) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	returnURL := "http://localhost:3000/es/processes/0123456789abcdef01234567"
	q := url.Values{
		"processId": {"0123456789abcdef01234567"}, "returnUrl": {returnURL},
		"state": {"c3RhdGUtc3RhdGUtc3RhdGU"}, "codeChallenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"},
	}
	browser := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: serverCA.Pool(), Certificates: []tls.Certificate{client}},
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", authLn.Addr().String())
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	var resp *http.Response
	for range 50 { // the auth listener starts once Run polls the certificate
		if resp, err = browser.Get("https://cert.localtest.me:8443/v1/auth?" + q.Encode()); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("X-Cert-Gateway-Dev") != "devca" {
		t.Fatalf("status %d, dev header %q", resp.StatusCode, resp.Header.Get("X-Cert-Gateway-Dev"))
	}
	if loc := resp.Header.Get("Location"); loc != returnURL+"#certCode="+strings.Repeat("D", 43)+"&state=c3RhdGUtc3RhdGUtc3RhdGU" {
		t.Fatalf("Location %q", loc)
	}
	fields := <-got
	if fields["nationalId"] != "12345678Z" || fields["documentType"] != "DNI" || fields["provider"] != "devca" {
		t.Fatalf("backend got %v", fields)
	}
}

// TestDevCABuildConfig (AC12): a devca build accepts a backend url http://localhost:<port> (the
// local stack's plain-HTTP saas-backend), and without DEVCA_CERT_FILE adds no provider.
func TestDevCABuildConfig(t *testing.T) {
	env := map[string]string{
		"DOMAIN": "cert.localtest.me", "TLS_CERT_FILE": "c", "TLS_KEY_FILE": "k",
		"SIGNING_KEY_LOCAL": "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		"BACKENDS": `[{"name":"local","url":"http://localhost:8080","keyId":"local-1","keyEnv":"SIGNING_KEY_LOCAL",` +
			`"origins":["http://localhost:3000"]}]`,
	}
	cfg, err := gateway.LoadConfig(func(k string) string { return env[k] }, devCA)
	if err != nil || cfg.Backends[0].URL != "http://localhost:8080" {
		t.Fatalf("an http://localhost backend url: %+v, %v", cfg.Backends, err)
	}
	providers, err := withDevProviders(gateway.ProductionProviders(), gateway.Config{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 6 {
		t.Fatalf("%d providers without DEVCA_CERT_FILE, want the 6 production ones", len(providers))
	}
}
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && go test -tags devca ./cmd/...`
Expected: `FAIL	github.com/vocdoni/saas-cert-gateway/cmd/cert-gateway [build failed]` (`undefined: devCA`: with the tag, `providers_prod.go` is excluded and nothing defines it yet)

Step 2: implement
Create `internal/devca/devca.go`:

```go
//go:build devca

// Package devca is a certcheck.Provider for local development only: it accepts client
// certificates issued by a local test CA (scripts/devca), skips revocation and reads the DNI/NIE
// from the subject serialNumber (IDCES-<number>). It is compiled only with the build tag devca;
// the Dockerfile and CI never pass it.
package devca

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
)

// ID is the provider id.
const ID = "devca"

var oidSerialNumber = asn1.ObjectIdentifier{2, 5, 4, 5}

// Provider accepts the client certificates the test CA issued.
type Provider struct {
	ca    *x509.Certificate
	roots *x509.CertPool
}

// Load reads the test CA certificate (PEM) from path.
func Load(path string) (*Provider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("devca: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("devca: no PEM certificate in DEVCA_CERT_FILE")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("devca: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	return &Provider{ca: ca, roots: roots}, nil
}

// Info describes the provider.
func (*Provider) Info() certcheck.ProviderInfo {
	return certcheck.ProviderInfo{ID: ID, Name: "Local test CA", Description: "Local development certificates (devca build only)."}
}

// CAs returns the test CA, advertised in the CertificateRequest.
func (p *Provider) CAs() []*x509.Certificate { return []*x509.Certificate{p.ca} }

// Verify accepts a leaf issued by the test CA for client authentication whose subject
// serialNumber is IDCES-<valid DNI or NIE>. There is no revocation check.
func (p *Provider) Verify(_ context.Context, chain []*x509.Certificate) (*certcheck.Result, error) {
	if len(chain) == 0 || chain[0] == nil {
		return nil, fail(certcheck.CodeNoCertificate, certcheck.StepBounds, "no client certificate")
	}
	leaf := chain[0]
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: p.roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, fail(certcheck.CodeUntrustedIssuer, certcheck.StepPath, "not issued by the test CA for client auth")
	}
	var serial string
	for _, name := range leaf.Subject.Names {
		if name.Type.Equal(oidSerialNumber) {
			serial, _ = name.Value.(string)
		}
	}
	number, ok := strings.CutPrefix(serial, "IDCES-")
	docType, valid := documentType(number)
	if !ok || !valid {
		return nil, fail(certcheck.CodeIdentityInvalid, certcheck.StepIdentity, "subject serialNumber is not IDCES-<DNI or NIE>")
	}
	sum := sha256.Sum256(leaf.Raw)
	now := time.Now().UTC()
	return &certcheck.Result{
		Provider: certcheck.ProviderRef{ID: ID, Name: "Local test CA"},
		Profile:  certcheck.ProfileInfo{ID: ID, Name: "Local test CA", HolderRole: certcheck.RoleNaturalPerson},
		Identity: certcheck.Identity{Document: certcheck.Document{
			Type: docType, Country: "ES", Number: number, Raw: serial, Source: "subject.2.5.4.5",
		}},
		Certificate: certcheck.CertificateInfo{
			Subject: leaf.Subject.String(), Issuer: leaf.Issuer.CommonName, SerialHex: leaf.SerialNumber.Text(16),
			NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), SHA256: hex.EncodeToString(sum[:]),
		},
		Revocation:  certcheck.RevocationInfo{Status: certcheck.RevocationStatusGood, CheckedAt: now},
		ValidatedAt: now,
		Diagnostics: certcheck.Diagnostics{DiagnosticID: certcheck.NewDiagnosticID(), Time: now, Provider: ID},
	}, nil
}

// documentType validates number in cert-check's canonical form: 8 digits and the check letter
// (DNI), or X/Y/Z, 7 digits and the check letter (NIE).
func documentType(number string) (certcheck.DocumentType, bool) {
	const letters = "TRWAGMYFPDXBNJZSQVHLCKE"
	if len(number) != 9 {
		return "", false
	}
	docType, digits := certcheck.DocumentDNI, number[:8]
	if i := strings.IndexByte("XYZ", number[0]); i >= 0 {
		docType, digits = certcheck.DocumentNIE, string(rune('0'+i))+number[1:8]
	}
	n := 0
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", false
		}
		n = n*10 + int(c-'0')
	}
	return docType, number[8] == letters[n%23]
}

// fail returns the *certcheck.Error of a rejected certificate.
func fail(code certcheck.ErrorCode, step certcheck.Step, detail string) *certcheck.Error {
	return &certcheck.Error{Code: code, Retryable: code.Retryable(), Detail: detail, Diagnostics: certcheck.Diagnostics{
		DiagnosticID: certcheck.NewDiagnosticID(), Time: time.Now().UTC(), Provider: ID, FailedStep: step, Code: code,
		TechnicalDetail: detail,
	}}
}
```
Create `cmd/cert-gateway/providers_devca.go`:

```go
//go:build devca

package main

import (
	"log/slog"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-cert-gateway/internal/devca"
	"github.com/vocdoni/saas-cert-gateway/internal/gateway"
)

// devCA reports a devca build: http://localhost origins and backend urls are accepted and every
// response carries X-Cert-Gateway-Dev: devca.
const devCA = true

// withDevProviders adds the local test CA provider when DEVCA_CERT_FILE is set.
func withDevProviders(providers []certcheck.Provider, cfg gateway.Config, log *slog.Logger) ([]certcheck.Provider, error) {
	log.Warn("devca build: local test certificates accepted, never deploy this binary")
	if cfg.DevCACertFile == "" {
		return providers, nil
	}
	p, err := devca.Load(cfg.DevCACertFile)
	if err != nil {
		return nil, err
	}
	return append(providers, p), nil
}
```
Create `scripts/devca`:

```bash
#!/usr/bin/env bash
# Local test CA for the cert-gateway devca build. Never use it outside local development.
#
#   scripts/devca init                                   create the CA (once)
#   scripts/devca issue <DNI|NIE> "<given names>" "<surnames>"
#                                                        issue a client certificate as PKCS#12
#
# Files live in $DEVCA_DIR (default ~/.cache/cert-gateway/devca), never in the repository.
# DEVCA_CERT_FILE for the gateway is $DEVCA_DIR/ca.pem.
set -euo pipefail

dir="${DEVCA_DIR:-$HOME/.cache/cert-gateway/devca}"

usage() {
	sed -n '2,9p' "$0" >&2
	exit 2
}

case "${1:-}" in
init)
	mkdir -p "$dir"
	chmod 700 "$dir"
	if [[ -f "$dir/ca.pem" ]]; then
		echo "CA already exists: $dir/ca.pem"
		exit 0
	fi
	openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 \
		-keyout "$dir/ca.key" -out "$dir/ca.pem" -subj "/C=ES/O=cert-gateway dev/CN=cert-gateway dev CA" \
		-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" 2>/dev/null
	chmod 600 "$dir/ca.key"
	echo "CA created: $dir/ca.pem (DEVCA_CERT_FILE)"
	;;
issue)
	[[ $# -eq 4 ]] || usage
	[[ -f "$dir/ca.pem" ]] || { echo "no CA: run scripts/devca init first" >&2; exit 1; }
	number="$(printf '%s' "$2" | tr '[:lower:]' '[:upper:]' | tr -d ' .-')"
	[[ "$number" =~ ^[0-9XYZ][0-9]{7}[A-Z]$ ]] || { echo "not a DNI/NIE in canonical form: $2" >&2; exit 1; }
	given="$3"
	surnames="$4"
	out="$dir/$number"
	openssl req -utf8 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -keyout "$out.key" -out "$out.csr" \
		-subj "/C=ES/serialNumber=IDCES-$number/GN=$given/SN=$surnames/CN=$surnames $given - $number" 2>/dev/null
	openssl x509 -req -in "$out.csr" -CA "$dir/ca.pem" -CAkey "$dir/ca.key" -CAcreateserial -days 365 -out "$out.pem" \
		-extfile <(printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\nextendedKeyUsage=clientAuth\n') \
		2>/dev/null
	password="$(openssl rand -hex 8)"
	openssl pkcs12 -export -inkey "$out.key" -in "$out.pem" -certfile "$dir/ca.pem" -name "$surnames $given - $number" \
		-out "$out.p12" -passout "pass:$password"
	rm -f "$out.csr"
	chmod 600 "$out.key" "$out.p12"
	echo "certificate: $out.p12"
	echo "password:    $password"
	;;
*)
	usage
	;;
esac
```
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && chmod +x scripts/devca && go vet -tags devca ./... && go test -race -count=1 -tags devca ./... && go test -race -count=1 ./... && golangci-lint run --build-tags devca ./... && golangci-lint run ./...`
Expected: `ok` for every package with and without the tag (`TestDevCAFlow` and `TestDevCABuildConfig` run only with it, `TestProductionBuildHasNoDevCA` only without it), `0 issues.` twice.
Manual check: `DEVCA_DIR=~/.cache/cert-gateway/devca-check scripts/devca init && DEVCA_DIR=~/.cache/cert-gateway/devca-check scripts/devca issue X1234567L "Ana" "Pérez Gómez"` prints `certificate: …/X1234567L.p12` and `password: <16 hex>`; `openssl x509 -in ~/.cache/cert-gateway/devca-check/X1234567L.pem -noout -subject -nameopt utf8` shows `serialNumber=IDCES-X1234567L, GN=Ana, SN=Pérez Gómez`. Remove `~/.cache/cert-gateway/devca-check` afterwards.

Step 3: commit
`git add -- internal/devca/devca.go cmd/cert-gateway/providers_devca.go cmd/cert-gateway/devca_test.go scripts/devca && git -c commit.gpgsign=false commit -m "feat(gateway): add a local test ca behind the devca build tag" -- internal/devca/devca.go cmd/cert-gateway/providers_devca.go cmd/cert-gateway/devca_test.go scripts/devca`

Interfaces this task provides to later tasks: `devca.Load(path) (*devca.Provider, error)`, provider id `devca`; `scripts/devca init|issue <DNI|NIE> <given> <surnames>` with `DEVCA_DIR` override; `make test-devca` (Task 14).

### Task 14: Dockerfile with the private-module ARG, CI, local stack and README
Model: fast   Repo: cert-gateway   Files: `Dockerfile`, `dev/Dockerfile.devca`, `docker-compose.dev.yml`, `Makefile`, `.github/workflows/ci.yml`, `README.md`
Goal: C1 "Deployment", "CI", D8 "Stack"/"Scripts" docs — the production image never contains the token or the devca tag; CI also runs the devca tests, test-only; Railway settings and the DNS/ACME runbook in the README (AC12 `make dev` walkthrough, owner-checked).

Step 1: write the files
Create `Dockerfile`:

```dockerfile
# Production image, built by Railway. Never pass -tags devca here.
# Build context: the repository root.

# golang:1.27 (multi-arch index, the digest cert-check's testui pins, resolved 2026-10-31)
FROM golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
ENV GOTOOLCHAIN=local GOPRIVATE=github.com/vocdoni/cert-check CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
# CERT_CHECK_READ_TOKEN: a read-only token for github.com/vocdoni/cert-check only (sealed Railway
# variable). It is used in this stage only and is not in the final image.
ARG CERT_CHECK_READ_TOKEN
RUN test -n "$CERT_CHECK_READ_TOKEN" || { echo "CERT_CHECK_READ_TOKEN is required" >&2; exit 1; } && \
    git config --global url."https://x-access-token:${CERT_CHECK_READ_TOKEN}@github.com/vocdoni/cert-check".insteadOf \
      "https://github.com/vocdoni/cert-check" && \
    go mod download && \
    rm -f /root/.gitconfig
COPY . .
ARG RAILWAY_GIT_COMMIT_SHA=dev
RUN go build -trimpath -ldflags "-s -w -X main.version=${RAILWAY_GIT_COMMIT_SHA}" -o /out/cert-gateway ./cmd/cert-gateway

# gcr.io/distroless/static-debian12:nonroot (multi-arch index, the digest cert-check's testui pins)
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/cert-gateway /cert-gateway
EXPOSE 8080 8443
ENTRYPOINT ["/cert-gateway"]
```
Create `dev/Dockerfile.devca`:

```dockerfile
# Local development image ONLY: built with -tags devca, so it accepts the local test CA,
# http://localhost voting pages and an http://localhost backend. Used by docker-compose.dev.yml;
# never deploy it.
FROM golang:1.27@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
ENV GOTOOLCHAIN=local GOPRIVATE=github.com/vocdoni/cert-check CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
ARG CERT_CHECK_READ_TOKEN
RUN test -n "$CERT_CHECK_READ_TOKEN" || { echo "CERT_CHECK_READ_TOKEN is required" >&2; exit 1; } && \
    git config --global url."https://x-access-token:${CERT_CHECK_READ_TOKEN}@github.com/vocdoni/cert-check".insteadOf \
      "https://github.com/vocdoni/cert-check" && \
    go mod download && \
    rm -f /root/.gitconfig
COPY . .
RUN go build -tags devca -o /out/cert-gateway ./cmd/cert-gateway

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/cert-gateway /cert-gateway
ENTRYPOINT ["/cert-gateway"]
```
Create `docker-compose.dev.yml`:

```yaml
# Local development stack, merged over saas-backend's docker-compose.yml by `make dev`
# (see the README, "Local development"). Never use these keys or images anywhere else.
services:
  api:
    environment:
      # public key of the test signing seed below (the shared signature test vector)
      - VOCDONI_CERT_GATEWAY_PUBLIC_KEYS=local-1:A6EHv/POEL4dcN0Y50vAmWfk1jCbpQ1fHdyGZBJVMbg=
    # the gateway runs in this service's network namespace (see network_mode below), so its two
    # listeners are published here
    ports:
      - 8443:8443
      - 8082:8082

  cert-gateway:
    build:
      context: ${CERT_GATEWAY_DIR:?run make dev}
      dockerfile: dev/Dockerfile.devca
      args:
        CERT_CHECK_READ_TOKEN: ${CERT_CHECK_READ_TOKEN:?set CERT_CHECK_READ_TOKEN}
    image: cert-gateway-devca
    # the image's default user (65532) cannot read the developer's 0600 key and 0700 devca directory;
    # run as root here, as Railway does with RAILWAY_RUN_UID=0
    user: "0:0"
    # share the api container's network, so the plain-HTTP backend is at http://localhost:<port>,
    # the only non-https backend url a devca binary accepts
    network_mode: "service:api"
    environment:
      DOMAIN: cert.localtest.me
      PORT: "8082" # the main listener (/readyz); api already listens on 8080 in this network
      TLS_CERT_FILE: /certs/cert.pem
      TLS_KEY_FILE: /certs/key.pem
      DEVCA_CERT_FILE: /devca/ca.pem
      SIGNING_KEY_LOCAL: AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=
      BACKENDS: >-
        [{"name":"local","url":"http://localhost:${VOCDONI_PORT:-8080}","keyId":"local-1","keyEnv":"SIGNING_KEY_LOCAL",
        "origins":["http://localhost:3000"]}]
      LOG_LEVEL: debug
    volumes:
      - ${DEVCERTS_DIR:?run make dev}:/certs:ro
      - ${DEVCA_DIR:?run make dev}:/devca:ro
    depends_on:
      - api
```
Create `Makefile`:

```make
.PHONY: build test test-devca lint dev

BIN ?= $(HOME)/.cache/cert-gateway/bin/cert-gateway
SAAS_BACKEND_DIR ?= ../saas-backend
DEVCERTS_DIR ?= $(HOME)/.cache/cert-gateway/devcerts
DEVCA_DIR ?= $(HOME)/.cache/cert-gateway/devca

build:
	go build -trimpath -o $(BIN) ./cmd/cert-gateway

test:
	go test -race ./...

# The devca build and its end-to-end test (scripts/devca needs openssl). CI runs it as a separate
# test-only step; no image is ever built with the tag.
test-devca:
	go test -race -tags devca ./...

lint:
	golangci-lint run ./...
	golangci-lint run --build-tags devca ./...

# Mongo, vocone and saas-backend (its own compose file), and the devca gateway in the backend
# container's network on https://cert.localtest.me:8443. See the README, "Local development".
dev:
	CERT_GATEWAY_DIR=$(CURDIR) DEVCERTS_DIR=$(DEVCERTS_DIR) DEVCA_DIR=$(DEVCA_DIR) \
		docker compose --project-directory $(SAAS_BACKEND_DIR) -f $(SAAS_BACKEND_DIR)/docker-compose.yml \
		-f $(CURDIR)/docker-compose.dev.yml --profile with-vocone up --build
```
Create `.github/workflows/ci.yml`:

```yaml
name: ci

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

env:
  GOPRIVATE: github.com/vocdoni/cert-check

jobs:
  ci:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
      - name: Private module access
        env:
          CERT_CHECK_READ_TOKEN: ${{ secrets.CERT_CHECK_READ_TOKEN }}
        run: |
          git config --global url."https://x-access-token:${CERT_CHECK_READ_TOKEN}@github.com/vocdoni/cert-check".insteadOf \
            "https://github.com/vocdoni/cert-check"
      - name: go mod tidy
        run: go mod tidy -diff
      - name: Test
        run: go test -race ./...
      - name: Test the devca build (test only; no image is built with the tag)
        run: go vet -tags devca ./... && go test -race -tags devca ./...
      - name: Lint
        uses: golangci/golangci-lint-action@v9
        with:
          version: v2.15.0
      - name: Docker build
        env:
          CERT_CHECK_READ_TOKEN: ${{ secrets.CERT_CHECK_READ_TOKEN }}
        run: docker build --build-arg CERT_CHECK_READ_TOKEN -t cert-gateway:ci .
```
Create `README.md`:

````markdown
# cert-gateway

Verifies a voter's Spanish personal certificate (FNMT, idCAT, ACCV, DIGITEL, Bewor, Sectigo) with
[cert-check](https://github.com/vocdoni/cert-check) and hands the proven DNI/NIE to saas-backend,
server to server, so a census with `certAuth` can authenticate voters by certificate.

```
voter ──GET /v1/auth?processId&returnUrl&state&codeChallenge&lang──▶ cert-gateway (TLS, client certificate)
cert-gateway ──POST <backend>/cert-auth/assertions (Ed25519-signed)──▶ saas-backend ──201 {certCode}
cert-gateway ──303 returnUrl#certCode=…&state=…──▶ voting page ──auth/0 {certCode, codeVerifier}──▶ saas-backend
```

The design is saas-backend's `docs/specs/2026-11-11-certificate-voter-auth-design.md`.

## Listeners

- **Auth listener** `AUTH_LISTEN` (`:8443`), TLS 1.3 and 1.2 (`TLS13=false`: 1.2 only), client
  certificate requested with the CAs of the six providers, HTTP/1 only. Serves `GET /v1/auth` only.
  Reached through the Railway TCP proxy at `https://<DOMAIN>:<proxy port>`.
- **Main listener** `PORT` (`8080`), plain HTTP, no public domain. Serves `GET /readyz` only: 200
  once both listeners are up and the auth certificate is present and valid, else 503.

## Configuration

| Variable | Meaning |
|---|---|
| `DOMAIN` | the gateway's hostname (`cert-dev.vocdoni.net`, `cert.vocdoni.io`) |
| `AUTH_LISTEN` | auth listener address, default `:8443` |
| `PORT` | main listener port, `8080` |
| `ACME_EMAIL` | contact email for Let's Encrypt |
| `ACME_CA` | `staging` or `production`; required in ACME mode (first issuance uses `staging`) |
| `DATA_DIR` | volume mount, default `/data`; certificates in `DATA_DIR/certmagic` |
| `CLOUDFLARE_API_TOKEN` | DNS-01 token for the dedicated ACME zone only (sealed) |
| `ACME_DNS_OVERRIDE_DOMAIN` | required with the token: full TXT record name, `_acme-challenge.` label included, in the dedicated ACME zone |
| `TLS_CERT_FILE`, `TLS_KEY_FILE` | static certificate instead of ACME; exclusive with the two above |
| `BACKENDS` | JSON array of `{name, url, keyId, keyEnv, origins}` (not secret) |
| `SIGNING_KEY_<NAME>` | one per backend, named by its `keyEnv`: base64 of the 32-byte Ed25519 seed (sealed) |
| `RATE_LIMIT_PER_MIN` | per client IP, default 60 (burst 30); `0` turns the per-IP bucket off |
| `RATE_LIMIT_GLOBAL_PER_MIN` | requests with a client certificate, default 1200 (burst a tenth) |
| `TLS13` | `true` (default) or `false`; a restart applies it |
| `LOG_LEVEL` | `debug`, `info` (default), `warn`, `error`; `debug` logs `remoteAddr` per auth request |
| `DEVCA_CERT_FILE` | devca builds only: the local test CA (see "Local development") |

`BACKENDS` example (the dev gateway also serves staging):

```json
[{"name":"dev","url":"https://saas-api-dev.vocdoni.net","keyId":"dev-1","keyEnv":"SIGNING_KEY_DEV",
  "origins":["https://app-dev.vocdoni.io"]},
 {"name":"stg","url":"https://saas-api-stg.vocdoni.net","keyId":"stg-1","keyEnv":"SIGNING_KEY_STG",
  "origins":["https://app-stg.vocdoni.io"]}]
```

Origins are canonical: `https://` plus the lowercase host, no path, no trailing slash, no `:443`.
The production gateway has one entry for the prod backend listing `https://app.vocdoni.io` and every
custom organisation domain that serves voting pages. Startup fails, naming the entry and never
printing a key, on any invalid or duplicate value. Only a devca binary (see "Local development")
also accepts an origin and a backend `url` of the form `http://localhost:<port>`; any other binary
refuses both at startup.

Generate a key pair (the seed goes to the gateway, the public key to the backend's
`VOCDONI_CERT_GATEWAY_PUBLIC_KEYS` as `<keyId>:<public key>`):

```sh
seed=$(openssl genpkey -algorithm ed25519 -outform DER | tail -c 32 | base64)     # SIGNING_KEY_<NAME>
echo "$seed" | base64 -d | (printf '\x30\x2e\x02\x01\x00\x30\x05\x06\x03\x2b\x65\x70\x04\x22\x04\x20'; cat) \
  | openssl pkey -inform DER -pubout -outform DER | tail -c 32 | base64          # its public key
```

Each backend has its own key pair; a key is never configured on two backends.

## Development

```sh
export GOPRIVATE=github.com/vocdoni/cert-check   # private module: git must reach it with your credentials
make test          # go test -race ./...
make test-devca    # the devca build and its end-to-end test (needs openssl)
make lint
make build         # binary at ~/.cache/cert-gateway/bin/cert-gateway
```

## Local development

Everything runs on one machine, no cloud service. Requirements: Docker, `mkcert`, `openssl`, a
checkout of saas-backend next to this repository (`../saas-backend`, or `SAAS_BACKEND_DIR`) and of
vocdoni-app, and a read token for cert-check in `CERT_CHECK_READ_TOKEN`.

1. Local TLS certificates, trusted by your browser:
   ```sh
   mkdir -p ~/.cache/cert-gateway/devcerts && cd ~/.cache/cert-gateway/devcerts
   mkcert -install
   mkcert -cert-file cert.pem -key-file key.pem cert.localtest.me
   ```
2. The test CA and a certificate for a test DNI (the CA is created once, in
   `~/.cache/cert-gateway/devca`, never in the repository):
   ```sh
   scripts/devca init
   scripts/devca issue 12345678Z "Ana" "Pérez Gómez"      # prints the .p12 path and its password
   ```
   Import the `.p12` into the browser (Firefox: Settings › Certificates › Your Certificates ›
   Import; Chrome/Edge: the operating system's certificate store).
3. `cp ../saas-backend/example.env ../saas-backend/.env`, set `VOCDONI_VOCDONIAPI=http://vocone:9090/v2`
   and the keys it needs, then `make dev`. It starts Mongo, vocone, saas-backend (with the test
   gateway key in `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS`) and the devca gateway on
   `https://cert.localtest.me:8443` (`/readyz` on `http://localhost:8082`). The gateway shares the
   backend container's network and posts to it at `http://localhost:8080`, a backend `url` only a
   devca binary accepts.
4. Run vocdoni-app on `http://localhost:3000`:
   `SAAS_URL=http://localhost:8080 CERT_GATEWAY_URL=https://cert.localtest.me:8443 pnpm dev`.
5. In the app: create an organisation, add a member with DNI `12345678Z`, create a process whose
   voter sign-in is "Certificado digital", publish it, open the voting page, click "Acceder con
   certificado digital", pick the imported certificate, and vote.

A devca binary logs a warning at startup and answers `X-Cert-Gateway-Dev: devca` on every response.
A real certificate (FNMT, idCAT…) also works locally against a binary built without `devca`; it
needs internet for OCSP/CRL and its DNI in the local member list.

## Operations

One Railway project per environment: `cert-gateway-dev` (`cert-dev.vocdoni.net`, serves dev and
stg) and `cert-gateway-prod` (`cert.vocdoni.io`). Each has one service, region EU West
(Amsterdam), never moved outside the EU. Every step below that creates, deploys or changes
something waits for the owner's OK.

### Railway service settings

Applied once with `railway environment edit --service-config cert-gateway <path> <value>` (no
`railway.json`), following cert-check's `testui/README.md` §4:

- `build.builder DOCKERFILE`, `build.dockerfilePath Dockerfile`
- `deploy.healthcheckPath /readyz`, `deploy.healthcheckTimeout 600`
- `deploy.restartPolicyType ON_FAILURE`, `deploy.restartPolicyMaxRetries 10`
- `deploy.numReplicas 1`
- `source.checkSuites true` (wait for CI); `source.branch main` (dev) or `source.branch release` (prod)

Plus: a volume at `/data`, a TCP proxy to port 8443
(`railway tcp-proxy create --service cert-gateway --port 8443`), no public HTTP domain.

Variables (`railway variable set KEY=value --service cert-gateway`):

```
RAILWAY_RUN_UID=0            # Railway mounts volumes as root
PORT=8080
DOMAIN=cert-dev.vocdoni.net  # cert.vocdoni.io in prod
ACME_EMAIL=<ops mailbox>
ACME_CA=staging              # first issuance, then production
ACME_DNS_OVERRIDE_DOMAIN=_acme-challenge.cert-dev.<acme-zone>
BACKENDS=<JSON above>
LOG_LEVEL=info
```

Sealed, entered by the owner in the dashboard only: `CLOUDFLARE_API_TOKEN`, every
`SIGNING_KEY_<NAME>`, and `CERT_CHECK_READ_TOKEN` (read-only token for cert-check, used by the
Dockerfile's build stage only). Variable changes redeploy, and a service with a volume cannot run
two deployments at once: do not change variables on the production gateway while a certificate
vote is open.

Deploys: dev tracks `main`; prod tracks `release`, which only the owner advances, by fast-forward to
a `main` commit that dev has run.

### DNS records

DNS-only (grey cloud) records in Cloudflare:

| Name | Type | Target |
|---|---|---|
| `cert-dev.vocdoni.net` / `cert.vocdoni.io` | CNAME | the Railway TCP proxy host (`<x>.proxy.rlwy.net`) |
| `_acme-challenge.cert-dev.vocdoni.net` / `_acme-challenge.cert.vocdoni.io` | CNAME | `_acme-challenge.cert-dev.<acme-zone>` / `_acme-challenge.cert.<acme-zone>` |

The public URL carries the proxy port, e.g. `https://cert.vocdoni.io:15140`; vocdoni-app's
`CERT_GATEWAY_URL` includes it. If the TCP proxy is recreated, its port (and maybe its host)
changes: update `CERT_GATEWAY_URL` and the CNAME in the same change.

### Cloudflare API token

Never zone-wide on `vocdoni.io` or `vocdoni.net`. A dedicated Cloudflare zone (`<acme-zone>`) holds
nothing but the challenge TXT records; the token has **Zone: Zone: Read** and **Zone: DNS: Edit** on
that zone only. Dev and prod use separate tokens. `ACME_DNS_OVERRIDE_DOMAIN` is the complete TXT
record name: certmagic writes the challenge there and does not follow the CNAME.

### First ACME issuance

1. Before it: `dig +short CAA vocdoni.net` (or `vocdoni.io`) must not exclude `letsencrypt.org`, and
   the TCP proxy port must not be on Chromium's restricted ports (`net/base/port_util.cc`) or
   Firefox's `network.security.ports.banned`; if it is, recreate the proxy.
2. Deploy with `ACME_CA=staging`; `railway logs` shows `auth certificate ready`. A failure is
   retried with backoff 1, 5, 15, 30, 60 minutes (`auth certificate not obtained; retrying`);
   `/readyz` answers 503 meanwhile, and Railway marks a deployment failed after 600 s.
3. Switch to `ACME_CA=production`. Renewal is automatic; redeploys reuse `/data/certmagic`. Do not
   wipe the volume without a reason (5 certificates per identifier set per week).

### Backend check

At startup the gateway sends each backend one unsigned empty `POST /cert-auth/assertions` and logs
`backend check` with the backend name and status. Expected: **401** (keys set, signature refused).
404 means the backend has no keys yet. 403 means the origin secret or a Cloudflare WAF/bot rule
refused it: add a WAF custom rule with action Skip (managed rules, rate limiting rules, Super Bot
Fight Mode) for path `/cert-auth/assertions` with header `X-Cert-Gateway-Signature`, redeploy, read
the line again. Railway egress IPs are not fixed, so the rule cannot use the source IP. If the zone
runs the free Bot Fight Mode, stop and ask the owner.

### Client IP check

Set `LOG_LEVEL=debug` in dev, make auth requests from two networks and compare the logged
`remoteAddr` with the clients' public IPs. If they are the proxy's, set `RATE_LIMIT_PER_MIN=0` on
every gateway. Reset `LOG_LEVEL=info`.

### Logs

The gateway never logs the document number, names, the certificate, `returnUrl` or `state`. Per
request it logs the outcome code, provider id, backend name, cert-check's `diagnosticId` and
latency; TLS handshake failures are logged at warn by the HTTP server.

### Rollback

The gateway has no state: stop it or redeploy the previous commit at any time. Unsetting
`VOCDONI_CERT_GATEWAY_PUBLIC_KEYS` on a backend turns the feature off there.
````
Step 2: check what can be checked without credentials
Run: `cd /home/lucasmenendez/cert-gateway && export GOPRIVATE=github.com/vocdoni/cert-check && make test && make build && grep -c devca Dockerfile`
Expected: `ok` for every package, the binary at `~/.cache/cert-gateway/bin/cert-gateway`, and `1` (the Dockerfile only mentions the tag in its "never pass -tags devca" comment).
Run: `cd /home/lucasmenendez/cert-gateway && B=/home/lucasmenendez/saas-backend/.worktrees/feature-cert-auth && created= && { [ -e $B/.env ] || { touch $B/.env; created=1; }; } && CERT_GATEWAY_DIR=$PWD DEVCERTS_DIR=$HOME/.cache/cert-gateway/devcerts DEVCA_DIR=$HOME/.cache/cert-gateway/devca CERT_CHECK_READ_TOKEN=unused docker compose --project-directory $B -f $B/docker-compose.yml -f docker-compose.dev.yml --profile with-vocone config >/dev/null && echo ok; [ -z "$created" ] || rm $B/.env`
Expected: `ok` (the merged file is valid; the `api` service gains `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS` and publishes 8443 and 8082; `cert-gateway` runs with `network_mode: service:api` and posts to `http://localhost:8080`).
Not verifiable here: `docker build .` and the CI run need a read token for cert-check (`CERT_CHECK_READ_TOKEN`, an owner-provided secret), and `make dev` needs that token, mkcert and the README steps; the owner runs the walkthrough once (AC12).

Step 3: commit
`git add -- Dockerfile dev/Dockerfile.devca docker-compose.dev.yml Makefile .github/workflows/ci.yml README.md && git -c commit.gpgsign=false commit -m "build(gateway): add the image, ci, local stack and operations guide" -- Dockerfile dev/Dockerfile.devca docker-compose.dev.yml Makefile .github/workflows/ci.yml README.md`

Interfaces this task provides to later tasks: `make build|test|test-devca|lint|dev`; the Railway settings and runbook used in "Merge and release".


## Part C — vocdoni-integrator-sdk (D6)

### Task 15: SDK types, `voterRef` in react-providers, changeset
Model: standard   Repo: vocdoni-integrator-sdk (`/home/lucasmenendez/vocdoni-integrator-sdk`)   Files: `packages/api-types/src/index.ts`, `packages/react-providers/src/election/use-election-auth.ts`, `packages/react-providers/src/election/ElectionProvider.mount.test.tsx`, `skills/vocdoni-integrator-sdk/references/react.md`, `skills/vocdoni-integrator-sdk/references/client.md`, `.changeset/cert-auth-voter-ref.md` (new)
Goal: D6/C3 — `@vocdoni/api-types` gains `certAuth`, `certCode`, `codeVerifier`, `voterRef`; `auth0` sets `owner` to `res.voterRef ?? ownerHash(participant)` (AC8 last bullet).

Step 0: branch from the recorded base
Run: `cd /home/lucasmenendez/vocdoni-integrator-sdk && git fetch origin && git switch -c feat/cert-auth 2be5f7e && pnpm install --frozen-lockfile`
Expected: `Switched to a new branch 'feat/cert-auth'`, then `Done`

Step 1: write the failing test
Edit `packages/react-providers/src/election/ElectionProvider.mount.test.tsx`, replace:

```tsx
  it('AC6: does not re-send while the process is not ONGOING', async () => {
```

with:

```tsx
  it('certificate census: a reload and a new certificate login re-send the entry saved under the voterRef', async () => {
    const VOTER_REF = 'ab'.repeat(32)
    server.use(
      http.get('http://localhost/processes/:id', ({ params }) =>
        HttpResponse.json({
          ...mockProcess,
          id: params.id as string,
          census: { authFields: ['nationalId'], twoFaFields: [], certAuth: true },
          questions: [{ ...mockProcess.questions[0], status: 'ONGOING' }],
        }),
      ),
      // every certificate login sends a fresh code and verifier; the backend answers the same voterRef
      http.post('http://localhost/processes/:processId/auth/0', () =>
        HttpResponse.json({ authToken: 'cert-token', voterRef: VOTER_REF }),
      ),
    )
    // saved before the reload, by the first certificate login of this voter
    savePendingVote(UPSTREAM, { owner: VOTER_REF, nullifier: NULLIFIER, txPayload: STORED_TX, savedAt: 1 })
    const { batches, signCalls } = mockRelay()
    mockCheck(() => batches.length > 0)

    const { result } = renderHook(useVoter, { wrapper })
    await waitFor(() => expect(result.current.election).not.toBeNull())
    await act(async () => {
      await result.current.auth0({ certCode: 'c'.repeat(43), codeVerifier: 'v'.repeat(43) })
    })

    await waitFor(() => expect(result.current.voteStatus).toEqual({ 'q-0': 'confirmed' }))
    expect(batches).toEqual([[STORED_TX]])
    expect(signCalls.count).toBe(0)
    expect(listPendingVotes(UPSTREAM)).toEqual([])
  })

  it('AC6: does not re-send while the process is not ONGOING', async () => {
```
Run: `cd /home/lucasmenendez/vocdoni-integrator-sdk && pnpm vitest run packages/react-providers/src/election/ElectionProvider.mount.test.tsx`
Expected: `× certificate census: a reload and a new certificate login re-send the entry saved under the voterRef` and `Tests  1 failed | 8 passed (9)` (vitest transpiles without type-checking, so the unknown `certCode` fields do not stop it)

Step 2: implement
Edit `packages/api-types/src/index.ts`, replace:

```ts
  totalWeight?: number
}

/** Per-question member eligibility restriction (subset of the process census). */
```

with:

```ts
  totalWeight?: number
  /**
   * Certificate census: voters prove their `nationalId` with a Spanish personal
   * certificate through the certificate gateway instead of typing it. Requires
   * `authFields` to be exactly `['nationalId']`; `twoFaFields` may add an email
   * or SMS code. Refused (40025) by a backend without certificate gateway keys.
   * Echoed on reads. Step 0 then takes `certCode` and `codeVerifier` (see
   * {@link AuthRequest}).
   */
  certAuth?: boolean
}

/** Per-question member eligibility restriction (subset of the process census). */
```
Edit `packages/api-types/src/index.ts`, replace:

```ts
  password?: string
}

/** Auth step 1 — confirm the 2FA challenge. Not used by auth-only censuses. */
```

with:

```ts
  password?: string
  /**
   * Certificate census only (`census.certAuth`): the single-use code the
   * certificate gateway returned to the voting page in the URL fragment
   * (`#certCode=`). Sent with {@link codeVerifier} and, when the census has 2FA,
   * the email or phone; no other login field. An unknown, expired, used or
   * foreign code, or a wrong verifier, answers 40802.
   */
  certCode?: string
  /** PKCE verifier (43–128 base64url characters) of the `codeChallenge` sent to the gateway. */
  codeVerifier?: string
}

/** Auth step 1 — confirm the 2FA challenge. Not used by auth-only censuses. */
```
Edit `packages/api-types/src/index.ts`, replace:

```ts
  /** Hex-encoded census weight. */
  weight?: string
}

export interface AuthResendRequest {
```

with:

```ts
  /** Hex-encoded census weight. */
  weight?: string
  /**
   * Certificate census, step 0 only: a stable reference of the voter on the
   * process (lowercase hex sha256), the same on every login. `auth0` binds the
   * tab's pending votes to it.
   */
  voterRef?: string
}

export interface AuthResendRequest {
```
Edit `packages/react-providers/src/election/use-election-auth.ts`, replace:

```ts
      setOwner(await ownerHash(participant))
```

with:

```ts
      setOwner(res.voterRef ?? (await ownerHash(participant)))
```
Edit `packages/react-providers/src/election/use-election-auth.ts`, replace:

```ts
  /**
   * sha256 hex of the canonical (sorted-key JSON) auth0 participant payload,
   * so the same person re-identifying in this tab gets the same value. Null
```

with:

```ts
  /**
   * The step-0 `voterRef` when the backend returns one (certificate censuses,
   * whose payload changes on every login), else sha256 hex of the canonical
   * (sorted-key JSON) auth0 participant payload, so the same person
   * re-identifying in this tab gets the same value. Null
```
Edit `skills/vocdoni-integrator-sdk/references/react.md`, replace:

```markdown
`owner` is the sha256 of the voter's canonical `auth0()` payload. So:
```

with:

```markdown
`owner` is the sha256 of the voter's canonical `auth0()` payload, or the step-0
`voterRef` when the backend returns one (certificate censuses, `census.certAuth`,
where the payload is a fresh `{ certCode, codeVerifier }` on every login). So:
```
Edit `skills/vocdoni-integrator-sdk/references/client.md`, after the line
`// A wrong one fails like an unknown voter (401, error code 40802).` insert:

```ts
// A census with `certAuth: true` takes no typed field: step 0 is
// authStep0(processId, { certCode, codeVerifier /*, email | phone when the census has 2FA */ }),
// with the single-use code the certificate gateway returned to the voting page and its PKCE verifier.
// The answer also carries `voterRef`, a stable reference of that voter on the process.
```
Create `.changeset/cert-auth-voter-ref.md`:

```markdown
---
"@vocdoni/api-types": minor
"@vocdoni/react-providers": patch
---

Certificate voter authentication: `CensusSpec` gains `certAuth`, `AuthRequest` gains `certCode` and `codeVerifier`, and `AuthResponse` gains `voterRef`. `auth0` binds the tab's pending votes to the step-0 `voterRef` when the backend returns one, so a certificate voter (whose step-0 payload changes on every login) gets their pending vote re-sent after a reload.
```
Run: `cd /home/lucasmenendez/vocdoni-integrator-sdk && pnpm vitest run packages/react-providers/src/election/ElectionProvider.mount.test.tsx && pnpm test && pnpm lint`
Expected: `Tests  9 passed (9)`; then `Test Files  62 passed (62)` / `Tests  906 passed (906)`; then `Tasks:    6 successful, 6 total` (tsc in every package)

Step 3: commit
`git add -- packages/api-types/src/index.ts packages/react-providers/src/election/use-election-auth.ts packages/react-providers/src/election/ElectionProvider.mount.test.tsx skills/vocdoni-integrator-sdk/references/react.md skills/vocdoni-integrator-sdk/references/client.md .changeset/cert-auth-voter-ref.md && git -c commit.gpgsign=false commit -m "feat(react-providers): bind pending votes to the certificate voter ref" -- packages/api-types/src/index.ts packages/react-providers/src/election/use-election-auth.ts packages/react-providers/src/election/ElectionProvider.mount.test.tsx skills/vocdoni-integrator-sdk/references/react.md skills/vocdoni-integrator-sdk/references/client.md .changeset/cert-auth-voter-ref.md`

Interfaces this task provides to later tasks: after the release, `@vocdoni/api-types` (minor, expected 2.6.0) with `CensusSpec.certAuth`, `AuthRequest.certCode`/`codeVerifier`, `AuthResponse.voterRef`; `@vocdoni/react-providers` (patch, expected 4.0.3) whose `auth0` binds `owner` to `voterRef`. Dependents get patch bumps through `updateInternalDependents: always`.


## Part D — vocdoni-app (C3)

### Task 16: Bump the SDK packages to the release with `certAuth`/`voterRef`
Model: fast   Repo: vocdoni-app (`/home/lucasmenendez/vocdoni-app`)   Files: `package.json`, `pnpm-lock.yaml`
Goal: C3 item 7 — `feat/cert-auth` is cut from `origin/develop` (react-providers 4.x) and uses the release carrying D6 and the new api-types fields. **Blocked until the owner has approved and merged the integrator-sdk release PR and CI has published it** (see "Merge and release").

Step 0: branch from the recorded base
Run: `cd /home/lucasmenendez/vocdoni-app && git fetch origin && git status --short && git switch -c feat/cert-auth 6daf44fb && pnpm install --frozen-lockfile`
Expected: an empty `git status`, `Switched to a new branch 'feat/cert-auth'`, `Done`

Step 1: bump (use the versions the release actually published; expected `@vocdoni/api-types` 2.6.0 and `@vocdoni/react-providers` 4.0.3)
Run: `cd /home/lucasmenendez/vocdoni-app && npm view @vocdoni/api-types version && npm view @vocdoni/react-providers version && pnpm add @vocdoni/api-types@^2.6.0 @vocdoni/react-providers@^4.0.3 && pnpm dedupe && grep -n '"certAuth?"\|certAuth?: boolean' node_modules/@vocdoni/api-types/dist/index.d.mts | head -1`
Expected: the two published versions, then `pnpm add` and `pnpm dedupe` succeed and the grep prints one `certAuth?: boolean` line. If `@vocdoni/react-components` still resolves its own older `@vocdoni/api-types` (check `pnpm why @vocdoni/api-types`), also bump it to the version the same release published.

Step 2: check
Run: `cd /home/lucasmenendez/vocdoni-app && pnpm lint && pnpm test`
Expected: lint passes (tsc, prettier, knip) and `Test Files  … passed` with no failures.
Not verifiable while planning: the release did not exist yet; the scratch verification of Tasks 17–19 linked the locally built `@vocdoni/api-types` instead.

Step 3: commit
`git -c commit.gpgsign=false commit -m "build(deps): bump @vocdoni/api-types and @vocdoni/react-providers for certificate auth" -- package.json pnpm-lock.yaml`

Interfaces this task provides to later tasks: `CensusSpec.certAuth`, `AuthRequest.certCode`/`codeVerifier`, `AuthResponse.voterRef` types; `auth0` binding pending votes to `voterRef`.

### Task 17: `CERT_GATEWAY_URL` and the organiser's "digital certificate" option
Model: standard   Repo: vocdoni-app   Files: `src/app-env-build.ts`, `src/app-env-build.test.ts`, `src/components/Process/Create/common.ts`, `src/components/Process/Create/census-spec.ts`, `src/components/Process/Create/draft-mapping.ts`, `src/components/Process/Create/draft-mapping.test.ts`, `src/components/Process/Create/VoterAuthentication/utils.ts`, `src/components/Process/Create/VoterAuthentication/useCensusCheck.ts`, `src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx`, `src/components/Process/Create/VoterAuthentication/index.test.tsx`, `.env.example`, `README.md`
Goal: C3 organiser side and configuration — the option is shown only with `CERT_GATEWAY_URL`, fixes the credentials to `["nationalId"]`, keeps the 2FA choices, sends `certAuth: true`, round-trips drafts and clones, and names "a valid DNI/NIE (missing or invalid)" in the missing-data alert (AC8 first bullet). `useCensusCheck` drops `certAuth` from its contact-only breakdown query (the backend refuses `certAuth` without `["nationalId"]`).

Step 1: write the failing tests (apply with `git apply`)
```diff
diff --git a/src/app-env-build.test.ts b/src/app-env-build.test.ts
index f930f559..382983e9 100644
--- a/src/app-env-build.test.ts
+++ b/src/app-env-build.test.ts
@@ -42,6 +42,18 @@ describe('buildAppEnv', () => {
     expect(buildAppEnv({ DISABLE_ANONYMOUS_VOTING: '1' }).DISABLE_ANONYMOUS_VOTING).toBe(false)
   })
 
+  it('resolves CERT_GATEWAY_URL to an https origin, dropping anything else', () => {
+    const warn = vi.spyOn(console, 'warn').mockImplementation(() => undefined)
+    expect(buildAppEnv({}).CERT_GATEWAY_URL).toBeUndefined()
+    expect(buildAppEnv({ CERT_GATEWAY_URL: 'https://cert.vocdoni.io:15140/' }).CERT_GATEWAY_URL).toBe(
+      'https://cert.vocdoni.io:15140'
+    )
+    expect(buildAppEnv({ CERT_GATEWAY_URL: 'http://cert.vocdoni.io:15140' }).CERT_GATEWAY_URL).toBeUndefined()
+    expect(buildAppEnv({ CERT_GATEWAY_URL: 'https://cert.vocdoni.io/v1' }).CERT_GATEWAY_URL).toBeUndefined()
+    expect(buildAppEnv({ CERT_GATEWAY_URL: 'not a url' }).CERT_GATEWAY_URL).toBeUndefined()
+    warn.mockRestore()
+  })
+
   it('resolves HOME_PROCESS_ID, treating blank values as unset', () => {
     expect(buildAppEnv({}).HOME_PROCESS_ID).toBeUndefined()
     expect(buildAppEnv({ HOME_PROCESS_ID: '   ' }).HOME_PROCESS_ID).toBeUndefined()
```
```diff
diff --git a/src/components/Process/Create/draft-mapping.test.ts b/src/components/Process/Create/draft-mapping.test.ts
index 342ab586..434edc29 100644
--- a/src/components/Process/Create/draft-mapping.test.ts
+++ b/src/components/Process/Create/draft-mapping.test.ts
@@ -278,6 +278,15 @@ describe('votingProcessToForm', () => {
       expect(votingProcessToForm(process({ census: spec as any })).census).toEqual(census)
     })
 
+    it('round-trips a certificate census through the census spec and the clone request', () => {
+      const census = { credentials: ['nationalId'], use2FA: true, use2FAMethod: 'email' as const, certAuth: true }
+      const spec = buildCensusSpec({ ...defaultProcessValues, groupId: 'group-1', census })
+
+      expect(spec).toEqual({ groupId: 'group-1', authFields: ['nationalId'], twoFaFields: ['email'], certAuth: true })
+      expect(votingProcessToForm(process({ census: spec })).census).toEqual(census)
+      expect(votingProcessToCreateRequest(process({ census: spec }), '0xorg').census?.certAuth).toBe(true)
+    })
+
     it('restores anonymous voting', () => {
       expect(votingProcessToForm(process({ census: { anonymous: true } })).anonymousVoting).toBe(true)
     })
```
```diff
diff --git a/src/components/Process/Create/VoterAuthentication/index.test.tsx b/src/components/Process/Create/VoterAuthentication/index.test.tsx
index babc6b81..712ceff1 100644
--- a/src/components/Process/Create/VoterAuthentication/index.test.tsx
+++ b/src/components/Process/Create/VoterAuthentication/index.test.tsx
@@ -269,6 +269,52 @@ describe('VoterAuthentication', () => {
     })
   })
 
+  it('offers the digital certificate only when a gateway is configured', async () => {
+    const user = userEvent.setup()
+    render(<TestForm />)
+    const dialog = await openDialog(user)
+    expect(dialog.queryByRole('checkbox', { name: /digital certificate/i })).not.toBeInTheDocument()
+  })
+
+  it('builds a certificate census on the national ID, keeping the code choices', async () => {
+    const user = userEvent.setup()
+    render(<TestForm initialCensus={null} />, { appEnv: { CERT_GATEWAY_URL: 'https://cert.test:15140' } })
+    const dialog = await openDialog(user)
+
+    await user.click(dialog.getByRole('checkbox', { name: /digital certificate/i }))
+    expect(dialog.getByRole('checkbox', { name: /national id/i })).toBeChecked()
+    expect(dialog.getByRole('checkbox', { name: /member number/i })).toBeDisabled()
+    await chooseCode(user, 'email')
+
+    await waitFor(() =>
+      expect(mockValidateCensus).toHaveBeenLastCalledWith({
+        orgAddress: '0x1',
+        census: { groupId: 'group-1', authFields: ['nationalId'], twoFaFields: ['email'], certAuth: true },
+      })
+    )
+    await waitFor(() => expect(dialog.getByRole('status')).toHaveAttribute('data-status', 'valid'))
+    await user.click(dialog.getByTestId('voter-auth-save'))
+
+    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
+    expect(storedCensus()).toEqual({ credentials: ['nationalId'], use2FA: true, use2FAMethod: 'email', certAuth: true })
+  })
+
+  it('reports members of a certificate census with a missing or invalid DNI/NIE', async () => {
+    mockValidateCensus.mockRejectedValue(membersError({ memberIds: ['m1'], duplicates: [], missingData: ['m2', 'm3'] }))
+    const user = userEvent.setup()
+    render(
+      <TestForm
+        initialCensus={{ credentials: ['nationalId'], use2FA: false, use2FAMethod: 'email', certAuth: true }}
+      />,
+      { appEnv: { CERT_GATEWAY_URL: 'https://cert.test:15140' } }
+    )
+    const dialog = await openDialog(user)
+
+    expect(dialog.getByRole('checkbox', { name: /digital certificate/i })).toBeChecked()
+    const status = dialog.getByRole('status')
+    await waitFor(() => expect(status).toHaveTextContent('2 members are missing a valid DNI/NIE (missing or invalid).'))
+  })
+
   it('checks the census as it will be published, weighting and anonymity included', async () => {
     const user = userEvent.setup()
     render(<TestForm anonymousVoting weightedVote />)
```
Run: `cd /home/lucasmenendez/vocdoni-app && npx vitest run src/app-env-build.test.ts src/components/Process/Create`
Expected: failures in `resolves CERT_GATEWAY_URL to an https origin, dropping anything else`, `builds a certificate census on the national ID, keeping the code choices`, `reports members of a certificate census with a missing or invalid DNI/NIE` and `round-trips a certificate census through the census spec and the clone request`

Step 2: implement (apply with `git apply`)
```diff
diff --git a/src/app-env-build.ts b/src/app-env-build.ts
index 756e7a7d..2f4d3e0c 100644
--- a/src/app-env-build.ts
+++ b/src/app-env-build.ts
@@ -42,6 +42,9 @@ export type AppEnv = {
   SHOW_ORG_LOGO?: boolean
   // When true, the anonymous voting option is not offered in the process wizard.
   DISABLE_ANONYMOUS_VOTING?: boolean
+  // The certificate gateway, port included (e.g. https://cert.vocdoni.io:15140). Unset hides the
+  // "digital certificate" voter sign-in option.
+  CERT_GATEWAY_URL?: string
 }
 
 type AppEnvObject = AppEnv
@@ -167,6 +170,26 @@ const resolvePrimaryColor = (value: string | undefined): string | undefined => {
   return normalized
 }
 
+// CERT_GATEWAY_URL must be an https origin (the gateway answers on /v1/auth); anything else is
+// logged and dropped, which hides the certificate sign-in option.
+const resolveCertGatewayUrl = (value: string | undefined): string | undefined => {
+  if (!value) {
+    return undefined
+  }
+
+  try {
+    const url = new URL(value)
+    if (url.protocol !== 'https:' || url.pathname !== '/' || url.search || url.hash) {
+      console.warn('CERT_GATEWAY_URL must be an https origin like https://cert.vocdoni.io:15140. Ignoring.')
+      return undefined
+    }
+    return url.origin
+  } catch (error: any) {
+    console.warn(`Invalid CERT_GATEWAY_URL. Ignoring. ${error.message}`)
+    return undefined
+  }
+}
+
 // HOME_PROCESS_ID turns the app root into the voting page of a single process
 // (see src/pages/home-process/). Only the id is validated for emptiness here —
 // whether it resolves to an existing process is answered by the SaaS API at
@@ -240,5 +263,6 @@ export const buildAppEnv = (env: EnvSource = {}): AppEnvObject => {
     PRIMARY_COLOR: resolvePrimaryColor(env.PRIMARY_COLOR),
     SHOW_ORG_LOGO: env.SHOW_ORG_LOGO === 'true',
     DISABLE_ANONYMOUS_VOTING: env.DISABLE_ANONYMOUS_VOTING === 'true',
+    CERT_GATEWAY_URL: resolveCertGatewayUrl(env.CERT_GATEWAY_URL),
   }
 }
```
```diff
diff --git a/src/components/Process/Create/common.ts b/src/components/Process/Create/common.ts
index 3c27eb37..9399f91d 100644
--- a/src/components/Process/Create/common.ts
+++ b/src/components/Process/Create/common.ts
@@ -39,6 +39,8 @@ export type Census = {
   credentials: string[]
   use2FA: boolean
   use2FAMethod: TwoFAMethod
+  // Voters prove their national ID with a digital certificate (credentials are then ['nationalId']).
+  certAuth?: boolean
 }
 
 export type Process = {
```
```diff
diff --git a/src/components/Process/Create/VoterAuthentication/utils.ts b/src/components/Process/Create/VoterAuthentication/utils.ts
index 7ce5edbe..7fd85f50 100644
--- a/src/components/Process/Create/VoterAuthentication/utils.ts
+++ b/src/components/Process/Create/VoterAuthentication/utils.ts
@@ -16,8 +16,12 @@ export type VoterAuthField = OrgMemberAuthField
 export type VoterAuthFormData = {
   credentials: VoterAuthField[]
   codeMethod: CodeMethod
+  certAuth: boolean
 }
 
+/** The only credential of a certificate census: the certificate proves it. */
+export const CERT_AUTH_CREDENTIALS: VoterAuthField[] = ['nationalId']
+
 /**
  * A product choice, not an API limit: past three details, voters mistype more
  * than the extra detail protects.
@@ -54,14 +58,17 @@ export const getCodeTwoFaFields = (method: CodeMethod): OrgMemberTwoFaField[] =>
 export const fromCensus = (census?: Census | null): VoterAuthFormData => ({
   credentials: (census?.credentials ?? []) as VoterAuthField[],
   codeMethod: census?.use2FA ? (census.use2FAMethod ?? 'email') : 'none',
+  certAuth: !!census?.certAuth,
 })
 
-export const toCensus = ({ credentials, codeMethod }: VoterAuthFormData): Census => ({
-  credentials: [...credentials],
+export const toCensus = ({ credentials, codeMethod, certAuth }: VoterAuthFormData): Census => ({
+  credentials: certAuth ? [...CERT_AUTH_CREDENTIALS] : [...credentials],
   use2FA: codeMethod !== 'none',
   // The stored shape always carries a method; it only means something while
   // `use2FA` is on.
   use2FAMethod: codeMethod === 'none' ? 'email' : codeMethod,
+  // Only stored when on, so a census without it keeps its shape.
+  ...(certAuth && { certAuth: true }),
 })
 
 export const hasAuthConfig = ({
@@ -78,6 +85,6 @@ export const hasAuthConfig = ({
  * code method is irrelevant while no code is sent.
  */
 export const censusConfigSignature = (census: Census) => {
-  const { credentials, codeMethod } = fromCensus(census)
-  return JSON.stringify([[...credentials].sort(), codeMethod])
+  const { credentials, codeMethod, certAuth } = fromCensus(census)
+  return JSON.stringify([[...credentials].sort(), codeMethod, certAuth])
 }
```
```diff
diff --git a/src/components/Process/Create/census-spec.ts b/src/components/Process/Create/census-spec.ts
index 512fed6c..663b6ffc 100644
--- a/src/components/Process/Create/census-spec.ts
+++ b/src/components/Process/Create/census-spec.ts
@@ -27,5 +27,10 @@ export const buildCensusSpec = (form: CensusSpecSource, disableAnonymous?: boole
   if (form.census?.use2FA && form.census?.use2FAMethod) {
     spec.twoFaFields = getTwoFaFields(form.census.use2FAMethod)
   }
+  // A certificate census: the backend requires authFields to be exactly ['nationalId'].
+  if (form.census?.certAuth) {
+    spec.certAuth = true
+    spec.authFields = ['nationalId']
+  }
   return spec
 }
```
```diff
diff --git a/src/components/Process/Create/draft-mapping.ts b/src/components/Process/Create/draft-mapping.ts
index ae11667c..9b17c377 100644
--- a/src/components/Process/Create/draft-mapping.ts
+++ b/src/components/Process/Create/draft-mapping.ts
@@ -78,6 +78,7 @@ export const votingProcessToForm = (process: VotingProcessResponse, disableAnony
             credentials: [...authFields],
             use2FA: twoFaFields.length > 0,
             use2FAMethod: twoFaMethod ?? 'email',
+            ...(process.census?.certAuth && { certAuth: true }),
           }
         : defaultProcessValues.census,
     // A process always has at least one question (the API rejects an empty list),
@@ -165,6 +166,7 @@ export const votingProcessToCreateRequest = (
     anonymous: disableAnonymous ? undefined : process.census?.anonymous,
     authFields: process.census?.authFields,
     twoFaFields: process.census?.twoFaFields,
+    certAuth: process.census?.certAuth,
     // Absent for an organization-wide census, which is exactly what we want to
     // send back for one.
     groupId: process.census?.groupId,
```
```diff
diff --git a/src/components/Process/Create/VoterAuthentication/useCensusCheck.ts b/src/components/Process/Create/VoterAuthentication/useCensusCheck.ts
index 9fd77a68..af325db6 100644
--- a/src/components/Process/Create/VoterAuthentication/useCensusCheck.ts
+++ b/src/components/Process/Create/VoterAuthentication/useCensusCheck.ts
@@ -100,6 +100,12 @@ const fieldParts = (spec: CensusSpec) => [
       spec: { ...spec, authFields: [field], twoFaFields: undefined },
     })),
   ...(spec.twoFaFields?.length
-    ? [{ field: 'contact', spec: { ...spec, authFields: undefined, twoFaFields: spec.twoFaFields } }]
+    ? [
+        {
+          field: 'contact',
+          // certAuth needs authFields ['nationalId'], so the contact part is checked without it
+          spec: { ...spec, authFields: undefined, twoFaFields: spec.twoFaFields, certAuth: undefined },
+        },
+      ]
     : []),
 ]
```
```diff
diff --git a/src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx b/src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx
index dbfe919f..bab74340 100644
--- a/src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx
+++ b/src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx
@@ -31,6 +31,7 @@ import { MemberCheck } from './MemberCheck'
 import { ProtectionMeter } from './ProtectionLevel'
 import { useCensusCheck } from './useCensusCheck'
 import {
+  CERT_AUTH_CREDENTIALS,
   CodeMethod,
   countedCredentials,
   fromCensus,
@@ -97,8 +98,13 @@ const formatList = (language: string, items: string[], type: 'conjunction' | 'di
  * - duplicates: the details plus one contact match another member's (either
  *   contact for voter's choice), so "Member Number and Email or Phone".
  */
-const describeFields = (t: TFunction, language: string, { credentials, codeMethod }: VoterAuthFormData) => {
-  const details = credentials.map((field) => getAuthFieldLabel(t, field))
+const describeFields = (t: TFunction, language: string, { credentials, codeMethod, certAuth }: VoterAuthFormData) => {
+  // A certificate census also leaves out members whose DNI/NIE is not a valid one.
+  const label = (field: string) =>
+    certAuth && field === 'nationalId'
+      ? t('voter_auth.cert.missing_label', { defaultValue: 'a valid DNI/NIE (missing or invalid)' })
+      : getAuthFieldLabel(t, field)
+  const details = credentials.map(label)
   // The backend leaves the password out of duplicate detection, so it never shares a clash.
   const duplicateDetails = credentials
     .filter((field) => field !== PASSWORD_CREDENTIAL)
@@ -124,7 +130,7 @@ const describeFields = (t: TFunction, language: string, { credentials, codeMetho
       'conjunction'
     ),
     /** The name of one part of the per-field breakdown: an auth field id, or 'contact'. */
-    of: (field: string) => (field === 'contact' ? (missingContact ?? field) : getAuthFieldLabel(t, field)),
+    of: (field: string) => (field === 'contact' ? (missingContact ?? field) : label(field)),
   }
 }
 
@@ -151,14 +157,15 @@ type VoterAuthFormProps = {
 export const VoterAuthForm = ({ census, group, onCancel, onSave }: VoterAuthFormProps) => {
   const { t, i18n } = useTranslation()
   const toast = useToast()
-  const { DISABLE_ANONYMOUS_VOTING: disableAnonymousVoting } = useAppEnv()
+  const { DISABLE_ANONYMOUS_VOTING: disableAnonymousVoting, CERT_GATEWAY_URL: certGatewayUrl } = useAppEnv()
   const { watch: watchProcess } = useFormContext<Process>()
   const [groupId, anonymousVoting, weightedVote] = watchProcess(['groupId', 'anonymousVoting', 'weightedVote'])
 
-  const { control, watch } = useForm<VoterAuthFormData>({ defaultValues: fromCensus(census) })
+  const { control, watch, setValue } = useForm<VoterAuthFormData>({ defaultValues: fromCensus(census) })
   const credentials = watch('credentials') ?? []
   const codeMethod = watch('codeMethod') ?? 'none'
-  const values: VoterAuthFormData = { credentials, codeMethod }
+  const certAuth = watch('certAuth') ?? false
+  const values: VoterAuthFormData = { credentials, codeMethod, certAuth }
   const configured = hasAuthConfig(values)
   // A password alone is refused before saving, so the backend never gets a census it would reject.
   const passwordAlone = passwordNeedsAnotherCredential(credentials, codeMethod)
@@ -292,6 +299,43 @@ export const VoterAuthForm = ({ census, group, onCancel, onSave }: VoterAuthForm
                   defaultValue: 'Optional if you send a one-time code: voters can then sign in with the code alone.',
                 })}
               </Text>
+              {/* Offered only where a certificate gateway is configured, or to keep showing a saved choice. */}
+              {(certGatewayUrl || certAuth) && (
+                <Controller
+                  name='certAuth'
+                  control={control}
+                  render={({ field }) => (
+                    <CheckboxCard.Root
+                      size='sm'
+                      mb='3'
+                      checked={field.value}
+                      disabled={saving}
+                      onCheckedChange={({ checked }) => {
+                        field.onChange(checked === true)
+                        // the certificate proves the national ID: it is the only credential
+                        if (checked === true) setValue('credentials', [...CERT_AUTH_CREDENTIALS])
+                      }}
+                      {...optionCardStyles}
+                    >
+                      <CheckboxCard.HiddenInput />
+                      <CheckboxCard.Control px='3' py='2.5' gap='2.5'>
+                        <CheckboxCard.Indicator mt='0.5' />
+                        <CheckboxCard.Content gap='0.5'>
+                          <CheckboxCard.Label fontSize='sm' fontWeight='medium'>
+                            {t('voter_auth.cert.label', { defaultValue: 'Digital certificate' })}
+                          </CheckboxCard.Label>
+                          <CheckboxCard.Description fontSize='xs' color='fg.muted'>
+                            {t('voter_auth.cert.description', {
+                              defaultValue:
+                                'Voters sign in with their Spanish personal certificate (FNMT, idCAT…), which proves their DNI/NIE.',
+                            })}
+                          </CheckboxCard.Description>
+                        </CheckboxCard.Content>
+                      </CheckboxCard.Control>
+                    </CheckboxCard.Root>
+                  )}
+                />
+              )}
               <Controller
                 name='credentials'
                 control={control}
@@ -308,7 +352,7 @@ export const VoterAuthForm = ({ census, group, onCancel, onSave }: VoterAuthForm
                             value={id}
                             size='sm'
                             flex='0 0 auto'
-                            disabled={saving || (!checked && atLimit)}
+                            disabled={saving || certAuth || (!checked && atLimit)}
                             {...optionCardStyles}
                           >
                             <CheckboxCard.HiddenInput />
```
Edit `.env.example`, after the line `DISABLE_ANONYMOUS_VOTING=` insert:

```
# Certificate gateway origin, port included (e.g. https://cert.vocdoni.io:15140). Unset hides the
# "digital certificate" voter sign-in option.
CERT_GATEWAY_URL=
```
Edit `README.md`, after the `DISABLE_ANONYMOUS_VOTING` bullet (it ends with `creation wizard. Unset or any other value
leaves the option available. Read at runtime.`) insert:

```markdown
- `CERT_GATEWAY_URL` the https origin of the certificate gateway, port included (for example
  `https://cert.vocdoni.io:15140`). When unset, the "digital certificate" voter sign-in option is hidden.
```
Run: `cd /home/lucasmenendez/vocdoni-app && npx prettier --check src && npx tsc --noEmit -p . && npx vitest run src/app-env-build.test.ts src/components/Process/Create`
Expected: `All matched files use Prettier code style!`, no tsc output, `Test Files  18 passed (18)` / `Tests  240 passed (240)`

Step 3: commit
`git -c commit.gpgsign=false commit -m "feat(process): offer certificate sign-in to organisers" -- src/app-env-build.ts src/app-env-build.test.ts src/components/Process/Create/common.ts src/components/Process/Create/census-spec.ts src/components/Process/Create/draft-mapping.ts src/components/Process/Create/draft-mapping.test.ts src/components/Process/Create/VoterAuthentication/utils.ts src/components/Process/Create/VoterAuthentication/useCensusCheck.ts src/components/Process/Create/VoterAuthentication/VoterAuthForm.tsx src/components/Process/Create/VoterAuthentication/index.test.tsx .env.example README.md`

Interfaces this task provides to later tasks: `AppEnv.CERT_GATEWAY_URL?: string` (an https origin, port included); `Census.certAuth?: boolean`; `VoterAuthFormData.certAuth`; `CERT_AUTH_CREDENTIALS`.

### Task 18: Voter certificate flow (state, PKCE, fragment, errors)
Model: strong   Repo: vocdoni-app   Files: `src/components/Process/CSP/cert-auth.ts` (new), `src/components/Process/CSP/cert-auth.test.ts` (new), `src/components/Process/CSP/CertAuth.tsx` (new), `src/components/Process/CSP/CertAuth.test.tsx` (new), `src/components/Process/CSP/CSPStepsProvider.tsx`, `src/components/Process/CSP/CSPAuthModal.tsx`, `src/components/Process/CSP/Step0.tsx`, `src/components/Process/CSP/basics.tsx`
Goal: C3 voter side 1–6b — a single certificate button (terms required), `state` (16 bytes) and `codeVerifier` (32 bytes) in `sessionStorage["certAuth:<processId>"]`, top-level navigation to `${CERT_GATEWAY_URL}/v1/auth` with `returnUrl = origin + /[lang/]processes/<id>`, on return: state check, fragment stripped, entry deleted only on a match, `auth0({certCode, codeVerifier[, email|phone]})`, localised errors with the "close the browser" note (every non-retryable code) or a retry (retryable codes), the network note, never `pushCrispUserFields` (AC8 bullets 2–4, AC11 app half).

Step 1: write the failing tests
Create `src/components/Process/CSP/cert-auth.test.ts`:

```ts
import { certAuthStorageKey, codeChallengeOf, readCertReturn, startCertAuth } from './cert-auth'

const PID = '0123456789abcdef01234567'

const fakeWindow = (hash: string) => {
  const history = { state: { k: 1 }, replaceState: vi.fn() }
  const location = { hash, pathname: `/es/processes/${PID}`, search: '' }
  return { history, location }
}

describe('cert-auth', () => {
  beforeEach(() => sessionStorage.clear())

  it('computes the RFC 7636 S256 challenge', async () => {
    expect(await codeChallengeOf('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk')).toBe(
      'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM'
    )
  })

  it('stores only state and codeVerifier, and sends the challenge to the gateway', async () => {
    const target = new URL(
      await startCertAuth({
        gatewayUrl: 'https://cert.vocdoni.io:15140',
        processId: PID,
        returnUrl: `https://app.vocdoni.io/es/processes/${PID}`,
        language: 'es',
      })
    )
    expect(sessionStorage).toHaveLength(1)
    const stored = JSON.parse(sessionStorage.getItem(certAuthStorageKey(PID))!)
    expect(Object.keys(stored).sort()).toEqual(['codeVerifier', 'state'])
    expect(stored.state).toMatch(/^[A-Za-z0-9_-]{22}$/)
    expect(stored.codeVerifier).toMatch(/^[A-Za-z0-9_-]{43}$/)

    expect(target.origin + target.pathname).toBe('https://cert.vocdoni.io:15140/v1/auth')
    expect(Object.fromEntries(target.searchParams)).toEqual({
      processId: PID,
      returnUrl: `https://app.vocdoni.io/es/processes/${PID}`,
      state: stored.state,
      codeChallenge: await codeChallengeOf(stored.codeVerifier),
      lang: 'es',
    })
  })

  it('sends no lang the gateway does not speak', async () => {
    const target = new URL(
      await startCertAuth({ gatewayUrl: 'https://g.test', processId: PID, returnUrl: 'https://a.test', language: 'de' })
    )
    expect(target.searchParams.has('lang')).toBe(false)
  })

  it('accepts a code with the stored state, strips the fragment and deletes the entry', () => {
    sessionStorage.setItem(
      certAuthStorageKey(PID),
      JSON.stringify({ state: 's'.repeat(22), codeVerifier: 'v'.repeat(43) })
    )
    const { history, location } = fakeWindow(`#certCode=${'c'.repeat(43)}&state=${'s'.repeat(22)}`)

    expect(readCertReturn(PID, { history, location })).toEqual({
      kind: 'code',
      certCode: 'c'.repeat(43),
      codeVerifier: 'v'.repeat(43),
    })
    expect(history.replaceState).toHaveBeenCalledWith({ k: 1 }, '', `/es/processes/${PID}`)
    expect(sessionStorage.getItem(certAuthStorageKey(PID))).toBeNull()
  })

  it('reads an error code', () => {
    sessionStorage.setItem(
      certAuthStorageKey(PID),
      JSON.stringify({ state: 's'.repeat(22), codeVerifier: 'v'.repeat(43) })
    )
    const { history, location } = fakeWindow(`#certError=not_in_census&state=${'s'.repeat(22)}`)
    expect(readCertReturn(PID, { history, location })).toEqual({ kind: 'error', code: 'not_in_census' })
  })

  it.each([
    ['another state', JSON.stringify({ state: 'x'.repeat(22), codeVerifier: 'v'.repeat(43) })],
    ['nothing stored', null],
  ])('a state mismatch (%s) strips the fragment, keeps the entry and yields no code', (_, stored) => {
    if (stored) sessionStorage.setItem(certAuthStorageKey(PID), stored)
    const { history, location } = fakeWindow(`#certCode=${'c'.repeat(43)}&state=${'s'.repeat(22)}`)

    expect(readCertReturn(PID, { history, location })).toEqual({ kind: 'mismatch' })
    expect(history.replaceState).toHaveBeenCalled()
    expect(sessionStorage.getItem(certAuthStorageKey(PID))).toBe(stored)
  })

  it('leaves any other fragment alone', () => {
    const { history, location } = fakeWindow('#questions')
    expect(readCertReturn(PID, { history, location })).toBeNull()
    expect(history.replaceState).not.toHaveBeenCalled()
  })
})
```
Create `src/components/Process/CSP/CertAuth.test.tsx`:

```tsx
import userEvent from '@testing-library/user-event'
import { render, screen, waitFor } from '~src/test-utils'
import { setReactProvidersMock } from '~src/test-utils-react-providers-mock'
import { certAuthStorageKey, RETRYABLE_CERT_ERRORS, STICKY_CERT_ERRORS } from './cert-auth'
import { CertAuthError } from './CertAuth'
import { CspAuth, CspAuthSession } from './CSPAuthModal'

vi.mock('@vocdoni/react-components', async (importOriginal) => {
  const actual = (await importOriginal()) as typeof import('@vocdoni/react-components')
  const { getReactProvidersMock } = await import('~src/test-utils-react-providers-mock')
  return { ...actual, ...getReactProvidersMock() }
})

vi.mock('./Step1', () => ({ Step1Base: () => <div>Step 1</div> }))

const PID = '0123456789abcdef01234567'
const STATE = 's'.repeat(22)
const VERIFIER = 'v'.repeat(43)
const CODE = 'c'.repeat(43)
const GATEWAY = 'https://cert.test:15140'
const appEnv = { CERT_GATEWAY_URL: GATEWAY, CRISP_WEBSITE_ID: 'crisp' }

const auth0 = vi.fn()
const crispPush = vi.fn()

const useCensus = (twoFaFields: string[]) =>
  setReactProvidersMock({
    useElection: () => ({ election: { id: PID, census: { authFields: ['nationalId'], twoFaFields, certAuth: true } } }),
    useElectionAuth: () => ({ connected: false, authToken: null, weight: null, auth0 }),
  })

/** Lands on the voting page as the gateway sends the voter back. */
const landWith = (fragment: string, stored: object | null = { state: STATE, codeVerifier: VERIFIER }) => {
  if (stored) sessionStorage.setItem(certAuthStorageKey(PID), JSON.stringify(stored))
  window.history.replaceState(null, '', `/en/processes/${PID}#${fragment}`)
  render(
    <CspAuthSession>
      <CspAuth />
    </CspAuthSession>,
    { appEnv }
  )
}

describe('certificate voter flow', () => {
  beforeEach(() => {
    sessionStorage.clear()
    auth0.mockReset().mockResolvedValue(undefined)
    crispPush.mockReset()
    ;(window as any).$crisp = { push: crispPush }
    window.history.replaceState(null, '', `/en/processes/${PID}`)
  })

  it('requires the terms, then sends the voter to the gateway, storing only state and verifier', async () => {
    useCensus([])
    const navigate = vi.fn()
    const user = userEvent.setup()
    const { CertAuthStart } = await import('./CertAuth')
    render(<CertAuthStart navigate={navigate} />, { appEnv })

    const start = screen.getByRole('button', { name: 'Sign in with a digital certificate' })
    expect(start).toBeDisabled()
    expect(screen.getByText(/try again from another network, such as mobile data/)).toBeInTheDocument()
    await user.click(screen.getByRole('checkbox'))
    await user.click(start)

    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1))
    const target = new URL(navigate.mock.calls[0][0])
    expect(target.origin + target.pathname).toBe(`${GATEWAY}/v1/auth`)
    expect(target.searchParams.get('processId')).toBe(PID)
    expect(target.searchParams.get('returnUrl')).toBe(`${window.location.origin}/en/processes/${PID}`)
    expect(target.searchParams.get('lang')).toBe('en')
    expect(Object.keys(sessionStorage)).toEqual([certAuthStorageKey(PID)])
    expect(Object.keys(JSON.parse(sessionStorage.getItem(certAuthStorageKey(PID))!)).sort()).toEqual([
      'codeVerifier',
      'state',
    ])
  })

  it('shows the certificate button instead of the login fields', async () => {
    useCensus([])
    const user = userEvent.setup()
    render(
      <CspAuthSession>
        <CspAuth />
      </CspAuthSession>,
      { appEnv }
    )
    await user.click(screen.getByRole('button', { name: /login/i }))
    expect(await screen.findByRole('button', { name: 'Sign in with a digital certificate' })).toBeInTheDocument()
    expect(document.querySelector('input[name="nationalId"]')).toBeNull()
  })

  it('auth-only: redeems the code once, strips the fragment and forgets the flow', async () => {
    useCensus([])
    landWith(`certCode=${CODE}&state=${STATE}`)

    await waitFor(() => expect(auth0).toHaveBeenCalledWith({ certCode: CODE, codeVerifier: VERIFIER }))
    expect(auth0).toHaveBeenCalledTimes(1)
    expect(window.location.hash).toBe('')
    expect(sessionStorage.getItem(certAuthStorageKey(PID))).toBeNull()
    expect(crispPush).not.toHaveBeenCalled()
  })

  it('with 2FA: asks for the contact, sends it with the code, then the code step', async () => {
    useCensus(['email'])
    const user = userEvent.setup()
    landWith(`certCode=${CODE}&state=${STATE}`)

    await user.type(await screen.findByRole('textbox'), ' voter@example.com ')
    await user.click(screen.getByRole('button', { name: 'Receive Code' }))

    await waitFor(() =>
      expect(auth0).toHaveBeenCalledWith({ certCode: CODE, codeVerifier: VERIFIER, email: 'voter@example.com' })
    )
    expect(await screen.findByText('Step 1')).toBeInTheDocument()
    expect(crispPush).not.toHaveBeenCalled()
  })

  it.each([
    ['a state that is not the stored one', { state: 'x'.repeat(22), codeVerifier: VERIFIER }],
    ['nothing stored', null],
  ])('%s: strips the fragment, shows the generic error and calls nothing', async (_, stored) => {
    useCensus([])
    landWith(`certCode=${CODE}&state=${STATE}`, stored)

    expect(await screen.findByText("We couldn't sign you in with your certificate.")).toBeInTheDocument()
    expect(window.location.hash).toBe('')
    expect(auth0).not.toHaveBeenCalled()
  })

  it('shows a gateway error from the fragment', async () => {
    useCensus([])
    landWith(`certError=not_in_census&state=${STATE}`)
    expect(
      await screen.findByText("The DNI/NIE of this certificate isn't in the census of this vote.")
    ).toBeInTheDocument()
    expect(auth0).not.toHaveBeenCalled()
  })

  it.each(STICKY_CERT_ERRORS)('%s: tells the voter to restart the browser, with no retry', (code) => {
    useCensus([])
    render(<CertAuthError code={code} />, { appEnv })
    expect(screen.getByText(/close every window of the browser and open the voting link again/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument()
  })

  it.each(RETRYABLE_CERT_ERRORS)('%s: offers a retry', async (code) => {
    useCensus([])
    const navigate = vi.fn()
    const user = userEvent.setup()
    render(<CertAuthError code={code} navigate={navigate} />, { appEnv })
    expect(screen.queryByText(/close every window of the browser/)).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(expect.stringContaining(`${GATEWAY}/v1/auth?`)))
  })
})
```
Run: `cd /home/lucasmenendez/vocdoni-app && npx vitest run src/components/Process/CSP/cert-auth.test.ts src/components/Process/CSP/CertAuth.test.tsx`
Expected: `FAIL` for both files: `Failed to resolve import "./cert-auth"` / `"./CertAuth"`

Step 2: implement
Create `src/components/Process/CSP/cert-auth.ts`:

```ts
// Certificate voter authentication: the browser side of the certificate gateway flow.
//
// The voting page sends the voter to the gateway with a random `state` and the S256 challenge of
// a random PKCE `codeVerifier`, both kept in this tab's sessionStorage. The gateway sends the voter
// back to the voting page with `#certCode=…&state=…` (or `#certError=…&state=…`) in the fragment.
// The page accepts the code only when `state` matches the stored one, and redeems it at step 0 with
// the verifier. Nothing else is stored: no DNI, name or certificate data ever reaches the browser.

/**
 * Every non-retryable gateway and backend error code: retrying as is gets the same answer, because
 * the browser keeps offering the same certificate (or none) until it is restarted.
 */
export const STICKY_CERT_ERRORS = [
  'no_certificate',
  'not_in_census',
  'zero_weight',
  'not_cert_census',
  'anonymous_unavailable',
  'process_not_open',
  'provider_mismatch',
  'untrusted_issuer',
  'unsupported_profile',
  'invalid_chain',
  'expired',
  'not_yet_valid',
  'unsupported_purpose',
  'weak_algorithm',
  'test_certificate',
  'identity_invalid',
  'identity_unsupported_document',
  'revoked',
  'revocation_unknown',
] as const

/** Error codes worth retrying as is, a bit later. */
export const RETRYABLE_CERT_ERRORS = [
  'revocation_unavailable',
  'internal',
  'backend_unavailable',
  'rate_limited',
] as const

/** The gateway pages' languages. */
const GATEWAY_LANGS = ['es', 'ca', 'en']

type StoredFlow = { state: string; codeVerifier: string }

/** What the voting page found in its fragment on load. */
export type CertReturn =
  | { kind: 'code'; certCode: string; codeVerifier: string }
  | { kind: 'error'; code: string }
  // a fragment whose state is not the one this tab stored (or none was): nothing is called
  | { kind: 'mismatch' }

export const certAuthStorageKey = (processId: string) => `certAuth:${processId}`

const base64Url = (bytes: Uint8Array) =>
  btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

const randomBase64Url = (size: number) => base64Url(crypto.getRandomValues(new Uint8Array(size)))

/** base64url(sha256(verifier)), unpadded: the PKCE S256 challenge. */
export const codeChallengeOf = async (codeVerifier: string) =>
  base64Url(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(codeVerifier))))

/**
 * The URL of the gateway's /v1/auth for a voting page: stores a fresh state and verifier for the
 * process in sessionStorage and returns where to navigate (top level).
 */
export const startCertAuth = async ({
  gatewayUrl,
  processId,
  returnUrl,
  language,
  storage = window.sessionStorage,
}: {
  gatewayUrl: string
  processId: string
  returnUrl: string
  language: string
  storage?: Pick<Storage, 'setItem'>
}): Promise<string> => {
  const flow: StoredFlow = { state: randomBase64Url(16), codeVerifier: randomBase64Url(32) }
  storage.setItem(certAuthStorageKey(processId), JSON.stringify(flow))
  const url = new URL('/v1/auth', gatewayUrl)
  url.searchParams.set('processId', processId)
  url.searchParams.set('returnUrl', returnUrl)
  url.searchParams.set('state', flow.state)
  url.searchParams.set('codeChallenge', await codeChallengeOf(flow.codeVerifier))
  const lang = language.split('-')[0]
  if (GATEWAY_LANGS.includes(lang)) url.searchParams.set('lang', lang)
  return url.toString()
}

/**
 * Reads the gateway's answer from the fragment, if any. It always strips the fragment (without a
 * navigation) and deletes the stored flow only when `state` matches it. Returns null when the
 * fragment carries no certificate answer.
 */
export const readCertReturn = (
  processId: string,
  {
    location = window.location,
    history = window.history,
    storage = window.sessionStorage,
  }: {
    location?: Pick<Location, 'hash' | 'pathname' | 'search'>
    history?: Pick<History, 'replaceState' | 'state'>
    storage?: Pick<Storage, 'getItem' | 'removeItem'>
  } = {}
): CertReturn | null => {
  const params = new URLSearchParams(location.hash.replace(/^#/, ''))
  const certCode = params.get('certCode')
  const certError = params.get('certError')
  if (!certCode && !certError) return null

  history.replaceState(history.state, '', location.pathname + location.search)
  let stored: StoredFlow | null = null
  try {
    stored = JSON.parse(storage.getItem(certAuthStorageKey(processId)) ?? 'null')
  } catch {
    stored = null
  }
  if (!stored?.state || stored.state !== params.get('state')) return { kind: 'mismatch' }

  storage.removeItem(certAuthStorageKey(processId))
  if (certCode) return { kind: 'code', certCode, codeVerifier: stored.codeVerifier }
  return { kind: 'error', code: certError ?? 'internal' }
}
```
Create `src/components/Process/CSP/CertAuth.tsx`:

```tsx
import {
  AlertDescription,
  AlertIndicator,
  AlertRoot as Alert,
  Button,
  CheckboxControl,
  CheckboxHiddenInput,
  CheckboxLabel,
  CheckboxRoot,
  Dialog,
  FieldHelperText as FormHelperText,
  FieldLabel as FormLabel,
  FieldRoot as FormControl,
  Input,
  Link,
  Portal,
  Spinner,
  Stack,
  Text,
} from '@chakra-ui/react'
import type { OrgMemberTwoFaField } from '@vocdoni/api-types'
import { useElection } from '@vocdoni/react-components'
import type { TFunction } from 'i18next'
import { useEffect, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { Trans, useTranslation } from 'react-i18next'
import { normalizePublicLanguageCandidate } from '~i18n/public-language'
import { useToast } from '~components/Toast'
import { CloseButton } from '~components/ui/CloseButton'
import { DialogHeader } from '~components/ui/DialogHeader'
import { useAppEnv, useLanguagesEnv } from '~src/app-env'
import { useCspAuth0 } from './basics'
import { RETRYABLE_CERT_ERRORS, startCertAuth, STICKY_CERT_ERRORS } from './cert-auth'
import { useCspAuthContext } from './CSPStepsProvider'
import { getContactFieldLabel } from './fields'
import { Step1Base } from './Step1'

/**
 * Starts the certificate flow: a top-level navigation to the gateway, which comes back to this
 * process' voting page (`/[lang/]processes/<id>`, also from a home-process root page).
 */
const useStartCertAuth = (navigate: (url: string) => void = (url) => window.location.assign(url)) => {
  const { election } = useElection()
  const { i18n } = useTranslation()
  const { CERT_GATEWAY_URL: gatewayUrl } = useAppEnv()
  const supportedLanguages = Object.keys(useLanguagesEnv())

  if (!gatewayUrl || !election) return null
  return async () => {
    const lang = normalizePublicLanguageCandidate(i18n.language, supportedLanguages)
    const path = `${lang ? `/${lang}` : ''}/processes/${election.id}`
    navigate(
      await startCertAuth({
        gatewayUrl,
        processId: election.id,
        returnUrl: window.location.origin + path,
        language: i18n.language,
      })
    )
  }
}

/** Step 0 of a certificate census: the terms, then the certificate button. */
export const CertAuthStart = ({ navigate }: { navigate?: (url: string) => void }) => {
  const { t } = useTranslation()
  const { PRIVACY_POLICY_URL: privacyPolicyUrl, TERMS_OF_SERVICE_URL: termsOfServiceUrl } = useAppEnv()
  const start = useStartCertAuth(navigate)
  const [accepted, setAccepted] = useState(false)
  const [starting, setStarting] = useState(false)

  if (!start) {
    return (
      <Alert status='error'>
        <AlertIndicator />
        <AlertDescription>
          {t('csp.cert.unavailable', { defaultValue: 'Signing in with a digital certificate is not available here.' })}
        </AlertDescription>
      </Alert>
    )
  }

  return (
    <Stack gap='4'>
      <FormControl required>
        <CheckboxRoot checked={accepted} onCheckedChange={({ checked }) => setAccepted(checked === true)}>
          <CheckboxHiddenInput />
          <CheckboxControl />
          <CheckboxLabel>
            <Trans i18nKey='csp.terms_acceptance'>
              I have read and accept the{' '}
              <Link href={termsOfServiceUrl} target='_blank' rel='noopener noreferrer'>
                Terms and Conditions
              </Link>{' '}
              and the{' '}
              <Link href={privacyPolicyUrl} target='_blank' rel='noopener noreferrer'>
                Privacy Policy
              </Link>
              .
            </Trans>
          </CheckboxLabel>
        </CheckboxRoot>
      </FormControl>
      <Button
        w='full'
        disabled={!accepted}
        loading={starting}
        onClick={async () => {
          setStarting(true)
          await start()
        }}
      >
        {t('csp.cert.start', { defaultValue: 'Sign in with a digital certificate' })}
      </Button>
      <Text textStyle='body-sm' color='fg.muted'>
        {t('csp.cert.network_note', {
          defaultValue:
            "If the certificate page doesn't load, your network may block it: try again from another network, such as mobile data.",
        })}
      </Text>
    </Stack>
  )
}

/** The voter-facing message of a gateway or backend error code. */
const certErrorMessage = (t: TFunction, code: string): string => {
  switch (code) {
    case 'no_certificate':
      return t('csp.cert.errors.no_certificate', { defaultValue: 'No certificate was selected.' })
    case 'not_in_census':
      return t('csp.cert.errors.not_in_census', {
        defaultValue: "The DNI/NIE of this certificate isn't in the census of this vote.",
      })
    case 'zero_weight':
      return t('csp.cert.errors.zero_weight', {
        defaultValue: "You don't have enough voting power to access the vote.",
      })
    case 'provider_mismatch':
    case 'untrusted_issuer':
    case 'unsupported_profile':
      return t('csp.cert.errors.unsupported_certificate', {
        defaultValue:
          "This certificate isn't accepted. Use a personal certificate from FNMT, idCAT, ACCV, DIGITEL, Bewor or Sectigo.",
      })
    case 'expired':
    case 'not_yet_valid':
      return t('csp.cert.errors.expired', { defaultValue: "This certificate isn't valid today." })
    case 'revoked':
      return t('csp.cert.errors.revoked', { defaultValue: 'This certificate has been revoked.' })
    case 'revocation_unavailable':
    case 'internal':
      return t('csp.cert.errors.check_unavailable', {
        defaultValue: "We couldn't check your certificate right now. Try again in a few minutes.",
      })
    case 'backend_unavailable':
      return t('csp.cert.errors.backend_unavailable', {
        defaultValue: "The voting service didn't answer. Try again in a few minutes.",
      })
    case 'rate_limited':
      return t('csp.cert.errors.rate_limited', { defaultValue: 'Too many attempts. Try again later.' })
    case 'process_not_open':
    case 'not_cert_census':
    case 'anonymous_unavailable':
      return t('csp.cert.errors.process_unavailable', {
        defaultValue: "This vote can't be accessed with a certificate right now.",
      })
    default:
      return t('csp.cert.errors.generic', { defaultValue: "We couldn't sign you in with your certificate." })
  }
}

/** A failed certificate login: the message, then how to go on. */
export const CertAuthError = ({ code, navigate }: { code: string; navigate?: (url: string) => void }) => {
  const { t } = useTranslation()
  const start = useStartCertAuth(navigate)
  const sticky = (STICKY_CERT_ERRORS as readonly string[]).includes(code)
  const retryable = (RETRYABLE_CERT_ERRORS as readonly string[]).includes(code)

  return (
    <Stack gap='4'>
      <Alert status='error'>
        <AlertIndicator />
        <AlertDescription>{certErrorMessage(t, code)}</AlertDescription>
      </Alert>
      {sticky && (
        <Text textStyle='body-sm'>
          {t('csp.cert.restart_browser', {
            defaultValue:
              'Your browser remembers the certificate you chose. To use a different one, close every window of the browser and open the voting link again.',
          })}
        </Text>
      )}
      {retryable && start && (
        <Button w='full' onClick={() => start()}>
          {t('csp.cert.retry', { defaultValue: 'Try again' })}
        </Button>
      )}
    </Stack>
  )
}

/** The email or phone of a step-0 request, from the contact the voter typed. */
const contactOf = (contact: string, twoFaFields: OrgMemberTwoFaField[]) => {
  const email = twoFaFields.includes('email') && (!twoFaFields.includes('phone') || contact.includes('@'))
  return email ? { email: contact } : { phone: contact }
}

/** Redeems the code of an auth-only certificate census as soon as it is shown. */
const CertAutoLogin = ({ certCode, codeVerifier }: { certCode: string; codeVerifier: string }) => {
  const { t } = useTranslation()
  const auth = useCspAuth0()
  const sent = useRef(false)
  const { mutate } = auth

  useEffect(() => {
    // once, even under StrictMode: the code is single-use
    if (sent.current) return
    sent.current = true
    mutate({ certCode, codeVerifier })
  }, [mutate, certCode, codeVerifier])

  if (auth.isError) {
    return (
      <Alert status='error'>
        <AlertIndicator />
        <AlertDescription>{auth.error.message}</AlertDescription>
      </Alert>
    )
  }
  return (
    <Stack align='center' gap='3' py='4'>
      <Spinner />
      <Text>{t('csp.cert.signing_in', { defaultValue: 'Signing you in…' })}</Text>
    </Stack>
  )
}

/** A certificate census with 2FA: the email or phone, then the existing code step. */
const CertContactForm = ({ certCode, codeVerifier }: { certCode: string; codeVerifier: string }) => {
  const { t } = useTranslation()
  const toast = useToast()
  const { twoFaFields, setAuthData, setCurrentStep } = useCspAuthContext()
  const auth = useCspAuth0()
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<{ contact: string }>()

  const onSubmit = async ({ contact }: { contact: string }) => {
    const destination = contactOf(contact.trim(), twoFaFields)
    try {
      await auth.mutateAsync({ certCode, codeVerifier, ...destination })
    } catch {
      return // shown below; the code is spent, the voter goes through the certificate again
    }
    setAuthData(destination)
    toast({ title: t('csp.code_sent', { defaultValue: 'Verification code sent' }), type: 'success', duration: 3000 })
    setCurrentStep(1)
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)}>
      <Stack gap='4'>
        <FormControl invalid={!!errors.contact} required>
          <FormLabel>{getContactFieldLabel(t, twoFaFields)}</FormLabel>
          <Input {...register('contact', { required: true })} type='text' />
          <FormHelperText>{t('csp.contact_match_help', 'Must match the one registered in the system')}</FormHelperText>
        </FormControl>
        {auth.isError && (
          <Alert status='error'>
            <AlertIndicator />
            <AlertDescription>{auth.error.message}</AlertDescription>
          </Alert>
        )}
        <Button type='submit' w='full' loading={auth.isPending} disabled={auth.isError}>
          {t('csp.receive_code', 'Receive Code')}
        </Button>
      </Stack>
    </form>
  )
}

/** Handles the certificate gateway's answer after the redirect back to the voting page. */
export const CertAuthReturnDialog = () => {
  const { t } = useTranslation()
  const { certReturn, setCertReturn, twoFaFields, currentStep } = useCspAuthContext()

  if (!certReturn) return null

  let body
  if (certReturn.kind !== 'code') {
    body = <CertAuthError code={certReturn.kind === 'error' ? certReturn.code : 'mismatch'} />
  } else if (twoFaFields.length === 0) {
    body = <CertAutoLogin certCode={certReturn.certCode} codeVerifier={certReturn.codeVerifier} />
  } else if (currentStep === 0) {
    body = <CertContactForm certCode={certReturn.certCode} codeVerifier={certReturn.codeVerifier} />
  } else {
    body = <Step1Base />
  }

  return (
    <Dialog.Root open size='sm' onOpenChange={({ open }) => !open && setCertReturn(null)}>
      <Portal>
        <Dialog.Backdrop />
        <Dialog.Positioner p={{ base: '4', md: '6' }}>
          <Dialog.Content w='full' maxW='lg'>
            <Dialog.CloseTrigger asChild>
              <CloseButton />
            </Dialog.CloseTrigger>
            <DialogHeader title={t('csp.cert.title', { defaultValue: 'Digital certificate' })} />
            <Dialog.Body>{body}</Dialog.Body>
          </Dialog.Content>
        </Dialog.Positioner>
      </Portal>
    </Dialog.Root>
  )
}
```
Apply with `git apply`:

```diff
diff --git a/src/components/Process/CSP/basics.tsx b/src/components/Process/CSP/basics.tsx
index e29e6612..dac2dc72 100644
--- a/src/components/Process/CSP/basics.tsx
+++ b/src/components/Process/CSP/basics.tsx
@@ -8,6 +8,8 @@ import { useOptionalCspAuthContext } from './CSPStepsProvider'
 export type CensusData = {
   authFields: OrgMemberAuthField[]
   twoFaFields: OrgMemberTwoFaField[]
+  // voters prove their national ID with a digital certificate (cert-auth.ts)
+  certAuth?: boolean
 }
 
 export type AuthFieldType = OrgMemberAuthField
```
```diff
diff --git a/src/components/Process/CSP/CSPStepsProvider.tsx b/src/components/Process/CSP/CSPStepsProvider.tsx
index a7ca3679..b14488d0 100644
--- a/src/components/Process/CSP/CSPStepsProvider.tsx
+++ b/src/components/Process/CSP/CSPStepsProvider.tsx
@@ -1,6 +1,7 @@
 import { useElectionAuth } from '@vocdoni/react-components'
 import { createContext, useContext, useEffect, useState, type Dispatch, type SetStateAction } from 'react'
 import { AuthFieldType, CensusData, TwoFaFieldType } from './basics'
+import { readCertReturn, type CertReturn } from './cert-auth'
 
 // Contact info captured at step 0 so step 1 can resend the challenge. The auth
 // tokens themselves live in the ProcessProvider session, not here.
@@ -21,6 +22,11 @@ type CspAuthContextState = {
   processId?: string
   // Back to step 0 with no stored contact.
   resetFlow: () => void
+  // A census whose voters sign in with a digital certificate.
+  certAuth: boolean
+  // The certificate gateway's answer found in the URL fragment on load, until it is handled.
+  certReturn: CertReturn | null
+  setCertReturn: (certReturn: CertReturn | null) => void
 }
 
 const CspAuthContext = createContext<CspAuthContextState | undefined>(undefined)
@@ -37,7 +43,9 @@ export const CspAuthProvider = ({
 }) => {
   const [currentStep, setCurrentStep] = useState(0)
   const [authData, setAuthData] = useState<CspAuthData>({})
+  const [certReturn, setCertReturn] = useState<CertReturn | null>(null)
   const { connected } = useElectionAuth()
+  const certAuth = !!censusData?.certAuth
 
   // Bails out when the flow is already at its start, so the mount-time run
   // doesn't re-render every consumer for nothing.
@@ -58,6 +66,18 @@ export const CspAuthProvider = ({
   // a pending step 1 must not carry over to another process.
   useEffect(resetFlow, [processId])
 
+  // The certificate gateway sends the voter back with its answer in the fragment: read it once the
+  // process (and so whether its census uses certificates) is known.
+  useEffect(() => {
+    if (!processId || !certAuth) return
+    const found = readCertReturn(processId)
+    if (found) setCertReturn(found)
+  }, [processId, certAuth])
+
+  useEffect(() => {
+    if (connected) setCertReturn(null)
+  }, [connected])
+
   const value: CspAuthContextState = {
     currentStep,
     setCurrentStep,
@@ -69,6 +89,9 @@ export const CspAuthProvider = ({
     twoFaFields: censusData?.twoFaFields || [],
     processId,
     resetFlow,
+    certAuth,
+    certReturn,
+    setCertReturn,
   }
 
   return <CspAuthContext.Provider value={value}>{children}</CspAuthContext.Provider>
```
```diff
diff --git a/src/components/Process/CSP/CSPAuthModal.tsx b/src/components/Process/CSP/CSPAuthModal.tsx
index 0801aece..1ad0668a 100644
--- a/src/components/Process/CSP/CSPAuthModal.tsx
+++ b/src/components/Process/CSP/CSPAuthModal.tsx
@@ -4,6 +4,7 @@ import { DialogHeader } from '~components/ui/DialogHeader'
 import { useElection } from '@vocdoni/react-components'
 import type { ReactNode } from 'react'
 import { Trans, useTranslation } from 'react-i18next'
+import { CertAuthReturnDialog } from './CertAuth'
 import { CspAuthProvider, useCspAuthContext, useOptionalCspAuthContext } from './CSPStepsProvider'
 import { Step0Base } from './Step0'
 import { Step1Base } from './Step1'
@@ -45,7 +46,9 @@ export const CspAuthSession = ({ children }: { children: ReactNode }) => {
   // The v2 process read carries the census auth configuration inline
   // (CensusSpec.authFields/twoFaFields) — no separate census bundle fetch.
   const census = election?.census
-  const censusData = census ? { authFields: census.authFields ?? [], twoFaFields: census.twoFaFields ?? [] } : null
+  const censusData = census
+    ? { authFields: census.authFields ?? [], twoFaFields: census.twoFaFields ?? [], certAuth: !!census.certAuth }
+    : null
 
   // Always mount the provider, even before the process loads: swapping it in
   // conditionally would change the element type around `children` and remount
@@ -53,6 +56,8 @@ export const CspAuthSession = ({ children }: { children: ReactNode }) => {
   return (
     <CspAuthProvider censusData={censusData} processId={election?.id}>
       {children}
+      {/* once per session: handles the certificate gateway's answer after the redirect back */}
+      <CertAuthReturnDialog />
     </CspAuthProvider>
   )
 }
```
```diff
diff --git a/src/components/Process/CSP/Step0.tsx b/src/components/Process/CSP/Step0.tsx
index 4787c71e..857c1725 100644
--- a/src/components/Process/CSP/Step0.tsx
+++ b/src/components/Process/CSP/Step0.tsx
@@ -24,10 +24,17 @@ import { useToast } from '~components/Toast'
 import { useAppEnv } from '~src/app-env'
 import { hashMemberPassword } from '~utils/member-password'
 import { CSPStep0FormData, CSPStep0RequestData, useCspAuth0, useCspAuthPending, useIsCspAuthBusy } from './basics'
+import { CertAuthStart } from './CertAuth'
 import { useCspAuthContext } from './CSPStepsProvider'
 import { getAuthFieldInputType, getAuthFieldLabel, getContactFieldLabel } from './fields'
 
 export const Step0Base = () => {
+  const { certAuth } = useCspAuthContext()
+  // a certificate census never takes typed login fields
+  return certAuth ? <CertAuthStart /> : <TypedStep0 />
+}
+
+const TypedStep0 = () => {
   const { t } = useTranslation()
   const toast = useToast()
   const { setCurrentStep, setAuthData, authFields, twoFaFields } = useCspAuthContext()
```
Run: `cd /home/lucasmenendez/vocdoni-app && npx prettier --check src && npx tsc --noEmit -p . && npx vitest run src/components/Process/CSP && pnpm lint`
Expected: Prettier clean, no tsc output, `Test Files  8 passed (8)` / `Tests  86 passed (86)`, and `pnpm lint` passing (knip reports no unused export)

Step 3: commit
`git add -- src/components/Process/CSP/cert-auth.ts src/components/Process/CSP/cert-auth.test.ts src/components/Process/CSP/CertAuth.tsx src/components/Process/CSP/CertAuth.test.tsx src/components/Process/CSP/CSPStepsProvider.tsx src/components/Process/CSP/CSPAuthModal.tsx src/components/Process/CSP/Step0.tsx src/components/Process/CSP/basics.tsx && git -c commit.gpgsign=false commit -m "feat(csp): sign voters in with a digital certificate" -- src/components/Process/CSP/cert-auth.ts src/components/Process/CSP/cert-auth.test.ts src/components/Process/CSP/CertAuth.tsx src/components/Process/CSP/CertAuth.test.tsx src/components/Process/CSP/CSPStepsProvider.tsx src/components/Process/CSP/CSPAuthModal.tsx src/components/Process/CSP/Step0.tsx src/components/Process/CSP/basics.tsx`

Interfaces this task provides to later tasks: i18n keys `csp.cert.*` and `voter_auth.cert.*` with English `defaultValue`s (Task 19 extracts them).

### Task 19: Strings in en, es and ca (English in the other locales)
Model: fast   Repo: vocdoni-app   Files: `src/i18n/locales/{ca,de,el,en,es,eu,fr,it,pt,pt-br}/common.json`
Goal: C3 "Strings go in en, es and ca. The other locales take English" — `src/i18n/locales/index.test.ts` refuses empty values, so the other locales get the English text.

Step 1: extract
Run: `cd /home/lucasmenendez/vocdoni-app && pnpm translations && npx vitest run src/i18n/locales/index.test.ts`
Expected: `Extraction complete!` (21 new keys per locale, 27 JSON lines: `csp.cert.*` and `voter_auth.cert.*`), then failures `… has no empty translations` for every non-English locale.

Step 2: translate. Save this one-off script outside the repository as `~/.cache/vocdoni-app/fill-cert-translations.mjs` (never commit it); it fills only the new keys that are still empty, with the es and ca texts below (reviewed against `src/i18n/contexts/es.md` and `ca.md`: informal register, "proceso de votación"/"procés de votació", "censo"/"cens") and English elsewhere:

```js
// One-off: fills the certificate-auth keys `pnpm translations` added as empty strings. es and ca get
// their translations; every other locale takes the English text, as the spec asks. Not committed.
import { readFileSync, writeFileSync } from 'node:fs'

const dir = 'src/i18n/locales'
const tr = {
  es: {
    'csp.cert.errors.backend_unavailable': 'El servicio de votación no ha respondido. Vuelve a intentarlo en unos minutos.',
    'csp.cert.errors.check_unavailable': 'Ahora mismo no podemos comprobar tu certificado. Vuelve a intentarlo en unos minutos.',
    'csp.cert.errors.expired': 'Este certificado no es válido hoy.',
    'csp.cert.errors.generic': 'No hemos podido identificarte con tu certificado.',
    'csp.cert.errors.no_certificate': 'No se ha seleccionado ningún certificado.',
    'csp.cert.errors.not_in_census': 'El DNI/NIE de este certificado no está en el censo de este proceso de votación.',
    'csp.cert.errors.process_unavailable': 'Ahora mismo no se puede acceder a este proceso de votación con certificado.',
    'csp.cert.errors.rate_limited': 'Demasiados intentos. Vuelve a intentarlo más tarde.',
    'csp.cert.errors.revoked': 'Este certificado ha sido revocado.',
    'csp.cert.errors.unsupported_certificate':
      'Este certificado no se acepta. Usa un certificado personal de FNMT, idCAT, ACCV, DIGITEL, Bewor o Sectigo.',
    'csp.cert.errors.zero_weight': 'No tienes suficiente poder de voto para acceder a la votación.',
    'csp.cert.network_note':
      'Si la página del certificado no carga, puede que tu red la bloquee: inténtalo desde otra red, como los datos móviles.',
    'csp.cert.restart_browser':
      'Tu navegador recuerda el certificado que elegiste. Para usar otro, cierra todas las ventanas del navegador y vuelve a abrir el enlace de la votación.',
    'csp.cert.retry': 'Volver a intentarlo',
    'csp.cert.signing_in': 'Identificándote…',
    'csp.cert.start': 'Acceder con certificado digital',
    'csp.cert.title': 'Certificado digital',
    'csp.cert.unavailable': 'Aquí no está disponible el acceso con certificado digital.',
    'voter_auth.cert.description':
      'Los votantes se identifican con su certificado personal (FNMT, idCAT…), que acredita su DNI/NIE.',
    'voter_auth.cert.label': 'Certificado digital',
    'voter_auth.cert.missing_label': 'un DNI/NIE válido (falta o no es válido)',
  },
  ca: {
    'csp.cert.errors.backend_unavailable': 'El servei de votació no ha respost. Torna-ho a provar d’aquí a uns minuts.',
    'csp.cert.errors.check_unavailable': 'Ara mateix no podem comprovar el teu certificat. Torna-ho a provar d’aquí a uns minuts.',
    'csp.cert.errors.expired': 'Aquest certificat no és vàlid avui.',
    'csp.cert.errors.generic': 'No t’hem pogut identificar amb el teu certificat.',
    'csp.cert.errors.no_certificate': 'No s’ha seleccionat cap certificat.',
    'csp.cert.errors.not_in_census': 'El DNI/NIE d’aquest certificat no és al cens d’aquest procés de votació.',
    'csp.cert.errors.process_unavailable': 'Ara mateix no es pot accedir a aquest procés de votació amb certificat.',
    'csp.cert.errors.rate_limited': 'Massa intents. Torna-ho a provar més tard.',
    'csp.cert.errors.revoked': 'Aquest certificat ha estat revocat.',
    'csp.cert.errors.unsupported_certificate':
      'Aquest certificat no s’accepta. Fes servir un certificat personal de FNMT, idCAT, ACCV, DIGITEL, Bewor o Sectigo.',
    'csp.cert.errors.zero_weight': 'No tens prou poder de vot per accedir a la votació.',
    'csp.cert.network_note':
      'Si la pàgina del certificat no es carrega, potser la teva xarxa la bloqueja: prova-ho des d’una altra xarxa, com les dades mòbils.',
    'csp.cert.restart_browser':
      'El navegador recorda el certificat que vas triar. Per fer-ne servir un altre, tanca totes les finestres del navegador i torna a obrir l’enllaç de la votació.',
    'csp.cert.retry': 'Torna-ho a provar',
    'csp.cert.signing_in': 'T’estem identificant…',
    'csp.cert.start': 'Accedeix amb certificat digital',
    'csp.cert.title': 'Certificat digital',
    'csp.cert.unavailable': 'Aquí no està disponible l’accés amb certificat digital.',
    'voter_auth.cert.description':
      'Els votants s’identifiquen amb el seu certificat personal (FNMT, idCAT…), que acredita el seu DNI/NIE.',
    'voter_auth.cert.label': 'Certificat digital',
    'voter_auth.cert.missing_label': 'un DNI/NIE vàlid (falta o no és vàlid)',
  },
}

const get = (obj, path) => path.split('.').reduce((o, k) => o?.[k], obj)
const set = (obj, path, value) => {
  const keys = path.split('.')
  const last = keys.pop()
  keys.reduce((o, k) => o[k], obj)[last] = value
}
const en = JSON.parse(readFileSync(`${dir}/en/common.json`, 'utf8'))
const keys = Object.keys(tr.es)
for (const lang of ['ca', 'de', 'el', 'es', 'eu', 'fr', 'it', 'pt', 'pt-br']) {
  const file = `${dir}/${lang}/common.json`
  const json = JSON.parse(readFileSync(file, 'utf8'))
  for (const key of keys) {
    if (get(json, key) === '') set(json, key, tr[lang]?.[key] ?? get(en, key))
  }
  writeFileSync(file, JSON.stringify(json, null, 2) + '\n')
}
```
Run: `cd /home/lucasmenendez/vocdoni-app && node ~/.cache/vocdoni-app/fill-cert-translations.mjs && npx prettier --check src && npx vitest run src/i18n && git diff --stat src/i18n`
Expected: Prettier clean, `Test Files  5 passed (5)` / `Tests  66 passed (66)`, and 10 files with 27 insertions each (no deletions). Check that no `{{ … }}` placeholder was introduced (none of these strings has one).
Then the whole app: `cd /home/lucasmenendez/vocdoni-app && pnpm lint && pnpm test` → lint passes; `Test Files  236 passed | 1 skipped (237)` / `Tests  2091 passed | 1 skipped (2092)` on the scratch base (counts grow with develop).

Step 3: commit
`git -c commit.gpgsign=false commit -m "feat(i18n): translate certificate sign-in" -- src/i18n/locales/ca/common.json src/i18n/locales/de/common.json src/i18n/locales/el/common.json src/i18n/locales/en/common.json src/i18n/locales/es/common.json src/i18n/locales/eu/common.json src/i18n/locales/fr/common.json src/i18n/locales/it/common.json src/i18n/locales/pt/common.json src/i18n/locales/pt-br/common.json`

Interfaces this task provides to later tasks: none (last task).


## Merge and release
<!-- Not copied into briefs; the controller executes this after the gate audit. -->

1. Full suites, on the final commit of each branch:
   - saas-backend: `go test -failfast -timeout=45m ./... -count=1` → `ok` for every package (scratch run: all 24 packages,
     `api` 384 s); `golangci-lint run --new-from-rev=origin/main ./...` → `0 issues.`; `./scripts/check-qt-patterns.sh` →
     no anti-patterns; `go mod tidy -diff` → empty; `PATH="$(go env GOPATH)/bin:$PATH" make swagger` → no diff.
   - saas-backend, before opening the PR and again immediately before merging it:
     `git fetch origin && git ls-tree --name-only origin/main migrations/ | grep -E '/00(3[7-9]|[4-9][0-9])_'` → no
     output. If it prints anything, renumber this branch's two migrations above the highest one on main (file names,
     the `AddMigration` versions, `migrationByVersion(c, 37)` in `db/migration_canonical_national_id_test.go`) and
     re-run `go test ./db/ ./migrations/`.
   - cert-gateway: `make test && make test-devca && make lint` → `ok` for `cmd/cert-gateway`, `internal/certs`,
     `internal/gateway` (both builds), `0 issues.` twice; `go mod tidy -diff` → empty.
   - vocdoni-integrator-sdk: `pnpm test` → `Test Files  62 passed (62)`, `Tests  906 passed (906)` at base 2be5f7e;
     `pnpm lint` → `6 successful`.
   - vocdoni-app: `pnpm lint && pnpm test` → lint clean; `236 passed | 1 skipped` files at base 6daf44fb (more on a
     newer develop).
2. Merge order:
   1. saas-backend `feat/cert-auth` → `main` (PR; feature off until keys are set), then the usual `main → stage → lts`.
   2. cert-gateway `main` (new repository; first push after the repo exists).
   3. vocdoni-integrator-sdk `feat/cert-auth` → `main` (PR; post `/integration saas-backend=<PR>` as a separate comment
      while the backend PR is unmerged), then the `chore(release): version packages` PR → npm.
   4. vocdoni-app `feat/cert-auth` (Tasks 16–19, after the npm release) → `develop` (PR; `/integration saas-backend=<PR>
      integrator-sdk=<PR>` if needed), then `develop → stage → main`.
3. Release: integrator-sdk through changesets (expected `@vocdoni/api-types` 2.6.0, `@vocdoni/react-providers` 4.0.3 and
   patch bumps of their dependents); saas-backend and vocdoni-app through their branch promotions; cert-gateway dev
   deploys from `main`, prod from the `release` branch (fast-forward to a `main` commit dev has run). No tags are part of
   this plan.
4. **Owner OK required before**, each one separately:
   1. Creating the GitHub repository `vocdoni/saas-cert-gateway` (visibility, branch protection on `main` requiring `ci`), and
      adding its Actions secret `CERT_CHECK_READ_TOKEN`.
   2. Creating the read-only token for `github.com/vocdoni/cert-check` used by CI and Railway builds.
   3. Pushing saas-backend `feat/cert-auth`.
   4. Pushing cert-gateway `main`.
   5. Pushing vocdoni-integrator-sdk `feat/cert-auth`.
   6. Pushing vocdoni-app `feat/cert-auth`.
   7. Opening each PR: saas-backend → `main`; integrator-sdk → `main`; vocdoni-app → `develop`; and each promotion PR
      (saas-backend `main → stage → lts`, vocdoni-app `develop → stage → main`). The saas-backend PR description records
      the docs/SDK impact (suggest a `vocdoni/vocdoni.io` issue for the `stage` docs, and link the SDK PR).
   8. Merging the integrator-sdk release PR, which publishes the npm packages.
   9. Running `scripts/repairlogins` (dry run, built from this branch) against dev, then stg, then prod, and reading the
      collision count before each backend deploy (C2.6).
       A non-zero count is not harmless: each skipped member whose stored national ID is not already canonical
       cannot log in after the deploy (typed or by certificate) until the duplicate is removed and
       `scripts/repairlogins -apply` is run again. List the `SkippedMembers` of open processes and have the
       duplicates removed before deploying.
   10. Each saas-backend deploy (dev, stg, prod): migrations 0037 and 0038 run on start.
   11. Generating the Ed25519 key pairs (`dev-1`, `stg-1`, prod's) and entering each seed as a sealed
       `SIGNING_KEY_<NAME>` on the right Railway service, and each public key in the backend's
       `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS` (dev, stg, prod), one environment at a time.
   12. Creating the Railway project `cert-gateway-dev` (EU West, service settings, volume `/data`, TCP proxy to 8443,
       variables) and later `cert-gateway-prod`.
   13. Creating the dedicated Cloudflare ACME zone and the dev and prod tokens (Zone:Read, DNS:Edit on that zone only),
       entered as sealed `CLOUDFLARE_API_TOKEN`s.
   14. DNS records: `cert-dev.vocdoni.net` and `cert.vocdoni.io` CNAMEs to the TCP proxy hosts, and the two
       `_acme-challenge` CNAMEs into the ACME zone (after the CAA and restricted-port checks).
   15. Each gateway deploy: dev with `ACME_CA=staging`, then dev with `production`; prod likewise.
   16. Creating the cert-gateway `release` branch, and every advance of it.
   17. A Cloudflare WAF skip rule for `/cert-auth/assertions` if the `backend check` line shows 403 (stop and ask if the
       zone runs the free Bot Fight Mode).
   18. Setting `RATE_LIMIT_PER_MIN=0` on the gateways if the dev `remoteAddr` check shows the proxy's address.
   19. Setting `CERT_GATEWAY_URL` (port included) on vocdoni-app dev, then stg (pointing at the dev gateway), then prod,
       and adding custom organisation domains to the prod `BACKENDS` origins.
   20. Choosing `TLS13` for production after AC9 (`false` if any handshake failure was recorded).
5. Post-release checks (the spec's rollout order):
   - `railway logs`: `auth certificate ready`, then `backend check` with status **401** for each backend name (404 = keys
     not set; 403 = WAF/origin secret).
   - The main listener's `/readyz` answers 200 inside Railway; the public URL `https://<DOMAIN>:<port>/v1/auth` without
     parameters answers the 400 page with the hardening headers.
   - The `LOG_LEVEL=debug` `remoteAddr` check from two networks, then `LOG_LEVEL=info`.
   - AC9 manual end-to-end on dev by the owner (FNMT and idCAT software certificates; Windows store with Chrome/Edge,
     Firefox, macOS or Android; auth-only and email-2FA censuses; a DNI outside the census; one cancelled picker; in each
     browser, also leave the certificate picker open for more than 10 seconds before choosing, and record what the voter
     sees).
   - Logs of gateway and backend during AC9 contain no DNI, name, email, phone, `returnUrl` or `state`.
   - AC12 `make dev` walkthrough by the owner (cert-gateway README, "Local development"); until saas-backend is merged,
     run `make dev SAAS_BACKEND_DIR=<the feat/cert-auth worktree>`.
   - Staging: the `stg` entry's `backend check` shows 401 before app-stg gets `CERT_GATEWAY_URL`.
   - Rollback paths as in the spec: unset `VOCDONI_CERT_GATEWAY_PUBLIC_KEYS` (route 404, new cert censuses refused); the
     gateway is stateless.
