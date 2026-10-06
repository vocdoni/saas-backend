# AGENTS.md

This file provides guidance to coding agents (Claude Code, Codex, etc.) when working with code in this repository.

## Overview

Vocdoni SaaS backend is a Go service that lets multiple users act on behalf of a single
organization on the Vocdoni Chain. It also acts as a **remote signer** for the Vocdoni SDK,
making the service transparent to SDK consumers. It manages organizations, members, censuses,
voting processes, subscriptions (Stripe), and a CSP (Credential Service Provider) that
authenticates voters and signs their ballots.

## Commands

```bash
make test        # go test -v ./...  (spins up MongoDB + Voconed via testcontainers — needs Docker)
make lint        # golangci-lint run
make swagger     # regenerate docs/swagger.yaml from swag annotations (run after changing API handlers/types)

./scripts/check-qt-patterns.sh   # quicktest anti-pattern gate — CI runs it alongside golangci-lint

# Run a single test / package
go test -run TestName ./api/
go test -v ./db/
# The api package is the slow one (~7 min for the whole suite) because every test
# publishes real elections against the Voconed container — always narrow with -run.

# Local dev stack (API + MongoDB + mongo-express on :8081)
cp example.env .env   # then edit secrets
docker compose up
# Optional add-ons via compose profiles (docker-compose.yml):
#   --profile with-ui      UI + defaultplan seed     --profile with-vocone  local Voconed chain + fundaccount
#   --profile local-smtp   fake SMTP capture server
```

- **Tests require Docker.** `TestMain` starts ephemeral MongoDB (`mongo:7`) and Voconed
  containers via `testcontainers-go`, each test run using a random database name (`test.RandomDatabaseName()`).
  There are no unit-test-only mocks for the DB — integration tests hit a real containerized Mongo.
- After editing any API handler, route, or `apicommon` request/response type, run `make swagger`
  so `docs/swagger.yaml` stays in sync (it is generated from `//` swag annotations on `api/api.go` and handlers).
  Gotcha: `scripts/generate-swagger.sh` deletes `docs/swagger.yaml` before regenerating and calls
  `swag` without `$(go env GOPATH)/bin` on `PATH`, so on a machine without `swag` already installed
  it deletes the file and then fails. Put that directory on `PATH` (or run `swag fmt -d ./` and
  `swag init -g api/api.go -o docs --outputTypes yaml --parseDependency --parseInternal --parseDepth=4`
  by hand) if the target errors out.
- API integration tests drive the server over HTTP through generic helpers in `api/api_test.go`:
  `requestAndParse[T]`, `requestAndAssertCode`, `requestAndAssertError`, and `enqueueAndPollJob` /
  `pollJob` for the async job endpoints. Use those rather than hand-rolling requests.

## Architecture

Configuration flows through **Viper with the `VOCDONI_` env prefix** (e.g. `--mongoURL` flag ⇄
`VOCDONI_MONGOURL`). `cmd/service/main.go` wires every component into an `api.Config` and calls
`api.New(conf).Start()`. Services are optional and conditionally enabled based on whether their
config is present (SMTP, Twilio SMS); Stripe is required.

### Voting processes: one question = one on-chain election

A `db.VotingProcess` (`/processes`) is a *container* of `db.VotingProcessQuestion`s, and **each
question is its own on-chain election**, identified after publish by its `UpstreamID`. So a voter
casts one vote transaction per question, holds one CSP signature per question, and one nullifier
per question. Anything taking an on-chain election id resolves it with `db.QuestionByUpstreamID`.
Voter-facing CSP handlers live in `csp/handlers/processes.go`.

