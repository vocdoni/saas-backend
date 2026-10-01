package migrations

import (
	"strings"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// memberHashDoc holds the subset of an orgMember document needed to recompute
// the census participant login hashes. Phone is decoded as raw bytes (the
// driver unwraps the binData payload), matching string(HashedPhone) in the db
// package.
type memberHashDoc struct {
	ID           bson.ObjectID `bson:"_id"`
	Email        string        `bson:"email"`
	Phone        []byte        `bson:"phone"`
	MemberNumber string        `bson:"memberNumber"`
	NationalID   string        `bson:"nationalId"`
	Name         string        `bson:"name"`
	Surname      string        `bson:"surname"`
	BirthDate    string        `bson:"birthDate"`
}

// censusHashDoc holds the census authentication configuration needed to know
// which fields feed each login hash.
type censusHashDoc struct {
	ID          bson.ObjectID `bson:"_id"`
	AuthFields  []string      `bson:"orgMemberAuthFields"`
	TwoFaFields []string      `bson:"orgMemberTwoFaFields"`
}

// hashMemberFields mirrors db.HashAuthTwoFaFields exactly: it collects the
// configured auth and twoFa field values from the member, keyed by field name,
// and feeds them to the shared internal.HashLoginFields primitive. The field
// names must be exactly db's, since they are part of what is hashed.
//
// It is the one copy shared by migration 0015, the repair tooling
// (RepairLoginHashes, scripts/repairlogins) and migration 0025, so they can
// recompute stored hashes straight from bson documents without importing db.
// That makes it a duplicate of a
// correctness-critical function: if the two ever disagree, the repair writes
// hashes the login path will never match and every repaired voter is locked out.
// db.TestRepairMatchesCanonicalHash asserts they agree — including the lowercasing
// below, which makes login case-insensitive and must stay identical to db's.
func hashMemberFields(m memberHashDoc, authFields, twoFaFields []string) []byte {
	fields := make(map[string]string, len(authFields)+len(twoFaFields))
	for _, field := range authFields {
		switch field {
		case "name":
			fields[field] = strings.ToLower(m.Name)
		case "surname":
			fields[field] = strings.ToLower(m.Surname)
		case "memberNumber":
			fields[field] = strings.ToLower(m.MemberNumber)
		case "nationalId":
			fields[field] = strings.ToLower(m.NationalID)
		case "birthDate": //nolint:goconst
			fields[field] = strings.ToLower(m.BirthDate)
		default:
			// ignore unknown fields, mirroring db.HashAuthTwoFaFields
		}
	}
	for _, field := range twoFaFields {
		switch field {
		case "email": //nolint:goconst
			fields[field] = strings.ToLower(m.Email)
		case "phone":
			if len(m.Phone) > 0 {
				// already hashed bytes, not text: never folded
				fields[field] = string(m.Phone)
			}
		default:
			// ignore unknown fields, mirroring db.HashAuthTwoFaFields
		}
	}
	return internal.HashLoginFields(fields)
}
