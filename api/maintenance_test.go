package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

func TestParseMaintenanceWindow(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("")
	c.Assert(err, qt.IsNil)
	c.Assert(m, qt.IsNil)

	m, err = ParseMaintenanceWindow("2026-11-23T13:45:00+01:00/2026-11-23T14:00:00+01:00")
	c.Assert(err, qt.IsNil)
	c.Assert(m.String(), qt.Equals, "2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")

	// window exceeding 24 h is rejected
	_, err = ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-24T12:45:01Z")
	c.Assert(err, qt.IsNotNil)

	// window of exactly 24 h is accepted
	m, err = ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-24T12:45:00Z")
	c.Assert(err, qt.IsNil)
	c.Assert(m, qt.IsNotNil)

	for _, bad := range []string{
		"2026-11-23T12:45:00Z",
		"2026-11-23T12:45:00Z/soon",
		"2026-11-23 12:45/2026-11-23T13:00:00Z",
		"2026-11-23T13:00:00Z/2026-11-23T12:45:00Z",
		"2026-11-23T13:00:00Z/2026-11-23T13:00:00Z",
	} {
		_, err := ParseMaintenanceWindow(bad)
		c.Assert(err, qt.IsNotNil, qt.Commentf("%q", bad))
	}
}

func TestMaintenanceMiddleware(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")
	c.Assert(err, qt.IsNil)
	var now time.Time
	// The global middleware only sets the header; blocking is per-handler via maintenanceWriteBlock.
	handler := maintenance(m, func() time.Time { return now })(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	do := func(method, path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, http.NoBody))
		return rec
	}

	// before the window: header present, nothing blocked
	now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	rec := do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get(maintenanceHeader), qt.Equals, m.String())

	// inside the window: header still present, nothing blocked (global middleware is header-only)
	now = time.Date(2026, 11, 23, 12, 50, 0, 0, time.UTC)
	c.Assert(do(http.MethodPost, "/process").Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodDelete, "/organizations/0x1/members").Code, qt.Equals, http.StatusOK)
	c.Assert(do(http.MethodGet, "/process/0x1").Code, qt.Equals, http.StatusOK)

	// after the window: header absent
	now = time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	rec = do(http.MethodPost, "/process")
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
	c.Assert(rec.Header().Get(maintenanceHeader), qt.Equals, "")

	// no window configured
	none := maintenance(nil, time.Now)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rec = httptest.NewRecorder()
	none.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/process", http.NoBody))
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
}

func TestMaintenanceWriteBlock(t *testing.T) {
	c := qt.New(t)
	m, err := ParseMaintenanceWindow("2026-11-23T12:45:00Z/2026-11-23T13:00:00Z")
	c.Assert(err, qt.IsNil)
	var now time.Time

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// handle() auto-wraps every POST/PUT/DELETE with maintenanceWriteBlock (deny-by-default).
	// The tests below verify this wrapper directly; the handleExempt() path skips it.
	blocked := maintenanceWriteBlock(m, func() time.Time { return now }, inner)
	do := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		blocked.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, http.NoBody))
		return rec
	}

	// before the window: not blocked
	now = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	c.Assert(do("/processes/0x1/sign-batch").Code, qt.Equals, http.StatusOK)

	// inside the window: blocked with 503 and Retry-After
	now = time.Date(2026, 11, 23, 12, 50, 0, 0, time.UTC)
	rec := do("/processes/0x1/sign-batch")
	c.Assert(rec.Code, qt.Equals, http.StatusServiceUnavailable)
	c.Assert(rec.Body.String(), qt.Contains, "50304")
	// Retry-After = ceil(until-now) + jitter(0-60) = 600..660
	ra, raErr := strconv.Atoi(rec.Header().Get("Retry-After"))
	c.Assert(raErr, qt.IsNil)
	c.Assert(ra >= 600, qt.IsTrue)
	c.Assert(ra <= 660, qt.IsTrue)

	// sign/sign-batch/blind-sign/auth routes are all refused: they consume signing
	// slots or send OTPs while the vote relay is unreachable.
	for _, path := range []string{
		"/processes/0x1/sign",
		"/processes/0x1/sign-batch",
		"/processes/0x1/blind-sign",
		"/processes/0x1/blind-point",
		"/processes/0x1/auth/step0",
		"/processes/0x1/auth/resend",
		"/cert-auth/assertions",
	} {
		c.Assert(do(path).Code, qt.Equals, http.StatusServiceUnavailable, qt.Commentf("path %q should be blocked", path))
	}

	// after the window: not blocked
	now = time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC)
	c.Assert(do("/processes/0x1/sign-batch").Code, qt.Equals, http.StatusOK)

	// nil window: handler passed through unchanged (maintenanceWriteBlock returns h as-is)
	passThru := maintenanceWriteBlock(nil, time.Now, inner)
	rec = httptest.NewRecorder()
	passThru.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/process", http.NoBody))
	c.Assert(rec.Code, qt.Equals, http.StatusOK)
}

