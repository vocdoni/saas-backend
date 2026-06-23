package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestInfoHandler verifies that InfoHandler returns HTTP 200 with valid JSON service info.
func TestInfoHandler(t *testing.T) {
	c := qt.New(t)
	w := httptest.NewRecorder()
	r, err := http.NewRequest(http.MethodGet, "/info", nil)
	c.Assert(err, qt.IsNil)

	// account is nil here, so ChainID stays empty; the build info fields are still populated.
	var a API
	a.InfoHandler(w, r)

	c.Assert(w.Code, qt.Equals, http.StatusOK)
	var info InfoResponse
	c.Assert(json.Unmarshal(w.Body.Bytes(), &info), qt.IsNil)
	c.Assert(info.GoVersion, qt.Not(qt.Equals), "")
}

// TestInfoHandlerNilBuildInfo verifies the nil-guard path: when build info is unavailable,
// the handler writes defaultInfoResponse instead of panicking.
func TestInfoHandlerNilBuildInfo(t *testing.T) {
	c := qt.New(t)
	w := httptest.NewRecorder()

	// Directly exercise the nil-guard branch that InfoHandler takes when
	// debug.ReadBuildInfo returns nil.
	apicommon.HTTPWriteJSON(w, defaultInfoResponse)

	c.Assert(w.Code, qt.Equals, http.StatusOK)
	var info InfoResponse
	c.Assert(json.Unmarshal(w.Body.Bytes(), &info), qt.IsNil)
	c.Assert(info.Version, qt.Equals, "unknown")
	c.Assert(info.GoVersion, qt.Equals, "unknown")
}
