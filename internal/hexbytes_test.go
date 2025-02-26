package internal

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestBytesSetBytes(t *testing.T) {
	b := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0xff}

	hb := new(HexBytes).SetBytes(b)
	if bytes.Equal(hb.Bytes(), b) == false {
		t.Error("SetBytes failed")
	}

	newB := []byte{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b}
	hb.SetBytes(newB)
	if bytes.Equal(hb.Bytes(), newB) == false {
		t.Error("SetBytes failed")
	}
}

func TestStringSetString(t *testing.T) {
	hexString := "0x0102030405ff"

	hb := new(HexBytes).SetString(hexString)
	if bytes.Equal(hb.Bytes(), []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0xff}) == false {
		t.Error("SetString failed")
	}
	if hb.String() != hexString[2:] {
		t.Error("SetString failed")
	}

	hexStringWithoutOx := "060708090a0b"
	hb.SetString(hexStringWithoutOx)
	if bytes.Equal(hb.Bytes(), []byte{0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b}) == false {
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
	if bytes.Equal(expectedHB.HB.Bytes(), decodeHB.HB.Bytes()) == false {
		t.Error("JSON marshal/unmarshal failed")
	}
}
