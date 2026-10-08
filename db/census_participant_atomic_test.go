package db

import (
	"context"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestUpsertOrgMemberCensusUpdateIsAllOrNothing covers the regression where a member edit
// updated its census participants one census at a time, validating as it went: a duplicate
// conflict in a later census aborted the edit but left the participants of the earlier
// censuses already rewritten, out of sync with the stored member.
func TestUpsertOrgMemberCensusUpdateIsAllOrNothing(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)

	c.Assert(testDB.SetOrganization(testOrg), qt.IsNil)

	newCensus := func() *Census {
		return &Census{
			OrgAddress:  testOrgAddress,
			Type:        CensusTypeMail,
			AuthFields:  OrgMemberAuthFields{OrgMemberAuthFieldsMemberNumber, OrgMemberAuthFieldsName},
			TwoFaFields: OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail},
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}
	}
	censusA := newCensus()
	censusAID, err := testDB.SetCensus(censusA)
	c.Assert(err, qt.IsNil)
	censusB := newCensus()
	_, err = testDB.SetCensus(censusB)
	c.Assert(err, qt.IsNil)

	m0 := &OrgMember{
		ID:           bson.NewObjectID(),
		OrgAddress:   testOrgAddress,
		MemberNumber: "atomic-0",
		Name:         "Atomic Zero",
		Email:        "atomic0@example.com",
		CreatedAt:    time.Now(),
	}
	m1 := &OrgMember{
		ID:           bson.NewObjectID(),
		OrgAddress:   testOrgAddress,
		MemberNumber: "atomic-1",
		Name:         "Atomic One",
		Email:        "atomic1@example.com",
		CreatedAt:    time.Now(),
	}
	for _, m := range []*OrgMember{m0, m1} {
		_, err := testDB.SetOrgMember(testSalt, m)
		c.Assert(err, qt.IsNil)
	}

	// m0 participates in censuses A and B (A inserted first, so it is processed first);
	// m1 participates only in B, so updating m0 to m1's identity conflicts only in B
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stored0, err := testDB.OrgMember(testOrgAddress, m0.ID.Hex())
	c.Assert(err, qt.IsNil)
	stored1, err := testDB.OrgMember(testOrgAddress, m1.ID.Hex())
	c.Assert(err, qt.IsNil)
	n, err := testDB.setBulkCensusParticipant(ctx, censusA,
		[]censusMember{{id: stored0.ID, hashes: calculateParticipantHashes(*censusA, *stored0)}})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(1))
	n, err = testDB.setBulkCensusParticipant(ctx, censusB, []censusMember{
		{id: stored0.ID, hashes: calculateParticipantHashes(*censusB, *stored0)},
		{id: stored1.ID, hashes: calculateParticipantHashes(*censusB, *stored1)},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	before, err := testDB.CensusParticipants(censusAID)
	c.Assert(err, qt.IsNil)
	c.Assert(before, qt.HasLen, 1)

	// updating m0 to m1's identity conflicts in census B and must fail...
	_, _, err = testDB.UpsertOrgMemberAndCensusParticipants(testOrg, &OrgMemberUpdate{
		ID:           stored0.ID,
		MemberNumber: new(m1.MemberNumber),
		Name:         new(m1.Name),
		Email:        new(m1.Email),
	}, testSalt)
	c.Assert(err, qt.ErrorMatches, ".*update would create duplicates.*")

	// ...without having touched the census A participant processed before the conflict
	after, err := testDB.CensusParticipants(censusAID)
	c.Assert(err, qt.IsNil)
	c.Assert(after, qt.HasLen, 1)
	c.Assert(after[0].LoginHash, qt.DeepEquals, before[0].LoginHash)
	c.Assert(after[0].UpdatedAt, qt.DeepEquals, before[0].UpdatedAt)

	// the member itself was not rewritten either
	unchanged, err := testDB.OrgMember(testOrgAddress, m0.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(unchanged.Email, qt.Equals, m0.Email)
}
