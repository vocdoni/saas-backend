package internal

import (
	"encoding/hex"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
)

// HexBytes is a []byte which encodes as hexadecimal in json, as opposed to the
// base64 default.
type HexBytes []byte

// SetBytes sets the raw bytes of the HexBytes.
func (hb *HexBytes) SetBytes(b []byte) *HexBytes {
	newHb := HexBytes(b)
	*hb = newHb
	return hb
}

// Bytes returns the raw bytes of the HexBytes.
func (hb *HexBytes) Bytes() []byte {
	return *hb
}

// SetString decodes a hex string into the HexBytes. It strips a leading '0x'
// or '0X' if found, for backwards compatibility. Panics if the string is not a
// valid hex string.
func (hb *HexBytes) SetString(s string) *HexBytes {
	// strip a leading "0x" prefix, for backwards compatibility.
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	// pad the string with a leading zero if the length is odd
	if len(s)%2 != 0 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return hb.SetBytes(b)
}

// String returns the hex string representation of the HexBytes.
func (b *HexBytes) String() string {
	return hex.EncodeToString(*b)
}

// MarshalJSON implements the json.Marshaler interface. The HexBytes are
// serialized as a JSON string.
func (b HexBytes) MarshalJSON() ([]byte, error) {
	enc := make([]byte, hex.EncodedLen(len(b))+2)
	enc[0] = '"'
	hex.Encode(enc[1:], b)
	enc[len(enc)-1] = '"'
	return enc, nil
}

// UnmarshalJSON implements the json.Unmarshaler interface. The HexBytes are
// expected as a JSON string.
func (b *HexBytes) UnmarshalJSON(data []byte) error {
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return fmt.Errorf("invalid JSON string: %q", data)
	}
	data = data[1 : len(data)-1]
	// Strip a leading "0x" prefix, for backwards compatibility.
	if len(data) >= 2 && data[0] == '0' && (data[1] == 'x' || data[1] == 'X') {
		data = data[2:]
	}
	decLen := hex.DecodedLen(len(data))
	if cap(*b) < decLen {
		*b = make([]byte, decLen)
	}
	if _, err := hex.Decode(*b, data); err != nil {
		return err
	}
	return nil
}

// ParseString decodes a hex string into the HexBytes.
func (b *HexBytes) ParseString(str string) error {
	// Strip a leading "0x" prefix, for backwards compatibility.
	if len(str) >= 2 && str[0] == '0' && (str[1] == 'x' || str[1] == 'X') {
		str = str[2:]
	}
	var err error
	(*b), err = hex.DecodeString(str)
	return err
}

// Address returns the Ethereum Address of the HexBytes.
func (b *HexBytes) Address() common.Address {
	return common.BytesToAddress(*b)
}
