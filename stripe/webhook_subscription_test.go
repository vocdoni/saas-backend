package stripe

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
)

// subscriptionEvent builds a subscription webhook event whose payload is the given JSON.
func subscriptionEvent(raw string) *stripeapi.Event {
	return &stripeapi.Event{
		ID:   "evt_test",
		Type: stripeapi.EventTypeCustomerSubscriptionUpdated,
		Data: &stripeapi.EventData{Raw: json.RawMessage(raw)},
	}
}

// stubLiveSubscription points the Stripe client at a stub serving GET /v1/subscriptions/{id}
// with the given body, calling onFetch first. It returns the number of fetches made.
func stubLiveSubscription(t *testing.T, body string, onFetch func()) *atomic.Int32 {
	t.Helper()
	fetches := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/subscriptions/sub_1" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
			return
		}
		fetches.Add(1)
		if onFetch != nil {
			onFetch()
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	return fetches
}

// TestHandleSubscriptionEventRouting pins what a subscription webhook event is trusted for:
// only its subscription id and organization address. A payload that lacks them can never be
// fixed by a retry (event payloads are immutable), so it is dropped as permanent instead of
// having Stripe redeliver it for three days; the live subscription is fetched while the
// organization lock is held, so concurrent events of one subscription cannot save their
// states out of order.
func TestHandleSubscriptionEventRouting(t *testing.T) {
	c := qt.New(t)
	orgA := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	orgB := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	newService := func() *Service {
		return &Service{
			client:      NewClient(&Config{APIKey: "sk_test_stub", WebhookSecret: "whsec_stub"}),
			lockManager: NewLockManager(),
		}
	}
	liveSub := func(addr common.Address) string {
		return `{"id":"sub_1","object":"subscription","status":"active","customer":"cus_1",` +
			`"metadata":{"address":"` + addr.Hex() + `"},"items":{"object":"list","data":[{"id":"si_1",` +
			`"plan":{"id":"plan_1","product":"prod_1"},"price":{"id":"price_1","type":"recurring",` +
			`"recurring":{"interval":"month"}}}]}}`
	}

	c.Run("payload without address is permanent and not fetched", func(c *qt.C) {
		fetches := stubLiveSubscription(t, liveSub(orgA), nil)
		err := newService().HandleEvent(subscriptionEvent(`{"id":"sub_1","object":"subscription"}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
		c.Assert(fetches.Load(), qt.Equals, int32(0))
	})

	c.Run("payload without id is permanent", func(c *qt.C) {
		err := newService().HandleEvent(subscriptionEvent(
			`{"object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
	})

	c.Run("stale payload fields are not required", func(c *qt.C) {
		// the payload has no items, customer nor price: only its id and address are used, so
		// the handler reaches the live fetch — whose subscription, now stamped for another
		// organization, is then dropped rather than written under the wrong lock
		stubLiveSubscription(t, liveSub(orgB), nil)
		err := newService().HandleEvent(subscriptionEvent(
			`{"id":"sub_1","object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
		c.Assert(err, qt.ErrorMatches, ".*now belongs to organization.*")
	})

	c.Run("live subscription missing its price is permanent", func(c *qt.C) {
		stubLiveSubscription(t, `{"id":"sub_1","object":"subscription","status":"active","customer":"cus_1",`+
			`"metadata":{"address":"`+orgA.Hex()+`"},"items":{"object":"list","data":[{"id":"si_1",`+
			`"plan":{"id":"plan_1","product":"prod_1"}}]}}`, nil)
		err := newService().HandleEvent(subscriptionEvent(
			`{"id":"sub_1","object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
	})

	c.Run("a subscription Stripe says does not exist is permanent", func(c *qt.C) {
		stubLiveSubscription(t, liveSub(orgA), nil)
		err := newService().HandleEvent(subscriptionEvent(
			`{"id":"sub_missing","object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
		c.Assert(err, qt.ErrorIs, errSubscriptionNotFound)
	})

	c.Run("a failed live fetch is retryable", func(c *qt.C) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"rate_limit"}}`))
		}))
		t.Cleanup(srv.Close)
		previous := stripeapi.GetBackend(stripeapi.APIBackend)
		stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
			&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
		t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
		err := newService().HandleEvent(subscriptionEvent(
			`{"id":"sub_1","object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.IsNotNil)
		c.Assert(err, qt.Not(qt.ErrorIs), errPermanentEvent)
	})

	c.Run("live state is fetched under the organization lock", func(c *qt.C) {
		service := newService()
		var heldDuringFetch atomic.Bool
		stubLiveSubscription(t, liveSub(orgB), func() {
			stored, ok := service.lockManager.locks.Load(orgA)
			if !ok {
				return
			}
			lock, ok := stored.(*sync.Mutex)
			if !ok {
				return
			}
			// a held lock cannot be taken: TryLock fails while the handler holds it
			if !lock.TryLock() {
				heldDuringFetch.Store(true)
			} else {
				lock.Unlock()
			}
		})
		err := service.HandleEvent(subscriptionEvent(
			`{"id":"sub_1","object":"subscription","metadata":{"address":"` + orgA.Hex() + `"}}`))
		c.Assert(err, qt.ErrorIs, errPermanentEvent)
		c.Assert(heldDuringFetch.Load(), qt.IsTrue)
	})
}
