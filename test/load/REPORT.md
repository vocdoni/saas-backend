# saas-backend capacity under concurrent voters

Date: 2026-10-30. Target: saas-backend `main` (`e139dbfa`) on z6, limited to the production size of
`saas-backend-lts` (DO App Platform apps-s-2vcpu-4gb), against a disposable voconed chain with the
production block settings. Purpose: confirm or refute the saas-backend hypotheses of the capacity
map (vochain-servers `docs/capacity-map.md`) before the 2026-11-02 cutover.

## TL;DR

| Hypothesis | Verdict | Measured |
|---|---|---|
| H1. `Throttle(100)`: request 101 in flight gets 429 at once | **Confirmed** | 100 in flight: 0 errors; 101: first 429s; 150: 57% 429; 400: 91% 429, and the 429s cut the useful throughput from 10.9k to 5.4k req/s |
| H2. Relay throughput ≈ 12 workers / inclusion time | **Confirmed, and lower than expected** | **~1.2 votes/s per instance**, whatever the offered rate. Job p95 13 s at 1 vote/s, 122 s at 2 votes/s, 330 s at 4 votes/s (16% get 503 "queue full"). With 128 workers: ~12 votes/s |
| H3. 1 s job polling fills the 100 slots | **Refuted** | `GET /jobs/{id}` serves 12,000 req/s at p99 0.5 ms. 1,000 voters polling every second keep ~0.1 requests in flight. What hurts is the SDK's 60 s polling timeout against the relay latency |
| H4. Chain-bound reads hold slots | **Confirmed** | Healthy chain: results 3,200 req/s at p99 0.5 ms. Chain answering in 1 s: 37% 429 at 200 results/s, and **every other endpoint gets the same share of 429s** (auth/0, which never reads the chain, got 32%) |
| (new) CSP auth step 0 | **Index bug** | Each `auth/0` scans the census: a 60k census saturates at **<100 auth/s** (p50 917 ms, 17% 429). With the index used: **1,600 auth/s** at p99 < 1 ms |

The ceiling of a single saas-backend instance on election day is not the 100-slot throttle by
itself. Three things reach it first, in this order:

1. **Vote relay: ~1.2 votes/s per instance.** Below the 2.1 votes/s historical LTS peak, which was
   reached when voters sent votes straight to the chain (the relay did not exist then; the current
   vocdoni-app relays every vote through `POST /vote`).
2. **CSP auth on a large census: <100 auth/s** because of the unused index. Above that, `auth/0`
   requests pile up, take the 100 slots and every endpoint answers 429.
3. **A slow chain** (≥200 ms per read) fills the 100 slots with results, process and verify reads,
   and the 429s spread to all voters.

Cheap reads, signing and job polling have plenty of room: the CPU tops out at about 11k req/s on
z6 (about 4-5k on a DO vCPU, see caveats), far above the LTS rates.

## Recommendations

In order of impact for the cutover:

1. **Fix the login-hash queries (one line each, before 2026-11-02).** `db.CensusParticipantByLoginHash`
   and the duplicate check in the census add loop (`db/census_participant.go`) query
   `{censusId, $or: [{loginHash: v}]}`. The `{censusId, loginHash*}` indexes are partial
   (`$exists: true, $type: "binData"`), and MongoDB only uses a partial index when the query
   implies its filter, which an equality does not. Adding the filter's predicates to each branch,
   `{loginHash: {$eq: v, $exists: true, $type: "binData"}}`, turns a 60,000-document scan into a
   1-key lookup (59 ms → 0 ms) and auth/0 from <100/s to ≥1,600/s.
2. **Stop holding a relay worker for the whole inclusion wait.** A worker sends the tx and then
   blocks in `WaitUntilTxIsMined`, which polls every 5 s (`PollInterval` = half the 10 s block
   time) and sleeps 2.5 s more once it sees the tx, so each vote holds a worker for 7.5-17.5 s.
   - Now, as a one-constant change: raise `relayTxQueueWorkers` from 12 to 128. The workers only
     wait; 128 of them measured **~12 votes/s** (see H2), about 6× the historical peak, with a
     20 votes/s burst kept under the SDK's 60 s timeout (job p95 40 s). The chain includes ~45
     votes/s, so it is still not the limit.
   - Then: send and release the worker; confirm all pending relays with one watcher per block (a
     batch lookup of the pending tx hashes or nullifiers) instead of one polling loop per vote.
     That removes the per-vote 2.5 s sleep and 5 s granularity too, and makes job latency ≈ one
     block (≈10 s).
