package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
)

// errCensusNotAttested answers a request that only applies to an attested census.
const errCensusNotAttested = "the process census is not attested"

// updateAttestedCensusSizeHandler godoc
//
//	@Summary		Grow the declared size of an attested census
//	@Description	Raise the maxCensusSize of a published process whose census is attested. The size only
//	@Description	grows; the growth is priced as adding members is (402 past what was paid, 422 when it
//	@Description	needs a custom quote). Every open question is resized on chain by an async job (202 with
//	@Description	jobId); 200 when no question needs it. Requires Manager/Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Voting process ID"
//	@Param			request		body		apicommon.AttestedCensusSizeRequest	true	"New declared size"
//	@Success		200			{object}	apicommon.AttestedCensusSizeResponse
//	@Success		202			{object}	apicommon.AttestedCensusSizeResponse
//	@Failure		400			{object}	errors.Error	"Not an attested census, or the size shrinks"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		402			{object}	errors.Error	"Census growth beyond the price paid"
//	@Failure		409			{object}	errors.Error	"Process is not published"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/processes/{processId}/census/size [put]
func (a *API) updateAttestedCensusSizeHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	if !vp.Published {
		errors.ErrDuplicateConflict.Withf("process is not published; edit the draft via PUT /processes/{processId}").Write(w)
		return
	}
	var req apicommon.AttestedCensusSizeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode the census size").Write(w)
		return
	}
	if req.Size <= 0 || req.Size > pricing.MaxCensusSize {
		errors.ErrInvalidData.Withf("size must be between 1 and %d", pricing.MaxCensusSize).Write(w)
		return
	}
	census, err := a.growAttestedCensus(r, vp, req.Size)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	targets := make([]censusSizeTarget, 0, len(questions))
	for i := range questions {
		if isOpenStatus(questions[i].Status) {
			targets = append(targets, censusSizeTarget{question: questions[i], census: census, size: uint64(census.Size)})
		}
	}
	jobID, err := a.enqueueSetProcessCensus(vp.OrgAddress, targets)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	resp := &apicommon.AttestedCensusSizeResponse{Size: census.Size, JobID: jobID}
	if jobID == "" {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// growAttestedCensus prices and stores the new declared size of vp's attested census. Like
// addProcessCensusMembers, the check and the write are one step under the organization's growth
// lock, released before the caller enqueues the on-chain resize.
func (a *API) growAttestedCensus(r *http.Request, vp *db.VotingProcess, size int64) (*db.Census, error) {
	growthLock, err := a.censusGrowthLocks.lockOrBusy(r.Context(), vp.OrgAddress)
	if err != nil {
		return nil, err
	}
	defer growthLock.Unlock()
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		return nil, err
	}
	if census.Attested == nil {
		return nil, errors.ErrInvalidData.With(errCensusNotAttested)
	}
	if size < census.Size {
		return nil, errors.ErrInvalidData.Withf("the census size can only grow, it is %d", census.Size)
	}
	if err := a.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, size: size}); err != nil {
		return nil, err
	}
	if err := a.db.GrowAttestedCensusSize(census.ID, size); err != nil {
		if stderrors.Is(err, db.ErrInvalidData) {
			return nil, errors.ErrInvalidData.With("the census size can only grow")
		}
		return nil, err
	}
	census.Size = size
	return census, nil
}

// updateAttestationSignerHandler godoc
//
//	@Summary		Rotate the attestation signer of an attested census
//	@Description	Make signer the only address whose attestations the CSP accepts for the process, and
//	@Description	append it to the signer history. Tokens issued under an earlier signer stop signing
//	@Description	(40808); voters authenticate again with an attestation of the new signer. Works on a
//	@Description	draft or a published process. Requires Manager/Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Voting process ID"
//	@Param			request		body		apicommon.AttestationSignerRequest	true	"New signer"
//	@Success		200			{object}	apicommon.AttestedCensusSpec
//	@Failure		400			{object}	errors.Error	"Not an attested census, or a zero signer"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Router			/processes/{processId}/attestation-signer [put]
func (a *API) updateAttestationSignerHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	vp, _, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	var req apicommon.AttestationSignerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode the signer").Write(w)
		return
	}
	if req.Signer == (common.Address{}) {
		errors.ErrInvalidData.With("signer cannot be the zero address").Write(w)
		return
	}
	if err := a.db.RotateAttestationSigner(vp.CensusID, req.Signer); err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			errors.ErrInvalidData.With(errCensusNotAttested).Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	spec := apicommon.CensusSpec{Attested: &apicommon.AttestedCensusSpec{
		Signer:        census.Attested.Signer,
		MaxCensusSize: census.Size,
		SignerHistory: census.Attested.SignerHistory,
	}}
	a.echoAttestationDomain(&spec)
	apicommon.HTTPWriteJSON(w, spec.Attested)
}
