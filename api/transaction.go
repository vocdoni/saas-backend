package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func (a *API) signTxHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// read the request body
	signReq := &TransactionData{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	// check if the user is a member of the organization
	if !user.IsMemberOf(signReq.Address) {
		ErrUnauthorized.With("user is not an organization member").Write(w)
		return
	}
	// get the organization info from the database with the address provided in
	// the request
	org, _, err := a.db.Organization(signReq.Address, false)
	if err != nil {
		if err == db.ErrNotFound {
			ErrOrganizationNotFound.Withf("organization not found").Write(w)
			return
		}
		ErrGenericInternalServerError.Withf("could not get organization: %v", err).Write(w)
		return
	}
	// get the user signer from secret, organization creator and organization nonce
	organizationSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
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
	// flag to know if the TX is New Process
	isNewProcess := false

	// check if the api is not in transparent mode
	if !a.transparentMode {
		// get subscription plan
		switch tx.Payload.(type) {
		case *models.Tx_SetAccount:
			txSetAccount := tx.GetSetAccount()
			// check the tx fields
			if txSetAccount == nil || txSetAccount.Account == nil || txSetAccount.InfoURI == nil {
				ErrInvalidTxFormat.With("missing fields").Write(w)
				return
			}
			// check the account is the same as the user
			if !bytes.Equal(txSetAccount.GetAccount(), organizationSigner.Address().Bytes()) {
				ErrUnauthorized.With("invalid account").Write(w)
				return
			}
			if hasPermission, err := a.subscriptions.HasTxPermission(tx, txSetAccount.Txtype, org, user); !hasPermission || err != nil {
				ErrUnauthorized.Withf("user does not have permission to sign transactions: %v", err).Write(w)
				return
			}
			// check the tx subtype
			switch txSetAccount.Txtype {
			case models.TxType_CREATE_ACCOUNT:
				// generate a new faucet package if it's not present and include it in the tx
				if txSetAccount.FaucetPackage == nil {
					// get the tx cost for the tx type
					amount, ok := a.account.TxCosts[models.TxType_CREATE_ACCOUNT]
					if !ok {
						panic("invalid tx type")
					}
					// generate the faucet package with the calculated amount
					faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
					if err != nil {
						ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
						return
					}
					// include the faucet package in the tx
					txSetAccount.FaucetPackage = faucetPkg
					tx = &models.Tx{
						Payload: &models.Tx_SetAccount{
							SetAccount: txSetAccount,
						},
					}
				}
			case models.TxType_SET_ACCOUNT_INFO_URI:
				// generate a new faucet package if it's not present and include it in the tx
				if txSetAccount.FaucetPackage == nil {
					// get the tx cost for the tx type
					amount, ok := a.account.TxCosts[models.TxType_SET_ACCOUNT_INFO_URI]
					if !ok {
						panic("invalid tx type")
					}
					// generate the faucet package with the calculated amount
					faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
					if err != nil {
						ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
						return
					}
					// include the faucet package in the tx
					txSetAccount.FaucetPackage = faucetPkg
					tx = &models.Tx{
						Payload: &models.Tx_SetAccount{
							SetAccount: txSetAccount,
						},
					}
				}

			}
		case *models.Tx_NewProcess:
			txNewProcess := tx.GetNewProcess()
			// check the tx fields
			if txNewProcess == nil || txNewProcess.Process == nil {
				// if txNewProcess == nil || txNewProcess.Process == nil || txNewProcess.Nonce == 0 {
				ErrInvalidTxFormat.With("missing fields").Write(w)
				return
			}
			if hasPermission, err := a.subscriptions.HasTxPermission(tx, txNewProcess.Txtype, org, user); !hasPermission || err != nil {
				ErrUnauthorized.Withf("user does not have permission to sign transactions: %v", err).Write(w)
				return
			}
			// check the tx subtype
			switch txNewProcess.Txtype {
			case models.TxType_NEW_PROCESS:
				isNewProcess = true
				// generate a new faucet package if it's not present and include it in the tx
				if txNewProcess.FaucetPackage == nil {
					// get the tx cost for the tx type
					amount, ok := a.account.TxCosts[models.TxType_NEW_PROCESS]
					if !ok {
						panic("invalid tx type")
					}
					// increment the amount with the election price to fund the
					// faucet package with the required amount for this type of
					// election
					amount += a.account.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
						MaxCensusSize:           txNewProcess.Process.MaxCensusSize,
						ElectionDurationSeconds: txNewProcess.Process.Duration,
						EncryptedVotes:          txNewProcess.Process.EnvelopeType.EncryptedVotes,
						AnonymousVotes:          txNewProcess.Process.EnvelopeType.Anonymous,
						MaxVoteOverwrite:        txNewProcess.Process.VoteOptions.MaxVoteOverwrites,
					})
					// generate the faucet package with the calculated amount
					faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
					if err != nil {
						ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
						return
					}
					// include the faucet package in the tx
					txNewProcess.FaucetPackage = faucetPkg
					tx = &models.Tx{
						Payload: &models.Tx_NewProcess{
							NewProcess: txNewProcess,
						},
					}
				}
			}
		case *models.Tx_SetProcess:
			txSetProcess := tx.GetSetProcess()
			// check the tx fields
			if txSetProcess == nil || txSetProcess.ProcessId == nil {
				ErrInvalidTxFormat.With("missing fields").Write(w)
				return
			}
			// get the tx cost for the tx type
			amount, ok := a.account.TxCosts[txSetProcess.Txtype]
			if !ok {
				ErrInvalidTxFormat.With("invalid tx type").Write(w)
				return
			}
			if hasPermission, err := a.subscriptions.HasTxPermission(tx, txSetProcess.Txtype, org, user); !hasPermission || err != nil {
				ErrUnauthorized.Withf("user does not have permission to sign transactions: %v", err).Write(w)
				return
			}
			// check the tx subtype
			switch txSetProcess.Txtype {
			case models.TxType_SET_PROCESS_STATUS:
				// check if the process status is in the tx
				if txSetProcess.Status == nil {
					ErrInvalidTxFormat.With("missing status field").Write(w)
					return
				}
			case models.TxType_SET_PROCESS_CENSUS:
				// check if the process census is in the tx
				if (txSetProcess.CensusRoot == nil || txSetProcess.CensusURI == nil) && txSetProcess.CensusSize == nil {
					ErrInvalidTxFormat.With("missing census fields").Write(w)
					return
				}
				// get the current process to fill the missing fields in the tx to
				// calculate the election price
				currentProcess, err := a.client.Election(txSetProcess.ProcessId)
				if err != nil {
					ErrVochainRequestFailed.WithErr(err).Write(w)
					return
				}
				// increment the amount with the election price to fund the
				// faucet package with the required amount for this type of
				// election update
				amount += a.account.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
					MaxCensusSize:           txSetProcess.GetCensusSize(),
					ElectionDurationSeconds: uint32(currentProcess.EndDate.Sub(currentProcess.StartDate).Seconds()),
					EncryptedVotes:          currentProcess.VoteMode.EncryptedVotes,
					AnonymousVotes:          currentProcess.VoteMode.Anonymous,
					MaxVoteOverwrite:        currentProcess.TallyMode.MaxVoteOverwrites,
				})
			case models.TxType_SET_PROCESS_RESULTS:
				// check if the process results are in the tx
				if txSetProcess.Results == nil {
					ErrInvalidTxFormat.With("missing results field").Write(w)
					return
				}
			case models.TxType_SET_PROCESS_DURATION:
				// check if the process duration is in the tx
				if txSetProcess.Duration == nil {
					ErrInvalidTxFormat.With("missing duration field").Write(w)
					return
				}
				// get the current process to fill the missing fields in the tx to
				// calculate the election price
				currentProcess, err := a.client.Election(txSetProcess.ProcessId)
				if err != nil {
					ErrVochainRequestFailed.WithErr(err).Write(w)
					return
				}
				// increment the amount with the election price to fund the
				// faucet package with the required amount for this type of
				// election update
				amount += a.account.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
					MaxCensusSize:           currentProcess.Census.MaxCensusSize,
					ElectionDurationSeconds: txSetProcess.GetDuration(),
					EncryptedVotes:          currentProcess.VoteMode.EncryptedVotes,
					AnonymousVotes:          currentProcess.VoteMode.Anonymous,
					MaxVoteOverwrite:        currentProcess.TallyMode.MaxVoteOverwrites,
				})
			}
			// include the faucet package in the tx if it's not present
			if txSetProcess.FaucetPackage == nil {
				// generate the faucet package with the calculated amount
				faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
				if err != nil {
					ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
					return
				}
				// include the faucet package in the tx
				txSetProcess.FaucetPackage = faucetPkg
				tx = &models.Tx{
					Payload: &models.Tx_SetProcess{
						SetProcess: txSetProcess,
					},
				}
			}
		case *models.Tx_SetSIK, *models.Tx_DelSIK:
			txSetSIK := tx.GetSetSIK()
			// check the tx fields
			if txSetSIK == nil || txSetSIK.SIK == nil {
				ErrInvalidTxFormat.With("missing fields").Write(w)
				return
			}
			// include the faucet package in the tx if it's not present
			if txSetSIK.FaucetPackage == nil {
				// get the tx cost for the tx type
				amount, ok := a.account.TxCosts[models.TxType_SET_ACCOUNT_SIK]
				if !ok {
					ErrInvalidTxFormat.With("invalid tx type").Write(w)
					return
				}
				// generate the faucet package with the calculated amount
				faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
				if err != nil {
					ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
					return
				}
				// include the faucet package in the tx
				txSetSIK.FaucetPackage = faucetPkg
				tx = &models.Tx{
					Payload: &models.Tx_SetSIK{
						SetSIK: txSetSIK,
					},
				}
			}
		case *models.Tx_CollectFaucet:
			txCollectFaucet := tx.GetCollectFaucet()
			// include the faucet package in the tx if it's not present
			if txCollectFaucet.FaucetPackage == nil {
				// get the tx cost for the tx type
				amount, ok := a.account.TxCosts[models.TxType_COLLECT_FAUCET]
				if !ok {
					ErrInvalidTxFormat.With("invalid tx type").Write(w)
					return
				}
				// generate the faucet package with the calculated amount
				faucetPkg, err := a.account.FaucetPackage(organizationSigner.AddressString(), amount)
				if err != nil {
					ErrCouldNotCreateFaucetPackage.WithErr(err).Write(w)
					return
				}
				// include the faucet package in the tx
				txCollectFaucet.FaucetPackage = faucetPkg
				tx = &models.Tx{
					Payload: &models.Tx_CollectFaucet{
						CollectFaucet: txCollectFaucet,
					},
				}
			}
		default:
			log.Warnw("transaction type not allowed", "user", user.Email, "type", fmt.Sprintf("%T", tx.Payload))
			ErrTxTypeNotAllowed.Write(w)
			return
		}
	}
	// sign the tx
	stx, err := a.account.SignTransaction(tx, organizationSigner)
	if err != nil {
		ErrCouldNotSignTransaction.WithErr(err).Write(w)
		return
	}

	// If isNewProcess and everything went well so far update the organization process counter
	if isNewProcess {
		org.Counters.Processes++
		if err := a.db.SetOrganization(org); err != nil {
			ErrGenericInternalServerError.Withf("could not update organization process counter: %v", err).Write(w)
			return
		}
	}

	// return the signed tx payload
	httpWriteJSON(w, &TransactionData{
		TxPayload: base64.StdEncoding.EncodeToString(stx),
	})
}

