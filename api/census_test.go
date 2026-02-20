package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

func TestCensus(t *testing.T) {
	c := qt.New(t)

	authFieldsNameAndMemberNumber := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName}

	// Set up test environment with user, org, and members
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	orgMembers := postOrgMembers(t, adminToken, orgAddress, newOrgMembers(2)...)

	// Test 1: Create a census
	censusID := postCensus(t, adminToken, orgAddress, authFieldsNameAndMemberNumber, twoFaEmail)

	// Verify the census was created correctly by retrieving it
	retrievedCensus := getCensus(t, adminToken, censusID)
	c.Assert(retrievedCensus.ID, qt.Equals, censusID)
	c.Assert(retrievedCensus.Type, qt.Equals, db.CensusTypeMail)
	c.Assert(retrievedCensus.OrgAddress, qt.Equals, orgAddress)

	// Test 1.3: Test with no authentication
	censusInfo := &apicommon.CreateCensusRequest{
		OrgAddress:  orgAddress,
		AuthFields:  authFieldsNameAndMemberNumber,
		TwoFaFields: db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail},
	}
	c.Assert(postCensusAndExpectError(t, "", censusInfo),
		qt.ErrorMatches, errors.ErrUnauthorized.Err.Error())

	// Test 1.4: Test with invalid organization address
	invalidCensusInfo := &apicommon.CreateCensusRequest{
		OrgAddress: common.Address{},
		AuthFields: authFieldsNameAndMemberNumber,
	}
	c.Assert(postCensusAndExpectError(t, adminToken, invalidCensusInfo),
		qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")

	// Test 2: Get census information
	// Test 2.1: Test with valid census ID (already tested above)

	// Test 2.2: Test with invalid census ID
	c.Assert(getCensusAndExpectError(t, adminToken, "invalid-id"),
		qt.ErrorMatches, errors.ErrMalformedURLParam.Err.Error()+".*")

	// Test 3: Add members to census
	// Test 3.1: Test with valid data (using the same members we added to the organization)
	censusMemberIDs := memberIDs(orgMembers)
	postCensusParticipants(t, adminToken, censusID, censusMemberIDs...)

	// Test 3.2: Test with no authentication
	c.Assert(postCensusParticipantsAndExpectError(t, "", censusID, censusMemberIDs...),
		qt.ErrorMatches, errors.ErrUnauthorized.Err.Error())

	// Test 3.3: Test with invalid census ID
	c.Assert(postCensusParticipantsAndExpectError(t, adminToken, "invalid-id", censusMemberIDs...),
		qt.ErrorMatches, errors.ErrMalformedURLParam.Err.Error()+".*")

	// Test 3.4: Test with empty members list
	postCensusParticipants(t, adminToken, censusID)

	// Test 3.5: Invalid member IDs are returned in response errors, not as HTTP errors
	invalidMembersResp := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{"invalid-member-id"}},
		censusEndpoint, censusID,
	)
	c.Assert(invalidMembersResp.Added, qt.Equals, uint32(0))
	c.Assert(invalidMembersResp.Errors, qt.HasLen, 1)
	c.Assert(invalidMembersResp.Errors[0], qt.Matches, ".*invalid-member-id.*")
	c.Assert(invalidMembersResp.Errors[0], qt.Matches, ".*"+errors.ErrInvalidData.Err.Error()+".*")

	// Test 3.6: Mixed valid/invalid IDs should partially add and report member-level errors
	uniqueNewMember := apicommon.OrgMember{
		MemberNumber: "P999",
		Name:         "Unique Test Member",
		Surname:      "Census",
		Email:        "unique-census-member@example.com",
		Phone:        "+34699999999",
		Password:     "password999",
		NationalID:   "DNI999",
		BirthDate:    "1980-12-31",
		Weight:       "1",
	}
	extraOrgMembers := postOrgMembers(t, adminToken, orgAddress, uniqueNewMember)
	existingIDs := make(map[string]bool, len(censusMemberIDs))
	for _, id := range censusMemberIDs {
		existingIDs[id] = true
	}
	validNewMemberID := ""
	for _, member := range extraOrgMembers {
		if !existingIDs[member.ID] {
			validNewMemberID = member.ID
			break
		}
	}
	c.Assert(validNewMemberID, qt.Not(qt.Equals), "")

	mixedMembersResp := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken,
		&apicommon.AddCensusParticipantsRequest{MemberIDs: []string{validNewMemberID, "invalid-member-id"}},
		censusEndpoint, censusID,
	)
	c.Assert(mixedMembersResp.Added, qt.Equals, uint32(1))
	c.Assert(mixedMembersResp.Errors, qt.HasLen, 1)
	c.Assert(mixedMembersResp.Errors[0], qt.Matches, ".*invalid-member-id.*")

	// Test 4: Publish census
	// Test 4.1: Test with valid data
	publishedCensus := requestAndParse[apicommon.PublishedCensusResponse](t, http.MethodPost, adminToken, nil,
		censusEndpoint, censusID, "publish")
	c.Assert(publishedCensus.URI, qt.Not(qt.Equals), "")
	c.Assert(publishedCensus.Root, qt.Not(qt.Equals), "")

	// Test 4.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "", nil, censusEndpoint, censusID, "publish")

	// Test 4.3: Test with invalid census ID
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, adminToken, nil, censusEndpoint, "invalid-id", "publish")

	// Test 5: Test with manager user permissions
	// Add members with duplicate member numbers to test validation
	duplicateMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 A",
				Email:        "duplicate7a@example.com",
				Phone:        "+34677777111",
				Password:     "password7a",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 B",
				Email:        "duplicate7b@example.com",
				Phone:        "+34677777222",
				Password:     "password7b",
			},
			{
				MemberNumber: "P007", // Same member number
				Name:         "Duplicate User7 C",
				Email:        "duplicate7c@example.com",
				Phone:        "+34677777333",
				Password:     "password7c",
			},
		},
	}

	// Add duplicate members to the organization
	postOrgMembers(t, adminToken, orgAddress, duplicateMembers.Members...)

	// Fetch updated organization members (needed for the server-side validation)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")

	// Test 6.1: Create a census with members having duplicate member numbers
	// Note: After simplification, duplicate validation has been removed from the handler
	postCensus(t, adminToken, orgAddress,
		db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsMemberNumber, // Has duplicates, but now accepted
		},
		db.OrgMemberTwoFaFields{},
	)

	// Test 7: Test census creation with empty auth field values
	// Add a member with empty email to test validation
	emptyFieldMember := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P008",
				Name:         "Empty Email User",
				Email:        "", // Empty email
				Phone:        "+34688888888",
				Password:     "password888",
			},
		},
	}

	// Add member with empty field to the organization
	postOrgMembers(t, adminToken, orgAddress, emptyFieldMember.Members...)

	// Fetch updated organization members (needed for the server-side validation)
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")

	// Test 7.1: Create a census with email twoFa field when some members have empty emails
	// Note: After simplification, empty field validation has been removed from the handler, so this is now accepted
	postCensus(t, adminToken, orgAddress, db.OrgMemberAuthFields{}, twoFaEmail)

	// Test 8: Create a user with manager role and test permissions
	// Create a second user
	managerToken := testCreateUser(t, "managerpassword123")

	managerUser := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	t.Logf("Manager user: %+v\n", managerUser)

	// Add the user as a manager to the organization
	// This would require implementing a helper to add a user to an organization with a specific role
	// For now, we'll skip this test as it would require additional API endpoints not covered in this test file

	// Test 9: Publish Group Census
	t.Run("PublishGroupCensus", func(t *testing.T) {
		c := qt.New(t)

		// Test 9.0: On free plan, creating a group census with OrgMemberTwoFaFieldPhone should fail
		c.Assert(createGroupBasedCensusAndExpectError(t, adminToken, orgAddress, authFieldsNameAndMemberNumber, twoFaPhone,
			memberIDs(orgMembers)...),
			qt.ErrorIs, errors.ErrProcessCensusSizeExceedsSMSAllowance)
		c.Assert(createGroupBasedCensusAndExpectError(t, adminToken, orgAddress, authFieldsNameAndMemberNumber, twoFaEmailOrPhone,
			memberIDs(orgMembers)...),
			qt.ErrorIs, errors.ErrProcessCensusSizeExceedsSMSAllowance)

		// After upgrading to a subscription, twoFaPhone or twoFaEmailOrPhone are now allowed
		setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
		createGroupBasedCensus(t, adminToken, orgAddress, authFieldsNameAndMemberNumber, twoFaPhone,
			memberIDs(orgMembers)...)

		// Test 9.1: Successful group census publication
		censusID, group, census := createGroupBasedCensus(t, adminToken, orgAddress, authFieldsNameAndMemberNumber, twoFaEmailOrPhone,
			memberIDs(orgMembers)...)

		// Verify that the census participants are correctly set
		participantsResp := requestAndParse[apicommon.CensusParticipantsResponse](
			t, http.MethodGet, adminToken, nil,
			censusEndpoint, censusID, "participants")
		c.Assert(participantsResp.MemberIDs, qt.HasLen, 2)
		c.Assert(participantsResp.MemberIDs[0], qt.Equals, orgMembers[0].ID)
		c.Assert(participantsResp.MemberIDs[1], qt.Equals, orgMembers[1].ID)

		// Test 9.2: Test with already published census
		// Publishing again should return the same information
		publishGroupRequest := &apicommon.PublishCensusGroupRequest{
			AuthFields: authFieldsNameAndMemberNumber, TwoFaFields: twoFaEmailOrPhone,
		}

		censusAgain := postGroupCensus(t, adminToken, censusID, group.ID, publishGroupRequest)
		c.Assert(censusAgain.URI, qt.Equals, census.URI)
		c.Assert(censusAgain.Root.String(), qt.Equals, census.Root.String())

		// Test 9.3: Test with no authentication
		c.Assert(postGroupCensusAndExpectError(t, "", censusID, group.ID, publishGroupRequest),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error())

		// Test 9.4: Test with invalid census ID
		c.Assert(postGroupCensusAndExpectError(t, adminToken, "invalid-id", group.ID, publishGroupRequest),
			qt.ErrorMatches, errors.ErrMalformedURLParam.Err.Error()+".*")

		// Test 9.5: Test with invalid group ID
		c.Assert(postGroupCensusAndExpectError(t, adminToken, censusID, "invalid-id", publishGroupRequest),
			qt.ErrorMatches, errors.ErrMalformedURLParam.Err.Error()+".*")

		// Test 9.6: Test with non-existent census
		nonExistentCensusID := "000000000000000000000000" // Valid format but doesn't exist
		c.Assert(postGroupCensusAndExpectError(t, adminToken, nonExistentCensusID, group.ID, publishGroupRequest),
			qt.ErrorMatches, errors.ErrCensusNotFound.Err.Error())

		// Test 9.7: Test with non-admin user
		// Create a third user who isn't admin of the organization
		nonAdminToken := testCreateUser(t, "nonadminpassword123")
		// Non-admin should not be able to publish group census
		c.Assert(postGroupCensusAndExpectError(t, nonAdminToken, censusID, group.ID, publishGroupRequest),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")
	})
}