The legacy `/process` + `/process/bundle` API (#722) is gone, but its `processes`/`processBundles`
collections are still **read**: `GET /processes` projects legacy rows, managed-org teardown deletes
them, and `parseRelayVote` in `api/process_vote.go` tries `db.ProcessByAddress` before
`db.QuestionByUpstreamID`. Nothing writes to them anymore — do not add writers.

A consequence worth internalizing: because one voter action fans out to N elections, several
endpoints exist purely in batch form so a voter cannot end up half-done — `POST /votes` (relay),
`POST /votes/verify`, `POST /processes/{processId}/sign-batch`. They share a shape: cap the body,
validate/authorize the **whole** batch before doing anything, then act.

### On-chain writes are asynchronous jobs, not request-scoped

Any endpoint that submits a transaction returns **202 + `{"jobId": ...}`** and the caller polls
`GET /jobs/{jobId}`. The machinery is `api/jobqueue.go`:

- `a.enqueueTx(txTask{...})` / `a.enqueueTxBatch(tasks)` push onto a buffered `txQueue`
  (512 slots, 16 workers). `enqueueTxBatch` takes all the slots or none; a full queue is a 503.
- A `txTask.run` closure does the submit *and* waits for the tx to be mined, so it blocks a worker
  the whole time — never spend one on work that isn't an on-chain submit.
- `txTask.record` overrides how the outcome is written. Tasks sharing one job (the envelopes of a
  batch relay) need it, or the first to finish would mark the whole job terminal.
- `a.orgTxLocks.lock(org)` serializes build→sign→submit per organization so two concurrent requests
  cannot read the same account nonce. The lock is handed to the worker and released after submit.

`statussync/` closes the loop in the other direction: an enqueue-driven worker that reconciles a
published question's stored status against the Vochain, triggered by API status changes and by
reads, rather than by a timer.

Component packages (each is a focused service composed in `main.go`):

- **`api/`** — the HTTP server (chi router + JWT auth via `jwtauth`). `initRouter()` in `api/api.go`
  registers all routes in two `chi` groups: a **protected group** (behind `jwtauth.Verifier` +
  `a.authenticator`) and a **public group** (login, registration, webhooks, voter-facing CSP
  endpoints, `/ping`). Handlers live in topic files (`organizations.go`, `processes*.go`,
  `org_members.go`, `organization_groups.go`, etc.); every route path constant is in `api/routes.go`.
  `api/apicommon/` holds shared request/response types and helpers (`HTTPWriteJSON`, `UserFromContext`).
- **`db/`** — MongoDB storage layer (`MongoStorage`). One file per collection/domain
  (`organizations.go`, `org_members.go`, `census.go`, `process.go`, `jobs.go`...). Collections are
  fields on `MongoStorage` initialized in `mongo.go`. Schema migrations live in **`migrations/`** and
  run via `RunMigrationsUp()`; register new ones with `migrations.AddMigration(version, name, up, down)`.
  Setting `VOCDONI_MONGO_RESET_DB=true` drops all collections on startup (used by tests).
- **`csp/`** — Credential Service Provider: authenticates voters (via email/SMS challenge) and
  blind/ECDSA-signs their votes so they can cast on-chain. `csp/signers/` (saltedkey), `csp/handlers/`
  (HTTP), `csp/notifications/` (challenge queue). Signing key derives from the service `RootKey`.
- **`account/`** — wraps the Vocdoni blockchain account: signs transactions, funds accounts via
  faucet, computes election prices. This is the "remote signer" core.
- **`notifications/`** — `NotificationService` interface with `smtp/` (email) and `twilio/` (SMS)
  implementations, plus `mailtemplates/` (HTML/text email templates, loaded with `mailtemplates.Load()`).
  `FailoverService` composes multiple services and delivers via the first that succeeds; the service
  wires it from the optional `VOCDONI_BACKUPSMTP*` vars to fail over from the primary SMTP relay to a
  backup that shares the same sender identity. The CSP drains email/SMS through
  `csp/notifications.Queue`: a concurrent worker pool (default 16) with a per-provider circuit breaker.
- **`statussync/`** — enqueue-driven worker reconciling a published question's stored on-chain status
  with the Vochain (no timer sweep). Fed by two triggers: a status change via the API (confirm the tx
  landed) and a read of a process/question (catch direct on-chain changes). Wired in
  `cmd/service/main.go` into `api.Config.StatusSyncer` (a nil enqueuer — as in tests — makes enqueues
  no-ops). The managed-org delete guard deliberately reads the chain *synchronously* rather than
  trusting the stored status.
- **`subscriptions/`** — permission/quota manager enforcing what an organization's plan allows.
- **`stripe/`** — Stripe client, checkout/portal sessions, and webhook handling for billing.
- **`objectstorage/`** — S3-like object storage (images) backed by Mongo, with upload/download handlers.
- **`errors/`** — typed API `Error` (code + HTTP status + optional data), JSON-serializable; handlers
  return these. Predefined errors in `errors_definition.go`.
- **`internal/`** — shared primitives: `HexBytes`, birthdate parsing, phone/argon2 helpers.
- **`cmd/`** — `service/` (the API server), `cli/` (`--setIntegrator` org switch),
  `userdel/` (GDPR user erasure), `localsmtp/` (dev SMTP capture).
- **`assets/`** are embedded via `embed.go` (`//go:embed all:assets`).

## Conventions

These come from `.clinerules/` and `.gemini/styleguide.md` (the Vocdoni Go style guide):

- **Errors:** use `fmt.Errorf()` (not `errors.New()`) — project-specific preference. Always wrap with
  context: `fmt.Errorf("doing x: %w", err)`. Inspect with `errors.Is` / `errors.As`. Check every error.
- **Log/error message style:** start messages with a **non-capital letter**. Use structured key/value
  logging: `log.Infow("starting api server", "host", host, "port", port)`.
- **Formatting:** `gofumpt` (enforced by golangci-lint). Line length limit 130 (`lll`).
- **Function signatures:** if >3 params or returns, pack into a struct.
- **Docs:** all exported entities must have godoc comments starting with the entity name.
- **Testing with `quicktest` (`qt`):** use the right matcher, not `len()`+`Equals`:
  - `c.Assert(s, qt.HasLen, N)` not `c.Assert(len(s), qt.Equals, N)`
  - `c.Assert(v, qt.IsNil)` / `qt.Not(qt.IsNil)`, `qt.IsTrue`/`qt.IsFalse`, `qt.Contains`, `qt.DeepEquals`
  - `scripts/check-qt-patterns.sh` flags these anti-patterns.
- **Linting:** `revive` runs with `enable-all-rules`; a few rules (`exported`, `use-errors-new`,
  `add-constant`, complexity rules) are temporarily disabled in `.golangci.yml` — don't rely on them
  being off forever, but matching existing code is fine.
- **Commits & PR titles:** [Conventional Commits](https://www.conventionalcommits.org/)
  (`fix(orgs): ...`). CI lints both but reports via a sticky PR comment instead of failing.
- **Communication** (`.clinerules/04-communication.md`): be factually rigorous and direct; don't
  reflexively agree ("You're absolutely right!") when the statement may be wrong; state uncertainty
  explicitly rather than speculating.

## API source of truth, and what follows it downstream

**The API code is the source of truth**: the route constants in `api/routes.go`, the handlers in
`api/` and `csp/handlers/`, and every type they expose (`api/apicommon`, plus the `csp/handlers` and
`db` types that reach the wire). `docs/swagger.yaml` is generated from their swag annotations
(`make swagger`) and published to GitHub Pages by `.github/workflows/swag.yml`; it is the only API
reference in this repo. Never edit it by hand, and don't add hand-written endpoint docs or request
collections here; the hand-maintained `api/docs.md` and its companion `api/examples.http` were removed
after they drifted out of date. If the spec is wrong, fix the annotations: a `@Router` path must match
the route constant the handler is registered under.

Everything else follows the API and goes stale whenever it changes without a matching update:

- the developer guides integrators actually read (quickstart, census, voting processes, casting
  votes, jobs, results, quotas...), which live in [`vocdoni/vocdoni.io`](https://github.com/vocdoni/vocdoni.io)
  under `content/developers/docs/en/`;
- the [`vocdoni/vocdoni-integrator-sdk`](https://github.com/vocdoni/vocdoni-integrator-sdk) (see below).

When one of them disagrees with the code, the code is right and they are what needs fixing — never
"fix" the API or the spec to match stale docs.

Whenever a change alters anything an API consumer can observe — a route added, removed or renamed,
a request/response field, a status or error code, auth or permission rules, job/polling behaviour,
quota checks, or the voter (CSP) flow — **suggest opening an issue or a PR in `vocdoni/vocdoni.io`**
naming the affected page(s) and what changed. Mind vocdoni.io's branches: its `main` ships to
production with the next `lts` release, while `stage` is a content-only branch shown through the docs
version selector (`?version=stage`). Docs for a change that has not reached this repo's production
branch (`lts`) belong on `stage`, not `main`. `stage` is never deployed on its own, so once the
backend change reaches `lts`, the same follow-up must also get those docs onto vocdoni.io `main`
(e.g. a PR porting them from `stage`); vocdoni.io's own AGENTS.md has the details.

The same goes for the [`vocdoni/vocdoni-integrator-sdk`](https://github.com/vocdoni/vocdoni-integrator-sdk)
TypeScript SDK, which wraps this API (`@vocdoni/api-types` mirrors the request/response types,
`@vocdoni/api-client` the endpoints and job polling, `@vocdoni/api-voting` the CSP/vote flow). When a
change means the SDK must be updated — a type or endpoint it wraps changed — or should gain support for
a new feature, **suggest opening an issue or a PR in `vocdoni/vocdoni-integrator-sdk`** describing what
it needs.

Always record the docs/SDK impact in this repo's PR description: link the issue or PR once it exists,
or say it is still pending or was deferred — never leave it out, or reviewers will read the silence as
"no impact". Suggest the follow-up rather than doing it unasked: whether to open it, and where, is the
user's call.

## CI

`.github/workflows/main.yml` gates PRs on: `golangci-lint` (v2.12.2, `only-new-issues`),
`./scripts/check-qt-patterns.sh`, a clean `go mod tidy` diff, and `go test -failfast -timeout=30m ./...`
with coverage (a diff report is posted as a PR comment). It runs on pull requests and on pushes to
`main` only, so in practice every push to `main` builds and pushes a Docker image, while its `-race`
step and the `stage`/`release*`/`aragon` Docker conditions never fire. The branch flow is
`main → stage → lts` (`lts` is production), and promotion PRs (head branch `main`) skip commit
linting. `.github/workflows/swag.yml` regenerates `docs/swagger.yaml` on PRs (posting the diff as a
comment) and, on pushes to `stage`, commits the regenerated spec and deploys the Swagger UI.
