package handlers

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSignOutcome(t *testing.T) {
	c := qt.New(t)
	// each known sentinel maps to its stable code — including wrapped, the form csp.Sign
	// actually returns for signer failures — and everything else to the retryable catch-all.
	for _, tc := range []struct {
		err  error
		code string
	}{
		{csp.ErrProcessAlreadyConsumed, signCodeAlreadyConsumed},
		{csp.ErrUserAlreadySigning, signCodeAlreadySigning},
		{csp.ErrInvalidAuthToken, signCodeAuthInvalid},
		{csp.ErrAuthTokenNotVerified, signCodeAuthInvalid},
		{csp.ErrAddressMismatch, signCodeAddressMismatch},
		{fmt.Errorf("wrapping: %w", csp.ErrProcessAlreadyConsumed), signCodeAlreadyConsumed},
		{csp.ErrSign, signCodeFailed},
		// the shape csp.Sign returns for a storage failure: joined, NOT an auth verdict, so a
		// transient Mongo error stays a retryable per-ballot outcome instead of aborting the batch
		{stderrors.Join(csp.ErrSign, fmt.Errorf("some storage blip")), signCodeFailed},
		{fmt.Errorf("some storage blip"), signCodeFailed},
	} {
		code, message := signOutcome(tc.err)
		c.Assert(code, qt.Equals, tc.code, qt.Commentf("error: %v", tc.err))
		c.Assert(message, qt.Not(qt.Equals), "")
		// the sanitized message must not echo the raw error text
		c.Assert(message, qt.Not(qt.Contains), "blip")
	}
}

func TestValidateVoterAddress(t *testing.T) {
	c := qt.New(t)
	// qt.IsNil rejects a typed-nil *errors.Error (it implements error), so compare the pointer.
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength))),
		qt.Equals, (*errors.Error)(nil))
	c.Assert(validateVoterAddress(nil), qt.Not(qt.IsNil))
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength-1))), qt.Not(qt.IsNil))
	c.Assert(validateVoterAddress(internal.HexBytes(make([]byte, common.AddressLength+1))), qt.Not(qt.IsNil))
}

// TestPhoneParseFailed pins the answers to a phone that could not be parsed: a hash that found no
// free argon2 slot is a 503 to retry, anything else an invalid phone.
func TestPhoneParseFailed(t *testing.T) {
	c := qt.New(t)
	c.Assert(phoneParseFailed(internal.ErrHashingBusy), qt.ErrorIs, errors.ErrServerBusy)
	c.Assert(phoneParseFailed(fmt.Errorf("not a phone number")), qt.ErrorIs, errors.ErrInvalidData)
}

func TestHandlePhoneContact(t *testing.T) {
	c := qt.New(t)

	test := func(org *db.Organization, phone, want string) {
		// the member's stored phone, hashed from the raw input as on member creation
		hashedPhone, err := db.NewHashedPhone(phone, org)
		c.Assert(err, qt.IsNil)

		submitted, err := parseVoterPhone(context.Background(), phone, org)
		c.Assert(err, qt.IsNil)
		normalized, challengeType, err := handlePhoneContact(submitted, hashedPhone)
		c.Assert(err, qt.IsNil)
		c.Assert(normalized, qt.Equals, want)
		c.Assert(challengeType, qt.Equals, notifications.SMSChallenge)

		// another number, or none at all, fails with the same error as an unknown participant
		other, err := parseVoterPhone(context.Background(), "+34699999999", org)
		c.Assert(err, qt.IsNil)
		_, _, err = handlePhoneContact(other, hashedPhone)
		c.Assert(err, qt.ErrorIs, errors.ErrCSPAuthFailed)
		_, _, err = handlePhoneContact(nil, hashedPhone)
		c.Assert(err, qt.ErrorIs, errors.ErrCSPAuthFailed)
	}

	org := &db.Organization{
		Address:   common.Address{0x01, 0x23, 0x45, 0x67, 0x89},
		CreatedAt: time.Now(),
	}
	org.Country = "ES"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+34612345601") })

	org.Country = "FR"
	t.Run(org.Country, func(*testing.T) { test(org, "612345601", "+33612345601") })

	org.Country = "US" // a valid phone with prefix, should keep it regardless of org country
	t.Run(org.Country, func(*testing.T) { test(org, "+34612345601", "+34612345601") })
}

