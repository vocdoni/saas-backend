// Package main provides a CLI tool for querying voting process and voter information
// from the database. It supports two modes:
// 1. Process-only mode: displays bundle, census, and CSP statistics for a process
// 2. Process+User mode: displays user-specific participation details
package main

import (
	"encoding/json"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	flag "github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	vocapi "go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
	voctypes "go.vocdoni.io/dvote/types"
	"go.vocdoni.io/dvote/vochain/state"
)

func main() {
	// Define command-line flags
	flag.StringP("processID", "i", "", "Process ID to query (required, hex format)")
	flag.StringP("userID", "u", "", "User ID to query (optional, hex format)")
	flag.StringP("mongoURL", "m", "", "MongoDB connection URL")
	flag.StringP("mongoDB", "d", "", "MongoDB database name")
	flag.StringP("vocdoniAPI", "v", "https://api-dev.vocdoni.net/v2", "Vocdoni node API URL")

	// Parse flags
	flag.Parse()

	// Initialize Viper for environment variable support
	viper.SetEnvPrefix("VOCDONI")
	if err := viper.BindPFlags(flag.CommandLine); err != nil {
		log.Fatalf("could not bind flags: %v", err)
	}
	viper.AutomaticEnv()

	// Read configuration
	processID := viper.GetString("processID")
	userID := viper.GetString("userID")
	mongoURL := viper.GetString("mongoURL")
	mongoDB := viper.GetString("mongoDB")
	apiEndpoint := viper.GetString("vocdoniAPI")
	// Initialize logger
	log.Init("info", "stdout", nil)

	// Validate required parameters
	if processID == "" {
		log.Fatal("processID is required")
	}
	if mongoURL == "" {
		log.Fatal("mongoURL is required")
	}
	if mongoDB == "" {
		log.Fatal("mongoDB is required")
	}

	// Parse processID from hex string
	processIDBytes := internal.HexBytes{}
	if err := processIDBytes.ParseString(processID); err != nil {
		log.Fatalf("invalid processID format: %v", err)
	}

	// Initialize MongoDB database
	database, err := db.New(mongoURL, mongoDB)
	if err != nil {
		log.Fatalf("could not connect to MongoDB: %v", err)
	}
	defer database.Close()

	// Initialize Vocdoni API client (needed for nullifier calculation)
	apiClient, err := vocapi.New(apiEndpoint)
	if err != nil {
		log.Fatalf("could not create Vocdoni API client: %v", err)
	}

	// Route to appropriate handler based on whether userID is provided
	if userID == "" {
		// Process-only mode
		if err := queryProcessOnly(database, apiClient, processIDBytes); err != nil {
			log.Fatalf("error querying process: %v", err)
		}
	} else {
		// Process+User mode
		userIDBytes := internal.HexBytes{}
		if err := userIDBytes.ParseString(userID); err != nil {
			log.Fatalf("invalid userId format: %v", err)
		}
		if err := queryProcessAndUser(database, processIDBytes, userIDBytes); err != nil {
			log.Fatalf("error querying process and user: %v", err)
		}
	}
}

