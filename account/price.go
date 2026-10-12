package account

import (
	"fmt"
	"os"
	"strconv"

	"github.com/vocdoni/saas-backend/chainclient"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

// InitElectionPriceCalculator initializes the election price calculator with
// the factors from the Vochain. It returns the election price calculator or an
// error if it fails to get the factors.
func InitElectionPriceCalculator(cli *chainclient.Client) (*electionprice.Calculator, error) {
	data, err := cli.ElectionPriceFactors()
	if err != nil {
		return nil, fmt.Errorf("failed to get election price factors: %w", err)
	}
	basePrice, capacity, factors := data.BasePrice, data.Capacity, data.Factors
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

// vochainTxCosts returns the transaction costs from the Vochain. It returns the
// transaction costs or an error if it fails to get them.
func vochainTxCosts(cli *chainclient.Client) (map[models.TxType]uint64, error) {
	costs, err := cli.TransactionCosts()
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction costs: %w", err)
	}
	log.Debugf("received tx costs: %v", costs)

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
	for strType, cost := range costs {
		txCosts[chainclient.TxCostNameToTxType(strType)] = cost + extraCost
	}
	return txCosts, nil
}
