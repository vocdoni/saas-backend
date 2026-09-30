package db

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// CensusMembersError is returned when complete members of a census would be stored under the same
// login hash as another member or participant of the census, which the census keeps under a unique
// index (e.g. names differing only by case, which the hash folds).
type CensusMembersError struct {
	Duplicates []bson.ObjectID `json:"duplicates"`
}

// Error implements the error interface.
func (e *CensusMembersError) Error() string {
	return fmt.Sprintf("%d census members share login data with another member", len(e.Duplicates))
}

// MissingLoginData reports whether the member lacks the data to log in to a census with these
// fields: any auth field empty, or, when 2FA is configured, no 2FA channel at all (with both email
// and phone configured either one is enough). An empty string counts as empty just like an absent
// field, and so does a whitespace-only one.
func (m *OrgMember) MissingLoginData(authFields OrgMemberAuthFields, twoFaFields OrgMemberTwoFaFields) bool {
	for _, field := range authFields {
		if value, known := memberAuthFieldValue(m, field); known && strings.TrimSpace(value) == "" {
			return true
		}
	}
	if len(twoFaFields) == 0 {
		return false
	}
	for _, field := range twoFaFields {
		switch field {
		case OrgMemberTwoFaFieldEmail:
			if strings.TrimSpace(m.Email) != "" {
				return false
			}
		case OrgMemberTwoFaFieldPhone:
			if !m.Phone.IsEmpty() {
				return false
			}
		default:
			// unknown fields hash to nothing (HashAuthTwoFaFields), so they are no channel either
		}
	}
	return true
}

// memberAuthFieldValue returns the value of an auth field of the member, and false for a field it
// does not know, which HashAuthTwoFaFields ignores too.
func memberAuthFieldValue(m *OrgMember, field OrgMemberAuthField) (value string, known bool) {
	switch field {
	case OrgMemberAuthFieldsName:
		return m.Name, true
	case OrgMemberAuthFieldsSurname:
		return m.Surname, true
	case OrgMemberAuthFieldsMemberNumber:
		return m.MemberNumber, true
	case OrgMemberAuthFieldsNationalID:
		return m.NationalID, true
	case OrgMemberAuthFieldsBirthDate:
		return m.BirthDate, true
	default:
		return "", false
	}
}

// loginKey is one unique key a census participant occupies: the censusParticipants field holding a
// login hash (each with its own unique index per census) and the hash value.
type loginKey struct {
	field string
	hash  string
}

// censusMember is a member to store as a census participant, with the login hashes it is stored under.
type censusMember struct {
	id     bson.ObjectID
	hashes map[string][]byte
}

// censusMembers splits the members of a census: those missing login data, left out of the census
// since they could never log in, and the rest with their login hashes, computed once.
type censusMembers struct {
	complete []censusMember
	missing  []bson.ObjectID
	// holders maps every login key to the members and existing participants holding it
	holders map[loginKey][]string
}

// duplicates returns the complete members holding a login hash that another member or existing
// participant holds too: exactly the ones the census unique indexes would refuse.
func (cm *censusMembers) duplicates() []bson.ObjectID {
	dups := make([]bson.ObjectID, 0)
	for _, m := range cm.complete {
		for field, hash := range m.hashes {
			if len(cm.holders[loginKey{field: field, hash: string(hash)}]) > 1 {
				dups = append(dups, m.id)
				break
			}
		}
	}
	return dups
}

// classifyCensusMembers reads the members from cur, a cursor over org members projected to the
// census login fields (findMemberFieldsCursor), in a single pass. The existing participants of the
// census, if any, take part in the duplicate check, except for the members being re-stored.
func classifyCensusMembers(
	ctx context.Context,
	cur *mongo.Cursor,
	census Census,
	existing []CensusParticipant,
) (*censusMembers, error) {
	cm := &censusMembers{missing: make([]bson.ObjectID, 0), holders: make(map[loginKey][]string)}
	seen := make(map[string]bool)
	for cur.Next(ctx) {
		m := &OrgMember{}
		if err := cur.Decode(m); err != nil {
			return nil, fmt.Errorf("decoding member: %w", err)
		}
		if m.MissingLoginData(census.AuthFields, census.TwoFaFields) {
			cm.missing = append(cm.missing, m.ID)
			continue
		}
		hashes := calculateParticipantHashes(census, *m)
		for field, hash := range hashes {
			key := loginKey{field: field, hash: string(hash)}
			cm.holders[key] = append(cm.holders[key], m.ID.Hex())
		}
		cm.complete = append(cm.complete, censusMember{id: m.ID, hashes: hashes})
		seen[m.ID.Hex()] = true
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("reading members: %w", err)
	}
	for _, p := range existing {
		if seen[p.ParticipantID] {
			continue // re-stored under its new hashes
		}
		for field, hash := range map[string][]byte{
			"loginHash": p.LoginHash, "loginHashEmail": p.LoginHashEmail, "loginHashPhone": p.LoginHashPhone,
		} {
			if len(hash) > 0 {
				key := loginKey{field: field, hash: string(hash)}
				cm.holders[key] = append(cm.holders[key], p.ParticipantID)
			}
		}
	}
	return cm, nil
}
