// Package twilio provides a Twilio-based implementation of the NotificationService interface
// for sending SMS notifications.
package twilio

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	t "github.com/twilio/twilio-go"
	"github.com/twilio/twilio-go/client"
	api "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

const (
	// AccountSidEnv is the environment variable name for the Twilio account SID.
	AccountSidEnv = "TWILIO_ACCOUNT_SID"
	// AuthTokenEnv is the environment variable name for the Twilio auth token.
	AuthTokenEnv = "TWILIO_AUTH_TOKEN"
)

// Config represents the configuration for the Twilio SMS service. It
// contains the account SID, the auth token and the number from which the SMS
// will be sent.
type Config struct {
	AccountSid string
	AuthToken  string
	FromNumber string
	// StatusCallbackURL, when set, is where Twilio posts each message's delivery
	// status (see SMS.HandleStatusCallback). It must be the public URL as Twilio
	// reaches it: the callback signature is checked against it.
	StatusCallbackURL string
	// FallbackFromNumber, when set, is a numeric sender used to resend a message
	// Twilio refused with 21612 for FromNumber: an alphanumeric sender ID is not
	// accepted in every country (some require it to be registered first).
	FallbackFromNumber string
	// Label, when set, names a non-production environment (VOCDONI_ENVIRONMENT): voter
	// messages start with "[Label] " and the canary names it.
	Label string
}

// messageCreator is the part of the Twilio REST API the service uses.
type messageCreator interface {
	CreateMessage(params *api.CreateMessageParams) (*api.ApiV2010Message, error)
}

// SMS is the implementation of the NotificationService interface for the
// Twilio SMS service. It contains the configuration and the Twilio REST client.
type SMS struct {
	config   *Config
	messages messageCreator
}

var _ notifications.NotificationService = &SMS{}

// New initializes the Twilio SMS service with the configuration. It sets the
// account SID and the auth token as environment variables and initializes the
// Twilio REST client. It returns an error if the configuration is invalid or if
// the environment variables could not be set.
// Read more here: https://www.twilio.com/docs/messaging/quickstart/go
func (tsms *SMS) New(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*Config)
	if !ok {
		return fmt.Errorf("invalid Twilio configuration")
	}
	// set configuration in struct
	tsms.config = config
	// set account SID and auth token as environment variables
	if err := os.Setenv(AccountSidEnv, tsms.config.AccountSid); err != nil {
		return err
	}
	if err := os.Setenv(AuthTokenEnv, tsms.config.AuthToken); err != nil {
		return err
	}
	// init Twilio REST client
	tsms.messages = t.NewRestClient().Api
	// a fresh start counts as a delivery, or the no-delivery alert would fire on
	// every restart until the first one
	smsLastDelivered.SetToCurrentTime()
	return nil
}

// SendNotification sends an SMS notification to the recipient. It creates a
// message with the configured sender number and the notification data. It
// returns an error if the notification could not be sent or if the context is
// done.
func (tsms *SMS) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	return tsms.deliver(ctx, notification, sourceVoter)
}

// deliver sends the notification from FromNumber, resending it from the fallback
// number when Twilio refuses that sender for the recipient.
func (tsms *SMS) deliver(ctx context.Context, notification *notifications.Notification, source string) error {
	err := tsms.send(ctx, tsms.config.FromNumber, notification, source)
	if fallback, ok := tsms.fallbackFor(tsms.config.FromNumber, err); ok {
		log.Infow("sms sender refused for recipient, resending from fallback number",
			"from", tsms.config.FromNumber, "fallback", fallback)
		err = tsms.send(ctx, fallback, notification, source)
	}
	return classifySendError(err)
}

// fallbackFor returns the sender to resend from when Twilio refused from for the
// recipient (21612), and whether there is one.
func (tsms *SMS) fallbackFor(from string, err error) (string, bool) {
	fallback := tsms.config.FallbackFromNumber
	ok := fallback != "" && from == tsms.config.FromNumber && fallback != from &&
		twilioErrorCode(err) == codeSenderRecipientMismatch
	return fallback, ok
}

// statusCallbackURL is the callback URL for one source's messages. The source
// travels in the query string, which Twilio includes in the signed URL.
func (tsms *SMS) statusCallbackURL(source string) string {
	if tsms.config.StatusCallbackURL == "" {
		return ""
	}
	return tsms.config.StatusCallbackURL + "?" + url.Values{"source": {source}}.Encode()
}

// body is the message text: voter messages carry the environment label, the
// canary names it in its own text.
func (tsms *SMS) body(notification *notifications.Notification, source string) string {
	if tsms.config.Label == "" || source == sourceCanary {
		return notification.PlainBody
	}
	return "[" + tsms.config.Label + "] " + notification.PlainBody
}

// send creates one message from the given sender and waits for Twilio's answer
// or for ctx to be done. The outcome is counted when Twilio answers, even if ctx
// is done by then.
func (tsms *SMS) send(ctx context.Context, from string, notification *notifications.Notification, source string) error {
	params := &api.CreateMessageParams{}
	params.SetTo(notification.ToNumber)
	params.SetFrom(from)
	params.SetBody(tsms.body(notification, source))
	if callback := tsms.statusCallbackURL(source); callback != "" {
		params.SetStatusCallback(callback)
	}
	errCh := make(chan error, 1)
	go func() {
		_, err := tsms.messages.CreateMessage(params)
		refusal := sendRejected
		if _, ok := tsms.fallbackFor(from, err); ok {
			refusal = sendRerouted
		}
		recordSend(source, err, refusal)
		errCh <- err
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// codeSenderRecipientMismatch is Twilio's 21612: the message cannot be sent with
// this combination of 'To' and 'From', e.g. an alphanumeric sender ID the
// recipient's country does not accept.
const codeSenderRecipientMismatch = 21612

// twilioErrorCode returns the Twilio error code in err, or 0 when err is not a
// Twilio REST error.
func twilioErrorCode(err error) int {
	var restErr *client.TwilioRestError
	if errors.As(err, &restErr) {
		return restErr.Code
	}
	return 0
}

// permanentTwilioCodes are the Twilio error codes that name a recipient which can
// never be reached, so retrying the same message cannot succeed.
var permanentTwilioCodes = map[int]struct{}{
	21211: {}, // invalid 'To' phone number
	21217: {}, // phone number does not appear to be valid
	21408: {}, // no permission to send to that region
	21610: {}, // recipient has unsubscribed (replied STOP)
	21612: {}, // 'To' phone number is not reachable via SMS
	21614: {}, // 'To' number is not a mobile number
}

// classifySendError marks a Twilio REST error whose code names an unreachable
// recipient as permanent. Any other error, including auth failures and rate
// limits, stays transient so retries can trip the circuit breaker.
func classifySendError(err error) error {
	var restErr *client.TwilioRestError
	if !errors.As(err, &restErr) {
		return err
	}
	if _, ok := permanentTwilioCodes[restErr.Code]; !ok {
		return err
	}
	return &notifications.PermanentError{Err: err}
}
