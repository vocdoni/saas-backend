package api

import (
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestBrandingChargedOncePerOrganization: branding is a once-per-organization add-on, but
// the organization is only stamped as having paid it at fulfillment — so two drafts that
// both select it and both check out before either pays would both be charged €149. The
// live payment holds the claim in the meantime, and a failed payment releases it again.
func TestBrandingChargedOncePerOrganization(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingpass1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	newBrandedProcess := func() string { return newBrandedVotingProcess(t, adminToken, orgAddress) }
	// 15 voters + email 2FA = €15.15; branding adds €149
	const plainCents = int64(1_515)
	const withBrandingCents = plainCents + 14_900

	first, second := newBrandedProcess(), newBrandedProcess()
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// nothing is paid or in flight yet, so the first draft quoted carries branding
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", first, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", first, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)

	// that open payment claims branding for the organization: the second draft still
	// selects it, but it is no longer chargeable — not at quote time, not at checkout
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, plainCents)
	secondCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", second, "checkout")
	c.Assert(secondCheckout.AmountCents, qt.Equals, plainCents)

	// the claim is recorded on the payment, which is what fulfillment reads to decide
	// whether to stamp the organization as having paid branding
	oid, err := bson.ObjectIDFromHex(first)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Branding, qt.IsTrue)
	secondOID, err := bson.ObjectIDFromHex(second)
	c.Assert(err, qt.IsNil)
	secondPayment, err := testDB.ProcessPayment(secondOID)
	c.Assert(err, qt.IsNil)
	c.Assert(secondPayment.Branding, qt.IsFalse)

	// a failed payment releases the claim once it is stale, so the second draft can carry
	// branding again
	released, err := testDB.MarkProcessPaymentFailed(oid, payment.CheckoutSessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(released, qt.IsTrue)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, plainCents)
	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
}

// staleBrandingClaims makes every branding claim stale for the rest of the test, standing in
// for the db.BrandingClaimStaleAfter window passing.
func staleBrandingClaims(t *testing.T) {
	t.Helper()
	restore := db.BrandingClaimStaleAfter
	db.BrandingClaimStaleAfter = -time.Minute
	t.Cleanup(func() { db.BrandingClaimStaleAfter = restore })
}

// TestBrandingClaimReleasedWhenClaimantDropsIt: a draft that claimed branding and then checked
// out again without it no longer pays for the add-on, so its claim must not keep denying
// branding to the rest of the organization.
func TestBrandingClaimReleasedWhenClaimantDropsIt(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingdrop1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	const plainCents = int64(1_515)
	const withBrandingCents = plainCents + 14_900

	claimant, next := newBrandedVotingProcess(t, adminToken, orgAddress), newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", claimant, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)

	// the claimant re-checks out without the add-on: its payment no longer carries branding
	claimantOID, err := bson.ObjectIDFromHex(claimant)
	c.Assert(err, qt.IsNil)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         claimantOID,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_without_branding",
		QuoteHash:         "hash",
		AmountCents:       plainCents,
		Currency:          "eur",
	}, checkout.SessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	// fresh, the claim still stands: the claimant may be about to store a branded payment
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, plainCents)

	// stale and unbacked, it is released
	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
}

// TestBrandingClaimReleasedByAbandonedCheckout covers the claim's other exit: the customer
// opens a branded checkout and walks away. Stripe eventually expires the session, and
// without that event the claim would be held by a payment that can never complete — the
// organization's once-only add-on denied to every later draft, forever.
func TestBrandingClaimReleasedByAbandonedCheckout(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingexpire1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	newBrandedProcess := func() string { return newBrandedVotingProcess(t, adminToken, orgAddress) }
	const plainCents = int64(1_515)
	const withBrandingCents = plainCents + 14_900

	abandoned, next := newBrandedProcess(), newBrandedProcess()
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", abandoned, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)

	// while that session is open the claim holds, so the next draft is quoted without branding
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, plainCents)

	// Stripe expires the abandoned session
	abandonedOID, err := bson.ObjectIDFromHex(abandoned)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(abandonedOID)
	c.Assert(err, qt.IsNil)
	status := postSignedStripeEvent(t, testWebhookSecret, "evt_branding_expired",
		"checkout.session.expired",
		checkoutSessionObject(payment.CheckoutSessionID, "unpaid", withBrandingCents, map[string]string{
			"voting_process_id": abandoned,
		}))
	c.Assert(status, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(abandonedOID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)

	// with the claim released (and stale), the next draft can be charged for branding
	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	nextCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", next, "checkout")
	c.Assert(nextCheckout.AmountCents, qt.Equals, withBrandingCents)
}
