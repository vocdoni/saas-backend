package electionkey

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	blind "github.com/vocdoni/go-blindsecp256k1"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// updateVectors regenerates testdata/csp-v3-vectors.json. The file is the shared reference for
// every implementation of the derivation and of V3 proofs (vochain-sequencer and the
// integrator SDK testkit keep byte-identical copies), so regenerate it only when the scheme
// changes, and copy it to them in the same change.
var updateVectors = flag.Bool("update", false, "regenerate testdata/csp-v3-vectors.json")

const vectorsFile = "testdata/csp-v3-vectors.json"

// Vectors is the schema of testdata/csp-v3-vectors.json. Byte strings are lowercase hex
// without 0x, weights are decimal strings.
type Vectors struct {
	Description string           `json:"description"`
	Root        string           `json:"root"`
	Voter       KeyVector        `json:"voter"`
	Elections   []ElectionVector `json:"elections"`
	Proofs      []ProofVector    `json:"proofs"`
}

// ElectionVector holds the keys derived for one seed, and the weight credentials of its blind
// election.
type ElectionVector struct {
	Seed           string         `json:"seed"`
	ProcessIDECDSA string         `json:"processIdEcdsa"`
	ProcessIDBlind string         `json:"processIdBlind"`
	ElectionECDSA  KeyVector      `json:"electionEcdsaKey"`
	ElectionBlind  KeyVector      `json:"electionBlindKey"`
	Weights        []WeightVector `json:"weights"`
}

// KeyVector is a derived key pair.
type KeyVector struct {
	Private string `json:"private"`
	Public  string `json:"public"`
}

// WeightVector is the weight key and certificate of one weight on the blind election.
type WeightVector struct {
	Weight        string    `json:"weight"`
	WeightKey     KeyVector `json:"weightKey"`
	CertMessage   string    `json:"certMessage"`
	CertSignature string    `json:"certSignature"`
}

// ProofVector is a complete, valid V3 vote proof: the census root and origin of the election,
// the CAbundle fields and their protobuf encoding, and the ProofCA fields. VoteWeight is empty
// when the bundle carries no voteWeight (the chain then uses weight 1).
type ProofVector struct {
	Name          string `json:"name"`
	CensusOrigin  string `json:"censusOrigin"`
	CensusRoot    string `json:"censusRoot"`
	ProcessID     string `json:"processId"`
	Address       string `json:"address"`
	VoteWeight    string `json:"voteWeight"`
	Weight        string `json:"weight"`
	Bundle        string `json:"bundle"`
	ProofType     string `json:"proofType"`
	Signature     string `json:"signature"`
	WeightKey     string `json:"weightKey,omitempty"`
	CertSignature string `json:"certSignature,omitempty"`
}

var (
	vectorRoot  = "8f1d3c5e7a9b0c2d4e6f8091a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5"
	vectorSeeds = []string{"65f0a1b2c3d4e5f60718293a", "65f0a1b2c3d4e5f60718293b"}
	// processIDs follow the on-chain layout: keccak(chainID)[:6] || org (20) || origin || envelope
	// type || nonce (4). Byte 26 is the census origin (5 ECDSA, 6 blind).
	vectorPIDsECDSA = []string{
		"c0ffee0000010000000000000000000000000000000000aa0500000000000001",
		"c0ffee0000010000000000000000000000000000000000aa0500000000000002",
	}
	vectorPIDsBlind = []string{
		"c0ffee0000010000000000000000000000000000000000aa0600000000000001",
		"c0ffee0000010000000000000000000000000000000000aa0600000000000002",
	}
	vectorWeights = []string{"1", "42", "340282366920938463463374607431768211455"}
	// vectorVoterKey is the voter of every proof vector; its address goes in the bundles and its
	// public key is the VoterID a verifier checks the bundle address against.
	vectorVoterKey = "4c0883a69102937d6231471b5dbb6204fe5129617082792ae468d01a3f362318"
)

