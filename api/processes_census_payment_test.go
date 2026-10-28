package api

import (
	"context"
	"net/http"
	"strconv"
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
	"go.vocdoni.io/dvote/util"
)

// Census sizes the growth tests walk through, sitting just above the free tier. Every voter above
// it costs €0.66, so no growth is free: a test that grows a paid census "within its price" first
// buys the headroom up to growthTierEnd, then grows into it, and growth to growthTotalVoters is
// refused with the projected quote. No 2FA channel, so only the base price is billed.
const (
	growthBaseVoters  = pricing.FreeCensusSize + 2
	growthTierEnd     = 34
	growthTotalVoters = 40
)

// growthCents is the price of a growth-test census of voters, no 2FA and no add-ons.
func growthCents(t *testing.T, voters int) pricing.Cents {
	t.Helper()
	quote, err := pricing.Compute(pricing.QuoteInput{CensusSize: voters})
	qt.Assert(t, err, qt.IsNil)
	return quote.TotalCents
}

// buyHeadroom raises a paid process's envelope from its paid price to the price of growthTierEnd
// voters, paid from its organization's wallet as census/checkout does when the wallet covers it.
func buyHeadroom(t *testing.T, oid bson.ObjectID, paid pricing.Cents) pricing.Cents {
	t.Helper()
	c := qt.New(t)
	target := growthCents(t, growthTierEnd)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	fundWallet(t, walletPayer(org), target-paid)
	debited, err := testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: target},
		branding: payment.Branding, customURL: payment.CustomURL,
	})
	c.Assert(err, qt.IsNil)
	c.Assert(debited, qt.Equals, target-paid)
	return target
}

// fundWallet credits cents to a wallet, as a verified top-up would.
func fundWallet(t *testing.T, wallet common.Address, cents pricing.Cents) {
	t.Helper()
	if cents <= 0 {
		return
	}
	qt.Assert(t, testDB.CreditWallet(db.WalletCredit{
		OrgAddress: wallet, AmountCents: cents, IdempotencyKey: "cs_fund_" + util.RandomHex(8),
	}), qt.IsNil)
}

// TestPublishedFreeProcessCensusGrowth: a process published free has no checkout to record
// a price, so publication records a €0 payment instead — without it the census would have no
// envelope and could grow into a priced size for nothing.
func TestPublishedFreeProcessCensusGrowth(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "freegrowth12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(growthBaseVoters)...))

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
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(0))

	// growing out of the free tier costs the whole priced amount
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[pricing.FreeCensusSize:]}, "processes", pid, "census")
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, growthCents(t, growthBaseVoters))
}

// TestParallelCensusGrowthStaysWithinPrice: the growth check and the write it allows are one
// step per organization. Without that, parallel PUTs each pass the check against the same size
// and together grow a free process far past what is free — five adds of 5 on a 20-voter process
// would reach 45.
func TestParallelCensusGrowthStaysWithinPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "parallelgrowth12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(pricing.FreeCensusSize+20)...))

	req := newVotingProcessRequest(orgAddress, ids[:pricing.FreeCensusSize-5])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// each batch alone fits the free tier (20+5 = 25); only one of them can
	codes := make(chan int, 5)
	var wg sync.WaitGroup
	for i := range 5 {
		batch := ids[pricing.FreeCensusSize-5+5*i : pricing.FreeCensusSize+5*i]
		wg.Go(func() {
			_, code := testRequest(t, http.MethodPut, token,
				&apicommon.AddCensusParticipantsRequest{MemberIDs: batch}, "processes", pid, "census")
			codes <- code
		})
	}
	wg.Wait()
	close(codes)
	refused := 0
	for code := range codes {
		if code == http.StatusPaymentRequired {
			refused++
		}
	}
	c.Assert(refused, qt.Equals, 4)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(pricing.FreeCensusSize))
}

