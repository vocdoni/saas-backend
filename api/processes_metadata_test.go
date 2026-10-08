package api

import (
	"crypto/sha256"
	"encoding/hex"
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
// shape and its header, so a PUT of the result changes every election's metadata.
func editedMetadata(meta apicommon.VotingProcessMetadata, label string) apicommon.VotingProcessMetadata {
	meta.Title = db.MultiLangString{"default": label + " title"}
	meta.Description = db.MultiLangString{"default": label + " description"}
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

// assertElectionCommits checks that an election commits on chain to the document metadataURL
// serves, whose SHA-256 is metadataHash, and returns the decoded document.
func assertElectionCommits(t *testing.T, electionID internal.HexBytes, metadataURL string, metadataHash []byte,
) *dvoteapi.ElectionMetadata {
	t.Helper()
	c := qt.New(t)
	served := servedMetadata(t, metadataURL)
	sum := sha256.Sum256(served)
	c.Assert(metadataHash, qt.DeepEquals, sum[:])
	election, err := testNewVocdoniClient(t).Election(electionID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert(election.MetadataURL, qt.Equals, metadataURL)
	c.Assert([]byte(election.MetadataHash), qt.DeepEquals, sum[:])
	doc := &dvoteapi.ElectionMetadata{}
	c.Assert(json.Unmarshal(served, doc), qt.IsNil)
	return doc
}

// TestVotingProcessMetadataPublished edits the text of a published two-question process and checks
// that each election, the parent's included, commits on chain to its new document, that the
// previous one stays served and in the election's metadata history, and that votes must attest the
// new hash from then on.
func TestVotingProcessMetadataPublished(t *testing.T) {
	c := qt.New(t)
	f := setupRelayVoting(t, 2)

	before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(before.Questions, qt.HasLen, 2)
	c.Assert(before.UpstreamID, qt.Not(qt.HasLen), 0)

	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, "", nil, "processes", f.pid, "metadata")
	c.Assert(meta.Questions, qt.HasLen, 2)
	c.Assert(meta.Title, qt.DeepEquals, before.Title)
	edit := editedMetadata(meta, "Edited")
	header := testPNG(t)
	edit.Header = testUploadImage(t, f.token, header)

	job := enqueueAndPollJob(t, http.MethodPut, f.token, edit, "processes", f.pid, "metadata")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
	c.Assert(job.Type, qt.Equals, db.JobTypeSetProcessMetadata)
	c.Assert(job.Result, qt.Not(qt.IsNil))
	c.Assert(job.Result.Questions, qt.HasLen, 2)
	c.Assert(job.Result.Parent, qt.Not(qt.IsNil))

	after := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, f.token, nil, "processes", f.pid)
	c.Assert(after.Title, qt.DeepEquals, edit.Title)
	c.Assert(after.Description, qt.DeepEquals, edit.Description)
	c.Assert(after.Header, qt.Equals, edit.Header)
	c.Assert(after.StreamURI, qt.Equals, edit.StreamURI)

	// the parent election commits to a new document with the new process text and header
	parent := job.Result.Parent
	c.Assert(parent.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("parent: %s", parent.Error))
	c.Assert(parent.QuestionID, qt.Equals, "")
	c.Assert(parent.ProcessID, qt.DeepEquals, before.UpstreamID)
	c.Assert(after.UpstreamID, qt.DeepEquals, before.UpstreamID)
	c.Assert(after.MetadataURL, qt.Equals, parent.MetadataURL)
	c.Assert(after.MetadataHash, qt.DeepEquals, parent.MetadataHash)
	c.Assert(after.MetadataURL, qt.Not(qt.Equals), before.MetadataURL)
	parentDoc := assertElectionCommits(t, after.UpstreamID, after.MetadataURL, after.MetadataHash)
	c.Assert(parentDoc.Title, qt.DeepEquals, dvoteapi.LanguageString(edit.Title))
	c.Assert(parentDoc.Media, qt.Equals, dvoteapi.ProcessMedia{Header: edit.Header, StreamURI: edit.StreamURI})
	headerSum := sha256.Sum256(header)
	// the parent keeps listing the same question elections
	c.Assert(parentDoc.Meta, qt.DeepEquals, map[string]any{
		"mediaHashes":       map[string]any{edit.Header: hex.EncodeToString(headerSum[:])},
		"questionElections": questionElectionsOf(f.processIDs),
	})

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

		// a new document at a new URL, hashed byte for byte, carrying only the new question text
		c.Assert(q.MetadataURL, qt.Not(qt.Equals), old.MetadataURL, comment)
		doc := assertElectionCommits(t, q.UpstreamID, q.MetadataURL, q.MetadataHash)
		c.Assert(doc.Title["default"], qt.Equals, edit.Questions[i].Title["default"], comment)
		c.Assert(doc.Media, qt.Equals, dvoteapi.ProcessMedia{}, comment)
		c.Assert(doc.Questions, qt.HasLen, 1, comment)
		c.Assert(doc.Questions[0].Choices, qt.HasLen, len(old.Choices), comment)
		c.Assert(doc.Questions[0].Choices[0].Title["default"], qt.Equals,
			edit.Questions[i].Choices[0].Title["default"], comment)

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
	c.Assert(again.MetadataHash, qt.DeepEquals, after.MetadataHash)
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
// refused before anything reaches the chain, the ones answered without a tx, and which elections
// an edit sends a tx to.
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

	t.Run("unimportable image", func(t *testing.T) {
		srv, _ := testImageServer(t, testPNG(t))
		edit := meta
		edit.Header = srv.URL + "/missing.png"
		apiErr := requestAndParseWithAssertCode[errors.Error](http.StatusUnprocessableEntity, t,
			http.MethodPut, token, edit, "processes", pid, "metadata")
		c.Assert(apiErr.Code, qt.Equals, errors.ErrMediaUnavailable.Code)
		c.Assert(apiErr.Error(), qt.Contains, "header: "+edit.Header)
	})

	t.Run("no-op needs no tx", func(t *testing.T) {
		requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, meta, "processes", pid, "metadata")
	})

	// none of the edits above touched the elections
	unchanged := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(unchanged.MetadataHash, qt.DeepEquals, published.MetadataHash)
	for i, q := range unchanged.Questions {
		c.Assert(q.Title, qt.DeepEquals, published.Questions[i].Title, qt.Commentf("question %d", i))
		c.Assert(q.MetadataHash, qt.DeepEquals, published.Questions[i].MetadataHash, qt.Commentf("question %d", i))
	}

	t.Run("process-only edit sends one tx, to the parent", func(t *testing.T) {
		titleOnly := meta
		titleOnly.Title = db.MultiLangString{"default": "Only the process title"}
		titleOnly.Description = db.MultiLangString{"default": "Only the process description"}
		job := enqueueAndPollJob(t, http.MethodPut, token, titleOnly, "processes", pid, "metadata")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
		c.Assert(job.Result.Questions, qt.HasLen, 0)
		c.Assert(job.Result.Parent, qt.Not(qt.IsNil))
		c.Assert(job.Result.Parent.Status, qt.Equals, db.JobStatusCompleted)
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.Title, qt.DeepEquals, titleOnly.Title)
		c.Assert(got.Description, qt.DeepEquals, titleOnly.Description)
		doc := assertElectionCommits(t, got.UpstreamID, got.MetadataURL, got.MetadataHash)
		c.Assert(doc.Title, qt.DeepEquals, dvoteapi.LanguageString(titleOnly.Title))
		for i, q := range got.Questions {
			comment := qt.Commentf("question %d", i)
			c.Assert(q.MetadataURL, qt.Equals, published.Questions[i].MetadataURL, comment)
			c.Assert(q.MetadataHash, qt.DeepEquals, published.Questions[i].MetadataHash, comment)
		}
		meta = titleOnly
	})

	t.Run("mixed edit sends a tx to the parent and to each changed question", func(t *testing.T) {
		mixed := meta
		mixed.Title = db.MultiLangString{"default": "Mixed process title"}
		mixed.Questions = append([]apicommon.VotingProcessMetadataQuestion(nil), meta.Questions...)
		mixed.Questions[1].Title = db.MultiLangString{"default": "Mixed second question"}
		job := enqueueAndPollJob(t, http.MethodPut, token, mixed, "processes", pid, "metadata")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
		c.Assert(job.Result.Parent, qt.Not(qt.IsNil))
		c.Assert(job.Result.Questions, qt.HasLen, 1)
		c.Assert(job.Result.Questions[0].QuestionID, qt.Equals, published.Questions[1].ID.Hex())
		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.Questions[0].MetadataHash, qt.DeepEquals, published.Questions[0].MetadataHash)
		c.Assert(got.Questions[1].Title, qt.DeepEquals, mixed.Questions[1].Title)
		assertElectionCommits(t, got.Questions[1].UpstreamID, got.Questions[1].MetadataURL, got.Questions[1].MetadataHash)
		meta = mixed
	})

	t.Run("choice display info edit sends a tx only for its question", func(t *testing.T) {
		image := testPNG(t)
		srv, _ := testImageServer(t, image)
		before := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		withMeta := meta
		withMeta.Questions = append([]apicommon.VotingProcessMetadataQuestion(nil), meta.Questions...)
		choices := append([]apicommon.VotingProcessMetadataChoice(nil), meta.Questions[0].Choices...)
		choices[1].Meta = map[string]any{
			"description": map[string]any{"default": "The no option"},
			"image":       srv.URL + "/image.png",
		}
		withMeta.Questions[0].Choices = choices
		job := enqueueAndPollJob(t, http.MethodPut, token, withMeta, "processes", pid, "metadata")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))
		c.Assert(job.Result.Parent, qt.IsNil)
		c.Assert(job.Result.Questions, qt.HasLen, 1)
		c.Assert(job.Result.Questions[0].QuestionID, qt.Equals, published.Questions[0].ID.Hex())

		got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
		c.Assert(got.MetadataHash, qt.DeepEquals, before.MetadataHash)
		c.Assert(got.Questions[1].MetadataHash, qt.DeepEquals, before.Questions[1].MetadataHash)
		q := got.Questions[0]
		doc := assertElectionCommits(t, q.UpstreamID, q.MetadataURL, q.MetadataHash)
		// the external image was imported: the document points at the local copy and hashes it
		choiceMeta, ok := doc.Questions[0].Choices[1].Meta.(map[string]any)
		c.Assert(ok, qt.IsTrue)
		importedURL, ok := choiceMeta["image"].(string)
		c.Assert(ok, qt.IsTrue)
		c.Assert(isLocalURL(importedURL), qt.IsTrue, qt.Commentf("image %s", importedURL))
		c.Assert(storedObject(t, importedURL), qt.DeepEquals, image)
		c.Assert(choiceMeta["description"], qt.DeepEquals, map[string]any{"default": "The no option"})
		imageSum := sha256.Sum256(image)
		c.Assert(doc.Meta, qt.DeepEquals, map[string]any{"mediaHashes": map[string]any{
			importedURL: hex.EncodeToString(imageSum[:]),
		}})

		// the metadata read returns the display info, pointing at the copy
		read := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, token, nil, "processes", pid, "metadata")
		c.Assert(read.Questions[0].Choices[1].Meta, qt.DeepEquals, map[string]any{
			"description": map[string]any{"default": "The no option"},
			"image":       importedURL,
		})
		c.Assert(read.Questions[0].Choices[0].Meta, qt.IsNil)
		meta = read
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

