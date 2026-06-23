package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// APIKeyPrefix is the public, non-secret prefix every API key secret starts with. It lets the
// authenticator route a credential to the API-key path (vs. JWT) and helps users recognize a key.
const APIKeyPrefix = "vsk_"

// apiKeyDisplayPrefixLen is how many characters of the secret are kept (in clear) for display.
const apiKeyDisplayPrefixLen = 12

// API key scopes. Keys are deny-by-default: a scope only grants the endpoints mapped to it in
// apiKeyAllowlist, and any endpoint not in that allowlist rejects API-key auth entirely.
const (
	ScopeQuotaRead    = "quota:read"    // read integrator quota & usage
	ScopeManagedRead  = "managed:read"  // list managed organizations
	ScopeManagedWrite = "managed:write" // create managed organizations
	ScopeVotingWrite  = "voting:write"  // create/publish processes, censuses and bundles
	ScopeMembersWrite = "members:write" // manage members and groups
)

// AllAPIKeyScopes is the canonical set of assignable scopes, used to validate key creation.
var AllAPIKeyScopes = []string{
	ScopeQuotaRead,
	ScopeManagedRead,
	ScopeManagedWrite,
	ScopeVotingWrite,
	ScopeMembersWrite,
}

// IsValidAPIKeyScope reports whether s is a known, assignable scope.
func IsValidAPIKeyScope(s string) bool {
	for _, sc := range AllAPIKeyScopes {
		if sc == s {
			return true
		}
	}
	return false
}

// apiKeyAllowlist maps "<METHOD> <chi route pattern>" to the scope required to call it with an
// API key. Endpoints absent from this map cannot be called with an API key at all (deny by
// default), which keeps account-sensitive endpoints (auth, password, OAuth, team/invites,
// tickets, checkout, etc.) JWT-only. The patterns must match the constants used at registration
// so they equal chi's RoutePattern().
var apiKeyAllowlist = map[string]string{
	// integrator
	"GET " + integratorEndpoint:            ScopeQuotaRead,
	"GET " + managedOrganizationsEndpoint:  ScopeManagedRead,
	"POST " + managedOrganizationsEndpoint: ScopeManagedWrite,

	// members & groups (of managed organizations)
	"GET " + organizationMembersEndpoint:             ScopeMembersWrite,
	"POST " + organizationAddMembersEndpoint:         ScopeMembersWrite,
	"PUT " + organizationUpsertMemberEndpoint:        ScopeMembersWrite,
	"DELETE " + organizationDeleteMembersEndpoint:    ScopeMembersWrite,
	"GET " + organizationAddMembersJobStatusEndpoint: ScopeMembersWrite,
	"POST " + organizationGroupsEndpoint:             ScopeMembersWrite,
	"GET " + organizationGroupsEndpoint:              ScopeMembersWrite,
	"GET " + organizationGroupEndpoint:               ScopeMembersWrite,
	"PUT " + organizationGroupEndpoint:               ScopeMembersWrite,
	"DELETE " + organizationGroupEndpoint:            ScopeMembersWrite,
	"GET " + organizationGroupMembersEndpoint:        ScopeMembersWrite,
	"POST " + organizationGroupValidateEndpoint:      ScopeMembersWrite,

	// voting: processes, censuses, bundles (for managed organizations)
	"POST " + processCreateEndpoint:                ScopeVotingWrite,
	"POST " + processPublishEndpoint:               ScopeVotingWrite,
	"POST " + processStatusEndpoint:                ScopeVotingWrite,
	"GET " + organizationListProcessDraftsEndpoint: ScopeVotingWrite,
	"GET " + organizationCensusesEndpoint:          ScopeVotingWrite,
	"GET " + organizationBundlesEndpoint:           ScopeVotingWrite,
	"POST " + censusEndpoint:                       ScopeVotingWrite,
	"POST " + censusPublishEndpoint:                ScopeVotingWrite,
	"POST " + censusGroupPublishEndpoint:           ScopeVotingWrite,
	"POST " + censusParticipantsEndpoint:           ScopeVotingWrite,
	"POST " + processBundleEndpoint:                ScopeVotingWrite,
	"PUT " + processBundleUpdateEndpoint:           ScopeVotingWrite,
}

// requiredScopeForRoute returns the scope required to call (method, pattern) with an API key and
// whether the endpoint allows API-key auth at all.
func requiredScopeForRoute(method, pattern string) (string, bool) {
	scope, ok := apiKeyAllowlist[method+" "+pattern]
	return scope, ok
}

// looksLikeAPIKey reports whether the given bearer credential is an API key (vs. a JWT).
func looksLikeAPIKey(token string) bool {
	return strings.HasPrefix(token, APIKeyPrefix)
}

// hashAPIKey returns the hex-encoded SHA-256 of an API key secret. The hash (never the secret) is
// what gets stored and looked up.
func hashAPIKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// generatedAPIKey holds the parts of a freshly generated key: the plaintext secret (shown once),
// its display prefix, and the hash to store.
type generatedAPIKey struct {
	secret string
	prefix string
	hash   string
}

// generateAPIKey creates a new random API key.
func generateAPIKey() (generatedAPIKey, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return generatedAPIKey{}, err
	}
	secret := APIKeyPrefix + hex.EncodeToString(buf)
	return generatedAPIKey{
		secret: secret,
		prefix: secret[:apiKeyDisplayPrefixLen],
		hash:   hashAPIKey(secret),
	}, nil
}
