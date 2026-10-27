package electionkey

import (
	"bytes"
	"encoding/binary"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	vocdonicrypto "go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

func testRoot(c *qt.C) *Root {
	root, err := NewRoot(bytes.Repeat([]byte{0x11}, RootSize))
	c.Assert(err, qt.IsNil)
	return root
}

func testSeed(b byte) [SeedSize]byte {
	var s [SeedSize]byte
	s[SeedSize-1] = b
	return s
}

func TestHashToScalarMatchesDefinition(t *testing.T) {
	c := qt.New(t)
	data := []byte("data")
	got, err := HashToScalar(TagElectionECDSA, data)
	c.Assert(err, qt.IsNil)
	var counter [4]byte
	binary.BigEndian.PutUint32(counter[:], 0)
	want := new(big.Int).SetBytes(ethcrypto.Keccak256([]byte(TagElectionECDSA), counter[:], data))
	c.Assert(got.Cmp(want), qt.Equals, 0)
}

func TestNewRootRejectsInvalidKeys(t *testing.T) {
	c := qt.New(t)
	_, err := NewRoot(make([]byte, 31))
	c.Assert(err, qt.ErrorMatches, "root key is 31 bytes, expected 32")
	_, err = NewRoot(make([]byte, RootSize))
	c.Assert(err, qt.ErrorMatches, "root key is not a valid secp256k1 scalar")
	_, err = NewRoot(curveN.FillBytes(make([]byte, RootSize)))
	c.Assert(err, qt.ErrorMatches, "root key is not a valid secp256k1 scalar")
}

func TestElectionKeysAreDistinctAndDeterministic(t *testing.T) {
	c := qt.New(t)
	root := testRoot(c)
	e1, err := root.ElectionECDSAKey(testSeed(1))
	c.Assert(err, qt.IsNil)
	e1again, err := root.ElectionECDSAKey(testSeed(1))
	c.Assert(err, qt.IsNil)
	c.Assert(ethcrypto.FromECDSA(e1), qt.DeepEquals, ethcrypto.FromECDSA(e1again))
	e2, err := root.ElectionECDSAKey(testSeed(2))
	c.Assert(err, qt.IsNil)
	b1, err := root.ElectionBlindKey(testSeed(1))
	c.Assert(err, qt.IsNil)
	c.Assert(ethcrypto.FromECDSA(e1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(e2))
	c.Assert(ethcrypto.FromECDSA(e1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(b1))
	c.Assert(CompressedPubKey(e1), qt.HasLen, PubKeySize)
}

func TestWeightKeyDependsOnWeight(t *testing.T) {
	c := qt.New(t)
	dB, err := testRoot(c).ElectionBlindKey(testSeed(1))
	c.Assert(err, qt.IsNil)
	k1, err := WeightKey(dB, big.NewInt(1))
	c.Assert(err, qt.IsNil)
	k2, err := WeightKey(dB, big.NewInt(2))
	c.Assert(err, qt.IsNil)
	c.Assert(ethcrypto.FromECDSA(k1), qt.Not(qt.DeepEquals), ethcrypto.FromECDSA(k2))
	_, err = WeightKey(dB, big.NewInt(0))
	c.Assert(err, qt.ErrorMatches, "weight must be positive")
	_, err = WeightKey(dB, new(big.Int).Lsh(big.NewInt(1), 256))
	c.Assert(err, qt.ErrorMatches, "weight does not fit in 32 bytes")
	_, err = WeightKey(nil, big.NewInt(1))
	c.Assert(err, qt.ErrorMatches, "nil election blind key")
}

func TestCertMessageLayout(t *testing.T) {
	c := qt.New(t)
	pid := bytes.Repeat([]byte{0xaa}, ProcessIDSize)
	key := bytes.Repeat([]byte{0x02}, PubKeySize)
	msg, err := CertMessage(pid, big.NewInt(42), key)
	c.Assert(err, qt.IsNil)
	c.Assert(msg, qt.HasLen, 123)
	tagEnd := len(CertTag)
	c.Assert(bytes.HasPrefix(msg, []byte(CertTag)), qt.IsTrue)
	c.Assert(msg[tagEnd:tagEnd+ProcessIDSize], qt.DeepEquals, pid)
	weight := msg[tagEnd+ProcessIDSize : tagEnd+ProcessIDSize+WeightSize]
	c.Assert(weight, qt.DeepEquals, big.NewInt(42).FillBytes(make([]byte, WeightSize)))
	c.Assert(msg[CertMessageSize-PubKeySize:], qt.DeepEquals, key)

	_, err = CertMessage(pid[:31], big.NewInt(1), key)
	c.Assert(err, qt.ErrorMatches, "process id is 31 bytes, expected 32")
	_, err = CertMessage(pid, big.NewInt(1), key[:32])
	c.Assert(err, qt.ErrorMatches, "weight key is 32 bytes, expected 33")
}

func TestIssueWeightCredentialRecoversElectionKey(t *testing.T) {
	c := qt.New(t)
	dB, err := testRoot(c).ElectionBlindKey(testSeed(3))
	c.Assert(err, qt.IsNil)
	pid := bytes.Repeat([]byte{0x01}, ProcessIDSize)
	cred, err := IssueWeightCredential(dB, pid, big.NewInt(7))
	c.Assert(err, qt.IsNil)
	c.Assert(cred.Certificate, qt.HasLen, 65)
	msg, err := CertMessage(pid, big.NewInt(7), cred.WeightKey)
	c.Assert(err, qt.IsNil)
	pub, err := vocdonicrypto.PubKeyFromSignature(msg, cred.Certificate)
	c.Assert(err, qt.IsNil)
	c.Assert(pub, qt.DeepEquals, CompressedPubKey(dB))
}

func TestNormalizeWeight(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		in   []byte
		want int64
		err  string
	}{
		{in: nil, want: 1},
		{in: []byte{}, want: 1},
		{in: []byte{0x2a}, want: 42},
		{in: []byte{0x00, 0x01}, want: 1},
		{in: []byte{0x00}, err: "weight is zero"},
		{in: make([]byte, 33), err: "weight is 33 bytes, at most 32 allowed"},
	} {
		w, err := NormalizeWeight(tc.in)
		if tc.err != "" {
			c.Assert(err, qt.ErrorMatches, tc.err)
			continue
		}
		c.Assert(err, qt.IsNil)
		c.Assert(w.Int64(), qt.Equals, tc.want)
	}
}

func TestIsV3Origin(t *testing.T) {
	c := qt.New(t)
	c.Assert(IsV3Origin(OriginECDSAName), qt.IsTrue)
	c.Assert(IsV3Origin(OriginBlindName), qt.IsTrue)
	c.Assert(IsV3Origin("OFF_CHAIN_CA_V2"), qt.IsFalse)
	c.Assert(IsV3Origin(""), qt.IsFalse)
	c.Assert(OriginName(OriginECDSA), qt.Equals, OriginECDSAName)
	c.Assert(OriginName(OriginBlind), qt.Equals, OriginBlindName)
	c.Assert(OriginName(models.CensusOrigin_OFF_CHAIN_CA_V2), qt.Equals, "")
}
