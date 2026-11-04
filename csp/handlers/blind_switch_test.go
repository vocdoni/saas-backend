package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestBlindRoundsRefusedWhileAnonymousVotingOff pins that both blind rounds answer 400 with the
// switch's message while anonymous voting is off, and that once it is back on the request reaches
// the usual checks (here the missing auth token's 401).
func TestBlindRoundsRefusedWhileAnonymousVotingOff(t *testing.T) {
	c := qt.New(t)
	h := New(&csp.CSP{Storage: testDB}, testDB, nil)
	election := internal.HexBytes{0x01}
	rounds := []struct {
		name    string
		handler http.HandlerFunc
		body    any
	}{
		{"blind-point", h.ProcessBlindPointHandler, &BlindPointRequest{Elections: []internal.HexBytes{election}}},
		{"blind-sign", h.ProcessBlindSignHandler, &BlindSignRequest{Ballots: []BlindSignBallot{
			{UpstreamID: election, BlindedMessage: internal.HexBytes{0x02}},
		}}},
	}
	// call runs one round and returns the HTTP status, API error code and error message it answered with
	call := func(handler http.HandlerFunc, body any) (status, code int, message string) {
		raw, err := json.Marshal(body)
		c.Assert(err, qt.IsNil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("processId", bson.NewObjectID().Hex())
		r := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		w := httptest.NewRecorder()
		handler(w, r)
		var apiErr struct {
			Error string `json:"error"`
			Code  int    `json:"code"`
		}
		c.Assert(json.Unmarshal(w.Body.Bytes(), &apiErr), qt.IsNil, qt.Commentf("body: %s", w.Body.String()))
		return w.Code, apiErr.Code, apiErr.Error
	}

	h.csp.SetBlindCSPDisabled(true)
	for _, round := range rounds {
		status, code, message := call(round.handler, round.body)
		c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", round.name))
		c.Assert(code, qt.Equals, errors.ErrInvalidData.Code, qt.Commentf("%s", round.name))
		c.Assert(message, qt.Contains, csp.AnonymousVotingUnavailable, qt.Commentf("%s", round.name))
	}

	h.csp.SetBlindCSPDisabled(false)
	for _, round := range rounds {
		status, _, message := call(round.handler, round.body)
		c.Assert(status, qt.Equals, http.StatusUnauthorized, qt.Commentf("%s", round.name))
		c.Assert(message, qt.Not(qt.Contains), csp.AnonymousVotingUnavailable, qt.Commentf("%s", round.name))
	}
}
