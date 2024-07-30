package main

import (
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/api"
	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init("debug", "stdout", nil)
	// define flags
	flag.StringP("chain", "c", "dev", "vocdoni network to connect with")
	flag.StringP("host", "h", "0.0.0.0", "API endpoint listen address")
	flag.IntP("port", "p", 9090, "API endpoint http port")
	flag.StringP("secret", "s", "vocdoniSuperSecret", "API secret")
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
	chain := viper.GetString("chain")
	secret := viper.GetString("secret")

	api.New(secret, chain).Start(host, port)

	// Wait forever, as the server is running in a goroutine
	log.Infow("server started", "host", host, "port", port)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
