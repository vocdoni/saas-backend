// Command orgdel erases a single organization and all of its data from the
// database (members, groups, censuses, participants, processes, voting
// processes, questions, API keys, bundles, CSP tokens, jobs, invitations).
// Users are never deleted: they are only unlinked from the organization.
//
// Drafts go first, the way the API deletes a draft: a paid one is credited back
// to the wallet that paid it (the organization's own, or its integrator's for a
// managed organization). The organization's own wallet and its history are
// kept; a non-zero balance is printed, to be returned by hand.
//
// It refuses, erasing nothing, while the organization manages other
// organizations (erase those first), and, unless -force is given, while any of
// its elections may still be live on-chain according to the stored state:
// questions READY or PAUSED, processes being published (or with elections
// already mined) and published legacy processes. The chain itself is not read.
//
// Out of scope (handle manually where applicable): Stripe billing data,
// database backups and on-chain state.
//
// Usage:
//
//	go run ./cmd/orgdel -org 0x... -dryRun  # print the impact report only
//	go run ./cmd/orgdel -org 0x...          # report + confirmation prompt
//	go run ./cmd/orgdel -org 0x... -yes     # skip confirmation
//	go run ./cmd/orgdel -org 0x... -force   # erase even with possibly live elections
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
	"github.com/vocdoni/saas-backend/api"
	"github.com/vocdoni/saas-backend/db"
)

// options holds the parsed CLI flags.
type options struct {
	mongoURL string
	database string
	org      string
	yes      bool
	dryRun   bool
	force    bool
}

func main() {
	var opts options
	flag.StringVar(&opts.mongoURL, "mongoURL", os.Getenv("VOCDONI_MONGOURL"), "MongoDB connection URL (or VOCDONI_MONGOURL)")
	flag.StringVar(&opts.database, "mongoDB", os.Getenv("VOCDONI_MONGODB"), "MongoDB database name (or VOCDONI_MONGODB)")
	flag.StringVar(&opts.org, "org", "", "address of the organization to erase")
	flag.BoolVar(&opts.yes, "yes", false, "skip the confirmation prompt")
	flag.BoolVar(&opts.dryRun, "dryRun", false, "print the impact report and exit without deleting anything")
	flag.BoolVar(&opts.force, "force", false, "erase even if some elections may still be live on-chain")
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

	s, err := printPlan(ms, address)
	if err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Println("\ndry run: nothing was deleted")
		return nil
	}
	if s.ManagedOrgs > 0 {
		return fmt.Errorf("the organization manages %d organizations, erase them first; nothing was erased", s.ManagedOrgs)
	}
	if live := s.ActiveQuestions + s.MidPublish + s.LegacyPublished; live > 0 && !opts.force {
		return fmt.Errorf("%d elections may still be live on-chain (see above), end them first or pass -force; "+
			"nothing was erased", live)
	}
	if !opts.yes && !confirm() {
		return fmt.Errorf("aborted")
	}
	// drafts first, crediting the paid ones back: the erasure refuses while any is left. Movements a
	// crash left unposted are posted first, so the balance printed matches the ledger.
	if err := ms.DrainUnpostedWalletEntries(); err != nil {
		return fmt.Errorf("could not post unposted wallet movements, nothing was erased: %w", err)
	}
	if err := api.DeleteOrgDrafts(ms, address); err != nil {
		return fmt.Errorf("could not delete the drafts, nothing else was erased: %w", err)
	}
	if err := printWalletBalance(ms, address); err != nil {
		return err
	}
	if err := ms.EraseOrganizationData(address); err != nil {
		return fmt.Errorf("erasure finished with errors: %w", err)
	}
	fmt.Printf("erased organization %s\n", address)
	return nil
}

// printWalletBalance prints the balance of the organization's own wallet when it is not zero. The
// wallet and its history are kept by the erasure, so that money is returned by hand. A managed
// organization has no wallet of its own (its integrator pays).
func printWalletBalance(ms *db.MongoStorage, address common.Address) error {
	org, err := ms.Organization(address)
	if err != nil {
		return fmt.Errorf("could not fetch org %s: %w", address, err)
	}
	if org.ManagedBy != (common.Address{}) {
		return nil
	}
	wallet, err := ms.Wallet(address)
	if err != nil {
		return fmt.Errorf("could not read the wallet of org %s: %w", address, err)
	}
	if wallet.BalanceCents != 0 {
		fmt.Printf("wallet balance: %d eur cents (kept; return it by hand)\n", wallet.BalanceCents)
	}
	return nil
}

// printPlan prints everything the erasure will touch, and returns the counts it printed.
func printPlan(ms *db.MongoStorage, address common.Address) (*db.OrgDataSummary, error) {
	org, err := ms.Organization(address)
	if err != nil {
		return nil, fmt.Errorf("could not find organization %s: %w", address, err)
	}
	name := org.DisplayName()
	if name == "" {
		name = "unnamed"
	}
	fmt.Printf("\nThis will erase organization %s (%q)\n", address, name)
	if org.ManagedBy != (common.Address{}) {
		fmt.Printf("Managed by %s: its paid drafts are credited back to that integrator's wallet.\n", org.ManagedBy)
	}
	s, err := ms.OrganizationDataSummary(address)
	if err != nil {
		return nil, err
	}
	fmt.Println("\nThis removes:")
	fmt.Printf("  the organization and its voting history metadata (%d processes, %d bundles)\n", s.Processes, s.Bundles)
	fmt.Printf("  %d voting processes (%d drafts; paid drafts are credited back first) with %d questions, %d API keys\n",
		s.VotingProcesses, s.Drafts, s.Questions, s.Keys)
	fmt.Printf("  %d members in %d groups\n", s.Members, s.Groups)
	fmt.Printf("  %d censuses with %d participants (incl. their CSP auth tokens)\n", s.Censuses, s.Participants)
	fmt.Printf("  %d pending invitations, plus import jobs\n", s.Invitations)
	if s.ActiveQuestions > 0 {
		fmt.Printf("\nWARNING: %d questions are READY or PAUSED and may still be live on-chain\n", s.ActiveQuestions)
	}
	if s.MidPublish > 0 {
		fmt.Printf("WARNING: %d voting processes are being published or have elections mined\n", s.MidPublish)
	}
	if s.LegacyPublished > 0 {
		fmt.Printf("WARNING: %d legacy processes are published and may still be live on-chain\n", s.LegacyPublished)
	}
	if s.ManagedOrgs > 0 {
		fmt.Printf("REFUSED: the organization manages %d organizations; erase them first\n", s.ManagedOrgs)
	}
	fmt.Println("\nKept: payment records of published and partially published processes, and wallets and")
	fmt.Println("their history (the organization's balance is printed, to be returned by hand).")
	fmt.Println("Not touched: Stripe billing data, database backups, on-chain state.")
	users, err := ms.OrganizationUsers(address)
	if err != nil {
		return nil, fmt.Errorf("could not list users of org %s: %w", address, err)
	}
	fmt.Printf("\nUsers unlinked from the organization (not deleted): %d\n", len(users))
	for _, u := range users {
		fmt.Printf("  #%d <%s>\n", u.ID, u.Email)
	}
	return s, nil
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
