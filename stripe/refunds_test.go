package stripe

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v86"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestRefundProcessPaymentReusesLiveRefund: a retried refund finds the one it already issued —
// past Stripe's idempotency window too — and reports that refund, not a new one. Failed or
// canceled refunds, and those of other processes, do not count.
func TestRefundProcessPaymentReusesLiveRefund(t *testing.T) {
	c := qt.New(t)
	processID := bson.NewObjectID()
	var created int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			created++
			_, _ = w.Write([]byte(`{"id":"re_new","object":"refund","status":"succeeded"}`))
			return
		}
		c.Check(r.URL.Query().Get("payment_intent"), qt.Equals, "pi_1")
		_, _ = w.Write([]byte(`{"object":"list","has_more":false,"url":"/v1/refunds","data":[
			{"id":"re_failed","object":"refund","status":"failed","metadata":{"voting_process_id":"` + processID.Hex() + `"}},
			{"id":"re_other","object":"refund","status":"succeeded","metadata":{"voting_process_id":"` + bson.NewObjectID().Hex() + `"}},
			{"id":"re_plain","object":"refund","status":"succeeded","metadata":{}},
			{"id":"re_live","object":"refund","status":"pending","metadata":{"voting_process_id":"` + processID.Hex() + `"}}]}`))
	}))
	defer srv.Close()
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend,
		stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(srv.URL)}))
	defer stripeapi.SetBackend(stripeapi.APIBackend, previous)

	refund, err := (&Service{}).RefundProcessPayment(processID, "pi_1", 1_000)
	c.Assert(err, qt.IsNil)
	c.Assert(*refund, qt.Equals, RefundInfo{ID: "re_live", Status: "pending"})
	c.Assert(created, qt.Equals, 0)

	// another process on the same intent holds nothing live, so it gets a refund of its own
	refund, err = (&Service{}).RefundProcessPayment(bson.NewObjectID(), "pi_1", 0)
	c.Assert(err, qt.IsNil)
	c.Assert(refund.ID, qt.Equals, "re_new")
	c.Assert(created, qt.Equals, 1)
}