// TestPublishedCensusGrowthWithinPaidPrice: PUT /processes/{processId}/census is the only
// way a published process's census can grow, and pay-per-process prices by census size — so
// the guard has to answer the price question. Refusing every paid process would refuse every
// published non-free one, which is the feature removed. Growth within the paid amount (here
// raised by headroom bought up front) is allowed; growth past it is refused with the projected
// quote.
func TestPublishedCensusGrowthWithinPaidPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "censusgrowth123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(growthTotalVoters)...)
	ids := memberIDs(members)

	// no 2FA channel, so only the base price is billed
	req := newVotingProcessRequest(orgAddress, ids[:growthBaseVoters])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)

	// pay it and publish, which is the state every priced published process is in
	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthBaseVoters))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	// headroom bought up to growthTierEnd
	paidCents := buyHeadroom(t, oid, growthCents(t, growthBaseVoters))
	grownCents := growthCents(t, growthTotalVoters)

	// 7 more voters fit what was paid: allowed, and they land in the census (202: the
	// whole-census questions need their on-chain maxCensusSize raised)
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthBaseVoters:growthTierEnd]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(7))

	// the ones that raise the price past it are refused, and nothing is added — but the
	// refusal has to say what the growth costs, or the user is simply stuck
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(refusal.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	c.Assert(refusal.Data.TotalCents, qt.Equals, grownCents)
	c.Assert(refusal.Data.PaidCents, qt.Equals, paidCents)
	c.Assert(refusal.Data.DueCents, qt.Equals, grownCents-paidCents)
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(growthTotalVoters))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(growthTierEnd))

	// the wallet is empty: buying the headroom opens a top-up for the difference only, not the
	// new total
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTotalVoters, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, grownCents-paidCents)

	// and until the money is verified the census is still refused
	requestAndAssertError(errors.ErrPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")

	// the webhook credits the wallet, then debits the envelope from the base up to the target
	// the session carries
	topUpMeta := fake.created[len(fake.created)-1].Metadata
	c.Assert(topUpMeta[stripe.MetadataKeyWalletTopUpOrg], qt.Equals, orgAddress.String())
	c.Assert(topUpMeta[stripe.MetadataKeyProcessTopUpFromCents], qt.Equals, strconv.FormatInt(int64(paidCents), 10))
	c.Assert(topUpMeta[stripe.MetadataKeyProcessTopUpCents], qt.Equals, strconv.FormatInt(int64(grownCents), 10))
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_headroom_1", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", grownCents-paidCents, topUpMeta))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, grownCents)

	// the same growth now lands
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(6))

	// a replay from another replica (fresh event id, same session) raises nothing: the
	// envelope must not creep upward on Stripe's retries
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_headroom_2", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", grownCents-paidCents, topUpMeta))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, grownCents)
	// credited once, debited once: the wallet is back where it was
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))

	// nothing is left to buy at that size
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTotalVoters, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
}

// TestQuestionEligibilityPricedAtItsSize: a question's eligibility change is priced at what its
// resize pushes on chain — the subset, or the whole census for an empty list — not at the census.
// Once a census has outgrown its price, narrowing a question still passes; reopening it to the
// whole census is refused.
func TestQuestionEligibilityPricedAtItsSize(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "eligibilityprice1")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(growthTotalVoters)...))

	req := newVotingProcessRequest(orgAddress, ids[:growthBaseVoters])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthBaseVoters))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// the census outgrows its price by a path the growth checks do not guard (40 voters cost more)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	_, _, err = testDB.AddCensusParticipantsByMemberIDs(census.ID.Hex(), ids[growthBaseVoters:])
	c.Assert(err, qt.IsNil)
	census.Size = growthTotalVoters
	_, err = testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// question 2 names one voter: naming two resizes it (202), well within the price
	restricted := got.Questions[1]
	requestAndAssertCode(http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: ids[:2]}, "processes", pid, "questions", restricted.ID.Hex(), "census")
	requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired, t, http.MethodPut, token,
		&apicommon.UpdateQuestionCensusRequest{MemberIDs: []string{}}, "processes", pid, "questions", restricted.ID.Hex(), "census")
}

