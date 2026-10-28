<p align="center" width="100%">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://app-dev.vocdoni.io/assets/logo-classic-white.svg" />
      <source media="(prefers-color-scheme: light)" srcset="https://app-dev.vocdoni.io/assets/logo-classic.svg" />
      <img alt="Vocdoni logo" src="https://app-dev.vocdoni.io/assets/logo-classic.svg" />
  </picture>
</p>

<p align="center" width="100%">
    <a href="https://github.com/vocdoni/saas-backend/commits/main/"><img src="https://img.shields.io/github/commit-activity/m/vocdoni/saas-backend" /></a>
    <a href="https://github.com/vocdoni/saas-backend/issues"><img src="https://img.shields.io/github/issues/vocdoni/saas-backend" /></a>
    <a href="https://github.com/vocdoni/saas-backend/actions/workflows/main.yml/"><img src="https://github.com/vocdoni/saas-backend/actions/workflows/main.yml/badge.svg" /></a>
    <a href="https://pkg.go.dev/github.com/vocdoni/saas-backend"><img src="https://godoc.org/go.vocdoni.io/saas-backend?status.svg"></a>
    <a href="https://chat.vocdoni.io"><img src="https://img.shields.io/badge/discord-join%20chat-blue.svg" /></a>
    <a href="https://twitter.com/vocdoni"><img src="https://img.shields.io/twitter/follow/vocdoni.svg?style=social&label=Follow" /></a>
</p>


  <div align="center">
    Vocdoni is the first universally verifiable, censorship-resistant, anonymous, and self-sovereign governance protocol. <br />
    Our main aim is a trustless voting system where anyone can speak their voice and where everything is auditable. <br />
    We are engineering building blocks for a permissionless, private and censorship resistant democracy.
    <br />
    <a href="https://vocdoni.io/developers"><strong>Explore the developer portal »</strong></a>
    <br />
    <h3>More About Us</h3>
    <a href="https://vocdoni.io">Vocdoni Website</a>
    |
    <a href="https://vocdoni.app">Web Application</a>
    |
    <a href="https://explorer.vote/">Blockchain Explorer</a>
    |
    <a href="https://law.mit.edu/pub/remotevotingintheageofcryptography/release/1">MIT Law Publication</a>
    |
    <a href="https://vocdoni.io/contact">Contact Us</a>
    <br />
    <h3>Key Repositories</h3>
    <a href="https://github.com/vocdoni/vocdoni-app">Vocdoni App</a>
    |
    <a href="https://github.com/vocdoni/vocdoni-node">Vocdoni Node</a>
    |
    <a href="https://github.com/vocdoni/vocdoni-integrator-sdk">Vocdoni Integrator SDK</a>
  </div>

