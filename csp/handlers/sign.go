package handlers

import (
	"encoding/json"
	"math/big"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/signers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

// parseSignRequest parses the sign request from the request body
func parseSignRequest(w http.ResponseWriter, r *http.Request) (*SignRequest, bool) {
	var req SignRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return nil, false
	}

	// Check that the request contains the auth token
	if req.AuthToken == nil {
		errors.ErrUnauthorized.Withf("missing auth token").Write(w)
		return nil, false
	}

	return &req, true
}

// getAuthInfo retrieves the authentication information from the token
func (c *CSPHandlers) getAuthInfo(w http.ResponseWriter, authToken internal.HexBytes) (*db.CSPAuth, bool) {
	auth, err := c.csp.Storage.CSPAuth(authToken)
	if err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return nil, false
	}
	return auth, true
}

// weightBytes encodes a voter weight as the minimal big-endian bytes carried in a CA bundle
// and returned to voters. SetUint64 rather than big.NewInt(int64(w)): db.OrgMember.Weight is a
// uint64, and the signed conversion would wrap to a negative value above 2^63.
func weightBytes(w uint64) internal.HexBytes {
	return new(big.Int).SetUint64(w).Bytes()
}

// validateVoterAddress checks a voter address is a full 20-byte Ethereum address. The address is
// signed into the CA bundle and pinned as the election's consumer, after which a different
// address is rejected forever — and HexBytes accepts any length and pads odd input — so one
// malformed value would lock the voter out of the election permanently. Single choke point for
// both the single-sign parseAddress and the batch validation pass: this is exactly the invariant
// that must not drift between them.
func validateVoterAddress(address internal.HexBytes) *errors.Error {
	if len(address) != common.AddressLength {
		return errors.Ptr(errors.ErrMalformedBody.Withf(
			"address is %d bytes, expected %d", len(address), common.AddressLength))
	}
	return nil
}

// parseAddress parses and validates the voter address from the payload.
func parseAddress(w http.ResponseWriter, payload string) (*internal.HexBytes, bool) {
	address := new(internal.HexBytes)
	if err := address.ParseString(payload); err != nil {
		errors.ErrMalformedBody.WithErr(err).Write(w)
		return nil, false
	}
	if sErr := validateVoterAddress(*address); sErr != nil {
		sErr.Write(w)
		return nil, false
	}
	return address, true
}

// signAndRespond signs the request and sends the response. A failure is mapped through
// signOutcome so the public body carries the sanitized message, not the raw signer error
// (csp.Sign joins internal detail into it), which stays in the log: authorization outcomes
// are 401, internal failures 500.
func (c *CSPHandlers) signAndRespond(w http.ResponseWriter, authToken, address, processID, weight internal.HexBytes) {
	log.Debugw("new CSP sign request", "address", address, "procId", processID, "weight", weight)

	signature, err := c.csp.Sign(authToken, address, processID, weight, signers.SignerTypeECDSASalted)
	if err != nil {
		code, message := signOutcome(err)
		if code == signCodeFailed {
			log.Warnw("could not sign", "procId", processID, "error", err)
			errors.ErrGenericInternalServerError.With(message).Write(w)
			return
		}
		log.Debugw("sign rejected", "procId", processID, "reason", err)
		errors.ErrUnauthorized.With(message).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &AuthResponse{Signature: signature, Weight: weight})
}
