package db

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestEraseUser covers the right-to-erasure cascade: an org where the user is
// the sole admin is torn down entirely, an org with another admin survives
// with only the membership removed (creator email retained), and the user's
// invitations, verification codes and document are deleted.
func TestEraseUser(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	// target user and a second admin
	target := &User{Email: "target@test.com", Password: "pass", FirstName: "Tar", LastName: "Get", Verified: true}
	targetID, err := testDB.SetUser(target)
	c.Assert(err, qt.IsNil)
	target.ID = targetID
	other := &User{Email: "other@test.com", Password: "pass", FirstName: "Oth", LastName: "Er", Verified: true}
	otherID, err := testDB.SetUser(other)
	c.Assert(err, qt.IsNil)

	// orgA: target is creator and sole admin, with member, census, participant and invite
	orgA := common.Address{0x0A}
	c.Assert(testDB.SetOrganization(&Organization{Address: orgA, Creator: target.Email, Country: "ES"}), qt.IsNil)
	memberID, censusID := seedMemberAndCensus(t, orgA, "erase-a")
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{
		ParticipantID: memberID.Hex(),
		CensusID:      censusID.Hex(),
		CreatedAt:     time.Now(),
	}), qt.IsNil)
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		InvitationCode:      "erasecodea",
		OrganizationAddress: orgA,
		CurrentUserID:       targetID,
		NewUserEmail:        "newcomer@test.com",
		Role:                AdminRole,
		Expiration:          time.Now().Add(time.Hour),
	}), qt.IsNil)

	// orgB: target is creator and admin, but other is admin too
	orgB := common.Address{0x0B}
	c.Assert(testDB.SetOrganization(&Organization{Address: orgB, Creator: target.Email, Country: "ES"}), qt.IsNil)
	other.ID = otherID
	other.Organizations = []OrganizationUser{{Address: orgB, Role: AdminRole}}
	_, err = testDB.SetUser(other)
	c.Assert(err, qt.IsNil)
	// an invitation addressed to the target
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		InvitationCode:      "erasecodeb",
		OrganizationAddress: orgB,
		CurrentUserID:       otherID,
		NewUserEmail:        target.Email,
		Role:                ManagerRole,
		Expiration:          time.Now().Add(time.Hour),
	}), qt.IsNil)

	// a pending verification code for the target
	c.Assert(testDB.SetVerificationCode(&UserVerification{
		UserID: target.ID, SealedCode: []byte("sealed"), Type: CodeTypePasswordReset, Expiration: time.Now().Add(time.Hour),
	}), qt.IsNil)

	report, err := testDB.EraseUser(targetID)
	c.Assert(err, qt.IsNil)
	c.Assert(report.DeletedOrgs, qt.DeepEquals, []common.Address{orgA})
	c.Assert(report.KeptOrgs, qt.DeepEquals, []common.Address{orgB})
	c.Assert(report.CreatorEmailRetained, qt.DeepEquals, []common.Address{orgB})

	// the user and their verification code are gone
	_, err = testDB.User(targetID)
	c.Assert(err, qt.Equals, ErrNotFound)
	_, err = testDB.UserVerificationCode(target, CodeTypePasswordReset)
	c.Assert(err, qt.Equals, ErrNotFound)

	// orgA and everything it owned are gone
	_, err = testDB.Organization(orgA)
	c.Assert(err, qt.Equals, ErrNotFound)
	membersA, err := testDB.CountOrgMembers(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(membersA, qt.Equals, int64(0))
	participantsA, err := testDB.CountCensusParticipants(censusID.Hex())
	c.Assert(err, qt.IsNil)
	c.Assert(participantsA, qt.Equals, int64(0))

	// both invitations (issued by and addressed to the target) are gone
	_, err = testDB.InvitationByCode("erasecodea")
	c.Assert(err, qt.Equals, ErrNotFound)
	_, err = testDB.InvitationByCode("erasecodeb")
	c.Assert(err, qt.Equals, ErrNotFound)

	// orgB survives with the other admin only, creator email retained
	orgBDoc, err := testDB.Organization(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(orgBDoc.Creator, qt.Equals, target.Email)
	usersB, err := testDB.OrganizationUsers(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(usersB, qt.HasLen, 1)
	c.Assert(usersB[0].ID, qt.Equals, otherID)
}

// seedProcessesData gives the organization a voting process with two questions (one published,
// with an upstream id), a CSP auth anchored on the process, the CSP status of the upstream id and
// an API key. It returns the process id, the upstream id and the CSP token.
func seedProcessesData(t *testing.T, org common.Address, tag byte) (pid bson.ObjectID, upstream, token internal.HexBytes) {
	t.Helper()
	c := qt.New(t)
	c.Assert(testDB.SetOrganization(&Organization{Address: org, Country: "ES"}), qt.IsNil)
	pid, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)
	upstream = internal.HexBytes{0xAA, tag}
	_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: pid, OrgAddress: org, Order: 0, UpstreamID: upstream})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetQuestion(&VotingProcessQuestion{ProcessID: pid, OrgAddress: org, Order: 1})
	c.Assert(err, qt.IsNil)
	token = internal.HexBytes{0xBB, tag}
	c.Assert(testDB.SetCSPAuth(token, internal.HexBytes{0xCC, tag}, internal.HexBytes(pid[:]), "secret"), qt.IsNil)
	c.Assert(testDB.ConsumeCSPProcess(token, upstream, internal.HexBytes{0xDD, tag}), qt.IsNil)
	c.Assert(testDB.SetAPIKey(&APIKey{ID: "key-" + org.Hex(), OrgAddress: org, Hash: "hash-" + org.Hex()}), qt.IsNil)
	return pid, upstream, token
}

