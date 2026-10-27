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
	restErr := func(status, code int) error {
		return fmt.Errorf("sms: %w", &client.TwilioRestError{Status: status, Code: code})
	}

	// recipient-specific codes are permanent, whatever the HTTP status
	for _, code := range []int{21211, 21217, 21408, 21610, 21612, 21614} {
		c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusBadRequest, code))), qt.IsTrue,
			qt.Commentf("code %d", code))
	}
	// 21611 (sender queue full) is a 400 but retrying later can succeed
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusBadRequest, 21611))), qt.IsFalse)
	// an auth error (bad credentials, suspended account) is provider-wide and stays transient
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusUnauthorized, 20003))), qt.IsFalse)
	// 429 is a rate limit and 5xx a provider fault: both stay transient
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusTooManyRequests, 20429))), qt.IsFalse)
	c.Assert(notifications.IsPermanentSendError(classifySendError(restErr(http.StatusInternalServerError, 20500))), qt.IsFalse)

	// the original REST error is still reachable through the wrapping
	var twilioErr *client.TwilioRestError
	c.Assert(errors.As(classifySendError(restErr(http.StatusBadRequest, 21211)), &twilioErr), qt.IsTrue)
	c.Assert(twilioErr.Code, qt.Equals, 21211)

	// nil and non-REST errors (network, timeouts) are returned unchanged
	c.Assert(classifySendError(nil), qt.IsNil)
	netErr := fmt.Errorf("dial tcp: connection refused")
	c.Assert(classifySendError(netErr), qt.Equals, netErr)
}
