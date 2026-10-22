# CSP key per election: design

Status: draft   Class: architectural
Epic: vocdoni/saas-backend#769 (label `epic-csp-key-per-election`)
Owner decisions (2026-10-22):
- D1. The chain side is vochain-sequencer (not vocdoni-node).
- D2. Hard cut at a flag day: no legacy CSP signing or voting after it. The flag day is placed in a
  window with no live CSP elections (LTS data: real CSP elections last hours; multi-day windows
  exist). The sequencer still gates the cut on a fork height, because it replays LTS/1.3 history.
- D3. Plain ECDSA CSP elections move to the same per-election-key model, with keys separate from the
  blind ones.
- D4. The weight certificate is an Ethereum-style secp256k1 signature (65 bytes) by the election key.
- D5. saas-backend#779 (every CSP election under OFF_CHAIN_CA_V2) is closed; this epic replaces it.
- D6. All PRs are drafts; nothing is merged, released or deployed without the owner.

## Problem

Blind CSP verification is linear in the public key. The sequencer checks
`sG == F + rx·h(m)·Q` (`go-blindsecp256k1@v0.2.0/blindsecp256k1.go:422`). Every CSP election's
verification key is the CSP root key plus a public scalar: `Q' = Q + salt·G`, with
`salt = processID` (OFF_CHAIN_CA) or `keccak256(processID || weight32)[:20]` (OFF_CHAIN_CA_V2)
(`vochain-sequencer/crypto/saltedkey/saltedkey.go:95,107,124`). A voter holding one valid blind
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
3. The chain verifies CSP proofs against keys it is told, checking a CSP-issued certificate. It never
   derives a key.
4. One coordinated cut: from the fork height on, the sequencer accepts only the new census origins
   for new CSP elections and CSP votes, and saas-backend signs only for them.
5. Every client that builds CSP vote proofs (vocdoni-sdk, vocdoni-integrator-sdk `api-voting` /
   `react-providers`) builds the new proofs; vocdoni-app ships with those packages.

## Non-goals

- Changing the blind signature scheme or the go-blindsecp256k1 library.
- Changing CSP authentication (auth steps, OTP, resend budgets) or the one-signature-per-election
  rule of the blind flow (`saas-backend/db/csp.go:339-429`).
- Changing vocdoni-node, or the standalone `vocdoni/blind-csp` service. vocdoni-sdk only passes the
  certificate through; it does not depend on any CSP producing one.
