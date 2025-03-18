package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
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

// Participant represents a single entry from the CSV file
type Participant struct {
	ParticipantNo string
	BundleId      string
	UserEmail     string
	UserPhone     string
}

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
	flag.String("csvFile", "", "Path to CSV file containing participant data to add")
	// Keep the original flags for backward compatibility or single-entry updates
	flag.String("participantNo", "", "Participant number to add (ignored if csvFile is provided)")
	flag.String("bundleId", "", "Bundle ID to associate with the participant (ignored if csvFile is provided)")
	flag.String("userEmail", "", "User email (ignored if csvFile is provided)")
	flag.String("userPhone", "", "User phone (ignored if csvFile is provided)")

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

	// Initialize the CSP storage using the database client from the db package
	cspStorage := new(storage.MongoStorage)
	if err := cspStorage.Init(&storage.MongoConfig{
		Client: database.DBClient,
		DBName: fmt.Sprintf("%s-csp", mongoDB),
	}); err != nil {
		log.Fatalf("cannot initialize CSP storage: %v", err)
	}

	log.Infow("database initialized successfully",
		"mongoURL", mongoURL,
		"mongoDB", mongoDB)

	// Check if we're using a CSV file or command-line arguments
	csvFilePath := viper.GetString("csvFile")
	if csvFilePath != "" {
		// Process participants from CSV file
		participants, err := readParticipantsFromCSV(csvFilePath)
		if err != nil {
			log.Fatalf("failed to read participants from CSV: %v", err)
		}

		log.Infof("Processing %d participants from CSV file", len(participants))

		for i, participant := range participants {
			log.Infof("Processing participant %d/%d: %s", i+1, len(participants), participant.ParticipantNo)
			err := processParticipant(database, cspStorage, vocdoniSecret, participant)
			if err != nil {
				log.Errorf("Failed to process participant %s: %v", participant.ParticipantNo, err)
				// Continue with the next participant
				continue
			}
		}

		log.Infof("Finished processing all participants from CSV file")
	} else {
		// Process a single participant from command-line arguments
		participantNo := viper.GetString("participantNo")
		bundleId := viper.GetString("bundleId")
		userEmail := viper.GetString("userEmail")
		userPhone := viper.GetString("userPhone")

		// Check if the required parameters are provided
		if participantNo == "" || bundleId == "" {
			log.Fatalf("participantNo and bundleId are required when not using a CSV file")
		}

		if userEmail == "" && userPhone == "" {
			log.Fatalf("at least one of userEmail or userPhone is required")
		}

		participant := Participant{
			ParticipantNo: participantNo,
			BundleId:      bundleId,
			UserEmail:     userEmail,
			UserPhone:     userPhone,
		}

		log.Infow("Adding participant with info",
			"bundleId", bundleId,
			"participantNo", participantNo,
			"userEmail", userEmail,
			"userPhone", userPhone)

		err := processParticipant(database, cspStorage, vocdoniSecret, participant)
		if err != nil {
			log.Fatalf("Failed to process participant: %v", err)
		}
	}
}

