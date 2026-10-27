# CSP key per election: design

Status: approved 2026-10-22 (controller, owner-authorized autonomous mode)   Class: architectural
Epic: vocdoni/saas-backend#769 (label `epic-csp-key-per-election`)
Short version for review: [2026-10-22-csp-key-per-election-design.md](2026-10-22-csp-key-per-election-design.md)
Owner decisions (2026-10-22):
- D1. The chain side is vochain-sequencer (not vocdoni-node).
- D2. Soft transition. From the deploy of the new sequencer on, new CSP elections are created only
  with the new scheme. Elections already open (`OFF_CHAIN_CA`, `OFF_CHAIN_CA_V2`) keep today's salted signing and
  verification until they end: on LTS, valid elections run past the flag day (11 `OFF_CHAIN_CA`
  elections of one organization end 2026-11-07). Removing the legacy paths is a follow-up.
- D2b. Target: deploy to LTS on 2026-11-02 (flag day), the afternoon after that morning's LTS
  cutover from vocdoni-node to vochain-sequencer, once the cutover is verified.
- D2c. No fork height. The rules apply unconditionally from the deploy of the new sequencer binary:
  vochain-sequencer is a single node that never re-executes past blocks (`--importGenesis` copies
  the state as is and continues at the next height, `recoverHeight` re-executes nothing, and the
  indexer reindex only decodes transactions), so no height has to keep old blocks valid.
- D3. Plain ECDSA CSP elections move to the same per-election-key model, with keys separate from the
  blind ones.
- D4. The weight certificate is an Ethereum-style secp256k1 signature (65 bytes) by the election key.
- D5. saas-backend#779 is folded into this epic's saas-backend PR rather than merged first. The epic
  keeps its idea of storing the census origin per question, and drops its switch of plain ECDSA
  elections to `OFF_CHAIN_CA_V2`: legacy ECDSA elections stay on `OFF_CHAIN_CA`, legacy blind ones on
  `OFF_CHAIN_CA_V2`, and every new one uses the new origins. #779 is closed as superseded once the
  epic PR is open.
- D6. All PRs are drafts; nothing is merged, released or deployed without the owner.

## Problem

Blind CSP verification is linear in the public key. The sequencer checks
`sG == F + rx·h(m)·Q` (`go-blindsecp256k1@v0.2.0/blindsecp256k1.go:422`). Every CSP election's
verification key is the CSP root key plus a public scalar: `Q' = Q + salt·G`, with
`salt = processID` (OFF_CHAIN_CA) or `keccak256(processID || weight32)[:20]` (OFF_CHAIN_CA_V2)
(`vochain-sequencer/crypto/saltedkey/saltedkey.go:95,107,125`). A voter holding one valid blind
signature `(s, F)` for `(Q, salt₁)` can compute `s' = s + rx·h(m)·(salt₂ − salt₁)`, which is valid for
`salt₂`. The voter needs no secret, so one authorization can be re-targeted offline to another
election or another vote weight. The plain ECDSA salted path is not linear in the same way, but it
shares one root with the blind path (`saas-backend/csp/csp.go:121,218-235`: one private scalar backs
both the ECDSA and the blind public key).

## Goals

1. A CSP authorization (ECDSA or blind) is valid in exactly one election, with exactly the weight the
   CSP assigned to the voter.
2. No two CSP verification keys have a publicly computable relation. Every key comes from a hash
   derivation keyed by the CSP's root secret.
3. For V3 elections, the chain verifies CSP proofs against keys it is told, checking a CSP-issued
   certificate; it never derives a V3 key. Legacy elections keep the salted derivation until they
   end (D2).
4. One coordinated switch: from the deploy of the new sequencer on, it creates CSP elections only
   with the new census origins, and saas-backend, deployed right after it, publishes only those.
   Votes on legacy elections already open keep working, unchanged, until those elections end.
5. Every client that builds CSP vote proofs (vocdoni-sdk, vocdoni-integrator-sdk `api-voting` /
   `react-providers`) builds the new proofs; vocdoni-app ships with those packages.

## Non-goals

