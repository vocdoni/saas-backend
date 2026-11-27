package handlers

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"

	"github.com/ethereum/go-ethereum/common"
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

func TestVoterAuthError(t *testing.T) {
	c := qt.New(t)
	for _, tc := range []struct {
		err  error
		want errors.Error
	}{
		{csp.ErrChallengeCodeFailure, errors.ErrChallengeCodeMismatch},
		{csp.ErrTooManyAttempts, errors.ErrChallengeLocked},
		{csp.ErrTokenExpired, errors.ErrChallengeExpired},
		{csp.ErrChallengeConsumed, errors.ErrChallengeExpired},
		{fmt.Errorf("wrapping: %w", csp.ErrTokenExpired), errors.ErrChallengeExpired},
		{csp.ErrStorageFailure, errors.ErrInternalStorageError},
		{csp.ErrNotificationFailure, errors.ErrGenericInternalServerError},
		{csp.ErrPrepareSignature, errors.ErrGenericInternalServerError},
		{csp.ErrInvalidAuthToken, errors.ErrUnauthorized},
		{fmt.Errorf("anything else"), errors.ErrUnauthorized},
		// API errors pass through untouched
		{errors.ErrCSPAuthFailed, errors.ErrCSPAuthFailed},
		{errors.ErrAttemptCoolDownTime.WithData(map[string]any{"coolDownTime": int64(1)}), errors.ErrAttemptCoolDownTime},
	} {
		got := voterAuthError(tc.err)
		c.Assert(got.Code, qt.Equals, tc.want.Code, qt.Commentf("error: %v", tc.err))
		c.Assert(got.HTTPstatus, qt.Equals, tc.want.HTTPstatus, qt.Commentf("error: %v", tc.err))
	}
	// the challenge failures keep the 401 clients already branch on; server failures are 5xx
	for _, e := range []errors.Error{
		errors.ErrChallengeCodeMismatch, errors.ErrChallengeLocked, errors.ErrChallengeExpired,
	} {
		c.Assert(e.HTTPstatus, qt.Equals, http.StatusUnauthorized)
	}
	c.Assert(voterAuthError(csp.ErrStorageFailure).HTTPstatus >= http.StatusInternalServerError, qt.IsTrue)
	// a consumed code and an expired one answer byte-identical bodies
	expired, err := json.Marshal(voterAuthError(csp.ErrTokenExpired))
	c.Assert(err, qt.IsNil)
	consumed, err := json.Marshal(voterAuthError(fmt.Errorf("verifying: %w", csp.ErrChallengeConsumed)))
	c.Assert(err, qt.IsNil)
	c.Assert(string(consumed), qt.Equals, string(expired))
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
			votable := unvotableElection(tc.vp, tc.q) == (*errors.Error)(nil)
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
		err := unvotableElection(tc.vp, tc.q)
		c.Assert(err, qt.Not(qt.Equals), (*errors.Error)(nil), qt.Commentf(tc.name))
		c.Assert(err.Error(), qt.Contains, tc.want, qt.Commentf(tc.name))
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
