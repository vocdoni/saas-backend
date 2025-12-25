package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOrganizationMembers(t *testing.T) {
	c := qt.New(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create a user with admin permissions
	adminToken := testCreateUser(t, "adminpassword123")

	// Verify the token works
	user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, adminToken, nil, usersMeEndpoint)
	t.Logf("Admin user: %+v\n", user)

	// Create an organization
	orgAddress := testCreateOrganization(t, adminToken)

	// Test 1: Get organization members (initially empty)
	// Test 1.1: Test with valid organization address
	emptyMembersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(emptyMembersResponse.Members, qt.HasLen, 0, qt.Commentf("expected empty members list"))

	// Test 1.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, "", nil,
		"organizations", orgAddress.String(), "members")

	// Test 1.3: Test with invalid organization address
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodGet, adminToken, nil,
		"organizations", "invalid-address", "members")

	// Test 2: Add members to organization
	// Test 2.1: Test with valid data
	members := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P001",
				Name:         "John",
				Surname:      "Doe",
				NationalID:   "12345678A",
				BirthDate:    "1992-05-15",
				Email:        "john.doe@example.com",
				Phone:        "+34612345678",
				Password:     "password123",
				Weight:       "1",
				Other: map[string]any{
					"department": "Engineering",
					"age":        30,
				},
			},
			{
				MemberNumber: "P002",
				Name:         "Jane",
				Surname:      "Smith",
				NationalID:   "87654321B",
				BirthDate:    "1995-08-22",
				Email:        "jane.smith@example.com",
				Phone:        "+34698765432",
				Password:     "password456",
				Weight:       "0",
				Other: map[string]any{
					"department": "Marketing",
					"age":        28,
				},
			},
		},
	}

	addedResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, members,
		"organizations", orgAddress.String(), "members",
	)
	c.Assert(addedResponse.Added, qt.Equals, uint32(2))

	time.Sleep(2 * time.Second)

	// check that no email is received
	_, err := testMailService.FindEmail(ctx, user.Email)
	qt.Assert(t, err.Error(), qt.Equals, "EOF")

	// Test 2.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodPost, "", members,
		"organizations", orgAddress.String(), "members")

	// Test 2.3: Test with invalid organization address
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodPost, adminToken, members,
		"organizations", "invalid-address", "members")

	// Test 2.4: Test with empty members list
	emptyMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{},
	}
	emptyAddedResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, emptyMembers,
		"organizations", orgAddress.String(), "members",
	)
	c.Assert(emptyAddedResponse.Added, qt.Equals, uint32(0))

	// Test 2.5: Test with members missing some of the new optional fields
	// Generate a new test member ID
	pedroID := primitive.NewObjectID().Hex()
	joanID := primitive.NewObjectID().Hex()
	membersWithMissingFields := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P005",
				Name:         "Carlos",
				// Surname is missing
				NationalID: "99887766E",
				BirthDate:  "1985-07-10",
				Email:      "carlos@example.com",
				Phone:      "+34600111222",
				Password:   "password999",
				Weight:     "0",
				Other: map[string]any{
					"department": "Finance",
				},
			},
			{
				MemberNumber: "P006",
				Name:         "Maria",
				Surname:      "Garcia",
				// NationalID is missing
				BirthDate: "1992-11-25",
				Email:     "maria.garcia@example.com",
				Phone:     "+34600333444",
				Password:  "passwordxyz",
				Weight:    "1",
				Other: map[string]any{
					"department": "Legal",
				},
			},
			{
				ID: pedroID,
				// MemberNumber is missing
				Name:       "Pedro",
				Surname:    "Martinez",
				NationalID: "44556677F",
				BirthDate:  "invalid-birthdate",
				Email:      "invalid-email",
				Phone:      "invalid-phone",
				Password:   "passwordabc",
				Weight:     "2",
				Other: map[string]any{
					"department": "Operations",
				},
			},
			{
				ID:           joanID,
				MemberNumber: "P007",
				Name:         "Joan",
				Surname:      "Lopez",
				NationalID:   "22334455G",
				BirthDate:    "1990-03-15",
				Email:        "joan.lopez@example.com",
				Phone:        "+34600444555",
				Password:     "passworddef",
				// Weight is missing, should default to 1 and no error produced
				Other: map[string]any{
					"department": "IT",
				},
			},
		},
	}

	missingFieldsResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, membersWithMissingFields,
		"organizations", orgAddress.String(), "members")
	c.Assert(missingFieldsResponse.Added, qt.Equals, uint32(4))
	c.Assert(missingFieldsResponse.Errors, qt.HasLen, 3)
	c.Assert(missingFieldsResponse.Errors[0], qt.Matches, ".*invalid-email.*")
	c.Assert(missingFieldsResponse.Errors[1], qt.Matches, ".*invalid-phone.*")
	c.Assert(missingFieldsResponse.Errors[2], qt.Matches, ".*invalid-birthdate.*")

	// Test 3: Get organization members (now with added members)
	membersResponse := requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 6, qt.Commentf("expected 5 members (2 from Test 2.1 + 3 from Test 2.5)"))

	// Verify that members with missing fields were stored correctly
	// Find the member with missing surname (Carlos)
	var carlosFound, mariaFound, pedroFound, joanFound bool
	for _, member := range membersResponse.Members {
		if member.MemberNumber == "P005" {
			carlosFound = true
			c.Assert(member.Name, qt.Equals, "Carlos")
			c.Assert(member.Surname, qt.Equals, "") // Should be empty
			c.Assert(member.NationalID, qt.Equals, "99887766E")
			c.Assert(member.BirthDate, qt.Equals, "1985-07-10")
			c.Assert(member.Weight, qt.Equals, "0")
		}
		if member.MemberNumber == "P006" {
			mariaFound = true
			c.Assert(member.Name, qt.Equals, "Maria")
			c.Assert(member.Surname, qt.Equals, "Garcia")
			c.Assert(member.NationalID, qt.Equals, "") // Should be empty
			c.Assert(member.BirthDate, qt.Equals, "1992-11-25")
			c.Assert(member.Phone, qt.Not(qt.Equals), "+34600333444") // Should be hashed, not the original string
			c.Assert(member.Weight, qt.Equals, "1")
		}
		if member.ID == pedroID {
			pedroFound = true
			c.Assert(member.Name, qt.Equals, "Pedro")
			c.Assert(member.Surname, qt.Equals, "Martinez")
			c.Assert(member.NationalID, qt.Equals, "44556677F")
			c.Assert(member.MemberNumber, qt.Equals, "") // Should be empty
			c.Assert(member.Weight, qt.Equals, "2")
		}
		if member.ID == joanID {
			joanFound = true
			c.Assert(member.Name, qt.Equals, "Joan")
			c.Assert(member.Surname, qt.Equals, "Lopez")
			c.Assert(member.Weight, qt.Equals, "1") // Should default to 1
		}
	}
	c.Assert(carlosFound, qt.IsTrue, qt.Commentf("Carlos member should be found"))
	c.Assert(mariaFound, qt.IsTrue, qt.Commentf("Maria member should be found"))
	c.Assert(pedroFound, qt.IsTrue, qt.Commentf("Pedro member should be found"))
	c.Assert(joanFound, qt.IsTrue, qt.Commentf("Joan member should be found"))

	// Test 3.1: Get organization members (filtered)
	for _, test := range []struct {
		filter  string
		results int
	}{
		{filter: "?search=Maria", results: 1},       // Name
		{filter: "?search=artin", results: 1},       // Name
		{filter: "?search=Garcia", results: 1},      // Surname
		{filter: "?search=5566", results: 1},        // NationalID
		{filter: "?search=77", results: 2},          // NationalID
		{filter: "?search=1992", results: 2},        // BirthDate
		{filter: "?search=P00", results: 5},         // MemberNumber
		{filter: "?search=example.com", results: 5}, // Email
	} {
		resp, code := testRequest(t, http.MethodGet, adminToken, nil, "organizations", orgAddress.String(), "members", test.filter)
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
		c.Assert(json.Unmarshal(resp, &membersResponse), qt.IsNil)
		c.Assert(membersResponse.Members, qt.HasLen, test.results,
			qt.Commentf("expected %d result(s) for filter %q", test.results, test.filter))
	}

	// Test 4: Add members asynchronously
	// Test 4.1: Test with valid data (including some errors) and async=true
	asyncMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P003",
				Name:         "Bob",
				Surname:      "Johnson",
				NationalID:   "11223344C",
				BirthDate:    "1988-12-03",
				Email:        "bob.johnson@example.com",
				Phone:        "+34611223344",
				Password:     "password789",
				Other: map[string]any{
					"department": "Sales",
					"age":        35,
				},
			},
			{
				MemberNumber: "P004",
				Name:         "Alice",
				Surname:      "Brown",
				NationalID:   "55443322D",
				BirthDate:    "invalid-birthdate",
				Email:        "invalid-email",
				Phone:        "invalid-phone",
				Password:     "passwordabc",
				Other: map[string]any{
					"department": "HR",
					"age":        42,
				},
			},
		},
	}

	// Make the request with async=true
	asyncResponse := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, asyncMembers,
		"organizations", orgAddress.String(), "members?async=true")
	c.Assert(asyncResponse.JobID, qt.Not(qt.IsNil))
	c.Assert(asyncResponse.JobID, qt.HasLen, 16) // JobID should be 16 bytes

	// Convert the job ID to a hex string for the API call
	var jobIDHex internal.HexBytes
	jobIDHex.SetBytes(asyncResponse.JobID)
	t.Logf("Async job ID: %s\n", jobIDHex.String())

	// Test 5: Check the job progress
	var (
		maxAttempts = 30
		attempts    = 0
		completed   = false
	)

	// Poll the job status until it's complete or max attempts reached
	var jobStatus apicommon.AddMembersJobResponse
	for attempts < maxAttempts && !completed {
		jobStatus = requestAndParse[apicommon.AddMembersJobResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "members", "job", jobIDHex.String())

		t.Logf("Job progress: %d%%, Added: %d, Total: %d, Errors: %d\n",
			jobStatus.Progress, jobStatus.Added, jobStatus.Total, len(jobStatus.Errors))

		if jobStatus.Progress == 100 {
			completed = true
		} else {
			attempts++
			time.Sleep(100 * time.Millisecond) // Wait a bit before checking again
		}
	}

	// Verify the job completed successfully
	c.Assert(completed, qt.IsTrue, qt.Commentf("Job did not complete within expected time"))
	c.Assert(jobStatus.Added, qt.Equals, uint32(2)) // We added 2 members
	c.Assert(jobStatus.Total, qt.Equals, uint32(2))
	c.Assert(jobStatus.Progress, qt.Equals, uint32(100))
	c.Assert(jobStatus.Errors, qt.HasLen, 3)
	c.Assert(jobStatus.Errors[0], qt.Matches, ".*invalid-email.*")
	c.Assert(jobStatus.Errors[1], qt.Matches, ".*invalid-phone.*")
	c.Assert(jobStatus.Errors[2], qt.Matches, ".*invalid-birthdate.*")

	// Check that the completion email was sent
	mailBody := waitForEmail(t, user.Email)
	c.Assert(mailBody, qt.Matches, regexp.MustCompile(`(?i)\s(has been completed)\s`),
		qt.Commentf("mail content does not, got:\n%s", mailBody))

	// Test 5.1: Test jobs endpoint - basic functionality
	jobsResponse := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs")
	c.Assert(jobsResponse.Jobs, qt.HasLen, 1, qt.Commentf("expected 1 job (the org_members job)"))
	c.Assert(jobsResponse.Pagination.TotalItems, qt.Equals, int64(1))
	c.Assert(jobsResponse.Pagination.CurrentPage, qt.Equals, int64(1))

	// Verify the job details
	job := jobsResponse.Jobs[0]
	c.Assert(job.Type, qt.Equals, db.JobTypeOrgMembers)
	c.Assert(job.Total, qt.Equals, 2)
	c.Assert(job.Added, qt.Equals, 2)
	c.Assert(job.Completed, qt.IsTrue)
	c.Assert(job.CreatedAt.IsZero(), qt.IsFalse)
	c.Assert(job.CompletedAt.IsZero(), qt.IsFalse)
	c.Assert(job.JobID, qt.Equals, jobIDHex.String())
	c.Assert(job.Errors, qt.HasLen, 3) // Should have the validation errors
	t.Logf("Found org_members job: ID=%s, Type=%s, Total=%d, Added=%d, Completed=%t, Errors=%d",
		job.JobID, job.Type, job.Total, job.Added, job.Completed, len(job.Errors))

	// Test 5.2: Test jobs endpoint - pagination and filtering
	// Test with pagination
	jobsResponsePaged := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?page=1&limit=1")
	c.Assert(jobsResponsePaged.Jobs, qt.HasLen, 1)
	c.Assert(jobsResponsePaged.Pagination.TotalItems, qt.Equals, int64(1))
	c.Assert(jobsResponsePaged.Pagination.CurrentPage, qt.Equals, int64(1))

	// Test with job type filter for org_members
	jobsResponseFiltered := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=org_members")
	c.Assert(jobsResponseFiltered.Jobs, qt.HasLen, 1)
	c.Assert(jobsResponseFiltered.Jobs[0].Type, qt.Equals, db.JobTypeOrgMembers)

	// Test with different job type filter (should return empty)
	jobsResponseEmpty := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=census_participants")
	c.Assert(jobsResponseEmpty.Jobs, qt.HasLen, 0, qt.Commentf("should be empty for census_participants filter"))

	// Test 5.3: Test jobs endpoint - authorization and error cases
	// Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodGet, "", nil,
		"organizations", orgAddress.String(), "jobs")

	// Test with invalid organization address
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodGet, adminToken, nil,
		"organizations", "invalid-address", "jobs")

	// Test with invalid job type filter
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?type=invalid_type")

	// Test 5.4: Create another async job to test multiple jobs scenario
	anotherAsyncMembers := &apicommon.AddMembersRequest{
		Members: []apicommon.OrgMember{
			{
				MemberNumber: "P010",
				Name:         "Test",
				Surname:      "User10",
				Email:        "test10@example.com",
				Phone:        "+34600000010",
				Password:     "password10",
			},
		},
	}

	// Create second async job
	asyncResponse2 := requestAndParse[apicommon.AddMembersResponse](
		t, http.MethodPost, adminToken, anotherAsyncMembers,
		"organizations", orgAddress.String(), "members?async=true")
	c.Assert(asyncResponse2.JobID, qt.Not(qt.IsNil))

	// Convert the job ID to a hex string
	var jobIDHex2 internal.HexBytes
	jobIDHex2.SetBytes(asyncResponse2.JobID)
	t.Logf("Second async job ID: %s\n", jobIDHex2.String())

	// Wait for second job to complete
	completed2 := false
	for attempts := 0; attempts < maxAttempts && !completed2; attempts++ {
		jobStatus2 := requestAndParse[apicommon.AddMembersJobResponse](
			t, http.MethodGet, adminToken, nil,
			"organizations", orgAddress.String(), "members", "job", jobIDHex2.String())

		if jobStatus2.Progress == 100 {
			completed2 = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	c.Assert(completed2, qt.IsTrue, qt.Commentf("Second job did not complete within expected time"))

	// Test multiple jobs - should now have 2 jobs
	multipleJobsResponse := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs")
	c.Assert(multipleJobsResponse.Jobs, qt.HasLen, 2, qt.Commentf("expected 2 jobs"))
	c.Assert(multipleJobsResponse.Pagination.TotalItems, qt.Equals, int64(2))
	c.Assert(multipleJobsResponse.Pagination.PreviousPage, qt.IsNil)
	c.Assert(multipleJobsResponse.Pagination.CurrentPage, qt.Equals, int64(1))
	c.Assert(multipleJobsResponse.Pagination.NextPage, qt.IsNil)
	c.Assert(multipleJobsResponse.Pagination.LastPage, qt.Equals, int64(1))

	// Verify jobs are sorted by creation date (newest first)
	// The second job should be first in the list
	c.Assert(multipleJobsResponse.Jobs[0].JobID, qt.Equals, jobIDHex2.String())
	c.Assert(multipleJobsResponse.Jobs[1].JobID, qt.Equals, jobIDHex.String())

	// Test pagination with multiple jobs
	paginatedJobsResponse := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?page=1&limit=1")
	c.Assert(paginatedJobsResponse.Jobs, qt.HasLen, 1)
	c.Assert(paginatedJobsResponse.Pagination.TotalItems, qt.Equals, int64(2))
	c.Assert(paginatedJobsResponse.Pagination.PreviousPage, qt.IsNil)
	c.Assert(paginatedJobsResponse.Pagination.CurrentPage, qt.Equals, int64(1))
	c.Assert(*paginatedJobsResponse.Pagination.NextPage, qt.Equals, int64(2))
	c.Assert(paginatedJobsResponse.Pagination.LastPage, qt.Equals, int64(2))
	c.Assert(paginatedJobsResponse.Jobs[0].JobID, qt.Equals, jobIDHex2.String()) // Should be the newest job

	// Test second page
	paginatedJobsResponse2 := requestAndParse[apicommon.JobsResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "jobs?page=2&limit=1")
	c.Assert(paginatedJobsResponse2.Jobs, qt.HasLen, 1)
	c.Assert(paginatedJobsResponse2.Pagination.TotalItems, qt.Equals, int64(2))
	c.Assert(*paginatedJobsResponse2.Pagination.PreviousPage, qt.Equals, int64(1))
	c.Assert(paginatedJobsResponse2.Pagination.CurrentPage, qt.Equals, int64(2))
	c.Assert(paginatedJobsResponse2.Pagination.NextPage, qt.IsNil)
	c.Assert(paginatedJobsResponse2.Pagination.LastPage, qt.Equals, int64(2))
	c.Assert(paginatedJobsResponse2.Jobs[0].JobID, qt.Equals, jobIDHex.String()) // Should be the older job

	// Test 6: Get organization members with pagination
	// Test 6.1: Test with page=1 and limit=2
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members?page=1&limit=2")
	c.Assert(membersResponse.Members, qt.HasLen, 2, qt.Commentf("expected 2 members with pagination"))

	// Test 6.2: Test with page=2 and limit=2
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members?page=2&limit=2")
	c.Assert(membersResponse.Members, qt.HasLen, 2, qt.Commentf("expected 2 members on page 2"))

	// Test 7: Delete members
	// Test 7.1: Test with valid member IDs
	deleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{
			membersResponse.Members[0].ID,
			membersResponse.Members[1].ID,
		},
	}

	deleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, deleteRequest,
		"organizations", orgAddress.String(), "members")
	c.Assert(deleteResponse.Count, qt.Equals, 2, qt.Commentf("expected 2 members deleted"))

	// Test 7.2: Test with no authentication
	requestAndAssertCode(http.StatusUnauthorized,
		t, http.MethodDelete, "", deleteRequest,
		"organizations", orgAddress.String(), "members")

	// Test 7.3: Test with invalid organization address
	requestAndAssertCode(http.StatusBadRequest,
		t, http.MethodDelete, adminToken, deleteRequest,
		"organizations", "invalid-address", "members")

	// Test 7.4: Test with empty member IDs list
	emptyDeleteRequest := &apicommon.DeleteMembersRequest{
		IDs: []string{},
	}
	emptyDeleteResponse := requestAndParse[apicommon.DeleteMembersResponse](
		t, http.MethodDelete, adminToken, emptyDeleteRequest,
		"organizations", orgAddress.String(), "members")
	c.Assert(emptyDeleteResponse.Count, qt.Equals, 0, qt.Commentf("expected 0 members deleted"))

	// Test 8: Verify members were deleted by getting the list again
	membersResponse = requestAndParse[apicommon.OrganizationMembersResponse](
		t, http.MethodGet, adminToken, nil,
		"organizations", orgAddress.String(), "members")
	c.Assert(membersResponse.Members, qt.HasLen, 7, qt.Commentf("expected 7 members remaining (9 total - 2 deleted)"))
}

