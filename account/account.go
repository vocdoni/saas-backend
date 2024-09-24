package account

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	vocdoniapi "go.vocdoni.io/dvote/api"
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

	TxCosts           map[models.TxType]uint64
	ElectionPriceCalc *electionprice.Calculator
}

// New creates a new account with the given private key and API endpoint.
// If the account doesn't exist, it creates a new one.
func New(privateKey string, apiEndpoint string) (*Account, error) {
	// create the remote API client and ensure account exists
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	if err := apiClient.SetAccount(privateKey); err != nil {
		return nil, fmt.Errorf("failed to set account: %w", err)
	}
	if err := ensureAccountExist(apiClient); err != nil {
		return nil, fmt.Errorf("failed to ensure account exists: %w", err)
	}
	// create the signer
	signer := ethereum.SignKeys{}
	if err := signer.AddHexKey(privateKey); err != nil {
		return nil, err
	}
	// get account and log some info
	account, err := apiClient.Account("")
	if err != nil {
		log.Fatalf("failed to get account: %v", err)
	}
	log.Infow("Vocdoni account initialized",
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
		TxCosts:           txCosts,
		ElectionPriceCalc: electionPriceCalc,
	}, nil
}

// FaucetPackage generates a faucet package for the given address and amount.
func (a *Account) FaucetPackage(toAddr string, amount uint64) (*models.FaucetPackage, error) {
	return vochain.GenerateFaucetPackage(a.signer, common.HexToAddress(toAddr), amount)
}

// ensureAccountExist checks if the account exists and creates it if it doesn't.
func ensureAccountExist(cli *apiclient.HTTPclient) error {
	if _, err := cli.Account(""); err == nil {
		return nil
	}
	faucetPkg, err := apiclient.GetFaucetPackageFromDefaultService(cli.MyAddress().Hex(), cli.ChainID())
	if err != nil {
		return fmt.Errorf("failed to get faucet package: %w", err)
	}
	accountMetadata := &vocdoniapi.AccountMetadata{
		Name:        map[string]string{"default": "Vocdoni SaaS backend"},
		Description: map[string]string{"default": "Vocdoni SaaS backend proxy account"},
		Version:     "1.0",
	}
	hash, err := cli.AccountBootstrap(faucetPkg, accountMetadata, nil)
	if err != nil {
		return fmt.Errorf("failed to bootstrap account: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	if _, err := cli.WaitUntilTxIsMined(ctx, hash); err != nil {
		return fmt.Errorf("failed to wait for tx to be mined: %w", err)
	}
	return nil
}
