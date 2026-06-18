# SUMMARY — Chain-abstracted SaaS API + new app SDK

A high-level overview for the team. Detailed designs live in
[BACKEND_PLAN.md](BACKEND_PLAN.md), [NEW_API.md](NEW_API.md), and [APP_SDK_PLAN.md](APP_SDK_PLAN.md).

## The goal

Make the Vocdoni SaaS a **complete, chain-abstracted election API**. Today an integrator (and our
own UI) must use `@vocdoni/sdk` + `RemoteSigner` and understand Vochain transactions to run an
election. After this work, they run the **entire election lifecycle and voting through plain REST**,
never touching the chain.

The backend already holds each organization's key and signs on its behalf. We extend it so it also
**forges, funds, submits and confirms** every organizer transaction server-side, and **relays**
client-signed votes — so callers see a normal SaaS API: resource ids in, resource ids out, no tx
hashes, gas, faucet, or signers.

## What's changing (in one picture)

```
            BEFORE                                   AFTER
  UI ──@vocdoni/sdk──► Vochain            UI / integrator ──REST──► SaaS ──► Vochain
   └── RemoteSigner ──► SaaS                         (no SDK, no chain, no signer)
```

## The model: organization-based (unchanged) + integrator (new)

We keep the **organization-based** flow exactly as vocdoni.app works today: register → create an
organization → its admins create elections. The new behavior is **opt-in and backwards compatible**:
when a caller sets `provisionAccount` (the new SDK does; managed client orgs always do), the SaaS
**forges the org's on-chain account server-side** at creation — no separate account/faucet step.
Existing clients omit the flag and keep today's two-step flow (DB org + SDK `createAccount`)
completely unchanged.

On top of that we add an **integrator**: an ordinary org granted a special capability so it can
**create and manage organizations for its own clients** through the API, under an accountable quota.
Managed client orgs are first-class (own address, own account, invitable members), linked to the
integrator by a `managedBy` field. Enabling an integrator and setting its quota are internal/admin
operations (a CLI command we run) — no public endpoint grants integrator status.

## New API surface

Everything below is **additive** — no existing route changes shape. `POST /transactions` (the old
RemoteSigner path) keeps working throughout, so old and new can coexist during migration.

| Area | Endpoint | Who |
|------|----------|-----|
| Org account | `POST /organizations` *(+ optional `provisionAccount` to forge the on-chain account)* | organizer |
| Election | `POST /process` / `PUT /process/{id}` *(+ optional `electionParams`)* | organizer |
| Publish | `POST /process/{draftId}/publish` → `{processId}` | manager/admin |
| Lifecycle | `PUT /process/{processId}/status` (ready/paused/ended/canceled) | manager/admin |
| Vote relay | `POST /process/{processId}/vote` → `{voteId}` | public (CSP-authorized) |
| Reads | `GET /process/{processId}/results` · `GET /process/{processId}/metadata` | public |
| Integrator | `POST`/`GET /organizations/{address}/managed` · `GET /organizations/{address}/integrator` | integrator admin |

Integrator quota dimensions: managed orgs, elections per org, total elections, census per election,
census per org, total census. A `0` limit means unlimited. Publishing under a managed org rolls
usage up to the integrator and enforces its caps.

## How the hard parts are solved

- **No migration:** all new DB fields (on `Process`, `Organization`, `Plan`) are optional/zero —
  Mongo is schemaless, existing docs unmarshal fine.
- **Election metadata URL chicken-and-egg:** the on-chain `Process.Metadata` needs a public URL
  *before* submit, but the `processId` only exists *after*. Solved by storing the metadata JSON
  **content-addressed** (`https://.../storage/{hash}.json`) — a stable URL known before the tx.
- **Ballot secrecy:** the vote relay verifies the envelope is a Vote for the right process and
  forwards it; it never decodes the ballot.
- **Idempotency:** org-account creation and publish are safe to retry (checked against on-chain
  account existence / stored `processId`).

## The new SDK — `vocdoni-app-sdk`

A standalone TypeScript SDK that talks **only** to this SaaS REST API (no `@vocdoni/sdk`
dependency). Mirrors `@vocdoni/sdk`'s structure/tooling. Consumer surface has **zero blockchain
concepts**: `login`, `createOrganization`, `createDraft`, `publish`, `setStatus`, `results`,
`vote(processId, choices)`, plus integrator methods. The only crypto it owns is building+signing the
vote envelope (a minimal ported module), entirely hidden inside `vote()`. The vocdoni.app UI
migrates to it incrementally.

## Plan & status

Backend ships in 6 phases (each: `make lint` + `make swagger` + `make test` green, plus a Voconed
integration test):

0. Election params on drafts *(scaffolding)*
1. Chain-abstracted org creation
2. Publish *(centerpiece)*
3. Vote relay + status lifecycle
4. Read proxies (results + metadata)
5. Integrator layer (managed orgs + quota)

**Current status:** designs finalized and reconciled across the three plan docs; implementation
starting at Phase 0. The SDK is planned but **out of scope for this round** — backend first.
