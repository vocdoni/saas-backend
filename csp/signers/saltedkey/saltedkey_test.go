package saltedkey

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"testing"

	qt "github.com/frankban/quicktest"
	blind "github.com/vocdoni/go-blindsecp256k1"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

func TestECDSAsaltedKey(t *testing.T) {
	privHex := fmt.Sprintf("%x", randomBytes(32))
	sk, err := NewSaltedKey(privHex)
	qt.Assert(t, err, qt.IsNil)

	salt := [SaltSize]byte{}
	copy(salt[:], randomBytes(20))
	msg := []byte("hello world!")

	signature, err := sk.SignECDSA(salt, msg)
	qt.Assert(t, err, qt.IsNil)

	saltAddr, err := ethereum.AddrFromSignature(msg, signature)
	qt.Assert(t, err, qt.IsNil)

	signingKeys := ethereum.NewSignKeys()
	signingKeys.AddAuthKey(saltAddr)

	ok, _, err := signingKeys.VerifySender(msg, signature)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, ok, qt.IsTrue)
}

func TestBlindsaltedKey(t *testing.T) {
	privHex := fmt.Sprintf("%x", randomBytes(32))
	sk, err := NewSaltedKey(privHex)
	qt.Assert(t, err, qt.IsNil)

	salt := [SaltSize]byte{}
	copy(salt[:], randomBytes(20))
	msgHash := ethereum.HashRaw([]byte("hello world!"))

	// Server: generate a new secretK and R (R is required for blinding and K for signing)
	k, signerR, err := blind.NewRequestParameters()
	qt.Assert(t, err, qt.IsNil)

	// Client: blinds the message with R (from server). Keeps userSecretData for unblinding
	msgBlinded, userSecretData, err := blind.Blind(new(big.Int).SetBytes(msgHash), signerR)
	qt.Assert(t, err, qt.IsNil)

	// Server: performs the signature with the commont salt using secretK
	blindedSignature, err := sk.SignBlind(salt, msgBlinded.Bytes(), k)
	qt.Assert(t, err, qt.IsNil)

	// Client: unblind the signature
	signature, err := blind.Unblind(new(big.Int).SetBytes(blindedSignature), userSecretData)
	qt.Assert(t, err, qt.IsNil)

	// Any: verifies the signature (salting previously the pubKey with the common salt)
	saltedPubKey, err := SaltBlindPubKey(sk.BlindPubKey(), salt)
	qt.Assert(t, err, qt.IsNil)
	valid := blind.Verify(new(big.Int).SetBytes(msgHash), signature, saltedPubKey)
	qt.Assert(t, valid, qt.IsTrue)
}

// TestBlindSaltedKeyV2 exercises the OFF_CHAIN_CA_V2 blind flow end to end and, crucially, that the
// V2 salt binds the vote weight: a signature issued for one weight does not verify against another
// weight's salted key. This mirrors the Vochain verifier (cspproof, ECDSA_BLIND_PIDSALTED), which
// salts the census root with saltedkey.Salt(processId, bundle.VoteWeight) before blind.Verify.
func TestBlindSaltedKeyV2(t *testing.T) {
	privHex := fmt.Sprintf("%x", randomBytes(32))
	sk, err := NewSaltedKey(privHex)
	qt.Assert(t, err, qt.IsNil)

	processID := randomBytes(32)
	weight := big.NewInt(7).Bytes()
	msgHash := ethereum.HashRaw([]byte("blind v2 ballot"))

	// V2 salt binds processID and the authorized weight
	salt, err := V2Salt(processID, weight)
	qt.Assert(t, err, qt.IsNil)

	// full two-round blind protocol with the V2 salt. go-blindsecp256k1's BlindSign rejects a
	// blinded message or nonce whose big-endian encoding is not exactly 32 bytes (~1/256 leading
	// zero), which a real client handles by retrying with a fresh blinding — mirror that here.
	var signature *blind.Signature
	for range 32 {
		k, signerR, err := blind.NewRequestParameters()
		qt.Assert(t, err, qt.IsNil)
		msgBlinded, userSecretData, err := blind.Blind(new(big.Int).SetBytes(msgHash), signerR)
		qt.Assert(t, err, qt.IsNil)
		blindedSignature, err := sk.SignBlind(salt, msgBlinded.Bytes(), k)
		if err != nil {
			continue
		}
		signature, err = blind.Unblind(new(big.Int).SetBytes(blindedSignature), userSecretData)
		qt.Assert(t, err, qt.IsNil)
		break
	}
	qt.Assert(t, signature, qt.Not(qt.IsNil))

	// verifies against the pubkey salted with the SAME processID+weight
	saltedPubKey, err := SaltBlindPubKey(sk.BlindPubKey(), salt)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, blind.Verify(new(big.Int).SetBytes(msgHash), signature, saltedPubKey), qt.IsTrue)

	// a forged weight fails: salting the pubkey with a different weight breaks verification
	otherSalt, err := V2Salt(processID, big.NewInt(8).Bytes())
	qt.Assert(t, err, qt.IsNil)
	forgedPubKey, err := SaltBlindPubKey(sk.BlindPubKey(), otherSalt)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, blind.Verify(new(big.Int).SetBytes(msgHash), signature, forgedPubKey), qt.IsFalse)
}

func randomBytes(n int) []byte {
	bytes := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, bytes); err != nil {
		panic(err)
	}
	return bytes
}
