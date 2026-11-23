package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

func TestParseMaintenanceWindow(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("")
	c.Assert(err, qt.IsNil)
	c.Assert(m, qt.IsNil)

	m, err = ParseMaintenanceWindow("2026-11-23T13:45:00+01:00/2026-11-23T14:00:00+01:00")
	c.Assert(err, qt.IsNil)
	c.Assert(m.String(), qt.Equals, "2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")

	for _, bad := range []string{
		"2026-11-23T12:45:00Z",
		"2026-11-23T12:45:00Z/soon",
		"2026-11-23 12:45/2026-11-23T13:00:00Z",
		"2026-11-23T13:00:00Z/2026-11-23T12:45:00Z",
		"2026-11-23T13:00:00Z/2026-11-23T13:00:00Z",
	} {
		_, err := ParseMaintenanceWindow(bad)
		c.Assert(err, qt.IsNotNil, qt.Commentf("%q", bad))
	}
}

func TestMaintenanceMiddleware(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")
	c.Assert(err, qt.IsNil)
	var now time.Time
	handler := maintenance(m, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		return rec
	}

	// before the window: announced, nothing refused
	now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rec := do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get(maintenanceHeader), qt.Equals, m.String())

	// inside the window: writes refused until its end, reads and exempt writes go through
	now = time.Date(2026, 11, 23, 12, 50, 0, 0, time.UTC)
	rec = do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(rec.Header().Get("Retry-After"), qt.Equals, "600")
	c.Assert(rec.Body.String(), qt.Contains, "50304")
	c.Assert(do(http.MethodDelete, "/organizations/0x1/members").Code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(do(http.MethodGet, "/process/0x1").Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodPost, authLoginEndpoint).Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodPost, authRefresTokenEndpoint).Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodPost, paymentsWebhook).Code, qt.Equals, http.StatusOK)

	// after the window: back to normal, no header
	now = time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	rec = do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get(maintenanceHeader), qt.Equals, "")

	// no window configured
	none := maintenance(nil, time.Now)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	none.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/process", http.NoBody))
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
}