3. **Keep the in-flight cap but stop slow reads from starving everything.**
   - Serve stale election reads while one refresh runs (stale-while-revalidate in
     `api/election_reads.go`), so a slow chain never makes a voter request wait for it.
   - Give chain-bound endpoints (results, process info, `/votes/verify`) their own smaller
     throttle, so they cannot take the slots of auth, sign and relay.
   - Replace `Throttle(100)` + `ThrottleBacklog(5000, …)` by one `ThrottleWithOpts` with a real
     backlog and a short backlog timeout (e.g. limit 200, backlog 1000, 5 s) and a `RetryAfterFn`.
     A short burst then queues for milliseconds instead of getting 429. Raising the limit alone
     is not the fix: cheap handlers are CPU-bound long before 100 are in flight, and slow ones
     would just hold more memory and goroutines.
4. **Job polling: fix the timeout, not the rate.** The integrator SDK (`packages/api-client/src/jobs.ts`
   in vocdoni-integrator-sdk) polls every 1 s and gives up after 60 s. At 2 votes/s the relay's
   job p95 is already 122 s, so voters would see an error for a vote that is later included.
   Keep polling until the job settles (or the 45 s server timeout × a few retries), with backoff
   and jitter (1 s → 5 s). A long-poll endpoint is not needed for load: polling is cheap.

## Setup

- Host z6: AMD Ryzen 9 9950X3D, 16 cores / 32 threads; thread N and N+16 share a core.
- `start-target.sh` runs each component pinned to its own cores, on a docker network with no route out:

  | Container | Image | Cores | Limits | Emulates |
  |---|---|---|---|---|
  | `saasload-api` | saas-backend `e139dbfa` | 13,29 (one physical core) | 2 CPU, 4 GB | apps-s-2vcpu-4gb |
  | `saasload-mongo` | mongo 7.0.45, WiredTiger cache 1.5 GB | 12,28 | 2 CPU, 4 GB | managed db-s-2vcpu-4gb |
  | `saasload-voconed` | vochain-sequencer `1fb3211b` (voconed) | 14,15,30,31 | 4 CPU, 8 GB, `GOMEMLIMIT=6GiB` | the DOKS node |
  | `saasload-smtp` | `cmd/localsmtp`, in the API's network namespace | 11,27 | — | SMTP provider |

- voconed: fresh chain, `--blockPeriod=10 --blockSize=500` (production), the service key funded at genesis.
- vegeta v12 on cores `0-7,16-23`, hitting the API container's IP (no docker-proxy).
- Fixture (`saasload setup`): a default plan without quotas, an admin, an organization with an
  on-chain account, 60,000 members with only a member number, and two CSP processes over all of
  them with an auth-only census (`authFields: [memberNumber]`, no 2FA, so no OTP): one with 1
  question and one with 5. Voters are fresh keys; `saasload` chains the stages auth/0 →
  sign-batch → signed vote txs, each stage a vegeta run.
- Relay outcomes come from the `jobs` collection (`createdAt` → `completedAt`, status), not from
  client polling.

## Results

### H1. The 100-request in-flight cap

chi v5.5.0 `Throttle(limit)` sizes the backlog channel to `limit + 0`: once 100 requests are in
the handler, the next one takes the `default` branch and gets 429 "Server capacity exceeded"
without waiting. The inner `ThrottleBacklog(5000, 40000, 60s)` is never reached.

Closed loop on `GET /processes/{id}` (5 questions), N requests always in flight, 20 s per step:

| In flight | Requests/s | OK/s | p50 | p95 | p99 | 429 | saas CPU |
|---|---|---|---|---|---|---|---|
| 50 | 11,325 | 11,323 | 4.2 ms | 7.2 ms | 9.2 ms | 0 | 199% |
| 100 | 10,894 | 10,891 | 8.9 ms | 14.9 ms | 17.7 ms | 0 | 200% |
| 101 | 10,975 | 10,941 | 8.9 ms | 15.0 ms | 17.9 ms | 0.3% | 199% |
| 120 | 15,342 | 10,366 | 7.9 ms | 15.8 ms | 19.5 ms | 32% | 199% |
| 150 | 22,216 | 9,570 | 5.1 ms | 16.3 ms | 20.6 ms | 57% | 199% |
| 200 | 34,035 | 8,255 | 3.7 ms | 16.7 ms | 22.0 ms | 76% | 199% |
| 400 | 58,233 | 5,421 | 5.3 ms | 22.5 ms | 28.9 ms | 91% | 198% |

