package apicommon

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// TestJobResponseFromDBBatchVoteTerminal covers the read-path derivation for batch vote jobs: the
// worker bumps the progress counter and writes the terminal status as two separate operations, so a
// job can legitimately be read with every envelope reported and no status stored — briefly during
// normal operation, or forever if the process died in between. The response must not call that
// pending.
func TestJobResponseFromDBBatchVoteTerminal(t *testing.T) {
	c := qt.New(t)

	batch := func(total, added int, votes []db.VoteJobResult) *db.Job {
		return &db.Job{
			JobID:  "job",
			Type:   db.JobTypeRelayVotes,
			Status: db.JobStatusPending,
			Total:  total,
			Added:  added,
			Result: &db.JobResult{Votes: votes},
		}
	}
	completed := db.VoteJobResult{Status: db.JobStatusCompleted}
	failed := db.VoteJobResult{Status: db.JobStatusFailed, Error: "vote already exists"}

	c.Run("every envelope succeeded", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 2, []db.VoteJobResult{completed, completed}))
		c.Assert(got.Status, qt.Equals, db.JobStatusCompleted)
		c.Assert(got.Errors, qt.HasLen, 0)
	})

	c.Run("one envelope failed", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 2, []db.VoteJobResult{completed, failed}))
		c.Assert(got.Status, qt.Equals, db.JobStatusFailed)
		c.Assert(got.Errors, qt.DeepEquals, []string{"vote 1: vote already exists"})
	})

	c.Run("still waiting on an envelope", func(c *qt.C) {
		got := JobResponseFromDB(batch(2, 1, []db.VoteJobResult{completed, {Status: db.JobStatusPending}}))
		c.Assert(got.Status, qt.Equals, db.JobStatusPending,
			qt.Commentf("a job with envelopes still outstanding is genuinely pending"))
		c.Assert(got.Result.Progress, qt.Equals, 50)
	})

	c.Run("a stored terminal status wins", func(c *qt.C) {
		job := batch(2, 2, []db.VoteJobResult{completed, failed})
		job.Status = db.JobStatusCompleted // already closed by the worker
		got := JobResponseFromDB(job)
		c.Assert(got.Status, qt.Equals, db.JobStatusCompleted,
			qt.Commentf("the derivation only fills a gap; it never overrides what the worker wrote"))
	})

	c.Run("other job types are untouched", func(c *qt.C) {
		got := JobResponseFromDB(&db.Job{
			JobID: "job", Type: db.JobTypeOrgMembers, Status: db.JobStatusPending, Total: 2, Added: 2,
		})
		c.Assert(got.Status, qt.Equals, db.JobStatusPending,
			qt.Commentf("an import job's added==total does not mean it is finished"))
	})
}

// TestUpsertOrgMemberRequestWeight pins the weight a member update carries: left out keeps the
// stored one, cleared is the default 1 rather than the 0 ParseUint64 reads "" as.
func TestUpsertOrgMemberRequestWeight(t *testing.T) {
	c := qt.New(t)

	update, err := (&UpsertOrgMemberRequest{}).ToDB()
	c.Assert(err, qt.IsNil)
	c.Assert(update.Weight, qt.IsNil)

	for weight, want := range map[string]uint64{"": 1, "0": 0, "42": 42} {
		update, err := (&UpsertOrgMemberRequest{Weight: new(weight)}).ToDB()
		c.Assert(err, qt.IsNil)
		c.Assert(*update.Weight, qt.Equals, want, qt.Commentf("weight %q", weight))
	}

	_, err = (&UpsertOrgMemberRequest{Weight: new("heavy")}).ToDB()
	c.Assert(err, qt.ErrorMatches, `invalid weight "heavy"`)
}

// TestOrgMemberWeight pins the weight a bulk-imported member carries: empty is the default 1, and
// an unparsable weight is rejected rather than stored as 0 or MaxUint64.
func TestOrgMemberWeight(t *testing.T) {
	c := qt.New(t)

	for weight, want := range map[string]uint64{"": 1, "0": 0, "42": 42} {
		member, err := (&OrgMember{Weight: weight}).ToDB()
		c.Assert(err, qt.IsNil)
		c.Assert(member.Weight, qt.Equals, want, qt.Commentf("weight %q", weight))
	}

	for _, weight := range []string{"heavy", "18446744073709551616"} {
		_, err := (&AddMembersRequest{Members: []OrgMember{{}, {Weight: weight}}}).ToDB()
		c.Assert(err, qt.ErrorMatches, `member 1: invalid weight ".*"`, qt.Commentf("weight %q", weight))
	}
}

