package api

import (
	"crypto/ecdsa"
	"crypto/rand"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/attestation"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// attestedAuthRequest builds the auth/0 body of an attested voter: an attestation for processID and
// voter, signed by key, opening contact with a fresh salt when contact is not empty.
func attestedAuthRequest(
	t *testing.T, key *ecdsa.PrivateKey, processID bson.ObjectID, voter common.Hash,
	kind attestation.ContactKind, contact string, expiresAt time.Time,
) *handlers.AuthRequest {
	t.Helper()
	att := attestation.Attestation{VoterID: voter, ExpiresAt: uint64(expiresAt.Unix())}
	copy(att.ProcessID[:], processID[:])
	req := &handlers.AuthRequest{}
	if contact != "" {
		salt := make([]byte, attestation.SaltLen)
		_, err := rand.Read(salt)
		qt.Assert(t, err, qt.IsNil)
		att.ContactHash = attestation.ContactHash(salt, kind, contact)
		req.Contact, req.Salt = contact, salt
	}
	domain, err := testCSP.AttestationDomain()
	qt.Assert(t, err, qt.IsNil)
	digest := domain.Digest(&att)
	sig, err := ethcrypto.Sign(digest.Bytes(), key)
	qt.Assert(t, err, qt.IsNil)
	sig[64] += 27
	req.Attestation = &handlers.AttestationPayload{
		ProcessID:   processID[:],
		VoterID:     att.VoterID.Bytes(),
		ContactHash: att.ContactHash.Bytes(),
		ExpiresAt:   att.ExpiresAt,
	}
	req.Signature = sig
	return req
}

// assertAuthError posts an auth/0 body and asserts the error code it answers.
func assertAuthError(t *testing.T, pid string, req *handlers.AuthRequest, want errors.Error) {
	t.Helper()
	got := requestAndParseWithAssertCode[errors.Error](want.HTTPstatus, t, http.MethodPost, "", req,
		"processes", pid, "auth", "0")
	qt.Assert(t, got.Code, qt.Equals, want.Code)
}

// TestAttestedAuthEmail drives auth/0, auth/1 and resend of an attested census with email 2FA, and
// each refusal of auth/0 and resend.
func TestAttestedAuthEmail(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedauth123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	key, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	other, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	req := newAttestedProcessRequest(orgAddress, ethcrypto.PubkeyToAddress(key.PublicKey), 10)
	req.Census.TwoFaFields = db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}
	pid, _ := publishProcessRequest(t, token, req)
	oid := objectID(c, pid)
	voter := common.Hash{0x01}
	email := fmt.Sprintf("attested%d@example.org", time.Now().UnixNano())
	soon := time.Now().Add(10 * time.Minute)

	c.Run("refusals", func(*qt.C) {
		assertAuthError(t, pid, attestedAuthRequest(t, other, oid, voter, attestation.ContactEmail, email, soon),
			errors.ErrAttestationInvalid)
		tampered := attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email, soon)
		tampered.Attestation.VoterID = common.Hash{0x02}.Bytes()
		assertAuthError(t, pid, tampered, errors.ErrAttestationInvalid)
		assertAuthError(t, pid, attestedAuthRequest(t, key, bson.NewObjectID(), voter, attestation.ContactEmail, email, soon),
			errors.ErrAttestationInvalid)
		assertAuthError(t, pid, attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email,
			time.Now().Add(-time.Minute)), errors.ErrAttestationExpired)
		assertAuthError(t, pid, attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email,
			time.Now().Add(2*time.Hour)), errors.ErrAttestationExpired)
		wrongContact := attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email, soon)
		wrongContact.Contact = "someone.else@example.org"
		assertAuthError(t, pid, wrongContact, errors.ErrContactHashMismatch)
		assertAuthError(t, pid, attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, "Voter@Example.org", soon),
			errors.ErrInvalidData)
		noContact := attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email, soon)
		noContact.Contact, noContact.Salt = "", nil
		assertAuthError(t, pid, noContact, errors.ErrInvalidData)
	})

	authReq := attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, email, soon)
	step0 := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
	c.Assert(step0.AuthToken, qt.Not(qt.HasLen), 0)
	stored, err := testDB.CSPAuth(step0.AuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.UserID, qt.DeepEquals, internal.HexBytes(attestation.UserID([12]byte(oid), voter)))
	c.Assert(stored.Attestation, qt.Not(qt.IsNil))
	c.Assert(stored.Attestation.ContactKind, qt.Equals, "email")
	c.Assert(stored.Attestation.Signer, qt.Equals, ethcrypto.PubkeyToAddress(key.PublicKey))
	_ = waitForEmail(t, email)

	wrongSalt := requestAndParseWithAssertCode[errors.Error](http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Contact: email, Salt: make([]byte, attestation.SaltLen)},
		"processes", pid, "auth", "resend")
	c.Assert(wrongSalt.Code, qt.Equals, errors.ErrContactHashMismatch.Code)
	resent := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AuthResendRequest{AuthToken: step0.AuthToken, Contact: email, Salt: authReq.Salt},
		"processes", pid, "auth", "resend")
	c.Assert(resent.AuthToken, qt.DeepEquals, step0.AuthToken)

	verified := verifyProcessCSP(t, pid, email, step0.AuthToken)
	c.Assert(verified, qt.DeepEquals, step0.AuthToken)
}

