package chainclient

import (
	"bytes"
	"math"
	"testing"

	ethcommon "github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/vochain"
	"go.vocdoni.io/dvote/vochain/genesis"
	"go.vocdoni.io/dvote/vochain/state"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// The tests in this file compare the inlined helpers against the vocdoni-node originals they
// replace. The originals are imported here only: test imports never reach the service binary.

func TestGenerateNullifierMatchesNode(t *testing.T) {
	c := qt.New(t)
	addrs := []ethcommon.Address{
		{},
		ethcommon.HexToAddress("0x0123456789abcdef0123456789abcdef01234567"),
		ethcommon.HexToAddress("0xffffffffffffffffffffffffffffffffffffffff"),
	}
	pids := [][]byte{
		nil,
		{},
		{0x01},
		bytes.Repeat([]byte{0xab}, 32),
		bytes.Repeat([]byte{0x5c}, 65),
	}
	for _, addr := range addrs {
		for _, pid := range pids {
			c.Assert(GenerateNullifier(addr, pid), qt.DeepEquals, state.GenerateNullifier(addr, pid),
				qt.Commentf("address %s, process %x", addr, pid))
		}
	}
}

func TestGenerateFaucetPackageMatchesNode(t *testing.T) {
	c := qt.New(t)
	for range 3 {
		signer := ethereum.NewSignKeys()
		c.Assert(signer.Generate(), qt.IsNil)
		for _, amount := range []uint64{0, 1, 12345, math.MaxUint64} {
			to := ethcommon.BytesToAddress(bytes.Repeat([]byte{byte(amount)}, 20))
			want, err := vochain.GenerateFaucetPackage(signer, to, amount)
			c.Assert(err, qt.IsNil)

			// the identifier is random on both sides: reuse the node's to compare the rest.
			wantPayload := &models.FaucetPayload{}
			c.Assert(proto.Unmarshal(want.Payload, wantPayload), qt.IsNil)
			c.Assert(wantPayload.Identifier <= math.MaxInt32, qt.IsTrue)

			got, err := faucetPackageWithID(signer, to, amount, wantPayload.Identifier)
			c.Assert(err, qt.IsNil)
			c.Assert(got.Payload, qt.DeepEquals, want.Payload)
			c.Assert(got.Signature, qt.DeepEquals, want.Signature)

			random, err := GenerateFaucetPackage(signer, to, amount)
			c.Assert(err, qt.IsNil)
			gotPayload := &models.FaucetPayload{}
			c.Assert(proto.Unmarshal(random.Payload, gotPayload), qt.IsNil)
			c.Assert(gotPayload.Identifier <= math.MaxInt32, qt.IsTrue)
			c.Assert(gotPayload.To, qt.DeepEquals, to.Bytes())
			c.Assert(gotPayload.Amount, qt.Equals, amount)
		}
	}
}

func TestTxCostNameToTxTypeMatchesNode(t *testing.T) {
	c := qt.New(t)
	c.Assert(txCostNames, qt.HasLen, len(genesis.TxCostNameToTxTypeMap))
	names := []string{"", "unknown", "newProcess", "NEW_PROCESS", "Vote"}
	for name := range genesis.TxCostNameToTxTypeMap {
		names = append(names, name)
	}
	for txType := range models.TxType_name {
		names = append(names, genesis.TxTypeToCostName(models.TxType(txType)))
	}
	for _, name := range names {
		c.Assert(TxCostNameToTxType(name), qt.Equals, genesis.TxCostNameToTxType(name), qt.Commentf("name %q", name))
	}
}
