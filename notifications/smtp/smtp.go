// Package smtp provides an SMTP-based implementation of the NotificationService interface
// for sending email notifications.
package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime/multipart"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
)

var disableTrackingFilter = []byte(`{"filters":{"clicktrack":{"settings":{"enable":0,"enable_text":false}}}}`)

// Config represents the configuration for the SMTP email service. It
// contains the sender's name, address, SMTP username, password, server, port,
// and dial+send timeout.
type Config struct {
	FromName     string
	FromAddress  string
	SMTPUsername string
	SMTPPassword string
	SMTPServer   string
	SMTPPort     int
	SMTPTimeout  time.Duration
}

// Email is the implementation of the NotificationService interface for the
// SMTP email service. It contains the configuration and the SMTP auth. It uses
// the net/smtp package to send emails.
type Email struct {
	config *Config
	auth   smtp.Auth
}

var _ notifications.NotificationService = &Email{}

// New initializes the SMTP email service with the configuration. It sets the
// SMTP auth if the username and password are provided. It returns an error if
// the configuration is invalid or if the from email could not be parsed.
func (se *Email) New(rawConfig any) error {
	// parse configuration
	config, ok := rawConfig.(*Config)
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
func (se *Email) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// compose email body
	body, err := se.composeBody(notification)
	if err != nil {
		return fmt.Errorf("could not compose email body: %v", err)
	}
	server := fmt.Sprintf("%s:%d", se.config.SMTPServer, se.config.SMTPPort)
	errCh := make(chan error, 1)
	go func() {
		errCh <- se.dialAndSend(server, body, notification.ToAddress)
		close(errCh)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

// dialAndSend establishes a TCP connection to the SMTP server using the
// configured timeout, then sends the pre-composed message body.
func (se *Email) dialAndSend(server string, body []byte, to string) error {
	dialer := &net.Dialer{Timeout: se.config.SMTPTimeout}
	conn, err := dialer.Dial("tcp", server)
	if err != nil {
		return fmt.Errorf("could not dial SMTP server: %w", err)
	}
	// apply the same timeout as a deadline for the send phase
	if se.config.SMTPTimeout > 0 {
		if err := conn.SetDeadline(time.Now().Add(se.config.SMTPTimeout)); err != nil {
			_ = conn.Close()
			return fmt.Errorf("could not set send deadline: %w", err)
		}
	}
	c, err := smtp.NewClient(conn, se.config.SMTPServer)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("could not create SMTP client: %w", err)
	}
	defer c.Close() //nolint:errcheck
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: se.config.SMTPServer}); err != nil {
			return fmt.Errorf("could not start TLS: %w", err)
		}
	}
	if se.auth != nil {
		if err := c.Auth(se.auth); err != nil {
			return fmt.Errorf("could not authenticate: %w", err)
		}
	}
	if err := c.Mail(se.config.FromAddress); err != nil {
		return fmt.Errorf("could not set mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("could not set rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("could not open data writer: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("could not write mail body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("could not close data writer: %w", err)
	}
	return c.Quit()
}

// composeBody creates the email body with the notification data. It creates a
// multipart email with a plain text and an HTML part. It returns the email
// content as a byte slice or an error if the body could not be composed.
func (se *Email) composeBody(notification *notifications.Notification) ([]byte, error) {
	// parse 'to' email
	to, err := mail.ParseAddress(notification.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("could not parse to email: %v", err)
	}
	// create email headers
	var headers bytes.Buffer
	boundary := "----=_Part_0_123456789.123456789"
	fromAddr := mail.Address{Name: se.config.FromName, Address: se.config.FromAddress}
	fmt.Fprintf(&headers, "From: %s\r\n", fromAddr.String())
	fmt.Fprintf(&headers, "To: %s\r\n", to.String())
	if notification.ReplyTo != "" {
		replyToAddress, err := mail.ParseAddress(notification.ReplyTo)
		if err != nil {
			return nil, fmt.Errorf("could not parse reply-to email: %v", err)
		}
		fmt.Fprintf(&headers, "Reply-To: %s\r\n", replyToAddress.String())
	}
	if notification.CCAddress != "" {
		cc, err := mail.ParseAddress(notification.CCAddress)
		if err != nil {
			return nil, fmt.Errorf("could not parse cc email: %v", err)
		}
		fmt.Fprintf(&headers, "Cc: %s\r\n", cc.String())
	}
	fmt.Fprintf(&headers, "Subject: %s\r\n", notification.Subject)
	if !notification.EnableTracking {
		fmt.Fprintf(&headers, "X-SMTPAPI: %s\r\n", disableTrackingFilter)
	}
	fmt.Fprint(&headers, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&headers, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	fmt.Fprint(&headers, "\r\n") // blank line between headers and body
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
