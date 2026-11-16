package handlers

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
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
	// DocumentType is DNI or NIE.
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
//	@Description	the server clock. On success it stores a single-use code, valid for 5 minutes, that the voter
//	@Description	redeems at POST /processes/{processId}/auth/0 with the PKCE verifier of codeChallenge. At most
//	@Description	10 assertions per census participant per process per hour.
//	@Description	Without certificate gateway keys configured the route answers a plain 404.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			X-Cert-Gateway-Signature	header		string							true	"Gateway signature"
//	@Param			request						body		handlers.CertAssertionRequest	true	"Verified certificate identity"
//	@Success		201							{object}	handlers.CertAssertionResponse
//	@Failure		400							{object}	handlers.CertAssertionError	"bad_request"
//	@Failure		401							{object}	handlers.CertAssertionError	"bad_signature"
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
	if err := csp.VerifyCertGatewaySignature(keys, header, body, time.Now()); err != nil {
		writeCertAssertionError(w, http.StatusUnauthorized, certErrBadSignature, "reason", err.Error())
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
	if errors.Is(err, db.ErrNotFound) || (err == nil && !vp.Published) {
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
	if !ok {
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
	granted, err := c.csp.ClaimCertAssertion(internal.HexBytes(processID[:]), internal.HexBytes(member.ID[:]))
	if err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	if !granted {
		writeCertAssertionError(w, http.StatusTooManyRequests, certErrRateLimited, logFields...)
		return
	}

	code := make([]byte, 32)
	if _, err := rand.Read(code); err != nil {
		c.certAssertionFailed(w, err, logFields)
		return
	}
	certCode := base64.RawURLEncoding.EncodeToString(code)
	codeHash := sha256.Sum256([]byte(certCode))
	now := time.Now().UTC()
	assertion := &db.CertAuthAssertion{
		CodeHash:      codeHash[:],
		ProcessID:     processID,
		CensusID:      census.ID.Hex(),
		ParticipantID: participant.ParticipantID,
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

// voterRef is the stable reference of a voter of a certificate census on a process (AuthResponse).
func voterRef(processID, memberID string) string {
	sum := sha256.Sum256([]byte("vocdoni-voter-ref:" + processID + ":" + memberID))
	return hex.EncodeToString(sum[:])
}

// certAuthFirstStep is step 0 of a census with CertAuth. The request carries the certificate code
// the gateway returned to the voting page and the PKCE verifier of its code challenge, plus the
// email or phone when the census has 2FA, and no typed login field.
//
// The request is checked in full before the code is consumed, so a malformed one spends nothing.
// The code is then consumed atomically, and is spent from then on whatever the outcome: a
// verifier, contact or weight that fails sends the voter through the certificate again. An unknown,
// expired, used or foreign code and a wrong verifier all answer the bare ErrCSPAuthFailed. The
// failed-attempt budget is the participant's (its login hash), claimed before the contact check as
// in the typed flow. The rest is the typed flow's: contact check, weight, then csp.AuthToken.
func (c *CSPHandlers) certAuthFirstStep(
	r *http.Request,
	req *AuthRequest,
	census *db.Census,
	org *db.Organization,
	anchorID internal.HexBytes,
) (token internal.HexBytes, ref string, err error) {
	if !base64URL43.MatchString(req.CertCode) || !base64URLVerifier.MatchString(req.CodeVerifier) ||
		req.Name != "" || req.Surname != "" || req.MemberNumber != "" || req.NationalID != "" ||
		req.BirthDate != "" || req.Password != "" {
		return nil, "", errors.ErrInvalidUserData.Withf("a certificate login takes certCode and codeVerifier only")
	}
	if err := validateAuthRequest(req, census); err != nil {
		return nil, "", err
	}
	var phone *voterPhone
	if census.TwoFaFields.Contains(db.OrgMemberTwoFaFieldPhone) {
		if phone, err = parseVoterPhone(r.Context(), req.Phone, org); err != nil {
			return nil, "", phoneParseFailed(err)
		}
	}
	lang := apicommon.NotificationLang(r.Context(), org)

	var processID bson.ObjectID
	copy(processID[:], anchorID)
	codeHash := sha256.Sum256([]byte(req.CertCode))
	assertion, err := c.mainDB.ConsumeCertAuthAssertion(codeHash[:], processID, time.Now())
	if errors.Is(err, db.ErrNotFound) {
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	verifierHash := sha256.Sum256([]byte(req.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(verifierHash[:])
	if subtle.ConstantTimeCompare([]byte(challenge), []byte(assertion.CodeChallenge)) != 1 ||
		assertion.CensusID != census.ID.Hex() {
		return nil, "", errors.ErrCSPAuthFailed
	}

	participant, err := c.mainDB.CensusParticipant(census.ID.Hex(), assertion.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	claim, err := c.csp.ClaimAuthAttempt(anchorID, participant.LoginHash, nil)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		if !errors.Is(err, errors.ErrCSPAuthFailed) {
			c.csp.ReleaseAuthAttempt(claim)
		}
	}()

	member, err := c.mainDB.OrgMember(census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		log.Warnw("census participant without org member", "participantID", participant.ParticipantID)
		return nil, "", errors.ErrCSPAuthFailed
	}
	if err != nil {
		return nil, "", errors.ErrGenericInternalServerError.WithErr(err)
	}
	toDestinations, challengeType, err := determineContactMethod(census, req, phone, member)
	if err != nil {
		return nil, "", err
	}
	if census.Weighted && member.Weight == 0 {
		return nil, "", errors.ErrZeroWeightVoter
	}
	name, logo := orgNameAndLogo(org)
	token, err = c.csp.AuthToken(anchorID, internal.HexBytesFromString(member.ID.Hex()), toDestinations,
		challengeType, lang, name, logo, org.Address)
	if err != nil {
		return nil, "", err
	}
	return token, voterRef(processID.Hex(), member.ID.Hex()), nil
}
