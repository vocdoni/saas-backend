package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
)

// explorerElectionID is the on-chain election of the record that motivated the legacy projection:
// minted outside the draft flow, registered into a process bundle, with no row of its own.
const explorerElectionID = "6b342d99f2187f3debbb14afcde7f41407f6cf87d4e7667f787c030000000000"

// legacyTestElection builds an election read as the node returns it, with one result row per
// question and the metadata inlined as the untyped document the projection has to decode. maxCount
// is the ballot's field count — one per result row — and describes the election itself, not its
// tally, which is why the projected question type does not change when results are published.
func legacyTestElection(results [][]uint64, questions []map[string]any) *dvoteapi.Election {
	rows := make([][]*types.BigInt, 0, len(results))
	for _, row := range results {
		values := make([]*types.BigInt, 0, len(row))
		for _, v := range row {
			values = append(values, new(types.BigInt).SetUint64(v))
		}
		rows = append(rows, values)
	}
	return &dvoteapi.Election{
		ElectionSummary: dvoteapi.ElectionSummary{
			ElectionID:   types.HexStringToHexBytes(explorerElectionID),
			Status:       "RESULTS",
			VoteCount:    8,
			FinalResults: true,
			Results:      rows,
		},
		Census:   &dvoteapi.ElectionCensus{MaxCensusSize: 13},
		VoteMode: dvoteapi.VoteMode{EnvelopeType: &models.EnvelopeType{EncryptedVotes: true}},
		TallyMode: dvoteapi.TallyMode{
			ProcessVoteOptions: &models.ProcessVoteOptions{MaxCount: uint32(len(results)), MaxValue: 1},
		},
		Metadata: map[string]any{
			"title":       map[string]any{"default": "Assemblea Straordinaria"},
			"description": map[string]any{"default": "convocazione"},
			"media":       map[string]any{"header": "https://example.org/header.png"},
			"questions":   questions,
		},
	}
}

// legacyTestQuestion is one metadata question with the two choices of the real record.
func legacyTestQuestion(title string) map[string]any {
	return map[string]any{
		"title": map[string]any{"default": title},
		"choices": []any{
			map[string]any{"title": map[string]any{"default": "Sì"}, "value": 0},
			map[string]any{"title": map[string]any{"default": "No"}, "value": 1},
		},
	}
}

// TestLegacyProjectionExplorerElection checks the record that motivated the projection: one legacy
// election holding two ballot questions, each getting its own result row.
func TestLegacyProjectionExplorerElection(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}, {8, 0}},
		[]map[string]any{legacyTestQuestion("Statuto"), legacyTestQuestion("Consiglio Direttivo")})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	c.Assert(content.title, qt.DeepEquals, db.MultiLangString{"default": "Assemblea Straordinaria"})
	c.Assert(content.description, qt.DeepEquals, db.MultiLangString{"default": "convocazione"})
	c.Assert(content.header, qt.Equals, "https://example.org/header.png")
	c.Assert(content.questions, qt.HasLen, 2)

	processID := bson.NewObjectID()
	questions := legacyElectionQuestions(processID, election, content.questions)
	c.Assert(questions, qt.HasLen, 2)
	for i, q := range questions {
		c.Assert(q.Order, qt.Equals, i)
		c.Assert(q.UpstreamID.String(), qt.Equals, explorerElectionID)
		c.Assert(q.Status, qt.Equals, "RESULTS")
		c.Assert(q.ProcessID, qt.Equals, processID)
		// one ballot field per question: a single-choice ballot, stated from the election's own
		// tally mode so it does not appear only once the tally is published.
		c.Assert(q.Type, qt.Equals, "singlechoice")
		c.Assert(q.SecretUntilTheEnd, qt.IsTrue)
		c.Assert(q.Choices, qt.HasLen, 2)
		// ballotProtocol/typeSetup stay unset: the election's tally mode describes the whole ballot.
		c.Assert(q.BallotProtocol, qt.IsNil)
		c.Assert(q.Results, qt.Not(qt.IsNil))
		c.Assert(q.Results.VoteCount, qt.Equals, uint64(8))
		c.Assert(q.Results.MaxVoters, qt.Equals, uint64(13))
		c.Assert(q.Results.FinalResults, qt.IsTrue)
		// one row per question: question i owns row i, not the whole matrix.
		c.Assert(q.Results.Results, qt.DeepEquals, [][]string{{"8", "0"}})
	}
	c.Assert(questions[0].Title, qt.DeepEquals, db.MultiLangString{"default": "Statuto"})
	c.Assert(questions[1].Title, qt.DeepEquals, db.MultiLangString{"default": "Consiglio Direttivo"})
	c.Assert(questions[0].Choices[1].Value, qt.Equals, uint32(1))
	// the questions of one legacy election share its id, so their own ids must not collide — a zero
	// (or repeated) id would make them indistinguishable to a client keying on it.
	c.Assert(questions[0].ID, qt.Not(qt.Equals), questions[1].ID)
	c.Assert(questions[0].ID.IsZero(), qt.IsFalse)
	// stable across reads: the same election and order derive the same id.
	c.Assert(legacyElectionQuestions(processID, election, content.questions)[0].ID, qt.Equals, questions[0].ID)
}

