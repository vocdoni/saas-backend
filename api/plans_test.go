package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// TestPlansAPI covers the public plansHandler (GET /plans) and planInfoHandler
// (GET /plans/{planID}) endpoints, including their error cases.
func TestPlansAPI(t *testing.T) {
	c := qt.New(t)
	defer func() {
		if err := testDB.DeleteAllDocuments(); err != nil {
			c.Logf("cleanup: %v", err)
		}
	}()

	// List all plans: TestMain seeds exactly 3 mock plans (IDs 1, 2, 3).
	plans := requestAndParse[[]*db.Plan](t, http.MethodGet, "", nil, plansEndpoint)
	c.Assert(plans, qt.HasLen, 3)

	// Get a single plan by ID.
	plan := requestAndParse[db.Plan](t, http.MethodGet, "", nil, "plans", "1")
	c.Assert(plan.ID, qt.Equals, uint64(1))
	c.Assert(plan.Name, qt.Equals, "Free Plan")

	// Unknown plan ID returns 404 ErrPlanNotFound.
	requestAndAssertError(errors.ErrPlanNotFound, t, http.MethodGet, "", nil, "plans", "999999")

	// Non-numeric plan ID returns 400 ErrMalformedURLParam.
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodGet, "", nil, "plans", "not-a-number")
}
