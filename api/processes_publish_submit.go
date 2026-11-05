package api

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	dvoteapi "go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/log"
)

// errPublishNotSettled answers an edit, a delete or a question census change of a draft while a
// publish tx it submitted is unresolved (resolvePublishTxs): 409 (40903), naming the way out.
var errPublishNotSettled = errors.ErrPublishInProgress.Withf(
	"a submitted election of the draft is not settled on chain yet; " +
		"publish the draft again (it adopts or resubmits at no new charge) or retry later")

const (
	// publishTxMempoolTTL is the number of blocks the node keeps a tx in its mempool before it is
	// dropped: vochain's transactionBlocksTTL (dvote vochain/app.go), 6 * 10 blocks.
	publishTxMempoolTTL = 60
	// publishTxExpiryMargin is the number of blocks past the TTL before a record none of whose
	// hashes is in a block is declared dead by height: room for a tx that reached the node later
	// than the height its record carries, or that a peer re-gossiped.
	publishTxExpiryMargin = 120
	// publishTxClockSkew is how much earlier than its record's submit time an election the record
	// created may have been created on chain: the service and the chain clocks are not the same.
	publishTxClockSkew = time.Hour
)

// publishTxChain is what resolvePublishTxs asks the chain: *account.Account in production, a fake
// in tests (API.publishChainOverride).
type publishTxChain interface {
	AccountNonce(org common.Address) (uint32, error)
	ChainHeight() (uint32, error)
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

// publishTxHashes lists the hashes of the records the questions hold, for logs.
func publishTxHashes(questions []db.VotingProcessQuestion) []string {
	var hashes []string
	for i := range questions {
		for _, tx := range questions[i].PublishTxs {
			hashes = append(hashes, tx.Hash.String())
		}
	}
	return hashes
}

// publishResolution is what resolvePublishTxs found.
type publishResolution struct {
	// unresolved reports that some question is left unresolved or the process flag stays set
	unresolved bool
	// accountNonce is the organization's account nonce the resolution read, valid when nonceRead:
	// a publish round that submits next reuses it instead of reading it again. An adoption clears
	// nonceRead: the adopted tx may have consumed its nonce after accountNonce was read.
	accountNonce uint32
	nonceRead    bool
}

// asUnresolved returns r marked unresolved.
func (r publishResolution) asUnresolved() publishResolution {
	r.unresolved = true
	return r
}

// resolvePublishTxs settles, against the chain, the submitted publish txs recorded on the
// questions of vp that have no election yet (db.PublishTx). Per question: a mined record is
// adopted (its election recorded on the question, as persistPublished does); records none of
// which mined are dead and dropped once the account nonce moved past theirs, or once the chain is
// publishTxMempoolTTL + publishTxExpiryMargin blocks past the height they were submitted at (the
// mempool dropped them); anything else is unresolved. Once no question holds records, vp's
// PublishSubmitted flag is cleared.
//
// It reports unresolved when any question is left unresolved or the flag stays set; a chain that
// cannot answer, or no chain at all (the cmd/userdel path), leaves every record unresolved, never
// dead. A flag set with no record has nothing in flight and is cleared without a chain. err is a
// storage failure, which leaves things unresolved too. vp and questions are updated in place. The
// caller holds the publish claim: that is what keeps another replica's publish from writing
// records meanwhile. It runs on the caller's goroutine and never takes a tx worker.
func (a *API) resolvePublishTxs(vp *db.VotingProcess, questions []db.VotingProcessQuestion) (publishResolution, error) {
	var held []*db.VotingProcessQuestion
	for i := range questions {
		if len(questions[i].PublishTxs) > 0 && len(questions[i].UpstreamID) == 0 {
			held = append(held, &questions[i])
		}
	}
	var res publishResolution
	if len(held) == 0 && !vp.PublishSubmitted {
		return res, nil
	}
	if len(held) > 0 {
		chain := a.publishChain()
		if chain == nil {
			return res.asUnresolved(), nil
		}
		// the nonce is read before any hash: on one node the indexer commits before the state, so
		// a nonce past a record's means the block that consumed it is already indexed
		accNonce, err := chain.AccountNonce(vp.OrgAddress)
		if err != nil {
			log.Warnw("could not read the account nonce to resolve submitted publish txs",
				"processId", vp.ID.Hex(), "error", err)
			return res.asUnresolved(), nil
		}
		res.accountNonce, res.nonceRead = accNonce, true
		// the height is read at most once, and only when a question needs it
		chainHeight := sync.OnceValues(chain.ChainHeight)
		for _, q := range held {
			settled, err := a.resolveQuestionPublishTxs(questionResolution{
				chain: chain, vp: vp, q: q, accountNonce: accNonce, chainHeight: chainHeight,
			})
			if err != nil {
				return res.asUnresolved(), err
			}
			if !settled {
				res.unresolved = true
			}
			if len(q.UpstreamID) > 0 {
				// adopted: its tx may have been mined between the nonce read and the hash lookup (a
				// tx of this worker's that outlived WaitTxMined), so the nonce read is not reused
				res.nonceRead = false
			}
		}
	}
	if res.unresolved {
		return res, nil
	}
	cleared, err := a.db.ClearVotingProcessPublishSubmitted(vp.ID)
	if err != nil {
		return res.asUnresolved(), err
	}
	if !cleared {
		return res.asUnresolved(), nil
	}
	vp.PublishSubmitted = false
	return res, nil
}

// questionResolution is one held question of a process under resolution, with the chain to ask
// and what was already read from it.
type questionResolution struct {
	chain publishTxChain
	vp    *db.VotingProcess
	q     *db.VotingProcessQuestion
	// accountNonce is the organization's account nonce, read before any hash lookup
	accountNonce uint32
	// chainHeight reads the current chain height, memoized across the questions of a resolution
	chainHeight func() (uint32, error)
}

// publishTxsExpired reports whether every record of q was submitted so long ago, in blocks, that
// the node's mempool has dropped it: the chain is past publishTxMempoolTTL + publishTxExpiryMargin
// blocks after the newest submit height. Height, not wall-clock time, so a halted chain never
// expires a tx the mempool still holds. A record without a submit height never expires, and an
// unreadable height is an error: not expired.
func (r questionResolution) publishTxsExpired() (bool, error) {
	var newest uint32
	for _, tx := range r.q.PublishTxs {
		if tx.SubmittedHeight == 0 {
			return false, nil
		}
		newest = max(newest, tx.SubmittedHeight)
	}
	height, err := r.chainHeight()
	if err != nil {
		return false, fmt.Errorf("could not read the chain height: %w", err)
	}
	return height > newest+publishTxMempoolTTL+publishTxExpiryMargin, nil
}

// resolveQuestionPublishTxs resolves one question's records (see resolvePublishTxs), reporting
// whether it is settled: adopted or dead.
func (a *API) resolveQuestionPublishTxs(r questionResolution) (bool, error) {
	vp, q := r.vp, r.q
	nonce := q.PublishTxs[0].Nonce
	lookupFailed := false
	for i := range q.PublishTxs {
		mined, err := r.chain.TxMined(q.PublishTxs[i].Hash)
		if err != nil {
			log.Warnw("could not look up a submitted publish tx", "processId", vp.ID.Hex(),
				"questionId", q.ID.Hex(), "hash", q.PublishTxs[i].Hash.String(), "error", err)
			lookupFailed = true
			continue
		}
		if mined {
			return a.adoptPublishTx(r, q.PublishTxs[i])
		}
	}
	// only "not found" answers from every lookup prove that none of them can mine, and only once
	// the nonce was consumed by some other tx (a nonce is consumed once, and they all share it) or
	// the mempool has long dropped them (they never reached a block in its TTL)
	if lookupFailed {
		return false, nil
	}
	if r.accountNonce <= nonce {
		expired, err := r.publishTxsExpired()
		if err != nil {
			log.Warnw("could not tell whether submitted publish txs expired", "processId", vp.ID.Hex(),
				"questionId", q.ID.Hex(), "error", err)
			return false, nil
		}
		if !expired {
			return false, nil
		}
	}
	if err := a.db.ClearQuestionPublishTxs(q.ID, nonce); err != nil {
		return false, err
	}
	log.Infow("submitted publish txs can never mine, released", "processId", vp.ID.Hex(),
		"questionId", q.ID.Hex(), "nonce", nonce, "accountNonce", r.accountNonce)
	q.PublishTxs = nil
	return true, nil
}

// adoptPublishTx records on r.q the election the mined record tx created, as persistPublished
// does. It reports false when that election cannot be found yet.
func (a *API) adoptPublishTx(r questionResolution, tx db.PublishTx) (bool, error) {
	vp, q := r.vp, r.q
	// the tx mined, so its election exists: flag the process before anything else, so no edit can
	// replace the question from here on
	if err := a.db.MarkVotingProcessMined(vp.ID); err != nil {
		return false, err
	}
	vp.HasMinedElections = true
	electionID, err := a.minedElectionID(r, tx)
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

// minedElectionID finds the election a mined publish tx of r.q created: the id the node predicted
// when its metadata URL matches the record's, else the newest election of the organization that
// carries that metadata URL. It returns nil, nil when the listing ends, or reaches elections older
// than the question's records, with no match.
//
// The metadata URL identifies the question: the metadata document carries the question id
// (db.ElectionParams.Meta, set in electionParamsForQuestion), so no two questions share a URL, and
// an election already recorded on a question is never a match.
//
// The listing is newest first, and a record is written before its tx is submitted, so the walk
// stops at the first election created more than publishTxClockSkew before the question's oldest
// record: the election looked for cannot be past it. The creation time comes from each election
// read; on a page of elections all recorded already (the usual history), the oldest one is read
// for it, so the walk costs at most one election read per page beyond those of the candidates.
func (a *API) minedElectionID(r questionResolution, tx db.PublishTx) (internal.HexBytes, error) {
	chain := r.chain
	cutoff := r.q.PublishTxs[0].SubmittedAt
	for _, rec := range r.q.PublishTxs[1:] {
		if rec.SubmittedAt.Before(cutoff) {
			cutoff = rec.SubmittedAt
		}
	}
	cutoff = cutoff.Add(-publishTxClockSkew)
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
		ids, err := chain.OrgElectionIDs(r.vp.OrgAddress, page)
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
		for i, id := range ids {
			if served[id.String()] && i < len(ids)-1 {
				continue
			}
			e, err := chain.Election(id)
			if err != nil {
				return nil, err
			}
			if !served[id.String()] && e.MetadataURL == tx.MetadataURL {
				return id, nil
			}
			// a missing creation time cannot end the walk
			if !e.CreationTime.IsZero() && e.CreationTime.Before(cutoff) {
				return nil, nil
			}
		}
	}
}
