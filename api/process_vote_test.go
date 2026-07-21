package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// waitForElectionStatus polls the chain until the election reports one of the
// accepted status strings (e.g. "PAUSED") or the retries are exhausted. Several
// values are accepted because an ended election is auto-advanced to "RESULTS"
// by the chain once its results are tallied.
func waitForElectionStatus(t *testing.T, address internal.HexBytes, accepted ...string) {
	t.Helper()
	c := qt.New(t)
	client := testNewVocdoniClient(t)
	var lastStatus string
	for i := 0; i < 20; i++ {
		election, err := client.Election(types.HexBytes(address))
		if err == nil {
			lastStatus = election.Status
			for _, s := range accepted {
				if lastStatus == s {
					return
				}
			}
		}
		time.Sleep(time.Second)
	}
	c.Fatalf("election %s never reached status %v (last seen %q)", address.String(), accepted, lastStatus)
}

// TestProcessStatusLifecycle publishes a draft as an on-chain election and then
// drives it through paused → ready → ended via the status endpoint, asserting the
// chain reflects each transition.
func TestProcessStatusLifecycle(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "password123")

	// create an org with eager on-chain account provisioning
	orgInfo := &apicommon.CreateOrganizationRequest{
		OrganizationInfo: apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://status-%d.com", internal.RandomInt(100000)),
		},
		ProvisionAccount: true,
	}
	org := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
	orgAddress := org.Address
	c.Assert(orgAddress, qt.Not(qt.Equals), common.Address{})

	// subscribe to a plan so NEW_PROCESS permission passes
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(24 * time.Hour),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// seed a draft with election params (interruptible so the status can change)
	draftID, err := testDB.SetProcess(&db.Process{
		OrgAddress: orgAddress,
		ElectionParams: &db.ElectionParams{
			Title:         db.MultiLangString{"default": "Status lifecycle election"},
			EndDate:       time.Now().Add(2 * time.Hour),
			MaxCensusSize: 100,
			Questions: []db.Question{{
				Title: db.MultiLangString{"default": "Question 1"},
				Choices: []db.Choice{
					{Title: db.MultiLangString{"default": "Yes"}, Value: 0},
					{Title: db.MultiLangString{"default": "No"}, Value: 1},
				},
			}},
			VoteType:     db.VoteType{MaxCount: 1, MaxValue: 1},
			ElectionType: db.ElectionType{Autostart: true, Interruptible: true},
		},
	})
	c.Assert(err, qt.IsNil)
	c.Assert(draftID.IsZero(), qt.IsFalse)

	// publish (async) to obtain the on-chain process address
	pubJob := enqueueAndPollJob(t, http.MethodPost, token, nil, "process", draftID.Hex(), "publish")
	c.Assert(pubJob.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", pubJob.Errors))
	c.Assert(len(pubJob.Result.Address) > 0, qt.IsTrue)
	c.Assert(pubJob.Result.Status, qt.Equals, "READY")
	addr := pubJob.Result.Address

	// drive the status transitions and assert the chain reflects each change. An
	// ended election is auto-advanced to "RESULTS" by the chain once tallied, so
	// the terminal transition accepts either.
	transitions := []struct {
		request     string
		respStatus  string
		chainStatus []string
	}{
		{"paused", "PAUSED", []string{"PAUSED"}},
		{"ready", "READY", []string{"READY"}},
		{"ended", "ENDED", []string{"ENDED", "RESULTS"}},
	}
	for _, tr := range transitions {
		job := enqueueAndPollJob(t, http.MethodPut, token,
			&apicommon.SetProcessStatusRequest{Status: tr.request}, "process", draftID.Hex(), "status")
		c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", job.Errors))
		c.Assert(job.Result.Status, qt.Equals, tr.respStatus)
		waitForElectionStatus(t, addr, tr.chainStatus...)
	}
}

// testRelayVoteRequest signs a vote tx, wraps it as a SignedTx, posts it to
// POST /vote, and returns the relayed vote nullifier.
func testRelayVoteRequest(t *testing.T, signer *ethereum.SignKeys, processID internal.HexBytes,
	proof *models.Proof, votePackage, memo []byte,
) internal.HexBytes {
	t.Helper()
	c := qt.New(t)
	tx := &models.Tx{Payload: &models.Tx_Vote{Vote: &models.VoteEnvelope{
		ProcessId: processID.Bytes(), Nonce: internal.RandomBytes(16), Proof: proof, VotePackage: votePackage,
		Memo: memo,
	}}}
	txBytes, err := proto.Marshal(tx)
	c.Assert(err, qt.IsNil)
	// the voter signs with the chain id (same as signAndSendVocdoniTx uses)
	signature, err := signer.SignVocdoniTx(txBytes, fetchVocdoniChainID(t, testNewVocdoniClient(t)))
	c.Assert(err, qt.IsNil)
	stx, err := proto.Marshal(&models.SignedTx{Tx: txBytes, Signature: signature})
	c.Assert(err, qt.IsNil)
	job := enqueueAndPollJob(t, http.MethodPost, "",
		&apicommon.RelayVoteRequest{TxPayload: stx}, "vote")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted, qt.Commentf("error: %s", job.Errors))
	c.Assert(job.Result.VoteID, qt.Not(qt.HasLen), 0)
	return job.Result.VoteID
}

