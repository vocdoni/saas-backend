package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
)

const (
	actionImportMembers         = "import-members"
	actionImportAndAddToCensus  = "import-and-add-to-census"
	identifierFieldNationalID   = "nationalId"
	identifierFieldMemberNumber = "memberNumber"
)

// Config contains CLI inputs.
type Config struct {
	APIEndpoint string
	OrgAddress  string
	Email       string
	Password    string
	Action      string
	CSVPath     string
	IDField     string
	BundleID    string
	Yes         bool
}

type processingSummary struct {
	TotalRows      int
	Created        int
	Updated        int
	Skipped        int
	Errors         int
	AddedToCensus  int
	ProcessingFail bool
}

type organizationContext struct {
	Address common.Address
	Country string
}

type verificationTarget struct {
	Identifier    string
	MemberID      string
	ExpectedEmail string
	ExpectedPhone string
}

type verificationResult struct {
	Passes int
	Fails  int
}

func main() {
	if err := run(); err != nil {
		fmt.Printf("error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseConfig(os.Args[1:])
	if err != nil {
		fmt.Print(clientUsage())
		return fmt.Errorf("configuration: %w", err)
	}

	client := newClient(cfg.APIEndpoint)
	if err := client.login(cfg.Email, cfg.Password); err != nil {
		return fmt.Errorf("login: %w", err)
	}

	orgAddress := common.HexToAddress(cfg.OrgAddress)
	orgInfo, err := client.organization(orgAddress)
	if err != nil {
		return fmt.Errorf("load organization %s: %w", orgAddress.Hex(), err)
	}
	orgCtx := &organizationContext{Address: orgAddress, Country: orgInfo.Country}

	stdin := bufio.NewReader(os.Stdin)
	importResult, err := runMemberImport(client, cfg, orgCtx, stdin)
	if err != nil {
		return fmt.Errorf("import workflow: %w", err)
	}

	summary := importResult.summary
	verificationTargets := importResult.verificationTargets
	censusMemberIDs := importResult.censusMemberIDs
	censusID := ""

	if cfg.Action == actionImportAndAddToCensus {
		censusID, err = client.censusIDByBundle(cfg.BundleID)
		if err != nil {
			return fmt.Errorf("resolve census from bundle %s: %w", cfg.BundleID, err)
		}

		addedToCensus, addErrs, addErr := addMembersToCensus(client, censusID, censusMemberIDs)
		if addErr != nil {
			summary.Errors++
			summary.ProcessingFail = true
			fmt.Printf("census add failed: %v\n", addErr)
		} else {
			summary.AddedToCensus = addedToCensus
			if len(addErrs) > 0 {
				summary.Errors += len(addErrs)
				for _, itemErr := range addErrs {
					fmt.Printf("census add warning: %s\n", itemErr)
				}
			}
		}
	}

	printSummary(summary, cfg.Action)

	verification := verifyResults(client, cfg, orgCtx, verificationTargets, censusMemberIDs, censusID)
	globalPass := verification.Fails == 0 && summary.Errors == 0 && !summary.ProcessingFail
	status := "PASS"
	if !globalPass {
		status = "FAIL"
	}
	fmt.Printf("\nVerification summary: pass=%d fail=%d global=%s\n", verification.Passes, verification.Fails, status)

	if !globalPass {
		return fmt.Errorf("verification failed")
	}
	return nil
}

func parseConfig(args []string) (Config, error) {
	cfg := Config{}
	flagSet := flag.NewFlagSet("client", flag.ContinueOnError)
	flagSet.SetOutput(io.Discard)

	flagSet.StringVar(&cfg.APIEndpoint, "apiEndpoint", "http://localhost:8080", "SaaS API endpoint URL")
	flagSet.StringVar(&cfg.OrgAddress, "orgAddress", "", "organization address")
	flagSet.StringVar(&cfg.Email, "email", "", "user email for /auth/login")
	flagSet.StringVar(&cfg.Password, "password", "", "user password for /auth/login")
	flagSet.StringVar(
		&cfg.Action,
		"action",
		"",
		"workflow action: import-members | import-and-add-to-census",
	)
	flagSet.StringVar(&cfg.CSVPath, "csv", "", "path to CSV file")
	flagSet.StringVar(
		&cfg.IDField,
		"idField",
		"",
		"member identifier field: nationalId | memberNumber",
	)
	flagSet.StringVar(
		&cfg.BundleID,
		"bundleId",
		"",
		"existing bundle ID (required for import-and-add-to-census)",
	)
	// TODO: extend --yes to cover additional non-interactive safety checks if needed.
	flagSet.BoolVar(
		&cfg.Yes,
		"yes",
		false,
		"auto-accept member contact updates where prompts would appear",
	)
	if err := flagSet.Parse(args); err != nil {
		return Config{}, fmt.Errorf("parse flags: %w", err)
	}

	if cfg.OrgAddress == "" {
		return Config{}, fmt.Errorf("--orgAddress is required")
	}
	if !common.IsHexAddress(cfg.OrgAddress) {
		return Config{}, fmt.Errorf("--orgAddress must be a valid hex address")
	}
	if cfg.Email == "" {
		return Config{}, fmt.Errorf("--email is required")
	}
	if cfg.Password == "" {
		return Config{}, fmt.Errorf("--password is required")
	}
	if cfg.Action != actionImportMembers && cfg.Action != actionImportAndAddToCensus {
		return Config{}, fmt.Errorf("--action must be %q or %q", actionImportMembers, actionImportAndAddToCensus)
	}
	if cfg.CSVPath == "" {
		return Config{}, fmt.Errorf("--csv is required")
	}
	if cfg.IDField != identifierFieldNationalID && cfg.IDField != identifierFieldMemberNumber {
		return Config{}, fmt.Errorf("--idField must be %q or %q", identifierFieldNationalID, identifierFieldMemberNumber)
	}
	if cfg.Action == actionImportAndAddToCensus && cfg.BundleID == "" {
		return Config{}, fmt.Errorf("--bundleId is required for action %q", actionImportAndAddToCensus)
	}

	return cfg, nil
}

func clientUsage() string {
	return `Usage:
  go run ./cmd/client --orgAddress <hex-address> --email <email> --password <password> \
    --action import-members --csv <path> --idField nationalId|memberNumber
  go run ./cmd/client --orgAddress <hex-address> --email <email> --password <password> \
    --action import-and-add-to-census --bundleId <bundle-id> --csv <path> \
    --idField nationalId|memberNumber
`
}

// verifyResults re-fetches affected member records and census participants to
// validate that updates and census additions were applied as expected.
func verifyResults(
	client *Client,
	cfg Config,
	org *organizationContext,
	targets map[string]verificationTarget,
	expectedCensusMemberIDs []string,
	censusID string,
) verificationResult {
	result := verificationResult{}

	if len(targets) == 0 {
		fmt.Println("\nVerification: no member targets to verify")
	} else {
		fmt.Println("\nVerification: member contact data")
		members := sortedVerificationTargets(targets)
		for _, target := range members {
			member, err := findMemberByIdentifier(client, org.Address, cfg.IDField, target.Identifier)
			if err != nil {
				result.Fails++
				fmt.Printf("FAIL member %s=%s: lookup failed: %v\n", cfg.IDField, target.Identifier, err)
				continue
			}
			if member == nil {
				result.Fails++
				fmt.Printf("FAIL member %s=%s: member not found\n", cfg.IDField, target.Identifier)
				continue
			}

			emailOK := member.Email == target.ExpectedEmail
			phoneOK := member.Phone == target.ExpectedPhone
			if emailOK && phoneOK {
				result.Passes++
				fmt.Printf("PASS member %s=%s (id=%s)\n", cfg.IDField, target.Identifier, member.ID)
				continue
			}

			result.Fails++
			fmt.Printf(
				"FAIL member %s=%s (id=%s) expected email=%q phone=%q got email=%q phone=%q\n",
				cfg.IDField,
				target.Identifier,
				member.ID,
				target.ExpectedEmail,
				target.ExpectedPhone,
				member.Email,
				member.Phone,
			)
		}
	}

	if cfg.Action == actionImportAndAddToCensus {
		if censusID == "" {
			result.Fails++
			fmt.Println("FAIL missing census ID resolved from bundle")
			return result
		}
		fmt.Println("\nVerification: census participants")
		participantsResp, err := client.censusParticipants(censusID)
		if err != nil {
			result.Fails++
			fmt.Printf("FAIL census %s participants lookup: %v\n", censusID, err)
			return result
		}

		participantSet := make(map[string]struct{}, len(participantsResp.MemberIDs))
		for _, memberID := range participantsResp.MemberIDs {
			participantSet[memberID] = struct{}{}
		}

		if len(expectedCensusMemberIDs) == 0 {
			fmt.Printf("PASS census %s: no expected member IDs\n", censusID)
			result.Passes++
			return result
		}

		for _, memberID := range expectedCensusMemberIDs {
			if _, ok := participantSet[memberID]; ok {
				result.Passes++
				fmt.Printf("PASS census %s contains member %s\n", censusID, memberID)
			} else {
				result.Fails++
				fmt.Printf("FAIL census %s missing member %s\n", censusID, memberID)
			}
		}
	}

	return result
}

func printSummary(summary processingSummary, action string) {
	fmt.Println("\nSummary")
	fmt.Printf("  total rows: %d\n", summary.TotalRows)
	fmt.Printf("  created: %d\n", summary.Created)
	fmt.Printf("  updated: %d\n", summary.Updated)
	fmt.Printf("  skipped: %d\n", summary.Skipped)
	fmt.Printf("  errors: %d\n", summary.Errors)
	if action == actionImportAndAddToCensus {
		fmt.Printf("  added to census: %d\n", summary.AddedToCensus)
	}
}

func promptYesNo(stdin *bufio.Reader, message string) (bool, error) {
	for {
		fmt.Print(message)
		input, err := stdin.ReadString('\n')
		if err != nil {
			return false, fmt.Errorf("read prompt input: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(input)) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Println("Please answer Y or N.")
		}
	}
}

// maskedPhone replicates backend hashing + masking behavior so contact
// comparisons can be performed locally against API-returned masked phones.
func maskedPhone(phone string, orgAddress common.Address, country string) (string, error) {
	if strings.TrimSpace(phone) == "" {
		return "", nil
	}

	cleanPhone, err := internal.SanitizeAndVerifyPhoneNumber(phone, country)
	if err != nil {
		return "", fmt.Errorf("sanitize phone %q: %w", phone, err)
	}

	hash := internal.HashOrgData(orgAddress, cleanPhone)
	hexHash := fmt.Sprintf("%x", hash)
	if len(hexHash) < 6 {
		return hexHash, nil
	}
	return hexHash[:6] + "***", nil
}

func sortedVerificationTargets(targets map[string]verificationTarget) []verificationTarget {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	ordered := make([]verificationTarget, 0, len(keys))
	for _, key := range keys {
		ordered = append(ordered, targets[key])
	}
	return ordered
}
