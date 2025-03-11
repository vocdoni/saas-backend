package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
)

func TestTransaction(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "superpassword123")

	// get the user to verify the token works
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("%s\n", resp)

	// create a new vocdoni client
	vocdoniClient := testNewVocdoniClient(t)

	// create an organization
	orgAddress := testCreateOrganization(t, token)
	t.Logf("fetched org address %s\n", orgAddress.String())

	// subscribe the organization to a plan
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans), qt.Not(qt.Equals), 0)

	err = testDB.SetOrganizationSubscription(orgAddress.String(), &db.OrganizationSubscription{
		PlanID:          plans[0].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(time.Hour * 24),
		LastPaymentDate: time.Now(),
		Active:          true,
		MaxCensusSize:   1000,
	})
	c.Assert(err, qt.IsNil)

	// build the create account transaction
	orgName := fmt.Sprintf("testorg-%d", internal.RandomInt(1000))
	orgInfoUri := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))

	nonce := uint32(0)
	tx := models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Nonce:   &nonce,
				Txtype:  models.TxType_CREATE_ACCOUNT,
				Account: orgAddress.Bytes(),
				Name:    &orgName,
				InfoURI: &orgInfoUri,
			},
		},
	}

	// send the transaction
	sendVocdoniTx(t, &tx, token, vocdoniClient, orgAddress)

	// get the organization
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("%s\n", resp)

	// create a process
	nonce = fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	tx = models.Tx{
		Payload: &models.Tx_NewProcess{
			NewProcess: &models.NewProcessTx{
				Txtype: models.TxType_NEW_PROCESS,
				Nonce:  nonce,
				Process: &models.Process{
					EntityId:      orgAddress.Bytes(),
					Duration:      60,
					CensusOrigin:  models.CensusOrigin_OFF_CHAIN_TREE_WEIGHTED,
					CensusRoot:    util.RandomBytes(32),
					MaxCensusSize: 5,
					EnvelopeType: &models.EnvelopeType{
						Anonymous:      false,
						CostFromWeight: false,
					},
					VoteOptions: &models.ProcessVoteOptions{
						MaxCount: 1,
						MaxValue: 2,
					},
				},
			},
		},
	}

	// send the transaction
	sendVocdoniTx(t, &tx, token, vocdoniClient, orgAddress)

}
