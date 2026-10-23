// Command userdel erases a registered user and their personal data from the
// database, to serve GDPR right-to-erasure requests in a standardized way.
//
// Organizations where the user is the sole admin are deleted entirely
// (members, groups, censuses, participants, processes, bundles, CSP tokens,
// jobs, invitations). Organizations with other admins are kept: only the
// user's membership is removed. In kept organizations created by the user the
// creator email is retained on purpose — the org signing key is derived from
// secret+creator+nonce (account.OrganizationSigner), so redacting it would
// break the org's on-chain account; such orgs are listed in the output for
// manual follow-up.
//
// Drafts of the organizations to delete go first, the way the API deletes a
// draft: a paid one is credited back to the wallet that paid it before it goes,
// and its payment record goes with it (the credit-back in the wallet ledger is
// the audit trail); a once-per-organization add-on price no published or mined
// process received comes back too. No Stripe access is needed: an open top-up
// checkout is left to Stripe's own expiry, and paying it meanwhile only credits
// the wallet. The first draft that cannot be deleted aborts the erasure with
// nothing else erased, and running the command again resumes it. Wallets and
// their history are kept; the balance of each torn-down organization's own
// wallet is printed, to be returned by hand (it never blocks the erasure).
//
// Out of scope (handle manually where applicable): Stripe customer data and
// database backups.
//
// Usage:
//
//	go run ./cmd/userdel -email user@example.com -dryRun  # print the impact report only
//	go run ./cmd/userdel -email user@example.com          # report + confirmation prompt
//	go run ./cmd/userdel -id 42 -yes                      # skip confirmation
//
// The MongoDB connection may also be supplied via the VOCDONI_MONGOURL and
// VOCDONI_MONGODB environment variables.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
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
	email    string
	userID   uint64
	yes      bool
	dryRun   bool
}

func main() {
	var opts options
	flag.StringVar(&opts.mongoURL, "mongoURL", os.Getenv("VOCDONI_MONGOURL"), "MongoDB connection URL (or VOCDONI_MONGOURL)")
	flag.StringVar(&opts.database, "mongoDB", os.Getenv("VOCDONI_MONGODB"), "MongoDB database name (or VOCDONI_MONGODB)")
	flag.StringVar(&opts.email, "email", "", "email of the user to erase")
	flag.Uint64Var(&opts.userID, "id", 0, "ID of the user to erase")
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
	if opts.mongoURL == "" || (opts.email == "" && opts.userID == 0) {
		flag.Usage()
		return fmt.Errorf("-mongoURL and one of -email or -id are required")
	}

	ms, err := db.New(opts.mongoURL, opts.database)
	if err != nil {
		return fmt.Errorf("could not connect to mongodb: %w", err)
	}
	defer ms.Close()

	user, err := resolveUser(ms, opts.email, opts.userID)
	if err != nil {
		return fmt.Errorf("could not find user: %w", err)
	}

	if err := printPlan(ms, user); err != nil {
		return err
	}
	if opts.dryRun {
		fmt.Println("\ndry run: nothing was deleted")
		return nil
	}

	if !opts.yes && !confirm() {
		return fmt.Errorf("aborted")
	}
	if err := deleteDrafts(os.Stdout, ms, user); err != nil {
		return err
	}

	report, err := ms.EraseUser(user.ID)
	if report != nil {
		fmt.Printf("erased user %d <%s>\n", report.UserID, report.Email)
		for _, org := range report.DeletedOrgs {
			fmt.Printf("  deleted org %s\n", org)
		}
		for _, org := range report.KeptOrgs {
			fmt.Printf("  removed membership in org %s\n", org)
		}
		for _, org := range report.CreatorEmailRetained {
			fmt.Printf("  WARNING: creator email retained in org %s (signing-key derivation input), follow up manually\n", org)
		}
	}
	if err != nil {
		return fmt.Errorf("erasure finished with errors: %w", err)
	}
	return nil
}

// deleteDrafts deletes, crediting the paid ones back, the drafts of every organization the
// erasure will tear down, and then prints the balance each of those organizations' own wallet
// keeps. Every draft goes before any balance is printed: a managed organization's paid draft is
// credited to its integrator's wallet, which may be printed first otherwise. EraseUser refuses
// while any draft is left, so a failure here stops the erasure before anything else is erased.
// Movements a crash left unposted are posted first, so the balances printed match the ledger.
func deleteDrafts(w io.Writer, ms *db.MongoStorage, user *db.User) error {
	if err := ms.DrainUnpostedWalletEntries(); err != nil {
		return fmt.Errorf("could not post unposted wallet movements, nothing was erased: %w", err)
	}
	var tornDown []common.Address
	for _, userOrg := range user.Organizations {
		soleAdmin, err := ms.IsSoleOrgAdmin(userOrg.Address, user.ID)
		if err != nil {
			return fmt.Errorf("could not classify org %s: %w", userOrg.Address, err)
		}
		if !soleAdmin {
			continue
		}
		if err := api.DeleteOrgDrafts(ms, userOrg.Address); err != nil {
			return fmt.Errorf("could not delete the drafts of org %s, nothing else was erased: %w", userOrg.Address, err)
		}
		tornDown = append(tornDown, userOrg.Address)
	}
	for _, address := range tornDown {
		if err := printWalletBalance(w, ms, address); err != nil {
			return err
		}
	}
	return nil
}

