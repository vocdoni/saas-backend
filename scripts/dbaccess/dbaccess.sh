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
#   DB_CLIENT   mongosh (default) | compass|gui to hand the URI to MongoDB
#               Compass instead, keeping the grant open until you press enter.
#   DB_PASS     mongo password. Only needed for the compass client (Compass
#               has no password prompt of its own); prompted for if unset.
#   MONGO_URI   full mongosh connection URI. Overrides URI construction.
#               Placeholders {USER}, {PASS}, {HOST}, {DB} are substituted if
#               present; ":{PASS}" is dropped entirely when no password is set.
#               Example:
#                 MONGO_URI='mongodb+srv://{USER}:{PASS}@{HOST}/{DB}?tls=true&authSource=admin'
set -euo pipefail

need() { command -v "$1" >/dev/null || { echo "missing: $1" >&2; exit 1; }; }

# percent-encode for the URI userinfo field, keeping the RFC 3986 unreserved
# set. LC_ALL=C makes the loop walk bytes, so UTF-8 passwords encode correctly.
urlenc() {
  local LC_ALL=C s=$1 i c out=''
  for (( i=0; i<${#s}; i++ )); do
    c=${s:i:1}
    case $c in
      [A-Za-z0-9._~-]) out=$out$c ;;
      # & 0xFF: bash 3.2 sign-extends "'$c" for bytes >= 0x80.
      *) out=$out$(printf '%%%02X' "$(( $(printf '%d' "'$c") & 0xFF ))") ;;
    esac
  done
  printf '%s' "$out"
}
need doctl; need curl; need jq   # mongosh is checked in the client dispatch at the bottom

CLIENT=${DB_CLIENT:-mongosh}
# Embedding the password in the URI (argv, LaunchServices' opened-URL record,
# Compass's saved-connections store) is opt-in: only the compass client, and
# only when DB_EMBED_PASS is set. The mongosh client prompts for its own
# password, so it never needs — and must never see — one in the URI.
EMBED_PASS=0
case "$CLIENT" in compass|gui) [ -n "${DB_EMBED_PASS:-}" ] && EMBED_PASS=1 ;; esac

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

# Prompt for the password only when we are going to embed it (compass +
# DB_EMBED_PASS). A plain DB_CLIENT=compass run hands Compass a password-less
# URI and lets you type the credential into its connection form.
if [ "$EMBED_PASS" = 1 ] && [ -z "${DB_PASS:-}" ]; then
  # `|| true`: EOF (no tty, or Ctrl-D) makes read return 1, which set -e would
  # turn into a silent exit — let the emptiness check below report it instead.
  read -rsp "mongo password (embedded in the Compass URI): " DB_PASS || true
  echo >&2
  [ -n "$DB_PASS" ] || { echo "password is required when DB_EMBED_PASS is set" >&2; exit 1; }
fi

# doctl's tabular --format output is safe here: UUIDs, IPs, hostnames, cluster
# names, and rule types (ip_addr/app/tag/…) cannot contain whitespace.
DB=$(doctl databases list --format ID,Name --no-header |
     awk -v n="$DB_CLUSTER" '$2==n {print $1; found=1; exit} END {exit !found}') ||
  { echo "no such cluster: $DB_CLUSTER" >&2; exit 1; }
HOST=$(doctl databases connection "$DB" --format Host --no-header)

# URI defaults to the DO managed-Mongo shape. Override with $MONGO_URI to point
# at any provider, use a different scheme (mongodb:// with an explicit port),
# or add options like &replicaSet=... — {USER} {PASS} {HOST} {DB} are substituted.
URI_TEMPLATE=${MONGO_URI:-'mongodb+srv://{USER}:{PASS}@{HOST}/{DB}?tls=true&authSource=admin'}
# A custom $MONGO_URI predating {PASS} (or just written without it) would drop
# the credential silently and fail auth in Compass — splice the slot in.
case $URI_TEMPLATE in *'{PASS}'*) ;; *) URI_TEMPLATE=${URI_TEMPLATE/\{USER\}@/\{USER\}:\{PASS\}@} ;; esac
URI=${URI_TEMPLATE//\{USER\}/$DB_USER}
URI=${URI//\{HOST\}/$HOST}
URI=${URI//\{DB\}/$DB_NAME}
# Embed the password only on the opt-in compass path. Otherwise the whole
# ":{PASS}" segment goes, leaving exactly the URI this tool built before — so
# mongosh keeps prompting for the credential, and a default compass hand-off
# carries no password.
if [ "$EMBED_PASS" = 1 ]; then
  URI=${URI//\{PASS\}/$(urlenc "$DB_PASS")}
  URI_SAFE=${URI_TEMPLATE//\{USER\}/$DB_USER}
  URI_SAFE=${URI_SAFE//\{HOST\}/$HOST}
  URI_SAFE=${URI_SAFE//\{DB\}/$DB_NAME}
  URI_SAFE=${URI_SAFE//\{PASS\}/***}
  # A $MONGO_URI with no {PASS} *and* no "{USER}@" for the splice above to bite
  # on (a hardcoded username, or no userinfo at all) would drop the credential
  # silently — Compass would just fail auth with no clue why.
  if [ "$URI" = "${URI/$(urlenc "$DB_PASS")/}" ]; then
    echo "MONGO_URI has no {PASS} placeholder and no '{USER}@' to splice one into," >&2
    echo "so the password would be dropped. Add {PASS} explicitly, e.g." >&2
    echo "  MONGO_URI='mongodb+srv://{USER}:{PASS}@{HOST}/{DB}?tls=true&authSource=admin'" >&2
    exit 1
  fi
else
  URI=${URI//:\{PASS\}/}
  URI=${URI//\{PASS\}/}
  URI_SAFE=$URI
fi

# macOS `open` / Linux `xdg-open` — used only by DB_CLIENT=compass. Resolved
# here, after $URI exists, so the handler probe uses the URI's actual scheme
# (overridable via $MONGO_URI) rather than a hardcoded one. `command -v open`
# alone is a bad macOS test — Linux can have open -> xdg-open — so gate on the
# OS. On Linux xdg-open is used only once a handler for the scheme is actually
# registered: its fallback for an unknown scheme is the default *web browser*,
# which would put the URI in the URL bar, history, and possibly a search engine.
SCHEME=${URI%%://*}
OPENER=''
case "$(uname -s)" in
  Darwin) OPENER=$(command -v open || true) ;;
  *) [ -n "$(xdg-mime query default "x-scheme-handler/$SCHEME" 2>/dev/null)" ] &&
       OPENER=$(command -v xdg-open || true) ;;
esac

# uuids (one per line) of ip_addr rules matching $1. The type filter is
# load-bearing: without it a cleanup could match and delete an `app` rule.
# `firewalls list` has no --format flag, so the rules are read as JSON.
rule_uuids() {
  doctl databases firewalls list "$DB" -o json |
    jq -r --arg ip "$1" '.[] | select(.type == "ip_addr" and .value == $ip) | .uuid'
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
  # Case-insensitive pattern rather than ${ans,,}: macOS ships bash 3.2, where
  # the ,, lowercase expansion is a fatal "bad substitution".
  case "$ans" in [yY]|[yY][eE][sS]) ADOPT=1 ;; esac
  [ "$ADOPT" = 1 ] || { echo "leaving existing rule(s) untouched; not connecting" >&2; exit 1; }
fi

# Arm the trap BEFORE the append. cleanup is idempotent, so an append that
# half-succeeds still gets reaped; there is no window where a rule can leak.
# A failed list/remove is surfaced loudly — silence would invert the tool's
# core guarantee (revocation on exit).
GRANTED=0   # set once a rule is known to be live, so cleanup can tell an empty
            # match ("nothing to do") from a broken parse ("grant left standing")
cleanup() {
  local uuids one ok=1
  if ! uuids=$(rule_uuids "$IP"); then
    echo "WARNING: could not list firewall rules to verify $IP — run 'doctl databases firewalls list $DB'" >&2
    return 0
  fi
  if [ -z "$uuids" ]; then
    [ "$GRANTED" = 0 ] || echo "WARNING: no ip_addr rule matched $IP at cleanup — the grant may still be live; check 'doctl databases firewalls list $DB'" >&2
    return 0
  fi
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
GRANTED=1

# ponytail: doctl's append/remove are read-modify-write over the whole rule list.
# Two people running this in the same second can clobber each other's rule.
# Rare enough to ignore; serialize (or move grants ops-side) if it ever bites.
# ponytail: `open` returns immediately, so the compass session lasts until you say
# so. `open -W` would wait for *all* of Compass to quit — wrong scope, and it never
# returns when other connections are open. The EXIT trap still covers Ctrl-C,
# SIGTERM and terminal close while we sit on the read.
case "$CLIENT" in
  compass|gui)
    echo "connect Compass to:" >&2
    echo "  $URI_SAFE" >&2
    [ "$EMBED_PASS" = 1 ] || echo "  (type your password into Compass's connection form)" >&2
    # `open` exits nonzero when nothing handles the scheme (Compass not
    # installed). Fall back to pasting rather than letting set -e kill the run.
    if [ -z "$OPENER" ] || ! "$OPENER" "$URI"; then
      echo "no handler for $SCHEME — paste this into Compass:" >&2
      echo "  $URI" >&2
    fi
    # Hold the grant open until the user is done. With a tty, wait on a prompt.
    # Without one, `read` returns instantly and the EXIT trap would revoke the
    # rule the moment Compass launches — so block on a sleep loop instead (the
    # trap still reaps on Ctrl-C/SIGTERM/terminal close).
    if [ -t 0 ]; then
      # `|| true`: Ctrl-D (EOF) is a legitimate "done" here, but read returns 1
      # and set -e would turn a clean exit into a nonzero one.
      read -r -p "press enter when done to revoke access: " || true
    else
      echo "no tty; holding access until interrupted (Ctrl-C/SIGTERM)…" >&2
      while :; do sleep 3600; done
    fi
    ;;
  *)
    need mongosh
    mongosh "$URI"
    ;;
esac
