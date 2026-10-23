// Command orgdel erases a single organization and all of its data from the
// database (members, groups, censuses, participants, processes, voting
// processes, questions, API keys, bundles, CSP tokens, jobs, invitations).
// Users are never deleted: they are only unlinked from the organization.
//
// Out of scope (handle manually where applicable): Stripe billing data,
// database backups and on-chain state.
//
// Usage:
//
//	go run ./cmd/orgdel -org 0x... -dryRun  # print the impact report only
//	go run ./cmd/orgdel -org 0x...          # report + confirmation prompt
//	go run ./cmd/orgdel -org 0x... -yes     # skip confirmation
//
// The MongoDB connection may also be supplied via the VOCDONI_MONGOURL and
// VOCDONI_MONGODB environment variables.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
)

// options holds the parsed CLI flags.
type options struct {
	mongoURL string
	database string
	org      string
	yes      bool
	dryRun   bool
}

func main() {
	var opts options
	flag.StringVar(&opts.mongoURL, "mongoURL", os.Getenv("VOCDONI_MONGOURL"), "MongoDB connection URL (or VOCDONI_MONGOURL)")
	flag.StringVar(&opts.database, "mongoDB", os.Getenv("VOCDONI_MONGODB"), "MongoDB database name (or VOCDONI_MONGODB)")
	flag.StringVar(&opts.org, "org", "", "address of the organization to erase")
	flag.BoolVar(&opts.yes, "yes", false, "skip the confirmation prompt")
	flag.BoolVar(&opts.dryRun, "dryRun", false, "print the impact report and exit without deleting anything")
	flag.Parse()
	if err := run(&opts); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// run holds the whole CLI flow so os.Exit and flag.Parse stay in main.
func run(opts *options) error {
	if opts.mongoURL == "" || opts.org == "" {
		flag.Usage()
		return fmt.Errorf("-mongoURL and -org are required")
	}
	if !common.IsHexAddress(opts.org) {
		return fmt.Errorf("invalid organization address %q", opts.org)
	}
	address := common.HexToAddress(opts.org)

	ms, err := db.New(opts.mongoURL, opts.database)
	if err != nil {
		return fmt.Errorf("could not connect to mongodb: %w", err)
	}
	defer ms.Close()

	if err := printPlan(ms, address); err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Println("\ndry run: nothing was deleted")
		return nil
	}
	if !opts.yes && !confirm() {
		return fmt.Errorf("aborted")
	}
	if err := ms.EraseOrganizationData(address); err != nil {
		return fmt.Errorf("erasure finished with errors: %w", err)
	}
	fmt.Printf("erased organization %s\n", address)
	return nil
}

// printPlan prints everything the erasure will touch.
func printPlan(ms *db.MongoStorage, address common.Address) error {
	org, err := ms.Organization(address)
	if err != nil {
		return fmt.Errorf("could not find organization %s: %w", address, err)
	}
	name := org.DisplayName()
	if name == "" {
		name = "unnamed"
	}
	fmt.Printf("\nThis will erase organization %s (%q)\n", address, name)
	if org.ManagedBy != (common.Address{}) {
		fmt.Printf("WARNING: managed organization (managed by %s); the manager's counters are not adjusted.\n", org.ManagedBy)
		fmt.Println("         Prefer DELETE /integrator/organizations/{orgAddress}, which also rolls them back.")
	}
	s, err := ms.OrganizationDataSummary(address)
	if err != nil {
		return err
	}
	fmt.Println("\nThis removes:")
	fmt.Printf("  the organization and its voting history metadata (%d processes, %d bundles)\n", s.Processes, s.Bundles)
	fmt.Printf("  %d voting processes with %d questions, %d API keys\n", s.VotingProcesses, s.Questions, s.Keys)
	fmt.Printf("  %d members in %d groups\n", s.Members, s.Groups)
	fmt.Printf("  %d censuses with %d participants (incl. their CSP auth tokens)\n", s.Censuses, s.Participants)
	fmt.Printf("  %d pending invitations, plus import jobs\n", s.Invitations)
	if s.ActiveQuestions > 0 {
		fmt.Printf("\nWARNING: %d questions are READY or PAUSED and may still be live on-chain\n", s.ActiveQuestions)
	}
	fmt.Println("\nNot touched: Stripe billing data, database backups, on-chain state.")
	users, err := ms.OrganizationUsers(address)
	if err != nil {
		return fmt.Errorf("could not list users of org %s: %w", address, err)
	}
	fmt.Printf("\nUsers unlinked from the organization (not deleted): %d\n", len(users))
	for _, u := range users {
		fmt.Printf("  #%d <%s>\n", u.ID, u.Email)
	}
	return nil
}

// confirm asks the operator to type "yes" before proceeding.
func confirm() bool {
	fmt.Print("type 'yes' to proceed: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	return strings.TrimSpace(line) == "yes"
}
