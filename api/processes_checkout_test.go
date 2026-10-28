package api

import (
	"fmt"
	"sync"
	"testing"

	"github.com/vocdoni/saas-backend/stripe"
)

// fakePaymentGW implements paymentGateway in memory: sessions are created open and can
// be inspected, expired or force-completed by the test.
type fakePaymentGW struct {
	mu       sync.Mutex
	seq      int
	sessions map[string]*stripe.PaymentSessionInfo
	created  []*stripe.PaymentSessionParams
	expired  []string
	// expireErr, when set, makes ExpirePaymentSession fail (Stripe unreachable)
	expireErr error
	// createErr, when set, makes CreatePaymentSession fail (Stripe unreachable)
	createErr error
	// expireFn, when set, runs once after a session is expired: a request racing the expiry
	expireFn func(sessionID string)
	// createFn, when set, runs as a session is created: a request racing the checkout
	createFn func()
}

func newFakePaymentGW() *fakePaymentGW {
	return &fakePaymentGW{sessions: map[string]*stripe.PaymentSessionInfo{}}
}

func (f *fakePaymentGW) CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error) {
	if f.createFn != nil {
		f.createFn()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.seq++
	session := &stripe.PaymentSessionInfo{
		ID:            fmt.Sprintf("cs_test_%d", f.seq),
		ClientSecret:  fmt.Sprintf("cs_test_%d_secret", f.seq),
		Status:        stripe.SessionStatusOpen,
		PaymentStatus: stripe.PaymentStatusUnpaid,
	}
	f.sessions[session.ID] = session
	f.created = append(f.created, params)
	return session, nil
}

func (f *fakePaymentGW) GetPaymentSession(sessionID string) (*stripe.PaymentSessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	copied := *session
	return &copied, nil
}

func (f *fakePaymentGW) ExpirePaymentSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expireErr != nil {
		return f.expireErr
	}
	session, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	session.Status = stripe.SessionStatusExpired
	f.expired = append(f.expired, sessionID)
	if fn := f.expireFn; fn != nil {
		f.expireFn = nil
		f.mu.Unlock()
		fn(sessionID)
		f.mu.Lock()
	}
	return nil
}

// installFakePaymentGW swaps the API's payment gateway for a fake for one test.
func installFakePaymentGW(t *testing.T) *fakePaymentGW {
	t.Helper()
	fake := newFakePaymentGW()
	prev := testAPI.paymentGW
	testAPI.paymentGW = fake
	t.Cleanup(func() { testAPI.paymentGW = prev })
	return fake
}
