package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

func TestTransaction(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "superpassword123")
	t.Logf("fetched token %s\n", token)
	resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK)
	t.Logf("%s\n", resp)

	orgAddress := testCreateOrganization(t, token)
	t.Logf("fetched org address %s\n", orgAddress)

	orgAddressBytes := new(internal.HexBytes).SetString(orgAddress)

	orgName := fmt.Sprintf("testorg-%d", internal.RandomInt(1000))
	orgInfoUri := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))

	tx := models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Txtype:  models.TxType_CREATE_ACCOUNT,
				Account: orgAddressBytes.Bytes(),
				Name:    &orgName,
				InfoURI: &orgInfoUri,
			},
		},
	}

	txBytes, err := proto.Marshal(&tx)
	c.Assert(err, qt.IsNil)

	td := &TransactionData{
		Address:   orgAddress,
		TxPayload: base64.StdEncoding.EncodeToString(txBytes), // TODO: this should be []bytes directly
	}

	resp, code = testRequest(t, http.MethodPost, token, td, signTxEndpoint)
	c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
}
