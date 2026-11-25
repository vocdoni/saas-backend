package errors

import (
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestErrorWriteRetryAfter(t *testing.T) {
	c := qt.New(t)
	retryAfter := func(e Error) string {
		w := httptest.NewRecorder()
		e.Write(w)
		return w.Header().Get("Retry-After")
	}

	// a cooldown in milliseconds is rounded up to whole seconds
	c.Assert(retryAfter(ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": int64(1500)})), qt.Equals, "2")
	c.Assert(retryAfter(ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": int64(60000)})), qt.Equals, "60")
	// a counter refusal carries seconds already
	c.Assert(retryAfter(ErrVerificationMaxAttempts.WithData(map[string]any{"retryAfter": int64(3600)})), qt.Equals, "3600")
	// coolDownTime is a wait only on ErrAttemptCoolDownTime; on ErrUserAlreadyVerified it is the code's lifetime
	c.Assert(retryAfter(ErrUserAlreadyVerified.WithData(map[string]any{"coolDownTime": 120.0})), qt.Equals, "")
	c.Assert(retryAfter(ErrUnauthorized), qt.Equals, "")
	// a 503 keeps its default
	c.Assert(retryAfter(ErrServerBusy), qt.Equals, "10")
}
