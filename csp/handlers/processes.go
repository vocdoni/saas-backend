package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/vochain/state"
)

// apiErr lifts an API error to a pointer, so a helper can signal "no error" with nil. Named
// asErr (not apiErr) because handlers.go has several `apiErr, ok := err.(errors.Error)` locals
// that would shadow a same-named helper.
func asErr(e errors.Error) *errors.Error { return &e }

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
	oid, _, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	req, ok := parseSignRequest(w, r)
	if !ok {
		return
	}
	sc, ok := c.resolveSignContext(w, oid, req.AuthToken)
	if !ok {
		return
	}
	upstreamID, sErr := c.authorizeQuestion(sc, req.ProcessID)
	if sErr != nil {
		sErr.Write(w)
		return
	}
	address, ok := parseAddress(w, req.Payload)
	if !ok {
		return
	}
	c.signAndRespond(w, req.AuthToken, *address, upstreamID, sc.weight)
}

// signContext is the part of a ballot signing request that is resolved once per call, from
// the process named in the path and the auth token in the body: the voting process, the
// member behind the token and their census weight. A batch signs every ballot under one of
// these, which is the whole point of the batch endpoint.
type signContext struct {
	process  *db.VotingProcess
	memberID string
	weight   internal.HexBytes
}

// resolveSignContext runs the per-request half of a ballot signing request: load the voting
// process, check the auth token is verified and anchored to it, resolve the org member behind
// it and compute their census weight. It writes the proper error and returns false on failure.
func (c *CSPHandlers) resolveSignContext(
	w http.ResponseWriter, oid primitive.ObjectID, authToken internal.HexBytes,
) (*signContext, bool) {
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
	if !bytes.Equal(oid[:], auth.BundleID) {
		errors.ErrUnauthorized.Withf("token does not belong to the process").Write(w)
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
	return &signContext{
		process:  vp,
		memberID: auth.UserID.String(),
		weight:   weightBytes(weight),
	}, true
}

// authorizeQuestion runs the per-ballot half of a signing request: resolve the target question
// by its on-chain election id, verify it belongs to this process and authorize the member
// against the question's eligibility subset. It returns the question's on-chain election id, or
// the API error to write back, never both.
func (c *CSPHandlers) authorizeQuestion(
	sc *signContext, electionID internal.HexBytes,
) (internal.HexBytes, *errors.Error) {
	// an empty id cannot name an election, and db.QuestionByUpstreamID reports it as
	// ErrInvalidData, which the branch below would surface to the client as a 500. The message is
	// field-neutral because the single-sign endpoint sends this same id as "electionId".
	if len(electionID) == 0 {
		return nil, asErr(errors.ErrMalformedBody.With("missing election id"))
	}
	question, err := c.mainDB.QuestionByUpstreamID(electionID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, asErr(errors.ErrGenericInternalServerError.WithErr(err))
	}
	if err != nil || question.ProcessID != sc.process.ID {
		return nil, asErr(errors.ErrUnauthorized.Withf("election not found in process"))
	}
	if !memberEligibleForQuestion(question, sc.memberID) {
		return nil, asErr(errors.ErrUnauthorized.Withf("member not eligible for this question"))
	}
	return question.UpstreamID, nil
}

// maxSignBatchBodyBytes bounds a POST /processes/{processId}/sign-batch body. One ballot is an
// election id plus a voter address, ~200 characters of JSON once hex-encoded and quoted, so 512
// bytes each leaves ample headroom. The cap tracks db.MaxQuestionsPerProcess (a voter holds at
// most one ballot per question); like the relay endpoints this one is public, so the body has to
// be bounded before it is buffered.
const maxSignBatchBodyBytes = db.MaxQuestionsPerProcess*512 + 4<<10

// authorizedBallot is one item of a sign batch that survived the authorization pass: the
// question's on-chain election id, resolved from the request, and the voter address.
type authorizedBallot struct {
	upstreamID internal.HexBytes
	address    internal.HexBytes
}

// ProcessSignBatchHandler godoc
//
//	@Summary		Sign every question's ballot of a voting process in one call
//	@Description	Batch form of POST /processes/{processId}/sign. A multi-question voting process is
//	@Description	one on-chain election per question, so a voter holds one ballot per question and
//	@Description	signing them one by one costs a round trip each. This signs them all under a single
//	@Description	verified auth token. Public endpoint: the token authenticates the voter.
//	@Description	Each ballot names a question by its on-chain election id (upstreamId, as returned by
//	@Description	the check and sign-info endpoints) and the voter address to sign for it.
//	@Description	The batch is authorized as a unit and signs nothing on failure — every ballot must
//	@Description	name an election of this process (else 401) the member is eligible for (else 401),
//	@Description	carry a 20-byte voter address (else 400), and no election may be repeated (else
//	@Description	400). Authorization strictly precedes the first signature, so a rejected batch
//	@Description	consumes nothing.
//	@Description	Once authorized, every ballot is signed and the response is always 200 with one
//	@Description	entry per request item, in order — even if every ballot fails (the request was
//	@Description	honored; the outcome is per item). Each entry carries a signature and weight, or a
//	@Description	stable machine-readable code plus a message. Retry ONLY the entries that carry a
//	@Description	code: re-sending the whole batch re-signs the ballots that already succeeded, and
//	@Description	each re-sign counts toward the election's finite overwrite budget
//	@Description	(MaxVoteOverwritesPerProcess, 10) — past it the election is permanently locked.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"Process ID"
//	@Param			request		body		handlers.SignBatchRequest	true	"Auth token and one ballot per question"
//	@Success		200			{object}	handlers.SignBatchResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, election not in process, or member not eligible"
//	@Failure		404			{object}	errors.Error	"Census or user not found"
//	@Failure		413			{object}	errors.Error	"Request body too large"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/sign-batch [post]
func (c *CSPHandlers) ProcessSignBatchHandler(w http.ResponseWriter, r *http.Request) {
	oid, _, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	req, ok := parseSignBatchRequest(w, r)
	if !ok {
		return
	}
	// the size checks need only the parsed body, so they run before the four DB lookups of
	// resolveSignContext — an empty or oversized batch never touches the database. The cap is the
	// shared question limit, not the per-process QuestionIDs count: that field is empty for
	// processes predating it (see questionSetProblem), which would otherwise make this endpoint
	// reject every batch for them, including a single ballot.
	switch {
	case len(req.Ballots) == 0:
		errors.ErrVoteBatchEmpty.Write(w)
		return
	case len(req.Ballots) > db.MaxQuestionsPerProcess:
		errors.ErrVoteBatchTooLarge.Withf("%d ballots, the maximum is %d",
			len(req.Ballots), db.MaxQuestionsPerProcess).Write(w)
		return
	}
	sc, ok := c.resolveSignContext(w, oid, req.AuthToken)
	if !ok {
		return
	}

	// authorize the whole batch before signing anything: signing consumes a per-election slot
	// that cannot be given back, so a rejected batch must leave the voter with nothing signed
	// rather than with a signed prefix.
	ballots := make([]authorizedBallot, len(req.Ballots))
	seen := make(map[string]int, len(req.Ballots))
	for i, item := range req.Ballots {
		// a repeated election would contend with itself on the per-(user, election) signer
		// lock, and a voter has at most one ballot per question anyway.
		if first, dup := seen[string(item.UpstreamID)]; dup {
			errors.ErrMalformedBody.Withf(
				"the ballot at index %d repeats the election of index %d", i, first,
			).Write(w)
			return
		}
		seen[string(item.UpstreamID)] = i
		upstreamID, sErr := c.authorizeQuestion(sc, item.UpstreamID)
		if sErr != nil {
			sErr.Withf("at index %d", i).Write(w)
			return
		}
		// the address is signed into the CA bundle and pinned as the election's consumer, after
		// which a different address is rejected forever — so validate its length here. HexBytes
		// accepts any length and pads odd input, which is exactly the silent corruption to stop.
		if len(item.Address) != common.AddressLength {
			errors.ErrMalformedBody.Withf(
				"the ballot at index %d has an address of %d bytes, expected %d",
				i, len(item.Address), common.AddressLength,
			).Write(w)
			return
		}
		ballots[i] = authorizedBallot{upstreamID: upstreamID, address: item.Address}
	}

	// ponytail: signed sequentially. db.ConsumeCSPProcess takes the storage write lock, so a
	// parallel fan-out inside one request would serialize on it anyway; revisit only if that
	// lock stops being the bottleneck.
	resp := &SignBatchResponse{Signatures: make([]SignBatchResult, len(ballots))}
	for i, b := range ballots {
		res := &resp.Signatures[i]
		res.UpstreamID = b.upstreamID
		signature, err := c.csp.Sign(req.AuthToken, b.address, b.upstreamID, sc.weight,
			signers.SignerTypeECDSASalted)
		if err != nil {
			// per-ballot by nature: this election's signing slot is spent, or a concurrent
			// request holds its lock. Map it to a stable code + sanitized message for the
			// response; the raw error stays in the log.
			res.Code, res.Error = signOutcome(err)
			logSignFailure(oid, sc.memberID, b.upstreamID, err)
			continue
		}
		res.Signature = signature
		res.Weight = sc.weight
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// signOutcome maps a per-ballot signing error to a stable, machine-readable code and a message
// safe for the public response body. The raw error (which for signer failures is
// errors.Join(ErrSign, <internal detail>)) is kept out of the response and logged instead.
func signOutcome(err error) (code, message string) {
	switch {
	case errors.Is(err, csp.ErrProcessAlreadyConsumed):
		return "already_consumed", "this election's signing slot is already consumed"
	case errors.Is(err, csp.ErrUserAlreadySigning):
		return "already_signing", "a concurrent request is signing this election"
	default:
		return "sign_failed", "could not sign the ballot"
	}
}

// logSignFailure records why one ballot of a batch could not be signed. A spent signing slot and
// a concurrent request for the same election are the normal outcomes of a voter retrying or of
// two tabs racing, and the caller is told about them in the response anyway, so they are debug —
// a warn per occurrence would bury the storage and signer failures that do need attention. The
// member and process ids make a warn attributable during an incident.
func logSignFailure(oid primitive.ObjectID, memberID string, upstreamID internal.HexBytes, err error) {
	if errors.Is(err, csp.ErrProcessAlreadyConsumed) || errors.Is(err, csp.ErrUserAlreadySigning) {
		log.Debugw("skipped a batch ballot", "procId", oid.Hex(), "member", memberID,
			"electionId", upstreamID, "reason", err)
		return
	}
	log.Warnw("could not sign a batch ballot", "procId", oid.Hex(), "member", memberID,
		"electionId", upstreamID, "error", err)
}

// parseSignBatchRequest decodes a capped sign-batch body and checks it carries an auth token,
// writing the proper error and returning false on failure.
func parseSignBatchRequest(w http.ResponseWriter, r *http.Request) (*SignBatchRequest, bool) {
	var req SignBatchRequest
	if apiErr := apicommon.DecodeCappedJSON(w, r, &req, maxSignBatchBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return nil, false
	}
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return nil, false
	}
	return &req, true
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
	apicommon.HTTPWriteJSON(w, &UserWeightResponse{Weight: weightBytes(weight)})
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
		resp.Weight = weightBytes(weight)
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