// readParticipantsFromCSV reads participant data from a CSV file
func readParticipantsFromCSV(filePath string) ([]Participant, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open CSV file: %w", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Errorf("failed to close CSV file: %v", err)
		}
	}()

	reader := csv.NewReader(file)

	// Allow flexible number of fields per record
	reader.FieldsPerRecord = -1

	// Read the header row to determine column positions
	header, err := reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read CSV header: %w", err)
	}

	// Trim spaces and convert to lowercase for case-insensitive matching
	for i := range header {
		header[i] = strings.TrimSpace(strings.ToLower(header[i]))
	}

	// Find column indices
	colIndices := map[string]int{
		"participantno": -1,
		"bundleid":      -1,
		"useremail":     -1,
		"userphone":     -1,
	}

	// Map header names to column indices
	for i, colName := range header {
		// Handle variations in column names
		switch {
		case strings.Contains(colName, "participant") || strings.Contains(colName, "número") || colName == "no":
			colIndices["participantno"] = i
		case strings.Contains(colName, "bundle"):
			colIndices["bundleid"] = i
		case strings.Contains(colName, "email"):
			colIndices["useremail"] = i
		case strings.Contains(colName, "phone"):
			colIndices["userphone"] = i
		}
	}

	// Check if required columns are present
	if colIndices["participantno"] == -1 || colIndices["bundleid"] == -1 {
		return nil, fmt.Errorf("CSV must contain at least 'participantNo' and 'bundleId' columns")
	}

	var participants []Participant

	// Read and process each row
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading CSV record: %w", err)
		}

		// Skip empty rows
		if len(record) == 0 {
			continue
		}

		// Skip comment rows (starting with #)
		if len(record[0]) > 0 && record[0][0] == '#' {
			continue
		}

		participant := Participant{}

		// Extract values using the determined column indices
		if colIndices["participantno"] >= 0 && colIndices["participantno"] < len(record) {
			participant.ParticipantNo = strings.TrimSpace(record[colIndices["participantno"]])
		}
		if colIndices["bundleid"] >= 0 && colIndices["bundleid"] < len(record) {
			participant.BundleId = strings.TrimSpace(record[colIndices["bundleid"]])
		}
		if colIndices["useremail"] >= 0 && colIndices["useremail"] < len(record) {
			participant.UserEmail = strings.TrimSpace(record[colIndices["useremail"]])
		}
		if colIndices["userphone"] >= 0 && colIndices["userphone"] < len(record) {
			participant.UserPhone = strings.TrimSpace(record[colIndices["userphone"]])
		}

		// Skip rows without required fields
		if participant.ParticipantNo == "" || participant.BundleId == "" {
			log.Warnf("Skipping row with missing required fields: %v", record)
			continue
		}

		// Ensure at least one of email or phone is provided
		if participant.UserEmail == "" && participant.UserPhone == "" {
			log.Warnf("Skipping row without email or phone: %v", record)
			continue
		}

		participants = append(participants, participant)
	}

	return participants, nil
}