The cap is exact, and 429s are not free: they take CPU from the requests that get through. In open
loop, in-flight requests = rate × latency, so with ~1 ms handlers the CPU saturates (~11k req/s)
long before 100 are in flight. The cap only bites when requests get slow, which is what the next
findings measure.

### CSP auth step 0 and signing

Open loop, each request a different voter, 10 s per step.

`POST /processes/{id}/auth/0`, 1-question process, 60k census:

| Voters (census position) | 100/s | 200/s | 400/s | 800/s | 1,600/s |
|---|---|---|---|---|---|
| 3,000-9,000, `main` | p99 3 ms | p99 3 ms | **296/s OK, p50 276 ms, 26% 429** | — | — |
| 29,000-60,000, `main` | **82/s OK, p50 917 ms, 17% 429** | — | — | — | — |
| 29,000-60,000, query fixed | p99 1 ms | p99 1 ms | p99 0 ms | p99 0 ms | p99 0 ms, saas CPU 36% |

With `main`, saas used 3-8% CPU while MongoDB sat at its 2-CPU limit. The MongoDB profiler showed
one query per auth: `censusParticipants` by `censusId` and login hash, plan `IXSCAN { censusId: 1 }`,
**37,750 documents examined on average** (25 ms each at only 50 auth/s). `explain` on the last member:

| Query | Index | Docs examined | Time |
|---|---|---|---|
| `{censusId, $or: [{loginHash: v}]}` (`main`) | `censusId_1` | 60,000 | 59 ms |
| `{censusId, $or: [{loginHash: {$eq: v, $exists: true, $type: "binData"}}]}` | `censusId_1_loginHash_1` | 1 | 0 ms |

The cost grows with the voter's position in the census, so the first voters of a big census are
served fast and later ones collapse; a census of the size of the largest LTS election (60,686
votes) cannot serve 100 auth/s. The same `$or` shape is used by the duplicate check when members
are added to an existing census, one scan per added member.

`POST /processes/{id}/sign-batch`, 5-question process (5 signatures per request):

| | 100/s | 200/s | 400/s | 800/s |
|---|---|---|---|---|
| sign-batch | p99 3 ms | p99 2 ms | p99 5 ms | 628/s OK, p50 155 ms, 21% 429 |

Signing is not a bottleneck: 400 voters/s is 2,000 ballots/s.

### H2. Vote relay

`POST /vote` enqueues the vote and answers 202 at once (p99 ≤ 2 ms at every rate, max 7 ms), so
the relay never holds a router slot. The 12 relay workers do the slow part: `SendTx`, then
`WaitUntilTxIsMined`, which polls the tx every 5 s and sleeps 2.5 s after it appears. Each worker
holds a vote for 7.5-17.5 s.

1-question process, pre-signed votes sent open loop, then wait until no job is pending:

| Sent | Votes | 202 / 503 | Included/s | Job latency p50 / p95 / max | Drain after send |
|---|---|---|---|---|---|
| 0.5/s × 240 s | 120 | 120 / 0 | 0.48 | 7.5 / 12.5 / 12.5 s | 10 s |
| 1/s × 240 s | 240 | 240 / 0 | 0.96 | 12.5 / 13.0 / 13.0 s | 11 s |
| 2/s × 180 s | 360 | 360 / 0 | 1.16 | 67 / 122 / 131 s | 130 s |
| 4/s × 180 s | 720 | 608 / 112 | 1.19 | 180 / 330 / 333 s | 332 s |
| 20/s × 60 s | 1,200 | 468 / 732 | 1.21 | 184 / 333 / 333 s | 333 s |

The ceiling is ~1.2 votes/s per instance, set by the worker count and the polling cadence, not by
CPU (saas under 1%) or the chain (45 votes/s). Above it, the 384-task queue fills in a few
minutes; a full queue takes ~320 s to drain, so the last queued voter waits 5.5 minutes, and new
votes get `503 transaction queue is full` (code 50301).

The relay is recent: `POST /vote` appeared in June 2026 and the 12-worker relay pool in October
2026. Until then voters' clients sent votes straight to the chain API, which is how LTS reached
2.1 votes/s. vocdoni-app `develop` relays every vote through saas (`@vocdoni/api-voting`).

With `relayTxQueueWorkers = 128` (and the login-hash fix; same setup, fresh fixture):

