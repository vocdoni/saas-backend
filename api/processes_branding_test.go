package api

import (
	"context"
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
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestBrandingChargedOncePerOrganization: branding is a once-per-organization add-on, but
// the organization is only stamped as having paid it at fulfillment — so two drafts that
// both select it and both check out before either pays would both be charged €149. The
// live payment holds the claim in the meantime: the sibling stays priced with branding and
// cannot pay until the claim is released.
func TestBrandingChargedOncePerOrganization(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "brandingpass1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	newBrandedProcess := func() string { return newBrandedVotingProcess(t, adminToken, orgAddress) }
	// 26 voters, email 2FA free = €17.16; branding adds €149
	const plainCents = pricedProcessCents
	const withBrandingCents = plainCents + pricing.BrandingCents

	first, second := newBrandedProcess(), newBrandedProcess()
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// nothing is paid or in flight yet, so the first draft quoted carries branding
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", first, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", first, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)

	// that open payment claims branding for the organization: the second draft stays priced
	// with it, names the holder, and cannot pay until the holder pays or lets go
	heldByFirst := map[string]string{string(db.OrgAddOnBranding): first}
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, heldByFirst)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken, checkoutReq,
		"processes", second, "checkout")

	// the claim is recorded on the payment, which is what fulfillment reads to decide
	// whether to stamp the organization as having paid branding
	oid, err := bson.ObjectIDFromHex(first)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Branding, qt.IsTrue)
	secondOID, err := bson.ObjectIDFromHex(second)
	c.Assert(err, qt.IsNil)
	_, err = testDB.ProcessPayment(secondOID)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a dropped checkout releases the claim once it is stale, so the second draft can pay for
	// branding
	released, err := testDB.DeleteProcessPaymentCheckout(oid, payment.CheckoutSessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(released, qt.IsTrue)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, heldByFirst)
	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.HasLen, 0)
	secondCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", second, "checkout")
	c.Assert(secondCheckout.AmountCents, qt.Equals, withBrandingCents)
}

// staleBrandingClaims makes every branding claim stale for the rest of the test, standing in
// for the db.OrgAddOnClaimStaleAfter window passing.
func staleBrandingClaims(t *testing.T) {
	t.Helper()
	restore := db.OrgAddOnClaimStaleAfter
	db.OrgAddOnClaimStaleAfter = -time.Minute
	t.Cleanup(func() { db.OrgAddOnClaimStaleAfter = restore })
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
	const plainCents = pricedProcessCents
	const withBrandingCents = plainCents + pricing.BrandingCents

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
		AmountCents:       plainCents,
		TopUpCents:        plainCents,
		Currency:          "eur",
	}, checkout.SessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	// fresh, the claim still stands: the claimant may be about to store a branded payment
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, map[string]string{string(db.OrgAddOnBranding): claimant})

	// stale and unbacked, it is released
	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.HasLen, 0)
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
	c.Assert(price.TotalCents, qt.Equals, pricedProcessCents+pricing.BrandingCents)

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
	const withBrandingCents = pricedProcessCents + pricing.BrandingCents
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
	const withBrandingCents = pricedProcessCents + pricing.BrandingCents

	cancelled, sibling := newBrandedVotingProcess(t, adminToken, orgAddress), newBrandedVotingProcess(t, adminToken, orgAddress)
	requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", cancelled, "checkout")

	racingCode := 0
	fake.expireFn = func(string) {
		_, racingCode = testRequest(t, http.MethodPost, adminToken, checkoutReq, "processes", cancelled, "checkout")
	}
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", cancelled, "checkout")
	c.Assert(racingCode, qt.Equals, http.StatusConflict)

	// the cancelled draft holds no live session, so the claim it released is free for the sibling
	oid, err := bson.ObjectIDFromHex(cancelled)
	c.Assert(err, qt.IsNil)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
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
	const plainCents = pricedProcessCents
	const withBrandingCents = plainCents + pricing.BrandingCents

	abandoned, next := newBrandedProcess(), newBrandedProcess()
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", abandoned, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)

	// while that session is open the claim holds: the next draft is quoted with branding and the
	// holder named, and paying for it is refused until the holder pays or lets go — never priced
	// without it, which would publish it branded for free if the holder never paid
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, map[string]string{string(db.OrgAddOnBranding): abandoned})
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}, "processes", next, "checkout")
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken, nil,
		"processes", next, "publish")

	// Stripe expires the abandoned session
	abandonedOID, err := bson.ObjectIDFromHex(abandoned)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(abandonedOID)
	c.Assert(err, qt.IsNil)
	status := postSignedStripeEvent(t, testWebhookSecret, "evt_branding_expired",
		"checkout.session.expired",
		checkoutSessionObject(payment.CheckoutSessionID, "unpaid", withBrandingCents, map[string]string{
			stripe.MetadataKeyWalletTopUpOrg: orgAddress.String(),
			stripe.MetadataKeyProcessID:      abandoned,
		}))
	c.Assert(status, qt.Equals, http.StatusOK)
	_, err = testDB.ProcessPayment(abandonedOID)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// the expiry released the claim at once: the next draft can be charged for branding
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	c.Assert(price.AddOnsHeldBy, qt.HasLen, 0)
	nextCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", next, "checkout")
	c.Assert(nextCheckout.AmountCents, qt.Equals, withBrandingCents)
}

