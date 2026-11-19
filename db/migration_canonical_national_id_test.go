package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/migrations"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/log"
)

// TestMigrationCanonicalNationalIDHashes: migration 37 rehashes participants stored under a
// non-canonical national ID, leaves one stored without its check letter (not a DNI) as it was,
// leaves (and counts) the members stored twice under two spellings of one DNI with their old
// hashes, and a second run changes nothing.
func TestMigrationCanonicalNationalIDHashes(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress, CreatedAt: time.Now()}), qt.IsNil)
	ctx := context.Background()
	database := testDB.DBClient.Database(testDB.database)
	mig := migrationByVersion(c, 37)

	auth := OrgMemberAuthFields{OrgMemberAuthFieldsNationalID}
	census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, CreatedAt: time.Now()}
	censusID, err := testDB.SetCensus(census)
	c.Assert(err, qt.IsNil)

	// legacyHash is what the previous release stored: the lowercased value as written
	legacyHash := func(m OrgMember) []byte {
		return internal.HashLoginFields(map[string]string{"nationalId": strings.ToLower(m.NationalID)})
	}
	seed := func(nationalID string) (*OrgMember, []byte) {
		m := &OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, NationalID: nationalID, CreatedAt: time.Now()}
		h := legacyHash(*m)
		seedLegacyParticipant(t, censusID, m, h)
		return m, h
	}
	rehashed, _ := seed("1234567-l")
	letterless, letterlessHash := seed("55.555.555")
	canonical, canonicalHash := seed("12345678Z")
	foreign, foreignHash := seed("P1234567")
	twinA, twinAHash := seed("X1234567L")
	twinB, twinBHash := seed("x-01234567-l")

	dry := runRepair(t, migrations.RepairOptions{})
	c.Assert(dry.ParticipantsRehashed, qt.Equals, 1)
	c.Assert(dry.ParticipantsSkipped, qt.Equals, 2)
	c.Assert(dry.SkippedMembers, qt.ContentEquals, []migrations.SkippedMember{
		{MemberID: twinA.ID.Hex(), CensusID: censusID}, {MemberID: twinB.ID.Hex(), CensusID: censusID},
	})

	stored := func(m *OrgMember) []byte {
		var p CensusParticipant
		c.Assert(testDB.censusParticipants.FindOne(ctx,
			bson.M{"participantID": m.ID.Hex(), "censusId": censusID}).Decode(&p), qt.IsNil)
		return p.LoginHash
	}
	// each skipped row is logged with its census and participant ids, so the organiser can act,
	// and never with the national ID
	out := captureLogs(t, func() { c.Assert(mig.Up(ctx, database), qt.IsNil) })
	for _, twin := range []*OrgMember{twinA, twinB} {
		c.Assert(out, qt.Matches, `(?s).*participantId=\S*`+twin.ID.Hex()+`.*`)
	}
	c.Assert(out, qt.Matches, `(?s).*censusId=\S*`+censusID+`.*`)
	for _, nationalID := range []string{"1234567", "X1234567L"} {
		c.Assert(out, qt.Not(qt.Contains), nationalID)
	}
	for range 2 { // a second run must change nothing
		c.Assert(mig.Up(ctx, database), qt.IsNil)
		c.Assert(stored(rehashed), qt.DeepEquals, HashAuthTwoFaFields(OrgMember{NationalID: "01234567L"}, auth, nil))
		c.Assert(stored(letterless), qt.DeepEquals, letterlessHash)
		c.Assert(stored(canonical), qt.DeepEquals, canonicalHash)
		c.Assert(stored(foreign), qt.DeepEquals, foreignHash)
		c.Assert(stored(twinA), qt.DeepEquals, twinAHash)
		c.Assert(stored(twinB), qt.DeepEquals, twinBHash)
	}
	again := runRepair(t, migrations.RepairOptions{})
	c.Assert(again.ParticipantsRehashed, qt.Equals, 0)
	c.Assert(again.ParticipantsSkipped, qt.Equals, 2)
}

// captureLogs runs fn with the global logger writing to a file, and returns what it logged.
func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	prev := *log.Logger()
	t.Cleanup(func() { *log.Logger() = prev })
	logFile := filepath.Join(t.TempDir(), "db.log")
	f, err := os.Create(logFile)
	qt.Assert(t, err, qt.IsNil)
	t.Cleanup(func() { _ = f.Close() })
	stdout := os.Stdout
	os.Stdout = f
	log.Init("debug", "stdout", nil)
	os.Stdout = stdout
	fn()
	out, err := os.ReadFile(logFile)
	qt.Assert(t, err, qt.IsNil)
	return string(out)
}
