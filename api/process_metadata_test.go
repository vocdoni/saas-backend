package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
)

// TestResolveMetadataPromotesRemote asserts that a remote (external http) metadata
// reference is resolved, cached locally and the reference promoted to a /storage/ URL,
// so a later read resolves from local storage even when the external source is gone.
func TestResolveMetadataPromotesRemote(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "password123")
	orgAddress := testCreateOrganization(t, token)

	served := map[string]any{"title": "Remote election", "version": "1.0"}
	body, err := json.Marshal(served)
	c.Assert(err, qt.IsNil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	// the test closes ts mid-run to prove local resolution; this cleanup closes it if an
	// assertion fails first, and the guard avoids a double close.
	tsClosed := false
	t.Cleanup(func() {
		if !tsClosed {
			ts.Close()
		}
	})

	// seed a published process whose metadata reference points at the external server
	// (no on-chain lookup happens because the reference is already set).
	addr := common.HexToAddress(internal.RandomHex(20))
	id, err := testDB.SetProcess(&db.Process{
		OrgAddress:  orgAddress,
		Address:     internal.HexBytes(addr.Bytes()),
		Status:      "READY",
		MetadataURL: ts.URL,
	})
	c.Assert(err, qt.IsNil)

	// first read: resolves externally, caches locally, promotes the reference
	info := requestAndParse[apicommon.ProcessInfo](t, http.MethodGet, token, nil, "process", id.Hex())
	c.Assert(info.Metadata["title"], qt.Equals, "Remote election")
	c.Assert(strings.HasPrefix(info.MetadataURL, "/storage/"), qt.IsTrue,
		qt.Commentf("reference should be promoted to local storage, got %q", info.MetadataURL))

	// the promoted reference must be persisted
	stored, err := testDB.Process(id)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.MetadataURL, qt.Equals, info.MetadataURL)

	// second read resolves from local storage even after the external source disappears
	ts.Close()
	tsClosed = true
	info2 := requestAndParse[apicommon.ProcessInfo](t, http.MethodGet, token, nil, "process", id.Hex())
	c.Assert(info2.Metadata["title"], qt.Equals, "Remote election")
}
