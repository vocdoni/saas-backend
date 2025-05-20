package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// addOrganizationMetaHandler godoc
//
//	@Summary		Add meta information to an organization
//	@Description	Overwrite the meta information of an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationAddMetaRequest	true	"Meta information to add to the organization"
//	@Success		200			{string}	string									"OK"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
//	@Failure		422			{object}	errors.Error							"Invalid meta information"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/organizations/{orgAddress}/meta [post]
func (a *API) addOrganizationMetaHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not a manager or admin of organization").Write(w)
		return
	}

	// get the meta information from the request body
	var meta apicommon.OrganizationAddMetaRequest
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Withf("invalid meta information").Write(w)
		return
	}
	// Update the organization meta information
	if err := a.db.AddOrganizationMeta(org.Address, meta.Meta); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// updateOrganizationMetaHandler godoc
//
//	@Summary		Update organization meta information
//	@Description	Updates existing or adds new key/value pairs in the meta information of an organization.
//	@Description	Has only one layer of depth. If a second layer document is provided, for example meta.doc = [a,b,c],
//	@Description	the entire document will be updated.
//	@Description 	Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationAddMetaRequest	true	"Meta information to update in the organization"
//	@Success		200			{string}	string									"OK"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
//	@Failure		422			{object}	errors.Error							"Invalid meta information"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/organizations/{orgAddress}/meta [put]
func (a *API) updateOrganizationMetaHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not a manager or admin of organization").Write(w)
		return
	}

	// get the meta information from the request body
	var meta apicommon.OrganizationAddMetaRequest
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Withf("invalid meta information").Write(w)
		return
	}
	// Update the organization meta information
	if err := a.db.UpdateOrganizationMeta(org.Address, meta.Meta); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// organizationMetaHandler godoc
//
//	@Summary		Get organization meta information
//	@Description	Retrieves the meta information of an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string								true	"Organization address"
//	@Success		200			{object}	apicommon.OrganizationMetaResponse	"Organization meta information"
//	@Failure		401			{object}	errors.Error						"Unauthorized"
//	@Failure		403			{object}	errors.Error						"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error						"Organization not found"
//	@Failure		500			{object}	errors.Error						"Internal server error"
//	@Router			/organizations/{orgAddress}/meta [get]
func (a *API) organizationMetaHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not a manager or admin of organization").Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, apicommon.OrganizationMetaResponse{
		Meta: org.Meta,
	})
}

// deleteOrganizationMetaHandler godoc
//
//	@Summary		Delete organization meta information
//	@Description	Deletes a set of keys from the meta information of an organization.
//					Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationDeleteMetaRequest	true	"Keys to delete from the organization meta"
//	@Success		200			{string}	string									"OK"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
//	@Failure		422			{object}	errors.Error							"Invalid meta information"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/organizations/{orgAddress}/meta [delete]
func (a *API) deleteOrganizationMetaHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not a manager or admin of organization").Write(w)
		return
	}

	// get the meta information from the request body
	var meta apicommon.OrganizationDeleteMetaRequest
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Withf("invalid meta information").Write(w)
		return
	}
	// Update the organization meta information
	if err := a.db.DeleteOrganizationMetaKeys(org.Address, meta.Keys); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
