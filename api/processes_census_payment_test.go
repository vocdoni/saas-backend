package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v86"
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

	// pay it and publish, which is the state every priced published process is in
	c.Assert(markProcessPaid(t, pid), qt.Equals, int64(1_500))
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
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(19))

	// buying the headroom opens a session for the difference only, not the new total
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 25, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, int64(500))

	// and until the money is verified the census is still refused
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[19:]}, "processes", pid, "census")

	// the webhook raises the paid envelope from the base to the target the session carries
	topUpMeta := fake.created[len(fake.created)-1].Metadata
	c.Assert(topUpMeta[stripe.MetadataKeyProcessTopUpFromCents], qt.Equals, "1500")
	c.Assert(topUpMeta[stripe.MetadataKeyProcessTopUpCents], qt.Equals, "2000")
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

// markProcessPaid records a completed checkout for the process at its current quote, the state
// the webhook leaves a paid process in, and returns the amount paid.
func markProcessPaid(t *testing.T, pid string) int64 {
	t.Helper()
	c := qt.New(t)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	quote, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	session := "cs_paid_" + pid
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        vp.OrgAddress,
		CheckoutSessionID: session,
		QuoteHash:         pricing.QuoteHash(input, quote.TotalCents),
		AmountCents:       quote.TotalCents,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(oid, session, db.ProcessCharge{})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	return quote.TotalCents
}

// TestMemberbaseGrowthBeyondPaidPrice: a census built from a group grows with the memberbase —
// a created member joins the auto "All members" group, a group update adds its members — and
// those changes resize the published elections on chain. They must answer the same price
// question as the census routes, or adding members is a way to grow a paid election for free.
// The refusal names the process, the organization buys the headroom, and the retry lands.
func TestMemberbaseGrowthBeyondPaidPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "memberbasegrowth")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	all := newOrgMembers(29)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, all[:19]...))

	// no 2FA channel, so only the base price is billed: 19 voters cost €15, the 20th €20
	groupProcess := func(groupID string) string {
		req := newVotingProcessRequest(orgAddress, ids)
		req.StartDate = ""
		req.Questions = req.Questions[:1] // one whole-census question
		req.Census = apicommon.CensusSpec{
			AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
			GroupID:    groupID,
		}
		return requestAndParse[apicommon.CreateVotingProcessResponse](
			t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	}
	publish := func(pid string) {
		job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	}

	// a group update: a process published free on a 5-member group holds a €0 envelope, so
	// adding the other 14 members prices it at €15 — refused, and the group is left as it was
	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, &apicommon.CreateOrganizationMemberGroupRequest{
			Title: "board", Description: "group census", MemberIDs: ids[:5],
		}, "organizations", orgAddress.String(), "groups")
	boardPID := groupProcess(group.ID)
	publish(boardPID)
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[5:]},
		"organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(refusal.Data.ProcessID, qt.Equals, boardPID)
	c.Assert(refusal.Data.PaidCents, qt.Equals, int64(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, int64(1_500))
	stored, err := testDB.OrganizationMemberGroup(group.ID, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.MemberIDs, qt.HasLen, 5)

	// member creation: a paid process on the auto group, whose census every new member joins
	autoGroup, err := testDB.AutoMemberGroup(orgAddress)
	c.Assert(err, qt.IsNil)
	pid := groupProcess(autoGroup.ID.Hex())
	c.Assert(markProcessPaid(t, pid), qt.Equals, int64(1_500))
	publish(pid)

	// one more member is the 20th voter, which raises the price: refused, and not created
	apiErr := putOrgMemberAndExpectError(t, token, orgAddress, all[19])
	c.Assert(apiErr.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	// the bulk import is refused the same way, naming the process to top up
	bulk := &apicommon.AddMembersRequest{Members: all[19:]}
	refusal = requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPost, token, bulk, organizationMembersURL(orgAddress.String()))
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, int64(1_500))
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(29))
	count, err := testDB.CountOrgMembers(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(count, qt.Equals, int64(19))

	// buying the headroom the refusal quoted lets the same import through
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: 29, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, refusal.Data.DueCents)
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_memberbase_headroom", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", headroom.AmountCents, fake.created[len(fake.created)-1].Metadata))
	c.Assert(code, qt.Equals, http.StatusOK)

	postOrgMembers(t, token, orgAddress, all[19:]...)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(29))

	// an ended election never grows: once the auto-group process stops accepting votes, its
	// price no longer gates new members, though its census stays a propagation target
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	for _, qid := range vp.QuestionIDs {
		c.Assert(testDB.SetQuestionStatus(qid, db.QuestionStatusEnded), qt.IsNil)
	}
	putOrgMember(t, token, orgAddress, newOrgMembers(1)[0]) // the 30th voter, past the price paid
}

