package api

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

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
		Status:        "open",
		PaymentStatus: "unpaid",
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
func (f *fakePaymentGW) setSessionStatus(sessionID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[sessionID].Status = status
}

func (f *fakePaymentGW) ExpirePaymentSession(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	session, ok := f.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	session.Status = "expired"
	f.expired = append(f.expired, sessionID)
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

// newPricedVotingProcess creates an org with enough members that the draft prices above
// zero (15 voters with email 2FA -> €15.15 = 1515 cents) and returns the process id.
func newPricedVotingProcess(t *testing.T, token string, orgAddress common.Address) string {
	t.Helper()
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(15)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)), processesCreateEndpoint)
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
	c.Assert(status.SessionStatus, qt.Equals, "open")

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
	won, err = testDB.MarkProcessPaymentPaid(oid, "cs_test_2")
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
	fake.setSessionStatus(checkout.SessionID, "complete")
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
	fake.setSessionStatus(first.SessionID, "complete")
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
	fake.setSessionStatus(second.SessionID, "expired")
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
