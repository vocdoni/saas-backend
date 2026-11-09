package testprovider

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"
)

// PKI is an in-memory synthetic CA that mints the client certificates of the synthetic provider,
// and server certificates for test listeners. Tests create one with NewPKI; the e2e harness keeps
// one on disk with CertPEM/KeyPEM and LoadPKI.
type PKI struct {
	// CA is the CA certificate the backend trusts (New, NewWithCA).
	CA  *x509.Certificate
	key *ecdsa.PrivateKey
}

// Holder describes a client certificate to mint.
type Holder struct {
	GivenName string
	Surname   string
	// Document is the canonical DNI/NIE, written into the subject serialNumber as "IDCES-<Document>".
	Document string
	// OU, when set, is the subject organizational unit: OURevoked or OUOutage pick an error answer.
	OU string
	// NotBefore and NotAfter default to an hour ago and a year ahead.
	NotBefore time.Time
	NotAfter  time.Time
}

// NewPKI returns a fresh synthetic CA.
func NewPKI() (*PKI, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("testprovider: generating the CA key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: caName, Organization: []string{"Vocdoni test"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("testprovider: creating the CA certificate: %w", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	return &PKI{CA: ca, key: key}, nil
}

// LoadPKI returns the PKI of a CA certificate and its EC private key, both PEM.
func LoadPKI(certPEM, keyPEM []byte) (*PKI, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("testprovider: loading the CA key pair: %w", err)
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("testprovider: the CA key is not an EC key")
	}
	ca, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the CA certificate: %w", err)
	}
	return &PKI{CA: ca, key: key}, nil
}

// CertPEM returns the CA certificate as PEM, what --certAuthTestProviderCA points at.
func (p *PKI) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.CA.Raw})
}

// KeyPEM returns the CA private key as PEM, for the harness only.
func (p *PKI) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(p.key)
	if err != nil {
		return nil, fmt.Errorf("testprovider: encoding the CA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

// Issue mints a client certificate for h, signed by the CA.
func (p *PKI) Issue(h Holder) (*tls.Certificate, error) {
	return issue(h, p.CA, p.key)
}

// SelfSigned mints a client certificate for h signed by its own fresh key, not by any CA: a forged
// certificate that names a document without proving anything.
func SelfSigned(h Holder) (*tls.Certificate, error) {
	return issue(h, nil, nil)
}

// ServerCertificate mints a TLS server certificate for dnsNames (wildcards allowed), signed by the
// CA, as PEM certificate and key: what a test auth listener serves.
func (p *PKI) ServerCertificate(dnsNames ...string) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: generating the server key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(1, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.CA, &key.PublicKey, p.key)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: creating the server certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("testprovider: encoding the server key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// issue mints a client certificate for h, signed by parent and parentKey, or self-signed when
// parent is nil.
func issue(h Holder, parent *x509.Certificate, parentKey crypto.Signer) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("testprovider: generating the client key: %w", err)
	}
	notBefore, notAfter := h.NotBefore, h.NotAfter
	if notBefore.IsZero() {
		notBefore = time.Now().Add(-time.Hour)
	}
	if notAfter.IsZero() {
		notAfter = time.Now().AddDate(1, 0, 0)
	}
	subject := pkix.Name{
		CommonName:   fmt.Sprintf("%s %s - %s", h.Surname, h.GivenName, h.Document),
		SerialNumber: documentPrefix + h.Document,
		Country:      []string{"ES"},
		ExtraNames: []pkix.AttributeTypeAndValue{
			{Type: oidGivenName, Value: h.GivenName},
			{Type: oidSurname, Value: h.Surname},
		},
	}
	if h.OU != "" {
		subject.OrganizationalUnit = []string{h.OU}
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      subject,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if parent == nil {
		parent, parentKey = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		return nil, fmt.Errorf("testprovider: creating the client certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("testprovider: parsing the client certificate: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// serial returns a random 128-bit certificate serial number.
func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err) // crypto/rand never fails since Go 1.24
	}
	return n
}
