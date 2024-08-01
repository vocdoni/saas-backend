package api

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

const (
	bootStrapFaucetAmount = 100
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
	signReq := &TransactionData{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	if signReq.TxPayload == "" {
		ErrMalformedBody.Withf("missing data field in request body").Write(w)
		return
	}
	txData, err := base64.StdEncoding.DecodeString(signReq.TxPayload)
	if err != nil {
		ErrMalformedBody.Withf("could not decode the base64 data from the body").Write(w)
		return
	}

	// unmarshal the tx
	tx := &models.Tx{}
	if err := proto.Unmarshal(txData, tx); err != nil {
		ErrInvalidTxFormat.Write(w)
		return
	}

	// get the user signer from the user identifier
	organizationSigner, err := signerFromUserEmail(userID)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not create signer for user: %v", err).Write(w)
		return
	}

	// check the tx payload
	if !FullTransparentMode {
		switch tx.Payload.(type) {
		case *models.Tx_SetAccount:
			// check the account is the same as the user
			txSetAccount := tx.GetSetAccount()
			if txSetAccount == nil || txSetAccount.Account == nil || txSetAccount.InfoURI == nil {
				ErrInvalidTxFormat.With("missing fields").Write(w)
				return
			}
			if !bytes.Equal(txSetAccount.GetAccount(), organizationSigner.Address().Bytes()) {
				ErrUnauthorized.With("invalid account").Write(w)
				return
			}
			log.Infow("signing SetAccount transaction", "user", userID, "type", txSetAccount.Txtype.String())

			// check the tx subtype
			switch txSetAccount.Txtype {
			case models.TxType_CREATE_ACCOUNT:
				// generate a new faucet package if it's not present and include it in the tx
				if txSetAccount.FaucetPackage == nil {
					faucetPkg, err := a.acc.FaucetPackage(organizationSigner.AddressString(), bootStrapFaucetAmount)
					if err != nil {
						ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
						return
					}
					txSetAccount.FaucetPackage = faucetPkg
					tx = &models.Tx{
						Payload: &models.Tx_SetAccount{
							SetAccount: txSetAccount,
						},
					}
				}
			}
		case *models.Tx_SetProcess:
			log.Infow("signing SetProcess transaction", "user", userID)
		case *models.Tx_CollectFaucet:
			log.Infow("signing CollectFaucet transaction", "user", userID)
		case *models.Tx_NewProcess:
			log.Infow("signing NewProcess transaction", "user", userID)
		default:
			log.Warnw("transaction type not allowed", "user", userID, "type", fmt.Sprintf("%T", tx.Payload))
			ErrTxTypeNotAllowed.Write(w)
			return
		}
	} else {
		log.Infow("signing transaction in full transparent mode", "user", userID, "type", fmt.Sprintf("%T", tx.Payload))
	}

	// sign the tx
	stx, err := a.acc.SignTransaction(tx, organizationSigner)
	if err != nil {
		ErrCouldNotSignTransaction.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, &TransactionData{
		TxPayload: base64.StdEncoding.EncodeToString(stx),
	})
}
