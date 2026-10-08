package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
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
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
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

	// no authentication at all is refused before reaching Stripe
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, "", nil, "subscriptions", "checkout", "cs_mine")
}
