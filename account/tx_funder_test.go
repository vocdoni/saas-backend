package account

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// TestFundSetProcessDuration pins that the funder pays a SET_PROCESS_DURATION tx exactly what the
// chain charges (txCostIncreaseDuration): the base cost plus the price of the election at the new
// duration minus its price now, both computed from the election's real on-chain parameters.
func TestFundSetProcessDuration(t *testing.T) {
	c := qt.New(t)
	electionID := []byte(strings.Repeat("e", 32))
	start := time.Unix(1_800_000_000, 0)
	election := &api.Election{
		ElectionSummary: api.ElectionSummary{
			ElectionID: electionID,
			StartDate:  start,
			EndDate:    start.Add(24 * time.Hour),
		},
		Census:    &api.ElectionCensus{MaxCensusSize: 5000},
		VoteMode:  api.VoteMode{EnvelopeType: &models.EnvelopeType{EncryptedVotes: true, Anonymous: true}},
		TallyMode: api.TallyMode{ProcessVoteOptions: &models.ProcessVoteOptions{MaxVoteOverwrites: 2}},
	}
	var reads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if r.URL.Path != "/elections/"+strings.Repeat("65", 32) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(election); err != nil {
			t.Errorf("encoding election: %v", err)
		}
	}))
	defer srv.Close()
	client, err := apiclient.New(srv.URL + "/")
	c.Assert(err, qt.IsNil)
	signer := ethereum.NewSignKeys()
	c.Assert(signer.Generate(), qt.IsNil)
	calc := electionprice.NewElectionPriceCalculator(electionprice.DefaultElectionPriceFactors)
	calc.SetBasePrice(10)
	calc.SetCapacity(2000)
	const baseCost = 3
	a := &Account{
		client:            client,
		signer:            signer,
		TxCosts:           map[models.TxType]uint64{models.TxType_SET_PROCESS_DURATION: baseCost},
		ElectionPriceCalc: calc,
	}
	price := func(seconds uint32) uint64 {
		return calc.Price(&electionprice.ElectionParameters{
			MaxCensusSize: 5000, ElectionDurationSeconds: seconds,
			EncryptedVotes: true, AnonymousVotes: true, MaxVoteOverwrite: 2,
		})
	}
	durationTx := func(d *uint32) *models.Tx {
		return &models.Tx{Payload: &models.Tx_SetProcess{SetProcess: &models.SetProcessTx{
			Txtype: models.TxType_SET_PROCESS_DURATION, ProcessId: electionID, Duration: d,
		}}}
	}
	fundedAmount := func(tx *models.Tx) uint64 {
		pkg := tx.GetSetProcess().GetFaucetPackage()
		c.Assert(pkg, qt.Not(qt.IsNil))
		var payload models.FaucetPayload
		c.Assert(proto.Unmarshal(pkg.Payload, &payload), qt.IsNil)
		return payload.Amount
	}
	org := signer.Address()

	// extend 24h -> 72h: base cost plus the exact duration delta
	longer := uint32((72 * time.Hour).Seconds())
	funded, txType, err := a.FundTransaction(durationTx(&longer), org)
	c.Assert(err, qt.IsNil)
	c.Assert(*txType, qt.Equals, models.TxType_SET_PROCESS_DURATION)
	delta := price(longer) - price(uint32((24 * time.Hour).Seconds()))
	c.Assert(delta > 0, qt.IsTrue)
	c.Assert(fundedAmount(funded), qt.Equals, baseCost+delta)

	// with the election already read, the same amount is funded without reading it again
	readsBefore := reads.Load()
	funded, _, err = a.FundTransactionWith(durationTx(&longer), org, election)
	c.Assert(err, qt.IsNil)
	c.Assert(fundedAmount(funded), qt.Equals, baseCost+delta)
	c.Assert(reads.Load(), qt.Equals, readsBefore)
	// ...and another election than the tx's is refused
	other := *election
	other.ElectionID = []byte(strings.Repeat("f", 32))
	_, _, err = a.FundTransactionWith(durationTx(&longer), org, &other)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidTxFormat)

	// a duration that is not dearer funds the base cost only
	shorter := uint32((12 * time.Hour).Seconds())
	funded, _, err = a.FundTransaction(durationTx(&shorter), org)
	c.Assert(err, qt.IsNil)
	c.Assert(fundedAmount(funded), qt.Equals, uint64(baseCost))

	// a duration tx without the duration is malformed
	_, _, err = a.FundTransaction(durationTx(nil), org)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidTxFormat)

	// other SetProcess types are still refused
	results := &models.Tx{Payload: &models.Tx_SetProcess{SetProcess: &models.SetProcessTx{
		Txtype: models.TxType_SET_PROCESS_RESULTS, ProcessId: electionID,
	}}}
	_, _, err = a.FundTransaction(results, org)
	c.Assert(err, qt.ErrorIs, errors.ErrInvalidTxFormat)
}
