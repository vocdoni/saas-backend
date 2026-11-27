package account

import (
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestNodeToken(t *testing.T) {
	c := qt.New(t)
	t.Cleanup(func() { nodeToken.Store(nil) })

	req := httptest.NewRequest("GET", "/", nil)
	setNodeAuth(req)
	c.Assert(req.Header.Get("Authorization"), qt.Equals, "") // unset: no header, as before

	c.Assert(SetNodeToken("not-a-uuid"), qt.ErrorMatches, ".*not a UUID.*")
	c.Assert(SetNodeToken(""), qt.IsNil)
	c.Assert(nodeToken.Load(), qt.IsNil)

	const tok = "11111111-2222-3333-4444-555555555555"
	c.Assert(SetNodeToken(tok), qt.IsNil)
	setNodeAuth(req)
	c.Assert(req.Header.Get("Authorization"), qt.Equals, "Bearer "+tok)
}