- Changing the blind signature scheme or the go-blindsecp256k1 library.
- Changing CSP authentication (auth steps, OTP, resend budgets) or the one-signature-per-election
  rule of the blind flow (`saas-backend/db/csp.go:339-429`).
- Changing vocdoni-node, or the standalone `vocdoni/blind-csp` service. vocdoni-sdk only passes the
  certificate through; it does not depend on any CSP producing one.
- Removing the legacy (salted) signing and verification paths. They stay while legacy elections are
  open, because those elections still take votes (D2). A follow-up issue removes the saas-backend
  side once no legacy election is open.
- Fixing the re-targeting weakness for legacy elections that are still open: that residual risk is
  accepted until they end.

## Current state

- Proto (`dvote-protobuf/src/vochain/vochain.proto`): `ProofCA{type, bundle, signature}` (l.57-68),
  types `ECDSA=1, ECDSA_PIDSALTED=2, ECDSA_BLIND=3, ECDSA_BLIND_PIDSALTED=4`;
  `CAbundle{processId=1, address=2, optional voteWeight=3}` (l.70-74); `CensusOrigin`
  `OFF_CHAIN_CA=3, OFF_CHAIN_CA_V2=4` (l.383-400). The generated Go and JS live under `build/` and
  are committed. Released as `go.vocdoni.io/proto` / `@vocdoni/proto` 1.17.0.
- Sequencer: `VerifyProof` picks the verifier by `process.CensusOrigin`
  (`vochain/transaction/proof.go:23-70`); `cspproof.ProofVerifierCSP.Verify` checks the bundle
  address and processId, then the signature, with any of the four proof types on either origin
  (`vochain/transaction/proofs/cspproof/cspproof.go:42-131`). `NewProcessTxCheck` accepts both CSP
  origins and does not validate the census root (`vochain/transaction/election_tx.go:58-70`). The
  processID embeds the census origin in byte 26 (`vochain/processid/process_id.go:44-64`). The REST API maps census types
  `csp`/`csp2` to the two origins (`api/censuses.go:28-31`, `api/api_types.go:562-575`).
  `ethereum.PubKeyFromSignature` rewrites `signature[64]` in place (`crypto/ethereum/ethereum.go:224`).
- saas-backend: one root key from `--privateKey` (`cmd/service/main.go:147-181`) wrapped in
  `saltedkey.SaltedKey` (`csp/csp.go:121`). At publish, the census root is the global ECDSA or blind
  public key, stored once per census (`api/processes_publish.go:364-374`). The origin is
  `OFF_CHAIN_CA`, or `OFF_CHAIN_CA_V2` when the census is anonymous (`account/process.go:212-215`).
  ECDSA signing salts with `processID[:20]` (`csp/sign.go:225-229`). Blind round 1 arms a nonce and
  pins the weight (`csp/sign.go:61`, `db/csp.go:339`); round 2 claims it once and signs with the V2
  salt (`csp/sign.go:113-160`, `db/csp.go:394`). Each `VotingProcessQuestion` is one on-chain
  election with a stable Mongo `_id` (`db/types.go:840`). The integration tests run an in-process
  Vocone from `go.vocdoni.io/dvote/vocone` (`test/voconed.go:69-110`). Proto pin
  `go.vocdoni.io/proto v1.15.14`.
- vocdoni-sdk: builds `ProofCA` in `packageSignedProof`, defaulting to `ECDSA_BLIND_PIDSALTED`
  (`src/core/vote.ts:124-135`); the bundle is built in `cspCaBundle` (l.168-194); the CSP flow is in
  `src/services/csp.ts:96-159`. Proto pin `@vocdoni/proto 1.15.14`.
- vocdoni-integrator-sdk: `@vocdoni/api-voting` builds the bundle and `ProofCA`
  (`packages/api-voting/src/vote-transaction.ts`) and runs the two-round blind flow against
  saas-backend (`src/blind-csp.ts`). `@vocdoni/react-providers` picks `ECDSA_BLIND_PIDSALTED` for
  anonymous censuses and the default (`ECDSA_PIDSALTED`) otherwise
  (`packages/react-providers/src/election/ElectionProvider.tsx:455`). `@vocdoni/api-types` declares
  the CSP request and response types. Released with changesets.
