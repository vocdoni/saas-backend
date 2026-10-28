package api

import (
	"fmt"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
)

// TestManagedOrgSpendCap: an integrator caps what a managed organization may spend from its
// wallet. A publish past the cap is refused with 402 (40183) and charges nothing, the listing
// shows cap and spending, a deleted draft's refund frees its amount, a census top-up is capped
// like a publish, and null lifts the cap.
func TestManagedOrgSpendCap(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "spendcappass1234")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_spend_cap")
	fundWallet(t, integratorAddr, 100_000)
	setCap := func(capCents *pricing.Cents) apicommon.ManagedOrganizationInfo {
		return requestAndParse[apicommon.ManagedOrganizationInfo](t, http.MethodPut, token,
			&apicommon.UpdateManagedOrganizationRequest{SpendCapCents: capCents},
			"integrator", "organizations", managedAddr.String())
	}

	capCents := pricing.Cents(1_000)
	info := setCap(&capCents)
	c.Assert(*info.SpendCapCents, qt.Equals, capCents)
	c.Assert(info.SpentCents, qt.Equals, pricing.Cents(0))
	c.Assert(info.Address, qt.Equals, managedAddr)

	// a priced draft with email 2FA costs integratorProcessCents: past the cap, refused without a charge
	first := newManagedDraft(t, token, managedAddr)
	requestAndAssertError(errors.ErrManagedOrgSpendCapReached, t, http.MethodPost, token, nil,
		"processes", first.Hex(), "publish")
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000))
	// the dry-run reports the same refusal, though the wallet covers the price
	val := requestAndParse[apicommon.VotingProcessValidateResponse](t, http.MethodGet, token, nil,
		"processes", first.Hex(), "validation")
	c.Assert(val.Valid, qt.IsFalse)
	c.Assert(val.Errors, qt.Contains, errors.ErrManagedOrgSpendCapReached.Error())

	// raised to fit: publishes, and the listing shows what it spent
	capCents = integratorProcessCents
	setCap(&capCents)
	job := enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", first.Hex(), "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusCompleted)
	list := requestAndParse[apicommon.ListManagedOrganizations](t, http.MethodGet, token, nil,
		"integrator", "organizations")
	c.Assert(list.Organizations, qt.HasLen, 1)
	c.Assert(*list.Organizations[0].SpendCapCents, qt.Equals, capCents)
	c.Assert(list.Organizations[0].SpentCents, qt.Equals, pricing.Cents(integratorProcessCents))

	// a census top-up of the published process is capped like a publish
	vp, err := testDB.VotingProcess(first)
	c.Assert(err, qt.IsNil)
	org, err := testDB.Organization(managedAddr)
	c.Assert(err, qt.IsNil)
	_, err = testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: org, quote: pricing.Quote{TotalCents: 2_000},
	})
	c.Assert(errors.Is(err, errors.ErrManagedOrgSpendCapReached), qt.IsTrue, qt.Commentf("got %v", err))

	// lifted: a second draft is paid, and its delete's refund frees what it spent
	info = setCap(nil)
	c.Assert(info.SpendCapCents, qt.IsNil)
	second := newManagedDraft(t, token, managedAddr)
	vp, err = testDB.VotingProcess(second)
	c.Assert(err, qt.IsNil)
	c.Assert(testAPI.debitProcessWallet(vp, org), qt.IsNil)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.ManagedOrgSpend(managedAddr).SpentCents, qt.Equals, pricing.Cents(2*integratorProcessCents))
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", second.Hex())
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.ManagedOrgSpend(managedAddr).SpentCents, qt.Equals, pricing.Cents(integratorProcessCents))

	// a failed publish's credit-back frees what it had spent
	third := newManagedDraft(t, token, managedAddr)
	injectPublishFaults(t, func(int32) error { return fmt.Errorf("draft cannot be built") })
	job = enqueueAndPollJob(t, http.MethodPost, token, nil, "processes", third.Hex(), "publish")
	c.Assert(job.Status, qt.Equals, db.JobStatusFailed)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.ManagedOrgSpend(managedAddr).SpentCents, qt.Equals, pricing.Cents(integratorProcessCents))
	c.Assert(walletBalance(c, integratorAddr), qt.Equals, pricing.Cents(100_000-integratorProcessCents))

	// a negative cap is refused; an organization the caller does not manage is not found
	negative := pricing.Cents(-1)
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPut, token,
		&apicommon.UpdateManagedOrganizationRequest{SpendCapCents: &negative},
		"integrator", "organizations", managedAddr.String())
	requestAndAssertCode(http.StatusNotFound, t, http.MethodPut, token,
		&apicommon.UpdateManagedOrganizationRequest{}, "integrator", "organizations", integratorAddr.String())
	// the key is required, so an empty body or a typo never lifts the cap
	capCents = integratorProcessCents
	setCap(&capCents)
	for _, body := range []any{
		map[string]any{}, map[string]any{"spend_cap_cents": nil}, map[string]any{"spendCapCents": nil, "extra": 1},
	} {
		requestAndAssertCode(http.StatusBadRequest, t, http.MethodPut, token, body,
			"integrator", "organizations", managedAddr.String())
	}
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(*wallet.ManagedOrgSpend(managedAddr).CapCents, qt.Equals, capCents)
}
