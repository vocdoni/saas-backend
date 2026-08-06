package migrations

import (
	"context"
	stderrors "errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func init() {
	AddMigration(19, "census_revocation_indexes", upCensusRevocationIndexes, downCensusRevocationIndexes)
}

// upCensusRevocationIndexes adds the indexes behind the census revocation cascade (#621), promised
// as a follow-up in #629. Each of these lookups was a collection scan before: cspTokens carried no
// index beyond _id, and 0017's votingProcesses index leads with orgAddress.
func upCensusRevocationIndexes(ctx context.Context, database *mongo.Database) error {
	// Two indexes cover every way cspTokens is queried.
	//
	// The compound one is keyed for LastCSPAuth — filter {userid, bundleid}, sort {createdat: -1},
	// the hot voter-auth path — and its userid prefix serves the revocation cascade's
	// DeleteMany({userid: $in}) just as a bare {userid: 1} would.
	//
	// bundleid alone is the second: DeleteCSPAuthByBundle and the two Count*ByBundle helpers
	// predicate on bundleid with no userid, so the compound index cannot serve them (a non-leading
	// field is not a usable prefix). The delete runs once per bundle in a loop on org teardown and
	// GDPR erasure, so those are the scans that repeat.
	if _, err := database.Collection("cspTokens").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{
			{Key: "userid", Value: 1},
			{Key: "bundleid", Value: 1},
			{Key: "createdat", Value: -1},
		}},
		{Keys: bson.D{{Key: "bundleid", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on cspTokens: %w", err)
	}
	// the access path from a census back to the processes built on it (VotingProcessesByCensus,
	// arriving with #621); 0017's index leads with orgAddress and cannot serve a censusId predicate.
	if _, err := database.Collection("votingProcesses").Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "censusId", Value: 1}}},
	}); err != nil {
		return fmt.Errorf("failed to create indexes on votingProcesses: %w", err)
	}
	return nil
}

// downCensusRevocationIndexes drops the indexes again. They carry no data, so unlike the
// data-bearing collections this migration is genuinely reversible.
func downCensusRevocationIndexes(ctx context.Context, database *mongo.Database) error {
	for _, idx := range []struct {
		collection string
		name       string
	}{
		{"cspTokens", "userid_1_bundleid_1_createdat_-1"},
		{"cspTokens", "bundleid_1"},
		{"votingProcesses", "censusId_1"},
	} {
		if _, err := database.Collection(idx.collection).Indexes().DropOne(ctx, idx.name); err != nil {
			// Nothing to drop is not a failure to drop: IndexNotFound (27) means the up never got
			// far enough to create it, NamespaceNotFound (26) that the collection itself is gone.
			// The driver only forgives 26 on IndexView.List, so DropOne needs it spelled out here.
			var cmdErr mongo.CommandError
			if !stderrors.As(err, &cmdErr) || (cmdErr.Code != 27 && cmdErr.Code != 26) {
				return fmt.Errorf("failed to drop index %s on %s: %w", idx.name, idx.collection, err)
			}
		}
	}
	return nil
}
