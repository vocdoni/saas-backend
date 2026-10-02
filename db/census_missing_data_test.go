package db

import (
	"context"
	"errors"
	"testing"

	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestClassifyCensusMembers pins how the census build splits its members: those missing auth data are
// left out, and complete members that could not be told apart (same login hash as another member or
// an existing participant) are duplicates.
func TestClassifyCensusMembers(t *testing.T) {
	type member struct {
		name, surname, nationalID, email string
		phone                            HashedPhone
	}
	nameNationalID := OrgMemberAuthFields{OrgMemberAuthFieldsName, OrgMemberAuthFieldsNationalID}
	emailOnly := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail}
	emailOrPhone := OrgMemberTwoFaFields{OrgMemberTwoFaFieldEmail, OrgMemberTwoFaFieldPhone}

	for _, tc := range []struct {
		name       string
		auth       OrgMemberAuthFields
		twoFa      OrgMemberTwoFaFields
		members    []member
		existing   []member // already participants of the census
		missing    []int    // indexes into members
		duplicates []int
	}{
		{
			name:  "no email under email 2FA",
			twoFa: emailOnly,
			members: []member{
				{name: "A", email: ""},
				{name: "B", email: "  "},
				{name: "C", email: "c@example.com"},
			},
			missing: []int{0, 1},
		},
		{
			name: "same auth field missing on two members is not a duplicate",
			auth: nameNationalID,
			members: []member{
				{name: "Twin", nationalID: ""},
				{name: "Twin", nationalID: " "},
				{name: "Twin", nationalID: "X1"},
			},
			missing: []int{0, 1},
		},
		{
			name:  "email-or-phone: a missing email with a phone is not missing data",
			auth:  OrgMemberAuthFields{OrgMemberAuthFieldsName},
			twoFa: emailOrPhone,
			members: []member{
				{name: "Sam", email: "", phone: HashedPhone("phone-1")},
				{name: "Sam", email: " ", phone: HashedPhone("phone-2")},
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
			duplicates: []int{0, 1},
		},
		{
			name:  "email-and-phone: same email, different phones share the email hash",
			auth:  OrgMemberAuthFields{OrgMemberAuthFieldsName},
			twoFa: emailOrPhone,
			members: []member{
				{name: "Sam", email: "sam@example.com", phone: HashedPhone("phone-1")},
				{name: "Sam", email: "sam@example.com", phone: HashedPhone("phone-2")},
			},
			duplicates: []int{0, 1},
		},
		{
			name:       "a member clashing with an existing participant",
			auth:       nameNationalID,
			members:    []member{{name: "Solo", nationalID: "X1"}, {name: "Other", nationalID: "X2"}},
			existing:   []member{{name: "solo", nationalID: "x1"}},
			duplicates: []int{0},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			census := Census{AuthFields: tc.auth, TwoFaFields: tc.twoFa}
			toMember := func(m member) OrgMember {
				return OrgMember{
					ID: bson.NewObjectID(), Name: m.name, Surname: m.surname, NationalID: m.nationalID,
					Email: m.email, Phone: m.phone,
				}
			}
			ids := make([]bson.ObjectID, len(tc.members))
			docs := make([]any, len(tc.members))
			for i, m := range tc.members {
				om := toMember(m)
				ids[i], docs[i] = om.ID, om
			}
			existing := make([]CensusParticipant, 0, len(tc.existing))
			for _, m := range tc.existing {
				om := toMember(m)
				hashes := calculateParticipantHashes(census, om)
				existing = append(existing, CensusParticipant{
					ParticipantID: om.ID.Hex(), LoginHash: hashes["loginHash"],
					LoginHashEmail: hashes["loginHashEmail"], LoginHashPhone: hashes["loginHashPhone"],
				})
			}
			pick := func(idx []int) []bson.ObjectID {
				out := make([]bson.ObjectID, 0, len(idx))
				for _, i := range idx {
					out = append(out, ids[i])
				}
				return out
			}

			cur, err := mongo.NewCursorFromDocuments(docs, nil, nil)
			c.Assert(err, qt.IsNil)
			cm, err := classifyCensusMembers(context.Background(), cur, census, existing)
			c.Assert(err, qt.IsNil)
			c.Assert(cm.missing, qt.DeepEquals, pick(tc.missing))
			c.Assert(cm.duplicates(), qt.DeepEquals, pick(tc.duplicates))
			c.Assert(cm.complete, qt.HasLen, len(tc.members)-len(tc.missing))
			for _, m := range cm.complete {
				// a whitespace-only email is no channel: no email login hash
				if _, ok := m.hashes["loginHashEmail"]; ok {
					c.Assert(tc.twoFa, qt.HasLen, 2)
				}
			}
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

	t.Run("build: members with missing data are left out and reported", func(t *testing.T) {
		c := qt.New(t)
		census := &Census{OrgAddress: testOrgAddress, TwoFaFields: emailOnly}
		size, missing, err := testDB.PopulateGroupCensus(census, noEmailGroup)
		c.Assert(err, qt.IsNil)
		c.Assert(size, qt.Equals, int64(0))
		c.Assert(hexes(missing), qt.ContentEquals, noEmail)
		participants, err := testDB.CensusParticipants(census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 0)
	})

	t.Run("build: a clash with an existing participant is refused and nothing is written", func(t *testing.T) {
		c := qt.New(t)
		auth := OrgMemberAuthFields{OrgMemberAuthFieldsName}
		first := insertRaw(bson.M{"name": "Bob", "email": "bob@example.com"})
		census := &Census{OrgAddress: testOrgAddress, AuthFields: auth, TwoFaFields: emailOnly}
		size, _, err := testDB.PopulateMembersCensus(census, []string{first})
		c.Assert(err, qt.IsNil)
		c.Assert(size, qt.Equals, int64(1))

		second := insertRaw(bson.M{"name": "BOB", "email": "Bob@example.com"})
		groupID := group(second)
		_, _, err = testDB.PopulateGroupCensus(census, groupID)
		var clash *CensusMembersError
		c.Assert(errors.As(err, &clash), qt.IsTrue, qt.Commentf("got %v", err))
		c.Assert(hexes(clash.Duplicates), qt.DeepEquals, []string{second})

		stored, err := testDB.OrganizationMemberGroup(groupID, testOrgAddress)
		c.Assert(err, qt.IsNil)
		c.Assert(stored.CensusIDs, qt.HasLen, 0)
		participants, err := testDB.CensusParticipants(census.ID.Hex())
		c.Assert(err, qt.IsNil)
		c.Assert(participants, qt.HasLen, 1)
	})

	t.Run("adding members by id skips those missing data", func(t *testing.T) {
		c := qt.New(t)
		census := &Census{OrgAddress: testOrgAddress, TwoFaFields: emailOnly}
		censusID, err := testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		complete := insertRaw(bson.M{"name": "Eve", "email": "eve@example.com"})
		added, memberErrs, err := testDB.AddCensusParticipantsByMemberIDs(censusID, []string{noEmail[0], complete})
		c.Assert(err, qt.IsNil)
		c.Assert(added, qt.Equals, 1)
		c.Assert(memberErrs, qt.HasLen, 1)
		c.Assert(memberErrs[0], qt.Contains, ErrMissingLoginData.Error())
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
		size, missing, err := testDB.PopulateGroupCensus(census, groupID)
		c.Assert(err, qt.IsNil)
		c.Assert(size, qt.Equals, int64(2))
		c.Assert(missing, qt.HasLen, 0)
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
		_, _, err = testDB.PopulateGroupCensus(census, groupID)
		var clash *CensusMembersError
		c.Assert(errors.As(err, &clash), qt.IsTrue, qt.Commentf("got %v", err))
		c.Assert(hexes(clash.Duplicates), qt.ContentEquals, ids)
	})

	t.Run("a member edited out of its login data leaves an inert participant", func(t *testing.T) {
		c := qt.New(t)
		member := &OrgMember{ID: bson.NewObjectID(), OrgAddress: testOrgAddress, Name: "Ann", Email: "ann@example.com"}
		memberID, err := testDB.SetOrgMember("salt", member)
		c.Assert(err, qt.IsNil)
		c.Assert(memberID, qt.Equals, member.ID.Hex())
		census := &Census{
			OrgAddress: testOrgAddress, AuthFields: OrgMemberAuthFields{OrgMemberAuthFieldsName}, TwoFaFields: emailOnly,
		}
		size, _, err := testDB.PopulateMembersCensus(census, []string{memberID})
		c.Assert(err, qt.IsNil)
		c.Assert(size, qt.Equals, int64(1))

		// a blank name normalizes to empty, clearing the stored one
		update := &OrgMemberUpdate{ID: member.ID, Name: new("   ")}
		_, _, err = testDB.UpsertOrgMemberAndCensusParticipants(&Organization{Address: testOrgAddress}, update, "salt")
		c.Assert(err, qt.IsNil)
		p, err := testDB.CensusParticipant(census.ID.Hex(), memberID)
		c.Assert(err, qt.IsNil)
		c.Assert(p.LoginHash, qt.HasLen, 0)
		c.Assert(p.LoginHashEmail, qt.HasLen, 0)
	})
}
