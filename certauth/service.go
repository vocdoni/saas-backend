package certauth

import (
	"crypto/tls"
	"fmt"
	"slices"
	"time"

	"github.com/ethereum/go-ethereum/common"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// Storage is the persistence the certificate flow needs; *db.MongoStorage implements it.
type Storage interface {
	InsertCertAuthTransaction(tx *db.CertAuthTransaction) error
	ConsumeCertAuthState(in db.CertAuthStateConsume) (*db.CertAuthTransaction, error)
	CertAuthTransactionByState(state, nonce string) (*db.CertAuthTransaction, error)
	ConsumeCertAuthCode(code string, authenticatedAfter time.Time) (*db.CertAuthTransaction, error)
	CertAuthTransactionByCode(code string) (*db.CertAuthTransaction, error)
	ProcessWithQuestions(id bson.ObjectID) (*db.VotingProcess, []db.VotingProcessQuestion, error)
	Census(censusID string) (*db.Census, error)
	CensusParticipantByLoginHash(census db.Census, member db.OrgMember) (*db.CensusParticipant, error)
	OrgMember(orgAddress common.Address, id string) (*db.OrgMember, error)
	ClaimCSPCounter(kind string, key []byte, maxCount int) (db.CSPCounterResult, error)
	ReleaseCSPCounter(claim db.CSPCounterClaim) error
}

// TokenIssuer issues the pre-verified auth-only CSP token an auth/0 census issues; *csp.CSP
// implements it.
type TokenIssuer interface {
	AuthToken(anchorID, uID internal.HexBytes, to string, ctype notifications.ChallengeType, lang string,
		orgName, orgLogo string, orgAddress common.Address) (internal.HexBytes, error)
}

// Deps are the services the certificate flow runs on.
type Deps struct {
	Storage Storage
	Tokens  TokenIssuer
}

// logger is the structured log sink. Tests replace it to check what reaches the logs: never a
// DNI/NIE, a name, a certificate, a state, a code or a verifier.
type logger interface {
	Infow(msg string, keyvalues ...any)
	Warnw(msg string, keyvalues ...any)
}

// dvoteLogger logs through the service logger.
type dvoteLogger struct{}

func (dvoteLogger) Infow(msg string, keyvalues ...any) { log.Infow(msg, keyvalues...) }
func (dvoteLogger) Warnw(msg string, keyvalues ...any) { log.Warnw(msg, keyvalues...) }

// Service is the certificate sign-in feature: the auth listener and the create and redeem
// handlers. It is immutable after New and safe for concurrent use.
type Service struct {
	cfg     Config
	v       *validated
	store   Storage
	tokens  TokenIssuer
	handler *certcheck.Handler
	// probeTLS and authTLS are the per-SNI server configurations of origin mode.
	probeTLS *tls.Config
	authTLS  *tls.Config
	refKey   []byte
	log      logger
	now      func() time.Time
}

// New validates cfg (spec section 1: every rule fails loudly) and builds the service: the
// cert-check handler of the configured natural-person providers, the TLS configurations and the
// voterRef key. Call it only when the feature is configured; feature off is a nil *Service.
func New(cfg Config, deps Deps) (*Service, error) {
	if deps.Storage == nil || deps.Tokens == nil {
		return nil, fmt.Errorf("certauth: storage and token issuer are required")
	}
	v, err := cfg.validate()
	if err != nil {
		return nil, err
	}
	handler, err := certcheck.NewHandler(v.providers)
	if err != nil {
		return nil, fmt.Errorf("certauth: building the certificate handler: %w", err)
	}
	s := &Service{
		cfg: cfg, v: v, store: deps.Storage, tokens: deps.Tokens, handler: handler,
		refKey: voterRefKey(cfg.RootKey), log: dvoteLogger{}, now: time.Now,
	}
	getCert := func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return v.serverCert, nil }
	s.authTLS = handler.TLSConfig(getCert)
	s.probeTLS = &tls.Config{
		MinVersion: tls.VersionTLS12, NextProtos: []string{alpnHTTP1}, GetCertificate: getCert,
		ClientAuth: tls.NoClientCert, SessionTicketsDisabled: true,
	}
	for _, w := range v.warnings {
		s.log.Warnw("certificate sign-in: " + w)
	}
	ids := make([]string, 0, len(v.providers))
	for _, p := range v.providers {
		ids = append(ids, p.Info().ID)
	}
	s.log.Infow("certificate sign-in enabled", "mode", cfg.Mode, "host", cfg.Host, "listen", cfg.ListenAddr,
		"providers", ids)
	return s, nil
}

// AllowedOrigins returns the allowlisted app origins, sorted.
func (s *Service) AllowedOrigins() []string {
	origins := make([]string, 0, len(s.v.origins))
	for o := range s.v.origins {
		origins = append(origins, o)
	}
	slices.Sort(origins)
	return origins
}
