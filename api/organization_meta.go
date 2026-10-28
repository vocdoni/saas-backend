package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// isIdentityMetaKey reports whether key targets an identity meta key, either directly ("name")
// or through MongoDB dot notation ("name.default"), which UpdateOrganizationMeta and
// DeleteOrganizationMetaKeys pass straight into $set / $unset.
func isIdentityMetaKey(key string) bool {
	top, _, _ := strings.Cut(key, ".")
	return slices.Contains(apicommon.IdentityMetaKeys, top)
}

// identityFieldStored reports whether the top-level field a (possibly dotted) identity key writes
// to is present in meta.
func identityFieldStored(meta map[string]any, key string) bool {
	top, _, _ := strings.Cut(key, ".")
	_, ok := meta[top]
	return ok
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

// sameIdentityValue is sameMetaValue for identity keys, which are compared once normalized: a
// legacy plain string ("Acme") equals the {"default": "Acme"} form GET /organizations returns.
func sameIdentityValue(a, b any) bool {
	ma, mb := apicommon.MultilingualFromAny(a), apicommon.MultilingualFromAny(b)
	if ma != nil && mb != nil {
		return maps.Equal(*ma, *mb)
	}
	return sameMetaValue(a, b)
}

// identityMetaReplaced reports whether replacing the whole current meta map with next would
// add, change or remove any identity meta key.
func identityMetaReplaced(current, next map[string]any) bool {
	for _, key := range apicommon.IdentityMetaKeys {
		cur, hasCur := current[key]
		nxt, hasNext := next[key]
		if hasCur != hasNext || (hasCur && !sameIdentityValue(cur, nxt)) {
			return true
		}
	}
	return false
}

// identityMetaUpdated reports whether merging updates into current would add or change any
// identity meta key. Resending a key with its stored value (the web app PUTs back the whole
// meta map it read) is not a change, but a dotted sub-key write (e.g. "name.default") always is.
func identityMetaUpdated(current, updates map[string]any) bool {
	for key, value := range updates {
		if !isIdentityMetaKey(key) {
			continue
		}
		// a dotted key writes into the stored value, whatever a literal field of that name holds
		if strings.Contains(key, ".") {
			return true
		}
		cur, ok := current[key]
		if !ok || !sameIdentityValue(cur, value) {
			return true
		}
	}
	return false
}

// checkMetaPathsConflict returns an error naming two dot-notation paths that overlap: the same path
// twice, or one a dot prefix of the other ("a" and "a.b"), which MongoDB refuses in a single update.
// It sorts a copy, so the order of paths does not matter.
func checkMetaPathsConflict(paths []string) error {
	sorted := slices.Clone(paths)
	slices.SortFunc(sorted, comparePaths)
	// every path that extends another one sorts right after it, so a neighbour check finds any overlap
	for i := 1; i < len(sorted); i++ {
		if isMetaPathPrefix(sorted[i-1], sorted[i]) {
			return fmt.Errorf("meta keys %q and %q conflict with each other", sorted[i-1], sorted[i])
		}
	}
	return nil
}

// comparePaths orders dot-notation paths with a dot before any other byte, so "a.b" sorts right after
// "a" and before "a-" (plain string order would put "a-" between them).
func comparePaths(a, b string) int {
	for i := range min(len(a), len(b)) {
		switch {
		case a[i] == b[i]:
			continue
		case a[i] == '.':
			return -1
		case b[i] == '.':
			return 1
		default:
			return int(a[i]) - int(b[i])
		}
	}
	return len(a) - len(b)
}

// isMetaPathPrefix reports whether parent is child itself or a dot-notation prefix of it.
func isMetaPathPrefix(parent, child string) bool {
	return strings.HasPrefix(child, parent) && (len(child) == len(parent) || child[len(parent)] == '.')
}

// validateMetaPaths checks the dot-notation paths of a PUT or DELETE request.
func validateMetaPaths(paths []string) error {
	for _, path := range paths {
		if err := apicommon.ValidateMetaPath(path); err != nil {
			return err
		}
	}
	return checkMetaPathsConflict(paths)
}

// validateChangedMetaValues applies ValidateMetaValue to the values of meta that differ from the
// stored ones. An unchanged value is accepted as it is: the web app echoes back the whole meta map it
// read, which may hold values stored before the nested key check existed.
func validateChangedMetaValues(stored, meta map[string]any) error {
	for key, value := range meta {
		// a dotted key is a path into the stored value, so it is never matched to a literal field
		if !strings.Contains(key, ".") {
			if cur, ok := stored[key]; ok && sameMetaValue(cur, value) {
				continue
			}
		}
		if err := apicommon.ValidateMetaValue(value); err != nil {
			return err
		}
	}
	return nil
}

// writeIdentityMetaAdminOnly writes the error returned when a non-admin tries to change an
// identity meta key.
func writeIdentityMetaAdminOnly(w http.ResponseWriter) {
	errors.ErrUnauthorized.Withf("only organization admins can change the organization name, logo or description").Write(w)
}

// writeMetaUpdateError writes the error of a failed meta update: a path MongoDB refused is a
// client fault, anything else is internal.
func writeMetaUpdateError(w http.ResponseWriter, err error) {
	if errors.Is(err, db.ErrInvalidMetaPath) {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	errors.ErrGenericInternalServerError.WithErr(err).Write(w)
}

// addOrganizationMetaHandler godoc
//
//	@Summary		Add meta information to an organization
//	@Description	Overwrite the meta information of an organization. Requires Manager/Admin role.
//	@Description	Only admins may change the organization identity keys (name, logo, description): a manager's
//	@Description	request must carry them exactly as they are stored, or it is rejected with 401.
//	@Description	Keys cannot be empty, contain dots or start with "$", at any level, for any role (400), except that
//	@Description	the nested values of a top-level key left unchanged from the stored one are accepted as they are.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationAddMetaRequest	true	"Meta information to add to the organization"
//	@Success		200			{string}	string									"OK"
//	@Failure		400			{object}	errors.Error							"Invalid meta information (e.g. a key containing a dot)"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
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
	// the whole map is stored as-is, so a dotted key would become a literal "a.b" field; later PUTs
	// treat dots as paths, which would either write into it (e.g. a planted "name.default" becoming
	// the org name) or fail with a path conflict, and DELETE could never remove it. Top-level keys
	// are always checked, nested values only when they change.
	for key := range meta.Meta {
		if err := apicommon.ValidateMetaKey(key); err != nil {
			errors.ErrInvalidUserData.WithErr(err).Write(w)
			return
		}
	}
	if err := validateChangedMetaValues(org.Meta, meta.Meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	isAdmin := user.HasRoleFor(org.Address, db.AdminRole)
	if !isAdmin && identityMetaReplaced(org.Meta, meta.Meta) {
		writeIdentityMetaAdminOnly(w)
		return
	}
	// Update the organization meta information
	var err error
	if isAdmin {
		err = a.db.AddOrganizationMeta(org.Address, meta.Meta)
	} else {
		// the identity keys were just verified as unchanged; keeping their stored values inside the
		// update stops a concurrent admin rename from being reverted
		err = a.db.ReplaceOrganizationMetaKeeping(org.Address, meta.Meta, apicommon.IdentityMetaKeys)
	}
	if err != nil {
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
//	@Description	Keys are dot paths: an empty or "$"-prefixed segment, or a key that is a prefix of another, is rejected with 400.
//	@Description	Nested keys cannot be empty, contain dots or start with "$" (400), unless the top-level key's value
//	@Description	is unchanged from the stored one, which is accepted as it is.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationAddMetaRequest	true	"Meta information to update in the organization"
//	@Success		200			{string}	string									"OK"
//	@Failure		400			{object}	errors.Error							"Invalid meta information (e.g. an empty path segment or conflicting keys)"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
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
	if err := validateMetaPaths(slices.Collect(maps.Keys(meta.Meta))); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	if err := validateChangedMetaValues(org.Meta, meta.Meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	isAdmin := user.HasRoleFor(org.Address, db.AdminRole)
	if !isAdmin {
		if identityMetaUpdated(org.Meta, meta.Meta) {
			writeIdentityMetaAdminOnly(w)
			return
		}
		// identity keys resent with their stored values are verified unchanged: leaving them out
		// keeps this write from reverting a concurrent admin change
		maps.DeleteFunc(meta.Meta, func(key string, _ any) bool {
			return slices.Contains(apicommon.IdentityMetaKeys, key)
		})
	}
	// Update the organization meta information
	if err := a.db.UpdateOrganizationMeta(org.Address, meta.Meta); err != nil {
		writeMetaUpdateError(w, err)
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
//	@Description	(name, logo, description); a manager's request naming one of them that is stored is rejected with 401.
//	@Description	Keys are dot paths: an empty or "$"-prefixed segment, or a key that is a prefix of another, is rejected with 400.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			request		body		apicommon.OrganizationDeleteMetaRequest	true	"Keys to delete from the organization meta"
//	@Success		200			{string}	string									"OK"
//	@Failure		400			{object}	errors.Error							"Invalid meta information (e.g. an empty path segment or conflicting keys)"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		403			{object}	errors.Error							"Forbidden - User is not a manager or admin of the organization"
//	@Failure		404			{object}	errors.Error							"Organization not found"
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
	if err := validateMetaPaths(meta.Keys); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	keys := meta.Keys
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		if slices.ContainsFunc(meta.Keys, func(key string) bool {
			return isIdentityMetaKey(key) && identityFieldStored(org.Meta, key)
		}) {
			writeIdentityMetaAdminOnly(w)
			return
		}
		// an identity key with no stored field is a no-op: leaving it out keeps this request from
		// unsetting a value a concurrent admin has just added
		keys = slices.DeleteFunc(slices.Clone(meta.Keys), isIdentityMetaKey)
	}
	// Update the organization meta information
	if err := a.db.DeleteOrganizationMetaKeys(org.Address, keys); err != nil {
		writeMetaUpdateError(w, err)
		return
	}
	apicommon.HTTPWriteOK(w)
}
