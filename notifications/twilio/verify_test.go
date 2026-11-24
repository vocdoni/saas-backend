package twilio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
	otp := &notifications.Notification{
		ToNumber: "+43600000000", PlainBody: "code 123456", OTPCode: "123456", Lang: "ca", OTPTTL: 5*time.Minute + 30*time.Second,
	}
	newSMS := func(verifySID string, verr error, msgErrs map[string]error) (*SMS, *fakeVerifications, *fakeMessages) {
		v := &fakeVerifications{err: verr}
		m := &fakeMessages{errs: msgErrs}
		conf := &Config{FromNumber: "VOCDONI", FallbackFromNumber: "+15550001111", VerifyServiceSID: verifySID}
		return &SMS{config: conf, messages: m, verifications: v}, v, m
	}

	// a one-time code goes through Verify with the custom code and locale, no direct message
	sms, v, m := newSMS("VA1", nil, nil)
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 1)
	c.Assert(v.sids, qt.DeepEquals, []string{"VA1"})
	c.Assert(*v.calls[0].To, qt.Equals, "+43600000000")
	c.Assert(*v.calls[0].Channel, qt.Equals, "sms")
	c.Assert(*v.calls[0].CustomCode, qt.Equals, "123456")
	c.Assert(*v.calls[0].Locale, qt.Equals, "ca")
	// the code's real lifetime replaces Verify's own "expires in 10 minutes"
	c.Assert(*v.calls[0].TemplateCustomSubstitutions, qt.Equals, `{"ttl":"5"}`)
	c.Assert(m.froms, qt.HasLen, 0)

	// Verify failing falls back to the direct message path, sender fallback included
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

	// a Verify call outliving the context is not followed by a direct resend
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

func TestVerifyTTL(t *testing.T) {
	c := qt.New(t)
	for ttl, want := range map[time.Duration]string{
		0: "", 10 * time.Minute: `{"ttl":"10"}`, 4*time.Minute + 59*time.Second: `{"ttl":"4"}`, 30 * time.Second: `{"ttl":"1"}`,
	} {
		c.Assert(verifyTTL(ttl), qt.Equals, want, qt.Commentf("ttl %s", ttl))
	}
}

