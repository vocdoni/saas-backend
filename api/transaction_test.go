package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
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

	orgAddress := new(internal.HexBytes).SetString(testCreateOrganization(t, token))
	t.Logf("fetched org address %s\n", orgAddress.String())

	orgName := fmt.Sprintf("testorg-%d", internal.RandomInt(1000))
	orgInfoUri := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))

	// build the create account transaction
	tx := models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Txtype:  models.TxType_CREATE_ACCOUNT,
				Account: orgAddress.Bytes(),
				Name:    &orgName,
				InfoURI: &orgInfoUri,
			},
		},
	}

	// send the transaction
	sendVocdoniTx(t, &tx, token, vocdoniClient, *orgAddress)

	// get the organization
	resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
	t.Logf("%s\n", resp)

}
