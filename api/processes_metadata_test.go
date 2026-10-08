package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
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

	t.Run("no-op needs no tx", func(t *testing.T) {
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, meta, "processes", pid, "metadata")
	})

	// none of the edits above touched the elections
	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	for i, q := range after.Questions {
		c.Assert(q.Title, qt.DeepEquals, published.Questions[i].Title, qt.Commentf("question %d", i))
		c.Assert(q.MetadataHash, qt.DeepEquals, published.Questions[i].MetadataHash, qt.Commentf("question %d", i))
	}

	t.Run("process-only edit updates every election", func(t *testing.T) {
		// every question's document carries the process title and description as meta.process
		titleOnly := meta
		titleOnly.Title = db.MultiLangString{"default": "Only the process title"}
		titleOnly.Description = db.MultiLangString{"default": "Only the process description"}
		job := enqueueAndPollJob(t, http.MethodPut, token, titleOnly, "processes", pid, "metadata")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
		c.Assert(job.Result.Questions, qt.HasLen, len(published.Questions))
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.Title, qt.DeepEquals, titleOnly.Title)
		c.Assert(got.Description, qt.DeepEquals, titleOnly.Description)
		for i, q := range got.Questions {
			comment := qt.Commentf("question %d", i)
			c.Assert(q.Title, qt.DeepEquals, published.Questions[i].Title, comment)
			c.Assert(q.MetadataURL, qt.Not(qt.Equals), published.Questions[i].MetadataURL, comment)
			var doc dvoteapi.ElectionMetadata
			c.Assert(json.Unmarshal(servedMetadata(t, q.MetadataURL), &doc), qt.IsNil, comment)
			docMeta, ok := doc.Meta.(map[string]any)
			c.Assert(ok, qt.IsTrue, comment)
			c.Assert(docMeta["process"], qt.DeepEquals, metaProcessOf(titleOnly.Title, titleOnly.Description), comment)
		}
		meta = titleOnly
	})

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

// storedQuestionText returns the text update that writes q's stored version back.
func storedQuestionText(q *db.VotingProcessQuestion) *db.QuestionTextUpdate {
	titles := make([]db.MultiLangString, len(q.Choices))
	for j := range q.Choices {
		titles[j] = q.Choices[j].Title
	}
	return &db.QuestionTextUpdate{
		ID:           q.ID,
		Title:        q.Title,
		Description:  q.Description,
		ChoiceTitles: titles,
		MetadataURL:  q.MetadataURL,
		MetadataHash: q.MetadataHash,
	}
}

// pendingJobResult returns a job's question entries with every status reset to pending, as the job
// row reads while its txs are unconfirmed.
func pendingJobResult(t *testing.T, jobID string) *db.JobResult {
	t.Helper()
	job, err := testDB.Job(jobID)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, job.Result, qt.Not(qt.IsNil))
	result := &db.JobResult{Questions: make([]db.QuestionMetadataJobResult, len(job.Result.Questions))}
	for i, entry := range job.Result.Questions {
		entry.Status, entry.Error = db.JobStatusPending, ""
		result.Questions[i] = entry
	}
	return result
}

