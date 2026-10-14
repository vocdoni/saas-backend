// Package handlers provides HTTP handlers for the CSP
// API endpoints, managing authentication, token verification, and cryptographic
// signing operations for voting processes in the Vocdoni voting platform.
package handlers

import (
	"runtime"
	"time"

	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	DefaultOrgName = "Vocdoni"
	DefaultOrgLogo = "https://tomato-giant-grasshopper-196.mypinata.cloud/ipfs/" +
		"bafkreifqyu5m5as4gvcirlog5j267um24q7y4ri6r3svhsi7fda24676ny"
)

// passwordSlotWait bounds how long a member password check waits for a slot. A waiting request
// holds one of the router's global throttle tokens, so an unbounded wait during a login burst would
// starve every other route and end in the 45s request timeout as a 500. Past it the voter is told to
// retry (errors.ErrAttemptCoolDownTime) instead.
const passwordSlotWait = 5 * time.Second

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
	// passwordSlotWait is how long a check waits for a slot (passwordSlotWait, shortened by tests).
	passwordSlotWait time.Duration
	// hashPassword computes a member password hash; a seam so tests can count the checks run.
	hashPassword func(salt string, memberID bson.ObjectID, clientHash string) []byte
}

// New creates a new instance of the CSP handlers instance. It receives the CSP
// instance and the main database instance as parameters.
func New(c *csp.CSP, mainDB *db.MongoStorage) *CSPHandlers {
	return &CSPHandlers{
		csp:    c,
		mainDB: mainDB,
		// GOMAXPROCS rather than NumCPU: since Go 1.25 it follows the container's cgroup CPU quota,
		// while NumCPU reports every core of the host, so a 1-CPU pod on a large node would allow
		// dozens of 64 MiB checks at once
		passwordSlots:    make(chan struct{}, runtime.GOMAXPROCS(0)),
		passwordSlotWait: passwordSlotWait,
		hashPassword:     db.HashMemberPassword,
	}
}
