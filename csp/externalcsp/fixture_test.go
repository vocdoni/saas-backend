package externalcsp_test

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/vochain/state"
	"go.vocdoni.io/dvote/vochain/transaction/proofs/cspproof"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

var update = flag.Bool("update", false, "rewrite testdata/csp-external-v2-ecdsa.json")

const (
	tagElectionECDSA = "vocdoni/csp/v3/election-ecdsa"
	vectorsFile      = "csp-v3-vectors.json"
	fixtureFile      = "csp-external-v2-ecdsa.json"
	// fixtureProcessID is the on-chain election the fixture proof is bound to.
	fixtureProcessID = "c0ffee0000010000000000000000000000000000000000aa0400000000000001"
)

type vectorKey struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

type vectorElection struct {
	Seed             string    `json:"seed"`
	ElectionEcdsaKey vectorKey `json:"electionEcdsaKey"`
}

type vectorProof struct {
	Name       string `json:"name"`
	ProcessID  string `json:"processId"`
	Address    string `json:"address"`
	VoteWeight string `json:"voteWeight"`
	Bundle     string `json:"bundle"`
	Signature  string `json:"signature"`
}

type vectors struct {
	Root      string           `json:"root"`
	Voter     vectorKey        `json:"voter"`
	Elections []vectorElection `json:"elections"`
	Proofs    []vectorProof    `json:"proofs"`
}

// externalV2Fixture is one OFF_CHAIN_CA_V2 ECDSA proof of weight 01 under a per-election key
// derived as the integrator derives it. Integrators reproduce it byte for byte.
type externalV2Fixture struct {
	CensusOrigin    string `json:"censusOrigin"`
	ProofType       string `json:"proofType"`
	Root            string `json:"root"`
	Seed            string `json:"seed"`
	CensusRoot      string `json:"censusRoot"`
	WrongCensusRoot string `json:"wrongCensusRoot"`
	ProcessID       string `json:"processId"`
	Address         string `json:"address"`
	VoteWeight      string `json:"voteWeight"`
	Bundle          string `json:"bundle"`
	Digest          string `json:"digest"`
	Signature       string `json:"signature"`
	Nullifier       string `json:"nullifier"`
}

type ecdsaKey struct {
	key *ecdsa.PrivateKey
	pub []byte // compressed: the census root of the election
}

func readVectors(c *qt.C) *vectors {
	raw, err := os.ReadFile(filepath.Join("testdata", vectorsFile))
	c.Assert(err, qt.IsNil)
	vec := &vectors{}
	c.Assert(json.Unmarshal(raw, vec), qt.IsNil)
	c.Assert(vec.Elections, qt.HasLen, 2)
	c.Assert(vec.Proofs, qt.Not(qt.HasLen), 0)
	return vec
}

func mustHex(c *qt.C, s string) []byte {
	b, err := hex.DecodeString(s)
	c.Assert(err, qt.IsNil)
	return b
}

// hashToScalar mirrors the per-election key derivation the integrator implements.
func hashToScalar(tag string, data []byte) *big.Int {
	n := ethcrypto.S256().Params().N
	var c [4]byte
	for i := range 256 {
		binary.BigEndian.PutUint32(c[:], uint32(i))
		h := new(big.Int).SetBytes(ethcrypto.Keccak256([]byte(tag), c[:], data))
		if h.Sign() > 0 && h.Cmp(n) < 0 {
			return h
		}
	}
	panic("no scalar")
}

// electionKey derives the per-election key of seed under root and checks it against the vector.
func electionKey(c *qt.C, root, seed []byte, wantPrivate, wantPublic string) *ecdsaKey {
	d := math.PaddedBigBytes(hashToScalar(tagElectionECDSA, slices.Concat(root, seed)), 32)
	c.Assert(hex.EncodeToString(d), qt.Equals, wantPrivate)
	key, err := ethcrypto.ToECDSA(d)
	c.Assert(err, qt.IsNil)
	pub := ethcrypto.CompressPubkey(&key.PublicKey)
	c.Assert(hex.EncodeToString(pub), qt.Equals, wantPublic)
	return &ecdsaKey{key: key, pub: pub}
}

