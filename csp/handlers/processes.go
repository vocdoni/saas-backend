package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/vochain/state"
)

// parseProcessID parses the {processId} URL param (a voting-process Mongo ObjectID) and
// returns both the ObjectID and its bytes, which are used as the CSP token anchor.
func parseProcessID(w http.ResponseWriter, r *http.Request) (primitive.ObjectID, internal.HexBytes, bool) {
	oid, err := primitive.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return primitive.NilObjectID, nil, false
	}
	return oid, internal.HexBytes(oid[:]), true
}

// getVotingProcess loads a voting process by id, writing the proper error on failure.
func (c *CSPHandlers) getVotingProcess(w http.ResponseWriter, oid primitive.ObjectID) (*db.VotingProcess, bool) {
	vp, err := c.mainDB.VotingProcess(oid)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("process not found").Write(w)
			return nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}
	return vp, true
}

// memberEligibleForQuestion reports whether a member may sign a question. An empty
// eligibility subset means every census member is eligible.
func memberEligibleForQuestion(q *db.VotingProcessQuestion, memberID string) bool {
	if len(q.EligibleMemberIDs) == 0 {
		return true
	}
	for _, id := range q.EligibleMemberIDs {
		if id == memberID {
			return true
		}
	}
	return false
}

// ProcessAuthHandler godoc
//
//	@Summary		Authenticate a voter for a voting process
//	@Description	Two-step voter authentication for a multi-question voting process (the /processes
//	@Description	replacement of the bundle auth flow); the issued token is anchored to the process.
//	@Description	- Step 0: handlers.AuthRequest — member identification fields (name, surname,
//	@Description	memberNumber, nationalId, birthDate, email, phone); which are required depends on the
//	@Description	census auth configuration. If valid, a challenge is sent and a token returned.
//	@Description	- Step 1: handlers.AuthChallengeRequest — { authToken, authData: [challenge solution] }.
//	@Description	If valid the token is marked verified and returned. Auth-only censuses may not require
//	@Description	a challenge solution.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string					true	"Process ID"
//	@Param			step		path		string					true	"Authentication step (0 or 1)"
//	@Param			request		body		handlers.AuthRequest	true	"Step 0 body; step 1 uses AuthChallengeRequest (see description)"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, cooldown not reached, or invalid challenge"
//	@Failure		404			{object}	errors.Error	"Process, census, organization, or participant not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/auth/{step} [post]
func (c *CSPHandlers) ProcessAuthHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	step, ok := parseAuthStep(w, r)
	if !ok {
		return
	}
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return
	}
	c.handleAuthStep(w, r, step, anchor, vp.CensusID.Hex())
}

// ProcessAuthResendHandler godoc
//
//	@Summary		Resend a voting process auth challenge
//	@Description	Resend the challenge for an existing (non-verified) authentication token of a voting
//	@Description	process. The request must include the auth token and a valid contact method for the
//	@Description	census type (email/phone). The same token is returned if the challenge is queued.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"Process ID"
//	@Param			request		body		handlers.AuthResendRequest	true	"Resend request with auth token and contact data"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Malformed body, missing auth token, invalid contact, or token already verified"
//	@Failure		401			{object}	errors.Error	"Invalid/expired token, token not belonging to the process, or contact mismatch"
//	@Failure		404			{object}	errors.Error	"Census or organization not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/auth/resend [post]
func (c *CSPHandlers) ProcessAuthResendHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return
	}
	var req AuthResendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if len(req.AuthToken) == 0 {
		errors.ErrInvalidData.Withf("missing auth token").Write(w)
		return
	}
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !bytes.Equal(anchor, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}
	org, err := c.mainDB.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrOrganizationNotFound.WithErr(err).Write(w)
		return
	}
	member, ok := c.orgMemberFromAuth(w, vp.OrgAddress, auth)
	if !ok {
		return
	}
	lang := apicommon.DefaultLang
	if l, ok := r.Context().Value(apicommon.LangMetadataKey).(string); ok && l != "" {
		lang = l
	}
	toDestination, challengeType, err := determineContactMethod(
		census, org, &AuthRequest{Email: req.Email, Phone: req.Phone}, member,
	)
	if err != nil {
		if apiErr, ok := err.(errors.Error); ok {
			apiErr.Write(w)
		} else {
			errors.ErrUnauthorized.WithErr(err).Write(w)
		}
		return
	}
	name, logo := orgNameAndLogo(org)
	if err := c.csp.ResendChallenge(req.AuthToken, toDestination, challengeType, lang, name, logo, org.Address); err != nil {
		writeResendError(w, err)
		return
	}
	apicommon.HTTPWriteJSON(w, &AuthResponse{AuthToken: req.AuthToken})
}

