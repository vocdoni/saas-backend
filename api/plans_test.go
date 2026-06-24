package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
)

// TestPlansAPI covers the public plansHandler (GET /plans). Plans are synced from Stripe and
// keyed by their Stripe product ID; only public plans are listed (private custom/free-integrator
// plans are hidden). The per-plan GET /plans/{planID} endpoint was removed: a subscriber reads
// its own plan from its subscription payload instead.
func TestPlansAPI(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// List all plans: TestMain seeds exactly 3 public mock plans.
	plans := requestAndParse[[]*db.Plan](t, http.MethodGet, "", nil, plansEndpoint)
	c.Assert(plans, qt.HasLen, 3)
	for _, p := range plans {
		c.Assert(p.Public, qt.IsTrue)
		c.Assert(p.ID, qt.Not(qt.Equals), "")
	}
}
