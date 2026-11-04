package errors

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestCertificateAuthError pins the certificate sign-in envelope: one code for every reason, the
// reason's own HTTP status, the client-safe message as the error string (5xx included) and the
// data object verbatim.
func TestCertificateAuthError(t *testing.T) {
	c := qt.New(t)
	data := map[string]any{"reason": "revocation_unavailable", "retryable": true}

	e := CertificateAuthError(http.StatusServiceUnavailable, "revocation status is unavailable", data)
	code, body := writtenBody(t, e)
	c.Assert(code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(body["code"], qt.Equals, float64(40950))
	c.Assert(body["error"], qt.Equals, "revocation status is unavailable")
	c.Assert(body["data"], qt.DeepEquals, map[string]any{"reason": "revocation_unavailable", "retryable": true})
	c.Assert(e, qt.ErrorIs, ErrCertificateAuth)

	// the package-level definition is never modified by building a response from it
	c.Assert(ErrCertificateAuth.HTTPstatus, qt.Equals, http.StatusUnprocessableEntity)
	c.Assert(ErrCertificateAuth.Data, qt.IsNil)

	code, body = writtenBody(t, ErrCertificateAuthDisabled)
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(body["code"], qt.Equals, float64(40185))
	c.Assert(body["data"], qt.IsNil)
}
