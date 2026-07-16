package api

import (
	"encoding/json"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// validateProcessCensusHandler godoc
//
//	@Summary		Validate a voting process census
//	@Description	Pre-flight check of a /processes census spec: verifies the chosen auth/2FA fields
//	@Description	produce unique, complete credentials over the target members — a group (groupId), an
//	@Description	explicit memberIds subset, or the whole organization when neither is set. Returns 400
//	@Description	with the offending member ids (duplicates / missingData) when the census is not usable,
//	@Description	otherwise 200. Requires Manager/Admin role of the organization.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.ValidateProcessCensusRequest	true	"Org address + census spec"
//	@Success		200		{string}	string									"OK"
//	@Failure		400		{object}	errors.Error							"Invalid census (duplicates or missing data)"
//	@Failure		401		{object}	errors.Error							"Unauthorized"
//	@Failure		500		{object}	errors.Error							"Internal server error"
//	@Router			/processes/census/validation [post]
func (a *API) validateProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	var req apicommon.ValidateProcessCensusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.OrgAddress.Cmp(common.Address{}) == 0 {
		errors.ErrMalformedBody.Withf("missing orgAddress").Write(w)
		return
	}
	if !user.HasRoleFor(req.OrgAddress, db.AdminRole) && !user.HasRoleFor(req.OrgAddress, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}
	census := req.Census
	if len(census.AuthFields) == 0 && len(census.TwoFaFields) == 0 {
		errors.ErrInvalidData.Withf("missing both authFields and twoFaFields").Write(w)
		return
	}

	// select the member set to validate: a group, an explicit subset, or (default) the whole org.
	var results *db.OrgMemberAggregationResults
	var err error
	switch {
	case census.GroupID != "":
		results, err = a.db.CheckGroupMembersFields(req.OrgAddress, census.GroupID, census.AuthFields, census.TwoFaFields)
	case len(census.MemberIDs) > 0:
		results, err = a.db.CheckMembersFields(req.OrgAddress, census.MemberIDs, census.AuthFields, census.TwoFaFields)
	default:
		results, err = a.db.CheckGroupMembersFields(req.OrgAddress, "", census.AuthFields, census.TwoFaFields)
	}
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(results.Duplicates) > 0 || len(results.MissingData) > 0 {
		errors.ErrInvalidData.WithData(results).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
