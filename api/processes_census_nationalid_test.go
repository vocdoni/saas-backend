package api

import (
	"encoding/json"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestValidateProcessCensusNationalIDWarnings pins the nationalId warnings of POST
// /processes/census/validation: reported per class in the 200 body and in the 400 data alike, only
// when nationalId is an auth field, and never blocking on their own.
func TestValidateProcessCensusNationalIDWarnings(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	all := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "V-1", NationalID: "12345678"},
		apicommon.OrgMember{MemberNumber: "V-2", NationalID: "X1234567A"},
		apicommon.OrgMember{MemberNumber: "V-3", NationalID: "PAB123456"},
		apicommon.OrgMember{MemberNumber: "V-4", NationalID: "K1234567L"},
		apicommon.OrgMember{MemberNumber: "V-5", NationalID: "02345678T"},
		apicommon.OrgMember{MemberNumber: "V-6", NationalID: "2.345.678-t"},
		apicommon.OrgMember{MemberNumber: "V-7"},
	)
	id := make(map[string]string, len(all))
	for _, m := range all {
		id[m.MemberNumber] = m.ID
	}
	// letterless/lettered twins as migration 34 leaves them after a collision: distinct hashes
	skipped := insertRawMember(t, orgAddress, "87654321")
	twin := insertRawMember(t, orgAddress, "87654321X")

	byNationalID := apicommon.CensusSpec{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}}
	// validate asserts the status code and returns the warnings, plus the blocking duplicates of a 400
	validate := func(code int, spec apicommon.CensusSpec, memberIDs ...string) db.OrgMemberAggregationResults {
		t.Helper()
		spec.MemberIDs = memberIDs
		req := &apicommon.ValidateProcessCensusRequest{OrgAddress: orgAddress.Bytes(), Census: spec}
		if code == http.StatusOK {
			resp := requestAndParse[apicommon.ValidateProcessCensusResponse](t, http.MethodPost, token, req,
				"processes", "census", "validation")
			c.Assert(resp.NationalIDWarnings, qt.IsNotNil) // an empty array, never null
			return db.OrgMemberAggregationResults{NationalIDWarnings: resp.NationalIDWarnings}
		}
		apiErr := requestAndParseWithAssertCode[errors.Error](code, t, http.MethodPost, token, req,
			"processes", "census", "validation")
		data, err := json.Marshal(apiErr.Data)
		c.Assert(err, qt.IsNil)
		var results db.OrgMemberAggregationResults
		c.Assert(json.Unmarshal(data, &results), qt.IsNil)
		return results
	}

	c.Run("each class is reported and the census stays valid", func(c *qt.C) {
		got := validate(http.StatusOK, byNationalID, id["V-1"], id["V-2"], id["V-3"], id["V-4"])
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-1"], Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
			{MemberID: id["V-2"], Class: "letter_mismatch"},
			{MemberID: id["V-3"], Class: "invalid"},
			{MemberID: id["V-4"], Class: "unsupported_document"},
		})
	})

	c.Run("no warnings when nationalId is not an auth field", func(c *qt.C) {
		byNumber := apicommon.CensusSpec{AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}}
		c.Assert(validate(http.StatusOK, byNumber, id["V-1"], id["V-2"], id["V-3"]).NationalIDWarnings, qt.HasLen, 0)
	})

	c.Run("a canonical pair is both blocking duplicates and warnings", func(c *qt.C) {
		got := validate(http.StatusBadRequest, byNationalID, id["V-5"], id["V-6"])
		c.Assert(got.Duplicates, qt.HasLen, 2)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-5"], Class: db.NationalIDWarningDuplicate},
			{MemberID: id["V-6"], Class: db.NationalIDWarningDuplicate},
		})
	})

	c.Run("a collision-skipped pair is only a warning", func(c *qt.C) {
		got := validate(http.StatusOK, byNationalID, skipped, twin)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: skipped, Class: "derived_letter", Original: "87654321", Derived: "87654321X"},
			{MemberID: skipped, Class: db.NationalIDWarningDuplicate},
			{MemberID: twin, Class: db.NationalIDWarningDuplicate},
		})
	})

	c.Run("missing data still blocks and is reported as missing", func(c *qt.C) {
		got := validate(http.StatusBadRequest, byNationalID, id["V-7"], id["V-1"])
		c.Assert(got.MissingData, qt.HasLen, 1)
		c.Assert(got.NationalIDWarnings, qt.ContentEquals, []db.NationalIDWarning{
			{MemberID: id["V-7"], Class: "missing"},
			{MemberID: id["V-1"], Class: "derived_letter", Original: "12345678", Derived: "12345678Z"},
		})
	})
}
