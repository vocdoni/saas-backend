package csp

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	blind "github.com/vocdoni/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
	"github.com/vocdoni/saas-backend/csp/signers/saltedkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/util"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// failingSaltedSigner is a SaltedSigner that fails the test when any of its methods is called: the
// per-election signing paths must never touch the salted root key.
type failingSaltedSigner struct {
	c *qt.C
}

func (s failingSaltedSigner) SignECDSA([saltedkey.SaltSize]byte, []byte) ([]byte, error) {
	s.c.Fatalf("salted ECDSA signer used")
	return nil, nil
}

func (s failingSaltedSigner) SignBlind([saltedkey.SaltSize]byte, []byte, *big.Int) ([]byte, error) {
	s.c.Fatalf("salted blind signer used")
	return nil, nil
}

func (s failingSaltedSigner) ECDSAPubKey() (*ecdsa.PublicKey, error) {
	s.c.Fatalf("salted signer ECDSA public key used")
	return nil, nil
}

func (s failingSaltedSigner) BlindPubKey() *blind.PublicKey {
	s.c.Fatalf("salted signer blind public key used")
	return nil
}

func newElectionTestCSP(c *qt.C) *CSP {
	testDB, err := db.New(testMongoURI, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	csp, err := New(context.Background(), &Config{
		DB:                       testDB,
		MailService:              testMailService,
		SMSService:               testSMSService,
		NotificationCoolDownTime: time.Second * 5,
		RootKey:                  *testRootKey,
	})
	c.Assert(err, qt.IsNil)
	csp.Signer = failingSaltedSigner{c: c}
	return csp
}

func verifiedTestToken(c *qt.C, csp *CSP) {
	c.Cleanup(func() { c.Assert(csp.Storage.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(csp.Storage.SetCSPAuth(testToken, testUserID, testAnchorID, ""), qt.IsNil)
	c.Assert(csp.Storage.VerifyCSPAuth(testToken), qt.IsNil)
}

func TestElectionCensusIsPerQuestion(t *testing.T) {
	c := qt.New(t)
	csp := newElectionTestCSP(c)
	q1, q2 := bson.NewObjectID(), bson.NewObjectID()

	origin, root1, err := csp.ElectionCensus(q1, false)
	c.Assert(err, qt.IsNil)
	c.Assert(origin, qt.Equals, electionkey.OriginECDSA)
	c.Assert(root1, qt.HasLen, electionkey.PubKeySize)
	_, again, err := csp.ElectionCensus(q1, false)
	c.Assert(err, qt.IsNil)
	c.Assert(again, qt.DeepEquals, root1)
	_, root2, err := csp.ElectionCensus(q2, false)
	c.Assert(err, qt.IsNil)
	c.Assert(root2, qt.Not(qt.DeepEquals), root1)

	origin, blindRoot, err := csp.ElectionCensus(q1, true)
	c.Assert(err, qt.IsNil)
	c.Assert(origin, qt.Equals, electionkey.OriginBlind)
	c.Assert(blindRoot, qt.Not(qt.DeepEquals), root1)
}

func TestSignBallotElectionECDSA(t *testing.T) {
	c := qt.New(t)
	csp := newElectionTestCSP(c)
	verifiedTestToken(c, csp)
	questionID := bson.NewObjectID()
	_, root, err := csp.ElectionCensus(questionID, false)
	c.Assert(err, qt.IsNil)
	pid := internal.HexBytes(util.RandomBytes(32))
	address := internal.HexBytes(util.RandomBytes(20))

	signed, err := csp.SignBallot(&BallotRequest{
		Token: testToken, Address: address, Weight: testUserWeightBytes,
		Election: Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginECDSAName},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(signed.ProofType, qt.Equals, models.ProofCA_ECDSA.String())
	c.Assert(signed.Weight, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
	c.Assert(signed.WeightKey, qt.IsNil)
	c.Assert(signed.WeightCertificate, qt.IsNil)

	// the signature recovers to the election's census root, over the bundle the chain rebuilds
	bundle, err := proto.Marshal(&models.CAbundle{ProcessId: pid, Address: address, VoteWeight: testUserWeightBytes})
	c.Assert(err, qt.IsNil)
	pub, err := ethereum.PubKeyFromSignature(bundle, signed.Signature)
	c.Assert(err, qt.IsNil)
	c.Assert(internal.HexBytes(pub), qt.DeepEquals, root)

	// the signing slot is consumed, as on the legacy origins
	_, err = csp.SignBallot(&BallotRequest{
		Token: testToken, Address: internal.HexBytes(util.RandomBytes(20)), Weight: testUserWeightBytes,
		Election: Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginECDSAName},
	})
	c.Assert(err, qt.ErrorIs, ErrAddressMismatch)
	c.Assert(csp.isLocked(testUserID, pid), qt.IsFalse)
}

func TestBlindSignBallotElectionBlind(t *testing.T) {
	c := qt.New(t)
	csp := newElectionTestCSP(c)
	verifiedTestToken(c, csp)
	questionID := bson.NewObjectID()
	_, root, err := csp.ElectionCensus(questionID, true)
	c.Assert(err, qt.IsNil)
	pid := internal.HexBytes(util.RandomBytes(32))
	election := Election{ID: pid, QuestionID: questionID, CensusOrigin: electionkey.OriginBlindName}
	bundle, err := proto.Marshal(&models.CAbundle{
		ProcessId: pid, Address: util.RandomBytes(20), VoteWeight: testUserWeightBytes,
	})
	c.Assert(err, qt.IsNil)
	msgHash := new(big.Int).SetBytes(ethereum.HashRaw(bundle))

	var signed *BallotSignature
	var signature *blind.Signature
	for range 32 {
		rBytes, pinned, err := csp.NewBlindRequest(testToken, pid, testUserWeightBytes)
		c.Assert(err, qt.IsNil)
		c.Assert(pinned, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))
		signerR, err := blind.NewPointFromBytes(rBytes)
		c.Assert(err, qt.IsNil)
		blinded, unblinder, err := blind.Blind(msgHash, signerR)
		c.Assert(err, qt.IsNil)
		signed, err = csp.BlindSignBallot(testToken, election, blinded.Bytes())
		if err != nil {
			c.Assert(err, qt.ErrorIs, ErrInvalidBlindedMessage) // ~1/256; nonce not consumed
			continue
		}
		signature, err = blind.Unblind(new(big.Int).SetBytes(signed.Signature), unblinder)
		c.Assert(err, qt.IsNil)
		break
	}
	c.Assert(signature, qt.Not(qt.IsNil))
	c.Assert(signed.ProofType, qt.Equals, models.ProofCA_ECDSA_BLIND.String())
	c.Assert(signed.Weight, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))

	// the unblinded signature verifies under the weight key...
	weightKey, err := blind.NewPublicKeyFromECDSA(signed.WeightKey)
	c.Assert(err, qt.IsNil)
	c.Assert(blind.Verify(msgHash, signature, weightKey), qt.IsTrue)
	// ...and the certificate binds that key to this election and weight under the census root
	certMsg, err := electionkey.CertMessage(pid, new(big.Int).SetBytes(testUserWeightBytes), signed.WeightKey)
	c.Assert(err, qt.IsNil)
	pub, err := ethereum.PubKeyFromSignature(certMsg, signed.WeightCertificate)
	c.Assert(err, qt.IsNil)
	c.Assert(internal.HexBytes(pub), qt.DeepEquals, root)
	// the root key itself never signs a ballot
	rootKey, err := blind.NewPublicKeyFromECDSA(root)
	c.Assert(err, qt.IsNil)
	c.Assert(blind.Verify(msgHash, signature, rootKey), qt.IsFalse)

	// single use
	_, err = csp.BlindSignBallot(testToken, election, signed.Signature)
	c.Assert(err, qt.ErrorIs, ErrProcessAlreadyConsumed)
}

func TestSignBallotRejectsMismatchedScheme(t *testing.T) {
	c := qt.New(t)
	csp := newElectionTestCSP(c)
	verifiedTestToken(c, csp)
	pid := internal.HexBytes(util.RandomBytes(32))

	_, err := csp.SignBallot(&BallotRequest{
		Token: testToken, Address: internal.HexBytes(util.RandomBytes(20)), Weight: testUserWeightBytes,
		Election: Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginBlindName},
	})
	c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
	_, err = csp.BlindSignBallot(testToken,
		Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginECDSAName},
		internal.HexBytes(util.RandomBytes(32)))
	c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
}

func TestNewBlindBallotRequestRejectsUnsignableWeight(t *testing.T) {
	c := qt.New(t)
	csp := newElectionTestCSP(c)
	verifiedTestToken(c, csp)
	pid := internal.HexBytes(util.RandomBytes(32))
	election := Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginBlindName}

	// a zero-weight member gets no blind point, so round 2 never claims a nonce it cannot sign
	_, _, err := csp.NewBlindBallotRequest(testToken, election, internal.HexBytes{0})
	c.Assert(err, qt.ErrorIs, ErrInvalidWeight)
	_, err = csp.BlindSignBallot(testToken, election, internal.HexBytes(util.RandomBytes(32)))
	c.Assert(err, qt.ErrorIs, ErrBlindRequestNotFound)

	// neither does a weight wider than the certificate's 32-byte field
	_, _, err = csp.NewBlindBallotRequest(testToken, election, internal.HexBytes(util.RandomBytes(electionkey.WeightSize+1)))
	c.Assert(err, qt.ErrorIs, ErrInvalidWeight)

	// a valid weight is pinned as on the legacy origin
	r, pinned, err := csp.NewBlindBallotRequest(testToken, election, testUserWeightBytes)
	c.Assert(err, qt.IsNil)
	c.Assert(r, qt.Not(qt.HasLen), 0)
	c.Assert(pinned, qt.DeepEquals, internal.HexBytes(testUserWeightBytes))

	// a non-blind origin gets no blind point
	_, _, err = csp.NewBlindBallotRequest(testToken,
		Election{ID: pid, QuestionID: bson.NewObjectID(), CensusOrigin: electionkey.OriginECDSAName}, testUserWeightBytes)
	c.Assert(err, qt.ErrorIs, ErrInvalidSignerType)
}
