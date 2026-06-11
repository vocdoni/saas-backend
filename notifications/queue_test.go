package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// configurableSvc is a NotificationService whose behaviour can be tuned per
// test: fails the first failFor calls (0 = every call) with sendErr, and
// optionally delays each send to exercise concurrency.
type configurableSvc struct {
	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	failFor     int
	sendErr     error
	sendDelay   time.Duration
}

func (*configurableSvc) New(any) error { return nil }

func (m *configurableSvc) SendNotification(ctx context.Context, _ *Notification) error {
	m.mu.Lock()
	m.calls++
	call := m.calls
	delay := m.sendDelay
	m.inFlight++
	if m.inFlight > m.maxInFlight {
		m.maxInFlight = m.inFlight
	}
	var sendErr error
	if m.sendErr != nil && (m.failFor == 0 || call <= m.failFor) {
		sendErr = m.sendErr
	}
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}

	m.mu.Lock()
	m.inFlight--
	m.mu.Unlock()
	return sendErr
}

func (m *configurableSvc) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *configurableSvc) maxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxInFlight
}

// testNotification returns a minimal valid Notification for use in queue tests.
func testNotification() *Notification {
	return &Notification{
		ToAddress: "recipient@example.com",
		Subject:   "test subject",
		Body:      "test body",
	}
}

func TestIsPermanentSendError(t *testing.T) {
	c := qt.New(t)

	perm := &textproto.Error{Code: 550, Msg: "no such user"}
	transientProto := &textproto.Error{Code: 451, Msg: "try later"}
	netErr := fmt.Errorf("dial tcp: connection refused")

	c.Assert(IsPermanentSendError(nil), qt.IsFalse)
	c.Assert(IsPermanentSendError(perm), qt.IsTrue)
	c.Assert(IsPermanentSendError(transientProto), qt.IsFalse)
	c.Assert(IsPermanentSendError(netErr), qt.IsFalse)
	// wrapped permanent stays permanent
	c.Assert(IsPermanentSendError(fmt.Errorf("provider: %w", perm)), qt.IsTrue)
	// 5xx boundary
	c.Assert(IsPermanentSendError(&textproto.Error{Code: 500}), qt.IsTrue)
	c.Assert(IsPermanentSendError(&textproto.Error{Code: 599}), qt.IsTrue)
	c.Assert(IsPermanentSendError(&textproto.Error{Code: 499}), qt.IsFalse)
	c.Assert(IsPermanentSendError(&textproto.Error{Code: 600}), qt.IsFalse)

	// failover-style joined errors: permanent only when every branch is permanent
	c.Assert(IsPermanentSendError(errors.Join(
		fmt.Errorf("p0: %w", perm),
		fmt.Errorf("p1: %w", perm),
	)), qt.IsTrue)
	c.Assert(IsPermanentSendError(errors.Join(
		fmt.Errorf("p0: %w", perm),
		netErr,
	)), qt.IsFalse)
	c.Assert(IsPermanentSendError(errors.Join(
		fmt.Errorf("p0: %w", perm),
		fmt.Errorf("p1: %w", transientProto),
	)), qt.IsFalse)
}

func TestQueuePush(t *testing.T) {
	c := qt.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: &configurableSvc{}})

	c.Assert(q.Push(nil), qt.Not(qt.IsNil))
	c.Assert(q.Push(&QueueItem{Notification: nil}), qt.Not(qt.IsNil))
	c.Assert(q.Push(&QueueItem{Notification: testNotification()}), qt.IsNil)
}

func TestQueueDefaults(t *testing.T) {
	c := qt.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := NewQueue(ctx, QueueConfig{})
	c.Assert(q.workers, qt.Equals, DefaultQueueWorkers)
	c.Assert(q.maxRetries, qt.Equals, DefaultQueueMaxRetries)
	c.Assert(q.ttl, qt.Equals, DefaultQueueTTL)
	c.Assert(q.mailBreaker.maxFailures, qt.Equals, DefaultBreakerMaxFailures)
	c.Assert(q.mailBreaker.cooldown, qt.Equals, DefaultBreakerCooldown)
}

func TestQueueDeliverEmail(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &configurableSvc{}
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: svc})
	q.Start()

	item := &QueueItem{Notification: testNotification(), Type: Email, Label: "test-email"}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsTrue)
		c.Assert(res.Retries, qt.Equals, 0)
		c.Assert(svc.callCount(), qt.Equals, 1)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for email delivery")
	}
}

func TestQueueDeliverSMS(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	smsSvc := &configurableSvc{}
	mailSvc := &configurableSvc{}
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: mailSvc, SMSService: smsSvc})
	q.Start()

	item := &QueueItem{
		Notification: &Notification{ToNumber: "+1234567890", Body: "code: 123456"},
		Type:         SMS,
		Label:        "test-sms",
	}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsTrue)
		c.Assert(smsSvc.callCount(), qt.Equals, 1)
		c.Assert(mailSvc.callCount(), qt.Equals, 0)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for SMS delivery")
	}
}

