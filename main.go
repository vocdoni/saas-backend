package main

import (
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api"

	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init("debug", "stdout", nil)
	// define flags
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	// parse flags
	flag.Parse()

	// initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		panic(err)
	}
	viper.AutomaticEnv()

	host := viper.GetString("host")
	port := viper.GetInt("port")
	apiEndpoint := viper.GetString("vocdoniApi")
	secret := viper.GetString("secret")
	privKey := viper.GetString("privateKey")

	if secret == "" || privKey == "" {
		log.Fatal("secret and privateKey are required")
	}

	acc, err := account.New(privKey, apiEndpoint)
	if err != nil {
		log.Fatal(err)
	}

	// create the local API server
	api.New(secret, acc).Start(host, port)

	// Wait forever, as the server is running in a goroutine
	log.Infow("server started", "host", host, "port", port)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
