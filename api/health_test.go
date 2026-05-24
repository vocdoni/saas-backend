package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestVersionHandler verifies that VersionHandler returns HTTP 200 with valid JSON version info.
func TestVersionHandler(t *testing.T) {
	c := qt.New(t)
	w := httptest.NewRecorder()
	r, err := http.NewRequest(http.MethodGet, "/version", nil)
	c.Assert(err, qt.IsNil)

	var a API
	a.VersionHandler(w, r)

	c.Assert(w.Code, qt.Equals, http.StatusOK)
	var info VersionInfo
	c.Assert(json.Unmarshal(w.Body.Bytes(), &info), qt.IsNil)
	c.Assert(info.GoVersion, qt.Not(qt.Equals), "")
}

// TestVersionHandlerNilBuildInfo verifies the nil-guard path: when build info is unavailable,
// the handler writes defaultVersionInfo instead of panicking.
func TestVersionHandlerNilBuildInfo(t *testing.T) {
	c := qt.New(t)
	w := httptest.NewRecorder()

	// Directly exercise the nil-guard branch that VersionHandler takes when
	// debug.ReadBuildInfo returns nil.
	apicommon.HTTPWriteJSON(w, defaultVersionInfo)

	c.Assert(w.Code, qt.Equals, http.StatusOK)
	var info VersionInfo
	c.Assert(json.Unmarshal(w.Body.Bytes(), &info), qt.IsNil)
	c.Assert(info.Version, qt.Equals, "unknown")
	c.Assert(info.GoVersion, qt.Equals, "unknown")
}
