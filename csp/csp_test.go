package csp

import (
	"context"
	"math/big"
	"testing"

	"github.com/vocdoni/saas-backend/csp/testutil"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/test"
	"go.vocdoni.io/dvote/util"
)

const (
	testUserEmail = "user@test.com"
	adminEmail    = "admin@test.com"
	adminUser     = "admin"
	adminPass     = "admin123"
)

var (
	testMongoURI        string
	testMailService     = new(testutil.SMTP)
	testSMSService      = new(testutil.MockSMS)
	testRootKey         = new(internal.HexBytes).SetString("700e669712473377a92457f3ff2a4d8f6b17e139f127738018a80fe26983f410")
	testUserID          = internal.HexBytes("userID")
	testBundleID        = internal.HexBytes("bundleID")
	testUserWeight      = uint64(42)
	testUserWeightBytes = big.NewInt(int64(testUserWeight)).Bytes()
	testPID             = internal.HexBytes(util.RandomBytes(32))
	testToken           = internal.HexBytes("token")
	testAddress         = internal.HexBytes("address")
	testOrgName         = "Test Organization"
	testOrgLogo         = "https://example.com/logo.png"
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(err)
	}
	// ensure the container is stopped when the test finishes
	defer func() { _ = dbContainer.Terminate(ctx) }()
	// get the MongoDB connection string
	testMongoURI, err = dbContainer.ConnectionString(ctx)
	if err != nil {
		panic(err)
	}
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
	if err := mailtemplates.Load(); err != nil {
		panic(err)
	}
	m.Run()
}
