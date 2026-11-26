package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// signRefusalBody is the wire form of a refused sign request.
type signRefusalBody struct {
	Error string                   `json:"error"`
	Code  int                      `json:"code"`
	Data  handlers.SignRefusalData `json:"data"`
}

// refusalVoter is a voter authenticated against a 3-question process: elections[0] is paused,
// elections[1] votable and elections[2] closed to the voter by its eligibility list.
type refusalVoter struct {
	pid        string
	token      string
	orgAddress common.Address
	member     apicommon.OrgMember
	authToken  internal.HexBytes
	elections  []internal.HexBytes
	voter      *ethereum.SignKeys
}

// setupRefusalVoter publishes a 3-question process, authenticates its only member and sets the
// questions up as described on refusalVoter.
func setupRefusalVoter(t *testing.T, anonymous, weighted bool) *refusalVoter {
	t.Helper()
	c := qt.New(t)
	token := testCreateUser(t, "superpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	suffix := internal.RandomInt(1000000)
	members := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{
		Name:         "Refusal",
		Surname:      "Voter",
		MemberNumber: fmt.Sprintf("F%06d", suffix),
		NationalID:   fmt.Sprintf("REFU%05dA", suffix),
		BirthDate:    "1990-01-01",
		Email:        fmt.Sprintf("refusal.voter.%d@example.com", suffix),
		Phone:        "+34699000003",
		Weight:       "1",
	})
	pid, elections := publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		AuthFields: db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsSurname,
			db.OrgMemberAuthFieldsMemberNumber,
		},
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
		Anonymous:   anonymous,
		Weighted:    weighted,
	}, 3)
	authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
		Name:         members[0].Name,
		Surname:      members[0].Surname,
		MemberNumber: members[0].MemberNumber,
		Email:        members[0].Email,
	})
	voter := &ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	v := &refusalVoter{
		pid: pid, token: token, orgAddress: orgAddress, member: members[0],
		authToken: authToken, elections: elections, voter: voter,
	}
	qids := checkVotable(t, pid, authToken)
	for i, election := range elections {
		qid := objectID(c, qids[election.String()].QuestionID)
		status := db.QuestionStatusReady
		if i == 0 {
			status = db.QuestionStatusPaused
		}
		c.Assert(testDB.SetQuestionStatus(qid, status), qt.IsNil)
		if i == 2 {
			ok, err := testDB.SetQuestionEligibleMemberIDs(qid, nil, []string{bson.NewObjectID().Hex()})
			c.Assert(err, qt.IsNil)
			c.Assert(ok, qt.IsTrue)
		}
	}
	return v
}

// ballots returns the sign-batch ballots for the given election indexes.
func (v *refusalVoter) ballots(idx ...int) []handlers.SignBatchBallot {
	out := make([]handlers.SignBatchBallot, len(idx))
	for i, n := range idx {
		out[i] = handlers.SignBatchBallot{UpstreamID: v.elections[n], Address: v.voter.Address().Bytes()}
	}
	return out
}

// blindBallots returns the blind-sign ballots for the given election indexes.
func (v *refusalVoter) blindBallots(idx ...int) []handlers.BlindSignBallot {
	out := make([]handlers.BlindSignBallot, len(idx))
	for i, n := range idx {
		out[i] = handlers.BlindSignBallot{UpstreamID: v.elections[n], BlindedMessage: internal.HexBytes{0x01}}
	}
	return out
}

// assertBallotRefusals asserts the refused ballots, in request order.
func assertBallotRefusals(c *qt.C, got []handlers.SignRefusal, want ...handlers.SignRefusal) {
	c.Helper()
	c.Assert(got, qt.DeepEquals, want)
}

// postRefused posts a sign request that must be refused with the given status and returns its body.
func postRefused(t *testing.T, status int, body any, pid, endpoint string) signRefusalBody {
	t.Helper()
	return requestAndParseWithAssertCode[signRefusalBody](status, t, http.MethodPost, "", body, "processes", pid, endpoint)
}

// assertTwoRefusals checks the 401 for the paused (index 0) and not eligible (index 2) ballots.
func assertTwoRefusals(c *qt.C, v *refusalVoter, got signRefusalBody) {
	c.Helper()
	c.Assert(got.Code, qt.Equals, errors.ErrUnauthorized.Code)
	c.Assert(got.Error, qt.Contains, "question is not open for voting")
	c.Assert(got.Error, qt.Contains, "at index 0")
	c.Assert(got.Data.Reason, qt.Equals, handlers.VoteReason(""))
	assertBallotRefusals(c, got.Data.Ballots,
		handlers.SignRefusal{Index: 0, UpstreamID: v.elections[0], Reason: handlers.VoteReasonPaused},
		handlers.SignRefusal{Index: 2, UpstreamID: v.elections[2], Reason: handlers.VoteReasonNotEligible},
	)
}

