package certauth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	apierrors "github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// redemption is one redeem in progress: the attempt and what its steps derive.
type redemption struct {
	tx     *db.CertAuthTransaction
	vp     *db.VotingProcess
	census *db.Census
	chain  []*x509.Certificate
}

// Redeem is POST /processes/{processId}/auth/cert/redeem (contract section 6). Every failure is the
// certificate envelope. The steps run in the contract's order: consume the code (single use, the
// chain leaves storage with it), check the PKCE verifier, spend the presented certificate's budget,
// Verify, spend the verified document's budget, match the census, issue the token.
func (s *Service) Redeem(w http.ResponseWriter, r *http.Request) {
	res, apiErr := s.redeemCode(r)
	if apiErr != nil {
		apiErr.Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, res)
}

func (s *Service) redeemCode(r *http.Request) (*apicommon.CertAuthRedeemResponse, *apierrors.Error) {
	pid, err := bson.ObjectIDFromHex(chi.URLParam(r, "processId"))
	if err != nil {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	var req apicommon.CertAuthRedeemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !tokenRe.MatchString(req.Code) {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	now := s.now()
	tx, err := s.store.ConsumeCertAuthCode(req.Code, now.Add(-codeTTL))
	if err != nil {
		return nil, s.spentCode(req.Code, err)
	}
	if tx.ProcessID != pid {
		return nil, s.fail(apicommon.CertAuthReasonAttemptExpired)
	}
	// the code is consumed whatever the verifier says: a stolen code is burnt by its first use
	if !verifierMatches(req.Verifier, tx.Challenge) {
		return nil, s.fail(apicommon.CertAuthReasonVerifierMismatch)
	}
	rd := &redemption{tx: tx}
	if apiErr := s.loadProcess(rd); apiErr != nil {
		return nil, apiErr
	}
	if apiErr := s.parseChain(rd); apiErr != nil {
		return nil, apiErr
	}
	result, apiErr := s.verify(r.Context(), rd)
	if apiErr != nil {
		return nil, apiErr
	}
	return s.issue(rd, result)
}

// fail is the envelope of a backend reason without data.
func (*Service) fail(reason string) *apierrors.Error {
	return apierrors.Ptr(certError(reason, apicommon.CertAuthErrorData{}))
}

// spentCode tells an unusable code apart: redeemed already is code_used, anything else (expired,
// reaped, never issued) attempt_expired.
func (s *Service) spentCode(code string, consumeErr error) *apierrors.Error {
	if !errors.Is(consumeErr, db.ErrNotFound) {
		return apierrors.Ptr(s.internalFailure("consuming the code", consumeErr))
	}
	if tx, err := s.store.CertAuthTransactionByCode(code); err == nil && tx.Status == db.CertAuthStatusConsumed {
		return s.fail(apicommon.CertAuthReasonCodeUsed)
	}
	return s.fail(apicommon.CertAuthReasonAttemptExpired)
}

// verifierMatches checks the RFC 7636 S256 binding in constant time.
func verifierMatches(verifier, challenge string) bool {
	if !verifierRe.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// loadProcess reads the attempt's process and census.
func (s *Service) loadProcess(rd *redemption) *apierrors.Error {
	vp, _, err := s.store.ProcessWithQuestions(rd.tx.ProcessID)
	if err != nil {
		return apierrors.Ptr(s.internalFailure("reading the process", err, "transaction", rd.tx.ID.Hex()))
	}
	census, err := s.store.Census(vp.CensusID.Hex())
	if err != nil {
		return apierrors.Ptr(s.internalFailure("reading the census", err, "transaction", rd.tx.ID.Hex()))
	}
	rd.vp, rd.census = vp, census
	return nil
}

// parseChain turns the stored DER into certificates. No certificate is no_certificate; one that was
// presented but not kept, or does not parse, is invalid_chain; both with a fresh reference, since no
// Verify ran.
func (*Service) parseChain(rd *redemption) *apierrors.Error {
	if len(rd.tx.Chain) == 0 {
		reason := string(certcheck.CodeNoCertificate)
		if rd.tx.ChainPresented {
			reason = string(certcheck.CodeInvalidChain)
		}
		return apierrors.Ptr(certError(reason, apicommon.CertAuthErrorData{Reference: certcheck.NewDiagnosticID()}))
	}
	chain := make([]*x509.Certificate, 0, len(rd.tx.Chain))
	for _, der := range rd.tx.Chain {
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return apierrors.Ptr(certError(string(certcheck.CodeInvalidChain),
				apicommon.CertAuthErrorData{Reference: certcheck.NewDiagnosticID()}))
		}
		chain = append(chain, c)
	}
	rd.chain = chain
	return nil
}

// verify runs the budgeted Verify. The unit spent before it is keyed on the presented leaf's DER,
// so a forged certificate naming someone's document spends only its own budget; an outage
// (revocation_unavailable, internal) gives that unit back. The unit spent after it is keyed on the
// verified document number. Both stay spent on every other outcome, success included.
func (s *Service) verify(ctx context.Context, rd *redemption) (*certcheck.Result, *apierrors.Error) {
	leafSum := sha256.Sum256(rd.chain[0].Raw)
	certClaim, apiErr := s.claimBudget(rd, leafSum[:])
	if apiErr != nil {
		return nil, apiErr
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	result, err := s.handler.Verify(vctx, rd.chain)
	if err != nil {
		e, ok := errors.AsType[*certcheck.Error](err)
		if !ok {
			e = &certcheck.Error{Code: certcheck.CodeInternal, Retryable: true}
		}
		if e.Code.Retryable() {
			if relErr := s.store.ReleaseCSPCounter(certClaim); relErr != nil {
				s.log.Warnw("could not release a certificate budget unit", "error", relErr)
			}
		}
		s.logOutcome(rd, e.Diagnostics)
		return nil, apierrors.Ptr(s.verifyFailure(e, rd.chain[0]))
	}
	s.logOutcome(rd, result.Diagnostics)
	if _, apiErr := s.claimBudget(rd, []byte(result.Identity.Document.Number)); apiErr != nil {
		return nil, apiErr
	}
	if result.Profile.HolderRole != certcheck.RoleNaturalPerson {
		return nil, apierrors.Ptr(certError(string(certcheck.CodeUnsupportedProfile),
			apicommon.CertAuthErrorData{Reference: result.Diagnostics.DiagnosticID}))
	}
	return result, nil
}

// claimBudget spends one unit of the hourly redeem budget of subject (a leaf DER hash or a verified
// document number) on the attempt's process.
func (s *Service) claimBudget(rd *redemption, subject []byte) (db.CSPCounterClaim, *apierrors.Error) {
	h := sha256.New()
	_, _ = h.Write([]byte(rd.tx.ProcessID.Hex()))
	_, _ = h.Write([]byte{'|'})
	_, _ = h.Write(subject)
	res, err := s.store.ClaimCSPCounter(db.CSPCounterCertAuthFailures, h.Sum(nil), s.v.maxFails)
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("claiming the redeem budget", err, "transaction", rd.tx.ID.Hex()))
	}
	if !res.Granted {
		return nil, apierrors.Ptr(certError(apicommon.CertAuthReasonTooManyAttempts,
			apicommon.CertAuthErrorData{RetryAfter: retryAfter(s.now(), res.ResetAt)}))
	}
	return res.Claim, nil
}

// issue matches the verified document against the census and answers: the token when the attempt
// is a vote and the process accepts votes, a null token otherwise (contract section 11).
func (s *Service) issue(rd *redemption, result *certcheck.Result,
) (*apicommon.CertAuthRedeemResponse, *apierrors.Error) {
	holder := resultHolder(result.Identity)
	reference := result.Diagnostics.DiagnosticID
	notInCensus := apierrors.Ptr(certError(apicommon.CertAuthReasonDNINotInCensus,
		apicommon.CertAuthErrorData{Reference: reference, Holder: &holder}))
	// canonical already: cert-check reports the 9-character number, and the lookup hashes it
	// canonically (SP1), so a member stored as "12.345.678-z" matches
	input := db.OrgMember{NationalID: internal.NormalizeNationalID(result.Identity.Document.Number).Normalized}
	participant, err := s.store.CensusParticipantByLoginHash(*rd.census, input)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notInCensus
	}
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("looking up the census", err, "transaction", rd.tx.ID.Hex()))
	}
	member, err := s.store.OrgMember(rd.census.OrgAddress, participant.ParticipantID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notInCensus
	}
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("reading the member", err, "transaction", rd.tx.ID.Hex()))
	}
	// by certificate, "cannot vote in this process" is the truthful answer for a zero weight too
	if rd.census.Weighted && member.Weight == 0 {
		return nil, notInCensus
	}
	resp := &apicommon.CertAuthRedeemResponse{
		Purpose: rd.tx.Purpose, VoterRef: s.voterRef(rd.tx.ProcessID, participant.LoginHash),
		Holder: holder, Reference: reference,
	}
	if rd.tx.Purpose != apicommon.CertAuthPurposeVote || !acceptsVotes(rd.vp) {
		return resp, nil
	}
	anchor := internal.HexBytes(rd.tx.ProcessID[:])
	token, err := s.tokens.AuthToken(anchor, internal.HexBytesFromString(member.ID.Hex()), "", "", "", "", "",
		common.Address{})
	if err != nil {
		return nil, apierrors.Ptr(s.internalFailure("issuing the token", err, "transaction", rd.tx.ID.Hex()))
	}
	weight := uint64(1)
	if rd.census.Weighted {
		weight = member.Weight
	}
	resp.AuthToken, resp.Weight = &token, new(big.Int).SetUint64(weight).Bytes()
	return resp, nil
}

