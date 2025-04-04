package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
)

// plansHandler godoc
//
//	@Summary		Get all subscription plans
//	@Description	Get the list of available subscription plans
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
	// send the plans back to the user
	apicommon.HTTPWriteJSON(w, plans)
}

// planInfoHandler godoc
//
//	@Summary		Get plan information
//	@Description	Get detailed information about a specific subscription plan
//	@Tags			plans
//	@Accept			json
//	@Produce		json
//	@Param			planID	path		string	true	"Plan ID"
//	@Success		200		{object}	db.Plan
//	@Failure		400		{object}	errors.Error	"Invalid plan ID"
//	@Failure		404		{object}	errors.Error	"Plan not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/plans/{planID} [get]
func (a *API) planInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the plan ID from the URL
	planID := chi.URLParam(r, "planID")
	// check the the planID is not empty
	if planID == "" {
		errors.ErrMalformedURLParam.Withf("planID is required").Write(w)
		return
	}
	// get the plan from the database
	planIDUint, err := strconv.ParseUint(planID, 10, 64)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid planID: %v", err).Write(w)
		return
	}
	plan, err := a.db.Plan(planIDUint)
	if err != nil {
		errors.ErrPlanNotFound.Withf("could not get plan: %v", err).Write(w)
		return
	}
	// send the plan back to the user
	apicommon.HTTPWriteJSON(w, plan)
}
