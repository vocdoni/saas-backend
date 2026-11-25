// Package handlers provides HTTP handlers for the CSP
// API endpoints, managing authentication, token verification, and cryptographic
// signing operations for voting processes in the Vocdoni voting platform.
package handlers

import (
	"context"

	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// DefaultOrgName names the organization in the voter's OTP when it has no name.
const DefaultOrgName = "Vocdoni"

// CSPHandlers is a struct that contains an instance of the CSP and the main
// database (where the process and census data is stored). It is used to handle
// the CSP API requests such as the authentication and signing of the voting
// processes.
type CSPHandlers struct {
	csp         *csp.CSP
	mainDB      *db.MongoStorage
	voteOnChain VoteOnChainLookup
	// hashPassword computes a member password hash in the service-wide argon2 slots; a seam so tests
	// can count the checks run.
	hashPassword func(ctx context.Context, salt string, memberID bson.ObjectID, clientHash string) ([]byte, error)
}

// New creates a new instance of the CSP handlers instance. It receives the CSP
// instance, the main database instance and the lookup that tells whether a vote
// is on chain (used to report a voter's hasVoted).
func New(c *csp.CSP, mainDB *db.MongoStorage, voteOnChain VoteOnChainLookup) *CSPHandlers {
	return &CSPHandlers{
		csp:          c,
		mainDB:       mainDB,
		voteOnChain:  voteOnChain,
		hashPassword: db.HashMemberPasswordContext,
	}
}
