package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
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
//	@Failure		413		{object}	errors.Error				"Request body too large"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Failure		503		{object}	errors.Error				"Transaction queue is full"
//	@Router			/vote [post]
func (a *API) relayVoteHandler(w http.ResponseWriter, r *http.Request) {
	var req apicommon.RelayVoteRequest
	if err := decodeCappedJSON(w, r, &req, maxVoteBodyBytes); err != nil {
		err.Write(w)
		return
	}

	vote, apiErr := a.parseRelayVote(req.TxPayload)
	if apiErr != nil {
		apiErr.Write(w)
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
	// seed the job with what is already known about the envelope, so a caller polling a
	// pending — or ultimately failed — job still learns which vote it is waiting on.
	seed := &db.JobResult{ProcessID: vote.processID, Nullifier: vote.nullifier}
	if err := a.db.CreateTxJobWithResult(jobID, db.JobTypeRelayVote, vote.org, seed); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !a.enqueueTx(txTask{jobID: jobID, run: a.submitRelayVote(vote)}) {
		// full queue: mark the job failed so it is not orphaned pending.
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}

	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

const (
	// maxVotesPerBatch bounds a POST /votes call. It matches the cap of the vochain batch
	// transaction endpoint and equals txQueueSize, so the largest accepted batch can at most
	// fill an empty queue.
	maxVotesPerBatch = 100
	// maxVoteBodyBytes bounds the request body of a single relayed envelope. A CSP-signed vote
	// envelope marshals to ~300 bytes, ~600 characters once hex-encoded into the JSON field, so
	// this leaves an order of magnitude of headroom for larger proofs while keeping these public,
	// unauthenticated endpoints from reading an unbounded body.
	maxVoteBodyBytes = 8 << 10
	// maxVotesBodyBytes bounds a whole batch: one envelope allowance each, plus slack for the
	// JSON framing around them.
	maxVotesBodyBytes = maxVotesPerBatch*maxVoteBodyBytes + 4<<10
)

// decodeCappedJSON reads at most limit bytes of the request body and decodes them into dst,
// returning the API error to write back on a body that is too large or not the expected JSON.
// The relay endpoints are public, so the body has to be bounded before it is buffered.
func decodeCappedJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) *errors.Error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if stderrors.As(err, &tooLarge) {
			return asPtr(errors.ErrRequestBodyTooLarge.Withf("the limit is %d bytes", limit))
		}
		return asPtr(errors.ErrMalformedBody)
	}
	return nil
}

