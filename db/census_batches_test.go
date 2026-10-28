package db

import (
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestPopulateMembersCensusBatches builds a census larger than the participant write batch size
// (200), so the build exercises the batched write path: several bulk writes, each under its own
// lock acquisition, must together upsert every member exactly once.
func TestPopulateMembersCensusBatches(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	org := &Organization{
		Address:   testOrgAddress,
		CreatedAt: time.Now(),
	}
	c.Assert(testDB.SetOrganization(org), qt.IsNil)

	const total = 450 // > 2 full batches of 200
	memberIDs := make([]string, 0, total)
	for i := range total {
		member := &OrgMember{
			OrgAddress:   testOrgAddress,
			Email:        fmt.Sprintf("batch-member-%d@example.com", i),
			MemberNumber: fmt.Sprintf("batch-%d", i),
			Name:         fmt.Sprintf("Member %d", i),
		}
		id, err := testDB.SetOrgMember(testSalt, member)
		c.Assert(err, qt.IsNil)
		memberIDs = append(memberIDs, id)
	}

	census := &Census{
		OrgAddress: testOrgAddress,
		TwoFaFields: OrgMemberTwoFaFields{
			OrgMemberTwoFaFieldEmail,
		},
	}
	inserted, missing, err := testDB.PopulateMembersCensus(census, memberIDs)
	c.Assert(err, qt.IsNil)
	c.Assert(missing, qt.HasLen, 0)
	c.Assert(inserted, qt.Equals, int64(total))

	count, err := testDB.CountCensusParticipants(census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(count, qt.Equals, int64(total))

	stored, err := testDB.Census(census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Size, qt.Equals, int64(total))

	// repopulating the same members must not double-count: they are upserts, not inserts
	inserted, missing, err = testDB.PopulateMembersCensus(stored, memberIDs)
	c.Assert(err, qt.IsNil)
	c.Assert(missing, qt.HasLen, 0)
	c.Assert(inserted, qt.Equals, int64(0))
	count, err = testDB.CountCensusParticipants(census.ID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(count, qt.Equals, int64(total))
}
