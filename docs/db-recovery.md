# Database Recovery Drill (local, LAN-exposed)

Restore the latest production MongoDB backup into a local stack — **saas-backend from
`origin/lts`** (the branch production deploys) + **vocdoni-app from `origin/main`** — pointed
at the **vochain LTS gateway** as production is, and expose the app on the LAN for user testing.

Automated version: `scripts/test-db-recovery.sh`. Teardown: `scripts/test-db-recovery.sh --down`.

## How production is wired (facts this drill reproduces)

| Thing | Value | Source |
|---|---|---|
| Backup bucket | `mongo-backup-lts` @ `https://fra1.digitaloceanspaces.com` | DO app `saas-backend-lts`, job `mongodump-s3` |
| Backup schedule | every 4 h (`15 */4 * * *` Europe/Madrid) | DO app spec |
| Backup format | `backups/YYYY/MM/mongo-<ts>.archive.gz.gpg` = gpg-AES256( gzip( `mongodump --archive` of **`saas-lts`** ) ), plus `.sha256` and `.metadata.json` sidecars | `vocdoni/mongodump-s3`, branch `mongo_backups` |
| Production DB name | `saas-lts` | DO app spec (`VOCDONI_MONGODB`) |
| Vochain LTS gateway | `https://api.vocdoni.net/v2` | DO app spec (`VOCDONI_VOCDONIAPI`) |
| Backend branch | `lts` | DO app spec |
| Frontend | branch `main`, `VOCDONI_ENVIRONMENT=prod`, `SAAS_URL=https://saas-api-lts.vocdoni.net` | vocdoni-app README / DO |

## Requirements

Tools: **Docker** (compose v2), **aws CLI**, **gpg**, **mongorestore**
(`brew install mongodb-database-tools`), **git**, **curl**, **openssl**; frontend: **pnpm**
(node 22 pinned; newer majors work with a warning). Free ports: 8080, 27017, 8081, 3000.

Secrets (the script requires all three and fails fast without them):

| Env var | What it is |
|---|---|
| `SPACES_SECRET_KEY` | secret of a Spaces key with read access to `mongo-backup-lts`. `SPACES_ACCESS_KEY` defaults to `DO801E6DFFEDLZZF2RYP` (`key-wr-mongo-backup`); or mint a fresh read-only pair: `doctl spaces keys create db-recovery --grants "bucket=mongo-backup-lts;permission=read" -o json` |
| `BACKUP_PASSPHRASE` | gpg symmetric passphrase of the backups (DO-encrypted secret — team password manager). `BACKUP_PASSPHRASE_FILE` also accepted |
| `VOCDONI_PRIVATEKEY` | a funded LTS vochain account key (64 hex). Used as the service account and CSP root key |

Optional: `RECOVERY_SECRET` (API secret; default random), `DR_HOST` (hostname/IP testers use;
default autodetected LAN IP), `API_PORT` (default 8080 — override when it conflicts with other
local services), `BACKUP_KEY` (pin an archive), `SKIP_UI=1` (backend only).

## What the script does

```sh
export SPACES_SECRET_KEY=… BACKUP_PASSPHRASE=… VOCDONI_PRIVATEKEY=…
scripts/test-db-recovery.sh
```

1. **Checkouts** — disposable detached worktrees, so your own checkouts are untouched:
   `saas-backend-worktrees/dr-lts` @ `origin/lts` and `vocdoni-app-worktrees/dr-main` @
   `origin/main` (re-synced to the remote head on every run).
2. **Download** — latest `backups/YYYY/MM/mongo-<ts>.archive.gz.gpg` (timestamps sort
   lexicographically), sha256-verified. Archives under 100 KB are refused (see incident note).
3. **Restore** — `docker compose -p saas-dr` brings up `mongo:7`, then
   `gpg --decrypt | mongorestore --archive --gzip --drop --nsInclude='saas-lts.*'`.
   Restore **merges** unless `--drop` — the script always drops. Counts are verified.
