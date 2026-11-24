package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestRequireBearer(t *testing.T) {
	c := qt.New(t)
	handler := requireBearer("m3trics", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(auth string) int {
		req := httptest.NewRequest(http.MethodGet, "/metrics", http.NoBody)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	c.Assert(do("Bearer m3trics"), qt.Equals, http.StatusOK)
	c.Assert(do(""), qt.Equals, http.StatusUnauthorized)
	c.Assert(do("Bearer wrong"), qt.Equals, http.StatusUnauthorized)
	c.Assert(do("m3trics"), qt.Equals, http.StatusUnauthorized)
}
