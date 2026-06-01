package notifications

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	qt "github.com/frankban/quicktest"
)

// stubService is a NotificationService that records how many times it was
// called and returns a fixed error (nil means success).
type stubService struct {
	err   error
	calls atomic.Int32
}

func (*stubService) New(any) error { return nil }

func (s *stubService) SendNotification(context.Context, *Notification) error {
	s.calls.Add(1)
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
}
