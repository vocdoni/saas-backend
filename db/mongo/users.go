package mongo

import (
	"github.com/vocdoni/saas-backend/db"
)

func (ms *MongoStorage) UserByEmail(email string) (*db.User, error) {
	return nil, nil
}

func (ms *MongoStorage) SetUser(user *db.User) error {
	return nil
}

func (ms *MongoStorage) DelUser(user *db.User) error {
	return nil
}
