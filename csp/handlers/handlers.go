// Package handlers provides HTTP handlers for the CSP
// API endpoints, managing authentication, token verification, and cryptographic
// signing operations for voting processes in the Vocdoni voting platform.
package handlers

import (
	"runtime"

	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
)

const (
	DefaultOrgName = "Vocdoni"
	DefaultOrgLogo = "https://tomato-giant-grasshopper-196.mypinata.cloud/ipfs/" +
		"bafkreifqyu5m5as4gvcirlog5j267um24q7y4ri6r3svhsi7fda24676ny"
)

// CSPHandlers is a struct that contains an instance of the CSP and the main
// database (where the process and census data is stored). It is used to handle
// the CSP API requests such as the authentication and signing of the voting
// processes.
type CSPHandlers struct {
	csp    *csp.CSP
	mainDB *db.MongoStorage
	// passwordSlots bounds the member password checks running at once: each is an argon2id pass
	// holding 64 MiB, so excess login attempts wait for a slot instead of piling up memory.
	passwordSlots chan struct{}
}

// New creates a new instance of the CSP handlers instance. It receives the CSP
// instance and the main database instance as parameters.
func New(c *csp.CSP, mainDB *db.MongoStorage) *CSPHandlers {
	return &CSPHandlers{
		csp:           c,
		mainDB:        mainDB,
		passwordSlots: make(chan struct{}, runtime.NumCPU()),
	}
}
