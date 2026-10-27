package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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
	// expireErr, when set, makes ExpirePaymentSession fail (Stripe unreachable)
	expireErr error
	// createErr, when set, makes CreatePaymentSession fail (Stripe unreachable)
	createErr error
	// expireFn, when set, runs once after a session is expired: a request racing the expiry
	expireFn func(sessionID string)
	// createFn, when set, runs as a session is created: a request racing the checkout
	createFn func()
}

func newFakePaymentGW() *fakePaymentGW {
	return &fakePaymentGW{sessions: map[string]*stripe.PaymentSessionInfo{}}
}

func (f *fakePaymentGW) CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error) {
	if f.createFn != nil {
		f.createFn()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
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
	if fn := f.expireFn; fn != nil {
		f.expireFn = nil
		f.mu.Unlock()
		fn(sessionID)
		f.mu.Lock()
	}
	return nil
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

// postNewOrgMembers adds n fresh members to the organization and returns the IDs of those n
// alone, so a draft built on them is priced for n voters however many the organization holds.
func postNewOrgMembers(t *testing.T, token string, orgAddress common.Address, n int) []string {
	t.Helper()
	batch := newOrgMembers(n)
	posted := make(map[string]bool, n)
	for _, m := range batch {
		posted[m.MemberNumber] = true
	}
	postOrgMembers(t, token, orgAddress, batch...)
	// the listing is paged at 100 and an organization can hold several priced drafts' members
	var ids []string
	for page := 1; len(ids) < n && page <= 100; page++ {
		listed := requestAndParse[apicommon.OrganizationMembersResponse](t, http.MethodGet, token, nil,
			organizationMembersURL(orgAddress.String())+fmt.Sprintf("?limit=100&page=%d", page))
		if len(listed.Members) == 0 {
			break
		}
		for _, m := range listed.Members {
			if posted[m.MemberNumber] {
				ids = append(ids, m.ID)
			}
		}
	}
	qt.Assert(t, ids, qt.HasLen, n)
	return ids
}

// A census just above the free tier: priced, and with the free email 2FA a standard
// organization's draft costs only its base, every voter at the first band's €0.66.
const (
	pricedProcessVoters               = pricing.FreeCensusSize + 1
	pricedProcessCents  pricing.Cents = 1_716 // 26 × €0.66
	// an integrator's managed organization also pays €0.01 per voter for email 2FA
	integratorProcessCents = pricedProcessCents + pricedProcessVoters*pricing.Cent
)

// newPricedVotingProcess creates an org with enough members that the draft prices above
// zero (26 voters with free email 2FA -> €17.16 = 1716 cents) and returns the process id.
func newPricedVotingProcess(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, postNewOrgMembers(t, token, orgAddress, pricedProcessVoters)),
		processesCreateEndpoint)
	return created.ProcessID
}

// newBrandedVotingProcess creates a priced draft that selects the branding add-on (26
// voters with free email 2FA, €17.16, plus €149 of branding when the organization can still be
// charged for it).
func newBrandedVotingProcess(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	return newAddOnsVotingProcess(t, token, orgAddress, db.ProcessAddOns{Branding: true})
}

func TestProcessPrice(t *testing.T) {
	c := qt.New(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)

	// 26 voters, email 2FA free for a standard organization: only the base, 26 × €0.66 = €17.16
	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "price")
	c.Assert(price.TotalCents, qt.Equals, pricedProcessCents)
	c.Assert(price.Currency, qt.Equals, "eur")
	c.Assert(price.QuoteRecommended, qt.IsFalse)
	c.Assert(price.QuoteRequired, qt.IsFalse)
	c.Assert(price.PaymentStatus, qt.Equals, db.ProcessPaymentStatus(""))
	// email 2FA is free for a standard organization, so the base is the only line
	c.Assert(price.Lines, qt.HasLen, 1)
	c.Assert(price.Lines[0].Kind, qt.Equals, pricing.LineBase)

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
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// part of the price is already in the wallet: the checkout tops up only the shortfall
	const balance = 500
	fundWallet(t, orgAddress, balance)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(checkout.SessionID, qt.Equals, "cs_test_1")
	c.Assert(checkout.ClientSecret, qt.Equals, "cs_test_1_secret")
	c.Assert(checkout.AmountCents, qt.Equals, pricedProcessCents-balance)
	c.Assert(fake.created, qt.HasLen, 1)
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyWalletTopUpOrg], qt.Equals, orgAddress.String())
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyProcessID], qt.Equals, pid)
	c.Assert(fake.created[0].LineItems, qt.HasLen, 1)
	c.Assert(fake.created[0].LineItems[0].AmountCents, qt.Equals, pricedProcessCents-balance)

	// the pending payment holds the price it was opened for and what the session charges
	payment, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(payment.AmountCents, qt.Equals, pricedProcessCents)
	c.Assert(payment.TopUpCents, qt.Equals, pricedProcessCents-balance)
	c.Assert(payment.Payer, qt.Equals, orgAddress)
	status := requestAndParse[apicommon.ProcessPaymentStatusResponse](
		t, http.MethodGet, adminToken, nil, "processes", pid, "checkout")
	c.Assert(status.Status, qt.Equals, db.ProcessPaymentPending)
	c.Assert(status.SessionStatus, qt.Equals, string(stripe.SessionStatusOpen))

	// an unchanged retry reuses the open session instead of opening a second one
	again := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(again.SessionID, qt.Equals, "cs_test_1")
	c.Assert(fake.created, qt.HasLen, 1)

	// the balance moved: same price, other shortfall — the session is replaced
	fundWallet(t, orgAddress, 100)
	moved := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(moved.SessionID, qt.Equals, "cs_test_2")
	c.Assert(moved.AmountCents, qt.Equals, pricedProcessCents-balance-100)
	c.Assert(fake.expired, qt.DeepEquals, []string{"cs_test_1"})

	// editing the draft to a different price obsoletes the session just the same
	update := newVotingProcessRequest(orgAddress, postNewOrgMembers(t, adminToken, orgAddress, pricedProcessVoters+2))
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, adminToken, update, "processes", pid)
	replaced := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, checkoutReq, "processes", pid, "checkout")
	c.Assert(replaced.SessionID, qt.Equals, "cs_test_3")
	c.Assert(fake.expired, qt.DeepEquals, []string{"cs_test_1", "cs_test_2"})

	// once the wallet covers the price there is nothing to check out: publish directly
	fundWallet(t, orgAddress, 100_000)
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, adminToken, checkoutReq,
		"processes", pid, "checkout")
}

