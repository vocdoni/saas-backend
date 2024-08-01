package account

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/spf13/viper"
	vocdoniapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// Account handles the account operations that include signing transactions, creating faucet packages, etc.
type Account struct {
	client *apiclient.HTTPclient
	signer *ethereum.SignKeys
}

// New creates a new account with the given private key and API endpoint.
// If the account doesn't exist, it creates a new one.
func New(privateKey string, apiEndpoint string) (*Account, error) {
	// create the remote API client and ensure account exists
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to create API client: %w", err)
	}
	if err := apiClient.SetAccount(viper.GetString("privateKey")); err != nil {
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

	return &Account{
		client: apiClient,
		signer: &signer,
	}, nil
}

// SignTransaction signs a transaction with the account's private key.
// Returns the payload of the signed protobuf transaction (models.SignedTx).
func (a *Account) SignTransaction(tx *models.Tx, signer *ethereum.SignKeys) ([]byte, error) {
	// marshal the tx
	txData, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("could not marshal tx: %w", err)
	}
	// sign the tx
	signature, err := signer.SignVocdoniTx(txData, a.client.ChainID())
	if err != nil {
		return nil, fmt.Errorf("could not sign tx: %w", err)
	}

	// marshal the signed tx and send it back
	stx, err := proto.Marshal(
		&models.SignedTx{
			Tx:        txData,
			Signature: signature,
		})
	if err != nil {
		return nil, fmt.Errorf("could not marshal signed tx: %w", err)
	}
	return stx, nil
}

// FaucetPackage generates a faucet package for the given address and amount.
func (a *Account) FaucetPackage(toAddr string, amount uint64) (*models.FaucetPackage, error) {
	return vochain.GenerateFaucetPackage(a.signer, common.HexToAddress(toAddr), amount)
}

// ensureAccountExist checks if the account exists and creates it if it doesn't.
func ensureAccountExist(cli *apiclient.HTTPclient) error {
	account, err := cli.Account("")
	if err == nil {
		log.Infow("account already exists", "address", account.Address)
		return nil
	}

	log.Infow("creating new account", "address", cli.MyAddress().Hex())
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
