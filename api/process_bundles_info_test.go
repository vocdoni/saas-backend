package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/util"
)

// bundleTestSetup creates a verified user (Admin of a fresh org), a census, and
// a bundle row inserted directly via testDB. Inserting the bundle directly keeps
// these handler tests independent of the full on-chain bundle-creation path: the
// handlers under test only need the bundle row plus the census it references.
// It returns the owner's JWT, the org address, and the bundle's hex ID.
func bundleTestSetup(t *testing.T, c *qt.C, password string) (string, common.Address, string) {
	t.Helper()
	token := testCreateUser(t, password)
	orgAddress := testCreateOrganization(t, token)
	censusID := postCensus(t, token, orgAddress,
		db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail})

	census, err := testDB.Census(censusID)
	c.Assert(err, qt.IsNil)
	bundleObjID := testDB.NewBundleID()
	_, err = testDB.SetProcessBundle(&db.ProcessesBundle{
		ID:         bundleObjID,
		OrgAddress: orgAddress,
		Census:     *census,
	})
	c.Assert(err, qt.IsNil)
	return token, orgAddress, bundleObjID.Hex()
}

// TestProcessBundleInfo exercises GET /process/bundle/{bundleId}
// (processBundleInfoHandler, public/no auth).
func TestProcessBundleInfo(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	_, orgAddress, bundleID := bundleTestSetup(t, c, "bundleinfopass123")

	// Success: the public info endpoint returns the stored bundle.
	got := requestAndParse[db.ProcessesBundle](t, http.MethodGet, "", nil, "process", "bundle", bundleID)
	c.Assert(got.OrgAddress.String(), qt.Equals, orgAddress.String())

	// Unknown bundle (valid hex shape, not stored) → 400 (handler maps
	// db.ErrNotFound to ErrMalformedURLParam, not 404).
	unknownID := internal.HexBytes(util.RandomBytes(12)).String()
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodGet, "", nil,
		"process", "bundle", unknownID)

	// Malformed (un-parseable) bundle id → 400.
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodGet, "", nil,
		"process", "bundle", "zznothex")
}

// TestUpdateProcessBundle exercises PUT /process/bundle/{bundleId}
// (updateProcessBundleHandler, protected/Bearer JWT).
func TestUpdateProcessBundle(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token, _, bundleID := bundleTestSetup(t, c, "bundleupdatepass123")

	// Empty processes → early return (before any permission check) echoing the
	// existing bundle, with a non-empty URI.
	resp := requestAndParse[apicommon.CreateProcessBundleResponse](t, http.MethodPut, token,
		&AddProcessesToBundleRequest{Processes: []string{}}, "process", "bundle", bundleID)
	c.Assert(resp.URI, qt.Not(qt.Equals), "")

	// Add two processes. The handler hex-decodes each id (via util.TrimHex), so a
	// plain hex string from HexBytes.String() is accepted.
	pid1 := internal.HexBytes(util.RandomBytes(32)).String()
	pid2 := internal.HexBytes(util.RandomBytes(32)).String()
	_ = requestAndParse[apicommon.CreateProcessBundleResponse](t, http.MethodPut, token,
		&AddProcessesToBundleRequest{Processes: []string{pid1, pid2}}, "process", "bundle", bundleID)

	// Re-GET the bundle and confirm both processes were appended.
	got := requestAndParse[db.ProcessesBundle](t, http.MethodGet, "", nil, "process", "bundle", bundleID)
	c.Assert(got.Processes, qt.HasLen, 2)

	// Unknown bundle → 400.
	unknownID := internal.HexBytes(util.RandomBytes(12)).String()
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodPut, token,
		&AddProcessesToBundleRequest{Processes: []string{pid1}}, "process", "bundle", unknownID)
}

// TestProcessBundleParticipantInfo exercises
// GET /process/bundle/{bundleId}/{participantId}
// (processBundleParticipantInfoHandler, public/no auth, currently a stub).
func TestProcessBundleParticipantInfo(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	_, _, bundleID := bundleTestSetup(t, c, "bundleparticipantpass123")

	// Success: the bundle exists, so the stub writes JSON null with status 200.
	requestAndAssertCode(http.StatusOK, t, http.MethodGet, "", nil,
		"process", "bundle", bundleID, "somixeparticipant")

	// Unknown bundle → 400.
	unknownID := internal.HexBytes(util.RandomBytes(12)).String()
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodGet, "", nil,
		"process", "bundle", unknownID, "p1")
}

// TestCreateProcessBundleAcceptsProcessID exercises POST /process/bundle accepting either the 24-hex
// ProcessID or the 64-hex on-chain election id for each process (dual-accept, mirroring sign-info).
// The ProcessID resolves to the process' on-chain id; the on-chain id keeps working (non-breaking).
func TestCreateProcessBundleAcceptsProcessID(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	token := testCreateUser(t, "bundlepidpass123")
	orgAddress := testCreateOrganization(t, token)
	censusID := postCensus(t, token, orgAddress,
		db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber},
		db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail})

	// A published process has a 32-byte on-chain Address.
	onChain := internal.HexBytes(util.RandomBytes(32))
	processObjID, err := testDB.SetProcess(&db.Process{OrgAddress: orgAddress, Address: onChain})
	c.Assert(err, qt.IsNil)

	assertBundleHasOnChainID := func(bundleID string) {
		got := requestAndParse[db.ProcessesBundle](t, http.MethodGet, "", nil, "process", "bundle", bundleID)
		c.Assert(got.Processes, qt.HasLen, 1)
		c.Assert(got.Processes[0].String(), qt.Equals, onChain.String())
	}

	// (1) The 24-hex ProcessID is resolved to the process' on-chain id.
	byProcessID, _ := postProcessBundle(t, token, censusID, processObjID[:])
	assertBundleHasOnChainID(byProcessID)

	// (2) The 64-hex on-chain id keeps working directly (non-breaking).
	byOnChain, _ := postProcessBundle(t, token, censusID, onChain)
	assertBundleHasOnChainID(byOnChain)

	// (3) An existing but unpublished process (no on-chain id) → 400.
	unpublished, err := testDB.SetProcess(&db.Process{OrgAddress: orgAddress})
	c.Assert(err, qt.IsNil)
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token,
		&apicommon.CreateProcessBundleRequest{CensusID: censusID, Processes: []string{unpublished.Hex()}},
		"process", "bundle")

	// (4) A well-formed but unknown ProcessID → 404.
	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodPost, token,
		&apicommon.CreateProcessBundleRequest{CensusID: censusID, Processes: []string{"0123456789abcdef01234567"}},
		"process", "bundle")
}
