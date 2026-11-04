package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

// TestShowPlanBlocked: a blocked plan is printed and fails before anything is written: the draft
// stays. Once unblocked it passes.
func TestShowPlanBlocked(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	ms, err := db.New(uri, test.RandomDatabaseName())
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)

	org, child := common.Address{0x91}, common.Address{0x92}
	user := &db.User{
		Email: "userdel-blocked@test.com", Password: "pass", FirstName: "User", LastName: "Del", Verified: true,
		Organizations: []db.OrganizationUser{{Address: org, Role: db.AdminRole}},
	}
	user.ID, err = ms.SetUser(user)
	c.Assert(err, qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Creator: user.Email, Country: "ES"}), qt.IsNil)
	c.Assert(ms.SetOrganization(&db.Organization{Address: child, Parent: org, Country: "ES"}), qt.IsNil)
	draft, err := ms.SetVotingProcess(&db.VotingProcess{OrgAddress: org})
	c.Assert(err, qt.IsNil)

	var out bytes.Buffer
	c.Assert(showPlan(&out, ms, user.ID), qt.ErrorMatches, "erasure blocked: orgs .* would survive them")
	c.Assert(out.String(), qt.Contains, "  BLOCKED: ")
	_, err = ms.VotingProcess(draft)
	c.Assert(err, qt.IsNil)

	c.Assert(ms.DelOrganization(&db.Organization{Address: child}), qt.IsNil)
	c.Assert(showPlan(&bytes.Buffer{}, ms, user.ID), qt.IsNil)
}

// TestPrintPlan: the dry-run report is rendered from the erasure plan, API keys and blocking
// sub-orgs included.
func TestPrintPlan(t *testing.T) {
	c := qt.New(t)
	solo, shared, child := common.Address{0x81}, common.Address{0x82}, common.Address{0x83}
	var out bytes.Buffer
	c.Assert(printPlan(&out, &db.UserErasurePlan{
		UserID: 7, Name: "User Del", Email: "userdel-plan@test.com",
		Orgs: []db.ErasurePlanOrg{
			{
				Address: solo, Name: "Solo", Role: db.AdminRole, DeletedEntirely: true,
				LegacyProcesses: 1, Bundles: 2, VotingProcesses: 3, Drafts: 1, Members: 4, Groups: 1,
				Censuses: 2, CensusParticipants: 5, PendingInvitations: 1, APIKeys: 2,
				SurvivingChildren: []common.Address{child},
			},
			{Address: shared, Role: db.ManagerRole, CreatorEmailRetained: true},
		},
		Blocked: []common.Address{solo},
		Objects: 3,
	}), qt.IsNil)
	for _, want := range []string{
		"This will erase user #7: User Del <userdel-plan@test.com>",
		"Organization " + solo.String() + " (\"Solo\", role: admin)\n  DELETED ENTIRELY",
		"    the organization and its voting history metadata (1 processes, 2 bundles)\n",
		"    3 voting processes, 1 of them drafts\n",
		"    4 members in 1 groups\n",
		"    2 censuses with 5 participants (incl. their CSP auth tokens)\n",
		"    1 pending invitations, plus import jobs\n    2 API keys\n",
		"  BLOCKED: these sub-orgs or managed orgs would survive it; delete them or move them first:\n    " +
			child.String() + "\n",
		"Organization " + shared.String() + " (\"unnamed\", role: manager)\n  KEPT",
		"  NOTE: the creator email stays",
		"pending verification codes, and the user account itself.\n" +
			"Kept with the user's email removed: 3 uploaded objects (images, election metadata).\nNot touched",
		"Not touched: Stripe billing data and database backups.",
	} {
		c.Assert(out.String(), qt.Contains, want)
	}
}
