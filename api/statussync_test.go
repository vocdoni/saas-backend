package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/statussync"
	"go.vocdoni.io/proto/build/go/models"
)

// newSyncTestElection creates one READY on-chain election owned by orgAddress and returns its id.
func newSyncTestElection(
	t *testing.T, token string, orgAddress common.Address, censusRoot []byte,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	client := testNewVocdoniClient(t)
	nonce := fetchVocdoniAccountNonce(t, client, orgAddress)
	tx := &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
		Txtype: models.TxType_NEW_PROCESS,
		Nonce:  nonce,
		Process: &models.Process{
			EntityId:      orgAddress.Bytes(),
			Duration:      600,
			Status:        models.ProcessStatus_READY,
			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
			CensusRoot:    censusRoot,
			MaxCensusSize: 10,
			EnvelopeType:  &models.EnvelopeType{Anonymous: false, CostFromWeight: false},
			VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 5},
			Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
		},
	}}}
	id := signRemoteSignerAndSendVocdoniTx(t, tx, token, client, orgAddress)
	c.Assert(id, qt.Not(qt.HasLen), 0)
	return internal.HexBytes(id)
}

// setSyncTestElectionStatus submits a SET_PROCESS_STATUS tx moving an election to the given status.
func setSyncTestElectionStatus(
	t *testing.T, token string, orgAddress common.Address,
	processID internal.HexBytes, status models.ProcessStatus,
) {
	t.Helper()
	client := testNewVocdoniClient(t)
	nonce := fetchVocdoniAccountNonce(t, client, orgAddress)
	st := status
	tx := &models.Tx{Payload: &models.Tx_SetProcess{SetProcess: &models.SetProcessTx{
		Txtype:    models.TxType_SET_PROCESS_STATUS,
		Nonce:     nonce,
		ProcessId: processID.Bytes(),
		Status:    &st,
	}}}
	signRemoteSignerAndSendVocdoniTx(t, tx, token, client, orgAddress)
}

// TestStatusSync exercises the enqueue-driven syncer end to end against a live chain: a
// read-triggered reconcile converges a stored status to the chain; a status-change confirm refreshes
// syncedAt once the chain matches the target; and a confirm whose target never lands reconciles the
// optimistic value back to the real chain status.
func TestStatusSync(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateOrganization(t, token)

	// subscribe the organization to a plan so NEW_PROCESS is permitted
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(time.Hour * 24),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// create the organization account on-chain
	orgName := fmt.Sprintf("syncorg-%d", internal.RandomInt(1000))
	orgInfoURI := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))
	nonce := uint32(0)
	accountTx := &models.Tx{Payload: &models.Tx_SetAccount{SetAccount: &models.SetAccountTx{
		Nonce:   &nonce,
		Txtype:  models.TxType_CREATE_ACCOUNT,
		Account: orgAddress.Bytes(),
		Name:    &orgName,
		InfoURI: &orgInfoURI,
	}}}
	signRemoteSignerAndSendVocdoniTx(t, accountTx, token, vocdoniClient, orgAddress)

	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)

	// two READY elections
	electionEnded := newSyncTestElection(t, token, orgAddress, cspPubKey)
	electionPaused := newSyncTestElection(t, token, orgAddress, cspPubKey)

	// seed a published voting process with two questions pointing at the elections, both "ready"
	vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
		OrgAddress: orgAddress,
		Published:  true,
		Title:      db.MultiLangString{"default": "Sync process"}, //nolint:goconst
	})
	c.Assert(err, qt.IsNil)
	qEndedID, err := testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: orgAddress, Order: 0,
		Title: db.MultiLangString{"default": "Q ended"}, UpstreamID: electionEnded, Status: db.QuestionStatusReady,
	})
	c.Assert(err, qt.IsNil)
	qPausedID, err := testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: orgAddress, Order: 1,
		Title: db.MultiLangString{"default": "Q paused"}, UpstreamID: electionPaused, Status: db.QuestionStatusReady,
	})
	c.Assert(err, qt.IsNil)

	// drive the elections to distinct on-chain statuses
	setSyncTestElectionStatus(t, token, orgAddress, electionEnded, models.ProcessStatus_ENDED)
	waitForElectionStatus(t, electionEnded, "ENDED", "RESULTS")
	setSyncTestElectionStatus(t, token, orgAddress, electionPaused, models.ProcessStatus_PAUSED)
	waitForElectionStatus(t, electionPaused, "PAUSED")

	// maxAttempts == 1 (interval == confirmTimeout) makes ProcessPending deterministic: a confirm
	// resolves in a single pass instead of rescheduling.
	syncer := statussync.New(context.Background(), &statussync.Config{
		DB: testDB, Account: testAPI.account, Interval: time.Second, ConfirmTimeout: time.Second,
	})

	// --- read-triggered reconcile: stored READY converges to the chain status ---
	syncer.EnqueueReconcile(electionEnded, db.QuestionStatusReady)
	syncer.EnqueueReconcile(electionPaused, db.QuestionStatusReady)
	c.Assert(syncer.ProcessPending(), qt.Equals, 2)

	qEnded, err := testDB.Question(qEndedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qEnded.Status == db.QuestionStatusEnded || qEnded.Status == db.QuestionStatusResults, qt.IsTrue,
		qt.Commentf("status=%s", qEnded.Status))
	c.Assert(qEnded.SyncedAt.IsZero(), qt.IsFalse)
	qPaused, err := testDB.Question(qPausedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qPaused.Status, qt.Equals, db.QuestionStatusPaused)
	c.Assert(qPaused.SyncedAt.IsZero(), qt.IsFalse)

	// --- confirm success: chain already at the target only refreshes syncedAt, keeps the status ---
	before := qPaused.SyncedAt
	time.Sleep(5 * time.Millisecond)
	syncer.EnqueueConfirm(electionPaused, db.QuestionStatusPaused)
	c.Assert(syncer.ProcessPending(), qt.Equals, 1)
	qPaused, err = testDB.Question(qPausedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qPaused.Status, qt.Equals, db.QuestionStatusPaused)
	c.Assert(qPaused.SyncedAt.After(before), qt.IsTrue)

	// --- confirm give-up: an optimistic target that never lands is reconciled back to the chain ---
	c.Assert(testDB.SetQuestionStatus(qPausedID, db.QuestionStatusCanceled), qt.IsNil) // optimistic (wrong) write
	syncer.EnqueueConfirm(electionPaused, db.QuestionStatusCanceled)                   // chain stays PAUSED, never CANCELED
	c.Assert(syncer.ProcessPending(), qt.Equals, 1)
	qPaused, err = testDB.Question(qPausedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qPaused.Status, qt.Equals, db.QuestionStatusPaused)
}
