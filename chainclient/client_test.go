package chainclient

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// fakeNode serves the node endpoints the client tests exercise.
func fakeNode(c *qt.C, signer *ethereum.SignKeys) *httptest.Server {
	faucetPkg := &models.FaucetPackage{Payload: []byte{1, 2}, Signature: []byte{3, 4}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v2/chain/info":
			_, _ = w.Write([]byte(`{"chainId":"test-chain","height":10}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/open/claim/"+signer.AddressString():
			pkg, err := json.Marshal(&faucetPackage{FaucetPayload: faucetPkg.Payload, Signature: faucetPkg.Signature})
			c.Check(err, qt.IsNil)
			c.Check(json.NewEncoder(w).Encode(&faucetResponse{Amount: "100", FaucetPackage: pkg}), qt.IsNil)
		case r.Method == http.MethodPost && r.URL.Path == "/v2/accounts":
			req := &accountSet{}
			c.Check(json.NewDecoder(r.Body).Decode(req), qt.IsNil)
			stx := &models.SignedTx{}
			c.Check(proto.Unmarshal(req.TxPayload, stx), qt.IsNil)
			tx := &models.Tx{}
			c.Check(proto.Unmarshal(stx.Tx, tx), qt.IsNil)
			set := tx.GetSetAccount()
			c.Check(set.GetTxtype(), qt.Equals, models.TxType_CREATE_ACCOUNT)
			c.Check(set.GetAccount(), qt.DeepEquals, signer.Address().Bytes())
			c.Check(set.GetName(), qt.Equals, "svc")
			c.Check(set.InfoURI, qt.IsNil)
			c.Check(set.GetFaucetPackage().GetPayload(), qt.DeepEquals, faucetPkg.Payload)
			msg, _, err := ethereum.BuildVocdoniTransaction(stx.Tx, "test-chain")
			c.Check(err, qt.IsNil)
			addr, err := ethereum.AddrFromSignature(msg, stx.Signature)
			c.Check(err, qt.IsNil)
			c.Check(addr, qt.Equals, signer.Address())
			_, _ = w.Write([]byte(`{"txHash":"cafe"}`))
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	c.Cleanup(srv.Close)
	return srv
}

func TestClient(t *testing.T) {
	c := qt.New(t)
	signer := ethereum.NewSignKeys()
	c.Assert(signer.Generate(), qt.IsNil)
	srv := fakeNode(c, signer)

	cli, err := New(srv.URL + "/v2")
	c.Assert(err, qt.IsNil)
	c.Assert(cli.ChainID(), qt.Equals, "test-chain")

	_, err = cli.Account(signer.AddressString())
	statusErr := &StatusError{}
	c.Assert(errors.As(err, &statusErr), qt.IsTrue)
	c.Assert(statusErr.Code, qt.Equals, http.StatusNotFound)

	_, err = cli.TransactionReference(types.HexBytes{0x01})
	c.Assert(err, qt.ErrorIs, ErrTransactionDoesNotExist)

	pkg, err := cli.FaucetPackage(signer.AddressString())
	c.Assert(err, qt.IsNil)
	c.Assert(pkg.Payload, qt.DeepEquals, []byte{1, 2})
	c.Assert(pkg.Signature, qt.DeepEquals, []byte{3, 4})

	hash, err := cli.AccountBootstrap(signer, pkg, "svc")
	c.Assert(err, qt.IsNil)
	c.Assert(hash, qt.DeepEquals, types.HexBytes{0xca, 0xfe})
}