4. **API** — built from the `dr-lts` worktree with a **generated `.env`** (gitignored,
   0600): DB `saas-lts`, `VOCDONI_VOCDONIAPI=https://api.vocdoni.net/v2`, your private key,
   dummy Stripe, mail/SMS blanked. No compose override is needed: the base compose hardcodes
   `VOCDONI_MONGOURL=…/saasdb`, but `VOCDONI_MONGODB` from `.env` wins over the URL's database
   (`db/mongo.go`). Smoke: `/ping`, `GET /plans` (restored data), migration log scan
   (lts code = prod parity, so migrations are a no-op on a fresh backup).
5. **Frontend** — `pnpm dev` in `dr-main` with `SAAS_URL=http://<DR_HOST>:8080` and
   `VOCDONI_ENVIRONMENT=prod` (there is no `lts` value; `prod` *is* LTS for the frontend's
   explorer/archive links). Vite listens on all interfaces.
6. **Summary** — PASS/FAIL per phase and the tester URL.

### User testing

Share `http://<DR_HOST>:3000` with testers on your network (`DR_HOST` is printed; override it
if autodetection picks the wrong interface). Testers log in with **real production accounts**
— password hashes come from the restored dump, JWTs are issued locally.

Expected limitations:
- On-chain writes for **restored** orgs fail: org signers derive from `VOCDONI_SECRET`, which
  is random unless you export the production value as `RECOVERY_SECRET`. Newly created orgs
  sign fine (funded by your `VOCDONI_PRIVATEKEY`).
- Stripe checkout/billing endpoints are non-functional (dummy keys; restored `plans` serve reads).
- No emails or SMS are ever sent (SMTP/Twilio blanked).

### Teardown

```sh
scripts/test-db-recovery.sh --down
```

Stops the UI, removes the `saas-dr` compose project, deletes both `dr-*` worktrees and the
downloaded artifacts (`.dbrecovery/`).

## Safety notes

- Never set `VOCDONI_MONGO_RESET_DB`: **any** non-empty value (even `false`) drops all
  collections at API startup (`db/mongo.go`). The script regenerates `dr-lts/.env` on every
  run, so it can never linger there.
- The downloaded dump is production data — it stays inside the gitignored `.dbrecovery/`;
  `--down` deletes it. Treat the passphrase and dump accordingly.
- The compose project is isolated under `-p saas-dr` and the port pre-flight aborts if your
  regular dev stack is running.

## Manual restore (no script)

```sh
export AWS_ACCESS_KEY_ID=DO801E6DFFEDLZZF2RYP AWS_SECRET_ACCESS_KEY=… AWS_DEFAULT_REGION=fra1
EP=https://fra1.digitaloceanspaces.com
aws --endpoint-url $EP s3 ls "s3://mongo-backup-lts/backups/$(date -u +%Y/%m)/"   # pick newest
aws --endpoint-url $EP s3 cp "s3://mongo-backup-lts/<KEY>" .
aws --endpoint-url $EP s3 cp "s3://mongo-backup-lts/<KEY>.sha256" . && shasum -a 256 -c ./*.sha256

printf '%s' "$BACKUP_PASSPHRASE" > pp && chmod 600 pp
gpg --batch --yes --pinentry-mode loopback --lock-never --passphrase-file pp --decrypt mongo-<ts>.archive.gz.gpg \
  | mongorestore --uri "mongodb://root:vocdoni@localhost:27017/?authSource=admin" \
      --archive --gzip --drop --nsInclude='saas-lts.*'
```

## Incident history

> **Found 2026-09-24 while building this drill**: the backup job's `MONGO_URI` secret was
> scoped to **`saas-dev`**, not `saas-lts` — confirmed by capturing the live job logs (every
> dumped namespace was `saas-dev.*`). No archive before the fix contained production data; the
> ~1.6 MB pre-2026-09-22 archives were dev data, and the drop to ~10 KB on 09-22 was a
> `saas-dev` purge, not a backup failure.
>
> **Fixed 2026-09-24**: `MONGO_URI` repointed to `…/saas-lts` (path-scoped). First good
> archive: `backups/2026/09/mongo-20260924T145042Z.archive.gz.gpg` (74.7 MB, all namespaces
> `saas-lts.*`). **Archives older than that are dev data — do not use them for recovery.**
> The script's 100 KB size floor guards against regressions of this class.
