// Package externalcsp holds what the SaaS checks about census roots it does not own: an
// integrator signs the proofs of these elections with keys the SaaS never sees.
package externalcsp

import (
	"fmt"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// RootSize is the length of a compressed secp256k1 public key.
const RootSize = 33

// ParseRoot returns root when it is a compressed secp256k1 point on the curve.
func ParseRoot(root []byte) ([]byte, error) {
	if len(root) != RootSize || (root[0] != 0x02 && root[0] != 0x03) {
		return nil, fmt.Errorf("census root is not a 33-byte compressed public key")
	}
	if _, err := ethcrypto.DecompressPubkey(root); err != nil {
		return nil, fmt.Errorf("census root is not a secp256k1 point: %w", err)
	}
	return root, nil
}
