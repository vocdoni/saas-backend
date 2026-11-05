package certauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/cors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// generalCORS is the main router's CORS as api.initRouter configures it.
func generalCORS() func(http.Handler) http.Handler {
	return cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300,
	}).Handler
}

// TestCertAuthCORS pins the CORS dispatcher (contract section 13): the two certificate endpoints
// answer preflights and requests for the allowlisted origins only, never with credentials, and every
// other path gets exactly what the general middleware answers alone.
func TestCertAuthCORS(t *testing.T) {
	c := qt.New(t)
	s, _, _ := newTestService(c, 443, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	})
	dispatched := s.CORS(generalCORS())(ok)
	general := generalCORS()(ok)
	pid := bson.NewObjectID().Hex()
	upper := strings.ToUpper(pid)

	serve := func(h http.Handler, method, path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if method == http.MethodOptions {
			req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			req.Header.Set("Access-Control-Request-Headers", "content-type")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	for _, path := range []string{
		"/processes/" + pid + "/auth/cert", "/processes/" + pid + "/auth/cert/redeem",
		"/processes/" + upper + "/auth/cert", "/processes/" + upper + "/auth/cert/redeem",
	} {
		for _, method := range []string{http.MethodOptions, http.MethodPost} {
			allowed := serve(dispatched, method, path, testOrigin)
			c.Assert(allowed.Header().Get("Access-Control-Allow-Origin"), qt.Equals, testOrigin,
				qt.Commentf("%s %s", method, path))
			c.Assert(allowed.Header().Get("Access-Control-Allow-Credentials"), qt.Equals, "")
			foreign := serve(dispatched, method, path, "https://evil.test")
			c.Assert(foreign.Header().Get("Access-Control-Allow-Origin"), qt.Equals, "",
				qt.Commentf("%s %s", method, path))
			c.Assert(foreign.Header().Get("Access-Control-Allow-Credentials"), qt.Equals, "")
		}
	}

	// every other path, including the typed auth steps and look-alike paths, is untouched
	for _, path := range []string{
		"/processes/" + pid + "/auth/0", "/processes/" + pid + "/auth/cert/other", "/processes/" + pid + "/auth/certs",
		"/processes/" + pid + "/auth/cert/", "/processes/" + pid[:23] + "/auth/cert", "/processes/" + pid + "0/auth/cert",
		"/processes/" + pid[:23] + "g/auth/cert", "/x/processes/" + pid + "/auth/cert", "/processes/" + pid, "/ping", "/",
	} {
		for _, method := range []string{http.MethodOptions, http.MethodPost, http.MethodGet} {
			for _, origin := range []string{"https://evil.test", testOrigin, ""} {
				got, want := serve(dispatched, method, path, origin), serve(general, method, path, origin)
				comment := qt.Commentf("%s %s %q", method, path, origin)
				c.Assert(got.Code, qt.Equals, want.Code, comment)
				c.Assert(got.Header(), qt.DeepEquals, want.Header(), comment)
				c.Assert(got.Body.String(), qt.Equals, want.Body.String(), comment)
			}
		}
	}
}
