package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// legacyTestElection builds an election as the node returns it: metadata inlined as the untyped
// document the projection decodes, and maxCount set to the ballot's field count (one per result row),
// which describes the election itself and not its tally.
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
	// the real record's tally mode carries costExponent 1, which the chain ignores without a cost cap:
	// it must not cost the questions the singlechoice name their ballot runs.
	election.TallyMode.CostExponent = 1
	legacyTestElectionType(election, "single-choice-multiquestion", map[string]any{})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	c.Assert(content.title, qt.DeepEquals, db.MultiLangString{"default": "Assemblea Straordinaria"})
	c.Assert(content.description, qt.DeepEquals, db.MultiLangString{"default": "convocazione"})
	c.Assert(content.header, qt.Equals, "https://example.org/header.png")
	c.Assert(content.questions, qt.HasLen, 2)

	processID := bson.NewObjectID()
	questions := legacyElectionQuestions(processID, election, content)
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
		// the question's own slice of the ballot: one field, valued over the two choices.
		c.Assert(q.BallotProtocol, qt.DeepEquals, &db.BallotProtocol{MaxCount: 1, MaxValue: 1})
		c.Assert(q.TypeSetup, qt.Equals, db.QuestionTypeSetup{MinChoices: 1, MaxChoices: 1})
		c.Assert(legacyTestDeclaredType(q), qt.Not(qt.IsNil))
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
	c.Assert(legacyElectionQuestions(processID, election, content)[0].ID, qt.Equals, questions[0].ID)
}

// TestLegacyProjectionSingleQuestion checks that a single question owns the whole result matrix,
// however many ballot fields it has (a multichoice or ranked question).
func TestLegacyProjectionSingleQuestion(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{3, 1}, {2, 2}, {0, 4}},
		[]map[string]any{legacyTestQuestion("Statuto")})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 1)
	c.Assert(questions[0].Results.Results, qt.DeepEquals, [][]string{{"3", "1"}, {"2", "2"}, {"0", "4"}})
	// three ballot fields behind one question: no named type reproduces that ballot, but the ballot
	// itself is the question's own, so the client still gets the numbers it needs to read the matrix.
	c.Assert(questions[0].Type, qt.Equals, "")
	c.Assert(questions[0].BallotProtocol, qt.DeepEquals, &db.BallotProtocol{MaxCount: 3, MaxValue: 1})
}

// TestLegacyProjectionUnmappableResults checks that a tally whose rows do not map onto the questions
// still reports the counts but omits the matrix, rather than splitting it wrongly.
func TestLegacyProjectionUnmappableResults(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}, {8, 0}, {8, 0}},
		[]map[string]any{legacyTestQuestion("Statuto"), legacyTestQuestion("Consiglio Direttivo")})
	// three ballot fields over two questions: the tally does not map, and neither does the type.
	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 2)
	for _, q := range questions {
		c.Assert(q.Results.Results, qt.IsNil)
		c.Assert(q.Results.VoteCount, qt.Equals, uint64(8))
		c.Assert(q.Type, qt.Equals, "")
		c.Assert(q.BallotProtocol, qt.IsNil)
	}
}

// legacyTestChoices is a metadata question with n choices valued 0..n-1.
func legacyTestChoices(title string, n int) map[string]any {
	choices := make([]any, 0, n)
	for i := range n {
		choices = append(choices, map[string]any{
			"title": map[string]any{"default": fmt.Sprintf("choice %d", i)},
			"value": i,
		})
	}
	return map[string]any{"title": map[string]any{"default": title}, "choices": choices}
}

// legacyTestElectionType stamps a metadata "type" block on a fixture election: the declared name is a
// claim the chain never saw, so the projection only passes it through once the ballot corroborates it.
func legacyTestElectionType(election *dvoteapi.Election, name string, properties map[string]any) {
	if meta, ok := election.Metadata.(map[string]any); ok {
		meta["type"] = map[string]any{"name": name, "properties": properties}
	}
}

