# APP_SDK_PLAN.md — `vocdoni-app-sdk`

A brand-new TypeScript SDK that consumes **only** the SaaS REST API (the existing endpoints plus
the new managed, chain-abstracted ones). Used by third-party integrators and by the vocdoni.app UI
itself. It mirrors the **structure and coding style of `@vocdoni/sdk`** (in
`other-repos/vocdoni-sdk/`) but talks to the SaaS, not the Vochain.

Design decisions already locked:
- **Standalone**: no `@vocdoni/sdk` dependency. The one piece of Vochain crypto needed —
  building + signing the vote envelope — is a **minimal ported module** (see §5).
- **Thin vote relay**: the SDK builds + signs the vote locally and POSTs the opaque envelope
  to the SaaS `/process/{id}/vote`; the SaaS never sees plaintext.
- Organizer flows are **plain REST** (the SaaS forges all txs).

## 1. Tooling & conventions (match vocdoni-sdk exactly)

Mirror `other-repos/vocdoni-sdk/`:
- **Package**: `@vocdoni/app-sdk` (name TBD), AGPL-3.0, `main: src/index.ts`.
- **Build**: Rollup + esbuild (`rollup.config.mjs`), node-polyfills, outputs
  `dist/index.{js,mjs,umd.js,d.ts}` (CJS/ESM/UMD + types).
- **TS**: `tsconfig.json` target `esnext`, `moduleResolution nodenext`,
  `noImplicitReturns`, `noUnusedLocals/Parameters`.
- **Tests**: Jest (ts-jest), split `test/unit/`, `test/api/`, `test/integration/`.
- **Lint/format**: ESLint + Prettier — 120 width, single quotes, trailing commas, no semis if
  vocdoni-sdk omits them (match its `.prettierrc`).
- **Repo location**: a new repo `vocdoni-app-sdk` (sibling of vocdoni-sdk/vocdoni-app).

## 2. Source layout (mirror vocdoni-sdk's split)

```
src/
  index.ts                 // barrel: export * from client/types/api/services/util
  client.ts                // VocdoniAppClient (service composition, like VocdoniSDKClient)
  api/                     // thin REST wrappers, one file per SaaS domain
    auth.ts                //   /auth/login,/refresh,/addresses
    organization.ts        //   /organizations... (+ managed-org + integrator endpoints)
    census.ts              //   /census, members, groups
    process.ts             //   /process create/update/publish/status/results
    csp.ts                 //   /process/bundle/{id}/auth|sign|check|weight
    vote.ts                //   /process/{id}/vote (relay)
    index.ts
  services/                // business logic over api/ (like vocdoni-sdk services)
    auth.ts organization.ts census.ts election.ts vote.ts csp.ts
    service.ts             // base Service (shared url/token)
    index.ts
  types/                   // domain types + validation (yup, like vocdoni-sdk)
    organization/ census/ process/ vote/ auth/
    index.ts
  core/                    // the ONLY Vochain crypto — ported, minimal (see §5)
    vote.ts                // VoteCore: envelope + CSP ProofCA assembly
    transaction.ts         // encode/hash/sign SignedTx -> base64
  util/
    common.ts              // strip0x/ensure0x/getHex (port)
    signing.ts             // ethers signMessage wrapper (port)
    blind-signing.ts       // CensusBlind blind/unblind (port)
    encryption.ts          // NaCl sealedbox for encrypted votes (port, optional/phase 2)
    constants.ts           // SAAS_URL defaults per env, tx message templates
```

## 3. Main client (composition like `VocdoniSDKClient`)

```ts
export type AppClientOptions = {
  env: EnvOptions               // DEV | STG | PROD (maps to default SaaS URL)
  api_url?: string              // SaaS base URL override (that's it — no token, no wallet)
}

export class VocdoniAppClient {
  public authService: AuthService             // owns the JWT internally (login/refresh)
  public organizationService: OrganizationService
  public censusService: CensusService
  public electionService: ElectionService     // create/publish/status/results (REST)
  public cspService: CspService               // CSP two-factor (REST)
  public voteService: VoteService             // build+sign envelope, relay (owns voter key)
  public url: string
  // No public token, wallet, or signer. The JWT and the ephemeral voter key are private,
  // managed by AuthService / VoteService respectively.
}
```

Service-based composition with DI, identical to vocdoni-sdk's `client.ts`. `index.ts` is a
barrel re-exporting `client`, `types`, `api`, `services`, `util/common`.

