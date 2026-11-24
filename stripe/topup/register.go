package topup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
	stripeapi "github.com/stripe/stripe-go/v87"
	stripecustomer "github.com/stripe/stripe-go/v87/customer"
	"github.com/vocdoni/saas-backend/stripe"
)

// ErrCustomerExists is CreateCustomer refusing an organization a customer is stamped with already.
var ErrCustomerExists = fmt.Errorf("the organization has a stripe customer already")

// BillingDetails is the legal entity CreateCustomer registers as an organization's Stripe
// customer: every field is required but Line2 and State.
type BillingDetails struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	PostalCode string `json:"postalCode"`
	State      string `json:"state"`
	Country    string `json:"country"`    // ISO 3166-1 alpha-2, upper case
	TaxIDType  string `json:"taxIdType"`  // a Stripe customer tax id type: es_cif, eu_vat…
	TaxIDValue string `json:"taxIdValue"` // as Stripe expects it for the type
}

var (
	countryPattern   = regexp.MustCompile(`^[A-Z]{2}$`)
	taxIDTypePattern = regexp.MustCompile(`^[a-z]{2,3}(_[a-z]+){1,2}$`)
)

// ValidateBilling checks the details alone and returns them trimmed. Stripe checks the rest when
// the customer is created: that the country exists, the tax id type is one it knows and the value
// is valid for it, and that Stripe Tax can locate the address. Every refusal wraps ErrInvalid.
func ValidateBilling(b BillingDetails) (BillingDetails, error) {
	for _, f := range []*string{
		&b.Name, &b.Email, &b.Line1, &b.Line2, &b.City, &b.PostalCode, &b.State, &b.Country, &b.TaxIDType, &b.TaxIDValue,
	} {
		*f = strings.TrimSpace(*f)
	}
	for _, f := range []struct {
		name, value string
		limit       int
		optional    bool
	}{
		{"name", b.Name, 200, false},
		{"email", b.Email, 254, false},
		{"line1", b.Line1, 200, false},
		{"line2", b.Line2, 200, true},
		{"city", b.City, 200, false},
		{"postalCode", b.PostalCode, 200, false},
		{"state", b.State, 200, true},
		{"taxIdValue", b.TaxIDValue, 50, false},
	} {
		if f.value == "" && !f.optional {
			return b, fmt.Errorf("%w: %s is required", ErrInvalid, f.name)
		}
		if utf8.RuneCountInString(f.value) > f.limit {
			return b, fmt.Errorf("%w: %s is longer than %d characters", ErrInvalid, f.name, f.limit)
		}
	}
	if address, err := mail.ParseAddress(b.Email); err != nil || address.Address != b.Email {
		return b, fmt.Errorf("%w: email is not a valid address", ErrInvalid)
	}
	if !countryPattern.MatchString(b.Country) {
		return b, fmt.Errorf("%w: country must be an ISO 3166-1 alpha-2 code (ES, FR…)", ErrInvalid)
	}
	if !taxIDTypePattern.MatchString(b.TaxIDType) {
		return b, fmt.Errorf("%w: taxIdType must be a stripe tax id type (es_cif, eu_vat…)", ErrInvalid)
	}
	return b, nil
}

// StripeRefusalError is Stripe refusing a new customer's details: an invalid tax id, an address
// Stripe Tax cannot locate. Code and Param name what was refused; Msg is Stripe's message, which
// may quote the value, so only Code and Param belong in logs and audit rows. It wraps ErrInvalid.
type StripeRefusalError struct {
	Code, Param, Msg string
}

// Error is the refusal with Stripe's message.
func (e *StripeRefusalError) Error() string {
	return fmt.Sprintf("%s: stripe refused the customer details: %s", ErrInvalid, e.Msg)
}

// Unwrap makes a StripeRefusalError satisfy errors.Is(err, ErrInvalid).
func (*StripeRefusalError) Unwrap() error { return ErrInvalid }

// CreateCustomer registers the Stripe customer of org from b, stamped with org (metadata.address)
// so that checkouts, cmd/topup, Prepare and IssueDeferredInvoice find it. It refuses an org a
// customer is stamped with already (ErrCustomerExists, with that customer), with or without an
// address, and a failed search. Stripe validates the tax id and locates the address at once
// (tax.validate_location immediately): a refusal is a *StripeRefusalError. The create is
// idempotent per org and details for 24 hours (Stripe's Idempotency-Key), so a retry after a
// lost response gets the same customer. Customer search lags behind a new customer: until it
// catches up, the returned id is the only way to name it (Request.CustomerID,
// DeferredInvoice.CustomerID).
func CreateCustomer(ctx context.Context, c *stripe.Client, org common.Address, b BillingDetails) (Customer, error) {
	b, err := ValidateBilling(b)
	if err != nil {
		return Customer{}, err
	}
	found, err := c.GetCustomerByAddress(org.String())
	switch {
	case err == nil:
		return customerOf(found), fmt.Errorf("%w: organization %s is stamped on customer %s", ErrCustomerExists, org, found.ID)
	case !errors.Is(err, stripe.ErrCustomerNotFound):
		return Customer{}, fmt.Errorf("could not search the customer of organization %s, retry later: %w", org, err)
	}
	params := &stripeapi.CustomerParams{
		Name:  new(b.Name),
		Email: new(b.Email),
		Address: &stripeapi.AddressParams{
			Line1: new(b.Line1), City: new(b.City), PostalCode: new(b.PostalCode), Country: new(b.Country),
		},
		TaxIDData: []*stripeapi.CustomerTaxIDDataParams{{Type: new(b.TaxIDType), Value: new(b.TaxIDValue)}},
		Tax:       &stripeapi.CustomerTaxParams{ValidateLocation: new("immediately")},
		Metadata:  map[string]string{"address": org.String()},
	}
	if b.Line2 != "" {
		params.Address.Line2 = new(b.Line2)
	}
	if b.State != "" {
		params.Address.State = new(b.State)
	}
	params.Context = ctx
	params.SetIdempotencyKey(customerIdempotencyKey(org, b))
	customer, err := stripecustomer.New(params)
	if err != nil {
		var stripeErr *stripeapi.Error
		if errors.As(err, &stripeErr) && stripeErr.Type == stripeapi.ErrorTypeInvalidRequest &&
			stripeErr.HTTPStatusCode == http.StatusBadRequest {
			return Customer{}, &StripeRefusalError{Code: string(stripeErr.Code), Param: stripeErr.Param, Msg: stripeErr.Msg}
		}
		return Customer{}, fmt.Errorf("could not create the customer of organization %s: %w", org, err)
	}
	return customerOf(customer), nil
}

// customerIdempotencyKey is "topup-customer:<org>:<16 hex of the sha256 of b's JSON>": the same
// details for the same org are one customer.
func customerIdempotencyKey(org common.Address, b BillingDetails) string {
	canonical, _ := json.Marshal(b) // a struct of strings: never fails
	sum := sha256.Sum256(canonical)
	return "topup-customer:" + org.String() + ":" + hex.EncodeToString(sum[:])[:16]
}
