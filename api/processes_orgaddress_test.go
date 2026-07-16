package api

import (
	"net/http"
	"strings"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestVotingProcessOrgAddressWireFormat asserts that orgAddress is serialized on the wire as bare
// lowercase hex (internal.HexBytes), with no 0x prefix or EIP-55 checksum. Struct-parsing tests can't
// catch a format regression here, because internal.HexBytes decodes both 0x-prefixed and bare hex.
func TestVotingProcessOrgAddressWireFormat(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "adminpassword123")
	orgAddress := testCreateOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)

	created := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, memberIDs(members)), processesCreateEndpoint)

	raw, code := testRequest(t, http.MethodGet, token, nil, "processes", created.ProcessID)
	c.Assert(code, qt.Equals, http.StatusOK)
	// common.Address.Hex() returns "0x" + EIP-55 checksum; the wire form must be the bare lowercase hex.
	want := `"orgAddress":"` + strings.ToLower(orgAddress.Hex()[2:]) + `"`
	c.Assert(strings.Contains(string(raw), want), qt.IsTrue, qt.Commentf("raw body: %s", raw))
	c.Assert(strings.Contains(string(raw), `"orgAddress":"0x`), qt.IsFalse)
}
