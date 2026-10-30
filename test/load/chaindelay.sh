#!/usr/bin/env bash
# Add a fixed delay to every packet voconed sends, so each chain read of saas-backend takes at
# least that much longer: a slow or distant chain API. Needs sudo for tc inside the container's
# network namespace.
# Usage: chaindelay.sh DELAY   (e.g. 200ms, 1s; 0 removes the delay)
# Env: CONTAINER (default saasload-voconed)
set -euo pipefail
DELAY=${1:?delay}
PID=$(docker inspect -f '{{.State.Pid}}' "${CONTAINER:-saasload-voconed}")
if [ "$DELAY" = 0 ]; then
  sudo nsenter -t "$PID" -n tc qdisc del dev eth0 root 2>/dev/null || true
else
  sudo nsenter -t "$PID" -n tc qdisc replace dev eth0 root netem delay "$DELAY"
fi
sudo nsenter -t "$PID" -n tc qdisc show dev eth0
