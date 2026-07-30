# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Vocdoni SaaS backend is a Go service that lets multiple users act on behalf of a single
organization on the Vocdoni Chain. It also acts as a **remote signer** for the Vocdoni SDK,
making the service transparent to SDK consumers. It manages organizations, members, censuses,
voting processes/bundles, subscriptions (Stripe), and a CSP (Credential Service Provider) that
authenticates voters and signs their ballots.

## Commands

```bash
make test        # go test -v ./...  (spins up MongoDB + MailHog via testcontainers — needs Docker)
make lint        # golangci-lint run
make swagger     # regenerate docs/swagger.yaml from swag annotations (run after changing API handlers/types)

# Run a single test / package
go test -run TestName ./api/
go test -v ./db/

# Local dev stack (API + MongoDB + mongo-express on :8081)
cp example.env .env   # then edit secrets
docker compose up
# Optional add-ons via compose profiles (docker-compose.yml):
#   --profile with-ui      UI + defaultplan seed     --profile with-vocone  local Voconed chain + fundaccount
#   --profile local-smtp   fake SMTP capture server
```

- **Tests require Docker.** `TestMain` starts ephemeral MongoDB (`mongo:7`) and MailHog (email
  capture) containers via `testcontainers-go`, each test run using a random database name
  (`test.RandomDatabaseName()`). The Voconed test chain runs **in-process** via `test.SharedVoconed()`
  (a shared singleton, not a container). Helpers for all of this live in the `test/` package.
  There are no unit-test-only mocks for the DB — integration tests hit a real containerized Mongo.
- After editing any API handler, route, or `apicommon` request/response type, run `make swagger`
  so `docs/swagger.yaml` stays in sync (it is generated from `//` swag annotations on `api/api.go`
  and handlers). CI regenerates it on PRs touching `api/**` and posts the diff as a PR comment.
- **Conventional Commits are enforced by CI** (commitlint on every commit message and a semantic
  PR-title check), e.g. `feat(processes): ...`, `fix(users): ...`, `ci: ...`.

## Architecture

Configuration flows through **Viper with the `VOCDONI_` env prefix** (e.g. `--mongoURL` flag ⇄
`VOCDONI_MONGOURL`). `cmd/service/main.go` wires every component into an `api.Config` and calls
`api.New(conf).Start()`. Services are optional and conditionally enabled based on whether their
config is present (SMTP, Twilio SMS); Stripe is required.

Component packages (each is a focused service composed in `main.go`):

- **`api/`** — the HTTP server (chi router + JWT auth via `jwtauth`). `initRouter()` in `api/api.go`
  registers all routes in two `chi` groups: a **protected group** (behind `jwtauth.Verifier` +
  `a.authenticator`) and a **public group** (login, registration, webhooks, voter-facing CSP
  endpoints, `/ping`). Handlers live in topic files (`organizations.go`, `census.go`, `process.go`,
  `org_members.go`, `organization_groups.go`, etc.). Route path constants are defined alongside.
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
- **`subscriptions/`** — permission/quota manager enforcing what an organization's plan allows.
- **`stripe/`** — Stripe client, checkout/portal sessions, and webhook handling for billing.
- **`objectstorage/`** — S3-like object storage (images) backed by Mongo, with upload/download handlers.
- **`errors/`** — typed API `Error` (code + HTTP status + optional data), JSON-serializable; handlers
  return these. Predefined errors in `errors_definition.go`.
- **`internal/`** — shared primitives: `HexBytes`, birthdate parsing, phone/argon2 helpers.
- **`cmd/`** — `service/` (the API server), `cli/` (DB query tool for process/voter stats),
  `client/` (HTTP client for CSV member import + census workflows).
- **`assets/`** are embedded via `embed.go` (`//go:embed all:assets`).
- **`plan/`** — design docs (not code) for the in-progress "chain-abstracted API" rework: making the
  SaaS a complete REST election API so integrators no longer need `@vocdoni/sdk` + `RemoteSigner`.
  Read `plan/SUMMARY.md` first when working on anything related to that effort.

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
- **Communication (from `.clinerules/04-communication.md`):** be direct and factually rigorous; don't
  reflexively agree ("You're absolutely right!") when a statement may be incorrect; admit uncertainty
  explicitly rather than speculating.
