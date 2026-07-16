// Package statussync provides a background worker that reconciles each published voting-process
// question's stored on-chain status with the Vochain, so API reads (list, ?status= filter, the
// managed-org delete guard) can serve the stored status without per-request chain fan-out.
package statussync

import (
	"context"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

const (
	// defaultInterval is the wait between sync runs when Config.Interval is 0.
	defaultInterval = 60 * time.Second
	// defaultRunTimeout bounds a single sync run when Config.RunTimeout is 0.
	defaultRunTimeout = 5 * time.Minute
)

// Config wires the syncer's dependencies and tuning. Interval/RunTimeout of 0 use the defaults.
type Config struct {
	DB         *db.MongoStorage
	Account    *account.Account
	Interval   time.Duration
	RunTimeout time.Duration
}

// Syncer periodically reconciles question statuses with the chain. One run executes at a time:
// ticks that arrive while a run is in flight are dropped (never queued).
type Syncer struct {
	db       *db.MongoStorage
	account  *account.Account
	interval time.Duration
	runTTL   time.Duration
	ctx      context.Context
	running  atomic.Bool
}

// New builds a Syncer bound to ctx (cancelling ctx stops it). Zero Interval/RunTimeout default.
func New(ctx context.Context, c *Config) *Syncer {
	s := &Syncer{
		db:       c.DB,
		account:  c.Account,
		interval: c.Interval,
		runTTL:   c.RunTimeout,
		ctx:      ctx,
	}
	if s.interval <= 0 {
		s.interval = defaultInterval
	}
	if s.runTTL <= 0 {
		s.runTTL = defaultRunTimeout
	}
	return s
}

// Start launches the ticker loop and returns immediately. A CAS guard ensures a single run at a
// time; overlapping ticks are dropped. Each run is bounded by runTTL; ctx cancellation stops the
// loop (and aborts an in-flight run).
func (s *Syncer) Start() {
	log.Infow("starting question status syncer", "interval", s.interval.String(), "runTimeout", s.runTTL.String())
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				if !s.running.CompareAndSwap(false, true) {
					continue // a run is still in flight; drop this tick rather than queue it
				}
				go func() {
					defer s.running.Store(false)
					runCtx, cancel := context.WithTimeout(s.ctx, s.runTTL)
					defer cancel()
					if _, err := s.RunOnce(runCtx); err != nil {
						log.Warnw("question status sync run failed", "error", err.Error())
					}
				}()
			}
		}
	}()
}

// RunOnce performs one reconciliation pass: it reads every syncable question (one query), groups
// them by org, does one bulk election read per org, diffs chain vs stored status, and applies the
// differences in one bulk write. Returns the number of questions updated. Exported for tests.
func (s *Syncer) RunOnce(ctx context.Context) (int, error) {
	refs, err := s.db.QuestionsInSyncableStatus(ctx)
	if err != nil {
		return 0, fmt.Errorf("list syncable questions: %w", err)
	}
	if len(refs) == 0 {
		return 0, nil
	}

	// group candidates by owning org so each org needs a single (paged) chain read.
	byOrg := make(map[common.Address][]db.QuestionStatusRef)
	for _, r := range refs {
		byOrg[r.OrgAddress] = append(byOrg[r.OrgAddress], r)
	}

	var changes []db.QuestionStatusChange
	for org, orgRefs := range byOrg {
		if err := ctx.Err(); err != nil {
			// nothing is applied until SyncQuestionStatuses below, so report zero updates.
			return 0, err
		}
		chainStatus, err := s.electionStatusesByOrg(ctx, org)
		if err != nil {
			// isolate per-org failures: log and skip so other orgs still reconcile.
			log.Warnw("question status sync: skipping org", "org", org.Hex(), "error", err.Error())
			continue
		}
		for _, r := range orgRefs {
			cur, ok := chainStatus[hex.EncodeToString(r.UpstreamID)]
			if ok && cur != r.Status {
				changes = append(changes, db.QuestionStatusChange{UpstreamID: r.UpstreamID, NewStatus: cur})
			}
		}
	}

	if err := s.db.SyncQuestionStatuses(ctx, changes); err != nil {
		return 0, fmt.Errorf("apply status changes: %w", err)
	}
	log.Infow("question status sync run", "candidates", len(refs), "orgs", len(byOrg), "changed", len(changes))
	return len(changes), nil
}

// electionStatusesByOrg pages all of an org's on-chain elections and returns a map of
// hex(electionID) → chain status. Both chain and stored statuses are uppercase, so no
// normalization is needed.
func (s *Syncer) electionStatusesByOrg(ctx context.Context, org common.Address) (map[string]string, error) {
	out := make(map[string]string)
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		summaries, err := s.account.ElectionsByOrg(org, page)
		if err != nil {
			return nil, err
		}
		if len(summaries) == 0 {
			return out, nil
		}
		for i := range summaries {
			out[hex.EncodeToString(summaries[i].ElectionID)] = summaries[i].Status
		}
	}
}
