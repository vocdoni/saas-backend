package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// seedOrg persists an organization at addr (with Country so phone hashing would work) and returns it.
func seedOrg(t *testing.T, addr common.Address) {
	t.Helper()
	c := qt.New(t)
	c.Assert(testDB.SetOrganization(&Organization{Address: addr, Country: "ES"}), qt.IsNil)
}

// seedMemberAndCensus creates an org member and a census owned by addr, returning their ids.
// Both are prerequisites for several downstream collections (census participants, member groups).
func seedMemberAndCensus(t *testing.T, addr common.Address, suffix string) (memberID, censusID primitive.ObjectID) {
	t.Helper()
	c := qt.New(t)
	member := &OrgMember{
		ID:           primitive.NewObjectID(),
		OrgAddress:   addr,
		MemberNumber: "mn-" + suffix,
		Email:        "m-" + suffix + "@x.test",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	_, err := testDB.SetOrgMember("test_salt", member)
	c.Assert(err, qt.IsNil)
	censusIDStr, err := testDB.SetCensus(&Census{
		OrgAddress:  addr,
		AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber},
		TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	})
	c.Assert(err, qt.IsNil)
	censusOID, err := primitive.ObjectIDFromHex(censusIDStr)
	c.Assert(err, qt.IsNil)
	return member.ID, censusOID
}

// TestDeleteCSPByBundleAndProcess covers DeleteCSPAuthByBundle and DeleteCSPProcessByProcess:
// they remove only the rows matching the given bundle/process and leave unrelated rows intact.
func TestDeleteCSPByBundleAndProcess(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	bundleA := internal.HexBytes([]byte("bundleA"))
	bundleB := internal.HexBytes([]byte("bundleB"))
	processA := internal.HexBytes([]byte("processA"))
	processB := internal.HexBytes([]byte("processB"))

	// seed CSP auth tokens across two bundles
	c.Assert(testDB.SetCSPAuth(internal.HexBytes("tokA1"), internal.HexBytes("u1"), bundleA, ""), qt.IsNil)
	c.Assert(testDB.SetCSPAuth(internal.HexBytes("tokA2"), internal.HexBytes("u2"), bundleA, ""), qt.IsNil)
	c.Assert(testDB.SetCSPAuth(internal.HexBytes("tokB1"), internal.HexBytes("u1"), bundleB, ""), qt.IsNil)

	// delete bundle A's tokens → only bundle B's token survives
	n, err := testDB.DeleteCSPAuthByBundle(bundleA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))
	// bundle A's tokens are gone
	_, err = testDB.LastCSPAuth(internal.HexBytes("u1"), bundleA)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	// bundle B's token is untouched
	last, err := testDB.LastCSPAuth(internal.HexBytes("u1"), bundleB)
	c.Assert(err, qt.IsNil)
	c.Assert(last.Token, qt.DeepEquals, internal.HexBytes("tokB1"))

	// nil bundleID is rejected
	_, err = testDB.DeleteCSPAuthByBundle(nil)
	c.Assert(err, qt.ErrorIs, ErrBadInputs)

	// seed two CSP process-status rows (ConsumeCSPProcess requires a verified token) and delete
	// only process A's.
	c.Assert(testDB.VerifyCSPAuth(internal.HexBytes("tokB1")), qt.IsNil)
	c.Assert(testDB.ConsumeCSPProcess(internal.HexBytes("tokB1"), processA, internal.HexBytes("addr")), qt.IsNil)
	c.Assert(testDB.ConsumeCSPProcess(internal.HexBytes("tokB1"), processB, internal.HexBytes("addr")), qt.IsNil)
	del, err := testDB.DeleteCSPProcessByProcess(processA)
	c.Assert(err, qt.IsNil)
	c.Assert(del, qt.Equals, int64(1))
	// process A's status row is gone (lookup returns ErrTokenNotFound); process B's row still exists.
	_, err = testDB.CSPProcessByUserAndProcess(internal.HexBytes("u1"), processA)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	bStatus, err := testDB.CSPProcessByUserAndProcess(internal.HexBytes("u1"), processB)
	c.Assert(err, qt.IsNil)
	c.Assert(bStatus.Used, qt.IsTrue)

	// nil processID is rejected
	_, err = testDB.DeleteCSPProcessByProcess(nil)
	c.Assert(err, qt.ErrorIs, ErrBadInputs)
}

