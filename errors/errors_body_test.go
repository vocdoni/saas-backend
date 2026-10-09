package errors

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
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

func TestWriteKeepsDetailOn4xx(t *testing.T) {
	c := qt.New(t)

	code, body := writtenBody(t, testBadInputErr.WithErr(fmt.Errorf("unexpected EOF")))
	c.Assert(code, qt.Equals, http.StatusBadRequest)
	c.Assert(body["error"], qt.Equals, "malformed body: unexpected EOF")

	_, body = writtenBody(t, testBadInputErr.Withf("field %q is required", "name"))
	c.Assert(body["error"], qt.Equals, `malformed body: field "name" is required`)
}