- Keeping open legacy CSP elections usable after the fork (D2).
- Deleting the salted verification code from the sequencer: replaying history below the fork height
  still needs it.

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
  processID embeds the census origin in byte 26 (`vochain/processid/process_id.go:44-64`). Soft forks
  live in `config/forks.go` (`MetadataFork`, LTS/1.3 at 8_705_150). The REST API maps census types
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
    OFF_CHAIN_CA = 3;          // retired at the CSP key fork
    OFF_CHAIN_CA_V2 = 4;       // retired at the CSP key fork
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
dX = hashToScalar("vocdoni/csp/v3/weight", u256be(dB) || u256be(w))  // blind key for weight w
```

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

### Chain verification (vochain-sequencer, from `CSPKeyFork`)

For a vote on an `OFF_CHAIN_CA_V3_*` election, after the existing bundle address and processId checks:

- Weight: `w = 1` if `bundle.voteWeight` is absent; otherwise `len ≤ 32` and `w = bigEndian(voteWeight) ≥ 1`.
  Anything else is rejected.
- `V3_ECDSA`: `type == ECDSA` and `weightCertificate` absent. The public key recovered from the bundle
  signature must equal `process.CensusRoot` (33 bytes compressed). Weight = `w`.
- `V3_BLIND`: `type == ECDSA_BLIND` and `weightCertificate` present, with `len(weightKey) == 33` and
  `len(signature) == 65`. `X` is decompressed (must be on the curve). The key recovered from
  `certificate.signature` over `certMessage(envelope.ProcessId, w, X)`, working on a copy of the
  signature, must equal `process.CensusRoot`. Then `blind.Verify(keccak256(bundleBytes), sig, X)`
  must hold. Weight = `w`.

`NewProcessTxCheck`, from the fork on: CSP origins must be `V3_ECDSA` or `V3_BLIND`, and the census
root must be a valid 33-byte compressed point. `OFF_CHAIN_CA` and `OFF_CHAIN_CA_V2` are rejected
with `census origin %s retired`. `VerifyProof`, from the fork on: votes on `OFF_CHAIN_CA`/`V2`
processes are rejected with the same error. Below the fork, behaviour is byte-identical to today,
and the V3 origins are rejected at process creation.

`ForksCfg` gains `CSPKeyFork uint32`. Chains not listed activate it from genesis. LTS/1.3 and
DEV/36 get explicit heights; the LTS height is set at flag-day planning (see rollout). The REST API
adds census types `csp3` (`V3_ECDSA`) and `csp3blind` (`V3_BLIND`), reports the new origin names,
and marks `OFF_CHAIN_CA_V2` as deprecated as well.

### saas-backend

- Publish: for each question, compute its root from `dE`/`dB` (blind when the census is anonymous)
  and the question `_id`, and send it as the `CensusRoot` of that question's `NEW_PROCESS` tx with
  origin `V3_ECDSA` or `V3_BLIND`. Store the origin on the question (`censusOrigin`) together with
  `upstreamId`. `census.Published.Root` is no longer a valid verification key for a whole census;
  the per-question root is stored on the question (`censusRoot`).
- ECDSA sign / sign-batch: sign the `CAbundle{processId, address, voteWeight}` with `dE` of that
  question (Ethereum personal-sign, as today). No salt.
- Blind round 1 is unchanged (arm the nonce, pin the weight). Round 2: sign with `dX` for the pinned
  weight and return, per ballot, the blind signature plus `weightKey` (hex X) and `weightCertificate`
  (hex 65-byte signature). The certificate is deterministic and public.
- Questions without a stored V3 `censusOrigin` (published before the cut) are refused on every sign
  path with a new typed error `ErrCSPElectionRetired` (HTTP 410, code in the existing errors table),
  before any nonce or slot is consumed.
- Remove the salted signer (`csp/signers/saltedkey`), the `SignerType*Salted*` types, the V1/V2 salt
  code, and the `go.vocdoni.io/dvote/crypto/saltedkey` import. Fix or close #597 with it.
- The test harness runs the in-process Vocone from `github.com/vocdoni/vochain-sequencer/vocone`, so
  the integration tests publish and vote V3 elections end to end.
- Swagger regenerated (`make swagger`).

### Clients

- vocdoni-integrator-sdk:
  - `@vocdoni/api-types`: `SignBatchResult` gains optional `weightKey` and `weightCertificate` (hex).
  - `@vocdoni/api-voting`: `signBlindCspBallots` returns them; `buildVoteTransaction` accepts an
    optional certificate and puts it in `ProofCA.weightCertificate`. The testkit drops `salt()` and
    gains the derivation and certificate helpers needed to fake a V3 CSP.
  - `@vocdoni/react-providers`: proof type `ECDSA_BLIND` for anonymous censuses and `ECDSA`
    otherwise, passing the certificate through.
  - Changesets: major for `api-voting` and `react-providers` (wire-incompatible), minor for
    `api-types`.
- vocdoni-sdk: `CspSignature`/`CspVote`/`CspCensusProof` carry an optional `weightCertificate
  {weightKey, signature}` that `packageSignedProof` encodes. The default proof type becomes
  `ECDSA_BLIND`. The integration test helper stops salting. Breaking release (0.11.0).
- vocdoni-app: bump the `@vocdoni/*` packages to the releases above; no code change is expected.
- All Go consumers move to `go.vocdoni.io/proto v1.18.0` and all TS consumers to `@vocdoni/proto
  1.18.0`. During development, the PRs pin the proto PR's commit (Go pseudo-version; TS via a git or
  tarball dependency). Before merge, they re-pin to the released 1.18.0.

### Alternatives considered

| Option | Why not |
|---|---|
| Keep the salt and make it a hash of the root (`Q' = Q + H(root‖pid)·G`) | A publicly computable relation still exists once Q' is published, because `Q' − Q` is public, so the linear shift still works. |
| One origin `OFF_CHAIN_CA_V3` with both proof types | It works only because the certificate message and bundle encodings are disjoint. Two origins make "proof type matches origin" a chain rule (issue #769) and separate the processIDs. |
| Random per-election key stored in Mongo | It needs secret storage and backups, and losing the row loses the election. Hash derivation from the root plus the question `_id` needs nothing new. |
| CSP-signed weight inside the blinded bundle | The CSP cannot read a blinded bundle; only the choice of signing key is under its control (issue #769). |
| Per-election transition keeping legacy signing | Rejected by the owner (D2); the LTS data shows windows with no live CSP elections. |

## Compatibility and rollout

| Writer → reader | Result |
|---|---|
| Old client (salted proof) → sequencer after fork | Rejected cleanly: the origin is retired for OFF_CHAIN_CA/V2 elections, and PIDSALTED types are refused on V3 elections. |
| New client (V3 proof) → sequencer before fork | Not applicable: V3 elections cannot exist before the fork, because NewProcess rejects them. |
| Old saas (OFF_CHAIN_CA/V2 publish) → sequencer after fork | Rejected cleanly at CheckTx. |
| New saas (V3 publish) → sequencer before fork | Rejected cleanly at CheckTx. This is why saas switches at the fork. |
| Old proto decoder reading ProofCA with field 4 | The unknown field is ignored. Only old nodes read it, and they are not used after the flag day. |
| New sequencer replaying pre-fork blocks | Byte-identical to today (the salted code is kept under the fork gate). |
| Legacy question → new saas sign | `ErrCSPElectionRetired`, nothing consumed. |

Rollout order (each step needs the owner's OK):
1. Merge and tag dvote-protobuf v1.18.0 (npm + Go).
2. Re-pin consumers to 1.18.0; merge and release vochain-sequencer with `CSPKeyFork` for LTS/1.3 set
   to a height at least 24 h away, inside a window with no live CSP elections (checked with
   `vochain-sequencer/scripts/live-csp-elections.sh`). Deploy it before that height.
3. Release vocdoni-sdk and the integrator-sdk packages (inert until used).
4. At the fork height: pause CSP publication, deploy saas-backend and vocdoni-app (with the new
   packages), and resume.

Rollback: before the fork height, redeploy the previous sequencer, or move the height with a new
release. After V3 elections exist, only rolling forward is possible.

## Failure modes and security

Threat model: the attacker is a voter (or anyone) who holds legitimate CSP outputs for their own
elections and weights, sees every public key, root and certificate, and controls their client and
proofs. They must not get a valid proof for an election or weight the CSP did not authorize for them.
The CSP root secret and saas-backend are trusted.

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
- Restart: no new state.
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
- [ ] AC3 (sequencer, `vochain` integration): below the fork, the existing CSP tests pass unchanged
  and V3 NewProcess is rejected. From the fork, V3 elections vote end to end, OFF_CHAIN_CA/V2
  NewProcess and votes are rejected, and an invalid V3 census root is rejected.
- [ ] AC4 (shared golden vectors): `testdata/csp-v3-vectors.json` (key derivation, certificate
  message, certificate signature) is byte-identical in saas-backend and vochain-sequencer, and the
  TS testkit tests read the same vectors.
- [ ] AC5 (saas-backend, `api` integration on the sequencer Vocone): ECDSA and blind CSP elections
  publish as V3 with per-question roots and vote successfully. Weighted blind votes carry the
  certificate. Legacy questions get `ErrCSPElectionRetired` with nothing consumed. Two questions get
  different roots.
- [ ] AC6 (saas-backend): no reference to `saltedkey`, `V2Salt` or `SignerType*Salted*` remains;
  `go test ./...`, `golangci-lint` and swagger regeneration are clean.
- [ ] AC7 (integrator-sdk): unit tests build V3 ECDSA and V3 blind transactions with a faked V3 CSP,
  and the proto-decoded `ProofCA` carries the certificate; `pnpm test`, lint and build are green;
  changesets are present.
- [ ] AC8 (vocdoni-sdk): unit tests cover encoding the certificate and the default `ECDSA_BLIND`;
  `yarn test:unit`, lint and build are green.
- [ ] AC9 (vocdoni-app): `pnpm lint`, `pnpm test` and `pnpm build` are green with the bumped
  packages (draft until they are published).

## Constraints

- Conventional commits, no AI attribution or Co-Authored-By trailers. Docs and comments describe
  what is and why, never history.
- One branch name everywhere: `feat/csp-key-per-election`. Draft PRs labeled
  `epic-csp-key-per-election`, with concise bodies.
- Pre-fork behaviour of the sequencer is byte-identical. Untrusted proof input never panics.
- No secret is logged; derived keys never leave the CSP.

## Open questions

None. The LTS fork height is a release-time value chosen at flag-day planning, not a design question.