// electionGrid is every question status crossed with the process date situations, each tagged
// with the reason it must produce. base is the clock the dates are built around, margin the
// distance kept from every boundary.
func electionGrid(base time.Time, margin time.Duration) []struct {
	name   string
	vp     *db.VotingProcess
	q      *db.VotingProcessQuestion
	reason VoteReason
} {
	type dates struct {
		name       string
		start, end time.Time
		notStarted bool
		ended      bool
	}
	ds := []dates{
		{name: "no dates"},
		{name: "starts at the grace", start: base.Add(startGrace - margin)},
		{name: "not started", start: base.Add(startGrace + margin), notStarted: true},
		{name: "ends at the grace", end: base.Add(-endGrace + margin)},
		{name: "ended", end: base.Add(-endGrace - margin), ended: true},
	}
	var out []struct {
		name   string
		vp     *db.VotingProcess
		q      *db.VotingProcessQuestion
		reason VoteReason
	}
	for _, status := range []string{
		db.QuestionStatusReady, db.QuestionStatusPaused, db.QuestionStatusEnded,
		db.QuestionStatusResults, db.QuestionStatusCanceled, "", "BOGUS",
	} {
		for _, d := range ds {
			var want VoteReason
			switch {
			case status == db.QuestionStatusCanceled:
				want = VoteReasonCanceled
			case status == db.QuestionStatusEnded, status == db.QuestionStatusResults, d.ended:
				want = VoteReasonClosed
			case d.notStarted:
				want = VoteReasonNotStarted
			case status != db.QuestionStatusReady:
				want = VoteReasonPaused
			default:
				want = ""
			}
			out = append(out, struct {
				name   string
				vp     *db.VotingProcess
				q      *db.VotingProcessQuestion
				reason VoteReason
			}{
				name:   status + "/" + d.name,
				vp:     &db.VotingProcess{StartDate: d.start, EndDate: d.end},
				q:      &db.VotingProcessQuestion{Status: status},
				reason: want,
			})
		}
	}
	return out
}

func TestElectionStateReason(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range electionGrid(now, time.Nanosecond) {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			c.Assert(electionStateReason(tc.vp, tc.q, now), qt.Equals, tc.reason)
		})
	}
	// exactly on each grace boundary: the start is already open, the end is already closed
	c := qt.New(t)
	ready := &db.VotingProcessQuestion{Status: db.QuestionStatusReady}
	c.Assert(electionStateReason(&db.VotingProcess{StartDate: now.Add(startGrace)}, ready, now), qt.Equals, VoteReason(""))
	c.Assert(electionStateReason(&db.VotingProcess{EndDate: now.Add(-endGrace)}, ready, now), qt.Equals, VoteReasonClosed)
}

func TestUnvotableElectionMatchesState(t *testing.T) {
	// unvotableElection reads the clock itself, so keep a margin around every boundary
	for _, tc := range electionGrid(time.Now(), 5*time.Second) {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			votable := unvotableElection(tc.vp, tc.q, time.Now()) == (*errors.Error)(nil)
			c.Assert(votable, qt.Equals, electionStateReason(tc.vp, tc.q, time.Now()) == "")
		})
	}
	c := qt.New(t)
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	ready := &db.VotingProcessQuestion{Status: db.QuestionStatusReady}
	paused := &db.VotingProcessQuestion{Status: db.QuestionStatusPaused}
	for _, tc := range []struct {
		name string
		vp   *db.VotingProcess
		q    *db.VotingProcessQuestion
		want string
	}{
		{"paused and not started", &db.VotingProcess{StartDate: future}, paused, "question is not open for voting"},
		{"paused and ended", &db.VotingProcess{EndDate: past}, paused, "question is not open for voting"},
		{"ready and not started", &db.VotingProcess{StartDate: future}, ready, "process has not started yet"},
		{
			"ready, future start, past end", &db.VotingProcess{StartDate: future, EndDate: past}, ready,
			"process has not started yet",
		},
		{"ready and ended", &db.VotingProcess{EndDate: past}, ready, "process has ended"},
	} {
		err := unvotableElection(tc.vp, tc.q, time.Now())
		c.Assert(err, qt.Not(qt.Equals), (*errors.Error)(nil), qt.Commentf(tc.name))
		c.Assert(err.Error(), qt.Contains, tc.want, qt.Commentf(tc.name))
	}
}