// queryProcessOnly handles the process-only query mode
func queryProcessOnly(database *db.MongoStorage, client *vocapi.HTTPclient, processID internal.HexBytes) error {
	// Get process information from vochain
	printSectionHeader("VOCHAIN PROCESS INFORMATION")
	processIDHex := voctypes.HexStringToHexBytes(processID.String())
	processInfo, err := client.Election(processIDHex)
	if err != nil {
		log.Warnf("could not get process info from vochain: %v", err)
		fmt.Println("Status: NOT AVAILABLE")
		fmt.Println("Could not retrieve process information from vochain")
	} else {
		fmt.Println("Status: FOUND")
		printJSON("Process Information", processInfo)

		// Get organization information from vochain
		printSectionHeader("VOCHAIN ORGANIZATION INFORMATION")
		if len(processInfo.OrganizationID) > 0 {
			orgInfo, err := client.Account(processInfo.OrganizationID.String())
			if err != nil {
				log.Warnf("could not get organization info from vochain: %v", err)
				fmt.Println("Status: NOT AVAILABLE")
				fmt.Println("Could not retrieve organization information from vochain")
			} else {
				fmt.Println("Status: FOUND")
				printJSON("Organization Information", orgInfo)
			}
		} else {
			fmt.Println("Status: NOT AVAILABLE")
			fmt.Println("Organization ID not found in process information")
		}
	}

	// Find bundle(s) containing this process
	bundles, err := database.ProcessBundlesByProcess(processID)
	if err != nil {
		return fmt.Errorf("failed to find process bundles: %w", err)
	}

	// Validate exactly one bundle exists
	if len(bundles) == 0 {
		return fmt.Errorf("process not found in any bundle")
	}
	if len(bundles) > 1 {
		return fmt.Errorf("process found in multiple bundles (%d), this is an error condition", len(bundles))
	}

	bundle := bundles[0]

	// Print process bundle information
	printSectionHeader("PROCESS BUNDLE INFORMATION")
	fmt.Printf("Bundle ID:           %s\n", bundle.ID.Hex())
	fmt.Printf("Organization:        %s\n", bundle.OrgAddress.Hex())
	fmt.Printf("Census ID:           %s\n", bundle.Census.ID.Hex())
	fmt.Printf("Number of Processes: %d\n", len(bundle.Processes))
	fmt.Printf("Processes:\n")
	for i, proc := range bundle.Processes {
		fmt.Printf("  %d. %s\n", i+1, proc.String())
	}

	// Print census information
	printSectionHeader("CENSUS INFORMATION")
	fmt.Printf("Census ID:      %s\n", bundle.Census.ID.Hex())
	fmt.Printf("Type:           %s\n", bundle.Census.Type)
	fmt.Printf("Weighted:       %t\n", bundle.Census.Weighted)
	fmt.Printf("Size:           %d\n", bundle.Census.Size)
	if !bundle.Census.GroupID.IsZero() {
		fmt.Printf("Group ID:       %s\n", bundle.Census.GroupID.Hex())
	}
	fmt.Printf("Auth Fields:    %v\n", bundle.Census.AuthFields)
	fmt.Printf("2FA Fields:     %v\n", bundle.Census.TwoFaFields)
	fmt.Printf("Created At:     %s\n", bundle.Census.CreatedAt.Format("2006-01-02 15:04:05"))
	if !bundle.Census.Published.CreatedAt.IsZero() {
		fmt.Printf("Published At:   %s\n", bundle.Census.Published.CreatedAt.Format("2006-01-02 15:04:05"))
		fmt.Printf("Census URI:     %s\n", bundle.Census.Published.URI)
		fmt.Printf("Census Root:    %s\n", bundle.Census.Published.Root.String())
	}

	// Count census participants
	participants, err := database.CensusParticipants(bundle.Census.ID.Hex())
	if err != nil {
		return fmt.Errorf("failed to count census participants: %w", err)
	}
	fmt.Printf("\nTotal Census Participants: %d\n", len(participants))

	// Print CSP statistics
	printSectionHeader("CSP STATISTICS")

	bundleIDBytes := internal.HexBytes{}
	if err := bundleIDBytes.ParseString(bundle.ID.Hex()); err != nil {
		return fmt.Errorf("failed to parse bundle ID: %w", err)
	}

	// Count login attempts
	loginAttempts, err := database.CountCSPAuthByBundle(bundleIDBytes)
	if err != nil {
		return fmt.Errorf("failed to count login attempts: %w", err)
	}
	fmt.Printf("Login Attempts (CSP Tokens created):     %d\n", loginAttempts)

	// Count verified logins
	verifiedLogins, err := database.CountCSPAuthVerifiedByBundle(bundleIDBytes)
	if err != nil {
		return fmt.Errorf("failed to count verified logins: %w", err)
	}
	fmt.Printf("Verified Logins (tokens verified):       %d\n", verifiedLogins)

	// Count voting proofs obtained
	votingProofs, err := database.CountCSPProcessConsumedByProcess(processID)
	if err != nil {
		return fmt.Errorf("failed to count voting proofs: %w", err)
	}
	fmt.Printf("Voting Proofs Obtained (proofs consumed): %d\n", votingProofs)

	return nil
}

