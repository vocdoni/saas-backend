package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

func TestSetLangNormalizes(t *testing.T) {
	c := qt.New(t)
	var got any
	handler := new(API).setLang(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = r.Context().Value(apicommon.LangMetadataKey)
	}))
	for query, want := range map[string]any{
		"?lang=pt-BR": "pt-br",
		"?lang=EU":    "eu",
		"?lang=ca":    "ca",
		"?lang=xx":    nil, // unsupported values are dropped
		"":            nil,
	} {
		got = nil
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/users"+query, http.NoBody))
		c.Assert(got, qt.Equals, want, qt.Commentf("query %q", query))
	}
}