// TestDeleteProcessesByOrg covers DeleteProcessesByOrg: it removes every process (drafts and
// published) for the org, leaves other orgs untouched, and rejects a zero address.
func TestDeleteProcessesByOrg(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	seedOrg(t, orgA)
	seedOrg(t, orgB)

	// orgA: one draft (nil Address) + one published (non-nil Address); orgB: one published
	draftA, err := testDB.SetProcess(&Process{OrgAddress: orgA})
	c.Assert(err, qt.IsNil)
	pubA, err := testDB.SetProcess(&Process{OrgAddress: orgA, Address: internal.HexBytes{0xaa}})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetProcess(&Process{OrgAddress: orgB, Address: internal.HexBytes{0xbb}})
	c.Assert(err, qt.IsNil)

	n, err := testDB.DeleteProcessesByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	// orgA's processes are gone
	_, err = testDB.Process(draftA)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	_, err = testDB.Process(pubA)
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	// orgB's process survives
	_, err = testDB.ProcessByAddress(internal.HexBytes{0xbb})
	c.Assert(err, qt.IsNil)

	// zero address rejected
	_, err = testDB.DeleteProcessesByOrg(common.Address{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestDeleteProcessBundlesByOrg covers DeleteProcessBundlesByOrg: it removes the org's bundles
// only and rejects a zero address.
func TestDeleteProcessBundlesByOrg(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	seedOrg(t, orgA)
	seedOrg(t, orgB)
	_, censusA := seedMemberAndCensus(t, orgA, "a")
	_, censusB := seedMemberAndCensus(t, orgB, "b")

	_, err := testDB.SetProcessBundle(&ProcessesBundle{OrgAddress: orgA, Census: Census{ID: censusA}})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetProcessBundle(&ProcessesBundle{OrgAddress: orgB, Census: Census{ID: censusB}})
	c.Assert(err, qt.IsNil)

	n, err := testDB.DeleteProcessBundlesByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))

	aBundles, err := testDB.ProcessBundlesByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(aBundles, qt.HasLen, 0)
	bBundles, err := testDB.ProcessBundlesByOrg(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(bBundles, qt.HasLen, 1)

	_, err = testDB.DeleteProcessBundlesByOrg(common.Address{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestDeleteCensusParticipantsByCensus covers DeleteCensusParticipantsByCensus.
func TestDeleteCensusParticipantsByCensus(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	seedOrg(t, orgA)
	seedOrg(t, orgB)
	// each census needs a real member (validateCensusParticipant checks the member exists)
	memberA1, censusA := seedMemberAndCensus(t, orgA, "a1")
	memberA2, _ := seedMemberAndCensus(t, orgA, "a2") // second member under orgA, same census below
	memberB, censusB := seedMemberAndCensus(t, orgB, "b")

	// two participants under censusA (using the two orgA members), one under censusB
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{ParticipantID: memberA1.Hex(), CensusID: censusA.Hex()}), qt.IsNil)
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{ParticipantID: memberA2.Hex(), CensusID: censusA.Hex()}), qt.IsNil)
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{ParticipantID: memberB.Hex(), CensusID: censusB.Hex()}), qt.IsNil)

	n, err := testDB.DeleteCensusParticipantsByCensus(censusA.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	aCount, err := testDB.CountCensusParticipants(censusA.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(aCount, qt.Equals, int64(0))
	bCount, err := testDB.CountCensusParticipants(censusB.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(bCount, qt.Equals, int64(1))

	_, err = testDB.DeleteCensusParticipantsByCensus("")
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestDeleteAllOrgMemberGroups covers DeleteAllOrgMemberGroups: it drops the auto group (and any
// manual groups) for the org and leaves other orgs intact.
func TestDeleteAllOrgMemberGroups(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	seedOrg(t, orgA)
	seedOrg(t, orgB)
	memberA, _ := seedMemberAndCensus(t, orgA, "a")

	// auto groups for both orgs
	c.Assert(testDB.EnsureAutoMemberGroup(orgA), qt.IsNil)
	c.Assert(testDB.EnsureAutoMemberGroup(orgB), qt.IsNil)
	// a manual group on orgA (requires a real member id)
	_, err := testDB.CreateOrganizationMemberGroup(&OrganizationMemberGroup{
		OrgAddress: orgA, Title: "manual A", MemberIDs: []string{memberA.Hex()},
	})
	c.Assert(err, qt.IsNil)

	n, err := testDB.DeleteAllOrgMemberGroups(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2)) // auto + manual

	_, aGroups, err := testDB.OrganizationMemberGroups(orgA, 1, 100)
	c.Assert(err, qt.IsNil)
	c.Assert(aGroups, qt.HasLen, 0)
	_, bGroups, err := testDB.OrganizationMemberGroups(orgB, 1, 100)
	c.Assert(err, qt.IsNil)
	c.Assert(bGroups, qt.HasLen, 1) // orgB's auto group survives

	_, err = testDB.DeleteAllOrgMemberGroups(common.Address{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestDeleteJobsByOrg covers DeleteJobsByOrg: it removes the org's jobs only.
func TestDeleteJobsByOrg(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	c.Assert(testDB.CreateJob("jobA1", JobTypeOrgMembers, orgA, 10), qt.IsNil)
	c.Assert(testDB.CreateJob("jobA2", JobTypePublishProcess, orgA, 1), qt.IsNil)
	c.Assert(testDB.CreateJob("jobB1", JobTypeOrgMembers, orgB, 5), qt.IsNil)

	n, err := testDB.DeleteJobsByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	_, err = testDB.Job("jobA1")
	c.Assert(err, qt.ErrorIs, ErrNotFound)
	_, err = testDB.Job("jobB1")
	c.Assert(err, qt.IsNil) // orgB's job survives

	_, err = testDB.DeleteJobsByOrg(common.Address{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestDeleteInvitationsByOrg covers DeleteInvitationsByOrg: it removes the org's pending invites
// only (using DeleteMany, unlike the single-invitation helpers which silently no-op on a miss).
func TestDeleteInvitationsByOrg(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")
	// CreateInvitation validates that the inviting user exists and belongs to the org.
	seedOrg(t, orgA)
	seedOrg(t, orgB)
	inviterA, err := testDB.SetUser(&User{
		Email: "inviter-a@x.test", Password: "p", FirstName: "I", LastName: "A",
		Organizations: []OrganizationUser{{Address: orgA, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)
	inviterB, err := testDB.SetUser(&User{
		Email: "inviter-b@x.test", Password: "p", FirstName: "I", LastName: "B",
		Organizations: []OrganizationUser{{Address: orgB, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)
	expiration := time.Now().Add(time.Hour)

	// three invites for orgA, one for orgB. invitationCode must match ^[\w]{6,}$.
	orgACodes := []string{"codeA1xxx", "codeA2xxx", "codeA3xxx"}
	orgAEmails := []string{"a1@x.test", "a2@x.test", "a3@x.test"}
	for i, email := range orgAEmails {
		c.Assert(testDB.CreateInvitation(&OrganizationInvite{
			OrganizationAddress: orgA,
			CurrentUserID:       inviterA,
			NewUserEmail:        email,
			Role:                ManagerRole,
			Expiration:          expiration,
			InvitationCode:      orgACodes[i],
		}), qt.IsNil)
	}
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		OrganizationAddress: orgB,
		CurrentUserID:       inviterB,
		NewUserEmail:        "b1@x.test",
		Role:                ManagerRole,
		Expiration:          expiration,
		InvitationCode:      "codeB1xxx",
	}), qt.IsNil)

	n, err := testDB.DeleteInvitationsByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(3))

	// orgA has no pending invites left; orgB still has one.
	aPending, err := testDB.PendingInvitations(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(aPending, qt.HasLen, 0)
	bPending, err := testDB.PendingInvitations(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(bPending, qt.HasLen, 1)

	// deleting orgA again deletes nothing (already empty).
	n, err = testDB.DeleteInvitationsByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(0))

	_, err = testDB.DeleteInvitationsByOrg(common.Address{})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestRemoveOrganizationFromAllUsers covers RemoveOrganizationFromAllUsers: it pulls the org out
// of every user's Organizations list, across multiple users, leaving other org links intact.
func TestRemoveOrganizationFromAllUsers(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA := common.HexToAddress("0xa0000000000000000000000000000000000000aA")
	orgB := common.HexToAddress("0xb0000000000000000000000000000000000000bB")

	// two users, both members of orgA; one is also a member of orgB.
	_, err := testDB.SetUser(&User{
		Email: "u1@x.test", Password: "p", FirstName: "U", LastName: "1",
		Organizations: []OrganizationUser{{Address: orgA, Role: AdminRole}, {Address: orgB, Role: ManagerRole}},
	})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetUser(&User{
		Email: "u2@x.test", Password: "p", FirstName: "U", LastName: "2",
		Organizations: []OrganizationUser{{Address: orgA, Role: ManagerRole}},
	})
	c.Assert(err, qt.IsNil)

	c.Assert(testDB.RemoveOrganizationFromAllUsers(orgA), qt.IsNil)

	// u1 keeps only orgB; u2 has no orgs left.
	u1, err := testDB.UserByEmail("u1@x.test")
	c.Assert(err, qt.IsNil)
	c.Assert(u1.Organizations, qt.HasLen, 1)
	c.Assert(u1.Organizations[0].Address, qt.Equals, orgB)
	u2, err := testDB.UserByEmail("u2@x.test")
	c.Assert(err, qt.IsNil)
	c.Assert(u2.Organizations, qt.HasLen, 0)

	c.Assert(testDB.RemoveOrganizationFromAllUsers(common.Address{}), qt.ErrorIs, ErrInvalidData)
}
