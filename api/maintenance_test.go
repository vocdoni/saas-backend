package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
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

	// window exceeding 24 h is rejected
	_, err = ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-24T12:45:01Z")
	c.Assert(err, qt.IsNotNil)

	// window of exactly 24 h is accepted
	m, err = ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-24T12:45:00Z")
	c.Assert(err, qt.IsNil)
	c.Assert(m, qt.IsNotNil)

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
	// The global middleware only sets the header; blocking is per-handler via maintenanceWriteBlock.
	handler := maintenance(m, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		return rec
	}

	// before the window: header present, nothing blocked
	now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rec := do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get(maintenanceHeader), qt.Equals, m.String())

	// inside the window: header still present, nothing blocked (global middleware is header-only)
	now = time.Date(2026, 11, 23, 12, 50, 0, 0, time.UTC)
	c.Assert(do(http.MethodPost, "/process").Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodDelete, "/organizations/0x1/members").Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodGet, "/process/0x1").Code, qt.Equals, http.StatusOK)

	// after the window: header absent
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

func TestMaintenanceWriteBlock(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")
	c.Assert(err, qt.IsNil)
	var now time.Time

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// handle() auto-wraps every POST/PUT/DELETE with maintenanceWriteBlock (deny-by-default).
	// The tests below verify this wrapper directly; the handleExempt() path skips it.
	blocked := maintenanceWriteBlock(m, func() time.Time { return now }, inner)
	do := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		blocked.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, http.NoBody))
		return rec
	}

	// before the window: not blocked
	now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c.Assert(do("/processes/0x1/sign-batch").Code, qt.Equals, http.StatusOK)

	// inside the window: blocked with 503 and Retry-After
	now = time.Date(2026, 11, 23, 12, 50, 0, 0, time.UTC)
	rec := do("/processes/0x1/sign-batch")
	c.Assert(rec.Code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(rec.Body.String(), qt.Contains, "50304")
	// Retry-After = ceil(until-now) + jitter(0-60) = 600..660
	ra, raErr := strconv.Atoi(rec.Header().Get("Retry-After"))
	c.Assert(raErr, qt.IsNil)
	c.Assert(ra >= 600, qt.IsTrue)
	c.Assert(ra <= 660, qt.IsTrue)

	// sign/sign-batch/blind-sign/auth routes are all refused: they consume signing
	// slots or send OTPs while the vote relay is unreachable.
	for _, path := range []string{
		"/processes/0x1/sign",
		"/processes/0x1/sign-batch",
		"/processes/0x1/blind-sign",
		"/processes/0x1/blind-point",
		"/processes/0x1/auth/step0",
		"/processes/0x1/auth/resend",
		"/cert-auth/assertions",
	} {
		c.Assert(do(path).Code, qt.Equals, http.StatusServiceUnavailable, qt.Commentf("path %q should be blocked", path))
	}

	// after the window: not blocked
	now = time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	c.Assert(do("/processes/0x1/sign-batch").Code, qt.Equals, http.StatusOK)

	// nil window: handler passed through unchanged (maintenanceWriteBlock returns h as-is)
	passThru := maintenanceWriteBlock(nil, time.Now, inner)
	rec = httptest.NewRecorder()
	passThru.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/process", http.NoBody))
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
}
