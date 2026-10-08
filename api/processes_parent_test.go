package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"path"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// parentProcessFixture is a published two-question process whose parent election is under test.
type parentProcessFixture struct {
	token      string
	orgAddress common.Address
	req        *apicommon.CreateVotingProcessRequest
	members    []apicommon.OrgMember
	pid        string
	elections  []internal.HexBytes // question elections, in question order
}

// setupParentProcess publishes a two-question process with a description and an external
// stream, so its parent election's document has every process-level field to carry.
func setupParentProcess(t *testing.T) *parentProcessFixture {
	t.Helper()
	token := testCreateUser(t, "parentpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)

	req := minimalVotingProcessRequest(orgAddress)
	req.StartDate = ""
	req.Description = db.MultiLangString{"default": "Yearly assembly", "es": "Asamblea general"}
	req.StreamURI = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	req.Census = apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
	}
	second := req.Questions[0]
	second.Title = db.MultiLangString{"default": "Q2"}
	req.Questions = append(req.Questions, second)
	// display info of the first question and its first choice, which its document must carry
	req.Questions[0].Metadata = map[string]any{
		"note": "first question",
		"choices": []any{map[string]any{
			"value": 0, "description": map[string]any{"default": "The yes option"}, "extra": "kept verbatim",
		}},
	}
	pid, elections := publishProcessRequest(t, token, req)
	return &parentProcessFixture{
		token: token, orgAddress: orgAddress, req: req, members: members, pid: pid, elections: elections,
	}
}

// assertParentElection checks the parent election of a published process: its document carries
// the process text and media with no questions, its hash is the SHA-256 of the served bytes, the
// chain commits both with a census of one, and every question election is linked to it.
func assertParentElection(t *testing.T, f *parentProcessFixture, got *apicommon.VotingProcessResponse) {
	t.Helper()
	c := qt.New(t)
	c.Assert(got.UpstreamID, qt.Not(qt.HasLen), 0)
	for i, q := range got.Questions {
		c.Assert(q.UpstreamID, qt.Not(qt.DeepEquals), got.UpstreamID, qt.Commentf("question %d", i))
		c.Assert(q.ParentUpstreamID, qt.DeepEquals, got.UpstreamID, qt.Commentf("question %d", i))
	}
	c.Assert(got.MetadataURL, qt.Not(qt.Equals), "")
	served, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(got.MetadataURL))
	c.Assert(code, qt.Equals, http.StatusOK)
	sum := sha256.Sum256(served)
	c.Assert([]byte(got.MetadataHash), qt.DeepEquals, sum[:])

	var raw map[string]any
	c.Assert(json.Unmarshal(served, &raw), qt.IsNil)
	c.Assert(raw["questions"], qt.DeepEquals, []any{})
	var doc dvoteapi.ElectionMetadata
	c.Assert(json.Unmarshal(served, &doc), qt.IsNil)
	c.Assert(doc.Title, qt.DeepEquals, dvoteapi.LanguageString(f.req.Title))
	c.Assert(doc.Description, qt.DeepEquals, dvoteapi.LanguageString(f.req.Description))
	c.Assert(doc.Media, qt.Equals, dvoteapi.ProcessMedia{StreamURI: f.req.StreamURI})
	c.Assert(doc.Meta, qt.IsNil)

	election, err := testNewVocdoniClient(t).Election(got.UpstreamID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert(election.MetadataURL, qt.Equals, got.MetadataURL)
	c.Assert([]byte(election.MetadataHash), qt.DeepEquals, sum[:])
	c.Assert([]byte(election.OrganizationID), qt.DeepEquals, f.orgAddress.Bytes())
	// TODO(parent-process): once the parent is metadata-only it has no census, and the chain
	// reports each question election's parentProcessId.
	c.Assert(election.Census, qt.Not(qt.IsNil))
	c.Assert(election.Census.MaxCensusSize, qt.Equals, uint64(parentElectionMaxCensusSize))
}

