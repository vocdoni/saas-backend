# BACKEND_PLAN.md — Managed chain-abstraction for the SaaS API

Goal: extend the SaaS backend so it forges + funds + signs + submits + confirms every
organizer transaction, and relays client-signed votes — so integrators (and the new
`vocdoni-app-sdk`) never call the Vochain or use `@vocdoni/sdk`.

The model stays **organization-based**, exactly like the vocdoni.app UI today: a user registers,
creates an organization (which now gets its on-chain account server-side), and the organization's
admins/managers create elections under it. On top of that we add an **integrator** capability: a
special organization that can create and manage organizations for its own clients via the API,
under an accountable quota.

This document is the build plan for:

- an optional `electionParams` field on draft create/update,
- **chain-abstracted organization creation** (the SaaS forges `CreateAccount` for the org),
- the managed election endpoints: `POST /process/{draftId}/publish`,
  `PUT /process/{processId}/status`, `POST /process/{processId}/vote`,
  `GET /process/{processId}/results`, `GET /process/{processId}/metadata`,
- the **integrator** layer: managed-organization creation/listing, plan + manual quota, and
  quota enforcement that rolls election/census usage up to the integrator.

## Non-negotiable: do not break the current API

- **Keep** `POST /transactions` and `/transactions/message` (`api/transaction.go`) exactly
  as-is. The old `RemoteSigner` path must keep working during and after the migration.
- **Keep** `POST /process`, `PUT /process/{id}`, `GET /process/{id}`,
  `GET /organizations/{address}/processes[/drafts]` behavior. Only add **optional** request
  fields and **optional** struct fields.
- `POST /organizations` stays **backwards compatible**. It gains **one optional request field**,
  `provisionAccount` (default `false`): when set, the SaaS forges the org's on-chain
  `CreateAccount` server-side (idempotent — see "Chain-abstracted organization creation").
  Omitted/false → byte-for-byte today's behavior (DB row only; the on-chain account is still
  created by the legacy SDK `createAccount` via `/transactions`, same latency, same failure modes).
  The new SDK sets `provisionAccount: true`; the managed-org endpoint always forges. Every existing
  client keeps working unchanged.
- All new `db.Organization` / `db.Process` / `db.Plan` fields are optional/zero-valued → **no
  migration needed** (Mongo is schemaless; existing docs unmarshal fine). No new indexes required.
- After any handler/type/route change: run `make swagger` (regenerate `docs/swagger.yaml`)
  and `make lint`. Tests (`make test`) require Docker (testcontainers Mongo + Voconed).

## What already exists and is reused (no rebuild)

From `account/` (the remote-signer core):
- `account.OrganizationSigner(secret, creatorEmail, nonce)` — restores the org's signer.
- `account.NewSigner(secret, creatorEmail)` — derives a fresh signer + nonce (used at org creation).
- `Account.FundTransaction(tx, addr)` (`account/tx_funder.go`) — already funds `CreateAccount`,
  `SetAccount`, `NewProcess`, `SetProcess`, `SetSIK`, `CollectFaucet` with a faucet package +
  election price.
- `Account.SignTransaction(tx, signer)` — signs.
- `Account.client *apiclient.HTTPclient` — the dvote client; already used to submit + confirm
  (`AccountBootstrap` + `WaitUntilTxIsMined`, see `account/account.go:88`). `ensureAccountExist`
  there is for the **SaaS master/faucet account**, not per-org accounts; model the org submit +
  `WaitUntilTxIsMined` on the same pattern.
- `Account.ElectionPriceCalc`, `Account.TxCosts` — pricing.
- `subscriptions.HasTxPermission(tx, txType, org, user)` + org process counter — the permission/
  quota logic in `api/transaction.go`, reused by the publish handler and extended for integrators.
- CSP signer (`csp/`) — provides the CSP public key used as the census root and the
  `/process/bundle/.../auth|sign` flow (unchanged).

The genuinely new work is: (a) **forging** `CreateAccount` server-side at org creation, (b)
**building** the protobuf election txs server-side from high-level input (today the client builds
them), (c) **submitting** the org-signed tx, (d) a **vote relay**, (e) **read proxies**, (f)
extending the draft model, and (g) the **integrator** layer (managed orgs + quota).

