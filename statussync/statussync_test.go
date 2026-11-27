package statussync

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

// TestProcessDuePaused: while paused, due tasks are neither processed nor dropped.
func TestProcessDuePaused(t *testing.T) {
	c := qt.New(t)
	s := New(t.Context(), &Config{Paused: func(time.Time) bool { return true }})
	s.EnqueueReconcile(internal.HexBytes{0x01}, "READY")
	c.Assert(s.processDue(false), qt.Equals, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	c.Assert(s.pending, qt.HasLen, 1)
}
