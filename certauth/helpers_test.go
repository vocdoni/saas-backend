package certauth

import (
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	testHost      = "auth.test"
	testOrigin    = "https://app.test"
	testReturnURL = testOrigin + "/ca/processes/0123456789abcdef01234567"
	testDefault   = testOrigin + "/"
)

// memStore is an in-memory Storage: one process with its census and members, the transactions
// and the counters, with the semantics of the Mongo implementation (single-use consumes that re-check
// their deadlines, a capped counter that can be released).
type memStore struct {
	mu        sync.Mutex
	txs       []*db.CertAuthTransaction
	vp        *db.VotingProcess
	questions []db.VotingProcessQuestion
	census    *db.Census
	members   map[string]*db.OrgMember // by canonical nationalId
	counters  map[string]int
	reset     time.Time
}

func newMemStore() *memStore {
	census := &db.Census{
		ID: bson.NewObjectID(), OrgAddress: common.Address{1}, AuthMethod: db.CensusAuthMethodCertificate,
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID},
	}
	return &memStore{
		vp: &db.VotingProcess{
			ID: bson.NewObjectID(), Published: true, CensusID: census.ID, OrgAddress: census.OrgAddress,
			StartDate: time.Now().Add(-time.Hour), EndDate: time.Now().Add(time.Hour),
		},
		questions: []db.VotingProcessQuestion{{Status: db.QuestionStatusReady}},
		census:    census,
		members:   map[string]*db.OrgMember{},
		counters:  map[string]int{},
		reset:     time.Now().Add(30 * time.Minute),
	}
}

// addMember adds a census member whose nationalId canonicalises to the given number.
func (m *memStore) addMember(nationalID string, weight uint64) *db.OrgMember {
	m.mu.Lock()
	defer m.mu.Unlock()
	member := &db.OrgMember{ID: bson.NewObjectID(), OrgAddress: m.census.OrgAddress, NationalID: nationalID, Weight: weight}
	m.members[internal.NormalizeNationalID(nationalID).Normalized] = member
	return member
}

func (m *memStore) InsertCertAuthTransaction(tx *db.CertAuthTransaction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == tx.State {
			return fmt.Errorf("duplicate state")
		}
	}
	if tx.ID.IsZero() {
		tx.ID = bson.NewObjectID()
	}
	cp := *tx
	m.txs = append(m.txs, &cp)
	return nil
}

func (m *memStore) ConsumeCertAuthState(in db.CertAuthStateConsume) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == in.State && t.Nonce == in.Nonce && t.Status == db.CertAuthStatusCreated && t.CreatedAt.After(in.CreatedAfter) {
			before := *t
			t.Status, t.Code, t.Chain, t.ChainPresented = db.CertAuthStatusAuthenticated, in.Code, in.Chain, in.ChainPresented
			t.AuthenticatedAt, t.ExpiresAt = in.Now, in.ExpiresAt
			return &before, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) CertAuthTransactionByState(state, nonce string) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == state && t.Nonce == nonce {
			cp := *t
			return &cp, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) ConsumeCertAuthCode(code string, after time.Time) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.Code == code && t.Status == db.CertAuthStatusAuthenticated && t.AuthenticatedAt.After(after) {
			before := *t
			t.Status, t.Chain = db.CertAuthStatusConsumed, nil
			return &before, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) CertAuthTransactionByCode(code string) (*db.CertAuthTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.Code == code {
			cp := *t
			return &cp, nil
		}
	}
	return nil, db.ErrNotFound
}

// transaction returns the stored transaction holding the state.
func (m *memStore) transaction(state string) *db.CertAuthTransaction {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.txs {
		if t.State == state {
			cp := *t
			return &cp
		}
	}
	return nil
}

func (m *memStore) ProcessWithQuestions(id bson.ObjectID) (*db.VotingProcess, []db.VotingProcessQuestion, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != m.vp.ID {
		return nil, nil, db.ErrNotFound
	}
	vp := *m.vp
	return &vp, m.questions, nil
}

func (m *memStore) Census(string) (*db.Census, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *m.census
	return &c, nil
}