// acceptsVotes reports whether the process is open for votes now: started (a zero StartDate counts
// as started) and not ended.
func acceptsVotes(vp *db.VotingProcess) bool {
	return !handlers.ProcessPastEnd(vp) && !handlers.ProcessBeforeStart(vp)
}

// voterRef is the opaque, stable reference of one participant of one process: an HMAC under a key
// derived from the root key, never the document or a bare hash of it.
func (s *Service) voterRef(pid bson.ObjectID, loginHash []byte) string {
	mac := hmac.New(sha256.New, s.refKey)
	_, _ = mac.Write(pid[:])
	_, _ = mac.Write(loginHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// logOutcome logs a Verify outcome with cert-check's PII-safe attributes and the attempt's ids only.
func (s *Service) logOutcome(rd *redemption, d certcheck.Diagnostics) {
	kv := append([]any{"transaction", rd.tx.ID.Hex(), "processId", rd.tx.ProcessID.Hex()}, diagnosticKV(d)...)
	s.log.Infow("certificate verified", kv...)
}

// diagnosticKV turns cert-check's PII-safe log attributes into key/value pairs for the logger.
func diagnosticKV(d certcheck.Diagnostics) []any {
	attrs := d.LogAttrs()
	kv := make([]any, 0, 2*len(attrs))
	for _, a := range attrs {
		kv = append(kv, a.Key, a.Value.Resolve().Any())
	}
	return kv
}