// ProcessSignHandler godoc
//
//	@Summary		Sign a ballot for a voting process question
//	@Description	Sign a voter's ballot for one question's on-chain election. Requires a verified token
//	@Description	bound to the process; authorizes the member against the question's eligibility subset
//	@Description	and consumes the per-election signing slot (a question cannot be signed twice).
//	@Description	Body: authToken, electionId (the question's on-chain election id) and payload (the voter
//	@Description	address). tokenR is unused.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string					true	"Process ID"
//	@Param			request		body		handlers.SignRequest	true	"Sign request (see description for fields)"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, election not in process, or member not eligible"
//	@Failure		404			{object}	errors.Error	"Process, census, or user not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/sign [post]
func (c *CSPHandlers) ProcessSignHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return
	}
	req, ok := parseSignRequest(w, r)
	if !ok {
		return
	}
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}
	if !bytes.Equal(anchor, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return
	}
	// resolve the target question by its on-chain election id and verify it belongs to
	// this process
	question, err := c.mainDB.QuestionByUpstreamID(req.ProcessID)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if err != nil || question.ProcessID != oid {
		errors.ErrUnauthorized.Withf("election not found in process").Write(w)
		return
	}
	// authorize the member against the question's eligibility subset
	if !memberEligibleForQuestion(question, auth.UserID.String()) {
		errors.ErrUnauthorized.Withf("member not eligible for this question").Write(w)
		return
	}
	member, ok := c.orgMemberFromAuth(w, vp.OrgAddress, auth)
	if !ok {
		return
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}
	weight := uint64(1)
	if census.Weighted {
		if member.Weight == 0 {
			errors.ErrZeroWeightVoter.Write(w)
			return
		}
		weight = member.Weight
	}
	address, ok := parseAddress(w, req.Payload)
	if !ok {
		return
	}
	c.signAndRespond(w, req.AuthToken, *address, question.UpstreamID, big.NewInt(int64(weight)).Bytes())
}

// ProcessWeightHandler godoc
//
//	@Summary		Get a voter's weight for a voting process
//	@Description	Return the voter's weight for a voting process. Requires a verified token bound to the
//	@Description	process.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"Process ID"
//	@Param			request		body		handlers.UserWeightRequest	true	"Request with auth token"
//	@Success		200			{object}	handlers.UserWeightResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Invalid token, token not verified, or token not belonging to the process"
//	@Failure		404			{object}	errors.Error	"Process, user, or census not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/weight [post]
func (c *CSPHandlers) ProcessWeightHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return
	}
	var req UserWeightRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !bytes.Equal(anchor, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return
	}
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}
	member, ok := c.orgMemberFromAuth(w, vp.OrgAddress, auth)
	if !ok {
		return
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return
	}
	weight := uint64(1)
	if census.Weighted {
		weight = member.Weight
	}
	apicommon.HTTPWriteJSON(w, &UserWeightResponse{Weight: internal.HexBytes(big.NewInt(int64(weight)).Bytes())})
}

