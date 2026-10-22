package api

import (
	"bytes"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
)

// orphanElectionWait is how long a publish round that found unattributed organization elections
// waits before the next round looks for them again.
const orphanElectionWait = 2 * time.Second

// maxOrphanRounds bounds the publish rounds that wait for unattributed organization elections:
// elections another publish of the organization minted can never be attributed by this one, so
// after maxOrphanRounds rounds the publish proceeds.
const maxOrphanRounds = 3

// pendingQuestions returns the questions that have no election yet: neither persisted on chain nor
// mined with their DB write pending.
func (pw *publishWorker) pendingQuestions() []*db.VotingProcessQuestion {
	pending := make([]*db.VotingProcessQuestion, 0, len(pw.questions))
	for i := range pw.questions {
		q := &pw.questions[i]
		if _, mined := pw.minedUnpersisted[q.ID]; mined {
			continue // already on chain, only the DB write is pending
		}
		if len(q.UpstreamID) == 0 {
			pending = append(pending, q)
		}
	}
	return pending
}

// assignCensus sets the census origin and root of q's election, as the CSP derives them from the
// question id and the census mode, and returns the origin.
func (pw *publishWorker) assignCensus(q *db.VotingProcessQuestion) (models.CensusOrigin, error) {
	origin, root, err := pw.a.csp.ElectionCensus(q.ID, pw.census.Anonymous)
	if err != nil {
		return 0, fmt.Errorf("could not derive the census of question %s: %w", q.ID.Hex(), err)
	}
	q.CensusOrigin = electionkey.OriginName(origin)
	q.CensusRoot = root
	return origin, nil
}

// initElectionIndex reads the organization's election index before this worker submits anything.
// It also adopts the elections an earlier publish of this process left unrecorded: one that mined
// after that publish gave up is among the organization's last len(questions) elections.
func (pw *publishWorker) initElectionIndex() error {
	index, err := pw.a.account.AccountElectionIndex(pw.vp.OrgAddress)
	if err != nil {
		return fmt.Errorf("could not read organization election index: %w", err)
	}
	pw.knownElectionIndex = index
	pw.electionIndexRead = true
	from := uint32(0)
	if lookback := uint32(len(pw.questions)); index > lookback {
		from = index - lookback
	}
	if _, err := pw.adoptElections(from, index); err != nil {
		return fmt.Errorf("could not look for unrecorded elections: %w", err)
	}
	return nil
}

// adoptOrphanElections records the elections that mined without this worker seeing them confirmed
// (a lost batch response, or a tx that mined after its confirmation timed out). It reports false
// while the organization has created more elections than the worker can attribute, for example
// because the node does not serve a new election yet: submitting then could create a second
// election for a question that already has one.
func (pw *publishWorker) adoptOrphanElections() (bool, error) {
	if !pw.electionIndexRead {
		return true, nil // nothing was submitted before the index was read
	}
	index, err := pw.a.account.AccountElectionIndex(pw.vp.OrgAddress)
	if err != nil {
		return false, fmt.Errorf("could not read organization election index: %w", err)
	}
	if index <= pw.knownElectionIndex {
		return true, nil
	}
	adopted, err := pw.adoptElections(pw.knownElectionIndex, index)
	if err != nil {
		return false, err
	}
	pw.knownElectionIndex += uint32(adopted)
	return pw.knownElectionIndex >= index, nil
}

// adoptElections looks for the elections of the pending questions among the organization's
// elections with an index in [from, to), and records each one found for its question. It returns
// how many it adopted.
func (pw *publishWorker) adoptElections(from, to uint32) (int, error) {
	pending := pw.pendingQuestions()
	if len(pending) == 0 || from >= to {
		return 0, nil
	}
	candidates := make([]orphanCandidate, 0, len(pending))
	for _, q := range pending {
		origin, err := pw.assignCensus(q)
		if err != nil {
			return 0, err
		}
		ep, err := electionParamsForQuestion(pw.vp, q, pw.census)
		if err != nil {
			return 0, err
		}
		ids := make([]internal.HexBytes, 0, to-from)
		for index := from; index < to; index++ {
			id, err := pw.a.account.ElectionID(&account.ElectionIDParams{
				OrgAddress: pw.vp.OrgAddress, CensusOrigin: origin, Params: ep, Index: index,
			})
			if err != nil {
				return 0, err
			}
			ids = append(ids, id)
		}
		candidates = append(candidates, orphanCandidate{question: q.ID, root: q.CensusRoot, ids: ids})
	}
	adopted, err := matchOrphanElections(candidates, pw.a.account.ElectionCensusRoot)
	if err != nil {
		return 0, err
	}
	for _, q := range pending {
		if id, ok := adopted[q.ID]; ok {
			log.Warnw("recording an election that mined unseen", "processId", pw.vp.ID.Hex(),
				"questionId", q.ID.Hex(), "electionId", id.String())
			pw.persistPublished(q, id)
		}
	}
	return len(adopted), nil
}

// orphanCandidate is a pending question, the census root of its election and the ids its election
// would have at each organization election index under consideration.
type orphanCandidate struct {
	question bson.ObjectID
	root     internal.HexBytes
	ids      []internal.HexBytes
}

// electionRootLookup returns the census root of an on-chain election; found is false when the
// node does not know the election.
type electionRootLookup func(id internal.HexBytes) (root internal.HexBytes, found bool, err error)

// matchOrphanElections returns the id of each candidate's existing election. An election belongs
// to a candidate only when its census root is the candidate's: a per-election root is derived from
// the question id, so no other question's election has it. Each id is looked up once and given to
// at most one candidate.
func matchOrphanElections(
	candidates []orphanCandidate, lookup electionRootLookup,
) (map[bson.ObjectID]internal.HexBytes, error) {
	roots := make(map[string]internal.HexBytes)
	looked := make(map[string]bool)
	taken := make(map[string]bool)
	adopted := make(map[bson.ObjectID]internal.HexBytes)
	for _, cand := range candidates {
		if len(cand.root) == 0 {
			continue
		}
		for _, id := range cand.ids {
			key := string(id)
			if !looked[key] {
				root, found, err := lookup(id)
				if err != nil {
					return nil, err
				}
				looked[key] = true
				if found {
					roots[key] = root
				}
			}
			if root, ok := roots[key]; ok && !taken[key] && bytes.Equal(root, cand.root) {
				adopted[cand.question] = id
				taken[key] = true
				break
			}
		}
	}
	return adopted, nil
}
