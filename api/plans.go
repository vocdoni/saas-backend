package api

import (
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// plansHandler godoc
//
//	@Summary		Get all subscription plans
//	@Description	Get the list of publicly available subscription plans
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Success		200	{array}		db.Plan
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/plans [get]
func (a *API) plansHandler(w http.ResponseWriter, _ *http.Request) {
	// get the subscritions from the database
	plans, err := a.db.Plans()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get plans: %v", err).Write(w)
		return
	}
	// only list public plans. Private plans (per-customer custom contracts and the internal
	// free integrator tier) are hidden from the catalog; a subscriber still sees its own plan
	// embedded in its subscription payload (see the organization subscription handler).
	visible := make([]*db.Plan, 0, len(plans))
	for _, p := range plans {
		if p != nil && p.Public {
			visible = append(visible, a.planForResponse(p))
		}
	}
	// send the plans back to the user
	apicommon.HTTPWriteJSON(w, visible)
}

// planForResponse returns the plan as clients see it. While anonymous voting is temporarily off it is
// a copy with Features.Anonymous forced to false; otherwise it is the stored plan, its value unchanged.
func (a *API) planForResponse(plan *db.Plan) *db.Plan {
	if plan == nil || !a.csp.BlindCSPDisabled() {
		return plan
	}
	shown := *plan
	shown.Features.Anonymous = false
	return &shown
}
