package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestPublishedFreeProcessCensusGrowth: a process published free has no checkout to record
// a price, so publication records a €0 payment instead — without it the census would have no
// envelope and could grow into a priced size for nothing.
func TestPublishedFreeProcessCensusGrowth(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "freegrowth12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(15)...))

	// within the free tier and no 2FA channel: nothing to pay
	req := newVotingProcessRequest(orgAddress, ids[:pricing.FreeCensusSize])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, int64(0))

	// growing out of the free tier costs the whole priced amount
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[pricing.FreeCensusSize:]}, "processes", pid, "census")
	c.Assert(refusal.Data.PaidCents, qt.Equals, int64(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, int64(1_500))
}

// TestPublishedCensusGrowthWithinPaidPrice: PUT /processes/{processId}/census is the only
// way a published process's census can grow, and pay-per-process prices by census size — so
// the guard has to answer the price question. Refusing every paid process would refuse every
// published non-free one, which is the feature removed. Growth that the €5 rounding absorbs
// is free and allowed; growth that raises the price is refused with the projected quote.
func TestPublishedCensusGrowthWithinPaidPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "censusgrowth123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(25)...)
	ids := memberIDs(members)

	// no 2FA channel, so only the base price is billed and the rounding leaves room: 14 to
	// 19 voters all cost €15, the 20th costs €20
	req := newVotingProcessRequest(orgAddress, ids[:15])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	c.Assert(quote.TotalCents, qt.Equals, int64(1_500))

	// pay it and publish, which is the state every priced published process is in
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        orgAddress,
		CheckoutSessionID: "cs_census_growth",
		QuoteHash:         pricing.QuoteHash(input, quote.TotalCents),
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, "cs_census_growth", db.ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// 4 more voters still cost €15: allowed, and they land in the census (202: the
	// whole-census questions need their on-chain maxCensusSize raised)
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[15:19]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(4))

	// the ones that raise the price to €20 are refused, and nothing is added — but the
	// refusal has to say what the growth costs, or the user is simply stuck
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	c.Assert(refusal.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	c.Assert(refusal.Data.TotalCents, qt.Equals, int64(2_000))
	c.Assert(refusal.Data.PaidCents, qt.Equals, int64(1_500))
	c.Assert(refusal.Data.DueCents, qt.Equals, int64(500))
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(25))
	// the legacy census route is no way around it
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPost, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "census", vp.CensusID.Hex())
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(19))

	// buying the headroom opens a session for the difference only, not the new total
	installFakePaymentGW(t)
	installStripeWebhookService(t)
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 25, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, int64(500))

	// and until the money is verified the census is still refused
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")

	// the webhook raises the paid envelope to the target the session carries
	topUpMeta := map[string]string{
		stripe.MetadataKeyProcessID:         pid,
		stripe.MetadataKeyProcessTopUpCents: "2000",
	}
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_headroom_1", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", 500, topUpMeta))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, int64(2_000))

	// the same growth now lands
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(6))

	// a replay from another replica (fresh event id, same session) raises nothing: the
	// envelope must not creep upward on Stripe's retries
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_headroom_2", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", 500, topUpMeta))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, int64(2_000))
	c.Assert(payment.TopUpSessions, qt.DeepEquals, []string{headroom.SessionID})

	// nothing is left to buy at that size
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 25, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
}

// censusGrowthRefusal is the 402 of a census that outgrew its price, typed so the test reads
// the quote the client is meant to act on rather than a map.
type censusGrowthRefusal struct {
	Code int                                `json:"code"`
	Data apicommon.ProcessCensusGrowthQuote `json:"data"`
}

// TestManagedCensusGrowthChargesIntegratorWallet: a managed organization has no card, so the
// headroom endpoint debits its integrator's prepaid wallet instead of opening a checkout —
// otherwise the 402 is a dead end the integrator cannot act on. The debit is keyed on
// (process, price), so buying the same size twice charges once.
func TestManagedCensusGrowthChargesIntegratorWallet(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "managedgrowth123")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_managed_growth")

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(25)...)
	ids := memberIDs(members)
	req := newVotingProcessRequest(managedAddr, ids[:15])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_managed_growth_topup",
	}), qt.IsNil)

	// publish, which debits the wallet for 15 voters without 2FA (€15)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_500))

	// growth the rounding absorbs costs nothing
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[15:19]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(4))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_500))

	// growth past the price is refused with the difference, wallet untouched: the guard only
	// projects an upper bound, so it must never charge against it
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](
		http.StatusPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	c.Assert(refusal.Data.DueCents, qt.Equals, int64(500))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-1_500))

	// the integrator buys the headroom from its wallet — no checkout session, effective at once
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 25, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.ClientSecret, qt.Equals, "")
	c.Assert(headroom.AmountCents, qt.Equals, int64(500))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-2_000))
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, int64(2_000))

	// the same growth now lands, and re-adding those members changes nothing
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(6))
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(0))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-2_000))

	// buying the same size again is nothing left to buy, not a second debit
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 25, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, int64(100_000-2_000))
}
