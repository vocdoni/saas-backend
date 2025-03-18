package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/csp/storage"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

// Participant represents a single entry from the CSV file
type Participant struct {
	ParticipantNo string
	BundleId      string
	UserEmail     string
	UserPhone     string
	NewEmail      string
	NewPhone      string
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
	flag.String("csvFile", "", "Path to CSV file containing participant data to update")
	// Keep the original flags for backward compatibility or single-entry updates
	flag.String("participantNo", "", "Participant number to update (ignored if csvFile is provided)")
	flag.String("bundleId", "", "Bundle ID to associate with the participant (ignored if csvFile is provided)")
	flag.String("userEmail", "", "User email to verify (ignored if csvFile is provided)")
	flag.String("userPhone", "", "User phone to verify (ignored if csvFile is provided)")
	flag.String("newPhone", "", "New phone number to update (ignored if csvFile is provided)")
	flag.String("newEmail", "", "New email to update (ignored if csvFile is provided)")

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
			err := processParticipant(database, cspStorage, participant)
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
		newPhone := viper.GetString("newPhone")
		newEmail := viper.GetString("newEmail")

		// Check if the required parameters are provided
		if participantNo == "" || bundleId == "" {
			log.Fatalf("participantNo and bundleId are required when not using a CSV file")
		}

		participant := Participant{
			ParticipantNo: participantNo,
			BundleId:      bundleId,
			UserEmail:     userEmail,
			UserPhone:     userPhone,
			NewEmail:      newEmail,
			NewPhone:      newPhone,
		}

		log.Infow("updating participant with info",
			"bundleId", bundleId,
			"participantNo", participantNo,
			"userEmail", userEmail,
			"userPhone", userPhone)

		err := processParticipant(database, cspStorage, participant)
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
			log.Warnf("failed to close CSV file: %v", err)
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
		"newemail":      -1,
		"newphone":      -1,
	}

	// Map header names to column indices
	for i, colName := range header {
		// Handle variations in column names
		switch {
		case strings.Contains(colName, "participant") || strings.Contains(colName, "número") || colName == "no":
			colIndices["participantno"] = i
		case strings.Contains(colName, "bundle"):
			colIndices["bundleid"] = i
		case strings.Contains(colName, "useremail") ||
			strings.Contains(colName, "oldemail") ||
			strings.Contains(colName, "currentemail"):
			colIndices["useremail"] = i
		case strings.Contains(colName, "userphone") ||
			strings.Contains(colName, "oldphone") ||
			strings.Contains(colName, "currentphone"):
			colIndices["userphone"] = i
		case strings.Contains(colName, "newemail") || colName == "email":
			colIndices["newemail"] = i
		case strings.Contains(colName, "newphone") || colName == "phone":
			colIndices["newphone"] = i
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
		if colIndices["newemail"] >= 0 && colIndices["newemail"] < len(record) {
			participant.NewEmail = strings.TrimSpace(record[colIndices["newemail"]])
		}
		if colIndices["newphone"] >= 0 && colIndices["newphone"] < len(record) {
			participant.NewPhone = strings.TrimSpace(record[colIndices["newphone"]])
		}

		// Skip rows without required fields
		if participant.ParticipantNo == "" || participant.BundleId == "" {
			log.Warnf("Skipping row with missing required fields: %v", record)
			continue
		}

		participants = append(participants, participant)
	}

	return participants, nil
}

// processParticipant handles the update process for a single participant
func processParticipant(database *db.MongoStorage, cspStorage *storage.MongoStorage, participant Participant) error {
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

	// Get the user data
	userData, err := cspStorage.User(userID)
	if err != nil {
		log.Warnf("cannot get user data: %v", err)
		// Continue with the process even if the user doesn't exist in CSP
		// as we might just be updating the org participant data
	} else {
		// Print user data if found
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
	}

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

	// Get the org participant data
	orgParticipant, err := database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
	if err != nil {
		return fmt.Errorf("could not find orgParticipant: %v", err)
	}

	// Verify email if provided
	if participant.UserEmail != "" {
		if !internal.ValidEmail(participant.UserEmail) {
			return fmt.Errorf("invalid email format: %s", participant.UserEmail)
		}

		hashedEmail := internal.HashOrgData(orgAddress, participant.UserEmail)

		if orgParticipant.HashedEmail != nil {
			emailMatches := bytes.Equal(hashedEmail, []byte(orgParticipant.HashedEmail))
			log.Infow("email verification result",
				"email", participant.UserEmail,
				"matches", emailMatches)
			if !emailMatches {
				return fmt.Errorf("email verification failed")
			}
		} else {
			log.Infow("email verification result",
				"email", participant.UserEmail,
				"matches", false,
				"reason", "user has no email")
		}
	}

	// Verify phone if provided
	if participant.UserPhone != "" {
		sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(participant.UserPhone)
		if err != nil {
			return fmt.Errorf("invalid phone number: %v", err)
		}

		hashedPhone := internal.HashOrgData(orgAddress, sanitizedPhone)

		if orgParticipant.HashedPhone != nil {
			phoneMatches := bytes.Equal(hashedPhone, []byte(orgParticipant.HashedPhone))
			log.Infow("phone verification result",
				"phone", sanitizedPhone,
				"matches", phoneMatches)
			if !phoneMatches {
				return fmt.Errorf("phone verification failed")
			}
		} else {
			log.Infow("phone verification result",
				"phone", sanitizedPhone,
				"matches", false,
				"reason", "user has no phone")
		}
	}

	// Update phone if requested
	if participant.NewPhone != "" {
		// Sanitize and hash the new phone number
		sanitizedPhone, err := internal.SanitizeAndVerifyPhoneNumber(participant.NewPhone)
		if err != nil {
			return fmt.Errorf("invalid new phone number: %v", err)
		}

		hashedPhone := internal.HashOrgData(orgAddress, sanitizedPhone)

		// Update the corresponding orgParticipant entry
		orgParticipant, err := database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
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
					"participantNo", participant.ParticipantNo,
					"orgAddress", orgAddress)
			}
		}

		// Validate that the new entry was saved correctly
		orgParticipant, err = database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
		if err != nil {
			return fmt.Errorf("could not find orgParticipant after update: %v", err)
		}
		if !bytes.Equal(hashedPhone, []byte(orgParticipant.HashedPhone)) {
			return fmt.Errorf("failed to update orgParticipant phone")
		}

		log.Infow("user phone updated and verified successfully",
			"userID", userID.String(),
			"newPhone", sanitizedPhone)
	}

	// Update email if requested
	if participant.NewEmail != "" {
		// Validate and hash the new email
		if !internal.ValidEmail(participant.NewEmail) {
			return fmt.Errorf("invalid new email format: %s", participant.NewEmail)
		}

		hashedEmail := internal.HashOrgData(orgAddress, participant.NewEmail)

		// Update the corresponding orgParticipant entry
		orgParticipant, err := database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
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
					"participantNo", participant.ParticipantNo,
					"orgAddress", orgAddress)
			}
		}

		// Validate that the new entry was saved correctly
		orgParticipant, err = database.OrgParticipantByNo(orgAddress, participant.ParticipantNo)
		if err != nil {
			return fmt.Errorf("could not find orgParticipant after update: %v", err)
		}
		if !bytes.Equal(hashedEmail, []byte(orgParticipant.HashedEmail)) {
			return fmt.Errorf("failed to update orgParticipant email")
		}

		log.Infow("user email updated and verified successfully",
			"userID", userID.String(),
			"newEmail", participant.NewEmail)
	}

	return nil
}
