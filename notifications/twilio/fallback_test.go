package twilio

import (
	"context"
	"net/http"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/twilio/twilio-go/client"
	api "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/vocdoni/saas-backend/notifications"
)

// fakeMessages records the sender of each CreateMessage call and answers with
// the error configured for that sender.
type fakeMessages struct {
	mu    sync.Mutex
	froms []string
	errs  map[string]error
}

func (f *fakeMessages) CreateMessage(params *api.CreateMessageParams) (*api.ApiV2010Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.froms = append(f.froms, *params.From)
	return &api.ApiV2010Message{}, f.errs[*params.From]
}

func twilioErr(code int) error {
	return &client.TwilioRestError{Status: http.StatusBadRequest, Code: code}
}

func TestSendNotificationFallbackSender(t *testing.T) {
	c := qt.New(t)
	n := &notifications.Notification{ToNumber: "+34600000000", PlainBody: "123456"}
	newSMS := func(fallback string, errs map[string]error) (*SMS, *fakeMessages) {
		fake := &fakeMessages{errs: errs}
		return &SMS{config: &Config{FromNumber: "VOCDONI", FallbackFromNumber: fallback}, messages: fake}, fake
	}

	// 21612 for the alphanumeric sender: resent once from the fallback number
	sms, fake := newSMS("+15550001111", map[string]error{"VOCDONI": twilioErr(21612)})
	c.Assert(sms.SendNotification(context.Background(), n), qt.IsNil)
	c.Assert(fake.froms, qt.DeepEquals, []string{"VOCDONI", "+15550001111"})

	// the fallback failing too returns its error, classified as usual
	sms, fake = newSMS("+15550001111", map[string]error{"VOCDONI": twilioErr(21612), "+15550001111": twilioErr(21211)})
	err := sms.SendNotification(context.Background(), n)
	c.Assert(notifications.IsPermanentSendError(err), qt.IsTrue)
	c.Assert(twilioErrorCode(err), qt.Equals, 21211)
	c.Assert(fake.froms, qt.HasLen, 2)

	// no fallback configured: the 21612 is returned as a permanent error, one attempt
	sms, fake = newSMS("", map[string]error{"VOCDONI": twilioErr(21612)})
	c.Assert(notifications.IsPermanentSendError(sms.SendNotification(context.Background(), n)), qt.IsTrue)
	c.Assert(fake.froms, qt.DeepEquals, []string{"VOCDONI"})

	// any other error does not trigger the fallback
	sms, fake = newSMS("+15550001111", map[string]error{"VOCDONI": twilioErr(21211)})
	c.Assert(sms.SendNotification(context.Background(), n), qt.Not(qt.IsNil))
	c.Assert(fake.froms, qt.DeepEquals, []string{"VOCDONI"})

	// success on the first sender sends once
	sms, fake = newSMS("+15550001111", nil)
	c.Assert(sms.SendNotification(context.Background(), n), qt.IsNil)
	c.Assert(fake.froms, qt.DeepEquals, []string{"VOCDONI"})
}
