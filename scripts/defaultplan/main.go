// Package defaultplan contains a go script that creates the default plan in
// MongoDB for testing purposes. It uses the same envvars and flags as the
// cmd/service/main.go.
package main

import (
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.vocdoni.io/dvote/log"

	"github.com/vocdoni/saas-backend/db"
)

func main() {
	log.Init(log.LogLevelDebug, "stdout", nil)

	flag.StringP("mongoURL", "m", "", "MongoDB URL")
	flag.StringP("mongoDB", "d", "", "MongoDB database name")
	flag.Parse()

	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Fatalf("could not bind flags: %v", err)
	}
	viper.AutomaticEnv()

	mongoURL := viper.GetString("mongoURL")
	if mongoURL == "" {
		log.Fatal("mongoURL is required")
	}
	mongoDB := viper.GetString("mongoDB")

	log.Infow("connecting to MongoDB", "url", mongoURL, "database", mongoDB)
	storage, err := db.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("failed to connect to MongoDB: %v", err)
	}
	defer storage.Close()

	_, err = storage.DefaultPlan()
	if err == nil {
		log.Info("default plan already exists, nothing to do")
		return
	}
	if err != db.ErrNotFound {
		log.Fatalf("failed to get default plan: %v", err)
	}

	plan := &db.Plan{
		Name:         "Local Dev",
		Default:      true,
		MonthlyPrice: 0,
		YearlyPrice:  0,
		Organization: db.PlanLimits{
			Users:        100,
			SubOrgs:      10,
			MaxProcesses: 1000,
			MaxCensus:    1000,
			MaxDuration:  365,
			CustomURL:    true,
			MaxDrafts:    100,
			CustomPlan:   true,
		},
		VotingTypes: db.VotingTypes{
			Single:     true,
			Multiple:   true,
			Approval:   true,
			Cumulative: true,
			Ranked:     true,
			Weighted:   true,
		},
		Features: db.Features{
			Anonymous:       true,
			Overwrite:       true,
			LiveResults:     true,
			Personalization: true,
			EmailReminder:   true,
			TwoFaSms:        1000,
			TwoFaEmail:      1000,
			WhiteLabel:      true,
			LiveStreaming:   true,
			PhoneSupport:    true,
		},
	}

	id, err := storage.SetPlan(plan)
	if err != nil {
		log.Fatalf("failed to create default plan: %v", err)
	}
	log.Infow("default plan created", "id", id)
}