// relayVotesHandler godoc
//
//	@Summary		Relay a batch of already-signed votes to the Vochain
//	@Description	Relays several voter transactions, already signed by the voter, in one call. A
//	@Description	multi-question voting process is one vochain process per question, so a voter casts
//	@Description	one vote transaction per question; relaying them one by one leaves a window in which
//	@Description	some are on chain and the rest are not, and the voter ends up half-voted. This
//	@Description	endpoint closes that window. Public endpoint: no authentication is required.
//	@Description	The whole batch is checked synchronously and is accepted or rejected as a unit —
//	@Description	every payload must decode to a Vote envelope (else 400) for a process the backend
//	@Description	knows (else 404), all of them must belong to the same organization (else 400), and
//	@Description	the queue must have room for all of them (else 503). A rejected batch enqueues
//	@Description	nothing. At most 100 votes per call.
//	@Description	The call returns 202 with a single job id covering the batch. Poll
//	@Description	GET /jobs/{jobId}: its result carries one entry per vote, index-aligned with the
//	@Description	request, each with the process id and the nullifier — both known before submission,
//	@Description	so they are readable while the job is pending — plus the chain-assigned voteID once
//	@Description	that vote is accepted, or the reason it failed.
//	@Tags			vote
//	@Accept			json
//	@Produce		json
//	@Param			request	body		apicommon.RelayVotesRequest	true	"Signed vote transaction payloads"
//	@Success		202		{object}	apicommon.EnqueuedResponse	"Job accepted; poll GET /jobs/{jobId}"
//	@Failure		400		{object}	errors.Error				"Invalid input data"
//	@Failure		404		{object}	errors.Error				"Process not found"
//	@Failure		413		{object}	errors.Error				"Request body too large"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Failure		503		{object}	errors.Error				"Transaction queue is full"
//	@Router			/votes [post]
func (a *API) relayVotesHandler(w http.ResponseWriter, r *http.Request) {
	var req apicommon.RelayVotesRequest
	if err := decodeCappedJSON(w, r, &req, maxVotesBodyBytes); err != nil {
		err.Write(w)
		return
	}
	switch {
	case len(req.Votes) == 0:
		errors.ErrVoteBatchEmpty.Write(w)
		return
	case len(req.Votes) > maxVotesPerBatch:
		errors.ErrVoteBatchTooLarge.Withf("%d votes, the maximum is %d", len(req.Votes), maxVotesPerBatch).Write(w)
		return
	}

	// validate the whole batch before enqueuing anything: one bad envelope rejects the
	// batch and leaves the voter with nothing relayed, which is recoverable, rather than
	// with a relayed prefix, which is not.
	votes := make([]*parsedVote, len(req.Votes))
	seenNullifier := make(map[string]int, len(req.Votes))
	for i, item := range req.Votes {
		vote, apiErr := a.parseRelayVote(item.TxPayload)
		if apiErr != nil {
			apiErr.Withf("at vote index %d", i).Write(w)
			return
		}
		// every vote is metered against, and the job belongs to, a single organization.
		// The questions of a voting process share one, so a mixed batch is a client bug.
		if i > 0 && vote.org != votes[0].org {
			errors.ErrVoteBatchMixedOrganizations.Withf(
				"vote index %d targets %s, vote index 0 targets %s", i, vote.org, votes[0].org).Write(w)
			return
		}
		if len(vote.nullifier) > 0 {
			if first, dup := seenNullifier[vote.nullifier.String()]; dup {
				errors.ErrInvalidTxFormat.Withf(
					"vote index %d repeats the nullifier of vote index %d", i, first).Write(w)
				return
			}
			seenNullifier[vote.nullifier.String()] = i
		}
		votes[i] = vote
	}

	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	seed := make([]db.VoteJobResult, len(votes))
	tasks := make([]txTask, len(votes))
	for i, vote := range votes {
		seed[i] = db.VoteJobResult{
			ProcessID: vote.processID,
			Nullifier: vote.nullifier,
			Status:    db.JobStatusPending,
		}
		tasks[i] = txTask{
			jobID:  jobID,
			run:    a.submitRelayVote(vote),
			record: a.recordBatchVote(jobID, i),
		}
	}
	if err := a.db.CreateVoteBatchJob(jobID, votes[0].org, seed); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// one task per envelope rather than one task submitting them all: each submit blocks
	// its worker until the tx is mined, so a single task would serialize the batch and hold
	// one worker for the whole of it. enqueueTxBatch takes all the slots or none.
	if !a.enqueueTxBatch(tasks) {
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}

	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}

// parsedVote is one validated vote envelope: the payload to relay plus everything the
// job needs to describe it before the chain has said anything about it.
type parsedVote struct {
	payload   internal.HexBytes
	processID internal.HexBytes
	org       common.Address
	nullifier internal.HexBytes
}

