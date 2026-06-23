package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
)

// TestProvisionAccountOnOrgCreation covers the opt-in provisioning flag of the
// organization creation endpoint: with the flag off the on-chain account must
// not be created (legacy two-step flow), and with the flag on the account is
// provisioned eagerly and the provisioning is idempotent.
func TestProvisionAccountOnOrgCreation(t *testing.T) {
	// (a) flag off = no on-chain account (legacy two-step flow).
	t.Run("flag off = no on-chain account", func(t *testing.T) {
		c := qt.New(t)
		token := testCreateUser(t, "password123")
		orgInfo := &apicommon.OrganizationInfo{
			Type:    string(db.CompanyType),
			Website: fmt.Sprintf("https://off-%d.com", internal.RandomInt(100000)),
		}
		resp := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
		c.Assert(resp.Address, qt.Not(qt.Equals), common.Address{})

		client := testNewVocdoniClient(t)
		_, err := client.Account(resp.Address.String())
		c.Assert(err, qt.Not(qt.IsNil))
	})

	// (b) flag on = provisioned + idempotent.
	t.Run("flag on = provisioned + idempotent", func(t *testing.T) {
		c := qt.New(t)
		token := testCreateUser(t, "password123")
		orgInfo := &apicommon.CreateOrganizationRequest{
			OrganizationInfo: apicommon.OrganizationInfo{
				Type:    string(db.CompanyType),
				Website: fmt.Sprintf("https://on-%d.com", internal.RandomInt(100000)),
			},
			ProvisionAccount: true,
		}
		resp := requestAndParse[apicommon.OrganizationInfo](t, http.MethodPost, token, orgInfo, organizationsEndpoint)
		c.Assert(resp.Address, qt.Not(qt.Equals), common.Address{})

		addr := resp.Address
		client := testNewVocdoniClient(t)
		acc, err := client.Account(addr.String())
		c.Assert(err, qt.IsNil)
		c.Assert(acc, qt.Not(qt.IsNil))

		// idempotent: calling CreateOrgAccount again must not error since the
		// on-chain account already exists.
		dbOrg, err := testDB.Organization(addr)
		c.Assert(err, qt.IsNil)
		orgSigner, err := account.OrganizationSigner(testSecret, dbOrg.Creator, dbOrg.Nonce)
		c.Assert(err, qt.IsNil)
		accClient, err := account.New(test.VoconedFoundedPrivKey, testAPIEndpoint)
		c.Assert(err, qt.IsNil)
		c.Assert(accClient.CreateOrgAccount(orgSigner, addr.String(), "https://example.com/x"), qt.IsNil)
	})
}
