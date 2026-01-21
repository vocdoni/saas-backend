package notifications

import (
	"context"
	"regexp"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/testutil"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/test"
)

// testMailService is the test mail service for the tests. Make it global so it
// can be accessed by the tests directly.
var (
	testMailService = new(testutil.SMTP)
	testSMSService  = new(testutil.MockSMS)
)

const (
	testUserEmail     = "user@test.com"
	testUserPhone     = "+34612345678"
	adminEmail        = "admin@test.com"
	adminUser         = "admin"
	adminPass         = "admin123"
	testOrgName       = "Test Organization"
	testOrgLogo       = "https://example.com/logo.png"
	testRemainingTime = "5m30s"
)

var testOrgAddress = common.HexToAddress("0xfafa")

var testOrgInfo = OrganizationInfo{
	Address: testOrgAddress,
	Name:    testOrgName,
	Logo:    testOrgLogo,
}

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
	if err := testMailService.New(&testutil.SMTPConfig{
		Config: smtp.Config{
			FromAddress:  adminEmail,
			SMTPUsername: adminUser,
			SMTPPassword: adminPass,
			SMTPServer:   mailHost,
			SMTPPort:     smtpPort.Int(),
		},
		TestAPIPort: apiPort.Int(),
	}); err != nil {
		panic(err)
	}
	m.Run()
}

func TestNewEmailChallengeSent(t *testing.T) {
	c := qt.New(t)
	// invalid inputs
	_, err := NewNotificationChallenge(
		EmailChallenge,
		"en",
		nil,
		[]byte("bundle"),
		testUserEmail,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		EmailChallenge,
		"en",
		[]byte("user"),
		nil,
		testUserEmail,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		EmailChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		"",
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		EmailChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserEmail,
		"",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	// invalid notification template
	_, err = NewNotificationChallenge(
		EmailChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserEmail,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrCreateNotification)
	// load email templates
	c.Assert(mailtemplates.Load(), qt.IsNil)
	// invalid notification type
	_, err = NewNotificationChallenge(
		"invalid",
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserEmail,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationType)
	// valid notification
	ch, err := NewNotificationChallenge(
		EmailChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserEmail,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.IsNil)
	c.Assert(ch, qt.Not(qt.IsNil))
	c.Assert(ch.Type, qt.Equals, EmailChallenge)
	c.Assert(string(ch.UserID), qt.Equals, "user")
	c.Assert(string(ch.BundleID), qt.Equals, "bundle")
	c.Assert(ch.Notification, qt.Not(qt.IsNil))
	c.Assert(ch.Notification.ToAddress, qt.Equals, testUserEmail)
	c.Assert(ch.Notification.ToNumber, qt.Equals, "")
	c.Assert(ch.Notification.PlainBody, qt.Contains, "123456")
	c.Assert(ch.Notification.Subject, qt.Contains, testOrgName)
	// send the notification and check the result
	c.Assert(ch.Send(context.Background(), testMailService), qt.IsNil)
	c.Assert(ch.Success, qt.IsTrue)
	// get the verification code from the email
	mailBody, err := testMailService.FindEmail(context.Background(), testUserEmail)
	c.Assert(err, qt.IsNil)
	// parse the email body to get the verification code
	seedNotification, err := mailtemplates.
		VerifyOTPCodeNotification.
		Localized("en").
		ExecPlain(struct {
			Code, Organization string
		}{
			Code:         `(.{6})`,
			Organization: testOrgName,
		})
	c.Assert(err, qt.IsNil)
	rgxNotification := regexp.MustCompile(seedNotification.PlainBody)
	// verify the user
	mailCode := rgxNotification.FindStringSubmatch(mailBody)
	c.Assert(mailCode, qt.HasLen, 2)
	c.Assert(mailCode[1], qt.Equals, "123456")
	regSubject := regexp.MustCompile(seedNotification.Subject)
	subjectMatch := regSubject.FindStringSubmatch(mailBody)
	c.Assert(subjectMatch, qt.HasLen, 1)
	c.Assert(subjectMatch[0], qt.Contains, testOrgName)
}

func TestNewSMSChallengeSent(t *testing.T) {
	c := qt.New(t)

	// load templates
	c.Assert(mailtemplates.Load(), qt.IsNil)

	// invalid inputs
	_, err := NewNotificationChallenge(
		SMSChallenge,
		"en",
		nil,
		[]byte("bundle"),
		testUserPhone,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		SMSChallenge,
		"en",
		[]byte("user"),
		nil,
		testUserPhone,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		SMSChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		"",
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)
	_, err = NewNotificationChallenge(
		SMSChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserPhone,
		"",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.ErrorIs, ErrInvalidNotificationInputs)

	// valid notification
	ch, err := NewNotificationChallenge(
		SMSChallenge,
		"en",
		[]byte("user"),
		[]byte("bundle"),
		testUserPhone,
		"123456",
		testOrgInfo,
		testRemainingTime,
	)
	c.Assert(err, qt.IsNil)
	c.Assert(ch, qt.Not(qt.IsNil))
	c.Assert(ch.Type, qt.Equals, SMSChallenge)
	c.Assert(string(ch.UserID), qt.Equals, "user")
	c.Assert(string(ch.BundleID), qt.Equals, "bundle")
	c.Assert(ch.Notification, qt.Not(qt.IsNil))
	c.Assert(ch.Notification.ToAddress, qt.Equals, "")
	c.Assert(ch.Notification.ToNumber, qt.Equals, testUserPhone)
	c.Assert(ch.Notification.PlainBody, qt.Contains, "123456")
	c.Assert(ch.Notification.Subject, qt.Contains, testOrgName)
	// send the notification and check the result
	c.Assert(ch.Send(context.Background(), testSMSService), qt.IsNil)
	c.Assert(ch.Success, qt.IsTrue)
	// get the verification code from the mock SMS
	smsNotification := testSMSService.ConsumeSMS(testUserPhone)
	c.Assert(smsNotification, qt.IsNotNil)
	c.Assert(smsNotification.PlainBody, qt.Contains, "123456")
}
