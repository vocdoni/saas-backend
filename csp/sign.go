package csp

import (
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/internal"
)

func (c *CSP) Sign(token, msg, processID internal.HexBytes, bundleID, signType signers.SignerType) (internal.HexBytes, error) {
	switch signType {
	case signers.SignerTypeEthereum:
		salt, message, err := c.prepareEthereumSigner()
		if err != nil {
			// TODO: custom error
			return nil, err
		}
		return c.EthSigner.Sign(salt, message)
	default:
		return nil, ErrInvalidSignerType
	}
}

// prepareEthereumSigner method prepares the data for the Ethereum signer. It
// returns the salt and the message to sign.
func (c *CSP) prepareEthereumSigner() (internal.HexBytes, internal.HexBytes, error) {
	return nil, nil, nil
}