// processParticipant handles the add process for a single participant
func processParticipant(
	database *db.MongoStorage,
	cspStorage *storage.MongoStorage,
	vocdoniSecret string,
	participant Participant,
) error {
	log.Infow("processing participant",
		"bundleId", participant.BundleId,
		"participantNo", participant.ParticipantNo,
		"userEmail", participant.UserEmail,
		"userPhone", participant.UserPhone)

	// 1. Prepare the bundleID and userID
	bundleIDBytes := internal.HexBytes{}
	if err := bundleIDBytes.ParseString(participant.BundleId); err != nil {
		return fmt.Errorf("invalid bundleId format: %v", err)
	}
	userID := internal.HexBytes(participant.ParticipantNo)

	log.Infow("calculated userID", "userID", userID.String())

	// Get the organization address from the bundle
	var orgAddress string
	bundle, err := database.ProcessBundle(bundleIDBytes)
	if err != nil {
		return fmt.Errorf("failed to get bundle: %v", err)
	}
	orgAddress = bundle.OrgAddress

	if orgAddress == "" {
		return fmt.Errorf("could not determine organization address")
	}

	// check that the user does not already exist in the org and the bundle
	orgParticipant, err := database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
	// check that err string does not have a substring "no documents in result"
	if err == nil || (err != nil && !strings.Contains(err.Error(), "no documents in result")) {
		// Participant already exists, compare the given data
		log.Warnf("participant already exists in the organization, comparing the data...")
		// Compare the given email
		if orgParticipant.HashedEmail != nil {
			emailMatches := verifyHashedEmail(orgAddress, participant.UserEmail, orgParticipant.HashedEmail)
			log.Infow("email verification result",
				"email", participant.UserEmail,
				"matches", emailMatches)
			if !emailMatches {
				log.Warnf("email verification failed")
			}
		} else {
			log.Infow("email verification result",
				"email", participant.UserEmail,
				"matches", false,
				"reason", "user has no email")
		}

		if orgParticipant.HashedPhone != nil {
			phoneMatches := verifyHashedPhone(orgAddress, participant.UserPhone, orgParticipant.HashedPhone)
			log.Infow("phone verification result",
				"phone", participant.UserPhone,
				"matches", phoneMatches)
			if !phoneMatches {
				log.Warnf("phone verification failed")
			}
		} else {
			log.Infow("phone verification result",
				"phone", participant.UserPhone,
				"matches", false,
				"reason", "user has no phone")
		}

		// Check if the user also exists in the CSP
		userData, err := cspStorage.User(userID)
		if err != nil {
			log.Errorf("User exists in the organization but not in the csp: %v", err)
			userData, err := csp.NewUserForBundle(userID, bundleIDBytes, bundle.Processes...)
			if err != nil {
				return fmt.Errorf("could not create user for bundle: %v", err)
			}
			if err := cspStorage.SetUser(userData); err != nil {
				return fmt.Errorf("could not set user in the csp storage: %v", err)
			}
			// verify that the user data where stored correctly
			storedUserData, err := cspStorage.User(userID)
			if err != nil {
				return fmt.Errorf("could not get user data: %v", err)
			}
			cspBundle, ok := storedUserData.Bundles[participant.BundleId]
			if !ok {
				return fmt.Errorf("bundle not found in user data")
			}
			if cspBundle.Processes == nil {
				return fmt.Errorf("bundle data was not stored correctly")
			}
			log.Infof("Added the following process for the user %s in the CSP", userID.String())
			for processID, process := range cspBundle.Processes {
				log.Infow("process data", "processID", processID, "at", process.At)
			}
			log.Infow("user added to the CSP successfully")
			return nil
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
		return nil
	}

	// 4. Add the user to the organization
	// Create a new orgParticipant entry
	newOrgParticipant := db.OrgParticipant{
		ID:            primitive.NilObjectID,
		OrgAddress:    orgAddress,
		ParticipantNo: participant.ParticipantNo,
		Email:         participant.UserEmail,
		Phone:         participant.UserPhone,
	}

	// add the org participants to the census in the database
	progressChan, err := database.SetBulkCensusMembership(
		vocdoniSecret,
		bundle.Census.ID.Hex(),
		[]db.OrgParticipant{newOrgParticipant},
	)
	if err != nil {
		return fmt.Errorf("could not add participants to census: %v", err)
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
	orgParticipant, err = database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
	if err != nil {
		return fmt.Errorf("could not find orgParticipant: %v", err)
	}
	if participant.UserEmail != "" && !verifyHashedEmail(orgAddress, participant.UserEmail, orgParticipant.HashedEmail) {
		return fmt.Errorf("failed to add correctly orgParticipant, could not verify email")
	}
	if participant.UserPhone != "" && !verifyHashedPhone(orgAddress, participant.UserPhone, orgParticipant.HashedPhone) {
		return fmt.Errorf("failed to add correctly orgParticipant, could not verify phone")
	}

	log.Infow("Verified that user was added correctly to the organization")

	// 5. Add the user to the CSP
	// Create user data and store them
	userData, err := csp.NewUserForBundle(userID, bundleIDBytes, bundle.Processes...)
	if err != nil {
		return fmt.Errorf("could not create user for bundle: %v", err)
	}
	if err := cspStorage.SetUser(userData); err != nil {
		return fmt.Errorf("could not set user in the csp storage: %v", err)
	}
	// verify that the user data where stored correctly
	storedUserData, err := cspStorage.User(userID)
	if err != nil {
		return fmt.Errorf("could not get user data: %v", err)
	}
	cspBundle, ok := storedUserData.Bundles[participant.BundleId]
	if !ok {
		return fmt.Errorf("bundle not found in user data")
	}
	if cspBundle.Processes == nil {
		return fmt.Errorf("bundle data was not stored correctly")
	}
	log.Infof("Added the following process for the user %s in the CSP", userID.String())
	for processID, process := range cspBundle.Processes {
		log.Infow("process data", "processID", processID, "at", process.At)
	}
	log.Infow("user added to the CSP successfully")

	return nil
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
