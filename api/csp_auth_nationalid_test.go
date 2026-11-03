package api

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// nationalIDCensus is the auth-only census of the nationalId tests: auth/0 resolves the participant
// and returns a token in one step, and the token names the member it authenticated.
var nationalIDCensus = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsNationalID}

// authenticatedMember runs auth/0 with the given nationalId and returns the id of the member the
// token was issued to.
func authenticatedMember(t *testing.T, pid, nationalID string) string {
	t.Helper()
	token := postProcessAuth0(t, pid, &handlers.AuthRequest{NationalID: nationalID})
	auth, err := testDB.CSPAuth(token)
	qt.Assert(t, err, qt.IsNil)
	return auth.UserID.String()
}

// insertRawMember stores a member as a database written before nationalId normalization holds it,
// bypassing the write path that would canonicalize it.
func insertRawMember(t *testing.T, orgAddress common.Address, nationalID string) string {
	t.Helper()
	member := &db.OrgMember{
		ID: bson.NewObjectID(), OrgAddress: orgAddress, NationalID: nationalID, Weight: 1, CreatedAt: time.Now(),
	}
	insertLegacyDoc(t, "orgMembers", member)
	return member.ID.Hex()
}

// TestCSPAuthNationalIDNormalization pins that SP1 does not regress live nationalId censuses: any
// variant of a canonicalisable DNI typed at auth/0 matches the canonical stored member, a value
// that is not a DNI keeps matching exactly as before, and a rewritten input costs one unit of the
// failed-attempt budget however many lookups it takes.
func TestCSPAuthNationalIDNormalization(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	members := postOrgMembers(t, token, orgAddress,
		apicommon.OrgMember{MemberNumber: "N-1", NationalID: "12345678Z"},
		apicommon.OrgMember{MemberNumber: "N-2", NationalID: "12345678A"},
	)
	byNumber := make(map[string]string, len(members))
	for _, m := range members {
		byNumber[m.MemberNumber] = m.ID
	}
	canonical, wrongLetter := byNumber["N-1"], byNumber["N-2"]
	pid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: []string{canonical, wrongLetter}}, 1)

	for _, typed := range []string{"12345678Z", "12.345.678-Z", " 12345678 z ", "12345678", "12 345 678"} {
		c.Assert(authenticatedMember(t, pid, typed), qt.Equals, canonical, qt.Commentf("typed %q", typed))
	}

	// a stored wrong letter is passed through on both sides: the same value, in any case, still
	// matches, and a separator variant fails exactly as it did before normalization
	c.Assert(authenticatedMember(t, pid, "12345678A"), qt.Equals, wrongLetter)
	c.Assert(authenticatedMember(t, pid, " 12345678a"), qt.Equals, wrongLetter)
	c.Assert(postProcessAuth0AndExpectError(t, pid, &handlers.AuthRequest{NationalID: "12.345.678-A"}).Code,
		qt.Equals, errors.ErrCSPAuthFailed.Code)

	// a rewritten input matching nobody runs two lookups but spends one unit per attempt: the budget
	// runs out after exactly DefaultMaxHourlyAuthFailures attempts, not half as many
	nobody := &handlers.AuthRequest{NationalID: "87.654.321"}
	for range csp.DefaultMaxHourlyAuthFailures {
		c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code, qt.Equals, errors.ErrCSPAuthFailed.Code)
	}
	c.Assert(postProcessAuth0AndExpectError(t, pid, nobody).Code, qt.Equals, errors.ErrVerificationMaxAttempts.Code)
}

// TestCSPAuthNationalIDCollisionSkipped pins the letterless/lettered twins migration 34 leaves alone
// (12345678 next to 12345678Z in one census): each member keeps authenticating as themselves with
// their own input, and the letterless input never resolves to the lettered twin.
func TestCSPAuthNationalIDCollisionSkipped(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)

	letterless := insertRawMember(t, orgAddress, "12345678")
	lettered := insertRawMember(t, orgAddress, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: []string{letterless, lettered}}, 1)

	c.Assert(authenticatedMember(t, pid, "12345678"), qt.Equals, letterless)
	c.Assert(authenticatedMember(t, pid, " 12345678 "), qt.Equals, letterless)
	c.Assert(authenticatedMember(t, pid, "12345678Z"), qt.Equals, lettered)
	c.Assert(authenticatedMember(t, pid, "12345678z"), qt.Equals, lettered)
	// a separator variant has no exact holder, so it falls back to the canonical member
	c.Assert(authenticatedMember(t, pid, "12.345.678-Z"), qt.Equals, lettered)
}
