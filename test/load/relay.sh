#!/usr/bin/env bash
# Vote relay throughput: send pre-signed votes to POST /vote at a fixed rate, wait until every
# relay job has left pending, and summarize the jobs from MongoDB (outcomes, creation-to-completion
# latency, completions per second).
# Usage: relay.sh VOTE_TARGETS RATE DURATION_S OUT_PREFIX [OFFSET]
#   RATE may be fractional (0.5). Uses lines OFFSET+1 .. OFFSET+RATE*DURATION of VOTE_TARGETS
#   (built by `saasload votes`; every vote can be relayed once).
# Env: MONGO (MongoDB URL of the target), SAASLOAD (saasload binary, default bin/saasload),
#      DRAIN_TIMEOUT (s, default 900), plus VEGETA, LG_CPUS, CONTAINER as in attack.sh
set -uo pipefail
VOTES=${1:?vote targets}; RATE=${2:?rate}; DUR=${3:?duration}; OUT=${4:?out prefix}; OFF=${5:-0}
HERE=$(cd "$(dirname "$0")" && pwd)
SAASLOAD=${SAASLOAD:-$HERE/bin/saasload}; [ -x "$SAASLOAD" ] || SAASLOAD=saasload
: "${MONGO:?set MONGO}"
N=$(printf "%.0f" "$(echo "$RATE * $DUR" | bc -l)")
AVAIL=$(( $(wc -l < "$VOTES") - OFF ))
[ "$N" -gt "$AVAIL" ] && { echo "need $N votes, only $AVAIL left in $VOTES after offset $OFF"; exit 1; }
mkdir -p "$(dirname "$OUT")"
tail -n +$((OFF + 1)) "$VOTES" | head -n "$N" > "$OUT.targets"
# vegeta takes a rate as hits per period: 0.5/s is 1 hit every 2 s
PER=$(printf "%.0f" "$(echo "1000 / $RATE" | bc -l)")
T0=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ); t0=$(date +%s)
DURATION=$DUR "$HERE/attack.sh" "$OUT.targets" "1/${PER}ms" "$OUT" >/dev/null
t1=$(date +%s)
DT=${DRAIN_TIMEOUT:-900}
while :; do
  s=$("$SAASLOAD" jobstats -mongo "$MONGO" -since "$T0" -type relay_vote | head -1)
  pending=$(sed -E 's/.*pending=([0-9]+).*/\1/' <<<"$s")
  [ "$pending" -eq 0 ] && break
  [ $(( $(date +%s) - t1 )) -gt "$DT" ] && { echo "drain timeout"; break; }
  sleep 5
done
t2=$(date +%s)
{
  echo "relay rate=$RATE/s duration=${DUR}s send_s=$((t1 - t0)) drain_after_send_s=$((t2 - t1))"
  echo "http: $(cat "$OUT.summary")"
  echo "jobs: $("$SAASLOAD" jobstats -mongo "$MONGO" -since "$T0" -type relay_vote)"
} | tee "$OUT.relay"
rm -f "$OUT.targets"
