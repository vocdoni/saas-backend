package signers

import (
	"sync"

	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/db"
)

// KeyStore is a key-value store for internal use by the signers.
type KeyStore struct {
	db       db.Database
	keysLock sync.RWMutex
}

// NewKeyStore creates a new key store with the given database.
func NewKeyStore(db db.Database) *KeyStore {
	return &KeyStore{db: db}
}

// Add adds a new key-value pair to the store. It returns an error if the key
// cannot be added. It locks the store to avoid concurrent writes.
func (ks *KeyStore) Add(key, value internal.HexBytes) error {
	ks.keysLock.Lock()
	defer ks.keysLock.Unlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	if err := tx.Set(key.Bytes(), value.Bytes()); err != nil {
		return err
	}
	return tx.Commit()
}

// Del deletes a key from the store. It returns an error if the key cannot be
// deleted. It locks the store to avoid concurrent writes.
func (ks *KeyStore) Del(key internal.HexBytes) error {
	ks.keysLock.Lock()
	defer ks.keysLock.Unlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	if err := tx.Delete(key.Bytes()); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns the value of a key from the store. It returns an error if the
// key cannot be found. It locks the store to avoid concurrent reads.
func (ks *KeyStore) Get(key internal.HexBytes) (internal.HexBytes, error) {
	ks.keysLock.RLock()
	defer ks.keysLock.RUnlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	return tx.Get(key.Bytes())
}
