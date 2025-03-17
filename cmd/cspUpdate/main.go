package main

import (
	"bytes"
	"os"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/stripe"
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
	flag.String("newPhone", "", "New phone number to update")
	flag.String("newEmail", "", "New email to update")

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
	newPhone := viper.GetString("newPhone")
	newEmail := viper.GetString("newEmail")

	// Check if the required parameters are provided
	if participantNo == "" || bundleId == "" {
		log.Fatalf("participantNo and bundleId are required")
	}

	log.Infow("database initialized successfully",
		"mongoURL", mongoURL,
		"mongoDB", mongoDB)
	log.Infow("updating participant with info",
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

	// Get the user data
	userData, err := cspStorage.User(userID)
	if err != nil {
		log.Fatalf("cannot get user data: %v", err)
	}

	// 3. Return the user info and verify email/phone if provided
	log.Infow("user data retrieved successfully",
		"userID", userID.String(),
		"extraData", userData.ExtraData)

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
	// Get the org participant data
	orgParticipant, err := database.OrgParticipantByNo(orgAddress, participantNo)
	if err != nil {
		log.Fatalf("could not find orgParticipant: %v", err)
	}

	// Verify email if provided
	if userEmail != "" {
		if !internal.ValidEmail(userEmail) {
			log.Fatalf("invalid email format: %s", userEmail)
		}

		hashedEmail := internal.HashOrgData(orgAddress, userEmail)

		if orgParticipant.HashedEmail != nil {
			emailMatches := bytes.Equal(hashedEmail, []byte(orgParticipant.HashedEmail))
			log.Infow("email verification result",
				"email", userEmail,
				"matches", emailMatches)
			if !emailMatches {
				log.Fatalf("email verification failed")
			}
		} else {
			log.Infow("email verification result",
				"email", userEmail,
				"matches", false,
				"reason", "user has no email")
		}
	}

	// Verify phone if provided
	if userPhone != "" {
		sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(userPhone)
		if err != nil {
			log.Fatalf("invalid phone number: %v", err)
		}

		hashedPhone := internal.HashOrgData(orgAddress, sanitizedPhone)

		if orgParticipant.HashedPhone != nil {
			phoneMatches := bytes.Equal(hashedPhone, []byte(orgParticipant.HashedPhone))
			log.Infow("phone verification result",
				"phone", sanitizedPhone,
				"matches", phoneMatches)
			if !phoneMatches {
				log.Fatalf("phone verification failed")
			}
		} else {
			log.Infow("phone verification result",
				"phone", sanitizedPhone,
				"matches", false,
				"reason", "user has no phone")
		}
	}

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

	// Update phone if requested
	if newPhone != "" {

		// Sanitize and hash the new phone number
		sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(newPhone)
		if err != nil {
			log.Fatalf("invalid new phone number: %v", err)
		}

		hashedPhone := internal.HashOrgData(orgAddress, sanitizedPhone)

		// Update the corresponding orgParticipant entry
		orgParticipant, err := database.OrgParticipantByNo(orgAddress, participantNo)
		if err != nil {
			log.Warnf("could not find orgParticipant: %v", err)
		} else {
			// Update the hashedPhone field
			orgParticipant.HashedPhone = hashedPhone
			orgParticipant.Phone = "" // Clear the phone field for security
			orgParticipant.UpdatedAt = time.Now()

			// Save the updated orgParticipant
			_, err = database.SetOrgParticipant("", orgParticipant)
			if err != nil {
				log.Warnf("failed to update orgParticipant phone: %v", err)
			} else {
				log.Infow("orgParticipant phone updated successfully",
					"participantNo", participantNo,
					"orgAddress", orgAddress)
			}
		}

		// Validate that the new entry was saved correctly
		orgParticipant, err = database.OrgParticipantByNo(orgAddress, participantNo)
		if err != nil {
			log.Fatalf("could not find orgParticipant: %v", err)
		}
		if !bytes.Equal(hashedPhone, []byte(orgParticipant.HashedPhone)) {
			log.Fatalf("failed to update orgParticipant phone")
		}

		log.Infow("user phone updated and verified successfully",
			"userID", userID.String(),
			"newPhone", sanitizedPhone)
	}

	// Update email if requested
	if newEmail != "" {

		// Validate and hash the new email
		if !internal.ValidEmail(newEmail) {
			log.Fatalf("invalid new email format: %s", newEmail)
		}

		hashedEmail := internal.HashOrgData(orgAddress, newEmail)

		// Update the corresponding orgParticipant entry
		orgParticipant, err := database.OrgParticipantByNo(orgAddress, participantNo)
		if err != nil {
			log.Warnf("could not find orgParticipant: %v", err)
		} else {
			// Update the hashedEmail field
			orgParticipant.HashedEmail = hashedEmail
			orgParticipant.Email = "" // Clear the email field for security
			orgParticipant.UpdatedAt = time.Now()

			// Save the updated orgParticipant
			_, err = database.SetOrgParticipant("", orgParticipant)
			if err != nil {
				log.Warnf("failed to update orgParticipant email: %v", err)
			} else {
				log.Infow("orgParticipant email updated successfully",
					"participantNo", participantNo,
					"orgAddress", orgAddress)
			}
		}

		// Validate that the new entry was saved correctly
		orgParticipant, err = database.OrgParticipantByNo(orgAddress, participantNo)
		if err != nil {
			log.Fatalf("could not find orgParticipant: %v", err)
		}
		if !bytes.Equal(hashedEmail, []byte(orgParticipant.HashedEmail)) {
			log.Fatalf("failed to update orgParticipant email")
		}

		log.Infow("user email updated and verified successfully",
			"userID", userID.String(),
			"newEmail", newEmail)
	}
}
