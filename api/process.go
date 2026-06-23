package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
)

// createProcessHandler godoc
//
//	@Summary		Create a new voting process
//	@Description	Create a new voting process (draft). Requires Manager/Admin role of the owning organization. The
//	@Description	request must reference the organization via either a `censusId` or an explicit `orgAddress`.
//	@Description	Creating a draft consumes the plan's draft permission.
//	@Description
//	@Description	The optional `electionParams` field carries the on-chain election definition (questions, dates,
//	@Description	vote/election types) used later to publish the draft on chain via POST /process/{processId}/publish.
//	@Description	It is additive: omitting it keeps the legacy draft behavior.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateProcessRequest	true	"Process creation information (optionally with electionParams)"
//	@Success		200		{object}	primitive.ObjectID				"Draft process ID (Mongo ObjectID)"
//	@Failure		400		{object}	errors.Error					"Invalid input data"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		403		{object}	errors.Error					"Plan does not allow creating more drafts"
//	@Failure		404		{object}	errors.Error					"Published census not found"
//	@Failure		409		{object}	errors.Error					"Process already exists"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/process [post]
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	// parse the process info from the request body
	processInfo := &apicommon.CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// if it's a draft process
	if processInfo.Address.Equals(nil) && processInfo.OrgAddress == (common.Address{}) {
		errors.ErrMalformedBody.Withf("draft processes must provide an org address").Write(w)
		return
	}

	// Create or update the process
	process := &db.Process{
		Metadata:       processInfo.Metadata,
		ElectionParams: processInfo.ElectionParams,
	}

	var orgAddress common.Address
	if processInfo.CensusID != nil {
		var err error
		census, err := a.db.Census(processInfo.CensusID.String())
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedURLParam.Withf("invalid census provided").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		process.Census = *census
		orgAddress = census.OrgAddress
	} else if processInfo.OrgAddress != (common.Address{}) {
		orgAddress = processInfo.OrgAddress
	} else {
		errors.ErrMalformedBody.Withf("either census ID or organization address must be provided").Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(orgAddress, db.ManagerRole) && !user.HasRoleFor(orgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	process.OrgAddress = orgAddress
	if !processInfo.Address.Equals(nil) {
		process.Address = processInfo.Address
	}

	// if it's a new draft process
	if process.Address.Equals(nil) && process.ID == primitive.NilObjectID {
		if err := a.subscriptions.OrgHasPermission(process.OrgAddress, subscriptions.CreateDraft); err != nil {
			if apierr, ok := err.(errors.Error); ok {
				apierr.Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}

	processID, err := a.db.SetProcess(process)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, processID)
}

// updateProcessHandler godoc
//
//	@Summary		Update an existing voting process
//	@Description	Update an existing draft voting process. Requires Manager/Admin role of the owning organization.
//	@Description	Only drafts that have not yet been published on chain can be updated; updating a published process
//	@Description	returns a 409. The optional `electionParams` field can be set or replaced here (additive), same as on
//	@Description	create.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string							true	"Draft process ID (Mongo ObjectID)"
//	@Param			request		body		apicommon.UpdateProcessRequest	true	"Process update information (optionally with electionParams)"
//	@Success		200			{string}	string							"OK"
//	@Failure		400			{object}	errors.Error					"Invalid input data"
//	@Failure		401			{object}	errors.Error					"Unauthorized"
//	@Failure		404			{object}	errors.Error					"Process not found"
//	@Failure		409			{object}	errors.Error					"Process already published and not in draft mode"
//	@Failure		500			{object}	errors.Error					"Internal server error"
//	@Router			/process/{processId} [put]
func (a *API) updateProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	// parse the process info from the request body
	processInfo := &apicommon.UpdateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	existingProcess, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check if it's a draft process and can be overwritten
	if !existingProcess.Address.Equals(nil) {
		errors.ErrDuplicateConflict.Withf("process already exists and is not in draft mode").Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(existingProcess.OrgAddress, db.ManagerRole) &&
		!user.HasRoleFor(existingProcess.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
		return
	}

	var census *db.Census
	if !processInfo.CensusID.Equals(nil) {
		census, err = a.db.Census(processInfo.CensusID.String())
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedURLParam.Withf("census not found").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		existingProcess.Census = *census
	}

	if len(processInfo.Metadata) > 0 {
		existingProcess.Metadata = processInfo.Metadata
	}

	if processInfo.ElectionParams != nil {
		existingProcess.ElectionParams = processInfo.ElectionParams
	}

	if !processInfo.Address.Equals(nil) {
		existingProcess.Address = processInfo.Address
	}

	_, err = a.db.SetProcess(existingProcess)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}

// processInfoHandler godoc
//
//	@Summary		Get process information
//	@Description	Retrieve voting process information by ID. Returns process details including census and metadata.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	db.Process
//	@Failure		400			{object}	errors.Error	"Invalid process ID"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId} [get]
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	process, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, process)
}