// TestCensusGrowthIgnoresUnknownMembers: an id naming no member is a client error, not growth. At
// the edge of the paid price, a request with members that fit plus one typo adds the members and
// reports the typo, instead of refusing everything with a 402 that would sell room for nobody.
func TestCensusGrowthIgnoresUnknownMembers(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "growthtypo12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(growthTierEnd)...))

	// no 2FA channel: only the base price is billed
	req := newVotingProcessRequest(orgAddress, ids[:growthBaseVoters])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthBaseVoters))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	// headroom bought up to growthTierEnd: the edge of the paid price, which one more voter crosses
	buyHeadroom(t, objectID(c, pid), growthCents(t, growthBaseVoters))

	typo := bson.NewObjectID().Hex()
	withTypo := append(ids[growthBaseVoters:growthTierEnd:growthTierEnd], typo)
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: withTypo}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(7))
	c.Assert(strings.Join(added.Errors, "; "), qt.Contains, typo)
}

// markProcessPaid pays a priced draft from its payer's wallet, as the publish debit
// (debitProcessWallet) would but without publishing: it claims the once-per-organization add-ons
// first, funds the wallet with the claimed price, then charges it. It returns the price.
func markProcessPaid(t *testing.T, pid string) pricing.Cents {
	t.Helper()
	c := qt.New(t)
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(vp.OrgAddress)
	c.Assert(err, qt.IsNil)
	_, input, err := testAPI.processQuote(vp)
	c.Assert(err, qt.IsNil)
	quote, claims, err := testAPI.claimOrgAddOnsForPayment(vp, org, &input)
	if err != nil {
		testAPI.releaseRefusedOrgAddOnClaims(vp, claims)
	}
	c.Assert(err, qt.IsNil)
	fundWallet(t, walletPayer(org), quote.TotalCents)
	_, err = testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: quote, branding: input.Branding, customURL: input.CustomURL,
	})
	if err != nil {
		testAPI.releaseRefusedOrgAddOnClaims(vp, claims)
	}
	c.Assert(err, qt.IsNil)
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
	all := newOrgMembers(growthTierEnd + 7)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, all[:growthTierEnd]...))

	// no 2FA channel, so only the base price is billed
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
	// adding the other members prices it at growthTierEnd voters — refused, and the group is
	// left as it was
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
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, growthCents(t, growthTierEnd))
	stored, err := testDB.OrganizationMemberGroup(group.ID, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.MemberIDs, qt.HasLen, 5)

	// member creation: a paid process on the auto group, whose census every new member joins
	autoGroup, err := testDB.AutoMemberGroup(orgAddress)
	c.Assert(err, qt.IsNil)
	pid := groupProcess(autoGroup.ID.Hex())
	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthTierEnd))
	publish(pid)

	// one more member is the 35th voter, which raises the price: refused, and not created
	apiErr := putOrgMemberAndExpectError(t, token, orgAddress, all[growthTierEnd])
	c.Assert(apiErr.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	// the bulk import is refused the same way, naming the process to top up
	bulk := &apicommon.AddMembersRequest{Members: all[growthTierEnd:]}
	refusal = requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPost, token, bulk, organizationMembersURL(orgAddress.String()))
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, growthCents(t, growthTierEnd))
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(growthTierEnd+7))
	count, err := testDB.CountOrgMembers(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(count, qt.Equals, int64(growthTierEnd))

	// buying the headroom the refusal quoted lets the same import through
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTierEnd + 7, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, refusal.Data.DueCents)
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_memberbase_headroom", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", headroom.AmountCents, fake.created[len(fake.created)-1].Metadata))
	c.Assert(code, qt.Equals, http.StatusOK)

	postOrgMembers(t, token, orgAddress, all[growthTierEnd:]...)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(growthTierEnd+7))

	// an ended election never grows: once the auto-group process stops accepting votes, its
	// price no longer gates new members, though its census stays a propagation target
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	for _, qid := range vp.QuestionIDs {
		c.Assert(testDB.SetQuestionStatus(qid, db.QuestionStatusEnded), qt.IsNil)
	}
	putOrgMember(t, token, orgAddress, newOrgMembers(1)[0]) // the 42nd voter, past the price paid
}

