# Integrator-attested census for the SaaS CSP

Status: draft for owner approval, 2026-11-09. Code references are saas-backend at `abc28929`.

## 1. Goal

An integrator (first: Decidim) keeps its own census and identifies its voters its own way. The SaaS
keeps doing what it sells: second-factor checks (email OTP, SMS OTP) and CSP signatures. Nothing is
uploaded to the memberbase, and the integrator implements no CSP: it signs a small EIP-712 message.

Non-goals for v1:
- No vochain or sequencer change. The chain keeps verifying one CSP signature (OFF_CHAIN_CA) under
  the SaaS CSP key.
- No MPC or threshold signing. Preventing the SaaS from signing alone is future work (2-of-2 ECDSA,
  or a two-signature census origin on the sequencer).
- No anonymous (blind) censuses yet. The attestation carries no address, so blind signing becomes
  compatible once it returns; until then `attested` with `anonymous` is refused (§4.6).

## 2. Decisions

| # | Decision |
|---|---|
| D1 | The integrator authenticates the voter (step 1) and signs an **attestation**; the SaaS runs the 2FA (step 2) and signs as CSP. The SaaS never calls the integrator. |
| D2 | The integrator's signer is an Ethereum address registered per process, with a rotation history (§4.1, §8). |
| D3 | `voterId` is opaque, chosen by the integrator, stable and unique per person **per process**, e.g. `HMAC(integrator_secret, internal_voter_id ‖ processId)`. |
| D4 | The attestation carries **no** voter address. The integrator never learns which address, and so which ballot, belongs to whom. |
| D5 | The contact appears in the attestation only as `contactHash = keccak256(salt ‖ contact)` with a fresh 32-byte random salt. The voter sends contact and salt to the SaaS, which checks the hash, uses the contact for the 2FA, and never stores or logs the salt. |
| D6 | After the process ends, the SaaS publishes which `voterId`s it signed for and on which questions (§7). The integrator compares that with the chain and lets each voter check their own entry. |

## 3. Attestation

EIP-712 typed data, so integrators sign with stock libraries (ethers, go-ethereum `apitypes`, Ruby
`eth`):

```
domain: {
  name: "Vocdoni CSP attestation",
  version: "1",
  verifyingContract: <the SaaS CSP signer address of this environment>
}

Attestation {
  bytes12 processId;    // SaaS process id (ObjectID bytes); binds org and process
  bytes32 voterId;      // D3
  bytes32 contactHash;  // D5; zero if and only if the process has no 2FA
  uint64  expiresAt;    // unix seconds
}
```

- The verifier rebuilds the domain from server config, never from the request, so an attestation for
  one environment (dev, staging, prod) is worthless in another.
- Signature: 65 bytes r‖s‖v. Accept `v ∈ {0, 1, 27, 28}`; reject `s > N/2`
  (`crypto.ValidateSignatureValues(v, r, s, true)`); reject a recovered or configured `0x0` signer.
  Store it canonical (low-s, `v ∈ {27, 28}`). Integrators must emit low-s.
- `expiresAt` is checked only at `auth/0`: it must be greater than `now - 30s` and at most
  `now + 1h`. A verified token stays valid until the process ends, as tokens do today.
- One attestation covers every question of the process.

### Contact hash

```
contactHash = keccak256(salt32 ‖ utf8(kind ‖ ":" ‖ contact))
kind = "email" | "phone"
```

The SaaS hashes `contact` byte for byte as received and never re-normalizes it before hashing
(`parseVoterPhone` re-parses with the org's country and drops Italian leading zeros,
`internal/utils.go:72-86`). It refuses (400 40037) a contact that is not already canonical:

- **phone:** starts with `+`, and `phonenumbers.Parse(v, "ZZ")` → `IsValidNumber` →
  `Format(E164) == v`. `org.Country` is not used. The SMS goes to `v`.
- **email:** `ValidEmail(v) && v == norm.NFC(strings.ToLower(strings.TrimSpace(v)))`.

The salt gives the hash 256 bits of entropy, so a phone number cannot be brute-forced from a
published hash. Salt and contact travel only in request bodies (the request logger logs method and
URI only, `api/api.go:415`). The body decode error is a fixed message, as in `authFirstStep`
(`csp/handlers/auth.go:277-279`), because `HexBytes.UnmarshalJSON` can echo input
(`internal/hexbytes.go:96`). No error or log line interpolates `contact`, `salt` or `contactHash`.