func TestVectors(t *testing.T) {
	c := qt.New(t)
	if *updateVectors {
		v := generateVectors(c)
		data, err := json.MarshalIndent(v, "", "  ")
		c.Assert(err, qt.IsNil)
		c.Assert(os.MkdirAll(filepath.Dir(vectorsFile), 0o755), qt.IsNil)
		c.Assert(os.WriteFile(vectorsFile, append(data, '\n'), 0o600), qt.IsNil)
	}
	data, err := os.ReadFile(vectorsFile)
	c.Assert(err, qt.IsNil)
	var v Vectors
	c.Assert(json.Unmarshal(data, &v), qt.IsNil)
	c.Assert(v.Elections, qt.HasLen, len(vectorSeeds))
	c.Assert(v.Proofs, qt.HasLen, 3)

	root, err := NewRoot(mustHex(c, v.Root))
	c.Assert(err, qt.IsNil)
	for _, ev := range v.Elections {
		var seed [SeedSize]byte
		copy(seed[:], mustHex(c, ev.Seed))
		dE, err := root.ElectionECDSAKey(seed)
		c.Assert(err, qt.IsNil)
		c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(dE)), qt.Equals, ev.ElectionECDSA.Private)
		c.Assert(hex.EncodeToString(CompressedPubKey(dE)), qt.Equals, ev.ElectionECDSA.Public)
		dB, err := root.ElectionBlindKey(seed)
		c.Assert(err, qt.IsNil)
		c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(dB)), qt.Equals, ev.ElectionBlind.Private)
		c.Assert(hex.EncodeToString(CompressedPubKey(dB)), qt.Equals, ev.ElectionBlind.Public)
		for _, wv := range ev.Weights {
			w, ok := new(big.Int).SetString(wv.Weight, 10)
			c.Assert(ok, qt.IsTrue)
			cred, err := IssueWeightCredential(dB, mustHex(c, ev.ProcessIDBlind), w)
			c.Assert(err, qt.IsNil)
			c.Assert(hex.EncodeToString(ethcrypto.FromECDSA(cred.Key)), qt.Equals, wv.WeightKey.Private)
			c.Assert(hex.EncodeToString(cred.WeightKey), qt.Equals, wv.WeightKey.Public)
			msg, err := CertMessage(mustHex(c, ev.ProcessIDBlind), w, cred.WeightKey)
			c.Assert(err, qt.IsNil)
			c.Assert(msg, qt.HasLen, CertMessageSize)
			c.Assert(hex.EncodeToString(msg), qt.Equals, wv.CertMessage)
			c.Assert(hex.EncodeToString(cred.Certificate), qt.Equals, wv.CertSignature)
		}
	}
	voterKey, err := ethcrypto.HexToECDSA(v.Voter.Private)
	c.Assert(err, qt.IsNil)
	c.Assert(hex.EncodeToString(CompressedPubKey(voterKey)), qt.Equals, v.Voter.Public)
	voterAddr := hex.EncodeToString(ethcrypto.PubkeyToAddress(voterKey.PublicKey).Bytes())
	for _, pv := range v.Proofs {
		c.Assert(pv.Address, qt.Equals, voterAddr)
		checkProofVector(c, &pv)
	}
}