// printWalletBalance prints the balance of an organization's own wallet when it is not zero. The
// wallet and its history are kept by the erasure, so that money is returned by hand; it never
// blocks the erasure. A managed organization has no wallet of its own (its integrator pays).
func printWalletBalance(w io.Writer, ms *db.MongoStorage, address common.Address) error {
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
	if wallet.BalanceCents == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(w, "  org %s\n    wallet balance: %d eur cents (kept; return it by hand)\n",
		address, wallet.BalanceCents); err != nil {
		return fmt.Errorf("could not print the wallet balance of org %s: %w", address, err)
	}
	return nil
}

// printPlan prints a human-readable report of everything the erasure will
// touch: the user's identity, each organization with its classification (full
// teardown vs membership removal) and, for organizations about to be deleted,
// how much data they hold.
func printPlan(ms *db.MongoStorage, user *db.User) error {
	name := strings.TrimSpace(user.FirstName + " " + user.LastName)
	fmt.Printf("\nThis will erase user #%d: %s <%s>\n", user.ID, name, user.Email)
	if len(user.Organizations) == 0 {
		fmt.Println("\nThe user belongs to no organization.")
	}
	for _, userOrg := range user.Organizations {
		org, err := ms.Organization(userOrg.Address)
		if err != nil {
			return fmt.Errorf("could not fetch org %s: %w", userOrg.Address, err)
		}
		orgName := org.DisplayName()
		if orgName == "" {
			orgName = "unnamed"
		}
		fmt.Printf("\nOrganization %s (%q, role: %s)\n", userOrg.Address, orgName, userOrg.Role)
		soleAdmin, err := ms.IsSoleOrgAdmin(userOrg.Address, user.ID)
		if err != nil {
			return fmt.Errorf("could not classify org %s: %w", userOrg.Address, err)
		}
		if !soleAdmin {
			fmt.Println("  KEPT: other admins exist, only this user's membership is removed.")
			if org.Creator == user.Email {
				fmt.Println("  NOTE: the creator email stays (the org signing key is derived from it);")
				fmt.Println("        follow up manually if it must be redacted.")
			}
			continue
		}
		fmt.Println("  DELETED ENTIRELY: this user is its only admin. This removes:")
		if err := printOrgContents(ms, userOrg.Address); err != nil {
			return err
		}
	}
	fmt.Println("\nAlso deleted: invitations sent to or issued by the user,")
	fmt.Println("pending verification codes, and the user account itself.")
	fmt.Println("Kept: payment records of published and partially published processes (a draft's goes")
	fmt.Println("with it; the wallet ledger records its credit-back), and")
	fmt.Println("wallets and their history (a torn-down organization's balance is returned by hand).")
	fmt.Println("Not touched: Stripe billing data and database backups.")
	return nil
}

// printOrgContents prints how much data an organization about to be torn down
// currently holds.
func printOrgContents(ms *db.MongoStorage, address common.Address) error {
	s, err := ms.OrganizationDataSummary(address)
	if err != nil {
		return err
	}
	fmt.Printf("    the organization and its voting history metadata (%d processes, %d bundles)\n",
		s.Processes, s.Bundles)
	fmt.Printf("    %d voting processes (%d drafts; paid drafts are credited back first) with %d questions, %d API keys\n",
		s.VotingProcesses, s.Drafts, s.Questions, s.Keys)
	fmt.Printf("    %d members in %d groups\n", s.Members, s.Groups)
	fmt.Printf("    %d censuses with %d participants (incl. their CSP auth tokens)\n", s.Censuses, s.Participants)
	fmt.Printf("    %d pending invitations, plus import jobs\n", s.Invitations)
	if s.ActiveQuestions > 0 {
		fmt.Printf("    WARNING: %d questions are READY or PAUSED and may still be live on-chain\n", s.ActiveQuestions)
	}
	return nil
}

// resolveUser looks the user up by ID when provided, otherwise by email.
func resolveUser(ms *db.MongoStorage, email string, id uint64) (*db.User, error) {
	if id != 0 {
		return ms.User(id)
	}
	return ms.UserByEmail(email)
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
