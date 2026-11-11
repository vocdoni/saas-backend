package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestMigrationLockRenewal pins the lock renewal a long migration relies on: it extends the lock
// for its holder, fails once another instance took the lock over, does nothing without a renewer,
// and a migration that outlives the run timeout is still recorded in the ledger.
func TestMigrationLockRenewal(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()

	lock := func() (holder string, expiresAt time.Time) {
		var doc struct {
			Holder    string    `bson:"holder"`
			ExpiresAt time.Time `bson:"expiresat"`
		}
		c.Assert(testDB.migrations.FindOne(ctx, bson.M{"_id": migrationLockID}).Decode(&doc), qt.IsNil)
		return doc.Holder, doc.ExpiresAt
	}
	setExpiry := func(at time.Time) {
		_, err := testDB.migrations.UpdateOne(ctx, bson.M{"_id": migrationLockID},
			bson.M{"$set": bson.M{"expiresat": at}})
		c.Assert(err, qt.IsNil)
	}
	renewing := func(holder string) context.Context {
		return migrations.WithLockRenewer(ctx, func(renewCtx context.Context) error {
			return testDB.renewMigrationLock(renewCtx, holder)
		})
	}

	c.Run("extends the lock for its holder", func(c *qt.C) {
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-a"), qt.IsNil)
		defer testDB.releaseMigrationLock("holder-a")
		setExpiry(time.Now().Add(time.Minute))

		c.Assert(migrations.RenewLock(renewing("holder-a")), qt.IsNil)
		holder, expiresAt := lock()
		c.Assert(holder, qt.Equals, "holder-a")
		c.Assert(time.Until(expiresAt) > migrationLockTTL-time.Minute, qt.IsTrue,
			qt.Commentf("lock expires at %s", expiresAt))
	})

	c.Run("fails once another instance took the lock over", func(c *qt.C) {
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-a"), qt.IsNil)
		setExpiry(time.Now().Add(-time.Second))
		c.Assert(testDB.acquireMigrationLock(ctx, "holder-b"), qt.IsNil)
		defer testDB.releaseMigrationLock("holder-b")

		c.Assert(migrations.RenewLock(renewing("holder-a")), qt.Not(qt.IsNil))
		holder, _ := lock()
		c.Assert(holder, qt.Equals, "holder-b")
	})

	c.Run("does nothing without a renewer", func(c *qt.C) {
		c.Assert(migrations.RenewLock(ctx), qt.IsNil)
	})

	c.Run("a migration outliving the run timeout is recorded", func(c *qt.C) {
		saved := migrationRunTimeout
		migrationRunTimeout = 2 * time.Second
		defer func() { migrationRunTimeout = saved }()

		migs := migrations.SortedByVersionAsc()
		version := migs[len(migs)-1].Version + 1
		renewErr := fmt.Errorf("RenewLock not called")
		migrations.AddMigration(version, "test_outlives_run_timeout", func(ctx context.Context, _ *mongo.Database) error {
			<-ctx.Done() // the run timeout passes while the migration still works
			renewErr = migrations.RenewLock(context.WithoutCancel(ctx))
			return nil
		}, func(context.Context, *mongo.Database) error { return nil })
		defer migrations.DelMigration(version)
		defer func() {
			_, err := testDB.migrations.DeleteOne(ctx, bson.M{"version": version})
			c.Assert(err, qt.IsNil)
		}()

		c.Assert(testDB.RunMigrationsUp(), qt.IsNil)
		c.Assert(renewErr, qt.IsNil)
		recorded, err := testDB.migrations.CountDocuments(ctx, bson.M{"version": version})
		c.Assert(err, qt.IsNil)
		c.Assert(recorded, qt.Equals, int64(1))
	})
}