// checkProofVector verifies a proof vector the way the Vochain does, using only public data.
func checkProofVector(c *qt.C, pv *ProofVector) {
	bundle := &models.CAbundle{ProcessId: mustHex(c, pv.ProcessID), Address: mustHex(c, pv.Address)}
	if pv.VoteWeight != "" {
		bundle.VoteWeight = mustHex(c, pv.VoteWeight)
	}
	bundleBytes, err := proto.Marshal(bundle)
	c.Assert(err, qt.IsNil)
	c.Assert(hex.EncodeToString(bundleBytes), qt.Equals, pv.Bundle, qt.Commentf(pv.Name))
	w, err := NormalizeWeight(bundle.VoteWeight)
	c.Assert(err, qt.IsNil)
	c.Assert(w.String(), qt.Equals, pv.Weight)
	switch pv.CensusOrigin {
	case OriginECDSAName:
		c.Assert(pv.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
		pub, err := vocdonicrypto.PubKeyFromSignature(bundleBytes, mustHex(c, pv.Signature))
		c.Assert(err, qt.IsNil)
		c.Assert(hex.EncodeToString(pub), qt.Equals, pv.CensusRoot)
	case OriginBlindName:
		c.Assert(pv.ProofType, qt.Equals, models.ProofCA_ECDSA_BLIND.String())
		msg, err := CertMessage(bundle.ProcessId, w, mustHex(c, pv.WeightKey))
		c.Assert(err, qt.IsNil)
		pub, err := vocdonicrypto.PubKeyFromSignature(msg, mustHex(c, pv.CertSignature))
		c.Assert(err, qt.IsNil)
		c.Assert(hex.EncodeToString(pub), qt.Equals, pv.CensusRoot)
		uncompressed, err := vocdonicrypto.DecompressPubKey(mustHex(c, pv.WeightKey))
		c.Assert(err, qt.IsNil)
		blindPub, err := blind.NewPublicKeyFromECDSA(uncompressed)
		c.Assert(err, qt.IsNil)
		sig, err := blind.NewSignatureFromBytesUncompressed(mustHex(c, pv.Signature))
		c.Assert(err, qt.IsNil)
		m := new(big.Int).SetBytes(vocdonicrypto.HashRaw(bundleBytes))
		c.Assert(blind.Verify(m, sig, blindPub), qt.IsTrue, qt.Commentf(pv.Name))
	default:
		c.Fatalf("unknown census origin %q", pv.CensusOrigin)
	}
}

func generateVectors(c *qt.C) *Vectors {
	root, err := NewRoot(mustHex(c, vectorRoot))
	c.Assert(err, qt.IsNil)
	v := &Vectors{
		Description: "CSP per-election keys (OFF_CHAIN_CA_V3_*): derivation from a test root, weight " +
			"certificates and complete valid proofs. Generated by saas-backend " +
			"csp/signers/electionkey (go test -run TestVectors -update); copies must stay byte-identical.",
		Root: vectorRoot,
	}
	voterKey, err := ethcrypto.HexToECDSA(vectorVoterKey)
	c.Assert(err, qt.IsNil)
	v.Voter = keyVector(voterKey, CompressedPubKey(voterKey))
	voterAddr := ethcrypto.PubkeyToAddress(voterKey.PublicKey)
	vectorVoter := hex.EncodeToString(voterAddr.Bytes())
	for i, s := range vectorSeeds {
		var seed [SeedSize]byte
		copy(seed[:], mustHex(c, s))
		dE, err := root.ElectionECDSAKey(seed)
		c.Assert(err, qt.IsNil)
		dB, err := root.ElectionBlindKey(seed)
		c.Assert(err, qt.IsNil)
		ev := ElectionVector{
			Seed:           s,
			ProcessIDECDSA: vectorPIDsECDSA[i],
			ProcessIDBlind: vectorPIDsBlind[i],
			ElectionECDSA:  keyVector(dE, CompressedPubKey(dE)),
			ElectionBlind:  keyVector(dB, CompressedPubKey(dB)),
		}
		for _, ws := range vectorWeights {
			w, _ := new(big.Int).SetString(ws, 10)
			cred, err := IssueWeightCredential(dB, mustHex(c, vectorPIDsBlind[i]), w)
			c.Assert(err, qt.IsNil)
			msg, err := CertMessage(mustHex(c, vectorPIDsBlind[i]), w, cred.WeightKey)
			c.Assert(err, qt.IsNil)
			ev.Weights = append(ev.Weights, WeightVector{
				Weight:        ws,
				WeightKey:     keyVector(cred.Key, cred.WeightKey),
				CertMessage:   hex.EncodeToString(msg),
				CertSignature: hex.EncodeToString(cred.Certificate),
			})
		}
		v.Elections = append(v.Elections, ev)
	}

	var seed [SeedSize]byte
	copy(seed[:], mustHex(c, vectorSeeds[0]))
	dE, err := root.ElectionECDSAKey(seed)
	c.Assert(err, qt.IsNil)
	dB, err := root.ElectionBlindKey(seed)
	c.Assert(err, qt.IsNil)
	voter := voterAddr.Bytes()

	// ECDSA proof, weight 42
	pidE := mustHex(c, vectorPIDsECDSA[0])
	bundle := &models.CAbundle{ProcessId: pidE, Address: voter, VoteWeight: big.NewInt(42).Bytes()}
	bundleBytes, err := proto.Marshal(bundle)
	c.Assert(err, qt.IsNil)
	sig, err := SignEthereum(dE, bundleBytes)
	c.Assert(err, qt.IsNil)
	v.Proofs = append(v.Proofs, ProofVector{
		Name: "ecdsa weight 42", CensusOrigin: OriginECDSAName,
		CensusRoot: hex.EncodeToString(CompressedPubKey(dE)), ProcessID: vectorPIDsECDSA[0],
		Address: vectorVoter, VoteWeight: hex.EncodeToString(bundle.VoteWeight), Weight: "42",
		Bundle: hex.EncodeToString(bundleBytes), ProofType: models.ProofCA_ECDSA.String(),
		Signature: hex.EncodeToString(sig),
	})

	// blind proofs: weight 42, and weight 1 with no voteWeight in the bundle
	pidB := mustHex(c, vectorPIDsBlind[0])
	for _, tc := range []struct {
		name   string
		weight *big.Int
		inline bool
	}{
		{name: "blind weight 42", weight: big.NewInt(42), inline: true},
		{name: "blind default weight", weight: big.NewInt(1), inline: false},
	} {
		bundle := &models.CAbundle{ProcessId: pidB, Address: voter}
		if tc.inline {
			bundle.VoteWeight = tc.weight.Bytes()
		}
		bundleBytes, err := proto.Marshal(bundle)
		c.Assert(err, qt.IsNil)
		cred, err := IssueWeightCredential(dB, pidB, tc.weight)
		c.Assert(err, qt.IsNil)
		sig := blindSignForVector(c, cred, bundleBytes)
		v.Proofs = append(v.Proofs, ProofVector{
			Name: tc.name, CensusOrigin: OriginBlindName,
			CensusRoot: hex.EncodeToString(CompressedPubKey(dB)), ProcessID: vectorPIDsBlind[0],
			Address: vectorVoter, VoteWeight: hex.EncodeToString(bundle.VoteWeight), Weight: tc.weight.String(),
			Bundle: hex.EncodeToString(bundleBytes), ProofType: models.ProofCA_ECDSA_BLIND.String(),
			Signature: hex.EncodeToString(sig), WeightKey: hex.EncodeToString(cred.WeightKey),
			CertSignature: hex.EncodeToString(cred.Certificate),
		})
	}
	return v
}

// blindSignForVector runs the blind protocol with both roles: the CSP signing with dX and the
// voter blinding keccak256(bundle). It returns the unblinded signature in the uncompressed form a
// ProofCA carries.
func blindSignForVector(c *qt.C, cred *WeightCredential, bundleBytes []byte) []byte {
	m := new(big.Int).SetBytes(vocdonicrypto.HashRaw(bundleBytes))
	sk := blind.PrivateKey(*new(big.Int).SetBytes(ethcrypto.FromECDSA(cred.Key)))
	for range 64 {
		k, signerR, err := blind.NewRequestParameters()
		c.Assert(err, qt.IsNil)
		mBlinded, secret, err := blind.Blind(m, signerR)
		if err != nil {
			continue
		}
		sBlind, err := sk.BlindSign(mBlinded, k)
		if err != nil {
			continue
		}
		sig, err := blind.Unblind(sBlind, secret)
		c.Assert(err, qt.IsNil)
		return sig.BytesUncompressed()
	}
	c.Fatalf("blind signature failed")
	return nil
}

func keyVector(key *ecdsa.PrivateKey, pub []byte) KeyVector {
	return KeyVector{Private: hex.EncodeToString(ethcrypto.FromECDSA(key)), Public: hex.EncodeToString(pub)}
}

func mustHex(c *qt.C, s string) []byte {
	b, err := hex.DecodeString(s)
	c.Assert(err, qt.IsNil)
	return b
}
