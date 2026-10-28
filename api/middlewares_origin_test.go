package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestRequireOriginSecret(t *testing.T) {
	c := qt.New(t)
	handler := requireOriginSecret("s3cret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(path, secret string) int {
		req := httptest.NewRequest(http.MethodGet, path, http.NoBody)
		if secret != "" {
			req.Header.Set(originSecretHeader, secret)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	c.Assert(do("/auth/login", "s3cret"), qt.Equals, http.StatusOK)
	c.Assert(do("/auth/login", ""), qt.Equals, http.StatusForbidden)
	c.Assert(do("/auth/login", "wrong"), qt.Equals, http.StatusForbidden)
	// the platform health check does not go through the proxy
	c.Assert(do("/ping", ""), qt.Equals, http.StatusOK)
}
