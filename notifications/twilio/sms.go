package twilio

import (
	"context"
	"fmt"
	"os"

	t "github.com/twilio/twilio-go"
	api "github.com/twilio/twilio-go/rest/api/v2010"
	"github.com/vocdoni/saas-backend/notifications"
)

const (
	AccountSidEnv = "TWILIO_ACCOUNT_SID"
	AuthTokenEnv  = "TWILIO_AUTH_TOKEN"
)

// TwilioConfig represents the configuration for the Twilio SMS service. It
// contains the account SID, the auth token and the number from which the SMS
// will be sent.
type TwilioConfig struct {
	AccountSid string
	AuthToken  string
	FromNumber string
}

// TwilioSMS is the implementation of the NotificationService interface for the
// Twilio SMS service. It contains the configuration and the Twilio REST client.
type TwilioSMS struct {
	config *TwilioConfig
	client *t.RestClient
}

// New initializes the Twilio SMS service with the configuration. It sets the
// account SID and the auth token as environment variables and initializes the
// Twilio REST client. It returns an error if the configuration is invalid or if
// the environment variables could not be set.
// Read more here: https://www.twilio.com/docs/messaging/quickstart/go
func (tsms *TwilioSMS) New(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*TwilioConfig)
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
func (tsms *TwilioSMS) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// create message with configured sender number and notification data
	params := &api.CreateMessageParams{}
	params.SetTo(notification.ToNumber)
	params.SetFrom(tsms.config.FromNumber)
	params.SetBody(notification.Body)
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
		return err
	}
}
