package stripe

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/db"
)

// stubPortalBackend points the Stripe client at a stub that resolves customers and records
// which customer every billing portal session is created for.
type stubPortalBackend struct {
	// subscriptionCustomer answers GET /v1/subscriptions/{id}; empty means no subscription exists
	subscriptionCustomer string
	// subscriptionAddress is the address metadata of the subscription; empty means the
	// organization under test
	subscriptionAddress string
	// addressCustomer answers the customer search by address metadata; empty means no match
	addressCustomer string
	// emailCustomers answers any customer list by email, in order. The portal must never
	// use it: every case that reaches it would hand out a customer it cannot tie to the org.
	emailCustomers []string
	// portalCustomers records the customer of each portal session created
	portalCustomers []string
}

// portalTestOrg is the organization whose portal the tests open.
var portalTestOrg = common.HexToAddress("0x1234567890123456789012345678901234567890")

func (b *stubPortalBackend) install(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeNotFound := func() {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/subscriptions/sub_live":
			if b.subscriptionCustomer == "" {
				writeNotFound()
				return
			}
			address := b.subscriptionAddress
			if address == "" {
				address = portalTestOrg.Hex()
			}
			_, _ = w.Write([]byte(`{"id":"sub_live","object":"subscription","customer":"` + b.subscriptionCustomer +
				`","metadata":{"address":"` + address + `"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/customers/search":
			data := `[]`
			if b.addressCustomer != "" {
				data = `[{"id":"` + b.addressCustomer + `","object":"customer"}]`
			}
			_, _ = w.Write([]byte(`{"object":"search_result","url":"/v1/customers/search","has_more":false,"data":` + data + `}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/customers":
			data := ""
			for i, id := range b.emailCustomers {
				if i > 0 {
					data += ","
				}
				data += `{"id":"` + id + `","object":"customer","metadata":{}}`
			}
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/customers","has_more":false,"data":[` + data + `]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/billing_portal/sessions":
			if err := r.ParseForm(); err != nil {
				writeNotFound()
				return
			}
			b.portalCustomers = append(b.portalCustomers, r.PostForm.Get("customer"))
			_, _ = w.Write([]byte(`{"id":"bps_1","object":"billing_portal.session","url":"https://portal.example.com"}`))
		default:
			writeNotFound()
		}
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
}

// TestCreatePortalSessionResolvesOrgCustomer pins the portal customer resolution order: the
// customer of the organization's current subscription first, then the customer stamped with
// the organization's address metadata, and nothing else — never a customer matched by email,
// which a creator shares across every organization they create.
func TestCreatePortalSessionResolvesOrgCustomer(t *testing.T) {
	c := qt.New(t)
	orgAddress := portalTestOrg
	service := &Service{client: NewClient(&Config{APIKey: "sk_test_stub", WebhookSecret: "whsec_stub"})}

	orgWithSubscription := &db.Organization{
		Address: orgAddress,
		Creator: "creator@example.com",
		Subscription: db.OrganizationSubscription{
			StripeSubscriptionID: "sub_live",
		},
	}
	orgWithoutSubscription := &db.Organization{
		Address: orgAddress,
		Creator: "creator@example.com",
	}

	c.Run("customer of the current subscription wins", func(c *qt.C) {
		backend := &stubPortalBackend{
			subscriptionCustomer: "cus_subscription",
			addressCustomer:      "cus_address",
			emailCustomers:       []string{"cus_email"},
		}
		backend.install(t)
		session, err := service.CreatePortalSession(orgWithSubscription)
		c.Assert(err, qt.IsNil)
		c.Assert(session.URL, qt.Equals, "https://portal.example.com")
		c.Assert(backend.portalCustomers, qt.DeepEquals, []string{"cus_subscription"})
	})

	c.Run("a stored subscription that cannot be resolved is an error, not a fallback", func(c *qt.C) {
		backend := &stubPortalBackend{
			addressCustomer: "cus_address",
			emailCustomers:  []string{"cus_email"},
		}
		backend.install(t)
		_, err := service.CreatePortalSession(orgWithSubscription)
		c.Assert(err, qt.IsNotNil)
		c.Assert(backend.portalCustomers, qt.HasLen, 0)
	})

	c.Run("a stored subscription now stamped for another organization is refused", func(c *qt.C) {
		backend := &stubPortalBackend{
			subscriptionCustomer: "cus_other_org",
			subscriptionAddress:  "0x00000000000000000000000000000000000000bb",
			addressCustomer:      "cus_address",
		}
		backend.install(t)
		_, err := service.CreatePortalSession(orgWithSubscription)
		c.Assert(err, qt.ErrorMatches, ".*belongs to organization.*")
		c.Assert(backend.portalCustomers, qt.HasLen, 0)
	})

	c.Run("address metadata customer when there is no subscription", func(c *qt.C) {
		backend := &stubPortalBackend{
			addressCustomer: "cus_address",
			emailCustomers:  []string{"cus_email"},
		}
		backend.install(t)
		_, err := service.CreatePortalSession(orgWithoutSubscription)
		c.Assert(err, qt.IsNil)
		c.Assert(backend.portalCustomers, qt.DeepEquals, []string{"cus_address"})
	})

	// creator X owns organizations A and B; B paid for a process, whose one-time checkout left
	// an unstamped customer under X's email. An admin of A (not a member of B) opening A's
	// portal must not be handed that customer, the only one matching the creator's email.
	c.Run("another organization's unstamped customer sharing the creator's email is never used", func(c *qt.C) {
		backend := &stubPortalBackend{
			emailCustomers: []string{"cus_org_b_process_payment"},
		}
		backend.install(t)
		_, err := service.CreatePortalSession(orgWithoutSubscription)
		c.Assert(err, qt.ErrorMatches, ".*not found.*")
		c.Assert(backend.portalCustomers, qt.HasLen, 0)
	})

	c.Run("no customer at all is an error", func(c *qt.C) {
		backend := &stubPortalBackend{}
		backend.install(t)
		_, err := service.CreatePortalSession(orgWithoutSubscription)
		c.Assert(err, qt.ErrorMatches, ".*not found.*")
		c.Assert(backend.portalCustomers, qt.HasLen, 0)
	})
}