func TestCensusSizeExceedsEmailAllowance(t *testing.T) {
	c := qt.New(t)

	// Set up test environment with user, org, and members
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	members := newOrgMembers(3)
	orgMembers := postOrgMembers(t, adminToken, orgAddress, members...)
	processID := randomProcessID()

	authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName}

	// reduce limit of freePlan to allow exactly orgMembers
	reducedFreePlan := *mockFreePlan
	reducedFreePlan.Features.TwoFaEmail = len(orgMembers)
	id, err := testDB.SetPlan(&reducedFreePlan)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, id, qt.Equals, reducedFreePlan.ID)

	censusID, _, _ := createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaEmail,
		memberIDs(orgMembers)...)

	// Create a bundle with the census and process
	bundleID, _ := postProcessBundle(t, adminToken, censusID, processID)

	// Authenticate N members to trigger email sendings and hit MaxSentEmails limit
	for _, member := range members {
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: member.MemberNumber,
			Name:         member.Name,
			Email:        member.Email,
		})
	}

	// Now creating a group census with twoFaEmail should fail
	c.Assert(createGroupBasedCensusAndExpectError(t, adminToken, orgAddress, authFields, twoFaEmail,
		memberIDs(orgMembers)...),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsEmailAllowance)

	// After upgrading to a subscription, twoFaPhone or twoFaEmailOrPhone are now allowed
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaPhone,
		memberIDs(orgMembers)...)
	createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaEmailOrPhone,
		memberIDs(orgMembers)...)
}

