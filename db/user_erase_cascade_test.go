package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestEraseUserCascade seeds an organization in every org-keyed collection of the erasure cascade,
// plus an object stamped with its sole admin's email. Erasing the admin leaves no row of that
// organization or that email, keeps the wallet, its ledger and the payment rows (the audit trail),
// and leaves another organization's API key and another user's object alone.
func TestEraseUserCascade(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	ctx := context.Background()

	target := &User{Email: "cascade@test.com", Password: "pass", FirstName: "Cas", LastName: "Cade", Verified: true}
	targetID, err := testDB.SetUser(target)
	c.Assert(err, qt.IsNil)
	org, otherOrg := common.Address{0x0C, 0x01}, common.Address{0x0C, 0x02}
	c.Assert(testDB.SetOrganization(&Organization{Address: org, Creator: target.Email, Country: "ES"}), qt.IsNil)
	seedOrg(t, otherOrg)

	// organizationInvites, orgMembers, census, censusParticipants, orgMemberGroups
	memberID, censusID := seedMemberAndCensus(t, org, "cascade")
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{
		ParticipantID: memberID.Hex(), CensusID: censusID.Hex(), CreatedAt: time.Now(),
	}), qt.IsNil)
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		InvitationCode: "cascadecode", OrganizationAddress: org, CurrentUserID: targetID,
		NewUserEmail: "invitee@test.com", Role: AdminRole, Expiration: time.Now().Add(time.Hour),
	}), qt.IsNil)
	c.Assert(testDB.EnsureAutoMemberGroup(org), qt.IsNil)

	// processes and processBundles (legacy), with the CSP rows of a bundle and of its election
	legacyElection := internal.HexBytes{0xe1}
	insertLegacyProcess(c, &Process{OrgAddress: org, Address: legacyElection})
	bundle := &ProcessesBundle{OrgAddress: org, Census: Census{ID: censusID}}
	insertLegacyBundle(c, bundle)
	bundleAnchor := internal.HexBytes(bundle.ID[:])
	c.Assert(testDB.SetCSPAuth(internal.HexBytes("cascade-tok-bundle"), internal.HexBytes("u1"), bundleAnchor, ""), qt.IsNil)

	// votingProcesses and processesQuestions, with the CSP rows of the process and of its question
	vpID, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Published: true})
	c.Assert(err, qt.IsNil)
	upstreamID := internal.HexBytes{0xe2}
	_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: vpID, OrgAddress: org, UpstreamID: upstreamID})
	c.Assert(err, qt.IsNil)
	vpAnchor := internal.HexBytes(vpID[:])
	c.Assert(testDB.SetCSPAuth(internal.HexBytes("cascade-tok-vp"), internal.HexBytes("u1"), vpAnchor, ""), qt.IsNil)
	for i, electionID := range []internal.HexBytes{legacyElection, upstreamID} {
		_, err := testDB.cspTokensStatus.InsertOne(ctx, CSPProcess{
			ID: internal.HexBytes{0xcc, byte(i)}, UserID: internal.HexBytes("u1"), ProcessID: electionID,
		})
		c.Assert(err, qt.IsNil)
	}

	// jobs, meteredVotes, apiKeys (one of another org too), objects (one of another user too)
	c.Assert(testDB.CreateJob("cascadejob", JobTypeOrgMembers, org, 1), qt.IsNil)
	_, err = testDB.MeterRelayedVote(org, internal.HexBytes{0xe3})
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.SetAPIKey(&APIKey{ID: "cascade-key", OrgAddress: org, Hash: "cascade-hash"}), qt.IsNil)
	c.Assert(testDB.SetAPIKey(&APIKey{ID: "other-key", OrgAddress: otherOrg, Hash: "other-hash"}), qt.IsNil)
	c.Assert(testDB.SetObject("cascade-object", target.Email, "image/png", []byte{1}), qt.IsNil)
	c.Assert(testDB.SetObject("other-object", "someone@test.com", "image/png", []byte{2}), qt.IsNil)

	// the audit trail: wallet, ledger and payment rows
	c.Assert(testDB.CreditWallet(WalletCredit{OrgAddress: org, AmountCents: 100, IdempotencyKey: "cascade-topup"}), qt.IsNil)
	c.Assert(testDB.SetProcessPaymentCache(SetProcessPaymentCacheParams{
		ProcessID: vpID, OrgAddress: org, Status: ProcessPaymentPaid, AmountCents: 100,
	}), qt.IsNil)

	erased := []struct {
		coll   *mongo.Collection
		filter bson.M
	}{
		{testDB.organizations, bson.M{"_id": org}},
		{testDB.users, bson.M{"organizations._id": org}},
		{testDB.organizationInvites, bson.M{"organizationAddress": org}},
		{testDB.orgMembers, bson.M{"orgAddress": org}},
		{testDB.orgMemberGroups, bson.M{"orgAddress": org}},
		{testDB.censuses, bson.M{"orgAddress": org}},
		{testDB.censusParticipants, bson.M{"censusId": censusID.Hex()}},
		{testDB.processes, bson.M{"orgAddress": org}},
		{testDB.processBundles, bson.M{"orgAddress": org}},
		{testDB.votingProcesses, bson.M{"orgAddress": org}},
		{testDB.processesQuestions, bson.M{"orgAddress": org}},
		{testDB.cspTokens, bson.M{"bundleid": bundleAnchor}},
		{testDB.cspTokens, bson.M{"bundleid": vpAnchor}},
		{testDB.cspTokensStatus, bson.M{"processid": legacyElection}},
		{testDB.cspTokensStatus, bson.M{"processid": upstreamID}},
		{testDB.jobs, bson.M{"orgAddress": org}},
		{testDB.meteredVotes, bson.M{"orgAddress": org}},
		{testDB.apiKeys, bson.M{"orgAddress": org}},
		{testDB.objects, bson.M{"userId": target.Email}},
	}
	kept := []struct {
		coll   *mongo.Collection
		filter bson.M
	}{
		{testDB.wallets, bson.M{"_id": org}},
		{testDB.walletLedger, bson.M{"orgAddress": org}},
		{testDB.processPayments, bson.M{"orgAddress": org}},
		{testDB.apiKeys, bson.M{"orgAddress": otherOrg}},
		{testDB.objects, bson.M{"userId": "someone@test.com"}},
	}
	count := func(coll *mongo.Collection, filter bson.M) int64 {
		n, err := coll.CountDocuments(ctx, filter)
		c.Assert(err, qt.IsNil)
		return n
	}
	for _, e := range append(erased, kept...) {
		c.Assert(count(e.coll, e.filter) > 0, qt.IsTrue, qt.Commentf("not seeded: %s %v", e.coll.Name(), e.filter))
	}

	_, err = testDB.EraseUser(targetID)
	c.Assert(err, qt.IsNil)

	for _, e := range erased {
		c.Check(count(e.coll, e.filter), qt.Equals, int64(0), qt.Commentf("left behind: %s %v", e.coll.Name(), e.filter))
	}
	for _, k := range kept {
		c.Check(count(k.coll, k.filter) > 0, qt.IsTrue, qt.Commentf("erased: %s %v", k.coll.Name(), k.filter))
	}
}
