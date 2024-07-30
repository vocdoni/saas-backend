package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/vocdoni/saas-backend/api"
	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init("debug", "stdout", nil)
	api.New("vocdoniSuperSecret").Start("0.0.0.0", 8080)

	// Wait forever, as the server is running in a goroutine
	log.Infow("server started", "host", "localhost", "port", 8080)
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
}
