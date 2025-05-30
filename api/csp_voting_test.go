package api

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// signAndMarshalTx signs a transaction with the given signer and marshals it to bytes.
// This is a helper function for the test cases.
func signAndMarshalTx(t *testing.T, tx *models.Tx, signer *ethereum.SignKeys) []byte {
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
		resp, code := testRequest(t, http.MethodGet, token, nil, usersMeEndpoint)
		c.Assert(code, qt.Equals, http.StatusOK)
		t.Logf("User info: %s", resp)

		// Create a new vocdoni client
		vocdoniClient := testNewVocdoniClient(t)

		// Create an organization
		orgAddress := testCreateOrganization(t, token)
		t.Logf("Created organization with address: %s", orgAddress.String())

		// Subscribe the organization to a plan
		plans, err := testDB.Plans()
		c.Assert(err, qt.IsNil)
		c.Assert(len(plans), qt.Not(qt.Equals), 0)

		err = testDB.SetOrganizationSubscription(orgAddress.String(), &db.OrganizationSubscription{
			PlanID:          plans[0].ID,
			StartDate:       time.Now(),
			RenewalDate:     time.Now().Add(time.Hour * 24),
			LastPaymentDate: time.Now(),
			Active:          true,
			MaxCensusSize:   1000,
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
			resp, code = testRequest(t, http.MethodGet, token, nil, "organizations", orgAddress.String())
			c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("response: %s", resp))
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
				// Create a new census
				censusID := testCreateCensus(t, token, orgAddress, string(db.CensusTypeSMSorMail))

				// Generate test members
				members := testGenerateTestMembers(5) // Increased to 5 members for more test cases

				// Override email for easier testing
				members[0].Email = "john.doe@example.com"
				members[1].Email = "jane.smith@example.com"
				members[2].Email = "alice.johnson@example.com"
				members[3].Email = "bob.williams@example.com"
				members[4].Email = "charlie.brown@example.com"

				// Add members to the census
				testAddMembersToCensus(t, token, censusID, members)

				// Publish the census
				_, _ = testPublishCensus(t, token, censusID)

				// Create a bundle with the census and process
				bundleID, _ := testCreateBundle(t, token, censusID, [][]byte{processID})

				// Create a voting key for the member
				t.Run("Authenticate and Vote", func(_ *testing.T) {
					// Create the voting address for the first user
					user1 := ethereum.SignKeys{}
					err = user1.Generate()
					c.Assert(err, qt.IsNil)
					user1Addr := user1.Address().Bytes()

					// Authenticate the member with the CSP
					authToken := testCSPAuthenticate(t, bundleID, "P001", "john.doe@example.com")

					// Sign the voter's address with the CSP
					signature := testCSPSign(t, bundleID, authToken, processID, user1Addr)

					// Generate a vote proof with the signature
					proof := testGenerateVoteProof(processID, user1Addr, signature)

					// Cast a vote
					votePackage := []byte("[\"1\"]") // Vote for option 1
					nullifier := testCastVote(t, vocdoniClient, &user1, processID, proof, votePackage)
					t.Logf("Vote cast successfully with nullifier: %x", nullifier)

					// Verify the vote was counted
					votes, err := vocdoniClient.ElectionVoteCount(processID)
					c.Assert(err, qt.IsNil)
					c.Assert(votes, qt.Equals, uint32(1), qt.Commentf("expected 1 vote, got %d", votes))
				})

				// Test cases to try to break the authentication and voting mechanisms
				t.Run("Authentication Attack Vectors", func(_ *testing.T) {
					// Test case 1: Try to authenticate with invalid member ID
					t.Run("Invalid Member ID", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							MemberID: "INVALID",
							Email:    "john.doe@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 2: Try to authenticate with valid member ID but wrong email
					t.Run("Wrong Email", func(_ *testing.T) {
						authReq := &handlers.AuthRequest{
							MemberID: "P001",
							Email:    "wrong.email@example.com",
						}
						resp, code := testRequest(t, http.MethodPost, "", authReq, "process", "bundle", bundleID, "auth", "0")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
					})

					// Test case 3: Try to verify with invalid OTP code
					t.Run("Invalid OTP Code", func(_ *testing.T) {
						// First get a valid auth token
						authToken := testCSPAuthenticate(t, bundleID, "P002", "jane.smith@example.com")

						// Then try to verify with an invalid code
						authChallengeReq := &handlers.AuthChallengeRequest{
							AuthToken: authToken,
							AuthData:  []string{"123456"}, // Invalid code
						}
						resp, code := testRequest(t, http.MethodPost, "", authChallengeReq, "process", "bundle", bundleID, "auth", "1")
						c.Assert(code, qt.Equals, http.StatusUnauthorized, qt.Commentf("expected unauthorized, got %d: %s", code, resp))
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

						// Authenticate user 3
						authToken := testCSPAuthenticate(t, bundleID, "P003", "alice.johnson@example.com")

						// Sign the voter's address with the CSP
						signature := testCSPSign(t, bundleID, authToken, processID, user2Addr)

						// Generate a vote proof with the signature
						proof := testGenerateVoteProof(processID, user2Addr, signature)

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
						// Authenticate user 4
						authToken := testCSPAuthenticate(t, bundleID, "P004", "bob.williams@example.com")

						// Create a user
						user4 := ethereum.SignKeys{}
						err = user4.Generate()
						c.Assert(err, qt.IsNil)
						user4Addr := user4.Address().Bytes()

						// Sign the voter's address with the CSP
						signature := testCSPSign(t, bundleID, authToken, processID, user4Addr)

						// Generate a vote proof with the signature
						proof := testGenerateVoteProof(processID, user4Addr, signature)

						// Cast a vote
						votePackage := []byte("[\"1\"]") // Vote for option 1
						nullifier := testCastVote(t, vocdoniClient, &user4, processID, proof, votePackage)
						t.Logf("Vote cast successfully with nullifier: %x", nullifier)

						// Now authenticate user 5
						authToken5 := testCSPAuthenticate(t, bundleID, "P005", "charlie.brown@example.com")

						// Try to sign with user 5's token but for user 4's address
						// Note: The signature will be the same because the CSP signs the same data (processID + address)
						// regardless of which user is signing
						signature5 := testCSPSign(t, bundleID, authToken5, processID, user4Addr)

						// Try to use user 5's signature with user 4's key (should fail)
						invalidProof := testGenerateVoteProof(processID, user4Addr, signature5)

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
						invalidProof := testGenerateVoteProof(processID, user6Addr, forgedSignature)

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
			})
		})
	})
}
