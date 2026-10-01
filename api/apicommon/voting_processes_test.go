package apicommon

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

// TestProcessEndedAt verifies that the process-level endedAt is the latest question EndedAt,
// but only when every published question (non-empty UpstreamID) has a non-zero EndedAt.
func TestProcessEndedAt(t *testing.T) {
	c := qt.New(t)

	upstream := internal.HexBytes("election-1")

	t1 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// no published questions: zero time
	c.Assert(processEndedAt(nil), qt.DeepEquals, time.Time{})
	c.Assert(processEndedAt([]db.VotingProcessQuestion{}), qt.DeepEquals, time.Time{})

	// one draft question (no UpstreamID): zero time (no published questions)
	draft := db.VotingProcessQuestion{Title: db.MultiLangString{"default": "draft"}}
	c.Assert(processEndedAt([]db.VotingProcessQuestion{draft}), qt.DeepEquals, time.Time{})

	// one published question with endedAt: return it
	q1 := db.VotingProcessQuestion{UpstreamID: upstream, EndedAt: t1}
	c.Assert(processEndedAt([]db.VotingProcessQuestion{q1}), qt.DeepEquals, t1)

	// two published questions both with endedAt: return the latest
	upstream2 := internal.HexBytes("election-2")
	q2 := db.VotingProcessQuestion{UpstreamID: upstream2, EndedAt: t2}
	c.Assert(processEndedAt([]db.VotingProcessQuestion{q1, q2}), qt.DeepEquals, t2)

	// one question has no endedAt: zero time (not all ended early)
	q3 := db.VotingProcessQuestion{UpstreamID: upstream2} // EndedAt is zero
	c.Assert(processEndedAt([]db.VotingProcessQuestion{q1, q3}), qt.DeepEquals, time.Time{})

	// mix of draft (no UpstreamID) and published with endedAt: only count published ones
	q4 := db.VotingProcessQuestion{UpstreamID: upstream, EndedAt: t1}
	c.Assert(processEndedAt([]db.VotingProcessQuestion{draft, q4}), qt.DeepEquals, t1)
}

// TestVotingProcessResponseFromDBEndedAt verifies that VotingProcessResponseFromDB sets the
// process-level endedAt only when every published question has a non-zero EndedAt, and that
// PublicQuestionResponseFromDB forwards the question-level endedAt.
func TestVotingProcessResponseFromDBEndedAt(t *testing.T) {
	c := qt.New(t)

	upstream1 := internal.HexBytes("up1")
	upstream2 := internal.HexBytes("up2")
	t1 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 3, 1, 11, 0, 0, 0, time.UTC)

	vp := &db.VotingProcess{Title: db.MultiLangString{"default": "P"}}

	// both questions ended early: process-level endedAt is the later one
	qs := []db.VotingProcessQuestion{
		{UpstreamID: upstream1, EndedAt: t1},
		{UpstreamID: upstream2, EndedAt: t2},
	}
	resp := VotingProcessResponseFromDB(vp, qs, nil, "")
	c.Assert(resp.EndedAt, qt.Equals, "2026-03-01T11:00:00Z")

	// one question has no endedAt: process-level endedAt is absent
	qs2 := []db.VotingProcessQuestion{
		{UpstreamID: upstream1, EndedAt: t1},
		{UpstreamID: upstream2}, // EndedAt zero
	}
	resp2 := VotingProcessResponseFromDB(vp, qs2, nil, "")
	c.Assert(resp2.EndedAt, qt.Equals, "")

	// all draft questions: process-level endedAt is absent
	qs3 := []db.VotingProcessQuestion{
		{Title: db.MultiLangString{"default": "draft"}},
	}
	resp3 := VotingProcessResponseFromDB(vp, qs3, nil, "")
	c.Assert(resp3.EndedAt, qt.Equals, "")
}

// TestPublicQuestionResponseFromDBEndedAt verifies that PublicQuestionResponseFromDB forwards
// the question-level endedAt from the db question, and leaves it absent for non-ended questions.
func TestPublicQuestionResponseFromDBEndedAt(t *testing.T) {
	c := qt.New(t)

	t1 := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)

	q := &db.VotingProcessQuestion{
		Title:   db.MultiLangString{"default": "Q"},
		EndedAt: t1,
	}
	resp := PublicQuestionResponseFromDB(q, nil)
	c.Assert(resp.EndedAt, qt.Equals, "2026-05-01T09:00:00Z")

	qNoEnd := &db.VotingProcessQuestion{Title: db.MultiLangString{"default": "Q no end"}}
	respNoEnd := PublicQuestionResponseFromDB(qNoEnd, nil)
	c.Assert(respNoEnd.EndedAt, qt.Equals, "")
}
