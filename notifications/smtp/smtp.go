// Package smtp provides an SMTP-based implementation of the NotificationService interface
// for sending email notifications.
package smtp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/vocdoni/saas-backend/notifications"
)

// defaultSendTimeout bounds an SMTP delivery whose context carries no deadline.
const defaultSendTimeout = 30 * time.Second

var disableTrackingFilter = []byte(`{"filters":{"clicktrack":{"settings":{"enable":0,"enable_text":false}}}}`)

// Config represents the configuration for the SMTP email service. It
// contains the sender's name, address, SMTP username, password, server and
// port. The TestAPIPort is used to define the port of the API service used
// for testing the email service locally to check messages (for example using
// MailHog).
type Config struct {
	FromName     string
	FromAddress  string
	SMTPUsername string
	SMTPPassword string
	SMTPServer   string
	SMTPPort     int
}

// Valid checks if the SMTP configuration is valid. It checks that the required
// auth parameters are provided based on the SMTP server type. It also checks
// that the required identity parameters are provided, from address and name.
func (c Config) Valid() bool {
	// check if the required SMTP connection parameters are provided
	if c.SMTPServer == "" || c.SMTPPort == 0 {
		return false
	}
	// check if the required auth parameters are provided:
	//  - for a local server, no auth required (username and password can be empty)
	//  - for a remote server, auth required (username and password are mandatory)
	localSMTPServer := c.SMTPServer == "localhost" || c.SMTPServer == "127.0.0.1"
	validCredentials := c.SMTPUsername != "" && c.SMTPPassword != ""
	if !localSMTPServer && !validCredentials {
		return false
	}
	// check if the required identity parameters are provided, from address and
	// name
	if c.FromAddress == "" || c.FromName == "" {
		return false
	}
	return true
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
	if !config.Valid() {
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
	return classifySendError(se.sendMail(ctx, notification.ToAddress, body))
}

// classifySendError marks an SMTP 5xx reply (unknown mailbox, rejected message...)
// as permanent, since retrying the same message cannot succeed. 4xx deferrals and
// network errors are returned unchanged.
func classifySendError(err error) error {
	var protoErr *textproto.Error
	if errors.As(err, &protoErr) && protoErr.Code >= 500 && protoErr.Code < 600 {
		return &notifications.PermanentError{Err: err}
	}
	return err
}

// sendMail delivers msg to the recipient like smtp.SendMail does (STARTTLS when offered, then
// AUTH), but bounded by ctx: smtp.SendMail dials without a timeout and sets no I/O deadline, so a
// stalled server would leave the sending goroutine and its connection blocked indefinitely.
func (se *Email) sendMail(ctx context.Context, to string, msg []byte) error {
	server := net.JoinHostPort(se.config.SMTPServer, strconv.Itoa(se.config.SMTPPort))
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", server)
	if err != nil {
		return fmt.Errorf("could not connect to the smtp server: %w", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultSendTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return fmt.Errorf("could not set the smtp connection deadline: %w", err)
	}
	// unblock the exchange as soon as ctx is canceled, not only at its deadline
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	client, err := smtp.NewClient(conn, se.config.SMTPServer)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("could not start the smtp session: %w", err)
	}
	defer func() { _ = client.Close() }()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: se.config.SMTPServer}); err != nil {
			return fmt.Errorf("could not start tls: %w", err)
		}
	}
	if se.auth != nil {
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(se.auth); err != nil {
				return fmt.Errorf("could not authenticate: %w", err)
			}
		}
	}
	if err := client.Mail(se.config.FromAddress); err != nil {
		return fmt.Errorf("could not set the sender: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("could not set the recipient: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("could not start the message data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("could not write the message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("could not send the message: %w", err)
	}
	return client.Quit()
}

// encodeHeader makes a value safe for a single header line: CR and LF would end the header and
// let the value inject more headers (e.g. a Bcc), and non-ASCII text must be MIME-encoded.
func encodeHeader(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(value)
	return mime.QEncoding.Encode("utf-8", value)
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
	fmt.Fprintf(&headers, "Subject: %s\r\n", encodeHeader(notification.Subject))
	fmt.Fprintf(&headers, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	messageID, err := newMessageID(se.config.FromAddress)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(&headers, "Message-ID: %s\r\n", messageID)
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
	if err := writeQuotedPrintablePart(writer, "text/plain", notification.PlainBody); err != nil {
		return nil, fmt.Errorf("could not write plain text part: %w", err)
	}
	if err := writeQuotedPrintablePart(writer, "text/html", notification.Body); err != nil {
		return nil, fmt.Errorf("could not write HTML part: %w", err)
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

// writeQuotedPrintablePart adds a UTF-8 part of the given media type to w, encoded as
// quoted-printable so accented text and long lines survive 7-bit relays (RFC 2045).
func writeQuotedPrintablePart(w *multipart.Writer, mediaType, content string) error {
	part, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {mediaType + "; charset=\"UTF-8\""},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return fmt.Errorf("could not create part: %w", err)
	}
	qp := quotedprintable.NewWriter(part)
	if _, err := qp.Write([]byte(content)); err != nil {
		return fmt.Errorf("could not encode part: %w", err)
	}
	if err := qp.Close(); err != nil {
		return fmt.Errorf("could not flush part: %w", err)
	}
	return nil
}

// newMessageID returns a unique RFC 5322 Message-ID on the sender's domain.
func newMessageID(fromAddress string) (string, error) {
	domain := "localhost"
	if at := strings.LastIndex(fromAddress, "@"); at >= 0 && at < len(fromAddress)-1 {
		domain = fromAddress[at+1:]
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("could not generate message id: %w", err)
	}
	return fmt.Sprintf("<%s@%s>", hex.EncodeToString(id), domain), nil
}
