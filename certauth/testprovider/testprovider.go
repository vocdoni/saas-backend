// Package testprovider is a synthetic cert-check provider for integration tests and the e2e stack:
// cert-check rejects the specimens real CAs publish as test_certificate, so tests mint their own
// natural-person certificates from an in-memory CA (PKI) and the backend trusts that CA through
// Provider. Its key never lives in the backend: the harness holds the PKI, the backend only the
// CA certificate. Never enable it against a production API.
package testprovider

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/internal"
)

// ID is the provider ID of the synthetic provider.
const ID = "test"

// caName is the display name of the synthetic provider and the common name of its CA.
const caName = "Synthetic Test CA"

// Organizational units a leaf can carry to answer a deterministic error without any network.
const (
	// OURevoked makes Verify answer revoked.
	OURevoked = "REVOKED"
	// OUOutage makes Verify answer revocation_unavailable, as an OCSP/CRL outage does.
	OUOutage = "OUTAGE"
)

// documentPrefix is how the leaf subject serialNumber carries the document (ETSI EN 319 412-1).
const documentPrefix = "IDCES-"

var (
	oidGivenName = asn1.ObjectIdentifier{2, 5, 4, 42}
	oidSurname   = asn1.ObjectIdentifier{2, 5, 4, 4}
)

// Provider is the synthetic certcheck.Provider: it accepts natural-person leaves signed by its CA.
type Provider struct {
	ca  *x509.Certificate
	now func() time.Time
}

// New returns the provider of the CA certificate in caPEM (the first CERTIFICATE block).
func New(caPEM []byte) (*Provider, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("testprovider: no CERTIFICATE block in the CA PEM")
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	if !ca.IsCA {
		return nil, fmt.Errorf("testprovider: the certificate is not a CA")
	}
	return NewWithCA(ca), nil
}

// NewWithCA returns the provider of ca.
func NewWithCA(ca *x509.Certificate) *Provider {
	return &Provider{ca: ca, now: time.Now}
}

// Info implements certcheck.Provider.
func (*Provider) Info() certcheck.ProviderInfo {
	return certcheck.ProviderInfo{
		ID: ID, Name: caName, Beta: true,
		Description: "Synthetic certificates for tests and the e2e stack",
	}
}

// CAs implements certcheck.Provider.
func (p *Provider) CAs() []*x509.Certificate { return []*x509.Certificate{p.ca} }

// Verify implements certcheck.Provider: the leaf must be signed by the CA and within its validity;
// the OUs REVOKED and OUTAGE answer revoked and revocation_unavailable; the subject serialNumber
// "IDCES-<DNI or NIE>" must hold a canonical number, and givenName/surname (or the CN) name the holder.
func (p *Provider) Verify(_ context.Context, chain []*x509.Certificate) (*certcheck.Result, error) {
	if len(chain) == 0 || chain[0] == nil {
		return nil, p.fail(nil, certcheck.StepBounds, certcheck.CodeNoCertificate, "no client certificate")
	}
	leaf := chain[0]
	if len(leaf.Raw) > certcheck.MaxCertificateDER {
		return nil, p.fail(nil, certcheck.StepBounds, certcheck.CodeInvalidChain, "leaf too large")
	}
	if err := leaf.CheckSignatureFrom(p.ca); err != nil {
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeUntrustedIssuer, "not signed by the synthetic CA")
	}
	now := p.now()
	switch {
	case now.Before(leaf.NotBefore):
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeNotYetValid, "not yet valid")
	case now.After(leaf.NotAfter):
		return nil, p.fail(leaf, certcheck.StepPath, certcheck.CodeExpired, "expired")
	}
	identity, ok := leafIdentity(leaf)
	if !ok {
		return nil, p.fail(leaf, certcheck.StepIdentity, certcheck.CodeIdentityInvalid, "no canonical IDCES document")
	}
	for _, ou := range leaf.Subject.OrganizationalUnit {
		switch ou {
		case OURevoked:
			return nil, p.fail(leaf, certcheck.StepRevocation, certcheck.CodeRevoked, "synthetic revocation")
		case OUOutage:
			return nil, p.fail(leaf, certcheck.StepRevocation, certcheck.CodeRevocationUnavailable, "synthetic outage")
		}
	}
	sum := sha256.Sum256(leaf.Raw)
	return &certcheck.Result{
		Provider: certcheck.ProviderRef{ID: ID, Name: caName},
		Profile: certcheck.ProfileInfo{
			ID: "test-natural-person", Name: "Synthetic natural person", HolderRole: certcheck.RoleNaturalPerson,
		},
		Identity: identity,
		Certificate: certcheck.CertificateInfo{
			Subject: leaf.Subject.String(), Issuer: p.ca.Subject.CommonName, SerialHex: fmt.Sprintf("%x", leaf.SerialNumber),
			NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(), SHA256: hex.EncodeToString(sum[:]),
		},
		ValidatedAt: now.UTC(),
		Diagnostics: certcheck.Diagnostics{
			DiagnosticID: certcheck.NewDiagnosticID(), Time: now.UTC(), Provider: ID,
			Certificate: certcheck.DescribeCertificate(leaf),
		},
	}, nil
}

// fail builds the *certcheck.Error of a failed step, with Diagnostics as cert-check fills them.
func (p *Provider) fail(leaf *x509.Certificate, step certcheck.Step, code certcheck.ErrorCode, detail string,
) *certcheck.Error {
	detail = "testprovider: " + detail
	d := certcheck.Diagnostics{
		DiagnosticID: certcheck.NewDiagnosticID(), Time: p.now().UTC(), Provider: ID,
		FailedStep: step, Code: code, TechnicalDetail: detail,
	}
	if leaf != nil {
		d.Certificate = certcheck.DescribeCertificate(leaf)
	}
	return &certcheck.Error{Code: code, Retryable: code.Retryable(), Detail: detail, Diagnostics: d}
}

// leafIdentity reads the holder identity from the leaf subject, reporting false unless its
// serialNumber is "IDCES-" and a canonical DNI or NIE.
func leafIdentity(leaf *x509.Certificate) (certcheck.Identity, bool) {
	raw := leaf.Subject.SerialNumber
	number, found := strings.CutPrefix(raw, documentPrefix)
	if !found {
		return certcheck.Identity{}, false
	}
	n := internal.NormalizeNationalID(number)
	if n.Class != internal.NationalIDCanonical || n.Canonical != number {
		return certcheck.Identity{}, false
	}
	docType := certcheck.DocumentDNI
	if n.Type == internal.NationalIDTypeNIE {
		docType = certcheck.DocumentNIE
	}
	given, surname := nameAttribute(leaf, oidGivenName), nameAttribute(leaf, oidSurname)
	fullName := strings.TrimSpace(given + " " + surname)
	if fullName == "" {
		fullName = leaf.Subject.CommonName
	}
	return certcheck.Identity{
		Document: certcheck.Document{
			Type: docType, Country: "ES", Number: n.Canonical, Raw: raw, Source: "subject.2.5.4.5",
		},
		GivenNames: given, Surnames: surname, FullName: fullName,
	}, true
}

// nameAttribute returns the first subject attribute of type oid, or "".
func nameAttribute(c *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, atv := range c.Subject.Names {
		if atv.Type.Equal(oid) {
			if s, ok := atv.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
