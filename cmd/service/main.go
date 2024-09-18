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
	"github.com/vocdoni/saas-backend/notifications/sendgrid"
	"github.com/vocdoni/saas-backend/notifications/twilio"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init("debug", "stdout", nil)
	// define flags
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "saasdb", "The name of the MongoDB database")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.BoolP("fullTransparentMode", "a", false, "allow all transactions and do not modify any of them")
	flag.String("sendgridAPIKey", "", "SendGrid API key")
	flag.String("sendgridFromAddress", "", "SendGrid from address")
	flag.String("sendgridFromName", "Vocdoni", "SendGrid from name")
	flag.String("emailTemplatesPath", "./assets", "path to the email templates")
	flag.String("twilioAccountSid", "", "Twilio account SID")
	flag.String("twilioAuthToken", "", "Twilio auth token")
	flag.String("twilioFromNumber", "", "Twilio from number")
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
	// mail vars
	sendgridAPIKey := viper.GetString("sendgridAPIKey")
	sendgridFromAddress := viper.GetString("sendgridFromAddress")
	sendgridFromName := viper.GetString("sendgridFromName")
	emailTemplatesPath := viper.GetString("emailTemplatesPath")
	// sms vars
	twilioAccountSid := viper.GetString("twilioAccountSid")
	twilioAuthToken := viper.GetString("twilioAuthToken")
	twilioFromNumber := viper.GetString("twilioFromNumber")
	// initialize the MongoDB database
	database, err := db.New(mongoURL, mongoDB)
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
	// create email notifications service if the required parameters are set and
	// include it in the API configuration
	if sendgridAPIKey != "" && sendgridFromAddress != "" && sendgridFromName != "" {
		apiConf.MailService = new(sendgrid.SendGridEmail)
		if err := apiConf.MailService.Init(&sendgrid.SendGridConfig{
			FromName:    sendgridFromName,
			FromAddress: sendgridFromAddress,
			APIKey:      sendgridAPIKey,
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
		log.Infow("email service created", "from", fmt.Sprintf("%s <%s>", sendgridFromName, sendgridFromAddress))
	}
	// create SMS notifications service if the required parameters are set and
	// include it in the API configuration
	if twilioAccountSid != "" && twilioAuthToken != "" && twilioFromNumber != "" {
		apiConf.SMSService = new(twilio.TwilioSMS)
		if err := apiConf.SMSService.Init(&twilio.TwilioConfig{
			AccountSid: twilioAccountSid,
			AuthToken:  twilioAuthToken,
			FromNumber: twilioFromNumber,
		}); err != nil {
			log.Fatalf("could not create the SMS service: %v", err)
		}
		log.Infow("SMS service created", "from", twilioFromNumber)
	}
	// create the local API server
	api.New(apiConf).Start()
	log.Infow("server started", "host", host, "port", port)
	// wait forever, as the server is running in a goroutine
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
