package api

import (
	"net/http"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/stripe"
)

func TestWalletEndpoints(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "walletapipass123")
	integratorAddr := testCreateOrganization(t, token)
	topUpReq := &apicommon.WalletTopUpRequest{AmountCents: 50_000, ReturnURL: "https://app.example.com/wallet"}

	// a non-integrator cannot reach the wallet
	requestAndAssertCode(http.StatusForbidden, t, http.MethodGet, token, nil, "wallet")
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, token, topUpReq, "wallet", "topup")

	integratorOrg, err := testDB.Organization(integratorAddr)
	c.Assert(err, qt.IsNil)
	integratorOrg.IntegratorLimits = &db.IntegratorLimits{MaxManagedOrgs: 2}
	c.Assert(testDB.SetOrganization(integratorOrg), qt.IsNil)

	// an empty wallet reads as zero balance, empty ledger
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))
	c.Assert(wallet.Ledger, qt.HasLen, 0)

	// top-up amount bounds are enforced
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, token,
		&apicommon.WalletTopUpRequest{AmountCents: 100, ReturnURL: "https://x.example"}, "wallet", "topup")

	// a top-up opens a one-time session carrying the wallet routing metadata
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, topUpReq, "wallet", "topup")
	c.Assert(checkout.ClientSecret, qt.Not(qt.Equals), "")
	c.Assert(checkout.AmountCents, qt.Equals, int64(50_000))
	c.Assert(fake.created, qt.HasLen, 1)
	c.Assert(fake.created[0].Metadata[stripe.MetadataKeyWalletTopUpOrg], qt.Equals, integratorAddr.String())

	// the balance moves only through verified webhook credit, never the checkout call
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(0))
	c.Assert(testDB.CreditWallet(integratorAddr, 50_000, checkout.SessionID), qt.IsNil)
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil, "wallet")
	c.Assert(wallet.BalanceCents, qt.Equals, int64(50_000))
	c.Assert(wallet.Ledger, qt.HasLen, 1)
	c.Assert(wallet.Ledger[0].Kind, qt.Equals, db.WalletEntryTopUp)
	c.Assert(wallet.Ledger[0].AmountCents, qt.Equals, int64(50_000))

	// the wallet read is reachable with a quota:read API key; the top-up is not
	expires := time.Now().Add(time.Hour)
	keyResp := requestAndParse[apicommon.CreateAPIKeyResponse](t, http.MethodPost, token,
		&apicommon.CreateAPIKeyRequest{Label: "wallet", Scopes: []string{ScopeQuotaRead}, ExpiresAt: &expires},
		"integrator", "organizations", integratorAddr.String(), "apikeys")
	viaKey := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, keyResp.Secret, nil, "wallet")
	c.Assert(viaKey.BalanceCents, qt.Equals, int64(50_000))
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, keyResp.Secret, topUpReq, "wallet", "topup")
}
