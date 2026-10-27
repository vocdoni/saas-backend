package legacycsp

import (
	"context"
	"errors"
	"math/big"
	"os"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/testcontainers/testcontainers-go/modules/mongodb"
	blind "github.com/vocdoni/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/vochain-sequencer/crypto/ethereum"
	"github.com/vocdoni/vochain-sequencer/util"
	"github.com/vocdoni/vochain-sequencer/vochain/state"
	"github.com/vocdoni/vochain-sequencer/vochain/transaction/proofs/cspproof"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// mongoImage is the MongoDB image of saas-backend test.MongoImage. That package is not imported: it
// links go.vocdoni.io/dvote/apiclient, which does not build against the proto version the
// vochain-sequencer requires.
const mongoImage = "mongo:7.0.45@sha256:9650758f83af117fc96699f030a44f9300bed839734e67012f56df05b2fd6290"

var testMongoURI string

func TestMain(m *testing.M) {
	ctx := context.Background()
	mongo, err := mongodb.Run(ctx, mongoImage)
	if err != nil {
		panic(err)
	}
	testMongoURI, err = mongo.ConnectionString(ctx)
	if err != nil {
		panic(err)
	}
	code := m.Run()
	_ = mongo.Terminate(ctx)
	os.Exit(code)
}

// newLegacyCSP returns a CSP on a fresh database and a verified auth token of one of its users.
func newLegacyCSP(c *qt.C) (*csp.CSP, internal.HexBytes) {
	storage, err := db.New(testMongoURI, "legacycsp"+util.RandomHex(8))
	c.Assert(err, qt.IsNil)
	ctx, cancel := context.WithCancel(context.Background())
	c.Cleanup(cancel)
	rootKey := new(internal.HexBytes).SetString("700e669712473377a92457f3ff2a4d8f6b17e139f127738018a80fe26983f410")
	signer, err := csp.New(ctx, &csp.Config{DB: storage, RootKey: *rootKey})
	c.Assert(err, qt.IsNil)
	token := internal.HexBytes(util.RandomBytes(16))
	c.Assert(storage.SetCSPAuth(token, internal.HexBytes("user"), internal.HexBytes("anchor"), ""), qt.IsNil)
	c.Assert(storage.VerifyCSPAuth(token), qt.IsNil)
	return signer, token
}

// verifyLegacy runs the vochain-sequencer verifier of a legacy CSP census origin on proof, cast by
// voter on election pid with the given census root.
func verifyLegacy(origin models.CensusOrigin, root, pid []byte, proof *models.ProofCA, voter *ethereum.SignKeys,
) (bool, *big.Int, error) {
	verifier := &cspproof.ProofVerifierCSP{V2: origin == models.CensusOrigin_OFF_CHAIN_CA_V2}
	return verifier.Verify(
		&models.Process{ProcessId: pid, CensusRoot: root, CensusOrigin: origin},
		&models.VoteEnvelope{ProcessId: pid, Proof: &models.Proof{Payload: &models.Proof_Ca{Ca: proof}}},
		state.NewVoterID(state.VoterIDTypeECDSA, voter.PublicKey()))
}

// TestLegacyECDSASignatureVerifies signs a ballot of a question published on the legacy
// OFF_CHAIN_CA origin (empty census origin) with the saas-backend CSP, and checks that the
// vochain-sequencer verifies the proof against the CSP public key.
func TestLegacyECDSASignatureVerifies(t *testing.T) {
	c := qt.New(t)
	signer, token := newLegacyCSP(c)
	root, err := signer.PubKey()
	c.Assert(err, qt.IsNil)
	voter := ethereum.NewSignKeysBatch(1)[0]
	pid := util.RandomBytes(32)
	weight := big.NewInt(42)

	sig, err := signer.SignBallot(&csp.BallotRequest{
		Token: token, Address: voter.Address().Bytes(), Weight: weight.Bytes(),
		Election: csp.Election{ID: pid},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(sig.ProofType, qt.Equals, models.ProofCA_ECDSA_PIDSALTED.String())
	proof := &models.ProofCA{
		Type:      models.ProofCA_ECDSA_PIDSALTED,
		Signature: sig.Signature,
		Bundle:    &models.CAbundle{ProcessId: pid, Address: voter.Address().Bytes(), VoteWeight: weight.Bytes()},
	}

	valid, gotWeight, err := verifyLegacy(models.CensusOrigin_OFF_CHAIN_CA, root, pid, proof, voter)
	c.Assert(err, qt.IsNil)
	c.Assert(valid, qt.IsTrue)
	c.Assert(gotWeight.String(), qt.Equals, weight.String())

	// another voter cannot reuse the proof
	_, _, err = verifyLegacy(models.CensusOrigin_OFF_CHAIN_CA, root, pid, proof, ethereum.NewSignKeysBatch(1)[0])
	c.Assert(err, qt.IsNotNil)
}

// TestLegacyBlindSignatureVerifies runs the two-round blind signature of a question published on
// the legacy OFF_CHAIN_CA_V2 origin (empty census origin) with the saas-backend CSP, and checks
// that the vochain-sequencer verifies the unblinded proof against the CSP blind public key, for the
// weight the CSP pinned.
func TestLegacyBlindSignatureVerifies(t *testing.T) {
	c := qt.New(t)
	signer, token := newLegacyCSP(c)
	root, err := signer.BlindPubKey()
	c.Assert(err, qt.IsNil)
	voter := ethereum.NewSignKeysBatch(1)[0]
	pid := util.RandomBytes(32)
	election := csp.Election{ID: pid}

	r, weight, err := signer.NewBlindBallotRequest(token, election, big.NewInt(7).Bytes())
	c.Assert(err, qt.IsNil)
	point, err := blind.NewPointFromBytes(r)
	c.Assert(err, qt.IsNil)
	bundle := &models.CAbundle{ProcessId: pid, Address: voter.Address().Bytes(), VoteWeight: weight}
	bundleBytes, err := proto.Marshal(bundle)
	c.Assert(err, qt.IsNil)
	m := new(big.Int).SetBytes(ethereum.HashRaw(bundleBytes))

	var sig *csp.BallotSignature
	var secret *blind.UserSecretData
	for range 40 {
		var blinded *big.Int
		blinded, secret, err = blind.Blind(m, point)
		c.Assert(err, qt.IsNil)
		sig, err = signer.BlindSignBallot(token, election, blinded.Bytes())
		if !errors.Is(err, csp.ErrInvalidBlindedMessage) {
			break // a blinded message with a leading zero byte is rejected without spending the nonce
		}
	}
	c.Assert(err, qt.IsNil)
	c.Assert(sig.ProofType, qt.Equals, models.ProofCA_ECDSA_BLIND_PIDSALTED.String())
	c.Assert([]byte(sig.Weight), qt.DeepEquals, []byte(weight))
	unblinded, err := blind.Unblind(new(big.Int).SetBytes(sig.Signature), secret)
	c.Assert(err, qt.IsNil)
	proof := &models.ProofCA{
		Type:      models.ProofCA_ECDSA_BLIND_PIDSALTED,
		Signature: unblinded.BytesUncompressed(),
		Bundle:    bundle,
	}

	valid, gotWeight, err := verifyLegacy(models.CensusOrigin_OFF_CHAIN_CA_V2, root, pid, proof, voter)
	c.Assert(err, qt.IsNil)
	c.Assert(valid, qt.IsTrue)
	c.Assert(gotWeight.String(), qt.Equals, new(big.Int).SetBytes(weight).String())

	// the proof does not verify for a weight the CSP did not sign
	forged := proto.Clone(proof).(*models.ProofCA)
	forged.Bundle.VoteWeight = big.NewInt(1000).Bytes()
	_, _, err = verifyLegacy(models.CensusOrigin_OFF_CHAIN_CA_V2, root, pid, forged, voter)
	c.Assert(err, qt.IsNotNil)
}
