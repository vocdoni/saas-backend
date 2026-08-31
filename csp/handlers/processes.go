package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

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

// startGrace keeps a voter arriving right on the start boundary from being turned away. The
// comparison below is this host's clock against a stored date, and neither of them is the chain, so
// it has to err toward letting a real voter through.
const startGrace = time.Minute

// unvotableElection returns the 401 to write when the question's election does not currently
// accept a signature, or nil when it does.
//
// The CSP is the only place that can refuse on election state: the signature it issues carries no
// expiry, so a voter could otherwise bank one against a paused election and spend it the moment the
// chain opens.
func unvotableElection(vp *db.VotingProcess, q *db.VotingProcessQuestion) *errors.Error {
	if q.Status != db.QuestionStatusReady {
		return errors.Ptr(errors.ErrUnauthorized.Withf("question is not open for voting"))
	}
	// only StartDate is checked: publish always persists one, while EndDate is optional.
	if !vp.StartDate.IsZero() && time.Now().Add(startGrace).Before(vp.StartDate) {
		return errors.Ptr(errors.ErrUnauthorized.Withf("process has not started yet"))
	}
	return nil
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
	// a draft has no election to vote: authenticating against it would only mint a token that can
	// never be signed, and would burn the voter's email/SMS allowance doing so.
	if !vp.Published {
		errors.ErrUnauthorized.Withf("process is not published").Write(w)
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
	if sc.anonymous {
		errors.ErrMalformedBody.Withf("process uses a blind (anonymous) census; use the blind-point/blind-sign endpoints").Write(w)
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
	// anonymous is the census mode: true for a blind (OFF_CHAIN_CA_V2) census, which is served by
	// the two-round blind-point/blind-sign endpoints instead of the plain ECDSA sign endpoints.
	anonymous bool
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
	// Re-check census participation. The token was minted when the voter was in the census, and it
	// never expires, so without this a member removed from the census (or from the group that
	// backs it) would keep being signed for forever — this check is what makes the revocation
	// cascade actually revoke.
	if _, err := c.mainDB.CensusParticipant(vp.CensusID.Hex(), auth.UserID.String()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrUnauthorized.Withf("member is no longer part of the census").Write(w)
			return nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
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
		process:   vp,
		memberID:  auth.UserID.String(),
		weight:    weightBytes(weight),
		anonymous: census.Anonymous,
	}, true
}

// authorizeQuestion runs the per-ballot half of a signing request: resolve the target question
// by its on-chain election id, verify it belongs to this process and authorize the member
// against the question's eligibility subset. It returns the question's on-chain election id, or
// the API error to write back, never both. The batch handler resolves against a preloaded map
// instead (authorizeLoadedQuestion) to avoid one query per ballot.
func (c *CSPHandlers) authorizeQuestion(
	sc *signContext, electionID internal.HexBytes,
) (internal.HexBytes, *errors.Error) {
	// an empty id cannot name an election, and db.QuestionByUpstreamID reports it as
	// ErrInvalidData, which the branch below would surface to the client as a 500. The message is
	// field-neutral because the single-sign endpoint sends this same id as "electionId".
	if len(electionID) == 0 {
		return nil, errors.Ptr(errors.ErrMalformedBody.With("missing election id"))
	}
	question, err := c.mainDB.QuestionByUpstreamID(electionID)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return nil, errors.Ptr(errors.ErrGenericInternalServerError.WithErr(err))
	}
	if err != nil {
		question = nil // not found: authorizeLoadedQuestion folds it into the same 401
	}
	return authorizeLoadedQuestion(sc, question)
}

// authorizeLoadedQuestion authorizes one already-loaded question (nil means the election id
// resolved to nothing): it must belong to this process, currently accept a signature, and the
// member must be in its eligibility subset. Not-found and wrong-process collapse into the same
// 401 on purpose — a caller learns nothing about elections outside the process it is signing for.
func authorizeLoadedQuestion(
	sc *signContext, question *db.VotingProcessQuestion,
) (internal.HexBytes, *errors.Error) {
	if question == nil || question.ProcessID != sc.process.ID {
		return nil, errors.Ptr(errors.ErrUnauthorized.Withf("election not found in process"))
	}
	if sErr := unvotableElection(sc.process, question); sErr != nil {
		return nil, sErr
	}
	if !memberEligibleForQuestion(question, sc.memberID) {
		return nil, errors.Ptr(errors.ErrUnauthorized.Withf("member not eligible for this question"))
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
//	@Description	stable machine-readable code plus a message: already_consumed (that election's
//	@Description	signing slot is spent; not retryable), already_signing (a concurrent request holds
//	@Description	it; retry), auth_invalid (the token was invalidated mid-batch and every remaining
//	@Description	entry is stamped with it; authenticate again), address_mismatch (that election was
//	@Description	already signed for a different address; re-send with the address sign-info
//	@Description	reports) or sign_failed (internal failure; retry). Retry ONLY the entries that
//	@Description	carry a retryable code: re-sending the whole batch re-signs the ballots that
//	@Description	already succeeded, and each re-sign counts toward the election's finite overwrite
//	@Description	budget (MaxVoteOverwritesPerProcess, 10) — past it the election is permanently
//	@Description	locked.
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
	if sc.anonymous {
		errors.ErrMalformedBody.Withf("process uses a blind (anonymous) census; use the blind-point/blind-sign endpoints").Write(w)
		return
	}

	// authorize the whole batch before signing anything: signing consumes a per-election slot
	// that cannot be given back, so a rejected batch must leave the voter with nothing signed
	// rather than with a signed prefix. Every ballot must name a question of this process, so
	// all of them are loaded in one query and each ballot resolved against the map — a
	// per-ballot QuestionByUpstreamID would cost up to db.MaxQuestionsPerProcess sequential
	// round trips on a public endpoint.
	questions, err := c.mainDB.QuestionsByProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	byUpstream := make(map[string]*db.VotingProcessQuestion, len(questions))
	for i := range questions {
		byUpstream[string(questions[i].UpstreamID)] = &questions[i]
	}
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
		// an empty id cannot name an election; checked before the map so it gets its 400
		// rather than the map-miss 401.
		if len(item.UpstreamID) == 0 {
			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
			return
		}
		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
		if sErr != nil {
			sErr.Withf("at index %d", i).Write(w)
			return
		}
		if sErr := validateVoterAddress(item.Address); sErr != nil {
			sErr.Withf("at index %d", i).Write(w)
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
			// usually per-ballot: this election's signing slot is spent, or a concurrent
			// request holds its lock. Map it to a stable code + sanitized message for the
			// response; the raw error stays in the log.
			res.Code, res.Error = signOutcome(err)
			logSignFailure(oid, sc.memberID, b.upstreamID, err)
			// an invalid token dooms every remaining ballot too (the token was valid when
			// resolveSignContext ran, so it was deleted or unverified mid-batch): stamp the
			// rest with the same outcome instead of issuing one doomed Sign each.
			if res.Code == signCodeAuthInvalid {
				for j := i + 1; j < len(ballots); j++ {
					resp.Signatures[j].UpstreamID = ballots[j].upstreamID
					resp.Signatures[j].Code, resp.Signatures[j].Error = res.Code, res.Error
				}
				break
			}
			continue
		}
		res.Signature = signature
		res.Weight = sc.weight
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// blindBallotAuth is one item of a blind batch that survived the authorization pass: the
// question's on-chain election id, plus (round 2 only) the blinded message to sign for it.
type blindBallotAuth struct {
	upstreamID     internal.HexBytes
	blindedMessage internal.HexBytes
}

// ProcessBlindPointHandler godoc
//
//	@Summary		Issue blind points for a blind (anonymous) voting process (round 1)
//	@Description	Round 1 of the two-round blind CSP signing flow, for a process whose census is
//	@Description	anonymous (OFF_CHAIN_CA_V2). For each requested on-chain election id the CSP returns a
//	@Description	fresh blind point R (tokenR) the client uses to blind its CA bundle before calling
//	@Description	blind-sign, plus the CSP-authorized weight the client must carry in that bundle. The
//	@Description	batch is authorized as a unit (each election must belong to the process and the member
//	@Description	be eligible, else 401); per-election issuance failures are reported inline with a code.
//	@Description	Public endpoint: the verified auth token authenticates the voter. Rejected (400) for a
//	@Description	non-anonymous process — use the sign endpoints there.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"Process ID"
//	@Param			request		body		handlers.BlindPointRequest	true	"Auth token and one election id per question"
//	@Success		200			{object}	handlers.BlindPointResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data or non-anonymous process"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, election not in process, or member not eligible"
//	@Failure		413			{object}	errors.Error	"Request body too large"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/blind-point [post]
func (c *CSPHandlers) ProcessBlindPointHandler(w http.ResponseWriter, r *http.Request) {
	oid, _, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	var req BlindPointRequest
	if apiErr := apicommon.DecodeCappedJSON(w, r, &req, maxSignBatchBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}
	switch {
	case len(req.Elections) == 0:
		errors.ErrVoteBatchEmpty.Write(w)
		return
	case len(req.Elections) > db.MaxQuestionsPerProcess:
		errors.ErrVoteBatchTooLarge.Withf("%d elections, the maximum is %d",
			len(req.Elections), db.MaxQuestionsPerProcess).Write(w)
		return
	}
	sc, ok := c.resolveSignContext(w, oid, req.AuthToken)
	if !ok {
		return
	}
	if !sc.anonymous {
		errors.ErrMalformedBody.Withf("process is not anonymous; use the sign endpoints").Write(w)
		return
	}
	byUpstream, ok := c.questionsByUpstream(w, oid)
	if !ok {
		return
	}
	// authorize the whole batch before issuing any point, mirroring sign-batch: an ineligible or
	// out-of-process election fails the request rather than issuing a partial set.
	upstreamIDs := make([]internal.HexBytes, len(req.Elections))
	seen := make(map[string]int, len(req.Elections))
	for i, electionID := range req.Elections {
		if first, dup := seen[string(electionID)]; dup {
			errors.ErrMalformedBody.Withf("the election at index %d repeats index %d", i, first).Write(w)
			return
		}
		seen[string(electionID)] = i
		if len(electionID) == 0 {
			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
			return
		}
		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(electionID)])
		if sErr != nil {
			sErr.Withf("at index %d", i).Write(w)
			return
		}
		upstreamIDs[i] = upstreamID
	}

	resp := &BlindPointResponse{Points: make([]BlindPointResult, len(upstreamIDs))}
	for i, upstreamID := range upstreamIDs {
		res := &resp.Points[i]
		res.UpstreamID = upstreamID
		tokenR, pinnedWeight, err := c.csp.NewBlindRequest(req.AuthToken, upstreamID, sc.weight)
		if err != nil {
			res.Code, res.Error = signOutcome(err)
			logSignFailure(oid, sc.memberID, upstreamID, err)
			if res.Code == signCodeAuthInvalid {
				for j := i + 1; j < len(upstreamIDs); j++ {
					resp.Points[j].UpstreamID = upstreamIDs[j]
					resp.Points[j].Code, resp.Points[j].Error = res.Code, res.Error
				}
				break
			}
			continue
		}
		res.TokenR = tokenR
		// report the PINNED weight (idempotent across a re-arm), not the live sc.weight, so the
		// (R, weight) pair the client blinds always matches what round 2 salts with.
		res.Weight = pinnedWeight
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// ProcessBlindSignHandler godoc
//
//	@Summary		Blind-sign every ballot of a blind (anonymous) voting process (round 2)
//	@Description	Round 2 of the two-round blind CSP signing flow. For each election the client sends
//	@Description	the CA-bundle hash already blinded with the round-1 blind point; the CSP blind-signs it
//	@Description	without ever seeing the voter address, which is what keeps the ballot unlinkable. The
//	@Description	batch is authorized as a unit (same rules as sign-batch) and then each ballot is signed,
//	@Description	consuming that election's slot; the response is always 200 with one entry per item, in
//	@Description	order, carrying the raw blind-signature scalar or a stable code (blind_request_missing if
//	@Description	no round-1 point was issued; the other signing codes as usual). Public endpoint. Rejected
//	@Description	(400) for a non-anonymous process — use the sign endpoints there.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string						true	"Process ID"
//	@Param			request		body		handlers.BlindSignRequest	true	"Auth token and one blinded ballot per question"
//	@Success		200			{object}	handlers.BlindSignResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data or non-anonymous process"
//	@Failure		401			{object}	errors.Error	"Unauthorized, unverified token, election not in process, or member not eligible"
//	@Failure		413			{object}	errors.Error	"Request body too large"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/blind-sign [post]
func (c *CSPHandlers) ProcessBlindSignHandler(w http.ResponseWriter, r *http.Request) {
	oid, _, ok := parseProcessID(w, r)
	if !ok {
		return
	}
	var req BlindSignRequest
	if apiErr := apicommon.DecodeCappedJSON(w, r, &req, maxSignBatchBodyBytes); apiErr != nil {
		apiErr.Write(w)
		return
	}
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return
	}
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
	if !sc.anonymous {
		errors.ErrMalformedBody.Withf("process is not anonymous; use the sign endpoints").Write(w)
		return
	}
	byUpstream, ok := c.questionsByUpstream(w, oid)
	if !ok {
		return
	}
	ballots := make([]blindBallotAuth, len(req.Ballots))
	seen := make(map[string]int, len(req.Ballots))
	for i, item := range req.Ballots {
		if first, dup := seen[string(item.UpstreamID)]; dup {
			errors.ErrMalformedBody.Withf("the ballot at index %d repeats the election of index %d", i, first).Write(w)
			return
		}
		seen[string(item.UpstreamID)] = i
		if len(item.UpstreamID) == 0 {
			errors.ErrMalformedBody.With("missing election id").Withf("at index %d", i).Write(w)
			return
		}
		upstreamID, sErr := authorizeLoadedQuestion(sc, byUpstream[string(item.UpstreamID)])
		if sErr != nil {
			sErr.Withf("at index %d", i).Write(w)
			return
		}
		if len(item.BlindedMessage) == 0 {
			errors.ErrMalformedBody.With("missing blinded message").Withf("at index %d", i).Write(w)
			return
		}
		ballots[i] = blindBallotAuth{upstreamID: upstreamID, blindedMessage: item.BlindedMessage}
	}

	resp := &BlindSignResponse{Signatures: make([]SignBatchResult, len(ballots))}
	for i, b := range ballots {
		res := &resp.Signatures[i]
		res.UpstreamID = b.upstreamID
		signature, pinnedWeight, err := c.csp.BlindSign(req.AuthToken, b.upstreamID, b.blindedMessage)
		if err != nil {
			res.Code, res.Error = signOutcome(err)
			logSignFailure(oid, sc.memberID, b.upstreamID, err)
			if res.Code == signCodeAuthInvalid {
				for j := i + 1; j < len(ballots); j++ {
					resp.Signatures[j].UpstreamID = ballots[j].upstreamID
					resp.Signatures[j].Code, resp.Signatures[j].Error = res.Code, res.Error
				}
				break
			}
			continue
		}
		res.Signature = signature
		// report the weight actually signed (the pinned round-1 weight), matching SignBatchResult's
		// contract — not the live sc.weight, which may have drifted since round 1.
		res.Weight = pinnedWeight
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// questionsByUpstream loads a process's questions indexed by their on-chain election id, writing
// the proper error and returning false on failure. Shared by the blind round-1 and round-2 handlers.
func (c *CSPHandlers) questionsByUpstream(
	w http.ResponseWriter, oid primitive.ObjectID,
) (map[string]*db.VotingProcessQuestion, bool) {
	questions, err := c.mainDB.QuestionsByProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}
	byUpstream := make(map[string]*db.VotingProcessQuestion, len(questions))
	for i := range questions {
		byUpstream[string(questions[i].UpstreamID)] = &questions[i]
	}
	return byUpstream, true
}

// Stable, machine-readable outcomes of a failed csp.Sign, as returned by signOutcome.
const (
	// signCodeAlreadyConsumed: the election's signing slot is spent; retrying cannot succeed.
	signCodeAlreadyConsumed = "already_consumed"
	// signCodeAlreadySigning: a concurrent request holds this election's signing lock; retry.
	signCodeAlreadySigning = "already_signing"
	// signCodeAuthInvalid: the auth token is gone or no longer verified; the voter must
	// authenticate again — retrying with the same token cannot succeed, for ANY ballot.
	signCodeAuthInvalid = "auth_invalid"
	// signCodeAddressMismatch: the election was already signed for a different address; the
	// retry that succeeds re-sends with the pinned address, which sign-info reports.
	signCodeAddressMismatch = "address_mismatch"
	// signCodeFailed: an internal signer or storage failure; retrying may succeed.
	signCodeFailed = "sign_failed"
	// signCodeBlindRequestMissing: no round-1 blind point was issued for this election (or it was
	// already consumed); the client must call blind-point again before blind-sign.
	signCodeBlindRequestMissing = "blind_request_missing"
	// signCodeInvalidBlindedMessage: the blinded message was not a valid blind-signature input
	// (~1/256 leading-zero case). The nonce is NOT consumed, so the client re-blinds against the
	// same R (from blind-point, which is idempotent) and retries.
	signCodeInvalidBlindedMessage = "invalid_blinded_message"
)

// signOutcome maps a signing error to a stable, machine-readable code and a message safe for
// the public response body. The raw error (which for signer failures is errors.Join(ErrSign,
// <internal detail>)) is kept out of the response and logged instead.
func signOutcome(err error) (code, message string) {
	switch {
	case errors.Is(err, csp.ErrProcessAlreadyConsumed):
		return signCodeAlreadyConsumed, "this election's signing slot is already consumed"
	case errors.Is(err, csp.ErrUserAlreadySigning):
		return signCodeAlreadySigning, "a concurrent request is signing this election"
	case errors.Is(err, csp.ErrInvalidAuthToken), errors.Is(err, csp.ErrAuthTokenNotVerified):
		return signCodeAuthInvalid, "the auth token is no longer valid; authenticate again"
	case errors.Is(err, csp.ErrAddressMismatch):
		return signCodeAddressMismatch,
			"this election was already signed for a different address; re-send using the address reported by sign-info"
	case errors.Is(err, csp.ErrBlindRequestNotFound):
		return signCodeBlindRequestMissing, "no blind point issued for this election; request one via blind-point first"
	case errors.Is(err, csp.ErrInvalidBlindedMessage):
		return signCodeInvalidBlindedMessage, "the blinded message is invalid; re-blind against the same blind point and retry"
	default:
		return signCodeFailed, "could not sign the ballot"
	}
}

// logSignFailure records why one ballot of a batch could not be signed. A spent signing slot and
// a concurrent request for the same election are the normal outcomes of a voter retrying or of
// two tabs racing, and the caller is told about them in the response anyway, so they are debug —
// a warn per occurrence would bury the storage and signer failures that do need attention. The
// member and process ids make a warn attributable during an incident.
func logSignFailure(oid primitive.ObjectID, memberID string, upstreamID internal.HexBytes, err error) {
	if errors.Is(err, csp.ErrProcessAlreadyConsumed) || errors.Is(err, csp.ErrUserAlreadySigning) ||
		errors.Is(err, csp.ErrAddressMismatch) {
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
		entry := QuestionConsumedAddress{
			QuestionID: q.ID.Hex(),
			UpstreamID: q.UpstreamID,
			At:         cspProc.UsedAt,
		}
		// a blind (anonymous) consumption pins no address — the CSP never learned it — so there is
		// no server-side nullifier to report either; the voter derives its own from its address.
		if cspProc.UsedAddress != nil {
			entry.Address = cspProc.UsedAddress
			entry.Nullifier = state.GenerateNullifier(common.BytesToAddress(cspProc.UsedAddress), q.UpstreamID)
		}
		resp.Consumed = append(resp.Consumed, entry)
	}
	apicommon.HTTPWriteJSON(w, resp)
}