## Architecture

Thin handlers; reuse `account` for chain primitives; reuse/extend `subscriptions` for quota.

```
api/organizations.go (handlers)
      │  create org (+ forge CreateAccount) / create managed org / list managed / integrator usage
      ▼
api/process.go (handlers)
      │  publish / status / vote-relay / results / metadata
      ▼
account.Account  (new methods, alongside tx_funder.go / account.go)
   ├─ CreateOrgAccount(org)                       // idempotent CreateAccount + fund + sign + submit + wait
   ├─ BuildNewProcessTx(org, draft, metaURL)      // construct models.Tx_NewProcess (NewProcessParams carries CensusOrigin)
   ├─ BuildSetProcessStatusTx(processId, status)  // construct models.Tx_SetProcess
   ├─ SubmitSignedTx(stx) (data, error)           // apiclient submit + WaitUntilTxIsMined
   ├─ RelayVote(processId, signedTxBytes)         // verify Vote+process, submit, return voteID
   └─ ProcessResults(processId)                   // apiclient.Election(id) -> trimmed view

subscriptions.Subscriptions (new methods)
   ├─ IsIntegrator(org)                           // manual flag OR plan feature
   ├─ EffectiveIntegratorLimits(org)              // manual override if set, else plan limits
   ├─ CanCreateManagedOrg(integrator)             // ManagedOrgs counter < MaxManagedOrgs
   └─ CanPublishForManagedOrg(org, integrator, maxCensusSize)  // per-org + aggregate caps
```

Rationale: thin handlers + methods on `account`/`subscriptions` reusing the existing
client/funder/signer and quota engine. No new package (YAGNI); matches current structure and the
testcontainers/Voconed test harness.

## Chain-abstracted organization creation (opt-in, eager)

Before an org can own an on-chain election it must have an on-chain **account** (a one-time
`CreateAccount` tx). The **chosen model is org-based and eager** (not lazy-at-first-publish),
matching the current UX where, right after creating an organization, its admins can immediately
create elections — but it is **opt-in** so the existing UI/SDK flow is untouched.

- **Trigger:** the SaaS forges the org account when (a) `POST /organizations` is called with
  `provisionAccount: true`, or (b) the org is created via the managed-org endpoint (always). When
  `provisionAccount` is omitted/false, `createOrganizationHandler` behaves exactly as today: it
  derives the org signer + nonce, stores the `db.Organization`, and returns — the account is left
  to the legacy SDK `createAccount` path.
- **Forge (`account.CreateOrgAccount(dbOrg)`):** after `SetOrganization`, when triggered, it:
  1. checks whether the account already exists on chain (`apiclient.Account(addr)`); if so, returns
     nil (idempotent — safe to retry, and safe if a legacy client later calls `createAccount`),
  2. otherwise builds `CreateAccount` (with an account-metadata URL), funds it via the faucet,
     signs it with the org signer, submits, and `WaitUntilTxIsMined`.
- On forge failure the handler returns 500; the `db.Organization` already exists, so a retry (or
  the next management call) completes account creation idempotently.
- **No organizer-facing account/faucet/tx surface**: callers only ever see `POST /organizations`
  returning the org resource.

**Faucet cost / watch-out:** opt-in eager creation funds every org that requests it, including
dormant ones. For integrators it is bounded by the `MaxManagedOrgs` quota (below), so the blast
radius is capped per integrator. Legacy (flag-off) org creation funds nothing extra — unchanged.

## Integrator model

An **integrator** is an ordinary organization that has been granted the integrator capability and
can create/manage organizations for its clients through the API.

- **Enablement:** an org is an integrator if `org.IsIntegrator == true` (manual grant) **or** its
  plan has `Features.Integrator == true` (bought the integration plan). `subscriptions.IsIntegrator`
  encapsulates this OR.
