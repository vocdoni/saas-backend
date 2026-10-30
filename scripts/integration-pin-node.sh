#!/usr/bin/env bash
# Usage: [GH_TOKEN=...] scripts/integration-pin-node.sh <ref|#PR> <checkout-dir>
# Fetches that vocdoni-node commit into <checkout-dir> (absent or empty; kept, the replace points
# at it) and rewrites go.mod/go.sum to use it. Prints sha=<full sha>; GH_TOKEN avoids rate limits.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <ref|#PR> <checkout-dir>" >&2
  exit 1
fi
ref="$1"
checkout="$2"

# Ref grammar: the same rule is duplicated in scripts/integration-pins.sh. Keep both in sync.
if ! [[ "$ref" =~ ^#[0-9]{1,9}$ || "$ref" =~ ^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$ ]] || [[ "$ref" == *..* ]]; then
  echo "ERROR: invalid ref '$ref'" >&2
  exit 1
fi

if [ -e "$checkout" ] && [ -n "$(ls -A "$checkout" 2>/dev/null)" ]; then
  echo "ERROR: checkout dir '$checkout' exists and is not empty" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# Resolves the ref through the GitHub API, then shallow-fetches exactly that commit.
# A #N ref reads the PR's head SHA; a branch, tag or short/full SHA goes via commits.
auth=()
if [ -n "${GH_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer $GH_TOKEN")
fi
body="$tmp/resolve.txt"
if [[ "$ref" == \#* ]]; then
  command -v jq >/dev/null || {
    echo "ERROR: jq is required to resolve '$ref'" >&2
    exit 1
  }
  if curl -fsSL ${auth[@]+"${auth[@]}"} -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/vocdoni/vocdoni-node/pulls/${ref:1}" -o "$body"; then
    sha="$(jq -r '.head.sha // empty' "$body")"
  else
    sha=""
  fi
elif curl -fsSL ${auth[@]+"${auth[@]}"} -H 'Accept: application/vnd.github.sha' \
  "https://api.github.com/repos/vocdoni/vocdoni-node/commits/$ref" -o "$body"; then
  sha="$(cat "$body")"
else
  sha=""
fi
if ! [[ "$sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "ERROR: could not resolve '$ref' in vocdoni/vocdoni-node" >&2
  exit 1
fi

mkdir -p "$checkout"
checkout="$(cd "$checkout" && pwd)"
git -C "$checkout" init --quiet >&2
git -C "$checkout" fetch --quiet --depth 1 "https://github.com/vocdoni/vocdoni-node.git" "$sha" >&2
git -C "$checkout" checkout --quiet FETCH_HEAD >&2

if ! grep -Eq '^module[[:space:]]+go\.vocdoni\.io/dvote[[:space:]]*$' "$checkout/go.mod"; then
  echo "ERROR: $checkout/go.mod does not declare module go.vocdoni.io/dvote" >&2
  exit 1
fi

cd "$root"
go mod edit -replace "go.vocdoni.io/dvote=$checkout" >&2
go mod tidy >&2

printf 'sha=%s\n' "$sha"
