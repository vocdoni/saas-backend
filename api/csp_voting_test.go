package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

// TestCSPVoting tests the complete flow of creating an organization, a process,
// a census with participants, and a bundle, then authenticating a participant
// with the CSP, signing a vote, and casting it.
func TestCSPVoting(t *testing.T) {
	c := qt.New(t)

	// Create a test user and organization
	t.Run("Setup Organization", func(t *testing.T) {
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
		t.Run("Create Account Transaction", func(t *testing.T) {
			// Build the create account transaction
			orgName := fmt.Sprintf("testorg-%d", internal.RandomInt(1000))
			orgInfoUri := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))

			nonce := uint32(0)
			tx := models.Tx{
				Payload: &models.Tx_SetAccount{
					SetAccount: &models.SetAccountTx{
						Nonce:   &nonce,
						Txtype:  models.TxType_CREATE_ACCOUNT,
						Account: orgAddress.Bytes(),
						Name:    &orgName,
						InfoURI: &orgInfoUri,
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
		t.Run("Create Process", func(t *testing.T) {
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
							Duration:      60,
							Status:        models.ProcessStatus_READY,
							CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
							CensusRoot:    cspPubKey,
							MaxCensusSize: 5,
							EnvelopeType: &models.EnvelopeType{
								Anonymous:      false,
								CostFromWeight: false,
							},
							VoteOptions: &models.ProcessVoteOptions{
								MaxCount: 1,
								MaxValue: 2,
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

			// Create a census and add participants
			t.Run("Create Census and Bundle", func(t *testing.T) {
				// Create a new census
				censusID := testCreateCensus(t, token, orgAddress, string(db.CensusTypeSMSorMail))

				// Generate test participants
				participants := testGenerateTestParticipants(2)

				// Override email for easier testing
				participants[0].Email = "john.doe@example.com"
				participants[1].Email = "jane.smith@example.com"

				// Add participants to the census
				testAddParticipantsToCensus(t, token, censusID, participants)

				// Publish the census
				_, _ = testPublishCensus(t, token, censusID)

				// Create a bundle with the census and process
				bundleID, _ := testCreateBundle(t, token, censusID, [][]byte{processID})

				// Create a voting key for the participant
				t.Run("Authenticate and Vote", func(t *testing.T) {
					// Create the voting address for the first user
					user1 := ethereum.SignKeys{}
					err = user1.Generate()
					c.Assert(err, qt.IsNil)
					user1Addr := user1.Address().Bytes()

					// Authenticate the participant with the CSP
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
			})
		})
	})
}
