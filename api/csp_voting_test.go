package api

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common/math"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// testCSPAuthenticateWithFields performs the CSP authentication flow for a member using the new multi-field system.
// It returns the verified auth token.
func testCSPAuthenticateWithFields(t *testing.T, bundleID string, authReq *handlers.AuthRequest) internal.HexBytes {
	t.Helper()
	c := qt.New(t)

	// Step 1: Initiate authentication (auth/0)
	authToken := postProcessBundleAuth0(t, bundleID, authReq)

	// Step 2: Get the OTP code from the email with retries (if email is provided)
	if authReq.Email != "" {
		mailBody := waitForEmail(t, authReq.Email)
		// Extract the OTP code from the email
		otpCode := extractOTPFromEmail(mailBody)
		c.Assert(otpCode, qt.Not(qt.Equals), "", qt.Commentf("failed to extract OTP code from email"))
		t.Logf("Extracted OTP code: %s", otpCode)

		// Step 3: Verify authentication (auth/1)
		return postProcessBundleAuth1(t, bundleID, &handlers.AuthChallengeRequest{
			AuthToken: authToken,
			AuthData:  []string{otpCode},
		})
	}

	// For auth-only cases, return the initial token
	return authToken
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

// TestCSPVoting tests the complete flow of creating an organization, a process,
// a census with members, and a bundle, then authenticating a member
// with the CSP, signing a vote, and casting it.
func TestCSPVoting(t *testing.T) {
	c := qt.New(t)

	// Create a test user and organization
	t.Run("Setup Organization", func(_ *testing.T) {
		// Create a user with admin permissions
		token := testCreateUser(t, "superpassword123")

		// Verify the token works
		user := requestAndParse[apicommon.UserInfo](t, http.MethodGet, token, nil, usersMeEndpoint)
		t.Logf("User: %+v\n", user)

		// Create a new vocdoni client
		vocdoniClient := testNewVocdoniClient(t)

		// Create an organization
		orgAddress := testCreateOrganization(t, token)

		// Subscribe the organization to a plan
		plans, err := testDB.Plans()
		c.Assert(err, qt.IsNil)
		c.Assert(len(plans) > 0, qt.IsTrue)

		err = testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
			PlanID:          plans[1].ID,
			StartDate:       time.Now(),
			RenewalDate:     time.Now().Add(time.Hour * 24),
			LastPaymentDate: time.Now(),
			Active:          true,
		})
		c.Assert(err, qt.IsNil)

		// Create the organization account on the blockchain
		t.Run("Create Account Transaction", func(_ *testing.T) {
			// Build the create account transaction
			orgName := fmt.Sprintf("testorg-%d", internal.RandomInt(1000))
			orgInfoURI := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))

			nonce := uint32(0)
			tx := models.Tx{
				Payload: &models.Tx_SetAccount{
					SetAccount: &models.SetAccountTx{
						Nonce:   &nonce,
						Txtype:  models.TxType_CREATE_ACCOUNT,
						Account: orgAddress.Bytes(),
						Name:    &orgName,
						InfoURI: &orgInfoURI,
					},
				},
			}

			// Send the transaction
			signRemoteSignerAndSendVocdoniTx(t, &tx, token, vocdoniClient, orgAddress)

			// Verify the organization was created
			requestAndAssertCode(http.StatusOK, t, http.MethodGet, token, nil, "organizations", orgAddress.String())
		})

		// Create a process for the organization
		t.Run("Create Process", func(_ *testing.T) {
			// Get the CSP public key
			cspPubKey, err := testCSP.PubKey()
			c.Assert(err, qt.IsNil)

			// Get the account nonce
			nonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)

			// Build the new process transaction
			tx := models.Tx{
				Payload: &models.Tx_NewProcess{
					NewProcess: &models.NewProcessTx{
						Txtype: models.TxType_NEW_PROCESS,
						Nonce:  nonce,
						Process: &models.Process{
							EntityId:      orgAddress.Bytes(),
							Duration:      120,
							Status:        models.ProcessStatus_READY,
							CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
							CensusRoot:    cspPubKey,
							MaxCensusSize: 10,
							EnvelopeType: &models.EnvelopeType{
								Anonymous:      false,
								CostFromWeight: false,
							},
							VoteOptions: &models.ProcessVoteOptions{
								MaxCount: 1,
								MaxValue: 5,
							},
							Mode: &models.ProcessMode{
								AutoStart:     true,
								Interruptible: true,
							},
						},
					},
				},
			}

			// Send the new process transaction
			processID := signRemoteSignerAndSendVocdoniTx(t, &tx, token, vocdoniClient, orgAddress)
			t.Logf("Created process with ID: %x", processID)

			// Create a census and add members
			t.Run("Create Census and Bundle", func(_ *testing.T) {
				// Create a new census with multiple authFields for comprehensive testing
				authFields := db.OrgMemberAuthFields{
					db.OrgMemberAuthFieldsName,
					db.OrgMemberAuthFieldsSurname,
					db.OrgMemberAuthFieldsMemberNumber,
				}
				// use the email for two-factor authentication
				twoFaFields := db.OrgMemberTwoFaFields{
					db.OrgMemberTwoFaFieldEmail,
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
						Weight:       strptr("1"),
					},
					{
						Name:         "Jane",
						Surname:      "Smith",
						MemberNumber: "P002",
						NationalID:   "23456789B",
						BirthDate:    "1985-05-15",
						Email:        "jane.smith@example.com",
						Phone:        "+34612345602",
						Weight:       strptr("1"),
					},
					{
						Name:         "Alice",
						Surname:      "Johnson",
						MemberNumber: "P003",
						NationalID:   "34567890C",
						BirthDate:    "1992-10-22",
						Email:        "alice.johnson@example.com",
						Phone:        "+34612345603",
						Weight:       strptr("1"),
					},
					{
						Name:         "Bob",
						Surname:      "Williams",
						MemberNumber: "P004",
						NationalID:   "45678901D",
						BirthDate:    "1988-03-10",
						Email:        "bob.williams@example.com",
						Phone:        "+34612345604",
						Weight:       strptr("1"),
					},
					{
						Name:         "Charlie",
						Surname:      "Brown",
						MemberNumber: "P005",
						NationalID:   "56789012E",
						BirthDate:    "1995-12-03",
						Email:        "charlie.brown@example.com",
						Phone:        "+34612345605",
						Weight:       strptr("1"),
					},
					{
						Name:         "David",
						Surname:      "Garcia",
						MemberNumber: "", // Member without a memberNumber
						NationalID:   "67890123F",
						BirthDate:    "1993-07-25",
						Email:        "david.garcia@example.com",
						Phone:        "+34612345606",
						Weight:       strptr("1"),
					},
					{
						Name:         "Eva",
						Surname:      "Martinez",
						MemberNumber: "P007",
						NationalID:   "78901234G",
						BirthDate:    "1987-11-30",
						Email:        "", // Member without an email
						Phone:        "+34612345607",
						Weight:       strptr("1"),
					},
					{
						Name:         "Frank",
						Surname:      "Lopez",
						MemberNumber: "P008",
						NationalID:   "89012345H",
						BirthDate:    "1991-04-18",
						Email:        "frank.lopez@example.com",
						Phone:        "", // Member without a phone number
						Weight:       strptr("1"),
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
						Weight:       strptr("0"), // Member with weight 0
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

				// Create an empty census (without adding members directly)
				censusID := postCensus(t, token, orgAddress, authFields, twoFaFields)

				// Publish the group-based census using the correct endpoint
				publishGroupRequest := &apicommon.PublishCensusGroupRequest{
					AuthFields:  authFields,
					TwoFaFields: twoFaFields,
					Weighted:    true,
				}

				publishedGroupCensus := requestAndParse[apicommon.PublishedCensusResponse](
					t, http.MethodPost, token, publishGroupRequest,
					"census", censusID, "group", groupID, "publish")

				t.Logf("Published group census with URI: %s", publishedGroupCensus.URI)

				// Create a bundle with the census and process
				bundleID, _ := postProcessBundle(t, token, censusID, processID)

				// Create a voting key for the member
				t.Run("Authenticate and Vote", func(_ *testing.T) {
					// Create the voting address for the first user
					user1 := ethereum.SignKeys{}
					err = user1.Generate()
					c.Assert(err, qt.IsNil)
					user1Addr := user1.Address().Bytes()

					// Authenticate the member with the CSP using the new multi-field system
					authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
						Name:         "John",
						Surname:      "Doe",
						MemberNumber: "P001",
						Email:        "john.doe@example.com",
					})

					cspWeight := getCSPUserWeight(t, bundleID, authToken)

					weight, ok := math.ParseUint64(*members[0].Weight)
					c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[0].Weight))
					c.Assert(
						bytes.Equal(cspWeight, big.NewInt(int64(weight)).Bytes()),
						qt.IsTrue,
						qt.Commentf(
							"CSP reported weight %d does not match expected weight %d",
							cspWeight, weight,
						),
					)

					// Sign the voter's address with the CSP
					signature := testCSPSign(t, bundleID, authToken, processID, user1Addr)

					// Generate a vote proof with the signature
					proof := testGenerateVoteProof(processID, user1Addr, signature, weight)

					// Cast a vote
					votePackage := []byte("[\"1\"]") // Vote for option 1
					nullifier := testCastVote(t, vocdoniClient, &user1, processID, proof, votePackage)
					t.Logf("Vote cast successfully with nullifier: %x", nullifier)

					// Verify the vote was counted
					votes, err := vocdoniClient.ElectionVoteCount(processID)
					c.Assert(err, qt.IsNil)
					c.Assert(votes, qt.Equals, uint32(1), qt.Commentf("expected 1 vote, got %d", votes))
				})

				// Create a voting key for the member
				t.Run("Update user weight", func(_ *testing.T) {
					// Create the voting address for the first user
					user1 := ethereum.SignKeys{}
					err = user1.Generate()
					c.Assert(err, qt.IsNil)

					member := members[7]

					// Authenticate the member with the CSP using the new multi-field system
					authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
						Name:         member.Name,
						Surname:      member.Surname,
						MemberNumber: member.MemberNumber,
						Email:        member.Email,
					})

					cspWeight := getCSPUserWeight(t, bundleID, authToken)

					weight, ok := math.ParseUint64(*member.Weight)
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
					toUpdate.Weight = strptr("10")
					putOrgMember(t, token, orgAddress, toUpdate)
					member = getOrgMember(t, token, orgAddress, member.ID)
					c.Assert(member.Weight, qt.DeepEquals, strptr("10"))
					cspWeight = getCSPUserWeight(t, bundleID, authToken)
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
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 2: Try to authenticate with valid fields but wrong email
					t.Run("Wrong Email", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							Name:         "John",
							Surname:      "Doe",
							MemberNumber: "P001",
							Email:        "wrong.email@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 3: Try to authenticate with wrong name but correct other fields
					t.Run("Wrong Name", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							Name:         "Wrong",
							Surname:      "Doe",
							MemberNumber: "P001",
							Email:        "john.doe@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 4: Try to authenticate with wrong surname but correct other fields
					t.Run("Wrong Surname", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							Name:         "John",
							Surname:      "Wrong",
							MemberNumber: "P001",
							Email:        "john.doe@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 5: Try to authenticate with missing required auth fields
					t.Run("Missing Auth Fields", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							MemberNumber: "P001", // Missing name and surname which are required
							Email:        "john.doe@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 6: Try to authenticate with missing contact information
					t.Run("Missing Contact Info", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							Name:         "John",
							Surname:      "Doe",
							MemberNumber: "P001",
							// Missing both email and phone
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusBadRequest,
							qt.Commentf("expected bad request for missing contact info, got %d: %s", code, resp))
					})

					// Test case 7: Try to verify with invalid OTP code
					t.Run("Invalid OTP Code", func(_ *testing.T) {
						// First get a valid auth token
						authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
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
						resp, code := testRequest(t, http.MethodPost, "", authChallengeReq, "process", "bundle", bundleID, "auth", "1")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 8: Member without memberNumber doesn't disrupt authentication when not required
					t.Run("Member Without MemberNumber", func(_ *testing.T) {
						// Create a census without memberNumber in AuthFields
						noMemberNumAuthFields := db.OrgMemberAuthFields{
							db.OrgMemberAuthFieldsName,
							db.OrgMemberAuthFieldsSurname,
						}
						emailTwoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

						// Create an empty census
						noMemberNumCensusID := postCensus(t, token, orgAddress, noMemberNumAuthFields, emailTwoFaFields)

						// Publish the group-based census using the existing group
						publishGroupRequest := &apicommon.PublishCensusGroupRequest{
							AuthFields:  noMemberNumAuthFields,
							TwoFaFields: emailTwoFaFields,
						}

						requestAndParse[apicommon.PublishedCensusResponse](
							t, http.MethodPost, token, publishGroupRequest,
							"census", noMemberNumCensusID, "group", groupID, "publish")

						noMemberNumBundleID, _ := postProcessBundle(t, token, noMemberNumCensusID, processID)

						// Should be able to authenticate David Garcia (who has no memberNumber) when memberNumber isn't required
						authReq := &handlers.AuthRequest{
							Name:    "David",
							Surname: "Garcia",
							Email:   "david.garcia@example.com",
						}
						authResp := requestAndParse[handlers.AuthResponse](t,
							http.MethodPost, "", authReq,
							"process", "bundle", noMemberNumBundleID, "auth", "0")
						c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))

						// Now create a census that requires memberNumber
						withMemberNumAuthFields := db.OrgMemberAuthFields{
							db.OrgMemberAuthFieldsName,
							db.OrgMemberAuthFieldsSurname,
							db.OrgMemberAuthFieldsMemberNumber,
						}

						validateGroupRequest := &apicommon.ValidateMemberGroupRequest{
							AuthFields:  withMemberNumAuthFields,
							TwoFaFields: emailTwoFaFields,
						}

						// Should fail to validate org member group data when memberNumber is required
						resp, code := testRequest(t, http.MethodPost, token, validateGroupRequest,
							"organizations", orgAddress.String(), "groups", groupID, "validate")
						c.Assert(code, qt.Equals, http.StatusBadRequest,
							qt.Commentf("expected bad request when memberNumber required but missing, got %d: %s", code, resp))

						// Should be able to create census even when memberNumber is required but missing
						resp, code = testRequest(t, http.MethodPost, token, publishGroupRequest,
							"census", noMemberNumCensusID, "group", groupID, "publish")
						c.Assert(code, qt.Equals, http.StatusOK,
							qt.Commentf("expected bad request when memberNumber required but missing, got %d: %s", code, resp))

						// Create an empty census
						withMemberNumCensusID := postCensus(t, token, orgAddress, withMemberNumAuthFields, emailTwoFaFields)

						// Publish the group-based census
						publishGroupRequest = &apicommon.PublishCensusGroupRequest{
							AuthFields:  withMemberNumAuthFields,
							TwoFaFields: emailTwoFaFields,
						}

						requestAndParse[apicommon.PublishedCensusResponse](
							t, http.MethodPost, token, publishGroupRequest,
							"census", withMemberNumCensusID, "group", groupID, "publish")

						withMemberNumBundleID, _ := postProcessBundle(t, token, withMemberNumCensusID, processID)

						// Should not fail to authenticate David Garcia when memberNumber is required
						resp, code = testRequest(t, http.MethodPost, "", authReq,
							"process", "bundle", withMemberNumBundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusOK,
							qt.Commentf("expected unauthorized when memberNumber required but not provided, got %d: %s", code, resp))
					})
				})

				// Test different authFields and twoFaFields combinations using group-based census
				t.Run("Multi-Field Authentication Tests", func(_ *testing.T) {
					// Test case 1: Auth-only census (no twoFa fields)
					t.Run("Auth Only Census", func(_ *testing.T) {
						authOnlyFields := db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
						emptyTwoFaFields := db.OrgMemberTwoFaFields{}

						// Create an empty census
						authOnlyCensusID := postCensus(t, token, orgAddress, authOnlyFields, emptyTwoFaFields)

						// Publish the group-based census using the existing group
						publishGroupRequest := &apicommon.PublishCensusGroupRequest{
							AuthFields:  authOnlyFields,
							TwoFaFields: emptyTwoFaFields,
						}

						requestAndParse[apicommon.PublishedCensusResponse](
							t, http.MethodPost, token, publishGroupRequest,
							"census", authOnlyCensusID, "group", groupID, "publish")

						authOnlyBundleID, _ := postProcessBundle(t, token, authOnlyCensusID, processID)

						// Should be able to authenticate with just member number
						authReq := &handlers.AuthRequest{
							MemberNumber: "P001",
						}
						authResp := requestAndParse[handlers.AuthResponse](t,
							http.MethodPost, "", authReq,
							"process", "bundle", authOnlyBundleID, "auth", "0")
						c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "")

						// Should be able to verify immediately (no challenge)
						authChallengeReq := &handlers.AuthChallengeRequest{
							AuthToken: authResp.AuthToken,
							AuthData:  []string{},
						}
						verifyResp := requestAndParse[handlers.AuthResponse](t, http.MethodPost, "", authChallengeReq,
							"process", "bundle", authOnlyBundleID, "auth", "1")
						c.Assert(verifyResp.AuthToken, qt.Not(qt.Equals), "")
					})

					// Test case 2: SMS-only census
					t.Run("SMS Only Census", func(_ *testing.T) {
						smsAuthFields := db.OrgMemberAuthFields{
							db.OrgMemberAuthFieldsName,
							db.OrgMemberAuthFieldsMemberNumber,
						}
						smsTwoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldPhone}

						// Create an empty census
						smsCensusID := postCensus(t, token, orgAddress, smsAuthFields, smsTwoFaFields)

						// Publish the group-based census using the existing group
						publishGroupRequest := &apicommon.PublishCensusGroupRequest{
							AuthFields:  smsAuthFields,
							TwoFaFields: smsTwoFaFields,
						}

						requestAndParse[apicommon.PublishedCensusResponse](
							t, http.MethodPost, token, publishGroupRequest,
							"census", smsCensusID, "group", groupID, "publish")

						smsBundleID, _ := postProcessBundle(t, token, smsCensusID, processID)

						// Should be able to authenticate with name, member number, and phone
						authReq := &handlers.AuthRequest{
							Name:         "John",
							MemberNumber: "P001",
							Phone:        "+34612345601",
						}
						authResp := requestAndParse[handlers.AuthResponse](t,
							http.MethodPost, "", authReq,
							"process", "bundle", smsBundleID, "auth", "0")
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

						// Create an empty census
						complexCensusID := postCensus(t, token, orgAddress, complexAuthFields, emailTwoFaFields)

						// Publish the group-based census using the existing group
						publishGroupRequest := &apicommon.PublishCensusGroupRequest{
							AuthFields:  complexAuthFields,
							TwoFaFields: emailTwoFaFields,
						}

						requestAndParse[apicommon.PublishedCensusResponse](
							t, http.MethodPost, token, publishGroupRequest,
							"census", complexCensusID, "group", groupID, "publish")

						complexBundleID, _ := postProcessBundle(t, token, complexCensusID, processID)

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
							"process", "bundle", complexBundleID, "auth", "0")
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
							"process", "bundle", complexBundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized,
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
						authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
							Name:         "Alice",
							Surname:      "Johnson",
							MemberNumber: "P003",
							Email:        "alice.johnson@example.com",
						})

						// Sign the voter's address with the CSP
						signature := testCSPSign(t, bundleID, authToken, processID, user2Addr)

						// Get weight for user 2
						weight, ok := math.ParseUint64(*members[2].Weight)
						c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[2].Weight))

						// Generate a vote proof with the signature
						proof := testGenerateVoteProof(processID, user2Addr, signature, weight)

						// Cast a vote
						votePackage := []byte("[\"2\"]") // Vote for option 2
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
						resp, code := testRequest(t, http.MethodPost, "", signReq, "process", "bundle", bundleID, "sign")
						c.Assert(code, qt.Equals, http.StatusUnauthorized,
							qt.Commentf("expected unauthorized for reused token, got %d: %s", code, resp))
					})

					// Test case 5: Try to sign with a token from a different user
					t.Run("Token From Different User", func(_ *testing.T) {
						// Authenticate user 4 using the new multi-field system
						authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
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
						signature := testCSPSign(t, bundleID, authToken, processID, user4Addr)

						// Get weight for user 3
						weight, ok := math.ParseUint64(*members[3].Weight)
						c.Assert(ok, qt.IsTrue, qt.Commentf("Failed to convert member weight %s to int", members[3].Weight))

						// Generate a vote proof with the signature
						proof := testGenerateVoteProof(processID, user4Addr, signature, weight)

						// Cast a vote
						votePackage := []byte("[\"1\"]") // Vote for option 1
						nullifier := testCastVote(t, vocdoniClient, &user4, processID, proof, votePackage)
						t.Logf("Vote cast successfully with nullifier: %x", nullifier)

						// Now authenticate user 5 using the new multi-field system
						authToken5 := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
							Name:         "Charlie",
							Surname:      "Brown",
							MemberNumber: "P005",
							Email:        "charlie.brown@example.com",
						})

						// Try to sign with user 5's token but for user 4's address
						// Note: The signature will be the same because the CSP signs the same data (processID + address)
						// regardless of which user is signing
						signature5 := testCSPSign(t, bundleID, authToken5, processID, user4Addr)

						// Get weight for user 4
						weight, ok = math.ParseUint64(*members[4].Weight)
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
									VotePackage: []byte("[\"2\"]"),
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

					// Create a new census with both email and phone as 2FA methods
					bothMethodsCensusID := postCensus(t, token, orgAddress, authFields, bothTwoFaFields)

					// Publish the group-based census using the existing group
					publishGroupRequest := &apicommon.PublishCensusGroupRequest{
						AuthFields:  authFields,
						TwoFaFields: bothTwoFaFields,
					}

					requestAndParse[apicommon.PublishedCensusResponse](
						t, http.MethodPost, token, publishGroupRequest,
						"census", bothMethodsCensusID, "group", groupID, "publish")

					bothMethodsBundleID, _ := postProcessBundle(t, token, bothMethodsCensusID, processID)

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
							"process", "bundle", bothMethodsBundleID, "auth", "0")
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
							"process", "bundle", bothMethodsBundleID, "auth", "0")
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
							"process", "bundle", bothMethodsBundleID, "auth", "0")
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
							"process", "bundle", bothMethodsBundleID, "auth", "0")
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
							"process", "bundle", bothMethodsBundleID, "auth", "0")
						c.Assert(authResp.AuthToken, qt.Not(qt.Equals), "", qt.Commentf("auth token should not be empty"))
					})
				})
			})
		})
	})
}
