package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestProcessesCensusTotalWeight verifies the whole-census totalWeight surfaced on
// GET /processes/{id} census spec: for a weighted census it is the sum of the members' weights (not
// the participant count), and for a non-weighted census it equals the size.
func TestProcessesCensusTotalWeight(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "censusweightpass123")
	orgAddress := testCreateOrganization(t, token)

	// seed org members with distinct weights
	weights := []uint64{3, 5, 2}
	memberIDs := make([]string, 0, len(weights))
	for i, w := range weights {
		id, err := testDB.SetOrgMember("test_salt", &db.OrgMember{
			OrgAddress:   orgAddress,
			MemberNumber: fmt.Sprintf("W%d", i),
			Weight:       w,
		})
		c.Assert(err, qt.IsNil)
		memberIDs = append(memberIDs, id)
	}
	var wantTotal int64
	for _, w := range weights {
		wantTotal += int64(w)
	}

	// helper: build a published process over a census of the seeded members and read its census spec.
	censusSpecOf := func(weighted bool) apicommon.CensusSpec {
		censusID, err := testDB.SetCensus(&db.Census{
			OrgAddress:  orgAddress,
			Weighted:    weighted,
			AuthFields:  db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
			TwoFaFields: db.OrgMemberTwoFaFields{},
		})
		c.Assert(err, qt.IsNil)
		added, _, err := testDB.AddCensusParticipantsByMemberIDs(censusID, memberIDs)
		c.Assert(err, qt.IsNil)
		c.Assert(added, qt.Equals, len(memberIDs))

		censusOID, err := primitive.ObjectIDFromHex(censusID)
		c.Assert(err, qt.IsNil)
		vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
			OrgAddress: orgAddress, Published: true, CensusID: censusOID,
			Title: db.MultiLangString{"default": "Weight process"},
		})
		c.Assert(err, qt.IsNil)

		resp := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", vpID.Hex())
		c.Assert(resp.Census.Size, qt.Equals, int64(len(memberIDs)))
		return resp.Census
	}

	// weighted: totalWeight is the sum of the members' weights, distinct from the size.
	weighted := censusSpecOf(true)
	c.Assert(weighted.Weighted, qt.IsTrue)
	c.Assert(weighted.TotalWeight, qt.Equals, wantTotal)
	c.Assert(weighted.TotalWeight, qt.Not(qt.Equals), weighted.Size)

	// non-weighted: totalWeight equals the size (every member counts as 1).
	plain := censusSpecOf(false)
	c.Assert(plain.Weighted, qt.IsFalse)
	c.Assert(plain.TotalWeight, qt.Equals, plain.Size)
}
