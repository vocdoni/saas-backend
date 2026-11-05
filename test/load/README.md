# saas-backend load-test suite

Measures how much concurrent voter traffic one saas-backend instance serves and where it breaks:
the router's in-flight cap, CSP authentication and signing, vote relay throughput, job polling and
chain-bound reads. [vegeta](https://github.com/tsenart/vegeta) generates the load. `REPORT.md`
documents a run and its findings.

> **Never run this against production or a public endpoint.** The target is a disposable copy:
> its own MongoDB, a fresh voconed chain, a fake SMTP sink, on a docker network with no route out
> (the service calls Stripe at startup; Twilio is left unconfigured).

## Layout

| File | Purpose |
|---|---|
| `start-target.sh` | Starts voconed, MongoDB, the SMTP sink and saas-backend in resource-limited containers; prints the API URL |
| `saasload/` | Go tool: seeds the fixture and builds the vegeta targets of each voter stage (see its package doc) |
| `attack.sh` | Sends every target once at a fixed rate; keeps the results for the next stage |
| `ramp-once.sh` | Rate ramp over single-use targets (auth, sign), stops at the first saturated step |
| `ramp.sh` | Rate ramp over reusable targets (reads, job polling), from vochain-sequencer `test/load` |
| `concurrency.sh` | Closed-loop steps at a fixed number of in-flight requests (the router's cap) |
| `relay.sh` | Vote relay at a fixed rate; waits for the jobs and summarizes them from MongoDB |
| `chaindelay.sh` | Adds latency to every voconed reply with `tc netem` (a slow chain API) |

## Prerequisites

On the load host: Docker with cgroup v2, `jq`, `bc`, `taskset`, `sudo` (only for `chaindelay.sh`),
and the binaries in `test/load/bin/` (git-ignored):

```bash
docker build --build-arg GOLANG_VERSION=1.27.3 -t saas-backend:load .   # from the repo root
go build -o test/load/bin/saasload ./test/load/saasload
CGO_ENABLED=0 go build -o test/load/bin/localsmtp ./cmd/localsmtp
cp "$(which vegeta)" test/load/bin/vegeta                              # vegeta v12+
```

`start-target.sh` pins each container to its own cores; the defaults fit a 16-core/32-thread host
whose thread N and N+16 share a core (`lscpu -e=CPU,CORE`). The load generator runs on
`LG_CPUS` (default `0-7,16-23`), disjoint from the target.

## Run

```bash
cd test/load
export API=$(SAAS_IMAGE=saas-backend:load ./start-target.sh)
export MONGO=mongodb://$(docker inspect -f '{{(index .NetworkSettings.Networks "saasload").IPAddress}}' saasload-mongo):27017

# fixture: plan, admin, organization, 60k members, a 1-question and a 5-question CSP process
bin/saasload setup -api $API -mongo $MONGO -members 60000 -questions 1,5 -out results/fixture.json
bin/saasload reads -fixture results/fixture.json -out results

# router cap: N requests always in flight
./concurrency.sh results/process1.targets "50 100 101 150 200 400" 20 results/conc

# voter flow, one stage feeding the next: auth/0 -> sign-batch -> vote txs
D=results/voters
bin/saasload auth  -fixture results/fixture.json -process 0 -from 0 -n 31000 -out $D
./ramp-once.sh $D/auth.targets "100 200 400 800 1600" 10 $D/auth
bin/saasload sign  -fixture results/fixture.json -process 0 -dir $D
./ramp-once.sh $D/sign.targets "100 200 400 800 1600" 10 $D/sign
bin/saasload votes -fixture results/fixture.json -process 0 -dir $D

# vote relay: rate, seconds, offset into the vote targets
./relay.sh $D/vote.targets 1 240 results/relay-r1 0
./relay.sh $D/vote.targets 4 180 results/relay-r4 240

# job polling, with the jobs of the relay runs
cat results/relay-*.results.jsonl > results/relay.results.jsonl
bin/saasload jobs -fixture results/fixture.json -results results/relay.results.jsonl -out results/jobs.targets
./ramp.sh results/jobs.targets results/jobs "1000 2000 4000 8000" 15

# chain-bound reads with a slow chain
bin/saasload verify -fixture results/fixture.json -mongo $MONGO -out results/verify.targets
./chaindelay.sh 200ms
./ramp.sh results/results1.targets results/results-200ms "200 400 800 1600" 15
./ramp.sh results/verify.targets results/verify-200ms "25 50 100 200 400" 15
./chaindelay.sh 0
```

Every voter can authenticate and sign once per process and every vote can be relayed once, so
each stage consumes its targets: give later runs a fresh voter range (`-from`) or a fresh fixture.
The census is auth-only (member number), so no OTP is sent; job and voter outcomes are read back
from MongoDB (`saasload jobstats`).

Stop the target with `docker rm -f saasload-api saasload-smtp saasload-mongo saasload-voconed`.
