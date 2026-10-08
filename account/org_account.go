package account

import (
	"context"
	"fmt"
	"time"

	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

// CreateOrgAccount forges, funds, signs, submits and confirms a CREATE_ACCOUNT
// transaction on the Vocdoni chain for the organization identified by orgSigner.
// It is idempotent: if the account already exists on chain it returns nil
// without sending a transaction (safe to retry, and safe if a legacy client
// later creates the account itself). name and infoURI populate the account's
// on-chain metadata pointer.
func (a *Account) CreateOrgAccount(orgSigner *ethereum.SignKeys, name, infoURI string) error {
	addr := orgSigner.Address()
	// idempotent: if the account already exists on chain, nothing to do
	if _, err := a.client.Account(addr.String()); err == nil {
		return nil
	}
	nonce := uint32(0)
	tx := &models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Nonce:   &nonce,
				Txtype:  models.TxType_CREATE_ACCOUNT,
				Account: addr.Bytes(),
				Name:    &name,
				InfoURI: &infoURI,
			},
		},
	}
	fundedTx, _, err := a.FundTransaction(tx, addr)
	if err != nil {
		return fmt.Errorf("could not fund create account tx: %w", err)
	}
	stx, err := a.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		return fmt.Errorf("could not sign create account tx: %w", err)
	}
	hash, _, err := a.client.SendTx(stx)
	if err != nil {
		return fmt.Errorf("could not submit create account tx: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	if _, err := a.client.WaitUntilTxIsMined(ctx, hash); err != nil {
		return fmt.Errorf("could not wait for create account tx to be mined: %w", err)
	}
	return nil
}
