package notifications

import (
	"context"
	"regexp"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/test"
)

// testMailService is the test mail service for the tests. Make it global so it
// can be accessed by the tests directly.
var testMailService *smtp.Email

const (
	testUserEmail = "user@test.com"
	adminEmail    = "admin@test.com"
	adminUser     = "admin"
	adminPass     = "admin123"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start test mail server
	testMailServer, err := test.StartMailService(ctx)
	if err != nil {
		panic(err)
	}
	// get the host, the SMTP port and the API port
	mailHost, err := testMailServer.Host(ctx)
	if err != nil {
		panic(err)
	}
	smtpPort, err := testMailServer.MappedPort(ctx, test.MailSMTPPort)
	if err != nil {
		panic(err)
	}
	apiPort, err := testMailServer.MappedPort(ctx, test.MailAPIPort)
	if err != nil {
		panic(err)
	}
	// create test mail service
	testMailService = new(smtp.Email)
	if err := testMailService.New(&smtp.Config{
		FromAddress:  adminEmail,
		SMTPUsername: adminUser,
		SMTPPassword: adminPass,
		SMTPServer:   mailHost,
		SMTPPort:     smtpPort.Int(),
		TestAPIPort:  apiPort.Int(),
	}); err != nil {
		panic(err)
	}
	m.Run()
}

func TestNewChallengeSent(t *testing.T) {
	c := qt.New(t)
	// invalid inputs
	_, err := NewNotificationChallenge(EmailChallenge, nil, []byte("bundle"), testUserEmail, "123456")
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(EmailChallenge, []byte("user"), nil, testUserEmail, "123456")
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(EmailChallenge, []byte("user"), []byte("bundle"), "", "123456")
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(EmailChallenge, []byte("user"), []byte("bundle"), testUserEmail, "")
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	// invalid notification template
	_, err = NewNotificationChallenge(EmailChallenge, []byte("user"), []byte("bundle"), testUserEmail, "123456")
	c.Assert(err, qt.ErrorIs, ErrCreateNotification)
	// load email templates
	c.Assert(mailtemplates.Load(), qt.IsNil)
	// invalid notification type
	_, err = NewNotificationChallenge("invalid", []byte("user"), []byte("bundle"), testUserEmail, "123456")
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationType)
	// valid notification
	ch, err := NewNotificationChallenge(EmailChallenge, []byte("user"), []byte("bundle"), testUserEmail, "123456")
	c.Assert(err, qt.IsNil)
	c.Assert(ch, qt.Not(qt.IsNil))
	c.Assert(ch.Type, qt.Equals, EmailChallenge)
	c.Assert(string(ch.UserID), qt.Equals, "user")
	c.Assert(string(ch.BundleID), qt.Equals, "bundle")
	c.Assert(ch.Notification, qt.Not(qt.IsNil))
	c.Assert(ch.Notification.ToAddress, qt.Equals, testUserEmail)
	c.Assert(ch.Notification.ToNumber, qt.Equals, "")
	c.Assert(ch.Notification.PlainBody, qt.Contains, "123456")
	// send the notification and check the result
	c.Assert(ch.Send(context.Background(), testMailService), qt.IsNil)
	c.Assert(ch.Success, qt.IsTrue)
	// get the verification code from the email
	mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
	c.Assert(err, qt.IsNil)
	// parse the email body to get the verification code
	seedNotification, err := mailtemplates.VerifyOTPCodeNotification.ExecPlain(struct{ Code string }{`(.{6})`})
	c.Assert(err, qt.IsNil)
	rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
	// verify the user
	mailCode := rgxNotification.FindStringSubmatch(mailBody)
	c.Assert(mailCode, qt.HasLen, 2)
	c.Assert(mailCode[1], qt.Equals, "123456")
}
