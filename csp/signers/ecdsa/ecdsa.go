package ecdsa

import (
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/db"
)

type EthereumSigner struct {
	db db.Database
}

func (s *EthereumSigner) Init(db db.Database) error {
	s.db = db
	return nil
}

func (s *EthereumSigner) Sign(salt, msg internal.HexBytes) (internal.HexBytes, error) {
	return nil, nil
}