### Consumer experience: no blockchain surface

The SDK's public API is **100% like a standard SaaS client** — the integrator never sees a
transaction, tx hash, token, gas, faucet, signer, or private key:

- **Auth** is just `login(email, password)`. The JWT is stored and refreshed inside
  `AuthService`; callers never pass or read a token.
- **Organizer ops** (create org / census / draft / publish / setStatus) are plain awaited REST
  calls returning resource ids (org address, `processId`). The SaaS forges and confirms every tx.
  The three on-chain actions — `publish`, `setStatus`, `vote` — are **async on the wire** (the SaaS
  returns `202` + a `jobId` and confirms on a background worker), but the SDK hides this: each method
  enqueues, then polls `GET /jobs/{jobId}` until the job completes and resolves with the on-chain
  result (or rejects on a failed job). Consumers just `await` and never see the job id.
- **Voting** takes `vote(processId, choices)`. The ephemeral voter key needed to sign the CSP
  envelope is **generated and held internally by `VoteService`** (kept for the session so a
  re-vote/overwrite reuses the same nullifier). The integrator passes choices, gets back a
  `voteId` receipt — no key handling, no envelope, no tx, no polling.

The only crypto the SDK owns (the vote envelope, §5) runs entirely under the hood; it is an
implementation detail of `vote()`, not part of the consumer surface.

## 4. Public method surface (maps 1:1 to SaaS endpoints)

```
Auth:     login(email,password) refresh() logout() me()            // /auth/*, /users/me
Org:      createOrganization(...)  getOrganization(addr)            // POST forges the on-chain account server-side
          members.* (list/add/import CSV/delete)  groups.*          // /organizations/*
Integr.:  createManagedOrganization(integratorAddr, info)           // POST /organizations/{addr}/managed
          listManagedOrganizations(integratorAddr)                  // GET  /organizations/{addr}/managed
          integratorInfo(integratorAddr): {enabled,limits,usage}    // GET  /organizations/{addr}/integrator
Census:   createCensus(fromGroupId|members)  getCensus(id)         // /census
Election: createDraft(params)  updateDraft(id,params)
          publish(draftId): Promise<{processId}>                   // POST /process/{id}/publish → 202; polls /jobs/{jobId}
          setStatus(processId, 'ready'|'paused'|'ended'|'canceled')// PUT  /process/{id}/status   → 202; polls /jobs/{jobId}
          get(draftId)  results(processId)                          // GET  /process/{id}[/results]
Voter:    csp.info(bundleId)  csp.auth(bundleId,step,data)          // OTP challenge: consumer enters the code
          vote(processId, choices): Promise<{voteId}>               // CSP sign + envelope + relay (202) + poll, all internal
```

The `202` + `jobId` poll loop lives in a small shared helper (`awaitJob(jobId)`) inside the
HTTP client, reused by `publish`, `setStatus` and `vote`; it polls `GET /jobs/{jobId}` until
`status` is `completed` (resolve with `result`) or `failed` (reject with `error`).

`election.publish/setStatus/results`, `vote`, and the integrator methods are the new managed
endpoints; everything else maps to today's API. The integrator never constructs a transaction.