| Sent | Votes | 202 / 503 | Included/s | Job latency p50 / p95 / max | Drain after send |
|---|---|---|---|---|---|
| 4/s × 180 s | 720 | 720 / 0 | 3.72 (≈ the offered rate) | 12.5 / 12.5 / 17.5 s | 15 s |
| 20/s × 60 s | 1,200 | 1,171 / 29 | 11.9 (peak 12.8 over 10 s) | 25.5 / 40.3 / 42.5 s | 40 s |

The ceiling moves to ~12 votes/s (128 workers / ~10.5 s each), ten times higher, at no visible
CPU or memory cost (saas under 1% CPU, 266 MB). The price is chain read load: every waiting
worker polls `GET /chain/transactions/reference/{hash}` every 5 s, so 128 busy workers add
~26 req/s of point lookups, which voconed serves at thousands per second.

### H3. Job polling

`GET /jobs/{jobId}` over the 1,796 relay jobs, open loop, 15 s per step:

| Rate | 500 | 1,000 | 2,000 | 4,000 | 8,000 | 12,000 |
|---|---|---|---|---|---|---|
| p99 | 0.2 ms | 0.2 ms | 0.2 ms | 0.3 ms | 0.4 ms | 0.5 ms |
| saas CPU (of one core) | 4% | 6% | 13% | 22% | 43% | 56% |

No errors. MongoDB used ~13% CPU at ~3,000 req/s. A voter polling every second while its job
is pending costs ~0.1 ms of a slot. Polling is cheap; its problem is the client's 60 s timeout
(see recommendations).

### H4. Chain-bound reads with a slow chain

`tc netem` delays every reply of voconed (`chaindelay.sh`). On z6 the chain otherwise answers in
well under 1 ms; DO adds a few ms, and the vochain-sequencer report measured a loaded chain at
hundreds of ms (p95 876 ms at 200 req/s of its read mix).

Open loop, 15 s per step, ramp stopped at the first step with p95 > 2 s or success < 95%:

| Chain delay | `GET /processes/{id}/results` (5 questions) | `POST /votes/verify` (1 nullifier) |
|---|---|---|
| none | 3,200/s: p99 0.5 ms, no errors | 800/s: p99 0.3 ms, no errors |
| 200 ms | 200/s OK (p99 367 ms); 400/s: 3% 429; 800/s: 7% 429 | 100/s OK (p99 380 ms); 200/s: 156/s served, 19% 429 |
| 1 s | 200/s: 126/s served, 37% 429, p99 1.95 s | 25/s OK (p99 1.9 s); 50/s: 29/s served, 31% 429 |

Results are cached for 5 s per election with one chain read in flight (singleflight), but every
request that arrives during the refresh waits for it with its slot held: at 1 s per read, 200
results/s keep up to ~200 requests waiting. `/votes/verify` has no cache and a 32-slot semaphore
(32 / delay is its ceiling: 160/s at 200 ms, 32/s at 1 s); requests waiting for the semaphore also
hold router slots.

The 429s are not confined to the slow endpoint. Chain delay 1 s, results at 150/s for 30 s, and
in the middle 20 s two voter streams:

| Stream | Rate | OK | 429 | p99 |
|---|---|---|---|---|
| results (slow) | 150/s | 70% | 30% | 1.93 s |
| `GET /processes/{id}` | 100/s | 68% | 32% | 1.80 s |
| `POST .../auth/0` (no chain read) | 100/s | 68% | 32% | 15 ms |

Memo reads (`resolveQuestionMemos`, managers only, one chain read per vote, uncached) were not
measured; they hold a slot for the whole N+1 walk and add to the same pool.

## Caveats

- **CPU speed.** z6 runs about 2-2.5× faster per thread than a DO vCPU (see the vochain-sequencer
  report). CPU-bound ceilings (cheap reads ~11k req/s, job polling, signing) derate by that. The
  relay and the auth findings are not CPU-bound and do not derate: the relay is set by workers and
  block time, auth by MongoDB scans (DO's managed MongoDB has the same 2 vCPU, also slower).
- **MongoDB is local.** Production's managed MongoDB adds a network round trip (~0.5-1 ms) to every
  query, which lengthens every request and so raises the number of requests in flight at a given
  rate.
- **One instance.** `saas-backend-lts` runs one instance. The relay ceiling scales with instances
  (each has its own workers); the throttle and the read paths are per instance too.
- **Auth-only census.** OTP by email or SMS was not measured: the notification queue, its circuit
  breaker and the providers' rate limits remain a hypothesis in the capacity map.
- **No edge.** DO App Platform's ingress and Cloudflare in front of the service were not part of
  the test.
