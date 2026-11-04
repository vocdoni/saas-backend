package testprovider

import (
	"context"
	"crypto/x509"
	"errors"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	certcheck "github.com/vocdoni/cert-check"
)

func verifyErr(c *qt.C, h *certcheck.Handler, leaf *x509.Certificate) *certcheck.Error {
	c.Helper()
	_, err := h.Verify(context.Background(), []*x509.Certificate{leaf})
	e, ok := errors.AsType[*certcheck.Error](err)
	c.Assert(ok, qt.IsTrue, qt.Commentf("err: %v", err))
	return e
}

// TestProvider pins the synthetic provider: a cert-check Handler built with it accepts leaves of its
// CA as natural persons with their canonical DNI/NIE and holder name, and answers the deterministic
// error fixtures (revoked, outage, expired, forged, malformed document) as cert-check would.
func TestProvider(t *testing.T) {
	c := qt.New(t)
	pki, err := NewPKI()
	c.Assert(err, qt.IsNil)
	p, err := New(pki.CertPEM())
	c.Assert(err, qt.IsNil)
	h, err := certcheck.NewHandler([]certcheck.Provider{p})
	c.Assert(err, qt.IsNil)
	c.Assert(h.CertificateCheck(pki.CA.RawSubject), qt.IsTrue)

	dni, err := pki.Issue(Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	res, err := h.Verify(context.Background(), []*x509.Certificate{dni.Leaf})
	c.Assert(err, qt.IsNil)
	c.Assert(res.Provider.ID, qt.Equals, ID)
	c.Assert(res.Profile.HolderRole, qt.Equals, certcheck.RoleNaturalPerson)
	c.Assert(res.Identity.Document.Type, qt.Equals, certcheck.DocumentDNI)
	c.Assert(res.Identity.Document.Number, qt.Equals, "12345678Z")
	c.Assert(res.Identity.FullName, qt.Equals, "Maria García López")
	c.Assert(res.Diagnostics.DiagnosticID, qt.HasLen, 32)

	nie, err := pki.Issue(Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"})
	c.Assert(err, qt.IsNil)
	res, err = h.Verify(context.Background(), []*x509.Certificate{nie.Leaf})
	c.Assert(err, qt.IsNil)
	c.Assert(res.Identity.Document.Type, qt.Equals, certcheck.DocumentNIE)
	c.Assert(res.Identity.Document.Number, qt.Equals, "X1234567L")

	for _, tc := range []struct {
		name   string
		holder Holder
		code   certcheck.ErrorCode
	}{
		{"revoked", Holder{Document: "12345678Z", OU: OURevoked}, certcheck.CodeRevoked},
		{"outage", Holder{Document: "12345678Z", OU: OUOutage}, certcheck.CodeRevocationUnavailable},
		{"expired", Holder{Document: "12345678Z", NotAfter: time.Now().Add(-time.Hour)}, certcheck.CodeExpired},
		{"not yet valid", Holder{Document: "12345678Z", NotBefore: time.Now().Add(time.Hour)}, certcheck.CodeNotYetValid},
		{"wrong letter", Holder{Document: "12345678A"}, certcheck.CodeIdentityInvalid},
		{"not canonical", Holder{Document: "12.345.678-Z"}, certcheck.CodeIdentityInvalid},
	} {
		leaf, err := pki.Issue(tc.holder)
		c.Assert(err, qt.IsNil)
		e := verifyErr(c, h, leaf.Leaf)
		c.Assert(e.Code, qt.Equals, tc.code, qt.Commentf("%s", tc.name))
		c.Assert(e.Retryable, qt.Equals, tc.code == certcheck.CodeRevocationUnavailable, qt.Commentf("%s", tc.name))
		c.Assert(e.Diagnostics.DiagnosticID, qt.HasLen, 32)
	}

	// a forged certificate naming a real document chains to nothing the handler trusts
	forged, err := SelfSigned(Holder{Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	c.Assert(verifyErr(c, h, forged.Leaf).Code, qt.Equals, certcheck.CodeUntrustedIssuer)

	// the harness round-trips its CA through PEM
	keyPEM, err := pki.KeyPEM()
	c.Assert(err, qt.IsNil)
	loaded, err := LoadPKI(pki.CertPEM(), keyPEM)
	c.Assert(err, qt.IsNil)
	again, err := loaded.Issue(Holder{Document: "12345678Z"})
	c.Assert(err, qt.IsNil)
	_, err = h.Verify(context.Background(), []*x509.Certificate{again.Leaf})
	c.Assert(err, qt.IsNil)

	_, err = New([]byte("not a pem"))
	c.Assert(err, qt.Not(qt.IsNil))
}
