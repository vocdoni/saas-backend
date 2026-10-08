package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
)

// editedMetadata rewrites every text of a metadata payload with values tagged by label, keeping its
// shape, so a PUT of the result changes every question's election metadata.
func editedMetadata(meta apicommon.VotingProcessMetadata, label string) apicommon.VotingProcessMetadata {
	meta.Title = db.MultiLangString{"default": label + " title"}
	meta.Description = db.MultiLangString{"default": label + " description"}
	meta.Header = "https://example.com/" + label + "/header.png"
	meta.StreamURI = "https://example.com/" + label + "/stream"
	questions := make([]apicommon.VotingProcessMetadataQuestion, len(meta.Questions))
	for i, q := range meta.Questions {
		choices := make([]apicommon.VotingProcessMetadataChoice, len(q.Choices))
		for j := range q.Choices {
			choices[j].Title = db.MultiLangString{"default": fmt.Sprintf("%s choice %d-%d", label, i, j)}
		}
		questions[i] = apicommon.VotingProcessMetadataQuestion{
			Title:       db.MultiLangString{"default": fmt.Sprintf("%s Q%d", label, i)},
			Description: db.MultiLangString{"default": fmt.Sprintf("%s description Q%d", label, i)},
			Choices:     choices,
		}
	}
	meta.Questions = questions
	return meta
}

// servedMetadata fetches the metadata document a metadataURL serves.
func servedMetadata(t *testing.T, metadataURL string) []byte {
	t.Helper()
	served, code := testRequest(t, http.MethodGet, "", nil, "storage", path.Base(metadataURL))
	qt.Assert(t, code, qt.Equals, http.StatusOK, qt.Commentf("metadata %s: %s", metadataURL, served))
	return served
}

// TestVotingProcessMetadataPublished edits the text of a published two-question process and checks
// that each election commits on chain to the new document, that the previous one stays served and
// in the election's metadata history, and that votes must attest the new hash from then on.
func TestVotingProcessMetadataPublished(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 2)

	before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(before.Questions, qt.HasLen, 2)

	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, "", nil, "processes", f.pid, "metadata")
	c.Assert(meta.Questions, qt.HasLen, 2)
	c.Assert(meta.Title, qt.DeepEquals, before.Title)
	edit := editedMetadata(meta, "Edited")

	job := enqueueAndPollJob(t, http.MethodPut, f.token, edit, "processes", f.pid, "metadata")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
	c.Assert(job.Type, qt.Equals, db.JobTypeSetProcessMetadata)
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(job.Result.Questions, qt.HasLen, 2)

	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(after.Title, qt.DeepEquals, edit.Title)
	c.Assert(after.Description, qt.DeepEquals, edit.Description)
	c.Assert(after.Header, qt.Equals, edit.Header)
	c.Assert(after.StreamURI, qt.Equals, edit.StreamURI)
	c.Assert(after.Questions, qt.HasLen, 2)
	for i, q := range after.Questions {
		comment := qt.Commentf("question %d", i)
		old := before.Questions[i]
		c.Assert(q.UpstreamID, qt.DeepEquals, old.UpstreamID, comment)
		c.Assert(q.Title, qt.DeepEquals, edit.Questions[i].Title, comment)
		c.Assert(q.Description, qt.DeepEquals, edit.Questions[i].Description, comment)
		c.Assert(q.Choices, qt.HasLen, len(old.Choices), comment)
		for j := range q.Choices {
			c.Assert(q.Choices[j].Title, qt.DeepEquals, edit.Questions[i].Choices[j].Title, comment)
			c.Assert(q.Choices[j].Value, qt.Equals, old.Choices[j].Value, comment)
		}

		// the job reports the version each question now commits to
		entry := job.Result.Questions[i]
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("question %d: %s", i, entry.Error))
		c.Assert(entry.QuestionID, qt.Equals, q.ID.Hex(), comment)
		c.Assert(entry.MetadataURL, qt.Equals, q.MetadataURL, comment)
		c.Assert(entry.MetadataHash, qt.DeepEquals, q.MetadataHash, comment)

		// a new document at a new URL, hashed byte for byte, carrying the new text
		c.Assert(q.MetadataURL, qt.Not(qt.Equals), old.MetadataURL, comment)
		served := servedMetadata(t, q.MetadataURL)
		want := sha256.Sum256(served)
		c.Assert([]byte(q.MetadataHash), qt.DeepEquals, want[:], comment)
		var doc dvoteapi.ElectionMetadata
		c.Assert(json.Unmarshal(served, &doc), qt.IsNil, comment)
		c.Assert(doc.Title["default"], qt.Equals, edit.Questions[i].Title["default"], comment)
		c.Assert(doc.Media.Header, qt.Equals, edit.Header, comment)
		c.Assert(doc.Questions, qt.HasLen, 1, comment)
		c.Assert(doc.Questions[0].Choices, qt.HasLen, len(old.Choices), comment)
		c.Assert(doc.Questions[0].Choices[0].Title["default"], qt.Equals,
			edit.Questions[i].Choices[0].Title["default"], comment)

		// the election commits on chain to exactly that document
		election, err := f.client.Election(q.UpstreamID.Bytes())
		c.Assert(err, qt.IsNil, comment)
		c.Assert(election.MetadataURL, qt.Equals, q.MetadataURL, comment)
		c.Assert([]byte(election.MetadataHash), qt.DeepEquals, want[:], comment)

		// the previous version stays served, unchanged, at its own URL
		oldServed := servedMetadata(t, old.MetadataURL)
		oldSum := sha256.Sum256(oldServed)
		c.Assert([]byte(old.MetadataHash), qt.DeepEquals, oldSum[:], comment)

		// and the chain keeps both versions in the election's metadata history
		history, err := f.client.ElectionMetadataHistory(q.UpstreamID.Bytes())
		c.Assert(err, qt.IsNil, comment)
		c.Assert(history.Versions, qt.HasLen, 2, comment)
		c.Assert(history.Versions[0].MetadataURL, qt.Equals, old.MetadataURL, comment)
		c.Assert([]byte(history.Versions[0].MetadataHash), qt.DeepEquals, []byte(old.MetadataHash), comment)
		c.Assert(history.Versions[1].MetadataURL, qt.Equals, q.MetadataURL, comment)
		c.Assert([]byte(history.Versions[1].MetadataHash), qt.DeepEquals, []byte(q.MetadataHash), comment)
	}

	// the metadata read reflects the edit
	got := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, "", nil, "processes", f.pid, "metadata")
	c.Assert(got, qt.DeepEquals, edit)

	// a vote signed against the previous hash is turned away; one attesting the new hash is cast
	// (the CSP signs once per voter and election, and the turned-away vote never reached the chain, so
	// both envelopes carry the same proof)
	processID := f.processIDs[0]
	proof := f.proofFor(t, processID)
	requestAndAssertError(errors.ErrVoteMetadataChanged, t, http.MethodPost, "",
		&apicommon.RelayVoteRequest{TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, processID,
			proof, []byte("[\"1\"]"), nil, before.Questions[0].MetadataHash)},
		"vote")
	nullifier := testRelayVoteRequest(t, f.voter, processID, proof, []byte("[\"1\"]"), nil)
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	// sending the same edit again changes nothing and needs no tx
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, f.token, edit, "processes", f.pid, "metadata")
	again := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	for i, q := range again.Questions {
		c.Assert(q.MetadataHash, qt.DeepEquals, after.Questions[i].MetadataHash, qt.Commentf("question %d", i))
	}
}