func TestUpsertOrganizationMember(t *testing.T) {
	c := qt.New(t)
	c.Run("MemberNumber+Email", func(c *qt.C) {
		loginToken := testCreateUser(t, "adminpassword123")
		orgAddress := testCreateOrganization(t, loginToken)
		members := newOrgMembers(3)
		c.Logf("will add org members: %+v", members)
		orgMembers := postOrgMembers(t, loginToken, orgAddress, members...)
		c.Logf("resulting org members: %+v", orgMembers)

		censusID, _, _ := createGroupBasedCensus(t, loginToken, orgAddress,
			db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsMemberNumber,
			}, db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
			},
			memberIDs(orgMembers)...)
		bundleID, _ := postProcessBundle(t, loginToken, censusID, randomProcessID())

		editedMember0 := orgMembers[0]
		editedMember0.Phone = "" // unset Phone field since it contains the trimmed hash returned by API
		editedMember0.Email = anotherEmail
		c.Logf("putting member: %+v", editedMember0)
		member0ID := putOrgMember(t, loginToken, orgAddress, editedMember0)
		member := getOrgMember(t, loginToken, orgAddress, member0ID)
		c.Logf("got member: %+v", member)

		// MemberNumber+Email cannot all be the same as another member,
		// but a subset of those is fine.
		// So first set Email (and even Name, btw) to be the same as another member, should work
		editedMember0.Email = members[1].Email
		editedMember0.Name = members[1].Name
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		// but now setting MemberNumber to also be the same is not allowed.
		editedMember0.MemberNumber = members[1].MemberNumber
		c.Logf("putting member: %+v", editedMember0)
		err := putOrgMemberAndExpectError(t, loginToken, orgAddress, editedMember0)
		c.Assert(err.Code, qt.Equals, errors.ErrInvalidData.Code)
		c.Assert(err, qt.ErrorMatches, ".*update would create duplicates.*")

		// creating a new member with those same details should succeed
		newMember := editedMember0
		newMember.ID = ""
		c.Logf("putting member: %+v", newMember)
		putOrgMember(t, loginToken, orgAddress, newMember)

		// updating another parameter of member0, like weight, should just work
		editedMember0.MemberNumber = members[0].MemberNumber
		editedMember0.Weight = "42"
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		// setting same Phone should be OK since it's not used in the census
		editedMember0.Phone = members[1].Phone
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		c.Logf("resulting member: %+v", getOrgMember(t, loginToken, orgAddress, member0ID))

		// Finally try authenticating, old values should fail
		c.Assert(postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{
			MemberNumber: members[0].MemberNumber,
			Email:        members[0].Email,
		}),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")

		// New values should work
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: editedMember0.MemberNumber,
			Email:        editedMember0.Email,
		})
	})

	c.Run("MemberNumber+Phone", func(c *qt.C) {
		loginToken := testCreateUser(t, "adminpassword123")
		orgAddress := testCreateOrganization(t, loginToken)
		members := newOrgMembers(3)
		c.Logf("will add org members: %+v", members)
		orgMembers := postOrgMembers(t, loginToken, orgAddress, members...)
		c.Logf("resulting org members: %+v", orgMembers)

		censusID, _, _ := createGroupBasedCensus(t, loginToken, orgAddress,
			db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsMemberNumber,
			}, db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldPhone,
			},
			memberIDs(orgMembers)...)

		bundleID, _ := postProcessBundle(t, loginToken, censusID, randomProcessID())

		// We had a weird bug regarding updating an OrgMember Email
		// of a participant of a census with OrgMemberTwoFaFieldPhone, prevented auth
		// so we wrote this test to catch regressions
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: members[0].MemberNumber,
			Phone:        members[0].Phone,
		})

		// wait and try authenticating again, should work
		time.Sleep(cspNotificationCoolDownTime)
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: members[0].MemberNumber,
			Phone:        members[0].Phone,
		})

		// The census is TwoFaFieldPhone, so editing the email should be harmless
		editedMember0 := orgMembers[0]
		editedMember0.Phone = "" // unset Phone field since it contains the trimmed hash returned by API
		editedMember0.Email = anotherEmail
		c.Logf("putting member: %+v", editedMember0)
		member0ID := putOrgMember(t, loginToken, orgAddress, editedMember0)
		member := getOrgMember(t, loginToken, orgAddress, member0ID)
		c.Logf("got member: %+v", member)

		// Now wait and try authenticating again, should work
		time.Sleep(cspNotificationCoolDownTime)
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			MemberNumber: members[0].MemberNumber,
			Phone:        members[0].Phone,
		})
	})

	c.Run("AllAuthAndTwoFaFields", func(c *qt.C) {
		loginToken := testCreateUser(t, "adminpassword123")
		orgAddress := testCreateOrganization(t, loginToken)
		members := newOrgMembers(3)
		c.Logf("will add org members: %+v", members)
		orgMembers := postOrgMembers(t, loginToken, orgAddress, members...)
		c.Logf("resulting org members: %+v", orgMembers)

		censusID, _, _ := createGroupBasedCensus(t, loginToken, orgAddress,
			db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsSurname,
				db.OrgMemberAuthFieldsMemberNumber,
				db.OrgMemberAuthFieldsNationalID,
				db.OrgMemberAuthFieldsBirthDate,
			}, db.OrgMemberTwoFaFields{
				db.OrgMemberTwoFaFieldEmail,
				db.OrgMemberTwoFaFieldPhone,
			},
			memberIDs(orgMembers)...)
		bundleID, _ := postProcessBundle(t, loginToken, censusID, randomProcessID())

		editedMember0 := orgMembers[0]
		editedMember0.Phone = "" // unset Phone field since it contains the trimmed hash returned by API

		// So first set all auth fields to be the same as another member, this should work
		editedMember0.Name = members[1].Name
		editedMember0.Surname = members[1].Surname
		editedMember0.MemberNumber = members[1].MemberNumber
		editedMember0.NationalID = members[1].NationalID
		editedMember0.BirthDate = members[1].BirthDate
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		// but now setting same Phone too, is not allowed.
		c.Run("SamePhone", func(c *qt.C) {
			editedMember0.Phone = members[1].Phone
			c.Logf("putting member: %+v", editedMember0)
			err := putOrgMemberAndExpectError(t, loginToken, orgAddress, editedMember0)
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidData.Code)
			c.Assert(err, qt.ErrorMatches, ".*update would create duplicates.*")
		})

		// setting same Email too, is also not allowed.
		c.Run("SameEmail", func(c *qt.C) {
			editedMember0.Phone = ""
			editedMember0.Email = members[1].Email
			c.Logf("putting member: %+v", editedMember0)
			err := putOrgMemberAndExpectError(t, loginToken, orgAddress, editedMember0)
			c.Assert(err.Code, qt.Equals, errors.ErrInvalidData.Code)
			c.Assert(err, qt.ErrorMatches, ".*update would create duplicates.*")
		})

		// creating a new member with those same details should succeed
		newMember := editedMember0
		newMember.ID = ""
		c.Logf("putting member: %+v", newMember)
		putOrgMember(t, loginToken, orgAddress, newMember)

		// updating another parameter of member0, like weight, should just work
		editedMember0.Phone = ""
		editedMember0.Email = ""
		editedMember0.Weight = "42"
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		// setting same Phone and Email should be OK as long as Name is different
		editedMember0.Name = members[0].Name
		editedMember0.Email = members[1].Email
		editedMember0.Phone = members[1].Phone
		c.Logf("putting member: %+v", editedMember0)
		putOrgMember(t, loginToken, orgAddress, editedMember0)

		// Finally try authenticating, old values should fail
		c.Assert(postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{
			Name:         members[0].Name,
			Surname:      members[0].Surname,
			MemberNumber: members[0].MemberNumber,
			NationalID:   members[0].NationalID,
			BirthDate:    members[0].BirthDate,

			Email: members[0].Email,
			Phone: members[0].Phone,
		}),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")
		c.Assert(postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{
			Name:         members[0].Name,
			Surname:      members[0].Surname,
			MemberNumber: members[0].MemberNumber,
			NationalID:   members[0].NationalID,
			BirthDate:    members[0].BirthDate,

			Phone: members[0].Phone,
		}),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")
		c.Assert(postProcessBundleAuth0AndExpectError(t, bundleID, &handlers.AuthRequest{
			Name:         members[0].Name,
			Surname:      members[0].Surname,
			MemberNumber: members[0].MemberNumber,
			NationalID:   members[0].NationalID,
			BirthDate:    members[0].BirthDate,

			Email: members[0].Email,
		}),
			qt.ErrorMatches, errors.ErrUnauthorized.Err.Error()+".*")

		// New values should work
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         editedMember0.Name,
			Surname:      editedMember0.Surname,
			MemberNumber: editedMember0.MemberNumber,
			NationalID:   editedMember0.NationalID,
			BirthDate:    editedMember0.BirthDate,

			Email: editedMember0.Email,
			Phone: editedMember0.Phone,
		})
		time.Sleep(cspNotificationCoolDownTime)
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         editedMember0.Name,
			Surname:      editedMember0.Surname,
			MemberNumber: editedMember0.MemberNumber,
			NationalID:   editedMember0.NationalID,
			BirthDate:    editedMember0.BirthDate,

			Email: editedMember0.Email,
		})
		time.Sleep(cspNotificationCoolDownTime)
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         editedMember0.Name,
			Surname:      editedMember0.Surname,
			MemberNumber: editedMember0.MemberNumber,
			NationalID:   editedMember0.NationalID,
			BirthDate:    editedMember0.BirthDate,

			Phone: editedMember0.Phone,
		})

		// As well as member[1], should be able to auth just fine
		testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
			Name:         members[1].Name,
			Surname:      members[1].Surname,
			MemberNumber: members[1].MemberNumber,
			NationalID:   members[1].NationalID,
			BirthDate:    members[1].BirthDate,
			Email:        members[1].Email,
			Phone:        members[1].Phone,
		})
	})
}
