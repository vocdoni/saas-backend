package api

import (
	"encoding/hex"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// TestSignMessageHandler exercises the protected POST /transactions/message
// endpoint: it signs an allow-listed payload with the org signer when the
// caller is an admin of the target address, and rejects malformed,
// unauthorized, or non-allow-listed requests.
//
// account.SignMessage only signs messages whose hash is in
// account.AllowedSignMessagesHash (the production list holds a single
// SIK-generation message whose plaintext is not available to tests). The
// success case therefore temporarily allow-lists its payload and removes it
// again, while a separate case asserts a non-allow-listed message is refused.
func TestSignMessageHandler(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token := testCreateUser(t, testPass)
	orgAddress := testCreateOrganization(t, token)

	// Success: temporarily allow-list the payload so the signing path is
	// reachable, then assert the admin gets back a non-empty signature.
	payload := []byte("hello world")
	allowed := hex.EncodeToString(ethereum.Hash(payload))
	account.AllowedSignMessagesHash[allowed] = nil
	defer delete(account.AllowedSignMessagesHash, allowed)

	res := requestAndParse[apicommon.MessageSignature](
		t, http.MethodPost, token,
		&apicommon.MessageSignature{Address: orgAddress, Payload: payload},
		signMessageEndpoint,
	)
	c.Assert(len(res.Signature) > 0, qt.IsTrue)

	// Missing payload: a request without a payload is malformed.
	requestAndAssertError(
		errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.MessageSignature{Address: orgAddress},
		signMessageEndpoint,
	)

	// Non-allow-listed message: the org signer refuses to sign an arbitrary
	// payload, so the handler returns a generic 500.
	requestAndAssertError(
		errors.ErrGenericInternalServerError, t, http.MethodPost, token,
		&apicommon.MessageSignature{Address: orgAddress, Payload: []byte("not allow-listed")},
		signMessageEndpoint,
	)

	// Not admin: the user lacks the admin role for an unrelated address.
	k := ethereum.NewSignKeys()
	c.Assert(k.Generate(), qt.IsNil)
	other := k.Address()
	requestAndAssertError(
		errors.ErrUnauthorized, t, http.MethodPost, token,
		&apicommon.MessageSignature{Address: other, Payload: payload},
		signMessageEndpoint,
	)
}