func TestCensusSizeExceedsSMSAllowance(t *testing.T) {
	c := qt.New(t)

	// Set up test environment with user, org, and members
	adminToken := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, adminToken)
	members := newOrgMembers(3)
	orgMembers := postOrgMembers(t, adminToken, orgAddress, members...)
	processID := randomProcessID()

	authFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber, db.OrgMemberAuthFieldsName}

	// reduce limit of freePlan to allow exactly orgMembers
	reducedFreePlan := *mockFreePlan
	reducedFreePlan.Features.TwoFaSms = len(orgMembers)
	id, err := testDB.SetPlan(&reducedFreePlan)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, id, qt.Equals, reducedFreePlan.ID)

	censusID, _, _ := createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaPhone,
		memberIDs(orgMembers)...)

	// Create a bundle with the census and process
	bundleID, _ := postProcessBundle(t, adminToken, censusID, processID)

	// Authenticate N members to trigger SMS sendings and hit MaxSentEmails limit
	for _, member := range members {
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: member.MemberNumber,
			Name:         member.Name,
			Phone:        member.Phone,
		})
	}

	// Now creating a group census with twoFaPhone should fail
	c.Assert(createGroupBasedCensusAndExpectError(t, adminToken, orgAddress, authFields, twoFaPhone,
		memberIDs(orgMembers)...),
		qt.ErrorIs, errors.ErrProcessCensusSizeExceedsSMSAllowance)

	// After upgrading to a subscription, twoFaPhone or twoFaEmailOrPhone are now allowed
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaPhone,
		memberIDs(orgMembers)...)
	createGroupBasedCensus(t, adminToken, orgAddress, authFields, twoFaEmailOrPhone,
		memberIDs(orgMembers)...)
}
