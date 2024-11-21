package smtp

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/mail"
	"net/smtp"
	"net/textproto"

	"github.com/vocdoni/saas-backend/notifications"
)

var disableTrackingFilter = []byte(`{"filters":{"clicktrack":{"settings":{"enable":0,"enable_text":false}}}}`)

// SMTPConfig represents the configuration for the SMTP email service. It
// contains the sender's name, address, SMTP username, password, server and
// port. The TestAPIPort is used to define the port of the API service used
// for testing the email service locally to check messages (for example using
// MailHog).
type SMTPConfig struct {
	FromName     string
	FromAddress  string
	SMTPUsername string
	SMTPPassword string
	SMTPServer   string
	SMTPPort     int
	TestAPIPort  int
}

// SMTPEmail is the implementation of the NotificationService interface for the
// SMTP email service. It contains the configuration and the SMTP auth. It uses
// the net/smtp package to send emails.
type SMTPEmail struct {
	config *SMTPConfig
	auth   smtp.Auth
}

// New initializes the SMTP email service with the configuration. It sets the
// SMTP auth if the username and password are provided. It returns an error if
// the configuration is invalid or if the from email could not be parsed.
func (se *SMTPEmail) New(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*SMTPConfig)
	if !ok {
		return fmt.Errorf("invalid SMTP configuration")
	}
	// parse from email
	if _, err := mail.ParseAddress(config.FromAddress); err != nil {
		return fmt.Errorf("could not parse from email: %v", err)
	}
	// set configuration in struct
	se.config = config
	// init SMTP auth
	if se.config.SMTPUsername != "" && se.config.SMTPPassword != "" {
		se.auth = smtp.PlainAuth("", se.config.SMTPUsername, se.config.SMTPPassword, se.config.SMTPServer)
	}
	return nil
}

// SendNotification sends an email notification to the recipient. It composes
// the email body with the notification data and sends it using the SMTP server.
func (se *SMTPEmail) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// compose email body
	body, err := se.composeBody(notification)
	if err != nil {
		return fmt.Errorf("could not compose email body: %v", err)
	}
	// send the email
	server := fmt.Sprintf("%s:%d", se.config.SMTPServer, se.config.SMTPPort)
	// create a channel to handle errors
	errCh := make(chan error, 1)
	go func() {
		// send the message
		err := smtp.SendMail(server, se.auth, se.config.FromAddress, []string{notification.ToAddress}, body)
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

// composeBody creates the email body with the notification data. It creates a
// multipart email with a plain text and an HTML part. It returns the email
// content as a byte slice or an error if the body could not be composed.
func (se *SMTPEmail) composeBody(notification *notifications.Notification) ([]byte, error) {
	// parse 'to' email
	to, err := mail.ParseAddress(notification.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("could not parse to email: %v", err)
	}
	// create email headers
	var headers bytes.Buffer
	boundary := "----=_Part_0_123456789.123456789"
	headers.WriteString(fmt.Sprintf("From: %s\r\n", se.config.FromAddress))
	headers.WriteString(fmt.Sprintf("To: %s\r\n", to.String()))
	headers.WriteString(fmt.Sprintf("Subject: %s\r\n", notification.Subject))
	if !notification.EnableTracking {
		headers.WriteString(fmt.Sprintf("X-SMTPAPI: %s\r\n", disableTrackingFilter))
	}
	headers.WriteString("MIME-Version: 1.0\r\n")
	headers.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary))
	headers.WriteString("\r\n") // blank line between headers and body
	// create multipart writer
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.SetBoundary(boundary); err != nil {
		return nil, fmt.Errorf("could not set boundary: %v", err)
	}
	// plain text part
	textPart, _ := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=\"UTF-8\""},
		"Content-Transfer-Encoding": {"7bit"},
	})
	if _, err := textPart.Write([]byte(notification.PlainBody)); err != nil {
		return nil, fmt.Errorf("could not write plain text part: %v", err)
	}
	// HTML part
	htmlPart, _ := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=\"UTF-8\""},
		"Content-Transfer-Encoding": {"7bit"},
	})
	if _, err := htmlPart.Write([]byte(notification.Body)); err != nil {
		return nil, fmt.Errorf("could not write HTML part: %v", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("could not close writer: %v", err)
	}
	// combine headers and body and return the content
	var email bytes.Buffer
	email.Write(headers.Bytes())
	email.Write(body.Bytes())
	return email.Bytes(), nil
}