// parseRelayVote runs the synchronous checks of a vote relay: the payload must decode to
// a vote envelope naming a process this backend manages. It returns the resolved vote or
// the API error to write back, never both.
func (a *API) parseRelayVote(payload internal.HexBytes) (*parsedVote, *errors.Error) {
	if len(payload) == 0 {
		return nil, asPtr(errors.ErrMalformedBody.Withf("missing txPayload"))
	}

	signedTx := &models.SignedTx{}
	if err := proto.Unmarshal(payload, signedTx); err != nil {
		return nil, asPtr(errors.ErrInvalidTxFormat.Withf("could not decode signed tx: %v", err))
	}
	innerTx := &models.Tx{}
	if err := proto.Unmarshal(signedTx.Tx, innerTx); err != nil {
		return nil, asPtr(errors.ErrInvalidTxFormat.Withf("could not decode tx: %v", err))
	}

	vote := innerTx.GetVote()
	if vote == nil {
		return nil, asPtr(errors.ErrInvalidTxFormat.With("not a vote tx"))
	}
	// the target process is the one named in the signed vote envelope.
	pid := internal.HexBytes(vote.ProcessId)
	if len(pid) == 0 {
		return nil, asPtr(errors.ErrInvalidTxFormat.With("vote has no process id"))
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
			return nil, asPtr(errors.ErrProcessNotFound)
		default:
			return nil, asPtr(errors.ErrGenericInternalServerError.WithErr(qErr))
		}
	default:
		return nil, asPtr(errors.ErrGenericInternalServerError.WithErr(err))
	}

	return &parsedVote{
		payload:   payload,
		processID: pid,
		org:       orgAddress,
		nullifier: voteNullifier(signedTx, vote, pid, a.account.ChainID()),
	}, nil
}

// voteNullifier works out the nullifier of an envelope without submitting it, so a vote
// can be identified while its job is still pending and after it failed. An anonymous vote
// carries its own; otherwise it is hash(voter address + process id), the same value the
// chain returns as the voteID once it accepts the vote. Best effort: an envelope we
// cannot recover a signer from yields no nullifier and is relayed anyway — the chain, not
// this backend, decides whether a signature is valid.
func voteNullifier(signedTx *models.SignedTx, vote *models.VoteEnvelope, pid internal.HexBytes,
	chainID string,
) internal.HexBytes {
	if len(vote.Nullifier) > 0 {
		return internal.HexBytes(vote.Nullifier)
	}
	msg, _, err := ethereum.BuildVocdoniTransaction(signedTx.Tx, chainID)
	if err != nil {
		log.Debugw("could not build vote tx message to derive its nullifier", "error", err)
		return nil
	}
	addr, err := ethereum.AddrFromSignature(msg, signedTx.Signature)
	if err != nil {
		log.Debugw("could not recover the voter address to derive its nullifier", "error", err)
		return nil
	}
	return internal.HexBytes(state.GenerateNullifier(addr, pid))
}

// submitRelayVote builds the worker closure that relays one envelope: submit, wait for it
// to be mined, and meter it. The returned voteID is the vote nullifier as assigned on chain.
func (a *API) submitRelayVote(vote *parsedVote) func() (*db.JobResult, error) {
	return func() (*db.JobResult, error) {
		voteID, err := a.account.SubmitSignedTx(vote.payload)
		if err != nil {
			return nil, err
		}
		// meter the relayed vote for billing/quota; best-effort, never fail the relay on a
		// counter error. The quota itself is enforced at election publish, not here.
		if err := a.db.IncrementOrganizationSentVotesCounter(vote.org); err != nil {
			log.Errorw(err, "failed to increment org sent votes counter")
		}
		return &db.JobResult{
			ProcessID: vote.processID,
			Nullifier: vote.nullifier,
			VoteID:    internal.HexBytes(voteID),
		}, nil
	}
}

// recordBatchVote builds the outcome recorder for one envelope of a batch job: it writes
// that envelope's entry instead of closing the shared job, which only the last envelope
// to report does.
func (a *API) recordBatchVote(jobID string, index int) func(*db.JobResult, error) {
	return func(result *db.JobResult, runErr error) {
		var voteID internal.HexBytes
		var errMsg string
		if runErr != nil {
			errMsg = runErr.Error()
		} else if result != nil {
			voteID = result.VoteID
		}
		if err := a.db.RecordBatchVoteOutcome(jobID, index, voteID, errMsg); err != nil {
			log.Warnw("could not record batch vote outcome", "jobId", jobID, "index", index, "error", err)
		}
	}
}

// asPtr lifts an API error to a pointer, so parseRelayVote can signal "no error" with nil.
func asPtr(e errors.Error) *errors.Error { return &e }

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

	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
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
