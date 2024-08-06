package account

import (
	"crypto/rand"
	"encoding/hex"
	"math/big"

	"go.vocdoni.io/dvote/crypto/ethereum"
)

// nonceSize is the fixed size of the nonce used to calculate the private key
// of the organization signer.
const nonceSize = 16

// nonce generates a random number of the given size as a string. If the random
// number generation fails, it returns an error.
func nonce(size int) (string, error) {
	nonce := make([]byte, size)
	for i := range nonce {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		nonce[i] = byte('0' + n.Int64())
	}
	return string(nonce), nil
}

// OrganizationSigner calculates a signer for the organization and the nonce used
// to calculate the private key of thath signer. The private key is calculated
// using the SHA256 hash of the secret, the creator email and the nonce. It
// returns the signer, the nonce and an error if the private key calculation
// fails.
func NewSigner(secret, creatorEmail string) (*ethereum.SignKeys, string, error) {
	nonce, err := nonce(nonceSize)
	if err != nil {
		return nil, "", err
	}
	signer, err := OrganizationSigner(secret, creatorEmail, nonce)
	return signer, nonce, err
}

// OrganizationSigner calculates a signer for the organization using the SHA256
// hash of the secret, the creator email and the nonce. It returns the signer
// and an error if the private key calculation fails. It allows to recalculate
// the signer using the same secret, creator email and nonce for an
// organization.
func OrganizationSigner(secret, creatorEmail, nonce string) (*ethereum.SignKeys, error) {
	seed := hex.EncodeToString(ethereum.HashRaw([]byte(secret + creatorEmail + nonce)))
	signer := ethereum.SignKeys{}
	return &signer, signer.AddHexKey(seed)
}