// TestLegacyProjectionSingleQuestion checks that a single question owns the whole result matrix,
// however many ballot fields it has (a multichoice or ranked question).
func TestLegacyProjectionSingleQuestion(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{3, 1}, {2, 2}, {0, 4}},
		[]map[string]any{legacyTestQuestion("Statuto")})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content.questions)
	c.Assert(questions, qt.HasLen, 1)
	c.Assert(questions[0].Results.Results, qt.DeepEquals, [][]string{{"3", "1"}, {"2", "2"}, {"0", "4"}})
	// three ballot fields behind one question: not a single-choice ballot, and not statable.
	c.Assert(questions[0].Type, qt.Equals, "")
}

// TestLegacyProjectionUnmappableResults checks that a tally whose rows do not map onto the questions
// still reports the counts but omits the matrix, rather than splitting it wrongly.
func TestLegacyProjectionUnmappableResults(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}, {8, 0}, {8, 0}},
		[]map[string]any{legacyTestQuestion("Statuto"), legacyTestQuestion("Consiglio Direttivo")})
	// three ballot fields over two questions: the tally does not map, and neither does the type.
	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content.questions)
	c.Assert(questions, qt.HasLen, 2)
	for _, q := range questions {
		c.Assert(q.Results.Results, qt.IsNil)
		c.Assert(q.Results.VoteCount, qt.Equals, uint64(8))
		c.Assert(q.Type, qt.Equals, "")
	}
}

// TestLegacyProjectionStoredParams checks that a legacy row which kept its ElectionParams is
// projected from those, so its content needs no on-chain metadata at all.
func TestLegacyProjectionStoredParams(t *testing.T) {
	c := qt.New(t)
	// no Metadata on the election: whatever the projection reports has to come from the params.
	election := &dvoteapi.Election{
		ElectionSummary: dvoteapi.ElectionSummary{
			ElectionID: types.HexStringToHexBytes(explorerElectionID),
			Status:     "ENDED",
			VoteCount:  2,
		},
	}
	params := &db.ElectionParams{
		Title:     db.MultiLangString{"default": "stored title"},
		StreamURI: "https://example.org/stream",
		Questions: []db.Question{{
			Title:   db.MultiLangString{"default": "stored question"},
			Choices: []db.Choice{{Title: db.MultiLangString{"default": "Yes"}, Value: 0}},
		}},
	}

	content := testAPI.legacyContentOf(context.Background(), election, params)
	c.Assert(content.title, qt.DeepEquals, db.MultiLangString{"default": "stored title"})
	c.Assert(content.streamURI, qt.Equals, "https://example.org/stream")

	questions := legacyElectionQuestions(bson.NewObjectID(), election, content.questions)
	c.Assert(questions, qt.HasLen, 1)
	c.Assert(questions[0].Title, qt.DeepEquals, db.MultiLangString{"default": "stored question"})
	c.Assert(questions[0].Status, qt.Equals, "ENDED")
	c.Assert(questions[0].Results.VoteCount, qt.Equals, uint64(2))
	// no census on the election read: MaxVoters is simply unknown, not invented.
	c.Assert(questions[0].Results.MaxVoters, qt.Equals, uint64(0))
}

