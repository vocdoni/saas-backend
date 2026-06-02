package notifications

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// stubService is a NotificationService that records how many times it was
// called and returns a fixed error (nil means success). When delay > 0 it
// blocks for that long (or until the context is cancelled) before returning,
// simulating a slow provider.
type stubService struct {
	err   error
	delay time.Duration
	calls atomic.Int32
}

func (*stubService) New(any) error { return nil }

func (s *stubService) SendNotification(ctx context.Context, _ *Notification) error {
	s.calls.Add(1)
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.delay):
		}
	}
	return s.err
}

func TestFailoverService(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	n := &Notification{ToAddress: "user@example.com"}

	c.Run("primary success does not touch backup", func(c *qt.C) {
		primary := &stubService{}
		backup := &stubService{}
		f := NewFailoverService(primary, backup)
		c.Assert(f.SendNotification(ctx, n), qt.IsNil)
		c.Assert(primary.calls.Load(), qt.Equals, int32(1))
		c.Assert(backup.calls.Load(), qt.Equals, int32(0))
	})

	c.Run("falls over to backup on primary failure", func(c *qt.C) {
		primary := &stubService{err: fmt.Errorf("primary down")}
		backup := &stubService{}
		f := NewFailoverService(primary, backup)
		c.Assert(f.SendNotification(ctx, n), qt.IsNil)
		c.Assert(primary.calls.Load(), qt.Equals, int32(1))
		c.Assert(backup.calls.Load(), qt.Equals, int32(1))
	})

	c.Run("all providers fail returns joined error", func(c *qt.C) {
		primary := &stubService{err: fmt.Errorf("primary down")}
		backup := &stubService{err: fmt.Errorf("backup down")}
		f := NewFailoverService(primary, backup)
		err := f.SendNotification(ctx, n)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(err.Error(), qt.Contains, "primary down")
		c.Assert(err.Error(), qt.Contains, "backup down")
	})

	c.Run("nil services are skipped", func(c *qt.C) {
		backup := &stubService{}
		f := NewFailoverService(nil, backup)
		c.Assert(f.SendNotification(ctx, n), qt.IsNil)
		c.Assert(backup.calls.Load(), qt.Equals, int32(1))
	})

	c.Run("no services configured returns error", func(c *qt.C) {
		f := NewFailoverService()
		c.Assert(f.SendNotification(ctx, n), qt.Not(qt.IsNil))
	})

	c.Run("single service behaves transparently", func(c *qt.C) {
		primary := &stubService{}
		f := NewFailoverService(primary)
		c.Assert(f.SendNotification(ctx, n), qt.IsNil)
		c.Assert(primary.calls.Load(), qt.Equals, int32(1))
	})

	// A primary that is slow to fail must not consume the backup's time budget:
	// even when the caller's deadline expires during the primary attempt, the
	// backup gets its own fresh timeout and still delivers.
	c.Run("slow primary does not starve backup", func(c *qt.C) {
		primary := &stubService{delay: 60 * time.Millisecond, err: fmt.Errorf("primary slow then fails")}
		backup := &stubService{delay: 10 * time.Millisecond}
		f := NewFailoverService(primary, backup)
		f.providerTimeout = time.Second // generous per-provider timeout

		// Caller deadline shorter than the primary's delay: with a shared
		// deadline the backup would inherit an expired context and fail.
		callerCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()

		c.Assert(f.SendNotification(callerCtx, n), qt.IsNil)
		c.Assert(primary.calls.Load(), qt.Equals, int32(1))
		c.Assert(backup.calls.Load(), qt.Equals, int32(1))
	})

	// A hanging provider is bounded by providerTimeout rather than blocking
	// forever, so a stuck primary fails fast and the queue can move on.
	c.Run("hanging provider bounded by providerTimeout", func(c *qt.C) {
		primary := &stubService{delay: time.Hour} // would block effectively forever
		f := NewFailoverService(primary)
		f.providerTimeout = 20 * time.Millisecond

		start := time.Now()
		err := f.SendNotification(context.Background(), n)
		c.Assert(err, qt.Not(qt.IsNil))
		c.Assert(time.Since(start) < time.Second, qt.IsTrue)
	})
}
