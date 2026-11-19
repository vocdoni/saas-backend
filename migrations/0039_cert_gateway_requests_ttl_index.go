package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(39, "cert_gateway_requests_ttl_index", upCertGatewayRequestsTTLIndex, downCertGatewayRequestsTTLIndex)
}

// certGatewayRequestsTTLIndexName is Mongo's default name of the index below, kept explicit so the
// down migration drops it deterministically.
const certGatewayRequestsTTLIndexName = "expiresAt_1"

// upCertGatewayRequestsTTLIndex adds a TTL index on the digests of the accepted certificate gateway
// requests, kept only while their signature is fresh. Their _id is the digest, unique already.
func upCertGatewayRequestsTTLIndex(ctx context.Context, database *mongo.Database) error {
	model := mongo.IndexModel{
		Keys:    bson.D{{Key: "expiresAt", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	}
	if _, err := database.Collection("certGatewayRequests").Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("failed to create certGatewayRequests ttl index: %w", err)
	}
	return nil
}

// downCertGatewayRequestsTTLIndex drops that index, tolerating it being already gone.
func downCertGatewayRequestsTTLIndex(ctx context.Context, database *mongo.Database) error {
	err := replaceIndex(ctx, database.Collection("certGatewayRequests"), []string{certGatewayRequestsTTLIndexName}, nil)
	if err != nil {
		return fmt.Errorf("failed to drop certGatewayRequests ttl index: %w", err)
	}
	return nil
}