// queryProcessAndUser handles the process+user query mode
func queryProcessAndUser(database *db.MongoStorage, processID, userID internal.HexBytes) error {
	// Find bundle containing this process
	bundles, err := database.ProcessBundlesByProcess(processID)
	if err != nil {
		return fmt.Errorf("failed to find process bundles: %w", err)
	}

	// Validate exactly one bundle exists
	if len(bundles) == 0 {
		return fmt.Errorf("process not found in any bundle")
	}
	if len(bundles) > 1 {
		return fmt.Errorf("process found in multiple bundles (%d), this is an error condition", len(bundles))
	}

	bundle := bundles[0]
	censusID := bundle.Census.ID.Hex()

	printSectionHeader("USER PARTICIPATION DETAILS")
	fmt.Printf("Process ID:  %s\n", processID.String())
	fmt.Printf("User ID:     %s\n", userID.String())
	fmt.Printf("Bundle ID:   %s\n", bundle.ID.Hex())
	fmt.Printf("Census ID:   %s\n", censusID)
	fmt.Println()

	// Check if census participant exists
	printSubsectionHeader("Census Participant")
	participant, err := database.CensusParticipant(censusID, userID.String())
	if err != nil {
		if err != db.ErrNotFound {
			return fmt.Errorf("failed to get census participant: %w", err)
		}
		fmt.Println("Status: NOT FOUND")
		fmt.Println("This user is not a participant in this census.")
	} else {
		fmt.Println("Status: FOUND")
		printJSON("Census Participant Details", participant)
	}

	// Check for CSP authentication token
	printSubsectionHeader("CSP Authentication Token")
	bundleIDBytes := internal.HexBytes{}
	if err := bundleIDBytes.ParseString(bundle.ID.Hex()); err != nil {
		return fmt.Errorf("failed to parse bundle ID: %w", err)
	}

	cspAuth, err := database.LastCSPAuth(userID, bundleIDBytes)
	if err != nil {
		if err != db.ErrTokenNotFound {
			return fmt.Errorf("failed to get CSP auth: %w", err)
		}
		fmt.Println("Status: NOT FOUND")
		fmt.Println("No authentication token found for this user and bundle.")
	} else {
		fmt.Println("Status: FOUND")
		printJSON("CSP Auth Details", cspAuth)
	}

	// Check for CSP process status
	printSubsectionHeader("CSP Process Status")
	cspProcess, err := database.CSPProcessByUserAndProcess(userID, processID)
	if err != nil {
		if err != db.ErrTokenNotFound {
			return fmt.Errorf("failed to get CSP process: %w", err)
		}
		fmt.Println("Status: NOT FOUND")
		fmt.Println("No process status found for this user and process.")
	} else {
		fmt.Println("Status: FOUND")
		printJSON("CSP Process Details", cspProcess)

		// Calculate nullifier if consumedAddress is present
		if len(cspProcess.UsedAddress) > 0 {
			printSubsectionHeader("Nullifier Calculation")
			fmt.Printf("Consumed Address: %s\n", cspProcess.UsedAddress.String())

			// Generate nullifier using the vochain state package
			nullifier := state.GenerateNullifier(
				common.BytesToAddress(cspProcess.UsedAddress),
				bundle.Census.Published.Root,
			)
			fmt.Printf("Nullifier:        %x\n", nullifier)
		}
	}

	return nil
}

// printSectionHeader prints a formatted section header
func printSectionHeader(title string) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("  %s\n", title)
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println()
}

// printSubsectionHeader prints a formatted subsection header
func printSubsectionHeader(title string) {
	fmt.Println()
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Printf("  %s\n", title)
	fmt.Println("───────────────────────────────────────────────────────────────")
	fmt.Println()
}

// printJSON prints a struct as formatted JSON with a title
func printJSON(title string, data any) {
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		fmt.Printf("Error formatting data: %v\n", err)
		return
	}
	fmt.Printf("%s:\n%s\n", title, string(jsonData))
}