// publishMetadataTestProcess publishes a two-question process for a fresh organization, returning
// the admin token and the process id.
func publishMetadataTestProcess(t *testing.T) (token, pid string) {
	t.Helper()
	token = testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	pid, _ = publishCensusProcess(t, token, orgAddress, apicommon.CensusSpec{
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
		MemberIDs:   memberIDs(members),
	}, 2)
	return token, pid
}

// TestVotingProcessMetadataPublishedRejects covers the edits of a published process that are
// refused before anything reaches the chain, and the ones answered without a tx.
func TestVotingProcessMetadataPublishedRejects(t *testing.T) {
	c := qt.New(t)
	token, pid := publishMetadataTestProcess(t)
	published := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, token, nil, "processes", pid, "metadata")

	t.Run("structural changes", func(t *testing.T) {
		cases := []struct {
			name   string
			mutate func(*apicommon.VotingProcessMetadata)
		}{
			{"remove a question", func(m *apicommon.VotingProcessMetadata) {
				m.Questions = m.Questions[:len(m.Questions)-1]
			}},
			{"add a question", func(m *apicommon.VotingProcessMetadata) {
				m.Questions = append(m.Questions, m.Questions[len(m.Questions)-1])
			}},
			{"remove a choice", func(m *apicommon.VotingProcessMetadata) {
				m.Questions[0].Choices = m.Questions[0].Choices[:1]
			}},
			{"add a choice", func(m *apicommon.VotingProcessMetadata) {
				m.Questions[0].Choices = append(m.Questions[0].Choices, m.Questions[0].Choices[0])
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				edit := editedMetadata(meta, "Structural")
				tc.mutate(&edit)
				requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token, edit, "processes", pid, "metadata")
			})
		}
	})

	t.Run("not a manager", func(t *testing.T) {
		outsider := testCreateUser(t, "outsiderpassword123")
		requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, outsider,
			editedMetadata(meta, "Outsider"), "processes", pid, "metadata")
		requestAndAssertError(errors.ErrUnauthorized, t, http.MethodPut, "",
			editedMetadata(meta, "Anonymous"), "processes", pid, "metadata")
	})

	t.Run("election not editable", func(t *testing.T) {
		qid := published.Questions[1].ID
		c.Assert(testDB.SetQuestionStatus(qid, db.QuestionStatusEnded), qt.IsNil)
		defer func() { c.Assert(testDB.SetQuestionStatus(qid, published.Questions[1].Status), qt.IsNil) }()
		requestAndAssertError(errors.ErrMetadataNotEditable, t, http.MethodPut, token,
			editedMetadata(meta, "Ended"), "processes", pid, "metadata")
	})

	t.Run("no-op and process-only edits need no tx", func(t *testing.T) {
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, meta, "processes", pid, "metadata")

		titleOnly := meta
		titleOnly.Title = db.MultiLangString{"default": "Only the process title"}
		titleOnly.Description = db.MultiLangString{"default": "Only the process description"}
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, titleOnly, "processes", pid, "metadata")
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.Title, qt.DeepEquals, titleOnly.Title)
		c.Assert(got.Description, qt.DeepEquals, titleOnly.Description)
		for i, q := range got.Questions {
			c.Assert(q.MetadataURL, qt.Equals, published.Questions[i].MetadataURL, qt.Commentf("question %d", i))
		}
		meta = titleOnly
	})

	// none of the edits above touched the elections
	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	for i, q := range after.Questions {
		c.Assert(q.Title, qt.DeepEquals, published.Questions[i].Title, qt.Commentf("question %d", i))
		c.Assert(q.MetadataHash, qt.DeepEquals, published.Questions[i].MetadataHash, qt.Commentf("question %d", i))
	}

	t.Run("second edit while the first is pending", func(t *testing.T) {
		first := requestAndParseWithAssertCode[apicommon.EnqueuedResponse](http.StatusAccepted, t, http.MethodPut,
			token, editedMetadata(meta, "First"), "processes", pid, "metadata")
		requestAndAssertError(errors.ErrMetadataUpdateInProgress, t, http.MethodPut, token,
			editedMetadata(meta, "Second"), "processes", pid, "metadata")
		job := pollJob(t, first.JobID)
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))

		// a claim left behind by a crashed job blocks edits only until it goes stale
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		claimed, err := testDB.ClaimVotingProcessMetadataUpdate(oid)
		c.Assert(err, qt.IsNil)
		c.Assert(claimed, qt.IsTrue)
		requestAndAssertError(errors.ErrMetadataUpdateInProgress, t, http.MethodPut, token,
			editedMetadata(meta, "Third"), "processes", pid, "metadata")
		c.Assert(testDB.ClearVotingProcessMetadataUpdate(oid), qt.IsNil)
	})
}

