#!/usr/bin/env bash
# Database-recovery drill / user-testing environment.
# Downloads the latest production MongoDB backup from DigitalOcean Spaces, runs
# saas-backend (origin/lts) + vocdoni-app (origin/main) against the vochain LTS
# gateway, restores the backup into the local DB, and exposes the app on the LAN.
# See docs/db-recovery.md for the manual runbook.
#
# Required env:
#   SPACES_SECRET_KEY        secret of the Spaces key (SPACES_ACCESS_KEY defaults
#                            to DO801E6DFFEDLZZF2RYP, the key-wr-mongo-backup id)
#   BACKUP_PASSPHRASE        gpg passphrase of the backups (or BACKUP_PASSPHRASE_FILE)
#   VOCDONI_PRIVATEKEY       funded LTS vochain account key (64 hex chars)
# Optional env:
#   RECOVERY_SECRET          API secret (default: random; restored orgs then cannot
#                            sign on-chain — reads and login work)
#   DR_HOST                  host testers use (default: autodetected LAN IP)
#   API_PORT                 API port (default: 8080 — override on conflicts)
#   BACKUP_KEY               pin a specific archive key instead of the latest
#   SKIP_UI=1                backend-only
# Flags:
#   --logs                   follow the API's debug logs (lts always logs at debug level)
#   --down                   tear everything down (containers, UI, worktrees, artifacts)
set -euo pipefail

BUCKET="mongo-backup-lts"
ENDPOINT="https://fra1.digitaloceanspaces.com"
PROD_DB="saas-lts"
PROJECT="saas-dr"
API_PORT="${API_PORT:-8080}"
UI_PORT=3000   # vike's dev server default; not configurable from here

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)"
BACKEND_GIT="$(git -C "$SCRIPT_DIR" rev-parse --path-format=absolute --git-common-dir | xargs dirname)"
WORKSPACE="$(dirname "$BACKEND_GIT")"
LTS_DIR="$WORKSPACE/saas-backend-worktrees/dr-lts"
APP_GIT="$WORKSPACE/vocdoni-app"
APP_DIR="$WORKSPACE/vocdoni-app-worktrees/dr-main"
WORKDIR="$REPO_ROOT/.dbrecovery"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }
RESULTS=()
pass() { RESULTS+=("PASS  $1"); printf '\033[1;32mPASS\033[0m %s\n' "$1"; }

compose() {
  docker compose -p "$PROJECT" -f "$LTS_DIR/docker-compose.yml" "$@"
}

# --- logs --------------------------------------------------------------------
if [[ "${1:-}" == "--logs" ]]; then
  exec docker compose -p "$PROJECT" -f "$LTS_DIR/docker-compose.yml" logs -f api
fi

# --- teardown ----------------------------------------------------------------
if [[ "${1:-}" == "--down" ]]; then
  [[ -f "$WORKDIR/ui.pid" ]] && kill "$(cat "$WORKDIR/ui.pid")" 2>/dev/null || true
  # -v: mongo's data lives in an anonymous volume (the compose file mounts the
  # named volume at /data/mongodb, not mongo:7's /data/db), so without -v the
  # restored production DB would survive teardown and pile up across runs.
  [[ -d "$LTS_DIR" ]] && compose down -v --remove-orphans 2>/dev/null || true
  [[ -d "$LTS_DIR" ]] && git -C "$BACKEND_GIT" worktree remove --force "$LTS_DIR"
  [[ -d "$APP_DIR" ]] && git -C "$APP_GIT" worktree remove --force "$APP_DIR"
  rm -rf "$WORKDIR"
  log "stack down; worktrees and artifacts removed"
  exit 0
fi

# --- prerequisites -----------------------------------------------------------
log "checking prerequisites"
for bin in docker aws gpg curl mongorestore git openssl; do
  command -v "$bin" >/dev/null || fail "missing required tool: $bin"
done
[[ -z "${SKIP_UI:-}" ]] && { command -v pnpm >/dev/null || fail "missing pnpm (or set SKIP_UI=1)"; }

