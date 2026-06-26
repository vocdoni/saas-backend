package migrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.vocdoni.io/dvote/log"
)

func init() {
	AddMigration(12, "normalize_member_emails", upNormalizeMemberEmails, downNormalizeMemberEmails)
}

// memberHashDoc holds the subset of an orgMember document needed to recompute
// the census participant login hashes. Phone is decoded as raw bytes (the
// driver unwraps the binData payload), matching string(HashedPhone) in the db
// package.
type memberHashDoc struct {
	ID           primitive.ObjectID `bson:"_id"`
	Email        string             `bson:"email"`
	Phone        []byte             `bson:"phone"`
	MemberNumber string             `bson:"memberNumber"`
	NationalID   string             `bson:"nationalId"`
	Name         string             `bson:"name"`
	Surname      string             `bson:"surname"`
	BirthDate    string             `bson:"birthDate"`
}

// censusHashDoc holds the census authentication configuration needed to know
// which fields feed each login hash.
type censusHashDoc struct {
	AuthFields  []string `bson:"orgMemberAuthFields"`
	TwoFaFields []string `bson:"orgMemberTwoFaFields"`
}

// participantHashDoc holds the census participant identifiers needed to locate
// and update the participant document.
type participantHashDoc struct {
	ParticipantID string `bson:"participantID"`
	CensusID      string `bson:"censusId"`
}

// hashMemberFields mirrors db.HashAuthTwoFaFields exactly: it collects the
// configured auth and twoFa field values from the member and feeds them to the
// shared internal.HashSortedFields primitive. The append order is irrelevant
// because HashSortedFields sorts before hashing, so only the set of values
// must match the canonical implementation.
func hashMemberFields(m memberHashDoc, authFields, twoFaFields []string) []byte {
	data := make([]string, 0, len(authFields)+len(twoFaFields))
	for _, field := range authFields {
		switch field {
		case "name":
			data = append(data, m.Name)
		case "surname":
			data = append(data, m.Surname)
		case "memberNumber":
			data = append(data, m.MemberNumber)
		case "nationalId":
			data = append(data, m.NationalID)
		case "birthDate":
			data = append(data, m.BirthDate)
		default:
			// ignore unknown fields, mirroring db.HashAuthTwoFaFields
		}
	}
	for _, field := range twoFaFields {
		switch field {
		case "email":
			data = append(data, m.Email)
		case "phone":
			if len(m.Phone) > 0 {
				data = append(data, string(m.Phone))
			}
		default:
			// ignore unknown fields, mirroring db.HashAuthTwoFaFields
		}
	}
	return internal.HashSortedFields(data)
}

// recomputeParticipantHashes mirrors db.calculateParticipantHashesBson: it
// produces exactly the same hash keys that were originally stored for a
// participant, so the migration never adds or removes keys.
func recomputeParticipantHashes(m memberHashDoc, c censusHashDoc) bson.M {
	hashes := bson.M{
		"loginHash": hashMemberFields(m, c.AuthFields, c.TwoFaFields),
	}
	if len(c.TwoFaFields) == 2 && len(m.Email) > 0 {
		hashes["loginHashEmail"] = hashMemberFields(m, c.AuthFields, []string{"email"})
	}
	if len(c.TwoFaFields) == 2 && len(m.Phone) > 0 {
		hashes["loginHashPhone"] = hashMemberFields(m, c.AuthFields, []string{"phone"})
	}
	return hashes
}

