package db

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (ms *MongoStorage) GetUsageSnapshot(orgAddress common.Address, periodStart time.Time) (*UsageSnapshot, error) {
	if orgAddress.Cmp(common.Address{}) == 0 || periodStart.IsZero() {
		return nil, ErrInvalidData
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"orgAddress": orgAddress, "periodStart": periodStart}
	snapshot := &UsageSnapshot{}
	if err := ms.usageSnapshots.FindOne(ctx, filter).Decode(snapshot); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to get usage snapshot: %w", err)
	}
	return snapshot, nil
}

func (ms *MongoStorage) UpsertUsageSnapshot(snapshot *UsageSnapshot) error {
	if snapshot == nil || snapshot.OrgAddress.Cmp(common.Address{}) == 0 || snapshot.PeriodStart.IsZero() {
		return ErrInvalidData
	}

	now := time.Now()
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = now
	}
	if snapshot.UpdatedAt.IsZero() {
		snapshot.UpdatedAt = now
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"orgAddress": snapshot.OrgAddress, "periodStart": snapshot.PeriodStart}
	update := bson.M{"$setOnInsert": snapshot}
	_, err := ms.usageSnapshots.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("failed to upsert usage snapshot: %w", err)
	}
	return nil
}

func (ms *MongoStorage) EnsureUsageSnapshot(snapshot *UsageSnapshot) (*UsageSnapshot, bool, error) {
	if err := ms.UpsertUsageSnapshot(snapshot); err != nil {
		return nil, false, err
	}
	created := false
	stored, err := ms.GetUsageSnapshot(snapshot.OrgAddress, snapshot.PeriodStart)
	if err != nil {
		return nil, false, err
	}
	if stored.CreatedAt.Equal(snapshot.CreatedAt) {
		created = true
	}
	return stored, created, nil
}