[[ -n "${SPACES_SECRET_KEY:-}" ]] || fail "SPACES_SECRET_KEY is required (secret of the Spaces key)"
SPACES_ACCESS_KEY="${SPACES_ACCESS_KEY:-DO801E6DFFEDLZZF2RYP}"
export AWS_ACCESS_KEY_ID="$SPACES_ACCESS_KEY" AWS_SECRET_ACCESS_KEY="$SPACES_SECRET_KEY" \
  AWS_DEFAULT_REGION=fra1

[[ -n "${VOCDONI_PRIVATEKEY:-}" ]] || fail "VOCDONI_PRIVATEKEY is required (funded LTS vochain account)"
[[ "$VOCDONI_PRIVATEKEY" =~ ^(0x)?[0-9a-fA-F]{64}$ ]] || fail "VOCDONI_PRIVATEKEY is not a 64-hex key"

if [[ -n "${BACKUP_PASSPHRASE_FILE:-}" ]]; then
  [[ -r "$BACKUP_PASSPHRASE_FILE" ]] || fail "BACKUP_PASSPHRASE_FILE not readable"
  PASSFILE="$BACKUP_PASSPHRASE_FILE"
elif [[ -n "${BACKUP_PASSPHRASE:-}" ]]; then
  PASSFILE="$(mktemp)"
  chmod 600 "$PASSFILE"
  printf '%s' "$BACKUP_PASSPHRASE" > "$PASSFILE"
  trap 'rm -f "$PASSFILE"' EXIT
else
  fail "BACKUP_PASSPHRASE or BACKUP_PASSPHRASE_FILE is required"
fi

# bash /dev/tcp rather than nc (not a guaranteed tool): a successful connect
# means the port is already bound. UI_PORT matters most — vite isn't strictPort,
# so a silent bind elsewhere would leave the script advertising the wrong port.
for port in "$API_PORT" 27017 "$UI_PORT"; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
    exec 3>&- 3<&- 2>/dev/null || true
    fail "port $port is already in use — stop the regular dev stack first (or $0 --down)"
  fi
done

if [[ -z "${DR_HOST:-}" ]]; then
  DR_HOST="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || hostname -I 2>/dev/null | awk '{print $1}' || true)"
  DR_HOST="${DR_HOST:-localhost}"
fi
mkdir -p "$WORKDIR"
pass "prerequisites (DR_HOST=$DR_HOST)"

# --- checkouts ---------------------------------------------------------------
# prepare_worktree <repo> <dir> <branch>: disposable detached worktree synced to
# origin/<branch>; a stale/broken dir (e.g. missing .git link) is recreated.
prepare_worktree() {
  git -C "$1" fetch -q origin "$3"
  if [[ -d "$2" ]] && git -C "$2" rev-parse --git-dir >/dev/null 2>&1; then
    git -C "$2" reset --hard -q "origin/$3"
  else
    rm -rf "$2"
    git -C "$1" worktree prune
    git -C "$1" worktree add -q --detach "$2" "origin/$3"
  fi
}

log "preparing saas-backend @ origin/lts worktree"
prepare_worktree "$BACKEND_GIT" "$LTS_DIR" lts
# The drill loads the production dump; the base compose publishes mongo on all
# interfaces (root/vocdoni), so rebind it to loopback — testers only need the
# API and UI. The checkout is disposable (reset --hard each run), so this edit
# is transient. Host mongorestore still reaches it via localhost.
sed -i.bak 's|- 27017:27017|- 127.0.0.1:27017:27017|' "$LTS_DIR/docker-compose.yml" &&
  rm -f "$LTS_DIR/docker-compose.yml.bak"
