package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

// DatabaseInfo contains filtered database information for an organization
type DatabaseInfo struct {
	Website   string    `json:"website"`
	Type      string    `json:"type"`
	Creator   string    `json:"creator"`
	FirstName string    `json:"firstName,omitempty"`
	LastName  string    `json:"lastName,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	Country   string    `json:"country"`
	Active    bool      `json:"active"`
	Size      string    `json:"size"`
}

// VochainInfo contains filtered vochain information for an organization
type VochainInfo struct {
	Name string `json:"name,omitempty"`
}

// OrganizationBI represents the business intelligence data for an organization
type OrganizationBI struct {
	Address      string        `json:"address"`
	DatabaseInfo *DatabaseInfo `json:"database_info,omitempty"`
	VochainInfo  *VochainInfo  `json:"vochain_info,omitempty"`
	Error        string        `json:"error,omitempty"`
}

// Summary provides overall statistics
type Summary struct {
	TotalOrganizations int `json:"total_organizations"`
	FoundInBoth        int `json:"found_in_both"`
	DatabaseOnly       int `json:"database_only"`
	VochainOnly        int `json:"vochain_only"`
	Errors             int `json:"errors"`
}

// BIReport is the final JSON output structure
type BIReport struct {
	Organizations []OrganizationBI `json:"organizations"`
	Summary       Summary          `json:"summary"`
	GeneratedAt   time.Time        `json:"generated_at"`
}

func main() {
	// Define flags similar to cmd/service/main.go
	flag.StringP("vocdoniApi", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "", "The name of the MongoDB database")
	flag.StringP("output", "o", "bi.json", "Output file path for the BI report")
	flag.BoolP("verbose", "V", false, "Enable verbose logging")

	// Parse flags
	flag.Parse()

	// Initialize Viper
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		fmt.Fprintf(os.Stderr, "Error binding flags: %v\n", err)
		os.Exit(1)
	}
	viper.AutomaticEnv()

	// Read configuration
	apiEndpoint := viper.GetString("vocdoniApi")
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	outputFile := viper.GetString("output")
	verbose := viper.GetBool("verbose")

	// Initialize logger
	logLevel := "info"
	if verbose {
		logLevel = "debug"
	}
	log.Init(logLevel, "stdout", os.Stderr)

	// Validate required parameters
	if mongoURL == "" {
		fmt.Fprintf(os.Stderr, "Error: mongoURL is required\n")
		os.Exit(1)
	}
	if mongoDB == "" {
		fmt.Fprintf(os.Stderr, "Error: mongoDB is required\n")
		os.Exit(1)
	}

	log.Infow("Starting BI extraction", "mongoURL", mongoURL, "mongoDB", mongoDB, "vocdoniApi", apiEndpoint)

	// Initialize MongoDB database
	database, err := db.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("Could not create MongoDB database connection: %v", err)
	}
	defer database.Close()

	// Create Vocdoni API client
	apiClient, err := apiclient.New(apiEndpoint)
	if err != nil {
		log.Fatalf("Could not create Vocdoni API client: %v", err)
	}
	log.Infow("Connected to Vocdoni API", "endpoint", apiEndpoint, "chainID", apiClient.ChainID())

	// Generate BI report
	report, err := generateBIReport(database, mongoDB, apiClient)
	if err != nil {
		log.Fatalf("Failed to generate BI report: %v", err)
	}

	// Output JSON report to file
	jsonData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("Failed to marshal JSON: %v", err)
	}

	// Write to output file
	err = os.WriteFile(outputFile, jsonData, 0o644)
	if err != nil {
		log.Fatalf("Failed to write report to file %s: %v", outputFile, err)
	}

	log.Infow("BI report generated successfully",
		"output_file", outputFile,
		"total", report.Summary.TotalOrganizations,
		"found_in_both", report.Summary.FoundInBoth,
		"db_only", report.Summary.DatabaseOnly,
		"errors", report.Summary.Errors)

	fmt.Printf("BI report saved to: %s\n", outputFile)
}

func generateBIReport(database *db.MongoStorage, dbName string, apiClient *apiclient.HTTPclient) (*BIReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Query all organizations from database
	log.Infow("Querying organizations from database")

	dbOrganizations, err := getAllOrganizations(database, dbName, ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all organizations: %w", err)
	}

	var orgBIList []OrganizationBI
	var summary Summary

	// Process each organization
	for _, dbOrg := range dbOrganizations {
		// Filter out organizations where creator email includes @vocdoni.org
		if strings.Contains(dbOrg.Creator, "@vocdoni.org") {
			log.Debugw("Skipping vocdoni.org organization", "address", dbOrg.Address.Hex(), "creator", dbOrg.Creator)
			continue
		}

		orgBI := OrganizationBI{
			Address: dbOrg.Address.Hex(),
			DatabaseInfo: &DatabaseInfo{
				Website:   dbOrg.Website,
				Type:      string(dbOrg.Type),
				Creator:   dbOrg.Creator,
				CreatedAt: dbOrg.CreatedAt,
				Country:   dbOrg.Country,
				Active:    dbOrg.Active,
				Size:      dbOrg.Size,
			},
		}

		summary.TotalOrganizations++

		// Add user infomration
		firstname, lastname, err := getCreatorNameAndLastNAme(database, dbName, dbOrg.Creator, ctx)
		if err != nil {
			log.Debugw("Failed to get creator user info", "email", dbOrg.Creator, "error", err)
		}

		orgBI.DatabaseInfo.FirstName = firstname
		orgBI.DatabaseInfo.LastName = lastname

		// Try to get vochain account information
		log.Debugw("Querying vochain for organization", "address", dbOrg.Address.Hex())
		vochainAccount, err := apiClient.Account(dbOrg.Address.Hex())
		if err != nil {
			log.Debugw("Failed to get vochain account", "address", dbOrg.Address.Hex(), "error", err)
			orgBI.Error = fmt.Sprintf("Vochain error: %v", err)
			summary.DatabaseOnly++
			summary.Errors++
		} else {
			log.Debugw("Found vochain account", "address", dbOrg.Address.Hex(), "nonce", vochainAccount.Nonce)

			// For now, just create basic vochain info
			// Metadata extraction would require additional API calls or parsing
			orgBI.VochainInfo = &VochainInfo{
				Name: vochainAccount.Metadata.Name["default"], // Will be empty until we implement proper metadata fetching
			}
			summary.FoundInBoth++
		}

		orgBIList = append(orgBIList, orgBI)
	}

	report := &BIReport{
		Organizations: orgBIList,
		Summary:       summary,
		GeneratedAt:   time.Now(),
	}

	return report, nil
}

// getAllOrganizations retrieves all organizations from the database
func getAllOrganizations(database *db.MongoStorage, dbName string, ctx context.Context) ([]db.Organization, error) {
	// Query the organizations collection
	organizationsCollection := database.DBClient.Database(dbName).Collection("organizations")
	cursor, err := organizationsCollection.Find(ctx, bson.D{{}})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var organizations []db.Organization
	for cursor.Next(ctx) {
		var org db.Organization
		if err := cursor.Decode(&org); err != nil {
			continue // Skip invalid documents
		}
		organizations = append(organizations, org)
	}

	return organizations, cursor.Err()
}

// getCreatorNameAndLastNAme retrieves the creator's name and last name from the user entry
func getCreatorNameAndLastNAme(database *db.MongoStorage, dbName, creatorEmail string, ctx context.Context) (string, string, error) {
	usersCollection := database.DBClient.Database(dbName).Collection("users")
	var user db.User
	err := usersCollection.FindOne(ctx, bson.M{"email": creatorEmail}).Decode(&user)
	if err != nil {
		return "", "", err
	}
	return user.FirstName, user.LastName, nil
}
