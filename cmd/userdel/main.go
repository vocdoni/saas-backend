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

	"github.com/vocdoni/saas-backend/api"
	"github.com/vocdoni/saas-backend/db"
	apierrors "github.com/vocdoni/saas-backend/errors"
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

	if err := showPlan(os.Stdout, ms, user.ID); err != nil {
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

// showPlan prints the user's erasure plan and fails when it is blocked, so that neither a dry run
// nor a real one goes on (to the prompt, the wallet drain or any deletion) with a sub-org or managed
// org that the erasure would orphan.
func showPlan(w io.Writer, ms *db.MongoStorage, userID uint64) error {
	plan, err := ms.UserErasurePlan(userID)
	if err != nil {
		return err
	}
	if err := printPlan(w, plan); err != nil {
		return fmt.Errorf("could not print the plan: %w", err)
	}
	if len(plan.Blocked) > 0 {
		return fmt.Errorf("erasure blocked: orgs %v have sub-orgs or managed orgs that would survive them", plan.Blocked)
	}
	return nil
}

// deleteDrafts deletes, crediting the paid ones back, the drafts of every organization the
// erasure will tear down (api.DeleteUserOrgDrafts), and then prints, in the user's organization
// order, the balance each of those organizations' own wallet keeps when it is not zero. The wallet
// and its history are kept by the erasure, so that money is returned by hand; it never blocks the
// erasure. A managed organization has no wallet of its own (its integrator pays) and is not listed.
// EraseUser refuses while any draft is left, so a failure here stops the erasure before anything
// else is erased.
func deleteDrafts(w io.Writer, ms *db.MongoStorage, user *db.User) error {
	balances, err := api.DeleteUserOrgDrafts(ms, user.ID)
	if err != nil {
		if apierrors.Is(err, apierrors.ErrPublishInProgress) {
			// without a chain client it cannot tell whether a submitted election exists
			return fmt.Errorf("%w; run the service so it settles the draft's publish, or delete the draft "+
				"through the API, first", err)
		}
		return err
	}
	for _, userOrg := range user.Organizations {
		balance := balances[userOrg.Address]
		if balance == 0 {
			continue
		}
		if _, err := fmt.Fprintf(w, "  org %s\n    wallet balance: %d eur cents (kept; return it by hand)\n",
			userOrg.Address, balance); err != nil {
			return fmt.Errorf("could not print the wallet balance of org %s: %w", userOrg.Address, err)
		}
	}
	return nil
}

// printPlan writes a human-readable report of everything the erasure will
// touch: the user's identity, each organization with its classification (full
// teardown vs membership removal) and, for organizations about to be deleted,
// how much data they hold and which of their sub-orgs block the erasure.
func printPlan(w io.Writer, plan *db.UserErasurePlan) error {
	var b strings.Builder
	fmt.Fprintf(&b, "\nThis will erase user #%d: %s <%s>\n", plan.UserID, plan.Name, plan.Email)
	if len(plan.Orgs) == 0 {
		fmt.Fprintln(&b, "\nThe user belongs to no organization.")
	}
	for _, org := range plan.Orgs {
		orgName := org.Name
		if orgName == "" {
			orgName = "unnamed"
		}
		fmt.Fprintf(&b, "\nOrganization %s (%q, role: %s)\n", org.Address, orgName, org.Role)
		if !org.DeletedEntirely {
			fmt.Fprintln(&b, "  KEPT: other admins exist, only this user's membership is removed.")
			if org.CreatorEmailRetained {
				fmt.Fprintln(&b, "  NOTE: the creator email stays (the org signing key is derived from it);")
				fmt.Fprintln(&b, "        follow up manually if it must be redacted.")
			}
			continue
		}
		fmt.Fprintln(&b, "  DELETED ENTIRELY: this user is its only admin. This removes:")
		fmt.Fprintf(&b, "    the organization and its voting history metadata (%d processes, %d bundles)\n",
			org.LegacyProcesses, org.Bundles)
		fmt.Fprintf(&b, "    %d voting processes, %d of them drafts (paid drafts are credited back first)\n",
			org.VotingProcesses, org.Drafts)
		fmt.Fprintf(&b, "    %d members in %d groups\n", org.Members, org.Groups)
		fmt.Fprintf(&b, "    %d censuses with %d participants (incl. their CSP auth tokens)\n",
			org.Censuses, org.CensusParticipants)
		fmt.Fprintf(&b, "    %d pending invitations, plus import jobs\n", org.PendingInvitations)
		fmt.Fprintf(&b, "    %d API keys\n", org.APIKeys)
		if len(org.SurvivingChildren) > 0 {
			fmt.Fprintln(&b, "  BLOCKED: these sub-orgs or managed orgs would survive it; delete them or move them first:")
			for _, child := range org.SurvivingChildren {
				fmt.Fprintf(&b, "    %s\n", child)
			}
		}
	}
	fmt.Fprintln(&b, "\nAlso deleted: invitations sent to or issued by the user,")
	fmt.Fprintln(&b, "pending verification codes, and the user account itself.")
	fmt.Fprintf(&b, "Kept with the user's email removed: %d uploaded objects (images, election metadata).\n", plan.Objects)
	fmt.Fprintln(&b, "Kept: payment records of published and partially published processes (a draft's goes")
	fmt.Fprintln(&b, "with it; the wallet ledger records its credit-back), and")
	fmt.Fprintln(&b, "wallets and their history (a torn-down organization's balance is returned by hand).")
	fmt.Fprintln(&b, "Not touched: Stripe billing data and database backups.")
	_, err := io.WriteString(w, b.String())
	return err
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
