package signers

import (
	"math/big"
	"sync"

	"go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/log"
)

type KeyStore struct {
	db       db.Database
	keysLock sync.RWMutex
}

func NewKeyStore(db db.Database) *KeyStore {
	return &KeyStore{db: db}
}

func (ks *KeyStore) Add(index string, point *big.Int) error {
	ks.keysLock.Lock()
	defer ks.keysLock.Unlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	if err := tx.Set([]byte(index), point.Bytes()); err != nil {
		log.Warnw("set key error", "error", err)
	}
	return tx.Commit()
}

func (ks *KeyStore) Del(index string) error {
	ks.keysLock.Lock()
	defer ks.keysLock.Unlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	if err := tx.Delete([]byte(index)); err != nil {
		log.Warnw("delete key error", "error", err)
	}
	return tx.Commit()
}

func (ks *KeyStore) Get(index string) (*big.Int, error) {
	ks.keysLock.RLock()
	defer ks.keysLock.RUnlock()
	tx := ks.db.WriteTx()
	defer tx.Discard()
	p, err := tx.Get([]byte(index))
	if err != nil {
		return nil, err
	}
	return new(big.Int).SetBytes(p), nil
}
