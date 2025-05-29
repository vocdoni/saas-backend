package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// signTxHandler godoc
//
//	@Summary		Sign a transaction
//	@Description	Sign a transaction with the organization's private key. The user must have a role in the organization.
//	@Tags			transactions
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.TransactionData	true	"Transaction data to sign"
//	@Success		200		{object}	apicommon.TransactionData
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/transactions [post]
func (a *API) signTxHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// read the request body
	signReq := &apicommon.TransactionData{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		errors.ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}

	// check if the user has a role in the organization
	if !user.HasRoleFor(signReq.Address.String(), db.AnyRole) {
		errors.ErrUnauthorized.With("user has no role in the organization").Write(w)
		return
	}

	// get the organization info from the database with the address provided in
	// the request
	org, err := a.db.Organization(signReq.Address.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Withf("organization not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get organization: %v", err).Write(w)
		return
	}

	// get the user signer from secret, organization creator and organization nonce
	organizationSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore the signer of the organization: %v", err).Write(w)
		return
	}

	// check if the request includes a payload to sign
	if signReq.TxPayload == nil {
		errors.ErrMalformedBody.Withf("missing data field in request body").Write(w)
		return
	}

	// unmarshal the tx with the protobuf model
	tx := &models.Tx{}
	if err := proto.Unmarshal(signReq.TxPayload, tx); err != nil {
		errors.ErrInvalidTxFormat.Write(w)
		return
	}

	// fund the tx with a faucet package
	var txType *models.TxType
	tx, txType, err = a.account.FundTransaction(tx, organizationSigner.Address())
	if err != nil {
		err.(errors.Error).Write(w)
		return
	}
	if txType == nil {
		errors.ErrInvalidTxFormat.With("missing tx type").Write(w)
		return
	}

	// check if the user has permission depending on the tx type
	if hasPermission, err := a.subscriptions.HasTxPermission(tx, *txType, org, user); !hasPermission || err != nil {
		errors.ErrUnauthorized.Withf("user does not have permission to sign transactions: %v", err).Write(w)
		return
	}

	// if isNewProcess and everything went well so far update the organization process counter
	if *txType == models.TxType_NEW_PROCESS {
		org.Counters.Processes++
		if err := a.db.SetOrganization(org); err != nil {
			errors.ErrGenericInternalServerError.Withf("could not update organization process counter: %v", err).Write(w)
			return
		}
	}

	// sign the tx
	stx, err := a.account.SignTransaction(tx, organizationSigner)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not sign transaction: %v", err).Write(w)
		return
	}

	// return the signed tx payload
	apicommon.HTTPWriteJSON(w, &apicommon.TransactionData{
		TxPayload: stx,
	})
}

// signMessageHandler godoc
//
//	@Summary		Sign a message
//	@Description	Sign a message with the organization's private key. The user must have admin role for the organization.
//	@Tags			transactions
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.MessageSignature	true	"Message to sign"
//	@Success		200		{object}	apicommon.MessageSignature
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/transactions/message [post]
func (a *API) signMessageHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// read the message from the request body
	signReq := &apicommon.MessageSignature{}
	if err := json.NewDecoder(r.Body).Decode(signReq); err != nil {
		errors.ErrMalformedBody.Withf("could not decode request body: %v", err).Write(w)
		return
	}
	// check if the request includes a payload to sign
	if signReq.Payload == nil {
		errors.ErrMalformedBody.Withf("missing payload field in request body").Write(w)
		return
	}
	// check if the user has the admin role for the organization
	if !user.HasRoleFor(signReq.Address, db.AdminRole) {
		errors.ErrUnauthorized.With("user does not have admin role").Write(w)
		return
	}
	// get the organization info from the database with the address provided in
	// the request
	org, err := a.db.Organization(signReq.Address)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Withf("organization not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get organization: %v", err).Write(w)
		return
	}
	// get the user signer from secret, organization creator and organization nonce
	organizationSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore the signer of the organization: %v", err).Write(w)
		return
	}
	// sign the message
	signature, err := account.SignMessage(signReq.Payload, organizationSigner)
	if err != nil {
		errors.ErrGenericInternalServerError.With("could not sign message").Write(w)
		return
	}
	// return the signature
	apicommon.HTTPWriteJSON(w, &apicommon.MessageSignature{
		Signature: signature,
	})
}
