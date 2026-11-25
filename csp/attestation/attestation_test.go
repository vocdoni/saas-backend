package attestation

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
	qt "github.com/frankban/quicktest"
)

var updateFixture = flag.Bool("update", false, "rewrite testdata/attestation_vectors.json")

// fixturePath is the cross-language fixture: the integrator side (the Decidim gem) signs the same
// vectors and must produce the same digests and signatures byte for byte.
const fixturePath = "testdata/attestation_vectors.json"

// fixtureKey is the integrator key of the fixture: keccak256("vocdoni attestation fixture signer").
func fixtureKey() []byte {
	return ethcrypto.Keccak256([]byte("vocdoni attestation fixture signer"))
}

// fixtureDomain is the domain of the fixture, with the address of an arbitrary CSP key as
// verifyingContract.
func fixtureDomain(c *qt.C) Domain {
	key, err := ethcrypto.ToECDSA(ethcrypto.Keccak256([]byte("vocdoni attestation fixture csp")))
	c.Assert(err, qt.IsNil)
	return Domain{VerifyingContract: ethcrypto.PubkeyToAddress(key.PublicKey)}
}

type fixtureCase struct {
	Name        string `json:"name"`
	ProcessID   string `json:"processId"`
	VoterID     string `json:"voterId"`
	Kind        string `json:"kind,omitempty"`
	Contact     string `json:"contact,omitempty"`
	Salt        string `json:"salt,omitempty"`
	ContactHash string `json:"contactHash"`
	ExpiresAt   uint64 `json:"expiresAt"`
	StructHash  string `json:"structHash"`
	Digest      string `json:"digest"`
	Signature   string `json:"signature"`
	UserID      string `json:"userId"`
}

type fixtureFile struct {
	PrivateKey        string        `json:"privateKey"`
	Signer            string        `json:"signer"`
	VerifyingContract string        `json:"verifyingContract"`
	DomainSeparator   string        `json:"domainSeparator"`
	Cases             []fixtureCase `json:"cases"`
}

// fixtureInputs are the cases of the fixture before hashing and signing.
var fixtureInputs = []struct {
	name, processID, voterID string
	kind                     ContactKind
	contact, salt            string
	expiresAt                uint64
}{
	{
		name: "email", processID: "6740c3a1b2d4e5f601234567",
		voterID: "1f0e2d3c4b5a69788796a5b4c3d2e1f00112233445566778899aabbccddeeff0",
		kind:    ContactEmail, contact: "voter@example.org",
		salt:      "a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebfc0",
		expiresAt: 1893456000,
	},
	{
		name: "phone", processID: "6740c3a1b2d4e5f601234567",
		voterID: "00000000000000000000000000000000000000000000000000000000000000ff",
		kind:    ContactPhone, contact: "+34600123456",
		salt:      "0101010101010101010101010101010101010101010101010101010101010101",
		expiresAt: 1893456001,
	},
	{
		name: "unicode-email", processID: "000000000000000000000001",
		voterID: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		kind:    ContactEmail, contact: "élise@exämple.org",
		salt:      "fefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefefe",
		expiresAt: 4102444800,
	},
	{
		name: "no-2fa", processID: "6740c3a1b2d4e5f601234567",
		voterID:   "5555555555555555555555555555555555555555555555555555555555555555",
		expiresAt: 1893456002,
	},
}

func mustHex(c *qt.C, s string) []byte {
	b, err := hexutil.Decode("0x" + s)
	c.Assert(err, qt.IsNil)
	return b
}

// buildFixture hashes and signs every fixture input with go-ethereum's crypto.Sign (RFC 6979, so
// deterministic), storing the signature canonical (v ∈ {27, 28}).
func buildFixture(c *qt.C) fixtureFile {
	keyBytes := fixtureKey()
	key, err := ethcrypto.ToECDSA(keyBytes)
	c.Assert(err, qt.IsNil)
	domain := fixtureDomain(c)
	out := fixtureFile{
		PrivateKey:        hexutil.Encode(keyBytes),
		Signer:            ethcrypto.PubkeyToAddress(key.PublicKey).Hex(),
		VerifyingContract: domain.VerifyingContract.Hex(),
		DomainSeparator:   domain.Separator().Hex(),
	}
	for _, in := range fixtureInputs {
		var a Attestation
		copy(a.ProcessID[:], mustHex(c, in.processID))
		a.VoterID = common.BytesToHash(mustHex(c, in.voterID))
		a.ExpiresAt = in.expiresAt
		fc := fixtureCase{
			Name: in.name, ProcessID: "0x" + in.processID, VoterID: a.VoterID.Hex(), ExpiresAt: in.expiresAt,
		}
		if in.kind != "" {
			a.ContactHash = ContactHash(mustHex(c, in.salt), in.kind, in.contact)
			fc.Kind, fc.Contact, fc.Salt = string(in.kind), in.contact, "0x"+in.salt
		}
		digest := domain.Digest(&a)
		sig, err := ethcrypto.Sign(digest.Bytes(), key)
		c.Assert(err, qt.IsNil)
		sig[64] += 27
		fc.ContactHash = a.ContactHash.Hex()
		fc.StructHash = a.StructHash().Hex()
		fc.Digest = digest.Hex()
		fc.Signature = hexutil.Encode(sig)
		fc.UserID = hexutil.Encode(UserID(a.ProcessID, a.VoterID))
		out.Cases = append(out.Cases, fc)
	}
	return out
}

