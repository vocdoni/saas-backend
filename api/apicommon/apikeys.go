package apicommon

import (
	"time"

	"github.com/vocdoni/saas-backend/db"
)

// CreateAPIKeyRequest is the body of POST /integrator/organizations/{orgAddress}/apikeys.
type CreateAPIKeyRequest struct {
	// Human-readable label to identify the key.
	Label string `json:"label"`
	// Scopes the key is allowed to use (must be a subset of the assignable scopes).
	Scopes []string `json:"scopes"`
	// Optional expiration; when set, the key stops working after this time.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// APIKeyInfo is the public (no-secret) representation of an API key.
type APIKeyInfo struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	Revoked    bool       `json:"revoked"`
}

// CreateAPIKeyResponse is returned once at creation and includes the plaintext secret, which is
// never retrievable again.
type CreateAPIKeyResponse struct {
	APIKeyInfo
	// Secret is the full API key, shown only at creation time.
	Secret string `json:"secret"`
}

// ListAPIKeysResponse is the list of an organization's API keys.
type ListAPIKeysResponse struct {
	APIKeys []APIKeyInfo `json:"apiKeys"`
}

// APIKeyInfoFromDB converts a db.APIKey to its public representation.
func APIKeyInfoFromDB(k *db.APIKey) APIKeyInfo {
	if k == nil {
		return APIKeyInfo{}
	}
	return APIKeyInfo{
		ID:         k.ID,
		Label:      k.Label,
		Prefix:     k.Prefix,
		Scopes:     k.Scopes,
		CreatedBy:  k.CreatedBy,
		CreatedAt:  k.CreatedAt,
		LastUsedAt: k.LastUsedAt,
		ExpiresAt:  k.ExpiresAt,
		Revoked:    k.Revoked,
	}
}
