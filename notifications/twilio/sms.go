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

type TwilioConfig struct {
	AccountSid string
	AuthToken  string
	FromNumber string
}

type TwilioSMS struct {
	config *TwilioConfig
	client *t.RestClient
}

func (tsms *TwilioSMS) Init(rawConfig any) error {
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

func (tsms *TwilioSMS) SendNotification(_ context.Context, notification *notifications.Notification) error {
	// create message with configured sender number and notification data
	params := &api.CreateMessageParams{}
	params.SetTo(notification.To)
	params.SetFrom(tsms.config.FromNumber)
	params.SetBody(notification.Body)
	// send the message
	_, err := tsms.client.Api.CreateMessage(params)
	return err
}
