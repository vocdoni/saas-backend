package api

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// fakePaymentGW implements paymentGateway in memory: sessions are created open and can
// be inspected, expired or force-completed by the test.
type fakePaymentGW struct {
	mu       sync.Mutex
	seq      int
	sessions map[string]*stripe.PaymentSessionInfo
	created  []*stripe.PaymentSessionParams
	expired  []string
	refunds  []fakeRefund
	// expireErr, when set, makes ExpirePaymentSession fail (Stripe unreachable)
	expireErr error
	refundFn  func(processID bson.ObjectID, paymentIntentID string) (*stripe.RefundInfo, error)
}

// fakeRefund is one recorded RefundProcessPayment call.
type fakeRefund struct {
	processID       bson.ObjectID
	paymentIntentID string
	amountCents     int64
}

func newFakePaymentGW() *fakePaymentGW {
	return &fakePaymentGW{sessions: map[string]*stripe.PaymentSessionInfo{}}
}

func (f *fakePaymentGW) CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	session := &stripe.PaymentSessionInfo{
		ID:            fmt.Sprintf("cs_test_%d", f.seq),
		ClientSecret:  fmt.Sprintf("cs_test_%d_secret", f.seq),
		Status:        stripe.SessionStatusOpen,
		PaymentStatus: stripe.PaymentStatusUnpaid,
	}
	f.sessions[session.ID] = session
	f.created = append(f.created, params)
	return session, nil
}

func (f *fakePaymentGW) GetPaymentSession(sessionID string) (*stripe.PaymentSessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	copied := *session
	return &copied, nil
}

// setSessionStatus forces a stored session's status, simulating Stripe-side transitions
// the fake cannot reach through its own API (a customer completing checkout, the 24h
// expiry of an abandoned session).
func (f *fakePaymentGW) setSessionStatus(sessionID string, status stripe.SessionStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[sessionID].Status = status
}

func (f *fakePaymentGW) ExpirePaymentSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expireErr != nil {
		return f.expireErr
	}
	session, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	session.Status = stripe.SessionStatusExpired
	f.expired = append(f.expired, sessionID)
	return nil
}

// RefundProcessPayment records the refund and reports it succeeded, unless the test
// installed refundFn to make Stripe fail.
func (f *fakePaymentGW) RefundProcessPayment(
	processID bson.ObjectID, paymentIntentID string, amountCents int64,
) (*stripe.RefundInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refunds = append(f.refunds, fakeRefund{
		processID: processID, paymentIntentID: paymentIntentID, amountCents: amountCents,
	})
	if f.refundFn != nil {
		return f.refundFn(processID, paymentIntentID)
	}
	return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
}

// installFakePaymentGW swaps the API's payment gateway for a fake for one test.
func installFakePaymentGW(t *testing.T) *fakePaymentGW {
	t.Helper()
	fake := newFakePaymentGW()
	prev := testAPI.paymentGW
	testAPI.paymentGW = fake
	t.Cleanup(func() { testAPI.paymentGW = prev })
	return fake
}

// newPricedVotingProcess creates an org with enough members that the draft prices above
// zero (15 voters with email 2FA -> €15.15 = 1515 cents) and returns the process id.
func newPricedVotingProcess(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)), processesCreateEndpoint)
	return created.ProcessID
}

// newBrandedVotingProcess creates a priced draft that selects the branding add-on (15
// voters with email 2FA, €15.15, plus €149 of branding when the organization can still be
// charged for it).
func newBrandedVotingProcess(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(15)...)
	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	req.AddOns = db.ProcessAddOns{Branding: true}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	return created.ProcessID
}

func TestProcessPrice(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)

	// 15 voters, email 2FA: base R5(1.5*15^0.824) = €15 plus 15 * €0.01
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "price")
	c.Assert(price.TotalCents, qt.Equals, int64(1_515))
	c.Assert(price.Currency, qt.Equals, "eur")
	c.Assert(price.QuoteRecommended, qt.IsFalse)
	c.Assert(price.QuoteRequired, qt.IsFalse)
	c.Assert(price.PaymentStatus, qt.Equals, db.ProcessPaymentStatus(""))
	c.Assert(len(price.Lines) >= 2, qt.IsTrue)

	// an unrelated user cannot read the price
	strangerToken := testCreateUser(t, "strangerpass123")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, strangerToken, nil,
		"processes", pid, "price")
}