// TestVotingProcessMetadataLateMined covers a metadata edit whose txs mined after the edit job gave
// up waiting for them: the store still holds the previous version with the edit pending, the job is
// pending and the claim held, while the chain commits the new version. Votes attesting the pending
// hash are relayed and counted, other hashes are rejected, and a read of
// the process settles the edit: questions, process text, job and claim.
func TestVotingProcessMetadataLateMined(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 2)
	oid, err := bson.ObjectIDFromHex(f.pid)
	c.Assert(err, qt.IsNil)

	vpBefore, before, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(before, qt.HasLen, 2)
	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, "", nil, "processes", f.pid, "metadata")
	edit := editedMetadata(meta, "Late")
	enq := requestAndParseWithAssertCode[apicommon.EnqueuedResponse](http.StatusAccepted, t, http.MethodPut,
		f.token, edit, "processes", f.pid, "metadata")
	job := pollJob(t, enq.JobID)
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
	vpEdited, edited, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)

	// roll the store back to how a job that timed out waiting for its txs leaves it
	for i := range before {
		c.Assert(testDB.SetQuestionText(storedQuestionText(&before[i])), qt.IsNil)
		c.Assert(testDB.SetQuestionPendingMetadata(edited[i].ID,
			storedQuestionText(&edited[i]).Pending(enq.JobID)), qt.IsNil)
	}
	c.Assert(testDB.SetVotingProcessText(oid, &db.ProcessText{
		Title: vpBefore.Title, Description: vpBefore.Description, Header: vpBefore.Header, StreamURI: vpBefore.StreamURI,
	}), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPendingText(oid, &db.ProcessText{
		Title: vpEdited.Title, Description: vpEdited.Description, Header: vpEdited.Header, StreamURI: vpEdited.StreamURI,
	}), qt.IsNil)
	c.Assert(testDB.SetJobStatus(enq.JobID, db.JobStatusPending, pendingJobResult(t, enq.JobID), ""), qt.IsNil)
	claimed, err := testDB.ClaimVotingProcessMetadataUpdate(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)

	// a vote attesting the pending hash is relayed (the chain commits it, so it is counted); one
	// attesting neither the stored nor the pending hash is rejected up front
	processID := f.processIDs[0]
	stx := testSignVoteTxWithMetadataHash(t, f.voter, processID, f.proofFor(t, processID), []byte("[\"1\"]"), nil,
		edited[0].MetadataHash)
	voteJob := enqueueAndPollJob(t, http.MethodPost, "", &apicommon.RelayVoteRequest{TxPayload: stx}, "vote")
	c.Assert(voteJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", voteJob.Errors))
	requestAndAssertError(errors.ErrVoteMetadataChanged, t, http.MethodPost, "",
		&apicommon.RelayVoteRequest{TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, f.processIDs[1], nil,
			[]byte("[\"1\"]"), nil, internal.RandomBytes(sha256.Size))}, "vote")
	// the relay does not settle anything
	stillPending, err := testDB.Question(edited[0].ID)
	c.Assert(err, qt.IsNil)
	c.Assert(stillPending.PendingMetadata, qt.Not(qt.IsNil))

	// a read of the process settles the edit and serves it
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", f.pid)
	c.Assert(got.Title, qt.DeepEquals, edit.Title)
	c.Assert(got.Header, qt.Equals, edit.Header)
	c.Assert(got.Questions, qt.HasLen, 2)
	for i, q := range got.Questions {
		comment := qt.Commentf("question %d", i)
		c.Assert(q.Title, qt.DeepEquals, edit.Questions[i].Title, comment)
		c.Assert(q.MetadataHash, qt.DeepEquals, edited[i].MetadataHash, comment)
	}

	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PendingText, qt.IsNil)
	c.Assert(vp.MetadataUpdating.IsZero(), qt.IsTrue)
	c.Assert(vp.Title, qt.DeepEquals, edit.Title)
	c.Assert(vp.Description, qt.DeepEquals, edit.Description)
	for i, q := range questions {
		comment := qt.Commentf("question %d", i)
		c.Assert(q.PendingMetadata, qt.IsNil, comment)
		c.Assert(q.MetadataURL, qt.Equals, edited[i].MetadataURL, comment)
		c.Assert(q.MetadataHash, qt.DeepEquals, edited[i].MetadataHash, comment)
		for j := range q.Choices {
			c.Assert(q.Choices[j].Title, qt.DeepEquals, edit.Questions[i].Choices[j].Title, comment)
		}
	}
	settled, err := testDB.Job(enq.JobID)
	c.Assert(err, qt.IsNil)
	c.Assert(settled.Status, qt.Equals, db.JobStatusCompleted)
	for i, entry := range settled.Result.Questions {
		c.Assert(entry.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("question %d", i))
	}
}

