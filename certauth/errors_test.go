package certauth

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/handlers"
)

// errorBody is a decoded error envelope.
type errorBody struct {
	Error string                      `json:"error"`
	Code  int                         `json:"code"`
	Data  apicommon.CertAuthErrorData `json:"data"`
}

// writtenEnvelope writes an error and decodes its envelope, returning the raw body too.
func writtenEnvelope(c *qt.C, write func(http.ResponseWriter)) (int, string, errorBody) {
	rec := httptest.NewRecorder()
	write(rec)
	var parsed errorBody
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &parsed), qt.IsNil)
	return rec.Code, rec.Body.String(), parsed
}

// TestCertAuthErrorMapping pins the error envelope (AC10): every reason answers code 40950 with its
// HTTP status and the verbatim reason; retryable only for revocation_unavailable and internal; the
// conditional fields where the contract puts them; and never the cert-check Detail, a serialized
// Diagnostics or certificate material, in the body or in the logs.
func TestCertAuthErrorMapping(t *testing.T) {
	c := qt.New(t)

	// every cert-check code has a mapping, so none falls through to internal by accident
	for _, code := range certcheck.Codes {
		_, ok := reasons[string(code)]
		c.Assert(ok, qt.IsTrue, qt.Commentf("%s", code))
	}

	s, _, pki := newTestService(c, 443, nil)
	logs, ok := s.log.(*captureLog)
	c.Assert(ok, qt.IsTrue)
	verify := func(h testprovider.Holder) (*certcheck.Error, *x509.Certificate) {
		cert, err := pki.Issue(h)
		c.Assert(err, qt.IsNil)
		_, err = s.handler.Verify(t.Context(), []*x509.Certificate{cert.Leaf})
		ce, ok := err.(*certcheck.Error)
		c.Assert(ok, qt.IsTrue, qt.Commentf("err: %v", err))
		return ce, cert.Leaf
	}
	holder := testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"}
	expiredHolder := holder
	expiredHolder.NotAfter = time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	revokedHolder := holder
	revokedHolder.OU = testprovider.OURevoked
	outageHolder := holder
	outageHolder.OU = testprovider.OUOutage

	for _, tc := range []struct {
		name      string
		holder    testprovider.Holder
		reason    string
		status    int
		withHold  bool
		notAfter  string
		retryable bool
	}{
		{"expired", expiredHolder, "expired", 422, true, "2026-10-03T00:00:00Z", false},
		{"revoked", revokedHolder, "revoked", 422, true, "", false},
		{"outage", outageHolder, "revocation_unavailable", 503, false, "", true},
	} {
		ce, leaf := verify(tc.holder)
		apiErr := s.verifyFailure(ce, leaf)
		status, raw, body := writtenEnvelope(c, apiErr.Write)
		comment := qt.Commentf("%s: %s", tc.name, raw)
		c.Assert(status, qt.Equals, tc.status, comment)
		c.Assert(body.Code, qt.Equals, 40950, comment)
		c.Assert(body.Data.Reason, qt.Equals, tc.reason, comment)
		c.Assert(body.Data.Retryable, qt.Equals, tc.retryable, comment)
		c.Assert(body.Data.Reference, qt.Equals, ce.Diagnostics.DiagnosticID, comment)
		c.Assert(body.Data.Issuer, qt.DeepEquals, &apicommon.CertAuthIssuer{ID: "test", Name: "Synthetic Test CA"}, comment)
		c.Assert(body.Data.NotAfter, qt.Equals, tc.notAfter, comment)
		if tc.withHold {
			c.Assert(body.Data.Holder, qt.DeepEquals, &apicommon.CertAuthHolder{
				FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
			}, comment)
		} else {
			c.Assert(body.Data.Holder, qt.IsNil, comment)
		}
		assertNoCertificateMaterial(c, raw, ce, leaf)
	}

	// unsupported_profile, as cert-check answers it for a representative certificate
	upErr := &certcheck.Error{
		Code: certcheck.CodeUnsupportedProfile, Detail: "representative OID 2.16.724.1.3.5.7",
		Diagnostics: certcheck.Diagnostics{DiagnosticID: certcheck.NewDiagnosticID(), TechnicalDetail: "profile"},
	}
	status, raw, body := writtenEnvelope(c, s.verifyFailure(upErr, nil).Write)
	c.Assert(status, qt.Equals, 422)
	c.Assert(body.Data.Reason, qt.Equals, "unsupported_profile")
	c.Assert(body.Data.Issuer, qt.IsNil)
	c.Assert(raw, qt.Not(qt.Contains), "representative OID")

	// a backend failure is internal, retryable, with a fresh reference the log line carries too
	internalErr := s.internalFailure("testing", http.ErrHandlerTimeout, "transaction", "tx")
	status, _, body = writtenEnvelope(c, internalErr.Write)
	c.Assert(status, qt.Equals, 500)
	c.Assert(body.Data.Reason, qt.Equals, "internal")
	c.Assert(body.Data.Retryable, qt.IsTrue)
	c.Assert(body.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
	logged := logs.all()
	last := logged[len(logged)-1]
	c.Assert(last.kv["reference"], qt.Equals, body.Data.Reference)

	// the auth/0 refusal of a certificate census shares the envelope
	status, _, body = writtenEnvelope(c, handlers.CertificateCensusError().Write)
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	c.Assert(body.Code, qt.Equals, 40950)
	c.Assert(body.Data, qt.DeepEquals, apicommon.CertAuthErrorData{Reason: "certificate_census"})
	want := certError(apicommon.CertAuthReasonCertificateCensus, apicommon.CertAuthErrorData{})
	c.Assert(handlers.CertificateCensusError().Error(), qt.Equals, want.Error())
}

// attrKeys lists the keys cert-check's LogAttrs emits for d.
func attrKeys(d certcheck.Diagnostics) []string {
	var keys []string
	for _, a := range d.LogAttrs() {
		keys = append(keys, a.Key)
	}
	return keys
}

// stringify renders a log value for the secret scan.
func stringify(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

// assertNoCertificateMaterial checks a body carries nothing of the Diagnostics or the certificate.
func assertNoCertificateMaterial(c *qt.C, raw string, ce *certcheck.Error, leaf *x509.Certificate) {
	c.Helper()
	for _, banned := range []string{
		ce.Detail, "technicalDetail", "diagnostics", "IDCES", leaf.Subject.String(),
		base64.StdEncoding.EncodeToString(leaf.Raw)[:40],
	} {
		if banned != "" {
			c.Assert(raw, qt.Not(qt.Contains), banned)
		}
	}
}
