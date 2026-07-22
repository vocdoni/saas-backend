package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/proto/build/go/models"
)

// TestProcessesEncryptionKeys exercises the per-question encryptionKeys field of the new
// /processes model:
//   - a published secretUntilTheEnd question exposes its election's on-chain encryption public keys
//     on both GET /processes/{id} (manager) and GET /processes/{id}/questions/{qId} (public voter),
//     and they are cached on the stored question once resolved;
//   - a non-encrypted question leaves the field absent (and the handler stays chain-free for it).
func TestProcessesEncryptionKeys(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, "enckeyspass123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateOrganization(t, token)

	// subscribe the org to a plan so NEW_PROCESS is permitted
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
	orgName := fmt.Sprintf("encorg-%d", internal.RandomInt(1000))
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

	// publish one ENCRYPTED on-chain election (its own election, as each question is in the new model)
	processNonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	processTx := &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
		Txtype: models.TxType_NEW_PROCESS,
		Nonce:  processNonce,
		Process: &models.Process{
			EntityId:      orgAddress.Bytes(),
			Duration:      120,
			Status:        models.ProcessStatus_READY,
			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
			CensusRoot:    cspPubKey,
			MaxCensusSize: 10,
			EnvelopeType:  &models.EnvelopeType{EncryptedVotes: true},
			VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 5},
			Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
		},
	}}}
	encElection := internal.HexBytes(signRemoteSignerAndSendVocdoniTx(t, processTx, token, vocdoniClient, orgAddress))

	// wait until the keykeepers publish the election encryption keys on chain
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	nodeKeys, err := vocdoniClient.WaitUntilElectionKeys(ctx, encElection.Bytes())
	cancel()
	c.Assert(err, qt.IsNil)
	c.Assert(nodeKeys.PublicKeys, qt.Not(qt.HasLen), 0)

	// seed a published voting process with an encrypted question (points at the election above) and a
	// non-encrypted one; the plain question keeps a distinct upstreamId to prove the secretUntilTheEnd
	// gate short-circuits before any chain call.
	vpID, err := testDB.SetVotingProcess(&db.VotingProcess{
		OrgAddress: orgAddress, Published: true,
		Title: db.MultiLangString{"default": "Enc process"},
	})
	c.Assert(err, qt.IsNil)
	qEncID, err := testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: orgAddress, Order: 0,
		Title: db.MultiLangString{"default": "Q enc"}, UpstreamID: encElection,
		Status: db.QuestionStatusReady, SecretUntilTheEnd: true,
	})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&db.VotingProcessQuestion{
		ProcessID: vpID, OrgAddress: orgAddress, Order: 1,
		Title: db.MultiLangString{"default": "Q plain"}, UpstreamID: internal.HexBytes{0x01, 0x02, 0x03, 0x04},
		Status: db.QuestionStatusReady, SecretUntilTheEnd: false,
	})
	c.Assert(err, qt.IsNil)

	assertMatchesNode := func(who string, keys []db.EncryptionKey) {
		c.Assert(keys, qt.HasLen, len(nodeKeys.PublicKeys), qt.Commentf("%s key count", who))
		for i, k := range nodeKeys.PublicKeys {
			c.Assert(keys[i].Index, qt.Equals, k.Index, qt.Commentf("%s key %d index", who, i))
			c.Assert(keys[i].Key.String(), qt.Equals, k.Key.String(), qt.Commentf("%s key %d value", who, i))
		}
	}

	// GET /processes/{id} (manager read): the encrypted question carries the keys, the plain one none.
	info := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", vpID.Hex())
	c.Assert(info.Questions, qt.HasLen, 2)
	assertMatchesNode("info.questions[0]", info.Questions[0].EncryptionKeys)
	c.Assert(info.Questions[1].EncryptionKeys, qt.HasLen, 0)

	// GET /processes/{id}/questions/{qId} (public voter read): same keys present.
	pub := requestAndParse[apicommon.PublicQuestionResponse](
		t, http.MethodGet, "", nil, "processes", vpID.Hex(), "questions", qEncID.Hex())
	assertMatchesNode("public question", pub.EncryptionKeys)

	// keys are immutable once published, so they are cached on the stored question.
	stored, err := testDB.Question(qEncID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.EncryptionKeys, qt.HasLen, len(nodeKeys.PublicKeys))
}
