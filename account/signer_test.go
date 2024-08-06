package account

import (
	"bytes"
	"testing"
)

func Test_nonce(t *testing.T) {
	previous := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		newNonce, err := nonce(nonceSize)
		if err != nil {
			t.Fatalf("nonce() failed: %v", err)
		}
		if _, alreadyExists := previous[newNonce]; alreadyExists {
			t.Fatalf("nonce() returned a repeated value: %s", newNonce)
		}
		previous[newNonce] = true
	}
}

func TestOrganizationSigner(t *testing.T) {
	secret := "secret"
	creatorEmail := "test@test.test"

	signer, nonce, err := NewSigner(secret, creatorEmail)
	if err != nil {
		t.Fatalf("NewSigner() failed: %v", err)
	}
	recovered, err := OrganizationSigner(secret, creatorEmail, nonce)
	if err != nil {
		t.Fatalf("OrganizationSigner() failed: %v", err)
	}
	if !bytes.Equal(signer.PrivateKey(), recovered.PrivateKey()) {
		t.Fatalf("OrganizationSigner() returned a different private key")
	}
}
