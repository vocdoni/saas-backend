package db

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// revocationFixture is a census of three members backed by one voting process with two published
// questions: one restricted to alice and bob, one open to the whole census.
type revocationFixture struct {
	census     *Census
	processID  primitive.ObjectID
	restricted primitive.ObjectID
	openToAll  primitive.ObjectID
	upstream   internal.HexBytes
	alice      *OrgMember
	bob        *OrgMember
	carol      *OrgMember
}

func setupRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	c := qt.New(t)

	c.Assert(testDB.SetOrganization(&Organization{
		Address: testOrgAddress, Active: true, CreatedAt: time.Now(),
	}), qt.IsNil)

	newMember := func(number, email string) *OrgMember {
		m := &OrgMember{
			OrgAddress:   testOrgAddress,
			MemberNumber: number,
			Name:         number,
			Email:        email,
		}
		id, err := testDB.SetOrgMember(testSalt, m)
		c.Assert(err, qt.IsNil)
		stored, err := testDB.OrgMember(testOrgAddress, id)
		c.Assert(err, qt.IsNil)
		return stored
	}
	alice := newMember("revoke-alice", "revoke-alice@example.com")
	bob := newMember("revoke-bob", "revoke-bob@example.com")
	carol := newMember("revoke-carol", "revoke-carol@example.com")

	census := &Census{
		OrgAddress:  testOrgAddress,
		AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber},
		TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
	}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	added, memberErrs, err := testDB.AddCensusParticipantsByMemberIDs(censusID,
		[]string{alice.ID.Hex(), bob.ID.Hex(), carol.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(memberErrs, qt.HasLen, 0)
	c.Assert(added, qt.Equals, 3)

	processID, err := testDB.SetVotingProcess(&VotingProcess{
		OrgAddress: testOrgAddress,
		CensusID:   census.ID,
		Title:      MultiLangString{"default": "revocation process"},
	})
	c.Assert(err, qt.IsNil)

	restricted, err := testDB.SetQuestion(&VotingProcessQuestion{
		ProcessID:         processID,
		OrgAddress:        testOrgAddress,
		Order:             0,
		Type:              VotingTypeSingleChoice,
		EligibleMemberIDs: []string{alice.ID.Hex(), bob.ID.Hex()},
	})
	c.Assert(err, qt.IsNil)
	openToAll, err := testDB.SetQuestion(&VotingProcessQuestion{
		ProcessID:  processID,
		OrgAddress: testOrgAddress,
		Order:      1,
		Type:       VotingTypeSingleChoice,
	})
	c.Assert(err, qt.IsNil)

	upstream := internal.HexBytes{0xE1, 0xEC, 0x71, 0x01}
	c.Assert(testDB.SetQuestionPublished(restricted, upstream, "url-1", QuestionStatusReady), qt.IsNil)
	c.Assert(testDB.SetQuestionPublished(
		openToAll, internal.HexBytes{0xE1, 0xEC, 0x71, 0x02}, "url-2", QuestionStatusReady), qt.IsNil)

	return &revocationFixture{
		census: census, processID: processID, restricted: restricted, openToAll: openToAll,
		upstream: upstream, alice: alice, bob: bob, carol: carol,
	}
}

func TestVotingProcessesByCensus(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	got, err := testDB.VotingProcessesByCensus([]string{f.census.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 1)
	c.Assert(got[0].ID, qt.Equals, f.processID)

	// an unrelated census resolves to nothing
	other := &Census{OrgAddress: testOrgAddress}
	otherID, err := testDB.SetCensus(other)
	c.Assert(err, qt.IsNil)
	got, err = testDB.VotingProcessesByCensus([]string{otherID})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)

	// a census id that is not an ObjectID is rejected rather than silently matching nothing
	_, err = testDB.VotingProcessesByCensus([]string{"not-an-object-id"})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

func TestOngoingQuestionsByCensuses(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	got, err := testDB.OngoingQuestionsByCensuses([]string{f.census.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 2)

	// PAUSED still holds its voters; ENDED releases them
	c.Assert(testDB.SetQuestionStatus(f.restricted, QuestionStatusPaused), qt.IsNil)
	c.Assert(testDB.SetQuestionStatus(f.openToAll, QuestionStatusEnded), qt.IsNil)
	got, err = testDB.OngoingQuestionsByCensuses([]string{f.census.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 1)
	c.Assert(got[0].ID, qt.Equals, f.restricted)

	// a draft question is not ongoing: it has no election to be signed for
	c.Assert(testDB.ResetQuestionsPublish(f.processID), qt.IsNil)
	c.Assert(testDB.SetQuestionStatus(f.restricted, ""), qt.IsNil)
	got, err = testDB.OngoingQuestionsByCensuses([]string{f.census.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)
}

func TestMembersWithUsedCSPProcesses(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	all := []string{f.alice.ID.Hex(), f.bob.ID.Hex(), f.carol.ID.Hex()}

	got, err := testDB.MembersWithUsedCSPProcesses([]internal.HexBytes{f.upstream}, all)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)

	seedUsedCSPProcess(t, f.alice.ID.Hex(), internal.HexBytes(f.processID[:]), f.upstream)

	got, err = testDB.MembersWithUsedCSPProcesses([]internal.HexBytes{f.upstream}, all)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, []string{f.alice.ID.Hex()})

	// scoped to the elections asked about
	got, err = testDB.MembersWithUsedCSPProcesses([]internal.HexBytes{{0xDE, 0xAD}}, all)
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.HasLen, 0)

	// an id that is not hex cannot hold a token, and must not panic the way
	// internal.HexBytesFromString would
	got, err = testDB.MembersWithUsedCSPProcesses(
		[]internal.HexBytes{f.upstream}, []string{"definitely-not-hex", f.alice.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(got, qt.DeepEquals, []string{f.alice.ID.Hex()})
}

func TestRevokeMembersFromCensuses(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	// alice has been signed for, and holds a live auth session
	seedUsedCSPProcess(t, f.alice.ID.Hex(), internal.HexBytes(f.processID[:]), f.upstream)

	emptied, err := testDB.RevokeMembersFromCensuses(
		[]string{f.census.ID.Hex()}, []string{f.alice.ID.Hex()})
	c.Assert(err, qt.IsNil)

	// the participant row is gone: this is what the CSP re-checks at sign time
	participants, err := testDB.CensusParticipants(f.census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(participants, qt.HasLen, 2)

	// the census size is a plain recount
	census, err := testDB.Census(f.census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(census.Size, qt.Equals, int64(2))

	// the eligibility list dropped her but kept bob, so the question is still a subset
	restricted, err := testDB.Question(f.restricted)
	c.Assert(err, qt.IsNil)
	c.Assert(restricted.EligibleMemberIDs, qt.DeepEquals, []string{f.bob.ID.Hex()})
	c.Assert(emptied, qt.HasLen, 0)

	// the auth session is dropped, forcing a fresh login that re-checks the census
	auth, err := testDB.CSPAuth(internal.HexBytes(append([]byte{0xC5, 0x90}, f.upstream...)))
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(auth, qt.IsNil)

	// ...but the consumption row survives. Deleting it would let a removed-then-re-added member
	// be signed for a second address, producing a second nullifier and a double vote the chain
	// accepts.
	consumed, err := testDB.MembersWithUsedCSPProcesses(
		[]internal.HexBytes{f.upstream}, []string{f.alice.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(consumed, qt.DeepEquals, []string{f.alice.ID.Hex()})
}

func TestRevokeMembersFromCensusesReportsEmptiedQuestions(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	// removing every named member opens the restricted question to the whole census, while its
	// election is still sized on chain for the two it named
	emptied, err := testDB.RevokeMembersFromCensuses(
		[]string{f.census.ID.Hex()}, []string{f.alice.ID.Hex(), f.bob.ID.Hex()})
	c.Assert(err, qt.IsNil)
	c.Assert(emptied, qt.HasLen, 1)
	c.Assert(emptied[0].ID, qt.Equals, f.restricted)
	c.Assert(emptied[0].UpstreamID, qt.DeepEquals, f.upstream)

	restricted, err := testDB.Question(f.restricted)
	c.Assert(err, qt.IsNil)
	c.Assert(restricted.EligibleMemberIDs, qt.HasLen, 0)

	// the question that was already whole-census never needed reporting
	openToAll, err := testDB.Question(f.openToAll)
	c.Assert(err, qt.IsNil)
	c.Assert(openToAll.EligibleMemberIDs, qt.HasLen, 0)
}

func TestRevokeMembersFromCensusesPrunesDrafts(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	// a draft question tracks the memberbase too: it must not go on chain naming a member who is
	// no longer in the census
	draft, err := testDB.SetQuestion(&VotingProcessQuestion{
		ProcessID:         f.processID,
		OrgAddress:        testOrgAddress,
		Order:             2,
		Type:              VotingTypeSingleChoice,
		EligibleMemberIDs: []string{f.alice.ID.Hex(), f.carol.ID.Hex()},
	})
	c.Assert(err, qt.IsNil)

	emptied, err := testDB.RevokeMembersFromCensuses(
		[]string{f.census.ID.Hex()}, []string{f.alice.ID.Hex()})
	c.Assert(err, qt.IsNil)
	// a draft has no election to resize, so it is never reported
	c.Assert(emptied, qt.HasLen, 0)

	got, err := testDB.Question(draft)
	c.Assert(err, qt.IsNil)
	c.Assert(got.EligibleMemberIDs, qt.DeepEquals, []string{f.carol.ID.Hex()})
}

// TestRevokeMembersFromCensusesMatchesEmptyEligibilityEncodings pins the three shapes "no
// restriction" reaches Mongo as: a missing field, an explicit null, and an empty array. All three
// mean the whole census, so none of them may be reported as newly emptied.
func TestRevokeMembersFromCensusesMatchesEmptyEligibilityEncodings(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	f := setupRevocationFixture(t)

	ctx := t.Context()
	for _, encoding := range []any{nil, bson.A{}} {
		_, err := testDB.processesQuestions.UpdateOne(ctx,
			bson.M{"_id": f.openToAll},
			bson.M{"$set": bson.M{"eligibleMemberIds": encoding}})
		c.Assert(err, qt.IsNil)

		emptied, err := testDB.RevokeMembersFromCensuses(
			[]string{f.census.ID.Hex()}, []string{f.carol.ID.Hex()})
		c.Assert(err, qt.IsNil)
		for _, q := range emptied {
			c.Assert(q.ID, qt.Not(qt.Equals), f.openToAll)
		}
	}

	// a missing field behaves the same
	_, err := testDB.processesQuestions.UpdateOne(ctx,
		bson.M{"_id": f.openToAll},
		bson.M{"$unset": bson.M{"eligibleMemberIds": ""}})
	c.Assert(err, qt.IsNil)
	emptied, err := testDB.RevokeMembersFromCensuses(
		[]string{f.census.ID.Hex()}, []string{f.bob.ID.Hex()})
	c.Assert(err, qt.IsNil)
	for _, q := range emptied {
		c.Assert(q.ID, qt.Not(qt.Equals), f.openToAll)
	}
}