- **Managed (client) organizations:** created via a dedicated endpoint by an admin of the
  integrator org. Each managed org is a **first-class organization** (own address, own on-chain
  account forged at creation, own members invitable later) and carries `ManagedBy = <integrator
  address>`. They are **not** sub-orgs (`Parent` is untouched), so a client can still use the
  normal parent/sub-org feature itself.
- **Accounting anchor:** aggregate usage (managed orgs, total elections, total census) is tracked
  as counters on the **integrator** org and checked against the integrator's effective limits.
- **Quota source:** `EffectiveIntegratorLimits(org)` = `org.IntegratorLimits` (manual override) if
  non-nil, else the plan's `IntegratorLimits`. Zero in any field means "unlimited" for that
  dimension.
- **Manual setup:** enabling an integrator and/or setting an override quota is an internal/admin
  operation done via a `cmd/cli` command (we run it); no public write endpoint. Integrators read
  their own quota + usage via `GET /organizations/{address}/integrator`.

## Data-model changes (additive)

`db/types.go`:

- Extend `Process` with optional election params + cached on-chain state:
  ```go
  type Process struct {
      ID             primitive.ObjectID
      Address        internal.HexBytes        // on-chain id (set on publish)
      OrgAddress     common.Address
      Census         Census
      Metadata       map[string]any
      ElectionParams *ElectionParams `json:"electionParams,omitempty" bson:"electionParams,omitempty"` // NEW
      Status         string          `json:"status,omitempty" bson:"status,omitempty"`                 // NEW (cached)
      PublishedAt    time.Time       `json:"publishedAt,omitempty" bson:"publishedAt,omitempty"`       // NEW
  }
  ```
- Add `ElectionParams` (+ `Question`, `Choice`, `VoteType`, `ElectionType`, `ElectionTypeMetadata`,
  and a `MultiLangString` helper): the high-level election inputs the publish handler maps into the
  on-chain `models.Process`.
- Add the non-CSP census types to `db.CensusType` — `CensusTypeWeighted="weighted"` and
  `CensusTypeZKWeighted="zkweighted"` — alongside the existing CSP types. For these, census
  participants carry `{key: address/pubkey, weight}` instead of email/phone (see Phase 6).
- Extend `Organization` (all optional/zero-valued):
  ```go
  ManagedBy        common.Address    `json:"managedBy,omitempty" bson:"managedBy,omitempty"`               // integrator that owns this client org
  IsIntegrator     bool              `json:"isIntegrator,omitempty" bson:"isIntegrator,omitempty"`         // manual enablement
  IntegratorLimits *IntegratorLimits `json:"integratorLimits,omitempty" bson:"integratorLimits,omitempty"` // manual quota override (nil = use plan)
  ```
- Extend `OrganizationCounters` with integrator aggregates:
  ```go
  ManagedOrgs       int   `json:"managedOrgs" bson:"managedOrgs"`             // client orgs created
  ManagedProcesses  int   `json:"managedProcesses" bson:"managedProcesses"`   // elections across all managed orgs
  ManagedCensusSize int64 `json:"managedCensusSize" bson:"managedCensusSize"` // census across all managed orgs
  ```
- Add `IntegratorLimits` and add it to `Plan` + an `Integrator` flag to `Features`:
  ```go
  type IntegratorLimits struct {
      MaxManagedOrgs      int   `json:"maxManagedOrgs" bson:"maxManagedOrgs"`           // number of client orgs
      MaxProcessesPerOrg  int   `json:"maxProcessesPerOrg" bson:"maxProcessesPerOrg"`   // elections per managed org
      MaxTotalProcesses   int   `json:"maxTotalProcesses" bson:"maxTotalProcesses"`     // elections across all managed orgs
      MaxCensusPerProcess int64 `json:"maxCensusPerProcess" bson:"maxCensusPerProcess"` // census size per election
      MaxCensusPerOrg     int64 `json:"maxCensusPerOrg" bson:"maxCensusPerOrg"`         // census size per managed org
      MaxTotalCensusSize  int64 `json:"maxTotalCensusSize" bson:"maxTotalCensusSize"`   // census across all managed orgs
  }
  // Plan gains:      IntegratorLimits IntegratorLimits
  // Features gains:   Integrator bool
  ```

