package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestStripeAPI(t *testing.T) {
	c := qt.New(t)

	t.Run("CreateSubscriptionCheckout", func(t *testing.T) {
		// Create a test user and organization
		token := testCreateUser(t, testPass)
		orgAddress := testCreateOrganization(t, token)

		t.Run("ZeroAddressValidation", func(t *testing.T) {
			// Test with zero address - should fail
			checkoutReq := &apicommon.SubscriptionCheckout{
				LookupKey: 1,
				ReturnURL: "https://example.com/return",
				Amount:    1000,
				Address:   common.Address{}, // Zero address
				Locale:    "en",
			}

			resp, status := testRequest(t, http.MethodPost, token, checkoutReq, "subscriptions", "checkout")
			c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))

			// Verify the error message indicates missing required fields
			var errorResp map[string]any
			err := json.Unmarshal(resp, &errorResp)
			c.Assert(err, qt.IsNil)
			c.Assert(errorResp["error"], qt.Contains, "Missing required fields")
		})

		t.Run("ValidAddress", func(t *testing.T) {
			// Test with valid address - should work (though may fail due to Stripe setup)
			checkoutReq := &apicommon.SubscriptionCheckout{
				LookupKey: 1,
				ReturnURL: "https://example.com/return",
				Amount:    1000,
				Address:   orgAddress, // Valid address
				Locale:    "en",
			}

			resp, status := testRequest(t, http.MethodPost, token, checkoutReq, "subscriptions", "checkout")
			// Note: This might fail due to Stripe configuration in tests, but it should not fail due to zero address
			// The important thing is that it doesn't fail with "Missing required fields" error
			if status == http.StatusBadRequest {
				var errorResp map[string]any
				err := json.Unmarshal(resp, &errorResp)
				c.Assert(err, qt.IsNil)
				// Should not be a "Missing required fields" error
				c.Assert(errorResp["error"], qt.Not(qt.Contains), "Missing required fields")
			}
		})

		t.Run("MissingAmount", func(t *testing.T) {
			// Test with zero amount - should fail
			checkoutReq := &apicommon.SubscriptionCheckout{
				LookupKey: 1,
				ReturnURL: "https://example.com/return",
				Amount:    0, // Zero amount
				Address:   orgAddress,
				Locale:    "en",
			}

			resp, status := testRequest(t, http.MethodPost, token, checkoutReq, "subscriptions", "checkout")
			c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("response: %s", resp))

			// Verify the error message indicates missing required fields
			var errorResp map[string]any
			err := json.Unmarshal(resp, &errorResp)
			c.Assert(err, qt.IsNil)
			c.Assert(errorResp["error"], qt.Contains, "Missing required fields")
		})
	})
}
