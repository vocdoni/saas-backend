package csp

import (
	"crypto/sha256"

	"github.com/vocdoni/saas-backend/internal"
)

// buildUserID returns the userID for a given participant and bundle. The
// userID is a sha256 hash of the participantId and the bundleId concatenated.
func buildUserID(participantId string, bundleId []byte) internal.HexBytes {
	hash := sha256.Sum256(append([]byte(participantId), []byte(bundleId)...))
	return internal.HexBytes(hash[:])
}
