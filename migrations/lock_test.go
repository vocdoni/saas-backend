package migrations

import (
	"context"
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestRenewLock pins the renewer plumbing: a no-op without a renewer, the renewer's error passed
// through, and the renewer surviving context.WithoutCancel, which a migration uses to drop the run
// timeout.
func TestRenewLock(t *testing.T) {
	c := qt.New(t)
	c.Assert(RenewLock(context.Background()), qt.IsNil)

	calls := 0
	ctx := WithLockRenewer(context.Background(), func(context.Context) error {
		calls++
		return nil
	})
	c.Assert(RenewLock(ctx), qt.IsNil)
	c.Assert(calls, qt.Equals, 1)

	lost := fmt.Errorf("lock lost")
	ctx = WithLockRenewer(context.Background(), func(context.Context) error { return lost })
	c.Assert(RenewLock(ctx), qt.ErrorIs, lost)
	c.Assert(RenewLock(context.WithoutCancel(ctx)), qt.ErrorIs, lost)
}