// legacyTestDeclaredType reads back the metadata "type" block the projection exposed.
func legacyTestDeclaredType(question db.VotingProcessQuestion) *db.ElectionTypeMetadata {
	declared, ok := question.Metadata["type"].(*db.ElectionTypeMetadata)
	if !ok {
		return nil
	}
	return declared
}

// TestLegacyProjectionChainBallot checks the record of issue #711: a ranked-with-overwrites ballot that
// no named type reproduces still projects the parameters a client needs to read its results matrix, and
// its declared multiple-choice name — which the ballot corroborates — comes through beside them.
func TestLegacyProjectionChainBallot(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{3, 1, 4}, {2, 2, 4}, {0, 4, 4}},
		[]map[string]any{legacyTestChoices("Statuto", 3)})
	election.TallyMode.MaxValue = 2
	election.TallyMode.MaxVoteOverwrites = 10
	election.TallyMode.CostExponent = 1
	election.VoteMode.UniqueValues = true
	legacyTestElectionType(election, "multiple-choice", map[string]any{
		"numChoices":   map[string]any{"min": 0, "max": 3},
		"repeatChoice": false,
	})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 1)
	// the whole ballot is the only question's, echoed field for field from tallyMode and voteMode.
	c.Assert(questions[0].BallotProtocol, qt.DeepEquals, &db.BallotProtocol{
		MaxCount: 3, MaxValue: 2, MaxVoteOverwrites: 10, CostExponent: 1, UniqueValues: true,
	})
	// no named type maps onto a ranked ballot that also allows overwrites, so none is claimed.
	c.Assert(questions[0].Type, qt.Equals, "")
	c.Assert(legacyTestDeclaredType(questions[0]), qt.Not(qt.IsNil))
	c.Assert(legacyTestDeclaredType(questions[0]).Name, qt.Equals, "multiple-choice")
	// with no named type, the bounds come from the corroborated block rather than staying zero:
	// numChoices.max caps the choices, and repeatChoice: false is a ranked ballot's unique values.
	bounds := db.QuestionTypeSetup{MaxChoices: 3, UniqueChoices: true}
	c.Assert(questions[0].TypeSetup, qt.Equals, bounds)
}

// TestLegacyProjectionSingleChoiceAbstain checks issue #724: the legacy SDK publishes a single-pick
// multiple-choice question with abstain as maxCount 1 over one value past the last choice. The ballot
// corroborates the declared name, so it comes through and a client keeps the abstain column; without
// that extra value a single pick reads the same as single-choice, so the name is still dropped.
func TestLegacyProjectionSingleChoiceAbstain(t *testing.T) {
	c := qt.New(t)
	project := func(maxValue uint32) db.VotingProcessQuestion {
		election := legacyTestElection([][]uint64{{3, 2, 1, 2}}, []map[string]any{legacyTestChoices("Statuto", 3)})
		election.TallyMode.MaxValue = maxValue
		legacyTestElectionType(election, "multiple-choice", map[string]any{
			"numChoices": map[string]any{"min": 0, "max": 1},
			"canAbstain": true,
		})
		content := testAPI.legacyContentOf(context.Background(), election, nil)
		questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
		c.Assert(questions, qt.HasLen, 1)
		return questions[0]
	}

	abstain := project(3)
	c.Assert(legacyTestDeclaredType(abstain), qt.Not(qt.IsNil))
	c.Assert(legacyTestDeclaredType(abstain).Name, qt.Equals, "multiple-choice")
	// no named type runs an abstain slot, so the name is the declared one and the bounds come from it.
	c.Assert(abstain.Type, qt.Equals, "")
	c.Assert(abstain.BallotProtocol.MaxCount, qt.Equals, uint32(1))
	c.Assert(abstain.BallotProtocol.MaxValue, qt.Equals, uint32(3))
	c.Assert(abstain.TypeSetup.MaxChoices, qt.Equals, uint32(1))

	c.Assert(legacyTestDeclaredType(project(2)), qt.IsNil)
}

