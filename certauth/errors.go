package certauth

import (
	"crypto/x509"
	"net/http"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	apierrors "github.com/vocdoni/saas-backend/errors"
)

// unprocessable is the status of every non-retryable certificate rejection.
const unprocessable = http.StatusUnprocessableEntity

// reasonSpec is the HTTP status and client message of one data.reason.
type reasonSpec struct {
	status  int
	message string
}

// reasons maps every data.reason to its status and message (contract section 8): cert-check's codes,
// verbatim, then the backend's own. The messages are client-safe; clients key on the reason.
var reasons = map[string]reasonSpec{
	string(certcheck.CodeNoCertificate):               {unprocessable, "no certificate was presented"},
	string(certcheck.CodeProviderMismatch):            {unprocessable, "the certificate is not of the expected type"},
	string(certcheck.CodeUntrustedIssuer):             {unprocessable, "the certificate issuer is not accepted"},
	string(certcheck.CodeInvalidChain):                {unprocessable, "the certificate chain is not valid"},
	string(certcheck.CodeExpired):                     {unprocessable, "the certificate has expired"},
	string(certcheck.CodeNotYetValid):                 {unprocessable, "the certificate is not valid yet"},
	string(certcheck.CodeUnsupportedProfile):          {unprocessable, "this kind of certificate is not accepted"},
	string(certcheck.CodeUnsupportedPurpose):          {unprocessable, "the certificate cannot be used to sign in"},
	string(certcheck.CodeWeakAlgorithm):               {unprocessable, "the certificate uses a weak algorithm"},
	string(certcheck.CodeTestCertificate):             {unprocessable, "test certificates are not accepted"},
	string(certcheck.CodeIdentityInvalid):             {unprocessable, "the certificate identity is not valid"},
	string(certcheck.CodeIdentityUnsupportedDocument): {unprocessable, "the certificate document is not a DNI or NIE"},
	string(certcheck.CodeRevoked):                     {unprocessable, "the certificate has been revoked"},
	string(certcheck.CodeRevocationUnknown):           {unprocessable, "the certificate revocation status is unknown"},
	string(certcheck.CodeRevocationUnavailable):       {http.StatusServiceUnavailable, "revocation status is unavailable"},
	string(certcheck.CodeInternal):                    {http.StatusInternalServerError, "the certificate could not be checked"},
	apicommon.CertAuthReasonDNINotInCensus:            {http.StatusForbidden, "certificate is not in the census"},
	apicommon.CertAuthReasonAttemptExpired:            {http.StatusGone, "the sign-in attempt has expired"},
	apicommon.CertAuthReasonCodeUsed:                  {http.StatusConflict, "the sign-in code was already used"},
	apicommon.CertAuthReasonVerifierMismatch:          {http.StatusForbidden, "the sign-in verifier does not match"},
	apicommon.CertAuthReasonTooManyAttempts:           {http.StatusTooManyRequests, "too many sign-in attempts"},
	apicommon.CertAuthReasonCertificateCensus:         {http.StatusBadRequest, "this census signs in with a digital certificate"},
}

// issuerNames are the contract's display names of the providers (section 8); any other provider
// shows its own Info().Name.
var issuerNames = map[string]string{"fnmt": "FNMT", "idcat": "idCAT"}

// certError is the envelope of reason with data: code 40950, the reason's status and message, and
// retryable true only for revocation_unavailable and internal. An unknown reason is internal.
func certError(reason string, data apicommon.CertAuthErrorData) apierrors.Error {
	spec, ok := reasons[reason]
	if !ok {
		reason = apicommon.CertAuthReasonInternal
		spec = reasons[reason]
	}
	data.Reason = reason
	data.Retryable = certcheck.ErrorCode(reason).Retryable()
	return apierrors.CertificateAuthError(spec.status, spec.message, data)
}

// verifyFailure is the envelope of a cert-check failure of leaf: the verbatim code, the diagnostic
// reference, the issuer when one provider is attributable, and on expiry and revocation the holder
// read from the leaf subject and, for the validity failures, the certificate's notAfter. Never the
// Detail, the Diagnostics or the certificate itself.
func (s *Service) verifyFailure(e *certcheck.Error, leaf *x509.Certificate) apierrors.Error {
	data := apicommon.CertAuthErrorData{Reference: e.Diagnostics.DiagnosticID}
	if data.Reference == "" {
		data.Reference = certcheck.NewDiagnosticID()
	}
	if p := s.providerOf(leaf); p != nil {
		info := p.Info()
		name, ok := issuerNames[info.ID]
		if !ok {
			name = info.Name
		}
		data.Issuer = &apicommon.CertAuthIssuer{ID: info.ID, Name: name}
	}
	switch e.Code {
	case certcheck.CodeExpired, certcheck.CodeNotYetValid:
		if d := e.Diagnostics.Certificate; d != nil {
			data.NotAfter = d.NotAfter.UTC().Format(time.RFC3339)
		}
		data.Holder = subjectHolder(leaf)
	case certcheck.CodeRevoked:
		data.Holder = subjectHolder(leaf)
	default:
	}
	return certError(string(e.Code), data)
}

// internalFailure is the envelope of a backend failure: a fresh reference the log line carries too,
// so support can find it.
func (s *Service) internalFailure(what string, err error, kv ...any) apierrors.Error {
	ref := certcheck.NewDiagnosticID()
	s.log.Warnw("certificate sign-in failed: "+what, append([]any{"reference", ref, "error", err}, kv...)...)
	return certError(apicommon.CertAuthReasonInternal, apicommon.CertAuthErrorData{Reference: ref})
}

// retryAfter is the whole seconds until resetAt, at least one.
func retryAfter(now, resetAt time.Time) int64 {
	secs := int64(resetAt.Sub(now).Round(time.Second) / time.Second)
	return max(secs, 1)
}

// providerOf returns the provider advertising the leaf's issuer, attributing a failure to it, or
// nil when none (or more than one) does.
func (s *Service) providerOf(leaf *x509.Certificate) certcheck.Provider {
	if leaf == nil {
		return nil
	}
	var found certcheck.Provider
	for _, p := range s.handler.Providers() {
		for _, ca := range p.CAs() {
			if string(ca.RawSubject) == string(leaf.RawIssuer) {
				if found != nil && found.Info().ID != p.Info().ID {
					return nil
				}
				found = p
			}
		}
	}
	return found
}
