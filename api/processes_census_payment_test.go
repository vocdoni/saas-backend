package api

import (
	"net/http"
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
}

// censusGrowthRefusal is the 402 of a census that outgrew its price, typed so the test reads
// the quote the client is meant to act on rather than a map.
type censusGrowthRefusal struct {
	Code int                                `json:"code"`
	Data apicommon.ProcessCensusGrowthQuote `json:"data"`
}
