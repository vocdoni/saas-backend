package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// legacyElectionCacheSize bounds each legacy cache (final elections, live elections, projected
// records). The legacy set is closed — nothing creates these any more — so this covers every read.
const legacyElectionCacheSize = 4096

// legacyLiveElectionTTL is how long a still-moving election (READY, ONGOING, PAUSED, ENDED) is served
// from cache, so a burst of list reads hits the Vochain once. ENDED→RESULTS shows within this window.
const legacyLiveElectionTTL = 30 * time.Second

// legacyFinalStatuses are the election statuses nothing can move any more; only those are cached
// without expiry.
var legacyFinalStatuses = []string{"RESULTS", "CANCELED"}

// errLegacyChainUnavailable marks a projection failure caused by the Vochain rather than by storage,
// so the handlers answer 50004 instead of a generic 500.
var errLegacyChainUnavailable = stderrors.New("legacy election could not be read from the chain")

// legacyProcessSource is one legacy record to project: a process bundle (several elections sharing a
// census) or a published db.Process row (one election). params is set only for a row that stored
// them — it holds the titles and choices, which then need no chain metadata fetch.
type legacyProcessSource struct {
	id         bson.ObjectID
	orgAddress common.Address
	census     *db.Census
	elections  []internal.HexBytes
	params     *db.ElectionParams
}

// legacyContent is the off-chain content of a legacy election: container fields plus ballot
// questions, from the stored ElectionParams or from the election's own metadata.
type legacyContent struct {
	title       db.MultiLangString
	description db.MultiLangString
	header      string
	streamURI   string
	questions   []db.Question
	// typeMetadata is the document's declared "type" block, describing how the ballot is meant to be
	// read. It is a claim, not a configuration — the chain never sees it — so it is only projected
	// once the ballot corroborates it (legacyCorroboratedType).
	typeMetadata *db.ElectionTypeMetadata
}

// legacyProcesses projects every legacy election of an organization onto the /processes read shape.
// An error, never a short list: a dropped record would understate the count reported with it.
// ponytail: unpaginated — the legacy set is closed and every election is cached after the first read.
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

// legacyProcessByID resolves one legacy record by bundle/process ObjectID or by on-chain election id.
// A nil response with a nil error means no legacy record owns the id.
func (a *API) legacyProcessByID(ctx context.Context, raw string) (*apicommon.VotingProcessResponse, error) {
	src, err := a.legacyProcessSourceByID(raw)
	if err != nil || src == nil {
		return nil, err
	}
	return a.projectLegacyProcess(ctx, src)
}

// legacyProcessResultsByID projects one legacy record's per-question tallies onto the /processes
// results shape. The projection already carries every question's chain tally, so this is the same
// read reshaped. A nil response with a nil error means no legacy record owns the id.
func (a *API) legacyProcessResultsByID(ctx context.Context, raw string) (*apicommon.VotingProcessResultsResponse, error) {
	resp, err := a.legacyProcessByID(ctx, raw)
	if err != nil || resp == nil {
		return nil, err
	}
	entries := make([]apicommon.VotingProcessQuestionResults, 0, len(resp.Questions))
	for i := range resp.Questions {
		q := &resp.Questions[i]
		if q.Results == nil {
			continue
		}
		entries = append(entries, apicommon.VotingProcessQuestionResults{
			QuestionID:      q.ID.Hex(),
			UpstreamID:      q.UpstreamID,
			QuestionResults: *q.Results,
		})
	}
	return &apicommon.VotingProcessResultsResponse{ID: resp.ID, Questions: entries}, nil
}

// legacyProjectionError maps a projection failure onto its API error: a failed chain read is 50004,
// anything else a storage failure.
func legacyProjectionError(err error) errors.Error {
	if stderrors.Is(err, errLegacyChainUnavailable) {
		return errors.ErrVochainRequestFailed.WithErr(err)
	}
	return errors.ErrGenericInternalServerError.WithErr(err)
}

