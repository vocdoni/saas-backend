package db

import (
	"context"
	"errors"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestLoginHashClashes pins which member sets the census build refuses up front: members that could
// not be told apart (same login hash), split into those missing auth data and complete duplicates.
func TestLoginHashClashes(t *testing.T) {
	type member struct {
		name, surname, nationalID, email string
		phone                            HashedPhone
	}
	nameNationalID := OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsNationalID}
	emailOnly := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}
	emailOrPhone := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone}

	for _, tc := range []struct {
		name     string
		auth     OrgMemberAuthFields
		twoFa    OrgMemberTwoFaFields
		members  []member
		missing  []int // indexes into members; both empty means the build is not refused
		complete []int
	}{
		{
			name:  "no email under email 2FA",
			twoFa: emailOnly,
			members: []member{
				{name: "A", email: ""},
				{name: "B", email: ""},
				{name: "C", email: "c@example.com"},
			},
			missing: []int{0, 1},
		},
		{
			name: "same auth field missing on two members",
			auth: nameNationalID,
			members: []member{
				{name: "Twin", nationalID: ""},
				{name: "Twin", nationalID: ""},
				{name: "Twin", nationalID: "X1"},
			},
			missing: []int{0, 1},
		},
		{
			name: "missing data that clashes with nobody",
			auth: nameNationalID,
			members: []member{
				{name: "Solo", nationalID: ""},
				{name: "Other", nationalID: "X1"},
			},
		},
		{
			name:  "email-or-phone: a missing email with a phone is not missing data",
			auth:  OrgMemberAuthFields{OrgMemberAuthFieldsName},
			twoFa: emailOrPhone,
			members: []member{
				{name: "Sam", email: "", phone: HashedPhone("phone-1")},
				{name: "Sam", email: "", phone: HashedPhone("phone-2")},
			},
		},
		{
			name: "complete members differing only by case share the folded hash",
			auth: OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname},
			members: []member{
				{name: "John", surname: "Smith"},
				{name: "john", surname: "smith"},
				{name: "Jane", surname: "Smith"},
			},
			complete: []int{0, 1},
		},
		{
			name:  "email-and-phone: same email, different phones share the email hash",
			auth:  OrgMemberAuthFields{OrgMemberAuthFieldsName},
			twoFa: emailOrPhone,
			members: []member{
				{name: "Sam", email: "sam@example.com", phone: HashedPhone("phone-1")},
				{name: "Sam", email: "sam@example.com", phone: HashedPhone("phone-2")},
			},
			complete: []int{0, 1},
		},
		{
			name: "a missing-data member clashing with a complete one",
			auth: nameNationalID,
			members: []member{
				{name: "Twin", nationalID: ""},
				{name: "Twin", nationalID: ""},
				{name: "Solo", nationalID: "X1"},
				{name: "solo", nationalID: "x1"},
			},
			missing:  []int{0, 1},
			complete: []int{2, 3},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			census := &Census{AuthFields: tc.auth, TwoFaFields: tc.twoFa}
			members := make([]*OrgMember, len(tc.members))
			for i, m := range tc.members {
				members[i] = &OrgMember{
					ID: bson.NewObjectID(), Name: m.name, Surname: m.surname, NationalID: m.nationalID,
					Email: m.email, Phone: m.phone,
				}
			}
			pick := func(idx []int) []bson.ObjectID {
				out := make([]bson.ObjectID, 0, len(idx))
				for _, i := range idx {
					out = append(out, members[i].ID)
				}
				return out
			}

			err := loginHashClashes(census, members)
			if len(tc.missing)+len(tc.complete) == 0 {
				c.Assert(err, qt.IsNil)
				return
			}
			var clash *CensusMembersError
			c.Assert(errors.As(err, &clash), qt.IsTrue, qt.Commentf("got %v", err))
			c.Assert(clash.MissingData, qt.DeepEquals, pick(tc.missing))
			c.Assert(clash.Duplicates, qt.DeepEquals, pick(tc.complete))
		})
	}
}

