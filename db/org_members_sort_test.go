package db

import (
	"context"
	"slices"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestOrgMembersSort asserts each sort field orders the way a person reads the column: case and
// accents ignored, member numbers compared as numbers, ties broken by the next key.
func TestOrgMembersSort(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	members := []*OrgMember{
		{Name: "beatriz", Surname: "Zapata", Email: "b@example.com", MemberNumber: "273"},
		{Name: "Álvaro", Surname: "Gómez", Email: "a10@example.com", MemberNumber: "63"},
		{Name: "Carlos", Surname: "álvarez", Email: "a9@example.com", MemberNumber: "1000"},
		{Name: "Álvaro", Surname: "Abad", Email: "C@example.com", MemberNumber: "9"},
	}
	for _, m := range members {
		m.OrgAddress = testOrgAddress
		_, err := testDB.SetOrgMember(testSalt, m)
		c.Assert(err, qt.IsNil)
	}

	listed := func(query OrgMembersQuery) []string {
		query.Page, query.Limit = 1, 10
		total, got, err := testDB.OrgMembers(testOrgAddress, query)
		c.Assert(err, qt.IsNil)
		c.Assert(total, qt.Equals, int64(len(members)))
		numbers := make([]string, 0, len(got))
		for _, m := range got {
			numbers = append(numbers, m.MemberNumber)
		}
		return numbers
	}

	for _, tc := range []struct {
		sortBy OrgMemberSortField
		want   []string // member numbers, ascending
	}{
		// "Álvaro" sorts with the a's, both Álvaros tie and fall to surname
		{"", []string{"9", "63", "273", "1000"}},
		{OrgMemberSortFieldName, []string{"9", "63", "273", "1000"}},
		{OrgMemberSortFieldSurname, []string{"9", "1000", "63", "273"}},
		// "a9" before "a10" (numeric), "C" among the lowercase c's
		{OrgMemberSortFieldEmail, []string{"1000", "63", "273", "9"}},
		{OrgMemberSortFieldMemberNumber, []string{"9", "63", "273", "1000"}},
	} {
		name := string(tc.sortBy)
		if name == "" {
			name = "default"
		}
		c.Run(name, func(c *qt.C) {
			c.Assert(listed(OrgMembersQuery{SortBy: tc.sortBy}), qt.DeepEquals, tc.want)
			desc := slices.Clone(tc.want)
			slices.Reverse(desc)
			c.Assert(listed(OrgMembersQuery{SortBy: tc.sortBy, Descending: true}), qt.DeepEquals, desc)
		})
	}

	_, _, err := testDB.OrgMembers(testOrgAddress, OrgMembersQuery{Limit: 10, SortBy: "phone"})
	c.Assert(err, qt.ErrorIs, ErrInvalidData)
}

// TestOrgMembersSortUsesIndex asserts every sort of the members list, in both directions, is served
// by an index rather than sorted in memory: that is what keeps paging a large memberbase cheap, and
// it silently breaks if the query's collation or keys drift from the migration's indexes.
func TestOrgMembersSortUsesIndex(t *testing.T) {
	c := qt.New(t)
	// the driver's Collation struct lowercases its field names, which a raw command rejects
	collation := bson.D{
		{Key: "locale", Value: OrgMemberSortCollation.Locale},
		{Key: "strength", Value: OrgMemberSortCollation.Strength},
		{Key: "numericOrdering", Value: OrgMemberSortCollation.NumericOrdering},
	}
	for sortBy, keys := range orgMemberSortKeys {
		for _, direction := range []int{1, -1} {
			sort := bson.D{}
			for _, key := range keys {
				sort = append(sort, bson.E{Key: key, Value: direction})
			}
			var explain explainResult
			err := testDB.DBClient.Database(testDB.database).RunCommand(context.Background(), bson.D{
				{Key: "explain", Value: bson.D{
					{Key: "find", Value: "orgMembers"},
					{Key: "filter", Value: bson.M{"orgAddress": testOrgAddress}},
					{Key: "sort", Value: sort},
					{Key: "collation", Value: collation},
					{Key: "limit", Value: 10},
				}},
			}).Decode(&explain)
			c.Assert(err, qt.IsNil)
			stages := explain.QueryPlanner.WinningPlan.stages()
			c.Assert(stages, qt.Not(qt.Contains), "SORT", qt.Commentf("sortBy %s, direction %d: %v", sortBy, direction, stages))
			c.Assert(stages, qt.Contains, "IXSCAN", qt.Commentf("sortBy %s, direction %d: %v", sortBy, direction, stages))
		}
	}
}

// explainResult is the part of an explain command's output that holds the chosen plan.
type explainResult struct {
	QueryPlanner explainPlanner `bson:"queryPlanner"`
}

// explainPlanner holds the plan the query planner chose.
type explainPlanner struct {
	WinningPlan explainPlan `bson:"winningPlan"`
}

// explainPlan is a query plan node from an explain command's output.
type explainPlan struct {
	Stage      string       `bson:"stage"`
	InputStage *explainPlan `bson:"inputStage"`
	// QueryPlan wraps the plan when the slot-based engine runs the query
	QueryPlan *explainPlan `bson:"queryPlan"`
}

// stages flattens the plan's stage chain.
func (p *explainPlan) stages() []string {
	if p.QueryPlan != nil {
		return p.QueryPlan.stages()
	}
	stages := []string{p.Stage}
	if p.InputStage != nil {
		stages = append(stages, p.InputStage.stages()...)
	}
	return stages
}

// TestMemberSortIndexesMigration asserts migration 0023 replaces the dead hashedPhone index with the
// sort indexes, that re-running it is a no-op, and that its down migration restores the old state.
func TestMemberSortIndexesMigration(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	mig, ok := migrations.AsMap()[23]
	c.Assert(ok, qt.IsTrue)
	database := testDB.DBClient.Database(testDB.database)
	c.Cleanup(func() {
		// restore the migrated state for the rest of the suite
		c.Assert(mig.Up(ctx, database), qt.IsNil)
	})

	byName := bson.D{
		{Key: "orgAddress", Value: int32(1)},
		{Key: "name", Value: int32(1)},
		{Key: "surname", Value: int32(1)},
		{Key: "_id", Value: int32(1)},
	}
	// the test database is migrated on init
	keys := indexKeys(c, testDB.orgMembers)
	c.Assert(keys["orgMembers_sort_name"], qt.DeepEquals, byName)
	for _, name := range []string{"orgMembers_sort_surname", "orgMembers_sort_email", "orgMembers_sort_memberNumber"} {
		_, ok := keys[name]
		c.Assert(ok, qt.IsTrue, qt.Commentf("missing index %s", name))
	}
	_, ok = keys["orgAddress_1_hashedPhone_1"]
	c.Assert(ok, qt.IsFalse)

	c.Assert(mig.Up(ctx, database), qt.IsNil)
	c.Assert(indexKeys(c, testDB.orgMembers)["orgMembers_sort_name"], qt.DeepEquals, byName)

	c.Assert(mig.Down(ctx, database), qt.IsNil)
	keys = indexKeys(c, testDB.orgMembers)
	_, ok = keys["orgMembers_sort_name"]
	c.Assert(ok, qt.IsFalse)
	_, ok = keys["orgAddress_1_hashedPhone_1"]
	c.Assert(ok, qt.IsTrue)
}
