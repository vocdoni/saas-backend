package account

import (
	"encoding/hex"
	"fmt"
	"strings"

	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

var AllowedSignMessagesHash = map[string]*struct{}{
	"55c85f40d49bf654adcd277bd44f91fb1ac51b680e34b9f0d022b96c4f91e5ea": nil, // SIK generation message
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

// SignMessage signs a message with the account's private key. It uses the Ethereum message signature format.
// Only a subset of messages are allowed to be signed.
func SignMessage(message []byte, signer *ethereum.SignKeys) ([]byte, error) {
	// check if the message is allowed to be signed
	hash := hex.EncodeToString(ethereum.Hash(message))
	if _, ok := AllowedSignMessagesHash[hash]; !ok {
		return nil, fmt.Errorf("message not allowed to be signed")
	}
	signature, err := signer.SignEthereum(message)
	if err != nil {
		return nil, fmt.Errorf("could not sign message: %w", err)
	}
	return signature, nil
}

func VerifySignature(message, signature, address string) error {
	// calcAddress, err := ethereum.AddrFromSignature([]byte(message), []byte(signature))
	messageBytes := []byte(message)
	signatureBytes, err := hex.DecodeString(util.TrimHex(signature))
	if err != nil {
		return fmt.Errorf("could not decode signature: %w", err)
	}
	calcAddress, err := ethereum.AddrFromSignature(messageBytes, signatureBytes)
	if err != nil {
		return fmt.Errorf("could not calculate address from signature: %w", err)
	}
	if strings.EqualFold(calcAddress.String(), util.TrimHex(address)) {
		return fmt.Errorf("signature verification failed")
	}
	return nil
}
