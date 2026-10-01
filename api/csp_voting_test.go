package api

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common/math"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// testCSPAuthenticateWithFields performs the CSP authentication flow for a member using the new multi-field system.
// It returns the verified auth token.
func testCSPAuthenticateWithFields(t *testing.T, pid string, authReq *handlers.AuthRequest) internal.HexBytes {
	t.Helper()
	c := qt.New(t)

	// Step 1: Initiate authentication (auth/0)
	authToken := postProcessAuth0(t, pid, authReq)

	// Step 2: Get the OTP code
	switch {
	case authReq.Email != "":
		mailBody := waitForEmail(t, authReq.Email)
		// Extract the OTP code from the email
		otpCode := extractOTPFromBody(mailBody)
		c.Assert(otpCode, qt.Not(qt.Equals), "", qt.Commentf("failed to extract OTP code from email"))
		t.Logf("Extracted OTP code: %s", otpCode)

		// Step 3: Verify authentication (auth/1)
		return postProcessAuth1(t, pid, &handlers.AuthChallengeRequest{
			AuthToken: authToken,
			AuthData:  []string{otpCode},
		})
	case authReq.Phone != "":
		smsBody := waitForSMS(t, authReq.Phone)
		otpCode := extractOTPFromBody(smsBody)
		c.Assert(otpCode, qt.Not(qt.Equals), "")

		// Step 3: Verify authentication (auth/1)
		return postProcessAuth1(t, pid, &handlers.AuthChallengeRequest{
			AuthToken: authToken,
			AuthData:  []string{otpCode},
		})
	default:
		// For auth-only cases, return the initial token
		return authToken
	}
}

// signAndMarshalTx signs a transaction with the given signer and marshals it to bytes.
// This is a helper function for the test cases.
func signAndMarshalTx(t *testing.T, tx *models.Tx, signer *ethereum.SignKeys) []byte {
	t.Helper()
	c := qt.New(t)
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)

	// Sign the transaction
	signature, err := signer.SignVocdoniTx(txBytes, "test")
	c.Assert(err, qt.IsNil)

	stx, err := proto.Marshal(&models.SignedTx{
		Tx:        txBytes,
		Signature: signature,
	})
	c.Assert(err, qt.IsNil)
	return stx
}

