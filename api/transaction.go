package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

const bootStrapFaucetAmount = 100

func (a *API) signTxHandler(w http.ResponseWriter, r *http.Request) {
	// retrieve the user identifier from the HTTP header
	userEmail := r.Header.Get("X-User-Id")
	if userEmail == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// read the request body
	signReq := &TransactionData{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	// get the user signer from the user identifier
	organizationSigner, err := a.organizationSignerForUser(signReq.OrganizationAddress, userEmail)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not restore the signer of the organization: %v", err).Write(w)
		return
	}
	// check if the request includes a payload to sign
	if signReq.TxPayload == "" {
		ErrMalformedBody.Withf("missing data field in request body").Write(w)
		return
	}
	// decode the transaction data from the request (base64 encoding)
	txData, err := base64.StdEncoding.DecodeString(signReq.TxPayload)
	if err != nil {
		ErrMalformedBody.Withf("could not decode the base64 data from the body").Write(w)
		return
	}
	// unmarshal the tx with the protobuf model
	tx := &models.Tx{}
	if err := proto.Unmarshal(txData, tx); err != nil {
		ErrInvalidTxFormat.Write(w)
		return
	}
	// check the tx payload
	if !a.transparentMode {
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
			log.Infow("signing SetAccount transaction", "user", userEmail, "type", txSetAccount.Txtype.String())
			// check the tx subtype
			switch txSetAccount.Txtype {
			case models.TxType_CREATE_ACCOUNT:
				// generate a new faucet package if it's not present and include it in the tx
				if txSetAccount.FaucetPackage == nil {
					faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), bootStrapFaucetAmount)
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
			log.Infow("signing SetProcess transaction", "user", userEmail)
		case *models.Tx_CollectFaucet:
			log.Infow("signing CollectFaucet transaction", "user", userEmail)
		case *models.Tx_NewProcess:
			log.Infow("signing NewProcess transaction", "user", userEmail)
		default:
			log.Warnw("transaction type not allowed", "user", userEmail, "type", fmt.Sprintf("%T", tx.Payload))
			ErrTxTypeNotAllowed.Write(w)
			return
		}
	} else {
		log.Infow("signing transaction in full transparent mode", "user", userEmail, "type", fmt.Sprintf("%T", tx.Payload))
	}
	// sign the tx
	stx, err := a.account.SignTransaction(tx, organizationSigner)
	if err != nil {
		ErrCouldNotSignTransaction.WithErr(err).Write(w)
		return
	}
	httpWriteJSON(w, &TransactionData{
		TxPayload: base64.StdEncoding.EncodeToString(stx),
	})
}

// signMessageHandler signs a message with the user's private key. Only certain messages are allowed to be signed.
func (a *API) signMessageHandler(w http.ResponseWriter, r *http.Request) {
	// retrieve the user identifier from the HTTP header
	userEmail := r.Header.Get("X-User-Id")
	if userEmail == "" {
		ErrUnauthorized.Write(w)
		return
	}
	// read the message from the request body
	signReq := &MessageSignature{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	if signReq.Payload == nil {
		ErrMalformedBody.Withf("missing payload field in request body").Write(w)
		return
	}
	// get the user signer from the user identifier
	organizationSigner, err := a.organizationSignerForUser(signReq.OrganizationAddress, userEmail)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not restore the signer of the organization: %v", err).Write(w)
		return
	}
	// sign the message
	signature, err := account.SignMessage(signReq.Payload, organizationSigner)
	if err != nil {
		ErrGenericInternalServerError.With("could not sign message").Write(w)
	}

	httpWriteJSON(w, &MessageSignature{
		Signature: signature,
	})
}
