package handlers

import (
	"context"
	"testing"

	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

// testDB is a MongoDB storage on an ephemeral container shared by the package tests.
var testDB *db.MongoStorage

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(err)
	}
	defer func() { _ = container.Terminate(ctx) }()
	uri, err := container.ConnectionString(ctx)
	if err != nil {
		panic(err)
	}
	if testDB, err = db.New(uri, test.RandomDatabaseName()); err != nil {
		panic(err)
	}
	defer testDB.Close()
	m.Run()
}
