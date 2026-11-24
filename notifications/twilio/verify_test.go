package twilio

import (
	"context"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	verify "github.com/twilio/twilio-go/rest/verify/v2"
	"github.com/vocdoni/saas-backend/notifications"
)

// fakeVerifications records each CreateVerification call and answers with err,
// after delay.
type fakeVerifications struct {
	mu    sync.Mutex
	calls []*verify.CreateVerificationParams
	sids  []string
	err   error
	delay time.Duration
}

func (f *fakeVerifications) CreateVerification(
	sid string, params *verify.CreateVerificationParams,
) (*verify.VerifyV2Verification, error) {
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, params)
	f.sids = append(f.sids, sid)
	return &verify.VerifyV2Verification{}, f.err
}

func TestSendNotificationVerify(t *testing.T) {
	c := qt.New(t)
	otp := &notifications.Notification{ToNumber: "+43600000000", PlainBody: "code 123456", OTPCode: "123456", Lang: "ca"}
	newSMS := func(verifySID string, verr error, msgErrs map[string]error) (*SMS, *fakeVerifications, *fakeMessages) {
		v := &fakeVerifications{err: verr}
		m := &fakeMessages{errs: msgErrs}
		conf := &Config{FromNumber: "VOCDONI", FallbackFromNumber: "+15550001111", VerifyServiceSID: verifySID}
		return &SMS{config: conf, messages: m, verifications: v}, v, m
	}

	// a one-time code goes through Verify with the custom code and locale, no plain message
	sms, v, m := newSMS("VA1", nil, nil)
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 1)
	c.Assert(v.sids, qt.DeepEquals, []string{"VA1"})
	c.Assert(*v.calls[0].To, qt.Equals, "+43600000000")
	c.Assert(*v.calls[0].Channel, qt.Equals, "sms")
	c.Assert(*v.calls[0].CustomCode, qt.Equals, "123456")
	c.Assert(*v.calls[0].Locale, qt.Equals, "ca")
	c.Assert(m.froms, qt.HasLen, 0)

	// Verify failing falls back to the plain message path, sender fallback included
	sms, v, m = newSMS("VA1", twilioErr(60200), map[string]error{"VOCDONI": twilioErr(21612)})
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 1)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI", "+15550001111"})

	// without a Verify service, or without a code, Verify is not used
	sms, v, m = newSMS("", nil, nil)
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 0)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI"})
	sms, v, m = newSMS("VA1", nil, nil)
	plain := &notifications.Notification{ToNumber: "+43600000000", PlainBody: "hello"}
	c.Assert(sms.SendNotification(context.Background(), plain), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 0)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI"})

	// a Verify call outliving the context is not followed by a plain resend
	sms, v, m = newSMS("VA1", nil, nil)
	v.delay = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	c.Assert(sms.SendNotification(ctx, otp), qt.ErrorIs, context.DeadlineExceeded)
	c.Assert(m.froms, qt.HasLen, 0)
}

func TestVerifyLocale(t *testing.T) {
	c := qt.New(t)
	for lang, want := range map[string]string{
		"es": "es", "ca": "ca", "pt-BR": "pt", "EN_us": "en", "eu": "", "gl": "", "": "",
	} {
		c.Assert(verifyLocale(lang), qt.Equals, want, qt.Commentf("lang %q", lang))
	}
}
