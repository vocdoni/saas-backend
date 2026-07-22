package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// relayVoteHandler godoc
//
//	@Summary		Relay an already-signed vote to the Vochain
//	@Description	Relays a voter transaction that has already been signed by the voter to the
//	@Description	Vochain. The body carries a marshaled models.SignedTx whose inner Tx is a Vote
//	@Description	envelope; the target process is taken from that envelope, so no process id is
//	@Description	passed in the path. Public endpoint: no authentication is required. The request is
//	@Description	checked synchronously — the body must decode to a Vote envelope (else 400) for a
//	@Description	process the backend knows (else 404) — then enqueued for submission on a background
//	@Description	worker; the call returns 202 with a job id. The chain's acceptance or rejection of
//	@Description	the vote (proof, nullifier, election state) is decided when the worker submits it and
//	@Description	reported on the job: poll GET /jobs/{jobId} for the voteID on success, or a failure.
//	@Tags			vote
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.RelayVoteRequest	true	"Signed vote transaction payload"
//	@Success		202		{object}	apicommon.EnqueuedResponse	"Job accepted; poll GET /jobs/{jobId}"
//	@Failure		400		{object}	errors.Error				"Invalid input data"
//	@Failure		404		{object}	errors.Error				"Process not found"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Failure		503		{object}	errors.Error				"Transaction queue is full"
//	@Router			/vote [post]
func (a *API) relayVoteHandler(w http.ResponseWriter, r *http.Request) {
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
	// the target process is the one named in the signed vote envelope.
	pid := internal.HexBytes(vote.ProcessId)
	if len(pid) == 0 {
		errors.ErrInvalidTxFormat.With("vote has no process id").Write(w)
		return
	}

	// ensure we manage this election, resolving its owning organization. Legacy elections
	// live in the processes collection; new multi-question elections are questions of a
	// voting process, resolved by their on-chain (upstream) id.
	// TODO: remove the legacy processes lookup once /processes is the only path.
	var orgAddress common.Address
	process, err := a.db.ProcessByAddress(pid)
	switch err {
	case nil:
		orgAddress = process.OrgAddress
	case db.ErrNotFound:
		question, qErr := a.db.QuestionByUpstreamID(pid)
		switch qErr {
		case nil:
			orgAddress = question.OrgAddress
		case db.ErrNotFound:
			errors.ErrProcessNotFound.Write(w)
			return
		default:
			errors.ErrGenericInternalServerError.WithErr(qErr).Write(w)
			return
		}
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// submit + confirm on the worker pool; the vote nullifier (voteID) is recorded on the
	// job. The structural checks above ran synchronously (a malformed vote got a 400, an
	// unknown process a 404); the chain's acceptance of the vote is decided here, async, and
	// surfaced on the job.
	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeRelayVote, orgAddress); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	payload := req.TxPayload
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		voteID, err := a.account.SubmitSignedTx(payload)
		if err != nil {
			return nil, err
		}
		// meter the relayed vote for billing/quota; best-effort, never fail the relay on a
		// counter error. The quota itself is enforced at election publish, not here.
		if err := a.db.IncrementOrganizationSentVotesCounter(orgAddress); err != nil {
			log.Errorw(err, "failed to increment org sent votes counter")
		}
		return &db.JobResult{VoteID: internal.HexBytes(voteID)}, nil
	}}) {
		// full queue: mark the job failed so it is not orphaned pending.
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}

	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// setProcessStatusHandler godoc
//
//	@Summary		Change an on-chain election status
//	@Description	Changes the status of an on-chain election (ready|paused|ended|canceled). The
//	@Description	backend builds a SET_PROCESS_STATUS transaction, funds and signs it with the
//	@Description	organization signer synchronously, then submits and confirms it on a background
//	@Description	worker; the call returns 202 with a job id. Poll GET /jobs/{jobId} for the result.
//	@Description	Requires Manager/Admin role of the organization that owns the process.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"24-hex ProcessID"
//	@Param			request		body		apicommon.SetProcessStatusRequest	true	"New process status"
//	@Success		202			{object}	apicommon.EnqueuedResponse			"Job accepted; poll GET /jobs/{jobId}"
//	@Failure		400			{object}	errors.Error						"Invalid input data"
//	@Failure		401			{object}	errors.Error						"Unauthorized"
//	@Failure		404			{object}	errors.Error						"Process not found"
//	@Failure		500			{object}	errors.Error						"Internal server error"
//	@Failure		503			{object}	errors.Error						"Transaction queue is full"
//	@Deprecated
//	@Router	/process/{processId}/status [put]
func (a *API) setProcessStatusHandler(w http.ResponseWriter, r *http.Request) {
	objID, err := primitive.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
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

	process, err := a.db.Process(objID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// only a published process has an on-chain election whose status can be changed.
	if len(process.Address) == 0 {
		errors.ErrProcessNotFound.Withf("process not published").Write(w)
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

	orgSigner, err := a.organizationSigner(org)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}

	// serialize build->sign->submit per organization so a concurrent status change or
	// publish for the same org cannot read the same account nonce and sign a conflicting
	// tx. The worker releases the lock after submit (held across the async hand-off);
	// every synchronous failure below releases it via the deferred unlock.
	orgLock := a.orgTxLocks.lock(org.Address)
	lockHeld := true
	defer func() {
		if lockHeld {
			orgLock.Unlock()
		}
	}()

	tx, err := a.account.BuildSetProcessStatusTx(orgSigner.Address(), process.Address.Bytes(), status)
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

	// quota / permission (same engine as the /transactions and publish paths)
	if hasPermission, err := a.subscriptions.HasTxPermission(fundedTx, *txType, org, user); !hasPermission || err != nil {
		errors.ErrUnauthorized.Withf("user does not have permission to change process status: %v", err).Write(w)
		return
	}

	// sign with the organization signer
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not sign status tx: %v", err).Write(w)
		return
	}

	// submit + wait on the worker pool; on success persist the new cached status
	// (canonical uppercase enum name e.g. "PAUSED").
	newStatus := strings.ToUpper(req.Status)
	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessStatus, process.OrgAddress); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		if _, err := a.account.SubmitSignedTx(stx); err != nil {
			return nil, err
		}
		process.Status = newStatus
		if _, err := a.db.SetProcess(process); err != nil {
			return nil, err
		}
		return &db.JobResult{Status: newStatus}, nil
	}}) {
		// full queue: mark the job failed so it is not orphaned pending; the deferred
		// unlock fires on return.
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	lockHeld = false

	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}