// TestOverlappingCensusTopUpsRefundTheStaleOne: two census top-ups opened against the same
// paid amount each charge the difference from it. Once the first is paid the envelope has
// moved, so the second, priced from a base that no longer holds, would overcharge if applied:
// it raises nothing and is refunded in full.
func TestOverlappingCensusTopUpsRefundTheStaleOne(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	refundIntents, _ := stubStripeRefunds(t)

	processID := bson.NewObjectID()
	stored, err := testDB.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         processID,
		OrgAddress:        common.HexToAddress("0x000000000000000000000000000000000000beef"),
		CheckoutSessionID: "cs_overlap_first",
		AmountCents:       1_500,
		Currency:          "eur",
	}, "")
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)
	won, err := testDB.MarkProcessPaymentPaid(processID, "cs_overlap_first",
		db.ProcessCharge{PaymentIntentID: "pi_overlap_first"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)

	topUp := func(sessionID string, amount int64, target string) map[string]any {
		session := checkoutSessionObject(sessionID, "paid", amount, map[string]string{
			stripe.MetadataKeyProcessID:             processID.Hex(),
			stripe.MetadataKeyProcessTopUpFromCents: "1500",
			stripe.MetadataKeyProcessTopUpCents:     target,
		})
		session["payment_intent"] = "pi_" + sessionID
		return session
	}
	// both opened at 1500: one charging 500 up to 2000, the other 1000 up to 2500
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_small", "checkout.session.completed",
		topUp("cs_overlap_small", 500, "2000"))
	c.Assert(code, qt.Equals, http.StatusOK)
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_large", "checkout.session.completed",
		topUp("cs_overlap_large", 1_000, "2500"))
	c.Assert(code, qt.Equals, http.StatusOK)

	payment, err := testDB.ProcessPayment(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, int64(2_000))
	c.Assert(payment.TopUpIntents, qt.DeepEquals, []string{"pi_cs_overlap_small"})
	c.Assert(*refundIntents, qt.DeepEquals, []string{"pi_cs_overlap_large"})

	// a replay of the applied one is still a no-op, not a refund
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_small_replay", "checkout.session.completed",
		topUp("cs_overlap_small", 500, "2000"))
	c.Assert(code, qt.Equals, http.StatusOK)
	c.Assert(*refundIntents, qt.HasLen, 1)
}

// stubStripeRefunds points the Stripe client at a stub that accepts every refund, recording
// the payment intent and idempotency key of each.
func stubStripeRefunds(t *testing.T) (intents, keys *[]string) {
	intents, keys = &[]string{}, &[]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/refunds" || r.ParseForm() != nil {
			t.Errorf("unexpected stripe call %s", r.URL.Path)
		}
		if r.Method == http.MethodGet {
			// the lookup for a refund already issued: there is none
			_, _ = w.Write([]byte(`{"object":"list","data":[],"has_more":false,"url":"/v1/refunds"}`))
			return
		}
		*intents = append(*intents, r.PostForm.Get("payment_intent"))
		*keys = append(*keys, r.Header.Get("Idempotency-Key"))
		_, _ = w.Write([]byte(`{"id":"re_stub","object":"refund","status":"succeeded"}`))
	}))
	t.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend,
		stripeapi.GetBackendWithConfig(stripeapi.APIBackend, &stripeapi.BackendConfig{URL: stripeapi.String(srv.URL)}))
	t.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	return intents, keys
}

// TestGrandfatheredProcessCensusGrowth: a process published before pay-per-process has no
// payment at all. Its first growth check grandfathers it at the price of the census it holds —
// that much stays free — and growth past it is refused like any paid process's.
func TestGrandfatheredProcessCensusGrowth(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "grandfathered")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	all := newOrgMembers(20)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, all...))

	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, &apicommon.CreateOrganizationMemberGroupRequest{
			Title: "voters", Description: "group census", MemberIDs: ids[:15],
		}, "organizations", orgAddress.String(), "groups")
	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Questions = req.Questions[:1]
	req.Census = apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		GroupID:    group.ID,
	}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	markProcessPaid(t, pid)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// drop the payment: this is what an election published before the migration looks like
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	_, err = testDB.DBClient.Database(testDBName).Collection("processPayments").
		DeleteOne(context.Background(), bson.M{"_id": oid})
	c.Assert(err, qt.IsNil)

	// 15 → 19 voters stays in the €15 tier the census already held: free
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[15:19]},
		"organizations", orgAddress.String(), "groups", group.ID)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, int64(1_500))

	// the 20th voter raises the price past the grandfathered envelope: refused
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[19:]},
		"organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, int64(1_500))
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