# Generate the full recovery .env (gitignored). The base compose hardcodes
# VOCDONI_MONGOURL=.../saasdb in `environment:`, but VOCDONI_MONGODB set here
# wins over the URL's database in db.New — no compose override file needed.
RECOVERY_SECRET="${RECOVERY_SECRET:-$(openssl rand -hex 32)}"
umask 077
cat > "$LTS_DIR/.env" <<EOF
# generated by scripts/test-db-recovery.sh — DO NOT COMMIT
VOCDONI_MONGODB=$PROD_DB
VOCDONI_VOCDONIAPI=https://api.vocdoni.net/v2
# compose interpolates \${VOCDONI_PORT} from this file: sets both the published
# port and the port the service listens on
VOCDONI_PORT=$API_PORT
VOCDONI_SERVERURL=http://$DR_HOST:$API_PORT
VOCDONI_WEBURL=http://$DR_HOST:${UI_PORT}
VOCDONI_PRIVATEKEY=${VOCDONI_PRIVATEKEY#0x}
VOCDONI_SECRET=$RECOVERY_SECRET
# non-empty dummies are enough to boot; the restored plans collection serves reads
VOCDONI_STRIPEAPISECRET=sk_test_dummy
VOCDONI_STRIPEWEBHOOKSECRET=whsec_dummy
# blank: no real mail/SMS can be sent from the drill
VOCDONI_SMTPSERVER=
VOCDONI_TWILIOACCOUNTSID=
VOCDONI_TWILIOAUTHTOKEN=
VOCDONI_TWILIOFROMNUMBER=
EOF
umask 022
pass "backend checkout: $(git -C "$LTS_DIR" log -1 --format='%h %s')"

if [[ -z "${SKIP_UI:-}" ]]; then
  log "preparing vocdoni-app @ origin/main worktree"
  prepare_worktree "$APP_GIT" "$APP_DIR" main
  node_major="$(node -v | sed 's/^v//;s/\..*//')"
  [[ "$node_major" == 22 ]] || log "warning: node v$node_major (app pins 22.x) — proceeding"
  pass "frontend checkout: $(git -C "$APP_DIR" log -1 --format='%h %s')"
fi

# --- locate + download backup ------------------------------------------------
latest_key() {
  # `|| true`: an empty listing prints `None` (aws --output text), which the grep
  # filters out and exits 1; under pipefail that would otherwise kill the caller's
  # KEY="$(latest_key …)" via set -e before the previous-month fallback runs.
  aws --endpoint-url "$ENDPOINT" s3api list-objects-v2 --bucket "$BUCKET" \
    --prefix "backups/$1/" --query 'Contents[].Key' --output text 2>/dev/null |
    tr '\t' '\n' | grep '\.archive\.gz\.gpg$' | sort | tail -1 || true
}
if [[ -n "${BACKUP_KEY:-}" ]]; then
  KEY="$BACKUP_KEY"
  log "using pinned backup: $KEY"
else
  log "locating latest backup in $BUCKET"
  KEY="$(latest_key "$(date -u +%Y/%m)")"
  [[ -n "$KEY" ]] || KEY="$(latest_key "$(date -u -v-1m +%Y/%m 2>/dev/null || date -u -d 'last month' +%Y/%m)")"
  [[ -n "$KEY" ]] || fail "no backup found in current or previous month"
fi
SIZE="$(aws --endpoint-url "$ENDPOINT" s3api head-object --bucket "$BUCKET" --key "$KEY" \
  --query ContentLength --output text)"
# ponytail: 100KB floor; a healthy prod dump is tens of MB, a broken one ~10KB
[[ "$SIZE" -ge 100000 ]] || fail "backup $KEY is only $SIZE bytes — not a production dump (see docs/db-recovery.md incident note); pin a good one with BACKUP_KEY"
ARCHIVE="$WORKDIR/$(basename "$KEY")"
pass "backup selected: $KEY ($((SIZE / 1024 / 1024)) MB)"

verify_checksum() {
  ( cd "$WORKDIR" && { shasum -a 256 -c "$(basename "$ARCHIVE").sha256" 2>/dev/null || \
                       sha256sum -c "$(basename "$ARCHIVE").sha256" 2>/dev/null; } ) >/dev/null
}
if [[ -f "$ARCHIVE" && -f "$ARCHIVE.sha256" ]] && verify_checksum; then
  log "archive already downloaded and checksum-valid, skipping download"
else
  log "downloading archive + sha256 sidecar"
  aws --endpoint-url "$ENDPOINT" s3 cp "s3://$BUCKET/$KEY" "$ARCHIVE"
  aws --endpoint-url "$ENDPOINT" s3 cp "s3://$BUCKET/$KEY.sha256" "$ARCHIVE.sha256"
  verify_checksum || fail "checksum mismatch"
fi
pass "checksum verified"

# --- mongo up + restore ------------------------------------------------------
log "starting mongo"
compose up -d mongo
for _ in $(seq 1 30); do
  compose exec -T mongo mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null 2>&1 && break
  sleep 2
done
compose exec -T mongo mongosh --quiet --eval 'db.runCommand({ping:1}).ok' >/dev/null ||
  fail "mongo did not become ready"
pass "mongo ready"

log "decrypting and restoring database '$PROD_DB' (drop + restore)"
gpg --batch --yes --pinentry-mode loopback --lock-never \
  --passphrase-file "$PASSFILE" --decrypt "$ARCHIVE" |
  mongorestore --uri "mongodb://root:vocdoni@localhost:27017/?authSource=admin" \
    --archive --gzip --drop --nsInclude="${PROD_DB}.*" --quiet ||
  fail "restore failed (wrong passphrase?)"
pass "restore completed"

mongo_count() {
  compose exec -T mongo mongosh --quiet -u root -p vocdoni \
    --authenticationDatabase admin "$PROD_DB" --eval "db.getCollection('$1').countDocuments()"
}
USERS="$(mongo_count users)"; ORGS="$(mongo_count organizations)"; PROCS="$(mongo_count processes)"
log "restored counts: users=$USERS organizations=$ORGS processes=$PROCS"
[[ "$USERS" -gt 0 && "$ORGS" -gt 0 ]] || fail "restored collections are empty"
pass "restored data present"

# --- api ---------------------------------------------------------------------
log "building and starting api from origin/lts (first build can take a while)"
compose up -d --build api
for _ in $(seq 1 60); do
  curl -sf "http://localhost:$API_PORT/ping" >/dev/null 2>&1 && break
  sleep 2
done
curl -sf "http://localhost:$API_PORT/ping" >/dev/null || {
  compose logs --tail 50 api >&2
  fail "api /ping not responding"
}
pass "api /ping"

PLANS="$(curl -sf "http://localhost:$API_PORT/plans")" && [[ "$PLANS" == *'"name"'* ]] ||
  fail "GET /plans did not return restored plans"
pass "api serves restored data (GET /plans)"

# dump to a file first, then grep: piping into `grep -q` closes the pipe on the
# first match, docker compose logs dies with SIGPIPE, and under pipefail the
# pipeline is non-zero even though grep matched — which would skip the fail.
compose logs api >"$WORKDIR/api.log" 2>/dev/null || true
if grep -qiE 'migration.*(fail|error)' "$WORKDIR/api.log"; then
  fail "migration errors in api logs (docker compose -p $PROJECT logs api)"
fi
pass "no migration errors in api logs"

# --- frontend ----------------------------------------------------------------
if [[ -z "${SKIP_UI:-}" ]]; then
  log "starting vocdoni-app (pnpm install on first run)"
  [[ -d "$APP_DIR/node_modules" ]] || (cd "$APP_DIR" && pnpm install)
  ( cd "$APP_DIR" && SAAS_URL="http://$DR_HOST:$API_PORT" VOCDONI_ENVIRONMENT=prod \
      nohup pnpm dev >"$WORKDIR/ui.log" 2>&1 & echo $! >"$WORKDIR/ui.pid" )
  for _ in $(seq 1 60); do
    curl -sf "http://localhost:${UI_PORT}" >/dev/null 2>&1 && break
    sleep 2
  done
  curl -sf "http://localhost:${UI_PORT}" >/dev/null || fail "frontend not responding (see $WORKDIR/ui.log)"
  pass "frontend up"
fi

# --- summary -----------------------------------------------------------------
echo
log "recovery environment ready (backup: $KEY)"
printf '  %s\n' "${RESULTS[@]}"
echo
echo "User testing:   http://$DR_HOST:${UI_PORT}   (share with testers on your network)"
echo "API:            http://$DR_HOST:$API_PORT"
echo
echo "API debug logs (the lts branch always logs at debug level):"
echo "  docker compose -p $PROJECT -f $LTS_DIR/docker-compose.yml logs -f api"
echo "  (or: $0 --logs)"
echo "UI logs:        tail -f $WORKDIR/ui.log"
echo
echo "Testers can log in with real production accounts (restored password hashes)."
echo "Limitations: on-chain signing for restored orgs fails (random secret),"
echo "Stripe checkout disabled, no emails/SMS."
echo
echo "Teardown: $0 --down"
