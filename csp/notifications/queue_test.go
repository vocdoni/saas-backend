package notifications

import (
	"context"
	"errors"
	"fmt"
	"net/textproto"
	"regexp"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// errNoSuchUser is the message of a permanent (5xx) SMTP error reused across
// tests that exercise the give-up / no-retry path.
const errNoSuchUser = "no such user"

// configurableMail is a NotificationService mock whose behaviour can be tuned
// per test: it can fail the first failFor calls (failFor == 0 means every call)
// with sendErr, and optionally delay each send to exercise concurrency.
type configurableMail struct {
	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	failFor     int
	sendErr     error
	sendDelay   time.Duration
}

func (*configurableMail) New(any) error { return nil }

func (m *configurableMail) SendNotification(ctx context.Context, _ *notifications.Notification) error {
	m.mu.Lock()
	m.calls++
	call := m.calls
	delay := m.sendDelay
	m.inFlight++
	if m.inFlight > m.maxInFlight {
		m.maxInFlight = m.inFlight
	}
	var err error
	if m.sendErr != nil && (m.failFor == 0 || call <= m.failFor) {
		err = m.sendErr
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
	return err
}

func (m *configurableMail) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *configurableMail) maxConcurrent() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxInFlight
}

func TestIsPermanentSendError(t *testing.T) {
	c := qt.New(t)

	perm := &textproto.Error{Code: 550, Msg: errNoSuchUser}
	transientProto := &textproto.Error{Code: 451, Msg: "try later"}
	netErr := fmt.Errorf("dial tcp: connection refused")

	c.Assert(isPermanentSendError(nil), qt.IsFalse)
	c.Assert(isPermanentSendError(perm), qt.IsTrue)
	c.Assert(isPermanentSendError(transientProto), qt.IsFalse)
	c.Assert(isPermanentSendError(netErr), qt.IsFalse)
	// wrapped permanent (as a failover branch wraps it with %w) stays permanent
	c.Assert(isPermanentSendError(fmt.Errorf("provider 0: %w", perm)), qt.IsTrue)
	// validation/configuration errors are permanent: retrying cannot fix them
	c.Assert(isPermanentSendError(ErrInvalidNotificationService), qt.IsTrue)
	c.Assert(isPermanentSendError(ErrInvalidNotificationInputs), qt.IsTrue)
	c.Assert(isPermanentSendError(fmt.Errorf("wrapped: %w", ErrInvalidNotificationService)), qt.IsTrue)

	// failover-style joined errors: permanent only when every branch is permanent
	c.Assert(isPermanentSendError(errors.Join(
		fmt.Errorf("provider 0: %w", perm),
		fmt.Errorf("provider 1: %w", perm),
	)), qt.IsTrue)
	c.Assert(isPermanentSendError(errors.Join(
		fmt.Errorf("provider 0: %w", perm),
		fmt.Errorf("provider 1: %w", netErr),
	)), qt.IsFalse)
	c.Assert(isPermanentSendError(errors.Join(
		fmt.Errorf("provider 0: %w", perm),
		fmt.Errorf("provider 1: %w", transientProto),
	)), qt.IsFalse)
}