`api/apicommon/types.go`:
- Add optional `ElectionParams *db.ElectionParams` to `CreateProcessRequest` and
  `UpdateProcessRequest`.
- Add optional `ProvisionAccount bool `json:"provisionAccount,omitempty"`` to `OrganizationInfo`
  (the `POST /organizations` body) — default false preserves today's behavior.
- Add `PublishProcessRequest/Response`, `SetProcessStatusRequest`, `RelayVoteRequest/Response`,
  `ProcessResultsResponse`.
- Add `CreateManagedOrganizationRequest` (client org info + optional `ownerEmail`),
  `IntegratorInfoResponse` (`enabled`, `limits`, `usage`).

`db/organizations.go` / `db/process.go`: `SetOrganization` / `SetProcess` already upsert the whole
struct, so the new fields persist with no extra code. Add counter helpers mirroring
`IncrementOrganizationSubOrgsCounter`: `Increment/DecrementOrganizationManagedOrgsCounter`, and
`AddOrganizationManagedProcesses/ManagedCensusSize` (delta updates).

## Metadata hosting — resolution of the URL circular dependency

The on-chain `Process.Metadata` must hold a public `https://` URL **before** the `NewProcess` tx is
submitted, but the `processId` is only known **after** submit. So the URL cannot contain the
`processId`. Resolution:

- Build the `ElectionMetadata` JSON from `electionParams`, then store it **content-addressed** in
  `objectstorage/` (key = hash of the JSON), yielding a stable, immutable
  `https://.../storage/{hash}.json` URL that is known **before** the tx. That URL becomes the
  on-chain `Process.Metadata`.
- `objectstorage/` is currently image-only (content-type sniffing). Add a small JSON path: store
  with explicit `ContentType: "application/json"` and allow `.json` in the download handler.
- `GET /process/{processId}/metadata` is a convenience proxy: look up `db.Process` by `Address`
  (= processId) and serve the same stored document (or 302 to the content-addressed URL). Resolving
  the on-chain pointer and calling this endpoint therefore return the same JSON.

## Routes & handlers

`api/routes.go` — add path constants:
```go
processPublishEndpoint       = "/process/{processId}/publish"   // POST  (processId = draftId here)
processStatusEndpoint        = "/process/{processId}/status"    // PUT
processVoteEndpoint          = "/process/{processId}/vote"      // POST  (public)
processResultsEndpoint       = "/process/{processId}/results"   // GET   (public)
processMetadataEndpoint      = "/process/{processId}/metadata"  // GET   (public)
processCensusProofEndpoint   = "/process/{processId}/census/proof"  // GET   (public; Phase 6)
processCensusEndpoint        = "/process/{processId}/census"        // PUT   (dynamic add; Phase 6)
managedOrganizationsEndpoint = "/organizations/{address}/managed"     // POST create, GET list (integrator)
integratorEndpoint           = "/organizations/{address}/integrator"  // GET quota + usage
```
`api/api.go` — register publish/status, managed-org create/list, integrator usage, and the dynamic
census add (`PUT .../census`, Phase 6) in the **protected** group (`jwtauth.Verifier` +
`a.authenticator`); register vote + results + metadata + the census-proof proxy
(`GET .../census/proof`, Phase 6) in the **public** group (next to the CSP voter routes). Election
handlers in `api/process.go` (relay in a small `api/process_vote.go`); integrator/managed-org
handlers in `api/organizations.go`. There is **no** org-account endpoint — account creation is
internal to org creation.

**Path-param naming (do not be misled by the docs):** the chi param is literally `processId` in
*every* `/process/{…}` route (matching the existing `processEndpoint`). Read it with
`chi.URLParam(r, "processId")` and interpret per handler: **draft ObjectID** for `publish` and the
existing `GET /process/{processId}`, **on-chain election id** for `status`/`vote`/`results`/
`metadata`. NEW_API.md writes `{draftId}` only as a readability hint.

## Implementation phases

