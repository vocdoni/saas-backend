// Package main provides a CLI tool that enables an organization as an integrator.
package main

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

func main() {
	flag.StringP("mongoURL", "m", "", "MongoDB connection URL")
	flag.StringP("mongoDB", "d", "", "MongoDB database name")
	flag.Bool("setIntegrator", false, "enable the given organization as an integrator")
	flag.String("orgAddress", "", "organization address (hex) for --setIntegrator")
	flag.Parse()

	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Fatalf("could not bind flags: %v", err)
	}
	viper.AutomaticEnv()
	log.Init("info", "stdout", nil)

	if !viper.GetBool("setIntegrator") {
		log.Fatal("nothing to do: pass --setIntegrator")
	}
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	if mongoURL == "" || mongoDB == "" {
		log.Fatal("mongoURL and mongoDB are required")
	}
	database, err := db.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("could not connect to MongoDB: %v", err)
	}
	defer database.Close()
	err = setIntegrator(database, viper.GetString("orgAddress"))
	if err != nil {
		log.Fatalf("could not set integrator: %v", err)
	}
}

// setIntegrator enables the organization at the given address as an integrator: it may
// create managed organizations and pay for their processes from its wallet, with no limit
// other than the wallet balance.
func setIntegrator(database *db.MongoStorage, orgAddress string) error {
	if orgAddress == "" {
		return fmt.Errorf("orgAddress is required")
	}
	if !common.IsHexAddress(orgAddress) {
		return fmt.Errorf("invalid orgAddress: %q is not a hex address", orgAddress)
	}
	addr := common.HexToAddress(orgAddress)
	org, err := database.Organization(addr)
	if err != nil {
		return fmt.Errorf("could not get organization: %w", err)
	}
	if org.ManagedBy != (common.Address{}) {
		return fmt.Errorf("organization %s is managed by %s and cannot be an integrator", addr.Hex(), org.ManagedBy.Hex())
	}
	org.Integrator = true
	if err := database.SetOrganization(org); err != nil {
		return fmt.Errorf("could not update organization: %w", err)
	}
	log.Infow("organization is now an integrator", "address", addr.Hex())
	return nil
}
