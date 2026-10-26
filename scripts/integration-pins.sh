#!/usr/bin/env bash
# Usage: printf '%s' "$COMMENT_BODY" | scripts/integration-pins.sh
# Parses the first line into vocdoni_node_ref=, sticky= (a non-SHA ref) and reset= lines, or exits 1.
# A bare 1-6 digit ref is a PR (#N); leading '-' and '..' are refused, so git never reads an option or path.
set -euo pipefail

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

IFS= read -r line || true
line="${line%$'\r'}"

if ! [[ "$line" =~ ^/integration([[:space:]]|$) ]]; then
  fail "first line must start with '/integration' (got '$line')"
fi

tokens=()
read -ra tokens <<<"${line#/integration}" || true

if [ "${#tokens[@]}" -gt 0 ] && [ "${tokens[0]}" = reset ]; then
  [ "${#tokens[@]}" = 1 ] || fail "'reset' takes no other tokens"
  printf 'vocdoni_node_ref=\nsticky=false\nreset=true\n'
  exit 0
fi

node_ref=""
seen_node=0
sticky=false
for tok in ${tokens[@]+"${tokens[@]}"}; do
  if [[ "$tok" != *=* ]]; then
    fail "'$tok' is not key=ref"
  fi
  key="${tok%%=*}"
  ref="${tok#*=}"
  # A bare PR number means the same as #N: GitHub autolinks #N to this repo.
  if [[ "$ref" =~ ^[0-9]{1,6}$ ]]; then
    ref="#$ref"
  fi
  case "$key" in
    vocdoni-node)
      [ "$seen_node" = 0 ] || fail "vocdoni-node given twice"
      seen_node=1
      node_ref="$ref"
      ;;
    *)
      fail "unknown key '$key' (expected vocdoni-node)"
      ;;
  esac
  [ -n "$ref" ] || fail "empty ref for '$key'"
  # Ref grammar: the same rule is duplicated in scripts/integration-pin-node.sh.
  # Keep both in sync.
  if [[ "$ref" =~ ^#[0-9]{1,9}$ ]]; then
    sticky=true
  elif [[ "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$ ]] && [[ "$ref" != *..* ]]; then
    if ! [[ "$ref" =~ ^[0-9A-Fa-f]{7,40}$ ]]; then
      sticky=true
    fi
  else
    fail "invalid ref '$ref' for '$key'"
  fi
done

printf 'vocdoni_node_ref=%s\nsticky=%s\nreset=false\n' "$node_ref" "$sticky"
