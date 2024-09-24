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

type SMTPConfig struct {
	FromName     string
	FromAddress  string
	SMTPServer   string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
}

type SMTPEmail struct {
	config *SMTPConfig
	auth   smtp.Auth
}

func (se *SMTPEmail) Init(rawConfig any) error {
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
	se.auth = smtp.PlainAuth("", se.config.SMTPUsername, se.config.SMTPPassword, se.config.SMTPServer)
	return nil
}

func (se *SMTPEmail) SendNotification(ctx context.Context, notification *notifications.Notification) error {
	// parse 'to' email
	to, err := mail.ParseAddress(notification.ToAddress)
	if err != nil {
		return fmt.Errorf("could not parse to email: %v", err)
	}
	// create email headers
	var headers bytes.Buffer
	boundary := "----=_Part_0_123456789.123456789"
	headers.WriteString(fmt.Sprintf("From: %s\r\n", se.config.FromAddress))
	headers.WriteString(fmt.Sprintf("To: %s\r\n", to.String()))
	headers.WriteString(fmt.Sprintf("Subject: %s\r\n", notification.Subject))
	headers.WriteString("MIME-Version: 1.0\r\n")
	headers.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary))
	headers.WriteString("\r\n") // blank line between headers and body
	// create multipart writer
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	writer.SetBoundary(boundary)
	// TODO: plain text part
	// textPart, _ := writer.CreatePart(textproto.MIMEHeader{
	// 	"Content-Type":              {"text/plain; charset=\"UTF-8\""},
	// 	"Content-Transfer-Encoding": {"7bit"},
	// })
	// textPart.Write([]byte(notification.PlainBody))
	// HTML part
	htmlPart, _ := writer.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=\"UTF-8\""},
		"Content-Transfer-Encoding": {"7bit"},
	})
	htmlPart.Write([]byte(notification.Body))
	writer.Close()
	// combine headers and body
	var email bytes.Buffer
	email.Write(headers.Bytes())
	email.Write(body.Bytes())
	// send the email
	server := fmt.Sprintf("%s:%d", se.config.SMTPServer, se.config.SMTPPort)
	return smtp.SendMail(server, se.auth, se.config.FromAddress, []string{notification.ToAddress}, email.Bytes())
}
