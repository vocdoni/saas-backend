package certauth

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// pkce returns a fresh verifier and its S256 challenge.
func pkce() (verifier, challenge string) {
	verifier = randomToken()
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// serveRoute sends a JSON body to handler mounted on pattern, as the main router does, from one
// client IP, and decodes the answer into out when it is not nil.
func serveRoute(c *qt.C, pattern string, handler http.HandlerFunc, path string, body, out any) int {
	data, err := json.Marshal(body)
	c.Assert(err, qt.IsNil)
	r := chi.NewRouter()
	r.Post(pattern, handler)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.RemoteAddr = "198.51.100.1:4000"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if out != nil {
		c.Assert(json.Unmarshal(rec.Body.Bytes(), out), qt.IsNil, qt.Commentf("body: %s", rec.Body))
	}
	return rec.Code
}

// post sends a create to the env's process path.
func (e *testEnv) post(c *qt.C, path string, body, out any) int {
	return serveRoute(c, "/processes/{processId}/auth/cert", e.s.Create, path, body, out)
}

// create runs a create for purpose and returns the answer and the verifier.
func (e *testEnv) create(c *qt.C, purpose string) (apicommon.CertAuthCreateResponse, string) {
	verifier, challenge := pkce()
	var resp apicommon.CertAuthCreateResponse
	code := e.post(c, "/processes/"+e.store.vp.ID.Hex()+"/auth/cert", apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testReturnURL, Purpose: purpose,
	}, &resp)
	c.Assert(code, qt.Equals, http.StatusOK)
	return resp, verifier
}

// TestCertAuthCreateValidation pins the create endpoint's checks against the in-memory store: the
// answer's shape, the body grammar, the returnUrl allowlist, the process and census state, and the
// per-IP limit.
func TestCertAuthCreateValidation(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	_, challenge := pkce()
	path := "/processes/" + e.store.vp.ID.Hex() + "/auth/cert"
	granted := 0 // the creates that spent a unit of the per-IP limit
	create := func(req apicommon.CertAuthCreateRequest) (int, errorBody) {
		var body errorBody
		status := e.post(c, path, req, &body)
		if status == http.StatusOK {
			granted++
		}
		return status, body
	}
	valid := apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testReturnURL, Purpose: "check",
	}

	// the answer: a stored attempt on a fresh nonce host, with the probe
	created, _ := e.create(c, "vote")
	granted++
	c.Assert(created.State, qt.Matches, `[A-Za-z0-9_-]{43}`)
	tx := e.store.transaction(created.State)
	c.Assert(tx.Status, qt.Equals, db.CertAuthStatusCreated)
	c.Assert(tx.Purpose, qt.Equals, "vote")
	c.Assert(created.AuthURL, qt.Equals, e.authURL(tx.Nonce, created.State))
	c.Assert(created.ProbeURL, qt.Equals, "https://probe."+testHost+":"+e.portString()+"/ok")
	expires, err := time.Parse(time.RFC3339, created.ExpiresAt)
	c.Assert(err, qt.IsNil)
	c.Assert(expires.Sub(tx.CreatedAt) > 14*time.Minute, qt.IsTrue)
	again, _ := e.create(c, "vote")
	granted++
	c.Assert(e.store.transaction(again.State).Nonce, qt.Not(qt.Equals), tx.Nonce)

	for _, u := range []string{
		testOrigin, testOrigin + "/", testOrigin + "/es", testOrigin + "/pt-br/",
		testOrigin + "/processes/0123456789abcdef01234567", "http://localhost:3000/ca/processes/0123456789abcdef01234567",
	} {
		req := valid
		req.ReturnURL = u
		status, _ := create(req)
		c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%s", u))
	}
	for _, u := range []string{
		"https://evil.test/", "https://app.test.evil.test/", testOrigin + "/?x=1", testOrigin + "/#frag",
		"https://user@app.test/", testOrigin + "/ca/processes/0123456789abcdef01234567/results",
		testOrigin + "/CA", testOrigin + "/processes/0123456789ABCDEF01234567", "http://app.test/",
		"//app.test/", "/processes/0123456789abcdef01234567", testOrigin + "/%2e%2e/", "javascript:alert(1)",
		testOrigin + "\\@evil.test", testOrigin + "/" + strings.Repeat("a", 3000),
	} {
		req := valid
		req.ReturnURL = u
		status, body := create(req)
		c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", u))
		c.Assert(body.Data.Reason, qt.Equals, "", qt.Commentf("%s", u))
	}
	for name, mutate := range map[string]func(*apicommon.CertAuthCreateRequest){
		"short challenge": func(r *apicommon.CertAuthCreateRequest) { r.Challenge = "abc" },
		"plain method":    func(r *apicommon.CertAuthCreateRequest) { r.ChallengeMethod = "plain" },
		"other purpose":   func(r *apicommon.CertAuthCreateRequest) { r.Purpose = "login" },
	} {
		req := valid
		mutate(&req)
		status, _ := create(req)
		c.Assert(status, qt.Equals, http.StatusBadRequest, qt.Commentf("%s", name))
	}

	// the process must take sign-ins, and its census sign in with a certificate
	e.store.questions = []db.VotingProcessQuestion{{Status: db.QuestionStatusEnded}, {Status: db.QuestionStatusResults}}
	status, _ := create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.questions = []db.VotingProcessQuestion{{Status: db.QuestionStatusPaused}}
	e.store.vp.EndDate = time.Now().Add(-2 * time.Minute)
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.vp.EndDate = time.Now().Add(time.Hour)
	e.store.vp.StartDate = time.Now().Add(24 * time.Hour) // upcoming is fine
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusOK)
	e.store.census.AuthMethod = ""
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	e.store.census.AuthMethod = db.CensusAuthMethodCertificate
	e.store.vp.Published = false
	status, _ = create(valid)
	c.Assert(status, qt.Equals, http.StatusConflict)
	e.store.vp.Published = true
	status = e.post(c, "/processes/"+bson.NewObjectID().Hex()+"/auth/cert", valid, nil)
	c.Assert(status, qt.Equals, http.StatusNotFound)

	// the create limit: 60 per minute from one IP on one process; refused creates spent nothing
	for granted < DefaultCreatesPerMinute {
		status, _ := create(valid)
		c.Assert(status, qt.Equals, http.StatusOK)
	}
	status, body := create(valid)
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
	c.Assert(body.Code, qt.Equals, 40950)
	c.Assert(body.Data.Reason, qt.Equals, "too_many_attempts")
	c.Assert(body.Data.RetryAfter > 0, qt.IsTrue)
}
