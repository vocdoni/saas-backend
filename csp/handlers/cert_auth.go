package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// certAssertionMaxBody bounds the assertion request body. The gateway signs the raw body, so it is
// read (and hashed) whole before being decoded.
const certAssertionMaxBody = 4 << 10

// certAssertionTTL is how long a certificate code can be redeemed at step 0.
const certAssertionTTL = 5 * time.Minute

// Error codes of POST /cert-auth/assertions. The gateway forwards to the browser only the
// voter-facing ones (process_not_open, not_cert_census, anonymous_unavailable, not_in_census,
// zero_weight, rate_limited); anything else becomes backend_unavailable there.
const (
	certErrBadRequest    = "bad_request"
	certErrBadSignature  = "bad_signature"
	certErrProcessClosed = "process_not_open"
	certErrNotCertCensus = "not_cert_census"
	certErrAnonymous     = "anonymous_unavailable"
	certErrNotInCensus   = "not_in_census"
	certErrZeroWeight    = "zero_weight"
	certErrRateLimited   = "rate_limited"
)

var (
	base64URL43 = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// CertAssertionRequest is the body the certificate gateway signs and posts to
// POST /cert-auth/assertions once it has verified a voter's certificate.
type CertAssertionRequest struct {
	// ProcessID is the voting process id, 24 lowercase hex characters.
	ProcessID string `json:"processId"`
	// NationalID is the certificate's DNI or NIE, 9 characters as cert-check reports it.
	NationalID string `json:"nationalId"`
	// DocumentType is DNI or NIE, the type of NationalID (an NIE starts with X, Y or Z), else bad_request.
	DocumentType string `json:"documentType"`
	// Provider and Profile are the cert-check provider and profile that accepted the certificate.
	Provider string `json:"provider"`
	Profile  string `json:"profile"`
	// CertSHA256 is the lowercase hex SHA-256 of the certificate.
	CertSHA256 string `json:"certSha256"`
	// CodeChallenge is base64url(sha256(codeVerifier)), unpadded, 43 characters (PKCE S256).
	CodeChallenge string `json:"codeChallenge"`
}

// CertAssertionResponse is the 201 answer of POST /cert-auth/assertions.
type CertAssertionResponse struct {
	// CertCode is the single-use code the voter redeems at step 0 with the PKCE verifier.
	CertCode string `json:"certCode"`
	// ExpiresAt is when the code expires, RFC 3339 UTC.
	ExpiresAt string `json:"expiresAt"`
}

// CertAssertionError is every failure answer of POST /cert-auth/assertions.
type CertAssertionError struct {
	Error string `json:"error"`
}

// valid reports whether every field of the request is well formed.
func (req *CertAssertionRequest) valid() bool {
	return (req.DocumentType == "DNI" || req.DocumentType == "NIE") &&
		req.Provider != "" && len(req.Provider) <= 64 && len(req.Profile) <= 64 &&
		hex64.MatchString(req.CertSHA256) && base64URL43.MatchString(req.CodeChallenge)
}

// nationalIDDocumentType is the document type of a canonical national ID: NIE for one with an X, Y
// or Z prefix, else DNI.
func nationalIDDocumentType(canonical string) string {
	if strings.ContainsAny(canonical[:1], "XYZ") {
		return "NIE"
	}
	return "DNI"
}

// writeCertAssertionJSON writes an answer of POST /cert-auth/assertions.
func writeCertAssertionJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Warnw("failed to write cert assertion answer", "error", err)
	}
}

// writeCertAssertionError writes a failure answer and logs its outcome. The fields logged are ids
// only: never the national ID.
func writeCertAssertionError(w http.ResponseWriter, status int, code string, logFields ...any) {
	log.Infow("cert assertion refused", append([]any{"outcome", code}, logFields...)...)
	writeCertAssertionJSON(w, status, CertAssertionError{Error: code})
}

