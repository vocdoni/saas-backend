// / Package main implements a command-line tool to extract business intelligence data
// / from the Vocdoni SaaS backend database and Vocdoni Vochain API. It generates a JSON
// / report containing organization details and optionally creates leads in Holded CRM.
// / The tool can also send an informative email with the report summary.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultQueryTimeout is the default timeout for database and API queries
	defaultQueryTimeout = 5 * time.Minute
	// vocdoniOrgDomain is the internal organization domain to filter out
	vocdoniOrgDomain = "@vocdoni.org"
)

// databaseInfo contains filtered database information for an organization
type databaseInfo struct {
	Website        string    `json:"website"`
	Type           string    `json:"type"`
	Creator        string    `json:"creator"`
	FirstName      string    `json:"firstName,omitempty"`
	LastName       string    `json:"lastName,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	Country        string    `json:"country"`
	Active         bool      `json:"active"`
	Size           string    `json:"size"`
	Communications bool      `json:"communications_enabled"`
}

// VochainInfo contains filtered vochain information for an organization
type vochainInfo struct {
	Name string `json:"name,omitempty"`
}

// OrganizationBI represents the business intelligence data for an organization
type OrganizationBI struct {
	Address      string        `json:"address"`
	DatabaseInfo *databaseInfo `json:"database_info,omitempty"`
	VochainInfo  *vochainInfo  `json:"vochain_info,omitempty"`
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

// LeadReport struct
type LeadReport struct {
	LeadID      string `json:"leadId"`
	ContactID   string `json:"contactId"`
	Email       string `json:"email"`
	OrgName     string `json:"orgName"`
	ContactName string `json:"contactName"`
	Country     string `json:"country"`
	Type        string `json:"type"`
}

type SMTPConfig struct {
	Server      string
	Port        int
	Auth        smtp.Auth
	FromAddress string
	FromName    string
	ToAddress   string
}

func main() {
	// Define flags similar to cmd/service/main.go
	flag.StringP("vocdoniAPI", "v", "https://api-dev.vocdoni.net/v2", "vocdoni node remote API URL")
	flag.StringP("mongoURL", "m", "", "The URL of the MongoDB server")
	flag.StringP("mongoDB", "d", "", "The name of the MongoDB database")
	flag.StringP("output", "o", "bi.json", "Output file path for the BI report")
	flag.StringP("crmAPIKey", "k", "", "CRM API key")
	flag.StringP("crmURL", "u", "", "CRM API base URL")
	flag.String("smtpServer", "", "SMTP server")
	flag.Int("smtpPort", 587, "SMTP port")
	flag.String("smtpUsername", "", "SMTP username")
	flag.String("smtpPassword", "", "SMTP password")
	flag.String("emailFromAddress", "", "SMTP from address")
	flag.String("smtpToAddress", "manos@vocdoni.org", "SMTP to address for report email")
	flag.IntP("daysBefore", "D", 7, "Number of days before today to include organizations created since then")
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
	vocdoniAPI := viper.GetString("vocdoniAPI")
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	outputFile := viper.GetString("output")
	crmAPIKey := viper.GetString("crmAPIKey")
	crmURL := viper.GetString("crmURL")
	smtpServer := viper.GetString("smtpServer")
	smtpPort := viper.GetInt("smtpPort")
	smtpUsername := viper.GetString("smtpUsername")
	smtpPassword := viper.GetString("smtpPassword")
	smtpFromAddress := viper.GetString("emailFromAddress")
	smtpToAddress := viper.GetString("smtpToAddress")
	daysBefore := viper.GetInt("daysBefore")
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

	log.Infow("Starting BI extraction", "mongoURL", mongoURL, "mongoDB", mongoDB, "vocdoniAPI", vocdoniAPI)

	// Initialize MongoDB database
	storage, err := db.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("Could not create MongoDB database connection: %v", err)
	}
	defer storage.Close()
	database := storage.DBClient.Database(mongoDB)

	// Create Vocdoni API client
	apiClient, err := apiclient.New(vocdoniAPI)
	if err != nil {
		log.Fatalf("Could not create Vocdoni API client: %v", err)
	}
	log.Infow("Connected to Vocdoni API", "endpoint", vocdoniAPI, "chainID", apiClient.ChainID())

	// Initialize SMTP email sender
	smtpEnabled := smtpServer != "" && smtpFromAddress != "" && smtpUsername != "" && smtpPassword != ""
	if !smtpEnabled {
		log.Warn("SMTP server or from address not provided, skipping email sender initialization")
	}
	var smtpConfig *SMTPConfig
	if smtpEnabled {
		smtpAuth := smtp.PlainAuth("", smtpUsername, smtpPassword, smtpServer)
		smtpConfig = &SMTPConfig{
			Server:      smtpServer,
			Port:        smtpPort,
			Auth:        smtpAuth,
			FromAddress: smtpFromAddress,
			FromName:    "Vocdoni BI Report",
			ToAddress:   smtpToAddress,
		}
	}

	// Initialize Holded CRM client if API key is provided
	var crmClient *CRMClient
	var funnelID string
	var stageID string
	if crmAPIKey == "" || crmURL == "" {
		log.Warn("No CRM API key or URL provided, skipping CRM client initialization")
	} else {
		crmClient = NewCRMClient(crmAPIKey, crmURL)
		log.Infow("Initialized CRM client", "baseURL", crmClient.baseURL)
		funnelID, stageID, err = crmClient.GetLeadsFunnelStageID() // Just to verify connectivity
		if err != nil {
			log.Warnw("Failed to retrieve Holded CRM Leads funnel ID", "error", err)
		} else {
			log.Infow("Retrieved Holded CRM Leads funnel ID", "funnelID", funnelID, "stageID", stageID)
		}
	}

	// Generate BI report
	report, err := generateBIReport(database, apiClient, daysBefore)
	if err != nil {
		log.Fatalf("Failed to generate BI report: %v", err)
	}

	log.Infow("BI report generated successfully",
		"output_file", outputFile,
		"total", report.Summary.TotalOrganizations,
		"found_in_both", report.Summary.FoundInBoth,
		"db_only", report.Summary.DatabaseOnly,
		"errors", report.Summary.Errors)

	var leads []LeadReport
	var errors []error
	if crmClient != nil && funnelID != "" && stageID != "" {
		leads, errors = updateCRMWithBIData(crmClient, funnelID, stageID, report)
		log.Infow("Created leads in Holded CRM", "total", len(leads), "errors", len(errors))
	} else {
		log.Warn("Skipping CRM lead creation; CRM client or funnel/stage not configured")
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

	// send email
	if smtpEnabled {
		err = sendInformativeEmail(smtpConfig, leads, errors, daysBefore)
		if err != nil {
			log.Fatalf("Could not send informative email: %v", err)
		}
		log.Infow("Informative email sent successfully", "to", smtpConfig.ToAddress)
	} else {
		log.Warn("Skipping informative email; SMTP not configured")
	}

	// TODO implement periodic execution (daily/weekly) using cron or similar if not available by hosting
}

// updateCRMWithBIData updates the CRM with BI data
func updateCRMWithBIData(crmClient *CRMClient, funnelID, stageID string, report *BIReport) ([]LeadReport, []error) {
	var leads []LeadReport
	var errors []error
	// create the leads and  contacts in Holded CRM for testing
	for _, v := range report.Organizations {
		if v.DatabaseInfo.Creator == "" {
			log.Warnw("Skipping organization with empty creator", "organization", v)
			errors = append(errors, fmt.Errorf("skipping organization with empty creator: %v", v.Address))
			continue
		}
		contactID, exists, err := crmClient.HandleContact(
			v.DatabaseInfo.Creator,
			v.DatabaseInfo.FirstName+" "+v.DatabaseInfo.LastName,
			v.DatabaseInfo.Country,
		)
		if err != nil {
			errors = append(errors, err)
			log.Errorw(err, v.DatabaseInfo.Creator)
			continue
		}
		if exists {
			continue
		}

		customFields := map[string]string{
			"org_type":               v.DatabaseInfo.Type,
			"size":                   v.DatabaseInfo.Size,
			"website":                v.DatabaseInfo.Website,
			"communications_enabled": fmt.Sprintf("%v", v.DatabaseInfo.Communications),
			"created_at":             v.DatabaseInfo.CreatedAt.Format(time.RFC3339),
			"active":                 fmt.Sprintf("%v", v.DatabaseInfo.Active),
			"origin":                 "bi_report",
		}
		orgName := ""
		if v.VochainInfo != nil {
			orgName = v.VochainInfo.Name
		}
		leadID, err := crmClient.HandleLead(contactID, funnelID, stageID, orgName, customFields)
		if err != nil {
			errors = append(errors, err)
			log.Errorw(err, v.DatabaseInfo.Creator)
			continue
		}
		leads = append(leads, LeadReport{
			LeadID:      leadID,
			ContactID:   contactID,
			Email:       v.DatabaseInfo.Creator,
			OrgName:     orgName,
			ContactName: v.DatabaseInfo.FirstName + " " + v.DatabaseInfo.LastName,
			Country:     v.DatabaseInfo.Country,
			Type:        v.DatabaseInfo.Type,
		})
		log.Infof("Created lead %s and contact %s for %s", leadID, contactID, v.DatabaseInfo.Creator)
	}
	return leads, errors
}

// generateBIReport generates the BI report by querying the database and Vocdoni API
func generateBIReport(database *mongo.Database, apiClient *apiclient.HTTPclient, daysBefore int) (*BIReport, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultQueryTimeout)
	defer cancel()

	// Query all organizations from database
	log.Infow("Querying organizations from database")

	dbOrganizations, err := getAllOrganizations(ctx, database)
	if err != nil {
		return nil, fmt.Errorf("failed to get all organizations: %w", err)
	}

	var orgBIList []OrganizationBI
	var summary Summary

	// Process each organization
	for _, dbOrg := range dbOrganizations {
		// Filter out organizations where creator email includes @vocdoni.org
		if strings.Contains(dbOrg.Creator, vocdoniOrgDomain) {
			log.Debugw("Skipping vocdoni.org organization", "address", dbOrg.Address.Hex(), "creator", dbOrg.Creator)
			continue
		}
		// filter out organizations that where created before one week ago
		cutoffTime := time.Now().Add(-time.Duration(daysBefore) * 24 * time.Hour)
		if dbOrg.CreatedAt.Before(cutoffTime) {
			log.Debugw("Skipping recently created organization", "address", dbOrg.Address.Hex(), "createdAt", dbOrg.CreatedAt)
			continue
		}

		firstname, lastname, err := getCreatorNameAndLastName(ctx, database, dbOrg.Creator)
		if err != nil {
			log.Debugw("Failed to get creator user info", "email", dbOrg.Creator, "error", err)
		}

		orgBI := OrganizationBI{
			Address: dbOrg.Address.Hex(),
			DatabaseInfo: &databaseInfo{
				Website:        dbOrg.Website,
				Type:           string(dbOrg.Type),
				Creator:        dbOrg.Creator,
				CreatedAt:      dbOrg.CreatedAt,
				Country:        dbOrg.Country,
				Active:         dbOrg.Active,
				Size:           dbOrg.Size,
				Communications: dbOrg.Communications,
				FirstName:      firstname,
				LastName:       lastname,
			},
		}

		summary.TotalOrganizations++

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
			orgBI.VochainInfo = &vochainInfo{
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
func getAllOrganizations(ctx context.Context, database *mongo.Database) ([]db.Organization, error) {
	// Query the organizations collection
	organizationsCollection := database.Collection("organizations")
	cursor, err := organizationsCollection.Find(ctx, bson.D{{}})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

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

// getCreatorNameAndLastName retrieves the creator's name and last name from the user entry
func getCreatorNameAndLastName(
	ctx context.Context,
	database *mongo.Database,
	creatorEmail string,
) (name string, lastName string, err error) {
	usersCollection := database.Collection("users")
	var user db.User
	if err := usersCollection.FindOne(ctx, bson.M{"email": creatorEmail}).Decode(&user); err != nil {
		return "", "", err
	}
	return user.FirstName, user.LastName, nil
}

func sendInformativeEmail(config *SMTPConfig, leads []LeadReport, errors []error, dayBefore int) error {
	if len(leads) == 0 && len(errors) == 0 {
		log.Infow("No leads or errors to report, skipping email sending")
		return nil
	}

	to, err := mail.ParseAddress(config.ToAddress)
	if err != nil {
		return fmt.Errorf("could not parse to email: %v", err)
	}

	date := time.Now().Format("2006-01-02 15:04:05")
	// compose email body
	subject := date + " Test BI Report Generated"
	var body strings.Builder
	fmt.Fprintf(&body, "The BI report has been generated successfully for the last %d days.\n\n", dayBefore)
	fmt.Fprintf(&body, "Total leads created: %d\n", len(leads))
	fmt.Fprint(&body, "Leads:\n")
	for _, lead := range leads {
		fmt.Fprintf(&body, "- LeadID: %s, ContactID: %s, Email: %s, OrgName: %s, ContactName: %s, Country: %s, Type: %s\n",
			lead.LeadID, lead.ContactID, lead.Email, lead.OrgName, lead.ContactName, lead.Country, lead.Type)
	}
	fmt.Fprint(&body, "\n")
	fmt.Fprintf(&body, "Total errors: %d\n\n", len(errors))
	if len(errors) > 0 {
		fmt.Fprint(&body, "Errors:\n")
		for _, err := range errors {
			fmt.Fprintf(&body, "- %v\n", err)
		}
	}

	// send the email
	server := fmt.Sprintf("%s:%d", config.Server, config.Port)
	msg := []byte("To: " + to.String() + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" +
		body.String() + "\r\n")
	err = smtp.SendMail(server, config.Auth, config.FromAddress, []string{to.Address}, msg)
	if err != nil {
		return fmt.Errorf("could not send email: %v", err)
	}
	return nil
}
