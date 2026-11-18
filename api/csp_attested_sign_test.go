package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/attestation"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// TestAttestedSign: an attested voter is signed for every question with weight 1 and no member
// lookup, each signed question lands in the voter's attestation record, check and weight answer for
// the voter, and a signer rotation revokes the token (40808).
func TestAttestedSign(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedsign123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	key, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	pid, elections := publishProcessRequest(t, token,
		newAttestedProcessRequest(orgAddress, ethcrypto.PubkeyToAddress(key.PublicKey), 10))
	c.Assert(elections, qt.HasLen, 2)
	oid := objectID(c, pid)
	voter := common.Hash{0x09}
	auth := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		attestedAuthRequest(t, key, oid, voter, "", "", time.Now().Add(10*time.Minute)), "processes", pid, "auth", "0")

	address := common.Address{0x42}
	batch := &handlers.SignBatchRequest{AuthToken: auth.AuthToken}
	for _, e := range elections {
		batch.Ballots = append(batch.Ballots, handlers.SignBatchBallot{UpstreamID: e, Address: address.Bytes()})
	}
	signed := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", batch, "processes", pid, "sign-batch")
	c.Assert(signed.Signatures, qt.HasLen, 2)
	for _, s := range signed.Signatures {
		c.Assert(s.Code, qt.Equals, "")
		c.Assert(s.Signature, qt.Not(qt.HasLen), 0)
		c.Assert(s.Weight, qt.DeepEquals, internal.HexBytes{1})
	}

	record, err := testDB.AttestationRecord(oid, attestation.UserID([12]byte(oid), voter))
	c.Assert(err, qt.IsNil)
	c.Assert(record.VoterID, qt.DeepEquals, internal.HexBytes(voter.Bytes()))
	c.Assert(record.Questions, qt.DeepEquals, elections)
	c.Assert(record.Signer, qt.Equals, ethcrypto.PubkeyToAddress(key.PublicKey))

	weight := requestAndParse[handlers.UserWeightResponse](t, http.MethodPost, "",
		&handlers.UserWeightRequest{AuthToken: auth.AuthToken}, "processes", pid, "weight")
	c.Assert(weight.Weight, qt.DeepEquals, internal.HexBytes{1})
	check := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: auth.AuthToken}, "processes", pid, "check")
	c.Assert(check.BelongsToProcess, qt.IsTrue)
	c.Assert(check.Questions, qt.HasLen, 2)
	c.Assert(check.Questions[0].CanVote, qt.IsTrue)
	c.Assert(check.Questions[0].HasSigned, qt.IsTrue)

	next, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	requestAndParse[apicommon.AttestedCensusSpec](t, http.MethodPut, token,
		&apicommon.AttestationSignerRequest{Signer: ethcrypto.PubkeyToAddress(next.PublicKey)},
		"processes", pid, "attestation-signer")
	revoked := requestAndParseWithAssertCode[errors.Error](http.StatusUnauthorized, t, http.MethodPost, "", batch,
		"processes", pid, "sign-batch")
	c.Assert(revoked.Code, qt.Equals, errors.ErrAttestationSignerRevoked.Code)
	check = requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: auth.AuthToken}, "processes", pid, "check")
	c.Assert(check.BelongsToProcess, qt.IsFalse)
}

// TestAttestedCensusFull: with maxCensusSize 2, once two voters were signed for a third one is
// refused at sign and sign-batch, and a new one already at auth/0 (40810), while a voter signed
// for keeps authenticating and signing.
func TestAttestedCensusFull(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "attestedfull123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	key, err := ethcrypto.GenerateKey()
	c.Assert(err, qt.IsNil)
	pid, elections := publishProcessRequest(t, token,
		newAttestedProcessRequest(orgAddress, ethcrypto.PubkeyToAddress(key.PublicKey), 2))
	oid := objectID(c, pid)
	soon := time.Now().Add(10 * time.Minute)
	address := common.Address{0x42}
	authenticate := func(voter byte) internal.HexBytes {
		return requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
			attestedAuthRequest(t, key, oid, common.Hash{voter}, "", "", soon), "processes", pid, "auth", "0").AuthToken
	}
	signOne := func(authToken internal.HexBytes) *handlers.SignRequest {
		return &handlers.SignRequest{AuthToken: authToken, ProcessID: elections[0], Payload: address.Hex()[2:]}
	}

	// the third voter authenticates while the census still has room
	tokens := []internal.HexBytes{authenticate(0x01), authenticate(0x02), authenticate(0x03)}
	for _, authToken := range tokens[:2] {
		requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", signOne(authToken), "processes", pid, "sign")
	}

	full := requestAndParseWithAssertCode[errors.Error](http.StatusConflict, t, http.MethodPost, "",
		signOne(tokens[2]), "processes", pid, "sign")
	c.Assert(full.Code, qt.Equals, errors.ErrAttestedCensusFull.Code)
	batch := &handlers.SignBatchRequest{AuthToken: tokens[2], Ballots: []handlers.SignBatchBallot{
		{UpstreamID: elections[1], Address: address.Bytes()},
	}}
	full = requestAndParseWithAssertCode[errors.Error](http.StatusConflict, t, http.MethodPost, "", batch,
		"processes", pid, "sign-batch")
	c.Assert(full.Code, qt.Equals, errors.ErrAttestedCensusFull.Code)
	assertAuthError(t, pid, attestedAuthRequest(t, key, oid, common.Hash{0x04}, "", "", soon),
		errors.ErrAttestedCensusFull)

	// a voter already signed for authenticates again and signs another question
	c.Assert(authenticate(0x01), qt.DeepEquals, tokens[0])
	batch.AuthToken = tokens[0]
	signed := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", batch, "processes", pid, "sign-batch")
	c.Assert(signed.Signatures, qt.HasLen, 1)
	c.Assert(signed.Signatures[0].Code, qt.Equals, "")
	record, err := testDB.AttestationRecord(oid, attestation.UserID([12]byte(oid), common.Hash{0x01}))
	c.Assert(err, qt.IsNil)
	c.Assert(record.Questions, qt.DeepEquals, elections)
	n, err := testDB.CountAttestationRecords(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))
}
