package db

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// CensusMissingDataError is returned when members missing required auth data cannot be told apart
// in the census: missing the same data, they would be stored under the same login hash, which the
// census keeps under a unique index. MissingData lists those members.
type CensusMissingDataError struct {
	MissingData []bson.ObjectID `json:"missingData"`
}

// Error implements the error interface.
func (e *CensusMissingDataError) Error() string {
	return fmt.Sprintf("%d census members are missing required auth data", len(e.MissingData))
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

// missingDataCollisions returns a *CensusMissingDataError when members missing required auth data
// would be stored under the same login hash as another member of the census. Members missing the
// same field otherwise hash identically (an empty value is hashed like any other), and the unique
// index would fail the whole census build. A member missing data that clashes with nobody is left
// in: it cannot log in anyway. Clashes between members whose data is complete are not checked here.
func missingDataCollisions(census *Census, members []*OrgMember) error {
	missing := make(map[bson.ObjectID]bool)
	holders := make(map[loginKey][]bson.ObjectID)
	for _, m := range members {
		if m.MissingLoginData(census.AuthFields, census.TwoFaFields) {
			missing[m.ID] = true
		}
		for field, hash := range calculateParticipantHashes(*census, *m) {
			key := loginKey{field: field, hash: string(hash)}
			holders[key] = append(holders[key], m.ID)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	clashing := make(map[bson.ObjectID]bool)
	for _, ids := range holders {
		if len(ids) < 2 {
			continue
		}
		for _, id := range ids {
			if missing[id] {
				clashing[id] = true
			}
		}
	}
	if len(clashing) == 0 {
		return nil
	}

	err := &CensusMissingDataError{MissingData: make([]bson.ObjectID, 0, len(clashing))}
	for _, m := range members {
		if clashing[m.ID] {
			err.MissingData = append(err.MissingData, m.ID)
		}
	}
	return err
}
