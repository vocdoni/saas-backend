package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// getSubscriptionsHandler handles the request to get the subscriptions of an organization.
// It returns the list of subscriptions with their information.
func (a *API) getPlansHandler(w http.ResponseWriter, r *http.Request) {
	// get the subscritions from the database
	plans, err := a.db.Plans()
	if err != nil {
		ErrGenericInternalServerError.Withf("could not get plans: %v", err).Write(w)
		return
	}
	// send the plans back to the user
	httpWriteJSON(w, plans)
}

func (a *API) planInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the plan ID from the URL
	planID := chi.URLParam(r, "planID")
	// check the the planID is not empty
	if planID == "" {
		ErrMalformedURLParam.Withf("planID is required").Write(w)
		return
	}
	// get the plan from the database
	planIDUint, err := strconv.ParseUint(planID, 10, 64)
	if err != nil {
		ErrMalformedURLParam.Withf("invalid planID: %v", err).Write(w)
		return
	}
	plan, err := a.db.Plan(planIDUint)
	if err != nil {
		ErrPlanNotFound.Withf("could not get plan: %v", err).Write(w)
		return
	}
	// send the plan back to the user
	httpWriteJSON(w, plan)
}
