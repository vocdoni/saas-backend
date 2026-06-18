# NEW_API.md — Managed (chain-abstracted) endpoints

New endpoints layered on top of the existing SaaS API so an integrator can run the
**full election lifecycle and voting without the Vocdoni SDK and without touching the
Vochain**. The SaaS *forges + funds + signs + submits + confirms* every management
transaction, and *relays* (never decodes) client-signed votes.

The model stays **organization-based**, exactly like the vocdoni.app UI today: a user registers,
creates an organization (which now gets its on-chain account server-side), and the organization's
admins/managers create elections under it. On top of that, an **integrator** organization can
create and manage organizations for its own clients through the API, under an accountable quota.

## Summary

A handful of new endpoints turn the SaaS into a complete, chain-abstracted election API. The
organizer creates an organization (its on-chain account is forged automatically), creates a draft
(now carrying the on-chain election parameters), publishes it, and drives its lifecycle; the voter
casts and verifies; everyone reads results and metadata — all without ever seeing a transaction.
Integrators additionally create and manage client organizations under a quota.

| Method | Endpoint | Auth | Body (JSON) | Purpose |
|--------|----------|------|-------------|---------|
| `POST` | `/organizations` (+ optional `provisionAccount`) | Bearer | existing org fields, optional `provisionAccount` | Create an org. With `provisionAccount: true` the SaaS **also forges the org's on-chain `CreateAccount`** (idempotent). Omitted/false → today's behavior (DB only). |
| `POST` | `/process` (+ `electionParams`) | Bearer | `orgAddress`, `censusId`, `metadata`, optional `electionParams` | Create a draft. **Additive:** gains an optional `electionParams` field carrying all on-chain election inputs. |
| `PUT`  | `/process/{draftId}` (+ `electionParams`) | Bearer | `censusId`, `metadata`, optional `electionParams` | Update a draft. Same optional `electionParams` field. |
| `POST` | `/process/{draftId}/publish` | Bearer (Manager/Admin) | optional `{ startDate }` (overrides the draft's start) | Publish a draft to the chain: forge → fund → sign → submit → confirm `NewProcess`. Returns the `processId`. |
| `PUT`  | `/process/{processId}/status` | Bearer (Manager/Admin) | `{ status }` (`ready`/`paused`/`ended`/`canceled`) | Change lifecycle status. |
| `POST` | `/process/{processId}/vote` | Public | `{ txPayload }` (base64 of the signed `Vote` envelope) | Relay an already-signed vote envelope to the chain. Returns a `voteId` receipt. |
| `GET`  | `/process/{processId}/results` | Public | — (none) | Read live status, vote count and tally. |
| `GET`  | `/process/{processId}/metadata` | Public | — (none) | Read the public election metadata (title, questions, choices). |
| `POST` | `/organizations/{address}/managed` | Bearer (Admin of integrator) | client org info + optional `ownerEmail` | **Integrator only.** Create a first-class client org managed by `{address}` (forges its account). Quota-enforced. |
| `GET`  | `/organizations/{address}/managed` | Bearer (Admin of integrator) | — (none) | **Integrator only.** Paginated list of orgs managed by `{address}`. |
| `GET`  | `/organizations/{address}/integrator` | Bearer (Admin of integrator) | — (none) | **Integrator only.** Read the integrator's effective quota + current usage. |

Everything below details each of these. Existing routes are unchanged (see Back-compatibility).

## Back-compatibility

Everything here is **additive** and **backwards compatible**. No existing route changes behavior:

- `POST /transactions` and `/transactions/message` stay exactly as they are (the old
  SDK `RemoteSigner` keeps working). The new endpoints are an alternative, higher-level path.
- `POST /process`, `PUT /process/{id}`, `GET /process/{id}` keep their current behavior;
  the request bodies only gain **optional** fields.
- `POST /organizations` keeps its current behavior **by default**. It gains one **optional** body
  field, `provisionAccount` (default false). Only when `true` does the SaaS forge the on-chain
  account (see §0); when omitted, the endpoint is byte-for-byte unchanged (DB row only; the account
  is still created by the legacy SDK `createAccount` via `/transactions`). So existing clients are
  untouched, and the new SDK opts in. The idempotent on-chain check also makes a stray legacy
  `createAccount` after a provisioned org a safe no-op rather than an error.
- New `db.Process` / `db.Organization` / `db.Plan` fields are optional and default to zero — no
  migration required.

## Conventions

- Base path: `http://localhost:8080` (default). Bearer JWT for protected routes.
- Hex strings for chain IDs/addresses; errors use the `errors.Error` shape
  `{ "code": int, "error": string, "data": any }`.
- **Resource-oriented, no blockchain surface.** This is a standard SaaS API: responses carry
  resource ids (organization address, election id, vote receipt id) — never transaction
  payloads, tx hashes, gas, faucet credits, or signers. The backend holds the organization
  private keys and abstracts all transaction complexity.
- **Two kinds of process identifier**, do not mix them:
  - `draftId` — Mongo ObjectID (24-hex) of the DB draft (`db.Process.ID`). Organizer-facing CRUD.
  - `processId` — on-chain election ID (64-hex), set after publishing (`db.Process.Address`).
    Voting and chain reads use this.
  - In the URL the path segment is always literally `{processId}` (the existing route convention),
    even where this doc writes `{draftId}` for clarity — `publish` and the existing
    `GET /process/{processId}` resolve it as a draft ObjectID; `status`/`vote`/`results`/`metadata`
    resolve it as the on-chain id.

---

## 0. Chain-abstracted organization creation (opt-in, eager)

Before an org can own an on-chain election it needs an on-chain **account**. The SaaS can forge this
eagerly, the moment the organization is created — matching the current UX where, right after
creating an organization, its admins can immediately create elections. This is **opt-in** so the
existing flow is untouched.

```
POST /organizations          // body gains optional: { "provisionAccount": true }
```

- **Default (omitted/false):** unchanged behavior — the SaaS stores the `db.Organization` and
  returns; the on-chain account is created by the legacy SDK `createAccount` via `/transactions`,
  exactly as today (same latency, same failure modes).
- **`provisionAccount: true`:** after storing the org, the SaaS builds `CreateAccount`, funds it via
  the faucet, signs it with the org key, submits and confirms it on chain — all internally. The
  response shape is unchanged (the org resource).
- **Idempotent:** if the on-chain account already exists, the forge is a no-op. Safe to retry; safe
  if a legacy client later calls `createAccount` against the same org.
- **No organizer-facing account/faucet/tx surface.** There is no account-creation endpoint; for the
  new SDK / integrator, creating an organization is a single REST call.
- **Errors:** if the forge fails, the org row already exists, so a retry (or the next management
  call) completes account creation idempotently.

The managed-org endpoint (§8a) always provisions the account. This is the eager counterpart to (and
replaces) any "lazy bootstrap at first publish" idea: when requested, account creation happens at
org-creation time, not deferred. See §7.

---

## 1. Election parameters on a draft (additive)

To forge `NewProcess` server-side, the draft must carry the on-chain election parameters
that the SDK used to build client-side. `CreateProcessRequest` and `UpdateProcessRequest`
gain one **optional** field, `electionParams`:

```jsonc
// ElectionParams (apicommon.ElectionParams) — all on-chain NewProcess inputs
{
  "title":       "Board election 2026",          // string | {"default": "...","ca": "..."}
  "description": "Annual board election",         // optional, multilang
  "header":      "https://.../banner.png",        // optional
  "startDate":   "2026-07-01T09:00:00Z",          // optional RFC3339; omitted/null = start ASAP
  "endDate":     "2026-07-08T09:00:00Z",          // required (or "duration" seconds)
  "duration":    604800,                           // optional, seconds; alternative to endDate
  "maxCensusSize": 5000,                           // optional; defaults to census size
  "questions": [
    {
      "title":       "Choose a candidate",
      "description": "",
      "choices": [
        { "title": "Alice", "value": 0 },
        { "title": "Bob",   "value": 1 }
      ]
    }
  ],
  "voteType": {                                    // -> models.Process.VoteOptions
    "maxCount":          1,
    "maxValue":          1,
    "maxVoteOverwrites": 0,
    "costExponent":      10000,
    "uniqueChoices":     false,
    "costFromWeight":    false
  },
  "electionType": {                               // -> models.Process.EnvelopeType + flags
    "interruptible":      true,
    "secretUntilTheEnd":  false,
    "anonymous":          false,
    "metadata": { "encrypted": false }
  }
}
```

Mapping to the on-chain `models.Process` (built server-side):

| ElectionParams                       | models.Process / Tx field                            |
|--------------------------------------|------------------------------------------------------|
| `title/description/questions/...`    | `ElectionMetadata` JSON stored by the SaaS, served at a public `https://` URL → `Process.Metadata` (the URL, **not** an `ipfs://` CID) |
| `startDate` / `duration`/`endDate`   | `Process.StartTime` / `Process.Duration`             |
| `maxCensusSize`                      | `Process.MaxCensusSize`                              |
| `voteType.*`                         | `Process.VoteOptions` (maxCount/maxValue/costExponent/maxVoteOverwrites) |
| `electionType.anonymous/encrypted`  | `Process.EnvelopeType` (Anonymous/EncryptedVotes)    |
| `electionType.interruptible`        | `Process.Mode.Interruptible`                         |
| census (from `censusId`)            | `Process.CensusOrigin = OFF_CHAIN_CA` (CSP), `CensusRoot = CSP pubkey`, `CensusURI = SaaS CSP endpoint` |

Census origin is always **CSP** for SaaS censuses (`auth`/`mail`/`sms`/`sms_or_mail`), so
there is no Merkle tree to publish — the CSP public key is the census root.

---

## 2. Publish a draft to the chain

Forge → fund → sign → submit → confirm a `NewProcess`. The org's on-chain account already exists by
this point (provisioned at org creation via §0, or created through the legacy `createAccount` path),
so publish never touches account bootstrap.

```
POST /process/{draftId}/publish
```

- **Auth:** Bearer. Caller must be **Manager or Admin** of the draft's org.
- **Plan:** enforces `subscriptions.HasTxPermission(NEW_PROCESS, org, user)` and bumps the
  org process counter (same rule as today's `/transactions` path — including the existing nuance
  that test-sized elections, `maxCensusSize ≤ TestMaxCensusSize`, are not counted).
- **Path:** `draftId` — the draft ObjectID.
- **Body (optional):**
  ```json
  { "startDate": "2026-07-01T09:00:00Z" }
  ```
  Overrides the draft's start date if provided.
- **200 OK:**
  ```json
  { "processId": "<64-hex on-chain election id>" }
  ```
  `processId` is also persisted to `db.Process.Address`. No transaction hash is exposed — the
  SaaS confirms the tx on chain before returning.
- **Idempotent:** if the draft is already published, returns the existing `processId` with 200
  (no new tx).
- **Errors:** `400` draft incomplete (no `electionParams` or no census), `401` not a
  manager/admin, `403` plan/quota exceeded, `404` draft not found, `500`
  `ErrVochainRequestFailed` (submit/mine failed).

---

## 3. Change process status (lifecycle)

Forge `SetProcessStatus` (pause / resume / end / cancel).

```
PUT /process/{processId}/status
```

- **Auth:** Bearer. Manager or Admin of the owning org.
- **Path:** `processId` — on-chain election ID (hex).
- **Body:**
  ```json
  { "status": "ready" | "paused" | "ended" | "canceled" }
  ```
- **200 OK:** `{ "status": "paused" }` (the new status, echoed back after the SaaS confirms the
  tx on chain).
- **Errors:** `400` invalid/illegal transition, `401`, `404` process unknown to SaaS, `500`.

---

## 4. Relay a vote

Submit an already-signed vote envelope to the Vochain. The SaaS **verifies it is a Vote tx
for `processId` and forwards it**; it never decrypts or inspects the ballot (ballot secrecy
preserved at the operator).

```
POST /process/{processId}/vote
```

- **Auth:** Public. The voter is authorized by the CSP proof embedded inside the envelope
  (obtained via the existing `/process/bundle/{id}/auth|sign` flow).
- **Path:** `processId` — on-chain election ID (hex).
- **Body:**
  ```json
  { "txPayload": "<base64 of protobuf SignedTx wrapping a Vote>" }
  ```
  (`txPayload` matches the field name used by `apicommon.TransactionData`.)
- **200 OK:**
  ```json
  { "voteId": "<hex nullifier / vote id>" }
  ```
  `voteId` is the vote receipt the voter can use to later verify their vote was counted.
- **Errors:** `400` payload is not a Vote tx or targets a different process, `404` process
  not found, `409` already voted / nullifier used (as reported by chain), `500`.

---

## 5. Read process results / status (proxy)

So integrators never query the Vochain directly.

```
GET /process/{processId}/results
```

- **Auth:** Public.
- **Path:** `processId` — on-chain election ID (hex).
- **200 OK:** subset of the on-chain election, e.g.:
  ```json
  {
    "status": "READY",
    "voteCount": 123,
    "startDate": "2026-07-01T09:00:00Z",
    "endDate": "2026-07-08T09:00:00Z",
    "results": [ ["10", "23"], ["5", "28"] ],
    "finalResults": false
  }
  ```
- **Errors:** `404` process not found on chain, `500` `ErrVochainRequestFailed`.

> The existing `GET /process/{draftId}` (by ObjectID) is unchanged and still returns the
> stored `db.Process` (draft + census + metadata). Use `/results` for live chain state.

---

## 6. Public election metadata (read)

The on-chain `Process.Metadata` is a public `https://` URL served by the SaaS (not IPFS). The
SDK and any client can fetch the human-readable election metadata (title, description,
questions, choices) here:

```
GET /process/{processId}/metadata
```

- **Auth:** Public.
- **Path:** `processId` — on-chain election ID (hex).
- **200 OK:** the `ElectionMetadata` JSON (same shape derived from the draft's `electionParams`).
- **Errors:** `404` unknown process.

This is the exact URL embedded as `Process.Metadata` at publish time, so resolving the on-chain
metadata pointer and calling this endpoint return the same document.

---

## 7. Account bootstrap is internal (no endpoint)

There is **no organizer-facing account-creation step** in the chain-abstracted path. When
`provisionAccount` is set (§0) — or always, for managed orgs (§8a) — the org's on-chain account is
forged at organization-creation time. The backend holds the org private key, builds `CreateAccount`,
funds it, signs, submits and confirms — all invisibly. From the new SDK's / integrator's point of
view, creating an organization and publishing an election are plain REST calls; there is no account,
faucet, or transaction concept to manage. (Legacy clients that don't set the flag keep using the
SDK `createAccount` via `/transactions`, unchanged.)

---

## 8. Integrator: managed organizations + quota

An **integrator** is an ordinary organization granted the integrator capability, so it can create
and manage organizations for its own clients via the API, under an accountable quota.

- **Enablement:** an org is an integrator if it was manually flagged (`IsIntegrator`) **or** its
  plan carries the integrator feature. Enabling and quota setup are **internal/admin operations**
  (a `cmd/cli` command we run) — there is no public write endpoint to grant integrator status or
  set limits.
- **Managed organizations are first-class:** each has its own address, its own on-chain account
  (forged at creation, §0), and its own invitable members. They are linked to the integrator by a
  `managedBy` field — they are **not** sub-orgs, so a client can still use the normal parent/sub-org
  feature itself.
- **Accounting:** aggregate usage (managed orgs, total elections, total census across all managed
  orgs) is tracked on the integrator org and checked against its effective limits. Quota dimensions:
  managed orgs, elections per managed org, total elections, census per election, census per managed
  org, total census.

### 8a. Create a managed organization

```
POST /organizations/{address}/managed
```

- **Auth:** Bearer. Caller must be **Admin** of the integrator org `{address}`, and `{address}`
  must be integrator-enabled.
- **Path:** `address` — the integrator org address.
- **Body:** the client org info (same fields as a normal org), plus an optional `ownerEmail` to
  designate the client org's owner/admin (defaults to the integrator's calling user).
- **200 OK:** the created org resource (its `address`, etc.). Its on-chain account is already
  forged. `managedBy` is set to `{address}`. The per-user `MaxOrgsPerUser` cap is bypassed for
  managed orgs.
- **Quota:** enforces `CanCreateManagedOrg` (the integrator's `ManagedOrgs` counter `<`
  `MaxManagedOrgs`); bumps the counter on success.
- **Errors:** `401` not an admin, `403` `ErrNotAnIntegrator` / `ErrMaxManagedOrgsReached`, `500`
  forge failed.

### 8b. List managed organizations

```
GET /organizations/{address}/managed
```

- **Auth:** Bearer. Admin of the integrator org `{address}`.
- **200 OK:** a paginated list of the orgs managed by `{address}` (mirrors the drafts list shape).
- **Errors:** `401`, `403` `ErrNotAnIntegrator`.

### 8c. Integrator quota + usage

```
GET /organizations/{address}/integrator
```

- **Auth:** Bearer. Admin of the integrator org `{address}`.
- **200 OK:**
  ```jsonc
  {
    "enabled": true,
    "limits": {                       // effective limits: manual override if set, else plan
      "maxManagedOrgs":      100,
      "maxProcessesPerOrg":  50,
      "maxTotalProcesses":   1000,
      "maxCensusPerProcess": 10000,
      "maxCensusPerOrg":     50000,
      "maxTotalCensusSize":  500000
    },
    "usage": {                        // current counters rolled up to the integrator
      "managedOrgs":       12,
      "managedProcesses":  87,
      "managedCensusSize": 41230
    }
  }
  ```
  A `0` in any limit field means **unlimited** for that dimension.
- **Errors:** `401`, `403` `ErrNotAnIntegrator`.

> **Publish under a managed org** (`POST /process/{draftId}/publish`, §2) additionally enforces the
> integrator's per-org and aggregate caps and bumps the integrator's `managedProcesses` /
> `managedCensusSize` counters. The endpoint shape is unchanged; only quota accounting differs when
> the draft's org is managed.

---

## Voter flow end-to-end (with the new SDK)

1. `POST /process/bundle/{bundleId}/auth/0` … `/auth/{step}` — CSP challenge (existing).
2. `POST /process/bundle/{bundleId}/sign` — CSP blind signature (existing).
3. The client builds + signs the `Vote` envelope locally using the CSP signature (the only
   client-side crypto).
4. `POST /process/{processId}/vote` — **new** relay endpoint submits it to the chain.
5. `GET /process/{processId}/results` — **new** read endpoint for live tally.

## New apicommon types (summary)

| Type | Used by |
|------|---------|
| `ElectionParams` (+ `Question`, `Choice`, `VoteType`, `ElectionType`) | create/update draft |
| `PublishProcessRequest` / `PublishProcessResponse` | `POST /process/{id}/publish` |
| `SetProcessStatusRequest` | `PUT /process/{id}/status` |
| `RelayVoteRequest` / `RelayVoteResponse` | `POST /process/{id}/vote` |
| `ProcessResultsResponse` | `GET /process/{id}/results` |
| `CreateManagedOrganizationRequest` | `POST /organizations/{address}/managed` |
| `IntegratorInfoResponse` (`enabled`, `limits`, `usage`) | `GET /organizations/{address}/integrator` |