// TestFixture keeps testdata/attestation_vectors.json equal to what this package computes. Run with
// -update to rewrite it, then copy it to the integrator's test suite.
func TestFixture(t *testing.T) {
	c := qt.New(t)
	built, err := json.MarshalIndent(buildFixture(c), "", "  ")
	c.Assert(err, qt.IsNil)
	built = append(built, '\n')
	if *updateFixture {
		c.Assert(os.MkdirAll(filepath.Dir(fixturePath), 0o755), qt.IsNil)
		c.Assert(os.WriteFile(fixturePath, built, 0o600), qt.IsNil)
	}
	stored, err := os.ReadFile(fixturePath)
	c.Assert(err, qt.IsNil)
	c.Assert(string(stored), qt.Equals, string(built))
}

// TestDigestMatchesApitypes cross-checks the hand-rolled EIP-712 encoding against go-ethereum's
// generic typed-data implementation, the one integrators' stock libraries agree with.
func TestDigestMatchesApitypes(t *testing.T) {
	c := qt.New(t)
	fixture := buildFixture(c)
	domain := fixtureDomain(c)
	for _, fc := range fixture.Cases {
		c.Run(fc.Name, func(c *qt.C) {
			td := apitypes.TypedData{
				Types: apitypes.Types{
					"EIP712Domain": {
						{Name: "name", Type: "string"},
						{Name: "version", Type: "string"},
						{Name: "verifyingContract", Type: "address"},
					},
					"Attestation": {
						{Name: "processId", Type: "bytes12"},
						{Name: "voterId", Type: "bytes32"},
						{Name: "contactHash", Type: "bytes32"},
						{Name: "expiresAt", Type: "uint64"},
					},
				},
				PrimaryType: "Attestation",
				Domain: apitypes.TypedDataDomain{
					Name: DomainName, Version: DomainVersion, VerifyingContract: domain.VerifyingContract.Hex(),
				},
				Message: apitypes.TypedDataMessage{
					"processId":   fc.ProcessID,
					"voterId":     fc.VoterID,
					"contactHash": fc.ContactHash,
					"expiresAt":   fmt.Sprint(fc.ExpiresAt),
				},
			}
			digest, _, err := apitypes.TypedDataAndHash(td)
			c.Assert(err, qt.IsNil)
			c.Assert(hexutil.Encode(digest), qt.Equals, fc.Digest)
			separator, err := td.HashStruct("EIP712Domain", td.Domain.Map())
			c.Assert(err, qt.IsNil)
			c.Assert(hexutil.Encode(separator), qt.Equals, fixture.DomainSeparator)
		})
	}
}

// fixtureAttestation decodes one fixture case back into an Attestation and its signature.
func fixtureAttestation(fc fixtureCase) (*Attestation, []byte) {
	var a Attestation
	copy(a.ProcessID[:], hexutil.MustDecode(fc.ProcessID))
	a.VoterID = common.HexToHash(fc.VoterID)
	a.ContactHash = common.HexToHash(fc.ContactHash)
	a.ExpiresAt = fc.ExpiresAt
	return &a, hexutil.MustDecode(fc.Signature)
}

