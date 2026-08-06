package api

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"net/http"
	"testing"

	blind "github.com/arnaucube/go-blindsecp256k1"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	dvotetypes "go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// blindBallot blinds a CA bundle against the CSP's R point, retrying until the
// blinded value is a full 32 bytes. About one blinding in 256 comes out short,
// which the signer rejects; a real client does the same thing on ErrRetryBlinding.
func blindBallot(
	c *qt.C, tokenR internal.HexBytes, bundle *models.CAbundle,
) (msgHash []byte, blinded internal.HexBytes, secret *blind.UserSecretData) {
	bundleBytes, err := proto.Marshal(bundle)
	c.Assert(err, qt.IsNil)
	msgHash = ethereum.HashRaw(bundleBytes)

	r, err := blind.NewPointFromBytes(tokenR)
	c.Assert(err, qt.IsNil)
	for {
		m, userSecret, err := blind.Blind(new(big.Int).SetBytes(msgHash), r)
		c.Assert(err, qt.IsNil)
		if len(m.Bytes()) == 32 {
			return msgHash, m.Bytes(), userSecret
		}
	}
}

// TestProcessCSPAnonymousVoting casts a real vote on chain using a blind
// signature, and asserts the thing the feature exists for: the backend cannot
// say which address the voter used.
func TestProcessCSPAnonymousVoting(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(1)...)
	ids := memberIDs(members)

	req := newVotingProcessRequest(orgAddress, ids)
	req.StartDate = ""
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsName, db.OrgMemberAuthFieldsSurname}
	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint)
	pid := created.ProcessID

	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", pid, "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("job error: %s", job.Errors))

	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, token, nil, "processes", pid)
	election := got.Questions[0].UpstreamID
	c.Assert(election, qt.Not(qt.HasLen), 0)

	// the voter's on-chain key. The CSP must never learn this address.
	vocdoniClient := testNewVocdoniClient(t)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)

	authTok := authProcessCSP(t, pid, &handlers.AuthRequest{
		Name:    members[0].Name,
		Surname: members[0].Surname,
		Email:   members[0].Email,
	})

	// --- step 1: open the anonymous signing session ---
	prep := requestAndParse[handlers.AnonymousSignPrepareResponse](t, http.MethodPost, "",
		&handlers.AnonymousSignPrepareRequest{AuthToken: authTok, ProcessID: election},
		"processes", pid, "sign", "anonymous", "prepare")
	c.Assert(prep.TokenR, qt.HasLen, 33)
	c.Assert(prep.WeightCert, qt.Not(qt.HasLen), 0)

	// --- step 2: blind the ballot bundle client-side ---
	weight := new(big.Int).SetBytes(prep.Weight)
	bundle := &models.CAbundle{
		ProcessId:  election,
		Address:    voter.Address().Bytes(),
		VoteWeight: prep.Weight,
	}
	msgHash, blinded, userSecret := blindBallot(c, prep.TokenR, bundle)

	// --- step 3: have the CSP sign a message it cannot read ---
	signed := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "",
		&handlers.AnonymousSignRequest{
			AuthToken: authTok,
			ProcessID: election,
			TokenR:    prep.TokenR,
			Payload:   hex.EncodeToString(blinded),
		}, "processes", pid, "sign", "anonymous")
	c.Assert(signed.Signature, qt.Not(qt.HasLen), 0)

	// --- step 4: unblind, and check it the way the chain will ---
	signature := blind.Unblind(new(big.Int).SetBytes(signed.Signature), userSecret)
	salt := make([]byte, saltedkey.SaltSize)
	copy(salt, election[:saltedkey.SaltSize])
	saltedPub, err := saltedkey.SaltBlindPubKey(testCSP.BlindPubKey(), salt)
	c.Assert(err, qt.IsNil)
	c.Assert(blind.Verify(new(big.Int).SetBytes(msgHash), signature, saltedPub), qt.IsTrue,
		qt.Commentf("blind signature must verify against the salted census root"))

	// --- step 5: cast it on chain as an ECDSA_BLIND_PIDSALTED proof ---
	proof := &models.Proof{
		Payload: &models.Proof_Ca{
			Ca: &models.ProofCA{
				Type:      models.ProofCA_ECDSA_BLIND_PIDSALTED,
				Signature: signature.BytesUncompressed(),
				Bundle:    bundle,
			},
		},
	}
	votesBefore, err := vocdoniClient.ElectionVoteCount(dvotetypes.HexBytes(election))
	c.Assert(err, qt.IsNil)

	nullifier := testCastVote(t, vocdoniClient, &voter, election, proof, []byte(`["1"]`))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	votesAfter, err := vocdoniClient.ElectionVoteCount(dvotetypes.HexBytes(election))
	c.Assert(err, qt.IsNil)
	c.Assert(votesAfter, qt.Equals, votesBefore+1,
		qt.Commentf("the chain must accept the blind proof; got %d votes", votesAfter))
	c.Assert(weight.Uint64() > 0, qt.IsTrue)

	// --- the point of all this: the backend kept no link to the voter's address ---
	cspProc, err := testDB.CSPProcessByUserAndProcess(
		internal.HexBytesFromString(members[0].ID), election)
	c.Assert(err, qt.IsNil)
	c.Assert(cspProc.Used, qt.IsTrue)
	c.Assert(cspProc.UsedAddress, qt.HasLen, 0,
		qt.Commentf("the CSP must not have recorded the voter's address"))

	signInfo := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: authTok}, "processes", pid, "sign-info")
	c.Assert(signInfo.Consumed, qt.HasLen, 1)
	c.Assert(bytes.Equal(signInfo.Consumed[0].UpstreamID, election), qt.IsTrue)
	c.Assert(signInfo.Consumed[0].Address, qt.HasLen, 0)
	c.Assert(signInfo.Consumed[0].Nullifier, qt.HasLen, 0)

	// --- and the signature is single-use: no second anonymous signature ---
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
		&handlers.AnonymousSignPrepareRequest{AuthToken: authTok, ProcessID: election},
		"processes", pid, "sign", "anonymous", "prepare")
}