// ballotCase is one ballot of the authorization pass: the reason it is refused for and the
// message today's code answers with ("" for both when it is signed).
type ballotCase struct {
	name   string
	sc     *signContext
	q      *db.VotingProcessQuestion
	reason VoteReason
	msg    string
}

func ballotCases(now time.Time) []ballotCase {
	pid := bson.NewObjectID()
	member := "member"
	sc := func(vp db.VotingProcess) *signContext {
		vp.ID = pid
		return &signContext{process: &vp, memberID: member}
	}
	open := sc(db.VotingProcess{})
	q := func(status string, eligible ...string) *db.VotingProcessQuestion {
		return &db.VotingProcessQuestion{
			ProcessID: pid, Status: status, EligibleMemberIDs: eligible, UpstreamID: internal.HexBytes{0x01},
		}
	}
	notOpen := "question is not open for voting"
	return []ballotCase{
		{"nil question", open, nil, VoteReasonUnknownElection, "election not found in process"},
		{
			"other process", open,
			&db.VotingProcessQuestion{ProcessID: bson.NewObjectID(), Status: db.QuestionStatusReady},
			VoteReasonUnknownElection, "election not found in process",
		},
		{"not eligible", open, q(db.QuestionStatusReady, "other"), VoteReasonNotEligible, "member not eligible for this question"},
		{"eligible", open, q(db.QuestionStatusReady, member), "", ""},
		{"canceled", open, q(db.QuestionStatusCanceled), VoteReasonCanceled, notOpen},
		{"ended status", open, q(db.QuestionStatusEnded), VoteReasonClosed, notOpen},
		{"results", open, q(db.QuestionStatusResults), VoteReasonClosed, notOpen},
		{"paused", open, q(db.QuestionStatusPaused), VoteReasonPaused, notOpen},
		{
			"not started", sc(db.VotingProcess{StartDate: now.Add(time.Hour)}), q(db.QuestionStatusReady),
			VoteReasonNotStarted, "process has not started yet",
		},
		{
			"process ended", sc(db.VotingProcess{EndDate: now.Add(-time.Hour)}), q(db.QuestionStatusReady),
			VoteReasonClosed, "process has ended",
		},
		// the reason follows /check's precedence, the message keeps today's order
		{"not eligible and paused", open, q(db.QuestionStatusPaused, "other"), VoteReasonNotEligible, notOpen},
		{
			"canceled and process ended", sc(db.VotingProcess{EndDate: now.Add(-time.Hour)}), q(db.QuestionStatusCanceled),
			VoteReasonClosed, notOpen,
		},
		{
			"paused and not started", sc(db.VotingProcess{StartDate: now.Add(time.Hour)}), q(db.QuestionStatusPaused),
			VoteReasonNotStarted, notOpen,
		},
		{
			"not started and not eligible", sc(db.VotingProcess{StartDate: now.Add(time.Hour)}), q(db.QuestionStatusReady, "other"),
			VoteReasonNotEligible, "process has not started yet",
		},
		{"votable", open, q(db.QuestionStatusReady), "", ""},
	}
}

func TestBallotRefusal(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range ballotCases(now) {
		t.Run(tc.name, func(t *testing.T) {
			qt.New(t).Assert(ballotRefusal(tc.sc, tc.q, now), qt.Equals, tc.reason)
		})
	}
}

