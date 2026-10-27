package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestAuthFirstStepUnknownParticipantPasswordWork pins that, on a census using the password, a login
// whose participant or member is not found runs one password check like a wrong password does, so
// the response time does not tell a member apart from a stranger. A census without the password runs
// none. It counts the checks rather than timing them.
func TestAuthFirstStepUnknownParticipantPasswordWork(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()

	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	dbName := test.RandomDatabaseName()
	mainDB, err := db.New(uri, dbName)
	c.Assert(err, qt.IsNil)
	c.Cleanup(mainDB.Close)

	org := &db.Organization{Address: common.Address{0x42}, CreatedAt: time.Now()}
	c.Assert(mainDB.SetOrganization(org), qt.IsNil)
	const salt = "salt"
	clientHash, wrongHash := strings.Repeat("ab", 32), strings.Repeat("cd", 32)
	memberID, err := mainDB.SetOrgMember(salt, &db.OrgMember{
		OrgAddress: org.Address, MemberNumber: "M-1", Password: clientHash, Weight: 1,
	})
	c.Assert(err, qt.IsNil)

	newCensus := func(fields db.OrgMemberAuthFields) string {
		census := &db.Census{OrgAddress: org.Address, AuthFields: fields}
		_, _, err := mainDB.PopulateMembersCensus(census, []string{memberID})
		c.Assert(err, qt.IsNil)
		return census.ID.Hex()
	}
	withPassword := newCensus(db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsPassword})
	withoutPassword := newCensus(db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber})

	h := New(&csp.CSP{PasswordSalt: salt, Storage: mainDB}, mainDB)
	checks := 0
	h.hashPassword = func(ctx context.Context, salt string, memberID bson.ObjectID, clientHash string) ([]byte, error) {
		checks++
		return db.HashMemberPasswordContext(ctx, salt, memberID, clientHash)
	}
	// auth0 runs auth step 0 and returns its API error code and the password checks it ran
	auth0 := func(censusID, memberNumber, password string) (int, int) {
		checks = 0
		body, err := json.Marshal(&AuthRequest{MemberNumber: memberNumber, Password: password})
		c.Assert(err, qt.IsNil)
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		_, err = h.authFirstStep(r, internal.HexBytes{0x01}, censusID)
		apiErr, ok := err.(errors.Error)
		c.Assert(ok, qt.IsTrue, qt.Commentf("error: %v", err))
		return apiErr.Code, checks
	}
	notFound := errors.ErrCSPAuthFailed.Code

	type answer struct{ code, checks int }
	for _, tc := range []struct {
		name     string
		censusID string
		number   string
		password string
		want     answer
	}{
		{"wrong password", withPassword, "M-1", wrongHash, answer{notFound, 1}},
		{"unknown participant", withPassword, "M-9", clientHash, answer{notFound, 1}},
		{"unknown participant without the password", withoutPassword, "M-9", "", answer{notFound, 0}},
	} {
		code, checks := auth0(tc.censusID, tc.number, tc.password)
		c.Assert(answer{code, checks}, qt.Equals, tc.want, qt.Commentf("%s", tc.name))
	}

	// a participant whose member is gone takes the "failed to get org member" path
	oid, err := bson.ObjectIDFromHex(memberID)
	c.Assert(err, qt.IsNil)
	_, err = mainDB.DBClient.Database(dbName).Collection("orgMembers").DeleteOne(ctx, bson.M{"_id": oid})
	c.Assert(err, qt.IsNil)
	code, checks := auth0(withPassword, "M-1", clientHash)
	c.Assert(answer{code, checks}, qt.Equals, answer{notFound, 1})
	code, checks = auth0(withoutPassword, "M-1", "")
	c.Assert(answer{code, checks}, qt.Equals, answer{notFound, 0})
}
