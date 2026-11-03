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

	all := bson.M{
		"loginHash":      last["loginHash"],
		"loginHashEmail": last["loginHashEmail"],
		"loginHashPhone": last["loginHashPhone"],
	}
	for _, tc := range []struct {
		name   string
		hashes bson.M
		extra  bson.M
		want   int // documents returned
	}{
		{"loginHash only", bson.M{"loginHash": last["loginHash"]}, nil, 1},
		{"2FA email and phone", all, nil, 1},
		// the duplicate checks exclude the member's own participant
		{"duplicate check", all, bson.M{"participantID": bson.M{"$ne": last["participantID"]}}, 0},
	} {
		c.Run(tc.name, func(c *qt.C) {
			filter := loginHashesFilter(censusID, tc.hashes, tc.extra)
			var explain explainExecution
			err := testDB.DBClient.Database(testDB.database).RunCommand(context.Background(), bson.D{
				{Key: "explain", Value: bson.D{
					{Key: "find", Value: "censusParticipants"},
					{Key: "filter", Value: filter},
				}},
				{Key: "verbosity", Value: "executionStats"},
			}).Decode(&explain)
			c.Assert(err, qt.IsNil)
			c.Assert(explain.ExecutionStats.NReturned, qt.Equals, tc.want)
			c.Assert(explain.ExecutionStats.TotalDocsExamined <= len(tc.hashes), qt.IsTrue,
				qt.Commentf("examined %d documents for %d branches", explain.ExecutionStats.TotalDocsExamined, len(tc.hashes)))
		})
	}
}

// explainExecution is the part of an executionStats explain that counts the work a query did.
type explainExecution struct {
	ExecutionStats explainExecutionStats `bson:"executionStats"`
}

// explainExecutionStats counts the documents a query returned and examined.
type explainExecutionStats struct {
	NReturned         int `bson:"nReturned"`
	TotalDocsExamined int `bson:"totalDocsExamined"`
}
