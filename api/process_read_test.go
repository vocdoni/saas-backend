package api

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
)

// TestProcessReadProxies publishes an on-chain election, casts one relayed vote and
// then exercises the public read proxies: it polls GET /process/{id}/results until the
// vote is counted and fetches GET /process/{id}/metadata, asserting the returned bytes
// match the deterministically rebuilt ElectionMetadata.
func TestProcessReadProxies(t *testing.T) {
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
	orgName := fmt.Sprintf("readorg-%d", internal.RandomInt(1000))
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
	addr := internal.HexBytes(processIDBytes)
	t.Logf("Created process with ID: %x", processIDBytes)

	// the read handlers require the process to be known by its on-chain address, and
	// the metadata handler rebuilds it from the stored ElectionParams.
	electionParams := &db.ElectionParams{
		Title:         db.MultiLangString{"default": "Read proxies election"},
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
	}
	processObjID, err := testDB.SetProcess(&db.Process{OrgAddress: orgAddress, Address: addr, ElectionParams: electionParams})
	c.Assert(err, qt.IsNil)

	// create a census, add members and publish a group-based census
	authFields := db.OrgMemberAuthFields{
		db.OrgMemberAuthFieldsName,
		db.OrgMemberAuthFieldsSurname,
		db.OrgMemberAuthFieldsMemberNumber,
	}
	twoFaFields := db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}

	members := []apicommon.OrgMember{{
		Name:         "Read",
		Surname:      "Voter",
		MemberNumber: "R001",
		NationalID:   "READ00001A",
		BirthDate:    "1990-01-01",
		Email:        "read.voter@example.com",
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
		}, "census", censusID, "group", group.ID, "publish",
	)

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
	signature := testCSPSign(t, bundleID, authToken, addr, voterAddr)
	proof := testGenerateVoteProof(addr, voterAddr, signature, 1)

	// relay one vote
	nullifier := testRelayVoteRequest(t, &voter, addr, proof, []byte("[\"1\"]"), nil)
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)

	// poll the public results endpoint until the vote is counted
	var res apicommon.ProcessResultsResponse
	for i := 0; i < 20; i++ {
		res = requestAndParse[apicommon.ProcessResultsResponse](
			t, http.MethodGet, "", nil, "process", processObjID.Hex(), "results",
		)
		if res.VoteCount == 1 {
			break
		}
		time.Sleep(time.Second)
	}
	c.Assert(res.VoteCount, qt.Equals, uint64(1))
	c.Assert(res.Status, qt.Not(qt.HasLen), 0)
	c.Assert(res.EndDate.IsZero(), qt.IsFalse)

	// fetch the public metadata endpoint and assert it matches the rebuilt bytes
	body, code := testRequest(t, http.MethodGet, "", nil, "process", processObjID.Hex(), "metadata")
	c.Assert(code, qt.Equals, http.StatusOK)
	expectedBytes, err := account.BuildElectionMetadata(electionParams)
	c.Assert(err, qt.IsNil)
	c.Assert(bytes.Equal(body, expectedBytes), qt.IsTrue)

	// sign-info resolves the process by either the 24-hex ProcessID (preferred) or the
	// 64-hex on-chain election id (backwards compatible), returning the same consumed address
	// and nullifier for the voter that just cast a vote.
	signInfoReq := &handlers.ConsumedAddressRequest{AuthToken: authToken}
	byObjID := requestAndParse[handlers.ConsumedAddressResponse](
		t, http.MethodPost, "", signInfoReq, "process", processObjID.Hex(), "sign-info",
	)
	byOnchain := requestAndParse[handlers.ConsumedAddressResponse](
		t, http.MethodPost, "", signInfoReq, "process", addr.String(), "sign-info",
	)
	c.Assert(byObjID.Address, qt.Not(qt.HasLen), 0)
	c.Assert(byObjID.Address.String(), qt.Equals, byOnchain.Address.String())
	c.Assert(byObjID.Nullifier.String(), qt.Equals, byOnchain.Nullifier.String())

	// a malformed id (valid hex but neither a 24-hex ProcessID nor a 32-byte on-chain id) is a
	// 400, and a well-formed-but-unknown ProcessID is a 404 — these are checked before auth.
	_, badCode := testRequest(t, http.MethodPost, "", signInfoReq, "process", "abcd", "sign-info")
	c.Assert(badCode, qt.Equals, http.StatusBadRequest)
	_, missCode := testRequest(t, http.MethodPost, "", signInfoReq, "process", "0123456789abcdef01234567", "sign-info")
	c.Assert(missCode, qt.Equals, http.StatusNotFound)
}
