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
| `POST` | `/process/{draftId}/publish` | Bearer (Manager/Admin) | optional `{ startDate }` (overrides the draft's start) | Publish a draft to the chain: forge → fund → sign → submit → confirm `NewProcess`. **Async:** validates synchronously, returns `202` + `jobId`; poll `GET /jobs/{jobId}` for the `processId`. |
| `PUT`  | `/process/{processId}/status` | Bearer (Manager/Admin) | `{ status }` (`ready`/`paused`/`ended`/`canceled`) | Change lifecycle status. **Async:** returns `202` + `jobId`. |
| `PUT`  | `/process/{processId}/census` | Bearer (Manager/Admin) | `{ participants }` (`{key, weight}[]`) | **Non-CSP only.** Dynamic-census add: append participants and grow the election census size. Gated behind the `DynamicCensus` mode flag. **Async:** returns `202` + `jobId`; poll for the new census size. |
| `POST` | `/process/{processId}/vote` | Public | `{ txPayload }` (hex of the signed `Vote` envelope) | Relay an already-signed vote envelope to the chain — a **CSP- or proof-authorized** envelope. **Async:** returns `202` + `jobId`; poll for the `voteID` receipt. |
| `GET`  | `/process/{processId}/census/proof` | Public | `?key=<addr>` | **Non-CSP only.** Proxy a Merkle census proof for the voter key, so the voter can build + sign the envelope. |
| `GET`  | `/jobs/{jobId}` | Public (capability) | — (none) | Poll an async tx job (publish/status/census/vote). Always `200`; `status` is `pending`/`completed`/`failed`, with the on-chain `result` when completed. |
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
| census (from `censusId`)            | `Process.CensusOrigin`, `CensusRoot`, `CensusURI` — depend on the census type (see table below) |

### Census types → on-chain origin

The census `type` selects the on-chain `CensusOrigin`, the root, and what a participant record
carries. The SaaS supports three (there is no plain "tree" type — all censuses are weighted now):

| Census type   | Anonymous | `CensusOrigin`            | Root / participants |
|---------------|-----------|--------------------------|---------------------|
| `csp`         | no        | `OFF_CHAIN_CA`           | CSP public key is the root — no Merkle tree to publish; participant = email/phone identity |
| `weighted`    | no        | `OFF_CHAIN_TREE_WEIGHTED`| Merkle root of `{key: address/pubkey, weight}` participants, published by the SaaS |
| `zkweighted`  | yes (sets `electionType.anonymous`) | `OFF_CHAIN_TREE_WEIGHTED` | same as `weighted`, but anonymous (Poseidon) |

- **CSP** (`auth`/`mail`/`sms`/`sms_or_mail`) is the existing path: the CSP public key is the
  census root, so there is no tree to publish, and the voter is authorized by a CSP blind signature.
- **`weighted` / `zkweighted`** are the **non-CSP (Merkle)** path added in Phase 6: at publish the
  SaaS builds the on-chain census from the participant list and uses its **published root + URI**;
  the voter is authorized by a **Merkle census proof** (fetched via §4a) instead of a CSP signature.
  `zkweighted` additionally sets `EnvelopeType.Anonymous`. The vote relay (§4) accepts either
  authorization and still never decodes the ballot.

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
- **202 Accepted:** the draft is validated, built, funded and signed **synchronously** (so bad,
  unauthorized or over-quota requests still fail immediately); the on-chain submit/confirm is queued
  on a bounded worker pool. The response carries the job id and sets `Location: /jobs/{jobId}`:
  ```json
  { "jobId": "<hex job id>" }
  ```
  Poll `GET /jobs/{jobId}` (see *Poll an async transaction job*) until `status` is `completed`; the
  result is `{ "address": "<64-hex on-chain election id>", "status": "READY" }`. `address` (the
  `processId`) is persisted to `db.Process.Address`. No transaction hash is exposed.
- **Idempotent:** if the draft is already published, returns **200** synchronously with
  `{ "address": "<processId>", "status": "..." }` (no new tx, no job).
- **Errors (synchronous):** `400` draft incomplete (no `electionParams`), `401` not a
  manager/admin, `403` plan/quota exceeded, `404` draft not found, `409` a publish is already in
  progress for this draft, `503` tx queue full. A submit/mine failure surfaces on the job as
  `status: failed` with an `error` message (the draft is reset and any quota reservation released).

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
- **202 Accepted:** built, funded and signed synchronously; submit/confirm is queued.
  ```json
  { "jobId": "<hex job id>" }
  ```
  (+ `Location: /jobs/{jobId}`.) Poll the job until `completed`; the result is
  `{ "status": "PAUSED" }` (the new status, set after the SaaS confirms the tx on chain).
- **Errors:** `400` invalid/illegal transition, `401`, `404` process unknown to SaaS, `503` tx
  queue full. On-chain failure surfaces on the job as `status: failed`.

---

## 3a. Dynamic census add (non-CSP)

Append participants to a published **non-CSP** (`weighted`/`zkweighted`) census and grow the
election's census size on chain. The SaaS calls `CensusAddParticipants` then
`SetElectionCensusSize`. Only valid for elections whose `DynamicCensus` mode flag is set; CSP
elections (where the root is the CSP pubkey, not a tree) do not support this.

```
PUT /process/{processId}/census
```

- **Auth:** Bearer. Manager or Admin of the owning org.
- **Path:** `processId` — on-chain election ID (hex).
- **Body:**
  ```json
  { "participants": [ { "key": "0x<addr/pubkey>", "weight": "1" } ] }
  ```
- **202 Accepted:** participants are validated synchronously; the `CensusAddParticipants` +
  `SetElectionCensusSize` submit is queued.
  ```json
  { "jobId": "<hex job id>" }
  ```
  (+ `Location: /jobs/{jobId}`.) Poll the job until `completed`; the result is
  `{ "censusSize": 5050 }` (the new on-chain census size, set after the SaaS confirms the tx).
- **Errors:** `400` election is not dynamic / CSP election / empty participants, `401`, `404`
  process unknown, `503` tx queue full. On-chain failure surfaces on the job as `status: failed`.

---

## 4. Relay a vote

Submit an already-signed vote envelope to the Vochain. The SaaS **verifies it is a Vote tx
for `processId` and forwards it**; it never decrypts or inspects the ballot (ballot secrecy
preserved at the operator).

```
POST /process/{processId}/vote
```

- **Auth:** Public. The voter is authorized by the proof embedded inside the envelope — either a
  **CSP blind signature** (CSP censuses, obtained via the existing `/process/bundle/{id}/auth|sign`
  flow) **or** a **Merkle census proof** (non-CSP `weighted`/`zkweighted` censuses, obtained via
  §4a). The relay accepts both and forwards either unchanged; it still never decodes the ballot.
- **Path:** `processId` — on-chain election ID (hex).
- **Body:**
  ```json
  { "txPayload": "<hex of protobuf SignedTx wrapping a Vote>" }
  ```
  (`txPayload` matches the field name used by `apicommon.TransactionData`.)
- **202 Accepted:** the envelope is decoded and validated synchronously; the submit is queued.
  ```json
  { "jobId": "<hex job id>" }
  ```
  (+ `Location: /jobs/{jobId}`.) Poll the job until `completed`; the result is
  `{ "voteID": "<hex nullifier>" }` — the receipt the voter can use to later verify their vote
  was counted.
- **Errors:** `400` payload is not a Vote tx or targets a different process, `404` process
  not found, `503` tx queue full. A chain rejection (e.g. already voted / nullifier used) surfaces
  on the job as `status: failed`.

---

## Poll an async transaction job

The four endpoints above (publish, status, census add, vote) submit on a bounded background worker
pool to keep on-chain confirmation (up to ~40s under congestion) off the request path, where it would
share the router's throttle/timeout budget with the public voter endpoints. Each returns a `jobId`;
this single endpoint reports the outcome.

```
GET /jobs/{jobId}
```

- **Auth:** Public. The 32-byte `jobId` **is** the capability — it is unguessable and the result
  contains only public on-chain data (process id, vote nullifier, status), so no token is needed
  (the relay-vote flow is itself public).
- **Path:** `jobId` — the hex id returned by a `202`.
- **200 OK** (always, while the job exists):
  ```json
  {
    "jobId":  "<hex>",
    "type":   "publish_process" | "set_process_status" | "add_census_participants" | "relay_vote",
    "status": "pending" | "completed" | "failed",
    "result": { "address": "<hex>", "voteID": "<hex>", "status": "READY", "censusSize": 5050 },
    "error":  "<reason, when failed>"
  }
  ```
  `result` is present only when `status` is `completed`, and carries just the fields relevant to the
  job type (`address`+`status` for publish, `status` for a status change, `censusSize` for a census
  add, `voteID` for a vote). `error` is present only when `status` is `failed`.
- **Polling:** clients poll until `status` leaves `pending`. There is no push/webhook.
- **Errors:** `400` malformed id, `404` unknown job.

---

## 4a. Census proof for a voter (non-CSP)

For **non-CSP** (`weighted`/`zkweighted`) elections the voter is authorized by a **Merkle census
proof** rather than a CSP blind signature. This endpoint proxies the chain's `CensusGenProof` for
the given voter key so the voter can assemble + sign the vote envelope client-side; the backend
never holds the voter key. CSP elections don't use it (their authorization is the CSP signature).

```
GET /process/{processId}/census/proof?key=<addr>
```

- **Auth:** Public.
- **Path:** `processId` — on-chain election ID (hex).
- **Query:** `key` — the voter's address/public key (hex), as registered in the census.
- **200 OK:** the census proof for the key, e.g.:
  ```json
  {
    "root":      "<hex census root>",
    "proof":     "<hex merkle proof>",
    "leafValue": "<hex>",
    "siblings":  "<hex>",
    "weight":    "1"
  }
  ```
  The voter feeds this into the envelope (in place of the CSP `ProofCA`), signs it locally, then
  posts it to §4.
- **Errors:** `400` CSP election (no Merkle proof) / missing `key`, `404` key not in census /
  process unknown, `500` `ErrVochainRequestFailed`.

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

**CSP census:**
1. `POST /process/bundle/{bundleId}/auth/0` … `/auth/{step}` — CSP challenge (existing).
2. `POST /process/bundle/{bundleId}/sign` — CSP blind signature (existing).
3. The client builds + signs the `Vote` envelope locally using the CSP signature (the only
   client-side crypto).
4. `POST /process/{processId}/vote` — **new** relay endpoint queues it; returns `202` + `jobId`.
5. `GET /jobs/{jobId}` — poll until `completed`; the result's `voteID` is the vote receipt.
6. `GET /process/{processId}/results` — **new** read endpoint for live tally.

**Non-CSP (`weighted`/`zkweighted`) census:**
1. `GET /process/{processId}/census/proof?key=<addr>` — **new** fetch the Merkle census proof for
   the voter key (§4a).
2. The client builds + signs the `Vote` envelope locally, embedding the Merkle proof in place of
   the CSP `ProofCA` (the only client-side crypto).
3. `POST /process/{processId}/vote` — same relay endpoint; it accepts the proof-authorized envelope,
   queues it and returns `202` + `jobId`.
4. `GET /jobs/{jobId}` — poll until `completed`; the result's `voteID` is the vote receipt.
5. `GET /process/{processId}/results` — live tally.

## New apicommon types (summary)

| Type | Used by |
|------|---------|
| `ElectionParams` (+ `Question`, `Choice`, `VoteType`, `ElectionType`) | create/update draft |
| `PublishProcessRequest` / `PublishProcessResponse` (idempotent 200 only) / `EnqueuedResponse` (202) | `POST /process/{id}/publish` |
| `SetProcessStatusRequest` / `EnqueuedResponse` (202) | `PUT /process/{id}/status` |
| `AddCensusParticipantsRequest` / `EnqueuedResponse` (202) | `PUT /process/{id}/census` (non-CSP dynamic add) |
| `RelayVoteRequest` / `EnqueuedResponse` (202) | `POST /process/{id}/vote` (CSP- or proof-authorized) |
| `CensusProofResponse` | `GET /process/{id}/census/proof` (non-CSP) |
| `EnqueuedResponse` (`jobId`) / `JobStatusResponse` (`jobId`, `type`, `status`, `result`, `error`) | `GET /jobs/{jobId}` |
| `ProcessResultsResponse` | `GET /process/{id}/results` |
| `CreateManagedOrganizationRequest` | `POST /organizations/{address}/managed` |
| `IntegratorInfoResponse` (`enabled`, `limits`, `usage`) | `GET /organizations/{address}/integrator` |
