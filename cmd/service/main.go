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
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "saasdb", "The name of the MongoDB database")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.BoolP("fullTransparentMode", "a", false, "allow all transactions and do not modify any of them")
	flag.String("subscriptionsFile", "subscriptions.json", "JSON file that contains the subscriptions info")
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
	secret := viper.GetString("secret")
	if secret == "" {
		log.Fatal("secret is required")
	}
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	subscriptionsFile := viper.GetString("subscriptionsFile")
	// email vars
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
	// initialize the MongoDB database
	database, err := db.New(mongoURL, mongoDB, subscriptionsFile)
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
		FullTransparentMode: fullTransparentMode,
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
		log.Infow("email service created", "from", fmt.Sprintf("%s <%s>", emailFromName, emailFromAddress))
	}
	// create Stripe client and include it in the API configuration
	if stripeApiSecret != "" || stripeWebhookSecret != "" {
		apiConf.StripeClient = stripe.New(stripeApiSecret, stripeWebhookSecret)
	} else {
		log.Fatalf("stripeApiSecret and stripeWebhookSecret are required")
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