// TestMaintenanceRouter builds the real router with a window covering time.Now() and checks which
// routes it refuses, so a write route registered with handleExempt or outside handle fails here.
func TestMaintenanceRouter(t *testing.T) {
	c := qt.New(t)

	token := testCreateUser(t, testPass)
	me := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)

	// an existing user with google already linked, to log in through OAuth inside the window
	oauthEmail := fmt.Sprintf("maintenance-oauth-%d@test.com", internal.RandomInt(100000000))
	oauthSig := fmt.Sprintf("user-oauth-signature-%d", internal.RandomInt(100000000))
	sigHash, err := internal.HexHashPasswordContext(t.Context(), passwordSalt, oauthSig)
	c.Assert(err, qt.IsNil)
	linkedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = testDB.SetUser(&db.User{
		Email:    oauthEmail,
		Verified: true,
		OAuth: map[string]db.OAuthProvider{"google": {
			ExternalID:        "0x0000000000000000000000000000000000000001",
			SignatureHash:     sigHash,
			LinkedAt:          linkedAt,
			LastAuthenticated: linkedAt,
		}},
	})
	c.Assert(err, qt.IsNil)

	now := time.Now()
	a := &API{
		db:            testDB,
		auth:          testAPI.auth,
		csp:           testCSP,
		objectStorage: testAPI.objectStorage,
		account:       testAPI.account,
		voteOnChain:   testAPI.voteOnChain,
		maintenance:   &MaintenanceWindow{From: now.Add(-time.Minute), Until: now.Add(time.Hour)},
		smsStatus:     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	}
	router := a.initRouter()

	do := func(path, jwt string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(body)
		c.Assert(err, qt.IsNil)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		if jwt != "" {
			req.Header.Set("Authorization", "Bearer "+jwt)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	withProcess := func(pattern string) string {
		return strings.Replace(pattern, "{processId}", internal.HexBytes(randomProcessID()).String(), 1)
	}

	unknownEmail := fmt.Sprintf("maintenance-signup-%d@test.com", internal.RandomInt(100000000))
	blocked := []struct {
		name, path, jwt string
		body            any
	}{
		{"process sign", withProcess(processesSignEndpoint), "", map[string]any{}},
		{"process publish", withProcess(processesPublishEndpoint), token, map[string]any{}},
		{"registration", usersEndpoint, "", &apicommon.UserInfo{
			Email: unknownEmail, Password: testPass, FirstName: "M", LastName: "W",
		}},
		{"stripe webhook", paymentsWebhook, "", map[string]any{}},
		{"sms status callback", SMSStatusCallbackRoute, "", map[string]any{}},
		{"oauth sign-up", oauthLoginEndpoint, "", &apicommon.OAuthLoginRequest{
			Email: unknownEmail, Provider: "google", OAuthSignature: "sig", UserOAuthSignature: "user-sig",
			Address: "0x0000000000000000000000000000000000000002",
		}},
	}
	for _, tc := range blocked {
		rec := do(tc.path, tc.jwt, tc.body)
		c.Assert(rec.Code, qt.Equals, http.StatusServiceUnavailable, qt.Commentf("%s: %s", tc.name, rec.Body))
		var apiErr errors.Error
		c.Assert(json.Unmarshal(rec.Body.Bytes(), &apiErr), qt.IsNil, qt.Commentf("%s", tc.name))
		c.Assert(apiErr.Code, qt.Equals, errors.ErrMaintenance.Code, qt.Commentf("%s", tc.name))
		c.Assert(rec.Header().Get("Retry-After"), qt.Not(qt.Equals), "", qt.Commentf("%s", tc.name))
	}
	// neither sign-up path created the user
	_, err = testDB.UserByEmail(unknownEmail)
	c.Assert(err, qt.Equals, db.ErrNotFound)

	exempt := []struct {
		name, path, jwt string
		body            any
		want            int
	}{
		{"login", authLoginEndpoint, "", &apicommon.UserInfo{Email: me.Email, Password: testPass}, http.StatusOK},
		{"refresh", authRefresTokenEndpoint, token, nil, http.StatusOK},
		{"oauth login of a linked user", oauthLoginEndpoint, "", &apicommon.OAuthLoginRequest{
			Email: oauthEmail, Provider: "google", OAuthSignature: "sig", UserOAuthSignature: oauthSig,
		}, http.StatusOK},
		{"votes verify", votesVerifyEndpoint, "", &apicommon.VerifyVotesRequest{}, http.StatusBadRequest},
	}
	for _, tc := range exempt {
		rec := do(tc.path, tc.jwt, tc.body)
		c.Assert(rec.Code, qt.Equals, tc.want, qt.Commentf("%s: %s", tc.name, rec.Body))
	}
	// the OAuth login inside the window wrote nothing
	user, err := testDB.UserByEmail(oauthEmail)
	c.Assert(err, qt.IsNil)
	c.Assert(user.OAuth["google"].LastAuthenticated.Equal(linkedAt), qt.IsTrue)
}
