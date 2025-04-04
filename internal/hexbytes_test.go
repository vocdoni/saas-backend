package internal

import (
	"bytes"
	"encoding/json"
	"math/big"
	"testing"
)

func TestBytesSetBytes(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0xff}

	hb := new(HexBytes).SetBytes(b)
	if !bytes.Equal(hb.Bytes(), b) {
		t.Error("SetBytes failed")
	}

	newB := []byte{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b}
	hb.SetBytes(newB)
	if !bytes.Equal(hb.Bytes(), newB) {
		t.Error("SetBytes failed")
	}
}

func TestStringSetString(t *testing.T) {
	hexString := "0x0102030405ff"

	hb := new(HexBytes).SetString(hexString)
	if !bytes.Equal(hb.Bytes(), []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0xff}) {
		t.Error("SetString failed")
	}
	if hb.String() != hexString[2:] {
		t.Error("SetString failed")
	}

	hexStringWithoutOx := "060708090a0b"
	hb.SetString(hexStringWithoutOx)
	if !bytes.Equal(hb.Bytes(), []byte{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b}) {
		t.Error("SetString failed")
	}
	if hb.String() != hexStringWithoutOx {
		t.Error("SetString failed")
	}

	invalidHexString := "0xzz"
	defer func() {
		if r := recover(); r == nil {
			t.Error("SetString failed")
		}
	}()
	hb.SetString(invalidHexString)
}

func TestJSONMarshalUnmarshal(t *testing.T) {
	type hbStruct struct {
		HB HexBytes `json:"hb"`
	}

	hb := new(HexBytes).SetString("0x0102030405ff")
	expectedHB := hbStruct{*hb}
	jsonData, err := json.Marshal(expectedHB)
	if err != nil {
		t.Error(err)
	}
	decodeHB := hbStruct{}
	if err = json.Unmarshal(jsonData, &decodeHB); err != nil {
		t.Error(err)
	}
	if !bytes.Equal(expectedHB.HB.Bytes(), decodeHB.HB.Bytes()) {
		t.Error("JSON marshal/unmarshal failed")
	}
}

func TestBigIntSetBigInt(t *testing.T) {
	bigInt := big.NewInt(1234567890)

	hb := new(HexBytes).SetBigInt(bigInt)
	if hb.BigInt().Cmp(bigInt) != 0 {
		t.Error("SetBigInt failed")
	}

	newBigInt := big.NewInt(9876543210)
	hb.SetBigInt(newBigInt)
	if hb.BigInt().Cmp(newBigInt) != 0 {
		t.Error("SetBigInt failed")
	}

	hexHb := new(HexBytes).SetString("0x0102030405ff")
	bigHb := hexHb.BigInt()
	newHexHb := new(HexBytes).SetBigInt(bigHb)
	if !bytes.Equal(hexHb.Bytes(), newHexHb.Bytes()) {
		t.Error("SetBigInt failed")
	}
}