// signMessageHandler signs a message with the user's private key. Only certain messages are allowed to be signed.
func (a *API) signMessageHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// read the message from the request body
	signReq := &MessageSignature{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	// check if the request includes a payload to sign
	if signReq.Payload == nil {
		ErrMalformedBody.Withf("missing payload field in request body").Write(w)
		return
	}
	// check if the user has the admin role for the organization
	if !user.HasRoleFor(signReq.Address, db.AdminRole) {
		ErrUnauthorized.With("user does not have admin role").Write(w)
		return
	}
	// get the organization info from the database with the address provided in
	// the request
	org, _, err := a.db.Organization(signReq.Address, false)
	if err != nil {
		if err == db.ErrNotFound {
			ErrOrganizationNotFound.Withf("organization not found").Write(w)
			return
		}
		ErrGenericInternalServerError.Withf("could not get organization: %v", err).Write(w)
		return
	}
	// get the user signer from secret, organization creator and organization nonce
	organizationSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not restore the signer of the organization: %v", err).Write(w)
		return
	}
	// sign the message
	signature, err := account.SignMessage(signReq.Payload, organizationSigner)
	if err != nil {
		ErrGenericInternalServerError.With("could not sign message").Write(w)
	}
	// return the signature
	httpWriteJSON(w, &MessageSignature{
		Signature: signature,
	})
}
