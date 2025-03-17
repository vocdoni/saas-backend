package main

import (
	"bytes"
	"os"
	"strings"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/dvote/log"
)

func main() {
	// define flags - same as in cmd/service/main.go
	flag.String("serverURL", "http://localhost:8080", "The full URL of the server (http or https)")
	flag.StringP("host", "h", "0.0.0.0", "listen address")
	flag.IntP("port", "p", 8080, "listen port")
	flag.StringP("secret", "s", "", "API secret")
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("webURL", "w", "https://saas-dev.vocdoni.app", "The URL of the web application")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "", "The name of the MongoDB database")
	flag.StringP("privateKey", "k", "", "private key for the Vocdoni account")
	flag.String("stripeApiSecret", "", "Stripe API secret")
	flag.String("stripeWebhookSecret", "", "Stripe Webhook secret")
	flag.String("participantNo", "", "Participant number to update")
	flag.String("bundleId", "", "Bundle ID to associate with the participant")
	flag.String("userEmail", "", "User email to verify")
	flag.String("userPhone", "", "User phone to verify")

	// parse flags
	flag.Parse()

	// initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		panic(err)
	}
	viper.AutomaticEnv()

	// read the configuration
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")

	// stripe vars
	stripeApiSecret := viper.GetString("stripeApiSecret")
	stripeWebhookSecret := viper.GetString("stripeWebhookSecret")
	vocdoniSecret := viper.GetString("secret")

	log.Init("debug", "stdout", os.Stderr)

	// create Stripe client to get available plans
	var stripeClient *stripe.StripeClient
	if stripeApiSecret != "" || stripeWebhookSecret != "" {
		stripeClient = stripe.New(stripeApiSecret, stripeWebhookSecret)
	} else {
		log.Fatalf("stripeApiSecret and stripeWebhookSecret are required")
	}

	availablePlans, err := stripeClient.GetPlans()
	if err != nil || len(availablePlans) == 0 {
		log.Fatalf("could not get the available plans: %v", err)
	}

	// initialize the MongoDB database
	database, err := db.New(mongoURL, mongoDB, availablePlans)
	if err != nil {
		log.Fatalf("could not create the MongoDB database: %v", err)
	}
	defer database.Close()

	// Get the new parameters
	participantNo := viper.GetString("participantNo")
	bundleId := viper.GetString("bundleId")
	userEmail := viper.GetString("userEmail")
	userPhone := viper.GetString("userPhone")

	// Check if the required parameters are provided
	if participantNo == "" || bundleId == "" {
		log.Fatalf("participantNo and bundleId are required")
	}

	if userEmail == "" && userPhone == "" {
		log.Fatalf("at least one of userEmail or userPhone is required")
	}

	log.Infow("database initialized successfully",
		"mongoURL", mongoURL,
		"mongoDB", mongoDB)
	log.Infow("Adding pariticipant with info",
		"bundleId", bundleId,
		"participantNo", participantNo,
		"userEmail", userEmail,
		"userPhone", userPhone)

	// 1. Prepare the bundleID and userID
	bundleIDBytes := internal.HexBytes{}
	if err := bundleIDBytes.ParseString(bundleId); err != nil {
		log.Fatalf("invalid bundleId format: %v", err)
	}
	userID := internal.HexBytes(participantNo)

	log.Infow("calculated userID", "userID", userID.String())

	// 2. Access the twofactor db and retrieve the user data with that userID
	// Initialize the CSP storage using the database client from the db package
	cspStorage := new(storage.MongoStorage)
	if err := cspStorage.Init(&storage.MongoConfig{
		Client: database.DBClient,
		DBName: "saas-lts-csp",
	}); err != nil {
		log.Fatalf("cannot initialize CSP storage: %v", err)
	}

	// Get the organization address from the bundle
	var orgAddress string
	bundle, err := database.ProcessBundle(bundleIDBytes)
	if err != nil {
		log.Fatalf("failed to get bundle: %v", err)
	}
	orgAddress = bundle.OrgAddress

	if orgAddress == "" {
		log.Fatalf("could not determine organization address")
	}
	// check that the user does not already exist in the org and the bundle
	// Update the corresponding orgParticipant entry
	orgParticipant, err := database.OrgParticipantByNo(orgAddress, participantNo)
	// check that err string does not have a substring "no documents in result"
	if err == nil || (err != nil && !strings.Contains(err.Error(), "no documents in result")) {
		// Participant already exists, compare the given data
		log.Warnf("participant already exists in the organization, comparing the data...")
		// Compare the given email
		if orgParticipant.HashedEmail != nil {
			emailMatches := verifyHashedEmail(orgAddress, userEmail, orgParticipant.HashedEmail)
			log.Infow("email verification result",
				"email", userEmail,
				"matches", emailMatches)
			if !emailMatches {
				log.Warnf("email verification failed")
			}
		} else {
			log.Infow("email verification result",
				"email", userEmail,
				"matches", false,
				"reason", "user has no email")
		}

		if orgParticipant.HashedPhone != nil {
			phoneMatches := verifyHashedPhone(orgAddress, userPhone, orgParticipant.HashedPhone)
			log.Infow("phone verification result",
				"phone", userPhone,
				"matches", phoneMatches)
			if !phoneMatches {
				log.Warnf("phone verification failed")
			}
		} else {
			log.Infow("phone verification result",
				"phone", userPhone,
				"matches", false,
				"reason", "user has no phone")
		}

		// Check if the user also exists in the CSP
		userData, err := cspStorage.User(userID)
		if err != nil {
			log.Errorf("User exists in the organization but not in the csp: %v", err)
			userData, err := csp.NewUserForBundle(userID, bundleIDBytes, bundle.Processes...)
			if err != nil {
				log.Fatalf("could not create user for bundle: %v", err)
			}
			if err := cspStorage.SetUser(userData); err != nil {
				log.Fatalf("could not set user in the csp storage: %v", err)
			}
			// verify that the user data where stored correctly
			storedUserData, err := cspStorage.User(userID)
			if err != nil {
				log.Fatalf("could not get user data: %v", err)
			}
			cspBundle, ok := storedUserData.Bundles[bundleId]
			if !ok {
				log.Fatalf("bundle not found in user data")
			}
			if cspBundle.Processes == nil {
				log.Fatalf("bundle data was not stored correctly")
			}
			log.Infof("Added the following process for the user %s in the CSP", userID.String())
			for processID, process := range cspBundle.Processes {
				log.Infow("process data", "processID", processID, "at", process.At)
			}
			log.Infow("user added to the CSP successfully")
			return
			// TODO add a check for the user to the CSP
		}

		// 3. Return the user info and verify email/phone if provided
		log.Infow("user data retrieved successfully",
			"userID", userID.String(),
			"extraData", userData.ExtraData)

		// Print the processes data
		for bundleID, bundle := range userData.Bundles {
			for processID, process := range bundle.Processes {
				log.Infow("process data",
					"bundleID", bundleID,
					"processID", processID,
					"consumed", process.Consumed,
					"withToken", process.WithToken.String(),
					"at", process.At)
			}
		}
		return
	}

	// 4. Add the user to the organization
	// Create a new orgParticipant entry
	newOrgParticipant := db.OrgParticipant{
		ID:            primitive.NilObjectID,
		OrgAddress:    orgAddress,
		ParticipantNo: participantNo,
		Email:         userEmail,
		Phone:         userPhone,
	}

	// add the org participants to the census in the database
	progressChan, err := database.SetBulkCensusMembership(
		vocdoniSecret,
		bundle.Census.ID.Hex(),
		[]db.OrgParticipant{newOrgParticipant},
	)
	if err != nil {
		log.Fatalf("could not add participants to census: %v", err)
	}

	// Wait for the channel to be closed (100% completion)
	var lastProgress *db.BulkCensusMembershipStatus
	for p := range progressChan {
		lastProgress = p
		// Just drain the channel until it's closed
		log.Debugw("census add participants",
			"census", bundle.Census.ID.Hex(),
			"org", orgAddress,
			"progress", p.Progress,
			"added", p.Added,
			"total", p.Total)
	}
	log.Infof("added %d participants to census", lastProgress.Added)

	// Validate that the new entry was saved correctly
	orgParticipant, err = database.OrgParticipantByNo(orgAddress, participantNo)
	if err != nil {
		log.Fatalf("could not find orgParticipant: %v", err)
	}
	if userEmail != "" && !verifyHashedEmail(orgAddress, userEmail, orgParticipant.HashedEmail) {
		log.Fatalf("failed to add correctly  orgParticipant, could not verify email")
	}
	if userPhone != "" && !verifyHashedPhone(orgAddress, userPhone, orgParticipant.HashedPhone) {
		log.Fatalf("failed to add correctly  orgParticipant, could not verify phone")
	}

	log.Infow("Verified that user was added correctly to the origanization")

	// 5. Add the user to the CSP
	// Create user data and store them
	userData, err := csp.NewUserForBundle(userID, bundleIDBytes, bundle.Processes...)
	if err != nil {
		log.Fatalf("could not create user for bundle: %v", err)
	}
	if err := cspStorage.SetUser(userData); err != nil {
		log.Fatalf("could not set user in the csp storage: %v", err)
	}
	// verify that the user data where stored correctly
	storedUserData, err := cspStorage.User(userID)
	if err != nil {
		log.Fatalf("could not get user data: %v", err)
	}
	cspBundle, ok := storedUserData.Bundles[bundleId]
	if !ok {
		log.Fatalf("bundle not found in user data")
	}
	if cspBundle.Processes == nil {
		log.Fatalf("bundle data was not stored correctly")
	}
	log.Infof("Added the following process for the user %s in the CSP", userID.String())
	for processID, process := range cspBundle.Processes {
		log.Infow("process data", "processID", processID, "at", process.At)
	}
	log.Infow("user added to the CSP successfully")
}

func verifyHashedPhone(orgAddress, phone string, hashedPhone []byte) bool {
	sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(phone)
	if err != nil {
		log.Errorf("invalid phone number: %v", err)
		return false
	}

	inHashedPhone := internal.HashOrgData(orgAddress, sanitizedPhone)

	return bytes.Equal(inHashedPhone, hashedPhone)
}

func verifyHashedEmail(orgAddress, email string, hashedEmail []byte) bool {
	if !internal.ValidEmail(email) {
		log.Errorf("invalid email format: %s", email)
		return false
	}

	inHashedEmail := internal.HashOrgData(orgAddress, email)

	return bytes.Equal(inHashedEmail, hashedEmail)
}
