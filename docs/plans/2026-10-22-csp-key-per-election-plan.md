# CSP key per election: implementation plan

Spec: docs/specs/2026-10-22-csp-key-per-election-design.md (saas-backend, approved at 89d9d646)
Class: architectural
Repos and branches: /home/user/src/vd/dvote-protobuf/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base 679dccc)
Repos and branches: /home/user/src/vd/vochain-sequencer/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base 1fb3211b)
Repos and branches: /home/user/src/vd/saas-backend/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base f784ff11)
Repos and branches: /home/user/src/vd/vocdoni-integrator-sdk/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base d97dafa)
Repos and branches: /home/user/src/vd/vocdoni-sdk/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base 434b3bf)
Repos and branches: /home/user/src/vd/vocdoni-app/.claude/worktrees/csp-key-per-election → feat/csp-key-per-election (base fc83ab20, develop)

Short names used below: `PROTO`, `SEQ`, `SAAS`, `ISDK`, `VSDK`, `APP` for the six worktree paths above.

How to read the code in this plan. Tasks 1 to 8 were written and run on scratch copies whose base
trees are identical to the worktree bases (proto, sequencer, integrator-sdk, vocdoni-sdk: same git
tree hash; saas-backend: identical except docs/ and .delivery/). The diffs below are those scratch
diffs with the dev pins rewritten to the worktree paths, so each one applies with `git apply` from
the repo root (save the fenced block to `$HOME/.cache/csp-epic/patches/tN-<repo>.diff` first) or can
be typed in by hand. Where a step says UNVERIFIED (verified at implementation), the code was not
compiled in that exact intermediate state: the implementer makes it compile and pass, keeping the
behaviour described, and reports any change to the shape.

## Global Constraints

- Conventional commits `type(scope): lowercase summary`. NO AI attribution and NO `Co-Authored-By`
  trailer in any commit. Commit with explicit paths only (`git commit -m ... -- <paths>`); never
  `git add -A`. Never push, never tag, never publish: the controller does those with the owner's OK.
- Build output and temp files go under `$HOME/.cache/csp-epic/` or the project's own target dirs,
  never `/tmp`. Export `TMPDIR=$HOME/.cache/csp-epic/tmp GOTMPDIR=$HOME/.cache/csp-epic/tmp` in every
  shell that builds or tests. Go needs `PATH=$PATH:$(go env GOROOT)/bin` (saas tests exec `go`).
- Docs and code comments describe what is and why. Never history: no "was", "previously", "moved
  from", "since <date>", "no longer" in files. History goes only in commit messages.
- vochain-sequencer: behaviour below `CSPKeyFork` is byte-identical to the base (replaying pre-fork
  blocks gives the same state). Every new branch of consensus code is gated on the fork.
- Untrusted proof input (envelope, ProofCA, certificate, bundle, weight) never panics: typed error,
  no index without a length check, signatures copied before any in-place rewrite.
- No secret is logged or returned: root key, `dE`, `dB`, `dX` never leave the CSP; only public keys,
  certificates and signatures do.
- One canonical golden vector file `csp-v3-vectors.json`
  (sha256 `7ccfd0e0f67ee8f02c33c617da0309475d01732ebdeee9c8f0936fda86a7bac5`), byte-identical in
  SEQ `vochain/transaction/proofs/cspproof/testdata/`, SAAS `csp/signers/electionkey/testdata/` and
  ISDK `packages/api-voting/src/testdata/`. Copy with `cp`, check with `sha256sum`.
- saas-backend AGENTS.md conventions: `fmt.Errorf` (never `errors.New`), lowercase error messages,
  structured zerolog logging, gofumpt, lll 130, more than 3 params or returns → a params struct,
  godoc on every exported symbol, quicktest matchers (`scripts/check-qt-patterns.sh` clean),
  `make swagger` after API type changes. Lint: `~/go/bin/golangci-lint-v2 run ./...` (5 revive
  findings in `db/csp_counters.go` exist on the base and are not ours).
- saas-backend production `go.mod` keeps `go.vocdoni.io/proto v1.15.14` and never requires
  `github.com/vocdoni/vochain-sequencer`. The V3 origins are local constants 5 and 6
  (`electionkey.OriginECDSA` / `OriginBlind`).
- Dev pins (the controller swaps them, implementers do not): SEQ `go.mod` has
  `replace go.vocdoni.io/proto => /home/user/src/vd/dvote-protobuf/.claude/worktrees/csp-key-per-election`;
  SAAS `test/sequencer/go.mod` replaces vochain-sequencer and proto with the SEQ and PROTO worktree
  paths; TS repos depend on `"@vocdoni/proto": "file:/home/user/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz"`.
- Model per task: opus for crypto, chain verification and security paths; sonnet for normal work;
  haiku for mechanical work.

## Review Focus

Line numbers are those of the finished files (scratch), so they hold after the task lands.

- SEQ `vochain/transaction/proofs/cspproof/cspv3.go:44` `Verify`: origin fixes the proof type
  (`:81`), certificate presence per origin, `checkWeightCertificate` (`:148`) builds the message from
  the envelope's processId and the bundle weight, `checkSigner` (`:175`) copies the signature before
  the `v` rewrite. `BundleWeight` (`:107`) rejects 0, empty and >32 bytes.
- SEQ `vochain/transaction/election_tx.go` (NewProcessTxCheck hunk): legacy origins rejected from the
  fork, V3 origins rejected below it, `checkCSPKeyCensusRoot` requires a 33-byte valid point.
- SEQ `vochain/transaction/vote_tx.go` `checkVoteCSPCertificate`: runs outside the vote-cache branch
  so a mempool vote from below the fork is re-checked in a block from it; ignored below the fork.
- SEQ `config/forks.go:31,38`: LTS/1.3 and DEV/36 `CSPKeyFork: math.MaxUint32` placeholders; the LTS
  height is a release-time value (owner OK). `SetCSPKeyForkHeight` (`:57`) refuses chain IDs listed
  in `config.Forks`; `cmd/voconed/voconed.go:396` `applyCSPKeyForkHeight` range check.
- SEQ `vochain/state/censusorigins.go`: V3 origins have `AllowCensusUpdate: false` (a per-election
  key root must never be swapped by SET_PROCESS_CENSUS).
- SAAS `csp/signers/electionkey/electionkey.go:97,130-173,206`: derivation, domain tags, cert
  message layout, RFC6979 signing; must match the vectors bit for bit.
- SAAS `csp/election.go:66` `ElectionCensus`, `:85` `SignBallot`, `:134` `BlindSignBallot`,
  `:188` `signWithWeightKey`: the scheme is chosen only from the stored question origin; a V3
  question never reaches the salted signer; empty origin keeps the legacy path untouched.
- SAAS `csp/sign.go:139` `claimBlindNonce`: one nonce, one signature, shared by both blind paths.
- SAAS `api/processes_publish_orphans.go:52,74,96,153` and `api/processes_publish.go:438-443,574-577,826`:
  idempotent publish per question. An election minted after a lost ack is found by recomputing the
  processId (org, election index, origin, per-question root) and adopted instead of minting a second
  one; a round with unattributed elections waits 2s and retries.
- SAAS `api/processes_admin.go` `questionCensusRoot`: readers of a published census root go through it
  (per-question root for V3, `Published.Root` for legacy).
- ISDK `packages/api-voting/src/csp-proof.ts:46` `cspProofOptions`: refuses results that would build
  a vote the chain rejects (unknown type via `Object.hasOwn`, certificate on a non-BLIND type,
  ECDSA_BLIND without one).

## Spec deviations

1. AC5 (spec:364-369) needs a legacy question signed and voted after the fork on a chain whose
   fork is above genesis; the harness text (spec:241-245) puts the legacy tests on a chain whose fork
   is never reached. Implemented: `TestMain` runs one chain (`test-csp-key`, fork 0);
   `TestCSPLegacyQuestionsAfterFork` starts its own chain `test-csp-legacy` with fork 25, asserts the
   legacy elections are created at height < 25, waits past it and votes (SAAS
   `api/csp_key_per_election_test.go`, `api/api_test.go` TestMain).
2. AC3 (spec:355) "existing CSP tests pass unchanged": because unlisted chains activate the fork at
   genesis (spec:194), the packages whose tests create OFF_CHAIN_CA/V2 processes pin their chain
   below the fork in a new `TestMain` (SEQ `vochain/main_test.go`, `vocone/main_test.go`,
   `api/main_test.go`, `apiclient/main_test.go`); the test bodies are unchanged.
3. spec:196-198 gives LTS/1.3 and DEV/36 explicit heights: SEQ `config/forks.go:31,38` set
   `math.MaxUint32` until the release-time heights are chosen (owner OK).
4. Not in the spec: V3 origins have `AllowCensusUpdate: false` (SEQ `vochain/state/censusorigins.go`),
   so SET_PROCESS_CENSUS cannot swap a per-election key root.
5. Not in the spec (spec:260-263 lists only the certificate for vocdoni-sdk): Task 9 also makes
   vocdoni-sdk read `OFF_CHAIN_CA_V3_*` elections as CSP (`src/types/census/census.ts:82`), or SDK
   readers of saas V3 elections would see an unknown census. Open for the owner: SDK-created CSP
   elections stay `OFF_CHAIN_CA_V2` (`src/core/election.ts:155-162`) and are rejected after the fork.

## Task order and parallelism

| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 proto origins + certificate | – | PROTO `src/vochain/vochain.proto`, `build/` | 5 |
| 2 seq V3 proof verifier + vectors | 1 | SEQ `vochain/transaction/proofs/cspproof/`, `test/testcommon/testcsp/v3.go`, `go.mod`, `go.sum` | 5, 8 |
| 3 seq fork rules | 2 | SEQ `config/`, `vochain/state/`, `vochain/transaction/`, `vochain/*_test.go`, 4× `main_test.go` | 5, 6, 8 |
| 4 seq voconed flag, API, e2e | 3 | SEQ `cmd/`, `api/`, `apiclient/`, `scripts/` | 5, 6, 8 |
| 5 saas electionkey | – (vectors file from 2, or from this plan) | SAAS `csp/signers/electionkey/` | 1-4, 8 |
| 6 saas CSP signing core + handlers | 5 | SAAS `csp/`, `db/types.go`, `docs/swagger.yaml` | 2-4, 8 |
| 7 saas V3 publish + sequencer harness | 4, 6 | SAAS `db/`, `account/`, `api/`, `test/`, `go.mod`, `go.sum` | 8, 9 |
| 8 integrator-sdk | 1 | ISDK `packages/{api-types,api-voting,react-providers}`, `.changeset/` | 2-7, 9 |
| 9 vocdoni-sdk | 1 | VSDK `src/`, `test/unit/`, `CHANGELOG.md` | 2-8 |
| 10 vocdoni-app bump | 8, 9 | APP `package.json`, `pnpm-lock.yaml` | – |

## Tasks

### Task 1: proto census origins V3 and ProofCA weight certificate

Model: sonnet   Repo: PROTO   Files: src/vochain/vochain.proto, build/ (regenerated)

Goal: `OFF_CHAIN_CA_V3_ECDSA = 5`, `OFF_CHAIN_CA_V3_BLIND = 6`, `ProofCA.weightCertificate = 4` and
message `CAWeightCertificate` exist in the Go and TS builds (AC1), and a dev npm tarball exists for
the TS repos.

Step 1 (failing check): the generated code lacks the symbols.

```bash
cd PROTO && grep -c "CensusOrigin_OFF_CHAIN_CA_V3_BLIND\|CAWeightCertificate" build/go/models/vochain.pb.go
```
Expected failure: `0`.

Step 2 (implementation): apply this diff, then regenerate with protoc 3.15.8.

```diff
diff --git a/src/vochain/vochain.proto b/src/vochain/vochain.proto
index d4bdabe..b5e2406 100644
--- a/src/vochain/vochain.proto
+++ b/src/vochain/vochain.proto
@@ -65,6 +65,24 @@ message ProofCA {
 	Type type = 1;
 	CAbundle bundle = 2;
 	bytes signature = 3;
+	// weightCertificate is required on OFF_CHAIN_CA_V3_BLIND elections and
+	// must be absent on every other census origin.
+	CAWeightCertificate weightCertificate = 4;
+}
+
+// CAWeightCertificate binds a blind signing key to one election and one vote
+// weight. The election key (the census root of an OFF_CHAIN_CA_V3_BLIND
+// process) signs the message
+//   "vocdoni/csp/v3/weight-cert" || processId (32 bytes) ||
+//   voteWeight (32 bytes, big-endian) || weightKey (33 bytes)
+// with an Ethereum personal-sign signature.
+message CAWeightCertificate {
+	// weightKey is the 33-byte compressed secp256k1 public key that signed the
+	// blind CAbundle.
+	bytes weightKey = 1;
+	// signature is the 65-byte Ethereum signature by the election key over the
+	// certificate message.
+	bytes signature = 2;
 }
 
 message CAbundle {
@@ -384,14 +402,22 @@ enum CensusOrigin {
 	CENSUS_UNKNOWN = 0;
 	OFF_CHAIN_TREE = 1;
 	OFF_CHAIN_TREE_WEIGHTED = 2;
+	// OFF_CHAIN_CA verifies CSP proofs against the census root shifted by a
+	// public salt. No new processes use it from the CSP key fork on.
 	OFF_CHAIN_CA = 3;
-	// OFF_CHAIN_CA_V2 is OFF_CHAIN_CA with a fixed salt derivation for the
-	// salted proof types (ECDSA_PIDSALTED, ECDSA_BLIND_PIDSALTED): the salt
-	// covers the whole processId and the CSP-authorized vote weight, so a CSP
-	// authorization is bound to one election and one weight. OFF_CHAIN_CA keeps
-	// the legacy derivation, which cropped the processId to 20 bytes and shared
-	// one salted key across every election of an organization.
+	// OFF_CHAIN_CA_V2 is OFF_CHAIN_CA with a salt derived from the whole
+	// processId and the CSP-authorized vote weight. The salt is public, so a
+	// blind signature for one salt can be shifted to another; no new processes
+	// use it from the CSP key fork on.
 	OFF_CHAIN_CA_V2 = 4;
+	// OFF_CHAIN_CA_V3_ECDSA: the census root is a per-election CSP public key
+	// (33 bytes, compressed) and proofs are ProofCA of type ECDSA, signed by it.
+	OFF_CHAIN_CA_V3_ECDSA = 5;
+	// OFF_CHAIN_CA_V3_BLIND: the census root is a per-election CSP public key
+	// that only signs weight certificates. Proofs are ProofCA of type
+	// ECDSA_BLIND, signed by the certified weight key, with a
+	// CAWeightCertificate.
+	OFF_CHAIN_CA_V3_BLIND = 6;
 	// Retired in LTS/1.3: ERC20 = 11, ERC721 = 12, ERC1155 = 13, ERC777 = 14,
 	// MINI_ME = 15, FARCASTER_FRAME = 16. Reserved so their numbers are never
 	// reused; old on-wire values decode as unknown enum values.
```

```bash
cd PROTO && export PATH=$HOME/.cache/csp-epic/tools/protoc-3.15.8/bin:$PATH TMPDIR=$HOME/.cache/csp-epic/tmp
make clean && make init && make all
grep -c "CensusOrigin_OFF_CHAIN_CA_V3_BLIND\|CAWeightCertificate" build/go/models/vochain.pb.go   # > 0
make all && git status --short build/      # second run: no further diff (AC1)
# dev tarball for the TS repos; the version bump is not committed
npm version --no-git-tag-version 1.18.0-dev.csp
mkdir -p $HOME/.cache/csp-epic/pkgs && npm pack --pack-destination $HOME/.cache/csp-epic/pkgs
git checkout -- package.json package-lock.json 2>/dev/null || git checkout -- package.json
ls $HOME/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz
```
Expected pass: grep count > 0, second `make all` leaves no diff, tarball present. The generated
`build/` diff is about 9 files (vochain.pb.go, js index/d.ts bundles).

Step 3: `git commit -m "feat(vochain): add per-election csp census origins and weight certificate" -- src/vochain/vochain.proto build/`

Interfaces this task provides: Go `models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA|_BLIND`,
`models.ProofCA.WeightCertificate *models.CAWeightCertificate{WeightKey, Signature []byte}`;
TS `CensusOrigin.OFF_CHAIN_CA_V3_ECDSA|_BLIND`, `ProofCA.weightCertificate?: CAWeightCertificate`;
tarball `~/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz`.

### Task 2: sequencer V3 CSP proof verifier and golden vectors

Model: opus   Repo: SEQ   Files: go.mod, go.sum, vochain/transaction/proofs/cspproof/cspv3.go,
vochain/transaction/proofs/cspproof/cspv3_test.go, vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json,
test/testcommon/testcsp/v3.go

Goal: `ProofVerifierCSPV3` verifies V3_ECDSA (ECDSA proof recovering to the census root, no
certificate) and V3_BLIND (ECDSA_BLIND under the certified weight key, certificate by the census
root over `"vocdoni/csp/v3/weight-cert" || processId || u256be(w) || X`, w = bundle weight) and
rejects every AC2 case without panicking. Not yet wired into `VerifyProof` (Task 3).

Step 1 (failing test): add the dev pin and the test files first.

```diff
diff --git a/go.mod b/go.mod
index 7518509..66a52e3 100644
--- a/go.mod
+++ b/go.mod
@@ -95,3 +95,5 @@ require (
 	gopkg.in/ini.v1 v1.67.0 // indirect
 	gopkg.in/yaml.v3 v3.0.1 // indirect
 )
+
+replace go.vocdoni.io/proto => /home/user/src/vd/dvote-protobuf/.claude/worktrees/csp-key-per-election
diff --git a/test/testcommon/testcsp/v3.go b/test/testcommon/testcsp/v3.go
new file mode 100644
index 0000000..c715f85
--- /dev/null
+++ b/test/testcommon/testcsp/v3.go
@@ -0,0 +1,129 @@
+package testcsp
+
+import (
+	"fmt"
+	"math/big"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
+	"github.com/vocdoni/vochain-sequencer/types"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+// V3Election is the CSP side of one OFF_CHAIN_CA_V3_ECDSA or
+// OFF_CHAIN_CA_V3_BLIND election: a per-election key whose public key is the
+// census root. On an ECDSA election it signs the CA bundles; on a blind
+// election it only certifies weight keys.
+//
+// The real CSP derives these keys from its root secret (see saas-backend
+// csp/signers/electionkey); the chain only sees public keys, so random keys
+// exercise the verifier the same way.
+type V3Election struct {
+	Key *ethereum.SignKeys
+}
+
+// NewV3Election returns a V3Election with a fresh election key.
+func NewV3Election() (*V3Election, error) {
+	key := ethereum.NewSignKeys()
+	if err := key.Generate(); err != nil {
+		return nil, fmt.Errorf("cannot generate CSP election key: %w", err)
+	}
+	return &V3Election{Key: key}, nil
+}
+
+// CensusRoot returns the value to store in process.CensusRoot: the 33-byte
+// compressed election public key.
+func (e *V3Election) CensusRoot() types.HexBytes {
+	return ethcrypto.CompressPubkey(&e.Key.Public)
+}
+
+// SignECDSAProof returns an OFF_CHAIN_CA_V3_ECDSA proof: bundle signed by the
+// election key, with no weight certificate.
+func (e *V3Election) SignECDSAProof(bundle *models.CAbundle) (*models.Proof, error) {
+	bundleBytes, err := proto.Marshal(bundle)
+	if err != nil {
+		return nil, fmt.Errorf("cannot marshal CSP bundle: %w", err)
+	}
+	signature, err := e.Key.SignEthereum(bundleBytes)
+	if err != nil {
+		return nil, fmt.Errorf("cannot sign CSP bundle: %w", err)
+	}
+	return caProof(&models.ProofCA{Type: models.ProofCA_ECDSA, Bundle: bundle, Signature: signature}), nil
+}
+
+// WeightCredential is a blind weight key and its certificate for one election
+// and one weight.
+type WeightCredential struct {
+	Key         *blind.PrivateKey
+	Certificate *models.CAWeightCertificate
+}
+
+// IssueWeightCredential returns a fresh blind weight key certified by the
+// election key for processID and weight.
+func (e *V3Election) IssueWeightCredential(processID []byte, weight *big.Int) (*WeightCredential, error) {
+	var key *blind.PrivateKey
+	for range blindSignAttempts {
+		k, err := blind.NewPrivateKey()
+		if err == nil {
+			key = k
+			break
+		}
+	}
+	if key == nil {
+		return nil, fmt.Errorf("cannot generate CSP weight key after %d attempts", blindSignAttempts)
+	}
+	weightKey := ethcrypto.CompressPubkey(blindPubToECDSA(key.Public()))
+	cert, err := e.Certify(processID, weight, weightKey)
+	if err != nil {
+		return nil, err
+	}
+	return &WeightCredential{Key: key, Certificate: cert}, nil
+}
+
+// Certify returns the certificate of weightKey for processID and weight,
+// signed by the election key.
+func (e *V3Election) Certify(processID []byte, weight *big.Int, weightKey []byte) (*models.CAWeightCertificate, error) {
+	msg := CertMessage(processID, weight, weightKey)
+	signature, err := e.Key.SignEthereum(msg)
+	if err != nil {
+		return nil, fmt.Errorf("cannot sign CSP weight certificate: %w", err)
+	}
+	return &models.CAWeightCertificate{WeightKey: weightKey, Signature: signature}, nil
+}
+
+// SignBlindProof returns an OFF_CHAIN_CA_V3_BLIND proof: bundle blind-signed
+// by the weight key, carrying its certificate.
+func (c *WeightCredential) SignBlindProof(bundle *models.CAbundle) (*models.Proof, error) {
+	bundleBytes, err := proto.Marshal(bundle)
+	if err != nil {
+		return nil, fmt.Errorf("cannot marshal CSP bundle: %w", err)
+	}
+	sig, err := blindSign(c.Key, bundleBytes)
+	if err != nil {
+		return nil, err
+	}
+	return caProof(&models.ProofCA{
+		Type:              models.ProofCA_ECDSA_BLIND,
+		Bundle:            bundle,
+		Signature:         sig.BytesUncompressed(),
+		WeightCertificate: c.Certificate,
+	}), nil
+}
+
+// CertMessage builds the weight certificate message
+// "vocdoni/csp/v3/weight-cert" || processID || u256be(weight) || weightKey
+// without validating its inputs, so tests can certify malformed values. The
+// verifier builds it independently (cspproof.CertMessage); the shared golden
+// vectors pin both.
+func CertMessage(processID []byte, weight *big.Int, weightKey []byte) []byte {
+	msg := []byte("vocdoni/csp/v3/weight-cert")
+	msg = append(msg, processID...)
+	msg = append(msg, weight.FillBytes(make([]byte, 32))...)
+	return append(msg, weightKey...)
+}
+
+func caProof(p *models.ProofCA) *models.Proof {
+	return &models.Proof{Payload: &models.Proof_Ca{Ca: p}}
+}
diff --git a/vochain/transaction/proofs/cspproof/cspv3_test.go b/vochain/transaction/proofs/cspproof/cspv3_test.go
new file mode 100644
index 0000000..b33bb01
--- /dev/null
+++ b/vochain/transaction/proofs/cspproof/cspv3_test.go
@@ -0,0 +1,574 @@
+package cspproof
+
+import (
+	"bytes"
+	"crypto/ecdsa"
+	"encoding/hex"
+	"encoding/json"
+	"math/big"
+	"os"
+	"testing"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	qt "github.com/frankban/quicktest"
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
+	"github.com/vocdoni/vochain-sequencer/test/testcommon/testcsp"
+	"github.com/vocdoni/vochain-sequencer/util"
+	"github.com/vocdoni/vochain-sequencer/vochain/state"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+const (
+	v3ECDSA = models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA
+	v3Blind = models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND
+)
+
+// verifyV3 runs the V3 verifier the way vochain/transaction.VerifyProof
+// dispatches it.
+func verifyV3(process *models.Process, proof *models.Proof, voter *ethereum.SignKeys) (bool, *big.Int, error) {
+	return (&ProofVerifierCSPV3{}).Verify(process,
+		&models.VoteEnvelope{ProcessId: process.ProcessId, Proof: proof},
+		state.NewVoterID(state.VoterIDTypeECDSA, voter.PublicKey()))
+}
+
+// v3PID returns a random processID with the census origin in byte 26, as
+// vochain/processid lays it out.
+func v3PID(origin models.CensusOrigin) []byte {
+	pid := util.RandomBytes(32)
+	pid[26] = byte(origin)
+	return pid
+}
+
+// blindFixture is a V3 blind election with one voter and a valid credential
+// for weight.
+type blindFixture struct {
+	election *testcsp.V3Election
+	process  *models.Process
+	voter    *ethereum.SignKeys
+	weight   *big.Int
+	cred     *testcsp.WeightCredential
+}
+
+func newBlindFixture(t *testing.T, weight *big.Int) *blindFixture {
+	t.Helper()
+	election, err := testcsp.NewV3Election()
+	qt.Assert(t, err, qt.IsNil)
+	pid := v3PID(v3Blind)
+	cred, err := election.IssueWeightCredential(pid, weight)
+	qt.Assert(t, err, qt.IsNil)
+	return &blindFixture{
+		election: election,
+		process:  newProcess(pid, election.CensusRoot(), v3Blind),
+		voter:    newVoter(t),
+		weight:   weight,
+		cred:     cred,
+	}
+}
+
+func (f *blindFixture) bundle() *models.CAbundle {
+	return testcsp.Bundle(f.process.ProcessId, f.voter.Address().Bytes(), f.weight)
+}
+
+func TestCSPV3ValidProofs(t *testing.T) {
+	t.Run("ecdsa", func(t *testing.T) {
+		election, err := testcsp.NewV3Election()
+		qt.Assert(t, err, qt.IsNil)
+		pid := v3PID(v3ECDSA)
+		voter := newVoter(t)
+		proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), big.NewInt(42)))
+		qt.Assert(t, err, qt.IsNil)
+		valid, weight, err := verifyV3(newProcess(pid, election.CensusRoot(), v3ECDSA), proof, voter)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, valid, qt.IsTrue)
+		qt.Assert(t, weight.Int64(), qt.Equals, int64(42))
+	})
+	t.Run("blind", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(42))
+		proof, err := f.cred.SignBlindProof(f.bundle())
+		qt.Assert(t, err, qt.IsNil)
+		valid, weight, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, valid, qt.IsTrue)
+		qt.Assert(t, weight.Int64(), qt.Equals, int64(42))
+	})
+	t.Run("blind absent weight is one", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(1))
+		bundle := testcsp.Bundle(f.process.ProcessId, f.voter.Address().Bytes(), nil)
+		proof, err := f.cred.SignBlindProof(bundle)
+		qt.Assert(t, err, qt.IsNil)
+		valid, weight, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, valid, qt.IsTrue)
+		qt.Assert(t, weight.Int64(), qt.Equals, int64(1))
+	})
+	t.Run("blind non-canonical weight encoding", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(42))
+		bundle := f.bundle()
+		bundle.VoteWeight = []byte{0x00, 0x00, 0x2a}
+		proof, err := f.cred.SignBlindProof(bundle)
+		qt.Assert(t, err, qt.IsNil)
+		valid, weight, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, valid, qt.IsTrue)
+		qt.Assert(t, weight.Int64(), qt.Equals, int64(42))
+	})
+}
+
+func TestCSPV3WrongElectionCertificateRejected(t *testing.T) {
+	f := newBlindFixture(t, big.NewInt(42))
+	other := v3PID(v3Blind)
+	otherCred, err := f.election.IssueWeightCredential(other, f.weight)
+	qt.Assert(t, err, qt.IsNil)
+	proof, err := otherCred.SignBlindProof(f.bundle())
+	qt.Assert(t, err, qt.IsNil)
+	valid, _, err := verifyV3(f.process, proof, f.voter)
+	qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate: signer .* does not match census root .*")
+	qt.Assert(t, valid, qt.IsFalse)
+}
+
+func TestCSPV3WeightBinding(t *testing.T) {
+	t.Run("certificate for another weight", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(42))
+		cred7, err := f.election.IssueWeightCredential(f.process.ProcessId, big.NewInt(7))
+		qt.Assert(t, err, qt.IsNil)
+		proof, err := cred7.SignBlindProof(f.bundle()) // bundle claims 42
+		qt.Assert(t, err, qt.IsNil)
+		valid, _, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate: signer .* does not match census root .*")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+	t.Run("bundle weight differs from certificate weight", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(42))
+		bundle := f.bundle()
+		bundle.VoteWeight = big.NewInt(43).Bytes()
+		proof, err := f.cred.SignBlindProof(bundle)
+		qt.Assert(t, err, qt.IsNil)
+		valid, _, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate: signer .* does not match census root .*")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+}
+
+// TestCSPV3ShiftedBlindSignatureRejected replays the attack of issue #769: a
+// valid blind signature under X is shifted, with public values only, into one
+// valid under X + δ·G. Without a certificate for X + δ·G it cannot be used.
+func TestCSPV3ShiftedBlindSignatureRejected(t *testing.T) {
+	f := newBlindFixture(t, big.NewInt(42))
+	bundle := f.bundle()
+	proof, err := f.cred.SignBlindProof(bundle)
+	qt.Assert(t, err, qt.IsNil)
+	sig, err := blind.NewSignatureFromBytesUncompressed(proof.GetCa().Signature)
+	qt.Assert(t, err, qt.IsNil)
+
+	n := ethcrypto.S256().Params().N
+	delta := big.NewInt(12345)
+	bundleBytes, err := proto.Marshal(bundle)
+	qt.Assert(t, err, qt.IsNil)
+	m := new(big.Int).SetBytes(ethereum.HashRaw(bundleBytes))
+	h := new(big.Int).SetBytes(ethcrypto.Keccak256(m.Bytes()))
+	rx := new(big.Int).Mod(sig.F.X, n)
+	shift := new(big.Int).Mul(rx, h)
+	shift.Mul(shift, delta)
+	shifted := &blind.Signature{S: new(big.Int).Mod(new(big.Int).Add(sig.S, shift), n), F: sig.F}
+
+	shiftedKeyX, shiftedKeyY := ethcrypto.S256().ScalarBaseMult(delta.FillBytes(make([]byte, 32)))
+	x := f.cred.Key.Public()
+	sumX, sumY := ethcrypto.S256().Add(x.X, x.Y, shiftedKeyX, shiftedKeyY)
+	shiftedPub := &blind.PublicKey{X: sumX, Y: sumY}
+	// the shift is a valid signature under the shifted key: the weakness exists
+	qt.Assert(t, blind.Verify(m, shifted, shiftedPub), qt.IsTrue)
+	shiftedWeightKey := ethcrypto.CompressPubkey(curvePub(sumX, sumY))
+
+	t.Run("with the original certificate", func(t *testing.T) {
+		p := proto.Clone(proof).(*models.Proof)
+		p.GetCa().Signature = shifted.BytesUncompressed()
+		valid, _, err := verifyV3(f.process, p, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "blind CSP verification failed .*")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+	t.Run("claiming the shifted key", func(t *testing.T) {
+		p := proto.Clone(proof).(*models.Proof)
+		p.GetCa().Signature = shifted.BytesUncompressed()
+		p.GetCa().WeightCertificate.WeightKey = shiftedWeightKey
+		valid, _, err := verifyV3(f.process, p, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate: signer .* does not match census root .*")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+}
+
+func TestCSPV3ProofTypeFixedByOrigin(t *testing.T) {
+	election, err := testcsp.NewV3Election()
+	qt.Assert(t, err, qt.IsNil)
+	legacy, err := testcsp.NewSigner()
+	qt.Assert(t, err, qt.IsNil)
+	voter := newVoter(t)
+	for _, origin := range []models.CensusOrigin{v3ECDSA, v3Blind} {
+		for _, proofType := range []models.ProofCA_Type{
+			models.ProofCA_ECDSA_PIDSALTED, models.ProofCA_ECDSA_BLIND_PIDSALTED,
+		} {
+			t.Run(origin.String()+"/"+proofType.String(), func(t *testing.T) {
+				pid := v3PID(origin)
+				proof, err := legacy.SignProof(proofType, testcsp.Bundle(pid, voter.Address().Bytes(), nil), pid)
+				qt.Assert(t, err, qt.IsNil)
+				valid, _, err := verifyV3(newProcess(pid, election.CensusRoot(), origin), proof, voter)
+				qt.Assert(t, err, qt.ErrorMatches, "CSP proof type .* not accepted on .*")
+				qt.Assert(t, valid, qt.IsFalse)
+			})
+		}
+	}
+	t.Run("blind type on V3_ECDSA", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(1))
+		proof, err := f.cred.SignBlindProof(f.bundle())
+		qt.Assert(t, err, qt.IsNil)
+		process := newProcess(f.process.ProcessId, f.election.CensusRoot(), v3ECDSA)
+		valid, _, err := verifyV3(process, proof, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP proof type ECDSA_BLIND not accepted on OFF_CHAIN_CA_V3_ECDSA, expected ECDSA")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+	t.Run("ecdsa type on V3_BLIND", func(t *testing.T) {
+		pid := v3PID(v3Blind)
+		proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), nil))
+		qt.Assert(t, err, qt.IsNil)
+		valid, _, err := verifyV3(newProcess(pid, election.CensusRoot(), v3Blind), proof, voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP proof type ECDSA not accepted on OFF_CHAIN_CA_V3_BLIND, expected ECDSA_BLIND")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+}
+
+func TestCSPV3CertificatePresence(t *testing.T) {
+	t.Run("certificate on V3_ECDSA", func(t *testing.T) {
+		election, err := testcsp.NewV3Election()
+		qt.Assert(t, err, qt.IsNil)
+		pid := v3PID(v3ECDSA)
+		voter := newVoter(t)
+		proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), nil))
+		qt.Assert(t, err, qt.IsNil)
+		cert, err := election.Certify(pid, big.NewInt(1), election.CensusRoot())
+		qt.Assert(t, err, qt.IsNil)
+		proof.GetCa().WeightCertificate = cert
+		valid, _, err := verifyV3(newProcess(pid, election.CensusRoot(), v3ECDSA), proof, voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate not accepted on OFF_CHAIN_CA_V3_ECDSA")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+	t.Run("missing certificate on V3_BLIND", func(t *testing.T) {
+		f := newBlindFixture(t, big.NewInt(1))
+		proof, err := f.cred.SignBlindProof(f.bundle())
+		qt.Assert(t, err, qt.IsNil)
+		proof.GetCa().WeightCertificate = nil
+		valid, _, err := verifyV3(f.process, proof, f.voter)
+		qt.Assert(t, err, qt.ErrorMatches, "CSP weight certificate is missing")
+		qt.Assert(t, valid, qt.IsFalse)
+	})
+}
+
+func TestCSPV3MalformedInputRejected(t *testing.T) {
+	notOnCurve := append([]byte{0x02}, bytes.Repeat([]byte{0xff}, 32)...)
+	for _, tc := range []struct {
+		name   string
+		mutate func(f *blindFixture, ca *models.ProofCA)
+		err    string
+	}{
+		{
+			name: "weight key too short",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) {
+				ca.WeightCertificate.WeightKey = ca.WeightCertificate.WeightKey[:32]
+			},
+			err: "CSP weight key is 32 bytes, expected 33",
+		},
+		{
+			name: "weight key uncompressed",
+			mutate: func(f *blindFixture, ca *models.ProofCA) {
+				x := f.cred.Key.Public()
+				ca.WeightCertificate.WeightKey = ethcrypto.FromECDSAPub(curvePub(x.X, x.Y))
+			},
+			err: "CSP weight key is 65 bytes, expected 33",
+		},
+		{
+			name:   "weight key not on the curve",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.WeightCertificate.WeightKey = notOnCurve },
+			err:    "invalid CSP weight key: .*",
+		},
+		{
+			name:   "weight key bad prefix",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.WeightCertificate.WeightKey[0] = 0x04 },
+			err:    "invalid CSP weight key: .*",
+		},
+		{
+			name: "certificate signature too short",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) {
+				ca.WeightCertificate.Signature = ca.WeightCertificate.Signature[:64]
+			},
+			err: "CSP weight certificate: signature is 64 bytes, expected 65",
+		},
+		{
+			name: "certificate signature too long",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) {
+				ca.WeightCertificate.Signature = append(ca.WeightCertificate.Signature, 0)
+			},
+			err: "CSP weight certificate: signature is 66 bytes, expected 65",
+		},
+		{
+			name:   "certificate recovery byte out of range",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.WeightCertificate.Signature[64] = 5 },
+			err:    "CSP weight certificate: cannot recover signer: bad recover ID byte",
+		},
+		{
+			name:   "certificate signature zeroed",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.WeightCertificate.Signature = make([]byte, 65) },
+			err:    "CSP weight certificate: cannot recover signer: .*",
+		},
+		{
+			name: "certificate signed by another key",
+			mutate: func(f *blindFixture, ca *models.ProofCA) {
+				other, err := testcsp.NewV3Election()
+				if err != nil {
+					panic(err)
+				}
+				cert, err := other.Certify(f.process.ProcessId, f.weight, ca.WeightCertificate.WeightKey)
+				if err != nil {
+					panic(err)
+				}
+				ca.WeightCertificate = cert
+			},
+			err: "CSP weight certificate: signer .* does not match census root .*",
+		},
+		{
+			name:   "blind signature truncated",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Signature = ca.Signature[:95] },
+			err:    "cannot parse blind CSP signature: .*",
+		},
+		{
+			name:   "blind signature garbage",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Signature = bytes.Repeat([]byte{0xff}, 96) },
+			err:    "cannot parse blind CSP signature: .*",
+		},
+		{
+			name:   "weight zero",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Bundle.VoteWeight = []byte{0} },
+			err:    "CSP bundle vote weight is zero",
+		},
+		{
+			name:   "weight empty",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Bundle.VoteWeight = []byte{} },
+			err:    "CSP bundle vote weight is empty",
+		},
+		{
+			name:   "weight longer than 32 bytes",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Bundle.VoteWeight = append(make([]byte, 32), 1) },
+			err:    "CSP bundle vote weight is 33 bytes, at most 32 allowed",
+		},
+		{
+			name:   "nil bundle",
+			mutate: func(_ *blindFixture, ca *models.ProofCA) { ca.Bundle = nil },
+			err:    "CSP proof or bundle are nil",
+		},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			f := newBlindFixture(t, big.NewInt(42))
+			proof, err := f.cred.SignBlindProof(f.bundle())
+			qt.Assert(t, err, qt.IsNil)
+			tc.mutate(f, proof.GetCa())
+			valid, weight, err := verifyV3(f.process, proof, f.voter)
+			qt.Assert(t, err, qt.ErrorMatches, tc.err)
+			qt.Assert(t, valid, qt.IsFalse)
+			qt.Assert(t, weight, qt.IsNil)
+		})
+	}
+}
+
+func TestCSPV3ECDSAMalformedInputRejected(t *testing.T) {
+	for _, tc := range []struct {
+		name   string
+		mutate func(process *models.Process, ca *models.ProofCA)
+		err    string
+	}{
+		{
+			name:   "signature too short",
+			mutate: func(_ *models.Process, ca *models.ProofCA) { ca.Signature = ca.Signature[:64] },
+			err:    "CSP bundle signature: signature is 64 bytes, expected 65",
+		},
+		{
+			name:   "signature with trailing bytes",
+			mutate: func(_ *models.Process, ca *models.ProofCA) { ca.Signature = append(ca.Signature, 1, 2) },
+			err:    "CSP bundle signature: signature is 67 bytes, expected 65",
+		},
+		{
+			name:   "census root uncompressed",
+			mutate: func(p *models.Process, _ *models.ProofCA) { p.CensusRoot = append(p.CensusRoot, make([]byte, 32)...) },
+			err:    "CSP bundle signature: census root is 65 bytes, expected 33",
+		},
+		{
+			name:   "weight zero",
+			mutate: func(_ *models.Process, ca *models.ProofCA) { ca.Bundle.VoteWeight = []byte{0, 0} },
+			err:    "CSP bundle vote weight is zero",
+		},
+		{
+			name: "signed by another key",
+			mutate: func(p *models.Process, _ *models.ProofCA) {
+				other, err := testcsp.NewV3Election()
+				if err != nil {
+					panic(err)
+				}
+				p.CensusRoot = other.CensusRoot()
+			},
+			err: "CSP bundle signature: signer .* does not match census root .*",
+		},
+	} {
+		t.Run(tc.name, func(t *testing.T) {
+			election, err := testcsp.NewV3Election()
+			qt.Assert(t, err, qt.IsNil)
+			pid := v3PID(v3ECDSA)
+			voter := newVoter(t)
+			proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), big.NewInt(3)))
+			qt.Assert(t, err, qt.IsNil)
+			process := newProcess(pid, election.CensusRoot(), v3ECDSA)
+			tc.mutate(process, proof.GetCa())
+			valid, _, err := verifyV3(process, proof, voter)
+			qt.Assert(t, err, qt.ErrorMatches, tc.err)
+			qt.Assert(t, valid, qt.IsFalse)
+		})
+	}
+}
+
+// TestCSPV3SignatureNotMutated checks that verification leaves the
+// transaction's signature bytes untouched, although ethereum.PubKeyFromSignature
+// rewrites the recovery byte of what it is given.
+func TestCSPV3SignatureNotMutated(t *testing.T) {
+	election, err := testcsp.NewV3Election()
+	qt.Assert(t, err, qt.IsNil)
+	pid := v3PID(v3ECDSA)
+	voter := newVoter(t)
+	proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), nil))
+	qt.Assert(t, err, qt.IsNil)
+	proof.GetCa().Signature[64] += 27
+	before := bytes.Clone(proof.GetCa().Signature)
+	process := newProcess(pid, election.CensusRoot(), v3ECDSA)
+	for range 2 {
+		valid, _, err := verifyV3(process, proof, voter)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, valid, qt.IsTrue)
+		qt.Assert(t, proof.GetCa().Signature, qt.DeepEquals, before)
+	}
+}
+
+func TestCSPV3RejectsOtherOrigins(t *testing.T) {
+	election, err := testcsp.NewV3Election()
+	qt.Assert(t, err, qt.IsNil)
+	pid := v3PID(v3ECDSA)
+	voter := newVoter(t)
+	proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), nil))
+	qt.Assert(t, err, qt.IsNil)
+	valid, _, err := verifyV3(newProcess(pid, election.CensusRoot(), v2Origin), proof, voter)
+	qt.Assert(t, err, qt.ErrorMatches, "census origin OFF_CHAIN_CA_V2 is not a V3 CSP origin")
+	qt.Assert(t, valid, qt.IsFalse)
+}
+
+// curvePub wraps curve coordinates for the go-ethereum serialization helpers.
+func curvePub(x, y *big.Int) *ecdsa.PublicKey {
+	return &ecdsa.PublicKey{Curve: ethcrypto.S256(), X: x, Y: y}
+}
+
+// v3Vectors is the subset of testdata/csp-v3-vectors.json the verifier checks:
+// certificate messages and complete proofs. The file is generated by
+// saas-backend (csp/signers/electionkey) and must stay byte-identical to its
+// copy there.
+type v3Vectors struct {
+	Voter struct {
+		Public string `json:"public"`
+	} `json:"voter"`
+	Elections []struct {
+		ProcessIDBlind string `json:"processIdBlind"`
+		ElectionBlind  struct {
+			Public string `json:"public"`
+		} `json:"electionBlindKey"`
+		Weights []struct {
+			Weight    string `json:"weight"`
+			WeightKey struct {
+				Public string `json:"public"`
+			} `json:"weightKey"`
+			CertMessage   string `json:"certMessage"`
+			CertSignature string `json:"certSignature"`
+		} `json:"weights"`
+	} `json:"elections"`
+	Proofs []struct {
+		Name          string `json:"name"`
+		CensusOrigin  string `json:"censusOrigin"`
+		CensusRoot    string `json:"censusRoot"`
+		ProcessID     string `json:"processId"`
+		Address       string `json:"address"`
+		VoteWeight    string `json:"voteWeight"`
+		Weight        string `json:"weight"`
+		Bundle        string `json:"bundle"`
+		ProofType     string `json:"proofType"`
+		Signature     string `json:"signature"`
+		WeightKey     string `json:"weightKey"`
+		CertSignature string `json:"certSignature"`
+	} `json:"proofs"`
+}
+
+func mustHex(t *testing.T, s string) []byte {
+	t.Helper()
+	b, err := hex.DecodeString(s)
+	qt.Assert(t, err, qt.IsNil)
+	return b
+}
+
+func TestCSPV3GoldenVectors(t *testing.T) {
+	data, err := os.ReadFile("testdata/csp-v3-vectors.json")
+	qt.Assert(t, err, qt.IsNil)
+	var v v3Vectors
+	qt.Assert(t, json.Unmarshal(data, &v), qt.IsNil)
+	qt.Assert(t, v.Elections, qt.Not(qt.HasLen), 0)
+	qt.Assert(t, v.Proofs, qt.HasLen, 3)
+
+	for _, e := range v.Elections {
+		for _, w := range e.Weights {
+			weight, ok := new(big.Int).SetString(w.Weight, 10)
+			qt.Assert(t, ok, qt.IsTrue)
+			msg, err := CertMessage(mustHex(t, e.ProcessIDBlind), weight, mustHex(t, w.WeightKey.Public))
+			qt.Assert(t, err, qt.IsNil)
+			qt.Assert(t, hex.EncodeToString(msg), qt.Equals, w.CertMessage)
+			qt.Assert(t, checkSigner(msg, mustHex(t, w.CertSignature), mustHex(t, e.ElectionBlind.Public)), qt.IsNil)
+		}
+	}
+
+	voterID := state.NewVoterID(state.VoterIDTypeECDSA, mustHex(t, v.Voter.Public))
+	for _, pv := range v.Proofs {
+		t.Run(pv.Name, func(t *testing.T) {
+			bundle := &models.CAbundle{ProcessId: mustHex(t, pv.ProcessID), Address: mustHex(t, pv.Address)}
+			if pv.VoteWeight != "" {
+				bundle.VoteWeight = mustHex(t, pv.VoteWeight)
+			}
+			bundleBytes, err := proto.Marshal(bundle)
+			qt.Assert(t, err, qt.IsNil)
+			qt.Assert(t, hex.EncodeToString(bundleBytes), qt.Equals, pv.Bundle)
+
+			ca := &models.ProofCA{
+				Type:      models.ProofCA_Type(models.ProofCA_Type_value[pv.ProofType]),
+				Bundle:    bundle,
+				Signature: mustHex(t, pv.Signature),
+			}
+			if pv.WeightKey != "" {
+				ca.WeightCertificate = &models.CAWeightCertificate{
+					WeightKey: mustHex(t, pv.WeightKey),
+					Signature: mustHex(t, pv.CertSignature),
+				}
+			}
+			// round-trip through the wire encoding, as a vote transaction does
+			proofBytes, err := proto.Marshal(&models.Proof{Payload: &models.Proof_Ca{Ca: ca}})
+			qt.Assert(t, err, qt.IsNil)
+			proof := &models.Proof{}
+			qt.Assert(t, proto.Unmarshal(proofBytes, proof), qt.IsNil)
+
+			origin := models.CensusOrigin(models.CensusOrigin_value[pv.CensusOrigin])
+			process := newProcess(bundle.ProcessId, mustHex(t, pv.CensusRoot), origin)
+			valid, weight, err := (&ProofVerifierCSPV3{}).Verify(process,
+				&models.VoteEnvelope{ProcessId: bundle.ProcessId, Proof: proof}, voterID)
+			qt.Assert(t, err, qt.IsNil)
+			qt.Assert(t, valid, qt.IsTrue)
+			qt.Assert(t, weight.String(), qt.Equals, pv.Weight)
+		})
+	}
+}
diff --git a/vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json b/vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json
new file mode 100644
index 0000000..436e5a1
--- /dev/null
+++ b/vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json
@@ -0,0 +1,136 @@
+{
+  "description": "CSP per-election keys (OFF_CHAIN_CA_V3_*): derivation from a test root, weight certificates and complete valid proofs. Generated by saas-backend csp/signers/electionkey (go test -run TestVectors -update); copies must stay byte-identical.",
+  "root": "8f1d3c5e7a9b0c2d4e6f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5",
+  "voter": {
+    "private": "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318",
+    "public": "024e3b81af9c2234cad09d679ce6035ed1392347ce64ce405f5dcd36228a25de6e"
+  },
+  "elections": [
+    {
+      "seed": "65f0a1b2c3d4e5f60718293a",
+      "processIdEcdsa": "c0ffee0000010000000000000000000000000000000000aa0500000000000001",
+      "processIdBlind": "c0ffee0000010000000000000000000000000000000000aa0600000000000001",
+      "electionEcdsaKey": {
+        "private": "49b0e84afa2d319f9bc9902a2909a0f2084b644a1f1e446f8b9f4a038c3897b8",
+        "public": "02886e8c4a05adc805cefa23b8268e764c79d653d197aa450acb1df09613f946bd"
+      },
+      "electionBlindKey": {
+        "private": "6eeae3ecfa27d7c5331416100e79be3986478d75f5786376d39664e7282e62ba",
+        "public": "02615d4bbd02fd3e072ce58b1dfc27b1291380a86250cbe4feeec0c8b2f20aeb04"
+      },
+      "weights": [
+        {
+          "weight": "1",
+          "weightKey": {
+            "private": "ae6b909e5c9e0b442094a8abbb8bc5319e97a5fc657ac5ed641657a92c8369d1",
+            "public": "02bee792d9c39f9224b915e898c3783e64b0da3fcd8b471e002b851926544b8e73"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa0600000000000001000000000000000000000000000000000000000000000000000000000000000102bee792d9c39f9224b915e898c3783e64b0da3fcd8b471e002b851926544b8e73",
+          "certSignature": "129e2028ada9a8794085222e83e5eaeb7f481db8c33e1286b115f6f9af4866dd0ed96e7cf67b20efad5c05084fee0db134370601adc4fc0235674bea8ea42b6e00"
+        },
+        {
+          "weight": "42",
+          "weightKey": {
+            "private": "6648cf1f6ffcb04ae6398316f5893b9d21bf18481fbc6266f2afed3753b4f4e4",
+            "public": "036bdd5ad9ac943bc6ade069813e165ee584b830a1d3e6d708d8ade16a407caa1f"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa0600000000000001000000000000000000000000000000000000000000000000000000000000002a036bdd5ad9ac943bc6ade069813e165ee584b830a1d3e6d708d8ade16a407caa1f",
+          "certSignature": "77b68d845c2a049644ff6135abd9cdaa52d94f1683c8e5ec311642d2324bd1351c2aedcb2708ac22e543ac47ebd37bc43bf3e0653c55e3dbaf69629aa3fe0cc800"
+        },
+        {
+          "weight": "340282366920938463463374607431768211455",
+          "weightKey": {
+            "private": "4296a03c3315cb3c94da1f7397281fd837b2bcf8e9ad61775a8a7eb1c6e9c551",
+            "public": "03cd846dd3b88924a933116e31e1bdfa359813826c17f09dc3e73a6efac0c582f0"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa060000000000000100000000000000000000000000000000ffffffffffffffffffffffffffffffff03cd846dd3b88924a933116e31e1bdfa359813826c17f09dc3e73a6efac0c582f0",
+          "certSignature": "0a7d19f64eae64d048efb2ec5cb76aa92af2229212b7b6859870eee38f6cb7fb0e2d4eb56abbe0980331ede456b3160b92c7951153e10da443db2e692316c9d401"
+        }
+      ]
+    },
+    {
+      "seed": "65f0a1b2c3d4e5f60718293b",
+      "processIdEcdsa": "c0ffee0000010000000000000000000000000000000000aa0500000000000002",
+      "processIdBlind": "c0ffee0000010000000000000000000000000000000000aa0600000000000002",
+      "electionEcdsaKey": {
+        "private": "734c50ae5f9ab2a9cdfa3ddd12674f861a763d80e15e774f714e7bb2c8617da1",
+        "public": "0399343c2c2f6c087f976d9cd71fe304766a184376a10b4030a1ac7895734af5cd"
+      },
+      "electionBlindKey": {
+        "private": "8cae10384b7bfaf9530dd4e7ae4bb84a497008de84be78389c04d95742b07120",
+        "public": "02fa0e1557298b3eecaaf40c44d93b79d5a7acc1803ae238048c7242f65a0c5ff3"
+      },
+      "weights": [
+        {
+          "weight": "1",
+          "weightKey": {
+            "private": "efb1f9357e7af471772900d42744111aebd13418a46795fbbce56b8ed05bee38",
+            "public": "03b99cc6a5e703c8eb41560f4d49d28048861c6fcaacd1173ef2b15ded78692a37"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa0600000000000002000000000000000000000000000000000000000000000000000000000000000103b99cc6a5e703c8eb41560f4d49d28048861c6fcaacd1173ef2b15ded78692a37",
+          "certSignature": "dba9233b6c9219bc8ad23e68d6fd1fb634a5d9c4910ca35053f11d8bce718f2b6ed331121179b5d504a48001e81e1abe9a0692b03be9773e35f8ff32d5ba51d101"
+        },
+        {
+          "weight": "42",
+          "weightKey": {
+            "private": "1c6007fbf26233132297ae23e9ef947dbab5ed6e27843506f429ad2483369d94",
+            "public": "02e8ec49c968211d955e94a7f548cf033b84df6f7d24fe0d4bbdb0275d5d7220cf"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa0600000000000002000000000000000000000000000000000000000000000000000000000000002a02e8ec49c968211d955e94a7f548cf033b84df6f7d24fe0d4bbdb0275d5d7220cf",
+          "certSignature": "bbc4844a6815d23aa02b934d2febc10ea09a6e6b14e94db6f76080ba3f214e6c4da31d0c4a650138f64e355c002f13ca1f7c174b95a75510211bee385c5b1aa301"
+        },
+        {
+          "weight": "340282366920938463463374607431768211455",
+          "weightKey": {
+            "private": "2a0028427cbb7046ec87efe83cd1b7f974ffe42d93e997fe495ebfdeacc7c67e",
+            "public": "0281cc733d01d98285cef173cc3508005e886d249cb7248636a117f5e8c09acbce"
+          },
+          "certMessage": "766f63646f6e692f6373702f76332f7765696768742d63657274c0ffee0000010000000000000000000000000000000000aa060000000000000200000000000000000000000000000000ffffffffffffffffffffffffffffffff0281cc733d01d98285cef173cc3508005e886d249cb7248636a117f5e8c09acbce",
+          "certSignature": "261a467ae32e783c66258bc640338d4a8acf514237fb22d67e86a11c57480e5d4f689b085c0575a58bee8121a291711c8bb2ad2f20c9abbc1299c526d80d5c0a01"
+        }
+      ]
+    }
+  ],
+  "proofs": [
+    {
+      "name": "ecdsa weight 42",
+      "censusOrigin": "OFF_CHAIN_CA_V3_ECDSA",
+      "censusRoot": "02886e8c4a05adc805cefa23b8268e764c79d653d197aa450acb1df09613f946bd",
+      "processId": "c0ffee0000010000000000000000000000000000000000aa0500000000000001",
+      "address": "2c7536e3605d9c16a7a3d7b1898e529396a65c23",
+      "voteWeight": "2a",
+      "weight": "42",
+      "bundle": "0a20c0ffee0000010000000000000000000000000000000000aa050000000000000112142c7536e3605d9c16a7a3d7b1898e529396a65c231a012a",
+      "proofType": "ECDSA",
+      "signature": "4cfe08ba5125dc4e023a7e4e9aa4d004943164b50ac41faa976f3b2befda37bb721bfff860ab8855f53ccbbd037d0876d40b0a8ef46377161807c86afbe395b201"
+    },
+    {
+      "name": "blind weight 42",
+      "censusOrigin": "OFF_CHAIN_CA_V3_BLIND",
+      "censusRoot": "02615d4bbd02fd3e072ce58b1dfc27b1291380a86250cbe4feeec0c8b2f20aeb04",
+      "processId": "c0ffee0000010000000000000000000000000000000000aa0600000000000001",
+      "address": "2c7536e3605d9c16a7a3d7b1898e529396a65c23",
+      "voteWeight": "2a",
+      "weight": "42",
+      "bundle": "0a20c0ffee0000010000000000000000000000000000000000aa060000000000000112142c7536e3605d9c16a7a3d7b1898e529396a65c231a012a",
+      "proofType": "ECDSA_BLIND",
+      "signature": "5f09d0f022bea5ec7bcb4e7ff1f4636f6f8913c5575c14d824c1c05e8afdf1c347e470e69cee0f1d5f2f7b8a1e47398fe7e6fb4f0542b140d6ca8e6a81d99273423b678080c753b53f15292f1cbf66cb3e10cc02280c86140b4934f240d72f48",
+      "weightKey": "036bdd5ad9ac943bc6ade069813e165ee584b830a1d3e6d708d8ade16a407caa1f",
+      "certSignature": "77b68d845c2a049644ff6135abd9cdaa52d94f1683c8e5ec311642d2324bd1351c2aedcb2708ac22e543ac47ebd37bc43bf3e0653c55e3dbaf69629aa3fe0cc800"
+    },
+    {
+      "name": "blind default weight",
+      "censusOrigin": "OFF_CHAIN_CA_V3_BLIND",
+      "censusRoot": "02615d4bbd02fd3e072ce58b1dfc27b1291380a86250cbe4feeec0c8b2f20aeb04",
+      "processId": "c0ffee0000010000000000000000000000000000000000aa0600000000000001",
+      "address": "2c7536e3605d9c16a7a3d7b1898e529396a65c23",
+      "voteWeight": "",
+      "weight": "1",
+      "bundle": "0a20c0ffee0000010000000000000000000000000000000000aa060000000000000112142c7536e3605d9c16a7a3d7b1898e529396a65c23",
+      "proofType": "ECDSA_BLIND",
+      "signature": "66c8295d725710632ed66403b2c171780966c6cbfec6f27cd2134eaa338b9d6412e4568841b62e547614d306d5c55c49ae231a374a6f892a2a4ba08e15a0b73ee2c877c2cb343b62798f6f31f6c556b9af5777986d832218bb95641d6f4ed70a",
+      "weightKey": "02bee792d9c39f9224b915e898c3783e64b0da3fcd8b471e002b851926544b8e73",
+      "certSignature": "129e2028ada9a8794085222e83e5eaeb7f481db8c33e1286b115f6f9af4866dd0ed96e7cf67b20efad5c05084fee0db134370601adc4fc0235674bea8ea42b6e00"
+    }
+  ]
+}
```

```bash
cd SEQ && export TMPDIR=$HOME/.cache/csp-epic/tmp GOTMPDIR=$HOME/.cache/csp-epic/tmp
go mod tidy   # drops the go.vocdoni.io/proto v1.17.0 sums (2 lines) for the local replace
sha256sum vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json   # 7ccfd0e0...a7bac5
go test ./vochain/transaction/proofs/cspproof/ -run 'CSPV3' -count=1
```
Expected failure: build error `undefined: ProofVerifierCSPV3` / `undefined: BundleWeight`.

Step 2 (implementation):

```diff
diff --git a/vochain/transaction/proofs/cspproof/cspv3.go b/vochain/transaction/proofs/cspproof/cspv3.go
new file mode 100644
index 0000000..4f858b1
--- /dev/null
+++ b/vochain/transaction/proofs/cspproof/cspv3.go
@@ -0,0 +1,190 @@
+package cspproof
+
+import (
+	"bytes"
+	"fmt"
+	"math/big"
+
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
+	"github.com/vocdoni/vochain-sequencer/vochain/state"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+const (
+	// CertTag prefixes every weight certificate message. Its first byte (0x76)
+	// has protobuf wire type 6, so a certificate message is never a valid
+	// CAbundle encoding and a certificate signature never verifies as a bundle
+	// signature.
+	CertTag = "vocdoni/csp/v3/weight-cert"
+	// processIDSize is the size of an election id inside a certificate message.
+	processIDSize = 32
+	// weightSize is the fixed width of a vote weight inside a certificate
+	// message, and the largest voteWeight a V3 bundle may carry.
+	weightSize = 32
+	// compressedKeySize is the size of a compressed secp256k1 public key: the
+	// census root of a V3 election and a certified weight key.
+	compressedKeySize = 33
+	// CertMessageSize is the size of a weight certificate message.
+	CertMessageSize = len(CertTag) + processIDSize + weightSize + compressedKeySize
+)
+
+// ProofVerifierCSPV3 verifies CSP proofs on OFF_CHAIN_CA_V3_ECDSA and
+// OFF_CHAIN_CA_V3_BLIND elections. The census root is a per-election CSP
+// public key that the chain uses as given; it never derives or salts a key.
+//
+// OFF_CHAIN_CA_V3_ECDSA proofs are ProofCA of type ECDSA, signed by the census
+// root key. OFF_CHAIN_CA_V3_BLIND proofs are ProofCA of type ECDSA_BLIND,
+// signed by a weight key that the census root key certifies for this election
+// and this vote weight.
+type ProofVerifierCSPV3 struct{}
+
+// Verify checks a V3 CSP proof and returns the vote weight it authorizes.
+func (*ProofVerifierCSPV3) Verify(process *models.Process, envelope *models.VoteEnvelope,
+	vID state.VoterID,
+) (bool, *big.Int, error) {
+	p := envelope.GetProof().GetCa()
+	if p == nil || p.Bundle == nil {
+		return false, nil, fmt.Errorf("CSP proof or bundle are nil")
+	}
+	if !bytes.Equal(p.Bundle.Address, vID.Address()) {
+		return false, nil, fmt.Errorf(
+			"CSP bundle address and key do not match: %x != %x", vID.Address(), p.Bundle.Address,
+		)
+	}
+	if !bytes.Equal(p.Bundle.ProcessId, envelope.ProcessId) {
+		return false, nil, fmt.Errorf("CSP bundle processID does not match")
+	}
+	weight, err := BundleWeight(p.Bundle.GetVoteWeight())
+	if err != nil {
+		return false, nil, err
+	}
+	cspBundle, err := proto.Marshal(p.Bundle)
+	if err != nil {
+		return false, nil, fmt.Errorf("cannot marshal CSP bundle to protobuf: %w", err)
+	}
+
+	switch process.CensusOrigin {
+	case models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA:
+		if p.GetType() != models.ProofCA_ECDSA {
+			return false, nil, fmt.Errorf("CSP proof type %s not accepted on %s, expected %s",
+				p.GetType(), process.CensusOrigin, models.ProofCA_ECDSA)
+		}
+		if p.GetWeightCertificate() != nil {
+			return false, nil, fmt.Errorf("CSP weight certificate not accepted on %s", process.CensusOrigin)
+		}
+		if err := checkSigner(cspBundle, p.GetSignature(), process.CensusRoot); err != nil {
+			return false, nil, fmt.Errorf("CSP bundle signature: %w", err)
+		}
+	case models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:
+		if p.GetType() != models.ProofCA_ECDSA_BLIND {
+			return false, nil, fmt.Errorf("CSP proof type %s not accepted on %s, expected %s",
+				p.GetType(), process.CensusOrigin, models.ProofCA_ECDSA_BLIND)
+		}
+		weightKey, err := checkWeightCertificate(p.GetWeightCertificate(), envelope.ProcessId, weight,
+			process.CensusRoot)
+		if err != nil {
+			return false, nil, err
+		}
+		signature, err := blind.NewSignatureFromBytesUncompressed(p.GetSignature())
+		if err != nil {
+			return false, nil, fmt.Errorf("cannot parse blind CSP signature: %w", err)
+		}
+		if !blind.Verify(new(big.Int).SetBytes(ethereum.HashRaw(cspBundle)), signature, weightKey) {
+			return false, nil, fmt.Errorf("blind CSP verification failed for pid %x with weight key %x",
+				envelope.ProcessId, p.GetWeightCertificate().GetWeightKey())
+		}
+	default:
+		return false, nil, fmt.Errorf("census origin %s is not a V3 CSP origin", process.CensusOrigin)
+	}
+	return true, weight, nil
+}
+
+// BundleWeight returns the vote weight a V3 CA bundle authorizes: 1 when
+// voteWeight is absent, otherwise its big-endian value, which must be at least
+// 1 and at most 32 bytes long. An empty or zero voteWeight is rejected.
+func BundleWeight(voteWeight []byte) (*big.Int, error) {
+	if voteWeight == nil {
+		return big.NewInt(1), nil
+	}
+	if len(voteWeight) == 0 {
+		return nil, fmt.Errorf("CSP bundle vote weight is empty")
+	}
+	if len(voteWeight) > weightSize {
+		return nil, fmt.Errorf("CSP bundle vote weight is %d bytes, at most %d allowed", len(voteWeight), weightSize)
+	}
+	w := new(big.Int).SetBytes(voteWeight)
+	if w.Sign() == 0 {
+		return nil, fmt.Errorf("CSP bundle vote weight is zero")
+	}
+	return w, nil
+}
+
+// CertMessage returns the message a weight certificate signs:
+// CertTag || processID (32 bytes) || weight (32 bytes, big-endian) ||
+// weightKey (33 bytes, compressed).
+func CertMessage(processID []byte, weight *big.Int, weightKey []byte) ([]byte, error) {
+	if len(processID) != processIDSize {
+		return nil, fmt.Errorf("process id is %d bytes, expected %d", len(processID), processIDSize)
+	}
+	if len(weightKey) != compressedKeySize {
+		return nil, fmt.Errorf("weight key is %d bytes, expected %d", len(weightKey), compressedKeySize)
+	}
+	if weight == nil || weight.Sign() <= 0 || weight.BitLen() > 8*weightSize {
+		return nil, fmt.Errorf("weight out of range")
+	}
+	msg := make([]byte, 0, CertMessageSize)
+	msg = append(msg, CertTag...)
+	msg = append(msg, processID...)
+	msg = append(msg, weight.FillBytes(make([]byte, weightSize))...)
+	msg = append(msg, weightKey...)
+	return msg, nil
+}
+
+// checkWeightCertificate checks that cert certifies its weight key for
+// processID and weight, signed by the census root key, and returns the weight
+// key as a blind public key.
+func checkWeightCertificate(cert *models.CAWeightCertificate, processID []byte, weight *big.Int,
+	censusRoot []byte,
+) (*blind.PublicKey, error) {
+	if cert == nil {
+		return nil, fmt.Errorf("CSP weight certificate is missing")
+	}
+	if len(cert.GetWeightKey()) != compressedKeySize {
+		return nil, fmt.Errorf("CSP weight key is %d bytes, expected %d", len(cert.GetWeightKey()), compressedKeySize)
+	}
+	weightKey, err := blind.NewPublicKeyFromECDSA(cert.GetWeightKey())
+	if err != nil {
+		return nil, fmt.Errorf("invalid CSP weight key: %w", err)
+	}
+	msg, err := CertMessage(processID, weight, cert.GetWeightKey())
+	if err != nil {
+		return nil, fmt.Errorf("cannot build CSP weight certificate message: %w", err)
+	}
+	if err := checkSigner(msg, cert.GetSignature(), censusRoot); err != nil {
+		return nil, fmt.Errorf("CSP weight certificate: %w", err)
+	}
+	return weightKey, nil
+}
+
+// checkSigner checks that signature is a 65-byte Ethereum signature of msg by
+// the key whose compressed form is expected. It works on a copy, because
+// ethereum.PubKeyFromSignature rewrites the recovery byte in place and the
+// signature belongs to the transaction.
+func checkSigner(msg, signature, expected []byte) error {
+	if len(signature) != ethereum.SignatureLength {
+		return fmt.Errorf("signature is %d bytes, expected %d", len(signature), ethereum.SignatureLength)
+	}
+	if len(expected) != compressedKeySize {
+		return fmt.Errorf("census root is %d bytes, expected %d", len(expected), compressedKeySize)
+	}
+	pub, err := ethereum.PubKeyFromSignature(msg, bytes.Clone(signature))
+	if err != nil {
+		return fmt.Errorf("cannot recover signer: %w", err)
+	}
+	if !bytes.Equal(pub, expected) {
+		return fmt.Errorf("signer %x does not match census root %x", pub, expected)
+	}
+	return nil
+}
```

```bash
go test ./vochain/transaction/proofs/cspproof/ -count=1 -v -run 'CSPV3' | grep -c '^--- PASS'
go test ./vochain/transaction/proofs/cspproof/ ./test/testcommon/... -count=1
go vet ./vochain/transaction/proofs/cspproof/ ./test/testcommon/testcsp/
```
Expected pass: 11 top-level `TestCSPV3*` tests pass (ValidProofs, WrongElectionCertificateRejected,
WeightBinding, ShiftedBlindSignatureRejected, ProofTypeFixedByOrigin, CertificatePresence,
MalformedInputRejected, ECDSAMalformedInputRejected, SignatureNotMutated, RejectsOtherOrigins,
GoldenVectors); the package's existing tests still pass.

Step 3: `git commit -m "feat(cspproof): verify per-election csp proofs and weight certificates" -- go.mod go.sum vochain/transaction/proofs/cspproof/cspv3.go vochain/transaction/proofs/cspproof/cspv3_test.go vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json test/testcommon/testcsp/v3.go`

Interfaces this task provides: `cspproof.ProofVerifierCSPV3{}` (implements the verifier interface
`VerifyProof` uses), `cspproof.BundleWeight([]byte) (*big.Int, error)`,
`cspproof.CertMessage(processID []byte, w *big.Int, weightKey []byte) ([]byte, error)`;
test helpers `testcsp.NewV3Election() (*testcsp.V3Election, error)` and `testcsp.WeightCredential`.

### Task 3: sequencer CSP key fork rules

Model: opus   Repo: SEQ   Files: config/forks.go, config/forks_test.go, vochain/state/censusorigins.go,
vochain/transaction/election_tx.go, vochain/transaction/proof.go, vochain/transaction/transaction.go,
vochain/transaction/vote_tx.go, vochain/cspkey_test.go, vochain/process_test.go, vochain/main_test.go,
vocone/main_test.go, api/main_test.go, apiclient/main_test.go

Goal: `ForksCfg.CSPKeyFork`. From the fork: V3 processes allowed (root a valid 33-byte point), new
OFF_CHAIN_CA/V2 processes rejected (`census origin %s retired for new processes`), a ProofCA with a
weight certificate on a legacy origin rejected. Below it: V3 processes rejected, everything else
byte-identical. Chains not in `config.Forks` activate it from genesis, so the existing tests of the
packages that create legacy CSP processes pin their chain below the fork in `TestMain`.

Step 1 (failing test):

```diff
diff --git a/api/main_test.go b/api/main_test.go
new file mode 100644
index 0000000..099af3c
--- /dev/null
+++ b/api/main_test.go
@@ -0,0 +1,21 @@
+package api
+
+import (
+	"math"
+	"os"
+	"testing"
+
+	"github.com/vocdoni/vochain-sequencer/config"
+)
+
+// legacyCSPChainID is the chain the tests of this package run on. They create
+// OFF_CHAIN_CA and OFF_CHAIN_CA_V2 processes, so it runs below the CSP key
+// fork. Tests of the fork rules use their own chain IDs.
+const legacyCSPChainID = "test"
+
+func TestMain(m *testing.M) {
+	if err := config.SetCSPKeyForkHeight(legacyCSPChainID, math.MaxUint32); err != nil {
+		panic(err)
+	}
+	os.Exit(m.Run())
+}
diff --git a/apiclient/main_test.go b/apiclient/main_test.go
new file mode 100644
index 0000000..771a98d
--- /dev/null
+++ b/apiclient/main_test.go
@@ -0,0 +1,21 @@
+package apiclient_test
+
+import (
+	"math"
+	"os"
+	"testing"
+
+	"github.com/vocdoni/vochain-sequencer/config"
+)
+
+// legacyCSPChainID is the chain the tests of this package run on. They create
+// OFF_CHAIN_CA and OFF_CHAIN_CA_V2 processes, so it runs below the CSP key
+// fork. Tests of the fork rules use their own chain IDs.
+const legacyCSPChainID = "test"
+
+func TestMain(m *testing.M) {
+	if err := config.SetCSPKeyForkHeight(legacyCSPChainID, math.MaxUint32); err != nil {
+		panic(err)
+	}
+	os.Exit(m.Run())
+}
diff --git a/config/forks_test.go b/config/forks_test.go
new file mode 100644
index 0000000..39a6a19
--- /dev/null
+++ b/config/forks_test.go
@@ -0,0 +1,27 @@
+package config
+
+import (
+	"testing"
+
+	qt "github.com/frankban/quicktest"
+)
+
+func TestCSPKeyForkConfigured(t *testing.T) {
+	c := qt.New(t)
+	for _, chainID := range []string{"vocdoni/LTS/1.3", "vocdoni/DEV/36"} {
+		c.Assert(ForksForChainID(chainID).CSPKeyFork, qt.Not(qt.Equals), uint32(0), qt.Commentf(chainID))
+	}
+	c.Assert(ForksForChainID("unlisted").CSPKeyFork, qt.Equals, uint32(0))
+}
+
+func TestSetCSPKeyForkHeight(t *testing.T) {
+	c := qt.New(t)
+	const chainID = "config-test-csp-fork"
+	t.Cleanup(func() { delete(Forks, chainID) })
+
+	c.Assert(SetCSPKeyForkHeight(chainID, 7), qt.IsNil)
+	c.Assert(ForksForChainID(chainID).CSPKeyFork, qt.Equals, uint32(7))
+	// registered chains are listed, so a second call is refused too
+	c.Assert(SetCSPKeyForkHeight(chainID, 8), qt.ErrorMatches, "chain config-test-csp-fork has configured fork heights.*")
+	c.Assert(SetCSPKeyForkHeight("vocdoni/LTS/1.3", 0), qt.ErrorMatches, "chain vocdoni/LTS/1.3 has configured fork heights.*")
+}
diff --git a/vochain/cspkey_test.go b/vochain/cspkey_test.go
new file mode 100644
index 0000000..e48677b
--- /dev/null
+++ b/vochain/cspkey_test.go
@@ -0,0 +1,245 @@
+package vochain
+
+import (
+	"bytes"
+	"math/big"
+	"testing"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/vochain-sequencer/config"
+	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
+	"github.com/vocdoni/vochain-sequencer/crypto/saltedkey"
+	"github.com/vocdoni/vochain-sequencer/test/testcommon/testcsp"
+	"github.com/vocdoni/vochain-sequencer/util"
+	"github.com/vocdoni/vochain-sequencer/vochain/state"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+// cspKeyTestApp returns a test application and accounts on its own chain,
+// whose CSP key fork activates at forkHeight.
+func cspKeyTestApp(t *testing.T, forkHeight uint32) (*BaseApplication, []*ethereum.SignKeys) {
+	t.Helper()
+	chainID := "csp-key-fork-" + t.Name()
+	qt.Assert(t, config.SetCSPKeyForkHeight(chainID, forkHeight), qt.IsNil)
+	t.Cleanup(func() { delete(config.Forks, chainID) })
+	return createTestBaseApplicationAndAccountsWithChainID(t, chainID, 1)
+}
+
+// cspKeyTestProcess builds a NewProcess payload for a CSP census.
+func cspKeyTestProcess(entityID []byte, origin models.CensusOrigin, root []byte) *models.Process {
+	censusURI := ipfsUrlTest
+	return &models.Process{
+		EnvelopeType:  &models.EnvelopeType{},
+		Mode:          &models.ProcessMode{Interruptible: true, AutoStart: true},
+		VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 1},
+		Status:        models.ProcessStatus_READY,
+		EntityId:      entityID,
+		CensusRoot:    root,
+		CensusURI:     &censusURI,
+		CensusOrigin:  origin,
+		Duration:      10240,
+		MaxCensusSize: 10,
+	}
+}
+
+func compressedTestKey(t *testing.T) []byte {
+	t.Helper()
+	k := ethereum.NewSignKeys()
+	qt.Assert(t, k.Generate(), qt.IsNil)
+	return ethcrypto.CompressPubkey(&k.Public)
+}
+
+func TestCSPKeyForkNewProcess(t *testing.T) {
+	const forkHeight = 100
+	app, accounts := cspKeyTestApp(t, forkHeight)
+	entity := accounts[0].Address().Bytes()
+	v3Origins := []models.CensusOrigin{
+		models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA, models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND,
+	}
+	legacyOrigins := []models.CensusOrigin{
+		models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2,
+	}
+
+	// below the fork: V3 is unknown, legacy CSP origins are accepted
+	app.State.SetHeight(forkHeight - 1)
+	for _, origin := range v3Origins {
+		err := testCreateProcessWithErr(t, accounts[0], app, cspKeyTestProcess(entity, origin, compressedTestKey(t)))
+		qt.Assert(t, err, qt.ErrorMatches, ".*census origin "+origin.String()+" not supported for new process.*")
+	}
+	for _, origin := range legacyOrigins {
+		pid := testCreateProcess(t, accounts[0], app, cspKeyTestProcess(entity, origin, compressedTestKey(t)))
+		qt.Assert(t, pid, qt.IsNotNil, qt.Commentf("origin %s", origin))
+	}
+
+	// from the fork: legacy CSP origins are retired, V3 needs a valid key as census root
+	app.State.SetHeight(forkHeight)
+	for _, origin := range legacyOrigins {
+		err := testCreateProcessWithErr(t, accounts[0], app, cspKeyTestProcess(entity, origin, compressedTestKey(t)))
+		qt.Assert(t, err, qt.ErrorMatches, ".*census origin "+origin.String()+" retired for new processes.*")
+	}
+	k := ethereum.NewSignKeys()
+	qt.Assert(t, k.Generate(), qt.IsNil)
+	invalidRoots := map[string][]byte{
+		"empty":        nil,
+		"32 bytes":     util.RandomBytes(32),
+		"uncompressed": ethcrypto.FromECDSAPub(&k.Public),
+		"bad prefix":   append([]byte{0x04}, ethcrypto.CompressPubkey(&k.Public)[1:]...),
+		"not on curve": append([]byte{0x02}, bytes.Repeat([]byte{0xff}, 32)...),
+	}
+	for _, origin := range v3Origins {
+		for name, root := range invalidRoots {
+			err := testCreateProcessWithErr(t, accounts[0], app, cspKeyTestProcess(entity, origin, root))
+			qt.Assert(t, err, qt.ErrorMatches, ".*CSP census root .*", qt.Commentf("%s %s", origin, name))
+		}
+		pid := testCreateProcess(t, accounts[0], app, cspKeyTestProcess(entity, origin, compressedTestKey(t)))
+		qt.Assert(t, pid, qt.IsNotNil, qt.Commentf("origin %s", origin))
+		process, err := app.State.Process(pid, false)
+		qt.Assert(t, err, qt.IsNil)
+		qt.Assert(t, process.CensusOrigin, qt.Equals, origin)
+		qt.Assert(t, pid[26], qt.Equals, byte(origin))
+	}
+}
+
+// TestCSPKeyV3VotesEndToEnd creates V3 elections through NewProcessTx on a
+// chain where the fork is active from genesis and votes on them through
+// CheckTx, DeliverTx and commit.
+func TestCSPKeyV3VotesEndToEnd(t *testing.T) {
+	app, accounts := cspKeyTestApp(t, 0)
+	entity := accounts[0].Address().Bytes()
+	vp, err := state.NewVotePackage([]int{1}).Encode()
+	qt.Assert(t, err, qt.IsNil)
+
+	t.Run("ecdsa", func(t *testing.T) {
+		election, err := testcsp.NewV3Election()
+		qt.Assert(t, err, qt.IsNil)
+		pid, err := testCreateProcessWithErrAndData(t, accounts[0], app,
+			cspKeyTestProcess(entity, models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA, election.CensusRoot()))
+		qt.Assert(t, err, qt.IsNil)
+		for _, voter := range ethereum.NewSignKeysBatch(3) {
+			proof, err := election.SignECDSAProof(testcsp.Bundle(pid, voter.Address().Bytes(), big.NewInt(2)))
+			qt.Assert(t, err, qt.IsNil)
+			testCSPsendVotes(t, pid, vp, voter, proof.GetCa(), app, true)
+		}
+		// a legacy salted proof from the same key is rejected
+		legacy := testcsp.Signer{ECDSA: election.Key}
+		voter := ethereum.NewSignKeysBatch(1)[0]
+		proof, err := legacy.Sign(models.ProofCA_ECDSA_PIDSALTED, testcsp.Bundle(pid, voter.Address().Bytes(), nil), pid)
+		qt.Assert(t, err, qt.IsNil)
+		testCSPsendVotes(t, pid, vp, voter, proof, app, false)
+	})
+
+	t.Run("blind", func(t *testing.T) {
+		election, err := testcsp.NewV3Election()
+		qt.Assert(t, err, qt.IsNil)
+		pid, err := testCreateProcessWithErrAndData(t, accounts[0], app,
+			cspKeyTestProcess(entity, models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND, election.CensusRoot()))
+		qt.Assert(t, err, qt.IsNil)
+		for _, w := range []int64{1, 7} {
+			cred, err := election.IssueWeightCredential(pid, big.NewInt(w))
+			qt.Assert(t, err, qt.IsNil)
+			voter := ethereum.NewSignKeysBatch(1)[0]
+			proof, err := cred.SignBlindProof(testcsp.Bundle(pid, voter.Address().Bytes(), big.NewInt(w)))
+			qt.Assert(t, err, qt.IsNil)
+			testCSPsendVotes(t, pid, vp, voter, proof.GetCa(), app, true)
+		}
+		// the weight-7 credential cannot authorize weight 8
+		cred, err := election.IssueWeightCredential(pid, big.NewInt(7))
+		qt.Assert(t, err, qt.IsNil)
+		voter := ethereum.NewSignKeysBatch(1)[0]
+		proof, err := cred.SignBlindProof(testcsp.Bundle(pid, voter.Address().Bytes(), big.NewInt(8)))
+		qt.Assert(t, err, qt.IsNil)
+		testCSPsendVotes(t, pid, vp, voter, proof.GetCa(), app, false)
+	})
+}
+
+// TestCSPKeyForkLegacyProcessKeepsVoting checks that OFF_CHAIN_CA and
+// OFF_CHAIN_CA_V2 processes created below the fork accept their legacy votes
+// from the fork on.
+func TestCSPKeyForkLegacyProcessKeepsVoting(t *testing.T) {
+	const forkHeight = 50
+	app, accounts := cspKeyTestApp(t, forkHeight)
+	entity := accounts[0].Address().Bytes()
+	signer, err := testcsp.NewSigner()
+	qt.Assert(t, err, qt.IsNil)
+	vp, err := state.NewVotePackage([]int{1}).Encode()
+	qt.Assert(t, err, qt.IsNil)
+
+	app.State.SetHeight(forkHeight - 10)
+	ecdsaRoot, err := signer.CensusRoot(models.ProofCA_ECDSA_PIDSALTED)
+	qt.Assert(t, err, qt.IsNil)
+	caPID, err := testCreateProcessWithErrAndData(t, accounts[0], app,
+		cspKeyTestProcess(entity, models.CensusOrigin_OFF_CHAIN_CA, ecdsaRoot))
+	qt.Assert(t, err, qt.IsNil)
+	blindRoot, err := signer.CensusRoot(models.ProofCA_ECDSA_BLIND_PIDSALTED)
+	qt.Assert(t, err, qt.IsNil)
+	v2PID, err := testCreateProcessWithErrAndData(t, accounts[0], app,
+		cspKeyTestProcess(entity, models.CensusOrigin_OFF_CHAIN_CA_V2, blindRoot))
+	qt.Assert(t, err, qt.IsNil)
+
+	app.State.SetHeight(forkHeight + 10)
+	voter := ethereum.NewSignKeysBatch(1)[0]
+	proof, err := signer.Sign(models.ProofCA_ECDSA_PIDSALTED, testcsp.Bundle(caPID, voter.Address().Bytes(), nil), caPID)
+	qt.Assert(t, err, qt.IsNil)
+	testCSPsendVotes(t, caPID, vp, voter, proof, app, true)
+
+	weight := big.NewInt(3)
+	salt, err := saltedkey.Salt(v2PID, weight.Bytes())
+	qt.Assert(t, err, qt.IsNil)
+	voter = ethereum.NewSignKeysBatch(1)[0]
+	proof, err = signer.Sign(models.ProofCA_ECDSA_BLIND_PIDSALTED, testcsp.Bundle(v2PID, voter.Address().Bytes(), weight), salt)
+	qt.Assert(t, err, qt.IsNil)
+	testCSPsendVotes(t, v2PID, vp, voter, proof, app, true)
+}
+
+// TestCSPKeyForkLegacyVoteWithCertificate checks that a weight certificate on
+// a legacy CSP vote is ignored below the fork and rejected from it on,
+// including for a vote checked into the mempool below the fork and delivered
+// from it on.
+func TestCSPKeyForkLegacyVoteWithCertificate(t *testing.T) {
+	const forkHeight = 50
+	app, accounts := cspKeyTestApp(t, forkHeight)
+	entity := accounts[0].Address().Bytes()
+	signer, err := testcsp.NewSigner()
+	qt.Assert(t, err, qt.IsNil)
+
+	app.State.SetHeight(forkHeight - 10)
+	root, err := signer.CensusRoot(models.ProofCA_ECDSA_BLIND_PIDSALTED)
+	qt.Assert(t, err, qt.IsNil)
+	pid, err := testCreateProcessWithErrAndData(t, accounts[0], app,
+		cspKeyTestProcess(entity, models.CensusOrigin_OFF_CHAIN_CA_V2, root))
+	qt.Assert(t, err, qt.IsNil)
+
+	voteWithCertificate := func() *models.SignedTx {
+		voter := ethereum.NewSignKeysBatch(1)[0]
+		weight := big.NewInt(2)
+		salt, err := saltedkey.Salt(pid, weight.Bytes())
+		qt.Assert(t, err, qt.IsNil)
+		proof, err := signer.Sign(models.ProofCA_ECDSA_BLIND_PIDSALTED,
+			testcsp.Bundle(pid, voter.Address().Bytes(), weight), salt)
+		qt.Assert(t, err, qt.IsNil)
+		proof.WeightCertificate = &models.CAWeightCertificate{
+			WeightKey: util.RandomBytes(33),
+			Signature: util.RandomBytes(65),
+		}
+		return testBuildSignedVote(t, pid, voter, proof, []int{1}, app.ChainID())
+	}
+
+	// below the fork the field is ignored
+	app.State.SetHeight(forkHeight - 1)
+	stx := voteWithCertificate()
+	qt.Assert(t, testSendVote(app, stx, false), qt.Equals, uint32(0))
+	qt.Assert(t, testSendVote(app, stx, true), qt.Equals, uint32(0))
+	testCommitState(t, app)
+
+	// checked into the mempool below the fork, delivered from it on
+	stx = voteWithCertificate()
+	qt.Assert(t, testSendVote(app, stx, false), qt.Equals, uint32(0))
+	app.State.SetHeight(forkHeight)
+	qt.Assert(t, testSendVote(app, stx, true), qt.Not(qt.Equals), uint32(0))
+
+	// from the fork on
+	stx = voteWithCertificate()
+	qt.Assert(t, testSendVote(app, stx, false), qt.Not(qt.Equals), uint32(0))
+	qt.Assert(t, testSendVote(app, stx, true), qt.Not(qt.Equals), uint32(0))
+}
diff --git a/vochain/main_test.go b/vochain/main_test.go
new file mode 100644
index 0000000..9032bd9
--- /dev/null
+++ b/vochain/main_test.go
@@ -0,0 +1,21 @@
+package vochain
+
+import (
+	"math"
+	"os"
+	"testing"
+
+	"github.com/vocdoni/vochain-sequencer/config"
+)
+
+// legacyCSPChainID is the chain the tests of this package run on. They create
+// OFF_CHAIN_CA and OFF_CHAIN_CA_V2 processes, so it runs below the CSP key
+// fork. Tests of the fork rules use their own chain IDs.
+const legacyCSPChainID = "test"
+
+func TestMain(m *testing.M) {
+	if err := config.SetCSPKeyForkHeight(legacyCSPChainID, math.MaxUint32); err != nil {
+		panic(err)
+	}
+	os.Exit(m.Run())
+}
diff --git a/vochain/process_test.go b/vochain/process_test.go
index 9dca09c..53d99a3 100644
--- a/vochain/process_test.go
+++ b/vochain/process_test.go
@@ -387,7 +387,15 @@ func TestCount(t *testing.T) {
 func createTestBaseApplicationAndAccounts(t *testing.T,
 	txCostNumber uint64,
 ) (*BaseApplication, []*ethereum.SignKeys) {
-	app := TestBaseApplication(t)
+	return createTestBaseApplicationAndAccountsWithChainID(t, "test", txCostNumber)
+}
+
+// createTestBaseApplicationAndAccountsWithChainID is
+// createTestBaseApplicationAndAccounts on the given chain.
+func createTestBaseApplicationAndAccountsWithChainID(t *testing.T, chainID string,
+	txCostNumber uint64,
+) (*BaseApplication, []*ethereum.SignKeys) {
+	app := TestBaseApplicationWithChainID(t, chainID)
 	keys := make([]*ethereum.SignKeys, 0)
 	for i := 0; i < 4; i++ {
 		key := &ethereum.SignKeys{}
diff --git a/vocone/main_test.go b/vocone/main_test.go
new file mode 100644
index 0000000..2c23009
--- /dev/null
+++ b/vocone/main_test.go
@@ -0,0 +1,21 @@
+package vocone
+
+import (
+	"math"
+	"os"
+	"testing"
+
+	"github.com/vocdoni/vochain-sequencer/config"
+)
+
+// legacyCSPChainID is the chain the tests of this package run on. They create
+// OFF_CHAIN_CA and OFF_CHAIN_CA_V2 processes, so it runs below the CSP key
+// fork. Tests of the fork rules use their own chain IDs.
+const legacyCSPChainID = "test-vocone"
+
+func TestMain(m *testing.M) {
+	if err := config.SetCSPKeyForkHeight(legacyCSPChainID, math.MaxUint32); err != nil {
+		panic(err)
+	}
+	os.Exit(m.Run())
+}
```

```bash
cd SEQ && go test ./config/ ./vochain/ -run 'CSPKey|SetCSPKeyForkHeight' -count=1
```
Expected failure: build error `undefined: config.SetCSPKeyForkHeight` / `CSPKeyFork`.

Step 2 (implementation):

```diff
diff --git a/config/forks.go b/config/forks.go
index 24422d3..3f352d6 100644
--- a/config/forks.go
+++ b/config/forks.go
@@ -1,5 +1,10 @@
 package config
 
+import (
+	"fmt"
+	"math"
+)
+
 // ForksCfg holds the heights at which soft forks activate on a chain. A zero
 // height means the fork is active from genesis.
 type ForksCfg struct {
@@ -8,6 +13,13 @@ type ForksCfg struct {
 	// NewProcessTx and the metadata hash attestation on votes. Before it, the
 	// chain must behave exactly as binaries without these rules.
 	MetadataFork uint32
+	// CSPKeyFork activates per-election CSP keys (saas-backend issue #769):
+	// NewProcessTx accepts the OFF_CHAIN_CA_V3_ECDSA and OFF_CHAIN_CA_V3_BLIND
+	// census origins and rejects new OFF_CHAIN_CA and OFF_CHAIN_CA_V2
+	// processes, and a ProofCA carrying a weight certificate is rejected on
+	// those legacy origins. Before it, the chain must behave exactly as
+	// binaries without these rules.
+	CSPKeyFork uint32
 }
 
 // Forks maps chainIDs to their soft fork heights. Chains not listed activate
@@ -15,10 +27,15 @@ type ForksCfg struct {
 var Forks = map[string]*ForksCfg{
 	"vocdoni/DEV/36": {
 		MetadataFork: 40_793,
+		// set when the release that activates it is cut
+		CSPKeyFork: math.MaxUint32,
 	},
 	"vocdoni/LTS/1.3": {
 		// ~2026-10-08 07:00 UTC
 		MetadataFork: 8_705_150,
+		// set to the height expected at the agreed time of the 2026-11-02 flag
+		// day when the release that activates it is cut
+		CSPKeyFork: math.MaxUint32,
 	},
 }
 
@@ -30,3 +47,17 @@ func ForksForChainID(chainID string) *ForksCfg {
 	}
 	return &ForksCfg{}
 }
+
+// SetCSPKeyForkHeight registers the CSPKeyFork height of a chain that is not
+// listed in Forks; every other fork of that chain stays active from genesis.
+// It lets test and development chains run legacy CSP elections before the
+// fork. It refuses chains listed in Forks, so it can never change the heights
+// of a public network. It is not safe for concurrent use: call it before the
+// chain starts.
+func SetCSPKeyForkHeight(chainID string, height uint32) error {
+	if _, found := Forks[chainID]; found {
+		return fmt.Errorf("chain %s has configured fork heights, its CSP key fork height cannot be changed", chainID)
+	}
+	Forks[chainID] = &ForksCfg{CSPKeyFork: height}
+	return nil
+}
diff --git a/vochain/state/censusorigins.go b/vochain/state/censusorigins.go
index ace7585..8eab874 100644
--- a/vochain/state/censusorigins.go
+++ b/vochain/state/censusorigins.go
@@ -29,4 +29,14 @@ var CensusOrigins = map[models.CensusOrigin]CensusProperties{
 		Name: "ca_v2", WeightedSupport: true,
 		NeedsURI: true, AllowCensusUpdate: true,
 	},
+	// The census root of a V3 origin is the per-election CSP key the proofs
+	// are checked against; it cannot be replaced once the process exists.
+	models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA: {
+		Name: "ca_v3_ecdsa", WeightedSupport: true,
+		NeedsURI: true,
+	},
+	models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND: {
+		Name: "ca_v3_blind", WeightedSupport: true,
+		NeedsURI: true,
+	},
 }
diff --git a/vochain/transaction/election_tx.go b/vochain/transaction/election_tx.go
index ab96f3d..b8f209e 100644
--- a/vochain/transaction/election_tx.go
+++ b/vochain/transaction/election_tx.go
@@ -5,6 +5,8 @@ import (
 	"encoding/hex"
 	"fmt"
 
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+
 	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
 	"github.com/vocdoni/vochain-sequencer/crypto/nacl"
 	"github.com/vocdoni/vochain-sequencer/log"
@@ -57,6 +59,18 @@ func (t *TransactionHandler) NewProcessTxCheck(vtx *vochaintx.Tx) (*models.Proce
 	// run specific checks based on census origin; no origin supports anonymous processes
 	switch tx.Process.CensusOrigin {
 	case models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2:
+		if t.cspKeyForkActive() {
+			return nil, ethereum.Address{}, fmt.Errorf("census origin %s retired for new processes",
+				tx.Process.CensusOrigin)
+		}
+	case models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA, models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:
+		if !t.cspKeyForkActive() {
+			return nil, ethereum.Address{}, fmt.Errorf("census origin %s not supported for new process",
+				tx.Process.CensusOrigin)
+		}
+		if err := checkCSPKeyCensusRoot(tx.Process.CensusRoot); err != nil {
+			return nil, ethereum.Address{}, err
+		}
 	case models.CensusOrigin_OFF_CHAIN_TREE, models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED:
 		// no additional origin-specific checks
 	default:
@@ -169,6 +183,19 @@ func (t *TransactionHandler) NewProcessTxCheck(vtx *vochaintx.Tx) (*models.Proce
 	return tx.Process, ethereum.Address(*addr), nil
 }
 
+// checkCSPKeyCensusRoot checks that the census root of a V3 CSP process is a
+// per-election CSP public key: a valid 33-byte compressed secp256k1 point.
+func checkCSPKeyCensusRoot(root []byte) error {
+	if len(root) != ethereum.PubKeyLengthBytes {
+		return fmt.Errorf("CSP census root is %d bytes, expected a %d-byte compressed public key",
+			len(root), ethereum.PubKeyLengthBytes)
+	}
+	if _, err := ethcrypto.DecompressPubkey(root); err != nil {
+		return fmt.Errorf("CSP census root is not a valid public key: %w", err)
+	}
+	return nil
+}
+
 // SetProcessTxCheck is an abstraction of ABCI checkTx for canceling an existing process
 func (t *TransactionHandler) SetProcessTxCheck(vtx *vochaintx.Tx) (ethereum.Address, error) {
 	// check vtx.Signature available
diff --git a/vochain/transaction/proof.go b/vochain/transaction/proof.go
index e44be7b..cf144dd 100644
--- a/vochain/transaction/proof.go
+++ b/vochain/transaction/proof.go
@@ -55,6 +55,8 @@ func VerifyProof(process *models.Process, envelope *models.VoteEnvelope, vID sta
 		verifier = &cspproof.ProofVerifierCSP{}
 	case models.CensusOrigin_OFF_CHAIN_CA_V2:
 		verifier = &cspproof.ProofVerifierCSP{V2: true}
+	case models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA, models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:
+		verifier = &cspproof.ProofVerifierCSPV3{}
 	default:
 		return false, nil, fmt.Errorf("census origin not compatible")
 	}
diff --git a/vochain/transaction/transaction.go b/vochain/transaction/transaction.go
index 63e57e3..6b2582f 100644
--- a/vochain/transaction/transaction.go
+++ b/vochain/transaction/transaction.go
@@ -56,6 +56,13 @@ func (t *TransactionHandler) metadataForkActive() bool {
 	return t.state.CurrentHeight() >= config.ForksForChainID(t.state.ChainID()).MetadataFork
 }
 
+// cspKeyForkActive reports whether per-election CSP keys (saas-backend issue
+// #769) are active at the current height, as set by config.Forks for the
+// chain.
+func (t *TransactionHandler) cspKeyForkActive() bool {
+	return t.state.CurrentHeight() >= config.ForksForChainID(t.state.ChainID()).CSPKeyFork
+}
+
 // CheckTx check the validity of a transaction and adds it to the state if forCommit=true.
 // It returns a bytes value which depends on the transaction type:
 //
diff --git a/vochain/transaction/vote_tx.go b/vochain/transaction/vote_tx.go
index dac96bc..e3ad540 100644
--- a/vochain/transaction/vote_tx.go
+++ b/vochain/transaction/vote_tx.go
@@ -41,6 +41,21 @@ func (t *TransactionHandler) checkVoteMetadataHash(voteHash []byte, process *mod
 	return nil
 }
 
+// checkVoteCSPCertificate rejects, from the CSP key fork on, a ProofCA that
+// carries a weight certificate on a legacy CSP origin (OFF_CHAIN_CA,
+// OFF_CHAIN_CA_V2). Below the fork the field is ignored, as binaries that do
+// not know it ignore it.
+func (t *TransactionHandler) checkVoteCSPCertificate(envelope *models.VoteEnvelope, process *models.Process) error {
+	if process.CensusOrigin != models.CensusOrigin_OFF_CHAIN_CA &&
+		process.CensusOrigin != models.CensusOrigin_OFF_CHAIN_CA_V2 {
+		return nil
+	}
+	if envelope.GetProof().GetCa().GetWeightCertificate() == nil || !t.cspKeyForkActive() {
+		return nil
+	}
+	return fmt.Errorf("CSP weight certificate not accepted on census origin %s", process.CensusOrigin)
+}
+
 // VoteTxCheck performs basic checks on a vote transaction.
 func (t *TransactionHandler) VoteTxCheck(vtx *vochaintx.Tx, forCommit bool) (*vstate.Vote, error) {
 	// Get the vote envelope from the transaction
@@ -144,7 +159,8 @@ func (t *TransactionHandler) VoteTxCheck(vtx *vochaintx.Tx, forCommit bool) (*vs
 		switch process.CensusOrigin {
 		case models.CensusOrigin_OFF_CHAIN_TREE,
 			models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED,
-			models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2:
+			models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2,
+			models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA, models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:
 			vote, err = initializeSignedVote(voteEnvelope, vtx.SignedBody, vtx.Signature, height)
 		default:
 			return nil, fmt.Errorf("could not initialize vote, census origin not compatible")
@@ -172,6 +188,12 @@ func (t *TransactionHandler) VoteTxCheck(vtx *vochaintx.Tx, forCommit bool) (*vs
 		return nil, err
 	}
 
+	// Like the memo, it runs outside the vote-cache branch: a vote that entered
+	// the mempool below the CSP key fork must be rejected in a block from it.
+	if err := t.checkVoteCSPCertificate(voteEnvelope, process); err != nil {
+		return nil, err
+	}
+
 	// Check if the vote is valid for the current state
 	isOverwrite, err := t.checkVoteCanBeCasted(vote.Nullifier, process)
 	if err != nil {
```

```bash
go test ./config/ -count=1 -v | grep -c '^--- PASS'            # includes TestCSPKeyForkConfigured, TestSetCSPKeyForkHeight
go test ./vochain/ -run 'CSPKey' -count=1 -v | grep '^--- '     # 4 PASS
go test ./vochain/... ./vocone/... ./api/... ./apiclient/... -count=1
```
Expected pass: the 2 config tests and the 4 `TestCSPKey*` tests (ForkNewProcess,
V3VotesEndToEnd, ForkLegacyProcessKeepsVoting, ForkLegacyVoteWithCertificate) pass; every existing
test of vochain, vocone, api and apiclient passes unchanged (AC3).

Step 3: `git commit -m "feat(vochain): add the csp key fork for per-election csp census origins" -- config/forks.go config/forks_test.go vochain/state/censusorigins.go vochain/transaction/election_tx.go vochain/transaction/proof.go vochain/transaction/transaction.go vochain/transaction/vote_tx.go vochain/cspkey_test.go vochain/process_test.go vochain/main_test.go vocone/main_test.go api/main_test.go apiclient/main_test.go`

Interfaces this task provides: `config.ForksCfg.CSPKeyFork uint32`,
`config.SetCSPKeyForkHeight(chainID string, height uint32) error` (refuses chain IDs in
`config.Forks`), `(*TransactionHandler).cspKeyForkActive() bool`.

### Task 4: voconed `--cspKeyForkHeight`, API census types, apiclient, end-to-end tests

Model: sonnet   Repo: SEQ   Files: cmd/voconed/voconed.go, cmd/voconed/voconed_test.go, api/api_types.go,
api/censuses.go, api/elections.go, api/csp_census_types_test.go, apiclient/vote.go, cmd/end2endtest/*.go,
scripts/live-csp-elections.sh

Goal: test and dev chains set their fork height with `voconed --cspKeyForkHeight N` (refused for
chain IDs in `config.Forks`). The REST API maps census types `csp3` → V3_ECDSA and `csp3blind` →
V3_BLIND, reports the new origin names and marks OFF_CHAIN_CA_V2 deprecated as well. apiclient can
send a weight certificate. end2endtest covers V3 elections; `scripts/live-csp-elections.sh` drives
them against a running voconed.

Step 1 (failing test):

```diff
diff --git a/api/csp_census_types_test.go b/api/csp_census_types_test.go
new file mode 100644
index 0000000..f276f52
--- /dev/null
+++ b/api/csp_census_types_test.go
@@ -0,0 +1,39 @@
+package api
+
+import (
+	"testing"
+
+	qt "github.com/frankban/quicktest"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+func TestCensusTypeToOriginCSP(t *testing.T) {
+	c := qt.New(t)
+	key := []byte{2, 1, 2, 3}
+	for typ, want := range map[string]models.CensusOrigin{
+		CensusTypeCSP:        models.CensusOrigin_OFF_CHAIN_CA,
+		CensusTypeCSPv2:      models.CensusOrigin_OFF_CHAIN_CA_V2,
+		CensusTypeCSPv3:      models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA,
+		CensusTypeCSPv3Blind: models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND,
+	} {
+		origin, root, err := CensusTypeToOrigin(CensusTypeDescription{Type: typ, PublicKey: key})
+		c.Assert(err, qt.IsNil, qt.Commentf(typ))
+		c.Assert(origin, qt.Equals, want, qt.Commentf(typ))
+		c.Assert([]byte(root), qt.DeepEquals, key)
+		_, _, err = CensusTypeToOrigin(CensusTypeDescription{Type: typ})
+		c.Assert(err, qt.ErrorMatches, ".*census root is nil.*", qt.Commentf(typ))
+	}
+}
+
+func TestCensusOriginDeprecated(t *testing.T) {
+	c := qt.New(t)
+	for origin, want := range map[models.CensusOrigin]bool{
+		models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED: false,
+		models.CensusOrigin_OFF_CHAIN_CA:            true,
+		models.CensusOrigin_OFF_CHAIN_CA_V2:         true,
+		models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA:   false,
+		models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:   false,
+	} {
+		c.Assert(censusOriginDeprecated(int32(origin)), qt.Equals, want, qt.Commentf(origin.String()))
+	}
+}
diff --git a/cmd/voconed/voconed_test.go b/cmd/voconed/voconed_test.go
new file mode 100644
index 0000000..4012119
--- /dev/null
+++ b/cmd/voconed/voconed_test.go
@@ -0,0 +1,32 @@
+package main
+
+import (
+	"math"
+	"testing"
+
+	qt "github.com/frankban/quicktest"
+	seqconfig "github.com/vocdoni/vochain-sequencer/config"
+)
+
+func TestApplyCSPKeyForkHeight(t *testing.T) {
+	c := qt.New(t)
+	const chainID = "voconed-test-csp-fork"
+	t.Cleanup(func() { delete(seqconfig.Forks, chainID) })
+
+	c.Assert(applyCSPKeyForkHeight(chainID, -1), qt.IsNil)
+	_, listed := seqconfig.Forks[chainID]
+	c.Assert(listed, qt.IsFalse)
+
+	c.Assert(applyCSPKeyForkHeight(chainID, math.MaxUint32+1), qt.ErrorMatches, "cspKeyForkHeight .* out of range")
+
+	c.Assert(applyCSPKeyForkHeight(chainID, 1000), qt.IsNil)
+	c.Assert(seqconfig.ForksForChainID(chainID).CSPKeyFork, qt.Equals, uint32(1000))
+	c.Assert(seqconfig.ForksForChainID(chainID).MetadataFork, qt.Equals, uint32(0))
+
+	for _, listedChain := range []string{"vocdoni/LTS/1.3", "vocdoni/DEV/36"} {
+		before := *seqconfig.ForksForChainID(listedChain)
+		c.Assert(applyCSPKeyForkHeight(listedChain, 0), qt.ErrorMatches,
+			"cannot apply cspKeyForkHeight: chain .* has configured fork heights.*")
+		c.Assert(*seqconfig.ForksForChainID(listedChain), qt.Equals, before)
+	}
+}
```

```bash
cd SEQ && go test ./cmd/voconed/ ./api/ -run 'ApplyCSPKeyForkHeight|CensusTypeToOriginCSP|CensusOriginDeprecated' -count=1
```
Expected failure: build error `undefined: applyCSPKeyForkHeight` and failing census type mapping.

Step 2 (implementation):

```diff
diff --git a/api/api_types.go b/api/api_types.go
index defd31f..a160832 100644
--- a/api/api_types.go
+++ b/api/api_types.go
@@ -179,7 +179,9 @@ type ElectionKeys struct {
 }
 
 type ElectionCensus struct {
-	CensusOrigin           string         `json:"censusOrigin"`
+	CensusOrigin string `json:"censusOrigin"`
+	// CensusOriginDeprecated is set on the CSP census origins that no new
+	// election uses from the CSP key fork on (OFF_CHAIN_CA, OFF_CHAIN_CA_V2).
 	CensusOriginDeprecated bool           `json:"censusOriginDeprecated,omitempty"`
 	CensusRoot             types.HexBytes `json:"censusRoot" `
 	PostRegisterCensusRoot types.HexBytes `json:"postRegisterCensusRoot" `
@@ -569,6 +571,12 @@ func CensusTypeToOrigin(ctype CensusTypeDescription) (models.CensusOrigin, []byt
 	case CensusTypeCSPv2:
 		origin = models.CensusOrigin_OFF_CHAIN_CA_V2
 		root = ctype.PublicKey
+	case CensusTypeCSPv3:
+		origin = models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA
+		root = ctype.PublicKey
+	case CensusTypeCSPv3Blind:
+		origin = models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND
+		root = ctype.PublicKey
 	case CensusTypeWeighted:
 		origin = models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED
 		root = ctype.RootHash
diff --git a/api/censuses.go b/api/censuses.go
index 14d7198..df52c78 100644
--- a/api/censuses.go
+++ b/api/censuses.go
@@ -28,8 +28,15 @@ const (
 	CensusTypeCSP      = "csp"
 	// CensusTypeCSPv2 is CensusTypeCSP with the fixed salt derivation for the
 	// salted proof types (census origin OFF_CHAIN_CA_V2).
-	CensusTypeCSPv2   = "csp2"
-	CensusTypeUnknown = "unknown"
+	CensusTypeCSPv2 = "csp2"
+	// CensusTypeCSPv3 is a CSP census whose public key belongs to one election
+	// only, with ECDSA proofs (census origin OFF_CHAIN_CA_V3_ECDSA).
+	CensusTypeCSPv3 = "csp3"
+	// CensusTypeCSPv3Blind is a CSP census whose public key belongs to one
+	// election only and certifies the blind signing key of each vote weight
+	// (census origin OFF_CHAIN_CA_V3_BLIND).
+	CensusTypeCSPv3Blind = "csp3blind"
+	CensusTypeUnknown    = "unknown"
 	// CensusTypeZKWeighted is vocdoni-node's anonymous census type. This node
 	// does not support anonymous voting, so creating one answers
 	// ErrEndpointRemoved rather than ErrCensusTypeUnknown.
diff --git a/api/elections.go b/api/elections.go
index 0fb10f1..4863946 100644
--- a/api/elections.go
+++ b/api/elections.go
@@ -251,7 +251,7 @@ func (a *API) electionHandler(_ *apirest.APIdata, ctx *httprouter.HTTPContext) e
 		TallyMode:       TallyMode{ProcessVoteOptions: proc.VoteOpts},
 		Census: &ElectionCensus{
 			CensusOrigin:           models.CensusOrigin_name[proc.CensusOrigin],
-			CensusOriginDeprecated: proc.CensusOrigin == int32(models.CensusOrigin_OFF_CHAIN_CA),
+			CensusOriginDeprecated: censusOriginDeprecated(proc.CensusOrigin),
 			CensusRoot:             proc.CensusRoot,
 			CensusURL:              proc.CensusURI,
 			MaxCensusSize:          proc.MaxCensusSize,
@@ -853,3 +853,10 @@ func electionParams(f func(key string) string, keys ...string) (*ElectionParams,
 		EndDateBefore:    dates[ParamEndDateBefore],
 	}, nil
 }
+
+// censusOriginDeprecated reports whether origin is a CSP census origin that no
+// new election uses from the CSP key fork on.
+func censusOriginDeprecated(origin int32) bool {
+	return origin == int32(models.CensusOrigin_OFF_CHAIN_CA) ||
+		origin == int32(models.CensusOrigin_OFF_CHAIN_CA_V2)
+}
diff --git a/apiclient/vote.go b/apiclient/vote.go
index cd39312..74bd9c4 100644
--- a/apiclient/vote.go
+++ b/apiclient/vote.go
@@ -5,6 +5,7 @@ import (
 	"encoding/json"
 	"fmt"
 	"math/big"
+	"slices"
 
 	"github.com/vocdoni/vochain-sequencer/api"
 	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
@@ -81,8 +82,12 @@ func (cl *HTTPclient) Vote(v *VoteData) (types.HexBytes, error) {
 
 	log.Debugw("generating a new vote", "electionId", v.Election.ElectionID, "voter", c.account.AddressString())
 	voteAPI := &api.Vote{}
-	censusOriginCSP := models.CensusOrigin_name[int32(models.CensusOrigin_OFF_CHAIN_CA)]
-	censusOriginCSPv2 := models.CensusOrigin_name[int32(models.CensusOrigin_OFF_CHAIN_CA_V2)]
+	cspCensusOrigins := []string{
+		models.CensusOrigin_OFF_CHAIN_CA.String(),
+		models.CensusOrigin_OFF_CHAIN_CA_V2.String(),
+		models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA.String(),
+		models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND.String(),
+	}
 	censusOriginWeighted := models.CensusOrigin_name[int32(models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED)]
 	switch {
 	case v.Election.Census.CensusOrigin == censusOriginWeighted:
@@ -109,8 +114,7 @@ func (cl *HTTPclient) Vote(v *VoteData) (types.HexBytes, error) {
 		if err != nil {
 			return nil, err
 		}
-	case v.Election.Census.CensusOrigin == censusOriginCSP ||
-		v.Election.Census.CensusOrigin == censusOriginCSPv2:
+	case slices.Contains(cspCensusOrigins, v.Election.Census.CensusOrigin):
 		// decode the CSP proof and include in a VoteEnvelope
 		p := models.ProofCA{}
 		if err := proto.Unmarshal(v.ProofCSP, &p); err != nil {
diff --git a/cmd/end2endtest/csp.go b/cmd/end2endtest/csp.go
index 1e9da12..5f0d4ea 100644
--- a/cmd/end2endtest/csp.go
+++ b/cmd/end2endtest/csp.go
@@ -17,16 +17,32 @@ func init() {
 		testFunc: func() VochainTest {
 			return &E2ECSPElection{}
 		},
-		description: "csp election",
+		description: "csp election with census origin OFF_CHAIN_CA (needs a chain below the CSP key fork)",
 		example:     os.Args[0] + " --operation=cspelection --votes=1000",
 	}
 	ops["cspelectionv2"] = operation{
 		testFunc: func() VochainTest {
 			return &E2ECSPElectionV2{}
 		},
-		description: "csp election with census origin OFF_CHAIN_CA_V2 and weighted blind pid-salted proofs (issue #1424 derivation)",
+		description: "csp election with census origin OFF_CHAIN_CA_V2 and weighted blind pid-salted proofs " +
+			"(issue #1424 derivation; needs a chain below the CSP key fork)",
 		example:     os.Args[0] + " --operation=cspelectionv2 --votes=1000",
 	}
+	ops["cspelectionv3"] = operation{
+		testFunc: func() VochainTest {
+			return &E2ECSPElectionV3{}
+		},
+		description: "csp election with census origin OFF_CHAIN_CA_V3_ECDSA: per-election CSP key, ECDSA proofs",
+		example:     os.Args[0] + " --operation=cspelectionv3 --votes=1000",
+	}
+	ops["cspelectionv3blind"] = operation{
+		testFunc: func() VochainTest {
+			return &E2ECSPElectionV3Blind{}
+		},
+		description: "csp election with census origin OFF_CHAIN_CA_V3_BLIND: per-election CSP key, " +
+			"weighted blind proofs with weight certificates",
+		example: os.Args[0] + " --operation=cspelectionv3blind --votes=1000",
+	}
 }
 
 var _ VochainTest = (*E2ECSPElection)(nil)
@@ -128,3 +144,59 @@ func (t *E2ECSPElectionV2) Setup(api *apiclient.HTTPclient, c *config) error {
 	logElection(t.election)
 	return nil
 }
+
+var _ VochainTest = (*E2ECSPElectionV3)(nil)
+
+// E2ECSPElectionV3 runs a CSP election with census origin
+// OFF_CHAIN_CA_V3_ECDSA, where the census root is a per-election CSP key that
+// signs every voter's bundle.
+type E2ECSPElectionV3 struct {
+	E2ECSPElection
+}
+
+func (t *E2ECSPElectionV3) Setup(api *apiclient.HTTPclient, c *config) error {
+	t.api = api
+	t.config = c
+
+	p := newTestProcess()
+	p.CensusOrigin = models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA
+
+	if err := t.setupElectionRaw(p); err != nil {
+		return err
+	}
+
+	logElection(t.election)
+	return nil
+}
+
+var _ VochainTest = (*E2ECSPElectionV3Blind)(nil)
+
+// E2ECSPElectionV3Blind runs a CSP election with census origin
+// OFF_CHAIN_CA_V3_BLIND, where every proof is ECDSA_BLIND with a
+// CSP-authorized weight, signed by a weight key that the election key
+// certifies for this election and weight.
+type E2ECSPElectionV3Blind struct {
+	E2ECSPElection
+}
+
+func (t *E2ECSPElectionV3Blind) Setup(api *apiclient.HTTPclient, c *config) error {
+	election, err := testcsp.NewV3Election()
+	if err != nil {
+		return err
+	}
+	t.cspV3Blind = election
+	t.cspVoteWeight = big.NewInt(10)
+
+	t.api = api
+	t.config = c
+
+	p := newTestProcess()
+	p.CensusOrigin = models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND
+
+	if err := t.setupElectionRaw(p); err != nil {
+		return err
+	}
+
+	logElection(t.election)
+	return nil
+}
diff --git a/cmd/end2endtest/electiontimebounds.go b/cmd/end2endtest/electiontimebounds.go
index af263a6..a58ee29 100644
--- a/cmd/end2endtest/electiontimebounds.go
+++ b/cmd/end2endtest/electiontimebounds.go
@@ -49,7 +49,7 @@ func (t *E2EElectionTimeBounds) Setup(api *apiclient.HTTPclient, c *config) erro
 	// Set end time to 40 seconds from now (20 seconds after start)
 	ed.EndDate = ed.StartDate.Add(20 * time.Second)
 
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, 10, false); err != nil {
 		return err
diff --git a/cmd/end2endtest/encrypted.go b/cmd/end2endtest/encrypted.go
index e4a55c9..941ce71 100644
--- a/cmd/end2endtest/encrypted.go
+++ b/cmd/end2endtest/encrypted.go
@@ -37,7 +37,7 @@ func (t *E2EEncryptedElection) Setup(api *apiclient.HTTPclient, c *config) error
 		SecretUntilTheEnd: true,
 	}
 	ed.VoteType = vapi.VoteType{MaxVoteOverwrites: 1}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/end2endtest/helpers.go b/cmd/end2endtest/helpers.go
index 0b939e8..50e37d3 100644
--- a/cmd/end2endtest/helpers.go
+++ b/cmd/end2endtest/helpers.go
@@ -67,7 +67,7 @@ func newTestProcess() *models.Process {
 			EncryptedVotes: false,
 			UniqueValues:   true,
 		},
-		CensusOrigin: models.CensusOrigin_OFF_CHAIN_CA_V2,
+		CensusOrigin: models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA,
 		VoteOptions: &models.ProcessVoteOptions{
 			MaxCount: 1,
 			MaxValue: 1,
@@ -200,6 +200,9 @@ func (t *e2eElection) generateProofs(csp *ethereum.SignKeys, voterAccts []*ether
 				if t.cspSigner != nil {
 					return t.cspGenProofSalted(voterKey)
 				}
+				if t.cspV3Blind != nil {
+					return t.cspGenProofV3Blind(voterKey)
+				}
 				if csp != nil {
 					return cspGenProof(t.election.ElectionID, voterKey, csp)
 				}
@@ -292,8 +295,8 @@ func (t *e2eElection) setupCensus(censusType string, nAcct int) (types.HexBytes,
 // the census proofs of nvAccts voter accounts. With census type
 // CensusTypeWeighted it creates and publishes a weighted census tree of those
 // accounts through the census API. With any other type the census type on ed
-// is overwritten to CensusTypeCSPv2 and the public key of a new CSP is set as
-// the root. The caller sets ed.Census.Size to restrict MaxCensusSize; if it is
+// is overwritten to CensusTypeCSPv3 and the public key of a new per-election
+// CSP key is set as the root. The caller sets ed.Census.Size to restrict MaxCensusSize; if it is
 // zero it defaults to nvAccts (or 1 when nvotes is 0).
 func (t *e2eElection) setupElection(ed *vapi.ElectionDescription, nvAccts int, waitUntilStarted bool) error {
 	if err := t.setupAccount(); err != nil {
@@ -318,7 +321,7 @@ func (t *e2eElection) setupElection(ed *vapi.ElectionDescription, nvAccts int, w
 			return fmt.Errorf("cannot generate CSP key: %w", err)
 		}
 		voterAccts = ethereum.NewSignKeysBatch(nvAccts)
-		ed.Census.Type = vapi.CensusTypeCSPv2
+		ed.Census.Type = vapi.CensusTypeCSPv3
 		ed.Census.PublicKey = csp.PublicKey()
 	}
 	if ed.Census.Size == 0 {
@@ -378,7 +381,16 @@ func (t *e2eElection) setupElectionRaw(prc *models.Process) error {
 	csp := &ethereum.SignKeys{}
 
 	switch prc.CensusOrigin {
-	case models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2:
+	case models.CensusOrigin_OFF_CHAIN_CA_V3_BLIND:
+		if t.cspV3Blind == nil {
+			return fmt.Errorf("census origin %s needs a V3 CSP election key", prc.CensusOrigin)
+		}
+		voterAccounts = ethereum.NewSignKeysBatch(t.config.nvotes)
+		prc.CensusRoot = t.cspV3Blind.CensusRoot()
+		csp = nil
+
+	case models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2,
+		models.CensusOrigin_OFF_CHAIN_CA_V3_ECDSA:
 		voterAccounts = ethereum.NewSignKeysBatch(t.config.nvotes)
 
 		if t.cspSigner != nil {
@@ -629,6 +641,30 @@ func (t *e2eElection) cspGenProofSalted(voterKey []byte) (*apiclient.CensusProof
 	return &apiclient.CensusProof{Proof: caProofBytes}, nil
 }
 
+// cspGenProofV3Blind issues an OFF_CHAIN_CA_V3_BLIND proof for the running
+// election: a weight key for t.cspVoteWeight (1 when nil), certified by
+// t.cspV3Blind for this election, blind-signs the voter's bundle.
+func (t *e2eElection) cspGenProofV3Blind(voterKey []byte) (*apiclient.CensusProof, error) {
+	pid := t.election.ElectionID
+	weight := t.cspVoteWeight
+	if weight == nil {
+		weight = big.NewInt(1)
+	}
+	cred, err := t.cspV3Blind.IssueWeightCredential(pid, weight)
+	if err != nil {
+		return nil, err
+	}
+	proof, err := cred.SignBlindProof(testcsp.Bundle(pid, voterKey, t.cspVoteWeight))
+	if err != nil {
+		return nil, err
+	}
+	caProofBytes, err := proto.Marshal(proof.GetCa())
+	if err != nil {
+		return nil, err
+	}
+	return &apiclient.CensusProof{Proof: caProofBytes}, nil
+}
+
 func cspGenProof(pid, voterKey []byte, csp *ethereum.SignKeys) (*apiclient.CensusProof, error) {
 	bundle := &models.CAbundle{
 		ProcessId: pid,
diff --git a/cmd/end2endtest/lifecycle.go b/cmd/end2endtest/lifecycle.go
index f202fd0..384204e 100644
--- a/cmd/end2endtest/lifecycle.go
+++ b/cmd/end2endtest/lifecycle.go
@@ -45,7 +45,7 @@ func (t *E2ELifecycleElection) Setup(api *apiclient.HTTPclient, c *config) error
 	for i, ed := range electionDescriptions {
 		ed.d.ElectionType.Autostart = true
 		ed.d.ElectionType.Interruptible = ed.interruptible
-		ed.d.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+		ed.d.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 		if err := t.elections[i].setupElection(ed.d, t.elections[0].config.nvotes, true); err != nil {
 			log.Errorf("failed to setup election %d: %v", i, err)
diff --git a/cmd/end2endtest/main.go b/cmd/end2endtest/main.go
index 68428bc..8d0ec77 100644
--- a/cmd/end2endtest/main.go
+++ b/cmd/end2endtest/main.go
@@ -52,6 +52,10 @@ type e2eElection struct {
 	cspSigner     *testcsp.Signer
 	cspProofType  models.ProofCA_Type
 	cspVoteWeight *big.Int
+	// cspV3Blind, when set on an OFF_CHAIN_CA_V3_BLIND election, certifies a
+	// weight key for cspVoteWeight per voter, and the CSP proofs are blind
+	// proofs signed by it
+	cspV3Blind *testcsp.V3Election
 }
 
 type operation struct {
diff --git a/cmd/end2endtest/memo.go b/cmd/end2endtest/memo.go
index a407da9..31572e3 100644
--- a/cmd/end2endtest/memo.go
+++ b/cmd/end2endtest/memo.go
@@ -37,7 +37,7 @@ func (t *E2EMemoElection) Setup(api *apiclient.HTTPclient, c *config) error {
 		Interruptible: true,
 	}
 	ed.VoteType = vapi.VoteType{MaxVoteOverwrites: 1}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/end2endtest/metadata.go b/cmd/end2endtest/metadata.go
index 089eebf..f0ac766 100644
--- a/cmd/end2endtest/metadata.go
+++ b/cmd/end2endtest/metadata.go
@@ -44,7 +44,7 @@ func (t *E2EMetadataElection) Setup(api *apiclient.HTTPclient, c *config) error
 		Autostart:     true,
 		Interruptible: true,
 	}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/end2endtest/overwrite.go b/cmd/end2endtest/overwrite.go
index 47d3978..8f32fe5 100644
--- a/cmd/end2endtest/overwrite.go
+++ b/cmd/end2endtest/overwrite.go
@@ -38,7 +38,7 @@ func (t *E2EOverwriteElection) Setup(api *apiclient.HTTPclient, c *config) error
 		Interruptible: true,
 	}
 	ed.VoteType = vapi.VoteType{MaxVoteOverwrites: 2}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/end2endtest/plaintext.go b/cmd/end2endtest/plaintext.go
index 1318794..44d4254 100644
--- a/cmd/end2endtest/plaintext.go
+++ b/cmd/end2endtest/plaintext.go
@@ -36,7 +36,7 @@ func (t *E2EPlaintextElection) Setup(api *apiclient.HTTPclient, c *config) error
 		Interruptible: true,
 	}
 	ed.VoteType = vapi.VoteType{MaxVoteOverwrites: 1}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/end2endtest/race.go b/cmd/end2endtest/race.go
index 32a581f..9fee4fe 100644
--- a/cmd/end2endtest/race.go
+++ b/cmd/end2endtest/race.go
@@ -39,7 +39,7 @@ func (t *E2ERaceDuringCommit) Setup(api *apiclient.HTTPclient, c *config) error
 		Interruptible: true,
 	}
 	ed.VoteType = vapi.VoteType{MaxVoteOverwrites: 1}
-	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv2}
+	ed.Census = vapi.CensusTypeDescription{Type: vapi.CensusTypeCSPv3}
 
 	if err := t.setupElection(ed, t.config.nvotes, true); err != nil {
 		return err
diff --git a/cmd/voconed/voconed.go b/cmd/voconed/voconed.go
index feb88ea..d6d1949 100644
--- a/cmd/voconed/voconed.go
+++ b/cmd/voconed/voconed.go
@@ -4,6 +4,7 @@ import (
 	"context"
 	"encoding/hex"
 	"fmt"
+	"math"
 	"os"
 	"os/signal"
 	"path/filepath"
@@ -17,6 +18,7 @@ import (
 	flag "github.com/spf13/pflag"
 	"github.com/spf13/viper"
 	"github.com/vocdoni/vochain-sequencer/api/faucet"
+	seqconfig "github.com/vocdoni/vochain-sequencer/config"
 	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
 	"github.com/vocdoni/vochain-sequencer/internal"
 	"github.com/vocdoni/vochain-sequencer/log"
@@ -80,6 +82,9 @@ func main() {
 
 	flag.StringSliceVar(&config.fundedAccounts, "fundedAccounts", []string{},
 		"list of pre-funded accounts (address:balance,address:balance,...), only applied when the chain is created")
+	cspKeyForkHeight := flag.Int64("cspKeyForkHeight", -1,
+		"height at which per-election CSP keys activate on this chain (default: from genesis); "+
+			"refused for chains with configured fork heights, not persisted: pass it on every start")
 	flag.CommandLine.SortFlags = false
 	flag.Parse()
 
@@ -326,6 +331,9 @@ func main() {
 		}
 	}
 	log.Infof("using chainID: %s", vc.App.ChainID())
+	if err := applyCSPKeyForkHeight(vc.App.ChainID(), *cspKeyForkHeight); err != nil {
+		log.Fatal(err)
+	}
 
 	if *setTxCosts {
 		if err := vc.SetBulkTxCosts(config.txCosts, true); err != nil {
@@ -383,6 +391,22 @@ func main() {
 }
 
 // fileExists reports whether path exists.
+// applyCSPKeyForkHeight registers the --cspKeyForkHeight value for chainID. A
+// negative height leaves the chain's CSP key fork as configured.
+func applyCSPKeyForkHeight(chainID string, height int64) error {
+	if height < 0 {
+		return nil
+	}
+	if height > math.MaxUint32 {
+		return fmt.Errorf("cspKeyForkHeight %d out of range", height)
+	}
+	if err := seqconfig.SetCSPKeyForkHeight(chainID, uint32(height)); err != nil {
+		return fmt.Errorf("cannot apply cspKeyForkHeight: %w", err)
+	}
+	log.Infow("CSP key fork height set", "chainID", chainID, "height", height)
+	return nil
+}
+
 func fileExists(path string) bool {
 	_, err := os.Stat(path)
 	return err == nil
diff --git a/scripts/live-csp-elections.sh b/scripts/live-csp-elections.sh
new file mode 100755
index 0000000..2584bd5
--- /dev/null
+++ b/scripts/live-csp-elections.sh
@@ -0,0 +1,33 @@
+#!/usr/bin/env bash
+# Lists the live (READY or PAUSED) elections of a Vochain API whose census
+# origin is OFF_CHAIN_CA or OFF_CHAIN_CA_V2: the legacy CSP elections that keep
+# the salted CSP keys until they end. The census origin is byte 26 of the
+# election id (vochain/processid), so no per-election request is needed.
+#
+# Usage: scripts/live-csp-elections.sh [API_URL]
+#   API_URL defaults to https://api.vocdoni.io/v2
+# Output: one tab-separated line per election:
+#   electionId  origin  status  endDate  organizationId
+# Requires curl and jq.
+set -euo pipefail
+
+api="${1:-https://api.vocdoni.io/v2}"
+api="${api%/}"
+
+for status in ready paused; do
+	page=0
+	while :; do
+		body="$(curl -fsS "${api}/elections?status=${status}&limit=100&page=${page}")"
+		jq -r '
+			.elections[]?
+			| (.electionId | ascii_downcase | .[52:54]) as $origin
+			| select($origin == "03" or $origin == "04")
+			| [.electionId,
+			   (if $origin == "03" then "OFF_CHAIN_CA" else "OFF_CHAIN_CA_V2" end),
+			   .status, .endDate, .organizationId]
+			| @tsv' <<<"${body}"
+		next="$(jq -r '.pagination.nextPage // empty' <<<"${body}")"
+		[[ -z "${next}" ]] && break
+		page="${next}"
+	done
+done
```

```bash
go test ./cmd/... ./api/... ./apiclient/... -count=1
go vet ./... && shellcheck scripts/live-csp-elections.sh   # shellcheck: UNVERIFIED (verified at implementation)
```
Expected pass: `TestApplyCSPKeyForkHeight`, `TestCensusTypeToOriginCSP`, `TestCensusOriginDeprecated`
pass; all cmd/api/apiclient packages ok.

Step 3: `git commit -m "feat(voconed): set the csp key fork height and expose v3 csp census types" -- cmd/voconed/voconed.go cmd/voconed/voconed_test.go api/api_types.go api/censuses.go api/elections.go api/csp_census_types_test.go apiclient/vote.go cmd/end2endtest/ scripts/live-csp-elections.sh`

Interfaces this task provides: `voconed --cspKeyForkHeight <uint32>`; API census types `csp3`,
`csp3blind`; origin names `OFF_CHAIN_CA_V3_ECDSA`, `OFF_CHAIN_CA_V3_BLIND` in election info.

### Task 5: saas-backend per-election key derivation (electionkey)

Model: opus   Repo: SAAS   Files: csp/signers/electionkey/electionkey.go, csp/signers/electionkey/electionkey_test.go,
csp/signers/electionkey/vectors_test.go, csp/signers/electionkey/testdata/csp-v3-vectors.json

Goal: `Root` derives `dE`/`dB` from root || question ObjectID, `WeightKey` derives `dX`, and
`IssueWeightCredential` returns X and its certificate, matching the shared golden vectors (AC4).
Origin constants 5/6 are local (proto stays v1.15.14).

Step 1 (failing test): copy the canonical vectors and add the tests.

```bash
mkdir -p SAAS/csp/signers/electionkey/testdata
cp SEQ/vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json SAAS/csp/signers/electionkey/testdata/
sha256sum SAAS/csp/signers/electionkey/testdata/csp-v3-vectors.json   # 7ccfd0e0...a7bac5
```

```diff
diff --git a/csp/signers/electionkey/electionkey_test.go b/csp/signers/electionkey/electionkey_test.go
new file mode 100644
index 0000000..090a015
--- /dev/null
+++ b/csp/signers/electionkey/electionkey_test.go
@@ -0,0 +1,150 @@
+package electionkey
+
+import (
+	"bytes"
+	"encoding/binary"
+	"math/big"
+	"testing"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	qt "github.com/frankban/quicktest"
+	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+func testRoot(c *qt.C) *Root {
+	root, err := NewRoot(bytes.Repeat([]byte{0x11}, RootSize))
+	c.Assert(err, qt.IsNil)
+	return root
+}
+
+func testSeed(b byte) [SeedSize]byte {
+	var s [SeedSize]byte
+	s[SeedSize-1] = b
+	return s
+}
+
+func TestHashToScalarMatchesDefinition(t *testing.T) {
+	c := qt.New(t)
+	data := []byte("data")
+	got, err := HashToScalar(TagElectionECDSA, data)
+	c.Assert(err, qt.IsNil)
+	var counter [4]byte
+	binary.BigEndian.PutUint32(counter[:], 0)
+	want := new(big.Int).SetBytes(ethcrypto.Keccak256([]byte(TagElectionECDSA), counter[:], data))
+	c.Assert(got.Cmp(want), qt.Equals, 0)
+}
+
+func TestNewRootRejectsInvalidKeys(t *testing.T) {
+	c := qt.New(t)
+	_, err := NewRoot(make([]byte, 31))
+	c.Assert(err, qt.ErrorMatches, "root key is 31 bytes, expected 32")
+	_, err = NewRoot(make([]byte, RootSize))
+	c.Assert(err, qt.ErrorMatches, "root key is not a valid secp256k1 scalar")
+	_, err = NewRoot(curveN.FillBytes(make([]byte, RootSize)))
+	c.Assert(err, qt.ErrorMatches, "root key is not a valid secp256k1 scalar")
+}
+
+func TestElectionKeysAreDistinctAndDeterministic(t *testing.T) {
+	c := qt.New(t)
+	root := testRoot(c)
+	e1, err := root.ElectionECDSAKey(testSeed(1))
+	c.Assert(err, qt.IsNil)
+	e1again, err := root.ElectionECDSAKey(testSeed(1))
+	c.Assert(err, qt.IsNil)
+	c.Assert(ethcrypto.FromECDSA(e1), qt.DeepEquals, ethcrypto.FromECDSA(e1again))
+	e2, err := root.ElectionECDSAKey(testSeed(2))
+	c.Assert(err, qt.IsNil)
+	b1, err := root.ElectionBlindKey(testSeed(1))
+	c.Assert(err, qt.IsNil)
+	c.Assert(ethcrypto.FromECDSA(e1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(e2))
+	c.Assert(ethcrypto.FromECDSA(e1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(b1))
+	c.Assert(CompressedPubKey(e1), qt.HasLen, PubKeySize)
+}
+
+func TestWeightKeyDependsOnWeight(t *testing.T) {
+	c := qt.New(t)
+	dB, err := testRoot(c).ElectionBlindKey(testSeed(1))
+	c.Assert(err, qt.IsNil)
+	k1, err := WeightKey(dB, big.NewInt(1))
+	c.Assert(err, qt.IsNil)
+	k2, err := WeightKey(dB, big.NewInt(2))
+	c.Assert(err, qt.IsNil)
+	c.Assert(ethcrypto.FromECDSA(k1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(k2))
+	_, err = WeightKey(dB, big.NewInt(0))
+	c.Assert(err, qt.ErrorMatches, "weight must be positive")
+	_, err = WeightKey(dB, new(big.Int).Lsh(big.NewInt(1), 256))
+	c.Assert(err, qt.ErrorMatches, "weight does not fit in 32 bytes")
+	_, err = WeightKey(nil, big.NewInt(1))
+	c.Assert(err, qt.ErrorMatches, "nil election blind key")
+}
+
+func TestCertMessageLayout(t *testing.T) {
+	c := qt.New(t)
+	pid := bytes.Repeat([]byte{0xaa}, ProcessIDSize)
+	key := bytes.Repeat([]byte{0x02}, PubKeySize)
+	msg, err := CertMessage(pid, big.NewInt(42), key)
+	c.Assert(err, qt.IsNil)
+	c.Assert(msg, qt.HasLen, 123)
+	tagEnd := len(CertTag)
+	c.Assert(bytes.HasPrefix(msg, []byte(CertTag)), qt.IsTrue)
+	c.Assert(msg[tagEnd:tagEnd+ProcessIDSize], qt.DeepEquals, pid)
+	weight := msg[tagEnd+ProcessIDSize : tagEnd+ProcessIDSize+WeightSize]
+	c.Assert(weight, qt.DeepEquals, big.NewInt(42).FillBytes(make([]byte, WeightSize)))
+	c.Assert(msg[CertMessageSize-PubKeySize:], qt.DeepEquals, key)
+
+	_, err = CertMessage(pid[:31], big.NewInt(1), key)
+	c.Assert(err, qt.ErrorMatches, "process id is 31 bytes, expected 32")
+	_, err = CertMessage(pid, big.NewInt(1), key[:32])
+	c.Assert(err, qt.ErrorMatches, "weight key is 32 bytes, expected 33")
+}
+
+func TestIssueWeightCredentialRecoversElectionKey(t *testing.T) {
+	c := qt.New(t)
+	dB, err := testRoot(c).ElectionBlindKey(testSeed(3))
+	c.Assert(err, qt.IsNil)
+	pid := bytes.Repeat([]byte{0x01}, ProcessIDSize)
+	cred, err := IssueWeightCredential(dB, pid, big.NewInt(7))
+	c.Assert(err, qt.IsNil)
+	c.Assert(cred.Certificate, qt.HasLen, 65)
+	msg, err := CertMessage(pid, big.NewInt(7), cred.WeightKey)
+	c.Assert(err, qt.IsNil)
+	pub, err := vocdonicrypto.PubKeyFromSignature(msg, cred.Certificate)
+	c.Assert(err, qt.IsNil)
+	c.Assert(pub, qt.DeepEquals, CompressedPubKey(dB))
+}
+
+func TestNormalizeWeight(t *testing.T) {
+	c := qt.New(t)
+	for _, tc := range []struct {
+		in   []byte
+		want int64
+		err  string
+	}{
+		{in: nil, want: 1},
+		{in: []byte{}, want: 1},
+		{in: []byte{0x2a}, want: 42},
+		{in: []byte{0x00, 0x01}, want: 1},
+		{in: []byte{0x00}, err: "weight is zero"},
+		{in: make([]byte, 33), err: "weight is 33 bytes, at most 32 allowed"},
+	} {
+		w, err := NormalizeWeight(tc.in)
+		if tc.err != "" {
+			c.Assert(err, qt.ErrorMatches, tc.err)
+			continue
+		}
+		c.Assert(err, qt.IsNil)
+		c.Assert(w.Int64(), qt.Equals, tc.want)
+	}
+}
+
+func TestIsV3Origin(t *testing.T) {
+	c := qt.New(t)
+	c.Assert(IsV3Origin(OriginECDSAName), qt.IsTrue)
+	c.Assert(IsV3Origin(OriginBlindName), qt.IsTrue)
+	c.Assert(IsV3Origin("OFF_CHAIN_CA_V2"), qt.IsFalse)
+	c.Assert(IsV3Origin(""), qt.IsFalse)
+	c.Assert(OriginName(OriginECDSA), qt.Equals, OriginECDSAName)
+	c.Assert(OriginName(OriginBlind), qt.Equals, OriginBlindName)
+	c.Assert(OriginName(models.CensusOrigin_OFF_CHAIN_CA_V2), qt.Equals, "")
+}
diff --git a/csp/signers/electionkey/vectors_test.go b/csp/signers/electionkey/vectors_test.go
new file mode 100644
index 0000000..0732282
--- /dev/null
+++ b/csp/signers/electionkey/vectors_test.go
@@ -0,0 +1,323 @@
+package electionkey
+
+import (
+	"crypto/ecdsa"
+	"encoding/hex"
+	"encoding/json"
+	"flag"
+	"math/big"
+	"os"
+	"path/filepath"
+	"testing"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	qt "github.com/frankban/quicktest"
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+// updateVectors regenerates testdata/csp-v3-vectors.json. The file is the shared reference for
+// every implementation of the derivation and of V3 proofs (vochain-sequencer and the
+// integrator SDK testkit keep byte-identical copies), so regenerate it only when the scheme
+// changes, and copy it to them in the same change.
+var updateVectors = flag.Bool("update", false, "regenerate testdata/csp-v3-vectors.json")
+
+const vectorsFile = "testdata/csp-v3-vectors.json"
+
+// Vectors is the schema of testdata/csp-v3-vectors.json. Byte strings are lowercase hex
+// without 0x, weights are decimal strings.
+type Vectors struct {
+	Description string           `json:"description"`
+	Root        string           `json:"root"`
+	Voter       KeyVector        `json:"voter"`
+	Elections   []ElectionVector `json:"elections"`
+	Proofs      []ProofVector    `json:"proofs"`
+}
+
+// ElectionVector holds the keys derived for one seed, and the weight credentials of its blind
+// election.
+type ElectionVector struct {
+	Seed           string         `json:"seed"`
+	ProcessIDECDSA string         `json:"processIdEcdsa"`
+	ProcessIDBlind string         `json:"processIdBlind"`
+	ElectionECDSA  KeyVector      `json:"electionEcdsaKey"`
+	ElectionBlind  KeyVector      `json:"electionBlindKey"`
+	Weights        []WeightVector `json:"weights"`
+}
+
+// KeyVector is a derived key pair.
+type KeyVector struct {
+	Private string `json:"private"`
+	Public  string `json:"public"`
+}
+
+// WeightVector is the weight key and certificate of one weight on the blind election.
+type WeightVector struct {
+	Weight        string    `json:"weight"`
+	WeightKey     KeyVector `json:"weightKey"`
+	CertMessage   string    `json:"certMessage"`
+	CertSignature string    `json:"certSignature"`
+}
+
+// ProofVector is a complete, valid V3 vote proof: the census root and origin of the election,
+// the CAbundle fields and their protobuf encoding, and the ProofCA fields. VoteWeight is empty
+// when the bundle carries no voteWeight (the chain then uses weight 1).
+type ProofVector struct {
+	Name          string `json:"name"`
+	CensusOrigin  string `json:"censusOrigin"`
+	CensusRoot    string `json:"censusRoot"`
+	ProcessID     string `json:"processId"`
+	Address       string `json:"address"`
+	VoteWeight    string `json:"voteWeight"`
+	Weight        string `json:"weight"`
+	Bundle        string `json:"bundle"`
+	ProofType     string `json:"proofType"`
+	Signature     string `json:"signature"`
+	WeightKey     string `json:"weightKey,omitempty"`
+	CertSignature string `json:"certSignature,omitempty"`
+}
+
+var (
+	vectorRoot  = "8f1d3c5e7a9b0c2d4e6f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5"
+	vectorSeeds = []string{"65f0a1b2c3d4e5f60718293a", "65f0a1b2c3d4e5f60718293b"}
+	// processIDs follow the on-chain layout: keccak(chainID)[:6] || org (20) || origin || envelope
+	// type || nonce (4). Byte 26 is the census origin (5 ECDSA, 6 blind).
+	vectorPIDsECDSA = []string{
+		"c0ffee0000010000000000000000000000000000000000aa0500000000000001",
+		"c0ffee0000010000000000000000000000000000000000aa0500000000000002",
+	}
+	vectorPIDsBlind = []string{
+		"c0ffee0000010000000000000000000000000000000000aa0600000000000001",
+		"c0ffee0000010000000000000000000000000000000000aa0600000000000002",
+	}
+	vectorWeights = []string{"1", "42", "340282366920938463463374607431768211455"}
+	// vectorVoterKey is the voter of every proof vector; its address goes in the bundles and its
+	// public key is the VoterID a verifier checks the bundle address against.
+	vectorVoterKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
+)
+
+func TestVectors(t *testing.T) {
+	c := qt.New(t)
+	if *updateVectors {
+		v := generateVectors(c)
+		data, err := json.MarshalIndent(v, "", "  ")
+		c.Assert(err, qt.IsNil)
+		c.Assert(os.MkdirAll(filepath.Dir(vectorsFile), 0o755), qt.IsNil)
+		c.Assert(os.WriteFile(vectorsFile, append(data, '\n'), 0o600), qt.IsNil)
+	}
+	data, err := os.ReadFile(vectorsFile)
+	c.Assert(err, qt.IsNil)
+	var v Vectors
+	c.Assert(json.Unmarshal(data, &v), qt.IsNil)
+	c.Assert(v.Elections, qt.HasLen, len(vectorSeeds))
+	c.Assert(v.Proofs, qt.HasLen, 3)
+
+	root, err := NewRoot(mustHex(c, v.Root))
+	c.Assert(err, qt.IsNil)
+	for _, ev := range v.Elections {
+		var seed [SeedSize]byte
+		copy(seed[:], mustHex(c, ev.Seed))
+		dE, err := root.ElectionECDSAKey(seed)
+		c.Assert(err, qt.IsNil)
+		c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(dE)), qt.Equals, ev.ElectionECDSA.Private)
+		c.Assert(hex.EncodeToString(CompressedPubKey(dE)), qt.Equals, ev.ElectionECDSA.Public)
+		dB, err := root.ElectionBlindKey(seed)
+		c.Assert(err, qt.IsNil)
+		c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(dB)), qt.Equals, ev.ElectionBlind.Private)
+		c.Assert(hex.EncodeToString(CompressedPubKey(dB)), qt.Equals, ev.ElectionBlind.Public)
+		for _, wv := range ev.Weights {
+			w, ok := new(big.Int).SetString(wv.Weight, 10)
+			c.Assert(ok, qt.IsTrue)
+			cred, err := IssueWeightCredential(dB, mustHex(c, ev.ProcessIDBlind), w)
+			c.Assert(err, qt.IsNil)
+			c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(cred.Key)), qt.Equals, wv.WeightKey.Private)
+			c.Assert(hex.EncodeToString(cred.WeightKey), qt.Equals, wv.WeightKey.Public)
+			msg, err := CertMessage(mustHex(c, ev.ProcessIDBlind), w, cred.WeightKey)
+			c.Assert(err, qt.IsNil)
+			c.Assert(msg, qt.HasLen, CertMessageSize)
+			c.Assert(hex.EncodeToString(msg), qt.Equals, wv.CertMessage)
+			c.Assert(hex.EncodeToString(cred.Certificate), qt.Equals, wv.CertSignature)
+		}
+	}
+	voterKey, err := ethcrypto.HexToECDSA(v.Voter.Private)
+	c.Assert(err, qt.IsNil)
+	c.Assert(hex.EncodeToString(CompressedPubKey(voterKey)), qt.Equals, v.Voter.Public)
+	voterAddr := hex.EncodeToString(ethcrypto.PubkeyToAddress(voterKey.PublicKey).Bytes())
+	for _, pv := range v.Proofs {
+		c.Assert(pv.Address, qt.Equals, voterAddr)
+		checkProofVector(c, &pv)
+	}
+}
+
+// checkProofVector verifies a proof vector the way the Vochain does, using only public data.
+func checkProofVector(c *qt.C, pv *ProofVector) {
+	bundle := &models.CAbundle{ProcessId: mustHex(c, pv.ProcessID), Address: mustHex(c, pv.Address)}
+	if pv.VoteWeight != "" {
+		bundle.VoteWeight = mustHex(c, pv.VoteWeight)
+	}
+	bundleBytes, err := proto.Marshal(bundle)
+	c.Assert(err, qt.IsNil)
+	c.Assert(hex.EncodeToString(bundleBytes), qt.Equals, pv.Bundle, qt.Commentf(pv.Name))
+	w, err := NormalizeWeight(bundle.VoteWeight)
+	c.Assert(err, qt.IsNil)
+	c.Assert(w.String(), qt.Equals, pv.Weight)
+	switch pv.CensusOrigin {
+	case OriginECDSAName:
+		c.Assert(pv.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
+		pub, err := vocdonicrypto.PubKeyFromSignature(bundleBytes, mustHex(c, pv.Signature))
+		c.Assert(err, qt.IsNil)
+		c.Assert(hex.EncodeToString(pub), qt.Equals, pv.CensusRoot)
+	case OriginBlindName:
+		c.Assert(pv.ProofType, qt.Equals, models.ProofCA_ECDSA_BLIND.String())
+		msg, err := CertMessage(bundle.ProcessId, w, mustHex(c, pv.WeightKey))
+		c.Assert(err, qt.IsNil)
+		pub, err := vocdonicrypto.PubKeyFromSignature(msg, mustHex(c, pv.CertSignature))
+		c.Assert(err, qt.IsNil)
+		c.Assert(hex.EncodeToString(pub), qt.Equals, pv.CensusRoot)
+		uncompressed, err := vocdonicrypto.DecompressPubKey(mustHex(c, pv.WeightKey))
+		c.Assert(err, qt.IsNil)
+		blindPub, err := blind.NewPublicKeyFromECDSA(uncompressed)
+		c.Assert(err, qt.IsNil)
+		sig, err := blind.NewSignatureFromBytesUncompressed(mustHex(c, pv.Signature))
+		c.Assert(err, qt.IsNil)
+		m := new(big.Int).SetBytes(vocdonicrypto.HashRaw(bundleBytes))
+		c.Assert(blind.Verify(m, sig, blindPub), qt.IsTrue, qt.Commentf(pv.Name))
+	default:
+		c.Fatalf("unknown census origin %q", pv.CensusOrigin)
+	}
+}
+
+func generateVectors(c *qt.C) *Vectors {
+	root, err := NewRoot(mustHex(c, vectorRoot))
+	c.Assert(err, qt.IsNil)
+	v := &Vectors{
+		Description: "CSP per-election keys (OFF_CHAIN_CA_V3_*): derivation from a test root, weight " +
+			"certificates and complete valid proofs. Generated by saas-backend " +
+			"csp/signers/electionkey (go test -run TestVectors -update); copies must stay byte-identical.",
+		Root: vectorRoot,
+	}
+	voterKey, err := ethcrypto.HexToECDSA(vectorVoterKey)
+	c.Assert(err, qt.IsNil)
+	v.Voter = keyVector(voterKey, CompressedPubKey(voterKey))
+	voterAddr := ethcrypto.PubkeyToAddress(voterKey.PublicKey)
+	vectorVoter := hex.EncodeToString(voterAddr.Bytes())
+	for i, s := range vectorSeeds {
+		var seed [SeedSize]byte
+		copy(seed[:], mustHex(c, s))
+		dE, err := root.ElectionECDSAKey(seed)
+		c.Assert(err, qt.IsNil)
+		dB, err := root.ElectionBlindKey(seed)
+		c.Assert(err, qt.IsNil)
+		ev := ElectionVector{
+			Seed:           s,
+			ProcessIDECDSA: vectorPIDsECDSA[i],
+			ProcessIDBlind: vectorPIDsBlind[i],
+			ElectionECDSA:  keyVector(dE, CompressedPubKey(dE)),
+			ElectionBlind:  keyVector(dB, CompressedPubKey(dB)),
+		}
+		for _, ws := range vectorWeights {
+			w, _ := new(big.Int).SetString(ws, 10)
+			cred, err := IssueWeightCredential(dB, mustHex(c, vectorPIDsBlind[i]), w)
+			c.Assert(err, qt.IsNil)
+			msg, err := CertMessage(mustHex(c, vectorPIDsBlind[i]), w, cred.WeightKey)
+			c.Assert(err, qt.IsNil)
+			ev.Weights = append(ev.Weights, WeightVector{
+				Weight:        ws,
+				WeightKey:     keyVector(cred.Key, cred.WeightKey),
+				CertMessage:   hex.EncodeToString(msg),
+				CertSignature: hex.EncodeToString(cred.Certificate),
+			})
+		}
+		v.Elections = append(v.Elections, ev)
+	}
+
+	var seed [SeedSize]byte
+	copy(seed[:], mustHex(c, vectorSeeds[0]))
+	dE, err := root.ElectionECDSAKey(seed)
+	c.Assert(err, qt.IsNil)
+	dB, err := root.ElectionBlindKey(seed)
+	c.Assert(err, qt.IsNil)
+	voter := voterAddr.Bytes()
+
+	// ECDSA proof, weight 42
+	pidE := mustHex(c, vectorPIDsECDSA[0])
+	bundle := &models.CAbundle{ProcessId: pidE, Address: voter, VoteWeight: big.NewInt(42).Bytes()}
+	bundleBytes, err := proto.Marshal(bundle)
+	c.Assert(err, qt.IsNil)
+	sig, err := SignEthereum(dE, bundleBytes)
+	c.Assert(err, qt.IsNil)
+	v.Proofs = append(v.Proofs, ProofVector{
+		Name: "ecdsa weight 42", CensusOrigin: OriginECDSAName,
+		CensusRoot: hex.EncodeToString(CompressedPubKey(dE)), ProcessID: vectorPIDsECDSA[0],
+		Address: vectorVoter, VoteWeight: hex.EncodeToString(bundle.VoteWeight), Weight: "42",
+		Bundle: hex.EncodeToString(bundleBytes), ProofType: models.ProofCA_ECDSA.String(),
+		Signature: hex.EncodeToString(sig),
+	})
+
+	// blind proofs: weight 42, and weight 1 with no voteWeight in the bundle
+	pidB := mustHex(c, vectorPIDsBlind[0])
+	for _, tc := range []struct {
+		name   string
+		weight *big.Int
+		inline bool
+	}{
+		{name: "blind weight 42", weight: big.NewInt(42), inline: true},
+		{name: "blind default weight", weight: big.NewInt(1), inline: false},
+	} {
+		bundle := &models.CAbundle{ProcessId: pidB, Address: voter}
+		if tc.inline {
+			bundle.VoteWeight = tc.weight.Bytes()
+		}
+		bundleBytes, err := proto.Marshal(bundle)
+		c.Assert(err, qt.IsNil)
+		cred, err := IssueWeightCredential(dB, pidB, tc.weight)
+		c.Assert(err, qt.IsNil)
+		sig := blindSignForVector(c, cred, bundleBytes)
+		v.Proofs = append(v.Proofs, ProofVector{
+			Name: tc.name, CensusOrigin: OriginBlindName,
+			CensusRoot: hex.EncodeToString(CompressedPubKey(dB)), ProcessID: vectorPIDsBlind[0],
+			Address: vectorVoter, VoteWeight: hex.EncodeToString(bundle.VoteWeight), Weight: tc.weight.String(),
+			Bundle: hex.EncodeToString(bundleBytes), ProofType: models.ProofCA_ECDSA_BLIND.String(),
+			Signature: hex.EncodeToString(sig), WeightKey: hex.EncodeToString(cred.WeightKey),
+			CertSignature: hex.EncodeToString(cred.Certificate),
+		})
+	}
+	return v
+}
+
+// blindSignForVector runs the blind protocol with both roles: the CSP signing with dX and the
+// voter blinding keccak256(bundle). It returns the unblinded signature in the uncompressed form a
+// ProofCA carries.
+func blindSignForVector(c *qt.C, cred *WeightCredential, bundleBytes []byte) []byte {
+	m := new(big.Int).SetBytes(vocdonicrypto.HashRaw(bundleBytes))
+	sk := blind.PrivateKey(*new(big.Int).SetBytes(ethcrypto.FromECDSA(cred.Key)))
+	for range 64 {
+		k, signerR, err := blind.NewRequestParameters()
+		c.Assert(err, qt.IsNil)
+		mBlinded, secret, err := blind.Blind(m, signerR)
+		if err != nil {
+			continue
+		}
+		sBlind, err := sk.BlindSign(mBlinded, k)
+		if err != nil {
+			continue
+		}
+		sig, err := blind.Unblind(sBlind, secret)
+		c.Assert(err, qt.IsNil)
+		return sig.BytesUncompressed()
+	}
+	c.Fatalf("blind signature failed")
+	return nil
+}
+
+func keyVector(key *ecdsa.PrivateKey, pub []byte) KeyVector {
+	return KeyVector{Private: hex.EncodeToString(ethcrypto.FromECDSA(key)), Public: hex.EncodeToString(pub)}
+}
+
+func mustHex(c *qt.C, s string) []byte {
+	b, err := hex.DecodeString(s)
+	c.Assert(err, qt.IsNil)
+	return b
+}
```

```bash
cd SAAS && go test ./csp/signers/electionkey/ -count=1
```
Expected failure: build error, `undefined: NewRoot` (package has no non-test files).

Step 2 (implementation):

```diff
diff --git a/csp/signers/electionkey/electionkey.go b/csp/signers/electionkey/electionkey.go
new file mode 100644
index 0000000..4f8c122
--- /dev/null
+++ b/csp/signers/electionkey/electionkey.go
@@ -0,0 +1,272 @@
+// Package electionkey derives the per-election CSP keys of OFF_CHAIN_CA_V3_ECDSA and
+// OFF_CHAIN_CA_V3_BLIND elections from the CSP root key, and builds the weight certificates that
+// bind a blind signing key to one election and one vote weight.
+//
+// Every key is a keyed hash of the root secret and a public seed (the Mongo ObjectID of the
+// VotingProcessQuestion behind the election), so no two published keys have a publicly computable
+// relation. Nothing is stored: the same root and seed always give the same keys.
+//
+//	hashToScalar(tag, data) = first keccak256(tag || u32be(i) || data), i = 0..255, in (0, n)
+//	dE = hashToScalar("vocdoni/csp/v3/election-ecdsa", root || seed)   census root of an ECDSA election
+//	dB = hashToScalar("vocdoni/csp/v3/election-blind", root || seed)   census root of a blind election
+//	dX = hashToScalar("vocdoni/csp/v3/weight-key", u256be(dB) || u256be(w))   blind key for weight w
+//
+// dE signs CA bundles. dB only signs weight certificates. dX only signs blind messages. The tags
+// are prefix-free (no tag is a prefix of another, including CertTag); a new tag must keep that.
+package electionkey
+
+import (
+	"crypto/ecdsa"
+	"encoding/binary"
+	"fmt"
+	"math/big"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+const (
+	// TagElectionECDSA derives dE, the census root key of an OFF_CHAIN_CA_V3_ECDSA election.
+	TagElectionECDSA = "vocdoni/csp/v3/election-ecdsa"
+	// TagElectionBlind derives dB, the census root key of an OFF_CHAIN_CA_V3_BLIND election.
+	TagElectionBlind = "vocdoni/csp/v3/election-blind"
+	// TagWeightKey derives dX, the blind signing key of one vote weight of a blind election.
+	TagWeightKey = "vocdoni/csp/v3/weight-key"
+	// CertTag prefixes every weight certificate message. Its first byte (0x76) has protobuf wire
+	// type 6, so a certificate message is never a valid CAbundle encoding.
+	CertTag = "vocdoni/csp/v3/weight-cert"
+
+	// SeedSize is the size of a derivation seed: a Mongo ObjectID.
+	SeedSize = 12
+	// RootSize is the size of the CSP root private key.
+	RootSize = 32
+	// ProcessIDSize is the size of an on-chain election id.
+	ProcessIDSize = 32
+	// WeightSize is the fixed width of a vote weight inside derivations and certificates.
+	WeightSize = 32
+	// PubKeySize is the size of a compressed secp256k1 public key.
+	PubKeySize = 33
+	// CertMessageSize is the size of a weight certificate message.
+	CertMessageSize = len(CertTag) + ProcessIDSize + WeightSize + PubKeySize
+
+	// maxCounter bounds hashToScalar. A keccak output falls outside (0, n) with probability
+	// about 2^-128, so the bound is never reached in practice.
+	maxCounter = 256
+)
+
+// The census origins of per-election CSP keys, as numbered by the CensusOrigin enum of
+// go.vocdoni.io/proto (vochain.proto, 1.18.0). This module pins an older proto release that
+// predates them, so they are declared here, together with their enum names, which is the form
+// stored on each published question (db.VotingProcessQuestion.CensusOrigin).
+const (
+	// OriginECDSA is CensusOrigin OFF_CHAIN_CA_V3_ECDSA: proofs are ProofCA ECDSA, signed by dE.
+	OriginECDSA models.CensusOrigin = 5
+	// OriginBlind is CensusOrigin OFF_CHAIN_CA_V3_BLIND: proofs are ProofCA ECDSA_BLIND, signed
+	// by dX and carrying a weight certificate signed by dB.
+	OriginBlind models.CensusOrigin = 6
+	// OriginECDSAName is the enum name of OriginECDSA.
+	OriginECDSAName = "OFF_CHAIN_CA_V3_ECDSA"
+	// OriginBlindName is the enum name of OriginBlind.
+	OriginBlindName = "OFF_CHAIN_CA_V3_BLIND"
+)
+
+// IsV3Origin reports whether a stored census origin name is one of the per-election key origins.
+func IsV3Origin(name string) bool {
+	return name == OriginECDSAName || name == OriginBlindName
+}
+
+// OriginName returns the enum name of a per-election CSP census origin, or "" for any other
+// origin. The protobuf release this module pins has no names for these origins.
+func OriginName(origin models.CensusOrigin) string {
+	switch origin {
+	case OriginECDSA:
+		return OriginECDSAName
+	case OriginBlind:
+		return OriginBlindName
+	default:
+		return ""
+	}
+}
+
+// curveN is the order of the secp256k1 group.
+var curveN = ethcrypto.S256().Params().N
+
+// HashToScalar returns the first keccak256(tag || u32be(i) || data), for i = 0..255, that is a
+// valid secp256k1 private scalar (0 < h < n).
+func HashToScalar(tag string, data []byte) (*big.Int, error) {
+	var counter [4]byte
+	for i := range maxCounter {
+		binary.BigEndian.PutUint32(counter[:], uint32(i))
+		h := new(big.Int).SetBytes(ethcrypto.Keccak256([]byte(tag), counter[:], data))
+		if h.Sign() > 0 && h.Cmp(curveN) < 0 {
+			return h, nil
+		}
+	}
+	return nil, fmt.Errorf("no valid scalar for tag %q after %d attempts", tag, maxCounter)
+}
+
+// Root is the CSP root secret the per-election keys derive from.
+type Root struct {
+	key [RootSize]byte
+}
+
+// NewRoot returns the Root for a 32-byte CSP private key.
+func NewRoot(key []byte) (*Root, error) {
+	if len(key) != RootSize {
+		return nil, fmt.Errorf("root key is %d bytes, expected %d", len(key), RootSize)
+	}
+	d := new(big.Int).SetBytes(key)
+	if d.Sign() == 0 || d.Cmp(curveN) >= 0 {
+		return nil, fmt.Errorf("root key is not a valid secp256k1 scalar")
+	}
+	r := &Root{}
+	copy(r.key[:], key)
+	return r, nil
+}
+
+// ElectionECDSAKey returns dE, the key whose public key is the census root of the
+// OFF_CHAIN_CA_V3_ECDSA election backed by seed, and which signs its CA bundles.
+func (r *Root) ElectionECDSAKey(seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
+	return r.electionKey(TagElectionECDSA, seed)
+}
+
+// ElectionBlindKey returns dB, the key whose public key is the census root of the
+// OFF_CHAIN_CA_V3_BLIND election backed by seed, and which signs its weight certificates.
+func (r *Root) ElectionBlindKey(seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
+	return r.electionKey(TagElectionBlind, seed)
+}
+
+func (r *Root) electionKey(tag string, seed [SeedSize]byte) (*ecdsa.PrivateKey, error) {
+	data := make([]byte, 0, RootSize+SeedSize)
+	data = append(data, r.key[:]...)
+	data = append(data, seed[:]...)
+	d, err := HashToScalar(tag, data)
+	if err != nil {
+		return nil, err
+	}
+	return scalarToKey(d)
+}
+
+// WeightKey returns dX, the blind signing key of vote weight w in the blind election whose
+// election key is electionBlindKey (dB).
+func WeightKey(electionBlindKey *ecdsa.PrivateKey, w *big.Int) (*ecdsa.PrivateKey, error) {
+	if electionBlindKey == nil {
+		return nil, fmt.Errorf("nil election blind key")
+	}
+	weight, err := u256(w)
+	if err != nil {
+		return nil, err
+	}
+	data := make([]byte, 0, 2*WeightSize)
+	data = append(data, ethcrypto.FromECDSA(electionBlindKey)...)
+	data = append(data, weight...)
+	d, err := HashToScalar(TagWeightKey, data)
+	if err != nil {
+		return nil, err
+	}
+	return scalarToKey(d)
+}
+
+// CertMessage returns the message a weight certificate signs:
+// CertTag || processID (32 bytes) || u256be(w) || weightKey (33 bytes, compressed).
+func CertMessage(processID []byte, w *big.Int, weightKey []byte) ([]byte, error) {
+	if len(processID) != ProcessIDSize {
+		return nil, fmt.Errorf("process id is %d bytes, expected %d", len(processID), ProcessIDSize)
+	}
+	if len(weightKey) != PubKeySize {
+		return nil, fmt.Errorf("weight key is %d bytes, expected %d", len(weightKey), PubKeySize)
+	}
+	weight, err := u256(w)
+	if err != nil {
+		return nil, err
+	}
+	msg := make([]byte, 0, CertMessageSize)
+	msg = append(msg, CertTag...)
+	msg = append(msg, processID...)
+	msg = append(msg, weight...)
+	msg = append(msg, weightKey...)
+	return msg, nil
+}
+
+// WeightCredential is the blind signing key of one vote weight of a blind election, together with
+// its public weight certificate.
+type WeightCredential struct {
+	// Key is dX: it blind-signs the ballots of this weight and never leaves the CSP.
+	Key *ecdsa.PrivateKey
+	// WeightKey is the compressed public key of Key (33 bytes).
+	WeightKey []byte
+	// Certificate is the Ethereum personal-sign signature by dB of CertMessage(processID, w, WeightKey)
+	// (65 bytes). It is deterministic (RFC 6979 nonces) and public.
+	Certificate []byte
+}
+
+// IssueWeightCredential derives dX for weight w from electionBlindKey (dB) and certifies it for
+// processID.
+func IssueWeightCredential(electionBlindKey *ecdsa.PrivateKey, processID []byte, w *big.Int) (*WeightCredential, error) {
+	key, err := WeightKey(electionBlindKey, w)
+	if err != nil {
+		return nil, err
+	}
+	weightPub := CompressedPubKey(key)
+	msg, err := CertMessage(processID, w, weightPub)
+	if err != nil {
+		return nil, err
+	}
+	sig, err := SignEthereum(electionBlindKey, msg)
+	if err != nil {
+		return nil, fmt.Errorf("could not sign weight certificate: %w", err)
+	}
+	return &WeightCredential{Key: key, WeightKey: weightPub, Certificate: sig}, nil
+}
+
+// SignEthereum returns the 65-byte Ethereum personal-sign signature of msg by key, with the
+// recovery byte in {0, 1}: the signature the Vochain recovers CSP keys from.
+func SignEthereum(key *ecdsa.PrivateKey, msg []byte) ([]byte, error) {
+	if key == nil {
+		return nil, fmt.Errorf("nil key")
+	}
+	return ethcrypto.Sign(vocdonicrypto.Hash(msg), key)
+}
+
+// CompressedPubKey returns the 33-byte compressed public key of key.
+func CompressedPubKey(key *ecdsa.PrivateKey) []byte {
+	return ethcrypto.CompressPubkey(&key.PublicKey)
+}
+
+// NormalizeWeight turns a CSP-authorized weight as carried in a CA bundle (minimal big-endian
+// bytes) into the integer the keys and certificates use. A missing or empty weight is 1, the
+// Vochain's default when a bundle has no voteWeight. Zero and weights wider than 32 bytes are
+// rejected, as the Vochain rejects them.
+func NormalizeWeight(weight []byte) (*big.Int, error) {
+	if len(weight) == 0 {
+		return big.NewInt(1), nil
+	}
+	if len(weight) > WeightSize {
+		return nil, fmt.Errorf("weight is %d bytes, at most %d allowed", len(weight), WeightSize)
+	}
+	w := new(big.Int).SetBytes(weight)
+	if w.Sign() == 0 {
+		return nil, fmt.Errorf("weight is zero")
+	}
+	return w, nil
+}
+
+// u256 encodes w as 32 big-endian bytes, rejecting nil, zero, negative or oversized weights.
+func u256(w *big.Int) ([]byte, error) {
+	if w == nil || w.Sign() <= 0 {
+		return nil, fmt.Errorf("weight must be positive")
+	}
+	if w.BitLen() > 8*WeightSize {
+		return nil, fmt.Errorf("weight does not fit in %d bytes", WeightSize)
+	}
+	return w.FillBytes(make([]byte, WeightSize)), nil
+}
+
+func scalarToKey(d *big.Int) (*ecdsa.PrivateKey, error) {
+	key, err := ethcrypto.ToECDSA(d.FillBytes(make([]byte, RootSize)))
+	if err != nil {
+		return nil, fmt.Errorf("invalid derived scalar: %w", err)
+	}
+	return key, nil
+}
```

```bash
go test ./csp/signers/electionkey/ -count=1 -v | grep -c '^--- PASS'
~/go/bin/golangci-lint-v2 run ./csp/signers/electionkey/ && scripts/check-qt-patterns.sh
```
Expected pass: 9 tests (HashToScalarMatchesDefinition, NewRootRejectsInvalidKeys,
ElectionKeysAreDistinctAndDeterministic, WeightKeyDependsOnWeight, CertMessageLayout,
IssueWeightCredentialRecoversElectionKey, NormalizeWeight, IsV3Origin, Vectors); lint clean.

Step 3: `git commit -m "feat(csp): derive per-election csp keys and weight certificates" -- csp/signers/electionkey/`

Interfaces this task provides: `electionkey.NewRoot(key []byte) (*Root, error)`,
`(*Root).ElectionECDSAKey/ElectionBlindKey(seed [SeedSize]byte) (*ecdsa.PrivateKey, error)`,
`WeightKey`, `CertMessage`, `IssueWeightCredential(dB, processID, w) (*WeightCredential, error)`,
`SignEthereum`, `CompressedPubKey`, `NormalizeWeight`, `IsV3Origin(name)`, `OriginName(origin)`,
`OriginECDSA`/`OriginBlind` (`models.CensusOrigin` 5/6) and `OriginECDSAName`/`OriginBlindName`.

### Task 6: saas-backend CSP signing by question origin, and sign responses with proofType

Model: opus   Repo: SAAS   Files: csp/csp.go, csp/sign.go, csp/election.go, csp/election_test.go,
csp/handlers/types.go, csp/handlers/processes.go, csp/handlers/sign.go, db/types.go, docs/swagger.yaml

Goal: the CSP picks the scheme from the question's stored census origin: V3_ECDSA signs with `dE`
(type `ECDSA`), V3_BLIND blind-signs with `dX` and returns X and its certificate (type
`ECDSA_BLIND`), empty origin signs exactly as today (`ECDSA_PIDSALTED` / `ECDSA_BLIND_PIDSALTED`).
Every sign result carries `proofType` (AC5, AC6). `db.VotingProcessQuestion` gains `CensusOrigin`
and `CensusRoot` (written by Task 7).

Step 1 (failing test):

```diff
diff --git a/csp/election_test.go b/csp/election_test.go
new file mode 100644
index 0000000..a6d7c68
--- /dev/null
+++ b/csp/election_test.go
@@ -0,0 +1,202 @@
+package csp
+
+import (
+	"context"
+	"crypto/ecdsa"
+	"math/big"
+	"testing"
+	"time"
+
+	qt "github.com/frankban/quicktest"
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
+	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/internal"
+	"github.com/vocdoni/saas-backend/test"
+	"go.mongodb.org/mongo-driver/v2/bson"
+	"go.vocdoni.io/dvote/crypto/ethereum"
+	"go.vocdoni.io/dvote/util"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+// failingSaltedSigner is a SaltedSigner that fails the test when any of its methods is called: the
+// per-election signing paths must never touch the salted root key.
+type failingSaltedSigner struct {
+	c *qt.C
+}
+
+func (s failingSaltedSigner) SignECDSA([saltedkey.SaltSize]byte, []byte) ([]byte, error) {
+	s.c.Fatalf("salted ECDSA signer used")
+	return nil, nil
+}
+
+func (s failingSaltedSigner) SignBlind([saltedkey.SaltSize]byte, []byte, *big.Int) ([]byte, error) {
+	s.c.Fatalf("salted blind signer used")
+	return nil, nil
+}
+
+func (s failingSaltedSigner) ECDSAPubKey() (*ecdsa.PublicKey, error) {
+	s.c.Fatalf("salted signer ECDSA public key used")
+	return nil, nil
+}
+
+func (s failingSaltedSigner) BlindPubKey() *blind.PublicKey {
+	s.c.Fatalf("salted signer blind public key used")
+	return nil
+}
+
+func newElectionTestCSP(c *qt.C) *CSP {
+	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
+	c.Assert(err, qt.IsNil)
+	csp, err := New(context.Background(), &Config{
+		DB:                       testDB,
+		MailService:              testMailService,
+		SMSService:               testSMSService,
+		NotificationCoolDownTime: time.Second * 5,
+		RootKey:                  *testRootKey,
+	})
+	c.Assert(err, qt.IsNil)
+	csp.Signer = failingSaltedSigner{c: c}
+	return csp
+}
+
+func verifiedTestToken(c *qt.C, csp *CSP) {
+	c.Cleanup(func() { c.Assert(csp.Storage.DeleteAllDocuments(), qt.IsNil) })
+	c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testAnchorID, ""), qt.IsNil)
+	c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
+}
+
+func TestElectionCensusIsPerQuestion(t *testing.T) {
+	c := qt.New(t)
+	csp := newElectionTestCSP(c)
+	q1, q2 := bson.NewObjectID(), bson.NewObjectID()
+
+	origin, root1, err := csp.ElectionCensus(q1, false)
+	c.Assert(err, qt.IsNil)
+	c.Assert(origin, qt.Equals, electionkey.OriginECDSA)
+	c.Assert(root1, qt.HasLen, electionkey.PubKeySize)
+	_, again, err := csp.ElectionCensus(q1, false)
+	c.Assert(err, qt.IsNil)
+	c.Assert(again, qt.DeepEquals, root1)
+	_, root2, err := csp.ElectionCensus(q2, false)
+	c.Assert(err, qt.IsNil)
+	c.Assert(root2, qt.Not(qt.DeepEquals), root1)
+
+	origin, blindRoot, err := csp.ElectionCensus(q1, true)
+	c.Assert(err, qt.IsNil)
+	c.Assert(origin, qt.Equals, electionkey.OriginBlind)
+	c.Assert(blindRoot, qt.Not(qt.DeepEquals), root1)
+}
+
+func TestSignBallotElectionECDSA(t *testing.T) {
+	c := qt.New(t)
+	csp := newElectionTestCSP(c)
+	verifiedTestToken(c, csp)
+	questionID := bson.NewObjectID()
+	_, root, err := csp.ElectionCensus(questionID, false)
+	c.Assert(err, qt.IsNil)
+	pid := internal.HexBytes(util.RandomBytes(32))
+	address := internal.HexBytes(util.RandomBytes(20))
+
+	signed, err := csp.SignBallot(&BallotRequest{
+		Token: testToken, Address: address, Weight: testUserWeightBytes,
+		Election: Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginECDSAName},
+	})
+	c.Assert(err, qt.IsNil)
+	c.Assert(signed.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
+	c.Assert(signed.Weight, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
+	c.Assert(signed.WeightKey, qt.IsNil)
+	c.Assert(signed.WeightCertificate, qt.IsNil)
+
+	// the signature recovers to the election's census root, over the bundle the chain rebuilds
+	bundle, err := proto.Marshal(&models.CAbundle{ProcessId: pid, Address: address, VoteWeight: testUserWeightBytes})
+	c.Assert(err, qt.IsNil)
+	pub, err := ethereum.PubKeyFromSignature(bundle, signed.Signature)
+	c.Assert(err, qt.IsNil)
+	c.Assert(internal.HexBytes(pub), qt.DeepEquals, root)
+
+	// the signing slot is consumed, as on the legacy origins
+	_, err = csp.SignBallot(&BallotRequest{
+		Token: testToken, Address: internal.HexBytes(util.RandomBytes(20)), Weight: testUserWeightBytes,
+		Election: Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginECDSAName},
+	})
+	c.Assert(err, qt.ErrorIs, ErrAddressMismatch)
+	c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
+}
+
+func TestBlindSignBallotElectionBlind(t *testing.T) {
+	c := qt.New(t)
+	csp := newElectionTestCSP(c)
+	verifiedTestToken(c, csp)
+	questionID := bson.NewObjectID()
+	_, root, err := csp.ElectionCensus(questionID, true)
+	c.Assert(err, qt.IsNil)
+	pid := internal.HexBytes(util.RandomBytes(32))
+	election := Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginBlindName}
+	bundle, err := proto.Marshal(&models.CAbundle{
+		ProcessId: pid, Address: util.RandomBytes(20), VoteWeight: testUserWeightBytes,
+	})
+	c.Assert(err, qt.IsNil)
+	msgHash := new(big.Int).SetBytes(ethereum.HashRaw(bundle))
+
+	var signed *BallotSignature
+	var signature *blind.Signature
+	for range 32 {
+		rBytes, pinned, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
+		c.Assert(err, qt.IsNil)
+		c.Assert(pinned, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
+		signerR, err := blind.NewPointFromBytes(rBytes)
+		c.Assert(err, qt.IsNil)
+		blinded, unblinder, err := blind.Blind(msgHash, signerR)
+		c.Assert(err, qt.IsNil)
+		signed, err = csp.BlindSignBallot(testToken, election, blinded.Bytes())
+		if err != nil {
+			c.Assert(err, qt.ErrorIs, ErrInvalidBlindedMessage) // ~1/256; nonce not consumed
+			continue
+		}
+		signature, err = blind.Unblind(new(big.Int).SetBytes(signed.Signature), unblinder)
+		c.Assert(err, qt.IsNil)
+		break
+	}
+	c.Assert(signature, qt.Not(qt.IsNil))
+	c.Assert(signed.ProofType, qt.Equals, models.ProofCA_ECDSA_BLIND.String())
+	c.Assert(signed.Weight, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
+
+	// the unblinded signature verifies under the weight key...
+	weightKey, err := blind.NewPublicKeyFromECDSA(signed.WeightKey)
+	c.Assert(err, qt.IsNil)
+	c.Assert(blind.Verify(msgHash, signature, weightKey), qt.IsTrue)
+	// ...and the certificate binds that key to this election and weight under the census root
+	certMsg, err := electionkey.CertMessage(pid, new(big.Int).SetBytes(testUserWeightBytes), signed.WeightKey)
+	c.Assert(err, qt.IsNil)
+	pub, err := ethereum.PubKeyFromSignature(certMsg, signed.WeightCertificate)
+	c.Assert(err, qt.IsNil)
+	c.Assert(internal.HexBytes(pub), qt.DeepEquals, root)
+	// the root key itself never signs a ballot
+	rootKey, err := blind.NewPublicKeyFromECDSA(root)
+	c.Assert(err, qt.IsNil)
+	c.Assert(blind.Verify(msgHash, signature, rootKey), qt.IsFalse)
+
+	// single use
+	_, err = csp.BlindSignBallot(testToken, election, signed.Signature)
+	c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
+}
+
+func TestSignBallotRejectsMismatchedScheme(t *testing.T) {
+	c := qt.New(t)
+	csp := newElectionTestCSP(c)
+	verifiedTestToken(c, csp)
+	pid := internal.HexBytes(util.RandomBytes(32))
+
+	_, err := csp.SignBallot(&BallotRequest{
+		Token: testToken, Address: internal.HexBytes(util.RandomBytes(20)), Weight: testUserWeightBytes,
+		Election: Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginBlindName},
+	})
+	c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
+	_, err = csp.BlindSignBallot(testToken,
+		Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginECDSAName},
+		internal.HexBytes(util.RandomBytes(32)))
+	c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
+}
```

```bash
cd SAAS && export PATH=$PATH:$(go env GOROOT)/bin TMPDIR=$HOME/.cache/csp-epic/tmp GOTMPDIR=$HOME/.cache/csp-epic/tmp
go test ./csp/ -run 'ElectionCensus|SignBallot|BlindSignBallot' -count=1
```
Expected failure: build error `undefined: BallotRequest` / `c.SignBallot`.

Step 2 (implementation). UNVERIFIED (verified at implementation) only as an intermediate state:
these files were verified together with Task 7, not alone; `db/types.go` only adds fields, so no
caller breaks.

```diff
diff --git a/csp/csp.go b/csp/csp.go
index d11f6ed..a10205c 100644
--- a/csp/csp.go
+++ b/csp/csp.go
@@ -4,11 +4,14 @@ import (
 	"context"
 	"crypto/ecdsa"
 	"fmt"
+	"math/big"
 	"sync"
 	"time"
 
 	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	blind "github.com/vocdoni/go-blindsecp256k1"
 	"github.com/vocdoni/saas-backend/csp/notifications"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
@@ -98,7 +101,10 @@ type Config struct {
 // notification queue and the per-user notification cooldown time.
 type CSP struct {
 	PasswordSalt string
-	Signer       *saltedkey.SaltedKey
+	// Signer signs questions published on the legacy CSP origins, with keys salted from the root
+	// key. Questions on the per-election origins are signed with electionKeys instead.
+	Signer       SaltedSigner
+	electionKeys *electionkey.Root
 	Storage      *db.MongoStorage
 	signerLock   sync.Map
 	notifyQueue  *notifications.Queue
@@ -122,6 +128,10 @@ func New(ctx context.Context, config *Config) (*CSP, error) {
 	if err != nil {
 		return nil, err
 	}
+	electionKeys, err := electionkey.NewRoot(config.RootKey)
+	if err != nil {
+		return nil, fmt.Errorf("invalid CSP root key: %w", err)
+	}
 	queue := notifications.NewQueue(ctx, notifications.QueueConfig{
 		TTL:                config.NotificationQueueTTL,
 		Workers:            config.NotificationQueueWorkers,
@@ -176,6 +186,7 @@ func New(ctx context.Context, config *Config) (*CSP, error) {
 	return &CSP{
 		Storage:                  config.DB,
 		Signer:                   s,
+		electionKeys:             electionKeys,
 		notifyQueue:              queue,
 		ctx:                      ctx,
 		notificationCoolDownTime: notificationCoolDownTime,
@@ -186,6 +197,16 @@ func New(ctx context.Context, config *Config) (*CSP, error) {
 	}, nil
 }
 
+// SaltedSigner signs with keys salted from the CSP root key: the signing schemes of the legacy
+// OFF_CHAIN_CA (ECDSA, process id salt) and OFF_CHAIN_CA_V2 (blind, process id and weight salt)
+// census origins. saltedkey.SaltedKey implements it.
+type SaltedSigner interface {
+	SignECDSA(salt [saltedkey.SaltSize]byte, msg []byte) ([]byte, error)
+	SignBlind(salt [saltedkey.SaltSize]byte, msgBlinded []byte, secretK *big.Int) ([]byte, error)
+	ECDSAPubKey() (*ecdsa.PublicKey, error)
+	BlindPubKey() *blind.PublicKey
+}
+
 // pushChallenge enqueues a notification challenge. In synchronous-delivery mode
 // (notifySync, used by tests) it blocks until the challenge has been delivered
 // or given up, so callers observe a deterministic happens-before instead of
diff --git a/csp/election.go b/csp/election.go
new file mode 100644
index 0000000..03287d0
--- /dev/null
+++ b/csp/election.go
@@ -0,0 +1,205 @@
+package csp
+
+import (
+	"crypto/ecdsa"
+	"errors"
+	"fmt"
+	"math/big"
+
+	ethcrypto "github.com/ethereum/go-ethereum/crypto"
+	blind "github.com/vocdoni/go-blindsecp256k1"
+	"github.com/vocdoni/saas-backend/csp/signers"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/internal"
+	"go.mongodb.org/mongo-driver/v2/bson"
+	"go.vocdoni.io/dvote/log"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+// Election identifies the on-chain election of a published question, and through its census
+// origin the scheme that signs its ballots.
+type Election struct {
+	// ID is the on-chain election id (the question's upstreamId).
+	ID internal.HexBytes
+	// QuestionID is the question's id: the seed of its per-election keys.
+	QuestionID bson.ObjectID
+	// CensusOrigin is the census origin name stored on the question: OFF_CHAIN_CA_V3_ECDSA or
+	// OFF_CHAIN_CA_V3_BLIND, or empty for a question published on a legacy CSP origin.
+	CensusOrigin string
+}
+
+// ElectionOf returns the Election of a published question.
+func ElectionOf(q *db.VotingProcessQuestion) Election {
+	return Election{ID: q.UpstreamID, QuestionID: q.ID, CensusOrigin: q.CensusOrigin}
+}
+
+// BallotRequest is a request to sign the CA bundle of one ballot.
+type BallotRequest struct {
+	Token    internal.HexBytes
+	Address  internal.HexBytes
+	Weight   internal.HexBytes
+	Election Election
+}
+
+// BallotSignature is a CSP signature of one ballot and what the client needs to turn it into a
+// proof.
+type BallotSignature struct {
+	Signature internal.HexBytes
+	// ProofType is the ProofCA.Type name of the proof the client builds with Signature.
+	ProofType string
+	// Weight is the weight the signature authorizes.
+	Weight internal.HexBytes
+	// WeightKey and WeightCertificate are set for an OFF_CHAIN_CA_V3_BLIND election: the compressed
+	// public key that blind-signed the ballot and its certificate by the election key, which the
+	// client puts in ProofCA.weightCertificate.
+	WeightKey         internal.HexBytes
+	WeightCertificate internal.HexBytes
+}
+
+// ElectionCensus returns the census origin and census root to publish the election of a question
+// with: OFF_CHAIN_CA_V3_BLIND and the public key of its election blind key for an anonymous census,
+// OFF_CHAIN_CA_V3_ECDSA and the public key of its election ECDSA key otherwise. Both depend only on
+// the root key and the question id, so a retried publish gets the same values.
+//
+//nolint:revive // flag-parameter: anonymous is the census's own mode, which picks the scheme
+func (c *CSP) ElectionCensus(questionID bson.ObjectID, anonymous bool) (models.CensusOrigin, internal.HexBytes, error) {
+	if anonymous {
+		key, err := c.electionKeys.ElectionBlindKey(questionID)
+		if err != nil {
+			return 0, nil, fmt.Errorf("could not derive election blind key: %w", err)
+		}
+		return electionkey.OriginBlind, electionkey.CompressedPubKey(key), nil
+	}
+	key, err := c.electionKeys.ElectionECDSAKey(questionID)
+	if err != nil {
+		return 0, nil, fmt.Errorf("could not derive election key: %w", err)
+	}
+	return electionkey.OriginECDSA, electionkey.CompressedPubKey(key), nil
+}
+
+// SignBallot signs the CA bundle {processId, address, voteWeight} of a ballot with the ECDSA scheme
+// of the election's census origin: the election ECDSA key on OFF_CHAIN_CA_V3_ECDSA, and the root
+// key salted with the election id on a legacy question. Like Sign, it consumes the election's
+// signing slot for the token's user.
+func (c *CSP) SignBallot(req *BallotRequest) (*BallotSignature, error) {
+	switch req.Election.CensusOrigin {
+	case "":
+		signature, err := c.Sign(req.Token, req.Address, req.Election.ID, req.Weight, signers.SignerTypeECDSASalted)
+		if err != nil {
+			return nil, err
+		}
+		return &BallotSignature{
+			Signature: signature,
+			ProofType: models.ProofCA_ECDSA_PIDSALTED.String(),
+			Weight:    req.Weight,
+		}, nil
+	case electionkey.OriginECDSAName:
+		signature, err := c.signElectionECDSA(req)
+		if err != nil {
+			return nil, err
+		}
+		return &BallotSignature{Signature: signature, ProofType: models.ProofCA_ECDSA.String(), Weight: req.Weight}, nil
+	default:
+		return nil, errors.Join(ErrInvalidSignerType,
+			fmt.Errorf("census origin %q is not signed with ECDSA", req.Election.CensusOrigin))
+	}
+}
+
+// signElectionECDSA signs a ballot of an OFF_CHAIN_CA_V3_ECDSA election with its election key. It
+// runs the same token, lock and consumption checks as the legacy ECDSA signer; only the key differs.
+func (c *CSP) signElectionECDSA(req *BallotRequest) (internal.HexBytes, error) {
+	key, err := c.electionKeys.ElectionECDSAKey(req.Election.QuestionID)
+	if err != nil {
+		return nil, errors.Join(ErrSign, err)
+	}
+	userID, _, message, err := c.prepareSaltedKeySigner(req.Token, req.Address, req.Election.ID, req.Weight)
+	defer c.unlock(userID, req.Election.ID)
+	if err != nil {
+		return nil, err
+	}
+	signature, err := electionkey.SignEthereum(key, message)
+	if err != nil {
+		return nil, errors.Join(ErrSign, err)
+	}
+	if err := c.finishSaltedKeySigner(req.Token, req.Address, req.Election.ID); err != nil {
+		return nil, err
+	}
+	return signature, nil
+}
+
+// BlindSignBallot performs round 2 of a blind signature with the scheme of the election's census
+// origin. On OFF_CHAIN_CA_V3_BLIND it blind-signs with the weight key of the weight pinned at round
+// 1 and returns that key with its certificate; on a legacy question it signs as BlindSign does.
+func (c *CSP) BlindSignBallot(token internal.HexBytes, election Election, blindedMsg internal.HexBytes) (
+	*BallotSignature, error,
+) {
+	switch election.CensusOrigin {
+	case "":
+		signature, weight, err := c.BlindSign(token, election.ID, blindedMsg)
+		if err != nil {
+			return nil, err
+		}
+		return &BallotSignature{
+			Signature: signature,
+			ProofType: models.ProofCA_ECDSA_BLIND_PIDSALTED.String(),
+			Weight:    weight,
+		}, nil
+	case electionkey.OriginBlindName:
+		return c.blindSignElection(token, election, blindedMsg)
+	default:
+		return nil, errors.Join(ErrInvalidSignerType,
+			fmt.Errorf("census origin %q is not signed with blind signatures", election.CensusOrigin))
+	}
+}
+
+// blindSignElection signs round 2 of an OFF_CHAIN_CA_V3_BLIND election. The election blind key is
+// derived before the nonce is claimed. The weight key depends on the weight pinned with the nonce,
+// so it is derived after the claim; a pinned weight is always a valid one (round 1 pins the
+// member's census weight), so that step does not fail in practice.
+func (c *CSP) blindSignElection(token internal.HexBytes, election Election, blindedMsg internal.HexBytes) (
+	*BallotSignature, error,
+) {
+	electionBlindKey, err := c.electionKeys.ElectionBlindKey(election.QuestionID)
+	if err != nil {
+		return nil, errors.Join(ErrSign, err)
+	}
+	claimed, err := c.claimBlindNonce(token, election.ID, blindedMsg)
+	if err != nil {
+		return nil, err
+	}
+	credential, signature, err := signWithWeightKey(electionBlindKey, election.ID, claimed, blindedMsg)
+	if err != nil {
+		// the nonce is already claimed (spent), so this is a hard failure, not a retry.
+		log.Errorw(errors.Join(ErrSign, err), "blind signature failed after claiming the nonce")
+		return nil, errors.Join(ErrSign, err)
+	}
+	return &BallotSignature{
+		Signature:         signature,
+		ProofType:         models.ProofCA_ECDSA_BLIND.String(),
+		Weight:            claimed.BlindWeight,
+		WeightKey:         credential.WeightKey,
+		WeightCertificate: credential.Certificate,
+	}, nil
+}
+
+// signWithWeightKey issues the weight credential of the weight pinned in claimed (a missing weight
+// is 1) and blind-signs blindedMsg with its key and the claimed nonce.
+func signWithWeightKey(
+	electionBlindKey *ecdsa.PrivateKey, processID internal.HexBytes, claimed *db.CSPProcess, blindedMsg internal.HexBytes,
+) (*electionkey.WeightCredential, internal.HexBytes, error) {
+	weight, err := electionkey.NormalizeWeight(claimed.BlindWeight)
+	if err != nil {
+		return nil, nil, err
+	}
+	credential, err := electionkey.IssueWeightCredential(electionBlindKey, processID, weight)
+	if err != nil {
+		return nil, nil, err
+	}
+	weightKey := blind.PrivateKey(*new(big.Int).SetBytes(ethcrypto.FromECDSA(credential.Key)))
+	signature, err := weightKey.BlindSign(new(big.Int).SetBytes(blindedMsg), new(big.Int).SetBytes(claimed.BlindSecret))
+	if err != nil {
+		return nil, nil, err
+	}
+	return credential, signature.Bytes(), nil
+}
diff --git a/csp/handlers/processes.go b/csp/handlers/processes.go
index 3aed532..d740160 100644
--- a/csp/handlers/processes.go
+++ b/csp/handlers/processes.go
@@ -11,7 +11,6 @@ import (
 	"github.com/go-chi/chi/v5"
 	"github.com/vocdoni/saas-backend/api/apicommon"
 	"github.com/vocdoni/saas-backend/csp"
-	"github.com/vocdoni/saas-backend/csp/signers"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/errors"
 	"github.com/vocdoni/saas-backend/internal"
@@ -262,7 +261,7 @@ func (c *CSPHandlers) ProcessSignHandler(w http.ResponseWriter, r *http.Request)
 		errors.ErrMalformedBody.Withf("process uses a blind (anonymous) census; use the blind-point/blind-sign endpoints").Write(w)
 		return
 	}
-	upstreamID, sErr := c.authorizeQuestion(sc, req.ProcessID)
+	election, sErr := c.authorizeQuestion(sc, req.ProcessID)
 	if sErr != nil {
 		sErr.Write(w)
 		return
@@ -271,7 +270,9 @@ func (c *CSPHandlers) ProcessSignHandler(w http.ResponseWriter, r *http.Request)
 	if !ok {
 		return
 	}
-	c.signAndRespond(w, req.AuthToken, *address, upstreamID, sc.weight)
+	c.signAndRespond(w, &csp.BallotRequest{
+		Token: req.AuthToken, Address: *address, Weight: sc.weight, Election: election,
+	})
 }
 
 // signContext is the part of a ballot signing request that is resolved once per call, from
@@ -353,16 +354,16 @@ func (c *CSPHandlers) resolveSignContext(
 // instead (authorizeLoadedQuestion) to avoid one query per ballot.
 func (c *CSPHandlers) authorizeQuestion(
 	sc *signContext, electionID internal.HexBytes,
-) (internal.HexBytes, *errors.Error) {
+) (csp.Election, *errors.Error) {
 	// an empty id cannot name an election, and db.QuestionByUpstreamID reports it as
 	// ErrInvalidData, which the branch below would surface to the client as a 500. The message is
 	// field-neutral because the single-sign endpoint sends this same id as "electionId".
 	if len(electionID) == 0 {
-		return nil, errors.Ptr(errors.ErrMalformedBody.With("missing election id"))
+		return csp.Election{}, errors.Ptr(errors.ErrMalformedBody.With("missing election id"))
 	}
 	question, err := c.mainDB.QuestionByUpstreamID(electionID)
 	if err != nil && !errors.Is(err, db.ErrNotFound) {
-		return nil, errors.Ptr(errors.ErrGenericInternalServerError.WithErr(err))
+		return csp.Election{}, errors.Ptr(errors.ErrGenericInternalServerError.WithErr(err))
 	}
 	if err != nil {
 		question = nil // not found: authorizeLoadedQuestion folds it into the same 401
@@ -374,19 +375,20 @@ func (c *CSPHandlers) authorizeQuestion(
 // resolved to nothing): it must belong to this process, currently accept a signature, and the
 // member must be in its eligibility subset. Not-found and wrong-process collapse into the same
 // 401 on purpose — a caller learns nothing about elections outside the process it is signing for.
+// It returns the question's election: its on-chain id and the census origin that picks the signer.
 func authorizeLoadedQuestion(
 	sc *signContext, question *db.VotingProcessQuestion,
-) (internal.HexBytes, *errors.Error) {
+) (csp.Election, *errors.Error) {
 	if question == nil || question.ProcessID != sc.process.ID {
-		return nil, errors.Ptr(errors.ErrUnauthorized.Withf("election not found in process"))
+		return csp.Election{}, errors.Ptr(errors.ErrUnauthorized.Withf("election not found in process"))
 	}
 	if sErr := unvotableElection(sc.process, question); sErr != nil {
-		return nil, sErr
+		return csp.Election{}, sErr
 	}
 	if !memberEligibleForQuestion(question, sc.memberID) {
-		return nil, errors.Ptr(errors.ErrUnauthorized.Withf("member not eligible for this question"))
+		return csp.Election{}, errors.Ptr(errors.ErrUnauthorized.Withf("member not eligible for this question"))
 	}
-	return question.UpstreamID, nil
+	return csp.ElectionOf(question), nil
 }
 
 // maxSignBatchBodyBytes bounds a POST /processes/{processId}/sign-batch body. One ballot is an
@@ -397,10 +399,10 @@ func authorizeLoadedQuestion(
 const maxSignBatchBodyBytes = db.MaxQuestionsPerProcess*512 + 4*internal.KiB
 
 // authorizedBallot is one item of a sign batch that survived the authorization pass: the
-// question's on-chain election id, resolved from the request, and the voter address.
+// question's election, resolved from the request, and the voter address.
 type authorizedBallot struct {
-	upstreamID internal.HexBytes
-	address    internal.HexBytes
+	election csp.Election
+	address  internal.HexBytes
 }
 
 // ProcessSignBatchHandler godoc
@@ -507,7 +509,7 @@ func (c *CSPHandlers) ProcessSignBatchHandler(w http.ResponseWriter, r *http.Req
 			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
 			return
 		}
-		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
+		election, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
 		if sErr != nil {
 			sErr.Withf("at index %d", i).Write(w)
 			return
@@ -516,7 +518,7 @@ func (c *CSPHandlers) ProcessSignBatchHandler(w http.ResponseWriter, r *http.Req
 			sErr.Withf("at index %d", i).Write(w)
 			return
 		}
-		ballots[i] = authorizedBallot{upstreamID: upstreamID, address: item.Address}
+		ballots[i] = authorizedBallot{election: election, address: item.Address}
 	}
 
 	// ponytail: signed sequentially. db.ConsumeCSPProcess takes the storage write lock, so a
@@ -525,37 +527,37 @@ func (c *CSPHandlers) ProcessSignBatchHandler(w http.ResponseWriter, r *http.Req
 	resp := &SignBatchResponse{Signatures: make([]SignBatchResult, len(ballots))}
 	for i, b := range ballots {
 		res := &resp.Signatures[i]
-		res.UpstreamID = b.upstreamID
-		signature, err := c.csp.Sign(req.AuthToken, b.address, b.upstreamID, sc.weight,
-			signers.SignerTypeECDSASalted)
+		res.UpstreamID = b.election.ID
+		signed, err := c.csp.SignBallot(&csp.BallotRequest{
+			Token: req.AuthToken, Address: b.address, Weight: sc.weight, Election: b.election,
+		})
 		if err != nil {
 			// usually per-ballot: this election's signing slot is spent, or a concurrent
 			// request holds its lock. Map it to a stable code + sanitized message for the
 			// response; the raw error stays in the log.
 			res.Code, res.Error = signOutcome(err)
-			logSignFailure(oid, sc.memberID, b.upstreamID, err)
+			logSignFailure(oid, sc.memberID, b.election.ID, err)
 			// an invalid token dooms every remaining ballot too (the token was valid when
 			// resolveSignContext ran, so it was deleted or unverified mid-batch): stamp the
 			// rest with the same outcome instead of issuing one doomed Sign each.
 			if res.Code == signCodeAuthInvalid {
 				for j := i + 1; j < len(ballots); j++ {
-					resp.Signatures[j].UpstreamID = ballots[j].upstreamID
+					resp.Signatures[j].UpstreamID = ballots[j].election.ID
 					resp.Signatures[j].Code, resp.Signatures[j].Error = res.Code, res.Error
 				}
 				break
 			}
 			continue
 		}
-		res.Signature = signature
-		res.Weight = sc.weight
+		res.setSignature(signed)
 	}
 	apicommon.HTTPWriteJSON(w, resp)
 }
 
-// blindBallotAuth is one item of a blind batch that survived the authorization pass: the
-// question's on-chain election id, plus (round 2 only) the blinded message to sign for it.
+// blindBallotAuth is one item of a blind round-2 batch that survived the authorization pass: the
+// question's election and the blinded message to sign for it.
 type blindBallotAuth struct {
-	upstreamID     internal.HexBytes
+	election       csp.Election
 	blindedMessage internal.HexBytes
 }
 
@@ -630,12 +632,12 @@ func (c *CSPHandlers) ProcessBlindPointHandler(w http.ResponseWriter, r *http.Re
 			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
 			return
 		}
-		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(electionID)])
+		election, sErr := authorizeLoadedQuestion(sc, byUpstream[string(electionID)])
 		if sErr != nil {
 			sErr.Withf("at index %d", i).Write(w)
 			return
 		}
-		upstreamIDs[i] = upstreamID
+		upstreamIDs[i] = election.ID
 	}
 
 	resp := &BlindPointResponse{Points: make([]BlindPointResult, len(upstreamIDs))}
@@ -732,7 +734,7 @@ func (c *CSPHandlers) ProcessBlindSignHandler(w http.ResponseWriter, r *http.Req
 			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
 			return
 		}
-		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
+		election, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
 		if sErr != nil {
 			sErr.Withf("at index %d", i).Write(w)
 			return
@@ -741,34 +743,42 @@ func (c *CSPHandlers) ProcessBlindSignHandler(w http.ResponseWriter, r *http.Req
 			errors.ErrMalformedBody.With("missing blinded message").Withf("at index %d", i).Write(w)
 			return
 		}
-		ballots[i] = blindBallotAuth{upstreamID: upstreamID, blindedMessage: item.BlindedMessage}
+		ballots[i] = blindBallotAuth{election: election, blindedMessage: item.BlindedMessage}
 	}
 
 	resp := &BlindSignResponse{Signatures: make([]SignBatchResult, len(ballots))}
 	for i, b := range ballots {
 		res := &resp.Signatures[i]
-		res.UpstreamID = b.upstreamID
-		signature, pinnedWeight, err := c.csp.BlindSign(req.AuthToken, b.upstreamID, b.blindedMessage)
+		res.UpstreamID = b.election.ID
+		signed, err := c.csp.BlindSignBallot(req.AuthToken, b.election, b.blindedMessage)
 		if err != nil {
 			res.Code, res.Error = signOutcome(err)
-			logSignFailure(oid, sc.memberID, b.upstreamID, err)
+			logSignFailure(oid, sc.memberID, b.election.ID, err)
 			if res.Code == signCodeAuthInvalid {
 				for j := i + 1; j < len(ballots); j++ {
-					resp.Signatures[j].UpstreamID = ballots[j].upstreamID
+					resp.Signatures[j].UpstreamID = ballots[j].election.ID
 					resp.Signatures[j].Code, resp.Signatures[j].Error = res.Code, res.Error
 				}
 				break
 			}
 			continue
 		}
-		res.Signature = signature
-		// report the weight actually signed (the pinned round-1 weight), matching SignBatchResult's
-		// contract — not the live sc.weight, which may have drifted since round 1.
-		res.Weight = pinnedWeight
+		// the weight reported is the one actually signed (the pinned round-1 weight), matching
+		// SignBatchResult's contract — not the live sc.weight, which may have drifted since round 1.
+		res.setSignature(signed)
 	}
 	apicommon.HTTPWriteJSON(w, resp)
 }
 
+// setSignature fills the success fields of r from a CSP ballot signature.
+func (r *SignBatchResult) setSignature(s *csp.BallotSignature) {
+	r.Signature = s.Signature
+	r.Weight = s.Weight
+	r.ProofType = s.ProofType
+	r.WeightKey = s.WeightKey
+	r.WeightCertificate = s.WeightCertificate
+}
+
 // questionsByUpstream loads a process's questions indexed by their on-chain election id, writing
 // the proper error and returning false on failure. Shared by the blind round-1 and round-2 handlers.
 func (c *CSPHandlers) questionsByUpstream(
diff --git a/csp/handlers/sign.go b/csp/handlers/sign.go
index 25d1655..f40509a 100644
--- a/csp/handlers/sign.go
+++ b/csp/handlers/sign.go
@@ -7,7 +7,7 @@ import (
 
 	"github.com/ethereum/go-ethereum/common"
 	"github.com/vocdoni/saas-backend/api/apicommon"
-	"github.com/vocdoni/saas-backend/csp/signers"
+	"github.com/vocdoni/saas-backend/csp"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/errors"
 	"github.com/vocdoni/saas-backend/internal"
@@ -78,23 +78,23 @@ func parseAddress(w http.ResponseWriter, payload string) (*internal.HexBytes, bo
 
 // signAndRespond signs the request and sends the response. A failure is mapped through
 // signOutcome so the public body carries the sanitized message, not the raw signer error
-// (csp.Sign joins internal detail into it), which stays in the log: authorization outcomes
+// (csp.SignBallot joins internal detail into it), which stays in the log: authorization outcomes
 // are 401, internal failures 500.
-func (c *CSPHandlers) signAndRespond(w http.ResponseWriter, authToken, address, processID, weight internal.HexBytes) {
-	log.Debugw("new CSP sign request", "address", address, "procId", processID, "weight", weight)
+func (c *CSPHandlers) signAndRespond(w http.ResponseWriter, req *csp.BallotRequest) {
+	log.Debugw("new CSP sign request", "address", req.Address, "procId", req.Election.ID, "weight", req.Weight)
 
-	signature, err := c.csp.Sign(authToken, address, processID, weight, signers.SignerTypeECDSASalted)
+	signed, err := c.csp.SignBallot(req)
 	if err != nil {
 		code, message := signOutcome(err)
 		if code == signCodeFailed {
-			log.Warnw("could not sign", "procId", processID, "error", err)
+			log.Warnw("could not sign", "procId", req.Election.ID, "error", err)
 			errors.ErrGenericInternalServerError.With(message).Write(w)
 			return
 		}
-		log.Debugw("sign rejected", "procId", processID, "reason", err)
+		log.Debugw("sign rejected", "procId", req.Election.ID, "reason", err)
 		errors.ErrUnauthorized.With(message).Write(w)
 		return
 	}
 
-	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signature, Weight: weight})
+	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signed.Signature, Weight: signed.Weight, ProofType: signed.ProofType})
 }
diff --git a/csp/handlers/types.go b/csp/handlers/types.go
index 7e38330..37879b1 100644
--- a/csp/handlers/types.go
+++ b/csp/handlers/types.go
@@ -37,6 +37,9 @@ type AuthResponse struct {
 	AuthToken internal.HexBytes `json:"authToken,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
 	Signature internal.HexBytes `json:"signature,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
 	Weight    internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
+	// ProofType is the ProofCA.Type name of the proof to build with Signature (ECDSA or
+	// ECDSA_PIDSALTED). Set with Signature.
+	ProofType string `json:"proofType,omitempty" example:"ECDSA"`
 }
 
 // AuthRequest defines the payload for finishing the authentication process.
@@ -86,8 +89,16 @@ type SignBatchResult struct {
 	UpstreamID internal.HexBytes `json:"upstreamId" swaggertype:"string" format:"hex" example:"deadbeef"`
 	Signature  internal.HexBytes `json:"signature,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
 	Weight     internal.HexBytes `json:"weight,omitempty" swaggertype:"string" format:"hex" example:"2a"`
-	Code       string            `json:"code,omitempty"`
-	Error      string            `json:"error,omitempty"`
+	// ProofType is the ProofCA.Type name of the proof to build with Signature: ECDSA,
+	// ECDSA_PIDSALTED, ECDSA_BLIND or ECDSA_BLIND_PIDSALTED. Set with Signature.
+	ProofType string `json:"proofType,omitempty" example:"ECDSA_BLIND"`
+	// WeightKey and WeightCertificate are set on a blind signature of an OFF_CHAIN_CA_V3_BLIND
+	// election: the compressed public key that blind-signed the ballot, and the 65-byte certificate
+	// of that key for this election and weight. The client puts both in ProofCA.weightCertificate.
+	WeightKey         internal.HexBytes `json:"weightKey,omitempty" swaggertype:"string" format:"hex" example:"02a1b2"`
+	WeightCertificate internal.HexBytes `json:"weightCertificate,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
+	Code              string            `json:"code,omitempty"`
+	Error             string            `json:"error,omitempty"`
 }
 
 // BlindPointRequest is round 1 of the blind (anonymous) signing flow of a voting process: for each
diff --git a/csp/sign.go b/csp/sign.go
index 4f54ef0..0ff1829 100644
--- a/csp/sign.go
+++ b/csp/sign.go
@@ -111,48 +111,59 @@ func (c *CSP) NewBlindRequest(token, processID, weight internal.HexBytes) (r, pi
 // together with the pinned weight it salted with, so the handler can report the weight actually
 // signed (its documented contract) rather than the live member weight.
 func (c *CSP) BlindSign(token, processID, blindedMsg internal.HexBytes) (signature, pinnedWeight internal.HexBytes, err error) {
+	claimed, err := c.claimBlindNonce(token, processID, blindedMsg)
+	if err != nil {
+		return nil, nil, err
+	}
+	salt, err := saltedkey.V2Salt(processID, claimed.BlindWeight)
+	if err != nil {
+		return nil, nil, errors.Join(ErrSign, err)
+	}
+	secretK := new(big.Int).SetBytes(claimed.BlindSecret)
+	sig, err := c.Signer.SignBlind(salt, blindedMsg, secretK)
+	if err != nil {
+		// unreachable given the pre-validation in claimBlindNonce; the nonce is already claimed
+		// (spent) so this is a hard failure, not a retry — log loudly rather than silently
+		// strand the slot.
+		log.Errorw(errors.Join(ErrSign, err), "blind signature failed after claiming the nonce")
+		return nil, nil, errors.Join(ErrSign, err)
+	}
+	return sig, claimed.BlindWeight, nil
+}
+
+// claimBlindNonce runs the part of round 2 shared by every blind scheme. It checks the auth token,
+// validates the blinded message against BlindSign's preconditions — 0 < m' < N and exactly 32
+// bytes — before touching any state, so a malformed message never consumes the one-time nonce,
+// and then atomically claims the nonce armed in round 1. The returned document carries the nonce
+// k and the pinned weight to sign with.
+func (c *CSP) claimBlindNonce(token, processID, blindedMsg internal.HexBytes) (*db.CSPProcess, error) {
 	authTokenData, err := c.Storage.CSPAuth(token)
 	if err != nil {
 		if errors.Is(err, db.ErrTokenNotFound) {
-			return nil, nil, ErrInvalidAuthToken
+			return nil, ErrInvalidAuthToken
 		}
-		return nil, nil, errors.Join(ErrSign, err)
+		return nil, errors.Join(ErrSign, err)
 	}
 	if !authTokenData.Verified {
-		return nil, nil, ErrAuthTokenNotVerified
+		return nil, ErrAuthTokenNotVerified
 	}
-	// validate the blinded message against BlindSign's preconditions before claiming the nonce, so
-	// an invalid input never consumes the one-time nonce (which would strand the voter or, worse,
-	// force a same-nonce retry). N is the secp256k1 group order shared by go-blindsecp256k1.
+	// N is the secp256k1 group order shared by go-blindsecp256k1.
 	m := new(big.Int).SetBytes(blindedMsg)
 	if m.Sign() == 0 || m.Cmp(ethcrypto.S256().Params().N) >= 0 || len(m.Bytes()) != blindScalarBytes {
-		return nil, nil, ErrInvalidBlindedMessage
+		return nil, ErrInvalidBlindedMessage
 	}
-	// atomically claim the nonce: the returned document carries the k, R and weight to sign with.
 	claimed, err := c.Storage.ClaimCSPProcessBlind(authTokenData.UserID, processID, token)
 	if err != nil {
 		switch {
 		case errors.Is(err, db.ErrBlindNonceNotFound):
-			return nil, nil, ErrBlindRequestNotFound
+			return nil, ErrBlindRequestNotFound
 		case errors.Is(err, db.ErrProcessAlreadyConsumed):
-			return nil, nil, ErrProcessAlreadyConsumed
+			return nil, ErrProcessAlreadyConsumed
 		default:
-			return nil, nil, errors.Join(ErrSign, err)
+			return nil, errors.Join(ErrSign, err)
 		}
 	}
-	salt, err := saltedkey.V2Salt(processID, claimed.BlindWeight)
-	if err != nil {
-		return nil, nil, errors.Join(ErrSign, err)
-	}
-	secretK := new(big.Int).SetBytes(claimed.BlindSecret)
-	sig, err := c.Signer.SignBlind(salt, blindedMsg, secretK)
-	if err != nil {
-		// unreachable given the pre-validation above; the nonce is already claimed (spent) so this
-		// is a hard failure, not a retry — log loudly rather than silently strand the slot.
-		log.Errorw(errors.Join(ErrSign, err), "blind signature failed after claiming the nonce")
-		return nil, nil, errors.Join(ErrSign, err)
-	}
-	return sig, claimed.BlindWeight, nil
+	return claimed, nil
 }
 
 // prepareSaltedKeySigner method prepares the data for the Ethereum signer.
diff --git a/db/types.go b/db/types.go
index 5c5ae50..05845e6 100644
--- a/db/types.go
+++ b/db/types.go
@@ -854,7 +854,16 @@ type VotingProcessQuestion struct {
 	UpstreamID        internal.HexBytes `json:"upstreamId,omitempty" bson:"upstreamId,omitempty" swaggertype:"string" format:"hex" example:"deadbeef"`
 	MetadataURL       string            `json:"-" bson:"metadataURL,omitempty"`
 	Status            string            `json:"status,omitempty" bson:"status,omitempty"`
-	SyncedAt          time.Time         `json:"-" bson:"syncedAt,omitempty"`
+	// CensusOrigin is the census origin name of this question's election when it uses a
+	// per-election CSP key (OFF_CHAIN_CA_V3_ECDSA or OFF_CHAIN_CA_V3_BLIND); the CSP picks the
+	// signing scheme from it. Empty for a question published on a legacy CSP origin, which is
+	// signed with the salted root key.
+	CensusOrigin string `json:"-" bson:"censusOrigin,omitempty"`
+	// CensusRoot is the census root of this question's election when CensusOrigin is set: the
+	// compressed public key of its per-election CSP key. Empty for a legacy question, whose root
+	// is the census's Published.Root.
+	CensusRoot internal.HexBytes `json:"-" bson:"censusRoot,omitempty"`
+	SyncedAt   time.Time         `json:"-" bson:"syncedAt,omitempty"`
 	// EndedAt is the actual on-chain moment this question's election stopped accepting votes,
 	// set only when the vote was ended early (before its scheduled end, i.e. ManuallyEnded on
 	// the chain). Absent while the vote is open or when it ran to its scheduled end. Recorded by
```

```bash
make swagger   # regenerates docs/swagger.yaml: +24 lines (proofType, weightKey, weightCertificate)
go test ./csp/... -count=1
go test ./api/ -run 'CSP|Blind|Sign' -count=1      # legacy flows unchanged (dvote Vocone still)
~/go/bin/golangci-lint-v2 run ./... && scripts/check-qt-patterns.sh
```
Expected pass: the 4 new tests (ElectionCensusIsPerQuestion, SignBallotElectionECDSA,
BlindSignBallotElectionBlind, SignBallotRejectsMismatchedScheme) and all existing csp tests pass;
the api CSP tests pass unchanged; lint shows only the 5 base findings. `TestSignBallot*` use a
salted-signer stub that fails the test if invoked (AC6).

Expected swagger diff:

```diff
diff --git a/docs/swagger.yaml b/docs/swagger.yaml
index de364da..a1c546f 100644
--- a/docs/swagger.yaml
+++ b/docs/swagger.yaml
@@ -2290,6 +2290,12 @@ definitions:
         example: deadbeef
         format: hex
         type: string
+      proofType:
+        description: |-
+          ProofType is the ProofCA.Type name of the proof to build with Signature (ECDSA or
+          ECDSA_PIDSALTED). Set with Signature.
+        example: ECDSA
+        type: string
       signature:
         example: deadbeef
         format: hex
@@ -2476,6 +2482,12 @@ definitions:
         type: string
       error:
         type: string
+      proofType:
+        description: |-
+          ProofType is the ProofCA.Type name of the proof to build with Signature: ECDSA,
+          ECDSA_PIDSALTED, ECDSA_BLIND or ECDSA_BLIND_PIDSALTED. Set with Signature.
+        example: ECDSA_BLIND
+        type: string
       signature:
         example: deadbeef
         format: hex
@@ -2488,6 +2500,18 @@ definitions:
         example: 2a
         format: hex
         type: string
+      weightCertificate:
+        example: deadbeef
+        format: hex
+        type: string
+      weightKey:
+        description: |-
+          WeightKey and WeightCertificate are set on a blind signature of an OFF_CHAIN_CA_V3_BLIND
+          election: the compressed public key that blind-signed the ballot, and the 65-byte certificate
+          of that key for this election and weight. The client puts both in ProofCA.weightCertificate.
+        example: 02a1b2
+        format: hex
+        type: string
     type: object
   handlers.SignRequest:
     properties:
```

Step 3: `git commit -m "feat(csp): sign per-election csp ballots by question census origin" -- csp/csp.go csp/sign.go csp/election.go csp/election_test.go csp/handlers/types.go csp/handlers/processes.go csp/handlers/sign.go db/types.go docs/swagger.yaml`

Interfaces this task provides: `csp.ElectionOf(*db.VotingProcessQuestion) Election`,
`(*CSP).ElectionCensus(questionID bson.ObjectID, anonymous bool) (models.CensusOrigin, internal.HexBytes, error)`,
`(*CSP).SignBallot(*BallotRequest) (*BallotSignature, error)`,
`(*CSP).BlindSignBallot(token, election, blindedMsg)`; `handlers.SignBatchResult{ProofType, WeightKey, WeightCertificate}`,
`handlers.AuthResponse.ProofType`; `db.VotingProcessQuestion{CensusOrigin string, CensusRoot internal.HexBytes}`.

### Task 7: saas-backend V3 publication, orphan adoption and the sequencer test harness

Model: opus   Repo: SAAS   Files: db/processes_questions.go, db/voting_processes_test.go,
db/census_revocation_test.go, account/process.go, account/process_test.go, account/elections.go,
api/processes_publish.go, api/processes_publish_orphans.go, api/processes_publish_orphans_test.go,
api/processes_admin.go, api/processes_publish_state_test.go, api/api_test.go, api/process_vote_test.go,
api/processes_blind_csp_test.go, api/statussync_test.go, api/processes_encryption_keys_test.go,
api/csp_key_per_election_test.go, test/voconed.go, test/sequencer/go.mod, test/sequencer/go.sum, go.mod, go.sum

Goal: every CSP question publishes as its own V3 election (V3_ECDSA, or V3_BLIND for an anonymous
census) with census root = the per-question key, stored on the question (origin + root).
`census.Published.Root` stays empty for V3; readers use `questionCensusRoot`. Publication is
idempotent per question: an election mined after a lost ack is adopted, never duplicated. The
integration tests run sequencer `voconed` as a subprocess built from the nested module
`test/sequencer` (production `go.mod` untouched apart from `go mod tidy`): `TestMain` starts chain
`test-csp-key` with `--cspKeyForkHeight 0`; `TestCSPLegacyQuestionsAfterFork` starts its own chain
`test-csp-legacy` with fork 25 (see deviation 1).

Step 1 (failing test): add the nested module, the harness, and the tests.

```diff
diff --git a/account/process_test.go b/account/process_test.go
index 35fd246..4ec69c4 100644
--- a/account/process_test.go
+++ b/account/process_test.go
@@ -5,6 +5,7 @@ import (
 	"time"
 
 	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/db"
 	"go.vocdoni.io/proto/build/go/models"
 )
@@ -76,6 +77,7 @@ func TestBuildNewProcessTxInitialStatus(t *testing.T) {
 			Params:        ep,
 			CensusRoot:    []byte{0x01},
 			CensusURI:     "https://example.invalid",
+			CensusOrigin:  electionkey.OriginECDSA,
 			MetadataURL:   "https://example.invalid/meta.json",
 			Nonce:         &nonce,
 			InitialStatus: status,
@@ -136,6 +138,38 @@ func TestBuildNewProcessTxInitialStatus(t *testing.T) {
 	})
 }
 
+// TestBuildNewProcessTxCensusOrigin checks that the census origin is published as given for every
+// CSP origin, and that any other origin, including the unset zero value, is refused.
+func TestBuildNewProcessTxCensusOrigin(t *testing.T) {
+	c := qt.New(t)
+	a := &Account{} // client is not touched when Nonce is set
+	params := func(origin models.CensusOrigin) *NewProcessParams {
+		nonce := uint32(0)
+		return &NewProcessParams{
+			Params:       &db.ElectionParams{EndDate: time.Now().Add(time.Hour), MaxCensusSize: 10},
+			CensusRoot:   []byte{0x02},
+			CensusURI:    "https://example.invalid",
+			CensusOrigin: origin,
+			Nonce:        &nonce,
+		}
+	}
+	for _, origin := range []models.CensusOrigin{
+		electionkey.OriginECDSA,
+		electionkey.OriginBlind,
+		models.CensusOrigin_OFF_CHAIN_CA,
+		models.CensusOrigin_OFF_CHAIN_CA_V2,
+	} {
+		tx, err := a.BuildNewProcessTx(params(origin))
+		c.Assert(err, qt.IsNil, qt.Commentf("origin=%d", origin))
+		c.Assert(tx.GetNewProcess().GetProcess().GetCensusOrigin(), qt.Equals, origin)
+		c.Assert(tx.GetNewProcess().GetProcess().GetCensusRoot(), qt.DeepEquals, []byte{0x02})
+	}
+	for _, origin := range []models.CensusOrigin{0, models.CensusOrigin_OFF_CHAIN_TREE} {
+		_, err := a.BuildNewProcessTx(params(origin))
+		c.Assert(err, qt.ErrorMatches, `census origin \d+ is not a CSP census origin`)
+	}
+}
+
 // TestParseInitialStatus covers the wire-string → on-chain enum parser used at the HTTP
 // boundary: empty and "READY" (case-insensitive) resolve to READY, "PAUSED" resolves to
 // PAUSED, anything else is rejected.
diff --git a/api/api_test.go b/api/api_test.go
index 167be76..58707ac 100644
--- a/api/api_test.go
+++ b/api/api_test.go
@@ -209,6 +209,9 @@ var testSMSService = new(testutil.MockSMS)
 // testAPIEndpoint is the Voconed API endpoint for the tests. Make it global so it can be accessed by the tests directly.
 var testAPIEndpoint string
 
+// testVoconedBin is the voconed binary TestMain builds, for tests that start a chain of their own.
+var testVoconedBin string
+
 // testCSP is the CSP service for the tests. Make it global so it can be accessed by the tests directly.
 var (
 	testCSP *csp.CSP
@@ -313,14 +316,28 @@ func TestMain(m *testing.M) {
 		panic(err)
 	}
 	defer testDB.Close()
-	// start an in-process voconed chain (built from the pinned dvote dep, so it always
-	// includes the batch-transaction endpoint the /processes publish path relies on)
-	voconed, err := test.SharedVoconed()
+	// build the vochain-sequencer voconed pinned by test/sequencer and start the chain the API
+	// publishes on, whose CSP key fork is active from genesis. The legacy CSP test starts a second
+	// chain of its own (see TestCSPLegacyQuestionsAfterFork).
+	binDir, err := os.MkdirTemp("", "voconed-bin-*")
+	if err != nil {
+		panic(err)
+	}
+	defer func() { _ = os.RemoveAll(binDir) }()
+	voconedBin, err := test.BuildVoconed(ctx, binDir)
+	if err != nil {
+		panic(err)
+	}
+	voconed, err := test.StartVoconed(test.VoconedConfig{
+		Binary: voconedBin, ChainID: "test-csp-key", CSPKeyForkHeight: 0,
+	})
 	if err != nil {
 		panic(err)
 	}
-	// set the test API endpoint global variable
+	defer voconed.Close()
+	// set the test API endpoint global variables
 	testAPIEndpoint = voconed.Endpoint
+	testVoconedBin = voconedBin
 	// start test mail server
 	testMailServer, err := test.StartMailService(ctx)
 	if err != nil {
@@ -723,21 +740,24 @@ func fetchCSPUserWeight(t *testing.T, pid string, authToken internal.HexBytes) i
 	return weightResp.Weight
 }
 
-// testCSPSign signs a payload with the CSP for the question election processID of the voting
-// process pid, using the given auth token. It returns the signature.
+// testCSPSign signs a payload with the CSP for the OFF_CHAIN_CA_V3_ECDSA question election
+// processID of the voting process pid, using the given auth token. It returns the signature.
 func testCSPSign(t *testing.T, pid string, authToken, processID, payload internal.HexBytes) internal.HexBytes {
 	t.Helper()
-	c := qt.New(t)
+	return testCSPSignProof(t, pid, &handlers.SignRequest{
+		AuthToken: authToken, ProcessID: processID, Payload: hex.EncodeToString(payload),
+	}, models.ProofCA_ECDSA)
+}
 
-	// Sign with the verified token
-	signReq := &handlers.SignRequest{
-		AuthToken: authToken,
-		ProcessID: processID,
-		Payload:   hex.EncodeToString(payload),
-	}
+// testCSPSignProof sends signReq to the sign endpoint of the voting process pid, asserts the
+// response names wantType as the proof type, and returns the signature.
+func testCSPSignProof(t *testing.T, pid string, signReq *handlers.SignRequest, wantType models.ProofCA_Type,
+) internal.HexBytes {
+	t.Helper()
+	c := qt.New(t)
 	signResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", signReq, "processes", pid, "sign")
 	c.Assert(signResp.Signature, qt.Not(qt.Equals), "", qt.Commentf("signature is empty"))
-
+	c.Assert(signResp.ProofType, qt.Equals, wantType.String())
 	t.Logf("Received signature: %s", signResp.Signature.String())
 	return signResp.Signature
 }
@@ -748,7 +768,7 @@ func testAuthenthicateAndVote(t *testing.T, vocdoniClient *apiclient.HTTPclient,
 	t.Helper()
 	c := qt.New(t)
 
-	votesBefore, err := vocdoniClient.ElectionVoteCount(processID.Bytes())
+	votesBefore, err := electionVoteCount(vocdoniClient, processID)
 	c.Assert(err, qt.IsNil)
 
 	// Create the voting address for the first user
@@ -785,7 +805,7 @@ func testAuthenthicateAndVote(t *testing.T, vocdoniClient *apiclient.HTTPclient,
 	t.Logf("Vote cast successfully with nullifier: %x", nullifier)
 
 	// Verify the vote was counted
-	votesAfter, err := vocdoniClient.ElectionVoteCount(processID.Bytes())
+	votesAfter, err := electionVoteCount(vocdoniClient, processID)
 	c.Assert(err, qt.IsNil)
 	c.Assert(votesAfter, qt.Equals, votesBefore+1, qt.Commentf("expected 1 more vote, got %d", votesAfter))
 }
@@ -824,13 +844,23 @@ func postProcessAuth1(t *testing.T, pid string, request *handlers.AuthChallengeR
 	return resp.AuthToken
 }
 
-// testGenerateVoteProof generates a vote proof with the given signature, process ID, and voter address.
-// It returns the proof.
+// electionVoteCount returns the number of votes the chain has indexed for an election, as reported
+// by GET /elections/{electionId}.
+func electionVoteCount(client *apiclient.HTTPclient, electionID internal.HexBytes) (uint64, error) {
+	election, err := client.Election(electionID.Bytes())
+	if err != nil {
+		return 0, err
+	}
+	return election.VoteCount, nil
+}
+
+// testGenerateVoteProof generates the OFF_CHAIN_CA_V3_ECDSA vote proof of a CSP signature for the
+// given election and voter address. It returns the proof.
 func testGenerateVoteProof(processID, voterAddr, signature internal.HexBytes, weight uint64) *models.Proof {
 	return &models.Proof{
 		Payload: &models.Proof_Ca{
 			Ca: &models.ProofCA{
-				Type:      models.ProofCA_ECDSA_PIDSALTED,
+				Type:      models.ProofCA_ECDSA,
 				Signature: signature,
 				Bundle: &models.CAbundle{
 					ProcessId:  processID,
diff --git a/api/csp_key_per_election_test.go b/api/csp_key_per_election_test.go
new file mode 100644
index 0000000..f110171
--- /dev/null
+++ b/api/csp_key_per_election_test.go
@@ -0,0 +1,303 @@
+package api
+
+import (
+	"encoding/hex"
+	"net/http"
+	"testing"
+	"time"
+
+	"github.com/ethereum/go-ethereum/common"
+	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/saas-backend/account"
+	"github.com/vocdoni/saas-backend/api/apicommon"
+	"github.com/vocdoni/saas-backend/csp/handlers"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/internal"
+	"github.com/vocdoni/saas-backend/test"
+	"go.mongodb.org/mongo-driver/v2/bson"
+	"go.vocdoni.io/dvote/apiclient"
+	"go.vocdoni.io/dvote/crypto/ethereum"
+	"go.vocdoni.io/dvote/types"
+	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/proto"
+)
+
+// legacyForkHeight is the CSP key fork height of the chain TestCSPLegacyQuestionsAfterFork starts:
+// high enough that its legacy elections are created before it, low enough to wait for.
+const legacyForkHeight = 25
+
+// twoQuestionCSPRequest returns a draft request over every member of the organization, with an
+// email-OTP census authenticated by name and surname and two yes/no questions open to everyone.
+func twoQuestionCSPRequest(orgAddress common.Address, members []apicommon.OrgMember,
+) *apicommon.CreateVotingProcessRequest {
+	req := newVotingProcessRequest(orgAddress, memberIDs(members))
+	req.StartDate = ""
+	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
+	second := req.Questions[0]
+	second.Title = db.MultiLangString{"default": "Q2"}
+	req.Questions = []apicommon.VotingProcessQuestionRequest{req.Questions[0], second}
+	return req
+}
+
+// loadProcess returns a voting process, its questions and its census as stored.
+func loadProcess(c *qt.C, pid string) (*db.VotingProcess, []db.VotingProcessQuestion, *db.Census) {
+	oid, err := bson.ObjectIDFromHex(pid)
+	c.Assert(err, qt.IsNil)
+	vp, questions, err := testDB.ProcessWithQuestions(oid)
+	c.Assert(err, qt.IsNil)
+	census, err := testDB.Census(vp.CensusID.Hex())
+	c.Assert(err, qt.IsNil)
+	return vp, questions, census
+}
+
+// TestCSPKeyPerElectionPublish publishes a two-question ECDSA CSP process and checks that each
+// question's election uses OFF_CHAIN_CA_V3_ECDSA with its own census root, that the question
+// records both, that the election ids are the ones the organization's election index predicts,
+// and that ballots signed in one batch carry the ECDSA proof type and vote.
+func TestCSPKeyPerElectionPublish(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "adminpassword123")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
+	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
+
+	pid, elections := publishProcessRequest(t, token, twoQuestionCSPRequest(orgAddress, members))
+	vp, questions, census := loadProcess(c, pid)
+	c.Assert(questions, qt.HasLen, 2)
+	// the census is published without a root: each question's election has its own
+	c.Assert(census.Published.CreatedAt.IsZero(), qt.IsFalse)
+	c.Assert(census.Published.Root, qt.HasLen, 0)
+
+	index, err := testAPI.account.AccountElectionIndex(orgAddress)
+	c.Assert(err, qt.IsNil)
+	c.Assert(index, qt.Equals, uint32(2))
+
+	vc := testNewVocdoniClient(t)
+	for i := range questions {
+		q := &questions[i]
+		_, wantRoot, err := testCSP.ElectionCensus(q.ID, false)
+		c.Assert(err, qt.IsNil)
+		c.Assert(q.UpstreamID, qt.DeepEquals, elections[i])
+		c.Assert(q.CensusOrigin, qt.Equals, electionkey.OriginECDSAName)
+		c.Assert(q.CensusRoot, qt.DeepEquals, wantRoot)
+
+		onchain, err := vc.Election(types.HexBytes(q.UpstreamID))
+		c.Assert(err, qt.IsNil)
+		c.Assert(onchain.Census, qt.Not(qt.IsNil))
+		c.Assert(onchain.Census.CensusOrigin, qt.Equals, electionkey.OriginECDSAName)
+		c.Assert([]byte(onchain.Census.CensusRoot), qt.DeepEquals, []byte(wantRoot))
+
+		ep, err := electionParamsForQuestion(vp, q, census)
+		c.Assert(err, qt.IsNil)
+		id, err := testAPI.account.ElectionID(&account.ElectionIDParams{
+			OrgAddress: orgAddress, CensusOrigin: electionkey.OriginECDSA, Params: ep, Index: uint32(i),
+		})
+		c.Assert(err, qt.IsNil)
+		c.Assert(id, qt.DeepEquals, q.UpstreamID)
+	}
+	c.Assert(questions[0].CensusRoot, qt.Not(qt.DeepEquals), questions[1].CensusRoot)
+
+	// one batch signs both ballots with each election's key, and both votes are accepted
+	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
+		Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email,
+	})
+	voter := ethereum.SignKeys{}
+	c.Assert(voter.Generate(), qt.IsNil)
+	address := internal.HexBytes(voter.Address().Bytes())
+	batch := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", &handlers.SignBatchRequest{
+		AuthToken: tok, Ballots: []handlers.SignBatchBallot{
+			{UpstreamID: elections[0], Address: address},
+			{UpstreamID: elections[1], Address: address},
+		},
+	}, "processes", pid, "sign-batch")
+	c.Assert(batch.Signatures, qt.HasLen, 2)
+	for i, res := range batch.Signatures {
+		c.Assert(res.Code, qt.Equals, "", qt.Commentf("sign error: %s", res.Error))
+		c.Assert(res.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
+		c.Assert(res.WeightCertificate, qt.HasLen, 0)
+		proof := testGenerateVoteProof(elections[i], address, res.Signature, 1)
+		testCastVote(t, vc, &voter, elections[i], proof, []byte(`{"votes":[0]}`))
+		count, err := electionVoteCount(vc, elections[i])
+		c.Assert(err, qt.IsNil)
+		c.Assert(count, qt.Equals, uint64(1))
+	}
+}
+
+// TestPublishAdoptsOrphanElection creates the election of one question of a draft process the way a
+// publish that lost its batch response would have left it, then publishes the process: the publish
+// records that election for the question instead of creating a second one.
+func TestPublishAdoptsOrphanElection(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "adminpassword123")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
+	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
+	created := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, token,
+		twoQuestionCSPRequest(orgAddress, members), processesCreateEndpoint)
+	pid := created.ProcessID
+
+	// the election of question 1 is on chain, but no publish recorded it
+	vp, questions, census := loadProcess(c, pid)
+	orphan := &questions[1]
+	ep, err := electionParamsForQuestion(vp, orphan, census)
+	c.Assert(err, qt.IsNil)
+	origin, root, err := testCSP.ElectionCensus(orphan.ID, false)
+	c.Assert(err, qt.IsNil)
+	tx, err := testAPI.account.BuildNewProcessTx(&account.NewProcessParams{
+		OrgAddress: orgAddress, Params: ep, CensusRoot: root, CensusURI: testAPI.serverURL, CensusOrigin: origin,
+	})
+	c.Assert(err, qt.IsNil)
+	vc := testNewVocdoniClient(t)
+	orphanElection := internal.HexBytes(signAsOrgAndSendVocdoniTx(t, tx, orgAddress, vc))
+	c.Assert(orphanElection, qt.HasLen, types.ProcessIDsize)
+
+	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
+	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
+
+	_, questions, _ = loadProcess(c, pid)
+	c.Assert(questions[1].UpstreamID, qt.DeepEquals, orphanElection)
+	c.Assert(questions[0].UpstreamID, qt.HasLen, types.ProcessIDsize)
+	c.Assert(questions[0].UpstreamID, qt.Not(qt.DeepEquals), orphanElection)
+	index, err := testAPI.account.AccountElectionIndex(orgAddress)
+	c.Assert(err, qt.IsNil)
+	c.Assert(index, qt.Equals, uint32(2), qt.Commentf("the publish created a duplicate election"))
+}
+
+// createLegacyElection creates a READY election on a legacy CSP census origin, owned by the funded
+// account of the chain, and returns its id.
+func createLegacyElection(t *testing.T, client *apiclient.HTTPclient, signer *ethereum.SignKeys,
+	origin models.CensusOrigin, root []byte,
+) internal.HexBytes {
+	t.Helper()
+	tx := legacyElectionTx(t, client, signer, origin, root)
+	return internal.HexBytes(signAndSendVocdoniTx(t, tx, signer, client))
+}
+
+// legacyElectionTx builds a NEW_PROCESS tx of an election on a legacy CSP census origin, owned by
+// signer.
+func legacyElectionTx(t *testing.T, client *apiclient.HTTPclient, signer *ethereum.SignKeys,
+	origin models.CensusOrigin, root []byte,
+) *models.Tx {
+	t.Helper()
+	return &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
+		Txtype: models.TxType_NEW_PROCESS,
+		Nonce:  fetchVocdoniAccountNonce(t, client, signer.Address()),
+		Process: &models.Process{
+			EntityId:      signer.Address().Bytes(),
+			Duration:      3600,
+			Status:        models.ProcessStatus_READY,
+			CensusOrigin:  origin,
+			CensusRoot:    root,
+			MaxCensusSize: 10,
+			EnvelopeType:  &models.EnvelopeType{},
+			VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 1},
+			Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
+		},
+	}}}
+}
+
+// markLegacyPublished records a single-question draft process as published on election, with the
+// empty census origin of a question published on a legacy CSP origin.
+func markLegacyPublished(c *qt.C, pid string, election internal.HexBytes) {
+	vp, questions, _ := loadProcess(c, pid)
+	c.Assert(questions, qt.HasLen, 1)
+	c.Assert(testDB.MarkVotingProcessMined(vp.ID), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(db.QuestionPublishedParams{
+		ID: questions[0].ID, UpstreamID: election, Status: db.QuestionStatusReady,
+	}), qt.IsNil)
+	c.Assert(testDB.SetVotingProcessPublished(vp.ID, "", time.Now()), qt.IsNil)
+}
+
+// waitForHeight blocks until the chain is past height.
+func waitForHeight(c *qt.C, client *apiclient.HTTPclient, height uint32) {
+	deadline := time.Now().Add(2 * time.Minute)
+	for {
+		info, err := client.ChainInfo()
+		c.Assert(err, qt.IsNil)
+		if info.Height > height {
+			return
+		}
+		c.Assert(time.Now().Before(deadline), qt.IsTrue, qt.Commentf("chain stuck at height %d", info.Height))
+		time.Sleep(time.Second)
+	}
+}
+
+// TestCSPLegacyQuestionsAfterFork covers the questions published before the CSP key fork: an
+// OFF_CHAIN_CA election (ECDSA) and an OFF_CHAIN_CA_V2 election (blind) are created on a chain
+// whose fork height is above genesis, and once the chain is past the fork their questions, stored
+// with an empty census origin, still sign with the legacy salted keys and their votes are accepted.
+func TestCSPLegacyQuestionsAfterFork(t *testing.T) {
+	c := qt.New(t)
+	chain, err := test.StartVoconed(test.VoconedConfig{
+		Binary: testVoconedBin, ChainID: "test-csp-legacy", CSPKeyForkHeight: legacyForkHeight,
+	})
+	c.Assert(err, qt.IsNil)
+	defer chain.Close()
+	client, err := apiclient.New(chain.Endpoint)
+	c.Assert(err, qt.IsNil)
+	funded := &ethereum.SignKeys{}
+	c.Assert(funded.AddHexKey(test.VoconedFoundedPrivKey), qt.IsNil)
+
+	token := testCreateUser(t, "adminpassword123")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
+	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
+	draft := func(anonymous bool) string {
+		req := twoQuestionCSPRequest(orgAddress, members)
+		req.Questions = req.Questions[:1]
+		req.Census.Anonymous = anonymous
+		return requestAndParse[apicommon.CreateVotingProcessResponse](
+			t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
+	}
+	ecdsaPid, blindPid := draft(false), draft(true)
+
+	// before the fork: create one election per legacy origin with the CSP's salted root keys
+	ecdsaRoot, err := testCSP.PubKey()
+	c.Assert(err, qt.IsNil)
+	blindRoot, err := testCSP.BlindPubKey()
+	c.Assert(err, qt.IsNil)
+	ecdsaElection := createLegacyElection(t, client, funded, models.CensusOrigin_OFF_CHAIN_CA, ecdsaRoot)
+	blindElection := createLegacyElection(t, client, funded, models.CensusOrigin_OFF_CHAIN_CA_V2, blindRoot)
+	info, err := client.ChainInfo()
+	c.Assert(err, qt.IsNil)
+	c.Assert(info.Height < legacyForkHeight, qt.IsTrue,
+		qt.Commentf("legacy elections created at height %d, not before the fork", info.Height))
+	markLegacyPublished(c, ecdsaPid, ecdsaElection)
+	markLegacyPublished(c, blindPid, blindElection)
+
+	// past the fork the chain refuses new legacy elections...
+	waitForHeight(c, client, legacyForkHeight)
+	txBytes, err := proto.Marshal(legacyElectionTx(t, client, funded, models.CensusOrigin_OFF_CHAIN_CA, ecdsaRoot))
+	c.Assert(err, qt.IsNil)
+	txSignature, err := funded.SignVocdoniTx(txBytes, fetchVocdoniChainID(t, client))
+	c.Assert(err, qt.IsNil)
+	stx, err := proto.Marshal(&models.SignedTx{Tx: txBytes, Signature: txSignature})
+	c.Assert(err, qt.IsNil)
+	_, _, err = client.SendTx(stx)
+	c.Assert(err, qt.ErrorMatches, `(?s).*OFF_CHAIN_CA.*`)
+
+	// ...but the legacy questions still sign with the salted keys and vote
+	voter := ethereum.SignKeys{}
+	c.Assert(voter.Generate(), qt.IsNil)
+	address := internal.HexBytes(voter.Address().Bytes())
+	authReq := &handlers.AuthRequest{Name: members[0].Name, Surname: members[0].Surname, Email: members[0].Email}
+
+	ecdsaTok := authProcessCSP(t, ecdsaPid, authReq)
+	signature := testCSPSignProof(t, ecdsaPid, &handlers.SignRequest{
+		AuthToken: ecdsaTok, ProcessID: ecdsaElection, Payload: hex.EncodeToString(address),
+	}, models.ProofCA_ECDSA_PIDSALTED)
+	proof := testGenerateVoteProof(ecdsaElection, address, signature, 1)
+	proof.GetCa().Type = models.ProofCA_ECDSA_PIDSALTED
+	testCastVote(t, client, &voter, ecdsaElection, proof, []byte(`{"votes":[0]}`))
+
+	blindTok := authProcessCSP(t, blindPid, authReq)
+	blindProof := testBlindSignProof(t, blindPid, blindTok, blindElection, address, models.ProofCA_ECDSA_BLIND_PIDSALTED)
+	testCastVote(t, client, &voter, blindElection, blindProof, []byte(`{"votes":[0]}`))
+
+	for _, election := range []internal.HexBytes{ecdsaElection, blindElection} {
+		count, err := electionVoteCount(client, election)
+		c.Assert(err, qt.IsNil)
+		c.Assert(count, qt.Equals, uint64(1))
+	}
+}
diff --git a/api/process_vote_test.go b/api/process_vote_test.go
index 53f1dd1..b381565 100644
--- a/api/process_vote_test.go
+++ b/api/process_vote_test.go
@@ -173,13 +173,13 @@ func TestRelayVote(t *testing.T) {
 	proof := f.proofFor(t, processID)
 
 	// relay the vote and assert the chain counted it
-	votesBefore, err := f.client.ElectionVoteCount(processID.Bytes())
+	votesBefore, err := electionVoteCount(f.client, processID)
 	c.Assert(err, qt.IsNil)
 
 	nullifier := testRelayVoteRequest(t, f.voter, processID, proof, []byte("[\"1\"]"), nil)
 	c.Assert(nullifier, qt.Not(qt.HasLen), 0)
 
-	votesAfter, err := f.client.ElectionVoteCount(processID.Bytes())
+	votesAfter, err := electionVoteCount(f.client, processID)
 	c.Assert(err, qt.IsNil)
 	c.Assert(votesAfter, qt.Equals, votesBefore+1, qt.Commentf("expected 1 more vote, got %d", votesAfter))
 
@@ -237,10 +237,10 @@ func TestRelayVotesBatch(t *testing.T) {
 	f := setupRelayVoting(t, questions)
 
 	req := &apicommon.RelayVotesRequest{Votes: make([]apicommon.RelayVoteRequest, questions)}
-	votesBefore := make([]uint32, questions)
+	votesBefore := make([]uint64, questions)
 	for i, processID := range f.processIDs {
 		var err error
-		votesBefore[i], err = f.client.ElectionVoteCount(processID.Bytes())
+		votesBefore[i], err = electionVoteCount(f.client, processID)
 		c.Assert(err, qt.IsNil)
 		proof := f.proofFor(t, processID)
 		req.Votes[i] = apicommon.RelayVoteRequest{
@@ -278,7 +278,7 @@ func TestRelayVotesBatch(t *testing.T) {
 
 	// every question got its vote, and every relayed envelope was metered
 	for i, processID := range f.processIDs {
-		votesAfter, err := f.client.ElectionVoteCount(processID.Bytes())
+		votesAfter, err := electionVoteCount(f.client, processID)
 		c.Assert(err, qt.IsNil)
 		c.Assert(votesAfter, qt.Equals, votesBefore[i]+1, qt.Commentf("process %d was not voted", i))
 	}
diff --git a/api/processes_blind_csp_test.go b/api/processes_blind_csp_test.go
index ff8ae86..a6faceb 100644
--- a/api/processes_blind_csp_test.go
+++ b/api/processes_blind_csp_test.go
@@ -12,11 +12,13 @@ import (
 	blind "github.com/vocdoni/go-blindsecp256k1"
 	"github.com/vocdoni/saas-backend/api/apicommon"
 	"github.com/vocdoni/saas-backend/csp/handlers"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
 	"go.vocdoni.io/dvote/crypto/ethereum"
 	"go.vocdoni.io/dvote/types"
 	"go.vocdoni.io/proto/build/go/models"
+	"google.golang.org/protobuf/encoding/protowire"
 	"google.golang.org/protobuf/proto"
 )
 
@@ -30,12 +32,36 @@ func requestBlindPoint(t *testing.T, pid string, tok, election internal.HexBytes
 	return point.Points[0]
 }
 
-// testBlindSignBallot drives the two-round blind CSP flow for one election and returns the on-chain
-// ProofCA the voter casts. It retries the ~1/256 case where the client's blinded message is not a
+// setWeightCertificate sets ProofCA.weightCertificate (field 4, a CAWeightCertificate with weightKey
+// = 1 and signature = 2). The protobuf release this module pins predates the field, so it is
+// encoded by hand as an unknown field, which the protobuf runtime marshals as is.
+func setWeightCertificate(p *models.ProofCA, weightKey, signature []byte) {
+	var cert []byte
+	cert = protowire.AppendTag(cert, 1, protowire.BytesType)
+	cert = protowire.AppendBytes(cert, weightKey)
+	cert = protowire.AppendTag(cert, 2, protowire.BytesType)
+	cert = protowire.AppendBytes(cert, signature)
+	var field []byte
+	field = protowire.AppendTag(field, 4, protowire.BytesType)
+	field = protowire.AppendBytes(field, cert)
+	p.ProtoReflect().SetUnknown(field)
+}
+
+// testBlindSignBallot drives the two-round blind CSP flow for one OFF_CHAIN_CA_V3_BLIND election and
+// returns the on-chain proof the voter casts.
+func testBlindSignBallot(t *testing.T, pid string, tok, election, voterAddr internal.HexBytes) *models.Proof {
+	t.Helper()
+	return testBlindSignProof(t, pid, tok, election, voterAddr, models.ProofCA_ECDSA_BLIND)
+}
+
+// testBlindSignProof drives the two-round blind CSP flow for one election, asserts the CSP names
+// wantType as the proof type (with a weight certificate exactly when it is ECDSA_BLIND), and returns
+// the on-chain proof the voter casts. It retries the ~1/256 case where the client's blinded message is not a
 // valid blind-signature input (the CSP returns invalid_blinded_message without consuming the nonce,
 // and blind-point is idempotent, so the same R is reused). The voter blinds hash(CAbundle) exactly
 // as the Vochain verifier re-derives it (blind.Verify over HashRaw(marshal(bundle))).
-func testBlindSignBallot(t *testing.T, pid string, tok, election, voterAddr internal.HexBytes) *models.Proof {
+func testBlindSignProof(t *testing.T, pid string, tok, election, voterAddr internal.HexBytes, wantType models.ProofCA_Type,
+) *models.Proof {
 	t.Helper()
 	c := qt.New(t)
 	for range 40 {
@@ -66,22 +92,33 @@ func testBlindSignBallot(t *testing.T, pid string, tok, election, voterAddr inte
 			continue // re-blind against the same (idempotent) R
 		}
 		c.Assert(res.Code, qt.Equals, "", qt.Commentf("blind-sign error: %s", res.Error))
+		c.Assert(res.ProofType, qt.Equals, wantType.String())
 		signature, err := blind.Unblind(new(big.Int).SetBytes(res.Signature), userSecretData)
 		c.Assert(err, qt.IsNil)
-		return &models.Proof{Payload: &models.Proof_Ca{Ca: &models.ProofCA{
-			Type:      models.ProofCA_ECDSA_BLIND_PIDSALTED,
+		proofCA := &models.ProofCA{
+			Type:      wantType,
 			Signature: signature.BytesUncompressed(),
 			Bundle:    bundle,
-		}}}
+		}
+		if wantType == models.ProofCA_ECDSA_BLIND {
+			c.Assert(res.WeightKey, qt.HasLen, 33)
+			c.Assert(res.WeightCertificate, qt.HasLen, 65)
+			setWeightCertificate(proofCA, res.WeightKey, res.WeightCertificate)
+		} else {
+			c.Assert(res.WeightKey, qt.HasLen, 0)
+			c.Assert(res.WeightCertificate, qt.HasLen, 0)
+		}
+		return &models.Proof{Payload: &models.Proof_Ca{Ca: proofCA}}
 	}
 	c.Fatalf("blind sign did not produce a valid signature within the retry budget")
 	return nil
 }
 
-// TestProcessBlindCSP exercises the blind (anonymous, OFF_CHAIN_CA_V2) CSP flow end to end: an
-// anonymous process publishes with the V2 origin, the plain ECDSA sign endpoints are rejected, a
-// voter completes the two-round blind protocol and casts an unlinkable vote on chain, the election
-// is single-use, and sign-info omits the address/nullifier the CSP never learns.
+// TestProcessBlindCSP exercises the blind (anonymous, OFF_CHAIN_CA_V3_BLIND) CSP flow end to end:
+// an anonymous process publishes with the per-election blind origin, the plain ECDSA sign
+// endpoints are rejected, a voter completes the two-round blind protocol and casts an unlinkable
+// vote on chain, the election is single-use, and sign-info omits the address/nullifier the CSP
+// never learns.
 func TestProcessBlindCSP(t *testing.T) {
 	c := qt.New(t)
 	token := testCreateUser(t, "adminpassword123")
@@ -119,13 +156,16 @@ func TestProcessBlindCSP(t *testing.T) {
 		"processes", pid, "questions", qid)
 	c.Assert(pubQ.Census.Anonymous, qt.IsTrue)
 
-	// the election is published under the V2 census origin
+	// the election is published under the per-election blind census origin, with the question's
+	// election blind key as census root
 	vc := testNewVocdoniClient(t)
 	onchain, err := vc.Election(types.HexBytes(election))
 	c.Assert(err, qt.IsNil)
 	c.Assert(onchain.Census, qt.Not(qt.IsNil))
-	c.Assert(onchain.Census.CensusOrigin, qt.Equals,
-		models.CensusOrigin_name[int32(models.CensusOrigin_OFF_CHAIN_CA_V2)])
+	c.Assert(onchain.Census.CensusOrigin, qt.Equals, electionkey.OriginBlindName)
+	_, wantRoot, err := testCSP.ElectionCensus(got.Questions[0].ID, true)
+	c.Assert(err, qt.IsNil)
+	c.Assert([]byte(onchain.Census.CensusRoot), qt.DeepEquals, []byte(wantRoot))
 
 	tok := authProcessCSP(t, pid, &handlers.AuthRequest{
 		Name: voterMember.Name, Surname: voterMember.Surname, Email: voterMember.Email,
diff --git a/api/processes_encryption_keys_test.go b/api/processes_encryption_keys_test.go
index db5401f..49b4b9a 100644
--- a/api/processes_encryption_keys_test.go
+++ b/api/processes_encryption_keys_test.go
@@ -8,6 +8,7 @@ import (
 
 	qt "github.com/frankban/quicktest"
 	"github.com/vocdoni/saas-backend/api/apicommon"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
 	"go.vocdoni.io/proto/build/go/models"
@@ -63,7 +64,7 @@ func TestProcessesEncryptionKeys(t *testing.T) {
 			EntityId:      orgAddress.Bytes(),
 			Duration:      120,
 			Status:        models.ProcessStatus_READY,
-			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
+			CensusOrigin:  electionkey.OriginECDSA,
 			CensusRoot:    cspPubKey,
 			MaxCensusSize: 10,
 			EnvelopeType:  &models.EnvelopeType{EncryptedVotes: true},
diff --git a/api/processes_publish_orphans_test.go b/api/processes_publish_orphans_test.go
new file mode 100644
index 0000000..0a8bbb8
--- /dev/null
+++ b/api/processes_publish_orphans_test.go
@@ -0,0 +1,83 @@
+package api
+
+import (
+	"fmt"
+	"testing"
+
+	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/saas-backend/internal"
+	"go.mongodb.org/mongo-driver/v2/bson"
+)
+
+// fakeElections is an electionRootLookup over a fixed set of elections, counting the lookups.
+type fakeElections struct {
+	roots   map[string]internal.HexBytes
+	lookups int
+	err     error
+}
+
+func (f *fakeElections) lookup(id internal.HexBytes) (internal.HexBytes, bool, error) {
+	f.lookups++
+	if f.err != nil {
+		return nil, false, f.err
+	}
+	root, ok := f.roots[string(id)]
+	return root, ok, nil
+}
+
+func TestMatchOrphanElections(t *testing.T) {
+	c := qt.New(t)
+	idA, idB, idOther := internal.HexBytes{0x0a}, internal.HexBytes{0x0b}, internal.HexBytes{0x0c}
+	rootQ1, rootQ2 := internal.HexBytes{0x02, 0x01}, internal.HexBytes{0x02, 0x02}
+	q1, q2, q3 := bson.NewObjectID(), bson.NewObjectID(), bson.NewObjectID()
+	// questions with the same origin and envelope type share their candidate ids: only the census
+	// root tells their elections apart
+	ids := []internal.HexBytes{idOther, idA, idB}
+
+	c.Run("adopts by census root", func(c *qt.C) {
+		elections := &fakeElections{roots: map[string]internal.HexBytes{
+			string(idOther): {0x03, 0x09}, // another process of the organization
+			string(idA):     rootQ2,
+			string(idB):     rootQ1,
+		}}
+		adopted, err := matchOrphanElections([]orphanCandidate{
+			{question: q1, root: rootQ1, ids: ids},
+			{question: q2, root: rootQ2, ids: ids},
+			{question: q3, root: internal.HexBytes{0x02, 0x03}, ids: ids},
+		}, elections.lookup)
+		c.Assert(err, qt.IsNil)
+		c.Assert(adopted, qt.DeepEquals, map[bson.ObjectID]internal.HexBytes{q1: idB, q2: idA})
+		c.Assert(elections.lookups, qt.Equals, len(ids))
+	})
+
+	c.Run("missing elections are not adopted", func(c *qt.C) {
+		elections := &fakeElections{roots: map[string]internal.HexBytes{}}
+		adopted, err := matchOrphanElections([]orphanCandidate{{question: q1, root: rootQ1, ids: ids}}, elections.lookup)
+		c.Assert(err, qt.IsNil)
+		c.Assert(adopted, qt.HasLen, 0)
+	})
+
+	c.Run("a candidate without a root never matches", func(c *qt.C) {
+		elections := &fakeElections{roots: map[string]internal.HexBytes{string(idA): nil}}
+		adopted, err := matchOrphanElections([]orphanCandidate{{question: q1, ids: ids}}, elections.lookup)
+		c.Assert(err, qt.IsNil)
+		c.Assert(adopted, qt.HasLen, 0)
+		c.Assert(elections.lookups, qt.Equals, 0)
+	})
+
+	c.Run("an election is given to one question only", func(c *qt.C) {
+		elections := &fakeElections{roots: map[string]internal.HexBytes{string(idA): rootQ1}}
+		adopted, err := matchOrphanElections([]orphanCandidate{
+			{question: q1, root: rootQ1, ids: ids},
+			{question: q2, root: rootQ1, ids: ids},
+		}, elections.lookup)
+		c.Assert(err, qt.IsNil)
+		c.Assert(adopted, qt.DeepEquals, map[bson.ObjectID]internal.HexBytes{q1: idA})
+	})
+
+	c.Run("lookup errors are returned", func(c *qt.C) {
+		elections := &fakeElections{err: fmt.Errorf("node unavailable")}
+		_, err := matchOrphanElections([]orphanCandidate{{question: q1, root: rootQ1, ids: ids}}, elections.lookup)
+		c.Assert(err, qt.ErrorMatches, "node unavailable")
+	})
+}
diff --git a/api/processes_publish_state_test.go b/api/processes_publish_state_test.go
index 5d46882..fd19993 100644
--- a/api/processes_publish_state_test.go
+++ b/api/processes_publish_state_test.go
@@ -35,7 +35,9 @@ func TestPartiallyPublishedDraftIsImmutable(t *testing.T) {
 
 	// simulate a publish that mined the first question's election and then failed
 	upstream := internal.HexBytes(randomProcessID())
-	c.Assert(testDB.SetQuestionPublished(questions[0].ID, upstream, "url", db.QuestionStatusReady), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(db.QuestionPublishedParams{
+		ID: questions[0].ID, UpstreamID: upstream, MetadataURL: "url", Status: db.QuestionStatusReady,
+	}), qt.IsNil)
 
 	// a draft update is refused: it would replace the questions and erase the mined id
 	edit := newVotingProcessRequest(orgAddress, ids)
@@ -80,9 +82,10 @@ func TestStatusChangeNoOpAndPerQuestionOutcomes(t *testing.T) {
 	// mark both questions published directly; the upstream ids are fake, so any transition that
 	// actually reaches the chain is rejected there — exactly what the mixed batch below needs.
 	for i := range questions {
-		c.Assert(testDB.SetQuestionPublished(
-			questions[i].ID, internal.HexBytes(randomProcessID()), "url", db.QuestionStatusReady,
-		), qt.IsNil)
+		c.Assert(testDB.SetQuestionPublished(db.QuestionPublishedParams{
+			ID: questions[i].ID, UpstreamID: internal.HexBytes(randomProcessID()), MetadataURL: "url",
+			Status: db.QuestionStatusReady,
+		}), qt.IsNil)
 	}
 
 	// everything already READY: asking for READY is a completed job of no-ops, chain untouched
diff --git a/api/statussync_test.go b/api/statussync_test.go
index e2afadf..ad5094c 100644
--- a/api/statussync_test.go
+++ b/api/statussync_test.go
@@ -8,6 +8,7 @@ import (
 
 	"github.com/ethereum/go-ethereum/common"
 	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
 	"github.com/vocdoni/saas-backend/statussync"
@@ -29,7 +30,7 @@ func newSyncTestElection(
 			EntityId:      orgAddress.Bytes(),
 			Duration:      600,
 			Status:        models.ProcessStatus_READY,
-			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
+			CensusOrigin:  electionkey.OriginECDSA,
 			CensusRoot:    censusRoot,
 			MaxCensusSize: 10,
 			EnvelopeType:  &models.EnvelopeType{Anonymous: false, CostFromWeight: false},
diff --git a/db/census_revocation_test.go b/db/census_revocation_test.go
index 05e02f0..6812514 100644
--- a/db/census_revocation_test.go
+++ b/db/census_revocation_test.go
@@ -86,9 +86,13 @@ func setupRevocationFixture(t *testing.T) *revocationFixture {
 	c.Assert(err, qt.IsNil)
 
 	upstream := internal.HexBytes{0xE1, 0xEC, 0x71, 0x01}
-	c.Assert(testDB.SetQuestionPublished(restricted, upstream, "url-1", QuestionStatusReady), qt.IsNil)
-	c.Assert(testDB.SetQuestionPublished(
-		openToAll, internal.HexBytes{0xE1, 0xEC, 0x71, 0x02}, "url-2", QuestionStatusReady), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: restricted, UpstreamID: upstream, MetadataURL: "url-1", Status: QuestionStatusReady,
+	}), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: openToAll, UpstreamID: internal.HexBytes{0xE1, 0xEC, 0x71, 0x02}, MetadataURL: "url-2",
+		Status: QuestionStatusReady,
+	}), qt.IsNil)
 
 	return &revocationFixture{
 		census: census, processID: processID, restricted: restricted, openToAll: openToAll,
diff --git a/db/voting_processes_test.go b/db/voting_processes_test.go
index 7a3a925..1f4cdab 100644
--- a/db/voting_processes_test.go
+++ b/db/voting_processes_test.go
@@ -52,7 +52,9 @@ func TestVotingProcessCRUD(t *testing.T) {
 
 	// publish one question, reverse lookup, then reset
 	upstream := internal.HexBytes("election-1")
-	c.Assert(testDB.SetQuestionPublished(q1ID, upstream, "url", QuestionStatusReady), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: q1ID, UpstreamID: upstream, MetadataURL: "url", Status: QuestionStatusReady,
+	}), qt.IsNil)
 	byUp, err := testDB.QuestionByUpstreamID(upstream)
 	c.Assert(err, qt.IsNil)
 	c.Assert(byUp.ID, qt.Equals, q1ID)
@@ -411,7 +413,9 @@ func TestServedUpstreamIDs(t *testing.T) {
 	_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: id, OrgAddress: org, Order: 1, Type: VotingTypeSingleChoice})
 	c.Assert(err, qt.IsNil)
 	served := internal.HexBytes("served-election")
-	c.Assert(testDB.SetQuestionPublished(qID, served, "url", QuestionStatusReady), qt.IsNil)
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: qID, UpstreamID: served, MetadataURL: "url", Status: QuestionStatusReady,
+	}), qt.IsNil)
 
 	got, err := testDB.ServedUpstreamIDs([]internal.HexBytes{served, internal.HexBytes("orphan-election")})
 	c.Assert(err, qt.IsNil)
@@ -422,6 +426,48 @@ func TestServedUpstreamIDs(t *testing.T) {
 	c.Assert(got, qt.HasLen, 0)
 }
 
+// TestSetQuestionPublishedCensusOrigin checks that a question published on a per-election census
+// origin stores the origin and root, a legacy one stores neither, and an origin without a root (or
+// the reverse) is refused.
+func TestSetQuestionPublishedCensusOrigin(t *testing.T) {
+	c := qt.New(t)
+	org := common.Address{0x13}
+	setupVotingProcessOrg(c, org)
+	id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org})
+	c.Assert(err, qt.IsNil)
+	v3ID, err := testDB.SetQuestion(&VotingProcessQuestion{ProcessID: id, OrgAddress: org, Type: VotingTypeSingleChoice})
+	c.Assert(err, qt.IsNil)
+	legacyID, err := testDB.SetQuestion(&VotingProcessQuestion{
+		ProcessID: id, OrgAddress: org, Order: 1, Type: VotingTypeSingleChoice,
+	})
+	c.Assert(err, qt.IsNil)
+	root := internal.HexBytes{0x02, 0x01, 0x02, 0x03}
+
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: v3ID, UpstreamID: internal.HexBytes("v3-election"), MetadataURL: "url", Status: QuestionStatusReady,
+		CensusOrigin: "OFF_CHAIN_CA_V3_ECDSA", CensusRoot: root,
+	}), qt.IsNil)
+	got, err := testDB.Question(v3ID)
+	c.Assert(err, qt.IsNil)
+	c.Assert(got.CensusOrigin, qt.Equals, "OFF_CHAIN_CA_V3_ECDSA")
+	c.Assert(got.CensusRoot, qt.DeepEquals, root)
+
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: legacyID, UpstreamID: internal.HexBytes("legacy-election"), MetadataURL: "url", Status: QuestionStatusReady,
+	}), qt.IsNil)
+	got, err = testDB.Question(legacyID)
+	c.Assert(err, qt.IsNil)
+	c.Assert(got.CensusOrigin, qt.Equals, "")
+	c.Assert(got.CensusRoot, qt.HasLen, 0)
+
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: legacyID, UpstreamID: internal.HexBytes("legacy-election"), CensusOrigin: "OFF_CHAIN_CA_V3_ECDSA",
+	}), qt.ErrorIs, ErrInvalidData)
+	c.Assert(testDB.SetQuestionPublished(QuestionPublishedParams{
+		ID: legacyID, UpstreamID: internal.HexBytes("legacy-election"), CensusRoot: root,
+	}), qt.ErrorIs, ErrInvalidData)
+}
+
 // TestClaimVotingProcessPublishOwnerAndRevision pins the two guards added to the publish claim:
 // the revision token, so a claim snapshot taken before a draft edit cannot publish the stale
 // content, and the owner token, so renewal, release and completion only succeed for the worker
diff --git a/test/sequencer/go.mod b/test/sequencer/go.mod
new file mode 100644
index 0000000..5be1255
--- /dev/null
+++ b/test/sequencer/go.mod
@@ -0,0 +1,97 @@
+// Module sequencer pins the vochain-sequencer whose voconed binary the saas-backend integration
+// tests run as their chain. It is a separate module so the production module never depends on
+// vochain-sequencer; nothing in it is imported by saas-backend code.
+module github.com/vocdoni/saas-backend/test/sequencer
+
+go 1.26.5
+
+tool github.com/vocdoni/vochain-sequencer/cmd/voconed
+
+require (
+	github.com/766b/chi-prometheus v0.0.0-20211217152057-87afa9aa2ca8 // indirect
+	github.com/DataDog/zstd v1.5.7 // indirect
+	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
+	github.com/VictoriaMetrics/metrics v1.24.0 // indirect
+	github.com/beorn7/perks v1.0.1 // indirect
+	github.com/cespare/xxhash/v2 v2.3.0 // indirect
+	github.com/cockroachdb/errors v1.11.3 // indirect
+	github.com/cockroachdb/fifo v0.0.0-20240606204812-0bbfbd93a7ce // indirect
+	github.com/cockroachdb/logtags v0.0.0-20230118201751-21c54148d20b // indirect
+	github.com/cockroachdb/pebble v1.1.5 // indirect
+	github.com/cockroachdb/redact v1.1.5 // indirect
+	github.com/cockroachdb/tokenbucket v0.0.0-20230807174530-cc333fc44b06 // indirect
+	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
+	github.com/ethereum/go-ethereum v1.17.0 // indirect
+	github.com/fsnotify/fsnotify v1.10.1 // indirect
+	github.com/getsentry/sentry-go v0.27.0 // indirect
+	github.com/glendc/go-external-ip v0.1.0 // indirect
+	github.com/go-chi/chi v4.1.2+incompatible // indirect
+	github.com/go-chi/chi/v5 v5.2.4 // indirect
+	github.com/go-chi/cors v1.2.1 // indirect
+	github.com/gogo/protobuf v1.3.2 // indirect
+	github.com/golang/snappy v1.0.0 // indirect
+	github.com/google/uuid v1.6.0 // indirect
+	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
+	github.com/hashicorp/hcl v1.0.1-vault-5 // indirect
+	github.com/holiman/uint256 v1.3.2 // indirect
+	github.com/iancoleman/strcase v0.3.0 // indirect
+	github.com/iden3/go-iden3-crypto v0.0.15 // indirect
+	github.com/ipfs/go-cid v0.6.2 // indirect
+	github.com/klauspost/compress v1.19.1 // indirect
+	github.com/klauspost/cpuid/v2 v2.4.0 // indirect
+	github.com/kr/pretty v0.3.1 // indirect
+	github.com/kr/text v0.2.0 // indirect
+	github.com/magiconair/properties v1.8.7 // indirect
+	github.com/mattn/go-colorable v0.1.14 // indirect
+	github.com/mattn/go-isatty v0.0.22 // indirect
+	github.com/mattn/go-sqlite3 v1.14.24 // indirect
+	github.com/mfridman/interpolate v0.0.2 // indirect
+	github.com/minio/sha256-simd v1.0.1 // indirect
+	github.com/mitchellh/mapstructure v1.5.0 // indirect
+	github.com/mr-tron/base58 v1.3.0 // indirect
+	github.com/multiformats/go-base32 v0.1.0 // indirect
+	github.com/multiformats/go-base36 v0.2.0 // indirect
+	github.com/multiformats/go-multibase v0.3.0 // indirect
+	github.com/multiformats/go-multihash v0.2.3 // indirect
+	github.com/multiformats/go-varint v0.1.0 // indirect
+	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
+	github.com/pelletier/go-toml/v2 v2.2.3 // indirect
+	github.com/pkg/errors v0.9.1 // indirect
+	github.com/pressly/goose/v3 v3.23.0 // indirect
+	github.com/prometheus/client_golang v1.24.1 // indirect
+	github.com/prometheus/client_model v0.6.2 // indirect
+	github.com/prometheus/common v0.70.1 // indirect
+	github.com/prometheus/procfs v0.21.1 // indirect
+	github.com/rogpeppe/go-internal v1.14.1 // indirect
+	github.com/rs/zerolog v1.31.0 // indirect
+	github.com/sagikazarmark/locafero v0.4.0 // indirect
+	github.com/sagikazarmark/slog-shim v0.1.0 // indirect
+	github.com/sethvargo/go-retry v0.3.0 // indirect
+	github.com/sourcegraph/conc v0.3.0 // indirect
+	github.com/spaolacci/murmur3 v1.1.0 // indirect
+	github.com/spf13/afero v1.11.0 // indirect
+	github.com/spf13/cast v1.6.0 // indirect
+	github.com/spf13/pflag v1.0.6 // indirect
+	github.com/spf13/viper v1.19.0 // indirect
+	github.com/subosito/gotenv v1.6.0 // indirect
+	github.com/valyala/fastrand v1.1.0 // indirect
+	github.com/valyala/histogram v1.2.0 // indirect
+	github.com/vocdoni/go-blindsecp256k1 v0.2.0 // indirect
+	github.com/vocdoni/vochain-sequencer v0.0.0-00010101000000-000000000000 // indirect
+	go.uber.org/multierr v1.11.0 // indirect
+	go.vocdoni.io/proto v1.17.0 // indirect
+	golang.org/x/crypto v0.55.0 // indirect
+	golang.org/x/exp v0.0.0-20260718201538-764159d718ef // indirect
+	golang.org/x/net v0.58.0 // indirect
+	golang.org/x/sync v0.22.0 // indirect
+	golang.org/x/sys v0.47.0 // indirect
+	golang.org/x/text v0.41.0 // indirect
+	google.golang.org/protobuf v1.36.11 // indirect
+	gopkg.in/ini.v1 v1.67.0 // indirect
+	gopkg.in/yaml.v3 v3.0.1 // indirect
+	lukechampine.com/blake3 v1.4.1 // indirect
+)
+
+replace github.com/vocdoni/vochain-sequencer => /home/user/src/vd/vochain-sequencer/.claude/worktrees/csp-key-per-election
+
+replace go.vocdoni.io/proto => /home/user/src/vd/dvote-protobuf/.claude/worktrees/csp-key-per-election
diff --git a/test/voconed.go b/test/voconed.go
index c06bedf..a78c0a9 100644
--- a/test/voconed.go
+++ b/test/voconed.go
@@ -1,5 +1,5 @@
 // Package test provides testing utilities for the saas-backend service,
-// including test containers for mail and MongoDB, and an in-process Voconed chain.
+// including test containers for mail and MongoDB, and vochain-sequencer test chains.
 package test
 
 import (
@@ -7,19 +7,13 @@ import (
 	"fmt"
 	"net"
 	"os"
-	"sync"
+	"os/exec"
+	"path/filepath"
+	"runtime"
+	"strconv"
 	"time"
 
-	"github.com/ethereum/go-ethereum/common"
-	"go.vocdoni.io/dvote/api"
 	"go.vocdoni.io/dvote/apiclient"
-	"go.vocdoni.io/dvote/crypto/ethereum"
-	"go.vocdoni.io/dvote/db"
-	"go.vocdoni.io/dvote/httprouter"
-	"go.vocdoni.io/dvote/log"
-	"go.vocdoni.io/dvote/vochain/state"
-	"go.vocdoni.io/dvote/vocone"
-	"go.vocdoni.io/proto/build/go/models"
 )
 
 const (
@@ -36,121 +30,91 @@ const (
 	VoconedFunds = 100000000
 )
 
-// Voconed is a running in-process Vocone chain for tests. It embeds *vocone.Vocone (so
-// Close() stops it) and exposes the base API URL.
-type Voconed struct {
-	*vocone.Vocone
-	Endpoint string
+// sequencerModuleDir is the nested Go module that pins vochain-sequencer for the tests.
+func sequencerModuleDir() string {
+	_, file, _, _ := runtime.Caller(0)
+	return filepath.Join(filepath.Dir(file), "sequencer")
 }
 
-var (
-	sharedVoconed     *Voconed
-	sharedVoconedErr  error
-	sharedVoconedOnce sync.Once
-)
+// BuildVoconed builds the voconed binary of the vochain-sequencer pinned by the nested test module
+// test/sequencer into dir, and returns its path. The production module does not depend on
+// vochain-sequencer; only this build does.
+func BuildVoconed(ctx context.Context, dir string) (string, error) {
+	bin := filepath.Join(dir, "voconed")
+	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/vocdoni/vochain-sequencer/cmd/voconed")
+	cmd.Dir = sequencerModuleDir()
+	if out, err := cmd.CombinedOutput(); err != nil {
+		return "", fmt.Errorf("could not build voconed: %w: %s", err, out)
+	}
+	return bin, nil
+}
+
+// VoconedConfig configures a test chain.
+type VoconedConfig struct {
+	// Binary is the voconed binary, as returned by BuildVoconed.
+	Binary string
+	// ChainID must not be listed in the sequencer's fork configuration.
+	ChainID string
+	// CSPKeyForkHeight is the height from which the chain applies the CSP key fork rules. Below it
+	// the chain accepts new OFF_CHAIN_CA and OFF_CHAIN_CA_V2 elections; from it on, only the
+	// per-election origins.
+	CSPKeyForkHeight uint32
+}
 
-// SharedVoconed returns the process-wide in-process Vocone chain, starting it on first call
-// and returning the same instance to every later caller. vocone registers global Prometheus
-// metrics, so only one instance may exist per test binary; this enforces that. The instance
-// lives for the lifetime of the test process (started with context.Background()); the OS
-// reclaims it on exit, so callers must not Close() it.
-func SharedVoconed() (*Voconed, error) {
-	sharedVoconedOnce.Do(func() {
-		sharedVoconed, sharedVoconedErr = startVoconed(context.Background())
-	})
-	return sharedVoconed, sharedVoconedErr
+// Voconed is a vochain-sequencer voconed chain running as a subprocess, with
+// VoconedFundedAccount funded and every transaction costing VoconedTxCosts.
+type Voconed struct {
+	Endpoint string
+	cmd      *exec.Cmd
+	dataDir  string
 }
 
-// startVoconed brings up an in-process Vocone chain built from the pinned dvote dependency
-// (so it always matches the client, including the batch-transaction endpoint) and returns
-// its base API URL together with the running instance. Callers go through SharedVoconed so
-// exactly one instance runs per process; the chain stops when ctx is canceled. Unlike a
-// pulled container image, this cannot drift from the version the code is compiled against.
-func startVoconed(ctx context.Context) (*Voconed, error) {
+// StartVoconed starts a test chain and waits until it has produced its first block. The caller
+// must Close it.
+func StartVoconed(cfg VoconedConfig) (*Voconed, error) {
 	dataDir, err := os.MkdirTemp("", "voconed-test-*")
 	if err != nil {
 		return nil, fmt.Errorf("could not create voconed data dir: %w", err)
 	}
-
-	keymng := &ethereum.SignKeys{}
-	if err := keymng.AddHexKey(VoconedFoundedPrivKey); err != nil {
-		return nil, fmt.Errorf("could not load voconed key: %w", err)
-	}
-
-	// IPFS is disabled: it binds fixed ports (colliding with a local daemon or parallel
-	// test binaries) and the SaaS never uses the chain's IPFS-backed census — it runs an
-	// off-chain (CSP) census. We therefore also enable only the API handlers the SaaS uses
-	// (see enableVoconeAPI); census/SIK need the IPFS census DB and are skipped.
-	vc, err := vocone.NewVocone(dataDir, keymng, true, "", nil)
-	if err != nil {
-		return nil, fmt.Errorf("could not create vocone: %w", err)
-	}
-	vc.App.SetChainID("test-vocone")
-	vc.App.SetBlockTimeTarget(time.Second)
-
-	go func() {
-		if err := vc.Start(ctx); err != nil && ctx.Err() == nil {
-			log.Warnw("vocone stopped with error", "error", err)
-		}
-	}()
-
-	// freePort only reserves a port momentarily, so another process could grab it before the API
-	// binds. Retry with a fresh port on a bind collision (enableVoconeAPI builds a new router each
-	// call, so a retry is clean) to avoid a flaky TOCTOU startup failure.
-	var port int
-	for range 10 {
-		port, err = freePort()
-		if err != nil {
-			return nil, err
-		}
-		if err = enableVoconeAPI(vc, "127.0.0.1", port, VoconedURLPath); err == nil {
-			break
-		}
-	}
+	port, err := freePort()
 	if err != nil {
-		return nil, fmt.Errorf("could not enable vocone api: %w", err)
-	}
-	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", port, VoconedURLPath)
-
-	// wait until the chain has produced its first block before touching state
-	if err := waitForVocone(endpoint); err != nil {
 		return nil, err
 	}
-
-	if err := vc.SetBulkTxCosts(VoconedTxCosts, true); err != nil {
-		return nil, fmt.Errorf("could not set tx costs: %w", err)
+	cmd := exec.Command(cfg.Binary,
+		"--dir", dataDir,
+		"--chainID", cfg.ChainID,
+		"--port", strconv.Itoa(port),
+		"--urlPath", VoconedURLPath,
+		"--blockPeriod", "1",
+		"--logLevel", "warn",
+		"--fundedAccounts", fmt.Sprintf("%s:%d", VoconedFundedAccount, VoconedFunds),
+		"--setTxCosts", "--txCosts", strconv.Itoa(VoconedTxCosts),
+		"--cspKeyForkHeight", strconv.FormatUint(uint64(cfg.CSPKeyForkHeight), 10),
+	)
+	cmd.Stdout = os.Stdout
+	cmd.Stderr = os.Stderr
+	if err := cmd.Start(); err != nil {
+		return nil, fmt.Errorf("could not start voconed: %w", err)
 	}
-	if err := vc.SetElectionPrice(); err != nil {
-		return nil, fmt.Errorf("could not set election price: %w", err)
+	v := &Voconed{
+		Endpoint: fmt.Sprintf("http://127.0.0.1:%d%s", port, VoconedURLPath),
+		cmd:      cmd,
+		dataDir:  dataDir,
 	}
-	if err := vc.CreateAccount(common.HexToAddress(VoconedFundedAccount), &state.Account{
-		Account: models.Account{Balance: VoconedFunds},
-	}); err != nil {
-		return nil, fmt.Errorf("could not fund voconed account: %w", err)
+	if err := waitForVocone(v.Endpoint); err != nil {
+		v.Close()
+		return nil, err
 	}
-	return &Voconed{Vocone: vc, Endpoint: endpoint}, nil
+	return v, nil
 }
 
-// enableVoconeAPI serves the Vocone API on host:port/urlPath with only the handlers the
-// SaaS uses. It mirrors vocone.EnableAPI but omits the census and SIK handlers, which
-// require the IPFS-backed census DB that is intentionally not initialized here.
-func enableVoconeAPI(vc *vocone.Vocone, host string, port int, urlPath string) error {
-	vc.Router = new(httprouter.HTTProuter)
-	if err := vc.Router.Init(host, port); err != nil {
-		return err
+// Close stops the chain and removes its data.
+func (v *Voconed) Close() {
+	if v.cmd.Process != nil {
+		_ = v.cmd.Process.Kill()
+		_ = v.cmd.Wait()
 	}
-	uAPI, err := api.NewAPI(vc.Router, urlPath, vc.Config.DataDir, db.TypePebble)
-	if err != nil {
-		return err
-	}
-	uAPI.Attach(vc.App, vc.Stats, vc.Indexer, vc.Storage, vc.CensusDB)
-	return uAPI.EnableHandlers(
-		api.ElectionHandler,
-		api.VoteHandler,
-		api.ChainHandler,
-		api.WalletHandler,
-		api.AccountHandler,
-	)
+	_ = os.RemoveAll(v.dataDir)
 }
 
 // freePort returns an available local TCP port.
@@ -163,9 +127,9 @@ func freePort() (int, error) {
 	return l.Addr().(*net.TCPAddr).Port, nil
 }
 
-// waitForVocone blocks until the Vocone API is reachable and has produced at least one block.
+// waitForVocone blocks until the chain API is reachable and has produced at least one block.
 func waitForVocone(endpoint string) error {
-	deadline := time.Now().Add(30 * time.Second)
+	deadline := time.Now().Add(60 * time.Second)
 	for time.Now().Before(deadline) {
 		if cli, err := apiclient.New(endpoint); err == nil {
 			if info, err := cli.ChainInfo(); err == nil && info.Height >= 1 {
@@ -174,5 +138,5 @@ func waitForVocone(endpoint string) error {
 		}
 		time.Sleep(200 * time.Millisecond)
 	}
-	return fmt.Errorf("timeout waiting for in-process vocone at %s", endpoint)
+	return fmt.Errorf("timeout waiting for voconed at %s", endpoint)
 }
```

```bash
cd SAAS/test/sequencer && go mod tidy && cd SAAS     # writes test/sequencer/go.sum
cd SAAS && go test ./db/ ./account/ ./api/ -run 'SetQuestionPublishedCensusOrigin|BuildNewProcessTxCensusOrigin|MatchOrphanElections|CSPKeyPerElection|AdoptsOrphan|CSPLegacyQuestionsAfterFork' -count=1
```
Expected failure: build errors `undefined: db.QuestionPublishedParams`,
`NewProcessParams.CensusOrigin`, `matchOrphanElections`, `account.ElectionID`.

Step 2 (implementation):

```diff
diff --git a/account/elections.go b/account/elections.go
new file mode 100644
index 0000000..e6e55e6
--- /dev/null
+++ b/account/elections.go
@@ -0,0 +1,74 @@
+package account
+
+import (
+	"encoding/json"
+	"fmt"
+	"net/http"
+
+	"github.com/ethereum/go-ethereum/common"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/internal"
+	"go.vocdoni.io/dvote/api"
+	"go.vocdoni.io/dvote/vochain/processid"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+// AccountElectionIndex returns the number of elections the organization account has created on
+// chain. It is read from the account state, so it counts an election as soon as it is mined.
+func (a *Account) AccountElectionIndex(org common.Address) (uint32, error) {
+	acc, err := a.client.Account(org.String())
+	if err != nil {
+		return 0, fmt.Errorf("could not fetch organization account: %w", err)
+	}
+	return acc.ElectionIndex, nil
+}
+
+// ElectionCensusRoot returns the census root of the on-chain election with the given id. found
+// is false, with a nil error, when the node does not know the election (yet).
+func (a *Account) ElectionCensusRoot(processID internal.HexBytes) (root internal.HexBytes, found bool, err error) {
+	resp, code, err := a.client.Request(http.MethodGet, nil, "elections", processID.String())
+	if err != nil {
+		return nil, false, fmt.Errorf("could not fetch election %x: %w", []byte(processID), err)
+	}
+	switch code {
+	case http.StatusOK:
+	case http.StatusNotFound:
+		return nil, false, nil
+	default:
+		return nil, false, fmt.Errorf("could not fetch election %x: status %d", []byte(processID), code)
+	}
+	var election api.Election
+	if err := json.Unmarshal(resp, &election); err != nil {
+		return nil, false, fmt.Errorf("could not decode election %x: %w", []byte(processID), err)
+	}
+	if election.Census == nil {
+		return nil, false, fmt.Errorf("election %x has no census", []byte(processID))
+	}
+	return internal.HexBytes(election.Census.CensusRoot), true, nil
+}
+
+// ElectionIDParams identifies the election an organization creates at a given election index.
+type ElectionIDParams struct {
+	OrgAddress   common.Address
+	CensusOrigin models.CensusOrigin
+	Params       *db.ElectionParams
+	// Index is the organization's election index when the election is created: the
+	// AccountElectionIndex read just before its NEW_PROCESS transaction mines.
+	Index uint32
+}
+
+// ElectionID returns the id the chain assigns to an election created from p, computed the way
+// the chain derives it from the chain id, organization, census origin, envelope type and index.
+func (a *Account) ElectionID(p *ElectionIDParams) (internal.HexBytes, error) {
+	pid := new(processid.ProcessID)
+	pid.SetChainID(a.client.ChainID())
+	pid.SetAddr(p.OrgAddress)
+	pid.SetNonce(p.Index)
+	if err := pid.SetCensusOrigin(p.CensusOrigin); err != nil {
+		return nil, fmt.Errorf("could not compute election id: %w", err)
+	}
+	if err := pid.SetEnvelopeType(EnvelopeType(p.Params)); err != nil {
+		return nil, fmt.Errorf("could not compute election id: %w", err)
+	}
+	return pid.Marshal(), nil
+}
diff --git a/account/process.go b/account/process.go
index 5e24b4e..b1793ca 100644
--- a/account/process.go
+++ b/account/process.go
@@ -13,6 +13,7 @@ import (
 	"time"
 
 	"github.com/ethereum/go-ethereum/common"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
 	"github.com/vocdoni/saas-backend/db"
 	"github.com/vocdoni/saas-backend/internal"
 	"go.vocdoni.io/dvote/api"
@@ -73,14 +74,16 @@ func BuildElectionMetadata(params *db.ElectionParams) ([]byte, error) {
 type NewProcessParams struct {
 	OrgAddress common.Address // organization (entity) that owns the election
 	Params     *db.ElectionParams
-	CensusRoot []byte // census root: the CSP ECDSA public key, or its blind public key when Anonymous
+	// CensusRoot is the census root of the election: the public key its CSP proofs are checked
+	// against.
+	CensusRoot []byte
 	CensusURI  string // census endpoint (SaaS CSP base URL)
-	// Anonymous selects blind CSP: the election is published with census origin OFF_CHAIN_CA_V2
-	// (the CSP blind-signs ballots so it cannot link a signature to the voter). CensusRoot must then
-	// be the CSP blind public key. This is unrelated to EnvelopeType.Anonymous (the zk-SNARK flag),
-	// which stays false — the Vochain routes both CA origins to the CSP verifier regardless of it.
-	Anonymous   bool
-	MetadataURL string // public https URL of the stored ElectionMetadata JSON
+	// CensusOrigin is the CSP census origin of the election: OFF_CHAIN_CA_V3_ECDSA or
+	// OFF_CHAIN_CA_V3_BLIND (electionkey.OriginECDSA, electionkey.OriginBlind) with a
+	// per-election key as CensusRoot, or one of the legacy OFF_CHAIN_CA and OFF_CHAIN_CA_V2. It is
+	// unrelated to EnvelopeType.Anonymous (the zk-SNARK flag).
+	CensusOrigin models.CensusOrigin
+	MetadataURL  string // public https URL of the stored ElectionMetadata JSON
 	// Nonce, when set, is used as the tx account nonce instead of reading the current
 	// on-chain nonce. Batch publishing sets explicit consecutive nonces so N txs can be
 	// signed and submitted together; single publishes leave it nil to read the nonce.
@@ -177,6 +180,28 @@ func electionStartDuration(startDate, endDate time.Time) (startTime, duration ui
 	return 0, uint32(d.Seconds()), nil
 }
 
+// isCSPCensusOrigin reports whether origin is a census origin whose proofs a CSP signs.
+func isCSPCensusOrigin(origin models.CensusOrigin) bool {
+	switch origin {
+	case models.CensusOrigin_OFF_CHAIN_CA, models.CensusOrigin_OFF_CHAIN_CA_V2,
+		electionkey.OriginECDSA, electionkey.OriginBlind:
+		return true
+	default:
+		return false
+	}
+}
+
+// EnvelopeType returns the on-chain envelope type of an election built from ep.
+func EnvelopeType(ep *db.ElectionParams) *models.EnvelopeType {
+	return &models.EnvelopeType{
+		Serial:         false,
+		Anonymous:      ep.ElectionType.Anonymous,
+		EncryptedVotes: ep.ElectionType.SecretUntilTheEnd,
+		UniqueValues:   ep.VoteType.UniqueChoices,
+		CostFromWeight: ep.VoteType.CostFromWeight,
+	}
+}
+
 // BuildNewProcessTx constructs an unsigned Tx_NewProcess from high-level election
 // params. It reads the organization account's current on-chain nonce, maps
 // ElectionParams into the on-chain models.Process using a CSP census, and always
@@ -207,12 +232,8 @@ func (a *Account) BuildNewProcessTx(p *NewProcessParams) (*models.Tx, error) {
 		return nil, err
 	}
 
-	// blind CSP publishes under the V2 origin (weight-binding salt); the plain ECDSA CSP stays on
-	// the legacy origin. Both are verified on-chain by the CSP verifier; the flag never touches
-	// EnvelopeType.Anonymous.
-	censusOrigin := models.CensusOrigin_OFF_CHAIN_CA
-	if p.Anonymous {
-		censusOrigin = models.CensusOrigin_OFF_CHAIN_CA_V2
+	if !isCSPCensusOrigin(p.CensusOrigin) {
+		return nil, fmt.Errorf("census origin %d is not a CSP census origin", p.CensusOrigin)
 	}
 
 	metadataURL := p.MetadataURL
@@ -221,17 +242,11 @@ func (a *Account) BuildNewProcessTx(p *NewProcessParams) (*models.Tx, error) {
 		Status:        initialStatus,
 		StartTime:     startTime,
 		Duration:      duration,
-		CensusOrigin:  censusOrigin,
+		CensusOrigin:  p.CensusOrigin,
 		CensusRoot:    p.CensusRoot,
 		MaxCensusSize: ep.MaxCensusSize,
 		Metadata:      &metadataURL,
-		EnvelopeType: &models.EnvelopeType{
-			Serial:         false,
-			Anonymous:      ep.ElectionType.Anonymous,
-			EncryptedVotes: ep.ElectionType.SecretUntilTheEnd,
-			UniqueValues:   ep.VoteType.UniqueChoices,
-			CostFromWeight: ep.VoteType.CostFromWeight,
-		},
+		EnvelopeType:  EnvelopeType(ep),
 		VoteOptions: &models.ProcessVoteOptions{
 			MaxCount:          ep.VoteType.MaxCount,
 			MaxValue:          ep.VoteType.MaxValue,
diff --git a/api/processes_admin.go b/api/processes_admin.go
index efb15b3..057133e 100644
--- a/api/processes_admin.go
+++ b/api/processes_admin.go
@@ -764,6 +764,23 @@ func (a *API) enqueueSetProcessCensus(orgAddress common.Address, targets []censu
 	return jobID, nil
 }
 
+// questionCensusRoot returns the census root of a published question's election: its own root
+// when it uses a per-election CSP key, otherwise the CSP root public key the legacy origins publish
+// (the blind one for an anonymous census).
+func (a *API) questionCensusRoot(q *db.VotingProcessQuestion, census *db.Census) ([]byte, error) {
+	if len(q.CensusRoot) > 0 {
+		return q.CensusRoot, nil
+	}
+	root, err := a.csp.PubKey()
+	if census.Anonymous {
+		root, err = a.csp.BlindPubKey()
+	}
+	if err != nil {
+		return nil, fmt.Errorf("could not get the CSP public key: %w", err)
+	}
+	return root, nil
+}
+
 // setProcessCensusSizes submits one SET_PROCESS_CENSUS tx per target, skipping the elections that
 // already have the room: a resize that waited for the organization lock may have been overtaken by
 // a larger one, and the chain refuses a shrink.
@@ -773,9 +790,12 @@ func (a *API) setProcessCensusSizes(orgSigner *ethereum.SignKeys, targets []cens
 		if current, err := a.electionMaxCensusSize(&t.question); err == nil && t.size <= current {
 			continue
 		}
+		root, err := a.questionCensusRoot(&t.question, t.census)
+		if err != nil {
+			return nil, err
+		}
 		tx, err := a.account.BuildSetProcessCensusTx(
-			orgSigner.Address(), t.question.UpstreamID,
-			t.census.Published.Root, t.census.Published.URI, t.size,
+			orgSigner.Address(), t.question.UpstreamID, root, t.census.Published.URI, t.size,
 		)
 		if err != nil {
 			return nil, err
diff --git a/api/processes_publish.go b/api/processes_publish.go
index ebcd7f1..6e4c827 100644
--- a/api/processes_publish.go
+++ b/api/processes_publish.go
@@ -361,17 +361,10 @@ func (a *API) startProcessPublish(ctx context.Context, t publishTarget) (string,
 	if err != nil {
 		return "", errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err)
 	}
-	// root = CSP public key; the on-chain census authorization is delegated to the CSP for every
-	// question. A blind (anonymous) census publishes the CSP blind public key instead, so the
-	// Vochain verifies each ballot's blind ECDSA proof against it under census origin OFF_CHAIN_CA_V2.
-	cspPubKey, err := a.csp.PubKey()
-	if census.Anonymous {
-		cspPubKey, err = a.csp.BlindPubKey()
-	}
-	if err != nil {
-		return "", errors.ErrGenericInternalServerError.Withf("could not get csp public key: %v", err)
-	}
-	census.Published = db.PublishedCensus{Root: cspPubKey, URI: a.serverURL, CreatedAt: time.Now()}
+	// the on-chain census authorization is delegated to the CSP for every question. Each question's
+	// election has its own census root, a per-election CSP key (see assignCensus), so the census
+	// records only where the CSP is served and leaves Root empty.
+	census.Published = db.PublishedCensus{URI: a.serverURL, CreatedAt: time.Now()}
 	if _, err := a.db.SetCensus(census); err != nil {
 		return "", fmt.Errorf("failed to publish census: %w", err)
 	}
@@ -400,7 +393,7 @@ func (a *API) startProcessPublish(ctx context.Context, t publishTarget) (string,
 	reserved := managedReserved
 	worker := &publishWorker{
 		a: a, vp: vp, questions: questions, census: census, org: org, user: user,
-		orgSigner: orgSigner, cspPubKey: cspPubKey, integratorAddr: integratorAddr,
+		orgSigner: orgSigner, integratorAddr: integratorAddr,
 		reserved: reserved, nonTestSized: nonTestSized, owner: owner,
 		minedUnpersisted: make(map[bson.ObjectID]internal.HexBytes),
 	}
@@ -430,7 +423,6 @@ type publishWorker struct {
 	org            *db.Organization
 	user           *db.User
 	orgSigner      *ethereum.SignKeys
-	cspPubKey      []byte
 	integratorAddr common.Address
 	reserved       bool
 	nonTestSized   bool
@@ -443,6 +435,13 @@ type publishWorker struct {
 	// (the election already exists — resubmitting would mint a duplicate); only the DB write is
 	// retried, until it succeeds or the attempt is abandoned.
 	minedUnpersisted map[bson.ObjectID]internal.HexBytes
+	// knownElectionIndex is the organization's election index that the elections this worker saw
+	// mine account for. A higher on-chain index means an election mined unseen (a lost batch
+	// response, or a tx that mined after its confirmation timed out); adoptOrphanElections finds it
+	// before anything is submitted again.
+	knownElectionIndex uint32
+	// electionIndexRead is set once knownElectionIndex holds the index read at the start of run.
+	electionIndexRead bool
 }
 
 // renewClaim refreshes the publish claim every PublishStaleAfter/3 until stop is closed, so a
@@ -497,7 +496,10 @@ func (pw *publishWorker) persistPublished(q *db.VotingProcessQuestion, upstreamI
 		if err = pw.a.db.MarkVotingProcessMined(pw.vp.ID); err != nil {
 			continue
 		}
-		if err = pw.a.db.SetQuestionPublished(q.ID, upstreamID, q.MetadataURL, initialStatus); err == nil {
+		if err = pw.a.db.SetQuestionPublished(db.QuestionPublishedParams{
+			ID: q.ID, UpstreamID: upstreamID, MetadataURL: q.MetadataURL, Status: initialStatus,
+			CensusOrigin: q.CensusOrigin, CensusRoot: q.CensusRoot,
+		}); err == nil {
 			q.UpstreamID = upstreamID
 			delete(pw.minedUnpersisted, q.ID)
 			return true
@@ -548,6 +550,10 @@ func (pw *publishWorker) run() (result *db.JobResult, err error) {
 		pw.abandon()
 		return nil, err
 	}
+	if err := pw.initElectionIndex(); err != nil {
+		pw.abandon()
+		return nil, err
+	}
 	confirmed := false
 	for round := 0; round < maxPublishRounds && !confirmed; round++ {
 		// a worker that no longer owns the publish claim must stop before submitting anything:
@@ -563,16 +569,15 @@ func (pw *publishWorker) run() (result *db.JobResult, err error) {
 		// first retry any chain-confirmed election whose DB write is still pending: it must
 		// never be resubmitted, only re-persisted.
 		pw.flushMinedUnpersisted()
-		pending := make([]*db.VotingProcessQuestion, 0, len(pw.questions))
-		for i := range pw.questions {
-			q := &pw.questions[i]
-			if _, mined := pw.minedUnpersisted[q.ID]; mined {
-				continue // already on chain, only the DB write is pending
-			}
-			if len(q.UpstreamID) == 0 {
-				pending = append(pending, q)
-			}
+		// then attribute any election that mined unseen; until every one is attributed, building
+		// could mint a second election for a question that already has one.
+		if attributed, err := pw.adoptOrphanElections(); err != nil || !attributed {
+			log.Warnw("organization elections not attributed yet, not submitting this round",
+				"processId", pw.vp.ID.Hex(), "error", err)
+			time.Sleep(orphanElectionWait)
+			continue
 		}
+		pending := pw.pendingQuestions()
 		if len(pending) == 0 {
 			// everything is on chain; confirmed only once every row is persisted too
 			confirmed = pw.flushMinedUnpersisted()
@@ -680,6 +685,12 @@ func (pw *publishWorker) confirmClaim() (bool, error) {
 // and reserves its own slot.
 func (pw *publishWorker) abandon() {
 	a := pw.a
+	// an election that mined unseen must be recorded before the questions are reset, or a later
+	// publish would create a second one for its question.
+	if attributed, err := pw.adoptOrphanElections(); err != nil || !attributed {
+		log.Errorw(fmt.Errorf("unattributed organization elections at abandon: %v", err),
+			fmt.Sprintf("process %s may have on-chain elections it does not record", pw.vp.ID.Hex()))
+	}
 	// last chance to persist elections that mined but whose DB write kept failing: losing the
 	// pair here means a later resume cannot know the election exists and will mint a new one.
 	if !pw.flushMinedUnpersisted() {
@@ -747,12 +758,16 @@ func (pw *publishWorker) buildBatch(
 		}
 		q.MetadataURL = a.objectStorage.LocalURL(objectName)
 		nonce := startNonce + uint32(i)
+		origin, err := pw.assignCensus(q)
+		if err != nil {
+			return nil, false, err
+		}
 		tx, err := a.account.BuildNewProcessTx(&account.NewProcessParams{
 			OrgAddress:    pw.vp.OrgAddress,
 			Params:        ep,
-			CensusRoot:    pw.cspPubKey,
+			CensusRoot:    q.CensusRoot,
 			CensusURI:     a.serverURL,
-			Anonymous:     pw.census.Anonymous,
+			CensusOrigin:  origin,
 			MetadataURL:   q.MetadataURL,
 			Nonce:         &nonce,
 			InitialStatus: initialStatus,
@@ -808,6 +823,7 @@ func (pw *publishWorker) confirmBatch(pending []*db.VotingProcessQuestion, resul
 			allConfirmed = false
 			continue
 		}
+		pw.knownElectionIndex++
 		// the election is confirmed on chain: record it in memory first (minedUnpersisted),
 		// then persist. If the DB write keeps failing the question is NOT retried as a new
 		// submission — the next rounds only retry the write — or every DB hiccup after a
diff --git a/api/processes_publish_orphans.go b/api/processes_publish_orphans.go
new file mode 100644
index 0000000..12c8659
--- /dev/null
+++ b/api/processes_publish_orphans.go
@@ -0,0 +1,184 @@
+package api
+
+import (
+	"bytes"
+	"fmt"
+	"time"
+
+	"github.com/vocdoni/saas-backend/account"
+	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/internal"
+	"go.mongodb.org/mongo-driver/v2/bson"
+	"go.vocdoni.io/dvote/log"
+	"go.vocdoni.io/proto/build/go/models"
+)
+
+// orphanElectionWait is how long a publish round that found unattributed organization elections
+// waits before the next round looks for them again.
+const orphanElectionWait = 2 * time.Second
+
+// pendingQuestions returns the questions that have no election yet: neither persisted on chain nor
+// mined with their DB write pending.
+func (pw *publishWorker) pendingQuestions() []*db.VotingProcessQuestion {
+	pending := make([]*db.VotingProcessQuestion, 0, len(pw.questions))
+	for i := range pw.questions {
+		q := &pw.questions[i]
+		if _, mined := pw.minedUnpersisted[q.ID]; mined {
+			continue // already on chain, only the DB write is pending
+		}
+		if len(q.UpstreamID) == 0 {
+			pending = append(pending, q)
+		}
+	}
+	return pending
+}
+
+// assignCensus sets the census origin and root of q's election, as the CSP derives them from the
+// question id and the census mode, and returns the origin.
+func (pw *publishWorker) assignCensus(q *db.VotingProcessQuestion) (models.CensusOrigin, error) {
+	origin, root, err := pw.a.csp.ElectionCensus(q.ID, pw.census.Anonymous)
+	if err != nil {
+		return 0, fmt.Errorf("could not derive the census of question %s: %w", q.ID.Hex(), err)
+	}
+	q.CensusOrigin = electionkey.OriginName(origin)
+	q.CensusRoot = root
+	return origin, nil
+}
+
+// initElectionIndex reads the organization's election index before this worker submits anything.
+// It also adopts the elections an earlier publish of this process left unrecorded: one that mined
+// after that publish gave up is among the organization's last len(questions) elections.
+func (pw *publishWorker) initElectionIndex() error {
+	index, err := pw.a.account.AccountElectionIndex(pw.vp.OrgAddress)
+	if err != nil {
+		return fmt.Errorf("could not read organization election index: %w", err)
+	}
+	pw.knownElectionIndex = index
+	pw.electionIndexRead = true
+	from := uint32(0)
+	if lookback := uint32(len(pw.questions)); index > lookback {
+		from = index - lookback
+	}
+	if _, err := pw.adoptElections(from, index); err != nil {
+		return fmt.Errorf("could not look for unrecorded elections: %w", err)
+	}
+	return nil
+}
+
+// adoptOrphanElections records the elections that mined without this worker seeing them confirmed
+// (a lost batch response, or a tx that mined after its confirmation timed out). It reports false
+// while the organization has created more elections than the worker can attribute, for example
+// because the node does not serve a new election yet: submitting then could create a second
+// election for a question that already has one.
+func (pw *publishWorker) adoptOrphanElections() (bool, error) {
+	if !pw.electionIndexRead {
+		return true, nil // nothing was submitted before the index was read
+	}
+	index, err := pw.a.account.AccountElectionIndex(pw.vp.OrgAddress)
+	if err != nil {
+		return false, fmt.Errorf("could not read organization election index: %w", err)
+	}
+	if index <= pw.knownElectionIndex {
+		return true, nil
+	}
+	adopted, err := pw.adoptElections(pw.knownElectionIndex, index)
+	if err != nil {
+		return false, err
+	}
+	pw.knownElectionIndex += uint32(adopted)
+	return pw.knownElectionIndex >= index, nil
+}
+
+// adoptElections looks for the elections of the pending questions among the organization's
+// elections with an index in [from, to), and records each one found for its question. It returns
+// how many it adopted.
+func (pw *publishWorker) adoptElections(from, to uint32) (int, error) {
+	pending := pw.pendingQuestions()
+	if len(pending) == 0 || from >= to {
+		return 0, nil
+	}
+	candidates := make([]orphanCandidate, 0, len(pending))
+	for _, q := range pending {
+		origin, err := pw.assignCensus(q)
+		if err != nil {
+			return 0, err
+		}
+		ep, err := electionParamsForQuestion(pw.vp, q, pw.census)
+		if err != nil {
+			return 0, err
+		}
+		ids := make([]internal.HexBytes, 0, to-from)
+		for index := from; index < to; index++ {
+			id, err := pw.a.account.ElectionID(&account.ElectionIDParams{
+				OrgAddress: pw.vp.OrgAddress, CensusOrigin: origin, Params: ep, Index: index,
+			})
+			if err != nil {
+				return 0, err
+			}
+			ids = append(ids, id)
+		}
+		candidates = append(candidates, orphanCandidate{question: q.ID, root: q.CensusRoot, ids: ids})
+	}
+	adopted, err := matchOrphanElections(candidates, pw.a.account.ElectionCensusRoot)
+	if err != nil {
+		return 0, err
+	}
+	for _, q := range pending {
+		if id, ok := adopted[q.ID]; ok {
+			log.Warnw("recording an election that mined unseen", "processId", pw.vp.ID.Hex(),
+				"questionId", q.ID.Hex(), "electionId", id.String())
+			pw.persistPublished(q, id)
+		}
+	}
+	return len(adopted), nil
+}
+
+// orphanCandidate is a pending question, the census root of its election and the ids its election
+// would have at each organization election index under consideration.
+type orphanCandidate struct {
+	question bson.ObjectID
+	root     internal.HexBytes
+	ids      []internal.HexBytes
+}
+
+// electionRootLookup returns the census root of an on-chain election; found is false when the
+// node does not know the election.
+type electionRootLookup func(id internal.HexBytes) (root internal.HexBytes, found bool, err error)
+
+// matchOrphanElections returns the id of each candidate's existing election. An election belongs
+// to a candidate only when its census root is the candidate's: a per-election root is derived from
+// the question id, so no other question's election has it. Each id is looked up once and given to
+// at most one candidate.
+func matchOrphanElections(
+	candidates []orphanCandidate, lookup electionRootLookup,
+) (map[bson.ObjectID]internal.HexBytes, error) {
+	roots := make(map[string]internal.HexBytes)
+	looked := make(map[string]bool)
+	taken := make(map[string]bool)
+	adopted := make(map[bson.ObjectID]internal.HexBytes)
+	for _, cand := range candidates {
+		if len(cand.root) == 0 {
+			continue
+		}
+		for _, id := range cand.ids {
+			key := string(id)
+			if !looked[key] {
+				root, found, err := lookup(id)
+				if err != nil {
+					return nil, err
+				}
+				looked[key] = true
+				if found {
+					roots[key] = root
+				}
+			}
+			if root, ok := roots[key]; ok && !taken[key] && bytes.Equal(root, cand.root) {
+				adopted[cand.question] = id
+				taken[key] = true
+				break
+			}
+		}
+	}
+	return adopted, nil
+}
diff --git a/db/processes_questions.go b/db/processes_questions.go
index 4c67654..27b3038 100644
--- a/db/processes_questions.go
+++ b/db/processes_questions.go
@@ -216,18 +216,38 @@ func (ms *MongoStorage) ServedUpstreamIDs(upstreamIDs []internal.HexBytes) (map[
 	return served, nil
 }
 
+// QuestionPublishedParams is the on-chain outcome of publishing one question.
+type QuestionPublishedParams struct {
+	ID          bson.ObjectID
+	UpstreamID  internal.HexBytes
+	MetadataURL string
+	Status      string
+	// CensusOrigin is the census origin name the election was created with when it uses a
+	// per-election CSP key (OFF_CHAIN_CA_V3_ECDSA or OFF_CHAIN_CA_V3_BLIND), and CensusRoot its
+	// census root. Both are empty for an election on a legacy CSP origin, and set together
+	// otherwise.
+	CensusOrigin string
+	CensusRoot   internal.HexBytes
+}
+
 // SetQuestionPublished records the on-chain outcome of a single question in one targeted
-// update, leaving sibling questions untouched.
-func (ms *MongoStorage) SetQuestionPublished(
-	id bson.ObjectID, upstreamID internal.HexBytes, metadataURL, status string,
-) error {
-	if id == bson.NilObjectID || len(upstreamID) == 0 {
+// update, leaving sibling questions untouched. The census origin and root are written only
+// when set, so a legacy question keeps an empty origin and is signed with the legacy scheme.
+func (ms *MongoStorage) SetQuestionPublished(p QuestionPublishedParams) error {
+	if p.ID == bson.NilObjectID || len(p.UpstreamID) == 0 {
+		return ErrInvalidData
+	}
+	if (p.CensusOrigin == "") != (len(p.CensusRoot) == 0) {
 		return ErrInvalidData
 	}
 	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
 	defer cancel()
-	update := bson.M{"$set": bson.M{"upstreamId": upstreamID, "metadataURL": metadataURL, "status": status}} //nolint:goconst
-	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": id}, update)
+	set := bson.M{"upstreamId": p.UpstreamID, "metadataURL": p.MetadataURL, "status": p.Status} //nolint:goconst
+	if p.CensusOrigin != "" {
+		set["censusOrigin"] = p.CensusOrigin
+		set["censusRoot"] = p.CensusRoot
+	}
+	res, err := ms.processesQuestions.UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": set})
 	if err != nil {
 		return fmt.Errorf("failed to set question published: %w", err)
 	}
diff --git a/go.mod b/go.mod
index 3bba39b..ae65f8b 100644
--- a/go.mod
+++ b/go.mod
@@ -36,7 +36,6 @@ require (
 require (
 	filippo.io/bigmod v0.1.1-0.20260103110540-f8a47775ebe5 // indirect
 	filippo.io/keygen v1.0.0 // indirect
-	git.sr.ht/~sircmpwn/go-bare v0.0.0-20210406120253-ab86bc2846d9 // indirect
 	github.com/ProjectZKM/Ziren/crates/go-runtime/zkvm_runtime v0.0.0-20251001021608-1fe7b43fc4d6 // indirect
 	github.com/RaduBerinde/axisds v0.1.0 // indirect
 	github.com/RaduBerinde/btreemap v0.0.0-20250419174037-3d62b7205d54 // indirect
diff --git a/go.sum b/go.sum
index 1f13545..1c890c7 100644
--- a/go.sum
+++ b/go.sum
@@ -45,9 +45,6 @@ filippo.io/bigmod v0.1.1-0.20260103110540-f8a47775ebe5 h1:JA0fFr+kxpqTdxR9LOBiTW
 filippo.io/bigmod v0.1.1-0.20260103110540-f8a47775ebe5/go.mod h1:OjOXDNlClLblvXdwgFFOQFJEocLhhtai8vGLy0JCZlI=
 filippo.io/keygen v1.0.0 h1:u0/Fhxlgz3uPv+XxhfgTq3BJt5VesIPM5ue/OuG7qjQ=
 filippo.io/keygen v1.0.0/go.mod h1:9nnw1SlYHYuPSo/3wjQzNjSbeHlq2NsKo5iEtfJPWP0=
-git.sr.ht/~sircmpwn/getopt v0.0.0-20191230200459-23622cc906b3/go.mod h1:wMEGFFFNuPos7vHmWXfszqImLppbc0wEhh6JBfJIUgw=
-git.sr.ht/~sircmpwn/go-bare v0.0.0-20210406120253-ab86bc2846d9 h1:Ahny8Ud1LjVMMAlt8utUFKhhxJtwBAualvsbc/Sk7cE=
-git.sr.ht/~sircmpwn/go-bare v0.0.0-20210406120253-ab86bc2846d9/go.mod h1:BVJwbDfVjCjoFiKrhkei6NdGcZYpkDkdyCdg1ukytRA=
 github.com/766b/chi-prometheus v0.0.0-20211217152057-87afa9aa2ca8 h1:hK1G69lDhhrGqJbRA5i1rmT2KI/W77MSdr7hEGHqWdQ=
 github.com/766b/chi-prometheus v0.0.0-20211217152057-87afa9aa2ca8/go.mod h1:X/LhbmoBoRu8TxoGIOIraVNhfz3hhikJoaelrOuhdPY=
 github.com/AdaLogics/go-fuzz-headers v0.0.0-20240806141605-e8a1dd7889d6 h1:He8afgbRMd7mFxO99hRNu+6tazq8nFF9lIwo9JFroBk=
```

```bash
go mod tidy    # drops git.sr.ht/~sircmpwn/go-bare (dvote Vocone no longer linked by tests); proto stays v1.15.14
grep -c vochain-sequencer go.mod     # 0
go test ./db/ ./account/ -count=1
go test ./api/ -run 'MatchOrphanElections|CSPKeyPerElection|AdoptsOrphan|CSPLegacyQuestionsAfterFork|BlindCSP|ProcessVote' -count=1 -timeout 30m
~/go/bin/golangci-lint-v2 run ./... && scripts/check-qt-patterns.sh && make swagger && git diff --exit-code docs/swagger.yaml
```
Step 2b (two fixes the full scratch run found; UNVERIFIED (verified at implementation)). The full
`go test ./...` on scratch with the diff above failed 2 api tests; everything else passed:

1. `TestPublishWorkerStopsWithoutClaim` (api/jobqueue_test.go:171): `publish worker panicked: nil
   pointer dereference`. The diff above calls `pw.initElectionIndex()` (which reads the chain through
   `pw.a.account`) in `run()` before the first `confirmClaim()`, so a worker that lost its claim reads the chain
   before it stops. Fix in `api/processes_publish.go` `run()`: delete the `initElectionIndex` call
   placed after `reloadQuestions`, and inside the round loop, right after the `confirmClaim` block,
   add
   ```go
   if !pw.electionIndexRead {
   	if err := pw.initElectionIndex(); err != nil {
   		pw.abandon()
   		return nil, err
   	}
   }
   ```
   so the claim is checked before any chain read; `adoptOrphanElections` already requires
   `electionIndexRead`.
2. `TestVotingProcessMemos` (api/processes_memos_test.go): results fetch memos through
   `account.ElectionMemos` (account/process.go:~374), which pages `GET /elections/{id}/votes/page/{n}`;
   the sequencer answers 410 `use GET /votes?electionId={id}&page={n}`. Switch the request to
   `votes?electionId=<id>&page=<n>` (same `api.VotesList` body) and run the test. Before merging,
   check that the node software the saas deployment queries serves that path; if it serves only the
   old one, try the old path again on a 410 instead. Report which one you did.

Run: `go test ./api/ -run 'PublishWorkerStopsWithoutClaim|VotingProcessMemos' -count=1`, then the full suite.

Expected pass: TestSetQuestionPublishedCensusOrigin, TestBuildNewProcessTxCensusOrigin,
TestMatchOrphanElections, TestCSPKeyPerElectionPublish, TestPublishAdoptsOrphanElection,
TestCSPLegacyQuestionsAfterFork and the updated blind/vote tests pass (AC5 subset took 158s on
scratch); `TestPublishAdoptsOrphanElection` fails if the adoption guard is disabled (mutation-checked);
lint shows only the 5 base findings; swagger unchanged by this task.

Step 3: `git commit -m "feat(api): publish csp questions as per-election csp elections" -- db/processes_questions.go db/voting_processes_test.go db/census_revocation_test.go account/process.go account/process_test.go account/elections.go api/processes_publish.go api/processes_publish_orphans.go api/processes_publish_orphans_test.go api/processes_admin.go api/processes_publish_state_test.go api/api_test.go api/process_vote_test.go api/processes_blind_csp_test.go api/statussync_test.go api/processes_encryption_keys_test.go api/csp_key_per_election_test.go test/voconed.go test/sequencer/go.mod test/sequencer/go.sum go.mod go.sum`

Interfaces this task provides: `db.QuestionPublishedParams{ID, UpstreamID, MetadataURL, Status, CensusOrigin, CensusRoot}`,
`(*MongoStorage).SetQuestionPublished(QuestionPublishedParams) error`, `account.NewProcessParams.CensusOrigin`,
`(*Account).AccountElectionIndex`, `ElectionCensusRoot`, `ElectionID(*ElectionIDParams)`;
`test.BuildVoconed`, `test.StartVoconed(VoconedConfig{Binary, ChainID, CSPKeyForkHeight})`.

### Task 8: integrator-sdk V3 CSP proofs (api-types, api-voting, react-providers)

Model: opus   Repo: ISDK   Files: packages/{api-types,api-voting,api-voting-zk,react-providers}/package.json,
pnpm-lock.yaml, packages/api-types/src/index.ts, packages/api-voting/src/{csp-proof.ts,vote-transaction.ts,blind-csp.ts,index.ts,
csp-v3.testkit.ts,csp-v3.testkit.test.ts,csp-v3.test.ts,testdata/csp-v3-vectors.json},
packages/react-providers/src/election/{use-election-auth.ts,ElectionProvider.tsx,ElectionProvider.test.tsx},
.changeset/csp-key-per-election.md

Goal: clients build the proof the CSP names (`proofType`), pass the weight certificate into
`ProofCA.weightCertificate`, fall back to today's types when the CSP names none, and refuse
results the chain would reject (AC7). The testkit fakes a V3 CSP anchored on the golden vectors.

Step 1 (failing test): pin the dev proto and add the tests and testkit.

```bash
cd ISDK
for p in api-voting api-voting-zk react-providers; do
  sed -i 's#"@vocdoni/proto": "1.15.14"#"@vocdoni/proto": "file:/home/user/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz"#' packages/$p/package.json
done
pnpm install
mkdir -p packages/api-voting/src/testdata
cp SEQ/vochain/transaction/proofs/cspproof/testdata/csp-v3-vectors.json packages/api-voting/src/testdata/
```

```diff
diff --git a/packages/api-voting/src/csp-v3.test.ts b/packages/api-voting/src/csp-v3.test.ts
new file mode 100644
index 0000000..af23e7d
--- /dev/null
+++ b/packages/api-voting/src/csp-v3.test.ts
@@ -0,0 +1,194 @@
+import { secp256k1 } from '@noble/curves/secp256k1'
+import { keccak_256 } from '@noble/hashes/sha3'
+import { concatBytes, utf8ToBytes } from '@noble/hashes/utils'
+import type { BlindPointRequest, BlindSignRequest } from '@vocdoni/api-types'
+import { CAbundle, ProofCA, ProofCA_Type, SignedTx, Tx } from '@vocdoni/proto/vochain'
+import { describe, expect, it } from 'vitest'
+import { signBlindCspBallots, type BlindCspApiClient } from './blind-csp'
+import { blindMessageFromBundle } from './blind-secp256k1'
+import { deserializeBlindSignature, verify } from './blind-secp256k1.testkit'
+import { cspProofOptions } from './csp-proof'
+import { certMessage, mockV3Csp } from './csp-v3.testkit'
+import { EphemeralSigner } from './ephemeral-signer'
+import { fromHex, toHex } from './hex'
+import { buildVoteTransaction, encodeCaBundle } from './vote-transaction'
+
+const PROCESS_ID = '6be21a5a9dc01036097ea184999095aed31735e7264a19652130030800000001'
+const ELECTION = 'c0ffee0000010000000000000000000000000000000000aa0600000000000001'
+const CHAIN_ID = 'vocdoni-test-1'
+const WEIGHT = '2a'
+
+/** The ProofCA a built vote transaction carries on chain. */
+function proofOnChain(txPayload: string): ProofCA {
+  const tx = Tx.decode(SignedTx.decode(fromHex(txPayload)).tx)
+  if (tx.payload?.$case !== 'vote') throw new Error('not a vote tx')
+  const proof = tx.payload.vote.proof?.payload
+  if (proof?.$case !== 'ca') throw new Error('not a CA proof')
+  return proof.ca
+}
+
+/** Recovers the compressed public key of a 65-byte personal-sign signature over msg. */
+function recoverSigner(msg: Uint8Array, signature: Uint8Array): string {
+  const digest = keccak_256(concatBytes(utf8ToBytes(`\x19Ethereum Signed Message:\n${msg.length}`), msg))
+  const sig = secp256k1.Signature.fromCompact(signature.subarray(0, 64)).addRecoveryBit(signature[64])
+  return toHex(sig.recoverPublicKey(digest).toRawBytes(true))
+}
+
+/** A blind CSP whose elections are all OFF_CHAIN_CA_V3_BLIND, signing with mockV3Csp. */
+const v3BlindClient: BlindCspApiClient = {
+  elections: {
+    blindPoint: async (_processId: string, body: BlindPointRequest) => ({
+      points: body.electionIds.map((upstreamId) => ({
+        upstreamId,
+        tokenR: mockV3Csp.point(upstreamId),
+        weight: WEIGHT,
+      })),
+    }),
+    blindSign: async (_processId: string, body: BlindSignRequest) => ({
+      signatures: body.ballots.map(({ upstreamId, blindedMessage }) => ({
+        upstreamId,
+        weight: WEIGHT,
+        proofType: 'ECDSA_BLIND' as const,
+        ...mockV3Csp.blindSign(upstreamId, blindedMessage, WEIGHT),
+      })),
+    }),
+  },
+}
+
+describe('cspProofOptions', () => {
+  it('falls back to the salted type when the CSP names none', () => {
+    expect(cspProofOptions({}, ProofCA_Type.ECDSA_BLIND_PIDSALTED)).toEqual({
+      proofType: ProofCA_Type.ECDSA_BLIND_PIDSALTED,
+    })
+    expect(cspProofOptions({}, ProofCA_Type.ECDSA_PIDSALTED)).toEqual({ proofType: ProofCA_Type.ECDSA_PIDSALTED })
+  })
+
+  it('uses the type the CSP names over the fallback', () => {
+    expect(cspProofOptions({ proofType: 'ECDSA' }, ProofCA_Type.ECDSA_PIDSALTED)).toEqual({
+      proofType: ProofCA_Type.ECDSA,
+    })
+    expect(cspProofOptions({ proofType: 'ECDSA_PIDSALTED' }, ProofCA_Type.ECDSA_BLIND_PIDSALTED)).toEqual({
+      proofType: ProofCA_Type.ECDSA_PIDSALTED,
+    })
+    expect(
+      cspProofOptions({ proofType: 'ECDSA_BLIND', weightKey: '02aa', weightCertificate: 'bb' }, ProofCA_Type.ECDSA_PIDSALTED),
+    ).toEqual({ proofType: ProofCA_Type.ECDSA_BLIND, weightCertificate: { weightKey: '02aa', signature: 'bb' } })
+  })
+
+  it('refuses results that would build a vote the chain rejects', () => {
+    const fallback = ProofCA_Type.ECDSA_PIDSALTED
+    expect(() => cspProofOptions({ proofType: 'ECDSA_BLIND' }, fallback)).toThrow('without its weight certificate')
+    expect(() => cspProofOptions({ proofType: 'ECDSA', weightKey: '02aa', weightCertificate: 'bb' }, fallback)).toThrow(
+      'weight certificate with a ECDSA signature',
+    )
+    expect(() => cspProofOptions({ weightKey: '02aa', weightCertificate: 'bb' }, fallback)).toThrow('without a proof type')
+    expect(() => cspProofOptions({ proofType: 'toString' as never }, fallback)).toThrow('Unknown CSP proof type')
+  })
+})
+
+describe('buildVoteTransaction on per-election CSP keys', () => {
+  it('builds an ECDSA proof that recovers to the election census root', () => {
+    const signer = new EphemeralSigner()
+    const bundle = toHex(encodeCaBundle({ processId: ELECTION, address: signer.address, weight: WEIGHT }))
+    const txPayload = buildVoteTransaction({
+      processId: ELECTION,
+      choices: [0],
+      chainId: CHAIN_ID,
+      signer,
+      cspSignature: mockV3Csp.signBundle(ELECTION, bundle),
+      cspWeight: WEIGHT,
+      ...cspProofOptions({ proofType: 'ECDSA' }, ProofCA_Type.ECDSA_PIDSALTED),
+    })
+
+    const ca = proofOnChain(txPayload)
+    expect(ca.type).toBe(ProofCA_Type.ECDSA)
+    expect(ca.weightCertificate).toBeUndefined()
+    const onChainBundle = new Uint8Array(CAbundle.encode(ca.bundle!).finish())
+    expect(recoverSigner(onChainBundle, new Uint8Array(ca.signature))).toBe(mockV3Csp.ecdsaRoot(ELECTION))
+  })
+
+  it('defaults to ECDSA_BLIND and encodes the certificate when one is given', () => {
+    const txPayload = buildVoteTransaction({
+      processId: ELECTION,
+      choices: [0],
+      chainId: CHAIN_ID,
+      signer: new EphemeralSigner(),
+      cspSignature: 'ab'.repeat(96),
+      cspWeight: WEIGHT,
+      weightCertificate: { weightKey: '02' + '11'.repeat(32), signature: '22'.repeat(65) },
+    })
+    const ca = proofOnChain(txPayload)
+    expect(ca.type).toBe(ProofCA_Type.ECDSA_BLIND)
+    expect(toHex(ca.weightCertificate!.weightKey)).toBe('02' + '11'.repeat(32))
+    expect(toHex(ca.weightCertificate!.signature)).toBe('22'.repeat(65))
+  })
+
+  it('refuses a certificate on any other proof type', () => {
+    expect(() =>
+      buildVoteTransaction({
+        processId: ELECTION,
+        choices: [0],
+        chainId: CHAIN_ID,
+        signer: new EphemeralSigner(),
+        cspSignature: 'ab'.repeat(96),
+        proofType: ProofCA_Type.ECDSA_BLIND_PIDSALTED,
+        weightCertificate: { weightKey: '02' + '11'.repeat(32), signature: '22'.repeat(65) },
+      }),
+    ).toThrow('only valid on an ECDSA_BLIND proof')
+  })
+
+  it('leaves the certificate out of a legacy proof', () => {
+    const txPayload = buildVoteTransaction({
+      processId: ELECTION,
+      choices: [0],
+      chainId: CHAIN_ID,
+      signer: new EphemeralSigner(),
+      cspSignature: 'ab'.repeat(65),
+    })
+    const ca = proofOnChain(txPayload)
+    expect(ca.type).toBe(ProofCA_Type.ECDSA_PIDSALTED)
+    expect(ca.weightCertificate).toBeUndefined()
+  })
+})
+
+describe('signBlindCspBallots on an OFF_CHAIN_CA_V3_BLIND election', () => {
+  it('returns the certificate and builds a proof the chain verifies', async () => {
+    const signer = new EphemeralSigner()
+    const [result] = await signBlindCspBallots({
+      processId: PROCESS_ID,
+      authToken: 'deadbeef',
+      client: v3BlindClient,
+      ballots: [{ upstreamId: ELECTION, address: signer.address }],
+    })
+    expect(result.code).toBeUndefined()
+    expect(result.proofType).toBe('ECDSA_BLIND')
+    expect(fromHex(result.weightKey!).length).toBe(33)
+    expect(fromHex(result.weightCertificate!).length).toBe(65)
+
+    const txPayload = buildVoteTransaction({
+      processId: ELECTION,
+      choices: [0],
+      chainId: CHAIN_ID,
+      signer,
+      cspSignature: result.signature!,
+      cspWeight: result.weight,
+      ...cspProofOptions(result, ProofCA_Type.ECDSA_BLIND_PIDSALTED),
+    })
+
+    // what the chain checks: the blind signature under the weight key, over the bundle on chain...
+    const ca = proofOnChain(txPayload)
+    expect(ca.type).toBe(ProofCA_Type.ECDSA_BLIND)
+    const weightKey = new Uint8Array(ca.weightCertificate!.weightKey)
+    const bundle = new Uint8Array(CAbundle.encode(ca.bundle!).finish())
+    expect(
+      verify(
+        blindMessageFromBundle(bundle),
+        deserializeBlindSignature(new Uint8Array(ca.signature)),
+        secp256k1.ProjectivePoint.fromHex(weightKey),
+      ),
+    ).toBe(true)
+    // ...and the certificate of that key for this election and weight, by the census root
+    const msg = certMessage(fromHex(ELECTION), BigInt(`0x${WEIGHT}`), weightKey)
+    expect(recoverSigner(msg, new Uint8Array(ca.weightCertificate!.signature))).toBe(mockV3Csp.blindRoot(ELECTION))
+  })
+})
diff --git a/packages/api-voting/src/csp-v3.testkit.test.ts b/packages/api-voting/src/csp-v3.testkit.test.ts
new file mode 100644
index 0000000..0d23259
--- /dev/null
+++ b/packages/api-voting/src/csp-v3.testkit.test.ts
@@ -0,0 +1,94 @@
+import { secp256k1 } from '@noble/curves/secp256k1'
+import { keccak_256 } from '@noble/hashes/sha3'
+import { utf8ToBytes } from '@noble/hashes/utils'
+import { describe, expect, it } from 'vitest'
+import { blindMessageFromBundle } from './blind-secp256k1'
+import { deserializeBlindSignature, verify } from './blind-secp256k1.testkit'
+import {
+  certMessage,
+  compressedPubKey,
+  electionBlindKey,
+  electionEcdsaKey,
+  personalSign,
+  weightKey,
+} from './csp-v3.testkit'
+import { fromHex, toHex } from './hex'
+// A byte-identical copy of saas-backend csp/signers/electionkey/testdata/csp-v3-vectors.json.
+import vectorsJson from './testdata/csp-v3-vectors.json'
+
+interface KeyPair {
+  private: string
+  public: string
+}
+
+interface Vectors {
+  root: string
+  elections: Array<{
+    seed: string
+    processIdBlind: string
+    electionEcdsaKey: KeyPair
+    electionBlindKey: KeyPair
+    weights: Array<{ weight: string; weightKey: KeyPair; certMessage: string; certSignature: string }>
+  }>
+  proofs: Array<{
+    name: string
+    censusRoot: string
+    bundle: string
+    proofType: string
+    signature: string
+    weightKey?: string
+  }>
+}
+
+const vectors: Vectors = vectorsJson
+
+const hexScalar = (d: bigint) => d.toString(16).padStart(64, '0')
+
+describe('CSP V3 key derivation (golden vectors)', () => {
+  const root = fromHex(vectors.root)
+
+  for (const election of vectors.elections) {
+    it(`derives the election keys of seed ${election.seed}`, () => {
+      const seed = fromHex(election.seed)
+      const dE = electionEcdsaKey(root, seed)
+      const dB = electionBlindKey(root, seed)
+      expect(hexScalar(dE)).toBe(election.electionEcdsaKey.private)
+      expect(toHex(compressedPubKey(dE))).toBe(election.electionEcdsaKey.public)
+      expect(hexScalar(dB)).toBe(election.electionBlindKey.private)
+      expect(toHex(compressedPubKey(dB))).toBe(election.electionBlindKey.public)
+
+      for (const w of election.weights) {
+        const weight = BigInt(w.weight)
+        const dX = weightKey(dB, weight)
+        expect(hexScalar(dX)).toBe(w.weightKey.private)
+        expect(toHex(compressedPubKey(dX))).toBe(w.weightKey.public)
+        const msg = certMessage(fromHex(election.processIdBlind), weight, compressedPubKey(dX))
+        expect(toHex(msg)).toBe(w.certMessage)
+        expect(toHex(personalSign(dB, msg))).toBe(w.certSignature)
+      }
+    })
+  }
+
+  it('verifies every vector proof signature under its key', () => {
+    for (const proof of vectors.proofs) {
+      const bundle = fromHex(proof.bundle)
+      if (proof.proofType === 'ECDSA') {
+        // a personal-sign signature over the bundle, recovering to the census root
+        const sig = fromHex(proof.signature)
+        const prefix = utf8ToBytes(`\x19Ethereum Signed Message:\n${bundle.length}`)
+        const recovered = secp256k1.Signature.fromCompact(sig.subarray(0, 64))
+          .addRecoveryBit(sig[64])
+          .recoverPublicKey(keccak_256(new Uint8Array([...prefix, ...bundle])))
+        expect(toHex(recovered.toRawBytes(true)), proof.name).toBe(proof.censusRoot)
+      } else {
+        // an unblinded blind signature over the bundle, verifying under the weight key
+        const key = secp256k1.ProjectivePoint.fromHex(proof.weightKey!)
+        expect(
+          verify(blindMessageFromBundle(bundle), deserializeBlindSignature(fromHex(proof.signature)), key),
+          proof.name,
+        ).toBe(true)
+        expect(proof.weightKey).not.toBe(proof.censusRoot)
+      }
+    }
+  })
+})
diff --git a/packages/api-voting/src/csp-v3.testkit.ts b/packages/api-voting/src/csp-v3.testkit.ts
new file mode 100644
index 0000000..47a8980
--- /dev/null
+++ b/packages/api-voting/src/csp-v3.testkit.ts
@@ -0,0 +1,121 @@
+import { mod } from '@noble/curves/abstract/modular'
+import { bytesToNumberBE, concatBytes, numberToBytesBE } from '@noble/curves/abstract/utils'
+import { secp256k1 } from '@noble/curves/secp256k1'
+import { keccak_256 } from '@noble/hashes/sha3'
+import { utf8ToBytes } from '@noble/hashes/utils'
+import { blindSign, compress, G, N } from './blind-secp256k1.testkit'
+import { fromHex, toHex } from './hex'
+
+/**
+ * The CSP's per-election keys of the `OFF_CHAIN_CA_V3_*` census origins, for
+ * tests that fake a V3 CSP. Test-only: not exported from the package index.
+ *
+ * Mirrors saas-backend `csp/signers/electionkey` and is anchored on its golden
+ * vectors (testdata/csp-v3-vectors.json, a byte-identical copy):
+ *
+ *     hashToScalar(tag, data) = first keccak256(tag || u32be(i) || data) in (0, n), i = 0..255
+ *     dE = hashToScalar("vocdoni/csp/v3/election-ecdsa", root || seed)
+ *     dB = hashToScalar("vocdoni/csp/v3/election-blind", root || seed)
+ *     dX = hashToScalar("vocdoni/csp/v3/weight-key", u256be(dB) || u256be(w))
+ *     certificate = personal_sign_dB("vocdoni/csp/v3/weight-cert" || processId || u256be(w) || X)
+ */
+
+export const TAG_ELECTION_ECDSA = 'vocdoni/csp/v3/election-ecdsa'
+export const TAG_ELECTION_BLIND = 'vocdoni/csp/v3/election-blind'
+export const TAG_WEIGHT_KEY = 'vocdoni/csp/v3/weight-key'
+export const CERT_TAG = 'vocdoni/csp/v3/weight-cert'
+
+/** The first `keccak256(tag || u32be(i) || data)` that is a valid secp256k1 private scalar. */
+export function hashToScalar(tag: string, data: Uint8Array): bigint {
+  const counter = new Uint8Array(4)
+  for (let i = 0; i < 256; i++) {
+    new DataView(counter.buffer).setUint32(0, i)
+    const h = bytesToNumberBE(keccak_256(concatBytes(utf8ToBytes(tag), counter, data)))
+    if (h > 0n && h < N) return h
+  }
+  throw new Error(`no valid scalar for tag ${tag}`)
+}
+
+/** dE: the key of an `OFF_CHAIN_CA_V3_ECDSA` election, whose public key is its census root. */
+export const electionEcdsaKey = (root: Uint8Array, seed: Uint8Array): bigint =>
+  hashToScalar(TAG_ELECTION_ECDSA, concatBytes(root, seed))
+
+/** dB: the key of an `OFF_CHAIN_CA_V3_BLIND` election, whose public key is its census root. */
+export const electionBlindKey = (root: Uint8Array, seed: Uint8Array): bigint =>
+  hashToScalar(TAG_ELECTION_BLIND, concatBytes(root, seed))
+
+/** dX: the blind signing key of weight `w` in the blind election of `dB`. */
+export const weightKey = (dB: bigint, w: bigint): bigint =>
+  hashToScalar(TAG_WEIGHT_KEY, concatBytes(numberToBytesBE(dB, 32), numberToBytesBE(w, 32)))
+
+/** The 33-byte compressed public key of a private scalar. */
+export const compressedPubKey = (d: bigint): Uint8Array => secp256k1.getPublicKey(d, true)
+
+/** The curve point of a compressed public key, as the blind verifier takes it. */
+export const publicKeyPoint = (key: Uint8Array) => secp256k1.ProjectivePoint.fromHex(key)
+
+/** The message a weight certificate signs: tag || processId (32) || u256be(w) || weight key (33). */
+export const certMessage = (processId: Uint8Array, w: bigint, weightKeyPub: Uint8Array): Uint8Array =>
+  concatBytes(utf8ToBytes(CERT_TAG), processId, numberToBytesBE(w, 32), weightKeyPub)
+
+/** The 65-byte Ethereum personal-sign signature (`r || s || v`, v in {0, 1}) of `msg` by `d`. */
+export function personalSign(d: bigint, msg: Uint8Array): Uint8Array {
+  const prefix = utf8ToBytes(`\x19Ethereum Signed Message:\n${msg.length}`)
+  const sig = secp256k1.sign(keccak_256(concatBytes(prefix, msg)), d)
+  return concatBytes(sig.toCompactRawBytes(), Uint8Array.of(sig.recovery))
+}
+
+/** The weight key of weight `w` in election `processId` and its certificate by `dB`, all hex. */
+export function weightCredential(dB: bigint, processId: string, w: bigint) {
+  const dX = weightKey(dB, w)
+  const weightKeyPub = compressedPubKey(dX)
+  const certificate = personalSign(dB, certMessage(fromHex(processId), w, weightKeyPub))
+  return { dX, weightKey: toHex(weightKeyPub), weightCertificate: toHex(certificate) }
+}
+
+/** A constant in a mock, not a secret. */
+const MOCK_ROOT = fromHex('5b3c1e8f2a4d6c7b9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e6f')
+
+/** Seed of an election in the mock: the first 12 bytes of its id stand in for the question id. */
+const mockSeed = (electionId: string): Uint8Array => fromHex(electionId).subarray(0, 12)
+
+/** This election's one-time nonce; top bit forced so it stays 32 bytes wide. */
+function mockNonce(electionId: string): bigint {
+  const digest = keccak_256(utf8ToBytes(`mock-csp-v3-nonce:${electionId}`))
+  digest[0] |= 0x80
+  return mod(bytesToNumberBE(digest), N)
+}
+
+/**
+ * A V3 CSP with a fixed root, for mocks: hex in, hex out, everything derived
+ * from the election id. Like the real CSP, the ECDSA key signs bundles, and the
+ * blind flow signs with the weight key and returns its certificate.
+ */
+export const mockV3Csp = {
+  /** Census root (compressed, hex) of the election as an `OFF_CHAIN_CA_V3_ECDSA` election. */
+  ecdsaRoot: (electionId: string): string =>
+    toHex(compressedPubKey(electionEcdsaKey(MOCK_ROOT, mockSeed(electionId)))),
+
+  /** Census root (compressed, hex) of the election as an `OFF_CHAIN_CA_V3_BLIND` election. */
+  blindRoot: (electionId: string): string =>
+    toHex(compressedPubKey(electionBlindKey(MOCK_ROOT, mockSeed(electionId)))),
+
+  /** Signs an encoded CA bundle (hex) for an `OFF_CHAIN_CA_V3_ECDSA` election. */
+  signBundle: (electionId: string, bundle: string): string =>
+    toHex(personalSign(electionEcdsaKey(MOCK_ROOT, mockSeed(electionId)), fromHex(bundle))),
+
+  /** Compressed blind point R (hex) for an election. */
+  point: (electionId: string): string => toHex(compress(G.multiply(mockNonce(electionId)))),
+
+  /** Round 2 of an `OFF_CHAIN_CA_V3_BLIND` election: blind-signs with the weight key of `weight` (hex). */
+  blindSign: (electionId: string, blindedMessage: string, weight: string) => {
+    const dB = electionBlindKey(MOCK_ROOT, mockSeed(electionId))
+    const credential = weightCredential(dB, electionId, BigInt(`0x${weight}`))
+    const signature = blindSign(BigInt(`0x${blindedMessage}`), credential.dX, mockNonce(electionId))
+    return {
+      signature: toHex(numberToBytesBE(signature, 32)),
+      weightKey: credential.weightKey,
+      weightCertificate: credential.weightCertificate,
+    }
+  },
+}
diff --git a/packages/react-providers/src/election/ElectionProvider.test.tsx b/packages/react-providers/src/election/ElectionProvider.test.tsx
index 48832be..60397db 100644
--- a/packages/react-providers/src/election/ElectionProvider.test.tsx
+++ b/packages/react-providers/src/election/ElectionProvider.test.tsx
@@ -1,7 +1,7 @@
 import { act, renderHook, waitFor } from '@testing-library/react'
 import { http, HttpResponse } from 'msw'
 import { CAbundle, ProofCA_Type, SignedTx, Tx } from '@vocdoni/proto/vochain'
-import { blindMessageFromBundle, fromHex } from '@vocdoni/api-voting'
+import { blindMessageFromBundle, encodeCaBundle, fromHex, toHex } from '@vocdoni/api-voting'
 import type { VotingProcessResponse } from '@vocdoni/api-types'
 import { describe, expect, it } from 'vitest'
 // The chain's side of the blind check — test-only, same helper the api-voting
@@ -10,6 +10,7 @@ import {
   deserializeBlindSignature,
   verify as verifyBlindSignature,
 } from '../../../api-voting/src/blind-secp256k1.testkit'
+import { mockV3Csp, publicKeyPoint } from '../../../api-voting/src/csp-v3.testkit'
 import {
   MOCK_CSP_SIGNATURE,
   MOCK_WEIGHT_HEX,
@@ -288,6 +289,104 @@ describe('ElectionProvider', () => {
     ).toBe(true)
   })
 
+  it('votes an OFF_CHAIN_CA_V3_BLIND election with the proof type and certificate the CSP returns', async () => {
+    server.use(
+      http.get(`http://localhost/processes/:id`, ({ params }) =>
+        HttpResponse.json({
+          ...mockProcess,
+          id: params.id as string,
+          census: { ...mockProcess.census, anonymous: true },
+        }),
+      ),
+      http.post(`http://localhost/processes/:processId/blind-point`, async ({ request }) => {
+        const body = (await request.json()) as { electionIds: string[] }
+        return HttpResponse.json({
+          points: body.electionIds.map((upstreamId) => ({
+            upstreamId,
+            tokenR: mockV3Csp.point(upstreamId),
+            weight: MOCK_WEIGHT_HEX,
+          })),
+        })
+      }),
+      http.post(`http://localhost/processes/:processId/blind-sign`, async ({ request }) => {
+        const body = (await request.json()) as { ballots: { upstreamId: string; blindedMessage: string }[] }
+        return HttpResponse.json({
+          signatures: body.ballots.map(({ upstreamId, blindedMessage }) => ({
+            upstreamId,
+            weight: MOCK_WEIGHT_HEX,
+            proofType: 'ECDSA_BLIND',
+            ...mockV3Csp.blindSign(upstreamId, blindedMessage, MOCK_WEIGHT_HEX),
+          })),
+        })
+      }),
+    )
+    const txPayloads = captureBatchVotes()
+
+    const { result } = renderHook(useVoter, { wrapper })
+    await waitFor(() => expect(result.current.election.election?.census?.anonymous).toBe(true))
+    await connect(result)
+    await waitFor(() => expect(result.current.election.isAbleToVote).toBe(true))
+
+    await act(async () => {
+      await result.current.election.vote([[0]])
+    })
+
+    expect(txPayloads).toHaveLength(1)
+    const tx = Tx.decode(SignedTx.decode(fromHex(txPayloads[0])).tx)
+    if (tx.payload?.$case !== 'vote') throw new Error('expected a vote payload')
+    const proof = tx.payload.vote.proof?.payload
+    if (proof?.$case !== 'ca') throw new Error('expected a CA proof')
+
+    expect(proof.ca.type).toBe(ProofCA_Type.ECDSA_BLIND)
+    const weightKey = new Uint8Array(proof.ca.weightCertificate!.weightKey)
+    expect(new Uint8Array(proof.ca.weightCertificate!.signature)).toHaveLength(65)
+    const bundle = new Uint8Array(CAbundle.encode(proof.ca.bundle!).finish())
+    expect(
+      verifyBlindSignature(
+        blindMessageFromBundle(bundle),
+        deserializeBlindSignature(new Uint8Array(proof.ca.signature!)),
+        publicKeyPoint(weightKey),
+      ),
+    ).toBe(true)
+  })
+
+  it('votes an OFF_CHAIN_CA_V3_ECDSA election as an ECDSA proof', async () => {
+    server.use(
+      http.post(`http://localhost/processes/:processId/sign-batch`, async ({ request }) => {
+        const body = (await request.json()) as { ballots: { upstreamId: string; address: string }[] }
+        return HttpResponse.json({
+          signatures: body.ballots.map(({ upstreamId, address }) => ({
+            upstreamId,
+            weight: MOCK_WEIGHT_HEX,
+            proofType: 'ECDSA',
+            signature: mockV3Csp.signBundle(
+              upstreamId,
+              toHex(encodeCaBundle({ processId: upstreamId, address, weight: MOCK_WEIGHT_HEX })),
+            ),
+          })),
+        })
+      }),
+    )
+    const txPayloads = captureBatchVotes()
+
+    const { result } = renderHook(useVoter, { wrapper })
+    await waitFor(() => expect(result.current.election.election).not.toBeNull())
+    await connect(result)
+    await waitFor(() => expect(result.current.election.isAbleToVote).toBe(true))
+
+    await act(async () => {
+      await result.current.election.vote([[0]])
+    })
+
+    expect(txPayloads).toHaveLength(1)
+    const tx = Tx.decode(SignedTx.decode(fromHex(txPayloads[0])).tx)
+    if (tx.payload?.$case !== 'vote') throw new Error('expected a vote payload')
+    const proof = tx.payload.vote.proof?.payload
+    if (proof?.$case !== 'ca') throw new Error('expected a CA proof')
+    expect(proof.ca.type).toBe(ProofCA_Type.ECDSA)
+    expect(proof.ca.weightCertificate).toBeUndefined()
+  })
+
   it('resolves per-question results from the results endpoint', async () => {
     const { result } = renderHook(useVoter, { wrapper })
     await waitFor(() => expect(result.current.election.results).not.toBeNull())
```

```bash
pnpm --filter @vocdoni/api-voting test -- csp-v3
```
Expected failure: `csp-v3.testkit.test.ts` passes (3 tests); `csp-v3.test.ts` fails to import
`./csp-proof` and the `weightCertificate` option.

Step 2 (implementation):

```diff
diff --git a/.changeset/csp-key-per-election.md b/.changeset/csp-key-per-election.md
new file mode 100644
index 0000000..742ab78
--- /dev/null
+++ b/.changeset/csp-key-per-election.md
@@ -0,0 +1,7 @@
+---
+'@vocdoni/api-types': minor
+'@vocdoni/api-voting': minor
+'@vocdoni/react-providers': minor
+---
+
+Per-election CSP keys (census origins `OFF_CHAIN_CA_V3_ECDSA` and `OFF_CHAIN_CA_V3_BLIND`). `SignBatchResult` and `AuthResponse` carry the `proofType` the CSP names for each signature, and a blind signature of a V3 blind election also carries `weightKey` and `weightCertificate`. `signBlindCspBallots` returns them, `buildVoteTransaction` takes an optional `weightCertificate` and puts it in `ProofCA.weightCertificate` (the proof type then defaults to `ECDSA_BLIND`), and the new `cspProofOptions(result, fallback)` turns a CSP result into the proof options of a vote. `ElectionProvider` votes with the proof type the CSP returns and falls back to `ECDSA_BLIND_PIDSALTED` for an anonymous census and `ECDSA_PIDSALTED` otherwise when it returns none. `@vocdoni/proto` moves to 1.18.0.
diff --git a/packages/api-types/src/index.ts b/packages/api-types/src/index.ts
index ffff090..992a707 100644
--- a/packages/api-types/src/index.ts
+++ b/packages/api-types/src/index.ts
@@ -929,6 +929,8 @@ export interface AuthResponse {
   signature?: string
   /** Hex-encoded census weight. */
   weight?: string
+  /** Proof type to build with `signature` — present on the sign response. */
+  proofType?: CspProofType
 }
 
 export interface AuthResendRequest {
@@ -1000,6 +1002,18 @@ export type SignFailureCode =
   | 'blind_request_missing'
   | 'invalid_blinded_message'
 
+/**
+ * The `ProofCA.Type` name of the proof a CSP signature is spent as. The CSP
+ * picks it per ballot from the election's census origin:
+ *
+ * - `ECDSA`: an `OFF_CHAIN_CA_V3_ECDSA` election, signed by its own key.
+ * - `ECDSA_BLIND`: an `OFF_CHAIN_CA_V3_BLIND` election; the result also carries
+ *   `weightKey` and `weightCertificate`, which go in `ProofCA.weightCertificate`.
+ * - `ECDSA_PIDSALTED` / `ECDSA_BLIND_PIDSALTED`: an election on the
+ *   `OFF_CHAIN_CA` / `OFF_CHAIN_CA_V2` origins, signed by the salted CSP key.
+ */
+export type CspProofType = 'ECDSA' | 'ECDSA_PIDSALTED' | 'ECDSA_BLIND' | 'ECDSA_BLIND_PIDSALTED'
+
 /** One ballot of a {@link SignBatchRequest}. */
 export interface SignBatchBallot {
   /** Vochain election id of the question (64-hex). */
@@ -1032,6 +1046,18 @@ export interface SignBatchResult {
   signature?: string
   /** Hex-encoded census weight the ballot was signed with, on success. */
   weight?: string
+  /** Proof type to build with `signature`, on success. */
+  proofType?: CspProofType
+  /**
+   * Hex compressed public key (33 bytes) that blind-signed the ballot, on an
+   * `ECDSA_BLIND` success.
+   */
+  weightKey?: string
+  /**
+   * Hex 65-byte certificate of `weightKey` for this election and weight, signed
+   * by the election's census root key, on an `ECDSA_BLIND` success.
+   */
+  weightCertificate?: string
   code?: SignFailureCode
   error?: string
 }
diff --git a/packages/api-voting/src/blind-csp.ts b/packages/api-voting/src/blind-csp.ts
index 2cdf4ad..31fd12d 100644
--- a/packages/api-voting/src/blind-csp.ts
+++ b/packages/api-voting/src/blind-csp.ts
@@ -3,6 +3,7 @@ import type {
   BlindPointResponse,
   BlindSignRequest,
   BlindSignResponse,
+  CspProofType,
   SignFailureCode,
 } from '@vocdoni/api-types'
 import { bytesToNumberBE, numberToBytesBE } from '@noble/curves/abstract/utils'
@@ -90,6 +91,16 @@ export interface BlindCspResult {
   signature?: string
   /** Weight the CSP authorized — feed it back as `cspWeight`, unchanged. */
   weight?: string
+  /**
+   * Proof type the CSP named for the signature, on success: `ECDSA_BLIND` on an
+   * `OFF_CHAIN_CA_V3_BLIND` election, `ECDSA_BLIND_PIDSALTED` on a legacy one,
+   * absent from a CSP that predates per-election keys.
+   */
+  proofType?: CspProofType
+  /** Hex weight key, on an `ECDSA_BLIND` success. */
+  weightKey?: string
+  /** Hex certificate of `weightKey`, on an `ECDSA_BLIND` success. */
+  weightCertificate?: string
   code?: SignFailureCode
   error?: string
 }
@@ -101,9 +112,10 @@ export interface BlindCspResult {
  * Two rounds: the CSP issues a blind point per election, the client blinds the
  * CA bundle it will later put on chain, the CSP signs bytes it cannot read, the
  * client unblinds. What comes back is a `ProofCA` signature to pass to
- * `buildVoteTransaction` with `proofType: ProofCA_Type.ECDSA_BLIND_PIDSALTED`
- * and `cspWeight` set to the returned `weight` — the weight is baked into the
- * key salt, so altering it invalidates the signature.
+ * `buildVoteTransaction` with the options `cspProofOptions(result,
+ * ProofCA_Type.ECDSA_BLIND_PIDSALTED)` returns and `cspWeight` set to the
+ * returned `weight` — the weight picks the signing key, so altering it
+ * invalidates the signature.
  *
  * The blinding factors never leave this function, which is the whole point: the
  * CSP cannot link the authorization it granted to the ballot that appears on
@@ -198,6 +210,9 @@ export async function signBlindCspBallots(opts: SignBlindCspBallotsOptions): Pro
             upstreamId: signed.upstreamId,
             signature: toHex(serializeBlindSignature(unblind(bytesToNumberBE(fromHex(signed.signature)), secret))),
             weight: pointsById.get(signed.upstreamId)?.weight,
+            proofType: signed.proofType,
+            weightKey: signed.weightKey,
+            weightCertificate: signed.weightCertificate,
           }
         : {
             upstreamId: signed.upstreamId,
diff --git a/packages/api-voting/src/csp-proof.ts b/packages/api-voting/src/csp-proof.ts
new file mode 100644
index 0000000..2bc95ab
--- /dev/null
+++ b/packages/api-voting/src/csp-proof.ts
@@ -0,0 +1,67 @@
+import type { CspProofType } from '@vocdoni/api-types'
+import { ProofCA_Type } from '@vocdoni/proto/vochain'
+
+/**
+ * The certificate of the key that blind-signed an `OFF_CHAIN_CA_V3_BLIND`
+ * ballot, as the CSP returns it (hex). It goes in `ProofCA.weightCertificate`.
+ */
+export interface WeightCertificate {
+  /** Compressed public key (33 bytes) that blind-signed the ballot. */
+  weightKey: string
+  /** 65-byte signature of the weight key by the election's census root key. */
+  signature: string
+}
+
+/** What a CSP signature result says about the proof it must be spent as. */
+export interface CspProofFields {
+  proofType?: CspProofType
+  weightKey?: string
+  weightCertificate?: string
+}
+
+/** The proof options of {@link buildVoteTransaction} a CSP result determines. */
+export interface CspProofOptions {
+  proofType: ProofCA_Type
+  weightCertificate?: WeightCertificate
+}
+
+const PROOF_TYPES: Record<CspProofType, ProofCA_Type> = {
+  ECDSA: ProofCA_Type.ECDSA,
+  ECDSA_PIDSALTED: ProofCA_Type.ECDSA_PIDSALTED,
+  ECDSA_BLIND: ProofCA_Type.ECDSA_BLIND,
+  ECDSA_BLIND_PIDSALTED: ProofCA_Type.ECDSA_BLIND_PIDSALTED,
+}
+
+/**
+ * The proof type, and for `ECDSA_BLIND` the weight certificate, to build a
+ * vote with from one CSP signature result.
+ *
+ * The CSP names the proof type of every signature, because it depends on the
+ * census origin of each question's election. A CSP that does not name it signs
+ * only the salted proof types, so `fallback` applies: `ECDSA_BLIND_PIDSALTED`
+ * for an anonymous census, `ECDSA_PIDSALTED` otherwise. An unknown name, or an
+ * `ECDSA_BLIND` result without its certificate, throws: either would build a
+ * vote the chain rejects.
+ */
+export function cspProofOptions(result: CspProofFields, fallback: ProofCA_Type): CspProofOptions {
+  if (result.proofType === undefined) {
+    if (result.weightKey || result.weightCertificate) {
+      throw new Error('The CSP returned a weight certificate without a proof type')
+    }
+    return { proofType: fallback }
+  }
+  if (!Object.hasOwn(PROOF_TYPES, result.proofType)) {
+    throw new Error(`Unknown CSP proof type ${result.proofType}`)
+  }
+  const proofType = PROOF_TYPES[result.proofType]
+  if (proofType !== ProofCA_Type.ECDSA_BLIND) {
+    if (result.weightKey || result.weightCertificate) {
+      throw new Error(`The CSP returned a weight certificate with a ${result.proofType} signature`)
+    }
+    return { proofType }
+  }
+  if (!result.weightKey || !result.weightCertificate) {
+    throw new Error('The CSP returned an ECDSA_BLIND signature without its weight certificate')
+  }
+  return { proofType, weightCertificate: { weightKey: result.weightKey, signature: result.weightCertificate } }
+}
diff --git a/packages/api-voting/src/index.ts b/packages/api-voting/src/index.ts
index 48d2007..e08a38c 100644
--- a/packages/api-voting/src/index.ts
+++ b/packages/api-voting/src/index.ts
@@ -9,6 +9,12 @@ export {
   type BuildVoteTransactionOptions,
   type CaBundleOptions,
 } from './vote-transaction'
+export {
+  cspProofOptions,
+  type CspProofFields,
+  type CspProofOptions,
+  type WeightCertificate,
+} from './csp-proof'
 export {
   signBlindCspBallots,
   type BlindCspApiClient,
diff --git a/packages/api-voting/src/vote-transaction.ts b/packages/api-voting/src/vote-transaction.ts
index d1d8bf2..6d69a7f 100644
--- a/packages/api-voting/src/vote-transaction.ts
+++ b/packages/api-voting/src/vote-transaction.ts
@@ -2,6 +2,7 @@ import type { EncryptionKey } from '@vocdoni/api-types'
 import { CAbundle, Proof, ProofCA, ProofCA_Type, SignedTx, Tx, VoteEnvelope } from '@vocdoni/proto/vochain'
 import { keccak_256 } from '@noble/hashes/sha3'
 import { randomBytes, utf8ToBytes } from '@noble/hashes/utils'
+import type { WeightCertificate } from './csp-proof'
 import { EphemeralSigner } from './ephemeral-signer'
 import { fromHex, strip0x, toHex } from './hex'
 import { buildVotePackage } from './vote-package'
@@ -22,7 +23,8 @@ export interface BuildVoteTransactionOptions {
   /**
    * CSP signature (hex): from the sign endpoints for a plain census, or the
    * unblinded 96-byte blind signature (`signBlindCspBallots`) for an anonymous
-   * one — pair the latter with `proofType: ECDSA_BLIND_PIDSALTED`.
+   * one. Spend it as the proof type the CSP returned with it
+   * (`cspProofOptions`).
    */
   cspSignature: string
   /**
@@ -33,8 +35,17 @@ export interface BuildVoteTransactionOptions {
   cspWeight?: string
   /** Election encryption keys, for secretUntilTheEnd elections. */
   encryptionKeys?: EncryptionKey[]
-  /** CSP proof type. Defaults to ECDSA_PIDSALTED (the SaaS CSP signer). */
+  /**
+   * CSP proof type. Defaults to ECDSA_BLIND when `weightCertificate` is set,
+   * and to ECDSA_PIDSALTED otherwise.
+   */
   proofType?: ProofCA_Type
+  /**
+   * Certificate of the key that blind-signed the ballot, returned by the CSP
+   * with an `ECDSA_BLIND` signature (an `OFF_CHAIN_CA_V3_BLIND` election). Only
+   * valid with that proof type.
+   */
+  weightCertificate?: WeightCertificate
   /**
    * Optional free-text note attached to the vote (e.g. an open "Other"
    * answer). The chain caps it at {@link MAX_MEMO_BYTES} UTF-8 bytes —
@@ -96,10 +107,15 @@ export function buildVoteTransaction(opts: BuildVoteTransactionOptions): string
     cspSignature,
     cspWeight,
     encryptionKeys,
-    proofType = ProofCA_Type.ECDSA_PIDSALTED,
+    weightCertificate,
+    proofType = weightCertificate ? ProofCA_Type.ECDSA_BLIND : ProofCA_Type.ECDSA_PIDSALTED,
     memo,
   } = opts
 
+  if (weightCertificate && proofType !== ProofCA_Type.ECDSA_BLIND) {
+    throw new Error('A weight certificate is only valid on an ECDSA_BLIND proof')
+  }
+
   let memoBytes: Uint8Array | undefined
   if (memo !== undefined) {
     memoBytes = utf8ToBytes(memo)
@@ -118,6 +134,10 @@ export function buildVoteTransaction(opts: BuildVoteTransactionOptions): string
         type: proofType,
         signature: fromHex(cspSignature),
         bundle: buildCaBundle({ processId, address: signer.address, weight: cspWeight }),
+        weightCertificate: weightCertificate && {
+          weightKey: fromHex(weightCertificate.weightKey),
+          signature: fromHex(weightCertificate.signature),
+        },
       }),
     },
   })
diff --git a/packages/react-providers/src/election/ElectionProvider.tsx b/packages/react-providers/src/election/ElectionProvider.tsx
index b4adc27..1f7f7e7 100644
--- a/packages/react-providers/src/election/ElectionProvider.tsx
+++ b/packages/react-providers/src/election/ElectionProvider.tsx
@@ -5,7 +5,14 @@ import type {
   VotingProcessResponse,
   VotingProcessResultsResponse,
 } from '@vocdoni/api-types'
-import { buildVoteTransaction, EphemeralSigner, MAX_MEMO_BYTES, ProofCA_Type } from '@vocdoni/api-voting'
+import {
+  buildVoteTransaction,
+  cspProofOptions,
+  EphemeralSigner,
+  MAX_MEMO_BYTES,
+  ProofCA_Type,
+  type CspProofOptions,
+} from '@vocdoni/api-voting'
 import { useQuery, type UseQueryOptions } from '@tanstack/react-query'
 import {
   createContext,
@@ -449,10 +456,12 @@ export function ElectionProvider({
           address: signers[k].address,
         })),
       )
-      // An anonymous census gets a blind CSP signature, which the chain
-      // verifies against the blind-salted census key rather than as a plain
-      // ECDSA signature over the bundle.
-      const proofType = election.census?.anonymous ? ProofCA_Type.ECDSA_BLIND_PIDSALTED : undefined
+      // The CSP names the proof type of each signature (it depends on the
+      // census origin of each question's election). A CSP that names none signs
+      // the salted types: blind for an anonymous census, ECDSA otherwise.
+      const fallbackProofType = election.census?.anonymous
+        ? ProofCA_Type.ECDSA_BLIND_PIDSALTED
+        : ProofCA_Type.ECDSA_PIDSALTED
 
       // A CSP signature is one-shot: signBatch() has already consumed every
       // authorization it granted. Throwing here would discard the successful
@@ -474,6 +483,16 @@ export function ElectionProvider({
           setVoteStatus((st) => ({ ...st, [question.id]: 'failed' }))
           return
         }
+        // a result whose proof fields are inconsistent cannot become a valid
+        // vote; it fails this question like a refused signature does.
+        let proof: CspProofOptions
+        try {
+          proof = cspProofOptions(result, fallbackProofType)
+        } catch (error) {
+          signFailures.push({ questionId: question.id, error })
+          setVoteStatus((st) => ({ ...st, [question.id]: 'failed' }))
+          return
+        }
         signed.push({
           question,
           i,
@@ -484,7 +503,7 @@ export function ElectionProvider({
             signer: signers[k],
             cspSignature: result.signature,
             cspWeight: result.weight,
-            proofType,
+            ...proof,
             encryptionKeys: question.secretUntilTheEnd ? question.encryptionKeys : undefined,
             memo: memos?.[i],
           }),
diff --git a/packages/react-providers/src/election/use-election-auth.ts b/packages/react-providers/src/election/use-election-auth.ts
index 093e5ec..8efa684 100644
--- a/packages/react-providers/src/election/use-election-auth.ts
+++ b/packages/react-providers/src/election/use-election-auth.ts
@@ -1,5 +1,6 @@
 import type {
   AuthRequest,
+  CspProofType,
   ProcessCheckResponse,
   SignFailureCode,
   VotingProcessResponse,
@@ -23,6 +24,8 @@ export interface ElectionSignResult {
   signature: string
   /** Hex-encoded census weight the CSP signed with. */
   weight?: string
+  /** Proof type the CSP named for the signature (see `cspProofOptions`). */
+  proofType?: CspProofType
 }
 
 /** One question's ballot in a {@link ElectionAuthContextValue.signBatch} call. */
@@ -44,6 +47,14 @@ export interface ElectionSignBatchResult {
   signature?: string
   /** Weight the CSP authorized — pass it back as `cspWeight`, unchanged. */
   weight?: string
+  /**
+   * Proof type the CSP named for the signature, with the weight key and
+   * certificate of an `ECDSA_BLIND` one. `cspProofOptions` turns them into the
+   * `buildVoteTransaction` options.
+   */
+  proofType?: CspProofType
+  weightKey?: string
+  weightCertificate?: string
   code?: SignFailureCode
   error?: string
 }
@@ -90,8 +101,10 @@ export interface ElectionAuthContextValue {
    * the plain batch sign. Either way the results come back in request order,
    * with per-question failures reported inline rather than thrown.
    *
-   * Anonymous results must be spent as `ProofCA_Type.ECDSA_BLIND_PIDSALTED`
-   * with `cspWeight` set to the returned `weight`.
+   * Spend each result as the proof `cspProofOptions(result, fallback)` names,
+   * with `cspWeight` set to the returned `weight`; the fallback is
+   * `ProofCA_Type.ECDSA_BLIND_PIDSALTED` for an anonymous census and
+   * `ProofCA_Type.ECDSA_PIDSALTED` otherwise.
    */
   signBatch(ballots: ElectionSignBatchBallot[]): Promise<ElectionSignBatchResult[]>
   /** Clear all auth/voter state. */
@@ -179,7 +192,7 @@ export function useVoterSession(
       }
       const res = await client.elections.sign(id, { authToken, electionId, payload: address })
       if (!res.signature) throw new Error('Process sign did not return a signature')
-      return { signature: res.signature, weight: res.weight }
+      return { signature: res.signature, weight: res.weight, proofType: res.proofType }
     },
     [client, id, authToken, process],
   )
@@ -221,6 +234,9 @@ export function useVoterSession(
           electionId: b.electionId,
           signature: signed?.signature,
           weight: signed?.weight,
+          proofType: signed?.proofType,
+          weightKey: signed?.weightKey,
+          weightCertificate: signed?.weightCertificate,
           code: signed?.code,
           error: signed?.signature
             ? undefined
```

```bash
pnpm test && pnpm lint && pnpm build
```
Expected pass: 57 test files / 834 tests as measured on scratch (base 55 / 821); the new ones are
3 vector tests, 9 csp-v3 tests and 2 ElectionProvider tests; lint (tsc) and build ok.

Step 3: `git commit -m "feat(api-voting): build per-election csp proofs with weight certificates" -- packages/api-types/src/index.ts packages/api-voting/ packages/api-voting-zk/package.json packages/react-providers/ pnpm-lock.yaml .changeset/csp-key-per-election.md`

Interfaces this task provides: `CspProofType` and `SignBatchResult.{proofType,weightKey,weightCertificate}`,
`AuthResponse.proofType` (api-types); `cspProofOptions(result, fallback): CspProofOptions`,
`WeightCertificate`, `buildVoteTransaction({..., proofType, weightCertificate})`,
`BlindCspResult.{proofType,weightKey,weightCertificate}` (api-voting).

### Task 9: vocdoni-sdk weight certificate and V3 census origins

Model: sonnet   Repo: VSDK   Files: package.json, yarn.lock, src/services/csp.ts, src/api/csp.ts,
src/types/vote/csp.ts, src/services/census.ts, src/client.ts, src/core/vote.ts,
src/types/census/census.ts, src/api/election.ts, src/api/chain/transactions.ts,
test/unit/types/core/vote.test.ts, test/unit/services/csp.test.ts, test/unit/types/census/*.test.ts, CHANGELOG.md

Goal: (AC8) `CspSignature`, `CspVote` and `CspCensusProof` carry an optional
`weightCertificate {weightKey, signature}` that `packageSignedProof` encodes into
`ProofCA.weightCertificate`; with a certificate and no explicit type the type is `ECDSA_BLIND`,
otherwise the default stays `ECDSA_BLIND_PIDSALTED`. Elections read from the chain with origin
`OFF_CHAIN_CA_V3_ECDSA` / `OFF_CHAIN_CA_V3_BLIND` are CSP elections (see deviation 5). Minor release.

All steps of this task are UNVERIFIED (verified at implementation).

Step 0: pin the dev proto: in `package.json` set
`"@vocdoni/proto": "file:/home/user/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz"`, then
`yarn install` (yarn 1.22). Baseline: `yarn test:unit` 15 suites / 102 tests, `yarn lint` ok.

Step 1 (failing tests):
- `test/unit/types/core/vote.test.ts`: add a `describe('cspProofCA')` with three cases on
  `Vote.cspProofCA(electionId, proof)` (exported static, see Step 2):
  1. `{address, signature, weight: 42n, weightCertificate: {weightKey: '02'+'11'.repeat(32), signature: '22'.repeat(65)}}`
     → `type === ProofCA_Type.ECDSA_BLIND`, `toHex(weightCertificate.weightKey)` and `.signature`
     equal the inputs.
  2. same without `weightCertificate` → `type === ProofCA_Type.ECDSA_BLIND_PIDSALTED`,
     `weightCertificate === undefined` (unchanged default).
  3. a certificate with `proof_type: CspProofType.ECDSA_BLIND_PIDSALTED` → throws
     `'A weight certificate is only valid on an ECDSA_BLIND proof'`.
  Also round-trip: `ProofCA.decode(ProofCA.encode(ca).finish()).weightCertificate` equals the input.
- `test/unit/services/csp.test.ts`: `cspSign` passes `weightKey`/`weightCertificate` from the
  mocked sign response into `CspSignature.weightCertificate`; `cspVote(vote, signature)` puts it on
  the `CspVote`; a response without them leaves `weightCertificate` undefined.
- census unit test (existing census type tests file): `Census.censusTypeFromCensusOrigin('OFF_CHAIN_CA_V3_ECDSA')`
  and `('OFF_CHAIN_CA_V3_BLIND', true)` return `CensusType.CSP`; `isCspCensusOrigin` true for both.

Run `yarn test:unit test/unit/types/core/vote.test.ts test/unit/services/csp.test.ts`.
Expected failure: `Vote.cspProofCA is not a function`; `weightCertificate` undefined.

Step 2 (implementation):
- `src/services/csp.ts`: `export type CspWeightCertificate = { weightKey: string; signature: string }` (hex,
  documented: 33-byte compressed weight key and its 65-byte certificate by the election key);
  `CspSignature` and `CspSignatureInput` gain `weightCertificate?: CspWeightCertificate`;
  `cspSign` sets it when the response has both `weightKey` and `weightCertificate` (throws if only
  one is present); `cspVote` passes it to `new CspVote(..., weightCertificate)`.
- `src/api/csp.ts`: `ICspSignResponse` gains `weightKey?: string; weightCertificate?: string; proofType?: string`.
- `src/types/vote/csp.ts`: `CspVote` gains a `weightCertificate?: CspWeightCertificate` property and
  a fifth optional constructor parameter.
- `src/services/census.ts`: `CspCensusProof` gains `weightCertificate?: CspWeightCertificate`.
- `src/client.ts` (~l.966, CSP branch of the vote flow): add `weightCertificate: vote.weightCertificate`
  to `censusProof`.
- `src/core/vote.ts`: extract the CSP branch of `packageSignedProof` (l.124-136) into
  ```ts
  public static cspProofCA(electionId: string, proof: CspCensusProof): ProofCA {
    const type = proof.proof_type
      ? (proof.proof_type as unknown as ProofCA_Type)
      : proof.weightCertificate ? ProofCA_Type.ECDSA_BLIND : ProofCA_Type.ECDSA_BLIND_PIDSALTED;
    if (proof.weightCertificate && type !== ProofCA_Type.ECDSA_BLIND) {
      throw new Error('A weight certificate is only valid on an ECDSA_BLIND proof');
    }
    return ProofCA.fromPartial({
      type,
      signature: new Uint8Array(Buffer.from(strip0x(proof.signature), 'hex')),
      bundle: this.cspCaBundle(electionId, proof.address, proof.weight),
      weightCertificate: proof.weightCertificate && {
        weightKey: new Uint8Array(Buffer.from(strip0x(proof.weightCertificate.weightKey), 'hex')),
        signature: new Uint8Array(Buffer.from(strip0x(proof.weightCertificate.signature), 'hex')),
      },
    });
  }
  ```
  and call it from `packageSignedProof`.
- `src/types/census/census.ts:82` `isCspCensusOrigin`: also true for `CensusTypeEnum.OFF_CHAIN_CA_V3_ECDSA`
  and `OFF_CHAIN_CA_V3_BLIND`; add both to `CensusTypeEnum` (`src/api/election.ts:~147`) and to the
  `CensusOrigin` declare-enum in `src/api/chain/transactions.ts:~243` (`= 5`, `= 6`).
  `Election.censusOriginFromCensusType` (src/core/election.ts:155) is not changed.
- `CHANGELOG.md`: an `## [Unreleased]` section, `### Added`: weight certificate support and the
  V3 origins being read as CSP; bump `package.json` `version` to `0.11.0` only at release time
  (controller).

Run `yarn test:unit && yarn lint && yarn build`.
Expected pass: 15 suites / 102 + the new tests (about 8), lint and build ok.

Step 3: `git commit -m "feat(csp): carry csp weight certificates and read v3 csp census origins" -- package.json yarn.lock src/services/csp.ts src/api/csp.ts src/types/vote/csp.ts src/services/census.ts src/client.ts src/core/vote.ts src/types/census/census.ts src/api/election.ts src/api/chain/transactions.ts test/unit CHANGELOG.md`

Interfaces this task provides: `CspWeightCertificate`, `CspSignature.weightCertificate`,
`CspVote.weightCertificate`, `CspCensusProof.weightCertificate`, `Vote.cspProofCA(electionId, proof)`.

### Task 10: vocdoni-app package bump (draft)

Model: haiku   Repo: APP   Files: package.json, pnpm-lock.yaml

Goal: (AC9) the app builds and tests green on the new `@vocdoni/*` packages; no code change.
UNVERIFIED (verified at implementation).

Step 1 (local check against unpublished packages, not committed): pack the ISDK packages
(`cd ISDK && pnpm build && for p in api-types api-voting react-providers; do (cd packages/$p && pnpm pack --pack-destination $HOME/.cache/csp-epic/pkgs); done`)
and add to APP `package.json` a temporary `"pnpm": {"overrides": {"@vocdoni/proto": "file:$HOME/.cache/csp-epic/pkgs/vocdoni-proto-1.18.0-dev.csp.tgz", "@vocdoni/api-types": "file:...", "@vocdoni/api-voting": "file:...", "@vocdoni/react-providers": "file:..."}}`
(absolute paths); `pnpm install && pnpm lint && pnpm test && pnpm build`. Expected: green. Then
`git checkout -- package.json pnpm-lock.yaml` (the overrides are never committed).

Step 2: after the controller publishes the packages, set `@vocdoni/api-types`, `@vocdoni/api-voting`
and `@vocdoni/react-providers` to the released versions (minor bumps from the changesets) and
`pnpm install && pnpm lint && pnpm test && pnpm build`. Until then the PR stays draft.

Step 3: `git commit -m "chore(deps): bump @vocdoni packages for per-election csp keys" -- package.json pnpm-lock.yaml`

Interfaces this task provides: none.

## Merge and release

Full suites before any PR leaves draft (each implementer reports the counts; the controller re-runs):

- PROTO: `make all` leaves no diff; `go build ./...`.
- SEQ: `go test ./... -count=1` → 35 packages ok (scratch, all tasks applied); `go vet ./...`;
  `golangci-lint run` per repo config.
- SAAS: `go test ./... -count=1 -timeout 60m` → 20 packages ok, 0 FAIL once Task 7 Step 2b is in (scratch before Step 2b: 19 ok, api FAIL on exactly TestPublishWorkerStopsWithoutClaim and TestVotingProcessMemos, api package 1480s); `golangci-lint-v2 run ./...` (only the 5
  base findings), `scripts/check-qt-patterns.sh`, `make swagger` with no diff, `grep -c vochain-sequencer go.mod` = 0,
  `grep 'go.vocdoni.io/proto ' go.mod` = v1.15.14.
- ISDK: `pnpm test` → 57 files / 834 tests; `pnpm lint`; `pnpm build`; `pnpm changeset status` lists the 3 minor bumps.
- VSDK: `yarn test:unit` → 15 suites / 102 + new tests; `yarn lint`; `yarn build`.
- APP: `pnpm lint && pnpm test && pnpm build`.

Merge order: proto → sequencer → saas-backend and the SDKs → vocdoni-app.

Pin swaps (controller):
1. After PROTO is pushed (owner OK): SEQ `replace go.vocdoni.io/proto => go.vocdoni.io/proto <pseudo-version of the pushed commit>` (or drop the replace and `go get go.vocdoni.io/proto@<sha>`); TS repos keep the tarball or use the git commit.
2. After v1.18.0 is tagged and published: SEQ `go get go.vocdoni.io/proto@v1.18.0`, drop the replace, `go mod tidy`; ISDK/VSDK `"@vocdoni/proto": "1.18.0"` and reinstall.
3. SAAS `test/sequencer/go.mod`: replace both local paths with `go get github.com/vocdoni/vochain-sequencer@<epic commit>` and `go.vocdoni.io/proto@v1.18.0`, `go mod tidy`; re-pin to the sequencer release tag once it exists.
4. APP: released `@vocdoni/*` versions (Task 10 Step 2).

Owner OK required before (pushing branches and opening DRAFT PRs is already authorized):
- tagging dvote-protobuf v1.18.0 (Go tag) and publishing `@vocdoni/proto` 1.18.0 to npm;
- publishing `@vocdoni/api-types`, `@vocdoni/api-voting`, `@vocdoni/react-providers` (changesets) and `@vocdoni/sdk` to npm;
- the vochain-sequencer release that sets `CSPKeyFork` for LTS/1.3 (`config/forks.go:31`) to the height expected at the agreed time on 2026-11-02, and the DEV/36 height (`:38`);
- every deploy: sequencer nodes, vocdoni-app, saas-backend.

Flag-day runbook (spec, Compatibility and rollout):
1. Merge and tag dvote-protobuf v1.18.0 (npm + Go).
2. Re-pin consumers to 1.18.0. Merge and release vochain-sequencer with the LTS `CSPKeyFork`
   height; deploy it to all LTS nodes before that height (check every node reports the release).
3. Release vocdoni-sdk and the integrator-sdk packages; deploy vocdoni-app with them (backward
   compatible, can go before the fork).
4. When the LTS chain reaches the fork height: deploy saas-backend. Between the fork and this
   deploy, CSP publication is paused (new legacy processes fail at CheckTx); announce the window.
Rollback: before the fork height, redeploy the previous sequencer or move the height with a new
release; clients need no rollback; once V3 elections exist only roll forward.

Follow-up issue (label `epic-csp-key-per-election`, link #597): remove the legacy CSP signers
(salted ECDSA and V2 blind), the empty-origin path in `csp/election.go`, and `Published.Root` reads,
once no legacy question is open (last legacy LTS elections end 2026-11-07).

Post-release checks:
- LTS height ≥ fork: `GET /chain/info` height, and a test org publishes a CSP election whose
  `census.censusOrigin` is `OFF_CHAIN_CA_V3_ECDSA` (blind: `OFF_CHAIN_CA_V3_BLIND`) with a 33-byte root
  different per question.
- A vote on each V3 election is counted (`voteCount` in `GET /elections/{id}`); a weighted blind vote
  carries the certificate.
- A legacy question published before the fork still signs (`proofType` `ECDSA_PIDSALTED` /
  `ECDSA_BLIND_PIDSALTED`) and its vote is counted.
- A new `OFF_CHAIN_CA`/`V2` NewProcessTx is rejected with `retired for new processes`.
- saas logs: no publish retries stuck on unattributed elections (the 2s orphan wait loop); no key
  material logged.
