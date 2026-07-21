package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// createAPIKeyHandler godoc
//
//	@Summary		Create an API key for an organization
//	@Description	Create a new API key owned by the organization at the given address. The caller
//	@Description	must be an admin of the organization, which must be enabled as an integrator —
//	@Description	API keys are integrator-only. The plaintext secret (prefixed "vsk_") is
//	@Description	returned ONCE in the response and cannot be retrieved again.
//	@Description
//	@Description	`label` and at least one `scope` are required. Valid scopes are deny-by-default and
//	@Description	must be a subset of: `quota:read`, `managed:read`, `managed:write`, `voting:write`,
//	@Description	`members:write`. The optional `expiresAt`, when set, must be in the future.
//	@Tags			integrator
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string							true	"Organization address"
//	@Param			request		body		apicommon.CreateAPIKeyRequest	true	"API key information"
//	@Success		200			{object}	apicommon.CreateAPIKeyResponse	"Created key including the one-time plaintext secret"
//	@Failure		400			{object}	errors.Error					"Invalid input (bad address, missing label/scopes, unknown scope, past expiresAt)"
//	@Failure		401			{object}	errors.Error					"Unauthorized"
//	@Failure		403			{object}	errors.Error					"Organization is not an integrator"
//	@Failure		404			{object}	errors.Error					"Organization not found"
//	@Failure		500			{object}	errors.Error					"Internal server error"
//	@Router			/integrator/organizations/{orgAddress}/apikeys [post]
func (a *API) createAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// validate the address up front: common.HexToAddress silently maps a malformed {orgAddress}
	// to the zero address, which would otherwise surface as a confusing 401/404 instead of 400.
	addr := chi.URLParam(r, "orgAddress")
	if !common.IsHexAddress(addr) {
		errors.ErrMalformedURLParam.With("invalid organization address").Write(w)
		return
	}
	orgAddr := common.HexToAddress(addr)
	if !user.HasRoleFor(orgAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	// API keys are an integrator capability: only integrator organizations may mint them.
	// An admin of any org that is not integrator-enabled is rejected here, even though they
	// are an admin, because the API-key scope set (quota/managed/voting/members) is
	// integrator-oriented. Integrator status is determined by subscriptions.IsIntegrator,
	// which accounts for per-org IntegratorLimits overrides and the active plan's limits.
	org, err := a.db.Organization(orgAddr)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !a.subscriptions.IsIntegrator(org) {
		errors.ErrNotAnIntegrator.Write(w)
		return
	}
	req := &apicommon.CreateAPIKeyRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if req.Label == "" {
		errors.ErrMalformedBody.Withf("label is required").Write(w)
		return
	}
	if len(req.Scopes) == 0 {
		errors.ErrMalformedBody.Withf("at least one scope is required").Write(w)
		return
	}
	for _, s := range req.Scopes {
		if !IsValidAPIKeyScope(s) {
			errors.ErrInvalidAPIKeyScope.Withf("unknown scope %q", s).Write(w)
			return
		}
	}
	if req.ExpiresAt != nil && !req.ExpiresAt.After(time.Now()) {
		errors.ErrMalformedBody.Withf("expiresAt must be in the future").Write(w)
		return
	}
	gen, err := generateAPIKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not generate API key: %v", err).Write(w)
		return
	}
	key := &db.APIKey{
		ID:         uuid.NewString(),
		OrgAddress: orgAddr,
		Label:      req.Label,
		Prefix:     gen.prefix,
		Hash:       gen.hash,
		Scopes:     req.Scopes,
		CreatedBy:  user.Email,
		CreatedAt:  time.Now(),
		ExpiresAt:  req.ExpiresAt,
		Revoked:    false,
	}
	if err := a.db.SetAPIKey(key); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not store API key: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.CreateAPIKeyResponse{
		APIKeyInfo: apicommon.APIKeyInfoFromDB(key),
		Secret:     gen.secret,
	})
}

// apiKeysHandler godoc
//
//	@Summary		List an organization's API keys
//	@Description	Returns the metadata of all API keys owned by the organization (never the secret).
//	@Description	The caller must be an admin of the organization.
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Success		200			{object}	apicommon.ListAPIKeysResponse
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations/{orgAddress}/apikeys [get]
func (a *API) apiKeysHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	orgAddr := common.HexToAddress(chi.URLParam(r, "orgAddress"))
	if !user.HasRoleFor(orgAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	keys, err := a.db.APIKeysByOrg(orgAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not list API keys: %v", err).Write(w)
		return
	}
	list := make([]apicommon.APIKeyInfo, 0, len(keys))
	for _, k := range keys {
		list = append(list, apicommon.APIKeyInfoFromDB(k))
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ListAPIKeysResponse{APIKeys: list})
}

// revokeAPIKeyHandler godoc
//
//	@Summary		Revoke an organization's API key
//	@Description	Revokes (permanently disables) the API key with the given ID. The caller must be
//	@Description	an admin of the organization.
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string			true	"Organization address"
//	@Param			keyId		path		string			true	"API key ID"
//	@Success		200			{string}	string			"OK"
//	@Failure		400			{object}	errors.Error	"Invalid input data (missing key ID)"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"API key not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations/{orgAddress}/apikeys/{keyId} [delete]
func (a *API) revokeAPIKeyHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	orgAddr := common.HexToAddress(chi.URLParam(r, "orgAddress"))
	if !user.HasRoleFor(orgAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	keyID := chi.URLParam(r, "keyId")
	if keyID == "" {
		errors.ErrMalformedURLParam.Withf("keyId is required").Write(w)
		return
	}
	if err := a.db.RevokeAPIKey(orgAddr, keyID); err != nil {
		if err == db.ErrNotFound {
			errors.ErrAPIKeyNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not revoke API key: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