func TestNotificationChallengeQueue(t *testing.T) {
	c := qt.New(t)
	// create a notification without to address to force an error during the
	// sending (compose fails: a transient, retriable error)
	c.Assert(mailtemplates.Load(), qt.IsNil)
	notification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).ExecPlain(struct {
		Code         string
		Organization string
	}{"123456", testOrgName})
	c.Assert(err, qt.IsNil)

	c.Run("retries reached", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Disable the breaker (very high threshold) so this single failing
		// message exhausts its retries instead of tripping the breaker.
		queue := NewQueue(ctx, QueueConfig{
			TTL:                time.Minute,
			Workers:            1,
			MailService:        testMailService,
			SMSService:         testSMSService,
			BreakerMaxFailures: 1 << 30,
		})
		queue.Start()
		c.Assert(queue.Push(&NotificationChallenge{
			Type:         EmailChallenge,
			UserID:       []byte("user"),
			BundleID:     []byte("bundle"),
			Notification: notification,
			CreatedAt:    time.Now(),
			Retries:      0,
			Success:      false,
		}), qt.IsNil)

		select {
		case errCh := <-queue.NotificationsSent:
			c.Assert(errCh.Success, qt.IsFalse)
			c.Assert(errCh.Retries, qt.Equals, DefaultQueueMaxRetries)
		case <-time.After(DefaultQueueMaxRetries * time.Second * 2):
			c.Fatal("timed out waiting for retries to be reached")
		}
	})

	c.Run("ttl reached", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, QueueConfig{
			TTL:                time.Minute,
			Workers:            1,
			MailService:        testMailService,
			SMSService:         testSMSService,
			BreakerMaxFailures: 1 << 30,
		})
		queue.Start()

		// CreatedAt in the past so the challenge is already older than the TTL:
		// it must be dropped on the first failed attempt without any retry.
		c.Assert(queue.Push(&NotificationChallenge{
			Type:         EmailChallenge,
			UserID:       []byte("user"),
			BundleID:     []byte("bundle"),
			Notification: notification,
			CreatedAt:    time.Now().Add(-time.Hour),
			Retries:      0,
			Success:      false,
		}), qt.IsNil)

		select {
		case errCh := <-queue.NotificationsSent:
			c.Assert(errCh.Success, qt.IsFalse)
			c.Assert(errCh.Retries, qt.Equals, 0)
		case <-time.After(time.Second * 25):
			c.Fatal("timed out waiting for ttl to be reached")
		}
	})

	c.Run("success", func(c *qt.C) {
		c.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Second * 10,
			Workers:     1,
			MailService: testMailService,
			SMSService:  testSMSService,
		})
		queue.Start()

		// templates are already loaded by the parent test; do not call
		// mailtemplates.Load() here as it races the parallel subtests that read
		// the template map concurrently.
		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		c.Assert(queue.Push(nc), qt.IsNil)

		select {
		case res := <-queue.NotificationsSent:
			c.Assert(res.Success, qt.IsTrue)
			// get the verification code from the email
			mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
			c.Assert(err, qt.IsNil)
			// parse the email body to get the verification code
			seedNotification, err := mailtemplates.VerifyOTPCodeNotification.Localized(apicommon.DefaultLang).
				ExecPlain(struct {
					Code         string
					Organization string
				}{`(.{6})`, testOrgName})
			c.Assert(err, qt.IsNil)
			rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
			// verify the user
			mailCode := rgxNotification.FindStringSubmatch(mailBody)
			c.Assert(mailCode, qt.HasLen, 2)
			c.Assert(mailCode[1], qt.Equals, "123456")
		case <-time.After(time.Second * 25):
			c.Fatal("timed out waiting for success")
		}
	})

	// concurrency verifies that the worker pool delivers many challenges in
	// parallel: with a per-send delay, the total time must be far below the
	// serial sum (workers * delay vs total * delay).
	c.Run("concurrency", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const total = 10
		const workers = 10
		// Hold each send open long enough that, if the pool is truly
		// concurrent, multiple sends overlap in flight at once.
		const sendDelay = 200 * time.Millisecond
		mail := &configurableMail{sendDelay: sendDelay}
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Minute,
			Workers:     workers,
			MailService: mail,
			SMSService:  testSMSService,
		})
		queue.Start()

		for i := 0; i < total; i++ {
			nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
				[]byte(fmt.Sprintf("user-%d", i)), []byte("bundle"),
				testUserEmail, "123456", testOrgInfo, testRemainingTime)
			c.Assert(err, qt.IsNil)
			c.Assert(queue.Push(nc), qt.IsNil)
		}

		delivered := 0
		for delivered < total {
			select {
			case res := <-queue.NotificationsSent:
				c.Assert(res.Success, qt.IsTrue)
				delivered++
			case <-time.After(10 * time.Second):
				c.Fatalf("timed out: only %d/%d delivered", delivered, total)
			}
		}
		c.Assert(mail.callCount(), qt.Equals, total)
		// The serial queue could only ever have one send in flight. Observing
		// several overlapping sends proves the worker pool runs concurrently.
		// Use a conservative bound to stay robust under loaded CI schedulers.
		c.Assert(mail.maxConcurrent() >= 2, qt.IsTrue)
	})

	// circuit breaker: the provider fails until the breaker trips, then
	// recovers; the challenge must still be delivered after the cooldown.
	c.Run("breaker recovers", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const cooldown = 50 * time.Millisecond
		mail := &configurableMail{
			sendErr: fmt.Errorf("452 4.2.2 mailbox full, try again"), // transient
			failFor: 2,                                               // first 2 sends fail, then succeed
		}
		queue := NewQueue(ctx, QueueConfig{
			TTL:                time.Minute,
			Workers:            1,
			MailService:        mail,
			SMSService:         testSMSService,
			BreakerMaxFailures: 2,
			BreakerCooldown:    cooldown,
		})
		queue.Start()

		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		c.Assert(queue.Push(nc), qt.IsNil)

		select {
		case res := <-queue.NotificationsSent:
			c.Assert(res.Success, qt.IsTrue)
			// 2 transient failures (which tripped the breaker) + 1 success.
			c.Assert(mail.callCount() >= 3, qt.IsTrue)
			// retries are only consumed by real attempts, not breaker-open waits.
			c.Assert(res.Retries, qt.Equals, 2)
		case <-time.After(5 * time.Second):
			c.Fatal("timed out waiting for breaker recovery")
		}
	})

	// permanent failure: a 5xx error must not be retried.
	c.Run("permanent failure not retried", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mail := &configurableMail{
			sendErr: &textproto.Error{Code: 550, Msg: errNoSuchUser}, // permanent
		}
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Minute,
			Workers:     1,
			MailService: mail,
			SMSService:  testSMSService,
		})
		queue.Start()

		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		c.Assert(queue.Push(nc), qt.IsNil)

		select {
		case res := <-queue.NotificationsSent:
			c.Assert(res.Success, qt.IsFalse)
			c.Assert(res.Retries, qt.Equals, 0)
			c.Assert(mail.callCount(), qt.Equals, 1)
		case <-time.After(5 * time.Second):
			c.Fatal("timed out waiting for permanent failure")
		}
	})

	// concurrent retries drives the re-enqueue path with multiple workers so
	// the race detector exercises it (the re-enqueued pointer must not be read
	// after it is published back to the queue). Several challenges fail
	// transiently a few times before succeeding.
	c.Run("concurrent retries", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const total = 4
		mail := &configurableMail{
			sendErr: fmt.Errorf("451 4.3.0 temporary failure"), // transient
			failFor: 6,                                         // first 6 sends (across challenges) fail, then succeed
		}
		queue := NewQueue(ctx, QueueConfig{
			TTL:                time.Minute,
			Workers:            4,
			MailService:        mail,
			SMSService:         testSMSService,
			BreakerMaxFailures: 1 << 30, // disable the breaker; exercise pure retries
		})
		queue.Start()

		for i := 0; i < total; i++ {
			nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
				[]byte(fmt.Sprintf("user-%d", i)), []byte("bundle"),
				testUserEmail, "123456", testOrgInfo, testRemainingTime)
			c.Assert(err, qt.IsNil)
			c.Assert(queue.Push(nc), qt.IsNil)
		}

		delivered := 0
		for delivered < total {
			select {
			case res := <-queue.NotificationsSent:
				c.Assert(res.Success, qt.IsTrue)
				delivered++
			case <-time.After(10 * time.Second):
				c.Fatalf("timed out: only %d/%d delivered", delivered, total)
			}
		}
	})
}

