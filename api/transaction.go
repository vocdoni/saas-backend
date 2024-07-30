package api

import (
	"encoding/hex"

	"go.vocdoni.io/dvote/crypto/ethereum"
)

func signerFromUserEmail(userEmail string) (*ethereum.SignKeys, error) {
	signer := ethereum.SignKeys{}
	return &signer, signer.AddHexKey(hex.EncodeToString(ethereum.HashRaw([]byte(userEmail))))
}
