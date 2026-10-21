#!/usr/bin/env bash
# Download the latest production MongoDB backup from DigitalOcean Spaces and
# restore it into a given MongoDB URL. No docker, no app — just download + restore.
# For the full local-stack drill, see scripts/test-db-recovery.sh.
#
# Usage:
#   scripts/restore-latest-backup.sh <mongodb-uri>
#
# Required env:
#   SPACES_SECRET_KEY   secret of the Spaces key (SPACES_ACCESS_KEY defaults to
#                       DO801E6DFFEDLZZF2RYP, the key-wr-mongo-backup id)
#   BACKUP_PASSPHRASE   gpg passphrase of the backups (or BACKUP_PASSPHRASE_FILE)
# Optional env:
#   BACKUP_KEY          pin a specific archive key instead of the latest
#   WORKDIR             download directory (default: ./.dbrecovery)
#
# The backup always carries saas-lts.* namespaces; this restores into `saas-lts`
# on the target server and drops existing collections first (clean restore).
set -euo pipefail

BUCKET="mongo-backup-lts"
ENDPOINT="https://fra1.digitaloceanspaces.com"
PROD_DB="saas-lts"
WORKDIR="${WORKDIR:-$PWD/.dbrecovery}"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL\033[0m %s\n' "$*" >&2; exit 1; }

TARGET_URI="${1:-}"
[[ -n "$TARGET_URI" ]] || fail "usage: $0 <mongodb-uri>"

# --- prerequisites -----------------------------------------------------------
for bin in aws gpg mongorestore; do
  command -v "$bin" >/dev/null || fail "missing required tool: $bin"
done

[[ -n "${SPACES_SECRET_KEY:-}" ]] || fail "SPACES_SECRET_KEY is required (secret of the Spaces key)"
export AWS_ACCESS_KEY_ID="${SPACES_ACCESS_KEY:-DO801E6DFFEDLZZF2RYP}" \
  AWS_SECRET_ACCESS_KEY="$SPACES_SECRET_KEY" AWS_DEFAULT_REGION=fra1

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

mkdir -p "$WORKDIR"

# --- locate latest backup ----------------------------------------------------
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
[[ "$SIZE" -ge 100000 ]] || fail "backup $KEY is only $SIZE bytes — not a production dump; pin a good one with BACKUP_KEY"
ARCHIVE="$WORKDIR/$(basename "$KEY")"
log "backup selected: $KEY ($((SIZE / 1024 / 1024)) MB)"

# --- download + checksum -----------------------------------------------------
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
log "checksum verified"

# --- restore -----------------------------------------------------------------
log "restoring database '$PROD_DB' into the target (drop + restore)"
gpg --batch --yes --pinentry-mode loopback --lock-never \
  --passphrase-file "$PASSFILE" --decrypt "$ARCHIVE" |
  mongorestore --uri "$TARGET_URI" --archive --gzip --drop --nsInclude="${PROD_DB}.*" ||
  fail "restore failed (wrong passphrase, or target unreachable?)"

log "done — restored $KEY into '$PROD_DB' on the target MongoDB"
