package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestMailAssets(t *testing.T) {
	c := qt.New(t)
	get := func(path string) *http.Response {
		req, err := http.NewRequest(http.MethodGet, testURL(path), http.NoBody)
		c.Assert(err, qt.IsNil)
		resp, err := http.DefaultClient.Do(req)
		c.Assert(err, qt.IsNil)
		c.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}
	resp := get("/mail/assets/vocdoni-wordmark-black-halo@2x.png")
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Header.Get("Content-Type"), qt.Equals, "image/png")
	c.Assert(resp.Header.Get("Cache-Control"), qt.Equals, "public, max-age=86400")
	c.Assert(resp.Header.Get("ETag"), qt.Not(qt.Equals), "")
	for _, path := range []string{
		"/mail/assets/unknown.png",
		"/mail/assets/..%2Flayout.html",
		"/mail/assets/..%2F..%2Fgo.mod",
		"/mail/assets/i18n",
		"/mail/assets/../layout.html",
	} {
		c.Assert(get(path).StatusCode, qt.Equals, http.StatusNotFound, qt.Commentf("%s", path))
	}
}