// upNormalizeMemberEmails lowercases every stored member email that still
// contains uppercase characters and recomputes the affected census participant
// login hashes, so that members imported before email normalization can still
// authenticate via the (now lowercase) CSP 2FA login.
//
// When lowercasing an email would make two members collide on the same login
// hash within a census (blocked by the unique index from migration 8), the
// whole member is skipped and logged for manual review rather than aborting the
// migration. A skipped member keeps its uppercase email, so it is re-detected
// on a subsequent run once the conflict is resolved.
func upNormalizeMemberEmails(ctx context.Context, database *mongo.Database) error {
	orgMembers := database.Collection("orgMembers")
	censusParticipants := database.Collection("censusParticipants")
	censuses := database.Collection("census")

	// Cache censuses by their hex id to avoid one lookup per participant.
	censusCache := make(map[string]*censusHashDoc)
	getCensus := func(hexID string) (*censusHashDoc, error) {
		if c, ok := censusCache[hexID]; ok {
			return c, nil
		}
		oid, err := primitive.ObjectIDFromHex(hexID)
		if err != nil {
			censusCache[hexID] = nil
			return nil, nil
		}
		var c censusHashDoc
		err = censuses.FindOne(ctx, bson.M{"_id": oid}).Decode(&c)
		if errors.Is(err, mongo.ErrNoDocuments) {
			censusCache[hexID] = nil
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load census %s: %w", hexID, err)
		}
		censusCache[hexID] = &c
		return &c, nil
	}

	// Coarse server-side prefilter: only members whose email contains an ASCII
	// uppercase letter. The exact check happens in Go below.
	cursor, err := orgMembers.Find(ctx, bson.M{"email": bson.M{"$regex": "[A-Z]"}})
	if err != nil {
		return fmt.Errorf("failed to list members: %w", err)
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()

	var normalized, skipped int
	for cursor.Next(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}

		var member memberHashDoc
		if err := cursor.Decode(&member); err != nil {
			return fmt.Errorf("failed to decode member: %w", err)
		}

		lower := internal.NormalizeEmail(member.Email)
		if lower == member.Email {
			continue // already normalized (the regex is only a coarse prefilter)
		}
		memberHex := member.ID.Hex()

		// Load this member's census participants.
		pcur, err := censusParticipants.Find(ctx, bson.M{"participantID": memberHex})
		if err != nil {
			return fmt.Errorf("failed to find participants for member %s: %w", memberHex, err)
		}
		var participants []participantHashDoc
		if err := pcur.All(ctx, &participants); err != nil {
			return fmt.Errorf("failed to decode participants for member %s: %w", memberHex, err)
		}

		// Recompute the hashes from the lowercased member, checking for
		// collisions before writing anything.
		lowered := member
		lowered.Email = lower

		type participantUpdate struct {
			filter bson.M
			hashes bson.M
		}
		var updates []participantUpdate
		conflictCensus := ""

		for _, p := range participants {
			census, err := getCensus(p.CensusID)
			if err != nil {
				return err
			}
			if census == nil {
				// Census missing: cannot recompute, leave this participant untouched.
				continue
			}

			hashes := recomputeParticipantHashes(lowered, *census)

			findHashes := make([]bson.M, 0, len(hashes))
			for k, v := range hashes {
				findHashes = append(findHashes, bson.M{k: v})
			}
			if len(findHashes) == 0 {
				continue
			}

			count, err := censusParticipants.CountDocuments(ctx, bson.M{
				"participantID": bson.M{"$ne": p.ParticipantID},
				"censusId":      p.CensusID,
				"$or":           findHashes,
			})
			if err != nil {
				return fmt.Errorf("failed to check collisions for member %s in census %s: %w",
					memberHex, p.CensusID, err)
			}
			if count > 0 {
				conflictCensus = p.CensusID
				break
			}

			updates = append(updates, participantUpdate{
				filter: bson.M{"participantID": p.ParticipantID, "censusId": p.CensusID},
				hashes: hashes,
			})
		}

		if conflictCensus != "" {
			log.Warnw("skipping member email normalization due to login hash collision",
				"memberID", memberHex, "censusID", conflictCensus, "email", member.Email)
			skipped++
			continue
		}

		// No conflicts: update the participant hashes, then the member email.
		now := time.Now()
		for _, u := range updates {
			set := bson.M{"updatedAt": now}
			for k, v := range u.hashes {
				set[k] = v
			}
			if _, err := censusParticipants.UpdateOne(ctx, u.filter, bson.M{"$set": set}); err != nil {
				return fmt.Errorf("failed to update participant hashes for member %s: %w", memberHex, err)
			}
		}
		if _, err := orgMembers.UpdateOne(ctx,
			bson.M{"_id": member.ID},
			bson.M{"$set": bson.M{"email": lower, "updatedAt": now}},
		); err != nil {
			return fmt.Errorf("failed to update email for member %s: %w", memberHex, err)
		}
		normalized++
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("cursor error iterating members: %w", err)
	}

	log.Infow("normalized member emails", "normalized", normalized, "skipped", skipped)
	return nil
}

// downNormalizeMemberEmails is intentionally a no-op: the normalization is lossy
// (the original mixed-case emails and their derived login hashes cannot be
// reconstructed), so this migration is irreversible by design.
func downNormalizeMemberEmails(_ context.Context, _ *mongo.Database) error {
	return nil
}
