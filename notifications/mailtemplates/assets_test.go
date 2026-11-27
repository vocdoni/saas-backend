package mailtemplates

import (
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestServeAsset(t *testing.T) {
	c := qt.New(t)
	get := func(name, etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/mail/assets/x", http.NoBody)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		rec := httptest.NewRecorder()
		ServeAsset(rec, req, name)
		return rec
	}
	rec := get("vocdoni-wordmark-black-halo@2x.png", "")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get("Content-Type"), qt.Equals, "image/png")
	c.Assert(rec.Header().Get("Cache-Control"), qt.Equals, "public, max-age=86400")
	etag := rec.Header().Get("ETag")
	c.Assert(etag, qt.Matches, `"[0-9a-f]{32}"`)
	c.Assert(rec.Body.Len() > 0, qt.IsTrue)
	c.Assert(get("vocdoni-wordmark-black-halo@2x.png", etag).Code, qt.Equals, http.StatusNotModified)
	for _, name := range []string{
		"", "../layout.html", "..%2Flayout.html", "layout.html", "i18n", "nope.png",
		"../../go.mod", "images/vocdoni-mark-black@2x.png",
	} {
		c.Assert(get(name, "").Code, qt.Equals, http.StatusNotFound, qt.Commentf("%q", name))
	}
}
