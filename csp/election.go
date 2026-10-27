package csp

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	blind "github.com/vocdoni/go-blindsecp256k1"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/csp/signers/electionkey"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/proto/build/go/models"
)

// Election identifies the on-chain election of a published question, and through its census
// origin the scheme that signs its ballots.
type Election struct {
	// ID is the on-chain election id (the question's upstreamId).
	ID internal.HexBytes
	// QuestionID is the question's id: the seed of its per-election keys.
	QuestionID bson.ObjectID
	// CensusOrigin is the census origin name stored on the question: OFF_CHAIN_CA_V3_ECDSA or
	// OFF_CHAIN_CA_V3_BLIND, or empty for a question published on a legacy CSP origin.
	CensusOrigin string
}

// ElectionOf returns the Election of a published question.
func ElectionOf(q *db.VotingProcessQuestion) Election {
	return Election{ID: q.UpstreamID, QuestionID: q.ID, CensusOrigin: q.CensusOrigin}
}

// BallotRequest is a request to sign the CA bundle of one ballot.
type BallotRequest struct {
	Token    internal.HexBytes
	Address  internal.HexBytes
	Weight   internal.HexBytes
	Election Election
}

// BallotSignature is a CSP signature of one ballot and what the client needs to turn it into a
// proof.
type BallotSignature struct {
	Signature internal.HexBytes
	// ProofType is the ProofCA.Type name of the proof the client builds with Signature.
	ProofType string
	// Weight is the weight the signature authorizes.
	Weight internal.HexBytes
	// WeightKey and WeightCertificate are set for an OFF_CHAIN_CA_V3_BLIND election: the compressed
	// public key that blind-signed the ballot and its certificate by the election key, which the
	// client puts in ProofCA.weightCertificate.
	WeightKey         internal.HexBytes
	WeightCertificate internal.HexBytes
}

// ElectionCensus returns the census origin and census root to publish the election of a question
// with: OFF_CHAIN_CA_V3_BLIND and the public key of its election blind key for an anonymous census,
// OFF_CHAIN_CA_V3_ECDSA and the public key of its election ECDSA key otherwise. Both depend only on
// the root key and the question id, so a retried publish gets the same values.
//
//nolint:revive // flag-parameter: anonymous is the census's own mode, which picks the scheme
func (c *CSP) ElectionCensus(questionID bson.ObjectID, anonymous bool) (models.CensusOrigin, internal.HexBytes, error) {
	if anonymous {
		key, err := c.electionKeys.ElectionBlindKey(questionID)
		if err != nil {
			return 0, nil, fmt.Errorf("could not derive election blind key: %w", err)
		}
		return electionkey.OriginBlind, electionkey.CompressedPubKey(key), nil
	}
	key, err := c.electionKeys.ElectionECDSAKey(questionID)
	if err != nil {
		return 0, nil, fmt.Errorf("could not derive election key: %w", err)
	}
	return electionkey.OriginECDSA, electionkey.CompressedPubKey(key), nil
}

// SignBallot signs the CA bundle {processId, address, voteWeight} of a ballot with the ECDSA scheme
// of the election's census origin: the election ECDSA key on OFF_CHAIN_CA_V3_ECDSA, and the root
// key salted with the election id on a legacy question. Like Sign, it consumes the election's
// signing slot for the token's user.
func (c *CSP) SignBallot(req *BallotRequest) (*BallotSignature, error) {
	switch req.Election.CensusOrigin {
	case "":
		signature, err := c.Sign(req.Token, req.Address, req.Election.ID, req.Weight, signers.SignerTypeECDSASalted)
		if err != nil {
			return nil, err
		}
		return &BallotSignature{
			Signature: signature,
			ProofType: models.ProofCA_ECDSA_PIDSALTED.String(),
			Weight:    req.Weight,
		}, nil
	case electionkey.OriginECDSAName:
		signature, err := c.signElectionECDSA(req)
		if err != nil {
			return nil, err
		}
		return &BallotSignature{Signature: signature, ProofType: models.ProofCA_ECDSA.String(), Weight: req.Weight}, nil
	default:
		return nil, errors.Join(ErrInvalidSignerType,
			fmt.Errorf("census origin %q is not signed with ECDSA", req.Election.CensusOrigin))
	}
}

// signElectionECDSA signs a ballot of an OFF_CHAIN_CA_V3_ECDSA election with its election key. It
// runs the same token, lock and consumption checks as the legacy ECDSA signer; only the key differs.
func (c *CSP) signElectionECDSA(req *BallotRequest) (internal.HexBytes, error) {
	key, err := c.electionKeys.ElectionECDSAKey(req.Election.QuestionID)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	userID, _, message, err := c.prepareSaltedKeySigner(req.Token, req.Address, req.Election.ID, req.Weight)
	defer c.unlock(userID, req.Election.ID)
	if err != nil {
		return nil, err
	}
	signature, err := electionkey.SignEthereum(key, message)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	if err := c.finishSaltedKeySigner(req.Token, req.Address, req.Election.ID); err != nil {
		return nil, err
	}
	return signature, nil
}

