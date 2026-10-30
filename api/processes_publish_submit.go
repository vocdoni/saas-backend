package api

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// publishTxChain is what resolvePublishTxs asks the chain: *account.Account in production, a fake
// in tests (API.publishChainOverride).
type publishTxChain interface {
	AccountNonce(org common.Address) (uint32, error)
	TxMined(hash internal.HexBytes) (bool, error)
	Election(processID []byte) (*dvoteapi.Election, error)
	OrgElectionIDs(org common.Address, page int) ([]internal.HexBytes, error)
}

// publishChain returns the chain resolvePublishTxs asks, or nil when the API has none (the
// cmd/userdel path, built without an account).
func (a *API) publishChain() publishTxChain {
	if a.publishChainOverride != nil {
		return a.publishChainOverride
	}
	if a.account == nil {
		return nil
	}
	return a.account
}

// publishedQuestionStatus is the stored status of a question whose election was just recorded:
// the status the process publishes its elections with.
func publishedQuestionStatus(vp *db.VotingProcess) string {
	if vp.InitialStatus == db.QuestionStatusPaused {
		return db.QuestionStatusPaused
	}
	return db.QuestionStatusReady
}

// resolvePublishTxs settles, against the chain, the submitted publish txs recorded on the
// questions of vp that have no election yet (db.PublishTx). Per question: a mined record is
// adopted (its election recorded on the question, as persistPublished does); records none of
// which mined while the account nonce moved past theirs are dead and dropped; anything else is
// unresolved. Once no question holds records, vp's PublishSubmitted flag is cleared.
//
// It reports unresolved when any question is left unresolved or the flag stays set; a chain that
// cannot answer, or no chain at all (the cmd/userdel path), leaves every record unresolved, never
// dead. A flag set with no record has nothing in flight and is cleared without a chain. err is a
// storage failure, which leaves things unresolved too. vp and questions are updated in place. The
// caller holds the publish claim: that is what keeps another replica's publish from writing
// records meanwhile. It runs on the caller's goroutine and never takes a tx worker.
func (a *API) resolvePublishTxs(vp *db.VotingProcess, questions []db.VotingProcessQuestion) (bool, error) {
	var held []*db.VotingProcessQuestion
	for i := range questions {
		if len(questions[i].PublishTxs) > 0 && len(questions[i].UpstreamID) == 0 {
			held = append(held, &questions[i])
		}
	}
	if len(held) == 0 && !vp.PublishSubmitted {
		return false, nil
	}
	unresolved := false
	if len(held) > 0 {
		chain := a.publishChain()
		if chain == nil {
			return true, nil
		}
		// the nonce is read before any hash: on one node the indexer commits before the state, so
		// a nonce past a record's means the block that consumed it is already indexed
		accNonce, err := chain.AccountNonce(vp.OrgAddress)
		if err != nil {
			log.Warnw("could not read the account nonce to resolve submitted publish txs",
				"processId", vp.ID.Hex(), "error", err)
			return true, nil
		}
		for _, q := range held {
			settled, err := a.resolveQuestionPublishTxs(chain, vp, q, accNonce)
			if err != nil {
				return true, err
			}
			if !settled {
				unresolved = true
			}
		}
	}
	if unresolved {
		return true, nil
	}
	cleared, err := a.db.ClearVotingProcessPublishSubmitted(vp.ID)
	if err != nil {
		return true, err
	}
	if !cleared {
		return true, nil
	}
	vp.PublishSubmitted = false
	return false, nil
}

