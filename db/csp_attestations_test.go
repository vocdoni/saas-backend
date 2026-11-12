package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func testAttestation(voter byte) *CSPAuthAttestation {
	return &CSPAuthAttestation{
		VoterID:     internal.HexBytes(common.Hash{voter}.Bytes()),
		ContactHash: internal.HexBytes(common.Hash{0xcc, voter}.Bytes()),
		ContactKind: "email",
		ExpiresAt:   time.Now().Add(time.Minute).Unix(),
		Signature:   internal.HexBytes(make([]byte, 65)),
		Signer:      common.Address{0xaa},
	}
}

func createAttestedCensus(c *qt.C, size int64) bson.ObjectID {
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	now := time.Now()
	id, err := testDB.SetCensus(&Census{
		OrgAddress: testOrgAddress,
		Size:       size,
		Attested: &AttestedCensus{
			Signer:        common.Address{0xaa},
			SignerHistory: []AttestationSigner{{Signer: common.Address{0xaa}, From: now}},
		},
	})
	c.Assert(err, qt.IsNil)
	oid, err := bson.ObjectIDFromHex(id)
	c.Assert(err, qt.IsNil)
	return oid
}

func TestAttestedCSPAuth(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	token := internal.HexBytes(uuid.New().String())
	att := testAttestation(1)
	c.Assert(testDB.SetAttestedCSPAuth(token, testUserID, testCSPBundleID, "", att), qt.IsNil)
	got, err := testDB.CSPAuth(token)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Attestation, qt.DeepEquals, att)

	// a memberbase token carries no attestation
	plain := internal.HexBytes(uuid.New().String())
	c.Assert(testDB.SetCSPAuth(plain, testUserID, testCSPBundleID, ""), qt.IsNil)
	got, err = testDB.CSPAuth(plain)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Attestation, qt.IsNil)
}

func TestAttestationRecords(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	processID := bson.NewObjectID()
	userA := internal.HexBytes{0x0a}
	userB := internal.HexBytes{0x0b}
	q1, q2 := internal.HexBytes{0x01}, internal.HexBytes{0x02}

	c.Assert(testDB.RecordAttestedSignature(processID, userA, nil, q1), qt.ErrorIs, ErrBadInputs)

	first := testAttestation(1)
	c.Assert(testDB.RecordAttestedSignature(processID, userA, first, q1), qt.IsNil)
	// a later attestation for the same voter keeps the first one and only adds the question
	c.Assert(testDB.RecordAttestedSignature(processID, userA, testAttestation(9), q2), qt.IsNil)
	c.Assert(testDB.RecordAttestedSignature(processID, userA, testAttestation(9), q2), qt.IsNil)
	c.Assert(testDB.RecordAttestedSignature(processID, userB, testAttestation(2), q1), qt.IsNil)
	// another process does not leak into this one
	c.Assert(testDB.RecordAttestedSignature(bson.NewObjectID(), internal.HexBytes{0x0c}, testAttestation(3), q1), qt.IsNil)

	record, err := testDB.AttestationRecord(processID, userA)
	c.Assert(err, qt.IsNil)
	c.Assert(record.VoterID, qt.DeepEquals, first.VoterID)
	c.Assert(record.Signature, qt.DeepEquals, first.Signature)
	c.Assert(record.Questions, qt.DeepEquals, []internal.HexBytes{q1, q2})

	_, err = testDB.AttestationRecord(processID, internal.HexBytes{0x0c})
	c.Assert(err, qt.ErrorIs, ErrNotFound)

	n, err := testDB.CountAttestationRecords(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(n, qt.Equals, int64(2))

	total, page, err := testDB.AttestationRecords(processID, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(total, qt.Equals, int64(2))
	c.Assert(page, qt.HasLen, 1)
	c.Assert(page[0].UserID, qt.DeepEquals, userA)
}

func TestRotateAttestationSigner(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	censusID := createAttestedCensus(c, 10)
	next := common.Address{0xbb}
	c.Assert(testDB.RotateAttestationSigner(censusID, common.Address{}), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.RotateAttestationSigner(censusID, next), qt.IsNil)
	// rotating to the active signer adds no history entry
	c.Assert(testDB.RotateAttestationSigner(censusID, next), qt.IsNil)

	census, err := testDB.Census(censusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(census.Attested.Signer, qt.Equals, next)
	c.Assert(census.Attested.SignerHistory, qt.HasLen, 2)
	c.Assert(census.Attested.SignerHistory[1].Signer, qt.Equals, next)

	plainID, err := testDB.SetCensus(&Census{OrgAddress: testOrgAddress})
	c.Assert(err, qt.IsNil)
	plain, err := bson.ObjectIDFromHex(plainID)
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.RotateAttestationSigner(plain, next), qt.ErrorIs, ErrNotFound)
}

func TestGrowAttestedCensusSize(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	censusID := createAttestedCensus(c, 10)
	c.Assert(testDB.GrowAttestedCensusSize(censusID, 12), qt.IsNil)
	c.Assert(testDB.GrowAttestedCensusSize(censusID, 12), qt.IsNil)
	c.Assert(testDB.GrowAttestedCensusSize(censusID, 11), qt.ErrorIs, ErrInvalidData)
	c.Assert(testDB.GrowAttestedCensusSize(bson.NewObjectID(), 20), qt.ErrorIs, ErrNotFound)

	census, err := testDB.Census(censusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(census.Size, qt.Equals, int64(12))
}

// TestMigrationCSPAttestations: the collection and its (processId, _id) index exist, and Up is
// idempotent.
func TestMigrationCSPAttestations(t *testing.T) {
	c := qt.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mig := migrationByVersion(c, 36)
	database := testDB.DBClient.Database(testDB.database)
	c.Assert(mig.Up(ctx, database), qt.IsNil)

	specs, err := database.Collection("cspAttestations").Indexes().ListSpecifications(ctx)
	c.Assert(err, qt.IsNil)
	var keys []string
	for _, spec := range specs {
		keys = append(keys, spec.KeysDocument.String())
	}
	c.Assert(keys, qt.Contains, `{"processId": {"$numberInt":"1"},"_id": {"$numberInt":"1"}}`)
}
