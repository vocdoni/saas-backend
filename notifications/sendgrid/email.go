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
	// create from and to email
	from := mail.NewEmail(sg.config.FromName, sg.config.FromAddress)
	to := mail.NewEmail(notification.ToName, notification.ToAddress)
	// create email with notification data
	message := mail.NewSingleEmail(from, notification.Subject, to, notification.PlainBody, notification.Body)
	// send the email
	res, err := sg.client.SendWithContext(ctx, message)
	if err != nil {
		return fmt.Errorf("could not send email: %v", err)
	}
	// check the response status code, it should be 2xx
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("could not send email: %v", res.Body)
	}
	return nil
}