// storedProcessMetadata returns the process update that writes vp's stored version back.
func storedProcessMetadata(vp *db.VotingProcess) *db.ProcessMetadataUpdate {
	return &db.ProcessMetadataUpdate{
		Text: db.ProcessText{
			Title: vp.Title, Description: vp.Description, Header: vp.Header, StreamURI: vp.StreamURI,
		},
		MetadataURL:  vp.MetadataURL,
		MetadataHash: vp.MetadataHash,
	}
}

// pendingJobResult returns a job's entries with every status reset to pending, as the job row reads
// while its txs are unconfirmed.
func pendingJobResult(t *testing.T, jobID string) *db.JobResult {
	t.Helper()
	job, err := testDB.Job(jobID)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, job.Result, qt.Not(qt.IsNil))
	result := &db.JobResult{Questions: make([]db.ElectionMetadataJobResult, len(job.Result.Questions))}
	for i, entry := range job.Result.Questions {
		entry.Status, entry.Error = db.JobStatusPending, ""
		result.Questions[i] = entry
	}
	if job.Result.Parent != nil {
		parent := *job.Result.Parent
		parent.Status, parent.Error = db.JobStatusPending, ""
		result.Parent = &parent
	}
	return result
}

// TestVotingProcessMetadataLateMined covers a metadata edit whose txs mined after the edit job gave
// up waiting for them: the store still holds the previous version with the edit pending, the job is
// pending and the claim held, while the chain commits the new version. Votes attesting the pending
// hash are relayed and counted, other hashes are rejected, and a read of the process settles the
// edit: questions, parent, job and claim.
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
	c.Assert(testDB.SetVotingProcessMetadata(oid, storedProcessMetadata(vpBefore)), qt.IsNil)
	c.Assert(testDB.SetVotingProcessPendingMetadata(oid, storedProcessMetadata(vpEdited).Pending(enq.JobID)), qt.IsNil)
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
	c.Assert(got.StreamURI, qt.Equals, edit.StreamURI)
	c.Assert(got.MetadataHash, qt.DeepEquals, vpEdited.MetadataHash)
	c.Assert(got.Questions, qt.HasLen, 2)
	for i, q := range got.Questions {
		comment := qt.Commentf("question %d", i)
		c.Assert(q.Title, qt.DeepEquals, edit.Questions[i].Title, comment)
		c.Assert(q.MetadataHash, qt.DeepEquals, edited[i].MetadataHash, comment)
	}

	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.PendingMetadata, qt.IsNil)
	c.Assert(vp.MetadataUpdating.IsZero(), qt.IsTrue)
	c.Assert(vp.Title, qt.DeepEquals, edit.Title)
	c.Assert(vp.Description, qt.DeepEquals, edit.Description)
	c.Assert(vp.MetadataURL, qt.Equals, vpEdited.MetadataURL)
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
	c.Assert(settled.Result.Parent, qt.Not(qt.IsNil))
	c.Assert(settled.Result.Parent.Status, qt.Equals, db.JobStatusCompleted)
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
	_, questions, err := testDB.ProcessWithQuestions(oid)
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
		&db.JobResult{Questions: []db.ElectionMetadataJobResult{{
			QuestionID: q.ID.Hex(), ProcessID: q.UpstreamID, MetadataURL: never.MetadataURL,
			MetadataHash: never.MetadataHash, Status: db.JobStatusPending,
		}}}), qt.IsNil)
	claimed, err := testDB.ClaimVotingProcessMetadataUpdate(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(claimed, qt.IsTrue)
	pending := never.Pending(jobID)
	c.Assert(testDB.SetQuestionPendingMetadata(q.ID, pending), qt.IsNil)

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
	c.Assert(got.Questions[0].Title, qt.DeepEquals, q.Title)
	c.Assert(got.Questions[0].MetadataHash, qt.DeepEquals, q.MetadataHash)

	vp, questions, err := testDB.ProcessWithQuestions(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(questions[0].PendingMetadata, qt.IsNil)
	c.Assert(questions[0].MetadataHash, qt.DeepEquals, q.MetadataHash)
	c.Assert(vp.PendingMetadata, qt.IsNil)
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
		t, http.MethodPost, token, req, processesCreateEndpoint,
	).ProcessID

	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodGet, "", nil, "processes", pid, "metadata")
	meta := requestAndParse[apicommon.VotingProcessMetadata](t, http.MethodGet, token, nil, "processes", pid, "metadata")
	c.Assert(meta.Questions, qt.HasLen, len(req.Questions))

	edit := editedMetadata(meta, "Draft")
	edit.Questions[0].Choices[0].Meta = map[string]any{"description": map[string]any{"default": "Draft yes"}}
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, edit, "processes", pid, "metadata")

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Title, qt.DeepEquals, edit.Title)
	c.Assert(got.StreamURI, qt.Equals, edit.StreamURI)
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
		t, http.MethodGet, token, nil, "processes", pid, "metadata",
	), qt.DeepEquals, edit)

	// the edit released the draft: it can still be fully edited (here, reshaped)
	upd := newVotingProcessRequest(orgAddress, memberIDs(members))
	upd.Questions = upd.Questions[:1]
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, upd, "processes", pid)

	// and a stale count is refused
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPut, token, edit, "processes", pid, "metadata")
}
