package handlers

import (
	"github.com/vocdoni/saas-backend/internal"
)

// AuthRequest defines the payload for participant authentication
type AuthRequest struct {
	ParticipantNo string `json:"participantNo"`
	Email         string `json:"email,omitempty"`
	Phone         string `json:"phone,omitempty"`
	Password      string `json:"password,omitempty"`
}

type AuthResponse struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty"`
	Signature internal.HexBytes `json:"signature,omitempty"`
}

// AuthRequest defines the payload for requesting authentication
type AuthChallengeRequest struct {
	AuthToken internal.HexBytes `json:"authToken,omitempty"`
	AuthData  []string          `json:"authData,omitempty"` // reserved for the auth handler
}