// TestEraseOrganizationDataProcesses checks the teardown removes the /processes data (voting
// processes, questions, their CSP tokens and status, API keys) of the organization only.
func TestEraseOrganizationDataProcesses(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	orgA, orgB := common.Address{0x1A}, common.Address{0x1B}
	pidA, upA, tokenA := seedProcessesData(t, orgA, 1)
	pidB, upB, tokenB := seedProcessesData(t, orgB, 2)

	// the zero address is rejected before anything is deleted
	c.Assert(testDB.EraseOrganizationData(common.Address{}), qt.Equals, ErrInvalidData)

	c.Assert(testDB.EraseOrganizationData(orgA), qt.IsNil)

	// org A: everything is gone
	_, err := testDB.VotingProcess(pidA)
	c.Assert(err, qt.Equals, ErrNotFound)
	qsA, err := testDB.QuestionsByProcess(pidA)
	c.Assert(err, qt.IsNil)
	c.Assert(qsA, qt.HasLen, 0)
	_, err = testDB.CSPAuth(tokenA)
	c.Assert(err, qt.Equals, ErrTokenNotFound)
	statusA, err := testDB.cspTokensStatus.CountDocuments(context.Background(), bson.M{"processid": upA})
	c.Assert(err, qt.IsNil)
	c.Assert(statusA, qt.Equals, int64(0))
	keysA, err := testDB.APIKeysByOrg(orgA)
	c.Assert(err, qt.IsNil)
	c.Assert(keysA, qt.HasLen, 0)

	// org B: untouched
	_, err = testDB.VotingProcess(pidB)
	c.Assert(err, qt.IsNil)
	qsB, err := testDB.QuestionsByProcess(pidB)
	c.Assert(err, qt.IsNil)
	c.Assert(qsB, qt.HasLen, 2)
	_, err = testDB.CSPAuth(tokenB)
	c.Assert(err, qt.IsNil)
	statusB, err := testDB.cspTokensStatus.CountDocuments(context.Background(), bson.M{"processid": upB})
	c.Assert(err, qt.IsNil)
	c.Assert(statusB, qt.Equals, int64(1))
	keysB, err := testDB.APIKeysByOrg(orgB)
	c.Assert(err, qt.IsNil)
	c.Assert(keysB, qt.HasLen, 1)
}
