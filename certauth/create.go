package certauth

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	apierrors "github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// maxReturnURLLen bounds a returnUrl before it is parsed.
const maxReturnURLLen = 2048

// returnPathRe is the path a returnUrl may carry: the app root or a process page, with an optional
// /:lang (contract section 3).
var returnPathRe = regexp.MustCompile(`^(/[a-z]{2}(-[a-z]{2})?)?(/processes/[0-9a-f]{24})?/?$`)

// closedStatuses are the question statuses that take no more sign-ins.
var closedStatuses = map[string]bool{
	db.QuestionStatusEnded: true, db.QuestionStatusCanceled: true, db.QuestionStatusResults: true,
}

// Create is POST /processes/{processId}/auth/cert (contract section 3): it validates the attempt,
// spends one unit of the per-IP create limit and stores a fresh transaction, answering its state
// and the per-attempt auth URL. A validation failure is a plain standard error, the limit the
// too_many_attempts envelope.
func (s *Service) Create(w http.ResponseWriter, r *http.Request) {
	pid, err := bson.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		apierrors.ErrMalformedURLParam.Withf("invalid process ID").Write(w)
		return
	}
	var req apicommon.CertAuthCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierrors.ErrMalformedBody.Write(w)
		return
	}
	if err := s.checkCreateRequest(&req); err != nil {
		apierrors.ErrMalformedBody.WithErr(err).Write(w)
		return
	}
	if apiErr := s.checkProcess(pid); apiErr != nil {
		apiErr.Write(w)
		return
	}
	now := s.now()
	key := sha256.Sum256([]byte(s.requestClientIP(r) + "|" + pid.Hex()))
	limit, err := s.store.ClaimCSPCounter(db.CSPCounterCertAuthCreates, key[:], s.cfg.CreatesPerMinute)
	if err != nil {
		apierrors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !limit.Granted {
		certError(apicommon.CertAuthReasonTooManyAttempts,
			apicommon.CertAuthErrorData{RetryAfter: retryAfter(now, limit.ResetAt)}).Write(w)
		return
	}
	tx := &db.CertAuthTransaction{
		State: randomToken(), Nonce: randomNonce(), Challenge: req.Challenge, ProcessID: pid,
		ReturnURL: req.ReturnURL, Purpose: req.Purpose, Status: db.CertAuthStatusCreated,
		CreatedAt: now, ExpiresAt: now.Add(stateTTL),
	}
	if err := s.store.InsertCertAuthTransaction(tx); err != nil {
		apierrors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	authURL := url.URL{
		Scheme: "https", Host: s.publicHost(tx.Nonce + "." + s.cfg.Host), Path: authenticatePath,
		RawQuery: url.Values{"state": {tx.State}}.Encode(),
	}
	probeURL := url.URL{Scheme: "https", Host: s.publicHost(probeLabel + "." + s.cfg.Host), Path: probePath}
	apicommon.HTTPWriteJSON(w, &apicommon.CertAuthCreateResponse{
		State: tx.State, AuthURL: authURL.String(), ExpiresAt: tx.ExpiresAt.UTC().Format(time.RFC3339),
		ProbeURL: probeURL.String(),
	})
}

// checkCreateRequest validates the body fields.
func (s *Service) checkCreateRequest(req *apicommon.CertAuthCreateRequest) error {
	if !tokenRe.MatchString(req.Challenge) || req.ChallengeMethod != "S256" {
		return fmt.Errorf("challenge must be a 43-character S256 challenge")
	}
	if req.Purpose != apicommon.CertAuthPurposeVote && req.Purpose != apicommon.CertAuthPurposeCheck {
		return fmt.Errorf("purpose must be vote or check")
	}
	return s.checkReturnURL(req.ReturnURL)
}

// checkReturnURL accepts an absolute URL on an allowlisted origin whose path is the app root or a
// process page with an optional /:lang, without query, fragment or userinfo (contract section 3).
func (s *Service) checkReturnURL(raw string) error {
	invalid := fmt.Errorf("returnUrl is not an allowed app URL")
	if len(raw) > maxReturnURLLen || strings.ContainsAny(raw, "#?\\") {
		return invalid
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Opaque != "" || u.User != nil || u.RawPath != "" {
		return invalid
	}
	if !secureScheme(u) || !s.v.origins[u.Scheme+"://"+u.Host] || !returnPathRe.MatchString(u.Path) {
		return invalid
	}
	return nil
}

// checkProcess requires a published process that still takes sign-ins, with a certificate census.
// UPCOMING is allowed for both purposes: the redeem decides whether to issue a token.
func (s *Service) checkProcess(pid bson.ObjectID) *apierrors.Error {
	vp, questions, err := s.store.ProcessWithQuestions(pid)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return apierrors.Ptr(apierrors.ErrProcessNotFound)
		}
		return apierrors.Ptr(apierrors.ErrGenericInternalServerError.WithErr(err))
	}
	if !vp.Published || !acceptsSignIns(vp, questions) {
		return apierrors.Ptr(apierrors.ErrDuplicateConflict.Withf("the process does not take sign-ins"))
	}
	census, err := s.store.Census(vp.CensusID.Hex())
	if err != nil {
		return apierrors.Ptr(apierrors.ErrGenericInternalServerError.WithErr(err))
	}
	if census.AuthMethod != db.CensusAuthMethodCertificate {
		return apierrors.Ptr(apierrors.ErrInvalidData.Withf("the process census does not sign in with a certificate"))
	}
	return nil
}

// acceptsSignIns reports whether a published process takes sign-ins: not past its end, and at least
// one question neither ENDED, CANCELED nor RESULTS.
func acceptsSignIns(vp *db.VotingProcess, questions []db.VotingProcessQuestion) bool {
	if handlers.ProcessPastEnd(vp) {
		return false
	}
	for i := range questions {
		if !closedStatuses[questions[i].Status] {
			return true
		}
	}
	return false
}

// requestClientIP is the client IP of a main-listener request: the TCP peer, or the rightmost
// X-Forwarded-For entry when the listener only takes requests through the trusted edge proxy.
func (s *Service) requestClientIP(r *http.Request) string {
	if s.cfg.TrustForwardedFor {
		if hops := r.Header.Values("X-Forwarded-For"); len(hops) > 0 {
			entries := strings.Split(hops[len(hops)-1], ",")
			if ip := net.ParseIP(strings.TrimSpace(entries[len(entries)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	return clientIP(r.RemoteAddr)
}
