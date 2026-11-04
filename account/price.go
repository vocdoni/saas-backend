package account

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/genesis"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

const (
	// electionPriceEndpoint is the endpoint to get the election price factors
	// from the Vochain.
	electionPriceEndpoint = "/chain/info/electionPriceFactors"
	// txCostsEndpoint is the endpoint to get the transaction costs from the
	// Vochain.
	txCostsEndpoint = "/chain/transactions/cost"
)

// InitElectionPriceCalculator initializes the election price calculator with
// the factors from the Vochain. It returns the election price calculator or an
// error if it fails to get the factors.
func InitElectionPriceCalculator(vochainURI string) (*electionprice.Calculator, error) {
	basePrice, capacity, factors, err := electionPriceFactors(vochainURI)
	if err != nil {
		return nil, fmt.Errorf("failed to get election price factors: %w", err)
	}
	electionPriceCalc := electionprice.NewElectionPriceCalculator(factors)
	electionPriceCalc.SetBasePrice(basePrice)
	electionPriceCalc.SetCapacity(capacity)
	log.Infow("Election price calculator initialized",
		"basePrice", basePrice,
		"capacity", capacity,
		"K1", factors.K1,
		"K2", factors.K2,
		"K3", factors.K3,
		"K4", factors.K4,
		"K5", factors.K5,
		"K6", factors.K6,
		"K7", factors.K7,
	)
	return electionPriceCalc, nil
}

// ElectionPriceFactors returns the election price factors from the Vochain. It
// returns the base price, capacity, and factors. If there is an error, it
// returns the error.
//
//revive:disable:function-result-limit
func electionPriceFactors(vochainURI string) (basePrice uint64, capacity uint64, factors electionprice.Factors, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	var data electionprice.Calculator
	if err := getJSON(ctx, vochainURI+electionPriceEndpoint, &data); err != nil {
		return 0, 0, electionprice.Factors{}, err
	}
	return data.BasePrice, data.Capacity, data.Factors, nil
}

// vochainTxCosts returns the transaction costs from the Vochain. It returns the
// transaction costs or an error if it fails to get them.
func vochainTxCosts(vochainURI string) (map[models.TxType]uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	var strTxCosts api.Transaction
	if err := getJSON(ctx, vochainURI+txCostsEndpoint, &strTxCosts); err != nil {
		return nil, err
	}
	log.Debugf("received tx costs: %v", strTxCosts.Costs)

	extraCostStr := os.Getenv("VOCDONI_FAUCET_TX_EXTRA_COST") // extra cost for faucet transactions, for testing purposes
	extraCost := uint64(0)
	if extraCostStr != "" {
		if cost, err := strconv.ParseUint(extraCostStr, 10, 64); err == nil {
			extraCost = cost
			log.Infow("using extra cost for faucet transactions", "cost", extraCost)
		} else {
			log.Errorw(err, "failed to parse extra cost for faucet transactions")
		}
	}

	txCosts := make(map[models.TxType]uint64)
	for strType, cost := range strTxCosts.Costs {
		txCosts[genesis.TxCostNameToTxType(strType)] = cost + extraCost
	}
	return txCosts, nil
}