// assertNoBlindRow asserts the CSP holds no row for the voter and election, i.e. nothing was armed or consumed.
func assertNoBlindRow(c *qt.C, tok, election internal.HexBytes) {
	c.Helper()
	auth, err := testDB.CSPAuth(tok)
	c.Assert(err, qt.IsNil)
	_, err = testDB.CSPProcessByUserAndProcess(auth.UserID, election)
	c.Assert(err, qt.ErrorIs, db.ErrTokenNotFound)
}

// TestProcessSignRefusalReasons checks that a refused sign request says why, per ballot or per
// request, while keeping its status and code, and that a refusal consumes nothing.
func TestProcessSignRefusalReasons(t *testing.T) {
	t.Run("sign-batch lists every refused ballot", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, false)
		got := postRefused(t, http.StatusUnauthorized,
			&handlers.SignBatchRequest{AuthToken: v.authToken, Ballots: v.ballots(0, 1, 2)}, v.pid, "sign-batch")
		assertTwoRefusals(c, v, got)
		consumed := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
			&handlers.ConsumedAddressRequest{AuthToken: v.authToken}, "processes", v.pid, "sign-info").Consumed
		c.Assert(consumed, qt.HasLen, 0)

		signed := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "",
			&handlers.SignBatchRequest{AuthToken: v.authToken, Ballots: v.ballots(1)}, "processes", v.pid, "sign-batch")
		c.Assert(signed.Signatures, qt.HasLen, 1)
		c.Assert(signed.Signatures[0].Code, qt.Equals, "")
		c.Assert(signed.Signatures[0].Signature, qt.Not(qt.HasLen), 0)
	})

	t.Run("blind-point lists every refused election", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, true, false)
		got := postRefused(t, http.StatusUnauthorized,
			&handlers.BlindPointRequest{AuthToken: v.authToken, Elections: v.elections}, v.pid, "blind-point")
		assertTwoRefusals(c, v, got)
		assertNoBlindRow(c, v.authToken, v.elections[1])

		point := requestBlindPoint(t, v.pid, v.authToken, v.elections[1])
		c.Assert(point.Code, qt.Equals, "")
	})

	t.Run("blind-sign lists every refused ballot", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, true, false)
		got := postRefused(t, http.StatusUnauthorized,
			&handlers.BlindSignRequest{AuthToken: v.authToken, Ballots: v.blindBallots(0, 1, 2)}, v.pid, "blind-sign")
		assertTwoRefusals(c, v, got)
		assertNoBlindRow(c, v.authToken, v.elections[1])

		proof := testBlindSignBallot(t, v.pid, v.authToken, v.elections[1], v.voter.Address().Bytes())
		c.Assert(proof.GetCa(), qt.Not(qt.IsNil))
	})

	t.Run("single sign names the refused ballot at index 0", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, false)
		got := postRefused(t, http.StatusUnauthorized, &handlers.SignRequest{
			AuthToken: v.authToken,
			ProcessID: v.elections[0],
			Payload:   hex.EncodeToString(v.voter.Address().Bytes()),
		}, v.pid, "sign")
		c.Assert(got.Code, qt.Equals, errors.ErrUnauthorized.Code)
		assertBallotRefusals(c, got.Data.Ballots,
			handlers.SignRefusal{Index: 0, UpstreamID: v.elections[0], Reason: handlers.VoteReasonPaused})
	})

	t.Run("elections of another process or unknown", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, false)
		other := setupRelayVoting(t, 1).processIDs[0]
		random := internal.HexBytes(internal.RandomBytes(32))
		got := postRefused(t, http.StatusUnauthorized, &handlers.SignBatchRequest{
			AuthToken: v.authToken,
			Ballots: []handlers.SignBatchBallot{
				{UpstreamID: other, Address: v.voter.Address().Bytes()},
				{UpstreamID: random, Address: v.voter.Address().Bytes()},
			},
		}, v.pid, "sign-batch")
		c.Assert(got.Code, qt.Equals, errors.ErrUnauthorized.Code)
		assertBallotRefusals(c, got.Data.Ballots,
			handlers.SignRefusal{Index: 0, UpstreamID: other, Reason: handlers.VoteReasonUnknownElection},
			handlers.SignRefusal{Index: 1, UpstreamID: random, Reason: handlers.VoteReasonUnknownElection},
		)
	})

	t.Run("member removed from the census", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, false)
		auth, err := testDB.CSPAuth(v.authToken)
		c.Assert(err, qt.IsNil)
		vp, err := testDB.VotingProcess(objectID(c, v.pid))
		c.Assert(err, qt.IsNil)
		// only the participant row: a revocation would also drop the auth session
		c.Assert(testDB.DelCensusParticipant(vp.CensusID.Hex(), auth.UserID.String()), qt.IsNil)
		got := postRefused(t, http.StatusUnauthorized,
			&handlers.SignBatchRequest{AuthToken: v.authToken, Ballots: v.ballots(1)}, v.pid, "sign-batch")
		c.Assert(got.Code, qt.Equals, errors.ErrUnauthorized.Code)
		c.Assert(got.Data, qt.DeepEquals, handlers.SignRefusalData{Reason: handlers.VoteReasonNotInCensus})
	})

	t.Run("zero weight on a weighted census", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, true)
		// only the API upsert stores a weight of 0; testDB.SetOrgMember drops zero fields
		requestAndParse[apicommon.OrgMember](t, http.MethodPut, v.token,
			apicommon.UpsertOrgMemberRequest{ID: v.member.ID, Weight: new("0")},
			organizationMembersURL(v.orgAddress.String()))
		got := postRefused(t, http.StatusUnauthorized,
			&handlers.SignBatchRequest{AuthToken: v.authToken, Ballots: v.ballots(1)}, v.pid, "sign-batch")
		c.Assert(got.Code, qt.Equals, errors.ErrZeroWeightVoter.Code)
		c.Assert(got.Data, qt.DeepEquals, handlers.SignRefusalData{Reason: handlers.VoteReasonZeroWeight})
	})

	t.Run("blind rounds with anonymous voting switched off", func(t *testing.T) {
		c := qt.New(t)
		setDisableBlindCSP(t, true)
		pid := bson.NewObjectID().Hex()
		election := internal.HexBytes(internal.RandomBytes(32))
		tok := internal.HexBytes(internal.RandomBytes(32))
		for endpoint, body := range map[string]any{
			"blind-point": &handlers.BlindPointRequest{AuthToken: tok, Elections: []internal.HexBytes{election}},
			"blind-sign": &handlers.BlindSignRequest{AuthToken: tok, Ballots: []handlers.BlindSignBallot{
				{UpstreamID: election, BlindedMessage: internal.HexBytes{0x01}},
			}},
		} {
			got := postRefused(t, http.StatusBadRequest, body, pid, endpoint)
			c.Assert(got.Code, qt.Equals, errors.ErrInvalidData.Code, qt.Commentf(endpoint))
			c.Assert(got.Data, qt.DeepEquals, handlers.SignRefusalData{Reason: handlers.VoteReasonUnavailable},
				qt.Commentf(endpoint))
		}
	})

	t.Run("a malformed ballot after a refusal or before it", func(t *testing.T) {
		c := qt.New(t)
		v := setupRefusalVoter(t, false, false)
		malformed := handlers.SignBatchBallot{Address: v.voter.Address().Bytes()}

		// the refusal came first: it decides, and the malformed ballot is skipped
		got := postRefused(t, http.StatusUnauthorized, &handlers.SignBatchRequest{
			AuthToken: v.authToken, Ballots: append(v.ballots(0), malformed),
		}, v.pid, "sign-batch")
		c.Assert(got.Code, qt.Equals, errors.ErrUnauthorized.Code)
		assertBallotRefusals(c, got.Data.Ballots,
			handlers.SignRefusal{Index: 0, UpstreamID: v.elections[0], Reason: handlers.VoteReasonPaused})

		// the malformed ballot came first: its 400 carries no data
		got = postRefused(t, http.StatusBadRequest, &handlers.SignBatchRequest{
			AuthToken: v.authToken, Ballots: append([]handlers.SignBatchBallot{malformed}, v.ballots(0)...),
		}, v.pid, "sign-batch")
		c.Assert(got.Code, qt.Equals, errors.ErrMalformedBody.Code)
		c.Assert(got.Data, qt.DeepEquals, handlers.SignRefusalData{})
	})
}