// TestDeleteDraftWithCompletedTopUp: a pending payment whose top-up the customer already
// completed (webhook not landed yet) no longer blocks the delete: the money lands in the wallet
// whatever happens to the draft, and the session is not expired (it cannot be).
func TestDeleteDraftWithCompletedTopUp(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	adminToken := testCreateUser(t, "deleterace123456")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, adminToken, orgAddress)

	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, adminToken, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")
	fake.setSessionStatus(checkout.SessionID, stripe.SessionStatusComplete)
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, adminToken, nil, "processes", pid)
	c.Assert(fake.expired, qt.HasLen, 0)
	_, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
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

// TestProcessCheckoutExcludesDelete: checkout holds the draft's publish claim until its pending
// payment is stored. A delete landing in between would otherwise drop the draft and leave a payable
// payment behind for a process that no longer exists — paying it would keep the money.
func TestProcessCheckoutExcludesDelete(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "checkoutdelete123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)

	deleteCode := 0
	fake.createFn = func() {
		_, deleteCode = testRequest(t, http.MethodDelete, token, nil, "processes", pid)
	}
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}, "processes", pid, "checkout")
	c.Assert(deleteCode, qt.Equals, http.StatusConflict)

	// the draft and its payment survive together, and the claim is given back
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.CheckoutSessionID, qt.Equals, checkout.SessionID)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.Publishing.IsZero(), qt.IsTrue)
}

func TestProcessCheckoutRefusals(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}

	// a managed organization has no wallet of its own: its integrator pays at publish
	_, managedAddr := newIntegratorWithManagedOrg(t, adminToken, "prod_test_checkout_managed")
	managedMembers := postOrgMembers(t, adminToken, managedAddr, newOrgMembers(pricedProcessVoters)...)
	managed := requestAndParse[apicommon.CreateVotingProcessResponse](t, http.MethodPost, adminToken,
		newVotingProcessRequest(managedAddr, memberIDs(managedMembers)), processesCreateEndpoint)
	requestAndAssertError(errors.ErrManagedOrgHasNoWallet, t, http.MethodPost, adminToken, checkoutReq,
		"processes", managed.ProcessID, "checkout")

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
		Payer:             orgAddress,
		CheckoutSessionID: "cs_unreachable",
		AmountCents:       1_515,
		TopUpCents:        1_515,
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

// TestFreePublishRefusedOverOpenCheckout: a draft that shrinks to free while its top-up checkout
// is open must not publish over it — the session would stay payable. Publish refuses until the
// checkout is cancelled; cancelling expires an open session, drops the payment, and a repeat finds
// nothing left to cancel.
func TestFreePublishRefusedOverOpenCheckout(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "freeoveropen123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(pricedProcessVoters)...))
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint).ProcessID
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, &apicommon.ProcessCheckoutRequest{ReturnURL: "https://x.example"},
		"processes", pid, "checkout")

	// shrink to the free tier with no 2FA channel: the draft now prices at zero
	free := newVotingProcessRequest(orgAddress, ids[:pricing.FreeCensusSize])
	free.Census.TwoFaFields = nil
	free.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, free, "processes", pid)

	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, nil,
		"processes", pid, "publish")
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", pid, "validation")
	c.Assert(strings.Join(validation.Errors, "; "), qt.Contains, "/checkout")

	// cancel expires the session and frees the draft; a repeat finds no payment
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", pid, "checkout")
	requestAndAssertCode(http.StatusNotFound, t, http.MethodDelete, token, nil, "processes", pid, "checkout")
	c.Assert(fake.expired, qt.DeepEquals, []string{checkout.SessionID})

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// the cancelled checkout gives way to the €0 envelope, or the census could grow for free
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(0))
	c.Assert(payment.CheckoutSessionID, qt.Equals, "")
}
