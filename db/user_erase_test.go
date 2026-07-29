package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
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
	c.Assert(testDB.SetVerificationCode(target, []byte("sealed"), CodeTypePasswordReset, time.Now().Add(time.Hour)), qt.IsNil)

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