// legacyProcessSources collects an organization's legacy records, deduped by on-chain election id.
// A db.Process row wins over a bundle registering the same election: it stores the election
// parameters. Elections already served by the /processes path are skipped entirely.
func (a *API) legacyProcessSources(org common.Address) ([]*legacyProcessSource, error) {
	processes, err := a.db.AllProcessesByOrg(org, db.PublishedOnly)
	if err != nil {
		return nil, fmt.Errorf("could not list legacy processes: %w", err)
	}
	bundles, err := a.db.ProcessBundlesByOrg(org)
	if err != nil {
		return nil, fmt.Errorf("could not list process bundles: %w", err)
	}
	// one lookup for the whole set: which elections the /processes path already serves.
	var electionIDs []internal.HexBytes
	for i := range processes {
		if len(processes[i].Address) > 0 {
			electionIDs = append(electionIDs, processes[i].Address)
		}
	}
	for _, bundle := range bundles {
		electionIDs = append(electionIDs, bundle.Processes...)
	}
	served, err := a.db.ServedUpstreamIDs(electionIDs)
	if err != nil {
		return nil, fmt.Errorf("could not look up questions by upstream id: %w", err)
	}

	// claimed starts as the served set: an election either path already owns is not projected again.
	claimed := served
	var out []*legacyProcessSource
	for i := range processes {
		p := &processes[i]
		if len(p.Address) == 0 || claimed[p.Address.String()] {
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
	for _, bundle := range bundles {
		var elections []internal.HexBytes
		for _, electionID := range bundle.Processes {
			if claimed[electionID.String()] {
				continue
			}
			claimed[electionID.String()] = true
			elections = append(elections, electionID)
		}
		// an empty bundle would project to a process with no questions, so it is not a record itself.
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

// legacyProcessSourceByID resolves one legacy record from the raw path id: a 24-hex ObjectID as a
// bundle then as a process row, anything else as an on-chain election id (its row, then the bundle
// registering it). A nil source with a nil error means no legacy record owns the id.
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
	// the row wins over a bundle registering the same election, as in legacyProcessSources, so the
	// single read and the list project the same record.
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

// bundleSource projects a bundle, dropping the elections already projected under another id: those
// the /processes path serves, and those a db.Process row owns (the list attributes them to the row).
// Returns nil when no election is left, so the bundle id answers 404.
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

// processSource projects a legacy db.Process row. Returns nil for an unpublished draft (nothing on
// chain to project) and for a row the /processes path already serves.
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

// servedByProcessesAPI reports whether an election already backs a /processes question, which the
// normal path lists and the projection must not duplicate.
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

// legacyElectionClaimedElsewhere reports whether an election is projected under an id other than the
// bundle registering it: a /processes question, or a db.Process row.
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

// projectLegacyProcess builds the read shape of one legacy record, flattening every question of
// every election it holds — so a multi-question election yields questions sharing one UpstreamID,
// which the Legacy flag warns about. Nil when it holds no question; a failed chain read is an error.
func (a *API) projectLegacyProcess(
	ctx context.Context, src *legacyProcessSource,
) (*apicommon.VotingProcessResponse, error) {
	if src == nil {
		return nil, nil
	}
	cacheKey := src.cacheKey()
	if cached, ok := a.legacyProjectionCache.Get(cacheKey); ok {
		// a shallow copy per caller: nothing downstream mutates the response or its slices.
		resp := *cached
		return &resp, nil
	}
	vp := &db.VotingProcess{ID: src.id, OrgAddress: src.orgAddress, Published: true}
	var questions []db.VotingProcessQuestion
	haveContent := false
	// cacheable while every election is final and resolved its content: a transient metadata or
	// chain miss must not be frozen into the cached record.
	cacheable := true
	for _, electionID := range src.elections {
		election, err := a.legacyElection(electionID)
		if err != nil {
			return nil, err
		}
		content := a.legacyContentOf(ctx, election, src.params)
		cacheable = cacheable && len(content.questions) > 0 && slices.Contains(legacyFinalStatuses, election.Status)
		// the container fields come from the first election that resolved content, all of them or
		// none, so a later election cannot overwrite half of them.
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
		questions = append(questions, legacyElectionQuestions(src.id, election, content)...)
	}
	if len(questions) == 0 {
		return nil, nil
	}
	resp := apicommon.VotingProcessResponseFromDB(vp, questions, src.census, a.account.ChainID())
	resp.Legacy = true
	resp.Census.TotalWeight = a.censusTotalWeight(src.census)
	if cacheable {
		cached := *resp
		a.legacyProjectionCache.Add(cacheKey, &cached)
	}
	return resp, nil
}

// cacheKey identifies a projected record by its id and the exact elections it spans, so a bundle
// that loses an election to another owner is never served its older, wider projection.
func (src *legacyProcessSource) cacheKey() string {
	var b strings.Builder
	b.WriteString(src.id.Hex())
	for _, electionID := range src.elections {
		b.WriteByte(':')
		b.WriteString(electionID.String())
	}
	return b.String()
}

// legacyElection returns an on-chain election, cached so the public list does not fan out to the
// Vochain on every anonymous hit: final ones for good, the rest for legacyLiveElectionTTL. ENDED is
// not final — it still moves to RESULTS when the tally is published — so it only gets the TTL.
func (a *API) legacyElection(electionID internal.HexBytes) (*dvoteapi.Election, error) {
	key := electionID.String()
	if cached, ok := a.electionCache.Get(key); ok {
		return cached, nil
	}
	if cached, ok := a.liveElectionCache.Get(key); ok {
		return cached, nil
	}
	election, err := a.account.Election(electionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", errLegacyChainUnavailable, key, err)
	}
	if slices.Contains(legacyFinalStatuses, election.Status) {
		a.electionCache.Add(key, election)
	} else {
		a.liveElectionCache.Add(key, election)
	}
	return election, nil
}

// legacyContentOf returns the content of a legacy election: the stored ElectionParams when the row
// kept questions (no metadata fetch at all), the election's own metadata otherwise. Params without
// questions are not content, so such a row is projected from the chain like a bundle-backed one.
func (a *API) legacyContentOf(
	ctx context.Context, election *dvoteapi.Election, params *db.ElectionParams,
) legacyContent {
	if params != nil && len(params.Questions) > 0 {
		typeMetadata := params.TypeMetadata
		if typeMetadata == nil {
			// account.BuildElectionMetadata stamped this default on the published document, so the row
			// declares the same block the chain-metadata path reads back.
			typeMetadata = &db.ElectionTypeMetadata{Name: account.DefaultElectionType}
		}
		return legacyContent{
			title:        params.Title,
			description:  params.Description,
			header:       params.Header,
			streamURI:    params.StreamURI,
			questions:    params.Questions,
			typeMetadata: typeMetadata,
		}
	}
	meta := a.legacyElectionMetadata(ctx, election)
	if meta == nil {
		return legacyContent{}
	}
	content := legacyContent{
		title:       db.MultiLangString(meta.Title),
		description: db.MultiLangString(meta.Description),
		header:      meta.Media.Header,
		streamURI:   meta.Media.StreamURI,
		questions:   legacyMetaQuestions(meta.Questions),
	}
	// the inverse of account.BuildElectionMetadata, which writes this same block from
	// ElectionParams.TypeMetadata (and stamps a default name when the row carried none).
	if meta.Type.Name != "" {
		content.typeMetadata = &db.ElectionTypeMetadata{Name: meta.Type.Name, Properties: meta.Type.Properties}
	}
	return content
}

// legacyElectionMetadata decodes an election's metadata document into the typed shape (a JSON
// round-trip, as Election.Metadata is typed any — the same decode as processInfoHandler). Returns
// nil when the document cannot be resolved or decoded.
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
// only for ipfs://, so any other reference is resolved as resolveProcessMetadata does: our own
// object storage locally, http(s) externally. A bundle-backed election has no other content.
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
		// an ipfs:// reference with nothing inlined, or none at all: no document to read.
		return nil
	}
}

// legacyMetaQuestions converts on-chain metadata questions into the db shape — the inverse of
// account.BuildElectionMetadata. The types are the same maps, so no field is lost or invented.
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

// legacyElectionQuestions projects one legacy election onto the new-format questions: one per ballot
// question, all carrying the election id as UpstreamID and its status. A question also carries the
// ballot parameters the chain ran wherever the election's ballot describes a single question, plus a
// named type only when those parameters are exactly one (account.QuestionTypeFromBallotProtocol).
func legacyElectionQuestions(
	processID bson.ObjectID, election *dvoteapi.Election, content legacyContent,
) []db.VotingProcessQuestion {
	tally := questionResultsFromElection(election)
	protocol := legacyBallotProtocol(election, len(content.questions))
	out := make([]db.VotingProcessQuestion, 0, len(content.questions))
	for i, q := range content.questions {
		question := db.VotingProcessQuestion{
			ID:          legacyQuestionID(internal.HexBytes(election.ElectionID), i),
			ProcessID:   processID,
			Order:       i,
			Title:       q.Title,
			Description: q.Description,
			Choices:     q.Choices,
			UpstreamID:  internal.HexBytes(election.ElectionID),
			Status:      election.Status,
			Results:     legacyQuestionResults(election, tally, i, len(content.questions)),
		}
		if protocol != nil {
			bp := *protocol // one copy per question, so a caller cannot alias the whole ballot
			question.BallotProtocol = &bp
			declared := legacyCorroboratedType(content.typeMetadata, &bp, content.questions)
			if declared != nil {
				// the same free-form bag the authoring path carries from the request, under the key
				// the metadata document itself uses, so a client reads one shape on either path.
				question.Metadata = map[string]any{"type": declared}
			}
			if shape, ok := legacyNamedShape(&bp, q.Choices); ok {
				question.Type, question.TypeSetup, question.BallotProtocol = shape.Type, shape.TypeSetup, shape.Protocol
			} else if setup, ok := legacyTypeSetupFromProperties(declared); ok {
				question.TypeSetup = setup
			}
		}
		if election.VoteMode.EnvelopeType != nil {
			question.SecretUntilTheEnd = election.VoteMode.EncryptedVotes
		}
		out = append(out, question)
	}
	return out
}

// legacyBallotProtocol returns the on-chain ballot parameters of one projected question, or nil when the
// election's ballot describes no single question. It is the inverse of the publish-time map in
// account.(*Account).BuildNewProcessTx, reading the ballot description rather than the published tally.
func legacyBallotProtocol(election *dvoteapi.Election, questions int) *db.BallotProtocol {
	// TallyMode and VoteMode are wrappers over an embedded protobuf pointer: reading a field panics
	// when it is nil.
	if questions == 0 || election.TallyMode.ProcessVoteOptions == nil {
		return nil
	}
	bp := &db.BallotProtocol{
		MaxCount:          election.TallyMode.MaxCount,
		MaxValue:          election.TallyMode.MaxValue,
		MaxVoteOverwrites: election.TallyMode.MaxVoteOverwrites,
		MaxTotalCost:      election.TallyMode.MaxTotalCost,
		CostExponent:      election.TallyMode.CostExponent,
	}
	switch {
	case questions == 1:
		// the whole ballot belongs to the only question, tally mode included
	case int(election.TallyMode.MaxCount) == questions:
		// one ballot field per question: each question owns a single field of the ballot. A cost cap or
		// uniqueValues constrains the fields jointly, so it is no one question's to state.
		if bp.MaxTotalCost > 0 || election.VoteMode.EnvelopeType != nil &&
			(election.VoteMode.UniqueValues || election.VoteMode.CostFromWeight) {
			return nil
		}
		bp.MaxCount = 1
	default:
		// any other shape states nothing per question
		return nil
	}
	if election.VoteMode.EnvelopeType != nil {
		bp.UniqueValues = election.VoteMode.UniqueValues
		bp.CostFromWeight = election.VoteMode.CostFromWeight
	}
	return bp
}

// legacyCorroboratedType returns the metadata "type" block to expose, or nil when the ballot the chain
// ran contradicts its declared name. Elections the SaaS API published stamp single-choice-multiquestion
// whatever their ballot, and a client reads the declared name in preference to the protocol, so passing
// an uncorroborated name through turns missing numbers into wrong ones.
//
// The ballot's maxValue is election-wide — the SDK sizes it to the widest question — so it is checked
// against the highest choice value across every question, not the one being projected. The other
// names only ever describe a single-question ballot, where the two are the same.
func legacyCorroboratedType(
	tm *db.ElectionTypeMetadata, bp *db.BallotProtocol, questions []db.Question,
) *db.ElectionTypeMetadata {
	if tm == nil || bp == nil || len(questions) == 0 {
		return nil
	}
	var maxChoiceValue uint32
	for i := range questions {
		maxChoiceValue = max(maxChoiceValue, account.MaxChoiceValue(questions[i].Choices))
	}
	single := len(questions) == 1
	switch tm.Name {
	case account.DefaultElectionType:
		if bp.MaxCount == 1 && bp.MaxValue == maxChoiceValue {
			return tm
		}
	case "multiple-choice":
		// a single pick still runs multichoice when the SDK reserved an abstain value past the last choice
		if single && bp.MaxValue >= maxChoiceValue && (bp.MaxCount > 1 || bp.MaxValue > maxChoiceValue) {
			return tm
		}
	case "approval":
		if single && bp.MaxValue == 1 && int(bp.MaxCount) == len(questions[0].Choices) {
			return tm
		}
	case "budget-based", "quadratic":
		if single && bp.MaxValue == 0 {
			return tm
		}
	default:
		// an unknown name states nothing the ballot can corroborate
	}
	return nil
}

// legacyNamedShape recognises the named type a question's ballot runs, as
// account.QuestionTypeFromBallotProtocol does, but through a costExponent the chain never read: it only
// applies with a cost cap (maxTotalCost > 0 or costFromWeight), and elections published outside the
// draft flow stamp one regardless (the record behind the projection carries 1). Without that, an exact
// comparison would deny such a ballot the name it runs. The shape's Protocol is the canonical one, so
// Type still re-derives it exactly; it differs from the chain's only in that inert field.
func legacyNamedShape(bp *db.BallotProtocol, choices []db.Choice) (account.BallotShape, bool) {
	candidates := []db.BallotProtocol{*bp}
	if bp.MaxTotalCost == 0 && !bp.CostFromWeight {
		for _, exponent := range []uint32{0, 1} { // the only exponents a named uncapped shape derives
			if exponent != bp.CostExponent {
				alt := *bp
				alt.CostExponent = exponent
				candidates = append(candidates, alt)
			}
		}
	}
	for i := range candidates {
		if qType, setup, ok := account.QuestionTypeFromBallotProtocol(&candidates[i], choices); ok {
			return account.BallotShape{Type: qType, TypeSetup: setup, Protocol: &candidates[i]}, true
		}
	}
	return account.BallotShape{}, false
}

// legacyTypeSetupFromProperties reads a question's own bounds out of a corroborated metadata type block:
// numChoices.min/max and repeatChoice, the only properties that describe the shape rather than the
// display. Reports false — leaving TypeSetup zero next to the protocol — for a block carrying neither.
func legacyTypeSetupFromProperties(tm *db.ElectionTypeMetadata) (db.QuestionTypeSetup, bool) {
	if tm == nil {
		return db.QuestionTypeSetup{}, false
	}
	props, ok := tm.Properties.(map[string]any)
	if !ok {
		return db.QuestionTypeSetup{}, false
	}
	setup, found := db.QuestionTypeSetup{}, false
	if numChoices, ok := props["numChoices"].(map[string]any); ok {
		if minChoices, ok := legacyPropertyUint32(numChoices["min"]); ok {
			setup.MinChoices, found = minChoices, true
		}
		if maxChoices, ok := legacyPropertyUint32(numChoices["max"]); ok {
			setup.MaxChoices, found = maxChoices, true
		}
	}
	if repeatChoice, ok := props["repeatChoice"].(bool); ok {
		setup.UniqueChoices, found = !repeatChoice, true
	}
	// keep the stored invariant MinChoices <= MaxChoices when the document states a bounded maximum.
	if setup.MaxChoices > 0 {
		setup.MinChoices = min(setup.MinChoices, setup.MaxChoices)
	}
	return setup, found
}

// legacyPropertyUint32 reads a metadata property as a count. The block is untyped either way it reaches
// us: a float64 through the json round-trip in legacyElectionMetadata, a bson integer from the stored
// ElectionParams. Anything else, or a negative, is not a count.
func legacyPropertyUint32(v any) (uint32, bool) {
	var n int64
	switch value := v.(type) {
	case float64:
		// a fraction is not a count, and the range check keeps the int64 conversion defined.
		if value != math.Trunc(value) || value < 0 || value > math.MaxUint32 {
			return 0, false
		}
		n = int64(value)
	case int32:
		n = int64(value)
	case int64:
		n = value
	case int:
		n = int64(value)
	default:
		return 0, false
	}
	if n < 0 || n > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// legacyQuestionID derives a projected question's id from the election id and ballot order: distinct
// per question and stable across reads, where the zero ObjectID it would otherwise carry repeats.
func legacyQuestionID(electionID internal.HexBytes, order int) bson.ObjectID {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s/%d", electionID.String(), order))
	var id bson.ObjectID
	copy(id[:], sum[:])
	return id
}

// legacyQuestionResults splits an election's tally (one row per ballot field) across its n questions:
// a single question owns the whole matrix, a one-field-per-question ballot gives question i row i.
// Any other shape has no defined mapping, so the counts are reported without the matrix.
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

// filterLegacyProcessesByStatus keeps the processes with at least one question in the given status,
// mirroring what ListVotingProcesses's status filter does on the stored collection.
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

// readVotingProcess resolves a stored voting process by its own ObjectID or by the on-chain election
// id of one of its questions — a read-only alias, as writes keep going through votingProcessID.
// db.ErrNotFound means no stored process owns the id, whatever its shape (try the legacy projection).
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

// isVotingProcessIDShape reports whether the raw path id is well-formed — a 24-hex ObjectID or a
// 32-byte on-chain election id — so an id nothing owns answers 404 and malformed input stays 400.
func isVotingProcessIDShape(raw string) bool {
	if _, err := bson.ObjectIDFromHex(raw); err == nil {
		return true
	}
	var electionID internal.HexBytes
	return electionID.ParseString(raw) == nil && len(electionID) == 32
}
