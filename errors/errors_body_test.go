package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.vocdoni.io/dvote/log"
)

var (
	testInternalErr = Error{
		Code: 59990, HTTPstatus: http.StatusInternalServerError,
		Err: fmt.Errorf("server error: operation failed"),
	}
	testBadInputErr = Error{Code: 49990, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("malformed body")}
)

// writtenBody writes e to a recorder and returns the status and the decoded body.
func writtenBody(t *testing.T, e Error) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.Write(rec)
	body := map[string]any{}
	qt.Assert(t, json.Unmarshal(rec.Body.Bytes(), &body), qt.IsNil, qt.Commentf("body: %s", rec.Body))
	return rec.Code, body
}

func TestWriteHidesInternalDetailOn5xx(t *testing.T) {
	c := qt.New(t)
	internal := fmt.Errorf("server selection error: mongo-0.internal:27017")

	cases := map[string]Error{
		"WithErr":           testInternalErr.WithErr(internal),
		"Withf with args":   testInternalErr.Withf("could not get org %s: %v", "0xabc", internal),
		"With then WithErr": testInternalErr.With("could not create group").WithErr(internal),
		"WithLogLevel":      testInternalErr.WithErr(internal).WithLogLevel("warn"),
	}
	for name, e := range cases {
		code, body := writtenBody(t, e)
		c.Assert(code, qt.Equals, http.StatusInternalServerError, qt.Commentf("%s", name))
		c.Assert(body["code"], qt.Equals, float64(59990), qt.Commentf("%s", name))
		c.Assert(body["error"], qt.Not(qt.Contains), "mongo-0.internal", qt.Commentf("%s", name))
		c.Assert(body["error"], qt.Contains, "server error: operation failed", qt.Commentf("%s", name))
		// the full detail is still on the error, for the log
		c.Assert(e.Error(), qt.Contains, "mongo-0.internal", qt.Commentf("%s", name))
	}
	// and WithErr still wraps it for errors.Is/As
	c.Assert(cases["WithErr"], qt.ErrorIs, internal)

	_, body := writtenBody(t, testInternalErr.With("could not create group").WithErr(internal))
	c.Assert(body["error"], qt.Equals, "server error: operation failed: could not create group")
}

func TestWriteKeepsClientMessagesOn5xx(t *testing.T) {
	c := qt.New(t)

	_, body := writtenBody(t, testInternalErr.Withf("publishing again does not charge it again"))
	c.Assert(body["error"], qt.Equals, "server error: operation failed: publishing again does not charge it again")

	_, body = writtenBody(t, testInternalErr.WithData(map[string]any{"retryAfter": 5}))
	c.Assert(body["data"], qt.DeepEquals, map[string]any{"retryAfter": float64(5)})
}

// captureLog runs fn with the global logger at level and returns what it logged.
// The logger is restored to its previous configuration when the test ends.
func captureLog(t *testing.T, level string, fn func()) string {
	t.Helper()
	prev := *log.Logger()
	t.Cleanup(func() { *log.Logger() = prev })

	logFile := filepath.Join(t.TempDir(), "api.log")
	f, err := os.Create(logFile)
	qt.Assert(t, err, qt.IsNil)
	t.Cleanup(func() { _ = f.Close() })

	// log.Init writes to os.Stdout, so point it at the file while it runs
	stdout := os.Stdout
	os.Stdout = f
	log.Init(level, "stdout", nil)
	os.Stdout = stdout

	fn()
	out, err := os.ReadFile(logFile)
	qt.Assert(t, err, qt.IsNil)
	return string(out)
}

func TestWriteLogs4xxAtTheirLevel(t *testing.T) {
	c := qt.New(t)
	out := captureLog(t, log.LogLevelInfo, func() {
		testBadInputErr.With("tagged warn").WithLogLevel("warn").Write(httptest.NewRecorder())
		testBadInputErr.With("tagged info").WithLogLevel("info").Write(httptest.NewRecorder())
		testBadInputErr.With("untagged").Write(httptest.NewRecorder())
	})
	c.Assert(out, qt.Contains, "malformed body: tagged warn")
	c.Assert(out, qt.Contains, "malformed body: tagged info")
	c.Assert(out, qt.Not(qt.Contains), "malformed body: untagged")
}

func TestWriteLogs5xxAtTheirLevel(t *testing.T) {
	c := qt.New(t)
	tagged := testInternalErr.With("tagged warn").WithLogLevel("warn")
	untagged := testInternalErr.With("untagged")

	// at warn the warn-tagged 5xx is logged, and the untagged one (error) too
	out := captureLog(t, log.LogLevelWarn, func() {
		tagged.Write(httptest.NewRecorder())
		untagged.Write(httptest.NewRecorder())
	})
	c.Assert(out, qt.Contains, "tagged warn")
	c.Assert(out, qt.Contains, "untagged")

	// at error the warn-tagged 5xx is dropped, the untagged one is still logged
	out = captureLog(t, log.LogLevelError, func() {
		tagged.Write(httptest.NewRecorder())
		untagged.Write(httptest.NewRecorder())
	})
	c.Assert(out, qt.Not(qt.Contains), "tagged warn")
	c.Assert(out, qt.Contains, "untagged")
}

func TestWithKeepsFields(t *testing.T) {
	c := qt.New(t)
	internal := fmt.Errorf("server selection error: mongo-0.internal:27017")
	data := map[string]any{"retryAfter": 5}

	e := testBadInputErr.WithData(data).With("tagged")
	c.Assert(e.Data, qt.DeepEquals, data)

	e = testInternalErr.WithData(data).WithErr(internal)
	c.Assert(e.Data, qt.DeepEquals, data)
	_, body := writtenBody(t, e)
	c.Assert(body["data"], qt.DeepEquals, map[string]any{"retryAfter": float64(5)})

	e = testInternalErr.WithLogLevel("warn").Withf("count %d", 3).With("tagged").WithErr(internal)
	c.Assert(e.LogLevel, qt.Equals, "warn")
	c.Assert(e.HTTPstatus, qt.Equals, http.StatusInternalServerError)
	c.Assert(e.Code, qt.Equals, testInternalErr.Code)
	_, body = writtenBody(t, e)
	c.Assert(body["error"], qt.Equals, testInternalErr.Err.Error()+": tagged")
}

func TestWriteKeepsDetailOn4xx(t *testing.T) {
	c := qt.New(t)

	code, body := writtenBody(t, testBadInputErr.WithErr(fmt.Errorf("unexpected EOF")))
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(body["error"], qt.Equals, "malformed body: unexpected EOF")

	_, body = writtenBody(t, testBadInputErr.Withf("field %q is required", "name"))
	c.Assert(body["error"], qt.Equals, `malformed body: field "name" is required`)
}

// TestWriteSetsRetryAfterOn503: a 503 tells the client when to retry, unless the caller set its
// own delay; other statuses carry none.
func TestWriteSetsRetryAfterOn503(t *testing.T) {
	c := qt.New(t)
	rec := httptest.NewRecorder()
	ErrServerBusy.Write(rec)
	c.Assert(rec.Header().Get("Retry-After"), qt.Equals, "10")

	rec = httptest.NewRecorder()
	rec.Header().Set("Retry-After", "2")
	ErrServerBusy.Write(rec)
	c.Assert(rec.Header().Get("Retry-After"), qt.Equals, "2")

	rec = httptest.NewRecorder()
	testInternalErr.Write(rec)
	c.Assert(rec.Header().Get("Retry-After"), qt.Equals, "")
}
