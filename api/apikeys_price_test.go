package api

import (
	"encoding/json"
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/pricing"
)

// TestIntegratorAPIKeyReadsPrice: API keys can set a spend cap (managed:write) and read a
// process's price (voting:write), each only with its scope.
func TestIntegratorAPIKeyReadsPrice(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "keypricepass1234")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token)
	draft := newManagedDraft(t, token, managedAddr)
	mintKey := func(scopes ...string) string {
		data, code := testRequest(t, http.MethodPost, token,
			&apicommon.CreateAPIKeyRequest{Label: "price", Scopes: scopes},
			"integrator", "organizations", integratorAddr.String(), "apikeys")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
		var created apicommon.CreateAPIKeyResponse
		c.Assert(json.Unmarshal(data, &created), qt.IsNil)
		return created.Secret
	}
	votingKey := mintKey(ScopeVotingWrite)
	quotaKey := mintKey(ScopeQuotaRead)
	managedKey := mintKey(ScopeManagedWrite)

	price := requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, votingKey, nil,
		"processes", draft.Hex(), "price")
	c.Assert(price.TotalCents, qt.Equals, integratorProcessCents)
	requestAndAssertCode(http.StatusForbidden, t, http.MethodGet, quotaKey, nil, "processes", draft.Hex(), "price")

	capCents := pricing.Cents(5_000)
	info := requestAndParse[apicommon.ManagedOrganizationInfo](t, http.MethodPut, managedKey,
		&apicommon.UpdateManagedOrganizationRequest{SpendCapCents: &capCents},
		"integrator", "organizations", managedAddr.String())
	c.Assert(*info.SpendCapCents, qt.Equals, capCents)
}
