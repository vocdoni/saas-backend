// Package main provides a local fake SMTP server for testing purposes as a CLI
// tool. It reuses the SMTP-related envvars/flags from cmd/service (smtpServer,
// smtpPort).
package main

import (
	"context"
	"os"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.vocdoni.io/dvote/log"

	"github.com/vocdoni/saas-backend/internal/fakesmtpserver"
)

func main() {
	log.Init(log.LogLevelDebug, "stdout", os.Stderr)

	flag.String("smtpServer", "0.0.0.0", "SMTP local host")
	flag.Int("smtpPort", 1025, "SMTP local port")
	// parse flags
	flag.Parse()
	// initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Fatalf("could not bind flags: %v", err)
	}
	viper.AutomaticEnv()

	smtpHost := viper.GetString("smtpServer")
	smtpPort := viper.GetInt("smtpPort")
	// start SMTP server
	messageChan := make(chan string, 100)
	smtpServer := fakesmtpserver.NewServer(smtpHost, smtpPort, messageChan)

	if err := smtpServer.Start(context.Background()); err != nil {
		log.Fatalf("error starting smtp server: %v", err)
	}

	log.Infow("Local fake SMTP server started", "host", smtpHost, "port", smtpPort)
	for {
		msg := <-messageChan
		log.Info(msg)
	}
}