// TestLegacyProjectionUncorroboratedType checks the caution the whole metadata pass-through rests on:
// elections the SaaS API published declare single-choice-multiquestion whatever ballot they then ran,
// and a client trusts the declared name over the parameters, so a contradicted name is dropped.
func TestLegacyProjectionUncorroboratedType(t *testing.T) {
	c := qt.New(t)
	// an approval ballot: one field per choice, each valued 0 or 1 — not one field over the choices.
	election := legacyTestElection([][]uint64{{6, 2}, {4, 4}}, []map[string]any{legacyTestChoices("Statuto", 2)})
	legacyTestElectionType(election, "single-choice-multiquestion", map[string]any{
		"numChoices": map[string]any{"min": 1, "max": 1},
	})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 1)
	// the name says one choice, the ballot ran two fields: dropped, and its bounds with it.
	c.Assert(legacyTestDeclaredType(questions[0]), qt.IsNil)
	// what the ballot runs is an uncapped approval — a multichoice once the inert costExponent is read
	// canonically — so the type comes from the protocol, never from the contradicted name.
	c.Assert(questions[0].Type, qt.Equals, db.VotingTypeMultiChoice)
	c.Assert(questions[0].BallotProtocol, qt.DeepEquals, &db.BallotProtocol{MaxCount: 2, MaxValue: 1, CostExponent: 1})
	c.Assert(questions[0].TypeSetup, qt.Equals, db.QuestionTypeSetup{})
}

// TestLegacyProjectionBudgetBallot checks a budget ballot, where a single field per choice carries a
// spend rather than a rank. Labelling it single-choice — as the projection's first cut did whenever the
// field count matched the question count — is exactly the wrong-numbers failure #711 reports.
func TestLegacyProjectionBudgetBallot(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{40}, {35}, {25}}, []map[string]any{legacyTestChoices("Repartiment", 3)})
	election.TallyMode.MaxValue = 0
	election.TallyMode.MaxTotalCost = 100
	election.TallyMode.CostExponent = 1
	legacyTestElectionType(election, "budget-based", map[string]any{"numChoices": map[string]any{"min": 1}})

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 1)
	// the ballot is reproduced exactly by a cumulative question, so it is named one — with the budget.
	c.Assert(questions[0].Type, qt.Equals, db.VotingTypeCumulative)
	c.Assert(questions[0].TypeSetup, qt.Equals, db.QuestionTypeSetup{Budget: 100, CostExponent: 1})
	c.Assert(legacyTestDeclaredType(questions[0]), qt.Not(qt.IsNil))
	c.Assert(legacyTestDeclaredType(questions[0]).Name, qt.Equals, "budget-based")
}

// TestLegacyProjectionUnevenQuestions checks a single-choice-multiquestion ballot whose questions offer
// different numbers of choices: the SDK sizes maxValue to the widest one, so the narrower question
// shares it and the declared name still holds for both.
func TestLegacyProjectionUnevenQuestions(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{3, 2, 1}, {4, 2, 0}},
		[]map[string]any{legacyTestChoices("Statuto", 3), legacyTestChoices("Consiglio", 2)})
	election.TallyMode.MaxValue = 2
	legacyTestElectionType(election, "single-choice-multiquestion", nil)

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 2)
	for _, q := range questions {
		c.Assert(legacyTestDeclaredType(q), qt.Not(qt.IsNil))
		c.Assert(q.BallotProtocol, qt.DeepEquals, &db.BallotProtocol{MaxCount: 1, MaxValue: 2})
	}
	c.Assert(questions[0].Type, qt.Equals, db.VotingTypeSingleChoice)
}