// organizationListProcessDraftsHandler godoc
//
//	@Summary		Get paginated list of process drafts
//	@Description	Returns a list of voting process drafts.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	apicommon.ListOrganizationProcesses
//	@Failure		404	{object}	errors.Error	"Process not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/processes/drafts [get]
func (a *API) organizationListProcessDraftsHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	// retrieve the orgMembers with pagination
	totalItems, processes, err := a.db.ListProcesses(org.Address, params.Page, params.Limit, db.DraftOnly)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get processes: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationProcesses{
		Pagination: pagination,
		Processes:  processes,
	})
}

// deleteProcessHandler godoc
//
//	@Summary		Delete a voting process
//	@Description	Delete a voting process. Requires Manager/Admin role.
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string			true	"Process ID"
//	@Success		200			{string}	string			"OK"
//	@Failure		400			{object}	errors.Error	"Invalid process ID"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/process/{processId} [delete]
func (a *API) deleteProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	existingProcess, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Withf("process not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(existingProcess.OrgAddress, db.ManagerRole) &&
		!user.HasRoleFor(existingProcess.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization that owns this process").Write(w)
		return
	}

	err = a.db.DelProcess(parsedID)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}

// publishProcessHandler godoc
//
//	@Summary		Publish a draft process as an on-chain election
//	@Description	Publishes an existing draft process (created via POST /process) as an on-chain
//	@Description	election. The backend builds the election metadata and NewProcess transaction,
//	@Description	funds and signs it (with the organization signer) synchronously, then submits and
//	@Description	confirms it on a background worker; the call returns 202 with a job id. Poll GET
//	@Description	/jobs/{jobId} for the resulting on-chain id. Requires Admin role. Idempotent: if
//	@Description	the draft is already published its on-chain id is returned with 200 without sending
//	@Description	a new transaction. Publishing under a managed organization additionally enforces the
//	@Description	integrator's per-org and aggregate election/census quotas.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Draft process ID"
//	@Success		202			{object}	apicommon.EnqueuedResponse			"Publish accepted; poll GET /jobs/{jobId}"
//	@Success		200			{object}	apicommon.PublishProcessResponse	"Already published (idempotent): on-chain id and status"
//	@Failure		400			{object}	errors.Error						"Invalid input data"
//	@Failure		401			{object}	errors.Error						"Unauthorized"
//	@Failure		404			{object}	errors.Error						"Process not found"
//	@Failure		409			{object}	errors.Error						"A publish is already in progress for this draft"
//	@Failure		500			{object}	errors.Error						"Internal server error"
//	@Failure		503			{object}	errors.Error						"Transaction queue is full"
//	@Router			/process/{processId}/publish [post]
func (a *API) publishProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if processID == "" {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}
	parsedID, err := primitive.ObjectIDFromHex(processID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}

	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	draft, err := a.db.Process(parsedID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrProcessNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// permission: Admin of the owning organization. Publishing maps to a NEW_PROCESS
	// tx, which subscriptions.HasTxPermission requires Admin for, so we enforce the
	// same role here rather than letting a Manager pass and be rejected downstream.
	if !user.HasRoleFor(draft.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization that owns this process").Write(w)
		return
	}

	// idempotent: already published
	if !draft.Address.Equals(nil) {
		apicommon.HTTPWriteJSON(w, &apicommon.PublishProcessResponse{Address: draft.Address, Status: draft.Status})
		return
	}

	// a draft must carry the high-level election definition to be publishable.
	// the on-chain census root is always the CSP public key, so the draft census
	// (member list used later by the CSP bundle flow) is not required here.
	if draft.ElectionParams == nil {
		errors.ErrMalformedBody.Withf("draft has no election params").Write(w)
		return
	}

	// atomically claim the draft for publishing BEFORE any expensive metadata/funding/
	// signing work. This single conditional update is the authoritative duplicate-publish
	// guard: two concurrent publishes cannot both win the claim, so only one NEW_PROCESS
	// tx is ever built for a draft.
	claimed, err := a.db.ClaimProcessForPublish(parsedID)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		// lost the claim: either another request is mid-publish, or it already finished.
		if cur, e := a.db.Process(parsedID); e == nil && !cur.Address.Equals(nil) {
			apicommon.HTTPWriteJSON(w, &apicommon.PublishProcessResponse{Address: cur.Address, Status: cur.Status})
			return
		}
		errors.ErrPublishInProgress.Write(w)
		return
	}

	// from here the draft is in PUBLISHING. Until a worker owns the job, every failure
	// path must release the claim, otherwise the draft is stuck (the release must $unset
	// status, since a zero-value string write is dropped by the dynamic update helper).
	committed := false
	defer func() {
		if committed {
			return
		}
		if e := a.db.ClearProcessPublishing(parsedID); e != nil {
			log.Warnw("could not clear publishing state after failed publish", "error", e)
		}
	}()

	org, err := a.db.Organization(draft.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// if this org is managed by an integrator, atomically reserve the integrator's
	// aggregate process/census quota BEFORE building the on-chain tx, so concurrent
	// publishes cannot each pass a stale check and exceed the cap. The reservation is
	// rolled back (deferred) unless the publish commits. Test-sized elections are exempt
	// from the integrator quota, mirroring the per-org Processes counter exemption below.
	managedReserved := false
	var integratorAddr common.Address
	nonTestSized := draft.ElectionParams.MaxCensusSize > uint64(db.TestMaxCensusSize)
	if org.ManagedBy != (common.Address{}) && nonTestSized {
		integrator, err := a.db.Organization(org.ManagedBy)
		if err != nil {
			errors.ErrGenericInternalServerError.Withf("could not get integrator organization: %v", err).Write(w)
			return
		}
		limits, err := a.subscriptions.EffectiveIntegratorLimits(integrator)
		if err != nil {
			if apiErr, ok := err.(errors.Error); ok {
				apiErr.Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		if err := a.db.ReserveManagedPublish(integrator.Address,
			limits.MaxManagedProcesses, limits.MaxManagedCensusSize, int(draft.ElectionParams.MaxCensusSize)); err != nil {
			if err == db.ErrManagedQuotaReached {
				errors.ErrIntegratorQuotaExceeded.Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		managedReserved = true
		integratorAddr = integrator.Address
		// roll back the reservation if the publish is not handed to a worker (any
		// synchronous failure below, or a full queue). Once enqueued the worker owns
		// the reservation outcome and clears managedReserved.
		defer func() {
			if !managedReserved {
				return
			}
			if e := a.db.AddOrganizationManagedProcesses(integratorAddr, -1); e != nil {
				log.Warnw("could not roll back managed processes counter", "error", e)
			}
			if e := a.db.AddOrganizationManagedCensusSize(integratorAddr,
				-int64(draft.ElectionParams.MaxCensusSize)); e != nil {
				log.Warnw("could not roll back managed census counter", "error", e)
			}
		}()
	}

	orgSigner, err := account.OrganizationSigner(a.secret, org.Creator, org.Nonce)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not restore organization signer: %v", err).Write(w)
		return
	}

	cspPubKey, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get csp public key: %v", err).Write(w)
		return
	}

	// build + store the election metadata content-addressed; its public URL is the
	// on-chain metadata pointer (must be known before the tx, so it cannot contain
	// the not-yet-known process id).
	metaBytes, err := account.BuildElectionMetadata(draft.ElectionParams)
	if err != nil {
		errors.ErrMalformedBody.Withf("invalid election params: %v", err).Write(w)
		return
	}
	objectName, err := a.objectStorage.PutJSON(metaBytes, user.Email)
	if err != nil {
		errors.ErrInternalStorageError.WithErr(err).Write(w)
		return
	}
	metadataURL := fmt.Sprintf("%s/storage/%s", a.serverURL, objectName)

	// serialize build->sign->submit per organization so a concurrent publish or status
	// change for the same org cannot read the same account nonce and sign a conflicting
	// tx. The worker releases the lock after submit (held across the async hand-off);
	// every synchronous failure below releases it via the deferred unlock.
	orgLock := a.orgTxLocks.lock(org.Address)
	lockHeld := true
	defer func() {
		if lockHeld {
			orgLock.Unlock()
		}
	}()

	// build the NewProcess tx (CSP census)
	tx, err := a.account.BuildNewProcessTx(&account.NewProcessParams{
		OrgAddress:  draft.OrgAddress,
		Params:      draft.ElectionParams,
		CensusRoot:  cspPubKey,
		CensusURI:   a.serverURL,
		MetadataURL: metadataURL,
	})
	if err != nil {
		errors.ErrMalformedBody.Withf("could not build election: %v", err).Write(w)
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
	if txType == nil || *txType != models.TxType_NEW_PROCESS {
		errors.ErrInvalidTxFormat.With("unexpected tx type for publish").Write(w)
		return
	}

	// quota / permission (same engine as the /transactions path)
	if hasPermission, err := a.subscriptions.HasTxPermission(fundedTx, *txType, org, user); !hasPermission || err != nil {
		errors.ErrUnauthorized.Withf("user does not have permission to publish: %v", err).Write(w)
		return
	}

	// sign with the organization signer
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not sign election tx: %v", err).Write(w)
		return
	}

	jobID, err := apicommon.NewJobID()
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypePublishProcess, org.Address); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// submit + confirm on the worker pool. On success the draft becomes READY with its
	// on-chain id; on failure the PUBLISHING claim is released and any managed reservation
	// is rolled back. The idempotency guard keys off draft.Address, so a retry after
	// failure cannot create a duplicate election.
	reserved := managedReserved
	censusSize := int64(draft.ElectionParams.MaxCensusSize)
	if !a.enqueueTx(txTask{jobID: jobID, run: func() (*db.JobResult, error) {
		defer orgLock.Unlock()
		data, err := a.account.SubmitSignedTx(stx)
		if err != nil {
			if e := a.db.ClearProcessPublishing(parsedID); e != nil {
				log.Warnw("could not clear publishing state after failed publish", "error", e)
			}
			if reserved {
				if e := a.db.AddOrganizationManagedProcesses(integratorAddr, -1); e != nil {
					log.Warnw("could not roll back managed processes counter", "error", e)
				}
				if e := a.db.AddOrganizationManagedCensusSize(integratorAddr, -censusSize); e != nil {
					log.Warnw("could not roll back managed census counter", "error", e)
				}
			}
			return nil, err
		}
		draft.Address = internal.HexBytes(data)
		draft.Status = "READY"
		draft.PublishedAt = time.Now()
		if _, err := a.db.SetProcess(draft); err != nil {
			return nil, err
		}
		// best-effort per-org Processes counter; only count non-test-sized elections.
		if nonTestSized {
			if err := a.db.IncrementOrganizationProcessesCounter(org.Address); err != nil {
				log.Warnw("could not update organization process counter", "error", err)
			}
		}
		return &db.JobResult{Address: draft.Address, Status: "READY"}, nil
	}}) {
		// full queue: mark the job failed so it is not orphaned pending; the deferred
		// unlock, publishing-claim release and reservation rollback all fire on return.
		if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
			log.Warnw("could not mark job failed after full queue", "error", e)
		}
		errors.ErrTxQueueFull.Write(w)
		return
	}
	// handed to a worker: it now owns the publish claim, reservation and org lock.
	committed = true
	managedReserved = false
	lockHeld = false

	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, &apicommon.EnqueuedResponse{JobID: jobID})
}
