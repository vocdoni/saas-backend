package account

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.vocdoni.io/dvote/vochain/state/electionprice"
)

// electionPriceEndpoint is the endpoint to get the election price factors from
// the Vochain.
const electionPriceEndpoint = "/chain/info/electionPriceFactors"

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
	return electionPriceCalc, nil
}

// ElectionPriceFactors returns the election price factors from the Vochain. It
// returns the base price, capacity, and factors. If there is an error, it
// returns the error.
func electionPriceFactors(vochainURI string) (uint64, uint64, electionprice.Factors, error) {
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