func TestAuthorizeLoadedQuestionMatchesReason(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range ballotCases(now) {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			upstreamID, reason, err := authorizeLoadedQuestion(tc.sc, tc.q, now)
			c.Assert(reason, qt.Equals, tc.reason)
			ok := err == (*errors.Error)(nil)
			c.Assert(ok, qt.Equals, reason == "")
			if ok {
				c.Assert(upstreamID, qt.DeepEquals, tc.q.UpstreamID)
				return
			}
			c.Assert(upstreamID, qt.IsNil)
			c.Assert(err.Error(), qt.Contains, tc.msg)
		})
	}
}

// signRefusalBody is the error body of a refused sign request.
type signRefusalBody struct {
	Error string          `json:"error"`
	Code  int             `json:"code"`
	Data  SignRefusalData `json:"data"`
}

// signBatch calls POST /processes/{pid}/sign-batch through a router, as the API mounts it, and
// decodes the error body (zero on success).
func signBatch(c *qt.C, pid string, req *SignBatchRequest) (int, signRefusalBody) {
	h := New(&csp.CSP{Storage: testDB}, testDB, nil)
	router := chi.NewRouter()
	router.Post("/processes/{processId}/sign-batch", h.ProcessSignBatchHandler)
	body, err := json.Marshal(req)
	c.Assert(err, qt.IsNil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/processes/"+pid+"/sign-batch", bytes.NewReader(body)))
	var out signRefusalBody
	c.Assert(json.Unmarshal(rec.Body.Bytes(), &out), qt.IsNil)
	return rec.Code, out
}

// signRefusalVoter stores a verified token for memberID on a new process of processOrg whose census
// is censusID, returning the process id and the token.
func signRefusalVoter(c *qt.C, processOrg common.Address, censusID, memberID string) (bson.ObjectID, internal.HexBytes) {
	censusOID, err := bson.ObjectIDFromHex(censusID)
	c.Assert(err, qt.IsNil)
	oid, err := testDB.SetVotingProcess(&db.VotingProcess{OrgAddress: processOrg, CensusID: censusOID})
	c.Assert(err, qt.IsNil)
	token := internal.HexBytes(internal.RandomBytes(16))
	c.Assert(testDB.SetCSPAuth(token, internal.HexBytesFromString(memberID), internal.HexBytes(oid[:]), ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(token), qt.IsNil)
	return oid, token
}

// signRefusalCensus stores an organization with one member in one census, returning their ids.
func signRefusalCensus(c *qt.C, org common.Address) (censusID, memberID string) {
	c.Assert(testDB.SetOrganization(&db.Organization{Address: org, CreatedAt: time.Now()}), qt.IsNil)
	memberID, err := testDB.SetOrgMember("salt", &db.OrgMember{OrgAddress: org, Email: "voter@example.com"})
	c.Assert(err, qt.IsNil)
	censusID, err = testDB.SetCensus(&db.Census{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.SetCensusParticipant(&db.CensusParticipant{CensusID: censusID, ParticipantID: memberID}), qt.IsNil)
	return censusID, memberID
}

func TestSignRefusedWhenOrgMemberGone(t *testing.T) {
	c := qt.New(t)
	// the member is deleted with its participant, so the process points from a second organization at
	// the first one's census: the participant is found and OrgMember(processOrg, id) is not
	censusOrg := common.Address{0xa1, byte(time.Now().UnixNano())}
	processOrg := common.Address{0xa2, byte(time.Now().UnixNano())}
	c.Assert(testDB.SetOrganization(&db.Organization{Address: processOrg, CreatedAt: time.Now()}), qt.IsNil)
	censusID, memberID := signRefusalCensus(c, censusOrg)
	oid, token := signRefusalVoter(c, processOrg, censusID, memberID)

	status, body := signBatch(c, oid.Hex(), &SignBatchRequest{AuthToken: token, Ballots: []SignBatchBallot{
		{UpstreamID: internal.HexBytes{0x01}, Address: make(internal.HexBytes, common.AddressLength)},
	}})
	c.Assert(status, qt.Equals, http.StatusNotFound)
	c.Assert(body.Code, qt.Equals, errors.ErrUserNotFound.Code)
	c.Assert(body.Data, qt.DeepEquals, SignRefusalData{Reason: VoteReasonNotInCensus})
}

func TestSignBatchCollectsRefusals(t *testing.T) {
	c := qt.New(t)
	org := common.Address{0xa3, byte(time.Now().UnixNano())}
	censusID, memberID := signRefusalCensus(c, org)
	oid, token := signRefusalVoter(c, org, censusID, memberID)
	paused, ready := internal.HexBytes(internal.RandomBytes(32)), internal.HexBytes(internal.RandomBytes(32))
	for i, q := range []struct {
		id     internal.HexBytes
		status string
	}{{paused, db.QuestionStatusPaused}, {ready, db.QuestionStatusReady}} {
		_, err := testDB.SetQuestion(&db.VotingProcessQuestion{
			ProcessID: oid, OrgAddress: org, Order: i, UpstreamID: q.id, Status: q.status,
		})
		c.Assert(err, qt.IsNil)
	}
	address := make(internal.HexBytes, common.AddressLength)
	unknown := internal.HexBytes(internal.RandomBytes(32))

	// a malformed ballot after a refusal is skipped: the first refusal decides, every refusal is listed
	status, body := signBatch(c, oid.Hex(), &SignBatchRequest{AuthToken: token, Ballots: []SignBatchBallot{
		{UpstreamID: paused, Address: address},
		{UpstreamID: unknown, Address: address},
		{UpstreamID: ready, Address: internal.HexBytes{0x01, 0x02, 0x03}},
	}})
	c.Assert(status, qt.Equals, http.StatusUnauthorized)
	c.Assert(body.Code, qt.Equals, errors.ErrUnauthorized.Code)
	c.Assert(body.Error, qt.Contains, "question is not open for voting")
	c.Assert(body.Error, qt.Contains, "at index 0")
	c.Assert(body.Data, qt.DeepEquals, SignRefusalData{Ballots: []SignRefusal{
		{Index: 0, UpstreamID: paused, Reason: VoteReasonPaused},
		{Index: 1, UpstreamID: unknown, Reason: VoteReasonUnknownElection},
	}})

	// a malformed ballot first answers its 400, with no data
	status, body = signBatch(c, oid.Hex(), &SignBatchRequest{AuthToken: token, Ballots: []SignBatchBallot{
		{UpstreamID: ready, Address: internal.HexBytes{0x01, 0x02, 0x03}},
		{UpstreamID: paused, Address: address},
	}})
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	c.Assert(body.Data, qt.DeepEquals, SignRefusalData{})
}

func TestAuthorizeQuestion(t *testing.T) {
	c := qt.New(t)
	oid := bson.NewObjectID()
	org := common.Address{0xa4, byte(time.Now().UnixNano())}
	ready := internal.HexBytes(internal.RandomBytes(32))
	_, err := testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: oid, OrgAddress: org, UpstreamID: ready, Status: db.QuestionStatusReady,
	})
	c.Assert(err, qt.IsNil)
	h := New(&csp.CSP{Storage: testDB}, testDB, nil)
	sc := &signContext{process: &db.VotingProcess{ID: oid}, memberID: "member"}
	now := time.Now()

	id, reason, sErr := h.authorizeQuestion(sc, nil, now)
	c.Assert(id, qt.IsNil)
	c.Assert(reason, qt.Equals, VoteReason(""))
	c.Assert(sErr.Code, qt.Equals, errors.ErrMalformedBody.Code)
	c.Assert(sErr.Error(), qt.Contains, "missing election id")

	id, reason, sErr = h.authorizeQuestion(sc, internal.RandomBytes(32), now)
	c.Assert(id, qt.IsNil)
	c.Assert(reason, qt.Equals, VoteReasonUnknownElection)
	c.Assert(sErr.Code, qt.Equals, errors.ErrUnauthorized.Code)
	c.Assert(sErr.Error(), qt.Contains, "election not found in process")

	id, reason, sErr = h.authorizeQuestion(sc, ready, now)
	c.Assert(sErr == (*errors.Error)(nil), qt.IsTrue)
	c.Assert(reason, qt.Equals, VoteReason(""))
	c.Assert(id, qt.DeepEquals, ready)
}

func TestBallotPass(t *testing.T) {
	pid := bson.NewObjectID()
	sc := &signContext{process: &db.VotingProcess{ID: pid}, memberID: "member"}
	ready, paused := internal.HexBytes{0x01}, internal.HexBytes{0x02}
	unknown, ready2 := internal.HexBytes{0x03}, internal.HexBytes{0x04}
	byUpstream := map[string]*db.VotingProcessQuestion{}
	for id, status := range map[string]string{
		string(ready): db.QuestionStatusReady, string(paused): db.QuestionStatusPaused, string(ready2): db.QuestionStatusReady,
	} {
		byUpstream[id] = &db.VotingProcessQuestion{ProcessID: pid, UpstreamID: internal.HexBytes(id), Status: status}
	}
	ballotRepeats := "the ballot at index %d repeats the election of index %d"
	pausedAt0 := SignRefusalData{Ballots: []SignRefusal{{Index: 0, UpstreamID: paused, Reason: VoteReasonPaused}}}
	for _, tc := range []struct {
		name    string
		ids     []internal.HexBytes
		bad     map[int]bool // indexes whose own check fails
		noCheck bool         // blind-point: no per-ballot check
		repeats string       // "" means ballotRepeats
		code    int          // 0: authorized
		msgs    []string
		data    any
	}{
		{name: "all authorized, request order", ids: []internal.HexBytes{ready2, ready}},
		{
			name: "refusals listed, first decides", ids: []internal.HexBytes{paused, unknown, ready},
			code: errors.ErrUnauthorized.Code, msgs: []string{"question is not open for voting", "at index 0"},
			data: SignRefusalData{Ballots: []SignRefusal{
				{Index: 0, UpstreamID: paused, Reason: VoteReasonPaused},
				{Index: 1, UpstreamID: unknown, Reason: VoteReasonUnknownElection},
			}},
		},
		{
			name: "duplicate after refusal skipped", ids: []internal.HexBytes{paused, ready, ready},
			code: errors.ErrUnauthorized.Code, data: pausedAt0,
		},
		{
			name: "empty id after refusal skipped", ids: []internal.HexBytes{paused, {}},
			code: errors.ErrUnauthorized.Code, data: pausedAt0,
		},
		{
			name: "failing check after refusal skipped", ids: []internal.HexBytes{paused, ready}, bad: map[int]bool{1: true},
			code: errors.ErrUnauthorized.Code, data: pausedAt0,
		},
		{
			name: "refused ballot's check skipped", ids: []internal.HexBytes{paused}, bad: map[int]bool{0: true},
			code: errors.ErrUnauthorized.Code, data: pausedAt0,
		},
		{
			name: "duplicate first", ids: []internal.HexBytes{ready, ready, paused},
			code: errors.ErrMalformedBody.Code, msgs: []string{"the ballot at index 1 repeats the election of index 0"},
		},
		{
			name: "empty id first", ids: []internal.HexBytes{{}, paused},
			code: errors.ErrMalformedBody.Code, msgs: []string{"missing election id", "at index 0"},
		},
		{
			name: "failing check first", ids: []internal.HexBytes{ready, paused}, bad: map[int]bool{0: true},
			code: errors.ErrMalformedBody.Code, msgs: []string{"bad item", "at index 0"},
		},
		{
			name: "repeated refused election listed once", ids: []internal.HexBytes{paused, paused},
			code: errors.ErrUnauthorized.Code, data: pausedAt0,
		},
		{name: "no check", ids: []internal.HexBytes{ready, ready2}, noCheck: true},
		{
			name: "blind-point duplicate message", ids: []internal.HexBytes{ready, ready}, noCheck: true,
			repeats: "the election at index %d repeats index %d",
			code:    errors.ErrMalformedBody.Code, msgs: []string{"the election at index 1 repeats index 0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			pass := ballotPass{sc: sc, byUpstream: byUpstream, now: time.Now(), repeats: cmp.Or(tc.repeats, ballotRepeats)}
			if !tc.noCheck {
				pass.check = func(i int) *errors.Error {
					if tc.bad[i] {
						return errors.Ptr(errors.ErrMalformedBody.With("bad item"))
					}
					return nil
				}
			}
			got, err := pass.authorize(tc.ids)
			if tc.code == 0 {
				c.Assert(err == (*errors.Error)(nil), qt.IsTrue)
				c.Assert(got, qt.DeepEquals, tc.ids)
				return
			}
			c.Assert(got, qt.IsNil)
			c.Assert(err == (*errors.Error)(nil), qt.IsFalse)
			c.Assert(err.Code, qt.Equals, tc.code)
			for _, msg := range tc.msgs {
				c.Assert(err.Error(), qt.Contains, msg)
			}
			c.Assert(err.Data, qt.DeepEquals, tc.data)
		})
	}
}

func TestSignatureBudgetSpent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		row       *db.CSPProcess
		anonymous bool
		want      bool
	}{
		{"nil ecdsa", nil, false, false},
		{"nil blind", nil, true, false},
		{"blind unused", &db.CSPProcess{}, true, false},
		{"blind used", &db.CSPProcess{Used: true}, true, true},
		{"blind ignores times voted", &db.CSPProcess{TimesVoted: db.MaxCSPSignaturesPerElection}, true, false},
		{"ecdsa below limit", &db.CSPProcess{TimesVoted: db.MaxCSPSignaturesPerElection - 1}, false, false},
		{"ecdsa at limit", &db.CSPProcess{TimesVoted: db.MaxCSPSignaturesPerElection}, false, true},
		{"ecdsa ignores used", &db.CSPProcess{Used: true, TimesVoted: 1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qt.New(t).Assert(signatureBudgetSpent(tc.row, tc.anonymous), qt.Equals, tc.want)
		})
	}
}

