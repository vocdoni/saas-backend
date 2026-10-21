package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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
	const plainCents = pricing.Cents(1_515)
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
	const plainCents = pricing.Cents(1_515)
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

// TestBrandingClaimReleasedByRefusedPayment: a paying attempt wins the branding claim before it
// knows whether it can charge. Refused before any money moves — Stripe down at checkout, a
// managed publish the wallet cannot cover — it must give the claim back at once, or every
// sibling would be priced, charged and published without the branding it selected until the
// claim went stale.
func TestBrandingClaimReleasedByRefusedPayment(t *testing.T) {
	c := qt.New(t)
	gw := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingrefused1234")
	claimedBy := func(orgAddress common.Address) bson.ObjectID {
		org, err := testDB.Organization(orgAddress)
		c.Assert(err, qt.IsNil)
		return org.BrandingClaimedBy
	}

	// card: the checkout session cannot be created
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	refused, sibling := newBrandedVotingProcess(t, adminToken, orgAddress), newBrandedVotingProcess(t, adminToken, orgAddress)
	gw.createErr = fmt.Errorf("stripe unreachable")
	requestAndAssertError(errors.ErrStripeError, t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", refused, "checkout")
	gw.createErr = nil
	c.Assert(claimedBy(orgAddress), qt.Equals, bson.NilObjectID)
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", sibling, "price")
	c.Assert(price.TotalCents, qt.Equals, pricing.Cents(1_515+14_900))

	// wallet: the integrator's balance does not cover the branded price
	_, managedAddr := newIntegratorWithManagedOrg(t, adminToken, "prod_test_branding_refused")
	managed := newBrandedVotingProcess(t, adminToken, managedAddr)
	requestAndAssertError(errors.ErrInsufficientWalletBalance, t, http.MethodPost, adminToken, nil,
		"processes", managed, "publish")
	c.Assert(claimedBy(managedAddr), qt.Equals, bson.NilObjectID)
}

// TestBrandingClaimReleasedByDroppedCheckout: cancelling a branded checkout, or deleting its
// draft, frees the branding claim at once. Left to go stale, it would make a sibling's checkout
// in the next two minutes sell the sibling without the branding it selected — and a paid
// process can never have it added.
func TestBrandingClaimReleasedByDroppedCheckout(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingcancel1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}
	const withBrandingCents = pricing.Cents(1_515 + 14_900)
	checkoutCents := func(pid string) pricing.Cents {
		return requestAndParse[apicommon.ProcessCheckoutResponse](
			t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout").AmountCents
	}

	cancelled, deleted, next := newBrandedVotingProcess(t, adminToken, orgAddress),
		newBrandedVotingProcess(t, adminToken, orgAddress), newBrandedVotingProcess(t, adminToken, orgAddress)
	c.Assert(checkoutCents(cancelled), qt.Equals, withBrandingCents)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", cancelled, "checkout")
	c.Assert(checkoutCents(deleted), qt.Equals, withBrandingCents)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", deleted)
	c.Assert(checkoutCents(next), qt.Equals, withBrandingCents)
}

// TestCancelCheckoutExcludesConcurrentCheckout: cancel releases the branding claim of the checkout
// it drops, so a checkout of the same draft must not store a new session — with the claim refreshed
// for it — in between. Both take the draft's publish claim: the racing checkout is refused, the
// cancel completes, and the claim it releases is one no live session relies on.
func TestCancelCheckoutExcludesConcurrentCheckout(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "cancelrace123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}
	const withBrandingCents = pricing.Cents(1_515 + 14_900)

	cancelled, sibling := newBrandedVotingProcess(t, adminToken, orgAddress), newBrandedVotingProcess(t, adminToken, orgAddress)
	requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", cancelled, "checkout")

	racingCode := 0
	fake.expireFn = func(string) {
		_, racingCode = testRequest(t, http.MethodPost, adminToken, checkoutReq, "processes", cancelled, "checkout")
	}
	status := requestAndParse[apicommon.ProcessPaymentStatusResponse](
		t, http.MethodDelete, adminToken, nil, "processes", cancelled, "checkout")
	c.Assert(racingCode, qt.Equals, http.StatusConflict)
	c.Assert(status.Status, qt.Equals, db.ProcessPaymentFailed)

	// the cancelled draft holds no live session, so the claim it released is free for the sibling
	oid, err := bson.ObjectIDFromHex(cancelled)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentFailed)
	next := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", sibling, "checkout")
	c.Assert(next.AmountCents, qt.Equals, withBrandingCents)
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
	const plainCents = pricing.Cents(1_515)
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

// TestPaidDraftBrandingFixedAtPayment: a paid draft's branding is what its payment bought. A
// draft that paid without it (a sibling held the claim) must not be re-priced with it once the
// claim frees up, and an edit must not turn it on — nothing sells the €149 to a paid process,
// so either would strand the draft behind a 402 no endpoint can settle.
func TestPaidDraftBrandingFixedAtPayment(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "brandingfixed1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}
	const plainCents = pricing.Cents(1_515)

	payAsCheckedOut := func(pid string) bson.ObjectID {
		checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
			t, http.MethodPost, token, checkoutReq, "processes", pid, "checkout")
		c.Assert(checkout.AmountCents, qt.Equals, plainCents)
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{})
		c.Assert(err, qt.IsNil)
		c.Assert(won, qt.IsTrue)
		return oid
	}

	// the sibling holds the claim, so the late draft pays without branding
	holder, late := newBrandedVotingProcess(t, token, orgAddress), newBrandedVotingProcess(t, token, orgAddress)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, checkoutReq, "processes", holder, "checkout")
	payAsCheckedOut(late)

	// the sibling's payment fails and its claim goes stale: branding is claimable again
	holderOID, err := bson.ObjectIDFromHex(holder)
	c.Assert(err, qt.IsNil)
	holderPayment, err := testDB.ProcessPayment(holderOID)
	c.Assert(err, qt.IsNil)
	_, err = testDB.MarkProcessPaymentFailed(holderOID, holderPayment.CheckoutSessionID)
	c.Assert(err, qt.IsNil)
	staleBrandingClaims(t)

	// but not by a draft that already paid without it: its price stays what it paid
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", late, "price")
	c.Assert(price.TotalCents, qt.Equals, plainCents)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", late, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// a draft paid without branding cannot turn it on
	ids := postNewOrgMembers(t, token, orgAddress, 15)
	plain := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint).ProcessID
	payAsCheckedOut(plain)
	branded := newVotingProcessRequest(orgAddress, ids)
	branded.AddOns = db.ProcessAddOns{Branding: true}
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPut, token, branded, "processes", plain)
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		newVotingProcessRequest(orgAddress, ids), "processes", plain)
}
