package chainclient

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
)

// assertWireSubset checks that a node API value decodes into our mirror type and re-encodes to the
// same JSON for every field the mirror keeps: each key of the mirror's encoding must carry the
// node's value. Fields the mirror drops are simply absent.
func assertWireSubset[T any](c *qt.C, node any) {
	c.Helper()
	nodeJSON, err := json.Marshal(node)
	c.Assert(err, qt.IsNil)
	var mirror T
	c.Assert(json.Unmarshal(nodeJSON, &mirror), qt.IsNil)
	mirrorJSON, err := json.Marshal(mirror)
	c.Assert(err, qt.IsNil)

	var want, got map[string]any
	c.Assert(json.Unmarshal(nodeJSON, &want), qt.IsNil)
	c.Assert(json.Unmarshal(mirrorJSON, &got), qt.IsNil)
	c.Assert(got, qt.Not(qt.HasLen), 0)
	for k, v := range got {
		c.Assert(v, qt.DeepEquals, want[k], qt.Commentf("field %q", k))
	}
}

func TestTypesMatchNodeWireFormat(t *testing.T) {
	c := qt.New(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hex := types.HexBytes{0xde, 0xad, 0xbe, 0xef}
	code := uint32(7)
	number := uint32(3)
	txIndex := int32(2)

	assertWireSubset[Election](c, &dvoteapi.Election{
		ElectionSummary: dvoteapi.ElectionSummary{
			ElectionID: hex, OrganizationID: hex, Status: "RESULTS", StartDate: now, EndDate: now,
			VoteCount: 9, FinalResults: true, ManuallyEnded: true, ChainID: "dev", Title: "t",
			Results: [][]*types.BigInt{{(*types.BigInt)(big.NewInt(4)), (*types.BigInt)(big.NewInt(6))}},
		},
		Census: &dvoteapi.ElectionCensus{
			CensusOrigin: "OFF_CHAIN_CA", CensusRoot: hex, CensusURL: "https://x", MaxCensusSize: 13,
		},
		MetadataURL:  "https://meta",
		CreationTime: now,
		VoteMode:     dvoteapi.VoteMode{EnvelopeType: &models.EnvelopeType{EncryptedVotes: true, UniqueValues: true}},
		ElectionMode: dvoteapi.ElectionMode{ProcessMode: &models.ProcessMode{AutoStart: true, Interruptible: true}},
		TallyMode:    dvoteapi.TallyMode{ProcessVoteOptions: &models.ProcessVoteOptions{MaxCount: 3, MaxValue: 2}},
	})
	assertWireSubset[Vote](c, &dvoteapi.Vote{
		TxHash: hex, VoteID: hex, EncryptionKeyIndexes: []uint32{1}, VotePackage: json.RawMessage(`{"votes":[1,0]}`),
		VoteWeight: "1", VoteNumber: &number, ElectionID: hex, VoterID: hex, BlockHeight: 42,
		TransactionIndex: &txIndex, Memo: []byte("memo"), Date: &now,
	})
	assertWireSubset[VotesList](c, &dvoteapi.VotesList{Votes: []*dvoteapi.Vote{{VoteID: hex, Memo: []byte("m")}}})
	assertWireSubset[ElectionKeys](c, &dvoteapi.ElectionKeys{
		PublicKeys:  []dvoteapi.Key{{Index: 1, Key: hex}},
		PrivateKeys: []dvoteapi.Key{{Index: 1, Key: hex}},
	})
	assertWireSubset[Account](c, &dvoteapi.Account{Address: hex, Nonce: 5, Balance: 100, InfoURL: "https://info"})
	assertWireSubset[ChainInfo](c, &dvoteapi.ChainInfo{ID: "dev", Height: 77})
	assertWireSubset[Transaction](c, &dvoteapi.Transaction{
		Payload: []byte{1}, Hash: hex, Response: []byte{2}, Code: &code,
		Costs: map[string]uint64{"NewProcess": 10}, Address: hex, ProcessID: hex,
	})
	assertWireSubset[TransactionBatch](c, &dvoteapi.TransactionBatch{
		Transactions: []dvoteapi.TransactionPayload{{Payload: []byte{1}}},
	})
	assertWireSubset[TransactionBatchResult](c, &dvoteapi.TransactionBatchResult{
		Submitted: []dvoteapi.TransactionBatchItem{{Hash: hex, ProcessID: hex, Code: &code}},
		Failed:    []dvoteapi.TransactionBatchItem{{Error: "bad nonce"}},
		Pending:   []dvoteapi.TransactionBatchItem{{}},
	})
	assertWireSubset[TransactionReference](c, &dvoteapi.TransactionReference{Height: 3, Index: 4})
	assertWireSubset[accountSet](c, &dvoteapi.AccountSet{TxPayload: []byte{1}, TxHash: hex})
	assertWireSubset[ElectionMetadata](c, &dvoteapi.ElectionMetadata{
		Title:       dvoteapi.LanguageString{"default": "t"},
		Version:     "1.0",
		Description: dvoteapi.LanguageString{"default": "d"},
		Media:       dvoteapi.ProcessMedia{Header: "h", StreamURI: "s"},
		Meta:        map[string]any{"k": "v"},
		Questions: []dvoteapi.Question{{
			Title:   dvoteapi.LanguageString{"default": "q"},
			Choices: []dvoteapi.ChoiceMetadata{{Title: dvoteapi.LanguageString{"default": "c"}, Value: 1}},
		}},
		Type: dvoteapi.ElectionProperties{Name: "single-choice-multiquestion", Properties: map[string]any{"a": 1.0}},
	})
}