func TestExternalCSPV2Fixture(t *testing.T) {
	c := qt.New(t)
	vec := readVectors(c)
	root := mustHex(c, vec.Root)
	seed0 := mustHex(c, vec.Elections[0].Seed)
	seed1 := mustHex(c, vec.Elections[1].Seed)
	c.Assert(root, qt.HasLen, 32)
	c.Assert(seed0, qt.HasLen, 12)

	k0 := electionKey(c, root, seed0, vec.Elections[0].ElectionEcdsaKey.Private, vec.Elections[0].ElectionEcdsaKey.Public)
	k1 := electionKey(c, root, seed1, vec.Elections[1].ElectionEcdsaKey.Private, vec.Elections[1].ElectionEcdsaKey.Public)

	voterPub := mustHex(c, vec.Voter.Public)
	vid := state.NewVoterID(state.VoterIDTypeECDSA, voterPub)
	addr := vid.Address()
	c.Assert(hex.EncodeToString(addr), qt.Equals, vec.Proofs[0].Address)

	// The vectors' weight-42 proof reproduces with the chain's bundle encoding and signer.
	p0 := vec.Proofs[0]
	b42, err := proto.Marshal(&models.CAbundle{
		ProcessId: mustHex(c, p0.ProcessID), Address: addr, VoteWeight: mustHex(c, p0.VoteWeight),
	})
	c.Assert(err, qt.IsNil)
	c.Assert(hex.EncodeToString(b42), qt.Equals, p0.Bundle)
	s42, err := ethcrypto.Sign(ethereum.Hash(b42), k0.key)
	c.Assert(err, qt.IsNil)
	c.Assert(hex.EncodeToString(s42), qt.Equals, p0.Signature)

	pid := mustHex(c, fixtureProcessID)
	bundle := &models.CAbundle{ProcessId: pid, Address: addr, VoteWeight: []byte{1}}
	raw, err := proto.Marshal(bundle)
	c.Assert(err, qt.IsNil)
	c.Assert(raw, qt.HasLen, 59)
	c.Assert(raw, qt.DeepEquals, slices.Concat([]byte{0x0a, 0x20}, pid, []byte{0x12, 0x14}, addr, []byte{0x1a, 0x01, 0x01}))

	// EIP-191 with the computed length: "\n59" for a 59-byte bundle.
	digest := ethereum.Hash(raw)
	c.Assert(digest, qt.DeepEquals, ethcrypto.Keccak256([]byte("\x19Ethereum Signed Message:\n59"), raw))

	sig, err := ethcrypto.Sign(digest, k0.key) // RFC 6979, low-s, v in {0,1}
	c.Assert(err, qt.IsNil)
	c.Assert(sig, qt.HasLen, 65)
	halfN := new(big.Int).Rsh(ethcrypto.S256().Params().N, 1)
	c.Assert(new(big.Int).SetBytes(sig[32:64]).Cmp(halfN) <= 0, qt.IsTrue)
	c.Assert(sig[64] <= 1, qt.IsTrue)

	env := &models.VoteEnvelope{ProcessId: pid, Proof: &models.Proof{Payload: &models.Proof_Ca{Ca: &models.ProofCA{
		Type: models.ProofCA_ECDSA, Bundle: bundle, Signature: bytes.Clone(sig),
	}}}}
	v := &cspproof.ProofVerifierCSP{V2: true}
	ok, w, err := v.Verify(&models.Process{CensusRoot: k0.pub, CensusOrigin: models.CensusOrigin_OFF_CHAIN_CA_V2}, env, vid)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)
	c.Assert(w.Int64(), qt.Equals, int64(1))
	// The other seed's root must not verify.
	ok, _, err = v.Verify(&models.Process{CensusRoot: k1.pub, CensusOrigin: models.CensusOrigin_OFF_CHAIN_CA_V2}, env, vid)
	c.Assert(err, qt.IsNotNil)
	c.Assert(ok, qt.IsFalse)

	nullifier := state.GenerateNullifier(common.BytesToAddress(addr), pid)
	c.Assert(nullifier, qt.DeepEquals, ethcrypto.Keccak256(addr, pid))

	fx := externalV2Fixture{
		CensusOrigin:    models.CensusOrigin_OFF_CHAIN_CA_V2.String(),
		ProofType:       models.ProofCA_ECDSA.String(),
		Root:            vec.Root,
		Seed:            vec.Elections[0].Seed,
		CensusRoot:      hex.EncodeToString(k0.pub),
		WrongCensusRoot: hex.EncodeToString(k1.pub),
		ProcessID:       fixtureProcessID,
		Address:         hex.EncodeToString(addr),
		VoteWeight:      "01",
		Bundle:          hex.EncodeToString(raw),
		Digest:          hex.EncodeToString(digest),
		Signature:       hex.EncodeToString(sig),
		Nullifier:       hex.EncodeToString(nullifier),
	}
	out, err := json.MarshalIndent(fx, "", "  ")
	c.Assert(err, qt.IsNil)
	out = append(out, '\n')

	path := filepath.Join("testdata", fixtureFile)
	if *update {
		c.Assert(os.WriteFile(path, out, 0o644), qt.IsNil)
	}
	committed, err := os.ReadFile(path)
	c.Assert(err, qt.IsNil)
	c.Assert(string(committed), qt.Equals, string(out))
}
