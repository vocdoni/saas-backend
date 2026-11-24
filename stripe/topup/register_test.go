package topup

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/stripe"
)

// validBilling is a complete set of billing details.
func validBilling() BillingDetails {
	return BillingDetails{
		Name: "Fundació Girona", Email: "billing@girona.test", Line1: "Carrer Major 1", City: "Girona",
		PostalCode: "17001", Country: "ES", TaxIDType: "es_cif", TaxIDValue: "G12345678",
	}
}

// TestValidateBilling (AC2): every field but line2 and state is required, each within its cap;
// the email is a bare address, the country two upper-case letters and the tax id type Stripe's
// shape. The details come back trimmed; every refusal is ErrInvalid.
func TestValidateBilling(t *testing.T) {
	c := qt.New(t)
	padded := validBilling()
	padded.Name, padded.Line2 = " Fundació Girona ", " 2n pis "
	got, err := ValidateBilling(padded)
	c.Assert(err, qt.IsNil)
	want := validBilling()
	want.Line2 = "2n pis"
	c.Assert(got, qt.DeepEquals, want)
	optional := validBilling()
	optional.Line2, optional.State = "", ""
	_, err = ValidateBilling(optional)
	c.Assert(err, qt.IsNil)

	multi := validBilling()
	multi.TaxIDType = "eu_oss_vat"
	_, err = ValidateBilling(multi)
	c.Assert(err, qt.IsNil)

	long := func(n int) string { return strings.Repeat("é", n) }
	for name, mutate := range map[string]func(b *BillingDetails){
		"no name":         func(b *BillingDetails) { b.Name = " " },
		"no email":        func(b *BillingDetails) { b.Email = "" },
		"no line1":        func(b *BillingDetails) { b.Line1 = "" },
		"no city":         func(b *BillingDetails) { b.City = "" },
		"no postal code":  func(b *BillingDetails) { b.PostalCode = "" },
		"no country":      func(b *BillingDetails) { b.Country = "" },
		"no tax id type":  func(b *BillingDetails) { b.TaxIDType = "" },
		"no tax id":       func(b *BillingDetails) { b.TaxIDValue = "" },
		"long name":       func(b *BillingDetails) { b.Name = long(201) },
		"long email":      func(b *BillingDetails) { b.Email = strings.Repeat("a", 245) + "@girona.test" },
		"long line1":      func(b *BillingDetails) { b.Line1 = long(201) },
		"long line2":      func(b *BillingDetails) { b.Line2 = long(201) },
		"long city":       func(b *BillingDetails) { b.City = long(201) },
		"long tax id":     func(b *BillingDetails) { b.TaxIDValue = long(51) },
		"bad email":       func(b *BillingDetails) { b.Email = "billing" },
		"named email":     func(b *BillingDetails) { b.Email = "Billing <billing@girona.test>" },
		"lower country":   func(b *BillingDetails) { b.Country = "es" },
		"long country":    func(b *BillingDetails) { b.Country = "ESP" },
		"upper tax type":  func(b *BillingDetails) { b.TaxIDType = "ES_CIF" },
		"free tax type":   func(b *BillingDetails) { b.TaxIDType = "cif" },
		"tax type inject": func(b *BillingDetails) { b.TaxIDType = "es_cif&x=1" },
	} {
		b := validBilling()
		mutate(&b)
		_, err := ValidateBilling(b)
		c.Assert(err, qt.ErrorIs, ErrInvalid, qt.Commentf("%s", name))
	}
}

// customerFake is a Stripe account for CreateCustomer: the customer search answers stamped (an
// id, "" for none, "fail" for a 500), POST /v1/customers creates cus_<n> unless its tax id is
// "bad" (Stripe's tax_id_invalid) and replays the customer of a known Idempotency-Key, and
// POST /v1/checkout/sessions records the customer it was opened for.
type customerFake struct {
	mu       sync.Mutex
	stamped  string
	created  []map[string]string // the form of every create Stripe executed
	keys     []string            // the Idempotency-Key of every create request
	replays  map[string]string
	sessions []string
}

