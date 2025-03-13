package ecdsa_salted

import (
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

var (
	validRootKey   = new(internal.HexBytes).SetString("21f5e1eda42d4f140b87de48c96ce66f1bd3e500b72f67620f67d50363adcd2d")
	invalidRootKey = new(internal.HexBytes).SetString("6f06f95001183f40476f7e5cc2ae3d04")
)

func TestInit(t *testing.T) {
	c := qt.New(t)
	signer := SaltedECDSA{}
	c.Assert(signer.Init(validRootKey.Bytes()), qt.IsNil)
	c.Assert(signer.Init(invalidRootKey.Bytes()), qt.ErrorIs, signers.ErrInvalidRootKey)
}

func TestSign(t *testing.T) {
	t.Skip("TODO: fix this test")
	c := qt.New(t)

	signer := SaltedECDSA{}
	c.Assert(signer.Init(validRootKey.Bytes()), qt.IsNil)
	msg := []byte("hello world")
	salt := internal.RandomBytes(32)
	signature, err := signer.Sign(nil, salt, msg)
	c.Assert(err, qt.IsNil)
	c.Assert(signature, qt.HasLen, ethcrypto.SignatureLength)

	// verify the signature
	verificationPubKey, err := ethereum.PubKeyFromSignature(msg, signature)
	c.Assert(err, qt.IsNil)
	originakPubKey, err := signer.PubKey(salt)
	c.Assert(err, qt.IsNil)
	c.Assert(verificationPubKey, qt.DeepEquals, originakPubKey)
}
