#!/usr/bin/env bash
# Usage: scripts/integration-failure.sh <step name> <log file>, printing the name and a plain-text
# excerpt of at most 30000 bytes (the end is kept): for "Run Go test", its `--- FAIL:` blocks (with
# the lines indented more) and `FAIL`/`panic:` lines; otherwise, or with no such block, the last 60.
set -euo pipefail
export LC_ALL=C

max=30000
name="${1:-unknown step}"
log="${2:-}"

echo "$name"
if [ -z "$log" ] || [ ! -s "$log" ]; then
  echo "no log was captured for this step."
  exit 0
fi

esc="$(printf '\033')"
stripped="$(sed -e "s/${esc}\[[0-9;?]*[A-Za-z]//g" -e '/^##\[/d' -e '/^::/d' "$log" | tr -d '\r')"

section=""
if [ "$name" = "Run Go test" ] && grep -q '^ *--- FAIL:' <<<"$stripped"; then
  section="$(awk '
    # indent: the number of leading spaces; a blank line counts as 0.
    function indent(s,   n) { n = match(s, /[^ ]/); return n ? n - 1 : 0 }
    {
      if (inblock && indent($0) > blockindent) { print; next }
      inblock = 0
      if ($0 ~ /^ *--- FAIL:/) {
        inblock = 1
        blockindent = indent($0)
        print
        next
      }
      if ($0 ~ /^FAIL/ || $0 ~ /^panic:/) print
    }
  ' <<<"$stripped")"
fi
if [ -z "$section" ]; then
  section="$(tail -n 60 <<<"$stripped")"
fi
if [ -z "$section" ]; then
  section="(the log has no output once control codes are removed)"
fi

# Bytes, not characters (LC_ALL=C above). The budget leaves room for the step
# name line and the newline that ends the excerpt.
budget=$((max - ${#name} - 2))
if [ "${#section}" -gt "$budget" ]; then
  marker=$'…(truncated)\n'
  keep=$((budget - ${#marker}))
  tail="${section: -keep}"
  # The cut can start inside a multibyte character: drop its UTF-8 continuation bytes.
  while [[ "$tail" == [$'\x80'-$'\xbf']* ]]; do
    tail="${tail:1}"
  done
  section="$marker$tail"
fi

printf '%s\n' "$section"
