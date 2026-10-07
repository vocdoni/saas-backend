package stripe

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestRefundProcessPaymentKeyMovesPastFailedRefund: Stripe replays a reused idempotency key's
// first response for 24 hours, so a retry after a refund that went pending and then failed
// must not reuse its key — it would get that stale refund back as success and return nothing.
func TestRefundProcessPaymentKeyMovesPastFailedRefund(t *testing.T) {
	c := qt.New(t)
	processID := bson.NewObjectID()
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// one refund of this process already failed; another process's is not ours to count
			_, _ = fmt.Fprintf(w, `{"object":"list","has_more":false,"url":"/v1/refunds","data":[
				{"id":"re_failed","object":"refund","status":"failed","metadata":{%q:%q}},
				{"id":"re_other","object":"refund","status":"failed","metadata":{%q:"other"}}]}`,
				MetadataKeyProcessID, processID.Hex(), MetadataKeyProcessID)
			return
		}
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"re_new","object":"refund","status":"pending"}`))
	}))
	defer srv.Close()
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend,
		stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(srv.URL)}))
	defer stripeapi.SetBackend(stripeapi.APIBackend, previous)

	refund, err := (&Service{}).RefundProcessPayment(processID, "pi_1", 0)
	c.Assert(err, qt.IsNil)
	c.Assert(refund.ID, qt.Equals, "re_new")
	c.Assert(keys, qt.DeepEquals, []string{"refund:" + processID.Hex() + ":pi_1:retry1"})
}

// TestRefundProcessPaymentRefusedForGood: an invalid request (a disputed charge, an amount the
// intent no longer holds) is final, and is told apart from a failure a retry may get past, so
// the caller can tell a refund that will never happen from one that has not happened yet.
func TestRefundProcessPaymentRefusedForGood(t *testing.T) {
	c := qt.New(t)
	answer := func(status int, body string) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"object":"list","has_more":false,"url":"/v1/refunds","data":[]}`))
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		t.Cleanup(srv.Close)
		stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
			&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	}
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	defer stripeapi.SetBackend(stripeapi.APIBackend, previous)

	answer(http.StatusBadRequest, `{"error":{"type":"invalid_request_error","code":"charge_disputed"}}`)
	_, err := (&Service{}).RefundProcessPayment(bson.NewObjectID(), "pi_disputed", 0)
	c.Assert(err, qt.ErrorIs, ErrRefundRefused)

	answer(http.StatusTooManyRequests, `{"error":{"type":"invalid_request_error","code":"rate_limit"}}`)
	_, err = (&Service{}).RefundProcessPayment(bson.NewObjectID(), "pi_busy", 0)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err, qt.Not(qt.ErrorIs), ErrRefundRefused)
}