- vocdoni-app: no CSP proof code; it uses `@vocdoni/react-components` / `react-providers` /
  `api-voting` / `api-types`.

## Design

### Census origins and proof types (dvote-protobuf, `@vocdoni/proto` 1.18.0)

```proto
enum CensusOrigin {
    ...
    OFF_CHAIN_CA = 3;          // no new processes on vochain-sequencer
    OFF_CHAIN_CA_V2 = 4;       // no new processes on vochain-sequencer
    OFF_CHAIN_CA_V3_ECDSA = 5; // per-election CSP key, ECDSA proofs
    OFF_CHAIN_CA_V3_BLIND = 6; // per-election CSP key, blind proofs with a weight certificate
}

message ProofCA {
    enum Type { UNKNOWN = 0; ECDSA = 1; ECDSA_PIDSALTED = 2; ECDSA_BLIND = 3; ECDSA_BLIND_PIDSALTED = 4; }
    Type type = 1;
    CAbundle bundle = 2;
    bytes signature = 3;
    CAWeightCertificate weightCertificate = 4; // required for OFF_CHAIN_CA_V3_BLIND, absent otherwise
}

message CAWeightCertificate {
    bytes weightKey = 1; // 33-byte compressed secp256k1 point X
    bytes signature = 2; // 65-byte Ethereum signature by the election key over the certificate message
}
```

The origin fixes the proof type. `OFF_CHAIN_CA_V3_ECDSA` accepts only `ECDSA`, and
`OFF_CHAIN_CA_V3_BLIND` accepts only `ECDSA_BLIND`. Separate origins also give separate processIDs,
because the origin is part of the processID.

### Key derivation (saas-backend only)

`root` is the existing 32-byte CSP private scalar. `seed` is the 12-byte Mongo ObjectID of the
`VotingProcessQuestion`: stable, unique, and known before the election exists on chain.
`u32be(i)`/`u256be(w)` are big-endian fixed-width encodings.

```
hashToScalar(tag, data):
    for i in 0..255:
        h = keccak256(tag || u32be(i) || data) as big-endian integer
        if 0 < h < n: return h
    fail

dE = hashToScalar("vocdoni/csp/v3/election-ecdsa", root || seed)  // census root of an ECDSA election
dB = hashToScalar("vocdoni/csp/v3/election-blind", root || seed)  // census root of a blind election
dX = hashToScalar("vocdoni/csp/v3/weight-key", u256be(dB) || u256be(w))  // blind key for weight w
```

Tags are prefix-free: no tag is a prefix of any other (the weight-key tag is
`"vocdoni/csp/v3/weight-key"`, so it is not a prefix of the certificate tag
`"vocdoni/csp/v3/weight-cert"`). Any future tag must keep this property.

The census root published for the election is the compressed public key of `dE` or `dB`. `dE` signs
CA bundles. `dB` only signs weight certificates and never a blind message. `dX` only signs blind
messages. Nothing is stored except the existing root key.

### Weight certificate

```
certMessage(processId, w, X) = "vocdoni/csp/v3/weight-cert" (26 ASCII bytes)
                               || processId (32 bytes) || u256be(w) || X (33 bytes, compressed)
certificate.signature = Ethereum personal-sign of certMessage with dB (65 bytes, v ∈ {0,1,27,28})
```

The message is not a valid `CAbundle` encoding, because its first byte, 0x76, has protobuf wire
type 6. A certificate signature therefore can never pass as a bundle signature, or the other way
round.

### Chain verification (vochain-sequencer)

For a vote on an `OFF_CHAIN_CA_V3_*` election, after the existing bundle address and processId checks:

- Weight: `w = 1` if `bundle.voteWeight` is absent; otherwise `len ≤ 32` and `w = bigEndian(voteWeight) ≥ 1`.
  Anything else is rejected.
- `V3_ECDSA`: `type == ECDSA` and `weightCertificate` absent. The public key recovered from the bundle
  signature, working on a copy because `ethereum.PubKeyFromSignature` rewrites `signature[64]` in
  place, must equal `process.CensusRoot` (33 bytes compressed). Weight = `w`.
