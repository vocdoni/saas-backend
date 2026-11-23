package topup

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/stripe"
)

// userStore is the one database read resolveCustomer needs: the user behind a customer's email.
type userStore interface {
	UserByEmail(email string) (*db.User, error)
}

// customerQuery is what resolveCustomer needs: the Stripe client, the users to vouch for an
// unstamped customer, the organization and the requested customer id (may be empty).
type customerQuery struct {
	client     *stripe.Client
	users      userStore
	org        common.Address
	customerID string
}

// resolveCustomer finds the organization's Stripe customer by its address, or takes customerID
// when none is found; stamp reports that the customer is not stamped yet and Execute must stamp
// it with the organization address. An unstamped customer is taken only if a checkout of the
// organization used it or, with no checkout at all, its email is an Admin of this organization
// and of no other (checkCustomerOrg). When the search found one and customerID names another,
// that one is used only if it is stamped with the same organization (a duplicate stamp), never
// one stamped with another organization nor an unstamped one. A failed search refuses: it cannot
// tell whether a customer is stamped. It never creates a customer, refuses one with no address
// (checkCustomer), and writes nothing.
func resolveCustomer(q customerQuery) (customer *stripeapi.Customer, stamp bool, err error) {
	client, org, customerID := q.client, q.org, q.customerID
	found, err := client.GetCustomerByAddress(org.String())
	if err != nil && !errors.Is(err, stripe.ErrCustomerNotFound) {
		return nil, false, fmt.Errorf("could not search the customer of organization %s, retry later: %w", org, err)
	}
	if customerID == "" {
		if found == nil {
			return nil, false, fmt.Errorf("%w is stamped with organization %s; name the one its checkouts created, "+
				"or create one in the stripe dashboard with the organization's billing address (and tax id) first "+
				"and name that", ErrNoCustomer, org)
		}
		return found, false, checkCustomer(found)
	}
	if found != nil && found.ID == customerID {
		return found, false, checkCustomer(found)
	}
	customer, err = client.GetCustomer(customerID)
	if err != nil {
		return nil, false, fmt.Errorf("could not read customer %s: %w", customerID, err)
	}
	stamped := customer.Metadata["address"]
	switch {
	case stamped != "" && common.HexToAddress(stamped) != org:
		return nil, false, fmt.Errorf("%w: customer %s belongs to organization %s, not %s",
			ErrCustomerOtherOrg, customer.ID, stamped, org)
	case stamped == "" && found != nil:
		return nil, false, fmt.Errorf("%w: organization %s is stamped on customer %s, not %s; drop the customer "+
			"or fix the stamp in the dashboard", ErrCustomerOtherOrg, org, found.ID, customerID)
	}
	// checked before the evidence: a customer refused here is never stamped
	if err := checkCustomer(customer); err != nil {
		return nil, false, err
	}
	if stamped == "" {
		if err := checkCustomerOrg(q, customer); err != nil {
			return nil, false, err
		}
	}
	return customer, stamped == "", nil
}

// checkCustomerOrg refuses an unstamped customer that cannot be tied to the organization
// (ErrCustomerOtherOrg). The evidence is the customer's checkout sessions: one carrying
// wallet_topup_org of this organization accepts it (its checkouts created the customer);
// sessions of other organizations alone refuse it (another tenant's customer). A customer with no
// checkout session at all (one created in the Dashboard) is accepted when its email is an Admin of
// this organization and belongs to no other organization, so the email vouches for one tenant only.
func checkCustomerOrg(q customerQuery, customer *stripeapi.Customer) error {
	sessions, err := q.client.CheckoutSessionsOfCustomer(customer.ID)
	if err != nil {
		return fmt.Errorf("could not list the checkout sessions of customer %s: %w", customer.ID, err)
	}
	if len(sessions) > 0 {
		var orgs []string
		for _, session := range sessions {
			raw := session.Metadata[stripe.MetadataKeyWalletTopUpOrg]
			if common.IsHexAddress(raw) && common.HexToAddress(raw) == q.org {
				return nil
			}
			if raw != "" {
				orgs = append(orgs, raw)
			}
		}
		if len(orgs) == 0 {
			return fmt.Errorf("%w: customer %s has checkout sessions but none a wallet top-up of organization %s, so "+
				"it is not stamped; verify the customer and stamp metadata.address with the organization address in "+
				"the stripe dashboard by hand", ErrCustomerOtherOrg, customer.ID, q.org)
		}
		return fmt.Errorf("%w: customer %s was created by checkouts of %s, not of organization %s, so it is not stamped",
			ErrCustomerOtherOrg, customer.ID, strings.Join(orgs, ", "), q.org)
	}
	user, err := q.users.UserByEmail(internal.NormalizeEmail(customer.Email))
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		return fmt.Errorf("could not look up the user of customer %s <%s>: %w", customer.ID, customer.Email, err)
	}
	if err != nil || !user.HasRoleFor(q.org, db.AdminRole) {
		return fmt.Errorf("%w: customer %s <%s> is not an admin of organization %s, so it is not stamped; verify the "+
			"customer and stamp metadata.address with the organization address in the stripe dashboard by hand",
			ErrCustomerOtherOrg, customer.ID, customer.Email, q.org)
	}
	for _, membership := range user.Organizations {
		if membership.Address != q.org {
			return fmt.Errorf("%w: customer %s <%s> is an admin of organization %s but also a member of %s, so its "+
				"email cannot vouch for it and it is not stamped; verify the customer and stamp metadata.address with "+
				"the organization address in the stripe dashboard by hand",
				ErrCustomerOtherOrg, customer.ID, customer.Email, q.org, membership.Address)
		}
	}
	return nil
}
