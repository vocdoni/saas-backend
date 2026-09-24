package api

import (
	"encoding/json"
	"slices"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// legacyElectionCacheSize is how many on-chain elections the legacy projection keeps cached. Only
// terminal (immutable) elections are cached, and the legacy set is closed — nothing creates these
// any more — so a few hundred entries covers every legacy read without ever going stale.
const legacyElectionCacheSize = 512

// legacyProcessSource is one legacy record to be projected onto a /processes container: a process
// bundle (one or more on-chain elections sharing a census) or a published db.Process row (exactly
// one election). params is set only for the latter, and only when the row stored it — it holds the
// titles and choices, which then need no chain metadata fetch.
type legacyProcessSource struct {
	id         bson.ObjectID
	orgAddress common.Address
	census     *db.Census
	elections  []internal.HexBytes
	params     *db.ElectionParams
}

// legacyContent is the off-chain content of a legacy election: the container-level fields and the
// ballot questions, taken from the stored ElectionParams when the legacy row kept them and from the
// election's own metadata otherwise.
type legacyContent struct {
	title       db.MultiLangString
	description db.MultiLangString
	header      string
	streamURI   string
	questions   []db.Question
}

// legacyProcesses projects every legacy election of an organization onto the /processes read shape.
// Read-only: nothing is written, and no legacy record becomes editable through /processes.
//
// ponytail: unpaginated — the legacy set is closed (nothing creates these any more) and an
// organization holds a handful. Move to a paginated db query if that stops being true.
func (a *API) legacyProcesses(org common.Address) []apicommon.VotingProcessResponse {
	sources := a.legacyProcessSources(org)
	projected := make([]*apicommon.VotingProcessResponse, len(sources))
	parallelForEach(len(sources), func(i int) {
		projected[i] = a.projectLegacyProcess(sources[i])
	})
	out := make([]apicommon.VotingProcessResponse, 0, len(projected))
	for _, resp := range projected {
		if resp != nil {
			out = append(out, *resp)
		}
	}
	return out
}

// legacyProcessByID resolves one legacy record by the id GET /processes/{processId} was called with:
// the ObjectID of a process bundle or of a published db.Process row, or the on-chain election id of
// either. Returns nil when no legacy record owns the id.
func (a *API) legacyProcessByID(raw string) *apicommon.VotingProcessResponse {
	src := a.legacyProcessSourceByID(raw)
	if src == nil {
		return nil
	}
	return a.projectLegacyProcess(src)
}

// legacyProcessSources collects every legacy record of an organization, keyed by on-chain election
// id so an election registered in a bundle and also stored as a db.Process row yields one entry.
// The Process row wins: it stores the election parameters, so its titles and choices resolve without
// a chain metadata fetch. Elections already served by the /processes path are skipped entirely.
func (a *API) legacyProcessSources(org common.Address) []*legacyProcessSource {
	claimed := map[string]bool{}
	var out []*legacyProcessSource

	processes, err := a.db.AllProcessesByOrg(org, db.PublishedOnly)
	if err != nil {
		log.Warnw("legacy processes: could not list legacy processes", "org", org.Hex(), "error", err)
	}
	for i := range processes {
		p := &processes[i]
		if len(p.Address) == 0 || a.servedByProcessesAPI(p.Address) {
			continue
		}
		claimed[p.Address.String()] = true
		out = append(out, &legacyProcessSource{
			id:         p.ID,
			orgAddress: p.OrgAddress,
			census:     &p.Census,
			elections:  []internal.HexBytes{p.Address},
			params:     p.ElectionParams,
		})
	}

	bundles, err := a.db.ProcessBundlesByOrg(org)
	if err != nil {
		log.Warnw("legacy processes: could not list process bundles", "org", org.Hex(), "error", err)
	}
	for _, bundle := range bundles {
		var elections []internal.HexBytes
		for _, electionID := range bundle.Processes {
			if claimed[electionID.String()] || a.servedByProcessesAPI(electionID) {
				continue
			}
			claimed[electionID.String()] = true
			elections = append(elections, electionID)
		}
		// an empty bundle (or one whose every election is already served elsewhere) would project to
		// a process with no questions, so it is not a record of its own.
		if len(elections) == 0 {
			continue
		}
		out = append(out, &legacyProcessSource{
			id:         bundle.ID,
			orgAddress: bundle.OrgAddress,
			census:     &bundle.Census,
			elections:  elections,
		})
	}
	return out
}

// legacyProcessSourceByID resolves a single legacy record from the raw path id. A 24-hex ObjectID is
// looked up as a process bundle and then as a published db.Process row; anything else is parsed as
// an on-chain election id and resolved through the process row that owns it and then through the
// bundle that registered it.
func (a *API) legacyProcessSourceByID(raw string) *legacyProcessSource {
	if oid, err := bson.ObjectIDFromHex(raw); err == nil {
		if bundle, err := a.db.ProcessBundle(oid[:]); err == nil {
			return a.bundleSource(bundle)
		}
		if process, err := a.db.Process(oid); err == nil {
			return a.processSource(process)
		}
		return nil
	}
	var electionID internal.HexBytes
	if err := electionID.ParseString(raw); err != nil || len(electionID) == 0 {
		return nil
	}
	// an election the /processes path already serves is not legacy; the caller resolved it there.
	if a.servedByProcessesAPI(electionID) {
		return nil
	}
	// the row is preferred over a bundle registering the same election, exactly as in
	// legacyProcessSources, so the single read and the list project the same record.
	if process, err := a.db.ProcessByAddress(electionID); err == nil {
		return a.processSource(process)
	}
	if bundles, err := a.db.ProcessBundlesByProcess(electionID); err == nil && len(bundles) > 0 {
		return a.bundleSource(bundles[0])
	}
	return nil
}

// bundleSource builds the projection source of a process bundle, dropping the elections the
// /processes path already serves. Returns nil when no legacy election is left.
func (a *API) bundleSource(bundle *db.ProcessesBundle) *legacyProcessSource {
	var elections []internal.HexBytes
	for _, electionID := range bundle.Processes {
		if !a.servedByProcessesAPI(electionID) {
			elections = append(elections, electionID)
		}
	}
	if len(elections) == 0 {
		return nil
	}
	return &legacyProcessSource{
		id:         bundle.ID,
		orgAddress: bundle.OrgAddress,
		census:     &bundle.Census,
		elections:  elections,
	}
}

// processSource builds the projection source of a legacy db.Process row. Returns nil for an
// unpublished draft (no on-chain election, so nothing to project) and for a row the /processes path
// already serves.
func (a *API) processSource(process *db.Process) *legacyProcessSource {
	if len(process.Address) == 0 || a.servedByProcessesAPI(process.Address) {
		return nil
	}
	return &legacyProcessSource{
		id:         process.ID,
		orgAddress: process.OrgAddress,
		census:     &process.Census,
		elections:  []internal.HexBytes{process.Address},
		params:     process.ElectionParams,
	}
}

// servedByProcessesAPI reports whether an on-chain election already backs a /processes question, in
// which case the normal path lists it and the legacy projection must not duplicate it.
func (a *API) servedByProcessesAPI(electionID internal.HexBytes) bool {
	_, err := a.db.QuestionByUpstreamID(electionID)
	return err == nil
}

// projectLegacyProcess builds the /processes read shape of one legacy record. Every question of
// every election it holds is flattened into the question list, in election then ballot order, so a
// legacy multi-question election becomes several questions sharing one UpstreamID — which is what
// the response's Legacy flag warns clients about. Returns nil when no election could be read from
// the chain, the whole content of a legacy record living there.
func (a *API) projectLegacyProcess(src *legacyProcessSource) *apicommon.VotingProcessResponse {
	if src == nil {
		return nil
	}
	vp := &db.VotingProcess{ID: src.id, OrgAddress: src.orgAddress, Published: true}
	var questions []db.VotingProcessQuestion
	for _, electionID := range src.elections {
		election := a.legacyElection(electionID)
		if election == nil {
			continue
		}
		content := legacyContentOf(election, src.params)
		// the container fields come from the first election that resolved any; a bundle's elections
		// are variations of one consultation, so the first is representative.
		if vp.Title == nil {
			vp.Title, vp.Description = content.title, content.description
			vp.Header, vp.StreamURI = content.header, content.streamURI
		}
		// a bundle spans several elections: report the window that covers all of them.
		if vp.StartDate.IsZero() || election.StartDate.Before(vp.StartDate) {
			vp.StartDate = election.StartDate
		}
		if election.EndDate.After(vp.EndDate) {
			vp.EndDate = election.EndDate
		}
		questions = append(questions, legacyElectionQuestions(election, content.questions)...)
	}
	if len(questions) == 0 {
		return nil
	}
	resp := apicommon.VotingProcessResponseFromDB(vp, questions, src.census, a.account.ChainID())
	resp.Legacy = true
	resp.Census.TotalWeight = a.censusTotalWeight(src.census)
	return resp
}

// legacyElection returns an on-chain election, caching the terminal ones. A finished election is
// immutable, so caching it keeps the public list endpoint from fanning out to the Vochain on every
// anonymous hit; a live one is always re-read. A fetch failure is logged and yields nil — a legacy
// record whose chain state is unreachable is omitted rather than served half-empty.
func (a *API) legacyElection(electionID internal.HexBytes) *dvoteapi.Election {
	key := electionID.String()
	if cached, ok := a.electionCache.Get(key); ok {
		return cached
	}
	election, err := a.account.Election(electionID)
	if err != nil {
		log.Warnw("legacy processes: election fetch failed", "election", key, "error", err)
		return nil
	}
	if slices.Contains([]string{"RESULTS", "ENDED", "CANCELED"}, election.Status) {
		a.electionCache.Add(key, election)
	}
	return election
}

// legacyContentOf returns the off-chain content of a legacy election: the stored ElectionParams when
// the legacy row kept them (no metadata fetch at all), otherwise the election's own metadata.
func legacyContentOf(election *dvoteapi.Election, params *db.ElectionParams) legacyContent {
	if params != nil {
		return legacyContent{
			title:       params.Title,
			description: params.Description,
			header:      params.Header,
			streamURI:   params.StreamURI,
			questions:   params.Questions,
		}
	}
	meta := legacyElectionMetadata(election)
	if meta == nil {
		return legacyContent{}
	}
	return legacyContent{
		title:       db.MultiLangString(meta.Title),
		description: db.MultiLangString(meta.Description),
		header:      meta.Media.Header,
		streamURI:   meta.Media.StreamURI,
		questions:   legacyMetaQuestions(meta.Questions),
	}
}

// legacyElectionMetadata decodes the off-chain ElectionMetadata document the node inlines into an
// election read. Election.Metadata is typed any, so it round-trips through JSON into the typed
// shape (same decode as processInfoHandler). Returns nil when the election carries no metadata.
func legacyElectionMetadata(election *dvoteapi.Election) *dvoteapi.ElectionMetadata {
	if election == nil || election.Metadata == nil {
		return nil
	}
	raw, err := json.Marshal(election.Metadata)
	if err != nil {
		return nil
	}
	meta := &dvoteapi.ElectionMetadata{}
	if err := json.Unmarshal(raw, meta); err != nil {
		log.Warnw("legacy processes: election metadata decode failed",
			"election", election.ElectionID.String(), "error", err)
		return nil
	}
	return meta
}

// legacyMetaQuestions converts on-chain metadata questions into the db question shape — the inverse
// of account.BuildElectionMetadata. api.LanguageString and db.MultiLangString are both
// map[string]string, and ChoiceMetadata mirrors db.Choice, so no field is lost or invented.
func legacyMetaQuestions(metaQuestions []dvoteapi.Question) []db.Question {
	out := make([]db.Question, 0, len(metaQuestions))
	for _, q := range metaQuestions {
		question := db.Question{
			Title:       db.MultiLangString(q.Title),
			Description: db.MultiLangString(q.Description),
			Choices:     make([]db.Choice, 0, len(q.Choices)),
		}
		for _, choice := range q.Choices {
			question.Choices = append(question.Choices, db.Choice{
				Title: db.MultiLangString(choice.Title),
				Value: choice.Value,
			})
		}
		out = append(out, question)
	}
	return out
}

// legacyElectionQuestions projects one legacy election onto the new-format questions: one question
// per ballot question, all carrying the election id as UpstreamID and the election's status, because
// a legacy election holds the whole ballot rather than a single question.
//
// BallotProtocol and TypeSetup are deliberately left unset: a legacy election has one tally mode for
// the whole ballot (maxCount is its number of questions), which describes no single question.
func legacyElectionQuestions(election *dvoteapi.Election, questions []db.Question) []db.VotingProcessQuestion {
	tally := questionResultsFromElection(election)
	out := make([]db.VotingProcessQuestion, 0, len(questions))
	for i, q := range questions {
		question := db.VotingProcessQuestion{
			Order:       i,
			Title:       q.Title,
			Description: q.Description,
			Choices:     q.Choices,
			UpstreamID:  internal.HexBytes(election.ElectionID),
			Status:      election.Status,
			Results:     legacyQuestionResults(election, tally, i, len(questions)),
		}
		// one ballot field per question is exactly a single-choice ballot; any other shape is not
		// statable per question, so the type is left empty rather than guessed.
		if len(questions) > 1 && len(tally.Results) == len(questions) {
			question.Type = "singlechoice"
		}
		out = append(out, question)
	}
	return out
}

// legacyQuestionResults splits an election's tally across the n questions of its ballot. The
// on-chain matrix holds one row per ballot field, which maps onto questions in exactly two shapes:
// a single question owns the whole matrix, and a one-field-per-question ballot gives question i row
// i. Any other shape (a multi-field ballot spread over several questions) has no defined mapping, so
// the counts are still reported but the matrix is omitted rather than split wrongly.
func legacyQuestionResults(
	election *dvoteapi.Election, tally db.QuestionResults, i, n int,
) *db.QuestionResults {
	results := tally
	switch {
	case n == 1:
		// the whole matrix belongs to the only question
	case len(tally.Results) == n:
		results.Results = [][]string{tally.Results[i]}
	default:
		if len(tally.Results) > 0 && i == 0 {
			log.Warnw("legacy processes: tally rows do not map onto questions",
				"election", election.ElectionID.String(), "rows", len(tally.Results), "questions", n)
		}
		results.Results = nil
	}
	return &results
}

// filterLegacyProcessesByStatus keeps the legacy processes with at least one question in the given
// status, mirroring what ListVotingProcesses's status filter does on the stored collection.
func filterLegacyProcessesByStatus(
	list []apicommon.VotingProcessResponse, status string,
) []apicommon.VotingProcessResponse {
	out := list[:0]
	for _, process := range list {
		for _, question := range process.Questions {
			if question.Status == status {
				out = append(out, process)
				break
			}
		}
	}
	return out
}

// readVotingProcess resolves a stored voting process from the id GET /processes/{processId} was
// called with: its own ObjectID, or the on-chain election id of one of its questions. The election
// id is a read-only alias — the write and publish handlers keep going through votingProcessID, so
// they still accept the ObjectID only. Returns db.ErrNotFound when no stored process owns the id,
// whatever its shape, leaving the caller to try the legacy projection.
func (a *API) readVotingProcess(raw string) (*db.VotingProcess, []db.VotingProcessQuestion, error) {
	if oid, err := bson.ObjectIDFromHex(raw); err == nil {
		return a.db.ProcessWithQuestions(oid)
	}
	var electionID internal.HexBytes
	if err := electionID.ParseString(raw); err != nil {
		return nil, nil, db.ErrNotFound
	}
	question, err := a.db.QuestionByUpstreamID(electionID)
	if err != nil {
		return nil, nil, db.ErrNotFound
	}
	return a.db.ProcessWithQuestions(question.ProcessID)
}

// isVotingProcessIDShape reports whether the raw path id is a well-formed process id — a 24-hex
// ObjectID or a 32-byte on-chain election id — so an id nothing owns answers 404 while genuinely
// malformed input stays a 400.
func isVotingProcessIDShape(raw string) bool {
	if _, err := bson.ObjectIDFromHex(raw); err == nil {
		return true
	}
	var electionID internal.HexBytes
	return electionID.ParseString(raw) == nil && len(electionID) == 32
}
