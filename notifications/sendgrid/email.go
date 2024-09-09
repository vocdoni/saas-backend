package sendgrid

import (
	"context"
	"fmt"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"github.com/vocdoni/saas-backend/notifications"
)

type SendGridConfig struct {
	FromName    string
	FromAddress string
	APIKey      string
}

type SendGridEmail struct {
	config *SendGridConfig
	client *sendgrid.Client
}

func (sg *SendGridEmail) Init(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*SendGridConfig)
	if !ok {
		return fmt.Errorf("invalid SendGrid configuration")
	}
	// set configuration in struct
	sg.config = config
	// init SendGrid client
	sg.client = sendgrid.NewSendClient(sg.config.APIKey)
	return nil
}

func (sg *SendGridEmail) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// create from email
	from := mail.NewEmail(sg.config.FromName, sg.config.FromAddress)
	// create email with notification data
	message := mail.NewSingleEmail(from, notification.Subject, mail.NewEmail("", notification.To), notification.Body, notification.Body)
	// send the email
	_, err := sg.client.SendWithContext(ctx, message)
	return err
}
