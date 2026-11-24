package twilio

import (
	"context"
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
	api "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
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

func postStatus(tsms *SMS, query string, form url.Values, signature string) int {
	target := "http://internal:8080/notifications/sms/status"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set(signatureHeader, signature)
	rec := httptest.NewRecorder()
	tsms.HandleStatusCallback(rec, req)
	return rec.Code
}

func TestHandleStatusCallback(t *testing.T) {
	c := qt.New(t)
	tsms := &SMS{config: &Config{AuthToken: testAuthToken, StatusCallbackURL: testCallbackURL}}
	status := func(source, st, code string) float64 {
		return testutil.ToFloat64(smsStatus.WithLabelValues(source, st, code))
	}

	// signed over the configured URL plus the query, not the URL the request arrived on
	undelivered := url.Values{"MessageSid": {"SM1"}, "MessageStatus": {"undelivered"}, "ErrorCode": {"30007"}}
	signedVoter := tsms.statusCallbackURL(sourceVoter)
	before := status(sourceVoter, "undelivered", "30007")
	c.Assert(postStatus(tsms, "source=voter", undelivered, twilioSignature(testAuthToken, signedVoter, undelivered)),
		qt.Equals, http.StatusNoContent)
	c.Assert(status(sourceVoter, "undelivered", "30007"), qt.Equals, before+1)

	// the canary source is read from the signed query
	signedCanary := tsms.statusCallbackURL(sourceCanary)
	canaryBefore := status(sourceCanary, "undelivered", "30007")
	c.Assert(postStatus(tsms, "source=canary", undelivered, twilioSignature(testAuthToken, signedCanary, undelivered)),
		qt.Equals, http.StatusNoContent)
	c.Assert(status(sourceCanary, "undelivered", "30007"), qt.Equals, canaryBefore+1)

	// a wrong token, a tampered form or query, or no signature is refused and not counted
	c.Assert(postStatus(tsms, "source=voter", undelivered, twilioSignature("other-token", signedVoter, undelivered)),
		qt.Equals, http.StatusForbidden)
	tampered := url.Values{"MessageSid": {"SM1"}, "MessageStatus": {"delivered"}, "ErrorCode": {"30007"}}
	c.Assert(postStatus(tsms, "source=voter", tampered, twilioSignature(testAuthToken, signedVoter, undelivered)),
		qt.Equals, http.StatusForbidden)
	c.Assert(postStatus(tsms, "source=canary", undelivered, twilioSignature(testAuthToken, signedVoter, undelivered)),
		qt.Equals, http.StatusForbidden)
	c.Assert(postStatus(tsms, "source=voter", undelivered, ""), qt.Equals, http.StatusForbidden)
	c.Assert(status(sourceVoter, "undelivered", "30007"), qt.Equals, before+1)

	// delivered moves the last-delivered gauge forward
	delivered := url.Values{"MessageSid": {"SM2"}, "MessageStatus": {"delivered"}}
	startSec := time.Now().Unix()
	c.Assert(postStatus(tsms, "source=voter", delivered, twilioSignature(testAuthToken, signedVoter, delivered)),
		qt.Equals, http.StatusNoContent)
	c.Assert(testutil.ToFloat64(smsLastDelivered) >= float64(startSec), qt.IsTrue)
}

func TestRecordStatusBoundsLabels(t *testing.T) {
	c := qt.New(t)
	before := testutil.ToFloat64(smsStatus.WithLabelValues(sourceVoter, "other", ""))
	recordStatus("made-up-source", "made-up", "not-a-code", time.Now())
	c.Assert(testutil.ToFloat64(smsStatus.WithLabelValues(sourceVoter, "other", "")), qt.Equals, before+1)
}

func TestRecordSend(t *testing.T) {
	c := qt.New(t)
	count := func(source, result, code string) float64 {
		return testutil.ToFloat64(smsSends.WithLabelValues(source, result, code))
	}

	accepted, rejected := count(sourceVoter, sendAccepted, ""), count(sourceVoter, sendRejected, "21612")
	rerouted := count(sourceVoter, sendRerouted, "21612")
	failed, canary := count(sourceVoter, sendError, ""), count(sourceCanary, sendAccepted, "")
	refused := &client.TwilioRestError{Status: http.StatusBadRequest, Code: 21612}
	recordSend(sourceVoter, nil, sendRejected)
	recordSend(sourceVoter, refused, sendRejected)
	recordSend(sourceVoter, refused, sendRerouted)
	recordSend(sourceVoter, fmt.Errorf("dial tcp: connection refused"), sendRejected)
	recordSend(sourceCanary, nil, sendRejected)
	c.Assert(count(sourceVoter, sendAccepted, ""), qt.Equals, accepted+1)
	c.Assert(count(sourceVoter, sendRejected, "21612"), qt.Equals, rejected+1)
	c.Assert(count(sourceVoter, sendRerouted, "21612"), qt.Equals, rerouted+1)
	c.Assert(count(sourceVoter, sendError, ""), qt.Equals, failed+1)
	c.Assert(count(sourceCanary, sendAccepted, ""), qt.Equals, canary+1)
}

func TestFallbackCountsRerouted(t *testing.T) {
	c := qt.New(t)
	count := func(result string) float64 {
		return testutil.ToFloat64(smsSends.WithLabelValues(sourceVoter, result, "21612"))
	}
	rerouted, rejected := count(sendRerouted), count(sendRejected)
	n := &notifications.Notification{ToNumber: "+43600000000", PlainBody: "123456"}

	// refused, then rescued by the fallback: rerouted, not rejected
	tsms := &SMS{
		config:   &Config{FromNumber: "VOCDONI", FallbackFromNumber: "+15550001111"},
		messages: &fakeMessages{errs: map[string]error{"VOCDONI": twilioErr(21612)}},
	}
	c.Assert(tsms.SendNotification(context.Background(), n), qt.IsNil)
	c.Assert(count(sendRerouted), qt.Equals, rerouted+1)
	c.Assert(count(sendRejected), qt.Equals, rejected)

	// refused by both senders: the first attempt is rerouted, the fallback's is rejected
	tsms.messages = &fakeMessages{errs: map[string]error{"VOCDONI": twilioErr(21612), "+15550001111": twilioErr(21612)}}
	c.Assert(tsms.SendNotification(context.Background(), n), qt.Not(qt.IsNil))
	c.Assert(count(sendRerouted), qt.Equals, rerouted+2)
	c.Assert(count(sendRejected), qt.Equals, rejected+1)
}

// slowMessages answers CreateMessage after a delay.
type slowMessages struct{ delay time.Duration }

func (s *slowMessages) CreateMessage(*api.CreateMessageParams) (*api.ApiV2010Message, error) {
	time.Sleep(s.delay)
	return &api.ApiV2010Message{}, nil
}

func TestSendCountsAfterTimeout(t *testing.T) {
	c := qt.New(t)
	tsms := &SMS{config: &Config{FromNumber: "VOCDONI"}, messages: &slowMessages{delay: 50 * time.Millisecond}}
	accepted := func() float64 { return testutil.ToFloat64(smsSends.WithLabelValues(sourceVoter, sendAccepted, "")) }
	before := accepted()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	err := tsms.SendNotification(ctx, &notifications.Notification{ToNumber: "+34600000000", PlainBody: "x"})
	c.Assert(err, qt.ErrorIs, context.DeadlineExceeded)
	// Twilio's late answer is still counted, once CreateMessage returns
	deadline := time.Now().Add(2 * time.Second)
	for accepted() == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Assert(accepted(), qt.Equals, before+1)
}

func TestStatusCallbackURLCarriesSource(t *testing.T) {
	c := qt.New(t)
	tsms := &SMS{config: &Config{StatusCallbackURL: testCallbackURL}}
	c.Assert(tsms.statusCallbackURL(sourceCanary), qt.Equals, testCallbackURL+"?source=canary")
	c.Assert((&SMS{config: &Config{}}).statusCallbackURL(sourceVoter), qt.Equals, "")
}

func TestCanaryNotification(t *testing.T) {
	c := qt.New(t)
	c.Assert(mailtemplates.Load(), qt.IsNil)
	n, err := canaryNotification("+34600000000")
	c.Assert(err, qt.IsNil)
	c.Assert(n.ToNumber, qt.Equals, "+34600000000")
	// the Spanish voter code text, with a 6-digit code
	c.Assert(n.PlainBody, qt.Matches, `(?s).*código.*\d{6}.*`)
}
