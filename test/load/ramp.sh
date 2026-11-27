#!/usr/bin/env bash
# Step/ramp read load test with vegeta.
# Usage: ramp.sh TARGETS_FILE OUT_PREFIX "RATES" [STEP_SECONDS]
#   RATES: space separated req/s steps, e.g. "50 100 200 400 800"
# Env:
#   CONTAINER   docker container to sample cgroup CPU/mem/IO of per step (default saasload-api)
#   VEGETA      vegeta binary (default bin/vegeta next to this script, else vegeta on PATH)
#   LG_CPUS     cpu list for the load generator via taskset (default 0-7,16-23)
#   STOP_P95_MS stop ramp once p95 exceeds this (default 2000)
#   STOP_SUCC   stop ramp once success ratio drops below this (default 0.95)
#   VEGETA_ATTACK_ARGS extra vegeta attack flags (e.g. -insecure, -h2c)
# Target URLs come from TARGETS_FILE (built by build_targets.py against any base URL).
set -uo pipefail
TARGETS=${1:?targets}; OUT=${2:?out prefix}; RATES=${3:?rates}; DUR=${4:-30}
HERE=$(cd "$(dirname "$0")" && pwd)
VEGETA=${VEGETA:-$HERE/bin/vegeta}; [ -x "$VEGETA" ] || VEGETA=vegeta
STOP_P95_MS=${STOP_P95_MS:-2000}; STOP_SUCC=${STOP_SUCC:-0.95}
TS=(taskset -c "${LG_CPUS:-0-7,16-23}")

mkdir -p "$(dirname "$OUT")"
CG=""
CONTAINER=${CONTAINER:-saasload-api}
if [ -n "$CONTAINER" ]; then
  CID=$(docker inspect -f '{{.Id}}' "$CONTAINER")
  CG=/sys/fs/cgroup/system.slice/docker-$CID.scope
fi
cg_cpu() { awk '/^usage_usec/{print $2}' "$CG/cpu.stat"; }
cg_thr() { awk '/^throttled_usec/{print $2}' "$CG/cpu.stat"; }
cg_io() { awk '{for(i=2;i<=NF;i++){split($i,a,"=");s[a[1]]+=a[2]}} END{printf "%d %d %d %d", s["rios"],s["wios"],s["rbytes"],s["wbytes"]}' "$CG/io.stat"; }
wait_idle() { # wait until the target drained the backlog of a previous saturated step
  if [ -n "$CG" ]; then
    for _ in $(seq 1 60); do a=$(cg_cpu); sleep 2; b=$(cg_cpu); [ $(( (b - a) / 20000 )) -lt 15 ] && return; done
  else sleep "${COOLDOWN:-15}"; fi
}
printf "%-6s %8s %8s %8s %8s %8s %8s %7s %7s %7s %8s %8s %9s\n" rate achieved p50ms p95ms p99ms maxms succ cpu% thr% memMB riops wiops codes | tee "$OUT.summary"
for R in $RATES; do
  wait_idle
  if [ -n "$CG" ]; then c0=$(cg_cpu); t0=$(cg_thr); read -r ri0 wi0 rb0 wb0 <<<"$(cg_io)"; s0=$(date +%s.%N); fi
  "${TS[@]}" "$VEGETA" attack ${VEGETA_ATTACK_ARGS:-} -format=json -targets="$TARGETS" -rate="$R" -duration="${DUR}s" -timeout=10s -max-workers=4000 -keepalive=true > "$OUT.r$R.bin"
  if [ -n "$CG" ]; then
    s1=$(date +%s.%N); c1=$(cg_cpu); t1=$(cg_thr); read -r ri1 wi1 rb1 wb1 <<<"$(cg_io)"; mem=$(( $(cat "$CG/memory.current") / 1048576 ))
    el=$(echo "$s1 - $s0" | bc -l)
    cpu=$(echo "($c1 - $c0) / ($el * 10000)" | bc -l); thr=$(echo "($t1 - $t0) / ($el * 10000)" | bc -l)
    riops=$(echo "($ri1 - $ri0) / $el" | bc -l); wiops=$(echo "($wi1 - $wi0) / $el" | bc -l)
  else cpu=0; thr=0; mem=0; riops=0; wiops=0; fi
  "$VEGETA" report -type=json < "$OUT.r$R.bin" > "$OUT.r$R.json"
  read -r ach p50 p95 p99 mx succ codes <<<"$(jq -r '[.throughput, .latencies["50th"]/1e6, .latencies["95th"]/1e6, .latencies["99th"]/1e6, .latencies.max/1e6, .success, (.status_codes|to_entries|map("\(.key):\(.value)")|join(","))]|@tsv' "$OUT.r$R.json")"
  printf "%-6s %8.1f %8.1f %8.1f %8.1f %8.0f %8.4f %7.0f %7.0f %7s %8.0f %8.0f %9s\n" "$R" "$ach" "$p50" "$p95" "$p99" "$mx" "$succ" "$cpu" "$thr" "$mem" "$riops" "$wiops" "$codes" | tee -a "$OUT.summary"
  rm -f "$OUT.r$R.bin"
  if (( $(echo "$p95 > $STOP_P95_MS" | bc -l) )) || (( $(echo "$succ < $STOP_SUCC" | bc -l) )); then echo "stop: saturated at $R" | tee -a "$OUT.summary"; break; fi
done
