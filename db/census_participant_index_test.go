package db

import (
	"context"
	"crypto/rand"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestLoginHashLookupUsesPartialIndex asserts a login-hash lookup examines at most one document per
// $or branch, however large the census. The (censusId, loginHash*) indexes are partial, so a query
// that does not imply their filter expression falls back to the censusId index and examines every
// participant of the census, which makes CSP auth cost grow with the census size.
func TestLoginHashLookupUsesPartialIndex(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	const participants = 500
	censusID := bson.NewObjectID().Hex()
	randomHash := func() []byte {
		b := make([]byte, 32)
		_, err := rand.Read(b)
		c.Assert(err, qt.IsNil)
		return b
	}
	docs := make([]any, 0, participants)
	var last bson.M
	for range participants {
		last = bson.M{
			"participantID":  bson.NewObjectID().Hex(),
			"censusId":       censusID,
			"loginHash":      randomHash(),
			"loginHashEmail": randomHash(),
			"loginHashPhone": randomHash(),
		}
		docs = append(docs, last)
	}
	_, err := testDB.censusParticipants.InsertMany(context.Background(), docs)
	c.Assert(err, qt.IsNil)

	for name, hashes := range map[string]bson.M{
		"loginHash only": {"loginHash": last["loginHash"]},
		"2FA email and phone": {
			"loginHash":      last["loginHash"],
			"loginHashEmail": last["loginHashEmail"],
			"loginHashPhone": last["loginHashPhone"],
		},
	} {
		c.Run(name, func(c *qt.C) {
			filter := bson.M{"censusId": censusID, "$or": loginHashesOrFilter(hashes)}
			var explain struct {
				ExecutionStats struct {
					NReturned         int `bson:"nReturned"`
					TotalDocsExamined int `bson:"totalDocsExamined"`
				} `bson:"executionStats"`
			}
			err := testDB.DBClient.Database(testDB.database).RunCommand(context.Background(), bson.D{
				{Key: "explain", Value: bson.D{
					{Key: "find", Value: "censusParticipants"},
					{Key: "filter", Value: filter},
				}},
				{Key: "verbosity", Value: "executionStats"},
			}).Decode(&explain)
			c.Assert(err, qt.IsNil)
			c.Assert(explain.ExecutionStats.NReturned, qt.Equals, 1)
			c.Assert(explain.ExecutionStats.TotalDocsExamined <= len(hashes), qt.IsTrue,
				qt.Commentf("examined %d documents for %d branches", explain.ExecutionStats.TotalDocsExamined, len(hashes)))
		})
	}
}
