package twilio

import (
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Twilio signs callbacks with HMAC-SHA1
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/twilio/twilio-go/client"
)

const (
	testAuthToken   = "test-auth-token"
	testCallbackURL = "https://saas.example/notifications/sms/status"
)

// twilioSignature computes X-Twilio-Signature the way Twilio does: HMAC-SHA1 over
// the URL followed by every form key and value, sorted by key.
func twilioSignature(token, rawURL string, form url.Values) string {
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var b strings.Builder
	b.WriteString(rawURL)
	for _, k := range keys {
		b.WriteString(k + form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte(token))
	_, _ = mac.Write([]byte(b.String()))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func postStatus(tsms *SMS, form url.Values, signature string) int {
	req := httptest.NewRequest(http.MethodPost, "http://internal:8080/notifications/sms/status",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(signatureHeader, signature)
	rec := httptest.NewRecorder()
	tsms.HandleStatusCallback(rec, req)
	return rec.Code
}

func TestHandleStatusCallback(t *testing.T) {
	c := qt.New(t)
	tsms := &SMS{config: &Config{AuthToken: testAuthToken, StatusCallbackURL: testCallbackURL}}

	undelivered := url.Values{"MessageSid": {"SM1"}, "MessageStatus": {"undelivered"}, "ErrorCode": {"30007"}}
	before := testutil.ToFloat64(smsStatus.WithLabelValues("undelivered", "30007"))
	// signed over the configured URL, not the one the request arrived on
	c.Assert(postStatus(tsms, undelivered, twilioSignature(testAuthToken, testCallbackURL, undelivered)),
		qt.Equals, http.StatusNoContent)
	c.Assert(testutil.ToFloat64(smsStatus.WithLabelValues("undelivered", "30007")), qt.Equals, before+1)

	// a wrong token, a tampered form or no signature is refused and not counted
	c.Assert(postStatus(tsms, undelivered, twilioSignature("other-token", testCallbackURL, undelivered)),
		qt.Equals, http.StatusForbidden)
	tampered := url.Values{"MessageSid": {"SM1"}, "MessageStatus": {"delivered"}, "ErrorCode": {"30007"}}
	c.Assert(postStatus(tsms, tampered, twilioSignature(testAuthToken, testCallbackURL, undelivered)),
		qt.Equals, http.StatusForbidden)
	c.Assert(postStatus(tsms, undelivered, ""), qt.Equals, http.StatusForbidden)
	c.Assert(testutil.ToFloat64(smsStatus.WithLabelValues("undelivered", "30007")), qt.Equals, before+1)

	// delivered moves the last-delivered gauge forward
	delivered := url.Values{"MessageSid": {"SM2"}, "MessageStatus": {"delivered"}}
	startSec := time.Now().Unix()
	c.Assert(postStatus(tsms, delivered, twilioSignature(testAuthToken, testCallbackURL, delivered)),
		qt.Equals, http.StatusNoContent)
	c.Assert(testutil.ToFloat64(smsLastDelivered) >= float64(startSec), qt.IsTrue)
}

func TestRecordStatusBoundsLabels(t *testing.T) {
	c := qt.New(t)
	before := testutil.ToFloat64(smsStatus.WithLabelValues("other", ""))
	recordStatus("made-up", "not-a-code", time.Now())
	c.Assert(testutil.ToFloat64(smsStatus.WithLabelValues("other", "")), qt.Equals, before+1)
}

func TestRecordSend(t *testing.T) {
	c := qt.New(t)
	count := func(result, code string) float64 { return testutil.ToFloat64(smsSends.WithLabelValues(result, code)) }

	accepted, rejected, failed := count(sendAccepted, ""), count(sendRejected, "21612"), count(sendError, "")
	recordSend(nil)
	recordSend(&client.TwilioRestError{Status: http.StatusBadRequest, Code: 21612})
	recordSend(fmt.Errorf("dial tcp: connection refused"))
	c.Assert(count(sendAccepted, ""), qt.Equals, accepted+1)
	c.Assert(count(sendRejected, "21612"), qt.Equals, rejected+1)
	c.Assert(count(sendError, ""), qt.Equals, failed+1)
}
