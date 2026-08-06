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
// returns both the ObjectID and its bytes, which are used as the CSP token scope.
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
//	@Description	replacement of the bundle auth flow); the issued token is scoped to the process.
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
	oid, scope, ok := parseProcessID(w, r)
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
	c.handleAuthStep(w, r, step, scope, vp.CensusID.Hex())
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
	oid, scope, ok := parseProcessID(w, r)
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
	if !bytes.Equal(scope, auth.ScopeID) {
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
//	@Description	address).
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
	oid, scope, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	req, ok := parseSignRequest(w, r)
	if !ok {
		return
	}
	authz, ok := c.authorizeSign(w, oid, scope, req.AuthToken, req.ProcessID)
	if !ok {
		return
	}
	address, ok := parseAddress(w, req.Payload)
	if !ok {
		return
	}
	c.signAndRespond(
		w, req.AuthToken, *address, authz.question.UpstreamID,
		new(big.Int).SetUint64(authz.weight).Bytes(), authz.startTime,
	)
}

// ProcessSignAnonymousPrepareHandler godoc
//
//	@Summary		Prepare an anonymous ballot signature
//	@Description	Open an anonymous signing session for one question's on-chain election and return
//	@Description	the ephemeral point the voter blinds their ballot against, plus the CSP's
//	@Description	attestation of their weight. Authorization is identical to /sign: a verified token
//	@Description	bound to the process, the member eligible for the question.
//	@Description	Unlike /sign this grants no vote overwrites -- one anonymous signature per voter
//	@Description	per election.
//	@Description	tokenR is returned in go-blindsecp256k1's own 33-byte compressed encoding, which
//	@Description	is not SEC1; parse it with that library.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		handlers.AnonymousSignPrepareRequest	true	"Auth token and election id"
//	@Success		200			{object}	handlers.AnonymousSignPrepareResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, ineligible member, or already signed"
//	@Failure		404			{object}	errors.Error	"Process, census, or user not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/sign/anonymous/prepare [post]
func (c *CSPHandlers) ProcessSignAnonymousPrepareHandler(w http.ResponseWriter, r *http.Request) {
	oid, scope, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	var req AnonymousSignPrepareRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}
	authz, ok := c.authorizeSign(w, oid, scope, req.AuthToken, req.ProcessID)
	if !ok {
		return
	}

	tokenR, weightCert, err := c.csp.PrepareBlindSign(req.AuthToken, authz.question.UpstreamID, authz.weight, authz.startTime)
	if err != nil {
		writeBlindSignError(w, err)
		return
	}
	apicommon.HTTPWriteJSON(w, &AnonymousSignPrepareResponse{
		TokenR:     tokenR,
		Weight:     new(big.Int).SetUint64(authz.weight).Bytes(),
		WeightCert: weightCert,
	})
}

// ProcessSignAnonymousHandler godoc
//
//	@Summary		Sign a ballot anonymously
//	@Description	Blind-sign a ballot for one question's on-chain election. The payload is the
//	@Description	hex-encoded blinded message, which the CSP cannot read -- it never learns the
//	@Description	address the voter casts with, and records none.
//	@Description	The response is the blinded scalar; the client unblinds it and puts the result in
//	@Description	an ECDSA_BLIND_PIDSALTED proof.
//	@Description	tokenR must be the point returned by the prepare step.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		handlers.AnonymousSignRequest	true	"Auth token, election id, tokenR and blinded payload"
//	@Success		200			{object}	handlers.AuthResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data, or a blinded message that must be blinded again"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, ineligible member, no live session, or already signed"
//	@Failure		404			{object}	errors.Error	"Process, census, or user not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/sign/anonymous [post]
func (c *CSPHandlers) ProcessSignAnonymousHandler(w http.ResponseWriter, r *http.Request) {
	oid, scope, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	var req AnonymousSignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}
	// The session is not authority on its own: re-run the full authorization.
	authz, ok := c.authorizeSign(w, oid, scope, req.AuthToken, req.ProcessID)
	if !ok {
		return
	}
	blindedMsg := new(internal.HexBytes)
	if err := blindedMsg.ParseString(req.Payload); err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}

	signature, err := c.csp.CompleteBlindSign(req.AuthToken, authz.question.UpstreamID, req.TokenR, *blindedMsg)
	if err != nil {
		writeBlindSignError(w, err)
		return
	}
	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signature})
}

