#!/usr/bin/env bash
# Usage: scripts/integration-report.sh > report.md
# Writes the reply to a `/integration` comment (EVENT=issue_comment) or a push run's report
# (EVENT=pull_request) from the env below, all optional. FAILURE and ERROR carry untrusted text.
set -euo pipefail

: "${EVENT:=}" "${REPO:=}" "${RUN_URL:=}" "${PARSED:=}" "${RESOLVE:=}" "${RESULT:=}"
: "${RESET:=}" "${STICKY:=}" "${CHECKOUT:=}" "${HEAD_SHA:=}" "${NODE_REF:=}" "${NODE_SHA:=}"
: "${FAILURE:=}" "${ERROR:=}"

# app_line: CHECKOUT is the merge commit tested and HEAD_SHA the PR head, both full SHAs when resolved.
app_line() {
  if [[ "$CHECKOUT" =~ ^[0-9a-f]{40}$ && "$HEAD_SHA" =~ ^[0-9a-f]{40}$ ]]; then
    echo "- saas-backend: merge of $HEAD_SHA ($CHECKOUT)"
  else
    echo "- saas-backend: not resolved"
  fi
}

# node_line: links are full URLs, as a bare #N or SHA would link to this repo. The SHA comes
# from the PR's own build steps, so anything but a 40-digit lowercase hex prints 'not built'.
node_line() {
  local node_url="https://github.com/vocdoni/vocdoni-node" ref_link sha_link
  if [ -z "$NODE_REF" ]; then
    echo "- vocdoni-node: go.mod version"
    return
  fi
  if [[ "$NODE_REF" =~ ^#([0-9]+)$ ]]; then
    ref_link="$node_url/pull/${BASH_REMATCH[1]}"
  else
    ref_link="\`$NODE_REF\`"
  fi
  if [[ "$NODE_SHA" =~ ^[0-9a-f]{40}$ ]]; then
    sha_link="$node_url/commit/$NODE_SHA"
  else
    sha_link='not built'
  fi
  echo "- vocdoni-node: $ref_link → $sha_link"
}

# error_code: the resolve or parse error as inline code. Backticks are stripped, so the
# text cannot close the code span.
error_code="\`${ERROR//\`/}\`"

# failure_block <text>: the text in a collapsed block. Lines starting with `~~~`
# (after optional indentation) are dropped, so PR-controlled text cannot close the fence.
failure_block() {
  local text="$1" line
  [ -n "$text" ] || return 0
  echo
  echo '<details><summary>Failure output</summary>'
  echo
  echo '~~~~~'
  while IFS= read -r line; do
    [[ "$line" =~ ^[[:space:]]*~~~ ]] && continue
    printf '%s\n' "$line"
  done <<<"$text"
  echo '~~~~~'
  echo '</details>'
}

# The failure block shows the test excerpt, or the parse or resolve error when there is none.
excerpt="${FAILURE:-$ERROR}"

case "$RESULT" in
  success) icon='✅' ;;
  failure) icon='❌' ;;
  *) icon='⚠️' ;;
esac

if [ "$EVENT" = pull_request ]; then
  echo "$icon Integration run on push: **$RESULT**"
  echo
  app_line
  node_line
  echo "- Run: $RUN_URL"
  echo
  echo "The ref comes from this PR's sticky \`/integration\` comment, re-resolved on this push."
  failure_block "$excerpt"
  exit 0
fi

if [ "$PARSED" != true ]; then
  if [ -n "$ERROR" ]; then
    echo "❌ Integration run: **rejected**: $error_code"
    echo
    echo "- Run: $RUN_URL"
  else
    echo "❌ Integration run: **rejected**: the \`/integration\` command could not be parsed, see the run log."
  fi
elif [ "$RESOLVE" != success ]; then
  if [ -n "$ERROR" ]; then
    echo "❌ Integration run: **error** resolving the PR ($RESOLVE): $error_code"
    echo
    echo "- Run: $RUN_URL"
  else
    echo "❌ Integration run: **error** resolving the PR ($RESOLVE), see the run log; comment again to retry."
  fi
elif [ "$RESET" = true ]; then
  echo "Sticky integration ref cleared; pushes no longer run this workflow's tests."
  echo
  echo "- Run: $RUN_URL"
else
  echo "$icon Integration run: **$RESULT**"
  echo
  app_line
  node_line
  echo "- Run: $RUN_URL"
  if [ "$STICKY" = true ]; then
    echo
    echo "These refs are re-resolved on every push to this PR until \`/integration reset\`."
  else
    echo
    echo "One-shot run: pushes keep using the previous sticky ref, if any."
  fi
fi
failure_block "$excerpt"
