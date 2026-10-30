#!/usr/bin/env bash
# Start a disposable saas-backend load target: a fresh voconed chain, MongoDB, a fake SMTP sink and
# saas-backend, each in its own resource-limited container on the docker network "saasload".
# Nothing here talks to an external provider: the network has no egress, Twilio is left
# unconfigured and mail goes to the sink.
# Usage: start-target.sh
# Env:
#   SAAS_IMAGE     saas-backend image (default saas-backend:load)
#   CHAIN_IMAGE    vochain-sequencer image providing voconed (default vochain-sequencer:1fb3211b)
#   LOCALSMTP      path of a cmd/localsmtp binary on this host (default bin/localsmtp)
#   CPUSET_SAAS    cores of saas-backend (default 13,29: both threads of one physical core = 2 DO vCPU)
#   CPUSET_MONGO   cores of MongoDB (default 12,28)
#   CPUSET_CHAIN   cores of voconed (default 14,15,30,31)
#   CPUSET_SINK    cores of the SMTP sink (default 11,27)
#   SAAS_MEM       saas-backend memory limit (default 4g, apps-s-2vcpu-4gb)
#   BLOCK_SIZE / BLOCK_PERIOD  voconed block size and period (default 500 / 10, as production)
# Prints the saas-backend base URL (container IP, so the load does not go through docker-proxy).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
SAAS_IMAGE=${SAAS_IMAGE:-saas-backend:load}
CHAIN_IMAGE=${CHAIN_IMAGE:-vochain-sequencer:1fb3211b}
LOCALSMTP=${LOCALSMTP:-$HERE/bin/localsmtp}
NET=saasload
# the funded test account of test/voconed.go: the service key of the saas copy
KEY=d52a488fa1511a07778cc94ed9d8130fb255537783ea7c669f38292b4f53ac4f
ADDR=0x032FaEf5d0F2c76bbD804215e822A5203e83385d

for c in saasload-api saasload-smtp saasload-mongo saasload-voconed; do docker rm -f $c >/dev/null 2>&1 || true; done
docker volume rm saasload-chain saasload-mongo >/dev/null 2>&1 || true
# an internal network has no route out: the service cannot reach Stripe, Twilio or any other
# external provider even by accident (it calls Stripe at startup), while this host still reaches
# the containers by IP
docker network rm $NET >/dev/null 2>&1 || true
docker network create --internal $NET >/dev/null

docker run -d --name saasload-voconed --network $NET -v saasload-chain:/app/run \
  --cpuset-cpus="${CPUSET_CHAIN:-14,15,30,31}" --cpus=4 --memory=8g -e GOMEMLIMIT=6GiB \
  "$CHAIN_IMAGE" --dir=/app/run --port=9090 --chainID=saasload \
  --blockPeriod="${BLOCK_PERIOD:-10}" --blockSize="${BLOCK_SIZE:-500}" \
  --enableFaucet=10000 --fundedAccounts="$ADDR:1000000000" --logLevel=warn >/dev/null

docker run -d --name saasload-mongo --network $NET -v saasload-mongo:/data/db \
  --cpuset-cpus="${CPUSET_MONGO:-12,28}" --cpus=2 --memory=4g \
  mongo:7 --wiredTigerCacheSizeGB 1.5 >/dev/null

CHAIN_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" saasload-voconed)
for _ in $(seq 1 60); do curl -sf "http://$CHAIN_IP:9090/v2/chain/info" >/dev/null && break; sleep 1; done

docker run -d --name saasload-api --network $NET \
  --cpuset-cpus="${CPUSET_SAAS:-13,29}" --cpus=2 --memory="${SAAS_MEM:-4g}" --memory-swap="${SAAS_MEM:-4g}" \
  -e VOCDONI_SECRET=load-test-secret -e VOCDONI_PRIVATEKEY=$KEY \
  -e VOCDONI_VOCDONIAPI=http://saasload-voconed:9090/v2 \
  -e VOCDONI_MONGOURL=mongodb://saasload-mongo:27017 -e VOCDONI_MONGODB=saasload \
  -e VOCDONI_SERVERURL=http://saasload-api:8080 -e VOCDONI_WEBURL=http://saasload.invalid \
  -e VOCDONI_SMTPSERVER=127.0.0.1 -e VOCDONI_SMTPPORT=1025 \
  -e VOCDONI_EMAILFROMADDRESS=load@load.test -e VOCDONI_EMAILFROMNAME=Load \
  -e VOCDONI_STRIPEAPISECRET=sk_test_unused -e VOCDONI_STRIPEWEBHOOKSECRET=whsec_unused \
  -e VOCDONI_OAUTHSERVICEURL=http://saasload.invalid -e VOCDONI_LOGLEVEL=info \
  -e VOCDONI_CSPMAXDAILYSENDS=-1 -e VOCDONI_CSPMAXHOURLYAUTHFAILURES=-1 \
  "$SAAS_IMAGE" >/dev/null

# the sink shares the API's network namespace, so the service reaches it on 127.0.0.1 without
# SMTP credentials
docker run -d --name saasload-smtp --network container:saasload-api --cpuset-cpus="${CPUSET_SINK:-11,27}" \
  -v "$(realpath "$LOCALSMTP"):/localsmtp:ro" --entrypoint /localsmtp "$SAAS_IMAGE" --smtpServer=127.0.0.1 >/dev/null

API_IP=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" saasload-api)
for i in $(seq 1 120); do
  if curl -sf "http://$API_IP:8080/ping" >/dev/null; then echo "http://$API_IP:8080"; exit 0; fi
  sleep 1
done
echo "saas-backend not up" >&2; docker logs --tail 50 saasload-api >&2; exit 1
