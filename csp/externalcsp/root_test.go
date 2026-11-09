package externalcsp_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/externalcsp"
)

func TestParseRoot(t *testing.T) {
	c := qt.New(t)
	vec := readVectors(c)
	pub, err := hex.DecodeString(vec.Elections[0].ElectionEcdsaKey.Public)
	c.Assert(err, qt.IsNil)

	got, err := externalcsp.ParseRoot(pub)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, pub)

	// x = 5 has no y on secp256k1: 5³+7 is not a square mod p.
	offCurve := make([]byte, externalcsp.RootSize)
	offCurve[0], offCurve[32] = 0x02, 0x05

	for name, root := range map[string][]byte{
		"empty":       nil,
		"32 bytes":    pub[1:],
		"65 bytes":    append([]byte{0x04}, bytes.Repeat([]byte{0x11}, 64)...),
		"prefix 0x04": append([]byte{0x04}, pub[1:]...),
		"off-curve x": offCurve,
	} {
		_, err := externalcsp.ParseRoot(root)
		c.Assert(err, qt.IsNotNil, qt.Commentf("%s", name))
	}
}