// NewBlindBallotRequest performs round 1 of a blind signature with the scheme of the election's
// census origin, returning the blind point R and the weight pinned with it. On OFF_CHAIN_CA_V3_BLIND
// the weight must be one a weight credential can be issued for: round 2 derives the weight key from
// the pinned weight after claiming the nonce, so a weight refused there would spend the nonce
// without a signature. A legacy question arms the nonce as NewBlindRequest does.
func (c *CSP) NewBlindBallotRequest(token internal.HexBytes, election Election, weight internal.HexBytes) (
	r, pinnedWeight internal.HexBytes, err error,
) {
	switch election.CensusOrigin {
	case "":
		return c.NewBlindRequest(token, election.ID, weight)
	case electionkey.OriginBlindName:
		if _, err := electionkey.NormalizeWeight(weight); err != nil {
			return nil, nil, errors.Join(ErrInvalidWeight, err)
		}
		r, pinnedWeight, err = c.NewBlindRequest(token, election.ID, weight)
		if err != nil {
			return nil, nil, err
		}
		// an idempotent re-arm returns the weight pinned by the first call, which went through
		// the same check; verify it anyway so an unsignable pinned weight never reaches round 2.
		if _, err := electionkey.NormalizeWeight(pinnedWeight); err != nil {
			return nil, nil, errors.Join(ErrInvalidWeight, err)
		}
		return r, pinnedWeight, nil
	default:
		return nil, nil, errors.Join(ErrInvalidSignerType,
			fmt.Errorf("census origin %q is not signed with blind signatures", election.CensusOrigin))
	}
}

// BlindSignBallot performs round 2 of a blind signature with the scheme of the election's census
// origin. On OFF_CHAIN_CA_V3_BLIND it blind-signs with the weight key of the weight pinned at round
// 1 and returns that key with its certificate; on a legacy question it signs as BlindSign does.
func (c *CSP) BlindSignBallot(token internal.HexBytes, election Election, blindedMsg internal.HexBytes) (
	*BallotSignature, error,
) {
	switch election.CensusOrigin {
	case "":
		signature, weight, err := c.BlindSign(token, election.ID, blindedMsg)
		if err != nil {
			return nil, err
		}
		return &BallotSignature{
			Signature: signature,
			ProofType: models.ProofCA_ECDSA_BLIND_PIDSALTED.String(),
			Weight:    weight,
		}, nil
	case electionkey.OriginBlindName:
		return c.blindSignElection(token, election, blindedMsg)
	default:
		return nil, errors.Join(ErrInvalidSignerType,
			fmt.Errorf("census origin %q is not signed with blind signatures", election.CensusOrigin))
	}
}

// blindSignElection signs round 2 of an OFF_CHAIN_CA_V3_BLIND election. The election blind key is
// derived before the nonce is claimed. The weight key depends on the weight pinned with the nonce,
// so it is derived after the claim; NewBlindBallotRequest only pins a weight a credential can be
// issued for, so that step does not fail in practice.
func (c *CSP) blindSignElection(token internal.HexBytes, election Election, blindedMsg internal.HexBytes) (
	*BallotSignature, error,
) {
	electionBlindKey, err := c.electionKeys.ElectionBlindKey(election.QuestionID)
	if err != nil {
		return nil, errors.Join(ErrSign, err)
	}
	claimed, err := c.claimBlindNonce(token, election.ID, blindedMsg)
	if err != nil {
		return nil, err
	}
	credential, signature, err := signWithWeightKey(electionBlindKey, election.ID, claimed, blindedMsg)
	if err != nil {
		// the nonce is already claimed (spent), so this is a hard failure, not a retry.
		log.Errorw(errors.Join(ErrSign, err), "blind signature failed after claiming the nonce")
		return nil, errors.Join(ErrSign, err)
	}
	return &BallotSignature{
		Signature:         signature,
		ProofType:         models.ProofCA_ECDSA_BLIND.String(),
		Weight:            claimed.BlindWeight,
		WeightKey:         credential.WeightKey,
		WeightCertificate: credential.Certificate,
	}, nil
}

// signWithWeightKey issues the weight credential of the weight pinned in claimed (a missing weight
// is 1) and blind-signs blindedMsg with its key and the claimed nonce.
func signWithWeightKey(
	electionBlindKey *ecdsa.PrivateKey, processID internal.HexBytes, claimed *db.CSPProcess, blindedMsg internal.HexBytes,
) (*electionkey.WeightCredential, internal.HexBytes, error) {
	weight, err := electionkey.NormalizeWeight(claimed.BlindWeight)
	if err != nil {
		return nil, nil, err
	}
	credential, err := electionkey.IssueWeightCredential(electionBlindKey, processID, weight)
	if err != nil {
		return nil, nil, err
	}
	weightKey := blind.PrivateKey(*new(big.Int).SetBytes(ethcrypto.FromECDSA(credential.Key)))
	signature, err := weightKey.BlindSign(new(big.Int).SetBytes(blindedMsg), new(big.Int).SetBytes(claimed.BlindSecret))
	if err != nil {
		return nil, nil, err
	}
	return credential, signature.Bytes(), nil
}