// TestVotingProcessMetadataDraft edits the text of a draft, which is stored right away with no tx,
// and checks a draft's metadata stays hidden from the public.
func TestVotingProcessMetadataDraft(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)

	req := newVotingProcessRequest(orgAddress, memberIDs(members))
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID

	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodGet, "", nil, "processes", pid, "metadata")
	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, token, nil, "processes", pid, "metadata")
	c.Assert(meta.Questions, qt.HasLen, len(req.Questions))

	edit := editedMetadata(meta, "Draft")
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, edit, "processes", pid, "metadata")

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Title, qt.DeepEquals, edit.Title)
	c.Assert(got.Header, qt.Equals, edit.Header)
	c.Assert(got.Questions, qt.HasLen, len(req.Questions))
	for i, q := range got.Questions {
		c.Assert(q.Title, qt.DeepEquals, edit.Questions[i].Title, qt.Commentf("question %d", i))
		c.Assert(q.MetadataURL, qt.Equals, "", qt.Commentf("question %d", i))
		for j := range q.Choices {
			c.Assert(q.Choices[j].Title, qt.DeepEquals, edit.Questions[i].Choices[j].Title)
			c.Assert(q.Choices[j].Value, qt.Equals, req.Questions[i].Choices[j].Value)
		}
	}
	c.Assert(requestAndParse[apicommon.VotingProcessMetadata](
		t, http.MethodGet, token, nil, "processes", pid, "metadata"), qt.DeepEquals, edit)

	// the edit released the draft: it can still be fully edited (here, reshaped)
	upd := newVotingProcessRequest(orgAddress, memberIDs(members))
	upd.Questions = upd.Questions[:1]
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, upd, "processes", pid)

	// and a stale count is refused
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token, edit, "processes", pid, "metadata")
}