// CertAssertionHandler godoc
//
//	@Summary		Create a certificate auth assertion (certificate gateway only)
//	@Description	Called by the certificate gateway, server to server, once it has verified a voter's
//	@Description	certificate. The body is signed with the gateway's Ed25519 key in the X-Cert-Gateway-Signature
//	@Description	header: t=<unix seconds>,k=<keyId>,v1=<base64url signature> over
//	@Description	"cert-gateway-v1\n" + t + "\nPOST\n/cert-auth/assertions\n" + hex(sha256(body)), within 60 s of
//	@Description	the server clock. Each signed request is accepted once: a replay answers 401 bad_signature.
//	@Description	On success it stores a single-use code, valid for 5 minutes, that the voter redeems at
//	@Description	POST /processes/{processId}/auth/0 with the PKCE verifier of codeChallenge. At most 10
//	@Description	assertions per census participant per process per hour.
//	@Description	Without certificate gateway keys configured the route answers a plain 404.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			X-Cert-Gateway-Signature	header		string							true	"Gateway signature"
//	@Param			request						body		handlers.CertAssertionRequest	true	"Verified certificate identity"
//	@Success		201							{object}	handlers.CertAssertionResponse
//	@Failure		400							{object}	handlers.CertAssertionError	"bad_request"
//	@Failure		401							{object}	handlers.CertAssertionError	"bad_signature (also a replay)"
//	@Failure		403							{object}	handlers.CertAssertionError	"zero_weight"
//	@Failure		404							{object}	handlers.CertAssertionError	"process_not_open, not_in_census; or no body when the feature is off"
//	@Failure		409							{object}	handlers.CertAssertionError	"not_cert_census, anonymous_unavailable"
//	@Failure		429							{object}	handlers.CertAssertionError	"rate_limited"
//	@Failure		500							"internal failure, no body (the gateway reports backend_unavailable)"
//	@Router			/cert-auth/assertions [post]
func (c *CSPHandlers) CertAssertionHandler(w http.ResponseWriter, r *http.Request) {
	keys := c.csp.CertGatewayKeys()
	if len(keys) == 0 {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, certAssertionMaxBody))
	if err != nil {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	header := r.Header.Get(csp.CertGatewaySignatureHeader)
	now := time.Now()
	digest, err := csp.VerifyCertGatewaySignature(keys, csp.CertGatewaySignedRequest{
		Header: header, Body: body, ReceivedAt: now,
	})
	if err != nil {
		writeCertAssertionError(w, http.StatusUnauthorized, certErrBadSignature, "reason", err.Error())
		return
	}
	// a signed request is accepted once: a captured one replayed within its freshness window would
	// otherwise spend the voter's assertion budget
	fresh, err := c.mainDB.RecordCertGatewayRequest(digest, now.Add(csp.CertGatewayReplayWindow))
	if err != nil {
		c.certAssertionFailed(w, err, nil)
		return
	}
	if !fresh {
		writeCertAssertionError(w, http.StatusUnauthorized, certErrBadSignature, "reason", "replayed request")
		return
	}
	var req CertAssertionRequest
	if err := json.Unmarshal(body, &req); err != nil || !req.valid() {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	processID, err := bson.ObjectIDFromHex(req.ProcessID)
	if err != nil || processID.Hex() != req.ProcessID {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest)
		return
	}
	logFields := []any{"provider", req.Provider, "processId", req.ProcessID}

	vp, err := c.mainDB.VotingProcess(processID)
	if errors.Is(err, db.ErrNotFound) || (err == nil && (!vp.Published || processEnded(vp))) {
		writeCertAssertionError(w, http.StatusNotFound, certErrProcessClosed, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	census, err := c.mainDB.Census(vp.CensusID.Hex())
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if !census.CertAuth {
		writeCertAssertionError(w, http.StatusConflict, certErrNotCertCensus, logFields...)
		return
	}
	if c.anonymousCensusRefused(census) {
		writeCertAssertionError(w, http.StatusConflict, certErrAnonymous, logFields...)
		return
	}
	nationalID, ok := internal.CanonicalNationalID(req.NationalID)
	if !ok || req.DocumentType != nationalIDDocumentType(nationalID) {
		writeCertAssertionError(w, http.StatusBadRequest, certErrBadRequest, logFields...)
		return
	}
	participant, err := c.mainDB.CensusParticipantByLoginHash(*census, db.OrgMember{NationalID: nationalID})
	if errors.Is(err, db.ErrNotFound) {
		writeCertAssertionError(w, http.StatusNotFound, certErrNotInCensus, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	logFields = append(logFields, "participantId", participant.ParticipantID)
	member, err := c.mainDB.OrgMember(census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		writeCertAssertionError(w, http.StatusNotFound, certErrNotInCensus, logFields...)
		return
	}
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if census.Weighted && member.Weight == 0 {
		writeCertAssertionError(w, http.StatusForbidden, certErrZeroWeight, logFields...)
		return
	}
	claim, granted, err := c.csp.ClaimCertAssertion(internal.HexBytes(processID[:]), internal.HexBytes(member.ID[:]))
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if !granted {
		writeCertAssertionError(w, http.StatusTooManyRequests, certErrRateLimited, logFields...)
		return
	}
	// an internal failure from here on gives the unit back: only created assertions spend the budget
	created := false
	defer func() {
		if !created {
			c.csp.ReleaseCertAssertion(claim)
		}
	}()

	code := make([]byte, 32)
	if _, err := rand.Read(code); err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	certCode := base64.RawURLEncoding.EncodeToString(code)
	codeHash := sha256.Sum256([]byte(certCode))
	now = now.UTC()
	assertion := &db.CertAuthAssertion{
		CodeHash:      codeHash[:],
		ProcessID:     processID,
		CensusID:      census.ID.Hex(),
		ParticipantID: participant.ParticipantID,
		LoginHash:     participant.LoginHash,
		CodeChallenge: req.CodeChallenge,
		Provider:      req.Provider,
		CertSHA256:    req.CertSHA256,
		CreatedAt:     now,
		ExpiresAt:     now.Add(certAssertionTTL),
	}
	if err := c.mainDB.CreateCertAuthAssertion(assertion); err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	created = true
	log.Infow("cert assertion created", append([]any{"outcome", "created"}, logFields...)...)
	writeCertAssertionJSON(w, http.StatusCreated, &CertAssertionResponse{
		CertCode:  certCode,
		ExpiresAt: assertion.ExpiresAt.Format(time.RFC3339),
	})
}

// certAssertionFailed answers an internal failure: a plain 500, which the gateway turns into
// backend_unavailable.
func (*CSPHandlers) certAssertionFailed(w http.ResponseWriter, err error, logFields []any) {
	log.Warnw("cert assertion failed", append([]any{"error", err.Error()}, logFields...)...)
	w.WriteHeader(http.StatusInternalServerError)
}

// base64URLVerifier is a PKCE code verifier: 43 to 128 base64url characters.
var base64URLVerifier = regexp.MustCompile(`^[A-Za-z0-9_-]{43,128}$`)

// certAuthFirstStep is step 0 of a census with CertAuth. The request carries the certificate code
// the gateway returned to the voting page and the PKCE verifier of its code challenge, plus the
// email or phone when the census has 2FA, and no typed login field.
//
// The request is checked in full before the code is looked at, so a malformed one spends nothing.
// The code is then looked up without being consumed, and every check that can refuse runs before
// it is consumed atomically, right before the token is minted:
//   - a failure on the voter's data (an unknown, expired, used or foreign code, a wrong verifier, a
//     participant that no longer holds the proved national ID, a wrong contact, a zero weight)
//     spends the code, and sends the voter through the certificate again. The code and verifier
//     failures answer the bare ErrCSPAuthFailed.
//   - a refusal the voter can only wait out (the failed-attempt budget, 40017, and for a census
//     with 2FA the cooldown and the daily send cap AuthToken applies) leaves the code redeemable.
//     The 2FA checks are a peek (csp.ChallengeSendRefusal): AuthToken enforces them again after
//     the code is consumed, so a send racing in between can still refuse with the code spent.
//
// The failed-attempt budget is the participant's (its login hash), claimed before the contact
// check as in the typed flow. The rest is the typed flow's: contact check, weight, then
// csp.AuthToken.
func (c *CSPHandlers) certAuthFirstStep(in firstStepRequest) (res firstStepResult, err error) {
	r, req, census, org, anchorID := in.r, in.req, in.census, in.org, in.anchorID
	if !base64URL43.MatchString(req.CertCode) || !base64URLVerifier.MatchString(req.CodeVerifier) ||
		req.Name != "" || req.Surname != "" || req.MemberNumber != "" || req.NationalID != "" ||
		req.BirthDate != "" || req.Password != "" {
		return firstStepResult{}, errors.ErrInvalidUserData.Withf("a certificate login takes certCode and codeVerifier only")
	}
	if err := validateAuthRequest(req, census); err != nil {
		return firstStepResult{}, err
	}
	var phone *voterPhone
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) {
		if phone, err = parseVoterPhone(r.Context(), req.Phone, org); err != nil {
			return firstStepResult{}, phoneParseFailed(err)
		}
	}
	lang := apicommon.NotificationLang(r.Context(), org)

	var processID bson.ObjectID
	copy(processID[:], anchorID)
	codeHash := sha256.Sum256([]byte(req.CertCode))
	assertion, err := c.mainDB.CertAuthAssertion(codeHash[:], processID, time.Now())
	if errors.Is(err, db.ErrNotFound) {
		return firstStepResult{}, errors.ErrCSPAuthFailed
	}
	if err != nil {
		return firstStepResult{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	consumed := false
	defer func() {
		if !consumed && (errors.Is(err, errors.ErrCSPAuthFailed) || errors.Is(err, errors.ErrZeroWeightVoter)) {
			c.spendCertCode(codeHash[:], processID)
		}
	}()
	verifierHash := sha256.Sum256([]byte(req.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(assertion.CodeChallenge)) != 1 ||
		assertion.CensusID != census.ID.Hex() {
		return firstStepResult{}, errors.ErrCSPAuthFailed
	}

	participant, err := c.mainDB.CensusParticipant(census.ID.Hex(), assertion.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		return firstStepResult{}, errors.ErrCSPAuthFailed
	}
	if err != nil {
		return firstStepResult{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	// the participant must still hold the national ID the certificate proved: an edited or cleared
	// one changes or unsets its login hash
	if len(participant.LoginHash) == 0 ||
		subtle.ConstantTimeCompare(participant.LoginHash, assertion.LoginHash) != 1 {
		return firstStepResult{}, errors.ErrCSPAuthFailed
	}
	claim, err := c.csp.ClaimAuthAttempt(anchorID, participant.LoginHash, nil)
	if err != nil {
		return firstStepResult{}, err
	}
	defer func() {
		if !errors.Is(err, errors.ErrCSPAuthFailed) {
			c.csp.ReleaseAuthAttempt(claim)
		}
	}()

	member, err := c.mainDB.OrgMember(census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("census participant without org member", "participantID", participant.ParticipantID)
		return firstStepResult{}, errors.ErrCSPAuthFailed
	}
	if err != nil {
		return firstStepResult{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	toDestinations, challengeType, err := determineContactMethod(census, req, phone, member)
	if err != nil {
		return firstStepResult{}, err
	}
	if census.Weighted && member.Weight == 0 {
		return firstStepResult{}, errors.ErrZeroWeightVoter
	}
	uID := internal.HexBytesFromString(member.ID.Hex())
	if challengeType != "" {
		if err := c.csp.ChallengeSendRefusal(uID, anchorID); err != nil {
			return firstStepResult{}, err
		}
	}

	// exactly once: of two concurrent redemptions that got this far, one consumes the code
	if _, err := c.mainDB.ConsumeCertAuthAssertion(codeHash[:], processID, time.Now()); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return firstStepResult{}, errors.ErrCSPAuthFailed
		}
		return firstStepResult{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	consumed = true
	name, logo := orgNameAndLogo(org)
	token, err := c.csp.AuthToken(anchorID, uID, toDestinations, challengeType, lang, name, logo, org.Address)
	if err != nil {
		return firstStepResult{}, err
	}
	res = firstStepResult{token: token, voterRef: c.csp.VoterRef(processID.Hex(), member.ID.Hex())}
	if toDestinations != "" {
		res.dest = csp.DestinationKey(challengeType, toDestinations)
	}
	return res, nil
}

// spendCertCode consumes a certificate code whose step 0 failed on the voter's data, so the voter
// goes through the certificate again. A code already spent is fine; other failures are logged.
func (c *CSPHandlers) spendCertCode(codeHash []byte, processID bson.ObjectID) {
	_, err := c.mainDB.ConsumeCertAuthAssertion(codeHash, processID, time.Now())
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		log.Warnw("failed to spend cert code", "processId", processID.Hex(), "error", err)
	}
}
