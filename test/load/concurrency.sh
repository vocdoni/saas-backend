#!/usr/bin/env bash
# Closed-loop concurrency steps: C clients each send their next request as soon as the previous
# one answers, so C requests are in flight at all times. Shows where the router's in-flight cap
# starts answering 429.
# Usage: concurrency.sh TARGETS "C1 C2 ..." DURATION_S OUT_PREFIX
# Env: VEGETA, LG_CPUS (see attack.sh), CONTAINER (default saasload-api)
set -uo pipefail
TARGETS=${1:?targets}; CONCS=${2:?concurrency steps}; DUR=${3:?duration}; OUT=${4:?out prefix}
HERE=$(cd "$(dirname "$0")" && pwd)
VEGETA=${VEGETA:-$HERE/bin/vegeta}; [ -x "$VEGETA" ] || VEGETA=vegeta
TS=(taskset -c "${LG_CPUS:-0-7,16-23}")
CG=/sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' "${CONTAINER:-saasload-api}").scope
mkdir -p "$(dirname "$OUT")"
printf "%-6s %9s %8s %8s %8s %8s %8s %6s %s\n" conc req_s ok_req_s p50ms p95ms p99ms ok% cpu% codes | tee "$OUT.summary"
for C in $CONCS; do
  sleep 5
  c0=$(awk '/^usage_usec/{print $2}' "$CG/cpu.stat"); s0=$(date +%s.%N)
  "${TS[@]}" "$VEGETA" attack -format=json -targets="$TARGETS" -rate=0 -workers="$C" -max-workers="$C" \
    -duration="${DUR}s" -timeout=60s > "$OUT.c$C.bin"
  s1=$(date +%s.%N); c1=$(awk '/^usage_usec/{print $2}' "$CG/cpu.stat")
  cpu=$(echo "($c1 - $c0) / (($s1 - $s0) * 10000)" | bc -l)
  "$VEGETA" report -type=json < "$OUT.c$C.bin" > "$OUT.c$C.json"
  rm -f "$OUT.c$C.bin"
  jq -r --arg c "$C" --arg cpu "$cpu" '[$c, .rate, .throughput, .latencies["50th"]/1e6, .latencies["95th"]/1e6,
      .latencies["99th"]/1e6, .success*100, ($cpu|tonumber),
      (.status_codes|to_entries|map("\(.key):\(.value)")|join(","))] | @tsv' "$OUT.c$C.json" |
    awk -F'\t' '{printf "%-6s %9.1f %8.1f %8.1f %8.1f %8.1f %8.2f %6.0f %s\n", $1,$2,$3,$4,$5,$6,$7,$8,$9}' | tee -a "$OUT.summary"
done