// TestLegacyProjectionExternalMetadata checks the bundle-only case the projection exists for: the
// node inlines the metadata document only for ipfs:// references, so an election pointing at an
// http(s) document must still resolve its questions — otherwise the record projects to nothing and
// disappears from /processes exactly as it does today.
func TestLegacyProjectionExternalMetadata(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}}, []map[string]any{legacyTestQuestion("Statuto")})
	doc, err := json.Marshal(election.Metadata)
	c.Assert(err, qt.IsNil)
	election.Metadata = nil // nothing inlined: the reference is all the projection has

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(doc)
	}))
	defer server.Close()
	election.MetadataURL = server.URL + "/metadata.json"

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	c.Assert(content.title, qt.DeepEquals, db.MultiLangString{"default": "Assemblea Straordinaria"})
	c.Assert(content.questions, qt.HasLen, 1)
	c.Assert(content.questions[0].Title, qt.DeepEquals, db.MultiLangString{"default": "Statuto"})

	// an unreachable reference resolves to no content rather than to a wrong one.
	election.MetadataURL = server.URL + "/missing.json"
	server.Close()
	c.Assert(testAPI.legacyContentOf(context.Background(), election, nil).questions, qt.HasLen, 0)
}

// TestLegacyProcessIDShape checks the 404-vs-400 boundary of GET /processes/{processId}: both id
// forms are well-formed even when nothing owns them, anything else is malformed.
func TestLegacyProcessIDShape(t *testing.T) {
	c := qt.New(t)
	c.Assert(isVotingProcessIDShape("6a56717fcd6cba70b16f4568"), qt.IsTrue)
	c.Assert(isVotingProcessIDShape(explorerElectionID), qt.IsTrue)
	c.Assert(isVotingProcessIDShape("not-hex"), qt.IsFalse)
	// hex, but neither an ObjectID nor an election id.
	c.Assert(isVotingProcessIDShape("deadbeef"), qt.IsFalse)
}

