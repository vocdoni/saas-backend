package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

// TestCheckProcessBundleParticipants exercises
// POST /process/bundle/{bundleId}/participants/check.
//
// Setup:
//   - Admin user creates an org and a census.
//   - Three org members are added:
//     Alice  — email "shared@x.com", phone "+34611111111", memberNumber "P001",
//     nationalId "DNI001"
//     Bob    — email "bob@x.com",    phone "+34622222222", memberNumber "P002"
//     Carol  — email "shared@x.com" (duplicate with Alice), phone "+34633333333"
//   - Alice and Bob are added to the census (and so become bundle participants);
//     Carol is intentionally NOT added.
//   - A bundle is created directly via testDB to avoid the full vocdoni setup
//     path; the handler only needs the bundle row + the census it references.
//   - A verified CSP auth token is seeded for Alice to assert hasVoted=true.
//
// The endpoint must return ONLY members who are participants of the bundle's
// census. So a lookup by "shared@x.com" returns Alice but not Carol (Carol
// matches the org-member query but is not in the census).
func TestCheckProcessBundleParticipants(t *testing.T) {
	c := qt.New(t)

	adminToken := testCreateUser(t, "checkbundlepass123")
	orgAddress := testCreateOrganization(t, adminToken)

	// Keep the same census setup used in other stable API tests.
	censusID := postCensus(t, adminToken, orgAddress,
		db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail})

	// Build three members; Carol shares Alice's email to test multi-match.
	memberA := apicommon.OrgMember{
		MemberNumber: "P001",
		Name:         "Alice",
		Surname:      "Alpha",
		Email:        "shared@x.com",
		Phone:        "+34611111111",
		NationalID:   "DNI001",
		Password:     "pwA",
	}
	memberB := apicommon.OrgMember{
		MemberNumber: "P002",
		Name:         "Bob",
		Surname:      "Beta",
		Email:        "bob@x.com",
		Phone:        "+34622222222",
		NationalID:   "DNI002",
		Password:     "pwB",
	}
	memberC := apicommon.OrgMember{
		MemberNumber: "P003",
		Name:         "Carol",
		Surname:      "Gamma",
		Email:        "shared@x.com", // duplicate of Alice
		Phone:        "+34633333333",
		NationalID:   "DNI003",
		Password:     "pwC",
	}

	posted := postOrgMembers(t, adminToken, orgAddress, memberA, memberB, memberC)
	c.Assert(posted, qt.HasLen, 3)

	// Alice and Carol share an email; identify them by member number instead.
	idByNumber := map[string]string{}
	for _, m := range posted {
		idByNumber[m.MemberNumber] = m.ID
	}
	aliceID := idByNumber["P001"]
	bobID := idByNumber["P002"]
	carolID := idByNumber["P003"]
	c.Assert(aliceID, qt.Not(qt.Equals), "")
	c.Assert(bobID, qt.Not(qt.Equals), "")
	c.Assert(carolID, qt.Not(qt.Equals), "")

	// Add Alice and Bob to the census. Carol stays out.
	postCensusParticipants(t, adminToken, censusID, aliceID, bobID)

	// Create the bundle directly via testDB — the handler under test only needs
	// the bundle row plus its referenced census.
	census, err := testDB.Census(censusID)
	c.Assert(err, qt.IsNil)
	bundleObjID := testDB.NewBundleID()
	_, err = testDB.SetProcessBundle(&db.ProcessesBundle{
		ID:         bundleObjID,
		OrgAddress: orgAddress,
		Census:     *census,
	})
	c.Assert(err, qt.IsNil)
	bundleID := bundleObjID.Hex()

	// processID whose voting status the endpoint reports. It does not need to be
	// a real on-chain process for these tests; the handler only uses it to look
	// up per-member CSP process status.
	processID := internal.HexBytes(util.RandomBytes(32))

	// Helper for the canonical successful call path.
	checkPath := []string{"process", "bundle", bundleID, "participants", "check"}

	t.Run("lookup by email returns only members in the bundle", func(_ *testing.T) {
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com", ProcessID: processID},
			checkPath...,
		)
		// Alice matches AND is in the bundle. Carol matches by email but is not.
		c.Assert(resp.Participants, qt.HasLen, 1)
		c.Assert(resp.Participants[0].MemberID, qt.Equals, aliceID)
		c.Assert(resp.Participants[0].Email, qt.Equals, "shared@x.com")
		c.Assert(resp.Participants[0].MemberNumber, qt.Equals, "P001")
		c.Assert(resp.Participants[0].HasVoted, qt.IsFalse)
	})

	t.Run("lookup by memberNumber returns the matching participant", func(_ *testing.T) {
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "memberNumber", Value: "P002", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 1)
		c.Assert(resp.Participants[0].MemberID, qt.Equals, bobID)
		c.Assert(resp.Participants[0].MemberNumber, qt.Equals, "P002")
	})

	t.Run("lookup by nationalId returns the matching participant", func(_ *testing.T) {
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "nationalId", Value: "DNI001", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 1)
		c.Assert(resp.Participants[0].MemberID, qt.Equals, aliceID)
	})

	t.Run("lookup by phone hashes plaintext server-side", func(_ *testing.T) {
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "phone", Value: "+34611111111", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 1)
		c.Assert(resp.Participants[0].MemberID, qt.Equals, aliceID)
	})

	t.Run("lookup with no match returns empty array", func(_ *testing.T) {
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "missing@x.com", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 0)
	})

	t.Run("member exists but is not in the bundle returns empty", func(_ *testing.T) {
		// Carol matches the org-member query (her email is "shared@x.com" and
		// her memberNumber is unique) but she was not added to the census.
		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "memberNumber", Value: "P003", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 0)
	})

	t.Run("invalid fieldName returns 400", func(_ *testing.T) {
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "invalid-field", Value: "Alice"},
			checkPath...,
		)
	})

	t.Run("empty value returns 400", func(_ *testing.T) {
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: ""},
			checkPath...,
		)
	})

	t.Run("malformed body returns 400", func(_ *testing.T) {
		// Send a string instead of an object.
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken,
			"not-a-json-object", checkPath...,
		)
	})

	t.Run("invalid bundleId returns 400", func(_ *testing.T) {
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com"},
			"process", "bundle", "not-hex", "participants", "check",
		)
	})

	t.Run("unknown bundle returns 404", func(_ *testing.T) {
		// Valid hex shape but not a stored bundle ID.
		missing := internal.HexBytes(util.RandomBytes(12)).String()
		requestAndAssertCode(http.StatusNotFound, t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com", ProcessID: processID},
			"process", "bundle", missing, "participants", "check",
		)
	})

	t.Run("missing auth token returns 401", func(_ *testing.T) {
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com", ProcessID: processID},
			checkPath...,
		)
	})

	t.Run("authenticated caller without manager role returns 403", func(_ *testing.T) {
		otherUserToken := testCreateUser(t, "checkbundlepass456")
		requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, otherUserToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com", ProcessID: processID},
			checkPath...,
		)
	})

	t.Run("missing processID returns 400", func(_ *testing.T) {
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "email", Value: "shared@x.com"},
			checkPath...,
		)
	})

	t.Run("hasVoted is true when the member has used the process", func(_ *testing.T) {
		// Seed a verified CSP auth token for Alice in this bundle and consume the
		// process with it, so Alice has a used CSP process for processID.
		bundleIDBytes := internal.HexBytes{}
		c.Assert(bundleIDBytes.ParseString(bundleID), qt.IsNil)
		userID := internal.HexBytesFromString(aliceID)
		token := internal.HexBytes(util.RandomBytes(32))
		address := internal.HexBytes(util.RandomBytes(20))

		c.Assert(testDB.SetCSPAuth(token, userID, bundleIDBytes, ""), qt.IsNil)
		c.Assert(testDB.VerifyCSPAuth(token), qt.IsNil)
		c.Assert(testDB.ConsumeCSPProcess(token, processID, address), qt.IsNil)

		resp := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "memberNumber", Value: "P001", ProcessID: processID},
			checkPath...,
		)
		c.Assert(resp.Participants, qt.HasLen, 1)
		c.Assert(resp.Participants[0].MemberID, qt.Equals, aliceID)
		c.Assert(resp.Participants[0].HasVoted, qt.IsTrue)

		// Bob has not consumed the process → hasVoted=false.
		respBob := requestAndParse[apicommon.CheckBundleParticipantsResponse](
			t, http.MethodPost, adminToken,
			apicommon.CheckBundleParticipantsRequest{FieldName: "memberNumber", Value: "P002", ProcessID: processID},
			checkPath...,
		)
		c.Assert(respBob.Participants, qt.HasLen, 1)
		c.Assert(respBob.Participants[0].HasVoted, qt.IsFalse)
	})
}
