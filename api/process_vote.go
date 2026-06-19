package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// relayVoteHandler godoc
//
//	@Summary		Relay an already-signed vote to the Vochain
//	@Description	Relays a voter transaction that has already been signed by the voter to the
//	@Description	Vochain and returns the resulting vote nullifier (voteID). The body carries a
//	@Description	marshaled models.SignedTx whose inner Tx is a Vote envelope. Public endpoint: no
//	@Description	authentication is required. The handler never decodes the vote package nor exposes
//	@Description	the transaction hash; only the nullifier is returned.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"On-chain process id (hex)"
//	@Param			request		body		apicommon.RelayVoteRequest	true	"Signed vote transaction payload"
//	@Success		200			{object}	apicommon.RelayVoteResponse	"Vote nullifier"
//	@Failure		400			{object}	errors.Error				"Invalid input data"
//	@Failure		404			{object}	errors.Error				"Process not found"
//	@Failure		500			{object}	errors.Error				"Internal server error"
//	@Router			/process/{processId}/vote [post]
func (a *API) relayVoteHandler(w http.ResponseWriter, r *http.Request) {
	var pid internal.HexBytes
	if err := pid.ParseString(chi.URLParam(r, "processId")); err != nil || len(pid) == 0 {
		errors.ErrMalformedURLParam.Withf("invalid process id").Write(w)
		return
	}

	var req apicommon.RelayVoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if len(req.TxPayload) == 0 {
		errors.ErrMalformedBody.Withf("missing txPayload").Write(w)
		return
	}

	signedTx := &models.SignedTx{}
	if err := proto.Unmarshal(req.TxPayload, signedTx); err != nil {
		errors.ErrInvalidTxFormat.Withf("could not decode signed tx: %v", err).Write(w)
		return
	}
	innerTx := &models.Tx{}
	if err := proto.Unmarshal(signedTx.Tx, innerTx); err != nil {
		errors.ErrInvalidTxFormat.Withf("could not decode tx: %v", err).Write(w)
		return
	}

	vote := innerTx.GetVote()
	if vote == nil {
		errors.ErrInvalidTxFormat.With("not a vote tx").Write(w)
		return
	}
	if !bytes.Equal(vote.ProcessId, pid.Bytes()) {
		errors.ErrInvalidTxFormat.With("vote process id does not match url").Write(w)
		return
	}

	// ensure we manage this process
	if _, err := a.db.ProcessByAddress(pid); err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// submit + confirm; data is the vote nullifier (voteID)
	voteID, err := a.account.SubmitSignedTx(req.TxPayload)
	if err != nil {
		if apiErr, ok := err.(errors.Error); ok {
			apiErr.Write(w)
			return
		}
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.RelayVoteResponse{VoteID: internal.HexBytes(voteID)})
}

// setProcessStatusHandler godoc
//
//	@Summary		Change an on-chain election status
//	@Description	Changes the status of an on-chain election (ready|paused|ended|canceled). The
//	@Description	backend builds a SET_PROCESS_STATUS transaction, funds, signs it with the
//	@Description	organization signer, submits and confirms it. Requires Manager/Admin role of the
//	@Description	organization that owns the process.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"On-chain process id (hex)"
//	@Param			request		body		apicommon.SetProcessStatusRequest	true	"New process status"
//	@Success		200			{object}	apicommon.SetProcessStatusResponse	"Updated process status"
//	@Failure		400			{object}	errors.Error						"Invalid input data"
//	@Failure		401			{object}	errors.Error						"Unauthorized"
//	@Failure		404			{object}	errors.Error						"Process not found"
//	@Failure		500			{object}	errors.Error						"Internal server error"
//	@Router			/process/{processId}/status [put]
func (a *API) setProcessStatusHandler(w http.ResponseWriter, r *http.Request) {
	var pid internal.HexBytes
	if err := pid.ParseString(chi.URLParam(r, "processId")); err != nil || len(pid) == 0 {
		errors.ErrMalformedURLParam.Withf("invalid process id").Write(w)
		return
	}

	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	var req apicommon.SetProcessStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	var status models.ProcessStatus
	switch strings.ToLower(req.Status) {
	case "ready":
		status = models.ProcessStatus_READY
	case "paused":
		status = models.ProcessStatus_PAUSED
	case "ended":
		status = models.ProcessStatus_ENDED
	case "canceled":
		status = models.ProcessStatus_CANCELED
	default:
		errors.ErrMalformedBody.With("invalid status").Write(w)
		return
	}

	process, err := a.db.ProcessByAddress(pid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// permission: Manager or Admin of the owning organization
	if !user.HasRoleFor(process.OrgAddress, db.ManagerRole) && !user.HasRoleFor(process.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
		return
	}

	org, err := a.db.Organization(process.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}

	tx, err := a.account.BuildSetProcessStatusTx(orgSigner.Address(), pid.Bytes(), status)
	if err != nil {
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}

	// fund
	fundedTx, txType, err := a.account.FundTransaction(tx, orgSigner.Address())
	if err != nil {
		if apiErr, ok := err.(errors.Error); ok {
			apiErr.Write(w)
			return
		}
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}
	if txType == nil || *txType != models.TxType_SET_PROCESS_STATUS {
		errors.ErrInvalidTxFormat.With("unexpected tx type for status change").Write(w)
		return
	}

	// sign with the organization signer
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not sign status tx: %v", err).Write(w)
		return
	}

	// submit + wait
	if _, err := a.account.SubmitSignedTx(stx); err != nil {
		errors.ErrVochainRequestFailed.WithErr(err).Write(w)
		return
	}

	// update the cached status and persist (canonical uppercase enum name e.g. "PAUSED")
	process.Status = strings.ToUpper(req.Status)
	if _, err := a.db.SetProcess(process); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.SetProcessStatusResponse{Status: process.Status})
}
