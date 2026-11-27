package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestProcessCSPAuthErrorCodes checks that step 1 and the resend answer each challenge failure
// with its own code: a wrong code, a code locked by failed attempts, an expired one and one
// solved through another token.
func TestProcessCSPAuthErrorCodes(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(3)...)
	c.Assert(members, qt.HasLen, 3)
	pid, _ := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		},
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
	}, 1)

	// stepZero asks a code for member i and returns the token and the code the member was sent
	stepZero := func(i int) (internal.HexBytes, string) {
		tok := postProcessAuth0(t, pid, &handlers.AuthRequest{
			Name:         members[i].Name,
			Surname:      members[i].Surname,
			MemberNumber: members[i].MemberNumber,
			Email:        members[i].Email,
		})
		code := extractOTPFromBody(waitForEmail(t, members[i].Email))
		c.Assert(code, qt.Not(qt.Equals), "")
		return tok, code
	}
	assertStepOne := func(want errors.Error, tok internal.HexBytes, code string) {
		requestAndAssertError(want, t, http.MethodPost, "",
			&handlers.AuthChallengeRequest{AuthToken: tok, AuthData: []string{code}},
			"processes", pid, "auth", "1")
	}
	wrongCode := func(code string) string {
		if code == "000000" {
			return "000001"
		}
		return "000000"
	}

	c.Run("wrong code, then locked", func(*qt.C) {
		tok, code := stepZero(0)
		for range csp.MaxChallengeAttempts {
			assertStepOne(errors.ErrChallengeCodeMismatch, tok, wrongCode(code))
		}
		// even the right code is refused once the attempts are spent
		assertStepOne(errors.ErrChallengeLocked, tok, code)
	})

	c.Run("solved through another token", func(c *qt.C) {
		first, code := stepZero(1)
		// a repeated step 0 inside the cooldown issues another token for the same code
		second := postProcessAuth0(t, pid, &handlers.AuthRequest{
			Name:         members[1].Name,
			Surname:      members[1].Surname,
			MemberNumber: members[1].MemberNumber,
			Email:        members[1].Email,
		})
		c.Assert(second, qt.Not(qt.DeepEquals), first)
		postProcessAuth1(t, pid, &handlers.AuthChallengeRequest{AuthToken: second, AuthData: []string{code}})
		// as expired, so another token of the code does not tell that the voter authenticated
		assertStepOne(errors.ErrChallengeExpired, first, code)
		// the first token's code cannot be re-sent either
		requestAndAssertError(errors.ErrChallengeExpired, t, http.MethodPost, "",
			&handlers.AuthResendRequest{AuthToken: first, Email: members[1].Email},
			"processes", pid, "auth", "resend")
		// the verified token's resend answers 40015 with the code's remaining lifetime in
		// milliseconds, more than the cooldown in seconds could ever read as
		apiErr := requestAndExpectError(t, http.MethodPost, "",
			&handlers.AuthResendRequest{AuthToken: second, Email: members[1].Email},
			"processes", pid, "auth", "resend")
		c.Assert(apiErr.Code, qt.Equals, errors.ErrUserAlreadyVerified.Code)
		data, ok := apiErr.Data.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		left, ok := data["coolDownTime"].(float64)
		c.Assert(ok, qt.IsTrue)
		c.Assert(left > float64(time.Minute.Milliseconds()), qt.IsTrue, qt.Commentf("coolDownTime %v", left))
	})

	c.Run("expired code", func(c *qt.C) {
		tok, code := stepZero(2)
		_, err := testDB.DBClient.Database(testDBName).Collection("cspTokens").UpdateOne(context.Background(),
			bson.M{"tokens.id": tok},
			bson.M{"$set": bson.M{"createdat": time.Now().Add(-24 * time.Hour)}})
		c.Assert(err, qt.IsNil)
		assertStepOne(errors.ErrChallengeExpired, tok, code)

		// once the TTL index deletes the code, its token answers the same, on step 1 and resend
		_, err = testDB.DBClient.Database(testDBName).Collection("cspTokens").DeleteOne(context.Background(),
			bson.M{"tokens.id": tok})
		c.Assert(err, qt.IsNil)
		assertStepOne(errors.ErrChallengeExpired, tok, code)
		requestAndAssertError(errors.ErrChallengeExpired, t, http.MethodPost, "",
			&handlers.AuthResendRequest{AuthToken: tok, Email: members[2].Email},
			"processes", pid, "auth", "resend")
	})
}