// TestGrandfatheredProcessCensusGrowth: a process published before pay-per-process has no
// payment at all. Its first growth check grandfathers it at the price of the census it holds —
// that much stays free — and growth past it is refused like any paid process's.
func TestGrandfatheredProcessCensusGrowth(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "grandfathered")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	all := newOrgMembers(growthTierEnd + 1)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, all...))

	group := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, &apicommon.CreateOrganizationMemberGroupRequest{
			Title: "voters", Description: "group census", MemberIDs: ids[:growthBaseVoters],
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

	// the first growth check, even one adding nobody, grandfathers it at the price of the 27
	// voters it holds: that much is free
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census}), qt.IsNil)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Status, qt.Equals, db.ProcessPaymentPaid)
	c.Assert(payment.AmountCents, qt.Equals, growthCents(t, growthBaseVoters))

	// headroom bought up to growthTierEnd: 27 → 34 voters then fits the envelope
	paidCents := buyHeadroom(t, oid, payment.AmountCents)
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token,
		&apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[growthBaseVoters:growthTierEnd]},
		"organizations", orgAddress.String(), "groups", group.ID)
	payment, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, paidCents)

	// the 35th voter raises the price past the envelope: refused
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[growthTierEnd:]},
		"organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, paidCents)
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

	members := postOrgMembers(t, token, managedAddr, newOrgMembers(growthTotalVoters)...)
	ids := memberIDs(members)
	req := newVotingProcessRequest(managedAddr, ids[:growthBaseVoters])
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

	// publish, which debits the wallet for 27 voters without 2FA
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-growthCents(t, growthBaseVoters))

	// headroom bought up to growthTierEnd from the wallet, before growing
	paidCents, grownCents := growthCents(t, growthTierEnd), growthCents(t, growthTotalVoters)
	upFront := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTierEnd, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(upFront.AmountCents, qt.Equals, paidCents-growthCents(t, growthBaseVoters))

	// growth within what was paid costs nothing more
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthBaseVoters:growthTierEnd]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(7))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-paidCents)

	// growth past the price is refused with the difference, wallet untouched: the guard only
	// projects an upper bound, so it must never charge against it
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](
		http.StatusPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(refusal.Data.DueCents, qt.Equals, grownCents-paidCents)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-paidCents)

	// with the integrator's wallet short of the difference, the refusal is the 40175 its
	// census/checkout would answer, not the standalone checkout hint
	dueCents := grownCents - paidCents
	drain := 100_000 - paidCents - dueCents + 1
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: integratorAddr, ProcessID: bson.NewObjectID(), ProcessOrgAddress: managedAddr,
		AmountCents: drain, PriceCents: drain,
	}), qt.IsNil)
	short := requestAndParseWithAssertCode[struct {
		Code int                      `json:"code"`
		Data map[string]pricing.Cents `json:"data"`
	}](http.StatusPaymentRequired, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(short.Code, qt.Equals, errors.ErrInsufficientWalletBalance.Code)
	c.Assert(short.Data, qt.DeepEquals, map[string]pricing.Cents{"requiredCents": dueCents, "availableCents": dueCents - 1})
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: drain, IdempotencyKey: "cs_managed_growth_refill",
	}), qt.IsNil)

	// the integrator buys the headroom from its wallet — no checkout session, effective at once
	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTotalVoters, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.ClientSecret, qt.Equals, "")
	c.Assert(headroom.AmountCents, qt.Equals, grownCents-paidCents)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grownCents)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, grownCents)

	// the same growth now lands, and re-adding those members changes nothing
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(6))
	added = requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[growthTierEnd:]}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(0))
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grownCents)

	// buying the same size again is nothing left to buy, not a second debit
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTotalVoters, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, 100_000-grownCents)
}