func (m *memStore) CensusParticipantByLoginHash(_ db.Census, member db.OrgMember) (*db.CensusParticipant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found, ok := m.members[internal.NormalizeNationalID(member.NationalID).Normalized]
	if !ok {
		return nil, db.ErrNotFound
	}
	return &db.CensusParticipant{ParticipantID: found.ID.Hex(), LoginHash: []byte("hash-" + found.ID.Hex())}, nil
}

func (m *memStore) OrgMember(_ common.Address, id string) (*db.OrgMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, member := range m.members {
		if member.ID.Hex() == id {
			return member, nil
		}
	}
	return nil, db.ErrNotFound
}

func (m *memStore) ClaimCSPCounter(kind string, key []byte, maxCount int) (db.CSPCounterResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxCount <= 0 {
		return db.CSPCounterResult{Granted: true}, nil
	}
	k := kind + "|" + string(key)
	if m.counters[k] >= maxCount {
		return db.CSPCounterResult{ResetAt: m.reset}, nil
	}
	m.counters[k]++
	return db.CSPCounterResult{Granted: true, Claim: db.CSPCounterClaim(k), ResetAt: m.reset}, nil
}

func (m *memStore) ReleaseCSPCounter(claim db.CSPCounterClaim) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k := string(claim); m.counters[k] > 0 {
		m.counters[k]--
	}
	return nil
}

// fakeTokens issues sequential auth-only tokens and records whom it issued them to.
type fakeTokens struct {
	mu     sync.Mutex
	issued []string
}

func (f *fakeTokens) AuthToken(anchorID, uID internal.HexBytes, to string, ctype notifications.ChallengeType,
	_, _, _ string, _ common.Address,
) (internal.HexBytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(anchorID) == 0 || to != "" || ctype != "" {
		return nil, fmt.Errorf("not an auth-only token request")
	}
	f.issued = append(f.issued, uID.String())
	return internal.HexBytes{byte(len(f.issued))}, nil
}

func (f *fakeTokens) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issued)
}

// logEntry is one captured log line.
type logEntry struct {
	msg string
	kv  map[string]any
}

// captureLog records what the service logs.
type captureLog struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *captureLog) add(msg string, keyvalues []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	kv := map[string]any{}
	for i := 0; i+1 < len(keyvalues); i += 2 {
		kv[fmt.Sprint(keyvalues[i])] = keyvalues[i+1]
	}
	l.entries = append(l.entries, logEntry{msg: msg, kv: kv})
}

func (l *captureLog) Infow(msg string, keyvalues ...any) { l.add(msg, keyvalues) }
func (l *captureLog) Warnw(msg string, keyvalues ...any) { l.add(msg, keyvalues) }

func (l *captureLog) all() []logEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]logEntry(nil), l.entries...)
}

// testConfig is a valid origin-mode configuration for the PKI, its listener on port.
func testConfig(c *qt.C, pki *testprovider.PKI, port int) Config {
	certPEM, keyPEM, err := pki.ServerCertificate("*." + testHost)
	c.Assert(err, qt.IsNil)
	return Config{
		Mode: ModeOrigin, Host: testHost, ListenAddr: "127.0.0.1:" + strconv.Itoa(port), PublicPort: port,
		TLSCertPEM: certPEM, TLSKeyPEM: keyPEM, Providers: []string{"fnmt", "idcat"},
		AllowedOrigins: []string{testOrigin, "http://localhost:3000"}, DefaultReturn: testDefault,
		CreatesPerMinute: DefaultCreatesPerMinute, TestProvider: true, TestProviderCAPEM: pki.CertPEM(),
		VocdoniAPI: "http://127.0.0.1:9090/v2", RootKey: []byte("test-root-key"),
	}
}

// newTestService builds a service over a memStore with cfg adjusted by mutate, its logs captured.
// It serves nothing: newTestEnv adds the loopback auth listener.
func newTestService(c *qt.C, port int, mutate func(*Config)) (*Service, *memStore, *testprovider.PKI) {
	pki, err := testprovider.NewPKI()
	c.Assert(err, qt.IsNil)
	cfg := testConfig(c, pki, port)
	if mutate != nil {
		mutate(&cfg)
	}
	store := newMemStore()
	s, err := New(cfg, Deps{Storage: store, Tokens: &fakeTokens{}})
	c.Assert(err, qt.IsNil)
	s.log = &captureLog{}
	return s, store, pki
}
