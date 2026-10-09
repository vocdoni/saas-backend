package api

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// identityMetaKeys are the organization meta keys that carry the organization identity shown to
// voters (emails, public organization info). As with PUT /organizations, changing them is
// admin-only; managers may still write every other meta key (e.g. the web app's tutorial flags).
var identityMetaKeys = []string{"name", "logo", "description"}

// isIdentityMetaKey reports whether key targets an identity meta key, either directly ("name")
// or through MongoDB dot notation ("name.default"), which UpdateOrganizationMeta and
// DeleteOrganizationMetaKeys pass straight into $set / $unset.
func isIdentityMetaKey(key string) bool {
	top, _, _ := strings.Cut(key, ".")
	return slices.Contains(identityMetaKeys, top)
}

// sameMetaValue reports whether two meta values are equal once both go through a JSON
// round-trip, so a value decoded from a request body compares equal to the same value
// decoded from MongoDB.
func sameMetaValue(a, b any) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var na, nb any
	if json.Unmarshal(ja, &na) != nil || json.Unmarshal(jb, &nb) != nil {
		return false
	}
	return reflect.DeepEqual(na, nb)
}

// identityMetaReplaced reports whether replacing the whole current meta map with next would
// add, change or remove any identity meta key.
func identityMetaReplaced(current, next map[string]any) bool {
	for _, key := range identityMetaKeys {
		cur, hasCur := current[key]
		nxt, hasNext := next[key]
		if hasCur != hasNext || (hasCur && !sameMetaValue(cur, nxt)) {
			return true
		}
	}
	return false
}

// identityMetaUpdated reports whether merging updates into current would add or change any
// identity meta key. Resending a key with its stored value (the web app PUTs back the whole
// meta map it read) is not a change; a dotted sub-key write (e.g. "name.default") always is.
func identityMetaUpdated(current, updates map[string]any) bool {
	for key, value := range updates {
		if !isIdentityMetaKey(key) {
			continue
		}
		cur, ok := current[key]
		if !ok || !sameMetaValue(cur, value) {
			return true
		}
	}
	return false
}

// writeIdentityMetaAdminOnly writes the error returned when a non-admin tries to change an
// identity meta key.
func writeIdentityMetaAdminOnly(w http.ResponseWriter) {
	errors.ErrUnauthorized.Withf("only organization admins can change the organization name, logo or description").Write(w)
}

// addOrganizationMetaHandler godoc
//
//	@Summary		Add meta information to an organization
//	@Description	Overwrite the meta information of an organization. Requires Manager/Admin role.
//	@Description	Only admins may change the organization identity keys (name, logo, description): a manager's
//	@Description	request must carry them exactly as they are stored, or it is rejected with 401.
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
	if !user.HasRoleFor(org.Address, db.AdminRole) && identityMetaReplaced(org.Meta, meta.Meta) {
		writeIdentityMetaAdminOnly(w)
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
//	@Description	Requires Manager/Admin role. Only admins may change the organization identity keys
//	@Description	(name, logo, description, including dotted sub-keys such as name.default): a manager may
//	@Description	resend them with their stored value, but any other write to them is rejected with 401.
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
	if !user.HasRoleFor(org.Address, db.AdminRole) && identityMetaUpdated(org.Meta, meta.Meta) {
		writeIdentityMetaAdminOnly(w)
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
//	@Description	Requires Manager/Admin role. Only admins may delete the organization identity keys
//	@Description	(name, logo, description); a manager's request naming any of them is rejected with 401.
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
	if !user.HasRoleFor(org.Address, db.AdminRole) && slices.ContainsFunc(meta.Keys, isIdentityMetaKey) {
		writeIdentityMetaAdminOnly(w)
		return
	}
	// Update the organization meta information
	if err := a.db.DeleteOrganizationMetaKeys(org.Address, meta.Keys); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
