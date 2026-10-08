package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/stripe"
)

// TestGetCheckoutSessionAuthorization pins the ownership rule of
// GET /subscriptions/checkout/{sessionId}: only an admin of the organization the session was
// created for may read it, and every refusal — another user's session, a session without an
// organization, a session that does not exist — is the same 404, so session existence is
// never revealed.
func TestGetCheckoutSessionAuthorization(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, adminToken)
	strangerToken := testCreateUser(t, testPass)

	service := newStripeService(t)
	previous := testAPI.stripeHandlers
	testAPI.stripeHandlers = NewStripeHandlers(service)
	t.Cleanup(func() { testAPI.stripeHandlers = previous })

	// serve the sessions from a stub Stripe backend: one stamped with the organization
	// address (as CreateCheckoutSession stamps them) and one without any organization
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/checkout/sessions/cs_mine":
			_, _ = w.Write([]byte(`{"id":"cs_mine","object":"checkout.session","status":"complete",` +
				`"payment_status":"paid","customer_details":{"email":"creator@example.com"},` +
				`"metadata":{"address":"` + orgAddress.String() + `"}}`))
		case "/v1/checkout/sessions/cs_orphan":
			_, _ = w.Write([]byte(`{"id":"cs_orphan","object":"checkout.session","status":"complete",` +
				`"payment_status":"paid"}`))
		case "/v1/checkout/sessions/cs_outage":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"type":"api_error","message":"stripe internal detail"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing",` +
				`"message":"No such checkout.session: 'cs_missing'"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	previousBackend := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previousBackend) })

	// the organization admin reads the session status, without the org address leaking out
	status := requestAndParse[stripe.CheckoutSessionStatus](
		t, http.MethodGet, adminToken, nil, "subscriptions", "checkout", "cs_mine")
	c.Assert(status.Status, qt.Equals, "complete")
	c.Assert(status.CustomerEmail, qt.Equals, "creator@example.com")
	c.Assert(status.OrgAddress, qt.Equals, "")

	// another authenticated user is told the session does not exist
	requestAndAssertError(errors.ErrCheckoutSessionNotFound,
		t, http.MethodGet, strangerToken, nil, "subscriptions", "checkout", "cs_mine")

	// a session with no recorded organization is hidden even from the admin
	requestAndAssertError(errors.ErrCheckoutSessionNotFound,
		t, http.MethodGet, adminToken, nil, "subscriptions", "checkout", "cs_orphan")

	// a session that does not exist answers with the same error
	requestAndAssertError(errors.ErrCheckoutSessionNotFound,
		t, http.MethodGet, adminToken, nil, "subscriptions", "checkout", "cs_missing")

	// a Stripe failure other than a missing session is a generic 5xx, without Stripe's message
	outage, outageCode := testRequest(t, http.MethodGet, adminToken, nil, "subscriptions", "checkout", "cs_outage")
	c.Assert(outageCode, qt.Equals, errors.ErrStripeError.HTTPstatus)
	c.Assert(string(outage), qt.Not(qt.Contains), "stripe internal detail")

	// byte for byte: echoing Stripe's "No such checkout.session" would tell them apart
	foreign, foreignCode := testRequest(t, http.MethodGet, strangerToken, nil, "subscriptions", "checkout", "cs_mine")
	missing, missingCode := testRequest(t, http.MethodGet, strangerToken, nil, "subscriptions", "checkout", "cs_missing")
	c.Assert(foreignCode, qt.Equals, http.StatusNotFound)
	c.Assert(missingCode, qt.Equals, http.StatusNotFound)
	c.Assert(string(missing), qt.Equals, string(foreign))

	// no authentication at all is refused before reaching Stripe
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, "", nil, "subscriptions", "checkout", "cs_mine")
}

// TestCreateSubscriptionCheckoutRefusals pins what POST /subscriptions/checkout refuses before
// reaching Stripe: a private plan the organization is not on (answered like an unknown plan),
// and any checkout while the organization has a paid subscription, whose plan changes go
// through the billing portal.
func TestCreateSubscriptionCheckoutRefusals(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, adminToken)

	service := newStripeService(t)
	previous := testAPI.stripeHandlers
	testAPI.stripeHandlers = NewStripeHandlers(service)
	t.Cleanup(func() { testAPI.stripeHandlers = previous })
	// no subscriptions and no checkout sessions: a request that gets past the checks fails at Stripe
	stubStripeSubscriptionBackend(t, map[string]*stripeapi.Subscription{})

	privatePlan := &db.Plan{
		ID:                   "prod_test_private",
		Name:                 "Private",
		StripeMonthlyPriceID: "price_month_test_private",
		StripeYearlyPriceID:  "price_year_test_private",
		Public:               false,
	}
	c.Assert(testDB.SetPlan(privatePlan), qt.IsNil)
	t.Cleanup(func() { c.Assert(testDB.DelPlan(privatePlan), qt.IsNil) })

	checkout := func(planID string) *apicommon.SubscriptionCheckout {
		return &apicommon.SubscriptionCheckout{
			LookupKey:     planID,
			BillingPeriod: string(db.BillingPeriodMonthly),
			Address:       orgAddress,
		}
	}

	// a private plan the organization is not on is an unknown plan
	requestAndAssertError(errors.ErrPlanNotFound,
		t, http.MethodPost, adminToken, checkout(privatePlan.ID), "subscriptions", "checkout")
	requestAndAssertError(errors.ErrPlanNotFound,
		t, http.MethodPost, adminToken, checkout("prod_test_missing"), "subscriptions", "checkout")

	// on the private plan (without a paid subscription) the checks pass, and the stub Stripe fails
	setOrganizationSubscription(t, orgAddress, privatePlan.ID)
	requestAndAssertError(errors.ErrStripeError,
		t, http.MethodPost, adminToken, checkout(privatePlan.ID), "subscriptions", "checkout")

	// with a paid subscription, no new checkout, for any plan
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:               mockEssentialPlan.ID,
		StripeSubscriptionID: "sub_paid",
		Active:               true,
	}), qt.IsNil)
	requestAndAssertError(errors.ErrOrganizationAlreadySubscribed,
		t, http.MethodPost, adminToken, checkout(mockPremiumPlan.ID), "subscriptions", "checkout")
}

// TestSubscriptionPortalErrorIsGeneric pins that a failure to open the billing portal answers a
// generic error: the detail can name another organization and stays in the log.
func TestSubscriptionPortalErrorIsGeneric(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, adminToken)
	otherOrg := common.Address{0xde, 0xad}

	service := newStripeService(t)
	previous := testAPI.stripeHandlers
	testAPI.stripeHandlers = NewStripeHandlers(service)
	t.Cleanup(func() { testAPI.stripeHandlers = previous })

	// the organization points at a subscription stamped for another organization
	foreign := mockStripeSubscription(otherOrg, mockEssentialPlan.ID)
	stubStripeSubscriptionBackend(t, map[string]*stripeapi.Subscription{foreign.ID: foreign})
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:               mockEssentialPlan.ID,
		StripeSubscriptionID: foreign.ID,
		Active:               true,
	}), qt.IsNil)

	resp, code := testRequest(t, http.MethodGet, adminToken, nil, "subscriptions", orgAddress.String(), "portal")
	c.Assert(code, qt.Equals, errors.ErrStripeError.HTTPstatus)
	c.Assert(string(resp), qt.Not(qt.Contains), foreign.ID)
	c.Assert(string(resp), qt.Not(qt.Contains), otherOrg.String())
}
