package account

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	// create the request to get the election price factors
	url := vochainURI + electionPriceEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, electionprice.Factors{}, fmt.Errorf("failed to create request: %w", err)
	}
	// send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, 0, electionprice.Factors{}, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	// parse the response
	if resp.StatusCode != http.StatusOK {
		return 0, 0, electionprice.Factors{}, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	var data electionprice.Calculator
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, electionprice.Factors{}, fmt.Errorf("failed to decode response: %w", err)
	}
	return data.BasePrice, data.Capacity, data.Factors, nil
}

// vochainTxCosts returns the transaction costs from the Vochain. It returns the
// transaction costs or an error if it fails to get them.
func vochainTxCosts(vochainURI string) (map[models.TxType]uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	// create the request to get the transactions costs
	url := vochainURI + txCostsEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	// send the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	// parse the response
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	var strTxCosts api.Transaction
	if err := json.NewDecoder(resp.Body).Decode(&strTxCosts); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
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