// TestProcessParentElection checks that publishing a process creates one election per question
// plus its parent election, that the process reads expose the parent's id, metadata URL and hash,
// and that the relay refuses a vote on the parent, which is not a question.
func TestProcessParentElection(t *testing.T) {
	c := qt.New(t)
	f := setupParentProcess(t)

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(got.Questions, qt.HasLen, 2)
	assertParentElection(t, f, &got)

	public := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", f.pid)
	c.Assert(public.UpstreamID, qt.DeepEquals, got.UpstreamID)
	c.Assert(public.MetadataURL, qt.Equals, got.MetadataURL)
	c.Assert(public.MetadataHash, qt.DeepEquals, got.MetadataHash)

	t.Run("question documents carry only the question", func(t *testing.T) {
		for i, q := range got.Questions {
			comment := qt.Commentf("question %d", i)
			served, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(q.MetadataURL))
			qt.Assert(t, code, qt.Equals, http.StatusOK, comment)
			var doc dvoteapi.ElectionMetadata
			qt.Assert(t, json.Unmarshal(served, &doc), qt.IsNil, comment)
			qt.Assert(t, doc.Title, qt.DeepEquals, dvoteapi.LanguageString(f.req.Questions[i].Title), comment)
			qt.Assert(t, doc.Media, qt.Equals, dvoteapi.ProcessMedia{}, comment)
			qt.Assert(t, doc.Meta, qt.IsNil, comment)
			qt.Assert(t, doc.Questions, qt.HasLen, 1, comment)
			if i == 0 {
				qt.Assert(t, doc.Questions[0].Meta, qt.DeepEquals, map[string]any{"note": "first question"})
				qt.Assert(t, doc.Questions[0].Choices[0].Meta, qt.DeepEquals, map[string]any{
					"description": map[string]any{"default": "The yes option"}, "extra": "kept verbatim",
				})
				qt.Assert(t, doc.Questions[0].Choices[1].Meta, qt.IsNil)
			} else {
				qt.Assert(t, doc.Questions[0].Meta, qt.IsNil, comment)
			}
		}
	})

	t.Run("vote on the parent election is refused", func(t *testing.T) {
		voter := &ethereum.SignKeys{}
		qt.Assert(t, voter.Generate(), qt.IsNil)
		requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodPost, "",
			&apicommon.RelayVoteRequest{
				TxPayload: testSignVoteTx(t, voter, got.UpstreamID, nil, []byte("[\"1\"]"), nil),
			}, "vote")
	})

	t.Run("ending every question ends the parent election", func(t *testing.T) {
		job := enqueueAndPollJob(t, http.MethodPut, f.token,
			&apicommon.SetQuestionsStatusRequest{Status: db.QuestionStatusEnded}, "processes", f.pid, "questions", "status")
		qt.Assert(t, job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))
		waitForElectionStatus(t, got.UpstreamID, "ENDED", "RESULTS")
		for _, election := range f.elections {
			waitForElectionStatus(t, election, "ENDED", "RESULTS")
		}
	})
}

// TestProcessPublishResumesAfterParent puts a process in the state a publish leaves when the parent
// election was mined but the question step failed: the process is not published, so it is hidden
// from the public and the CSP refuses to authenticate voters against it. Publishing again keeps the
// parent, mints only the question elections, linked to it, and only then marks it published.
func TestProcessPublishResumesAfterParent(t *testing.T) {
	c := qt.New(t)
	f := setupParentProcess(t)
	first := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)

	oid, err := bson.ObjectIDFromHex(f.pid)
	c.Assert(err, qt.IsNil)
	database := testDB.DBClient.Database(testDBName)
	_, err = database.Collection("votingProcesses").UpdateOne(context.Background(),
		bson.M{"_id": oid}, bson.M{"$set": bson.M{"published": false}})
	c.Assert(err, qt.IsNil)
	_, err = database.Collection("processesQuestions").UpdateMany(context.Background(),
		bson.M{"processId": oid}, bson.M{"$unset": bson.M{
			"upstreamId": "", "parentUpstreamId": "", "metadataURL": "", "metadataHash": "", "status": "",
		}})
	c.Assert(err, qt.IsNil)

	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, "", nil, "processes", f.pid)
	authErr := postProcessAuth0AndExpectError(t, f.pid, &handlers.AuthRequest{Email: f.members[0].Email})
	c.Assert(authErr.Code, qt.Equals, errors.ErrUnauthorized.Code)

	job := enqueueAndPollJob(t, http.MethodPost, f.token, nil, "processes", f.pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", f.pid)
	c.Assert(got.Published, qt.IsTrue)
	c.Assert(got.UpstreamID, qt.DeepEquals, first.UpstreamID)
	c.Assert(got.MetadataHash, qt.DeepEquals, first.MetadataHash)
	c.Assert(got.Questions, qt.HasLen, len(f.elections))
	for i, q := range got.Questions {
		c.Assert(q.UpstreamID, qt.Not(qt.HasLen), 0, qt.Commentf("question %d", i))
		c.Assert(q.UpstreamID, qt.Not(qt.DeepEquals), f.elections[i], qt.Commentf("question %d", i))
	}
	assertParentElection(t, f, &got)
}