## 4. SaaS changes

### 4.1 Census and process creation

The attestation mode is a census field, not a census type: `Census.Type` stays derived from
`twoFaFields` (`db/census.go:68`).

- API: `CensusSpec.attested: { signer: "0x…", maxCensusSize: N }` inside the existing `census` block
  of process creation (`api/apicommon/voting_processes.go:16-41`).
- DB: `db.Census.Attested *AttestedCensus { Signer common.Address; SignerHistory []{Signer, From} }`.
  A missing field means a memberbase census, so existing data needs no migration.
- `attested` excludes `groupId`, `memberIds`, `authFields`, `weighted`, `anonymous` and any question
  `eligibility`; violations answer 400 40037 at create, update and publish. Allowed `twoFaFields`:
  `[]`, `["email"]`, `["phone"]` (not `sms_or_mail`).
- Signer rotation: `PUT /processes/{id}/attestation-signer {signer}` appends to `SignerHistory`;
  afterwards `auth/0` and every sign accept only the active signer (§4.3, §8).

### 4.2 Census size

An attested census has no members, so today publish fails with "cannot determine census size"
(`api/processes_census.go:96-104`, `api/processes_publish.go:72-75`). The declared `maxCensusSize`
is stored in `Census.Size`, so pricing, `ComputeMaxCensusSize`, publish and the growth checks work
unchanged. Growth uses a new `PUT /processes/{id}/census/size {size}`, attested censuses only, which
only grows the size, runs `censusGrowthPaymentError` (402 40178 / 40175 / 40183 as today), then
`enqueueSetProcessCensus`, and answers 200, or 202 with `jobId`. Pricing needs no new rule. Decidim
declares `ceil(roster × 1.20)`.

### 4.3 Auth and sign

The handlers branch on `census.Attested`, not on the body shape.

