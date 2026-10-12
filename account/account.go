// Package account provides functionality for managing Vocdoni blockchain accounts,
// including signing transactions, creating faucet packages, and interacting with the Vocdoni API.
package account

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/chainclient"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

// Account handles the account operations that include signing transactions, creating faucet packages, etc.
type Account struct {
	client *chainclient.Client
	signer *ethereum.SignKeys

	PubKey            internal.HexBytes `swaggertype:"string" format:"hex" example:"deadbeef"`
	TxCosts           map[models.TxType]uint64
	ElectionPriceCalc *electionprice.Calculator
}

// New creates a new account with the given private key and API endpoint.
// If the account doesn't exist, it creates a new one.
func New(privateKey string, apiEndpoint string) (*Account, error) {
	// create the remote API client and ensure account exists
	apiClient, err := chainclient.New(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	// create the signer
	signer := ethereum.SignKeys{}
	if err := signer.AddHexKey(privateKey); err != nil {
		return nil, fmt.Errorf("failed to set account: %w", err)
	}
	if err := ensureAccountExist(apiClient, &signer); err != nil {
		return nil, fmt.Errorf("failed to ensure account exists: %w", err)
	}
	// get account and log some info
	account, err := apiClient.Account(signer.AddressString())
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %v", err)
	}
	log.Infow(
		"Vocdoni account initialized",
		"endpoint", apiEndpoint,
		"chainID", apiClient.ChainID(),
		"address", account.Address,
		"balance", account.Balance,
	)
	// initialize the election price calculator
	electionPriceCalc, err := InitElectionPriceCalculator(apiClient)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize election price calculator: %w", err)
	}
	txCosts, err := vochainTxCosts(apiClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction costs: %w", err)
	}
	return &Account{
		client:            apiClient,
		signer:            &signer,
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
	return chainclient.GenerateFaucetPackage(a.signer, toAddr, amount)
}

// serviceAccountName is the on-chain name of the service account.
const serviceAccountName = "Vocdoni SaaS backend"

// ensureAccountExist checks if the signer's account exists and, if it doesn't, creates it paying
// with a package from the node's faucet.
func ensureAccountExist(cli *chainclient.Client, signer *ethereum.SignKeys) error {
	addr := signer.AddressString()
	if _, err := cli.Account(addr); err == nil {
		return nil
	}
	faucetPkg, err := cli.FaucetPackage(addr)
	if err != nil {
		return fmt.Errorf("failed to get faucet package: %w", err)
	}
	hash, err := cli.AccountBootstrap(signer, faucetPkg, serviceAccountName)
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