// TestVotingProcessMetadataDeadEdit covers a metadata edit whose tx never lands: while it is not
// final its hash is relayed (and the chain rejects it) and a second edit is refused; once its
// mempool TTL has passed a read settles it as failed, the stored version stays the one voted on,
// and edits are allowed again.
func TestVotingProcessMetadataDeadEdit(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 1)
	oid, err := bson.ObjectIDFromHex(f.pid)
	c.Assert(err, qt.IsNil)
	processID := f.processIDs[0]
	vpBefore, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	q := questions[0]

	// an edit whose tx was submitted but will never mine
	jobID, err := apicommon.NewJobID()
	c.Assert(err, qt.IsNil)
	never := storedQuestionText(&q)
	never.Title = db.MultiLangString{"default": "Never on chain"}
	never.MetadataURL = "https://example.invalid/never.json"
	never.MetadataHash = internal.RandomBytes(sha256.Size)
	c.Assert(testDB.CreateTxJobWithResult(jobID, db.JobTypeSetProcessMetadata, f.orgAddress,
		&db.JobResult{Questions: []db.QuestionMetadataJobResult{{
			QuestionID: q.ID.Hex(), ProcessID: q.UpstreamID, MetadataURL: never.MetadataURL,
			MetadataHash: never.MetadataHash, Status: db.JobStatusPending,
		}}}), qt.IsNil)
	claimed, err := testDB.ClaimVotingProcessMetadataUpdate(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	pending := never.Pending(jobID)
	c.Assert(testDB.SetQuestionPendingMetadata(q.ID, pending), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPendingText(oid, &db.ProcessText{Title: db.MultiLangString{"default": "Never"}}),
		qt.IsNil)

	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, "", nil, "processes", f.pid, "metadata")
	requestAndAssertError(errors.ErrMetadataUpdateInProgress, t, http.MethodPut, f.token,
		editedMetadata(meta, "Blocked"), "processes", f.pid, "metadata")

	// the pending hash passes the relay check; the chain, which never committed it, rejects the vote
	proof := f.proofFor(t, processID)
	rejected := enqueueAndPollJob(t, http.MethodPost, "", &apicommon.RelayVoteRequest{
		TxPayload: testSignVoteTxWithMetadataHash(t, f.voter, processID, proof, []byte("[\"1\"]"), nil, never.MetadataHash),
	}, "vote")
	c.Assert(rejected.Status, qt.Equals, db.JobStatusFailed)

	// past the mempool TTL the edit is final: a read settles it as failed
	pending.Since = time.Now().Add(-db.PendingMetadataFinalAfter - time.Minute)
	c.Assert(testDB.SetQuestionPendingMetadata(q.ID, pending), qt.IsNil)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", f.pid)
	c.Assert(got.Title, qt.DeepEquals, vpBefore.Title)
	c.Assert(got.Questions[0].Title, qt.DeepEquals, q.Title)
	c.Assert(got.Questions[0].MetadataHash, qt.DeepEquals, q.MetadataHash)

	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(questions[0].PendingMetadata, qt.IsNil)
	c.Assert(questions[0].MetadataHash, qt.DeepEquals, q.MetadataHash)
	c.Assert(vp.PendingText, qt.IsNil)
	c.Assert(vp.MetadataUpdating.IsZero(), qt.IsTrue)
	settled, err := testDB.Job(jobID)
	c.Assert(err, qt.IsNil)
	c.Assert(settled.Status, qt.Equals, db.JobStatusFailed)
	c.Assert(settled.Result.Questions, qt.HasLen, 1)
	c.Assert(settled.Result.Questions[0].Status, qt.Equals, db.JobStatusFailed)

	// the stored version is still the one voted on
	nullifier := testRelayVoteRequest(t, f.voter, processID, proof, []byte("[\"1\"]"), nil)
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	// and edits are allowed again
	job := enqueueAndPollJob(t, http.MethodPut, f.token, editedMetadata(meta, "After"), "processes", f.pid, "metadata")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
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