// TestLegacyProjectionJointConstraints checks that a cost cap spanning a multi-question ballot is not
// handed to each question as its own: it bounds the fields together, so no question can state it.
func TestLegacyProjectionJointConstraints(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}, {8, 0}},
		[]map[string]any{legacyTestQuestion("Statuto"), legacyTestQuestion("Consiglio")})
	election.TallyMode.MaxTotalCost = 1
	election.TallyMode.CostExponent = 1

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 2)
	for _, q := range questions {
		c.Assert(q.BallotProtocol, qt.IsNil)
		c.Assert(q.Type, qt.Equals, "")
	}
}

// TestLegacyProjectionTypeContradictsChoices checks the other half of the same guard: a ballot whose
// field count matches the question count, but whose values reach past the choices on offer, describes
// no single-choice question either — the field count alone never proved it did.
func TestLegacyProjectionTypeContradictsChoices(t *testing.T) {
	c := qt.New(t)
	election := legacyTestElection([][]uint64{{8, 0}, {8, 0}},
		[]map[string]any{legacyTestQuestion("Statuto"), legacyTestQuestion("Consiglio")})
	election.TallyMode.MaxValue = 3 // three values over two choices: not a choice per value
	legacyTestElectionType(election, "single-choice-multiquestion", nil)

	content := testAPI.legacyContentOf(context.Background(), election, nil)
	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 2)
	for _, q := range questions {
		c.Assert(q.BallotProtocol, qt.DeepEquals, &db.BallotProtocol{MaxCount: 1, MaxValue: 3})
		c.Assert(q.Type, qt.Equals, "")
		c.Assert(legacyTestDeclaredType(q), qt.IsNil)
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
	// a row with no type block still declares the default BuildElectionMetadata stamped on-chain.
	c.Assert(content.typeMetadata, qt.DeepEquals, &db.ElectionTypeMetadata{Name: "single-choice-multiquestion"})

	questions := legacyElectionQuestions(bson.NewObjectID(), election, content)
	c.Assert(questions, qt.HasLen, 1)
	c.Assert(questions[0].Title, qt.DeepEquals, db.MultiLangString{"default": "stored question"})
	c.Assert(questions[0].Status, qt.Equals, "ENDED")
	c.Assert(questions[0].Results.VoteCount, qt.Equals, uint64(2))
	// no census on the election read: MaxVoters is simply unknown, not invented.
	c.Assert(questions[0].Results.MaxVoters, qt.Equals, uint64(0))
}

// TestLegacyProjectionExternalMetadata checks the bundle-only case the projection exists for: the
// node inlines the metadata only for ipfs://, so an election pointing at an http(s) document must
// still resolve its questions, or the record projects to nothing and stays invisible.
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

// legacyTestSource registers election under a fresh id in the election caches, as a chain read
// would, and returns a source spanning it. The election id is not on the test chain, so any read
// that misses the caches fails.
func legacyTestSource(election *dvoteapi.Election) *legacyProcessSource {
	election.ElectionID = types.HexStringToHexBytes(fmt.Sprintf("%064x", bson.NewObjectID()))
	if election.Status == "RESULTS" {
		testAPI.electionCache.Add(election.ElectionID.String(), election)
	} else {
		testAPI.liveElectionCache.Add(election.ElectionID.String(), election)
	}
	return &legacyProcessSource{
		id:        bson.NewObjectID(),
		census:    &db.Census{Size: 3},
		elections: []internal.HexBytes{internal.HexBytes(election.ElectionID)},
	}
}

