package twofactor

import (
	"crypto/rand"
	"fmt"
	"math/big"

	blind "github.com/arnaucube/go-blindsecp256k1"
	"go.vocdoni.io/dvote/log"
)

// PubKeyBlind returns the public key of the blind CSP signer.
// If processID is nil, returns the root public key.
// If processID is not nil, returns the salted public key.
// For process bundles, processID should be the bundle ID or the first process ID in the bundle.
func (tf *Twofactor) PubKeyBlind(processID []byte) string {
	if processID == nil {
		return fmt.Sprintf("%x", tf.Signer.BlindPubKey())
	}
	var salt [SaltSize]byte
	copy(salt[:], processID[:SaltSize])
	pk, err := SaltBlindPubKey(tf.Signer.BlindPubKey(), salt)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", pk.Bytes())
}

// PubKeyECDSA returns the public key of the plain CSP signer.
// If processID is nil, returns the root public key.
// If processID is not nil, returns the salted public key.
// For process bundles, processID should be the bundle ID or the first process ID in the bundle.
func (tf *Twofactor) PubKeyECDSA(processID []byte) string {
	k, err := tf.Signer.ECDSAPubKey()
	if err != nil {
		return ""
	}
	if processID == nil {
		return fmt.Sprintf("%x", k)
	}
	var salt [SaltSize]byte
	copy(salt[:], processID[:SaltSize])
	pk, err := SaltECDSAPubKey(k, salt)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", pk)
}

// NewBlindRequestKey generates a new request key for blinding content on the client side.
// It returns SignerR and SignerQ values. This is used in the authentication process
// for both individual processes and process bundles.
func (tf *Twofactor) NewBlindRequestKey() (*blind.Point, error) {
	k, signerR, err := blind.NewRequestParameters()
	if err != nil {
		log.Warnw("new request parameters error", "error", err)
		return nil, err
	}
	index := signerR.X.String() + signerR.Y.String()
	if err := tf.addKey(index, k); err != nil {
		log.Warnw("add key error", "error", err)
		return nil, err
	}
	if k.Uint64() == 0 {
		return nil, fmt.Errorf("k can not be 0, k: %s", k)
	}
	return signerR, nil
}

// NewRequestKey generates a new request key for authentication on the client side.
// It returns a token that can be used for signing. This is used in the authentication process
// for both individual processes and process bundles.
func (tf *Twofactor) NewRequestKey() []byte {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}
	if err := tf.addKey(string(b), new(big.Int).SetUint64(0)); err != nil {
		log.Warnw("add key error", "error", err)
		return nil
	}
	return b
}

// SignECDSA performs an ECDSA signature over hash(msg). It also checks if the token is valid
// and removes it from local storage after use. This can be used for signing in both
// individual processes and process bundles, where processID can be either a process ID or a bundle ID.
func (tf *Twofactor) SignECDSA(token, msg []byte, processID []byte) ([]byte, error) {
	if k, err := tf.getKey(string(token)); err != nil || k == nil {
		return nil, fmt.Errorf("token not found")
	}
	defer func() {
		if err := tf.delKey(string(token)); err != nil {
			log.Warnw("delete key error", "error", err)
		}
	}()
	if processID == nil {
		return tf.Signer.SignECDSA(nil, msg)
	}

	var salt [SaltSize]byte
	copy(salt[:], processID[:SaltSize])
	return tf.Signer.SignECDSA(salt[:], msg)
}

// SignBlind performs a blind signature over hash. It also checks if the R point is valid
// and removes it from local storage if successful. This can be used for signing in both
// individual processes and process bundles, where processID can be either a process ID or a bundle ID.
func (tf *Twofactor) SignBlind(signerR *blind.Point, hash, processID []byte) ([]byte, error) {
	key := signerR.X.String() + signerR.Y.String()
	k, err := tf.getKey(key)
	if k == nil || err != nil {
		return nil, fmt.Errorf("unknown R point")
	}

	var signature []byte
	if processID == nil {
		signature, err = tf.Signer.SignBlind(nil, hash, k)
	} else {
		var salt [SaltSize]byte
		copy(salt[:], processID[:SaltSize])
		signature, err = tf.Signer.SignBlind(salt[:], hash, k)
	}

	if err != nil {
		return nil, err
	}
	if err := tf.delKey(key); err != nil {
		return nil, err
	}
	return signature, nil
}

// SharedKey performs a signature over processID which can be used as a shared key
// for all users belonging to the same process or process bundle. For process bundles,
// processID should be the bundle ID.
func (tf *Twofactor) SharedKey(processID []byte) ([]byte, error) {
	var salt [SaltSize]byte
	copy(salt[:], processID[:SaltSize])
	return tf.Signer.SignECDSA(salt[:], processID)
}

// SyncMap helpers
func (tf *Twofactor) addKey(index string, point *big.Int) error {
	tf.keysLock.Lock()
	defer tf.keysLock.Unlock()
	tx := tf.keys.WriteTx()
	defer tx.Discard()
	if err := tx.Set([]byte(index), point.Bytes()); err != nil {
		log.Warnw("set key error", "error", err)
	}
	return tx.Commit()
}

func (tf *Twofactor) delKey(index string) error {
	tf.keysLock.Lock()
	defer tf.keysLock.Unlock()
	tx := tf.keys.WriteTx()
	defer tx.Discard()
	if err := tx.Delete([]byte(index)); err != nil {
		log.Warnw("delete key error", "error", err)
	}
	return tx.Commit()
}

func (tf *Twofactor) getKey(index string) (*big.Int, error) {
	tf.keysLock.RLock()
	defer tf.keysLock.RUnlock()
	tx := tf.keys.WriteTx()
	defer tx.Discard()
	p, err := tx.Get([]byte(index))
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(p), nil
}
