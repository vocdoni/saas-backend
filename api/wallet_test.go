package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// TestWalletEndpoints: every non-managed organization has one wallet, read and topped up under
// its own path, the same for a standard organization and an integrator. A top-up is €1 to
// €100 000 and moves the balance only once its webhook credits it.
func TestWalletEndpoints(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "walletapipass123")
	orgAddress := testCreateOrganization(t, token)
	walletPath := []string{"organizations", orgAddress.String(), "wallet"}
	topUpPath := append(walletPath, "topup")

	// a standard organization's empty wallet reads as zero balance, empty history
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, walletPath...)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
	c.Assert(wallet.Currency, qt.Equals, "eur")
	c.Assert(wallet.History, qt.HasLen, 0)

	// top-up bounds: the €1 minimum charge up to €100 000
	for _, cents := range []pricing.Cents{pricing.MinimumChargeCents - 1, walletTopUpMaxCents + 1} {
		requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
			&apicommon.WalletTopUpRequest{AmountCents: cents, ReturnURL: "https://x.example"}, topUpPath...)
	}
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.WalletTopUpRequest{AmountCents: pricing.MinimumChargeCents, ReturnURL: "https://x.example"},
		topUpPath...)
	c.Assert(checkout.AmountCents, qt.Equals, pricing.MinimumChargeCents)
	c.Assert(fake.created, qt.HasLen, 1)
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyWalletTopUpOrg], qt.Equals, orgAddress.String())
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyProcessID], qt.Equals, "")

	// the balance moves only through the verified webhook credit, never the checkout call
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, walletPath...)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
	c.Assert(testDB.CreditWallet(db.WalletCredit{
		OrgAddress: orgAddress, AmountCents: pricing.MinimumChargeCents, IdempotencyKey: checkout.SessionID,
	}), qt.IsNil)
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, walletPath...)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.MinimumChargeCents)
	c.Assert(wallet.History, qt.HasLen, 1)
	c.Assert(wallet.History[0].Kind, qt.Equals, db.WalletEntryTopUp)

	// someone with no role in the organization reads nothing
	stranger := testCreateUser(t, "walletstranger12")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, stranger, nil, walletPath...)
}

// TestWalletManagedOrganizationHasNone: a managed organization's processes are paid by its
// integrator, so it has no wallet to read or top up — 403 (40181) for its own admin.
func TestWalletManagedOrganizationHasNone(t *testing.T) {
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "walletmanaged123")
	_, managedAddr := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_managed")

	requestAndAssertError(errors.ErrManagedOrgHasNoWallet, t, http.MethodGet, token, nil,
		"organizations", managedAddr.String(), "wallet")
	requestAndAssertError(errors.ErrManagedOrgHasNoWallet, t, http.MethodPost, token,
		&apicommon.WalletTopUpRequest{AmountCents: 10_000, ReturnURL: "https://x.example"},
		"organizations", managedAddr.String(), "wallet", "topup")
	qt.Assert(t, fake.created, qt.HasLen, 0)
}

// TestWalletIntegratorHistoryAndKeys: an integrator's history shows which managed organization
// spent each payment, and a quota:read API key reads its own organization's wallet only — not
// the wallet of another organization its creator administers — and never tops one up.
func TestWalletIntegratorHistoryAndKeys(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "walletkeypass123")
	integratorAddr, managedA := newIntegratorWithManagedOrg(t, token, "prod_test_wallet_keys")
	managedB := requestAndParse[apicommon.OrganizationInfo](
		t, http.MethodPost, token, &apicommon.CreateManagedOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{
				Type: string(db.CompanyType), Website: fmt.Sprintf("https://wallet-b-%d.example", time.Now().UnixNano()),
			},
		}, "integrator", "organizations").Address
	otherOrg := testCreateOrganization(t, token) // a standard organization the same user administers

	fundWallet(t, integratorAddr, 10_000)
	for i, managed := range []common.Address{managedA, managedB} {
		cents := pricing.Cents(1_000 * (i + 1))
		c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
			OrgAddress: integratorAddr, ProcessID: bson.NewObjectID(), ProcessOrgAddress: managed,
			AmountCents: cents, PriceCents: cents,
		}), qt.IsNil)
	}
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil,
		"organizations", integratorAddr.String(), "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(7_000))
	c.Assert(wallet.History, qt.HasLen, 3)
	spentBy := map[common.Address]pricing.Cents{}
	for _, entry := range wallet.History {
		if entry.Kind == db.WalletEntryPayment {
			spentBy[entry.ProcessOrgAddress] += entry.AmountCents
		}
	}
	c.Assert(spentBy, qt.DeepEquals, map[common.Address]pricing.Cents{managedA: -1_000, managedB: -2_000})

	expires := time.Now().Add(time.Hour)
	key := requestAndParse[apicommon.CreateAPIKeyResponse](t, http.MethodPost, token,
		&apicommon.CreateAPIKeyRequest{Label: "wallet", Scopes: []string{ScopeQuotaRead}, ExpiresAt: &expires},
		"integrator", "organizations", integratorAddr.String(), "apikeys").Secret
	viaKey := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, key, nil,
		"organizations", integratorAddr.String(), "wallet")
	c.Assert(viaKey.BalanceCents, qt.Equals, pricing.Cents(7_000))
	requestAndAssertError(errors.ErrAPIKeyNotAllowed, t, http.MethodGet, key, nil,
		"organizations", otherOrg.String(), "wallet")
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, key,
		&apicommon.WalletTopUpRequest{AmountCents: 10_000, ReturnURL: "https://x.example"},
		"organizations", integratorAddr.String(), "wallet", "topup")
}

// fundWallet credits cents to a wallet, as a verified top-up would.
func fundWallet(t *testing.T, wallet common.Address, cents pricing.Cents) {
	t.Helper()
	if cents <= 0 {
		return
	}
	qt.Assert(t, testDB.CreditWallet(db.WalletCredit{
		OrgAddress: wallet, AmountCents: cents, IdempotencyKey: "cs_fund_" + util.RandomHex(8),
	}), qt.IsNil)
}
