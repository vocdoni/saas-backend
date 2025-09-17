package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

func TestMigrations(t *testing.T) {
	c := qt.New(t)

	log.Init("debug", "stdout", nil)
	testDBName := test.RandomDatabaseName()

	{
		testDB, err := New(mongoURI, testDBName)
		if err != nil {
			panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
		}

		org := &Organization{
			Address:   testOrgAddress,
			Active:    true,
			CreatedAt: time.Now(),
			Website:   "testorg.com",
		}
		err = testDB.SetOrganization(org)
		c.Assert(err, qt.IsNil)

		orgFromDB, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(orgFromDB.Website, qt.Equals, "testorg.com")

		testDB.Close()
	}

	t.Run("UpAndDown", func(*testing.T) {
		// now apply a migration
		migs := migrations.SortedByVersionAsc()
		lastVersion := migs[len(migs)-1].Version
		migrations.AddMigration(lastVersion+1, "test_migration", upRenameWebsiteField, downRenameWebsiteField)
		defer migrations.DelMigration(lastVersion + 1) // to avoid affecting other tests

		testDB, err := New(mongoURI, testDBName)
		if err != nil {
			panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
		}
		orgFromDB, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(orgFromDB.Website, qt.Equals, "") // now old struct doesn't match anymore

		orgFromNewDB, err := testDB.newFetchOrganizationFromDB(context.TODO(), testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(orgFromNewDB.WebsiteURL, qt.Equals, "testorg.com")

		// now roll back migration
		err = testDB.RunMigrationsDown(1)
		c.Assert(err, qt.IsNil)
		orgFromDB, err = testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(orgFromDB.Website, qt.Equals, "testorg.com")

		testDB.Close()
	})

	t.Run("Idempotency", func(*testing.T) {
		c.Log("check that all migrations are idempotent (can run again on top of an up-to-date DB)")
		c.Log("first drop migrations collection")
		testDB, err := New(mongoURI, testDBName)
		if err != nil {
			panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
		}
		err = testDB.migrations.Drop(context.TODO())
		c.Assert(err, qt.IsNil)
		testDB.Close()

		c.Log("now open DB again, and all migrations should run again")
		testDB, err = New(mongoURI, testDBName)
		if err != nil {
			panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
		}
		orgFromDB, err := testDB.Organization(testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(orgFromDB.Website, qt.Equals, "testorg.com")
		testDB.Close()
	})
}

func upRenameWebsiteField(ctx context.Context, database *mongo.Database) error {
	organizations := database.Collection("organizations")

	// Check if websiteUrl field already exists (idempotency check)
	count, err := organizations.CountDocuments(ctx, bson.M{"websiteUrl": bson.M{"$exists": true}})
	if err != nil {
		return fmt.Errorf("failed to check for existing websiteUrl field: %w", err)
	}

	// If websiteUrl field already exists, migration has already been applied
	if count > 0 {
		return nil
	}

	// Rename website field to websiteUrl in all documents
	_, err = organizations.UpdateMany(ctx,
		bson.M{"website": bson.M{"$exists": true}},
		bson.M{"$rename": bson.M{"website": "websiteUrl"}},
	)
	if err != nil {
		return fmt.Errorf("failed to rename website field to websiteUrl: %w", err)
	}

	return nil
}

func downRenameWebsiteField(ctx context.Context, database *mongo.Database) error {
	organizations := database.Collection("organizations")

	// Check if website field already exists (idempotency check)
	count, err := organizations.CountDocuments(ctx, bson.M{"website": bson.M{"$exists": true}})
	if err != nil {
		return fmt.Errorf("failed to check for existing website field: %w", err)
	}

	// If website field already exists, rollback has already been applied
	if count > 0 {
		return nil
	}

	// Rename websiteUrl field back to website in all documents
	_, err = organizations.UpdateMany(ctx,
		bson.M{"websiteUrl": bson.M{"$exists": true}},
		bson.M{"$rename": bson.M{"websiteUrl": "website"}},
	)
	if err != nil {
		return fmt.Errorf("failed to rename websiteUrl field back to website: %w", err)
	}

	return nil
}

type newOrganization struct {
	Address    common.Address `json:"address" bson:"_id"` // common.Address is serialized as bytes in the db
	WebsiteURL string         `json:"websiteUrl" bson:"websiteUrl"`
}

func (ms *MongoStorage) newFetchOrganizationFromDB(ctx context.Context, address common.Address) (*newOrganization, error) {
	// find the organization in the database by its address
	filter := bson.M{"_id": address}
	result := ms.organizations.FindOne(ctx, filter)
	org := &newOrganization{}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return org, nil
}