// resolveQuestionPublishTxs resolves one question's records (see resolvePublishTxs), reporting
// whether it is settled: adopted or dead.
func (a *API) resolveQuestionPublishTxs(
	chain publishTxChain, vp *db.VotingProcess, q *db.VotingProcessQuestion, accNonce uint32,
) (bool, error) {
	nonce := q.PublishTxs[0].Nonce
	lookupFailed := false
	for i := range q.PublishTxs {
		mined, err := chain.TxMined(q.PublishTxs[i].Hash)
		if err != nil {
			log.Warnw("could not look up a submitted publish tx", "processId", vp.ID.Hex(),
				"questionId", q.ID.Hex(), "hash", q.PublishTxs[i].Hash.String(), "error", err)
			lookupFailed = true
			continue
		}
		if mined {
			return a.adoptPublishTx(chain, vp, q, q.PublishTxs[i])
		}
	}
	// only "not found" answers from every lookup, with the nonce consumed by some other tx, prove
	// that none of them can ever mine (a nonce is consumed once, and they all share it)
	if lookupFailed || accNonce <= nonce {
		return false, nil
	}
	if err := a.db.ClearQuestionPublishTxs(q.ID, nonce); err != nil {
		return false, err
	}
	log.Infow("submitted publish txs can never mine, released", "processId", vp.ID.Hex(),
		"questionId", q.ID.Hex(), "nonce", nonce, "accountNonce", accNonce)
	q.PublishTxs = nil
	return true, nil
}

// adoptPublishTx records on q the election the mined record tx created, as persistPublished does.
// It reports false when that election cannot be found yet.
func (a *API) adoptPublishTx(
	chain publishTxChain, vp *db.VotingProcess, q *db.VotingProcessQuestion, tx db.PublishTx,
) (bool, error) {
	// the tx mined, so its election exists: flag the process before anything else, so no edit can
	// replace the question from here on
	if err := a.db.MarkVotingProcessMined(vp.ID); err != nil {
		return false, err
	}
	vp.HasMinedElections = true
	electionID, err := a.minedElectionID(chain, vp.OrgAddress, tx)
	if err != nil {
		log.Warnw("could not find the election of a mined publish tx", "processId", vp.ID.Hex(),
			"questionId", q.ID.Hex(), "hash", tx.Hash.String(), "error", err)
		return false, nil
	}
	if electionID == nil {
		log.Errorw(fmt.Errorf("mined publish tx has no matching election"),
			fmt.Sprintf("question %s of process %s: tx %s mined but no election of the organization carries "+
				"metadata %s; it needs manual recovery", q.ID.Hex(), vp.ID.Hex(), tx.Hash.String(), tx.MetadataURL))
		return false, nil
	}
	status := publishedQuestionStatus(vp)
	if err := a.db.SetQuestionPublished(q.ID, electionID, tx.MetadataURL, status); err != nil {
		return false, err
	}
	log.Infow("adopted the election of a submitted publish tx", "processId", vp.ID.Hex(),
		"questionId", q.ID.Hex(), "electionId", electionID.String(), "hash", tx.Hash.String())
	q.UpstreamID, q.MetadataURL, q.Status, q.PublishTxs = electionID, tx.MetadataURL, status, nil
	return true, nil
}

// minedElectionID finds the election a mined publish tx created: the id the node predicted when
// its metadata URL matches the record's, else the newest election of the organization that
// carries that metadata URL. It returns nil, nil when the listing ends with no match.
//
// The metadata URL identifies the question: the metadata document carries the question id
// (db.ElectionParams.Meta, set in electionParamsForQuestion), so no two questions share a URL, and
// an election already recorded on a question is never a match.
func (a *API) minedElectionID(chain publishTxChain, org common.Address, tx db.PublishTx) (internal.HexBytes, error) {
	if len(tx.UpstreamID) > 0 {
		// a predicted id that does not exist (an error here) falls back to the listing
		if e, err := chain.Election(tx.UpstreamID); err == nil && e.MetadataURL == tx.MetadataURL {
			served, err := a.db.ServedUpstreamIDs([]internal.HexBytes{tx.UpstreamID})
			if err != nil {
				return nil, err
			}
			if !served[tx.UpstreamID.String()] {
				return tx.UpstreamID, nil
			}
		}
	}
	for page := 0; ; page++ {
		ids, err := chain.OrgElectionIDs(org, page)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, nil
		}
		served, err := a.db.ServedUpstreamIDs(ids)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if served[id.String()] {
				continue
			}
			e, err := chain.Election(id)
			if err != nil {
				return nil, err
			}
			if e.MetadataURL == tx.MetadataURL {
				return id, nil
			}
		}
	}
}
