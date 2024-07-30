package api

import (
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func signerFromUserEmail(userEmail string) (*ethereum.SignKeys, error) {
	signer := ethereum.SignKeys{}
	return &signer, signer.AddHexKey(hex.EncodeToString(ethereum.HashRaw([]byte(userEmail))))
}

func (a *API) signTxHandler(w http.ResponseWriter, r *http.Request) {
	// retrieve the user identifier from the HTTP header
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// read the tx from the request body
	defer r.Body.Close()
	txData, err := io.ReadAll(r.Body)
	if err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// decode the tx provided
	tx := &models.Tx{}
	if err := proto.Unmarshal(txData, tx); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// create the payload to sign
	payloadToSign, err := ethereum.BuildVocdoniProtoTxMessage(tx, a.vocdoniChain, ethereum.HashRaw(txData))
	if err != nil {
		ErrGenericInternalServerError.Withf("could not build payload to sign: %v", err).Write(w)
		return
	}
	// get the user register from the user identifier
	signer, err := signerFromUserEmail(userID)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not create signer for user: %v", err).Write(w)
		return
	}
	// sign the payload
	signature, err := ethcrypto.Sign(payloadToSign, &signer.Private)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not sign payload: %v", err).Write(w)
		return
	}
	stx, err := proto.Marshal(
		&models.SignedTx{
			Tx:        txData,
			Signature: signature,
		})
	if err != nil {
		ErrGenericInternalServerError.Withf("could not marshal signed tx: %v", err).Write(w)
		return
	}
	httpWriteJSON(w, &EncodedSignedTxResponse{
		Data: base64.StdEncoding.EncodeToString(stx),
	})
}
