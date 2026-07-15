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

// TestStatusSync seeds two published questions in "ready", drives their elections to different
// on-chain statuses (one ended→results, one paused), runs the syncer, and asserts the stored
// question statuses converge to the chain (lowercased) and that a terminal question drops out of
// the syncable candidate set.
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

	// run one sync pass
	syncer := statussync.New(context.Background(), &statussync.Config{DB: testDB, Account: testAPI.account})
	changed, err := syncer.RunOnce(context.Background())
	c.Assert(err, qt.IsNil)
	// at least our two questions changed (the query is global; other orgs may also reconcile).
	c.Assert(changed >= 2, qt.IsTrue, qt.Commentf("changed=%d", changed))

	// stored statuses now mirror the chain (lowercased)
	qEnded, err := testDB.Question(qEndedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qEnded.Status, qt.Equals, db.QuestionStatusResults)
	c.Assert(qEnded.SyncedAt.IsZero(), qt.IsFalse)
	qPaused, err := testDB.Question(qPausedID)
	c.Assert(err, qt.IsNil)
	c.Assert(qPaused.Status, qt.Equals, db.QuestionStatusPaused)

	// the terminal (results) question drops out of the syncable set; the paused one remains.
	refs, err := testDB.QuestionsInSyncableStatus(context.Background())
	c.Assert(err, qt.IsNil)
	seen := map[string]string{}
	for _, r := range refs {
		seen[r.UpstreamID.String()] = r.Status
	}
	_, endedStillSyncable := seen[electionEnded.String()]
	c.Assert(endedStillSyncable, qt.IsFalse)
	c.Assert(seen[electionPaused.String()], qt.Equals, db.QuestionStatusPaused)

	// a second pass with nothing to change is a no-op
	changed, err = syncer.RunOnce(context.Background())
	c.Assert(err, qt.IsNil)
	c.Assert(changed, qt.Equals, 0)
}
