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
  # Unlike `databases list`, `firewalls list` has no --format/--no-header, so
  # parse its default table (UUID ClusterUUID Type Value) and skip the header.
  # -o text is explicit: doctl reads its default output format from config.yaml,
  # and a user with `output: json` there would otherwise silently parse to empty.
  doctl databases firewalls list "$DB" -o text |
    awk -v ip="$1" 'NR>1 && $3=="ip_addr" && $4==ip {print $1}'
}

# 2. rule_uuids returns empty for an unknown IP.
u=$(rule_uuids "203.0.113.254")   # TEST-NET-3, RFC 5737 — never appears in a real rule
[[ -z "$u" ]] || fail "rule_uuids returned '$u' for an unknown IP"
ok "unknown IP -> empty"

# 3. rule_uuids returns empty when handed an existing app rule's value.
#    Proves the type=ip_addr filter rejects app rules (so cleanup can't
#    delete an app rule whose value happens to collide with an IP).
app_val=$(doctl databases firewalls list "$DB" -o text |
          awk 'NR>1 && $3=="app" {print $4; exit}')
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

# 5. A handler for the mongodb+srv scheme exists for DB_CLIENT=compass. Checking
#    for `open` would be vacuous — it ships with every macOS whether or not
#    Compass was ever installed, so it is green exactly where the GUI path fails.
if [[ $OSTYPE == darwin* ]]; then
  if [[ -d "/Applications/MongoDB Compass.app" ]]; then
    ok "Compass installed — DB_CLIENT=compass can hand off the URI"
  else
    echo "skip: Compass not installed — DB_CLIENT=compass will print the URI to paste"
  fi
elif [[ -n "$(xdg-mime query default x-scheme-handler/mongodb+srv 2>/dev/null)" ]]; then
  ok "mongodb+srv scheme handler registered"
else
  echo "skip: no mongodb+srv handler — DB_CLIENT=compass will print the URI to paste"
fi

# 6. urlenc, pulled out of dbaccess.sh itself rather than copied, so this tests
#    the real function instead of a duplicate that can drift away from it.
eval "$(sed -n '/^urlenc()/,/^}/p' "$(dirname "$0")/dbaccess.sh")"
[[ $(urlenc 'p@ss:w/rd?#&=+ ') == 'p%40ss%3Aw%2Frd%3F%23%26%3D%2B%20' ]] || fail "urlenc left URI metacharacters unescaped"
[[ $(urlenc 'contraseña')      == 'contrase%C3%B1a' ]] || fail "urlenc encoded code points, not UTF-8 bytes"
[[ $(urlenc 'aA0._~-')         == 'aA0._~-' ]]            || fail "urlenc mangled the RFC 3986 unreserved set"
ok "urlenc percent-encodes the userinfo field"

echo "all checks passed"