// TestRelayVote builds the full CSP voting setup (org, census, bundle, on-chain
// process) and casts a vote via the public relay endpoint instead of submitting it
// directly to the chain, asserting the vote is counted and a nullifier is returned.
func TestRelayVote(t *testing.T) {
	c := qt.New(t)

	// create a user and organization
	token := testCreateUser(t, "superpassword123")
	vocdoniClient := testNewVocdoniClient(t)
	orgAddress := testCreateOrganization(t, token)

	// subscribe the organization to a plan
	plans, err := testDB.Plans()
	c.Assert(err, qt.IsNil)
	c.Assert(len(plans) > 1, qt.IsTrue)
	c.Assert(testDB.SetOrganizationSubscription(orgAddress, &db.OrganizationSubscription{
		PlanID:          plans[1].ID,
		StartDate:       time.Now(),
		RenewalDate:     time.Now().Add(time.Hour * 24),
		LastPaymentDate: time.Now(),
		Active:          true,
	}), qt.IsNil)

	// create the organization account on-chain
	orgName := fmt.Sprintf("relayorg-%d", internal.RandomInt(1000))
	orgInfoURI := fmt.Sprintf("https://example.com/%d", internal.RandomInt(1000))
	nonce := uint32(0)
	accountTx := &models.Tx{Payload: &models.Tx_SetAccount{SetAccount: &models.SetAccountTx{
		Nonce:   &nonce,
		Txtype:  models.TxType_CREATE_ACCOUNT,
		Account: orgAddress.Bytes(),
		Name:    &orgName,
		InfoURI: &orgInfoURI,
	}}}
	signRemoteSignerAndSendVocdoniTx(t, accountTx, token, vocdoniClient, orgAddress)

	// create an on-chain process whose census root is the CSP public key
	cspPubKey, err := testCSP.PubKey()
	c.Assert(err, qt.IsNil)
	processNonce := fetchVocdoniAccountNonce(t, vocdoniClient, orgAddress)
	processTx := &models.Tx{Payload: &models.Tx_NewProcess{NewProcess: &models.NewProcessTx{
		Txtype: models.TxType_NEW_PROCESS,
		Nonce:  processNonce,
		Process: &models.Process{
			EntityId:      orgAddress.Bytes(),
			Duration:      120,
			Status:        models.ProcessStatus_READY,
			CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
			CensusRoot:    cspPubKey,
			MaxCensusSize: 10,
			EnvelopeType:  &models.EnvelopeType{Anonymous: false, CostFromWeight: false},
			VoteOptions:   &models.ProcessVoteOptions{MaxCount: 1, MaxValue: 5},
			Mode:          &models.ProcessMode{AutoStart: true, Interruptible: true},
		},
	}}}
	processIDBytes := signRemoteSignerAndSendVocdoniTx(t, processTx, token, vocdoniClient, orgAddress)
	processID := internal.HexBytes(processIDBytes)
	t.Logf("Created process with ID: %x", processIDBytes)

	// the relay handler requires the process to be known by its on-chain address
	_, err = testDB.SetProcess(&db.Process{OrgAddress: orgAddress, Address: processID})
	c.Assert(err, qt.IsNil)

	// create a census, add members and publish a group-based census
	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	members := []apicommon.OrgMember{{
		Name:         "Relay",
		Surname:      "Voter",
		MemberNumber: "R001",
		NationalID:   "RELAY0001A",
		BirthDate:    "1990-01-01",
		Email:        "relay.voter@example.com",
		Phone:        "+34699000001",
		Weight:       "1",
	}}
	postedOrgMembers := postOrgMembers(t, token, orgAddress, members...)
	idMap := make(map[string]string, len(postedOrgMembers))
	for _, m := range postedOrgMembers {
		idMap[m.NationalID] = m.ID
	}
	for i := range members {
		members[i].ID = idMap[members[i].NationalID]
	}

	group := postGroup(t, token, orgAddress, memberIDs(members)...)
	censusID := postCensus(t, token, orgAddress, authFields, twoFaFields)
	requestAndParse[apicommon.PublishedCensusResponse](
		t, http.MethodPost, token, &apicommon.PublishCensusGroupRequest{
			AuthFields:  authFields,
			TwoFaFields: twoFaFields,
		}, "census", censusID, "group", group.ID, "publish")

	// create a bundle linking the census and process
	bundleID, _ := postProcessBundle(t, token, censusID, processIDBytes)

	// authenticate the voter with the CSP
	authToken := testCSPAuthenticateWithFields(t, bundleID, &handlers.AuthRequest{
		Name:         members[0].Name,
		Surname:      members[0].Surname,
		MemberNumber: members[0].MemberNumber,
		Email:        members[0].Email,
	})

	// generate the voter key, CSP-sign its address and build the vote proof
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	voterAddr := voter.Address().Bytes()
	signature := testCSPSign(t, bundleID, authToken, processID, voterAddr)
	proof := testGenerateVoteProof(processID, voterAddr, signature, 1)

	// relay the vote and assert the chain counted it
	votesBefore, err := vocdoniClient.ElectionVoteCount(processID.Bytes())
	c.Assert(err, qt.IsNil)

	nullifier := testRelayVoteRequest(t, &voter, processID, proof, []byte("[\"1\"]"), nil)
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	votesAfter, err := vocdoniClient.ElectionVoteCount(processID.Bytes())
	c.Assert(err, qt.IsNil)
	c.Assert(votesAfter, qt.Equals, votesBefore+1, qt.Commentf("expected 1 more vote, got %d", votesAfter))

	// a chain-accepted relay meters the owning organization's SentVotes counter
	orgAfter, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(orgAfter.Counters.SentVotes, qt.Equals, 1)
}
