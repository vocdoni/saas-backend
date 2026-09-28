package migrations

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func init() {
	AddMigration(23, "member_sort_indexes", upMemberSortIndexes, downMemberSortIndexes)
}

// deadOrgMembersPhoneIndexName is the {orgAddress, hashedPhone} index from 0002. Members store the
// hash under "phone" since the field was renamed, so no query uses it anymore.
const deadOrgMembersPhoneIndexName = "orgAddress_1_hashedPhone_1"

// orgMembersPhoneIndexName replaces the dead index for the phone lookups (db.OrgMembersByField).
const orgMembersPhoneIndexName = "orgAddress_1_phone_1"

// memberSortIndexSpecs lists one index per sort of the members list (db.OrgMembers), tie-breakers
// included, all under the organization prefix.
var memberSortIndexSpecs = []struct {
	name string
	keys []string
}{
	{"orgMembers_sort_name", []string{"name", "surname", "_id"}},
	{"orgMembers_sort_surname", []string{"surname", "name", "_id"}},
	{"orgMembers_sort_email", []string{"email", "_id"}},
	{"orgMembers_sort_memberNumber", []string{"memberNumber", "_id"}},
}

// memberSortIndexes builds the index models for memberSortIndexSpecs. The collation must match
// db.orgMemberSortCollation: Mongo only uses an index to sort when the query's collation equals
// the index's.
func memberSortIndexes() []mongo.IndexModel {
	collation := &options.Collation{Locale: "en", Strength: 1, NumericOrdering: true}
	models := make([]mongo.IndexModel, 0, len(memberSortIndexSpecs))
	for _, s := range memberSortIndexSpecs {
		keys := bson.D{{Key: "orgAddress", Value: 1}}
		for _, k := range s.keys {
			keys = append(keys, bson.E{Key: k, Value: 1})
		}
		models = append(models, mongo.IndexModel{
			Keys:    keys,
			Options: options.Index().SetName(s.name).SetCollation(collation),
		})
	}
	return models
}

// upMemberSortIndexes creates the collated sort indexes and swaps the dead phone index for one on
// the field the phone lookups actually query.
func upMemberSortIndexes(ctx context.Context, database *mongo.Database) error {
	phone := mongo.IndexModel{
		Keys:    bson.D{{Key: "orgAddress", Value: 1}, {Key: "phone", Value: 1}},
		Options: options.Index().SetName(orgMembersPhoneIndexName),
	}
	indexes := append(memberSortIndexes(), phone)
	orgMembers := database.Collection("orgMembers")
	if err := replaceIndex(ctx, orgMembers, []string{deadOrgMembersPhoneIndexName}, indexes); err != nil {
		return fmt.Errorf("failed to create member sort indexes: %w", err)
	}
	return nil
}

// downMemberSortIndexes drops the new indexes and restores the phone index as 0002 left it.
func downMemberSortIndexes(ctx context.Context, database *mongo.Database) error {
	names := make([]string, 0, len(memberSortIndexSpecs)+1)
	for _, s := range memberSortIndexSpecs {
		names = append(names, s.name)
	}
	names = append(names, orgMembersPhoneIndexName)
	restore := mongo.IndexModel{Keys: bson.D{{Key: "orgAddress", Value: 1}, {Key: "hashedPhone", Value: 1}}}
	if err := replaceIndex(ctx, database.Collection("orgMembers"), names, []mongo.IndexModel{restore}); err != nil {
		return fmt.Errorf("failed to drop member sort indexes: %w", err)
	}
	return nil
}
