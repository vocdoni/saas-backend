// Package internal provides internal utilities and types for the application.
package internal

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// HexBytes is a []byte that encodes as hexadecimal in JSON (instead of the default base64).
// swaggertype: string
// swagger:format hex
// swagger:description A byte array represented as a hexadecimal string in JSON.
// swagger:example deadbeef
type HexBytes []byte

// SetBytes sets the raw bytes of the HexBytes.
func (hb *HexBytes) SetBytes(b []byte) *HexBytes {
	*hb = HexBytes(bytes.Clone(b))
	return hb
}

// Bytes returns the raw bytes of the HexBytes.
func (hb *HexBytes) Bytes() []byte {
	return *hb
}

// SetBigInt sets the HexBytes to the big-endian encoding of the big.Int.
func (hb *HexBytes) SetBigInt(i *big.Int) *HexBytes {
	*hb = i.Bytes()
	return hb
}

// BigInt returns the big.Int representation of the HexBytes.
func (hb *HexBytes) BigInt() *big.Int {
	return new(big.Int).SetBytes(*hb)
}

// ParseString decodes a hex string into the HexBytes. It strips a leading '0x'
// or '0X' if found, for backwards compatibility. Also pads with a leading '0' if the length is odd.
func (hb *HexBytes) ParseString(s string) error {
	// Strip a leading "0x" prefix, for backwards compatibility.
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	// pad the string with a leading zero if the length is odd
	if len(s)%2 != 0 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*hb = HexBytes(b)
	return err
}

// SetString decodes a hex string into the HexBytes. It strips a leading '0x'
// or '0X' if found, for backwards compatibility. Also pads with a leading '0' if the length is odd.
// Panics if the resulting string is not a valid hex string.
func (hb *HexBytes) SetString(s string) *HexBytes {
	err := hb.ParseString(s)
	if err != nil {
		panic(err)
	}
	return hb
}

// String returns the hex string representation of the HexBytes.
func (hb *HexBytes) String() string {
	return hex.EncodeToString(*hb)
}

// MarshalJSON implements the json.Marshaler interface. The HexBytes are
// serialized as a JSON string.
func (hb HexBytes) MarshalJSON() ([]byte, error) {
	enc := make([]byte, hex.EncodedLen(len(hb))+2)
	enc[0] = '"'
	hex.Encode(enc[1:], hb)
	enc[len(enc)-1] = '"'
	return enc, nil
}

// UnmarshalJSON implements the json.Unmarshaler interface. The HexBytes are
// expected as a JSON string.
func (hb *HexBytes) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid JSON string: %q", data)
	}
	data = data[1 : len(data)-1]
	// Strip a leading "0x" prefix, for backwards compatibility.
	if len(data) >= 2 && data[0] == '0' && (data[1] == 'x' || data[1] == 'X') {
		data = data[2:]
	}
	decLen := hex.DecodedLen(len(data))
	if cap(*hb) < decLen {
		*hb = make([]byte, decLen)
	}
	if _, err := hex.Decode(*hb, data); err != nil {
		return err
	}
	return nil
}

// Address returns the Ethereum Address of the HexBytes.
func (hb *HexBytes) Address() common.Address {
	return common.BytesToAddress(*hb)
}
