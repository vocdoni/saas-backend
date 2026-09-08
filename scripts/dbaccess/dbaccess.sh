#!/usr/bin/env bash
# dbaccess — open a DigitalOcean managed DB to your current public IP for one
# session; revoke on exit. No bastion, no VPN, no standing firewall rule.
#
# Usage:
#   ./dbaccess.sh [mongo-database]
#
# Configuration (env vars OR interactive prompt if unset):
#   DB_CLUSTER  cluster name (as shown by `doctl databases list`)
#   DB_USER     mongo user
#   DB_NAME     mongo database (overrides the positional arg)
#   MONGO_URI   full mongosh connection URI. Overrides URI construction.
#               Placeholders {USER}, {HOST}, {DB} are substituted if present.
#               Example:
#                 MONGO_URI='mongodb+srv://{USER}@{HOST}/{DB}?tls=true&authSource=admin'
set -euo pipefail

need() { command -v "$1" >/dev/null || { echo "missing: $1" >&2; exit 1; }; }
need doctl; need mongosh; need curl

prompt() {  # prompt VAR "label" ["default"]
  local var=$1 label=$2 default=${3:-} val
  if [ -z "${!var:-}" ]; then
    if [ -n "$default" ]; then
      read -r -p "$label [$default]: " val
      printf -v "$var" '%s' "${val:-$default}"
    else
      read -r -p "$label: " val
      [ -n "$val" ] || { echo "$label is required" >&2; exit 1; }
      printf -v "$var" '%s' "$val"
    fi
  fi
}

prompt DB_CLUSTER "cluster name"
prompt DB_USER    "mongo user"
DB_NAME=${DB_NAME:-${1:-}}
prompt DB_NAME    "mongo database"

# doctl's tabular --format output is safe here: UUIDs, IPs, hostnames, cluster
# names, and rule types (ip_addr/app/tag/…) cannot contain whitespace.
DB=$(doctl databases list --format ID,Name --no-header |
     awk -v n="$DB_CLUSTER" '$2==n {print $1; found=1; exit} END {exit !found}') ||
  { echo "no such cluster: $DB_CLUSTER" >&2; exit 1; }
HOST=$(doctl databases connection "$DB" --format Host --no-header)

# URI defaults to the DO managed-Mongo shape. Override with $MONGO_URI to point
# at any provider, use a different scheme (mongodb:// with an explicit port),
# or add options like &replicaSet=... — {USER} {HOST} {DB} are substituted.
URI_TEMPLATE=${MONGO_URI:-'mongodb+srv://{USER}@{HOST}/{DB}?tls=true&authSource=admin'}
URI=${URI_TEMPLATE//\{USER\}/$DB_USER}
URI=${URI//\{HOST\}/$HOST}
URI=${URI//\{DB\}/$DB_NAME}

# uuids (one per line) of ip_addr rules matching $1. The type filter is
# load-bearing: without it a cleanup could match and delete an `app` rule.
rule_uuids() {
  doctl databases firewalls list "$DB" --format UUID,Type,Value --no-header |
    awk -v ip="$1" '$2=="ip_addr" && $3==ip {print $1}'
}

IP=$(curl -fsS https://ipv4.icanhazip.com | tr -d '[:space:]')
[[ $IP =~ ^[0-9]+(\.[0-9]+){3}$ ]] || { echo "not an IPv4 address: '$IP'" >&2; exit 1; }

# If a rule for our IP already exists, ask before touching it. Otherwise a
# standing office/monitoring rule (or a teammate on the same NAT'd IP) could
# be silently revoked on exit. Adopting also drives the stale-rule reap flow.
ADOPT=0
existing=$(rule_uuids "$IP" || true)
if [ -n "$existing" ]; then
  echo "an ip_addr rule for $IP already exists on $DB_CLUSTER:" >&2
  # shellcheck disable=SC2086
  printf '  %s\n' $existing >&2
  read -r -p "adopt it and revoke on exit? [y/N]: " ans
  case "${ans,,}" in y|yes) ADOPT=1 ;; esac
  [ "$ADOPT" = 1 ] || { echo "leaving existing rule(s) untouched; not connecting" >&2; exit 1; }
fi

# Arm the trap BEFORE the append. cleanup is idempotent, so an append that
# half-succeeds still gets reaped; there is no window where a rule can leak.
# A failed list/remove is surfaced loudly — silence would invert the tool's
# core guarantee (revocation on exit).
cleanup() {
  local uuids one ok=1
  if ! uuids=$(rule_uuids "$IP"); then
    echo "WARNING: could not list firewall rules to verify $IP — run 'doctl databases firewalls list $DB'" >&2
    return 0
  fi
  [ -n "$uuids" ] || return 0
  for one in $uuids; do
    if doctl databases firewalls remove "$DB" --uuid "$one" >/dev/null; then
      echo "revoked $IP ($one)" >&2
    else
      echo "WARNING: could not revoke $IP (uuid $one) — remove it manually with 'doctl databases firewalls remove $DB --uuid $one'" >&2
      ok=0
    fi
  done
  [ "$ok" = 1 ] || return 0  # don't propagate cleanup failure as script exit code
}
trap cleanup EXIT

if [ "$ADOPT" = 1 ]; then
  echo "adopted existing rule for $IP -> $DB_CLUSTER/$DB_NAME" >&2
else
  doctl databases firewalls append "$DB" --rule "ip_addr:$IP" >/dev/null
  echo "granted $IP -> $DB_CLUSTER/$DB_NAME" >&2
fi

# ponytail: doctl's append/remove are read-modify-write over the whole rule list.
# Two people running this in the same second can clobber each other's rule.
# Rare enough to ignore; serialize (or move grants ops-side) if it ever bites.
mongosh "$URI"
