package account

import (
	"testing"

	qt "github.com/frankban/quicktest"
	dvoteapi "go.vocdoni.io/dvote/api"
	dvotetypes "go.vocdoni.io/dvote/types"
)

// TestMapBatchResults pins the fail-fast ordering contract of SubmitSignedTxBatch: the node
// groups a batch into submitted / failed / pending, and mapBatchResults must flatten them back
// into per-input order (submitted…, then the failed one, then pending…) so confirmBatch's
// positional results[i] ↔ pending[i] mapping holds.
func TestMapBatchResults(t *testing.T) {
	c := qt.New(t)

	res := &dvoteapi.TransactionBatchResult{
		Submitted: []dvoteapi.TransactionBatchItem{
			{ProcessID: dvotetypes.HexBytes{0x01}, Hash: dvotetypes.HexBytes{0xaa}},
			{ProcessID: dvotetypes.HexBytes{0x02}, Hash: dvotetypes.HexBytes{0xbb}},
		},
		Failed:  []dvoteapi.TransactionBatchItem{{Error: "bad nonce"}},
		Pending: []dvoteapi.TransactionBatchItem{{}, {}},
	}

	out := mapBatchResults(res)

	// 2 submitted + 1 failed + 2 pending, in that exact order
	c.Assert(out, qt.HasLen, 5)
	c.Assert(out[0].Status, qt.Equals, BatchSubmitted)
	c.Assert(out[1].Status, qt.Equals, BatchSubmitted)
	c.Assert(out[2].Status, qt.Equals, BatchFailed)
	c.Assert(out[3].Status, qt.Equals, BatchPending)
	c.Assert(out[4].Status, qt.Equals, BatchPending)

	// submitted items carry the predicted upstream id + hash, in input order
	c.Assert([]byte(out[0].UpstreamID), qt.DeepEquals, []byte{0x01})
	c.Assert([]byte(out[0].Hash), qt.DeepEquals, []byte{0xaa})
	c.Assert([]byte(out[1].UpstreamID), qt.DeepEquals, []byte{0x02})
	c.Assert(out[2].Err, qt.Equals, "bad nonce")

	// all-submitted (happy path) preserves order and length
	allOK := &dvoteapi.TransactionBatchResult{
		Submitted: []dvoteapi.TransactionBatchItem{
			{ProcessID: dvotetypes.HexBytes{0x0a}},
			{ProcessID: dvotetypes.HexBytes{0x0b}},
			{ProcessID: dvotetypes.HexBytes{0x0c}},
		},
	}
	got := mapBatchResults(allOK)
	c.Assert(got, qt.HasLen, 3)
	for i := range got {
		c.Assert(got[i].Status, qt.Equals, BatchSubmitted)
	}
	c.Assert([]byte(got[2].UpstreamID), qt.DeepEquals, []byte{0x0c})
}