- **`POST /processes/{id}/auth/0`** decodes
  `{ attestation: {processId, voterId, contactHash, expiresAt}, signature, contact?, salt? }`.
  Byte fields are `internal.HexBytes`; `expiresAt` is a JSON number. `contact` and `salt` are
  required exactly when `twoFaFields` is non-empty and refused otherwise; a process with 2FA refuses
  a zero `contactHash`. Checks: process published and not ended (`processEnded`), signature and
  active signer (§3), `processId`, `expiresAt`, contact canonical and matching `contactHash`. Then:
  - `CSPAuth.UserID = uid = sha256("attested" ‖ processId ‖ voterId)`, so attested ids never share
    a key space with member ObjectIDs; `CSPAuth` also gains `ContactHash`, `ContactKind` and
    `Signer` (`omitempty`).
  - With 2FA: send the OTP to `contact`, answer `AuthResponse{authToken}` as today; `auth/1` is
    unchanged.
  - Without 2FA: reuse the voter's unexpired auth-only token, or create one, capped per `uid` per
    day (today's `createAuthOnlyToken` has no cap, `csp/auth.go:37-39`).
- **`auth/resend`** takes `{authToken, contact, salt}` and checks them against `token.ContactHash`
  before claiming a resend.
- **Budgets:** attested step 0 spends no failed-attempt budget (a bad signature reveals nothing).
  The cooldown and daily send cap key on `uid`; per-token OTP attempts and resends are unchanged.
- **`sign` / `sign-batch`:** `resolveSignContext` skips the participant and member lookups for an
  attested census (`csp/handlers/processes.go:348-372`, `:1124`), requires `token.Signer` to be the
  active signer, and uses weight 1. Each question keeps today's address pin, the `CSPProcess` row
  keyed `sha256(uid ‖ upstreamElectionID)` with its 10-signature budget (`db/csp.go:286-330`,
  `:461-463`).
- **`check`** answers `belongsToProcess = true` for a verified attested token and ignores
  eligibility; **`weight`** returns 1.
- Optionally, refuse new pins past `Census.Size`, since the chain would reject those votes anyway.

### 4.4 Errors

CSP block (40801 and 40802 are taken):

| Code | Error | Status |
|---|---|---|
| 40803 | `ErrAttestationInvalid`: signature, signer, domain or `processId` | 401 |
| 40804 | `ErrAttestationExpired` | 401 |
| 40806 | `ErrContactHashMismatch` (auth/0 and resend) | 401 |
| 40808 | `ErrAttestationSignerRevoked`: token minted under a rotated signer | 401 |

Invalid combinations of census fields use 400 40037.

### 4.5 State and admin views

- No new pin collection: the per-question `CSPProcess` pin keyed by `uid` is enough (D4).
- Per signed `voterId`, the SaaS keeps the attestation record `{processId, voterId, contactHash,
  expiresAt, signature, signer}` and the questions it signed for. No contact, no salt, no address.
- `GET /processes/{id}/participants` joins members, so for an attested census it is served from the
  attestation records instead.
- New API-key allowlist entries (`api/apikeys.go`, `ScopeVotingWrite`): the size and signer routes.

### 4.6 Anonymous censuses

Refused in v1: `attested` with `anonymous: true` answers 400 40037 at create and update, and
`auth/0`, blind-point and blind-sign refuse such a census next to `anonymousCensusRefused`
(`csp/handlers/auth.go:290`). Since the attestation carries no address, lifting this later needs no
format change.

## 5. Flow

1. The integrator creates the process with `census.attested {signer, maxCensusSize}` and
   `twoFaFields`.
2. The voter authenticates with the integrator. The integrator picks the contact it holds for them,
   draws a salt, and returns `{attestation, signature, contact, salt}` to the voter's browser.
3. The browser generates the ephemeral key as today, calls `auth/0` with the attestation, completes
   `auth/1` if there is 2FA, and calls `sign-batch` with its address.
4. The browser votes on chain as today.

## 6. Integrator side (Decidim)

- `voterId = HMAC(secret, "user:<id>" | "row:<voter id>" ‖ processId)`; the secret stays in Decidim.
- Signs with secp256k1 low-s; the primitives exist in pure Ruby on branch `abandoned/decidim-as-csp`
  (decidim-secure_elections). Keeps a count of the attestations it issued per process.
- Contact: the user's email for `internal_users`. A CSV census without contacts is allowed only with
  `twoFaFields: []`, decided per process, not per row.
- After the end, shows each voter whether the SaaS reports them as having obtained a signature, per
  question (D6).

## 7. Audit

Once `processEnded` holds and every question is ENDED or RESULTS, the SaaS serves a public, paginated
`GET /processes/{id}/attestations` (no API key, immutable from then on): the domain, the signer
history, and per signed `voterId` the attestation record and the questions it was signed for.

Checks:
- every record carries a valid signature by the signer active at its time;
- for each question, `votes on chain ≤ voterIds signed for it ≤ maxCensusSize`;
- the integrator confirms every `voterId` is one it issued, and that the totals agree with its own
  count;
- each voter checks their own entry through the integrator: someone listed as signed who never voted
  exposes a signature the SaaS issued on their behalf.

## 8. Threats

| Threat | Result |
|---|---|
| Integrator attests people outside the census, or one person under several `voterId`s | Not prevented: the integrator owns the census, as when it uploads members today. |
| SaaS signs addresses with no attestation | Votes exceed signed `voterId`s on some question: detected by §7. |
| SaaS reuses the attestation of a voter who started but never voted | Detected by the voter's own check (§6, §7). |
| Integrator signing key leaked | The attacker writes attestations with contacts they control, so the 2FA does not help; up to `maxCensusSize` votes per question of every process trusting that signer. Mitigation: per-process signer; rotation revokes tokens minted under the old signer (`resolveSignContext` checks `token.Signer`). §7 cannot tell these votes from real ones. |
| Attestation stolen in transit | With 2FA, useless without the contact's inbox or phone. Without 2FA it is a bearer credential: the thief can vote unless the voter already did (the per-question pin). The integrator delivers it only inside the voter's authenticated session, with a short `expiresAt`. |
| Replay to another process or environment | `processId` and the domain's `verifyingContract` are signed. |
| Contact recovered from published data | Needs the salt, which is never stored, logged or published. |
| Linkability | The integrator does not learn addresses (D4). The published list tells the integrator **who** obtained signatures, not how they voted. The SaaS still sees `uid → address`, as it sees member → address today. |