### Phase 0 — scaffolding (election params on drafts)
- Add `ElectionParams` types (`db/types.go`, `apicommon`) and the optional fields on
  create/update requests + `db.Process` (`ElectionParams`, `Status`, `PublishedAt`).
- Extend `createProcessHandler`/`updateProcessHandler` to persist `electionParams` (optional;
  draft with no params is still valid, exactly as today).
- `make swagger` + `make lint`. No behavior change for existing callers.
- **Tests:** round-trip a draft with `electionParams`; confirm a draft without params still works.

### Phase 1 — chain-abstracted organization creation (opt-in)
- `account.CreateOrgAccount(org)`: idempotent (check `apiclient.Account`); build `CreateAccount`
  (+ account metadata URL) → fund → sign(orgSigner) → submit → `WaitUntilTxIsMined`.
- Add optional `ProvisionAccount` to `OrganizationInfo`. In `createOrganizationHandler`, after
  `SetOrganization`, call `CreateOrgAccount` **only when** `provisionAccount` is true (the
  managed-org endpoint, Phase 5, always calls it). Response shape unchanged.
- Reuse the `CREATE_ACCOUNT` permission path in `HasTxPermission`.
- **Tests:** (a) default (flag off) → org created, **no** on-chain account, behavior identical to
  today; (b) `provisionAccount: true` → assert the account exists on chain (`apiclient.Account`);
  (c) idempotency (re-provision / legacy `createAccount` afterwards does not error); (d) a
  subsequent `/publish` works without any explicit account step.

### Phase 2 — process publish (centerpiece)
1. `POST /process/{draftId}/publish`: load draft; require Manager/Admin; require `electionParams`
   + census present.
2. Build `ElectionMetadata` from `electionParams`; store content-addressed in `objectstorage/`;
   that `https://` URL becomes `Process.Metadata` (also served by `GET .../metadata`).
3. Resolve census: this is the **CSP case** of a now-general census-origin switch (generalized in
   Phase 6) — origin = `OFF_CHAIN_CA`, `CensusRoot` = CSP pubkey, `CensusURI` = SaaS CSP endpoint.
   For the non-CSP (`weighted`/`zkweighted`) types the root/URI instead come from the published
   on-chain census.
4. `BuildNewProcessTx` → `FundTransaction` → `SignTransaction(orgSigner)` → `SubmitSignedTx` →
   `WaitUntilTxIsMined` → on-chain `processId` (the `data` returned by the submit).
5. Quota: reuse `HasTxPermission(NEW_PROCESS)` + counter bump. **Replicate the existing nuance**
   (`api/transaction.go`): only increment the org `Processes` counter when
   `MaxCensusSize > db.TestMaxCensusSize` (so test-sized elections don't count), keeping quota
   behavior identical to the `/transactions` path. If the org is managed (`ManagedBy != 0`), also
   enforce `CanPublishForManagedOrg` and bump the integrator's `ManagedProcesses` /
   `ManagedCensusSize` counters.
6. Persist `Address`, `Status="READY"`, `PublishedAt`. Idempotent: if `Address` already set,
   return it without a new tx.
- **Tests:** publish a draft against Voconed; assert election exists on chain and `Address` set;
  idempotency; Manager/Admin + quota enforcement (patterns from `api/process_test.go`,
  `api/csp_voting_test.go`).

### Phase 3 — vote relay + status lifecycle
- `POST /process/{processId}/vote` (public): decode `txPayload` (base64 → `models.SignedTx`),
  unmarshal inner `Tx`, assert `Tx_Vote` and matching `processId`; submit + confirm; return
  `{voteId}` (the nullifier). Never decode the VotePackage; never expose the tx hash.
- `PUT /process/{processId}/status`: map `ready|paused|ended|canceled` → `models.ProcessStatus`;
  `BuildSetProcessStatusTx` → fund/sign/submit. Manager/Admin.
- **Tests:** drive CSP `auth`→`sign`, build a vote envelope (as in `api/csp_voting_test.go`),
  relay it, assert the nullifier on chain; publish→pause→resume→end and assert chain transitions.

### Phase 4 — read proxies
- `GET /process/{processId}/results`: `apiclient.Election(processId)` → trimmed
  `ProcessResultsResponse` (status, voteCount, dates, results, finalResults).
- `GET /process/{processId}/metadata`: serve the stored `ElectionMetadata` JSON.
- **Tests:** publish + cast → results reflect the vote count; metadata round-trips the draft's
  `electionParams`.

### Phase 5 — integrator layer
- Data model: `ManagedBy`, `IsIntegrator`, `IntegratorLimits` (Organization), `Integrator` flag +
  `IntegratorLimits` (Plan/Features), integrator counters (`OrganizationCounters`), DB counter
  helpers.
- `subscriptions`: `IsIntegrator`, `EffectiveIntegratorLimits`, `CanCreateManagedOrg`,
  `CanPublishForManagedOrg`.
- `POST /organizations/{address}/managed` (admin of integrator `{address}`, integrator-enabled):
  enforce `CanCreateManagedOrg`; create a first-class org with `ManagedBy={address}`, forge its
  account (Phase 1), bypass `MaxOrgsPerUser`, set the integrator's user as creator/admin (or the
  optional `ownerEmail`), bump `ManagedOrgs`.