# Vocdoni SaaS Backend
Vocdoni SaaS backend is a service that works on top of the [Vocdoni Protocol](https://www.vocdoni.io/), allowing to multiple users to act in name of a single organization in the [Vocdoni Chain](https://explorer.vote/). 

This service also allows to the SDK to user it as remote signer, which makes that the use of this service transparent to the [Vocdoni SDK](https://github.com/vocdoni/vocdoni-sdk).

## API documentation

The API itself is the source of truth. Its reference is [`docs/swagger.yaml`](./docs/swagger.yaml),
generated from the handlers' annotations (`make swagger`) and browsable as
[Swagger UI](https://vocdoni.github.io/saas-backend/) (built from the `stage` branch, so it can be
ahead of production). The integrator guides live in the
[developer docs](https://vocdoni.io/developers/docs), and the
[Vocdoni Integrator SDK](https://github.com/vocdoni/vocdoni-integrator-sdk) wraps the API for TypeScript;
both follow the API, so when either disagrees with it, the API is right and they need updating.

## Local Development

### Prerequisites

- [Docker](https://docs.docker.com/get-docker/) with [Compose V2](https://docs.docker.com/compose/)
- Git

### Setup

1. **Copy the environment file** and adjust variables as needed:

   ```bash
   cp example.env .env
   ```

   The `.env` file already has sensible defaults for local development. At minimum you'll need to set:
   - `VOCDONI_SECRET` — a random string for JWT signing
   - `VOCDONI_PRIVATEKEY` — a valid Vocdoni ecosystem private key

2. **Start the backend** (API + MongoDB + Mongo Express):

   ```bash
   docker compose up
   ```

   The API will be available at `http://localhost:${VOCDONI_PORT}` (default `8080`).

### Running with the UI

If you also want the [vocdoni-app](https://github.com/vocdoni/vocdoni-app) UI locally, activate the `with-ui` profile:

```bash
docker compose --profile with-ui up
```

This will:
1. Clone the vocdoni-app repo at build time (shallow clone inside the Docker image)
2. Start the UI dev server at **http://localhost:${UI_PORT:-3000}**
3. Set `SAAS_URL` to the API (`http://localhost:${VOCDONI_PORT}`)

To rebuild the UI image (e.g. after the upstream repo is updated):

```bash
docker compose --profile with-ui build --no-cache ui
```

### Running with a local fake SMTP server

If you want to test email flows locally without a real SMTP relay, activate the `local-smtp` profile:

```bash
docker compose --profile local-smtp up
```

The fake SMTP shares the API's network namespace (`network_mode: "service:api"`), so it listens on `0.0.0.0:${VOCDONI_SMTPPORT}` (default `1025`) and is reachable from the API at `localhost:${VOCDONI_SMTPPORT}`.

To wire them together, set in your `.env`:

```env
VOCDONI_SMTPSERVER=localhost
VOCDONI_SMTPPORT=1025
```

The fake SMTP server will log all received email messages to stdout.

### Running with a local Vocone (standalone Vocdoni chain)

If you want to run a fully local Vocdoni chain instead of relying on the remote API,
activate the `with-vocone` profile:

```bash
docker compose --profile with-vocone up
```

This builds and runs [vocone](https://github.com/vocdoni/vocdoni-node/tree/main/dockerfiles/vocone) from `../vocdoni-node`, a single-binary replacement for the Vocdoni protocol (gateway, chain, faucet, etc.).
The first build will take a few minutes (Go compilation).

> **Important:** When using this profile, change `VOCDONI_VOCDONIAPI` in your `.env`
> to `http://vocone:9090/v2` so the SaaS backend connects to the local instance.

Vocone is also compatible with the UI:

```bash
VOCDONI_VOCDONIAPI=http://vocone:9090/v2 \
  docker compose --profile with-vocone --profile with-ui up
```

### Creating the default plan

When running the SaaS backend locally for the first time, you need a default plan in MongoDB so organizations can be created:

```bash
docker compose --profile with-ui run --rm defaultplan
```

This connects to MongoDB, checks if a default plan exists, and creates one with generous limits (all features enabled, 100 users, 1000 processes, etc.) if none is found.

### Funding your account on vocone

When using vocone locally, the account derived from your `VOCDONI_PRIVATEKEY` needs tokens. A helper script derives the address and calls the faucet:

```bash
docker compose --profile with-vocone run --rm --build fundaccount
```

This builds the `fundaccount` target from `dev.dockerfile`, reads `VOCDONI_PRIVATEKEY` from your `.env`, derives the Ethereum address, and calls `GET /v2/open/claim/{address}` on the vocone service.

### Running with multiple profiles

Profiles can be combined:

```bash
docker compose --profile with-ui --profile local-smtp up
docker compose --profile with-vocone --profile local-smtp up
docker compose --profile with-vocone --profile with-ui --profile local-smtp up
```

### Running backend only (without any profile)

```bash
docker compose up
```

### Services

| Service              | URL                                             | Description                                      |
|----------------------|-------------------------------------------------|--------------------------------------------------|
| API                  | `http://localhost:${VOCDONI_PORT}`               | SaaS backend REST API                            
| Mongo Express        | `http://localhost:8081`                         | MongoDB admin UI                                 |
| UI (with-ui)         | `http://localhost:${UI_PORT:-3000}`             | Vocdoni App dev server (Vite)                    |
| Fake SMTP (local-smtp) | `smtp://localhost:${VOCDONI_SMTPPORT:-1025}`   | Local fake SMTP server for email testing         |
| Vocone (with-vocone) | `http://localhost:9090/v2`                      | Standalone Vocdoni chain (gateway + faucet)      |

### Useful commands

```bash
# Start backend only
docker compose up -d

# Start backend + UI
docker compose --profile with-ui up -d

# Start backend + fake SMTP
docker compose --profile local-smtp up -d

# Start backend + vocone
docker compose --profile with-vocone up -d

# Start everything (API + UI + fake SMTP + vocone)
docker compose --profile with-ui --profile local-smtp --profile with-vocone up

# One-shot: create default plan in MongoDB
docker compose --profile with-ui run --rm defaultplan

# One-shot: fund your account on vocone
docker compose --profile with-vocone run --rm --build fundaccount

# View logs (for a specific service)
docker compose logs -f api

# Rebuild the API after changes
docker compose build api

# Stop everything
docker compose down

# Stop and remove volumes (resets DB)
docker compose down -v
```


## CI

### Testing against an unmerged vocdoni-node

Comment on a pull request to run the test suite with `go.vocdoni.io/dvote` replaced by a
vocdoni-node commit (a `go.mod` replace, so the in-process vocone runs that code too):

```text
/integration vocdoni-node=<ref>
```

When to use it (a PR depending on an unmerged vocdoni-node change, a vocdoni-node PR that may break
this repo, before a dvote bump) is in [AGENTS.md](AGENTS.md#when-to-post-integration).

- A ref is a commit SHA (7-40 hex), a branch, a tag, or a pull request number of
  `vocdoni/vocdoni-node`. Write the PR number bare (`vocdoni-node=1234`), the recommended form.
  `vocdoni-node=#1234` also works, but GitHub autolinks `#1234` to this repo's issue. A branch or
  tag named only 1-6 digits cannot be pinned by name.
- Only users with write access count: comments from anyone else are ignored silently. A line that
  does not parse gets a "rejected" reply. A bare `/integration` runs the suite against the
  vocdoni-node version in `go.mod`.
- The run tests the PR's merge commit, as the `pull_request` check does when the run starts. A push
  after that is not picked up by that run.
- Each comment starts its own run; earlier ones are not cancelled. The result is posted back to the
  PR with the saas-backend merge, the vocdoni-node SHA and the run link. A failed run's reply includes
  the failing step's output.
- A ref that is a SHA runs once. A ref that is a branch, tag or PR number sticks: every push to the PR
  re-resolves it and runs the suite again, until `/integration reset`. Each such push run posts its
  result as a new PR comment, failure output included. A later sticky comment replaces
  the earlier one. A bare `/integration` is one-shot and does not change the sticky state.
- `/integration reset` clears the sticky state; it takes no refs.
- On a pull request with a sticky ref, the test job runs on every push. Without one, pull requests are
  tested only by the regular Main workflow.
- A push to the vocdoni-node PR does not trigger a run here: comment the same line again, or use
  "Run workflow" on the Actions tab.
- The job's timeout is 45 minutes.
- Comment runs use `.github/workflows/integration.yml` and the pin parser from `main`, so changes to
  them take effect only after they are merged. Push-run reports run `scripts/integration-report.sh`
  from the PR's base branch, so PR code never gets the comment-writing token.

The "Run workflow" form on the Actions tab takes `vocdoni_node_ref`, with the same forms as above.
Leave it empty for the `go.mod` version. The branch selected in "Use workflow from" is the
saas-backend code under test.

To run locally against a vocdoni-node ref:

```bash
scripts/integration-pin-node.sh <ref> /tmp/vocdoni-node
go test -run TestName ./api/
# when done, restore the files the script rewrote:
git checkout go.mod go.sum
rm -rf /tmp/vocdoni-node
```

## CLI tools

Operational CLI tools shipped alongside the API server (`cmd/service`). All follow the
same Viper convention as the server: flags or `VOCDONI_*` env vars.

### [cmd/userdel](cmd/userdel/) — GDPR user erasure

**What it does.** Erases a registered user and their personal data from MongoDB to serve
right-to-erasure requests in a standardized way. The key logic is the org classification:
organizations where the user is the **sole admin are deleted entirely** (members, groups,
censuses, participants, processes and voting processes, bundles, CSP tokens, jobs,
invitations); organizations with other admins are kept and only the user's membership is
removed. The drafts of the organizations to delete go first, the way the API deletes a draft:
paid ones are credited back to the wallet that paid them and their payment records kept; no
Stripe secret is needed. A draft that cannot be deleted aborts the erasure with nothing else
erased. Wallets and their history are kept, and a torn-down organization's balance is printed
to be returned by hand. In kept orgs the user created, the creator email is deliberately retained — the org signing key is derived
from `secret+creator+nonce`, so redacting it would break the org's on-chain account. Those
orgs are flagged in the output for manual follow-up.

**How to run.**

```bash
go run ./cmd/userdel -email user@example.com -dryRun   # impact report only
go run ./cmd/userdel -email user@example.com           # report + "type 'yes'" prompt
go run ./cmd/userdel -id 42 -yes                       # skip confirmation
```

Mongo connection via `-mongoURL`/`-mongoDB` flags or `VOCDONI_MONGOURL`/`VOCDONI_MONGODB`
env vars. Drafts of the orgs to delete go first, each credited back the way `DELETE /processes/{id}`
does (to the wallet that paid it), and payment records are kept. No Stripe secret is needed: an
open top-up checkout is left to Stripe's own expiry, and paying it meanwhile only credits the
wallet. A draft that cannot be deleted (a publish holds it) aborts the run; rerun once it settles.
Wallets and their history are kept, and the balance of each torn-down organization's own wallet
is printed, to be returned by hand; a positive balance never blocks the erasure.

**Precautions.** This is the destructive one of the two. Always run `-dryRun` first and
read the impact report — a sole-admin org takes its entire voting history down with it.
Reserve `-yes` for scripted use. Watch for the `WARNING: creator email retained` lines in
the output; those need manual follow-up. Stripe customer data and database backups are
explicitly out of scope — handle those separately if the erasure request covers them.