func TestProcessCheckoutFlow(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// first checkout opens a session for the server-side price
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(checkout.SessionID, qt.Equals, "cs_test_1")
	c.Assert(checkout.ClientSecret, qt.Equals, "cs_test_1_secret")
	c.Assert(checkout.AmountCents, qt.Equals, int64(1_515))
	c.Assert(fake.created, qt.HasLen, 1)
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyProcessID], qt.Equals, pid)
	var lineSum int64
	for _, item := range fake.created[0].LineItems {
		lineSum += item.AmountCents
	}
	c.Assert(lineSum, qt.Equals, int64(1_515))

	// the payment is stored pending and visible on the status endpoint
	status := requestAndParse[apicommon.ProcessPaymentStatusResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "checkout")
	c.Assert(status.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(status.SessionStatus, qt.Equals, string(stripe.SessionStatusOpen))

	// an unchanged retry reuses the open session instead of opening a second one
	again := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(again.SessionID, qt.Equals, "cs_test_1")
	c.Assert(fake.created, qt.HasLen, 1)

	// editing the draft to a different price obsoletes the session: the next checkout
	// expires it and opens a replacement
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(30)[15:]...)
	update := newVotingProcessRequest(orgAddress, memberIDs(members))
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, update, "processes", pid)
	replaced := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(replaced.SessionID, qt.Equals, "cs_test_2")
	c.Assert(fake.expired, qt.DeepEquals, []string{"cs_test_1"})

	// once the payment is processing or paid, checkout refuses a second charge
	won, err := testDB.MarkProcessPaymentProcessing(oid, "cs_test_2")
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken, checkoutReq,
		"processes", pid, "checkout")
	won, err = testDB.MarkProcessPaymentPaid(oid, "cs_test_2", db.ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken, checkoutReq,
		"processes", pid, "checkout")
}

// TestDeleteDraftRefusesCompletedSession guards the money race: a pending payment whose
// Stripe session the customer already completed (webhook not landed yet) must not be
// deleted — that would capture the money and destroy the process. Delete refuses with a
// conflict, exactly as the checkout path does.
func TestDeleteDraftRefusesCompletedSession(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "deleterace123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)

	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	// the customer completed checkout; the webhook has not fulfilled yet (still pending)
	fake.setSessionStatus(checkout.SessionID, stripe.SessionStatusComplete)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, adminToken, nil,
		"processes", pid)

	// the process and its payment are intact — nothing was destroyed
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)

	// a still-open (abandoned) session, by contrast, does not block deletion
	pid2 := newPricedVotingProcess(t, adminToken, orgAddress)
	_ = requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid2, "checkout")
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", pid2)
}

// TestProcessCheckoutSessionStates covers the checkout reconciliation against
// Stripe-side session states the customer (or time) produced: a session the customer
// completed while the webhook has not landed yet refuses a second charge, and a session
// Stripe expired on its own is replaced without an expire call.
func TestProcessCheckoutSessionStates(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "sessionstates123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// the customer finished checkout (session complete) but the webhook is in flight:
	// the payment is still pending on our side, yet a new checkout must refuse — the
	// in-flight payment could already have charged them
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	first := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	fake.setSessionStatus(first.SessionID, stripe.SessionStatusComplete)
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, adminToken, checkoutReq,
		"processes", pid, "checkout")
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)

	// an abandoned session Stripe expired on its own (24h): the next checkout opens a
	// replacement directly — expiring an already-expired session would fail
	pid2 := newPricedVotingProcess(t, adminToken, orgAddress)
	second := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid2, "checkout")
	fake.setSessionStatus(second.SessionID, stripe.SessionStatusExpired)
	replacement := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid2, "checkout")
	c.Assert(replacement.SessionID, qt.Not(qt.Equals), second.SessionID)
	c.Assert(fake.expired, qt.HasLen, 0)
}

