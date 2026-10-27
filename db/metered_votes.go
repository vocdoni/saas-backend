package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// MeterRelayedVote meters, against org's sent-votes counter, the vote cast under nullifier, exactly
// once per nullifier: the vote's nullifier is recorded in meteredVotes with an insert-if-absent
// keyed on it, and the counter grows only when that insert happens. Overwrites of the vote (same
// nullifier) and re-relays of its public tx bytes find the record and are not metered again, and
// two relays of the same vote racing each other insert it only once. It reports whether this call
// metered the vote. If the counter cannot be incremented, the record is removed again so a later
// relay of the vote can meter it.
func (ms *MongoStorage) MeterRelayedVote(org common.Address, nullifier internal.HexBytes) (bool, error) {
	if len(nullifier) == 0 {
		return false, ErrBadInputs
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.meteredVotes.UpdateOne(ctx,
		bson.M{"_id": nullifier},
		bson.M{"$setOnInsert": bson.M{"orgAddress": org, "meteredAt": time.Now()}},
		options.UpdateOne().SetUpsert(true),
	)
	switch {
	case mongo.IsDuplicateKeyError(err):
		// a concurrent relay of the same vote inserted it first: that one meters it
		return false, nil
	case err != nil:
		return false, fmt.Errorf("could not record metered vote: %w", err)
	case res.UpsertedCount == 0:
		return false, nil // metered before
	}
	if err := ms.IncrementOrganizationSentVotesCounter(org); err != nil {
		if _, delErr := ms.meteredVotes.DeleteOne(ctx, bson.M{"_id": nullifier}); delErr != nil {
			log.Warnw("could not undo metered vote record", "nullifier", nullifier.String(), "error", delErr)
		}
		return false, fmt.Errorf("could not increment sent votes counter: %w", err)
	}
	return true, nil
}