- `V3_BLIND`: `type == ECDSA_BLIND` and `weightCertificate` present, with `len(weightKey) == 33` and
  `len(signature) == 65`. `X` is decompressed (must be on the curve). The key recovered from
  `certificate.signature` over `certMessage(envelope.ProcessId, w, X)`, working on a copy of the
  signature, must equal `process.CensusRoot`. Then `blind.Verify(keccak256(bundleBytes), sig, X)`
  must hold. Weight = `w`.

`NewProcessTxCheck`: CSP origins must be `V3_ECDSA` or `V3_BLIND`, and the census root must be a
valid 33-byte compressed point. New `OFF_CHAIN_CA` and `OFF_CHAIN_CA_V2` processes are rejected with
`census origin %s retired for new processes`. `VerifyProof` keeps verifying votes on existing
`OFF_CHAIN_CA`/`V2` processes exactly as today. A `ProofCA` carrying a `weightCertificate` is
rejected on those legacy origins.

These rules have no activation height: they apply on every chain from the deploy of the binary
that carries them (D2c). The REST API
adds census types `csp3` (`V3_ECDSA`) and `csp3blind` (`V3_BLIND`), reports the new origin names,
and marks `OFF_CHAIN_CA_V2` as deprecated as well.

### saas-backend

- Dependencies: saas-backend stays on `go.vocdoni.io/dvote` and `go.vocdoni.io/proto v1.15.14`.
  Proto 1.17+ drops census-origin constants that this dvote version still compiles, so the two
  cannot coexist in one module. saas does not need the new proto: it never builds or re-encodes a
  `ProofCA` (the vote relay forwards the client's signed bytes, `api/process_vote.go`
  `parseRelayVote`/`submitRelayVote`), so the two new origins are declared as local typed
  constants (`models.CensusOrigin(5)` / `(6)`) in one place, documented against the proto. The
  production module never imports `github.com/vocdoni/vochain-sequencer`.
- Publish: for each question, compute its root from `dE`/`dB` (blind when the census is anonymous)
  and the question `_id`, and send it as the `CensusRoot` of that question's `NEW_PROCESS` tx with
  origin `V3_ECDSA` or `V3_BLIND`. Store the origin on the question (`censusOrigin`) together with
  `upstreamId`, and the per-question root (`censusRoot`). Publish keeps writing
  `census.Published{URI, CreatedAt}` and leaves `Root` empty for V3 censuses. Every reader of
  `census.Published.Root` (e.g. `api/processes_admin.go:778`) is updated to tolerate the empty root
  and to read the question's `censusRoot` where a key is actually needed.
- ECDSA sign / sign-batch: sign the `CAbundle{processId, address, voteWeight}` with `dE` of that
  question (Ethereum personal-sign, as today). No salt.
- Weight normalisation: a member with no weight (or an empty pinned weight) is signed as `w = 1`:
  `dX` uses `u256be(1)` and the certificate carries `w = 1`, matching the chain's default when
  `bundle.voteWeight` is absent. The client may omit `voteWeight` or encode 1; both verify.
- Blind round 1 is unchanged (arm the nonce, pin the weight). Round 2: sign with `dX` for the pinned
  weight and return, per ballot, the blind signature plus `weightKey` (hex X) and `weightCertificate`
  (hex 65-byte signature). The certificate is deterministic and public.
- The signing scheme is chosen per question from a stored `censusOrigin` (the idea of #779, which is
  folded in): publish stores the origin together with `upstreamId`.
  `OFF_CHAIN_CA_V3_ECDSA` / `OFF_CHAIN_CA_V3_BLIND` → per-election keys (above). An empty origin
  means the question was published before this change and is signed exactly as today: plain ECDSA
  with the V1 salt (`processID[:20]`), blind with the V2 salt. No existing question gets a migration
  or a new signer, and the legacy signing code stays as is.
- Every sign response tells the client which proof to build: each `SignBatchResult` (sign,
  sign-batch, blind-sign) and the single `sign` response gain `proofType`, the `ProofCA.Type` name
  (`ECDSA`, `ECDSA_PIDSALTED`, `ECDSA_BLIND`, `ECDSA_BLIND_PIDSALTED`). Clients do not need to know
  the census origin.