// TestPaidDraftBrandingFixedAtPayment: a draft selecting the branding another draft is paying for
// is refused (409) at checkout and publish, never charged without it — which would publish it
// branded for free if the holder never paid — and is charged it once the holder lets go. A paid
// draft's branding is what its payment bought: while the organization has not paid branding, an
// edit must not turn it on — nothing sells the €149 to a paid process, so it would strand the draft
// behind a 402 no endpoint can settle.
func TestPaidDraftBrandingFixedAtPayment(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "brandingfixed1234")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}
	const plainCents = pricedProcessCents

	payAsCheckedOut := func(pid string, cents pricing.Cents) {
		checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
			t, http.MethodPost, token, checkoutReq, "processes", pid, "checkout")
		c.Assert(checkout.AmountCents, qt.Equals, cents)
		// the top-up lands: consumed, then the wallet pays
		won, err := testDB.ConsumeProcessPaymentTopUp(objectID(c, pid), checkout.SessionID)
		c.Assert(err, qt.IsNil)
		c.Assert(won, qt.IsTrue)
		c.Assert(markProcessPaid(t, pid), qt.Equals, cents)
	}

	// the sibling holds the claim: the late draft is refused at checkout and publish
	holder, late := newBrandedVotingProcess(t, token, orgAddress), newBrandedVotingProcess(t, token, orgAddress)
	requestAndAssertCode(http.StatusOK, t, http.MethodPost, token, checkoutReq, "processes", holder, "checkout")
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, checkoutReq,
		"processes", late, "checkout")
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, nil, "processes", late, "publish")
	_, err := testDB.ProcessPayment(objectID(c, late))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a draft paid without branding cannot turn it on while the organization has not paid it
	ids := postNewOrgMembers(t, token, orgAddress, pricedProcessVoters)
	plain := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint).ProcessID
	payAsCheckedOut(plain, plainCents)
	branded := newVotingProcessRequest(orgAddress, ids)
	branded.AddOns = db.ProcessAddOns{Branding: true}
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPut, token, branded, "processes", plain)
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		newVotingProcessRequest(orgAddress, ids), "processes", plain)

	// the holder's checkout is cancelled: the late draft pays branding, and publishes with it
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", holder, "checkout")
	payAsCheckedOut(late, plainCents+pricing.BrandingCents)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", late, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
}

// TestStaleClaimOfPaidAddOnStampsOrganization: a claimant that paid for branding but whose stamp
// was never written (the write after the debit only logs a failure) does not hold the add-on for
// good: once its claim is stale, quoting a sibling stamps the organization and prices it out.
func TestStaleClaimOfPaidAddOnStampsOrganization(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "brandingunstamped")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	paid := newBrandedVotingProcess(t, token, orgAddress)
	c.Assert(markProcessPaid(t, paid), qt.Equals, pricedProcessCents+pricing.BrandingCents)
	// the stamp the debit wrote is lost; the paid claimant's claim stays
	_, err := testDB.DBClient.Database(testDBName).Collection("organizations").UpdateOne(context.Background(),
		bson.M{"_id": orgAddress}, bson.M{"$unset": bson.M{"brandingPaidAt": ""}})
	c.Assert(err, qt.IsNil)
	sibling := newBrandedVotingProcess(t, token, orgAddress)
	price := requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, token, nil, "processes", sibling, "price")
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, map[string]string{string(db.OrgAddOnBranding): paid})

	staleBrandingClaims(t)
	price = requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, token, nil, "processes", sibling, "price")
	c.Assert(price.TotalCents, qt.Equals, pricedProcessCents)
	c.Assert(price.AddOnsHeldBy, qt.HasLen, 0)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
}
