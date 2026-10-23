// Package twilio provides a Twilio-based implementation of the NotificationService interface
// for sending SMS notifications.
package twilio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	t "github.com/twilio/twilio-go"
	"github.com/twilio/twilio-go/client"
	api "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/vocdoni/saas-backend/notifications"
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
}

// SMS is the implementation of the NotificationService interface for the
// Twilio SMS service. It contains the configuration and the Twilio REST client.
type SMS struct {
	config *Config
	client *t.RestClient
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
	tsms.client = t.NewRestClient()
	return nil
}

// SendNotification sends an SMS notification to the recipient. It creates a
// message with the configured sender number and the notification data. It
// returns an error if the notification could not be sent or if the context is
// done.
func (tsms *SMS) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// create message with configured sender number and notification data
	params := &api.CreateMessageParams{}
	params.SetTo(notification.ToNumber)
	params.SetFrom(tsms.config.FromNumber)
	params.SetBody(notification.PlainBody)
	// create a channel to handle errors
	errCh := make(chan error, 1)
	go func() {
		// send the message
		_, err := tsms.client.Api.CreateMessage(params)
		errCh <- err
		close(errCh)
	}()
	// wait for the message to be sent or the context to be done
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return classifySendError(err)
	}
}

// classifySendError marks a Twilio 4xx REST error (invalid or unsubscribed
// number, rejected sender...) as permanent, since retrying the same message
// cannot succeed. 429 is a rate limit, so it stays transient. Other errors are
// returned unchanged.
func classifySendError(err error) error {
	var restErr *client.TwilioRestError
	if !errors.As(err, &restErr) {
		return err
	}
	if restErr.Status < http.StatusBadRequest || restErr.Status >= http.StatusInternalServerError ||
		restErr.Status == http.StatusTooManyRequests {
		return err
	}
	return &notifications.PermanentError{Err: err}
}