// writeBlindSignError maps the CSP's blind-signing errors onto HTTP responses.
// ErrRetryBlinding is a 400 rather than a 401 because it is the client's message
// that is unusable, not its credentials -- and the session is still open, so
// retrying with a freshly blinded message is the correct response.
func writeBlindSignError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, csp.ErrRetryBlinding):
		errors.ErrMalformedBody.WithErr(err).Write(w)
	case errors.Is(err, csp.ErrAuthTokenNotVerified),
		errors.Is(err, csp.ErrInvalidAuthToken),
		errors.Is(err, csp.ErrProcessAlreadyConsumed),
		errors.Is(err, csp.ErrBlindSessionNotFound),
		errors.Is(err, csp.ErrUserAlreadySigning):
		errors.ErrUnauthorized.WithErr(err).Write(w)
	case errors.Is(err, csp.ErrInvalidSalt):
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
	}
}

// signAuthorization is what a signing request has proven: a verified token
// scoped to this process, a question that belongs to the process which the
// member is eligible for, and the weight they are entitled to.
type signAuthorization struct {
	question  *db.VotingProcessQuestion
	weight    uint64
	startTime uint32
}

// authorizeSign runs the checks every signing request must pass, whatever it
// then does with the signature. It lives in one place so the anonymous path
// cannot drift into enforcing less than the plain one.
func (c *CSPHandlers) authorizeSign(
	w http.ResponseWriter, oid primitive.ObjectID, scope, authToken, electionID internal.HexBytes,
) (*signAuthorization, bool) {
	vp, ok := c.getVotingProcess(w, oid)
	if !ok {
		return nil, false
	}
	auth, ok := c.getAuthInfo(w, authToken)
	if !ok {
		return nil, false
	}
	if !auth.Verified {
		errors.ErrUnauthorized.WithErr(csp.ErrAuthTokenNotVerified).Write(w)
		return nil, false
	}
	if !bytes.Equal(scope, auth.ScopeID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
		return nil, false
	}
	// resolve the target question by its on-chain election id and verify it belongs to
	// this process
	question, err := c.mainDB.QuestionByUpstreamID(electionID)
	if err != nil && err != db.ErrNotFound {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}
	if err != nil || question.ProcessID != oid {
		errors.ErrUnauthorized.Withf("election not found in process").Write(w)
		return nil, false
	}
	// authorize the member against the question's eligibility subset
	if !memberEligibleForQuestion(question, auth.UserID.String()) {
		errors.ErrUnauthorized.Withf("member not eligible for this question").Write(w)
		return nil, false
	}
	member, ok := c.orgMemberFromAuth(w, vp.OrgAddress, auth)
	if !ok {
		return nil, false
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrCensusNotFound.WithErr(err).Write(w)
		return nil, false
	}
	weight := uint64(1)
	if census.Weighted {
		if member.Weight == 0 {
			errors.ErrZeroWeightVoter.Write(w)
			return nil, false
		}
		weight = member.Weight
	}
	return &signAuthorization{question: question, weight: weight, startTime: uint32(vp.StartDate.Unix())}, true
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
	oid, scope, ok := parseProcessID(w, r)
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
	if !bytes.Equal(scope, auth.ScopeID) {
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
	oid, scope, ok := parseProcessID(w, r)
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
	if !bytes.Equal(scope, auth.ScopeID) {
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
	if n := org.DisplayName(); n != "" {
		name = n
		if l := org.LogoURL(); l != "" {
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
	oid, scope, ok := parseProcessID(w, r)
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
	if !bytes.Equal(scope, auth.ScopeID) {
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
		if err != nil {
			if errors.Is(err, db.ErrTokenNotFound) {
				continue // this voter has not consumed this question
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		if !cspProc.Used {
			continue // authenticated for this question but not consumed
		}
		consumed := QuestionConsumedAddress{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			At:         cspProc.UsedAt,
		}
		// An anonymously signed election has no recorded address. Deriving a
		// nullifier anyway would hash the zero address into a value that looks
		// real and belongs to nobody.
		if len(cspProc.UsedAddress) > 0 {
			consumed.Address = cspProc.UsedAddress
			consumed.Nullifier = state.GenerateNullifier(common.BytesToAddress(cspProc.UsedAddress), q.UpstreamID)
		}
		resp.Consumed = append(resp.Consumed, consumed)
	}
	apicommon.HTTPWriteJSON(w, resp)
}
