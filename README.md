<p align="center" width="100%">
    <img src="https://developer.vocdoni.io/img/vocdoni_logotype_full_white.svg" />
</p>

<p align="center" width="100%">
    <a href="https://github.com/vocdoni/saas-backend/commits/main/"><img src="https://img.shields.io/github/commit-activity/m/vocdoni/saas-backend" /></a>
    <a href="https://github.com/vocdoni/saas-backend/issues"><img src="https://img.shields.io/github/issues/vocdoni/saas-backend" /></a>
    <a href="https://github.com/vocdoni/saas-backend/actions/workflows/main.yml/"><img src="https://github.com/vocdoni/saas-backend/actions/workflows/main.yml/badge.svg" /></a>
    <a href="https://pkg.go.dev/github.com/vocdoni/saas-backend"><img src="https://godoc.org/go.vocdoni.io/saas-backend?status.svg"></a>
    <a href="https://discord.gg/xFTh8Np2ga"><img src="https://img.shields.io/badge/discord-join%20chat-blue.svg" /></a>
    <a href="https://twitter.com/vocdoni"><img src="https://img.shields.io/twitter/follow/vocdoni.svg?style=social&label=Follow" /></a>
</p>


  <div align="center">
    Vocdoni is the first universally verifiable, censorship-resistant, anonymous, and self-sovereign governance protocol. <br />
    Our main aim is a trustless voting system where anyone can speak their voice and where everything is auditable. <br />
    We are engineering building blocks for a permissionless, private and censorship resistant democracy.
    <br />
    <a href="https://developer.vocdoni.io/"><strong>Explore the developer portal »</strong></a>
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
    <a href="https://chat.vocdoni.io">Contact Us</a>
    <br />
    <h3>Key Repositories</h3>
    <a href="https://github.com/vocdoni/vocdoni-node">Vocdoni Node</a>
    |
    <a href="https://github.com/vocdoni/vocdoni-sdk/">Vocdoni SDK</a>
    |
    <a href="https://github.com/vocdoni/ui-components">UI Components</a>
    |
    <a href="https://github.com/vocdoni/ui-scaffold">Application UI</a>
    |
    <a href="https://github.com/vocdoni/census3">Census3</a>
  </div>

# Vocdoni SaaS Backend
Vocdoni SaaS backend is a service that works on top of the [Vocdoni Protocol](https://www.vocdoni.io/), allowing to multiple users to act in name of a single organization in the [Vocdoni Chain](https://explorer.vote/). 

This service also allows to the SDK to user it as remote signer, which makes that the use of this service transparent to the [Vocdoni SDK](https://github.com/vocdoni/vocdoni-sdk).

Check out the service [API documentation](./api/docs.md) here.

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

