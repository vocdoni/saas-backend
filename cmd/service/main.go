package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/smtp"
	"github.com/vocdoni/saas-backend/stripe"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

func main() {
	// define flags
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("webURL", "w", "https://saas-dev.vocdoni.app", "The URL of the web application")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "saasdb", "The name of the MongoDB database")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.BoolP("fullTransparentMode", "a", false, "allow all transactions and do not modify any of them")
	flag.String("emailTemplatesPath", "./assets", "path to the email templates")
	flag.String("smtpServer", "", "SMTP server")
	flag.Int("smtpPort", 587, "SMTP port")
	flag.String("smtpUsername", "", "SMTP username")
	flag.String("smtpPassword", "", "SMTP password")
	flag.String("emailFromAddress", "", "Email service from address")
	flag.String("emailFromName", "Vocdoni", "Email service from name")
	flag.String("stripeApiSecret", "", "Stripe API secret")
	flag.String("stripeWebhookSecret", "", "Stripe Webhook secret")
	// parse flags
	flag.Parse()
	// initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		panic(err)
	}
	viper.AutomaticEnv()
	// read the configuration
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
	emailTemplatesPath := viper.GetString("emailTemplatesPath")
	smtpServer := viper.GetString("smtpServer")
	smtpPort := viper.GetInt("smtpPort")
	smtpUsername := viper.GetString("smtpUsername")
	smtpPassword := viper.GetString("smtpPassword")
	emailFromAddress := viper.GetString("emailFromAddress")
	emailFromName := viper.GetString("emailFromName")
	// stripe vars
	stripeApiSecret := viper.GetString("stripeApiSecret")
	stripeWebhookSecret := viper.GetString("stripeWebhookSecret")

	log.Init("debug", "stdout", os.Stderr)
	// create Stripe client and include it in the API configuration
	var stripeClient *stripe.StripeClient
	if stripeApiSecret != "" || stripeWebhookSecret != "" {
		stripeClient = stripe.New(stripeApiSecret, stripeWebhookSecret)
	} else {
		log.Fatalf("stripeApiSecret and stripeWebhookSecret are required")
	}
	availablePlans, err := stripeClient.GetPlans()
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
	// create the Vocdoni client account with the private key
	acc, err := account.New(privKey, apiEndpoint)
	if err != nil {
		log.Fatal(err)
	}
	log.Infow("API client created", "endpoint", apiEndpoint, "chainID", apiClient.ChainID())
	// init the API configuration
	apiConf := &api.APIConfig{
		Host:                host,
		Port:                port,
		Secret:              secret,
		DB:                  database,
		Client:              apiClient,
		Account:             acc,
		WebAppURL:           webURL,
		FullTransparentMode: fullTransparentMode,
		StripeClient:        stripeClient,
	}
	// overwrite the email notifications service with the SMTP service if the
	// required parameters are set and include it in the API configuration
	if smtpServer != "" && smtpUsername != "" && smtpPassword != "" {
		if emailFromAddress == "" || emailFromName == "" {
			log.Fatal("emailFromAddress and emailFromName are required")
		}
		apiConf.MailService = new(smtp.SMTPEmail)
		if err := apiConf.MailService.New(&smtp.SMTPConfig{
			FromName:     emailFromName,
			FromAddress:  emailFromAddress,
			SMTPServer:   smtpServer,
			SMTPPort:     smtpPort,
			SMTPUsername: smtpUsername,
			SMTPPassword: smtpPassword,
		}); err != nil {
			log.Fatalf("could not create the email service: %v", err)
		}
		// load email templates
		if emailTemplatesPath != "" {
			apiConf.MailTemplates, err = notifications.GetMailTemplates(emailTemplatesPath)
			if err != nil {
				log.Fatalf("could not load email templates: %v", err)
			}
		}
		log.Infow("email service created", "from", fmt.Sprintf("%s <%s>", emailFromName, emailFromAddress))
	}
	subscriptions := subscriptions.New(&subscriptions.SubscriptionsConfig{
		DB: database,
	})
	apiConf.Subscriptions = subscriptions
	// create the local API server
	api.New(apiConf).Start()
	log.Infow("server started", "host", host, "port", port)
	// wait forever, as the server is running in a goroutine
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