// TestPushWait covers the per-item delivery-await contract exposed by PushWait:
// the returned channel is signalled exactly once with the final state on both
// success and give-up, and a caller that never reads the channel cannot block a
// worker (delivery still completes and the shared NotificationsSent channel is
// still fed).
func TestPushWait(t *testing.T) {
	c := qt.New(t)
	c.Assert(mailtemplates.Load(), qt.IsNil)

	c.Run("signalled once on success", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Second * 10,
			Workers:     1,
			MailService: &configurableMail{},
			SMSService:  testSMSService,
		})
		queue.Start()

		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		done, err := queue.PushWait(nc)
		c.Assert(err, qt.IsNil)

		select {
		case res := <-done:
			c.Assert(res.Success, qt.IsTrue)
			c.Assert(res, qt.Equals, nc)
		case <-time.After(5 * time.Second):
			c.Fatal("timed out waiting for delivery signal")
		}
		// The channel is buffered (cap 1) and signalled exactly once: no second
		// value must ever arrive.
		select {
		case extra := <-done:
			c.Fatalf("done signalled more than once: %v", extra)
		case <-time.After(200 * time.Millisecond):
		}
	})

	c.Run("signalled once on give up", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Minute,
			Workers:     1,
			MailService: &configurableMail{sendErr: &textproto.Error{Code: 550, Msg: errNoSuchUser}}, // permanent
			SMSService:  testSMSService,
		})
		queue.Start()

		nc, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		done, err := queue.PushWait(nc)
		c.Assert(err, qt.IsNil)

		select {
		case res := <-done:
			c.Assert(res.Success, qt.IsFalse)
		case <-time.After(5 * time.Second):
			c.Fatal("timed out waiting for give-up signal")
		}
		select {
		case extra := <-done:
			c.Fatalf("done signalled more than once: %v", extra)
		case <-time.After(200 * time.Millisecond):
		}
	})

	c.Run("non-reading caller does not block a worker", func(c *qt.C) {
		c.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		queue := NewQueue(ctx, QueueConfig{
			TTL:         time.Second * 10,
			Workers:     1,
			MailService: &configurableMail{},
			SMSService:  testSMSService,
		})
		queue.Start()

		// Enqueue with PushWait but deliberately never read the returned channel,
		// then enqueue a second challenge on the same single worker. If the
		// unread per-item signal blocked the worker, the second delivery would
		// never be reported.
		first, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user-1"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		_, err = queue.PushWait(first)
		c.Assert(err, qt.IsNil)

		second, err := NewNotificationChallenge(EmailChallenge, apicommon.DefaultLang,
			[]byte("user-2"), []byte("bundle"), testUserEmail, "123456", testOrgInfo, testRemainingTime)
		c.Assert(err, qt.IsNil)
		c.Assert(queue.Push(second), qt.IsNil)

		// Both challenges must reach the shared channel: the unread done channel
		// (buffered cap 1) absorbs the first signal without stalling the worker.
		delivered := 0
		for delivered < 2 {
			select {
			case res := <-queue.NotificationsSent:
				c.Assert(res.Success, qt.IsTrue)
				delivered++
			case <-time.After(5 * time.Second):
				c.Fatalf("timed out: only %d/2 delivered (worker blocked on unread done?)", delivered)
			}
		}
	})
}