// TestLegacyProjectionCache checks a final record is served from the projection cache with no chain
// read, and a still-moving one is re-projected on every read.
func TestLegacyProjectionCache(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()

	final := legacyTestElection([][]uint64{{8, 0}}, []map[string]any{legacyTestQuestion("Q")})
	src := legacyTestSource(final)
	first, err := testAPI.projectLegacyProcess(ctx, src)
	c.Assert(err, qt.IsNil)
	c.Assert(first, qt.Not(qt.IsNil))
	// drop the election: a second projection that went back to the chain would now fail.
	testAPI.electionCache.Remove(final.ElectionID.String())
	second, err := testAPI.projectLegacyProcess(ctx, src)
	c.Assert(err, qt.IsNil)
	c.Assert(second, qt.DeepEquals, first)
	c.Assert(second, qt.Not(qt.Equals), first)

	live := legacyTestElection([][]uint64{{8, 0}}, []map[string]any{legacyTestQuestion("Q")})
	live.Status = "ENDED"
	liveSrc := legacyTestSource(live)
	resp, err := testAPI.projectLegacyProcess(ctx, liveSrc)
	c.Assert(err, qt.IsNil)
	c.Assert(resp, qt.Not(qt.IsNil))
	c.Assert(testAPI.legacyProjectionCache.Contains(liveSrc.cacheKey()), qt.IsFalse)

	// a final election whose content did not resolve is not frozen either.
	unresolved := legacyTestElection([][]uint64{{8, 0}}, nil)
	unresolved.Metadata = nil
	unresolvedSrc := legacyTestSource(unresolved)
	resp, err = testAPI.projectLegacyProcess(ctx, unresolvedSrc)
	c.Assert(err, qt.IsNil)
	c.Assert(resp, qt.IsNil)
	c.Assert(testAPI.legacyProjectionCache.Contains(unresolvedSrc.cacheKey()), qt.IsFalse)
}

// TestLegacyProcessesProjection exercises the projection end to end: a legacy db.Process row listed
// and readable by its own id and by its election id, content from the stored params and state from
// the chain, deduped against a bundle registering the same election, and unwritable.
//
// The election is minted by publishing a throwaway /processes process and deleting its stored rows,
// which leaves the shape this projection exists for. The bundle-only case is left to the unit tests:
// the test chain does not serve a metadata URL pointing back at this in-process server.
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
	rowOID := bson.NewObjectID()
	insertLegacyDoc(t, "processes", db.Process{
		ID:         rowOID,
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

	// a bundle registering the same election is not a second record: the row wins, because it
	// carries the election parameters and needs no chain round-trip for its content.
	bundleID := bson.NewObjectID()
	insertLegacyDoc(t, "processBundles", db.ProcessesBundle{
		ID:         bundleID,
		OrgAddress: orgAddress,
		Census:     *census,
		Processes:  []internal.HexBytes{elections[0]},
	})

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
	_, duplicated := byID[bundleID.Hex()]
	c.Assert(duplicated, qt.IsFalse)
	// and not readable under the bundle id either: the record the list attributes to the row must
	// not answer a second time under another id.
	requestAndAssertCode(http.StatusNotFound, t, http.MethodGet, token, nil, "processes", bundleID.Hex())

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

// TestFetchExternalMetadata covers the external http(s) fetch helper: a valid JSON
// document is decoded, a non-200 yields nil, and a body over the 1 MiB cap is rejected.
func TestFetchExternalMetadata(t *testing.T) {
	c := qt.New(t)

	t.Run("ok", func(_ *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"title":"hello","version":"1.0"}`))
		}))
		defer ts.Close()
		m := fetchExternalMetadata(t.Context(), ts.URL)
		c.Assert(m, qt.Not(qt.IsNil))
		c.Assert(m["title"], qt.Equals, "hello")
	})

	t.Run("non-200", func(_ *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()
		c.Assert(fetchExternalMetadata(t.Context(), ts.URL), qt.IsNil)
	})

	t.Run("over size cap", func(_ *testing.T) {
		// a valid JSON document larger than the 1 MiB read cap is truncated and fails to
		// decode, so it must be rejected rather than partially parsed.
		big, err := json.Marshal(map[string]any{"x": strings.Repeat("a", 2<<20)})
		c.Assert(err, qt.IsNil)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(big)
		}))
		defer ts.Close()
		c.Assert(fetchExternalMetadata(t.Context(), ts.URL), qt.IsNil)
	})
}