func TestVoteReason(t *testing.T) {
	open := voteReasonInput{inCensus: true, eligible: true, published: true}
	with := func(f func(*voteReasonInput)) voteReasonInput {
		in := open
		f(&in)
		return in
	}
	for _, tc := range []struct {
		name string
		in   voteReasonInput
		want VoteReason
	}{
		{"all clear", open, ""},
		{"not in census", with(func(i *voteReasonInput) { i.inCensus = false }), VoteReasonNotInCensus},
		{"not eligible", with(func(i *voteReasonInput) { i.eligible = false }), VoteReasonNotEligible},
		{"zero weight", with(func(i *voteReasonInput) { i.zeroWeight = true }), VoteReasonZeroWeight},
		{"not published", with(func(i *voteReasonInput) { i.published = false }), VoteReasonNotPublished},
		{"canceled", with(func(i *voteReasonInput) { i.state = VoteReasonCanceled }), VoteReasonCanceled},
		{"closed", with(func(i *voteReasonInput) { i.state = VoteReasonClosed }), VoteReasonClosed},
		{"budget", with(func(i *voteReasonInput) { i.budgetSpent = true }), VoteReasonSignatureLimitReached},
		{"not started", with(func(i *voteReasonInput) { i.state = VoteReasonNotStarted }), VoteReasonNotStarted},
		{"paused", with(func(i *voteReasonInput) { i.state = VoteReasonPaused }), VoteReasonPaused},
		{"unavailable", with(func(i *voteReasonInput) { i.unavailable = true }), VoteReasonUnavailable},
		{"not eligible beats canceled", with(func(i *voteReasonInput) {
			i.eligible, i.state = false, VoteReasonCanceled
		}), VoteReasonNotEligible},
		{"not in census beats not eligible", with(func(i *voteReasonInput) {
			i.inCensus, i.eligible = false, false
		}), VoteReasonNotInCensus},
		{"canceled beats budget", with(func(i *voteReasonInput) {
			i.state, i.budgetSpent = VoteReasonCanceled, true
		}), VoteReasonCanceled},
		{"budget beats paused", with(func(i *voteReasonInput) {
			i.state, i.budgetSpent = VoteReasonPaused, true
		}), VoteReasonSignatureLimitReached},
		{"budget beats not started", with(func(i *voteReasonInput) {
			i.state, i.budgetSpent = VoteReasonNotStarted, true
		}), VoteReasonSignatureLimitReached},
		{"paused beats unavailable", with(func(i *voteReasonInput) {
			i.state, i.unavailable = VoteReasonPaused, true
		}), VoteReasonPaused},
		{"not published beats paused", with(func(i *voteReasonInput) {
			i.published, i.state = false, VoteReasonPaused
		}), VoteReasonNotPublished},
		{"process ended beats zero weight", with(func(i *voteReasonInput) {
			i.processEnded, i.zeroWeight = true, true
		}), VoteReasonClosed},
		{"process ended beats not published", with(func(i *voteReasonInput) {
			i.processEnded, i.published = true, false
		}), VoteReasonClosed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qt.New(t).Assert(voteReason(tc.in), qt.Equals, tc.want)
		})
	}
}

