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
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
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
