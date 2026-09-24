package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
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
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(0))

	// growing out of the free tier costs the whole priced amount
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: ids[pricing.FreeCensusSize:]}, "processes", pid, "census")
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, pricing.Cents(1_500))
}

// TestParallelCensusGrowthStaysWithinPrice: the growth check and the write it allows are one
// step per organization. Without that, parallel PUTs each pass the check against the same size
// and together grow a free process far past what is free — five adds of 5 on a 5-voter process
// would reach 30.
func TestParallelCensusGrowthStaysWithinPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "parallelgrowth12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(30)...))

	req := newVotingProcessRequest(orgAddress, ids[:5])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// each batch alone fits the free tier (5+5 = 10); only one of them can
	codes := make(chan int, 5)
	var wg sync.WaitGroup
	for i := range 5 {
		batch := ids[5+5*i : 10+5*i]
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

	// pay it and publish, which is the state every priced published process is in
	c.Assert(markProcessPaid(t, pid), qt.Equals, pricing.Cents(1_500))
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
	c.Assert(refusal.Data.TotalCents, qt.Equals, pricing.Cents(2_000))
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(1_500))
	c.Assert(refusal.Data.DueCents, qt.Equals, pricing.Cents(500))
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(25))
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Census.Size, qt.Equals, int64(19))
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
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(25)...))

	req := newVotingProcessRequest(orgAddress, ids[:15])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, pricing.Cents(1_500))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	// the census outgrows its €15 by a path the growth checks do not guard (25 voters cost €20)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	oid, err := bson.ObjectIDFromHex(pid)
	c.Assert(err, qt.IsNil)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	_, _, err = testDB.AddCensusParticipantsByMemberIDs(census.ID.Hex(), ids[15:])
	c.Assert(err, qt.IsNil)
	census.Size = 25
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
	ids := memberIDs(postOrgMembers(t, token, orgAddress, newOrgMembers(19)...))

	// no 2FA channel: 15 to 19 voters all cost €15, the 20th costs €20
	req := newVotingProcessRequest(orgAddress, ids[:15])
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, pricing.Cents(1_500))
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("publish job error: %s", job.Errors))

	typo := bson.NewObjectID().Hex()
	added := requestAndParseWithAssertCode[apicommon.UpdateProcessCensusResponse](
		http.StatusAccepted, t, http.MethodPut, token,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: append(ids[15:19:19], typo)}, "processes", pid, "census")
	c.Assert(added.Added, qt.Equals, uint32(4))
	c.Assert(strings.Join(added.Errors, "; "), qt.Contains, typo)
}

// markProcessPaid records a completed checkout for the process at its current quote, the state
// the webhook leaves a paid process in, and returns the amount paid.
func markProcessPaid(t *testing.T, pid string) pricing.Cents {
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
// The refusal names the process to buy headroom for.
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
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(0))
	c.Assert(refusal.Data.DueCents, qt.Equals, pricing.Cents(1_500))
	stored, err := testDB.OrganizationMemberGroup(group.ID, orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.MemberIDs, qt.HasLen, 5)

	// member creation: a paid process on the auto group, whose census every new member joins
	autoGroup, err := testDB.AutoMemberGroup(orgAddress)
	c.Assert(err, qt.IsNil)
	pid := groupProcess(autoGroup.ID.Hex())
	c.Assert(markProcessPaid(t, pid), qt.Equals, pricing.Cents(1_500))
	publish(pid)

	// one more member is the 20th voter, which raises the price: refused, and not created
	apiErr := putOrgMemberAndExpectError(t, token, orgAddress, all[19])
	c.Assert(apiErr.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	// the bulk import is refused the same way, naming the process to top up
	bulk := &apicommon.AddMembersRequest{Members: all[19:]}
	refusal = requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPost, token, bulk, organizationMembersURL(orgAddress.String()))
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(1_500))
	c.Assert(refusal.Data.CensusSize, qt.Equals, int64(29))
	count, err := testDB.CountOrgMembers(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(count, qt.Equals, int64(19))

	// an ended election never grows: once the auto-group process stops accepting votes, its
	// price no longer gates new members, though its census stays a propagation target
	vp, err := testDB.VotingProcess(objectID(c, pid))
	c.Assert(err, qt.IsNil)
	for _, qid := range vp.QuestionIDs {
		c.Assert(testDB.SetQuestionStatus(qid, db.QuestionStatusEnded), qt.IsNil)
	}
	putOrgMember(t, token, orgAddress, all[19])
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
	c.Assert(payment.AmountCents, qt.Equals, pricing.Cents(1_500))

	// the 20th voter raises the price past the grandfathered envelope: refused
	refusal := requestAndParseWithAssertCode[censusGrowthRefusal](http.StatusPaymentRequired,
		t, http.MethodPut, token, &apicommon.UpdateOrganizationMemberGroupsRequest{AddMembers: ids[19:]},
		"organizations", orgAddress.String(), "groups", group.ID)
	c.Assert(refusal.Data.ProcessID, qt.Equals, pid)
	c.Assert(refusal.Data.PaidCents, qt.Equals, pricing.Cents(1_500))
}

// censusGrowthRefusal is the 402 of a census that outgrew its price, typed so the test reads
// the quote the client is meant to act on rather than a map.
type censusGrowthRefusal struct {
	Code int                                `json:"code"`
	Data apicommon.ProcessCensusGrowthQuote `json:"data"`
}