func TestQueueMetaPassthrough(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &configurableSvc{}
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: svc})
	q.Start()

	type myMeta struct{ ID int }
	item := &QueueItem{
		Notification: testNotification(),
		Type:         Email,
		Meta:         &myMeta{ID: 42},
	}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsTrue)
		meta, ok := res.Meta.(*myMeta)
		c.Assert(ok, qt.IsTrue)
		c.Assert(meta.ID, qt.Equals, 42)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for meta passthrough")
	}
}

func TestQueueNilService(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// no mail service configured — item must be given up immediately
	q := NewQueue(ctx, QueueConfig{Workers: 1})
	q.Start()

	item := &QueueItem{Notification: testNotification(), Type: Email, Label: "no-service"}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsFalse)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for nil-service drop")
	}
}

func TestQueuePermanentFailure(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &configurableSvc{sendErr: &textproto.Error{Code: 550, Msg: "no such user"}}
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: svc})
	q.Start()

	item := &QueueItem{Notification: testNotification(), Type: Email}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsFalse)
		c.Assert(res.Retries, qt.Equals, 0)
		c.Assert(svc.callCount(), qt.Equals, 1)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for permanent failure")
	}
}

func TestQueueRetriesExhausted(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const maxRetries = 3
	svc := &configurableSvc{sendErr: fmt.Errorf("451 try again")} // transient forever
	q := NewQueue(ctx, QueueConfig{
		Workers:            1,
		MaxRetries:         maxRetries,
		TTL:                time.Minute,
		MailService:        svc,
		BreakerMaxFailures: 1 << 30, // disable breaker so retries aren't masked
	})
	q.Start()

	item := &QueueItem{Notification: testNotification(), Type: Email}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsFalse)
		c.Assert(res.Retries, qt.Equals, maxRetries)
	case <-time.After(30 * time.Second):
		c.Fatal("timed out waiting for retries to be exhausted")
	}
}

func TestQueueTTLExpired(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &configurableSvc{sendErr: fmt.Errorf("451 try again")}
	q := NewQueue(ctx, QueueConfig{
		Workers:            1,
		TTL:                time.Minute,
		MailService:        svc,
		BreakerMaxFailures: 1 << 30,
	})
	q.Start()

	// CreatedAt far in the past — already past TTL, dropped after first failure
	item := &QueueItem{
		Notification: testNotification(),
		Type:         Email,
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsFalse)
		c.Assert(res.Retries, qt.Equals, 0)
	case <-time.After(10 * time.Second):
		c.Fatal("timed out waiting for TTL drop")
	}
}

func TestQueueItemExpired(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := &configurableSvc{} // would succeed if called
	q := NewQueue(ctx, QueueConfig{Workers: 1, MailService: svc})
	q.Start()

	// ExpiresAt in the past — must be dropped without ever calling the service
	item := &QueueItem{
		Notification: testNotification(),
		Type:         Email,
		ExpiresAt:    time.Now().Add(-time.Minute),
	}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsFalse)
		c.Assert(svc.callCount(), qt.Equals, 0)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for expired item drop")
	}
}

func TestQueueBreakerRecovery(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const cooldown = 50 * time.Millisecond
	svc := &configurableSvc{
		sendErr: fmt.Errorf("452 mailbox full, try again"), // transient
		failFor: 2,                                         // first 2 calls fail, then succeed
	}
	q := NewQueue(ctx, QueueConfig{
		Workers:            1,
		TTL:                time.Minute,
		MailService:        svc,
		BreakerMaxFailures: 2,
		BreakerCooldown:    cooldown,
	})
	q.Start()

	item := &QueueItem{Notification: testNotification(), Type: Email}
	c.Assert(q.Push(item), qt.IsNil)

	select {
	case res := <-q.Done:
		c.Assert(res.Success, qt.IsTrue)
		// 2 transient failures trip the breaker, then the probe succeeds
		c.Assert(svc.callCount() >= 3, qt.IsTrue)
		c.Assert(res.Retries, qt.Equals, 2)
	case <-time.After(5 * time.Second):
		c.Fatal("timed out waiting for breaker recovery")
	}
}

func TestQueueConcurrency(t *testing.T) {
	c := qt.New(t)
	c.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const total = 10
	const workers = 10
	const sendDelay = 100 * time.Millisecond

	svc := &configurableSvc{sendDelay: sendDelay}
	q := NewQueue(ctx, QueueConfig{Workers: workers, TTL: time.Minute, MailService: svc})
	q.Start()

	for i := 0; i < total; i++ {
		item := &QueueItem{
			Notification: testNotification(),
			Type:         Email,
			Label:        fmt.Sprintf("item-%d", i),
		}
		c.Assert(q.Push(item), qt.IsNil)
	}

	delivered := 0
	for delivered < total {
		select {
		case res := <-q.Done:
			c.Assert(res.Success, qt.IsTrue)
			delivered++
		case <-time.After(10 * time.Second):
			c.Fatalf("timed out: only %d/%d delivered", delivered, total)
		}
	}
	c.Assert(svc.callCount(), qt.Equals, total)
	// with concurrent workers and per-send delay, at least 2 must have overlapped
	c.Assert(svc.maxConcurrent() >= 2, qt.IsTrue)
}
