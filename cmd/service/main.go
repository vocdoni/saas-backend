package main

import (
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/api"
	"github.com/vocdoni/saas-backend/db/mongo"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init("debug", "stdout", nil)
	// define flags
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.String("mongo-url", "", "The URL of the MongoDB server")
	flag.String("mongo-db", "backend-saas", "The name of the MongoDB database")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
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
	mongoURL := viper.GetString("mongo-url")
	mongoDB := viper.GetString("mongo-db")
	// initialize the MongoDB database
	db, err := mongo.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("could not create the MongoDB database: %v", err)
	}
	defer db.Close()
	// create the remote API client
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		log.Fatalf("failed to create API client: %v", err)
	}
	log.Infow("API client created", "endpoint", apiEndpoint, "chainID", apiClient.ChainID())
	// create the local API server
	api.New(&api.APIConfig{
		Host:   host,
		Port:   port,
		Secret: secret,
		DB:     db,
		Client: apiClient,
	}).Start()
	// wait forever, as the server is running in a goroutine
	log.Infow("server started", "host", host, "port", port)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