- `GET /organizations/{address}/managed`: paginated list of managed orgs (mirror the drafts list).
- `GET /organizations/{address}/integrator`: `{enabled, limits, usage}`.
- Wire integrator-aware quota into the publish handler (Phase 2, step 5).
- `cmd/cli`: a `set-integrator` command to enable an org and/or set its `IntegratorLimits` override.
- **Tests:** non-integrator is rejected from managed endpoints; integrator creates managed orgs up
  to `MaxManagedOrgs` then is blocked; publishing under a managed org enforces per-org + aggregate
  caps and bumps integrator counters; quota override beats plan limits.

### Phase 6 — non-CSP (merkle / weighted / dynamic) elections

So far every census in this plan is **CSP** (origin `OFF_CHAIN_CA`, root = CSP pubkey). The
vocdoni.app UI, however, still creates **non-CSP** elections — `weighted` and `zkweighted`
(anonymous) censuses with an explicit participant list — by talking to the Vochain via
`@vocdoni/sdk` directly, **bypassing the backend**. For the UI to use **only** the backend, the
managed path must also create these elections.

**No new crypto, nothing to vendor — reference only.** Everything Phase 6 needs is already exposed
by the `apiclient.HTTPclient` the backend already wraps (`account.Account.client`, from the existing
`go.vocdoni.io/dvote` dependency): census build/publish/proof and the election census setters. Proof
**verification** happens on-chain (node side); the backend only **generates** census proofs and
**relays** votes — it never holds a voter key and never decodes a ballot. So Phase 6 is **pure
wiring**: no import beyond the dependency already in `go.mod`, no vendored package, no new crypto.

Census taxonomy (the real on-chain types — there is no plain "tree" type; all censuses are weighted
now):

| `db.CensusType` | Anonymous | On-chain `CensusOrigin`     | Root / participants |
|-----------------|-----------|----------------------------|---------------------|
| `weighted`      | no        | `OFF_CHAIN_TREE_WEIGHTED`  | merkle root of `{key, weight}` participants |
| `zkweighted`    | yes (Poseidon hash; `EnvelopeType.Anonymous=true`) | `OFF_CHAIN_TREE_WEIGHTED` | merkle root of `{key, weight}` participants |
| `csp`           | no        | `OFF_CHAIN_CA`             | CSP pubkey (existing path, unchanged) |

apiclient methods reused (all already available on the wrapped client, signatures stable):
`NewCensus(censusType)` → root id; `CensusAddParticipants(censusID, *api.CensusParticipants)`
(participants are `{Key, Weight}`); `CensusPublish(censusID)` → `(root HexBytes, uri string)`;
`CensusGenProof(censusID, voterKey)` → `*CensusProof{Root, Proof, LeafValue, Siblings, LeafWeight}`;
`SetElectionCensus(electionID, api.ElectionCensus)` / `SetElectionCensusSize(electionID, newSize)`.

