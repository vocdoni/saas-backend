// Package main provides a CLI tool that enables an organization as an integrator
// and sets its managed-org limit.
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
	flag.Bool("setIntegrator", false, "enable the given organization as an integrator and set its managed-org limit")
	flag.String("orgAddress", "", "organization address (hex) for --setIntegrator")
	flag.Int("maxManagedOrgs", 0, "integrator limit: max managed organizations")
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
	err = setIntegrator(database, viper.GetString("orgAddress"), viper.GetInt("maxManagedOrgs"))
	if err != nil {
		log.Fatalf("could not set integrator: %v", err)
	}
}

// setIntegrator enables the organization at the given address as an integrator and
// sets its managed-org limit override. The aggregate process/census caps come from the
// integrator's subscription plan (Plan.Organization.MaxProcesses / MaxCensus).
func setIntegrator(database *db.MongoStorage, orgAddress string, maxOrgs int) error {
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
	// Setting a per-organization limits override both enables integrator status and
	// caps how many organizations it may manage (see Subscriptions.IsIntegrator).
	org.IntegratorLimits = &db.IntegratorLimits{
		MaxManagedOrgs: maxOrgs,
	}
	if err := database.SetOrganization(org); err != nil {
		return fmt.Errorf("could not update organization: %w", err)
	}
	log.Infow("organization is now an integrator",
		"address", addr.Hex(),
		"maxManagedOrgs", maxOrgs)
	return nil
}
