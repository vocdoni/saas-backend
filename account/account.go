// Package account provides functionality for managing Vocdoni blockchain accounts,
// including signing transactions, creating faucet packages, and interacting with the Vocdoni API.
package account

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

// Account handles the account operations that include signing transactions, creating faucet packages, etc.
type Account struct {
	client *apiclient.HTTPclient
	signer *ethereum.SignKeys
	// apiEndpoint is the node API base URL, for the routes that take query parameters
	// (apiclient.HTTPclient.Request only builds paths).
	apiEndpoint string

	PubKey            internal.HexBytes `swaggertype:"string" format:"hex" example:"deadbeef"`
	TxCosts           map[models.TxType]uint64
	ElectionPriceCalc *electionprice.Calculator
}

// New creates a new account with the given private key and API endpoint.
// The account must already exist on chain with a balance: on a local or CI vocone, fund it at
// genesis with voconed's --fundedAccounts.
func New(privateKey string, apiEndpoint string) (*Account, error) {
	// create the remote API client
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	if err := apiClient.SetAccount(privateKey); err != nil {
		return nil, fmt.Errorf("failed to set account: %w", err)
	}
	// create the signer
	signer := ethereum.SignKeys{}
	if err := signer.AddHexKey(privateKey); err != nil {
		return nil, err
	}
	// get account and log some info
	account, err := apiClient.Account("")
	if err != nil {
		return nil, fmt.Errorf("failed to get account %s, it must exist on chain before the "+
			"backend starts (fund it, e.g. with voconed --fundedAccounts): %w", apiClient.MyAddress().Hex(), err)
	}
	log.Infow(
		"Vocdoni account initialized",
		"endpoint", apiEndpoint,
		"chainID", apiClient.ChainID(),
		"address", account.Address,
		"balance", account.Balance,
	)
	// initialize the election price calculator
	electionPriceCalc, err := InitElectionPriceCalculator(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize election price calculator: %w", err)
	}
	txCosts, err := vochainTxCosts(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction costs: %w", err)
	}
	return &Account{
		client:            apiClient,
		signer:            &signer,
		apiEndpoint:       apiEndpoint,
		PubKey:            internal.HexBytes(signer.PublicKey()),
		TxCosts:           txCosts,
		ElectionPriceCalc: electionPriceCalc,
	}, nil
}

// ChainID returns the identifier of the Vocdoni chain the account is connected to.
func (a *Account) ChainID() string {
	return a.client.ChainID()
}

// FaucetPackage generates a faucet package for the given address and amount.
func (a *Account) FaucetPackage(toAddr common.Address, amount uint64) (*models.FaucetPackage, error) {
	log.Infow(
		"generating faucet package",
		"toAddr", toAddr,
		"amount", amount,
	)
	return vochain.GenerateFaucetPackage(a.signer, toAddr, amount)
}
