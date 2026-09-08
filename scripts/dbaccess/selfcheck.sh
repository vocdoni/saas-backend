#!/usr/bin/env bash
# selfcheck — non-destructive assertions against the live rule list.
# Reads only; makes no append/remove calls.
set -euo pipefail

CLUSTER=${DB_CLUSTER:-}
[ -n "$CLUSTER" ] || { echo "DB_CLUSTER is required (cluster name from \`doctl databases list\`)" >&2; exit 1; }

need() { command -v "$1" >/dev/null || { echo "missing: $1" >&2; exit 1; }; }
need doctl

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "ok:   $*"; }

# 1. Cluster name resolution returns a UUID and a hostname.
DB=$(doctl databases list --format ID,Name --no-header |
     awk -v n="$CLUSTER" '$2==n {print $1; found=1; exit} END {exit !found}') ||
  fail "no such cluster: $CLUSTER"
HOST=$(doctl databases connection "$DB" --format Host --no-header)
[[ -n "$DB"   ]] || fail "empty cluster uuid"
[[ -n "$HOST" ]] || fail "empty cluster host"
ok "cluster $CLUSTER resolves to $DB / $HOST"

# rule_uuids: same shape as dbaccess.sh, duplicated to keep selfcheck standalone.
rule_uuids() {
  doctl databases firewalls list "$DB" --format UUID,Type,Value --no-header |
    awk -v ip="$1" '$2=="ip_addr" && $3==ip {print $1}'
}

# 2. rule_uuids returns empty for an unknown IP.
u=$(rule_uuids "203.0.113.254")   # TEST-NET-3, RFC 5737 — never appears in a real rule
[[ -z "$u" ]] || fail "rule_uuids returned '$u' for an unknown IP"
ok "unknown IP -> empty"

# 3. rule_uuids returns empty when handed an existing app rule's value.
#    Proves the type=ip_addr filter rejects app rules (so cleanup can't
#    delete an app rule whose value happens to collide with an IP).
app_val=$(doctl databases firewalls list "$DB" --format Type,Value --no-header |
          awk '$1=="app" {print $2; exit}')
if [[ -n "$app_val" ]]; then
  u=$(rule_uuids "$app_val")
  [[ -z "$u" ]] || fail "rule_uuids matched app rule value '$app_val' -> $u"
  ok "app rule value ignored by ip_addr filter"
else
  echo "skip: no app rules on $CLUSTER (nothing to prove type filter against)"
fi

# 4. IPv4 regex accepts a valid v4 and rejects garbage.
check_ipv4() { [[ $1 =~ ^[0-9]+(\.[0-9]+){3}$ ]]; }
check_ipv4 "1.2.3.4"                 || fail "regex rejected 1.2.3.4"
check_ipv4 ""                        && fail "regex accepted empty string"
check_ipv4 "1.2.3"                   && fail "regex accepted 1.2.3"
check_ipv4 "<html>error</html>"      && fail "regex accepted html error"
ok "ipv4 regex boundaries"

echo "all checks passed"
