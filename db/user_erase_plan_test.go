package db

import (
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/internal"
)

// TestUserErasurePlan: the plan counts what each organization torn down holds (a draft is counted
// but does not block), marks a shared organization kept, blocks an organization whose sub-org
// would survive it, and does not block an integrator whose managed organization goes with it.
// EraseUser refuses, erasing nothing, while the plan is blocked, then while a draft is left.
func TestUserErasurePlan(t *testing.T) {
	c := qt.New(t)
	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })

	target := &User{Email: "plan@test.com", Password: "pass", FirstName: "Pl", LastName: "An", Verified: true}
	targetID, err := testDB.SetUser(target)
	c.Assert(err, qt.IsNil)
	solo, shared := common.Address{0x0D, 0x01}, common.Address{0x0D, 0x02}
	parent, child := common.Address{0x0D, 0x03}, common.Address{0x0D, 0x04}
	integrator, managed := common.Address{0x0D, 0x05}, common.Address{0x0D, 0x06}

	// solo: sole admin, with something in every counted collection and a draft
	c.Assert(testDB.SetOrganization(&Organization{
		Address: solo, Creator: target.Email, Country: "ES", Meta: map[string]any{"name": "Solo"},
	}), qt.IsNil)
	memberID, censusID := seedMemberAndCensus(t, solo, "plan")
	c.Assert(testDB.SetCensusParticipant(&CensusParticipant{
		ParticipantID: memberID.Hex(), CensusID: censusID.Hex(), CreatedAt: time.Now(),
	}), qt.IsNil)
	c.Assert(testDB.EnsureAutoMemberGroup(solo), qt.IsNil)
	insertLegacyProcess(c, &Process{OrgAddress: solo, Address: internal.HexBytes{0xd1}})
	insertLegacyBundle(c, &ProcessesBundle{OrgAddress: solo, Census: Census{ID: censusID}})
	_, err = testDB.SetVotingProcess(&VotingProcess{OrgAddress: solo, Published: true})
	c.Assert(err, qt.IsNil)
	_, err = testDB.SetVotingProcess(&VotingProcess{OrgAddress: solo})
	c.Assert(err, qt.IsNil)
	c.Assert(testDB.CreateInvitation(&OrganizationInvite{
		InvitationCode: "plancode", OrganizationAddress: solo, CurrentUserID: targetID,
		NewUserEmail: "invitee@test.com", Role: AdminRole, Expiration: time.Now().Add(time.Hour),
	}), qt.IsNil)
	c.Assert(testDB.SetAPIKey(&APIKey{ID: "plan-key", OrgAddress: solo, Hash: "plan-hash"}), qt.IsNil)

	// shared: created by the target, but another user is admin too
	c.Assert(testDB.SetOrganization(&Organization{Address: shared, Creator: target.Email, Country: "ES"}), qt.IsNil)
	_, err = testDB.SetUser(&User{
		Email: "plan-other@test.com", Password: "pass", FirstName: "Ot", LastName: "Her", Verified: true,
		Organizations: []OrganizationUser{{Address: shared, Role: AdminRole}},
	})
	c.Assert(err, qt.IsNil)

	// parent: sole admin, with a sub-org the target does not belong to
	c.Assert(testDB.SetOrganization(&Organization{Address: parent, Creator: target.Email, Country: "ES"}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{Address: child, Parent: parent, Country: "ES"}), qt.IsNil)

	// integrator and its managed organization: both torn down
	c.Assert(testDB.SetOrganization(&Organization{
		Address: integrator, Creator: target.Email, Country: "ES", IntegratorLimits: &IntegratorLimits{MaxManagedOrgs: 1},
	}), qt.IsNil)
	c.Assert(testDB.SetOrganization(&Organization{
		Address: managed, Creator: target.Email, Country: "ES", ManagedBy: integrator,
	}), qt.IsNil)

	// two objects uploaded by the target, one by someone else
	c.Assert(testDB.SetObject("plan-logo", target.Email, "image/png", []byte{1}), qt.IsNil)
	c.Assert(testDB.SetObject("plan-meta", target.Email, "application/json", []byte("{}")), qt.IsNil)
	c.Assert(testDB.SetObject("plan-other", "plan-other@test.com", "image/png", []byte{2}), qt.IsNil)

	plan, err := testDB.UserErasurePlan(targetID)
	c.Assert(err, qt.IsNil)
	c.Assert(plan, qt.DeepEquals, &UserErasurePlan{
		UserID: targetID, Name: "Pl An", Email: target.Email,
		Orgs: []ErasurePlanOrg{
			{
				Address: solo, Name: "Solo", Role: AdminRole, DeletedEntirely: true,
				LegacyProcesses: 1, Bundles: 1, VotingProcesses: 2, Drafts: 1, Members: 1, Groups: 1,
				Censuses: 1, CensusParticipants: 1, PendingInvitations: 1, APIKeys: 1,
			},
			{Address: shared, Role: AdminRole, CreatorEmailRetained: true},
			{Address: parent, Role: AdminRole, DeletedEntirely: true, SurvivingChildren: []common.Address{child}},
			{Address: integrator, Role: AdminRole, DeletedEntirely: true},
			{Address: managed, Role: AdminRole, DeletedEntirely: true, ManagedBy: integrator},
		},
		Blocked: []common.Address{parent},
		Objects: 2,
	})
	// blocked: nothing is erased
	_, err = testDB.EraseUser(targetID)
	c.Assert(err, qt.ErrorMatches, ".*would survive.*")
	_, err = testDB.User(targetID)
	c.Assert(err, qt.IsNil)
	_, err = testDB.Organization(solo)
	c.Assert(err, qt.IsNil)

	// the sub-org resolved, the erasure goes through, the draft with it
	c.Assert(testDB.DelOrganization(&Organization{Address: child}), qt.IsNil)
	plan, err = testDB.UserErasurePlan(targetID)
	c.Assert(err, qt.IsNil)
	c.Assert(plan.Blocked, qt.HasLen, 0)
	report, err := testDB.EraseUser(targetID)
	c.Assert(err, qt.IsNil)
	c.Assert(report.DeletedOrgs, qt.DeepEquals, []common.Address{solo, parent, integrator, managed})
	c.Assert(report.KeptOrgs, qt.DeepEquals, []common.Address{shared})
	c.Assert(report.ObjectsScrubbed, qt.Equals, int64(2))
}
