package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestRefreshToken exercises the protected POST /auth/refresh endpoint,
// asserting a fresh token is issued for an authenticated user and that
// unauthenticated requests are rejected with 401.
func TestRefreshToken(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token := testCreateUser(t, testPass)

	// Success: an authenticated user receives a fresh login response.
	res := requestAndParse[apicommon.LoginResponse](
		t, http.MethodPost, token, nil, authRefresTokenEndpoint,
	)
	c.Assert(res.Token, qt.Not(qt.Equals), "")

	// Unauthorized: requests without a token are rejected.
	requestAndAssertCode(
		http.StatusUnauthorized, t, http.MethodPost, "", nil, authRefresTokenEndpoint,
	)
}
