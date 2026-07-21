package api

import (
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
)

func TestGenerateAPIKey(t *testing.T) {
	c := qt.New(t)

	gen, err := generateAPIKey()
	c.Assert(err, qt.IsNil)
	c.Assert(strings.HasPrefix(gen.secret, APIKeyPrefix), qt.IsTrue)
	c.Assert(looksLikeAPIKey(gen.secret), qt.IsTrue)
	c.Assert(gen.prefix, qt.Equals, gen.secret[:apiKeyDisplayPrefixLen])
	// the hash is the SHA-256 of the full secret and never the secret itself
	c.Assert(gen.hash, qt.Equals, hashAPIKey(gen.secret))
	c.Assert(gen.hash, qt.Not(qt.Contains), gen.secret)
	c.Assert(gen.hash, qt.HasLen, 64) // hex-encoded sha256

	// two keys differ
	gen2, err := generateAPIKey()
	c.Assert(err, qt.IsNil)
	c.Assert(gen2.secret, qt.Not(qt.Equals), gen.secret)
	c.Assert(gen2.hash, qt.Not(qt.Equals), gen.hash)
}

func TestLooksLikeAPIKey(t *testing.T) {
	c := qt.New(t)
	c.Assert(looksLikeAPIKey("vsk_abcdef"), qt.IsTrue)
	c.Assert(looksLikeAPIKey("eyJhbGciOi.jwt.token"), qt.IsFalse)
	c.Assert(looksLikeAPIKey(""), qt.IsFalse)
}

func TestIsValidAPIKeyScope(t *testing.T) {
	c := qt.New(t)
	for _, s := range AllAPIKeyScopes {
		c.Assert(IsValidAPIKeyScope(s), qt.IsTrue)
	}
	c.Assert(IsValidAPIKeyScope("bogus:scope"), qt.IsFalse)
	c.Assert(IsValidAPIKeyScope(""), qt.IsFalse)
}

func TestRequiredScopeForRoute(t *testing.T) {
	c := qt.New(t)

	// allowlisted integrator endpoints map to their scopes
	scope, ok := requiredScopeForRoute("GET", integratorEndpoint)
	c.Assert(ok, qt.IsTrue)
	c.Assert(scope, qt.Equals, ScopeQuotaRead)

	scope, ok = requiredScopeForRoute("POST", managedOrganizationsEndpoint)
	c.Assert(ok, qt.IsTrue)
	c.Assert(scope, qt.Equals, ScopeManagedWrite)

	scope, ok = requiredScopeForRoute("GET", managedOrganizationsEndpoint)
	c.Assert(ok, qt.IsTrue)
	c.Assert(scope, qt.Equals, ScopeManagedRead)

	// account-sensitive / unmapped endpoints reject API keys (deny by default)
	_, ok = requiredScopeForRoute("POST", integratorOrgAPIKeysEndpoint)
	c.Assert(ok, qt.IsFalse)
	_, ok = requiredScopeForRoute("GET", usersMeEndpoint)
	c.Assert(ok, qt.IsFalse)

	// regression: the allowlisted (method, pattern) must match the route actually
	// registered in initRouter, or key auth can never reach the handler. These two
	// drifted once (POST /census/{id}/participants vs POST /census/{id}; POST vs PUT
	// status), which silently blocked census population and status changes via API key.
	scope, ok = requiredScopeForRoute("POST", censusIDEndpoint) // add census participants
	c.Assert(ok, qt.IsTrue)
	c.Assert(scope, qt.Equals, ScopeVotingWrite)
	scope, ok = requiredScopeForRoute("PUT", processStatusEndpoint) // change election status
	c.Assert(ok, qt.IsTrue)
	c.Assert(scope, qt.Equals, ScopeVotingWrite)
	// the previously-wrong keys must not linger (they matched no real route)
	_, ok = requiredScopeForRoute("POST", censusParticipantsEndpoint)
	c.Assert(ok, qt.IsFalse)
	_, ok = requiredScopeForRoute("POST", processStatusEndpoint)
	c.Assert(ok, qt.IsFalse)

	// every allowlisted scope is a valid, assignable scope
	for route, sc := range apiKeyAllowlist {
		c.Assert(IsValidAPIKeyScope(sc), qt.IsTrue, qt.Commentf("route %q maps to unknown scope %q", route, sc))
	}
}