// TestMemberPasswordClientHash pins the member password the API takes: the client hash (64 lowercase
// hex characters) or nothing. A plaintext password is refused by both write paths, before anything
// is stored, and a read never returns the password, only whether there is one.
func TestMemberPasswordClientHash(t *testing.T) {
	c := qt.New(t)
	hash := strings.Repeat("0123456789abcdef", 4)

	member, err := (&OrgMember{Password: hash}).ToDB()
	c.Assert(err, qt.IsNil)
	c.Assert(member.Password, qt.Equals, hash)
	_, err = (&OrgMember{}).ToDB()
	c.Assert(err, qt.IsNil)

	update, err := (&UpsertOrgMemberRequest{Password: new("")}).ToDB()
	c.Assert(err, qt.IsNil)
	c.Assert(*update.Password, qt.Equals, "")
	update, err = (&UpsertOrgMemberRequest{Password: new(hash)}).ToDB()
	c.Assert(err, qt.IsNil)
	c.Assert(*update.Password, qt.Equals, hash)

	for _, bad := range []string{"s3cret", strings.ToUpper(hash), hash[:63]} {
		_, err := (&AddMembersRequest{Members: []OrgMember{{}, {Password: bad}}}).ToDB()
		c.Assert(err, qt.ErrorMatches, `member 1: invalid password: .*`, qt.Commentf("password %q", bad))
		c.Assert(err, qt.Not(qt.ErrorMatches), ".*"+bad+".*")
		_, err = (&UpsertOrgMemberRequest{Password: new(bad)}).ToDB()
		c.Assert(err, qt.ErrorMatches, `invalid password: .*`, qt.Commentf("password %q", bad))
	}

	c.Assert(OrgMemberFromDb(db.OrgMember{HashedPass: []byte{1}}).HasPassword, qt.IsTrue)
	c.Assert(OrgMemberFromDb(db.OrgMember{HashedPass: []byte{1}}).Password, qt.Equals, "")
	c.Assert(OrgMemberFromDb(db.OrgMember{}).HasPassword, qt.IsFalse)
}

func TestValidateMetaKeys(t *testing.T) {
	c := qt.New(t)

	c.Assert(ValidateMetaKeys(map[string]any{
		"region": "Europe",
		"contact": map[string]any{
			"email":   "info@example.com",
			"methods": []any{map[string]any{"kind": "phone"}},
		},
		"price": "$5",
	}), qt.IsNil)

	for _, meta := range []map[string]any{
		{"": "empty"},
		{"a.b": 1},
		{"$set": 1},
		{"settings": map[string]any{"a.b": 1}},
		{"settings": map[string]any{"$x": 1}},
		{"list": []any{map[string]any{"a.b": 1}}},
		{"settings": map[string]any{"": 1}},
	} {
		c.Assert(ValidateMetaKeys(meta), qt.IsNotNil, qt.Commentf("%v", meta))
	}
}

func TestValidateMetaShorthands(t *testing.T) {
	c := qt.New(t)

	c.Assert(ValidateMetaShorthands(nil, nil, nil), qt.IsNil)
	c.Assert(ValidateMetaShorthands(
		&MultilingualText{"default": "Acme", "es": "Acme ES"},
		&MultilingualText{"default": "https://example.com/logo.png"},
		nil,
	), qt.IsNil)
	c.Assert(ValidateMetaShorthands(&MultilingualText{"default": "x", "en.US": "y"}, nil, nil), qt.IsNotNil)
	c.Assert(ValidateMetaShorthands(nil, nil, &MultilingualText{"": "x", "default": "y"}), qt.IsNotNil)
	c.Assert(ValidateMetaShorthands(nil, &MultilingualText{"$x": "y", "default": "z"}, nil), qt.IsNotNil)
}

func TestValidateMetaPath(t *testing.T) {
	c := qt.New(t)

	for _, path := range []string{"region", "contact.email", "name.default", "a$b"} {
		c.Assert(ValidateMetaPath(path), qt.IsNil, qt.Commentf("%q", path))
	}
	for _, path := range []string{"", ".", "a.", ".a", "a..b", "$a", "a.$b"} {
		c.Assert(ValidateMetaPath(path), qt.IsNotNil, qt.Commentf("%q", path))
	}
}
