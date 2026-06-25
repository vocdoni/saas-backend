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

// TestFetchExternalMetadata covers the external http(s) fetch helper: a valid JSON
// document is decoded, a non-200 yields nil, and a body over the 1 MiB cap is rejected.
func TestFetchExternalMetadata(t *testing.T) {
	c := qt.New(t)

	t.Run("ok", func(_ *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"title":"hello","version":"1.0"}`))
		}))
		defer ts.Close()
		m := fetchExternalMetadata(t.Context(), ts.URL)
		c.Assert(m, qt.Not(qt.IsNil))
		c.Assert(m["title"], qt.Equals, "hello")
	})

	t.Run("non-200", func(_ *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer ts.Close()
		c.Assert(fetchExternalMetadata(t.Context(), ts.URL), qt.IsNil)
	})

	t.Run("over size cap", func(_ *testing.T) {
		// a valid JSON document larger than the 1 MiB read cap is truncated and fails to
		// decode, so it must be rejected rather than partially parsed.
		big, err := json.Marshal(map[string]any{"x": strings.Repeat("a", 2<<20)})
		c.Assert(err, qt.IsNil)
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(big)
		}))
		defer ts.Close()
		c.Assert(fetchExternalMetadata(t.Context(), ts.URL), qt.IsNil)
	})
}

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
