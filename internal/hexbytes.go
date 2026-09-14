// Package internal provides internal utilities and types for the application.
package internal

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// HexBytes is a []byte that encodes as hexadecimal in JSON (instead of the default base64).
// swaggertype: string
// swagger:format hex
// swagger:description A byte array represented as a hexadecimal string in JSON.
// swagger:example deadbeef
type HexBytes []byte

func HexBytesFromString(s string) HexBytes {
	hb := HexBytes{}
	return *hb.SetString(s)
}

// SetBytes sets the raw bytes of the HexBytes.
func (hb *HexBytes) SetBytes(b []byte) *HexBytes {
	*hb = HexBytes(bytes.Clone(b))
	return hb
}

// Bytes returns the raw bytes of the HexBytes.
func (hb HexBytes) Bytes() []byte {
	return hb
}

// SetBigInt sets the HexBytes to the big-endian encoding of the big.Int.
func (hb *HexBytes) SetBigInt(i *big.Int) *HexBytes {
	*hb = i.Bytes()
	return hb
}

// BigInt returns the big.Int representation of the HexBytes.
func (hb HexBytes) BigInt() *big.Int {
	return new(big.Int).SetBytes(hb)
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
func (hb HexBytes) String() string {
	return hex.EncodeToString(hb)
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

// MarshalBSONValue makes HexBytes be marshalled to a string rather than the default (binary).
//
// An empty HexBytes — whether the slice is nil or a zero-length non-nil (`HexBytes{}`) —
// marshals as BSON null. The distinction between the two is a Go-side accident: a struct
// field left unset is nil, but the same field decoded from BSON `""` via UnmarshalBSONValue,
// or built by JSON decode of `""`, becomes a zero-length non-nil slice. Persisting the
// non-nil form as `""` created a class of documents invisible to `{$eq: null}` queries yet
// visible to any code that reads the field back and re-writes it, so an anomaly here was
// self-perpetuating (see the drafts-quota vs list-drafts discrepancy observed 2026-09-14).
// Normalising empty to null on write closes the loop for good.
func (hb HexBytes) MarshalBSONValue() (byte, []byte, error) {
	if len(hb) == 0 {
		return byte(bson.TypeNull), nil, nil
	}
	t, data, err := bson.MarshalValue(hb.String())
	return byte(t), data, err
}

func (hb *HexBytes) UnmarshalBSONValue(t byte, data []byte) error {
	// A null value on the wire (either a legitimate absence or a legacy empty string decoded
	// into null on write) leaves the receiver as the nil slice. Round-tripping such a value
	// back through MarshalBSONValue must produce the same null, not `""`, so the two write
	// paths (never-set vs read-null-and-write) stay consistent.
	if bson.Type(t) == bson.TypeNull {
		*hb = nil
		return nil
	}
	var s string
	if err := bson.UnmarshalValue(bson.Type(t), data, &s); err != nil {
		return err
	}
	return hb.ParseString(s)
}

// Address returns the Ethereum Address of the HexBytes.
func (hb HexBytes) Address() common.Address {
	return common.BytesToAddress(hb)
}

func (hb HexBytes) Equals(b HexBytes) bool {
	return hb.Cmp(b) == 0
}

// Cmp compares two HexBytes
func (hb HexBytes) Cmp(other HexBytes) int {
	return bytes.Compare(hb, other)
}
