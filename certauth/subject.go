package certauth

import (
	"crypto/x509"
	"encoding/asn1"
	"strings"

	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/internal"
)

var (
	oidGivenName = asn1.ObjectIdentifier{2, 5, 4, 42}
	oidSurname   = asn1.ObjectIdentifier{2, 5, 4, 4}
)

// subjectDocumentPrefixes are the ETSI semantic identifiers a Spanish personal certificate writes
// its document with (subject serialNumber).
var subjectDocumentPrefixes = []string{"IDCES-", "PNOES-"}

// subjectHolder reads a display holder from a leaf subject, for the failures that show one
// (expired, not_yet_valid, revoked), or nil when it does not parse. The subject is unverified
// bytes: this is display only, never an authorization or budget input.
func subjectHolder(leaf *x509.Certificate) *apicommon.CertAuthHolder {
	if leaf == nil {
		return nil
	}
	serial := leaf.Subject.SerialNumber
	for _, prefix := range subjectDocumentPrefixes {
		serial = strings.TrimPrefix(serial, prefix)
	}
	n := internal.NormalizeNationalID(serial)
	if n.Canonical == "" {
		return nil
	}
	name := strings.TrimSpace(subjectAttribute(leaf, oidGivenName) + " " + subjectAttribute(leaf, oidSurname))
	if name == "" {
		name = strings.TrimSpace(leaf.Subject.CommonName)
	}
	if name == "" {
		return nil
	}
	return &apicommon.CertAuthHolder{
		FullName: name, DocumentType: string(n.Type), DocumentEnding: n.Canonical[len(n.Canonical)-4:],
	}
}

// resultHolder is the holder of a verified identity.
func resultHolder(id certcheck.Identity) apicommon.CertAuthHolder {
	number := id.Document.Number
	ending := number
	if len(number) > 4 {
		ending = number[len(number)-4:]
	}
	return apicommon.CertAuthHolder{FullName: id.FullName, DocumentType: string(id.Document.Type), DocumentEnding: ending}
}

// subjectAttribute returns the first subject attribute of type oid, or "".
func subjectAttribute(c *x509.Certificate, oid asn1.ObjectIdentifier) string {
	for _, atv := range c.Subject.Names {
		if atv.Type.Equal(oid) {
			if s, ok := atv.Value.(string); ok {
				return s
			}
		}
	}
	return ""
}
