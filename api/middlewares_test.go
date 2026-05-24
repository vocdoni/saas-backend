package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
)

func TestRequestIDMiddleware(t *testing.T) {
	c := qt.New(t)

	handler := requestID(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	c.Assert(id, qt.Not(qt.Equals), "")
	_, err := uuid.Parse(id)
	c.Assert(err, qt.IsNil)
}