func TestProcessCheckoutRefusals(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// a free process (census within the free tier, no priced 2FA channel) has nothing
	// to pay — note even a tiny census with 2FA is NOT free (2FA bills per voter)
	members := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)
	freeReq := newVotingProcessRequest(orgAddress, memberIDs(members))
	freeReq.Census.TwoFaFields = nil
	freeReq.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, adminToken, freeReq, processesCreateEndpoint)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, checkoutReq,
		"processes", created.ProcessID, "checkout")

	// above 50 000 voters self-service checkout is refused: force the stored census size
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	census.Size = 50_001
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "price")
	c.Assert(price.QuoteRequired, qt.IsTrue)
	c.Assert(price.QuoteRecommended, qt.IsTrue)
	requestAndAssertError(errors.ErrQuoteRequired, t, http.MethodPost, adminToken, checkoutReq,
		"processes", pid, "checkout")
}

// TestDeleteDraftFailsClosedWhenSessionUnverifiable: deleting a draft expires its
// checkout session, so a session whose state cannot be read may already have been
// completed — deleting then captures the money and destroys what it paid for. Not
// knowing has to be a refusal, exactly as on the checkout path.
func TestDeleteDraftFailsClosedWhenSessionUnverifiable(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "faildelete12345")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	// a pending payment whose session the gateway cannot resolve (Stripe unreachable)
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_unreachable",
		QuoteHash:         "hash",
		AmountCents:       1_515,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, token, nil, "processes", pid)

	// no gateway configured at all is the same unknown
	testAPI.paymentGW = nil
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, token, nil, "processes", pid)
	testAPI.paymentGW = fake

	// every refusal left the draft and its payment intact
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
}

// TestDeleteDraftPaymentStates: what a delete does to the payment depends on its state. A
// session that cannot be expired may still be paid, so the delete refuses; one Stripe already
// expired is not expired again; a failed payment needs no gateway at all; and a refunded one
// (an earlier delete refunded, then failed to drop the draft) is kept as the audit record.
func TestDeleteDraftPaymentStates(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "deletestates1234")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"}
	openCheckout := func() (string, bson.ObjectID, string) {
		pid := newPricedVotingProcess(t, token, orgAddress)
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
			t, http.MethodPost, token, checkoutReq, "processes", pid, "checkout")
		return pid, oid, checkout.SessionID
	}

	// the open session cannot be expired: the draft and its payment stay
	pid, oid, _ := openCheckout()
	fake.expireErr = fmt.Errorf("stripe unreachable")
	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, token, nil, "processes", pid)
	fake.expireErr = nil
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)

	// a session Stripe already expired is not expired a second time
	pid, oid, sessionID := openCheckout()
	fake.setSessionStatus(sessionID, stripe.SessionStatusExpired)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(fake.expired, qt.Not(qt.Contains), sessionID)
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a failed payment is deleted with the draft even with no gateway configured
	pid, oid, sessionID = openCheckout()
	won, err := testDB.MarkProcessPaymentFailed(oid, sessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	testAPI.paymentGW = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	testAPI.paymentGW = fake
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// a refunded payment whose draft survived: the retry deletes the draft, keeps the row
	pid, oid, sessionID = openCheckout()
	fake.setSessionStatus(sessionID, stripe.SessionStatusComplete)
	won, err = testDB.MarkProcessPaymentPaid(oid, sessionID, db.ProcessCharge{PaymentIntentID: "pi_states"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	statesPayment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	won, err = testDB.MarkProcessPaymentRefunded(oid, "re_states", statesPayment.AmountCents)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	refunds := len(fake.refunds)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid)
	c.Assert(fake.refunds, qt.HasLen, refunds)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

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

// TestDeletePaidDraftRefunds covers the way out of a paid draft the organization no longer
// wants: DELETE returns the money before it removes the process. The payment document stays
// behind as the record that money came and went — the process it names is gone, so nothing
// can be paid for again — and the organization's once-only branding add-on is released with
// the refund, because it is about to be charged for it again.
func TestDeletePaidDraftRefunds(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "refundpass123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	const withBrandingCents = int64(1_515) + 14_900

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	next := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", paid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, withBrandingCents)
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)

	// fulfillment, as the webhook performs it: the payment records the intent a refund is
	// issued against, and the organization is stamped as having paid branding
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_refund"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// the stamp spares the next draft branding, but the paid one keeps it in its price: a
	// re-quote without it would hand the €149 to census growth
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", paid, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, int64(1_515))

	// a census top-up is its own charge, so it has to come back too
	raised, err := testDB.RaiseProcessPaymentAmount(oid, withBrandingCents+500, "cs_test_topup", "pi_test_topup")
	c.Assert(err, qt.IsNil)
	c.Assert(raised, qt.IsTrue)

	// a second top-up landing while the delete refunds: its intent was not in the snapshot,
	// so the payment is not recorded as refunded and the draft survives for a retry
	fake.refundFn = func(processID bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		fake.refundFn = nil // called under fake.mu; the retry must not raise again
		_, err := testDB.RaiseProcessPaymentAmount(oid, withBrandingCents+900, "cs_test_late", "pi_test_late")
		c.Assert(err, qt.IsNil)
		return &stripe.RefundInfo{ID: "re_test_" + processID.Hex(), Status: "succeeded"}, nil
	}
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodDelete, adminToken, nil, "processes", paid)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)

	fake.refunds = nil
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	var intents []string
	for _, refund := range fake.refunds {
		c.Assert(refund.processID, qt.Equals, oid)
		intents = append(intents, refund.paymentIntentID)
	}
	c.Assert(intents, qt.DeepEquals, []string{"pi_test_refund", "pi_test_topup", "pi_test_late"})
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
	c.Assert(payment.RefundID, qt.Equals, "re_test_"+oid.Hex())

	// branding came back with the money, so the next draft is charged for it again
	price = requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", next, "price")
	c.Assert(price.TotalCents, qt.Equals, withBrandingCents)
}

