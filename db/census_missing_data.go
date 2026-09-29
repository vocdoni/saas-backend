package db

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// CensusMembersError is returned when members of a census would be stored under the same login
// hash, which the census keeps under a unique index. MissingData lists those missing required auth
// data (missing the same data, they hash identically), Duplicates those whose data is complete but
// hashes like another member's (e.g. names differing only by case, which the hash folds).
type CensusMembersError struct {
	MissingData []bson.ObjectID `json:"missingData"`
	Duplicates  []bson.ObjectID `json:"duplicates"`
}

// Error implements the error interface.
func (e *CensusMembersError) Error() string {
	return fmt.Sprintf("%d census members are missing required auth data or share login data with another member",
		len(e.MissingData)+len(e.Duplicates))
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

// sharedLoginHashes returns the members holding a login hash that another of the members holds too:
// the same hashes the census unique indexes store, so exactly the members that cannot be told apart.
func sharedLoginHashes(census Census, members []*OrgMember) map[bson.ObjectID]bool {
	holders := make(map[loginKey][]bson.ObjectID)
	for _, m := range members {
		for field, hash := range calculateParticipantHashes(census, *m) {
			key := loginKey{field: field, hash: string(hash)}
			holders[key] = append(holders[key], m.ID)
		}
	}
	shared := make(map[bson.ObjectID]bool)
	for _, ids := range holders {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			shared[id] = true
		}
	}
	return shared
}

// loginHashClashes returns a *CensusMembersError when members would be stored under the same login
// hash as another member of the census, which the unique index would refuse halfway through the
// build. A member missing data that clashes with nobody is left in: it cannot log in anyway.
func loginHashClashes(census *Census, members []*OrgMember) error {
	shared := sharedLoginHashes(*census, members)
	if len(shared) == 0 {
		return nil
	}
	err := &CensusMembersError{MissingData: make([]bson.ObjectID, 0), Duplicates: make([]bson.ObjectID, 0)}
	for _, m := range members {
		switch {
		case !shared[m.ID]:
		case m.MissingLoginData(census.AuthFields, census.TwoFaFields):
			err.MissingData = append(err.MissingData, m.ID)
		default:
			err.Duplicates = append(err.Duplicates, m.ID)
		}
	}
	return err
}