// TestCanVoteReason pins that reason explains canVote: false gives one of the three eligibility
// reasons, true never does, so clients can read reason against canVote first, votableNow second.
func TestCanVoteReason(t *testing.T) {
	c := qt.New(t)
	states := []VoteReason{"", VoteReasonCanceled, VoteReasonClosed, VoteReasonNotStarted, VoteReasonPaused}
	for bits := range 1 << 7 {
		for _, state := range states {
			in := voteReasonInput{
				inCensus: bits&1 != 0, eligible: bits&2 != 0, processEnded: bits&4 != 0, zeroWeight: bits&8 != 0,
				published: bits&16 != 0, budgetSpent: bits&32 != 0, unavailable: bits&64 != 0, state: state,
			}
			reason := voteReason(in)
			if canVote(in) {
				c.Assert(reason, qt.Not(qt.Equals), VoteReasonNotInCensus, qt.Commentf("%+v", in))
				c.Assert(reason, qt.Not(qt.Equals), VoteReasonNotEligible, qt.Commentf("%+v", in))
				continue
			}
			c.Assert([]VoteReason{VoteReasonNotInCensus, VoteReasonNotEligible, VoteReasonClosed},
				qt.Contains, reason, qt.Commentf("%+v", in))
		}
	}
}

// TestMemberPasswordMatches covers the password check behind auth step 0: it matches the stored
// argon2 hash of the client hash and nothing else, and a check that finds no free argon2 slot gives
// up and answers ErrServerBusy instead of blocking.
func TestMemberPasswordMatches(t *testing.T) {
	c := qt.New(t)
	h := New(&csp.CSP{PasswordSalt: "salt"}, nil, nil)
	clientHash := strings.Repeat("ab", 32)
	stored := &db.OrgMember{ID: bson.NewObjectID()}
	stored.HashedPass = db.HashMemberPassword("salt", stored.ID, clientHash)

	ok, err := h.memberPasswordMatches(context.Background(), clientHash, stored)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsTrue)

	ok, err = h.memberPasswordMatches(context.Background(), strings.Repeat("cd", 32), stored)
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// the hash is salted with the member ID: another member's stored hash does not match
	ok, err = h.memberPasswordMatches(context.Background(), clientHash,
		&db.OrgMember{ID: bson.NewObjectID(), HashedPass: stored.HashedPass})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// a member without a stored password never matches
	ok, err = h.memberPasswordMatches(context.Background(), clientHash, &db.OrgMember{ID: stored.ID})
	c.Assert(err, qt.IsNil)
	c.Assert(ok, qt.IsFalse)

	// no argon2 slot freed up in time: the check gives up, and the voter is told to retry later
	h.hashPassword = func(context.Context, string, bson.ObjectID, string) ([]byte, error) {
		return nil, internal.ErrHashingBusy
	}
	_, err = h.memberPasswordMatches(context.Background(), clientHash, stored)
	c.Assert(err, qt.ErrorIs, internal.ErrHashingBusy)
	c.Assert(passwordCheckFailed(err), qt.ErrorIs, errors.ErrServerBusy)
	c.Assert(passwordCheckFailed(context.Canceled), qt.ErrorIs, errors.ErrGenericInternalServerError)
}