// TestLegacyProcessesProjection exercises the read-only legacy projection end to end against the
// in-process chain: a legacy db.Process row is listed and readable through /processes — by its own id
// and by its on-chain election id — with its content from the stored ElectionParams and its live
// state from the chain, deduped against a bundle registering the same election, and unwritable.
//
// The election is minted by publishing a throwaway /processes process and then deleting its stored
// rows: that leaves a real on-chain election the new format no longer owns — the shape of the records
// this projection exists for. A bundle-only election, whose content has to be decoded from the
// on-chain metadata document, is covered by the projection unit tests above: the test chain does not
// serve the metadata URL (it points back at this in-process server), so it cannot be exercised here.
func TestLegacyProcessesProjection(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "legacyprojection123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)
	ids := memberIDs(members)

	// a new-format process, kept as-is: it must stay listed and must not be flagged legacy.
	kept := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint)

	// a second process, published and then unstored, to obtain two orphaned on-chain elections.
	orphanReq := newVotingProcessRequest(orgAddress, ids)
	orphanReq.StartDate = ""
	orphan := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, orphanReq, processesCreateEndpoint)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", orphan.ProcessID, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job errors: %s", job.Errors))

	published := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", orphan.ProcessID)
	c.Assert(published.Questions, qt.HasLen, 2)
	elections := make([]internal.HexBytes, 0, 2)
	for _, q := range published.Questions {
		c.Assert(q.UpstreamID, qt.Not(qt.HasLen), 0)
		elections = append(elections, q.UpstreamID)
	}
	orphanOID, err := bson.ObjectIDFromHex(orphan.ProcessID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.DeleteVotingProcess(orphanOID), qt.IsNil)

	// a census for the legacy records to point at (both legacy shapes embed a full published census).
	var censusRoot internal.HexBytes
	c.Assert(censusRoot.ParseString("0xabcde"), qt.IsNil)
	censusID, err := testDB.SetCensus(&db.Census{
		OrgAddress: orgAddress,
		Type:       db.CensusTypeMail,
		Size:       7,
		AuthFields: db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		Published:  db.PublishedCensus{Root: censusRoot, URI: "ipfs://legacy-census"},
	})
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(censusID)
	c.Assert(err, qt.IsNil)

	// legacy shape 1: a db.Process row carrying the election parameters.
	rowOID, err := testDB.SetProcess(&db.Process{
		OrgAddress: orgAddress,
		Address:    elections[0],
		Census:     *census,
		ElectionParams: &db.ElectionParams{
			Title: db.MultiLangString{"default": "legacy row"},
			Questions: []db.Question{{
				Title:   db.MultiLangString{"default": "row question"},
				Choices: []db.Choice{{Title: db.MultiLangString{"default": "Yes"}, Value: 0}},
			}},
		},
	})
	c.Assert(err, qt.IsNil)

	// a bundle registering the same election is not a second record: the row wins, because it
	// carries the election parameters and needs no chain round-trip for its content.
	bundleID, err := testDB.SetProcessBundle(&db.ProcessesBundle{
		OrgAddress: orgAddress,
		Census:     *census,
		Processes:  []internal.HexBytes{elections[0]},
	})
	c.Assert(err, qt.IsNil)

	// the list now holds the kept process plus the legacy record, and counts both.
	list := requestAndParse[apicommon.VotingProcessListResponse](
		t, http.MethodGet, token, nil, "processes?orgAddress="+orgAddress.String())
	c.Assert(list.Pagination.TotalItems, qt.Equals, int64(2))
	c.Assert(list.Processes, qt.HasLen, 2)
	byID := map[string]apicommon.VotingProcessResponse{}
	for _, p := range list.Processes {
		byID[p.ID] = p
	}
	c.Assert(byID[kept.ProcessID].Legacy, qt.IsFalse)
	legacy, ok := byID[rowOID.Hex()]
	c.Assert(ok, qt.IsTrue)
	c.Assert(legacy.Legacy, qt.IsTrue)
	// deduped: the bundle registers an election the row already owns, so it is not listed itself.
	_, duplicated := byID[bundleID.String()]
	c.Assert(duplicated, qt.IsFalse)
	// and not readable under the bundle id either: the record the list attributes to the row must
	// not answer a second time under another id.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, token, nil, "processes", bundleID.String())

	// content from the stored params, live state from the chain.
	c.Assert(legacy.Title, qt.DeepEquals, db.MultiLangString{"default": "legacy row"})
	c.Assert(legacy.Questions, qt.HasLen, 1)
	c.Assert(legacy.Questions[0].UpstreamID.String(), qt.Equals, elections[0].String())
	c.Assert(legacy.Questions[0].Status, qt.Not(qt.Equals), "")
	c.Assert(legacy.Questions[0].Results, qt.Not(qt.IsNil))
	c.Assert(legacy.Questions[0].Results.VoteCount, qt.Equals, uint64(0))
	c.Assert(legacy.Census.Size, qt.Equals, int64(7))
	// the question carries the ids a client keys on: its own, and the process it belongs to.
	c.Assert(legacy.Questions[0].ID.IsZero(), qt.IsFalse)
	c.Assert(legacy.Questions[0].ProcessID, qt.Equals, rowOID)

	// readable by its own id and by the on-chain election id, projecting the same record.
	byRowID := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", rowOID.Hex())
	byElectionID := requestAndParse[apicommon.VotingProcessResponse](
		t, http.MethodGet, token, nil, "processes", elections[0].String())
	c.Assert(byRowID.Legacy, qt.IsTrue)
	c.Assert(byElectionID.ID, qt.Equals, byRowID.ID)

	// anonymous callers see it too: these records are finished and public.
	anon := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", rowOID.Hex())
	c.Assert(anon.Legacy, qt.IsTrue)

	// the projection is read-only: nothing legacy becomes writable through /processes.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodPut, token,
		newVotingProcessRequest(orgAddress, ids), "processes", rowOID.Hex())
	requestAndAssertCode(http.StatusNotFound, t, http.MethodDelete, token, nil, "processes", rowOID.Hex())
	requestAndAssertCode(http.StatusNotFound, t, http.MethodPost, token, nil, "processes", rowOID.Hex(), "publish")

	// a well-formed id nothing owns is a 404; malformed input is still a 400.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, token, nil, "processes", explorerElectionID)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, token, nil, "processes", "not-a-process-id")

	// the second orphaned election is left unregistered on purpose: nothing claims it, so it stays
	// out of the projection entirely.
	c.Assert(elections[1].String(), qt.Not(qt.Equals), elections[0].String())
}