- A follow-up issue (labeled for the epic) tracks removing the legacy signers once no legacy question
  is open; #597 is linked to it.
- Test harness: the integration tests run vochain-sequencer's `voconed` as a subprocess instead of
  dvote's in-process Vocone, so they publish and vote V3 elections end to end. The binary is built
  at `TestMain` from a nested Go module used only by tests (`test/sequencer/go.mod`, pinning
  `github.com/vocdoni/vochain-sequencer` at the epic commit and re-pinned at release), so the
  production module's dependency graph is unchanged. One chain runs per test binary. Legacy
  elections cannot be created on it, so legacy coverage is split in two: the `api` tests check that
  a legacy question (empty stored origin) signs with the legacy `proofType`, and that a legacy
  publish (`OFF_CHAIN_CA`/`V2` `NEW_PROCESS`, as a saas-backend without this change sends it) is
  rejected by the chain; a second nested module, `test/sequencer/legacycsp`, signs legacy ballots
  with the saas-backend CSP and verifies them in process with the vochain-sequencer `cspproof`
  verifier of the legacy origins. It is separate from `test/sequencer` so the saas-backend
  dependencies never change the versions the `voconed` binary is built with.
- Swagger regenerated (`make swagger`).

### Clients

- vocdoni-integrator-sdk:
  - `@vocdoni/api-types`: `SignBatchResult` gains optional `proofType`, `weightKey` and
    `weightCertificate` (hex).
  - `@vocdoni/api-voting`: `signBlindCspBallots` returns them; `buildVoteTransaction` accepts an
    optional certificate and puts it in `ProofCA.weightCertificate`. The testkit keeps `salt()` for
    legacy tests and gains the derivation and certificate helpers needed to fake a V3 CSP.
  - `@vocdoni/react-providers`: uses the `proofType` returned by the CSP for each ballot, and falls
    back to today's choice (`ECDSA_BLIND_PIDSALTED` for anonymous censuses, `ECDSA_PIDSALTED`
    otherwise) when it is absent. The certificate is passed through.
  - Changesets: minor for all three (additive and backward compatible).
- vocdoni-sdk: `CspSignature`/`CspVote`/`CspCensusProof` carry an optional `weightCertificate
  {weightKey, signature}` that `packageSignedProof` encodes. When a certificate is present and no
  proof type is given, the type is `ECDSA_BLIND`; otherwise the default stays
  `ECDSA_BLIND_PIDSALTED`. Minor release.
- vocdoni-app: bump the `@vocdoni/*` packages to the releases above; no code change is expected.
- vochain-sequencer moves to `go.vocdoni.io/proto v1.18.0` and all TS consumers to `@vocdoni/proto
  1.18.0` (saas-backend stays on 1.15.14, see above). During development, the PRs pin the proto PR's commit (Go pseudo-version; TS via a git or
  tarball dependency). Before merge, they re-pin to the released 1.18.0.

### Alternatives considered