func TestVerify(t *testing.T) {
	c := qt.New(t)
	fixture := buildFixture(c)
	domain := fixtureDomain(c)
	signer := common.HexToAddress(fixture.Signer)
	a, sig := fixtureAttestation(fixture.Cases[0])

	c.Run("canonical v accepted and returned", func(c *qt.C) {
		canonical, err := domain.Verify(a, sig, signer)
		c.Assert(err, qt.IsNil)
		c.Assert(canonical, qt.DeepEquals, sig)
	})
	c.Run("v 0/1 accepted, returned as 27/28", func(c *qt.C) {
		raw := bytes.Clone(sig)
		raw[64] -= 27
		canonical, err := domain.Verify(a, raw, signer)
		c.Assert(err, qt.IsNil)
		c.Assert(canonical, qt.DeepEquals, sig)
	})
	c.Run("v outside 0, 1, 27, 28 refused", func(c *qt.C) {
		for _, v := range []byte{2, 26, 29, 30} {
			bad := bytes.Clone(sig)
			bad[64] = v
			_, err := domain.Verify(a, bad, signer)
			c.Assert(err, qt.ErrorIs, ErrInvalid, qt.Commentf("v=%d", v))
		}
	})
	c.Run("high-s twin refused", func(c *qt.C) {
		n := ethcrypto.S256().Params().N
		s := new(big.Int).SetBytes(sig[32:64])
		high := bytes.Clone(sig)
		copy(high[32:64], common.LeftPadBytes(new(big.Int).Sub(n, s).Bytes(), 32))
		high[64] = 27 + (1 - (sig[64] - 27))
		_, err := domain.Verify(a, high, signer)
		c.Assert(err, qt.ErrorIs, ErrInvalid)
	})
	c.Run("wrong length refused", func(c *qt.C) {
		_, err := domain.Verify(a, sig[:64], signer)
		c.Assert(err, qt.ErrorIs, ErrInvalid)
	})
	c.Run("other signer refused", func(c *qt.C) {
		_, err := domain.Verify(a, sig, common.HexToAddress("0x00000000000000000000000000000000000000aa"))
		c.Assert(err, qt.ErrorIs, ErrInvalid)
	})
	c.Run("zero configured signer refused", func(c *qt.C) {
		_, err := domain.Verify(a, sig, common.Address{})
		c.Assert(err, qt.ErrorIs, ErrInvalid)
	})
	c.Run("every signed field is bound", func(c *qt.C) {
		tampered := []func(x *Attestation){
			func(x *Attestation) { x.ProcessID[0] ^= 1 },
			func(x *Attestation) { x.VoterID[31] ^= 1 },
			func(x *Attestation) { x.ContactHash[0] ^= 1 },
			func(x *Attestation) { x.ExpiresAt++ },
		}
		for i, tamper := range tampered {
			x := *a
			tamper(&x)
			_, err := domain.Verify(&x, sig, signer)
			c.Assert(err, qt.ErrorIs, ErrInvalid, qt.Commentf("field %d", i))
		}
	})
	c.Run("another environment's domain refused", func(c *qt.C) {
		other := Domain{VerifyingContract: common.HexToAddress("0x00000000000000000000000000000000000000bb")}
		_, err := other.Verify(a, sig, signer)
		c.Assert(err, qt.ErrorIs, ErrInvalid)
	})
}

func TestCheckExpiry(t *testing.T) {
	c := qt.New(t)
	now := time.Unix(1_800_000_000, 0)
	c.Assert(CheckExpiry(uint64(now.Unix()), now), qt.IsNil)
	c.Assert(CheckExpiry(uint64(now.Unix())-29, now), qt.IsNil)
	c.Assert(CheckExpiry(uint64(now.Unix())-30, now), qt.ErrorIs, ErrExpired)
	c.Assert(CheckExpiry(uint64(now.Add(time.Hour).Unix()), now), qt.IsNil)
	c.Assert(CheckExpiry(uint64(now.Add(time.Hour).Unix())+1, now), qt.ErrorIs, ErrExpired)
	c.Assert(CheckExpiry(0, now), qt.ErrorIs, ErrExpired)
}

func TestCheckCanonical(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		kind    ContactKind
		contact string
		ok      bool
	}{
		{ContactEmail, "voter@example.org", true},
		{ContactEmail, "voter.name+tag@sub.example.org", true},
		{ContactEmail, "Voter@example.org", false},
		{ContactEmail, " voter@example.org", false},
		{ContactEmail, "voter@example.org\n", false},
		{ContactEmail, "élise@example.org", false}, // internal.ValidEmail is ASCII-only
		{ContactEmail, "not-an-email", false},
		{ContactPhone, "+34600123456", true},
		{ContactPhone, "+390612345678", true}, // Italian landline keeps its leading zero
		{ContactPhone, "34600123456", false},
		{ContactPhone, "+34 600 123 456", false},
		{ContactPhone, "+3460012", false},
		{ContactKind("sms"), "+34600123456", false},
	} {
		err := CheckCanonical(tc.kind, tc.contact)
		if tc.ok {
			c.Assert(err, qt.IsNil, qt.Commentf("%s %q", tc.kind, tc.contact))
		} else {
			c.Assert(err, qt.ErrorIs, ErrNonCanonicalContact, qt.Commentf("%s %q", tc.kind, tc.contact))
		}
	}
}

func TestMatchContact(t *testing.T) {
	c := qt.New(t)
	salt := bytes.Repeat([]byte{7}, SaltLen)
	h := ContactHash(salt, ContactEmail, "voter@example.org")
	c.Assert(MatchContact(h, salt, ContactEmail, "voter@example.org"), qt.IsTrue)
	c.Assert(MatchContact(h, salt, ContactPhone, "voter@example.org"), qt.IsFalse)
	c.Assert(MatchContact(h, salt, ContactEmail, "voter2@example.org"), qt.IsFalse)
	c.Assert(MatchContact(h, salt[:31], ContactEmail, "voter@example.org"), qt.IsFalse)
	other := bytes.Repeat([]byte{8}, SaltLen)
	c.Assert(MatchContact(h, other, ContactEmail, "voter@example.org"), qt.IsFalse)
}

func TestUserIDNamespace(t *testing.T) {
	c := qt.New(t)
	var pid [ProcessIDLen]byte
	uid := UserID(pid, common.Hash{})
	c.Assert(uid, qt.HasLen, 32)
	c.Assert(bytes.Equal(uid, UserID(pid, common.Hash{1})), qt.IsFalse)
}