// TestDeletePaidDraftWithholdsReliedOnBranding: once another process got its branding free on
// the strength of the deleted draft's payment, the €149 is in use and does not come back. The
// card refund withholds it grossed up by the VAT the charge carried, and the organization keeps
// its stamp, so the process relying on it is not re-priced.
func TestDeletePaidDraftWithholdsReliedOnBranding(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "withholdpass1234")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	paid := newBrandedVotingProcess(t, adminToken, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", paid, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, int64(1_515+14_900))
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)
	// €164.15 net at 21% VAT
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{
		PaymentIntentID: "pi_test_withhold", SubtotalCents: 16_415, TotalCents: 19_862,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	stamped, err := testDB.SetOrganizationBrandingPaid(orgAddress, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)

	// a sibling that paid without branding because of the stamp, yet carries it
	sibling, err := bson.ObjectIDFromHex(newBrandedVotingProcess(t, adminToken, orgAddress))
	c.Assert(err, qt.IsNil)
	recorded, err := testDB.SetProcessPaymentFree(sibling, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(recorded, qt.IsTrue)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", paid)
	c.Assert(fake.refunds, qt.HasLen, 1)
	c.Assert(fake.refunds[0].paymentIntentID, qt.Equals, "pi_test_withhold")
	// €149 grossed up by 19862/16415 is €180.29; the €18.33 left is the base price and its VAT
	c.Assert(fake.refunds[0].amountCents, qt.Equals, int64(1_833))

	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsFalse)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentRefunded)
}

// TestDeletePaidDraftKeepsDraftWhenRefundFails is the failure this flow must never get
// wrong: if the money cannot go back, the draft it paid for stays. Deleting it anyway would
// destroy the process and keep the payment.
func TestDeletePaidDraftKeepsDraftWhenRefundFails(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.refundFn = func(_ bson.ObjectID, _ string) (*stripe.RefundInfo, error) {
		return nil, fmt.Errorf("refund declined")
	}
	adminToken := testCreateUser(t, "refundfail123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, adminToken,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"},
		"processes", pid, "checkout")
	won, err := testDB.MarkProcessPaymentPaid(oid, checkout.SessionID, db.ProcessCharge{PaymentIntentID: "pi_test_declined"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	requestAndAssertError(errors.ErrStripeError, t, http.MethodDelete, adminToken, nil, "processes", pid)
	_, err = testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.RefundID, qt.Equals, "")
}