// TestCensusCheckoutChargesTheMinimum: a headroom purchase whose shortfall is below the minimum
// charge (one voter in the first band, €0.66, on an empty wallet) tops up the minimum; the
// envelope is raised to the target price only and the rest stays in the wallet.
func TestCensusCheckoutChargesTheMinimum(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	token := testCreateUser(t, "censusminimum123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	paidCents := markProcessPaid(t, pid)

	headroom := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: pricedProcessVoters + 1, ReturnURL: "https://example.com/r"},
		"processes", pid, "census", "checkout")
	c.Assert(headroom.AmountCents, qt.Equals, pricing.MinimumChargeCents)
	session := fake.created[len(fake.created)-1]
	c.Assert(session.LineItems, qt.HasLen, 1)
	c.Assert(session.LineItems[0].AmountCents, qt.Equals, pricing.MinimumChargeCents)
	targetCents := paidCents + 66 // one more voter in the first band
	c.Assert(session.Metadata[stripe.MetadataKeyProcessTopUpCents], qt.Equals, strconv.FormatInt(int64(targetCents), 10))

	code := postSignedStripeEvent(t, testWebhookSecret, "evt_headroom_minimum", "checkout.session.completed",
		checkoutSessionObject(headroom.SessionID, "paid", pricing.MinimumChargeCents, session.Metadata))
	c.Assert(code, qt.Equals, http.StatusOK)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, targetCents)
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.MinimumChargeCents-66)
}

// TestOverlappingHeadroomTopUpsKeepTheStaleCredit: two headroom top-ups opened against the same
// envelope each priced the difference from it. Once the first is applied the envelope has moved,
// so the second debits nothing: its credit stays in the wallet, and a replay of the first changes
// nothing either.
func TestOverlappingHeadroomTopUpsKeepTheStaleCredit(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	token := testCreateUser(t, "overlaptopups123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	paidCents := markProcessPaid(t, pid)

	topUp := func(sessionID string, amount, target pricing.Cents) map[string]any {
		return checkoutSessionObject(sessionID, "paid", amount, map[string]string{
			stripe.MetadataKeyWalletTopUpOrg:        orgAddress.String(),
			stripe.MetadataKeyProcessID:             pid,
			stripe.MetadataKeyProcessTopUpFromCents: strconv.FormatInt(int64(paidCents), 10),
			stripe.MetadataKeyProcessTopUpCents:     strconv.FormatInt(int64(target), 10),
		})
	}
	// both opened at paidCents: one charging 500 up to +500, the other 1000 up to +1000
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_small", "checkout.session.completed",
		topUp("cs_overlap_small_"+pid, 500, paidCents+500))
	c.Assert(code, qt.Equals, http.StatusOK)
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_large", "checkout.session.completed",
		topUp("cs_overlap_large_"+pid, 1_000, paidCents+1_000))
	c.Assert(code, qt.Equals, http.StatusOK)

	payment, err := testDB.ProcessPayment(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, paidCents+500)
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_000))

	// a replay of the applied one credits and debits nothing
	code = postSignedStripeEvent(t, testWebhookSecret, "evt_overlap_small_replay", "checkout.session.completed",
		topUp("cs_overlap_small_"+pid, 500, paidCents+500))
	c.Assert(code, qt.Equals, http.StatusOK)
	wallet, err = testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(1_000))
}

// TestCensusCheckoutRefusesQuoteOnlySize: above 50 000 voters the quote has no price, so the
// headroom checkout answers 422 before opening a card session or debiting the wallet.
func TestCensusCheckoutRefusesQuoteOnlySize(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	installStripeWebhookService(t)
	token := testCreateUser(t, "censusquoteonly123")
	quoteOnly := &apicommon.ProcessCensusCheckoutRequest{CensusSize: 50_001, ReturnURL: "https://example.com/r"}

	// card: a standard organization's paid process
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	pid := newPricedVotingProcess(t, token, orgAddress)
	markProcessPaid(t, pid)
	requestAndAssertError(errors.ErrQuoteRequired, t, http.MethodPost, token, quoteOnly,
		"processes", pid, "census", "checkout")
	c.Assert(fake.created, qt.HasLen, 0)

	// wallet: a managed organization's published process
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_quote_only")
	members := postOrgMembers(t, token, managedAddr, newOrgMembers(pricedProcessVoters)...)
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(managedAddr, memberIDs(members)), processesCreateEndpoint)
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: integratorAddr, AmountCents: 100_000, IdempotencyKey: "cs_quote_only_topup",
	}), qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", created.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))
	before, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	requestAndAssertError(errors.ErrQuoteRequired, t, http.MethodPost, token, quoteOnly,
		"processes", created.ProcessID, "census", "checkout")
	after, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(after.BalanceCents, qt.Equals, before.BalanceCents)
	c.Assert(fake.created, qt.HasLen, 0)
}
