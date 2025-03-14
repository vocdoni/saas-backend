package csp

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

const (
	testUserEmail     = "user@test.com"
	testUserExtraData = "extraData"
	testUserPhone     = "+346787878"
	adminEmail        = "admin@test.com"
	adminUser         = "admin"
	adminPass         = "admin123"
)

var (
	dbClient        *mongo.Client
	testMailService *smtp.SMTPEmail
	testRootKey     = new(internal.HexBytes).SetString("700e669712473377a92457f3ff2a4d8f6b17e139f127738018a80fe26983f410")
	testUserID      = internal.HexBytes("userID")
	testBundleID    = internal.HexBytes("bundleID")
	testPID         = internal.HexBytes("processID")
	testToken       = internal.HexBytes("token")
	testAddress     = internal.HexBytes("address")
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(fmt.Sprintf("failed to start MongoDB container: %v", err))
	}
	// get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	if err != nil {
		panic(fmt.Sprintf("failed to get MongoDB endpoint: %v", err))
	}
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(mongoURI)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	dbClient, err = mongo.Connect(ctx, opts)
	if err != nil {
		panic(fmt.Errorf("cannot connect to mongodb: %w", err))
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	// try to ping the database
	if err = dbClient.Ping(ctx, readpref.Primary()); err != nil {
		panic(fmt.Errorf("cannot ping to mongodb: %w", err))
	}
	// ensure the database is reset when the test finishes
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := dbClient.Disconnect(ctx); err != nil {
			panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
		}
		if err := dbContainer.Terminate(ctx); err != nil {
			panic(fmt.Sprintf("failed to terminate MongoDB container: %v", err))
		}
	}()
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
	testMailService = new(smtp.SMTPEmail)
	if err := testMailService.New(&smtp.SMTPConfig{
		FromAddress:  adminEmail,
		SMTPUsername: adminUser,
		SMTPPassword: adminPass,
		SMTPServer:   mailHost,
		SMTPPort:     smtpPort.Int(),
		TestAPIPort:  apiPort.Int(),
	}); err != nil {
		panic(err)
	}
	if err := mailtemplates.Load(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
