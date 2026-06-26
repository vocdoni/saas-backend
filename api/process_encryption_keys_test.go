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
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.vocdoni.io/proto/build/go/models"
)

// TestProcessEncryptionKeys exercises the encryptionKeys field of GET /process/{id}:
//   - an encrypted (secretUntilTheEnd) election exposes the election's on-chain encryption
//     public keys, and they are cached on the stored process once resolved;
//   - a non-encrypted election leaves the field absent (and the handler stays chain-free).
func TestProcessEncryptionKeys(t *testing.T) {
	c := qt.New(t)

	// create a user and organization
	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateOrganization(t, token)

	// subscribe the organization to a plan
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

	// newProcess publishes an on-chain election (encrypted or not) and stores the matching
	// SaaS process record, returning its Mongo ObjectID and on-chain id.
	newProcess := func(encrypted bool) (string, internal.HexBytes) {
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
				EnvelopeType:  &models.EnvelopeType{EncryptedVotes: encrypted},
				VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 5},
				Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
			},
		}}}
		addr := internal.HexBytes(signRemoteSignerAndSendVocdoniTx(t, processTx, token, vocdoniClient, orgAddress))
		objID, err := testDB.SetProcess(&db.Process{
			OrgAddress: orgAddress,
			Address:    addr,
			ElectionParams: &db.ElectionParams{
				Title:         db.MultiLangString{"default": "Encryption keys election"},
				EndDate:       time.Now().Add(2 * time.Hour),
				MaxCensusSize: 100,
				VoteType:      db.VoteType{MaxCount: 1, MaxValue: 1},
				ElectionType: db.ElectionType{
					Autostart:         true,
					Interruptible:     true,
					SecretUntilTheEnd: encrypted,
				},
			},
		})
		c.Assert(err, qt.IsNil)
		return objID.Hex(), addr
	}

	// --- encrypted election: keys must be exposed and cached ---
	encObjID, encAddr := newProcess(true)

	// wait until the keykeepers publish the election encryption keys on chain
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	nodeKeys, err := vocdoniClient.WaitUntilElectionKeys(ctx, encAddr.Bytes())
	cancel()
	c.Assert(err, qt.IsNil)
	c.Assert(nodeKeys.PublicKeys, qt.Not(qt.HasLen), 0)

	info := requestAndParse[apicommon.ProcessInfo](t, http.MethodGet, token, nil, "process", encObjID)
	c.Assert(info.EncryptionKeys, qt.HasLen, len(nodeKeys.PublicKeys))
	for i, k := range nodeKeys.PublicKeys {
		c.Assert(info.EncryptionKeys[i].Index, qt.Equals, k.Index)
		c.Assert(info.EncryptionKeys[i].Key.String(), qt.Equals, k.Key.String())
	}

	// the keys are immutable once published, so they are cached on the stored process
	encParsedID, err := primitive.ObjectIDFromHex(encObjID)
	c.Assert(err, qt.IsNil)
	stored, err := testDB.Process(encParsedID)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.EncryptionKeys, qt.HasLen, len(nodeKeys.PublicKeys))

	// --- non-encrypted election: no keys, no chain lookup ---
	plainObjID, _ := newProcess(false)
	plain := requestAndParse[apicommon.ProcessInfo](t, http.MethodGet, token, nil, "process", plainObjID)
	c.Assert(plain.EncryptionKeys, qt.HasLen, 0)
}