func (f *customerFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = r.ParseForm()
	switch {
	case r.URL.Path == "/v1/customers/search" && f.stamped == "fail":
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
	case r.URL.Path == "/v1/customers/search":
		data := ""
		if f.stamped != "" {
			data = `{"id":"` + f.stamped + `","object":"customer","metadata":{"address":"` + flowOrg.Hex() + `"}}`
		}
		_, _ = w.Write([]byte(`{"object":"search_result","has_more":false,"data":[` + data + `]}`))
	case r.URL.Path == "/v1/customers" && r.Method == http.MethodPost:
		key := r.Header.Get("Idempotency-Key")
		f.keys = append(f.keys, key)
		if r.PostForm.Get("tax_id_data[0][value]") == "bad" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"tax_id_invalid",` +
				`"param":"tax_id_data[0][value]","message":"Invalid value for es_cif: bad"}}`))
			return
		}
		if r.PostForm.Get("tax_id_data[0][value]") == "denied" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"permission denied"}}`))
			return
		}
		id, ok := f.replays[key]
		if !ok {
			form := map[string]string{}
			for k, v := range r.PostForm {
				form[k] = v[0]
			}
			f.created = append(f.created, form)
			id = "cus_" + string(rune('0'+len(f.created)))
			f.replays[key] = id
		}
		_, _ = w.Write([]byte(`{"id":"` + id + `","object":"customer","name":"` + r.PostForm.Get("name") + `",` +
			`"email":"` + r.PostForm.Get("email") + `","address":{"line1":"` + r.PostForm.Get("address[line1]") + `",` +
			`"country":"` + r.PostForm.Get("address[country]") + `"},` +
			`"metadata":{"address":"` + r.PostForm.Get("metadata[address]") + `"}}`))
	case r.URL.Path == "/v1/checkout/sessions" && r.Method == http.MethodPost:
		f.sessions = append(f.sessions, r.PostForm.Get("customer"))
		_, _ = w.Write([]byte(`{"id":"cs_1","object":"checkout.session"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
	}
}

// TestCreateCustomer (AC2, AC11): the customer is created with the details, the tax id, the
// organization stamp and tax location validated at once, under an Idempotency-Key per org and
// details (the same details again are the same customer; other details another key). An org with
// a customer stamped already is refused with it, a failed search refuses without creating, invalid
// details never reach Stripe, and Stripe's refusal of the tax id is a *StripeRefusalError. A
// checkout opened once the search finds the new customer bills it instead of creating another.
func TestCreateCustomer(t *testing.T) {
	c := qt.New(t)
	f := &customerFake{replays: map[string]string{}}
	client := stubStripe(t, f)
	ctx := context.Background()

	customer, err := CreateCustomer(ctx, client, flowOrg, validBilling())
	c.Assert(err, qt.IsNil)
	c.Assert(customer, qt.DeepEquals, Customer{
		ID: "cus_1", Name: "Fundació Girona", Email: "billing@girona.test", Address: "Carrer Major 1, ES", Country: "ES",
	})
	c.Assert(f.created, qt.DeepEquals, []map[string]string{{
		"name": "Fundació Girona", "email": "billing@girona.test", "address[line1]": "Carrer Major 1",
		"address[city]": "Girona", "address[postal_code]": "17001", "address[country]": "ES",
		"tax_id_data[0][type]": "es_cif", "tax_id_data[0][value]": "G12345678",
		"tax[validate_location]": "immediately", "metadata[address]": flowOrg.String(),
	}})
	c.Assert(f.keys[0], qt.Matches, "topup-customer:"+flowOrg.String()+":[0-9a-f]{16}")

	// a retry with the same details (the response was lost, the search has not caught up): replayed
	again, err := CreateCustomer(ctx, client, flowOrg, validBilling())
	c.Assert(err, qt.IsNil)
	c.Assert(again.ID, qt.Equals, "cus_1")
	c.Assert(f.created, qt.HasLen, 1)
	c.Assert(f.keys[1], qt.Equals, f.keys[0])
	other := validBilling()
	other.Line2 = "2n pis"
	_, err = CreateCustomer(ctx, client, flowOrg, other)
	c.Assert(err, qt.IsNil)
	c.Assert(f.keys[2], qt.Not(qt.Equals), f.keys[0])
	c.Assert(f.created[1]["address[line2]"], qt.Equals, "2n pis")

	bad := validBilling()
	bad.TaxIDValue = "bad"
	_, err = CreateCustomer(ctx, client, flowOrg, bad)
	c.Assert(err, qt.ErrorIs, ErrInvalid)
	var refusal *StripeRefusalError
	c.Assert(errors.As(err, &refusal), qt.IsTrue)
	c.Assert(*refusal, qt.DeepEquals, StripeRefusalError{
		Code: "tax_id_invalid", Param: "tax_id_data[0][value]", Msg: "Invalid value for es_cif: bad",
	})

	// a permission error is a Stripe failure, not the admin's bad details
	denied := validBilling()
	denied.TaxIDValue = "denied"
	_, err = CreateCustomer(ctx, client, flowOrg, denied)
	c.Assert(err, qt.ErrorMatches, "could not create the customer of organization .*")
	c.Assert(errors.Is(err, ErrInvalid), qt.IsFalse)

	requests := len(f.keys)
	invalid := validBilling()
	invalid.Email = "nope"
	_, err = CreateCustomer(ctx, client, flowOrg, invalid)
	c.Assert(err, qt.ErrorIs, ErrInvalid)
	f.stamped = "fail"
	_, err = CreateCustomer(ctx, client, flowOrg, validBilling())
	c.Assert(err, qt.ErrorMatches, "could not search the customer of organization .*")
	c.Assert(errors.Is(err, ErrCustomerExists), qt.IsFalse)
	f.stamped = "cus_1"
	customer, err = CreateCustomer(ctx, client, flowOrg, other)
	c.Assert(err, qt.ErrorIs, ErrCustomerExists)
	c.Assert(customer.ID, qt.Equals, "cus_1")
	c.Assert(f.keys, qt.HasLen, requests)

	// the checkout of the organization reuses the registered customer
	_, err = client.CreatePaymentCheckoutSession(&stripe.PaymentSessionParams{
		OrgAddress: flowOrg.String(), CustomerEmail: "admin@girona.test",
		LineItems: []stripe.PaymentLineItem{{Description: "top-up", AmountCents: 10_000}},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(f.sessions, qt.DeepEquals, []string{"cus_1"})
}