func TestVerifyCountsSends(t *testing.T) {
	c := qt.New(t)
	count := func(result, code string) float64 {
		return testutil.ToFloat64(smsSends.WithLabelValues(sourceVoter, result, code))
	}
	otp := &notifications.Notification{ToNumber: "+43600000000", PlainBody: "code 123456", OTPCode: "123456"}
	accepted, rerouted, rejected := count(sendAccepted, ""), count(sendRerouted, "60200"), count(sendRejected, "60200")
	newSMS := func(v *fakeVerifications) *SMS {
		conf := &Config{FromNumber: "VOCDONI", VerifyServiceSID: "VA1"}
		return &SMS{config: conf, messages: &fakeMessages{}, verifications: v}
	}

	// delivered by Verify: one accepted send
	c.Assert(newSMS(&fakeVerifications{}).SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(count(sendAccepted, ""), qt.Equals, accepted+1)

	// refused by Verify and resent as a direct message: rerouted, then accepted
	c.Assert(newSMS(&fakeVerifications{err: twilioErr(60200)}).SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(count(sendRerouted, "60200"), qt.Equals, rerouted+1)
	c.Assert(count(sendAccepted, ""), qt.Equals, accepted+2)

	// refused after the caller gave up: nothing resends it, so it is rejected
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	slow := &fakeVerifications{err: twilioErr(60200), delay: 50 * time.Millisecond}
	c.Assert(newSMS(slow).SendNotification(ctx, otp), qt.ErrorIs, context.DeadlineExceeded)
	deadline := time.Now().Add(2 * time.Second)
	for count(sendRejected, "60200") == rejected && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Assert(count(sendRejected, "60200"), qt.Equals, rejected+1)
	c.Assert(count(sendRerouted, "60200"), qt.Equals, rerouted+1)
}

func TestSendNotificationDirectPrefixes(t *testing.T) {
	c := qt.New(t)
	otp := &notifications.Notification{ToNumber: "+34600000000", PlainBody: "code 123456", OTPCode: "123456"}
	newSMS := func(verr error, msgErrs map[string]error) (*SMS, *fakeVerifications, *fakeMessages) {
		v := &fakeVerifications{err: verr}
		m := &fakeMessages{errs: msgErrs}
		conf := &Config{
			FromNumber: "VOCDONI", FallbackFromNumber: "+15550001111", VerifyServiceSID: "VA1",
			DirectPrefixes: []string{"+34", "+55"},
		}
		return &SMS{config: conf, messages: m, verifications: v}, v, m
	}
	refused := twilioErr(codeSenderRecipientMismatch)

	// a direct-prefix code goes out as a direct message, Verify untouched
	sms, v, m := newSMS(nil, nil)
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI"})
	c.Assert(v.calls, qt.HasLen, 0)

	// the sender fallback still comes first
	sms, v, m = newSMS(nil, map[string]error{"VOCDONI": refused})
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI", "+15550001111"})
	c.Assert(v.calls, qt.HasLen, 0)

	// every sender refused: Verify delivers it
	sms, v, m = newSMS(nil, map[string]error{"VOCDONI": refused, "+15550001111": refused})
	c.Assert(sms.SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(m.froms, qt.DeepEquals, []string{"VOCDONI", "+15550001111"})
	c.Assert(v.calls, qt.HasLen, 1)

	// Verify failing too ends there: the direct path was already tried
	sms, v, m = newSMS(twilioErr(60200), map[string]error{"VOCDONI": refused, "+15550001111": refused})
	c.Assert(sms.SendNotification(context.Background(), otp), qt.Not(qt.IsNil))
	c.Assert(m.froms, qt.HasLen, 2)
	c.Assert(v.calls, qt.HasLen, 1)

	// any other refusal is not handed to Verify
	sms, v, _ = newSMS(nil, map[string]error{"VOCDONI": twilioErr(21211)})
	var permanent *notifications.PermanentError
	c.Assert(errors.As(sms.SendNotification(context.Background(), otp), &permanent), qt.IsTrue)
	c.Assert(v.calls, qt.HasLen, 0)

	// other countries still go through Verify first
	sms, v, m = newSMS(nil, nil)
	other := &notifications.Notification{ToNumber: "+43600000000", PlainBody: "code 123456", OTPCode: "123456"}
	c.Assert(sms.SendNotification(context.Background(), other), qt.IsNil)
	c.Assert(v.calls, qt.HasLen, 1)
	c.Assert(m.froms, qt.HasLen, 0)
}

func TestDirectPrefixesCountSends(t *testing.T) {
	c := qt.New(t)
	count := func(result, code string) float64 {
		return testutil.ToFloat64(smsSends.WithLabelValues(sourceVoter, result, code))
	}
	rerouted, rejected, accepted := count(sendRerouted, "21612"), count(sendRejected, "60200"), count(sendAccepted, "")
	refused := twilioErr(codeSenderRecipientMismatch)
	newSMS := func(verr error) *SMS {
		conf := &Config{
			FromNumber: "VOCDONI", FallbackFromNumber: "+15550001111", VerifyServiceSID: "VA1",
			DirectPrefixes: []string{"+34"},
		}
		m := &fakeMessages{errs: map[string]error{"VOCDONI": refused, "+15550001111": refused}}
		return &SMS{config: conf, messages: m, verifications: &fakeVerifications{err: verr}}
	}
	otp := &notifications.Notification{ToNumber: "+34600000000", PlainBody: "code 123456", OTPCode: "123456"}

	// both senders refused and Verify takes over: two rerouted sends, one accepted
	c.Assert(newSMS(nil).SendNotification(context.Background(), otp), qt.IsNil)
	c.Assert(count(sendRerouted, "21612"), qt.Equals, rerouted+2)
	c.Assert(count(sendAccepted, ""), qt.Equals, accepted+1)

	// Verify refusing as the last resort is rejected, not rerouted
	c.Assert(newSMS(twilioErr(60200)).SendNotification(context.Background(), otp), qt.Not(qt.IsNil))
	c.Assert(count(sendRejected, "60200"), qt.Equals, rejected+1)
}

func TestValidPrefixes(t *testing.T) {
	c := qt.New(t)
	got := validPrefixes([]string{"+34", "55", "", "+", "+3a", "+1234567", "+39"})
	c.Assert(got, qt.DeepEquals, []string{"+34", "+55", "+39"})
}