// TestCSPVoting tests the complete flow of publishing a process over a group-based census,
// then authenticating a member with the CSP, signing a vote, and casting it.
func TestCSPVoting(t *testing.T) {
	c := qt.New(t)
	var err error
	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateProvisionedOrganization(t, token)
	// premium: this test publishes seven processes, one per census variant
	setOrganizationSubscription(t, orgAddress, mockPremiumPlan.ID)

	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	// use the email OR sms for two-factor authentication
	twoFaFields := db.OrgMemberTwoFaFields{
		db.OrgMemberTwoFaFieldEmail,
		db.OrgMemberTwoFaFieldPhone,
	}

	// Generate test members with complete data for new authentication system
	members := []apicommon.OrgMember{
		{
			Name:         "John",
			Surname:      "Doe",
			MemberNumber: "P001",
			NationalID:   "12345678A",
			BirthDate:    "1990-01-01",
			Email:        "john.doe@example.com",
			Phone:        "612345601", // phone without country code should be handled gracefully
			Weight:       "1",
		},
		{
			Name:         "Jane",
			Surname:      "Smith",
			MemberNumber: "P002",
			NationalID:   "23456789B",
			BirthDate:    "1985-05-15",
			Email:        "jane.smith@example.com",
			Phone:        "+34612345602",
			Weight:       "1",
		},
		{
			Name:         "Alice",
			Surname:      "Johnson",
			MemberNumber: "P003",
			NationalID:   "34567890C",
			BirthDate:    "1992-10-22",
			Email:        "alice.johnson@example.com",
			Phone:        "+34612345603",
			Weight:       "1",
		},
		{
			Name:         "Bob",
			Surname:      "Williams",
			MemberNumber: "P004",
			NationalID:   "45678901D",
			BirthDate:    "1988-03-10",
			Email:        "bob.williams@example.com",
			Phone:        "+34612345604",
			Weight:       "1",
		},
		{
			Name:         "Charlie",
			Surname:      "Brown",
			MemberNumber: "P005",
			NationalID:   "56789012E",
			BirthDate:    "1995-12-03",
			Email:        "charlie.brown@example.com",
			Phone:        "+34612345605",
			Weight:       "1",
		},
		{
			Name:         "David",
			Surname:      "Garcia",
			MemberNumber: "", // Member without a memberNumber
			NationalID:   "67890123F",
			BirthDate:    "1993-07-25",
			Email:        "david.garcia@example.com",
			Phone:        "+34612345606",
			Weight:       "1",
		},
		{
			Name:         "Eva",
			Surname:      "Martinez",
			MemberNumber: "P007",
			NationalID:   "78901234G",
			BirthDate:    "1987-11-30",
			Email:        "", // Member without an email
			Phone:        "+34612345607",
			Weight:       "1",
		},
		{
			Name:         "Frank",
			Surname:      "Lopez",
			MemberNumber: "P008",
			NationalID:   "89012345H",
			BirthDate:    "1991-04-18",
			Email:        "frank.lopez@example.com",
			Phone:        "", // Member without a phone number
			Weight:       "1",
		},
		{
			Name:         "Grace",
			Surname:      "Gonzalez",
			MemberNumber: "P009",
			NationalID:   "90123456I",
			BirthDate:    "1989-09-09",
			Email:        "grace.gonzalez@example.com",
			Phone:        "+34612345609",
			// Weight not specified, should default to 1
		},
		{
			Name:         "Hannah",
			Surname:      "Wilson",
			MemberNumber: "P010",
			NationalID:   "01234567J",
			BirthDate:    "1994-06-14",
			Email:        "hannah.wilson@example.com",
			Phone:        "+34612345610",
			Weight:       "0", // Member with weight 0
		},
		{
			Name:         "MemberForSMS",
			Surname:      "Surname",
			MemberNumber: "P011",
			NationalID:   "12312312N",
			BirthDate:    "1991-01-11",
			Email:        "MemberForSMS@example.com",
			Phone:        "+34612312312",
			Weight:       "1",
		},
	}

	// Add members to the organization first
	postedOrgMembers := postOrgMembers(t, token, orgAddress, members...)

	// Fill in the IDs in the original members slice
	idMap := make(map[string]string, len(postedOrgMembers))
	for _, m := range postedOrgMembers {
		idMap[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = idMap[members[i].NationalID]
	}

	// Create a group with all the members
	createGroupReq := &apicommon.CreateOrganizationMemberGroupRequest{
		Title:       "CSP Voting Test Group",
		Description: "Group for testing CSP voting authentication",
		MemberIDs:   memberIDs(members),
	}

	groupResp := requestAndParse[apicommon.OrganizationMemberGroupInfo](
		t, http.MethodPost, token, createGroupReq,
		"organizations", orgAddress.String(), "groups")

	groupID := groupResp.ID
	t.Logf("Created member group with ID: %s", groupID)

	// every census variant below is built from the same group of members
	censusSpec := func(auth db.OrgMemberAuthFields, twoFa db.OrgMemberTwoFaFields, weighted bool,
	) apicommon.CensusSpec {
		return apicommon.CensusSpec{AuthFields: auth, TwoFaFields: twoFa, Weighted: weighted, GroupID: groupID}
	}
	pid, elections := publishCensusProcess(t, token, orgAddress, censusSpec(authFields, twoFaFields, true), 1)
	processID := elections[0]

	// Check census membership via the CSP token alone (the only voter
	// data the client stores), mirroring client.isInCensus() for CSP.
	t.Run("Check census membership", func(t *testing.T) {
		c := qt.New(t)

		// Grace (P009) is a verified participant of this process's census.
		grace := members[8]
		authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
			Name:         grace.Name,
			Surname:      grace.Surname,
			MemberNumber: grace.MemberNumber,
			Email:        grace.Email,
		})

		checkReq := &handlers.CheckMembershipRequest{AuthToken: authToken}
		check := func(req *handlers.CheckMembershipRequest) handlers.ProcessCheckResponse {
			return requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "", req, "processes", pid, "check")
		}

		// A verified token for this process whose user is in the census is eligible.
		resp := check(checkReq)
		c.Assert(resp.BelongsToProcess, qt.IsTrue)
		c.Assert(resp.Questions, qt.HasLen, 1)
		c.Assert(resp.Questions[0].HasVoted, qt.IsFalse)
		c.Assert(bytes.Equal(resp.Weight, big.NewInt(1).Bytes()), qt.IsTrue,
			qt.Commentf("unexpected weight %x", resp.Weight))

		// Once the member consumes the election, hasVoted flips to true.
		voter := ethereum.SignKeys{}
		c.Assert(voter.Generate(), qt.IsNil)
		c.Assert(testDB.ConsumeCSPProcess(authToken, processID,
			internal.HexBytes(voter.Address().Bytes())), qt.IsNil)
		resp = check(checkReq)
		c.Assert(resp.BelongsToProcess, qt.IsTrue)
		c.Assert(resp.Questions[0].HasVoted, qt.IsTrue)

		// The normal auth flow cannot mint a verified token for a non-participant, so seed one
		// directly: an org member outside the published group does not belong to the process.
		allMembers := postOrgMembers(t, token, orgAddress, apicommon.OrgMember{
			Name: "Out", Surname: "Sider", MemberNumber: "P999", NationalID: "OUTSIDER99",
			Email: "outsider@example.com", Phone: "+34612399999", Weight: "1",
		})
		var outsiderID string
		for _, m := range allMembers {
			if m.NationalID == "OUTSIDER99" {
				outsiderID = m.ID
			}
		}
		c.Assert(outsiderID, qt.Not(qt.Equals), "")
		outsiderTok := internal.HexBytes(internal.RandomBytes(16))
		c.Assert(testDB.SetCSPAuth(outsiderTok, internal.HexBytesFromString(outsiderID),
			internal.HexBytesFromString(pid), ""), qt.IsNil)
		c.Assert(testDB.VerifyCSPAuth(outsiderTok), qt.IsNil)
		c.Assert(check(&handlers.CheckMembershipRequest{AuthToken: outsiderTok}).BelongsToProcess, qt.IsFalse)

		// An unknown token is not a membership answer.
		requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, "",
			&handlers.CheckMembershipRequest{AuthToken: internal.HexBytes{0xde, 0xad, 0xbe, 0xef}},
			"processes", pid, "check")
	})

	t.Run("Authenticate with Email and Vote", func(t *testing.T) {
		orgBefore := getOrganization(t, orgAddress)

		testAuthenthicateAndVote(t, vocdoniClient, pid, processID, members[0].Weight,
			&handlers.AuthRequest{
				Name:         "John",
				Surname:      "Doe",
				MemberNumber: "P001",
				Email:        "john.doe@example.com",
			})

		// check only email counter is incremented
		orgAfter := getOrganization(t, orgAddress)
		qt.Assert(t, orgAfter.Counters.SentEmails, qt.Equals, orgBefore.Counters.SentEmails+1)
		qt.Assert(t, orgAfter.Counters.SentSMS, qt.Equals, orgBefore.Counters.SentSMS)
	})

	t.Run("Authenticate with SMS and Vote", func(t *testing.T) {
		orgBefore := getOrganization(t, orgAddress)

		testAuthenthicateAndVote(t, vocdoniClient, pid, processID, members[0].Weight,
			&handlers.AuthRequest{
				Name:         "MemberForSMS",
				Surname:      "Surname",
				MemberNumber: "P011",
				Phone:        "+34612312312",
			})

		// check only sms counter is incremented
		orgAfter := getOrganization(t, orgAddress)
		qt.Assert(t, orgAfter.Counters.SentEmails, qt.Equals, orgBefore.Counters.SentEmails)
		qt.Assert(t, orgAfter.Counters.SentSMS, qt.Equals, orgBefore.Counters.SentSMS+1)
	})

	// Create a voting key for the member
	t.Run("Update user weight", func(_ *testing.T) {
		// Create the voting address for the first user
		user1 := ethereum.SignKeys{}
		err = user1.Generate()
		c.Assert(err, qt.IsNil)

		member := members[7]

		// Authenticate the member with the CSP using the new multi-field system
		authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
			Name:         member.Name,
			Surname:      member.Surname,
			MemberNumber: member.MemberNumber,
			Email:        member.Email,
		})

		cspWeight := fetchCSPUserWeight(t, pid, authToken)

		weight, ok := math.ParseUint64(member.Weight)
		c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", member.Weight))
		c.Assert(
			bytes.Equal(cspWeight, big.NewInt(int64(weight)).Bytes()),
			qt.IsTrue,
			qt.Commentf(
				"CSP reported weight %d does not match expected weight %d",
				cspWeight, weight,
			),
		)

		toUpdate := member
		toUpdate.Phone = "" // getOrgMember returns a useless trimmed hash of the phone
		toUpdate.Weight = "10"
		putOrgMember(t, token, orgAddress, toUpdate)
		member = getOrgMember(t, token, orgAddress, member.ID)
		c.Assert(member.Weight, qt.Equals, "10")
		cspWeight = fetchCSPUserWeight(t, pid, authToken)
		c.Assert(
			bytes.Equal(cspWeight, big.NewInt(int64(10)).Bytes()),
			qt.IsTrue,
			qt.Commentf(
				"CSP reported weight %x does not match expected weight %d",
				cspWeight, 10,
			),
		)
	})

	// Test cases to try to break the authentication and voting mechanisms
	t.Run("Authentication Attack Vectors", func(_ *testing.T) {
		// Test case 1: Try to authenticate with invalid member number
		t.Run("Invalid Member Number", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				Name:         "John",
				Surname:      "Doe",
				MemberNumber: "INVALID",
				Email:        "john.doe@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusNotFound, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
		})

		// Test case 2: Try to authenticate with valid fields but wrong email
		t.Run("Wrong Email", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				Name:         "John",
				Surname:      "Doe",
				MemberNumber: "P001",
				Email:        "wrong.email@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusNotFound, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
		})

		// Test case 3: Try to authenticate with wrong name but correct other fields
		t.Run("Wrong Name", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				Name:         "Wrong",
				Surname:      "Doe",
				MemberNumber: "P001",
				Email:        "john.doe@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusNotFound, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
		})

		// Test case 4: Try to authenticate with wrong surname but correct other fields
		t.Run("Wrong Surname", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				Name:         "John",
				Surname:      "Wrong",
				MemberNumber: "P001",
				Email:        "john.doe@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusNotFound, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
		})

		// Test case 5: Try to authenticate with missing required auth fields
		t.Run("Missing Auth Fields", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				MemberNumber: "P001", // Missing name and surname which are required
				Email:        "john.doe@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusBadRequest,
				qt.Commentf("expected bad request for missing auth fields, got %d: %s", code, resp))
		})

		// Test case 6: Try to authenticate with missing contact information
		t.Run("Missing Contact Info", func(_ *testing.T) {
			authReq := &handlers.AuthRequest{
				Name:         "John",
				Surname:      "Doe",
				MemberNumber: "P001",
				// Missing both email and phone
			}
			resp, code := testRequest(t, http.MethodPost, "", authReq, "processes", pid, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusBadRequest,
				qt.Commentf("expected bad request for missing contact info, got %d: %s", code, resp))
		})

		// Test case 7: Try to verify with invalid OTP code
		t.Run("Invalid OTP Code", func(_ *testing.T) {
			orgBefore := getOrganization(t, orgAddress)

			// First get a valid auth token
			authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
				Name:         "Jane",
				Surname:      "Smith",
				MemberNumber: "P002",
				Email:        "jane.smith@example.com",
			})

			// Then try to verify with an invalid code
			authChallengeReq := &handlers.AuthChallengeRequest{
				AuthToken: authToken,
				AuthData:  []string{"123456"}, // Invalid code
			}
			resp, code := testRequest(t, http.MethodPost, "", authChallengeReq, "processes", pid, "auth", "1")
			c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))

			// check counter is anyway incremented, since OTP was sent
			orgAfter := getOrganization(t, orgAddress)
			qt.Assert(t, orgAfter.Counters.SentEmails, qt.Equals, orgBefore.Counters.SentEmails+1)
		})

		// Test case 8: Member without memberNumber doesn't disrupt authentication when not required
		t.Run("Member Without MemberNumber", func(_ *testing.T) {
			// Create a census without memberNumber in AuthFields
			noMemberNumAuthFields := db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsSurname,
			}
			emailTwoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

			noMemberNumPID, _ := publishCensusProcess(t, token, orgAddress, censusSpec(noMemberNumAuthFields, emailTwoFaFields, false), 1)

			// Should be able to authenticate David Garcia (who has no memberNumber) when memberNumber isn't required
			authReq := &handlers.AuthRequest{
				Name:    "David",
				Surname: "Garcia",
				Email:   "david.garcia@example.com",
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", noMemberNumPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

			// Now create a census that requires memberNumber
			withMemberNumAuthFields := db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsSurname,
				db.OrgMemberAuthFieldsMemberNumber,
			}

			withMemberNumPID, _ := publishCensusProcess(t, token, orgAddress,
				censusSpec(withMemberNumAuthFields, emailTwoFaFields, false), 1)

			// David Garcia has no memberNumber, so he cannot log in once the census requires it
			resp, code := testRequest(t, http.MethodPost, "", authReq,
				"processes", withMemberNumPID, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusBadRequest,
				qt.Commentf("expected bad request when memberNumber required but missing, got %d: %s", code, resp))
		})
	})

	// Test different authFields and twoFaFields combinations using group-based census
	t.Run("Multi-Field Authentication Tests", func(_ *testing.T) {
		// Test case 1: Auth-only census (no twoFa fields)
		t.Run("Auth Only Census", func(_ *testing.T) {
			authOnlyFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
			emptyTwoFaFields := db.OrgMemberTwoFaFields{}

			authOnlyPID, _ := publishCensusProcess(t, token, orgAddress, censusSpec(authOnlyFields, emptyTwoFaFields, false), 1)

			// Should be able to authenticate with just member number
			authReq := &handlers.AuthRequest{
				MemberNumber: "P001",
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", authOnlyPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "")

			// Should be able to verify immediately (no challenge)
			authChallengeReq := &handlers.AuthChallengeRequest{
				AuthToken: authResp.AuthToken,
				AuthData:  []string{},
			}
			verifyResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", authChallengeReq,
				"processes", authOnlyPID, "auth", "1")
			c.Assert(verifyResp.AuthToken, qt.Not(qt.Equals), "")
		})

		// Test case 2: SMS-only census
		t.Run("SMS Only Census", func(_ *testing.T) {
			smsAuthFields := db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsMemberNumber,
			}
			smsTwoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldPhone}

			smsPID, _ := publishCensusProcess(t, token, orgAddress, censusSpec(smsAuthFields, smsTwoFaFields, false), 1)

			// Should be able to authenticate with name, member number, and phone
			authReq := &handlers.AuthRequest{
				Name:         "John",
				MemberNumber: "P001",
				Phone:        "+34612345601",
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", smsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "")
		})

		// Test case 3: Complex auth fields combination
		t.Run("Complex Auth Fields", func(_ *testing.T) {
			complexAuthFields := db.OrgMemberAuthFields{
				db.OrgMemberAuthFieldsName,
				db.OrgMemberAuthFieldsSurname,
				db.OrgMemberAuthFieldsNationalID,
				db.OrgMemberAuthFieldsBirthDate,
			}
			emailTwoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

			complexPID, _ := publishCensusProcess(t, token, orgAddress, censusSpec(complexAuthFields, emailTwoFaFields, false), 1)

			// Should be able to authenticate with all required fields
			authReq := &handlers.AuthRequest{
				Name:       "John",
				Surname:    "Doe",
				NationalID: "12345678A",
				BirthDate:  "1990-01-01",
				Email:      "john.doe@example.com",
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", complexPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "")

			// Should fail if any required field is missing or wrong
			wrongAuthReq := &handlers.AuthRequest{
				Name:       "John",
				Surname:    "Doe",
				NationalID: "WRONG_ID",
				BirthDate:  "1990-01-01",
				Email:      "john.doe@example.com",
			}
			resp, code := testRequest(t, http.MethodPost, "", wrongAuthReq,
				"processes", complexPID, "auth", "0")
			c.Assert(code, qt.Equals, http.StatusNotFound,
				qt.Commentf("expected unauthorized for wrong national ID, got %d: %s", code, resp))
		})
	})

	t.Run("Voting Attack Vectors", func(_ *testing.T) {
		// Test case 4: Try to reuse an auth token for multiple processes
		t.Run("Reuse Auth Token", func(_ *testing.T) {
			// Create a second user
			user2 := ethereum.SignKeys{}
			err = user2.Generate()
			c.Assert(err, qt.IsNil)
			user2Addr := user2.Address().Bytes()

			// Authenticate user 3 using the new multi-field system
			authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
				Name:         "Alice",
				Surname:      "Johnson",
				MemberNumber: "P003",
				Email:        "alice.johnson@example.com",
			})

			// Sign the voter's address with the CSP
			signature := testCSPSign(t, pid, authToken, processID, user2Addr)

			// Get weight for user 2
			weight, ok := math.ParseUint64(members[2].Weight)
			c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[2].Weight))

			// Generate a vote proof with the signature
			proof := testGenerateVoteProof(processID, user2Addr, signature, weight)

			// Cast a vote
			votePackage := []byte("[\"0\"]") // Vote for option 0
			nullifier := testCastVote(t, vocdoniClient, &user2, processID, proof, votePackage)
			t.Logf("Vote cast successfully with nullifier: %x", nullifier)

			// Try to sign again with the same token (should fail)
			user3 := ethereum.SignKeys{}
			err = user3.Generate()
			c.Assert(err, qt.IsNil)
			user3Addr := user3.Address().Bytes()

			// Try to sign again with the same token
			signReq := &handlers.SignRequest{
				AuthToken: authToken,
				ProcessID: processID,
				Payload:   hex.EncodeToString(user3Addr),
			}
			resp, code := testRequest(t, http.MethodPost, "", signReq, "processes", pid, "sign")
			c.Assert(code, qt.Equals, http.StatusUnauthorized,
				qt.Commentf("expected unauthorized for reused token, got %d: %s", code, resp))
		})

		// Test case 5: Try to sign with a token from a different user
		t.Run("Token From Different User", func(_ *testing.T) {
			// Authenticate user 4 using the new multi-field system
			authToken := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
				Name:         "Bob",
				Surname:      "Williams",
				MemberNumber: "P004",
				Email:        "bob.williams@example.com",
			})

			// Create a user
			user4 := ethereum.SignKeys{}
			err = user4.Generate()
			c.Assert(err, qt.IsNil)
			user4Addr := user4.Address().Bytes()

			// Sign the voter's address with the CSP
			signature := testCSPSign(t, pid, authToken, processID, user4Addr)

			// Get weight for user 3
			weight, ok := math.ParseUint64(members[3].Weight)
			c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[3].Weight))

			// Generate a vote proof with the signature
			proof := testGenerateVoteProof(processID, user4Addr, signature, weight)

			// Cast a vote
			votePackage := []byte("[\"1\"]") // Vote for option 1
			nullifier := testCastVote(t, vocdoniClient, &user4, processID, proof, votePackage)
			t.Logf("Vote cast successfully with nullifier: %x", nullifier)

			// Now authenticate user 5 using the new multi-field system
			authToken5 := testCSPAuthenticateWithFields(t, pid, &handlers.AuthRequest{
				Name:         "Charlie",
				Surname:      "Brown",
				MemberNumber: "P005",
				Email:        "charlie.brown@example.com",
			})

			// Try to sign with user 5's token but for user 4's address
			// Note: The signature will be the same because the CSP signs the same data (processID + address)
			// regardless of which user is signing
			signature5 := testCSPSign(t, pid, authToken5, processID, user4Addr)

			// Get weight for user 4
			weight, ok = math.ParseUint64(members[4].Weight)
			c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[4].Weight))

			// Try to use user 5's signature with user 4's key (should fail)
			invalidProof := testGenerateVoteProof(processID, user4Addr, signature5, weight)

			// This should fail at the blockchain level because the signature doesn't match the address
			user4Copy := ethereum.SignKeys{}
			err = user4Copy.Generate()
			c.Assert(err, qt.IsNil)
			user4Copy.Private = user4.Private

			// Try to cast a vote with the invalid proof
			tx := models.Tx{
				Payload: &models.Tx_Vote{
					Vote: &models.VoteEnvelope{
						ProcessId:   processID,
						Nonce:       internal.RandomBytes(16),
						Proof:       invalidProof,
						VotePackage: []byte("[\"0\"]"),
					},
				},
			}

			// This should fail at the blockchain level
			_, _, err = vocdoniClient.SendTx(signAndMarshalTx(t, &tx, &user4Copy))
			c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("expected error for invalid signature"))
		})

		// Test case 6: Try to vote with a forged signature (should fail)
		t.Run("Forged Signature", func(_ *testing.T) {
			// Create a user
			user6 := ethereum.SignKeys{}
			err = user6.Generate()
			c.Assert(err, qt.IsNil)
			user6Addr := user6.Address().Bytes()

			// Create a forged signature (just random bytes)
			forgedSignature := internal.RandomBytes(65) // ECDSA signatures are 65 bytes

			// Generate a vote proof with the forged signature
			invalidProof := testGenerateVoteProof(processID, user6Addr, forgedSignature, 1)

			// Try to cast a vote with the invalid proof
			tx := models.Tx{
				Payload: &models.Tx_Vote{
					Vote: &models.VoteEnvelope{
						ProcessId:   processID,
						Nonce:       internal.RandomBytes(16),
						Proof:       invalidProof,
						VotePackage: []byte("[\"1\"]"),
					},
				},
			}

			// This should fail at the blockchain level
			_, _, err = vocdoniClient.SendTx(signAndMarshalTx(t, &tx, &user6))
			c.Assert(err, qt.Not(qt.IsNil), qt.Commentf("expected error for forged signature"))
		})
	})

	// Test case 4: Email and Phone both supported for 2FA
	t.Run("Email and Phone 2FA Census", func(_ *testing.T) {
		// Define auth fields and both email and phone for two-factor authentication
		authFields := db.OrgMemberAuthFields{
			db.OrgMemberAuthFieldsName,
			db.OrgMemberAuthFieldsMemberNumber,
		}
		bothTwoFaFields := db.OrgMemberTwoFaFields{
			db.OrgMemberTwoFaFieldEmail,
			db.OrgMemberTwoFaFieldPhone,
		}

		bothMethodsPID, _ := publishCensusProcess(t, token, orgAddress, censusSpec(authFields, bothTwoFaFields, true), 1)

		// Test 1: User with only email should be able to authenticate
		t.Run("User with Email Only", func(_ *testing.T) {
			// Use John Doe who has email but we'll only provide the email for 2FA
			authReq := &handlers.AuthRequest{
				Name:         "Frank",
				MemberNumber: "P008",
				Email:        "frank.lopez@example.com",
				// No phone provided
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))
		})

		// Test 2: User with only phone should be able to authenticate
		t.Run("User with Phone Only", func(_ *testing.T) {
			// Use Jane Smith who has phone but we'll only provide the phone for 2FA
			authReq := &handlers.AuthRequest{
				Name:         "Eva",
				MemberNumber: "P007",
				Phone:        "+34612345607",
				// No email provided
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))
		})

		// Test 3: User with both email and phone should be able to authenticate with either
		t.Run("User with Both Email and Phone", func(_ *testing.T) {
			// Use Alice Johnson who has both email and phone
			// First try with just email
			authReq := &handlers.AuthRequest{
				Name:         "John",
				MemberNumber: "P001",
				Email:        "john.doe@example.com",
				// No phone provided
			}
			authResp := requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

			// Then try with just phone
			authReq = &handlers.AuthRequest{
				Name:         "Jane",
				MemberNumber: "P002",
				Phone:        "+34612345602",
				// No email provided
			}
			authResp = requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

			// Finally try with both email and phone
			authReq = &handlers.AuthRequest{
				Name:         "Alice",
				MemberNumber: "P003",
				Email:        "alice.johnson@example.com",
				Phone:        "+34612345603",
			}
			authResp = requestAndParse[handlers.AuthResponse](t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))
		})
		// Test 4: User with zero weight should not be able to authenticate
		t.Run("User with Phone Only", func(_ *testing.T) {
			// Use Jane Smith who has phone but we'll only provide the phone for 2FA
			authReq := &handlers.AuthRequest{
				Name:         "Hannah",
				MemberNumber: "P010",
				Phone:        "+34612345610",
				// No email provided
			}
			err := requestAndExpectError(t,
				http.MethodPost, "", authReq,
				"processes", bothMethodsPID, "auth", "0")
			c.Assert(err, qt.ErrorIs, errors.ErrZeroWeightVoter)
		})
	})
}
