package twilio

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/twilio/twilio-go/client"
	"github.com/vocdoni/saas-backend/notifications"
)

func TestClassifySendError(t *testing.T) {
	c := qt.New(t)
	restErr := func(status int) error {
		return fmt.Errorf("sms: %w", &client.TwilioRestError{Status: status, Code: 21211})
	}

	// 4xx client faults (invalid or unsubscribed number) are permanent
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusBadRequest))), qt.IsTrue)
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusNotFound))), qt.IsTrue)
	// 429 is a rate limit and 5xx a provider fault: both stay transient
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusTooManyRequests))), qt.IsFalse)
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusServiceUnavailable))), qt.IsFalse)
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusInternalServerError))), qt.IsFalse)

	// the original REST error is still reachable through the wrapping
	var twilioErr *client.TwilioRestError
	c.Assert(errors.As(classifySendError(restErr(http.StatusBadRequest)), &twilioErr), qt.IsTrue)
	c.Assert(twilioErr.Code, qt.Equals, 21211)

	// nil and non-REST errors (network, timeouts) are returned unchanged
	c.Assert(classifySendError(nil), qt.IsNil)
	netErr := fmt.Errorf("dial tcp: connection refused")
	c.Assert(classifySendError(netErr), qt.Equals, netErr)
}
