// Package main implements a CLI to fund a Vocdoni account in a local
// environment that uses Vocone with the faucet enabled.
package main

import (
	"io"
	"net/http"

	"github.com/ethereum/go-ethereum/crypto"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.vocdoni.io/dvote/log"
)

func main() {
	log.Init(log.LogLevelDebug, "stdout", nil)

	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.String("voconeURL", "http://localhost:9090/v2", "Vocone API URL")
	flag.String("faucetPath", "/open/claim", "Faucet endpoint path")
	flag.Parse()

	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Errorf("could not bind flags: %v", err)
		return
	}
	viper.AutomaticEnv()

	privKey := viper.GetString("privateKey")
	if privKey == "" {
		log.Fatal("privateKey is required")
	}

	voconeURL := viper.GetString("voconeURL")
	faucetPath := viper.GetString("faucetPath")

	key, err := crypto.HexToECDSA(privKey)
	if err != nil {
		log.Errorf("invalid private key: %v", err)
		return
	}

	addr := crypto.PubkeyToAddress(key.PublicKey)
	log.Infow("derived address", "address", addr.Hex())

	faucetURL := voconeURL + faucetPath + "/" + addr.Hex()
	resp, err := http.Get(faucetURL)
	if err != nil {
		log.Errorf("faucet request failed: %v", err)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("faucet response read failed: %v", err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		log.Errorf("faucet request failed: %s", string(body))
		return
	}
	log.Infof("faucet response (%d): %s", resp.StatusCode, string(body))
}
