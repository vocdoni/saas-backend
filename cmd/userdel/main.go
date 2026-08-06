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
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
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
	fmt.Println("Not touched: Stripe billing data and database backups.")
	return nil
}

// printOrgContents prints how much data an organization about to be torn down
// currently holds.
func printOrgContents(ms *db.MongoStorage, address common.Address) error {
	members, err := ms.CountOrgMembers(address)
	if err != nil {
		return fmt.Errorf("could not count members of org %s: %w", address, err)
	}
	groups, _, err := ms.OrganizationMemberGroups(address, 1, 1)
	if err != nil {
		return fmt.Errorf("could not count groups of org %s: %w", address, err)
	}
	censuses, err := ms.CensusesByOrg(address)
	if err != nil {
		return fmt.Errorf("could not list censuses of org %s: %w", address, err)
	}
	var participants int64
	for _, census := range censuses {
		n, err := ms.CountCensusParticipants(census.ID.Hex())
		if err != nil {
			return fmt.Errorf("could not count participants of census %s: %w", census.ID.Hex(), err)
		}
		participants += n
	}
	processes, err := ms.CountProcesses(address, db.AllProcesses)
	if err != nil {
		return fmt.Errorf("could not count processes of org %s: %w", address, err)
	}
	bundles, err := ms.ProcessBundlesByOrg(address)
	if err != nil {
		return fmt.Errorf("could not list bundles of org %s: %w", address, err)
	}
	invites, err := ms.PendingInvitations(address)
	if err != nil {
		return fmt.Errorf("could not list invitations of org %s: %w", address, err)
	}
	fmt.Printf("    the organization and its voting history metadata (%d processes, %d bundles)\n",
		processes, len(bundles))
	fmt.Printf("    %d members in %d groups\n", members, groups)
	fmt.Printf("    %d censuses with %d participants (incl. their CSP auth tokens)\n",
		len(censuses), participants)
	fmt.Printf("    %d pending invitations, plus import jobs\n", len(invites))
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