| Option | Why not |
|---|---|
| Client picks the proof type from the election's census origin | Integrator clients get elections from saas-backend, which does not expose the origin. A `proofType` per signature is authoritative and works per ballot in batches. |
| Keep the salt and make it a hash of the root (`Q' = Q + H(root‖pid)·G`) | A publicly computable relation still exists once Q' is published, because `Q' − Q` is public, so the linear shift still works. |
| One origin `OFF_CHAIN_CA_V3` with both proof types | It works only because the certificate message and bundle encodings are disjoint. Two origins make "proof type matches origin" a chain rule (issue #769) and separate the processIDs. |
| Random per-election key stored in Mongo | It needs secret storage and backups, and losing the row loses the election. Hash derivation from the root plus the question `_id` needs nothing new. |
| CSP-signed weight inside the blinded bundle | The CSP cannot read a blinded bundle; only the choice of signing key is under its control (issue #769). |
| Hard cut (retire legacy elections on the flag day) | Rejected by the owner (D2): valid legacy elections are open past the flag day. |
| Activation height (`config/forks.go`) for the new rules | vochain-sequencer never re-executes past blocks (D2c), so a height protects nothing; the deploy itself is the switch. |

## Compatibility and rollout

| Writer → reader | Result |
|---|---|
| Legacy client (salted proof) → new sequencer, legacy election | Accepted as today. |
| Legacy client → V3 election | Rejected cleanly (PIDSALTED type or missing certificate). Clients are deployed before saas-backend. |
| New client → legacy election | Works: the CSP returns the legacy `proofType` and no certificate. |
| New client (V3 proof) → old sequencer | Not applicable: V3 elections exist only on the new sequencer. |
| Old saas (legacy publish) → new sequencer | Rejected cleanly at CheckTx (`census origin %s retired for new processes`); the idempotent publish retries and succeeds once the new saas-backend is deployed. |
| New saas (V3 publish) → old sequencer | Not applicable: saas-backend is deployed after the sequencer. |
| Old proto decoder reading ProofCA with field 4 | The unknown field is ignored; only old nodes would read it, and none runs after the flag day. |
| Legacy question → new saas sign | Signed as today, with the legacy `proofType`. |

Rollout, around the LTS flag day on 2026-11-02 (each step needs the owner's OK):
1. Beforehand: merge and tag dvote-protobuf v1.18.0 (npm + Go), and re-pin the consumers to it.
2. Beforehand: release vocdoni-sdk and the integrator-sdk packages, and deploy vocdoni-app with
   them. They are backward compatible with legacy elections and the current saas-backend.
3. Morning of 2026-11-02: the LTS cutover from vocdoni-node to vochain-sequencer (single sequencer)
   runs and is verified. It is a separate operation and carries none of these rules.
4. Afternoon of 2026-11-02, once the cutover is verified: deploy the vochain-sequencer release with
   the new rules, then saas-backend, back to back. In the minutes between the two, a publish by the
   old saas-backend is rejected cleanly at CheckTx and the idempotent publish retries it, so it
   lands as V3 once the new saas-backend runs.

Rollback: before saas-backend is deployed, redeploy the previous sequencer. The clients are
backward compatible and need no rollback. After V3 elections exist, only rolling forward is
possible.

## Failure modes and security

Threat model: the attacker is a voter (or anyone) who holds legitimate CSP outputs for their own
elections and weights, sees every public key, root and certificate, and controls their client and
proofs. They must not get a valid proof for an election or weight the CSP did not authorize for them.
The CSP root secret and saas-backend are trusted.

- Legacy elections still open after the flag day keep the #769 weakness until they end (accepted, D2).
  No V3 key or certificate is ever related to a legacy key, so a legacy authorization cannot be
  turned into a V3 one, or the other way round.
- Re-targeting across elections: keys of different elections are independent hash outputs, so no
  public δ links them. The certificate binds X to the processId, and the chain builds the message
  from the envelope's processId.
- Re-targeting across weights: `dX` differs per weight with no public relation. The certificate binds
  w, and the chain requires bundle weight == certificate weight.
- Mixing ECDSA and blind roles: `dE`, `dB` and `dX` come from distinct tags. The origin fixes the
  proof type, and certificate and bundle encodings are disjoint.
- Weight encodings: non-canonical big-endian bytes (leading zeros) map to the same integer and are
  covered by the certificate check. A zero, empty or >32-byte weight is rejected.
- Malformed input: wrong lengths, an off-curve X, a recovery byte > 1 after normalisation, a missing
  or extra certificate, or a nil bundle all return typed errors and never panic. The certificate
  signature is copied before recovery (in-place `v` rewrite).
- Partial publish failure: keys depend only on root and question `_id`, so retries reproduce them.
  A question `_id` must back at most one live on-chain election, so publish must be idempotent per
  question. The plan verifies that the existing batch publish (explicit nonces) cannot mint a
  second processId for a question after a lost ack, and adds a guard if it can. Even if a duplicate
  slipped through, each signature and certificate binds its processId, so one authorization still
  yields at most one vote, on exactly one of the duplicates.
- Restart: no new state.
- One-more forgery under a shared key: all voters authorized at weight w in a blind election are
  signed by the same `dX`, and the blind oracle signs any scalar (by design it cannot see
  bundles). ℓ colluding authorized voters holding ℓ concurrent round-1 nonces can attempt a
  ROS/Wagner-style attack for ℓ+1 signatures under `dX`: one extra ballot at weight w on a fresh
  address (practical at about 256 concurrent sessions with the polynomial ROS attack of Benhamouda
  et al. 2020). This cannot cross elections or weights, is a
  property of the blind scheme (non-goal), is unchanged from today's V2 salting, and is accepted as
  residual risk: per-voter blind keys would break vote unlinkability.
- Key compromise: leaking one `dX` or `dB` exposes only that election; leaking the root exposes all
  of them, as today.

## Acceptance criteria

- [ ] AC1 (proto): `ProofCA.weightCertificate`, `CAWeightCertificate` and the two origins exist in
  Go and TS builds; `make` regenerates with no diff.
- [ ] AC2 (sequencer, unit `cspproof`): valid V3_ECDSA and V3_BLIND proofs pass. Each of these is
  rejected: a wrong-election certificate, a wrong-weight certificate, a bundle weight ≠ certificate
  weight, a linearly shifted blind signature (the #769 attack), PIDSALTED types on V3, a certificate
  on V3_ECDSA, a missing certificate on V3_BLIND, a malformed X, a malformed signature, weight 0,
  empty or >32 bytes, and a certificate signed by the wrong key.
- [ ] AC3 (sequencer, `vochain` integration): V3 elections vote end to end, new OFF_CHAIN_CA/V2
  processes are rejected, an OFF_CHAIN_CA/V2 process already in the state (seeded directly, as no
  transaction creates one) accepts its legacy votes, an invalid V3 census root is rejected, and a
  legacy vote whose ProofCA carries a weightCertificate is rejected by CheckTx and DeliverTx.
  `config/forks.go` is untouched.
- [ ] AC4 (shared golden vectors): `testdata/csp-v3-vectors.json` (key derivation, certificate
  message, certificate signature) is byte-identical in saas-backend and vochain-sequencer, and the
  TS testkit tests read the same vectors.
- [ ] AC5 (saas-backend, `api` integration on the sequencer Vocone): ECDSA and blind CSP elections
  publish as V3 with per-question roots and vote successfully. Weighted blind votes carry the
  certificate. Every sign response carries the right `proofType`. A legacy question (empty stored
  origin) signs with `ECDSA_PIDSALTED` or `ECDSA_BLIND_PIDSALTED`, and those signatures verify with
  the vochain-sequencer `OFF_CHAIN_CA`/`OFF_CHAIN_CA_V2` verifiers (`test/sequencer/legacycsp`). A
  legacy `NEW_PROCESS` is rejected by the chain. Two questions get different roots.
- [ ] AC6 (saas-backend): a unit test runs every V3 signing path against a CSP whose salted signer
  is replaced by a stub that fails the test if invoked; the production `go.mod` keeps
  `go.vocdoni.io/proto v1.15.14` and does not require `vochain-sequencer`; `go test ./...`,
  `golangci-lint`, `check-qt-patterns.sh` and swagger regeneration are clean.
- [ ] AC7 (integrator-sdk): unit tests build V3 ECDSA and V3 blind transactions with a faked V3 CSP,
  the proto-decoded `ProofCA` carries the certificate, and legacy flows (no `proofType`) are
  unchanged; `pnpm test`, lint and build are green;
  changesets are present.
- [ ] AC8 (vocdoni-sdk): unit tests cover encoding the certificate, `ECDSA_BLIND` as the type when a
  certificate is present, and the unchanged default without one;
  `yarn test:unit`, lint and build are green.
- [ ] AC9 (vocdoni-app): `pnpm lint`, `pnpm test` and `pnpm build` are green with the bumped
  packages (draft until they are published).

## Constraints

- Conventional commits, no AI attribution or Co-Authored-By trailers. Docs and comments describe
  what is and why, never history.
- One branch name everywhere: `feat/csp-key-per-election`. Draft PRs labeled
  `epic-csp-key-per-election`, with concise bodies.
- Untrusted proof input never panics.
- No secret is logged; derived keys never leave the CSP.

## Open questions

None.
