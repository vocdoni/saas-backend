package migrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/bsonx/bsoncore"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(36, "retire_legacy_plans", upRetireLegacyPlans, downRetireLegacyPlans)
}

// legacyBillingFields are the organization fields of the subscription model that the wallet
// model no longer has: unset by upRetireLegacyPlans once archived. counters.sentSMS, sentEmails
// and sentVotes stay, they feed the integrator usage.
var legacyBillingFields = []string{
	"subscription", "integratorLimits", "tokensPurchased", "tokensRemaining",
	"counters.users", "counters.processes", "counters.subOrgs", "counters.managedOrgs", "counters.managedProcesses",
}

// legacyArchivedFields are copied from an organization into its legacySubscriptions row, the
// subscription without its billing email (see withoutBillingEmail).
var legacyArchivedFields = []string{
	"subscription", "counters", "integratorLimits", "tokensPurchased", "tokensRemaining", "managedBy",
}

// withoutBillingEmail returns the subscription value without its email key, every other field
// untouched. A value that is not a document (null) is returned as it is; a document that cannot
// be read is an error, never archived with its email.
func withoutBillingEmail(value bson.RawValue) (bson.RawValue, error) {
	doc, ok := value.DocumentOK()
	if !ok {
		return value, nil
	}
	elems, err := doc.Elements()
	if err != nil {
		return bson.RawValue{}, fmt.Errorf("failed to read subscription elements: %w", err)
	}
	kept := make([]byte, 0, len(doc))
	for _, elem := range elems {
		if elem.Key() != "email" {
			kept = append(kept, elem...)
		}
	}
	return bson.RawValue{Type: bson.TypeEmbeddedDocument, Value: bsoncore.BuildDocument(nil, kept)}, nil
}

// upRetireLegacyPlans moves organizations from the subscription model to the wallet model. For
// every organization holding a legacyBillingFields field it first archives, once, the legacy
// billing data (legacyArchivedFields, the plan document it was subscribed to and archivedAt) into
// legacySubscriptions under the organization address, then unsets legacyBillingFields. The
// plans collection is dropped last.
//
// The archive is written with $setOnInsert, so a re-run keeps the first run's values: a run cut
// after the archive of an organization finds the archive in place and only repeats the unset, and
// a run cut after the unset finds nothing left to do for it.
func upRetireLegacyPlans(ctx context.Context, database *mongo.Database) error {
	orgs := database.Collection("organizations")
	plans := database.Collection("plans")
	archive := database.Collection("legacySubscriptions")

	pending := bson.A{}
	projection := bson.M{}
	for _, field := range legacyBillingFields {
		pending = append(pending, bson.M{field: bson.M{"$exists": true}})
	}
	for _, field := range legacyArchivedFields {
		projection[field] = 1
	}
	cursor, err := orgs.Find(ctx, bson.M{"$or": pending}, options.Find().SetProjection(projection))
	if err != nil {
		return fmt.Errorf("failed to list organizations with legacy billing: %w", err)
	}
	// loaded up front (a few hundred organizations): the loop below rewrites the documents a
	// live cursor would be walking
	var legacy []bson.Raw
	if err := cursor.All(ctx, &legacy); err != nil {
		return fmt.Errorf("failed to read organizations with legacy billing: %w", err)
	}

	unset := bson.M{}
	for _, field := range legacyBillingFields {
		unset[field] = ""
	}
	for _, org := range legacy {
		id := org.Lookup("_id")
		row := bson.D{{Key: "archivedAt", Value: time.Now()}}
		for _, field := range legacyArchivedFields {
			if value, err := org.LookupErr(field); err == nil {
				if field == "subscription" {
					var err error
					if value, err = withoutBillingEmail(value); err != nil {
						return fmt.Errorf("failed to strip the billing email of organization %s: %w", id, err)
					}
				}
				row = append(row, bson.E{Key: field, Value: value})
			}
		}
		var plan any // stays null when there is no plan document to archive
		if planID, err := org.LookupErr("subscription", "planID"); err == nil && planID.Type != bson.TypeNull {
			var doc bson.Raw
			err := plans.FindOne(ctx, bson.M{"_id": planID}).Decode(&doc)
			if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
				return fmt.Errorf("failed to read plan of organization %s: %w", id, err)
			}
			if err == nil {
				plan = doc
			}
		}
		row = append(row, bson.E{Key: "plan", Value: plan})

		if _, err := archive.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$setOnInsert": row},
			options.UpdateOne().SetUpsert(true)); err != nil {
			return fmt.Errorf("failed to archive legacy billing of organization %s: %w", id, err)
		}
		if _, err := orgs.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$unset": unset}); err != nil {
			return fmt.Errorf("failed to remove legacy billing of organization %s: %w", id, err)
		}
	}

	// dropping a collection that does not exist is not an error
	if err := plans.Drop(ctx); err != nil {
		return fmt.Errorf("failed to drop plans: %w", err)
	}
	// the subscriptions collection migration 0001 created was never used: drop it unless it holds data
	subscriptions := database.Collection("subscriptions")
	n, err := subscriptions.CountDocuments(ctx, bson.M{})
	if err != nil {
		return fmt.Errorf("failed to count subscriptions: %w", err)
	}
	if n > 0 {
		log.Warnw("migration 0036: subscriptions collection holds documents; leaving it", "count", n)
		return nil
	}
	if err := subscriptions.Drop(ctx); err != nil {
		return fmt.Errorf("failed to drop subscriptions: %w", err)
	}
	return nil
}

func downRetireLegacyPlans(context.Context, *mongo.Database) error {
	// no-op: the previous release re-syncs plans from Stripe at startup, and an organization's
	// subscription can be restored by hand from legacySubscriptions
	return nil
}