// TestAttestedAuthOnly: without 2FA, auth/0 answers a pre-verified token, reuses it on the next
// login, refuses a contact, and after a rotation refuses the old signer and issues a new token.
func TestAttestedAuthOnly(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedonly123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	key, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	pid, _ := publishProcessRequest(t, token,
		newAttestedProcessRequest(orgAddress, ethcrypto.PubkeyToAddress(key.PublicKey), 10))
	oid := objectID(c, pid)
	voter := common.Hash{0x07}
	soon := time.Now().Add(10 * time.Minute)

	first := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		attestedAuthRequest(t, key, oid, voter, "", "", soon), "processes", pid, "auth", "0")
	stored, err := testDB.CSPAuth(first.AuthToken)
	c.Assert(err, qt.IsNil)
	c.Assert(stored.Verified, qt.IsTrue)
	again := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		attestedAuthRequest(t, key, oid, voter, "", "", soon), "processes", pid, "auth", "0")
	c.Assert(again.AuthToken, qt.DeepEquals, first.AuthToken)

	withContact := attestedAuthRequest(t, key, oid, voter, "", "", soon)
	withContact.Contact, withContact.Salt = "voter@example.org", make([]byte, attestation.SaltLen)
	assertAuthError(t, pid, withContact, errors.ErrInvalidData)

	// A validly signed attestation with a non-zero contactHash must be refused on a no-2FA census:
	// the census stores no contactHash and an auditor recomputing with zero finds a mismatch.
	withNonZeroHash := attestedAuthRequest(t, key, oid, voter, attestation.ContactEmail, "refusal@example.org", soon)
	withNonZeroHash.Contact, withNonZeroHash.Salt = "", nil
	assertAuthError(t, pid, withNonZeroHash, errors.ErrAttestationInvalid)

	next, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	requestAndParse[apicommon.AttestedCensusSpec](t, http.MethodPut, token,
		&apicommon.AttestationSignerRequest{Signer: ethcrypto.PubkeyToAddress(next.PublicKey)},
		"processes", pid, "attestation-signer")
	assertAuthError(t, pid, attestedAuthRequest(t, key, oid, voter, "", "", soon), errors.ErrAttestationInvalid)
	rotated := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		attestedAuthRequest(t, next, oid, voter, "", "", soon), "processes", pid, "auth", "0")
	c.Assert(rotated.AuthToken, qt.Not(qt.DeepEquals), first.AuthToken)
}
