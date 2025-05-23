// Package main is the entry point for the Vocdoni SaaS backend service.
// It initializes and configures all components including database connections,
// API endpoints, authentication services, and notification systems.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/notifications/twilio"
	"github.com/vocdoni/saas-backend/objectstorage"
	"github.com/vocdoni/saas-backend/stripe"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

func main() {
	// define flags
	flag.String("serverURL", "http://localhost:8080", "The full URL of the server (http or https)")
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("webURL", "w", "https://saas-dev.vocdoni.app", "The URL of the web application")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "", "The name of the MongoDB database")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.BoolP("fullTransparentMode", "a", false, "allow all transactions and do not modify any of them")
	flag.String("smtpServer", "", "SMTP server")
	flag.Int("smtpPort", 587, "SMTP port")
	flag.String("smtpUsername", "", "SMTP username")
	flag.String("smtpPassword", "", "SMTP password")
	flag.String("emailFromAddress", "", "Email service from address")
	flag.String("emailFromName", "Vocdoni", "Email service from name")
	flag.String("twilioAccountSid", "", "Twilio account SID")
	flag.String("twilioAuthToken", "", "Twilio auth token")
	flag.String("twilioFromNumber", "", "Twilio from number")
	flag.String("stripeApiSecret", "", "Stripe API secret")
	flag.String("stripeWebhookSecret", "", "Stripe Webhook secret")
	flag.String("oauthServiceURL", "http://oauth.vocdoni.net", "OAuth service URL")
	// parse flags
	flag.Parse()
	// initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		panic(err)
	}
	viper.AutomaticEnv()
	// read the configuration
	server := viper.GetString("serverURL")
	host := viper.GetString("host")
	port := viper.GetInt("port")
	apiEndpoint := viper.GetString("vocdoniApi")
	webURL := viper.GetString("webURL")
	secret := viper.GetString("secret")
	if secret == "" {
		log.Fatal("secret is required")
	}
	// MongoDB vars
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	// email vars
	smtpServer := viper.GetString("smtpServer")
	smtpPort := viper.GetInt("smtpPort")
	smtpUsername := viper.GetString("smtpUsername")
	smtpPassword := viper.GetString("smtpPassword")
	emailFromAddress := viper.GetString("emailFromAddress")
	emailFromName := viper.GetString("emailFromName")
	// sms vars
	twilioAccountSid := viper.GetString("twilioAccountSid")
	twilioAuthToken := viper.GetString("twilioAuthToken")
	twilioFromNumber := viper.GetString("twilioFromNumber")
	// stripe vars
	stripeAPISecret := viper.GetString("stripeApiSecret")
	stripeWebhookSecret := viper.GetString("stripeWebhookSecret")
	// oauth vars
	oauthServiceURL := viper.GetString("oauthServiceURL")

	log.Init("debug", "stdout", os.Stderr)
	// init Stripe client
	if stripeAPISecret == "" && stripeWebhookSecret == "" {
		log.Fatalf("stripeApiSecret and stripeWebhookSecret are required")
	}
	stripe.Init(stripeAPISecret, stripeWebhookSecret)
	availablePlans, err := stripe.GetPlans()
	if err != nil || len(availablePlans) == 0 {
		log.Fatalf("could not get the available plans: %v", err)
	}

	// initialize the MongoDB database
	database, err := db.New(mongoURL, mongoDB, availablePlans)
	if err != nil {
		log.Fatalf("could not create the MongoDB database: %v", err)
	}
	defer database.Close()
	// create the remote API client
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		log.Fatalf("could not create the remote API client: %v", err)
	}
	privKey := viper.GetString("privateKey")
	fullTransparentMode := viper.GetBool("fullTransparentMode")
	// check the required parameters
	if secret == "" || privKey == "" {
		log.Fatal("secret and privateKey are required")
	}
	bPrivKey := internal.HexBytes{}
	if err := bPrivKey.ParseString(privKey); err != nil {
		log.Fatalf("could not parse the private key: %v", err)
	}
	// create the Vocdoni client account with the private key
	acc, err := account.New(privKey, apiEndpoint)
	if err != nil {
		log.Fatal(err)
	}
	log.Infow("API client created", "endpoint", apiEndpoint, "chainID", apiClient.ChainID())
	// init the API configuration
	apiConf := &api.Config{
		Host:                host,
		Port:                port,
		Secret:              secret,
		DB:                  database,
		Client:              apiClient,
		Account:             acc,
		WebAppURL:           webURL,
		ServerURL:           server,
		FullTransparentMode: fullTransparentMode,
		OAuthServiceURL:     oauthServiceURL,
	}

	cspConf := &csp.Config{
		RootKey: bPrivKey,
		DB:      database,
	}
	// overwrite the email notifications service with the SMTP service if the
	// required parameters are set and include it in the API configuration
	if smtpServer != "" && smtpUsername != "" && smtpPassword != "" {
		if emailFromAddress == "" || emailFromName == "" {
			log.Fatal("emailFromAddress and emailFromName are required")
		}
		apiConf.MailService = new(smtp.Email)
		if err := apiConf.MailService.New(&smtp.Config{
			FromName:     emailFromName,
			FromAddress:  emailFromAddress,
			SMTPServer:   smtpServer,
			SMTPPort:     smtpPort,
			SMTPUsername: smtpUsername,
			SMTPPassword: smtpPassword,
		}); err != nil {
			log.Fatalf("could not create the email service: %v", err)
		}
		cspConf.MailService = apiConf.MailService
		// load email templates
		if err := mailtemplates.Load(); err != nil {
			log.Fatalf("could not load email templates: %v", err)
		}
		log.Infow("email templates loaded",
			"templates", len(mailtemplates.Available()))
	}
	// create SMS notifications service if the required parameters are set and
	// include it in the API configuration
	if twilioAccountSid != "" && twilioAuthToken != "" && twilioFromNumber != "" {
		apiConf.SMSService = new(twilio.SMS)
		if err := apiConf.SMSService.New(&twilio.Config{
			AccountSid: twilioAccountSid,
			AuthToken:  twilioAuthToken,
			FromNumber: twilioFromNumber,
		}); err != nil {
			log.Fatalf("could not create the SMS service: %v", err)
		}
		cspConf.SMSService = apiConf.SMSService
		log.Infow("SMS service created", "from", twilioFromNumber)
	}

	// create the CSP service and include it in the API configuration
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if apiConf.CSP, err = csp.New(ctx, cspConf); err != nil {
		log.Fatalf("could not create the CSP service: %v", err)
		return
	}
	apiConf.Subscriptions = subscriptions.New(&subscriptions.Config{
		DB: database,
	})
	// initialize the s3 like  object storage
	apiConf.ObjectStorage, err = objectstorage.New(&objectstorage.Config{
		DB: database,
	})
	if err != nil {
		log.Fatal(err)
	}
	// create the local API server
	api.New(apiConf).Start()
	log.Infow("server started", "host", host, "port", port)
	// wait forever, as the server is running in a goroutine
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
