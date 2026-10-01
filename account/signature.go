package account

import (
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

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

// VerifySignature recovers the signer address from the given message and
// signature and checks that it matches the expected address. It returns an
// error when the signature cannot be decoded, the address cannot be recovered,
// or the recovered address does not match the expected one.
func VerifySignature(message, signature, address string) error {
	messageBytes := []byte(message)
	signatureBytes, err := hex.DecodeString(util.TrimHex(signature))
	if err != nil {
		return fmt.Errorf("could not decode signature: %w", err)
	}
	recoveredAddr, err := ethereum.AddrFromSignature(messageBytes, signatureBytes)
	if err != nil {
		return fmt.Errorf("could not calculate address from signature: %w", err)
	}
	// common.HexToAddress normalizes the expected address regardless of the 0x
	// prefix or letter case, so the comparison against the recovered address is
	// reliable. The verification fails when they do not match.
	if recoveredAddr != common.HexToAddress(address) {
		return fmt.Errorf("signature verification failed: recovered address does not match expected address")
	}
	return nil
}
