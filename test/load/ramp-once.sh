#!/usr/bin/env bash
# Rate ramp over single-use targets (auth and sign requests of distinct voters): each step sends the
# next RATE*STEP_SECONDS targets of TARGETS at RATE req/s, open loop, and the ramp stops at the
# first saturated step. The results of every step are appended to OUT.results.jsonl, so the next
# saasload stage sees every voter that got through.
# Usage: ramp-once.sh TARGETS "RATES" STEP_SECONDS OUT_PREFIX
# Env: STOP_P95_MS (default 1000), STOP_SUCC (default 0.95), plus VEGETA, LG_CPUS, CONTAINER as in attack.sh
set -uo pipefail
TARGETS=${1:?targets}; RATES=${2:?rates}; DUR=${3:?step seconds}; OUT=${4:?out prefix}
HERE=$(cd "$(dirname "$0")" && pwd)
STOP_P95_MS=${STOP_P95_MS:-1000}; STOP_SUCC=${STOP_SUCC:-0.95}
mkdir -p "$(dirname "$OUT")"
: > "$OUT.results.jsonl"; : > "$OUT.summary"
off=0
for R in $RATES; do
  n=$(( R * DUR ))
  [ $(( off + n )) -gt "$(wc -l < "$TARGETS")" ] && { echo "out of targets at $R/s" | tee -a "$OUT.summary"; break; }
  tail -n +$((off + 1)) "$TARGETS" | head -n "$n" > "$OUT.r$R.targets"
  off=$(( off + n ))
  sleep 5
  "$HERE/attack.sh" "$OUT.r$R.targets" "$R" "$OUT.r$R" | head -1 | tee -a "$OUT.summary"
  cat "$OUT.r$R.results.jsonl" >> "$OUT.results.jsonl"
  read -r p95 succ <<<"$(jq -r '[.latencies["95th"]/1e6, .success] | @tsv' "$OUT.r$R.report.json")"
  rm -f "$OUT.r$R.targets" "$OUT.r$R.results.jsonl"
  if (( $(echo "$p95 > $STOP_P95_MS" | bc -l) )) || (( $(echo "$succ < $STOP_SUCC" | bc -l) )); then
    echo "stop: saturated at $R/s" | tee -a "$OUT.summary"; break
  fi
done
