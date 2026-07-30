package api

import (
	"math/big"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
)

// blindVoteEnvelope builds a vote envelope carrying a blind CSP proof declaring
// the given weight. A nil weight means the bundle omits it, which the chain
// treats as weight 1.
func blindVoteEnvelope(pid internal.HexBytes, weight []byte, t models.ProofCA_Type) *models.VoteEnvelope {
	return &models.VoteEnvelope{
		ProcessId: pid,
		Proof: &models.Proof{
			Payload: &models.Proof_Ca{
				Ca: &models.ProofCA{
					Type: t,
					Bundle: &models.CAbundle{
						ProcessId:  pid,
						Address:    util.RandomBytes(20),
						VoteWeight: weight,
					},
				},
			},
		},
	}
}

// TestCheckBlindVoteWeight covers the relay's backstop against a voter inflating
// the weight inside a bundle the CSP blind-signed without being able to read it.
func TestCheckBlindVoteWeight(t *testing.T) {
	c := qt.New(t)

	pid := internal.HexBytes(util.RandomBytes(32))
	const weight = uint64(42)

	// an attestation the CSP would have issued at prepare time
	token := internal.HexBytes(util.RandomBytes(16))
	c.Assert(testDB.SetCSPAuth(token, internal.HexBytes(util.RandomBytes(12)), pid, ""), qt.IsNil)
	c.Assert(testDB.VerifyCSPAuth(token), qt.IsNil)
	_, weightCert, err := testCSP.PrepareBlindSign(token, pid, weight)
	c.Assert(err, qt.IsNil)

	weightBytes := new(big.Int).SetUint64(weight).Bytes()

	c.Run("accepts the attested weight", func(c *qt.C) {
		env := blindVoteEnvelope(pid, weightBytes, models.ProofCA_ECDSA_BLIND_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, weightCert) == nil, qt.IsTrue)
	})

	c.Run("rejects an inflated weight", func(c *qt.C) {
		// the attack this exists to catch: the CSP signed a blinded bundle it
		// could not read, so the voter is free to put any number in it
		inflated := new(big.Int).SetUint64(1_000_000).Bytes()
		env := blindVoteEnvelope(pid, inflated, models.ProofCA_ECDSA_BLIND_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, weightCert) != nil, qt.IsTrue)
	})

	c.Run("rejects a missing certificate", func(c *qt.C) {
		env := blindVoteEnvelope(pid, weightBytes, models.ProofCA_ECDSA_BLIND_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, nil) != nil, qt.IsTrue)
	})

	c.Run("rejects a certificate for another election", func(c *qt.C) {
		other := internal.HexBytes(util.RandomBytes(32))
		env := blindVoteEnvelope(other, weightBytes, models.ProofCA_ECDSA_BLIND_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, other, weightCert) != nil, qt.IsTrue)
	})

	c.Run("no declared weight needs no attestation", func(c *qt.C) {
		// the chain applies weight 1 when the bundle omits it
		env := blindVoteEnvelope(pid, nil, models.ProofCA_ECDSA_BLIND_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, nil) == nil, qt.IsTrue)
	})

	c.Run("plain CSP proofs are not affected", func(c *qt.C) {
		// the CSP built and signed that bundle itself, weight included
		env := blindVoteEnvelope(pid, weightBytes, models.ProofCA_ECDSA_PIDSALTED)
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, nil) == nil, qt.IsTrue)
	})

	c.Run("non-CSP proofs are not affected", func(c *qt.C) {
		env := &models.VoteEnvelope{ProcessId: pid, Proof: &models.Proof{}}
		c.Assert(testAPI.checkBlindVoteWeight(env, pid, nil) == nil, qt.IsTrue)
	})
}
