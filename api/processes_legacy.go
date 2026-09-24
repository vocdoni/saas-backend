package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// legacyElectionCacheSize is how many on-chain elections the legacy projection keeps cached. Only
// immutable elections are cached, and the legacy set is closed — nothing creates these any more —
// so a few hundred entries covers every legacy read without ever going stale.
const legacyElectionCacheSize = 512

// errLegacyChainUnavailable marks a projection failure caused by the Vochain rather than by storage,
// so the handlers can answer 50004 (blockchain request failed) instead of a generic 500.
var errLegacyChainUnavailable = stderrors.New("legacy election could not be read from the chain")

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
// Read-only: nothing is written, and no legacy record becomes editable through /processes. An error
// is returned rather than a short list — a storage or chain failure must not silently drop records
// from a paginated response that also reports their count.
//
// ponytail: unpaginated — the legacy set is closed (nothing creates these any more) and an
// organization holds a handful, all finished, so the election cache serves them after the first
// read. Move to a paginated db query if that stops being true.
func (a *API) legacyProcesses(ctx context.Context, org common.Address) ([]apicommon.VotingProcessResponse, error) {
	sources, err := a.legacyProcessSources(org)
	if err != nil {
		return nil, err
	}
	projected := make([]*apicommon.VotingProcessResponse, len(sources))
	errs := make([]error, len(sources))
	parallelForEach(len(sources), func(i int) {
		projected[i], errs[i] = a.projectLegacyProcess(ctx, sources[i])
	})
	if err := stderrors.Join(errs...); err != nil {
		return nil, err
	}
	out := make([]apicommon.VotingProcessResponse, 0, len(projected))
	for _, resp := range projected {
		if resp != nil {
			out = append(out, *resp)
		}
	}
	return out, nil
}

// legacyProcessByID resolves one legacy record by the id GET /processes/{processId} was called with:
// the ObjectID of a process bundle or of a published db.Process row, or the on-chain election id of
// either. Returns a nil response and a nil error when no legacy record owns the id.
func (a *API) legacyProcessByID(ctx context.Context, raw string) (*apicommon.VotingProcessResponse, error) {
	src, err := a.legacyProcessSourceByID(raw)
	if err != nil || src == nil {
		return nil, err
	}
	return a.projectLegacyProcess(ctx, src)
}

// legacyProjectionError maps a projection failure onto the API error to answer with: a chain read
// that failed is a 50004, anything else is a storage failure.
func legacyProjectionError(err error) errors.Error {
	if stderrors.Is(err, errLegacyChainUnavailable) {
		return errors.ErrVochainRequestFailed.WithErr(err)
	}
	return errors.ErrGenericInternalServerError.WithErr(err)
}

