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
	flag.Int("maxManagedOrgs", 0,
		"required with --setIntegrator: max managed organizations (must be > 0; it overrides the plan's integrator limit)")
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
	addr, err := validateSetIntegratorArgs(orgAddress, maxOrgs)
	if err != nil {
		return err
	}
	// Setting a per-organization limits override both enables integrator status and
	// caps how many organizations it may manage (see Subscriptions.IsIntegrator).
	limits := &db.IntegratorLimits{MaxManagedOrgs: maxOrgs}
	if err := database.SetOrganizationIntegratorLimits(addr, limits); err != nil {
		return fmt.Errorf("could not update organization: %w", err)
	}
	log.Infow("organization is now an integrator",
		"address", addr.Hex(),
		"maxManagedOrgs", maxOrgs)
	return nil
}

// validateSetIntegratorArgs checks the --setIntegrator arguments and returns the parsed
// organization address. maxOrgs must be positive: a per-organization override with
// MaxManagedOrgs 0 does not fall back to the plan, it disables integrator status
// (see Subscriptions.IsIntegrator), which is the opposite of what --setIntegrator is for.
func validateSetIntegratorArgs(orgAddress string, maxOrgs int) (common.Address, error) {
	if orgAddress == "" {
		return common.Address{}, fmt.Errorf("orgAddress is required")
	}
	if !common.IsHexAddress(orgAddress) {
		return common.Address{}, fmt.Errorf("invalid orgAddress: %q is not a hex address", orgAddress)
	}
	if maxOrgs <= 0 {
		return common.Address{}, fmt.Errorf("maxManagedOrgs must be greater than 0, got %d", maxOrgs)
	}
	return common.HexToAddress(orgAddress), nil
}
