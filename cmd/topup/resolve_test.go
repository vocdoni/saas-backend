package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
)

// fakeUsers is a userStore of the users keyed by (normalized) email; "boom@org.test" fails.
type fakeUsers map[string]*db.User

func (f fakeUsers) UserByEmail(email string) (*db.User, error) {
	if email == "boom@org.test" {
		return nil, fmt.Errorf("mongo down")
	}
	if u, ok := f[email]; ok {
		return u, nil
	}
	return nil, db.ErrNotFound
}

// TestResolveCustomer: no customer is created, ever. With none stamped for the organization the
// command refuses unless -customer names one, which is stamped (not in a dry run) and must not
// belong to another organization nor lack an address, and must be tied to the organization: a
// checkout session of the organization used it (sessions of other organizations alone refuse
// it, whatever the email), or it has no checkout session and its email is an Admin of the
// organization (not a manager, not a stranger, not an admin who is also a member of another
// organization, nor unknown when the lookup fails); a refused run writes nothing. One already
// stamped is used as is, and never swapped for an unstamped one -customer names, though
// -customer may pick another customer stamped with the same organization. A failed search
// refuses the run instead of passing for "none stamped".
func TestResolveCustomer(t *testing.T) {
	c := qt.New(t)
	org, other := common.Address{0x01}, common.Address{0x02}
	users := fakeUsers{
		"admin@org.test":   {Email: "admin@org.test", Organizations: []db.OrganizationUser{{Address: org, Role: db.AdminRole}}},
		"manager@org.test": {Email: "manager@org.test", Organizations: []db.OrganizationUser{{Address: org, Role: db.ManagerRole}}},
		"other@org.test":   {Email: "other@org.test", Organizations: []db.OrganizationUser{{Address: other, Role: db.AdminRole}}},
		"both@org.test": {Email: "both@org.test", Organizations: []db.OrganizationUser{
			{Address: org, Role: db.AdminRole}, {Address: other, Role: db.ManagerRole},
		}},
	}
	// email is the billing email the stub gives a customer, by id; the default is the admin's
	email := func(id string) string {
		switch id {
		case "cus_manager":
			return "manager@org.test"
		case "cus_otheradmin":
			return "other@org.test"
		case "cus_stranger", "cus_session", "cus_othersession":
			return "stranger@example.com"
		case "cus_dberr":
			return "boom@org.test"
		case "cus_both":
			return "both@org.test"
		}
		return "Admin@Org.test"
	}
	// sessions is the checkout sessions the stub lists for a customer: the wallet_topup_org of each
	sessions := func(id string) string {
		session := func(o common.Address) string {
			return `{"id":"cs_` + o.Hex() + `","object":"checkout.session","metadata":{"` +
				stripe.MetadataKeyWalletTopUpOrg + `":"` + o.Hex() + `"}}`
		}
		switch id {
		case "cus_session":
			return session(other) + "," + session(org)
		case "cus_othersession":
			return session(other)
		}
		return ""
	}
	stamped := map[string]string{}
	searchHit := "" // the customer the search finds for the organization, when any
	searchFails := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := path.Base(r.URL.Path)
		switch {
		case r.URL.Path == "/v1/customers/search" && searchFails:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error"}}`))
		case r.URL.Path == "/v1/customers/search":
			data := "[]"
			if searchHit != "" {
				data = `[{"id":"` + searchHit + `","object":"customer","address":{"country":"ES"}}]`
			}
			_, _ = w.Write([]byte(`{"object":"search_result","url":"/v1/customers/search","has_more":false,"data":` + data + `}`))
		case r.URL.Path == "/v1/checkout/sessions":
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/checkout/sessions","has_more":false,"data":[` +
				sessions(r.URL.Query().Get("customer")) + `]}`))
		case !strings.HasPrefix(r.URL.Path, "/v1/customers/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		case r.Method == http.MethodPost:
			c.Assert(r.ParseForm(), qt.IsNil)
			stamped[id] = r.PostForm.Get("metadata[address]")
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"customer","address":{"country":"ES"}}`))
		case id == "cus_other":
			_, _ = w.Write([]byte(`{"id":"cus_other","object":"customer","address":{"country":"ES"},` +
				`"metadata":{"address":"` + other.String() + `"}}`))
		case id == "cus_noaddr":
			_, _ = w.Write([]byte(`{"id":"cus_noaddr","object":"customer","metadata":{}}`))
		case id == "cus_same":
			_, _ = w.Write([]byte(`{"id":"cus_same","object":"customer","address":{"country":"ES"},` +
				`"metadata":{"address":"` + org.String() + `"}}`))
		default:
			_, _ = w.Write([]byte(`{"id":"` + id + `","object":"customer","email":"` + email(id) + `",` +
				`"address":{"country":"ES"},"metadata":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	client := stripe.NewClient(&stripe.Config{APIKey: "sk_test_stub"})
	resolve := func(customerID string, write bool) (*stripeapi.Customer, error) {
		return resolveCustomer(customerQuery{client: client, users: users, org: org, customerID: customerID, write: write})
	}

	_, err := resolve("", true)
	c.Assert(err, qt.ErrorMatches, "no stripe customer is stamped with organization.*")
	_, err = resolve("cus_other", true)
	c.Assert(err, qt.ErrorMatches, "customer cus_other belongs to organization.*")
	_, err = resolve("cus_noaddr", true)
	c.Assert(err, qt.ErrorMatches, "customer cus_noaddr has no address.*")
	// unstamped: its email must be an admin of this organization, or the operator stamps it by hand
	for _, id := range []string{"cus_manager", "cus_otheradmin", "cus_stranger"} {
		_, err = resolve(id, true)
		c.Assert(err, qt.ErrorMatches, "customer "+id+" <.*> is not an admin of organization .*stamp metadata.address.*",
			qt.Commentf("-customer %q", id))
	}
	_, err = resolve("cus_dberr", true)
	c.Assert(err, qt.ErrorMatches, "could not look up the user of customer cus_dberr.*")
	// an admin of this organization who is also a member of another: the email vouches for neither
	_, err = resolve("cus_both", true)
	c.Assert(err, qt.ErrorMatches, "customer cus_both <.*> is an admin of organization .* but also a member of .*"+
		"stamp metadata.address.*")
	// used by another organization's checkouts: refused even though its email is this organization's admin
	_, err = resolve("cus_othersession", true)
	c.Assert(err, qt.ErrorMatches, "customer cus_othersession was created by checkouts of "+other.Hex()+
		", not of organization .*")
	c.Assert(stamped, qt.HasLen, 0)

	// used by a checkout of this organization: taken and stamped, whatever its email
	customer, err := resolve("cus_session", true)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_session")
	c.Assert(stamped, qt.DeepEquals, map[string]string{"cus_session": org.String()})
	delete(stamped, "cus_session")

	customer, err = resolve("cus_dry", false)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_dry")
	c.Assert(stamped, qt.HasLen, 0)
	customer, err = resolve("cus_new", true)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_new")
	c.Assert(stamped, qt.DeepEquals, map[string]string{"cus_new": org.String()})

	searchHit = "cus_found"
	customer, err = resolve("", true)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_found")
	customer, err = resolve("cus_found", true)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_found")
	_, err = resolve("cus_new", true)
	c.Assert(err, qt.ErrorMatches, "organization .* is stamped on customer cus_found, not cus_new.*")
	_, err = resolve("cus_other", true)
	c.Assert(err, qt.ErrorMatches, "customer cus_other belongs to organization.*")
	// a second customer stamped with the same organization: taken as named, not stamped again
	customer, err = resolve("cus_same", true)
	c.Assert(err, qt.IsNil)
	c.Assert(customer.ID, qt.Equals, "cus_same")
	c.Assert(stamped, qt.HasLen, 1)

	// the search failing is not "none stamped": nothing is taken, nothing is written
	searchFails = true
	for _, id := range []string{"", "cus_new", "cus_same"} {
		_, err = resolve(id, true)
		c.Assert(err, qt.ErrorMatches, "could not search the customer of organization.*", qt.Commentf("-customer %q", id))
	}
	c.Assert(stamped, qt.HasLen, 1)
}