**Voter model (decided):** the voter signs the ballot **client-side**; the backend issues the census
proof and relays. The backend never holds the voter key and never decodes the ballot — identical
trust boundary to the CSP path, only the authorization artifact differs (merkle proof vs CSP blind
signature).

Six changes:

1. **`db.CensusType`:** add `CensusTypeWeighted="weighted"` and `CensusTypeZKWeighted="zkweighted"`
   (also done in the data-model section above). For these, census participants carry
   `{key: address/pubkey, weight}` instead of email/phone.
2. **`publishCensusHandler`:** for the new types, build the on-chain census
   (`NewCensus` → `CensusAddParticipants` → `CensusPublish`) and store the returned **root + URI**,
   instead of stuffing the CSP pubkey. The CSP path is unchanged.
3. **`BuildNewProcessTx` / `NewProcessParams`:** gain a `CensusOrigin` field; the root/URI come from
   the published census (CSP path unchanged). Anonymous (`zkweighted`) additionally sets
   `EnvelopeType.Anonymous`.
4. **`GET /process/{processId}/census/proof?key=<addr>`** (new, public): proxies `CensusGenProof`.
   The voter fetches the proof, builds + signs the envelope client-side, then posts it.
5. **`POST /process/{processId}/vote`** (existing relay, Phase 3): generalize so it accepts a
   CSP-authorized envelope **OR** a merkle-proof-carrying envelope, and relays either. It still
   verifies the tx is a `Vote` for `processId` and **never decodes the ballot**.
6. **`PUT /process/{processId}/census`** (new): dynamic add — `CensusAddParticipants` +
   `SetElectionCensusSize`. Gated behind the election's `DynamicCensus` mode flag.

- **Tests:** publish a `weighted` election against Voconed; fetch a proof via the new GET; build +
  sign a vote envelope (merkle proof instead of CSP) and relay it; assert the nullifier on chain. A
  `zkweighted` election sets `EnvelopeType.Anonymous`. Dynamic add grows the census and bumps
  `SetElectionCensusSize` (rejected when `DynamicCensus` is off). The CSP path (Phase 2/3 tests)
  stays green.

## Error handling & idempotency

- Chain submit/confirm uses a context timeout (~40s, as in `account.go`); failures map to
  `errors.ErrVochainRequestFailed`. Reuse typed `errors.Error`; messages start lowercase; wrap with
  `fmt.Errorf("...: %w", err)`.
- Org-account creation and publish are idempotent (on `apiclient.Account` existence and
  `db.Process.Address` respectively). Status changes are naturally idempotent on chain (re-ending an
  ended process → chain error → surfaced as 400).
- Vote relay returns the chain's nullifier/error verbatim (already-voted → 409).
- New typed errors: `ErrNotAnIntegrator` (403), `ErrMaxManagedOrgsReached`,
  `ErrIntegratorQuotaExceeded`.

## Risks / watch-outs

- **Org-creation back-compat (resolved):** forging is **opt-in** via `provisionAccount`. Legacy
  clients omit it and keep the exact two-step flow (DB org + SDK `createAccount`). The new SDK sets
  it and drops its own `createAccount`. The idempotent on-chain check additionally protects against
  any double-create races. No breaking change for existing callers.
- **Faucet spend:** eager account creation funds every org; integrator blast radius is bounded by
  `MaxManagedOrgs`.
- **Metadata hosting:** content-addressed `https://` URL (no IPFS); ensure it is stable, public,
  and immutable per content hash so the on-chain pointer never dangles.
- **apiclient submit method:** model the org-signed submit + `WaitUntilTxIsMined` on
  `account/account.go:88`; confirm the raw-tx submit call name in the pinned dvote version in Phase 2.
- **Param validation:** validate `electionParams` at publish (dates, maxCount ≤ choices, positive
  census size) — boundary input.
- **Quota races:** counter-based aggregate quotas can race under concurrent publishes; acceptable
  for now (small overshoot), tighten with atomic `$inc`-guarded updates if it matters.

## Definition of done (per phase)

`make lint` clean, `make swagger` regenerated, `make test` green (Docker), and the matching new
integration test passes against Voconed. Old `/transactions` path still green.