// legacyProcessSources collects every legacy record of an organization, keyed by on-chain election
// id so an election registered in a bundle and also stored as a db.Process row yields one entry.
// The Process row wins: it stores the election parameters, so its titles and choices resolve without
// a chain metadata fetch. Elections already served by the /processes path are skipped entirely.
func (a *API) legacyProcessSources(org common.Address) ([]*legacyProcessSource, error) {
	claimed := map[string]bool{}
	var out []*legacyProcessSource

	processes, err := a.db.AllProcessesByOrg(org, db.PublishedOnly)
	if err != nil {
		return nil, fmt.Errorf("could not list legacy processes: %w", err)
	}
	for i := range processes {
		p := &processes[i]
		if len(p.Address) == 0 {
			continue
		}
		served, err := a.servedByProcessesAPI(p.Address)
		if err != nil {
			return nil, err
		}
		if served {
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
		return nil, fmt.Errorf("could not list process bundles: %w", err)
	}
	for _, bundle := range bundles {
		var elections []internal.HexBytes
		for _, electionID := range bundle.Processes {
			if claimed[electionID.String()] {
				continue
			}
			served, err := a.servedByProcessesAPI(electionID)
			if err != nil {
				return nil, err
			}
			if served {
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
	return out, nil
}

// legacyProcessSourceByID resolves a single legacy record from the raw path id. A 24-hex ObjectID is
// looked up as a process bundle and then as a published db.Process row; anything else is parsed as
// an on-chain election id and resolved through the process row that owns it and then through the
// bundle that registered it. A nil source with a nil error means no legacy record owns the id.
func (a *API) legacyProcessSourceByID(raw string) (*legacyProcessSource, error) {
	if oid, err := bson.ObjectIDFromHex(raw); err == nil {
		bundle, err := a.db.ProcessBundle(oid[:])
		if err == nil {
			return a.bundleSource(bundle)
		}
		if !stderrors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("could not read process bundle: %w", err)
		}
		process, err := a.db.Process(oid)
		if err == nil {
			return a.processSource(process)
		}
		if !stderrors.Is(err, db.ErrNotFound) {
			return nil, fmt.Errorf("could not read legacy process: %w", err)
		}
		return nil, nil
	}
	var electionID internal.HexBytes
	if err := electionID.ParseString(raw); err != nil || len(electionID) == 0 {
		return nil, nil
	}
	// the row is preferred over a bundle registering the same election, exactly as in
	// legacyProcessSources, so the single read and the list project the same record.
	process, err := a.db.ProcessByAddress(electionID)
	if err == nil {
		return a.processSource(process)
	}
	if !stderrors.Is(err, db.ErrNotFound) {
		return nil, fmt.Errorf("could not read legacy process: %w", err)
	}
	bundles, err := a.db.ProcessBundlesByProcess(electionID)
	if err != nil {
		return nil, fmt.Errorf("could not list bundles of election: %w", err)
	}
	if len(bundles) == 0 {
		return nil, nil
	}
	return a.bundleSource(bundles[0])
}

// bundleSource builds the projection source of a process bundle, dropping the elections that are
// projected under another id: those the /processes path serves, and those a db.Process row owns —
// the list attributes those to the row, and reading the bundle must not duplicate them under a
// second id. Returns nil when no election is left, so the bundle id answers 404.
func (a *API) bundleSource(bundle *db.ProcessesBundle) (*legacyProcessSource, error) {
	var elections []internal.HexBytes
	for _, electionID := range bundle.Processes {
		claimed, err := a.legacyElectionClaimedElsewhere(electionID)
		if err != nil {
			return nil, err
		}
		if !claimed {
			elections = append(elections, electionID)
		}
	}
	if len(elections) == 0 {
		return nil, nil
	}
	return &legacyProcessSource{
		id:         bundle.ID,
		orgAddress: bundle.OrgAddress,
		census:     &bundle.Census,
		elections:  elections,
	}, nil
}

// processSource builds the projection source of a legacy db.Process row. Returns nil for an
// unpublished draft (no on-chain election, so nothing to project) and for a row the /processes path
// already serves.
func (a *API) processSource(process *db.Process) (*legacyProcessSource, error) {
	if len(process.Address) == 0 {
		return nil, nil
	}
	served, err := a.servedByProcessesAPI(process.Address)
	if err != nil || served {
		return nil, err
	}
	return &legacyProcessSource{
		id:         process.ID,
		orgAddress: process.OrgAddress,
		census:     &process.Census,
		elections:  []internal.HexBytes{process.Address},
		params:     process.ElectionParams,
	}, nil
}

// servedByProcessesAPI reports whether an on-chain election already backs a /processes question, in
// which case the normal path lists it and the legacy projection must not duplicate it.
func (a *API) servedByProcessesAPI(electionID internal.HexBytes) (bool, error) {
	switch _, err := a.db.QuestionByUpstreamID(electionID); {
	case err == nil:
		return true, nil
	case stderrors.Is(err, db.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("could not look up question by upstream id: %w", err)
	}
}

// legacyElectionClaimedElsewhere reports whether an election is already projected under an id other
// than the bundle registering it: a /processes question, or a db.Process row.
func (a *API) legacyElectionClaimedElsewhere(electionID internal.HexBytes) (bool, error) {
	served, err := a.servedByProcessesAPI(electionID)
	if err != nil || served {
		return served, err
	}
	switch _, err := a.db.ProcessByAddress(electionID); {
	case err == nil:
		return true, nil
	case stderrors.Is(err, db.ErrNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("could not read legacy process: %w", err)
	}
}

// projectLegacyProcess builds the /processes read shape of one legacy record. Every question of
// every election it holds is flattened into the question list, in election then ballot order, so a
// legacy multi-question election becomes several questions sharing one UpstreamID — which is what
// the response's Legacy flag warns clients about. Returns nil when the record holds no question at
// all; a chain read that fails is an error, never a record served half-empty or dropped.
func (a *API) projectLegacyProcess(
	ctx context.Context, src *legacyProcessSource,
) (*apicommon.VotingProcessResponse, error) {
	if src == nil {
		return nil, nil
	}
	vp := &db.VotingProcess{ID: src.id, OrgAddress: src.orgAddress, Published: true}
	var questions []db.VotingProcessQuestion
	haveContent := false
	for _, electionID := range src.elections {
		election, err := a.legacyElection(electionID)
		if err != nil {
			return nil, err
		}
		content := a.legacyContentOf(ctx, election, src.params)
		// the container fields come from the first election that resolved its content, all of them
		// or none: a bundle's elections are variations of one consultation, so the first that
		// resolved is representative and a later one must not overwrite half of it.
		if !haveContent && len(content.questions) > 0 {
			vp.Title, vp.Description = content.title, content.description
			vp.Header, vp.StreamURI = content.header, content.streamURI
			haveContent = true
		}
		// a bundle spans several elections: report the window that covers all of them.
		if vp.StartDate.IsZero() || election.StartDate.Before(vp.StartDate) {
			vp.StartDate = election.StartDate
		}
		if election.EndDate.After(vp.EndDate) {
			vp.EndDate = election.EndDate
		}
		questions = append(questions, legacyElectionQuestions(src.id, election, content.questions)...)
	}
	if len(questions) == 0 {
		return nil, nil
	}
	resp := apicommon.VotingProcessResponseFromDB(vp, questions, src.census, a.account.ChainID())
	resp.Legacy = true
	resp.Census.TotalWeight = a.censusTotalWeight(src.census)
	return resp, nil
}

// legacyElection returns an on-chain election, caching the immutable ones. A published tally or a
// canceled election can no longer change, so caching it keeps the public list endpoint from fanning
// out to the Vochain on every anonymous hit. ENDED is deliberately not cached: an ended election
// still moves to RESULTS when its tally is published, and a cached ENDED would freeze the
// projection without its results.
func (a *API) legacyElection(electionID internal.HexBytes) (*dvoteapi.Election, error) {
	key := electionID.String()
	if cached, ok := a.electionCache.Get(key); ok {
		return cached, nil
	}
	election, err := a.account.Election(electionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errLegacyChainUnavailable, key, err)
	}
	if slices.Contains([]string{"RESULTS", "CANCELED"}, election.Status) {
		a.electionCache.Add(key, election)
	}
	return election, nil
}

// legacyContentOf returns the off-chain content of a legacy election: the stored ElectionParams when
// the legacy row kept them (no metadata fetch at all), otherwise the election's own metadata. Params
// without questions are not content — a row that stored only a draft form blob is projected from the
// chain like any bundle-backed election.
func (a *API) legacyContentOf(
	ctx context.Context, election *dvoteapi.Election, params *db.ElectionParams,
) legacyContent {
	if params != nil && len(params.Questions) > 0 {
		return legacyContent{
			title:       params.Title,
			description: params.Description,
			header:      params.Header,
			streamURI:   params.StreamURI,
			questions:   params.Questions,
		}
	}
	meta := a.legacyElectionMetadata(ctx, election)
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

// legacyElectionMetadata decodes the off-chain ElectionMetadata document of an election into the
// typed shape (a JSON round-trip, as Election.Metadata is typed any — the same decode as
// processInfoHandler). Returns nil when the document cannot be resolved or decoded.
func (a *API) legacyElectionMetadata(ctx context.Context, election *dvoteapi.Election) *dvoteapi.ElectionMetadata {
	doc := a.legacyMetadataDocument(ctx, election)
	if doc == nil {
		return nil
	}
	raw, err := json.Marshal(doc)
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

// legacyMetadataDocument resolves the metadata document an election points at. The node inlines it
// only for ipfs:// references, so every other reference is resolved the way the legacy /process read
// resolves it (resolveProcessMetadata): our own object storage locally, http(s) externally. A
// bundle-backed election is projected entirely from this document, so failing to resolve it costs
// the whole record its questions.
func (a *API) legacyMetadataDocument(ctx context.Context, election *dvoteapi.Election) any {
	if inlined, ok := election.Metadata.(map[string]any); ok && len(inlined) > 0 {
		return inlined
	}
	name, isLocal := a.objectStorage.LocalName(election.MetadataURL)
	switch {
	case isLocal:
		obj, err := a.objectStorage.GetByName(name)
		if err != nil {
			log.Warnw("legacy processes: metadata object not found",
				"election", election.ElectionID.String(), "url", election.MetadataURL, "error", err)
			return nil
		}
		var m map[string]any
		if json.Unmarshal(obj.Data, &m) != nil {
			return nil
		}
		return m
	case strings.HasPrefix(election.MetadataURL, "http://"), strings.HasPrefix(election.MetadataURL, "https://"):
		return fetchExternalMetadata(ctx, election.MetadataURL)
	default:
		// ipfs:// (the only scheme the node resolves for us) with nothing inlined, or no reference
		// at all: there is no document to read.
		return nil
	}
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
func legacyElectionQuestions(
	processID bson.ObjectID, election *dvoteapi.Election, questions []db.Question,
) []db.VotingProcessQuestion {
	tally := questionResultsFromElection(election)
	// one ballot field per question is exactly a single-choice ballot; any other shape is not
	// statable per question, so the type is left empty rather than guessed. This reads the election's
	// own ballot description, so it does not change when the tally is published.
	singleChoice := election.TallyMode.ProcessVoteOptions != nil && int(election.TallyMode.MaxCount) == len(questions)
	out := make([]db.VotingProcessQuestion, 0, len(questions))
	for i, q := range questions {
		question := db.VotingProcessQuestion{
			ID:          legacyQuestionID(internal.HexBytes(election.ElectionID), i),
			ProcessID:   processID,
			Order:       i,
			Title:       q.Title,
			Description: q.Description,
			Choices:     q.Choices,
			UpstreamID:  internal.HexBytes(election.ElectionID),
			Status:      election.Status,
			Results:     legacyQuestionResults(election, tally, i, len(questions)),
		}
		if singleChoice {
			question.Type = "singlechoice"
		}
		if election.VoteMode.EnvelopeType != nil {
			question.SecretUntilTheEnd = election.VoteMode.EncryptedVotes
		}
		out = append(out, question)
	}
	return out
}

// legacyQuestionID derives the id of a projected question. Nothing is stored, but the read shape
// carries one and a zero ObjectID would repeat across every question of every legacy record, so it
// is derived from the election id and the ballot order: distinct per question, and stable across
// reads so a client can key on it.
func legacyQuestionID(electionID internal.HexBytes, order int) bson.ObjectID {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%d", electionID.String(), order))
	var id bson.ObjectID
	copy(id[:], sum[:])
	return id
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
