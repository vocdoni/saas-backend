#!/usr/bin/env bash
# Usage: gh api --paginate "repos/$REPO/issues/$PR/comments" | jq -s 'add // []' | REPO=owner/repo scripts/integration-sticky.sh
# Prints vocdoni_node_ref= from the comments, oldest first: a sticky one sets it, a reset clears
# it, one-shots leave it. Only commenters with write access (checked live) count.
set -euo pipefail

if [ -z "${REPO:-}" ]; then
  echo "usage: REPO=owner/repo $0 < comments.json" >&2
  exit 1
fi

pins="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/integration-pins.sh"

# author_association depends on the reader: private org members read as
# CONTRIBUTOR to the Actions token, and read-only members read as MEMBER.
# So ask for the permission of each login, once.
declare -A permission=()
allowed() {
  local login="$1" perm
  [ -n "$login" ] || return 1
  if [ -z "${permission[$login]+set}" ]; then
    if perm="$(gh api "repos/$REPO/collaborators/$login/permission" --jq .permission 2>/dev/null)"; then
      permission[$login]="$perm"
    else
      echo "::warning::could not check $login's permission; ignoring their /integration comments" >&2
      permission[$login]=""
    fi
  fi
  case "${permission[$login]}" in
    admin | maintain | write) ;;
    *) return 1 ;;
  esac
}

# Each comment is base64-encoded so newlines and quotes survive the shell loop.
encoded="$(jq -r '.[] | {login: (.user.login // ""), body: (.body // "")} | @base64')"

node_ref=""
while IFS= read -r b64; do
  [ -n "$b64" ] || continue
  item="$(printf '%s' "$b64" | base64 -d)"
  body="$(jq -r '.body' <<<"$item")"
  # Cheap check first: the lookup below runs only for /integration comments.
  first="${body%%$'\n'*}"
  [[ "$first" == /integration* ]] || continue
  allowed "$(jq -r '.login' <<<"$item")" || continue
  # Only the first line goes to the parser: a long body must not SIGPIPE the pipe.
  if ! parsed="$(printf '%s\n' "$first" | "$pins" 2>/dev/null)"; then
    continue
  fi
  if [ "$(sed -n 's/^reset=//p' <<<"$parsed")" = true ]; then
    node_ref=""
  elif [ "$(sed -n 's/^sticky=//p' <<<"$parsed")" = true ]; then
    node_ref="$(sed -n 's/^vocdoni_node_ref=//p' <<<"$parsed")"
  fi
done <<<"$encoded"

printf 'vocdoni_node_ref=%s\n' "$node_ref"
