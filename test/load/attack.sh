#!/usr/bin/env bash
# Send every target of TARGETS once, open loop at RATE req/s, and summarize the run.
# Usage: attack.sh TARGETS RATE OUT_PREFIX
#   Writes OUT_PREFIX.results.jsonl (vegeta JSON results, read by the next saasload stage),
#   OUT_PREFIX.summary (one line) and OUT_PREFIX.errors.txt (top non-2xx bodies).
# Env:
#   VEGETA      vegeta binary (default bin/vegeta next to this script, else vegeta on PATH)
#   LG_CPUS     cpu list for the load generator via taskset (default 0-7,16-23)
#   TIMEOUT     per-request timeout (default 60s, above the service's 45 s request timeout)
#   CONTAINER   docker container to sample cgroup CPU and memory of (default saasload-api)
#   DURATION    attack seconds; required when RATE is not an integer req/s (e.g. 1/2000ms)
set -uo pipefail
TARGETS=${1:?targets}; RATE=${2:?rate}; OUT=${3:?out prefix}
HERE=$(cd "$(dirname "$0")" && pwd)
VEGETA=${VEGETA:-$HERE/bin/vegeta}; [ -x "$VEGETA" ] || VEGETA=vegeta
TS=(taskset -c "${LG_CPUS:-0-7,16-23}")
N=$(wc -l < "$TARGETS")
# an integer rate sets the duration that sends every target once; a vegeta rate such as 1/2s needs DURATION
DUR=${DURATION:-$(( (N + RATE - 1) / RATE ))}
mkdir -p "$(dirname "$OUT")"
CG=/sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' "${CONTAINER:-saasload-api}").scope
c0=$(awk '/^usage_usec/{print $2}' "$CG/cpu.stat"); s0=$(date +%s.%N)
"${TS[@]}" "$VEGETA" attack -lazy -format=json -targets="$TARGETS" -rate="$RATE" -duration="${DUR}s" \
  -timeout="${TIMEOUT:-60s}" -max-workers=20000 > "$OUT.bin"
s1=$(date +%s.%N); c1=$(awk '/^usage_usec/{print $2}' "$CG/cpu.stat")
cpu=$(echo "($c1 - $c0) / (($s1 - $s0) * 10000)" | bc -l)
mem=$(( $(cat "$CG/memory.current") / 1048576 ))
"$VEGETA" encode -to=json < "$OUT.bin" > "$OUT.results.jsonl"
"$VEGETA" report -type=json < "$OUT.bin" > "$OUT.report.json"
jq -r 'select(.code < 200 or .code >= 300) | "\(.code) \(.body|@base64d|.[0:100])"' "$OUT.results.jsonl" \
  | sort | uniq -c | sort -rn | head -5 > "$OUT.errors.txt"
rm -f "$OUT.bin"
jq -r --arg rate "$RATE" --arg cpu "$cpu" --arg mem "$mem" \
  '"rate=\($rate)/s sent=\(.requests) achieved=\(.throughput*10|floor/10)/s success=\(.success) p50=\(.latencies["50th"]/1e6|floor)ms p95=\(.latencies["95th"]/1e6|floor)ms p99=\(.latencies["99th"]/1e6|floor)ms max=\(.latencies.max/1e6|floor)ms cpu=\($cpu|tonumber|floor)% mem=\($mem)MB codes=\(.status_codes|to_entries|map("\(.key):\(.value)")|join(","))"' \
  "$OUT.report.json" | tee "$OUT.summary"
cat "$OUT.errors.txt"