// TestCensusMembersEmptyValues runs the census pre-flight and the census build over members whose
// empty values are stored the way real imports store them: "" rather than an absent field, and
// whitespace-only strings written without going through member normalization.
func TestCensusMembersEmptyValues(t *testing.T) {
	c := qt.New(t)
	c.Assert(testDB.DeleteAllDocuments(), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: testOrgAddress}), qt.IsNil)

	// insertRaw writes the member document as-is, bypassing SetOrgMember's normalization.
	insertRaw := func(doc bson.M) string {
		id := bson.NewObjectID()
		doc["_id"] = id
		doc["orgAddress"] = testOrgAddress
		_, err := testDB.orgMembers.InsertOne(context.Background(), doc)
		c.Assert(err, qt.IsNil)
		return id.Hex()
	}
	group := func(ids ...string) string {
		id, err := testDB.CreateOrganizationMemberGroup(&OrganizationMemberGroup{
			OrgAddress: testOrgAddress, Title: "g", MemberIDs: ids,
		})
		c.Assert(err, qt.IsNil)
		return id
	}
	hexes := func(ids []bson.ObjectID) []string {
		out := make([]string, 0, len(ids))
		for _, id := range ids {
			out = append(out, id.Hex())
		}
		return out
	}
	emailOnly := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}

	// three members with an empty (not absent) email, as imported without one
	noEmail := []string{
		insertRaw(bson.M{"name": "A", "email": ""}),
		insertRaw(bson.M{"name": "B", "email": ""}),
		insertRaw(bson.M{"name": "C", "email": "  "}),
	}
	noEmailGroup := group(noEmail...)

	t.Run("pre-flight: empty email is missing data, not a duplicate", func(t *testing.T) {
		c := qt.New(t)
		res, err := testDB.CheckGroupMembersFields(testOrgAddress, noEmailGroup, nil, emailOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(res.Duplicates, qt.HasLen, 0)
		c.Assert(hexes(res.MissingData), qt.ContentEquals, noEmail)

		res, err = testDB.CheckMembersFields(testOrgAddress, noEmail, nil, emailOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(res.Duplicates, qt.HasLen, 0)
		c.Assert(hexes(res.MissingData), qt.ContentEquals, noEmail)
	})

	t.Run("pre-flight: whitespace-only auth field is missing data", func(t *testing.T) {
		c := qt.New(t)
		id := insertRaw(bson.M{"name": " ", "surname": "Smith", "email": "ws@example.com"})
		res, err := testDB.CheckMembersFields(testOrgAddress, []string{id},
			OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsSurname}, emailOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(hexes(res.MissingData), qt.DeepEquals, []string{id})
	})

	t.Run("build: clashing members with missing data are refused and nothing is written", func(t *testing.T) {
		c := qt.New(t)
		census := &Census{OrgAddress: testOrgAddress, TwoFaFields: emailOnly}
		_, err := testDB.PopulateGroupCensus(census, noEmailGroup)
		var clash *CensusMembersError
		c.Assert(errors.As(err, &clash), qt.IsTrue, qt.Commentf("got %v", err))
		// "A" and "B" hash identically; "  " is not folded by the hash, so "C" clashes with nobody
		c.Assert(hexes(clash.MissingData), qt.ContentEquals, noEmail[:2])
		c.Assert(clash.Duplicates, qt.HasLen, 0)

		stored, err := testDB.OrganizationMemberGroup(noEmailGroup, testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(stored.CensusIDs, qt.HasLen, 0)
		participants, err := testDB.CensusParticipants(census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 0)
	})

	t.Run("build: email-or-phone members with empty emails do not clash on the email hash", func(t *testing.T) {
		c := qt.New(t)
		ids := []string{
			insertRaw(bson.M{"name": "Sam", "email": "", "phone": []byte("phone-1")}),
			insertRaw(bson.M{"name": "Sam", "email": "", "phone": []byte("phone-2")}),
		}
		emailOrPhone := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone}
		auth := OrgMemberAuthFields{OrgMemberAuthFieldsName}
		groupID := group(ids...)

		res, err := testDB.CheckGroupMembersFields(testOrgAddress, groupID, auth, emailOrPhone)
		c.Assert(err, qt.IsNil)
		c.Assert(res.Duplicates, qt.HasLen, 0)
		c.Assert(res.MissingData, qt.HasLen, 0)

		census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: emailOrPhone}
		size, err := testDB.PopulateGroupCensus(census, groupID)
		c.Assert(err, qt.IsNil)
		c.Assert(size, qt.Equals, int64(2))
		participants, err := testDB.CensusParticipants(census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 2)
		for _, p := range participants {
			c.Assert(p.LoginHashEmail, qt.HasLen, 0)
			c.Assert(p.LoginHashPhone, qt.Not(qt.HasLen), 0)
		}
	})

	t.Run("pre-flight and build agree on complete members differing only by case", func(t *testing.T) {
		c := qt.New(t)
		ids := []string{
			insertRaw(bson.M{"name": "Anna", "email": "anna@example.com"}),
			insertRaw(bson.M{"name": "anna", "email": "ANNA@example.com"}),
		}
		auth := OrgMemberAuthFields{OrgMemberAuthFieldsName}
		groupID := group(ids...)

		res, err := testDB.CheckMembersFields(testOrgAddress, ids, auth, emailOnly)
		c.Assert(err, qt.IsNil)
		c.Assert(hexes(res.Duplicates), qt.ContentEquals, ids)
		c.Assert(res.Members, qt.HasLen, 0)

		census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: emailOnly}
		_, err = testDB.PopulateGroupCensus(census, groupID)
		var clash *CensusMembersError
		c.Assert(errors.As(err, &clash), qt.IsTrue, qt.Commentf("got %v", err))
		c.Assert(hexes(clash.Duplicates), qt.ContentEquals, ids)
		c.Assert(clash.MissingData, qt.HasLen, 0)
	})
}