**Chain-abstracted org creation:** `createOrganization` is a single REST call that sets
`provisionAccount: true`, so the SaaS forges the org's on-chain `CreateAccount` server-side
(idempotent). The SDK does **not** call `createAccount` and does **not** wire a `RemoteSigner`;
there is no account/faucet/tx step on the org-creation path. (The flag is opt-in on the API, which
keeps the legacy UI's two-step flow working; the new SDK always opts in.)

**Integrator methods are optional** — they only succeed for integrator-enabled orgs. Managed
(client) organizations are **first-class** orgs: each has its own address, on-chain account, and
invitable members, and is linked to the integrator by `managedBy`. They are not sub-orgs.

Only `csp.auth` is consumer-driven, because the two-factor challenge needs the human to enter the
OTP they receive by email/SMS. The blind-signature step, the ephemeral voter key, the envelope and
the relay are all internal to `vote()`. `CspService` holds the completed auth token for the session
so `vote()` can use it.

### `vote(processId, choices)` internally
1. `electionService.results(processId)` (or a get) → process params needed for the envelope
   (censusOrigin=CSP, voteOptions, encryption pubkeys if `secretUntilTheEnd`).
2. Use the **internally generated** ephemeral voter key (held by `VoteService` for the session)
   and the CSP auth token from the completed `csp.auth` → CSP blind-sign → unblinded signature.
3. `core/vote.ts` builds `VoteEnvelope` with `ProofCA` (CSP), packages votes, signs with the
   internal voter key → hex `SignedTx`.
4. `voteService.relay(processId, txPayload)` → `POST /process/{processId}/vote` → `202 {jobId}`,
   then `awaitJob(jobId)` → `{voteID}` (the receipt). The poll is internal to `vote()`.

## 5. Ported minimal vote-envelope module (the only crypto we own)

Port from `other-repos/vocdoni-sdk/` (copy + trim to the CSP path only):

| Port into | From vocdoni-sdk |
|-----------|------------------|
| `core/vote.ts` `generateVoteTransaction` (CSP `ProofCA` branch), `packageVoteContent`, `cspCaBundle`, `encodeCspCaBundle` | `src/core/vote.ts` |
| `core/transaction.ts` `encodeTransaction`, `hashTransaction` | `src/core/transaction.ts` |
| `util/signing.ts` `Signing.signTransaction` | `src/util/signing.ts` |
| `util/blind-signing.ts` `CensusBlind` (blind/unblind, decodePoint), `getBlindedPayload` | `src/util/blind-signing.ts` |
| `util/common.ts` `strip0x/ensure0x/getHex` | `src/util/common.ts` |
| `util/encryption.ts` NaCl sealedbox (encrypted votes — phase 2) | `src/util/encryption.ts` |
| types `Vote`, `CspVote`, `VotePackage`, `CspProofType` | `src/types/vote/*` |

External deps for this module (same versions as vocdoni-sdk):
- `@vocdoni/proto` — `VoteEnvelope`, `Tx`, `SignedTx`, `ProofCA`, `ProofCA_Type`, `CAbundle`.
- `@ethersproject/wallet` + `@ethersproject/keccak256` (ethers v5) — sign + hash.
- `blindsecp256k1` — CSP blind/unblind.
- `tweetnacl` — encrypted-vote sealedbox (phase 2 only).

**Explicitly NOT ported** (now done server-side by the SaaS): election creation
(`core/election.ts`), account crypto (`core/account.ts`), Merkle/offchain census proofs,
ZK/anonymous (`snarkjs`/`circomlibjs`), Census3. This keeps the SDK lean and dependency-light.

## 6. Phases

1. **Scaffold** repo with vocdoni-sdk tooling (rollup/tsconfig/jest/eslint/prettier), empty
   `VocdoniAppClient`, `Service` base, `api/` HTTP layer (fetch/axios wrapper with JWT +
   refresh, mirroring the app's current `api()` wrapper).
2. **Auth + Org + Census** services (pure REST) + unit tests.
3. **Election** service: `createDraft/updateDraft/publish/setStatus/get/results` against the
   new SaaS endpoints.
4. **Vote**: port the minimal crypto module (§5), implement `csp.*` + `vote()`, integration
   test against a running SaaS + Voconed (publish → CSP auth/sign → vote → results).
5. **Encrypted votes** (optional): NaCl sealedbox path for `secretUntilTheEnd` elections.
6. **Docs + examples**, publish to npm, then migrate the vocdoni.app UI (§7).

## 7. vocdoni.app UI migration

The UI currently uses `@vocdoni/sdk` + `RemoteSigner` (`src/components/Auth/*`,
`src/queries/*`). Migration:
- Replace the `RemoteSigner`/`VocdoniSDKClient` wiring with `VocdoniAppClient` (token-based).
- Repoint `src/queries/*` data access to the new SDK's services (they already wrap the same
  SaaS endpoints, so most query functions become thin pass-throughs).
- Voting components call `client.vote(processId, choices)` instead of building envelopes.
- Keep the migration incremental: the SaaS keeps `/transactions` alive, so the old and new
  paths can coexist per-route during the switch.

## 8. Testing

- **Unit** (jest, no network): envelope assembly determinism, CSP proof bundle encoding,
  blind/unblind round-trip, request builders.
- **Integration**: against a local SaaS (`docker compose up` with `--profile with-vocone`):
  login → create org/census → createDraft → publish → CSP auth/sign → vote → results.
- Mirror vocdoni-sdk's `test/integration` structure (csp/election/account folders).

## Definition of done

Lint clean, build produces CJS/ESM/UMD + types, unit + integration suites green against a
local SaaS, and the vocdoni.app UI builds against the new SDK for at least the org + voting
flows.