// ProcessCheckHandler godoc
//
//	@Summary		Check a voter's status for a voting process
//	@Description	Report the voter's status for a voting process: census membership, weight, and per
//	@Description	question eligibility and vote status. The voter is identified solely by the auth token
//	@Description	(the only voter data the client stores); the token must be verified and issued for this
//	@Description	process. Ineligibility is reported as belongsToProcess=false with HTTP 200, not an error.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		handlers.CheckMembershipRequest	true	"Request with auth token"
//	@Success		200			{object}	handlers.ProcessCheckResponse
//	@Failure		400			{object}	errors.Error	"Malformed body or missing auth token"
//	@Failure		404			{object}	errors.Error	"Process or census not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/check [post]
func (c *CSPHandlers) ProcessCheckHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return
	}
	var req CheckMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !bytes.Equal(anchor, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return
	}
	memberID := auth.UserID.String()
	resp := &ProcessCheckResponse{}
	if _, err := c.mainDB.CensusParticipant(vp.CensusID.Hex(), memberID); err == nil {
		resp.BelongsToProcess = true
	} else if err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if member, err := c.orgMember(vp.OrgAddress, auth); err == nil {
		census, cErr := c.mainDB.Census(vp.CensusID.Hex())
		weight := uint64(1)
		if cErr == nil && census.Weighted {
			weight = member.Weight
		}
		resp.Weight = internal.HexBytes(big.NewInt(int64(weight)).Bytes())
	}
	questions, err := c.mainDB.QuestionsByProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	for i := range questions {
		q := &questions[i]
		status := ProcessQuestionStatus{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			// a voter can only vote a question if they are a participant of the process
			// census AND fall within the question's eligibility subset
			CanVote: resp.BelongsToProcess && memberEligibleForQuestion(q, memberID),
		}
		if len(q.UpstreamID) > 0 {
			if cspProc, err := c.mainDB.CSPProcessByUserAndProcess(auth.UserID, q.UpstreamID); err == nil {
				status.HasVoted = cspProc.Used
			}
		}
		resp.Questions = append(resp.Questions, status)
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// orgMemberFromAuth resolves the org member referenced by an auth token, writing the
// proper error on failure.
func (c *CSPHandlers) orgMemberFromAuth(
	w http.ResponseWriter, orgAddress common.Address, auth *db.CSPAuth,
) (*db.OrgMember, bool) {
	member, err := c.orgMember(orgAddress, auth)
	if err != nil {
		errors.ErrUserNotFound.WithErr(err).Write(w)
		return nil, false
	}
	return member, true
}

// orgMember resolves the org member referenced by an auth token (member ObjectID hex).
func (c *CSPHandlers) orgMember(orgAddress common.Address, auth *db.CSPAuth) (*db.OrgMember, error) {
	oid, err := primitive.ObjectIDFromHex(auth.UserID.String())
	if err != nil {
		return nil, fmt.Errorf("invalid user ID in token: %w", err)
	}
	return c.mainDB.OrgMember(orgAddress, oid.Hex())
}

// orgNameAndLogo returns the organization display name and logo, falling back to defaults.
func orgNameAndLogo(org *db.Organization) (name, logo string) {
	name, logo = DefaultOrgName, DefaultOrgLogo
	if n, ok := org.Meta["name"].(string); ok {
		name = n
		if l, ok := org.Meta["logo"].(string); ok {
			logo = l
		}
	}
	return name, logo
}

// writeResendError maps a ResendChallenge error to the proper HTTP error.
func writeResendError(w http.ResponseWriter, err error) {
	if apiErr, ok := err.(errors.Error); ok {
		apiErr.Write(w)
		return
	}
	switch err {
	case csp.ErrInvalidAuthToken, csp.ErrTokenExpired:
		errors.ErrUnauthorized.WithErr(err).Write(w)
	case csp.ErrStorageFailure:
		errors.ErrInternalStorageError.WithErr(err).Write(w)
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
	}
}

// ProcessSignInfoHandler godoc
//
//	@Summary		Get a voter's consumed sign info for a voting process
//	@Description	Per-question consumed address, nullifier and timestamp for the voter identified
//	@Description	by a verified CSP auth token. Only questions the voter has already voted are
//	@Description	returned. This is the /processes replacement of the single-election sign-info.
//	@Description	Public endpoint (the token authenticates the voter).
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		handlers.ConsumedAddressRequest	true	"Auth token"
//	@Success		200			{object}	handlers.ProcessSignInfoResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/sign-info [post]
func (c *CSPHandlers) ProcessSignInfoHandler(w http.ResponseWriter, r *http.Request) {
	oid, anchor, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	if _, ok := c.getVotingProcess(w, oid); !ok {
		return
	}
	var req ConsumedAddressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	auth, ok := c.getAuthInfo(w, req.AuthToken)
	if !ok {
		return
	}
	if !bytes.Equal(anchor, auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return
	}
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return
	}
	questions, err := c.mainDB.QuestionsByProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &ProcessSignInfoResponse{Consumed: []QuestionConsumedAddress{}}
	for i := range questions {
		q := &questions[i]
		if len(q.UpstreamID) == 0 {
			continue // question not yet on chain
		}
		cspProc, err := c.mainDB.CSPProcessByUserAndProcess(auth.UserID, q.UpstreamID)
		if err != nil || !cspProc.Used {
			continue // this voter has not consumed this question
		}
		resp.Consumed = append(resp.Consumed, QuestionConsumedAddress{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			Address:    cspProc.UsedAddress,
			Nullifier:  state.GenerateNullifier(common.BytesToAddress(cspProc.UsedAddress), q.UpstreamID),
			At:         cspProc.UsedAt,
		})
	}
	apicommon.HTTPWriteJSON(w, resp)
}
