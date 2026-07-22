package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestStripeHandlersDegradedMode verifies that the Stripe handlers which do not
// otherwise guard against an uninitialized service return a clean error instead
// of panicking when the Stripe service failed to initialize at boot (M14).
func TestStripeHandlersDegradedMode(t *testing.T) {
	c := qt.New(t)

	// a nil *StripeHandlers models the degraded mode where InitializeStripeService
	// failed but the routes were still registered
	var h *StripeHandlers

	t.Run("GetCheckoutSession", func(*testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/subscriptions/checkout/sess_123", nil)
		w := httptest.NewRecorder()
		// must not panic
		h.GetCheckoutSession(w, req)
		c.Assert(w.Code, qt.Equals, http.StatusInternalServerError)
	})

	t.Run("CreateSubscriptionPortalSession", func(*testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/subscriptions/0xabc/portal", nil)
		w := httptest.NewRecorder()
		// the guard runs before any use of the *API argument, so nil is safe here
		h.CreateSubscriptionPortalSession(w, req, nil)
		c.Assert(w.Code, qt.Equals, http.StatusInternalServerError)
	})
}
