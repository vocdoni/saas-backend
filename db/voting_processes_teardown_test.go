package db

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestDeleteVotingProcessesByOrg removes an organization's processes and questions and nothing
// of any other organization.
func TestDeleteVotingProcessesByOrg(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	org, other := common.Address{0x22}, common.Address{0x23}
	setupVotingProcessOrg(c, org)
	setupVotingProcessOrg(c, other)
	ids := map[common.Address]bson.ObjectID{}
	for _, addr := range []common.Address{org, other} {
		id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: addr})
		c.Assert(err, qt.IsNil)
		_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: id, OrgAddress: addr, Type: VotingTypeSingleChoice})
		c.Assert(err, qt.IsNil)
		ids[addr] = id
	}

	deleted, err := testDB.DeleteVotingProcessesByOrg(org)
	c.Assert(err, qt.IsNil)
	c.Assert(deleted, qt.Equals, int64(1))
	_, _, err = testDB.ProcessWithQuestions(ids[org])
	c.Assert(err, qt.Equals, ErrNotFound)
	questions, err := testDB.QuestionsByProcess(ids[org])
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 0)
	_, questions, err = testDB.ProcessWithQuestions(ids[other])
	c.Assert(err, qt.IsNil)
	c.Assert(questions, qt.HasLen, 1)
}

// TestEraseUserRefusesDrafts: a draft may hold money only the API layer can refund, so the
// erasure refuses, erasing nothing, until the drafts are gone; then the organization's published
// voting processes go with it and its payment rows stay.
func TestEraseUserRefusesDrafts(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
	target := &User{Email: "drafts@test.com", Password: "pass", FirstName: "Dr", LastName: "Aft", Verified: true}
	userID, err := testDB.SetUser(target)
	c.Assert(err, qt.IsNil)
	org := common.Address{0x24}
	c.Assert(testDB.SetOrganization(&Organization{Address: org, Creator: target.Email, Country: "ES"}), qt.IsNil)
	draftID, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	publishedID, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org, Published: true})
	c.Assert(err, qt.IsNil)
	stored, err := testDB.SetProcessPaymentPaidByWallet(&ProcessPayment{
		ProcessID: publishedID, OrgAddress: org, Payer: org, AmountCents: 1_515, Currency: "eur",
	})
	c.Assert(err, qt.IsNil)
	c.Assert(stored, qt.IsTrue)

	_, err = testDB.EraseUser(userID)
	c.Assert(err, qt.ErrorMatches, ".*still has 1 drafts.*")
	_, err = testDB.User(userID)
	c.Assert(err, qt.IsNil)
	_, err = testDB.Organization(org)
	c.Assert(err, qt.IsNil)

	c.Assert(testDB.DeleteVotingProcess(draftID), qt.IsNil)
	_, err = testDB.EraseUser(userID)
	c.Assert(err, qt.IsNil)
	_, err = testDB.VotingProcess(publishedID)
	c.Assert(err, qt.Equals, ErrNotFound)
	_, err = testDB.ProcessPayment(publishedID)
	c.Assert(err, qt.IsNil)
}
