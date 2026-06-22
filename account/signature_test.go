package account

import (
	"encoding/hex"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// TestVerifySignature ensures that VerifySignature only succeeds when the
// recovered signer address matches the expected address, regardless of the
// address format (0x-prefixed or not, any letter case), and fails otherwise.
func TestVerifySignature(t *testing.T) {
	c := qt.New(t)

	signer := ethereum.NewSignKeys()
	c.Assert(signer.Generate(), qt.IsNil)

	message := "hello vocdoni"
	sigBytes, err := signer.SignEthereum([]byte(message))
	c.Assert(err, qt.IsNil)
	signature := hex.EncodeToString(sigBytes)

	addr := signer.Address().Hex() // 0x-prefixed, checksummed
	addrNoPrefix := strings.TrimPrefix(addr, "0x")
	addrLower := strings.ToLower(addr)

	t.Run("valid signature and address", func(_ *testing.T) {
		c.Assert(VerifySignature(message, signature, addr), qt.IsNil)
	})
	t.Run("valid with non-0x address", func(_ *testing.T) {
		c.Assert(VerifySignature(message, signature, addrNoPrefix), qt.IsNil)
	})
	t.Run("valid with lowercase address", func(_ *testing.T) {
		c.Assert(VerifySignature(message, signature, addrLower), qt.IsNil)
	})
	t.Run("valid with 0x-prefixed signature", func(_ *testing.T) {
		c.Assert(VerifySignature(message, "0x"+signature, addr), qt.IsNil)
	})
	t.Run("wrong expected address is rejected", func(_ *testing.T) {
		other := ethereum.NewSignKeys()
		c.Assert(other.Generate(), qt.IsNil)
		c.Assert(VerifySignature(message, signature, other.Address().Hex()), qt.Not(qt.IsNil))
	})
	t.Run("tampered message is rejected", func(_ *testing.T) {
		c.Assert(VerifySignature("tampered message", signature, addr), qt.Not(qt.IsNil))
	})
	t.Run("malformed signature is rejected", func(_ *testing.T) {
		c.Assert(VerifySignature(message, "not-hex", addr), qt.Not(qt.IsNil))
	})
	t.Run("empty signature is rejected", func(_ *testing.T) {
		c.Assert(VerifySignature(message, "", addr), qt.Not(qt.IsNil))
	})
}
