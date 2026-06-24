package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// integratorAddress resolves the integrator organization for an integrator-scoped request. The
// integrator endpoints are path-less: the organization is taken from the authenticated principal,
// never from the URL. With an API key it is the key's own org. With a user session (JWT) it is the
// integrator organization the user administers — an integrator manages a single organization (the UI
// does not let an integrator create more), and the user may also administer the managed orgs it
// created, so we select the one that is itself an integrator rather than the first org in the list.
// Writes ErrNotAnIntegrator and returns ok=false when the user has no integrator organization. The
// handlers still enforce the concrete role and quota eligibility on the resolved org.
func (a *API) integratorAddress(w http.ResponseWriter, r *http.Request) (common.Address, bool) {
	if key, ok := apicommon.APIKeyFromContext(r.Context()); ok {
		return key.OrgAddress, true
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return common.Address{}, false
	}
	for _, orgUser := range user.Organizations {
		if orgUser.Role != db.AdminRole && orgUser.Role != db.ManagerRole {
			continue
		}
		org, err := a.db.Organization(orgUser.Address)
		if err != nil {
			continue
		}
		if a.subscriptions.IsIntegrator(org) {
			return org.Address, true
		}
	}
	errors.ErrNotAnIntegrator.Write(w)
	return common.Address{}, false
}

// releaseManagedOrgSlot rolls back a managed-org slot reserved by
// IncrementOrganizationManagedOrgsCounterWithLimit when provisioning or persisting the
// managed org fails. Best-effort: a failed rollback is only logged, never fatal.
func (a *API) releaseManagedOrgSlot(integratorAddr common.Address) {
	if err := a.db.DecrementOrganizationManagedOrgsCounter(integratorAddr); err != nil {
		log.Warnw("could not roll back managed orgs counter", "integrator", integratorAddr.Hex(), "error", err)
	}
}

// createManagedOrganizationHandler godoc
//
//	@Summary		Create a managed organization under an integrator
//	@Description	Create a new organization managed by the caller's integrator organization. The
//	@Description	integrator is resolved from the authenticated principal (the API key's org, or the
//	@Description	user's organization for a session) — no address is passed in the URL. The caller must
//	@Description	be an admin of that integrator organization, which must be enabled as an integrator and
//	@Description	within its managed-organizations quota. The managed org's on-chain account is always
//	@Description	provisioned eagerly. The optional `ownerEmail` designates the managed org's owner/admin
//	@Description	(defaults to the calling user); the per-user MaxOrgsPerUser cap is bypassed for managed orgs.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateManagedOrganizationRequest	true	"Managed organization information"
//	@Success		200		{object}	apicommon.OrganizationInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		403		{object}	errors.Error	"Not an integrator or quota reached"
//	@Failure		404		{object}	errors.Error	"Integrator organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations [post]
func (a *API) createManagedOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the integrator organization").Write(w)
		return
	}
	integrator, err := a.db.Organization(integratorAddr)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// quota / eligibility checks (all enforcement lives in the subscriptions package)
	if err := a.subscriptions.CanCreateManagedOrg(integrator); err != nil {
		if apiErr, ok := err.(errors.Error); ok {
			apiErr.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	limits, err := a.subscriptions.EffectiveIntegratorLimits(integrator)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	req := &apicommon.CreateManagedOrganizationRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if !db.IsOrganizationTypeValid(req.Type) {
		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
		return
	}
	creatorEmail := user.Email
	if req.OwnerEmail != "" {
		// validate the owner exists up front, before provisioning an on-chain account, so a
		// bad ownerEmail fails fast with a 400 instead of orphaning a funded on-chain account.
		if _, err := a.db.UserByEmail(req.OwnerEmail); err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedBody.Withf("owner email does not correspond to an existing user").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		creatorEmail = req.OwnerEmail
	}
	signer, nonce, err := account.NewSigner(a.secret, creatorEmail)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not create organization signer: %v", err).Write(w)
		return
	}
	defaultPlan, err := a.db.DefaultPlan()
	if err != nil || defaultPlan == nil {
		errors.ErrNoDefaultPlan.WithErr(err).Write(w)
		return
	}
	dbOrg := &db.Organization{
		Address:        signer.Address(),
		Website:        req.Website,
		Creator:        creatorEmail,
		CreatedAt:      time.Now(),
		Nonce:          nonce,
		Type:           db.OrganizationType(req.Type),
		Size:           req.Size,
		Color:          req.Color,
		Country:        req.Country,
		Subdomain:      req.Subdomain,
		Timezone:       req.Timezone,
		Active:         true,
		Communications: req.Communications,
		ManagedBy:      integratorAddr,
		Subscription: db.OrganizationSubscription{
			PlanID:    defaultPlan.ID,
			StartDate: time.Now(),
			Active:    true,
		},
	}
	// atomically reserve a managed-org slot BEFORE provisioning the (faucet-funded)
	// on-chain account, so two concurrent creates from the same integrator cannot both
	// pass the stale CanCreateManagedOrg check above and over-provision past the cap.
	if err := a.db.IncrementOrganizationManagedOrgsCounterWithLimit(integratorAddr, limits.MaxManagedOrgs); err != nil {
		if err == db.ErrManagedQuotaReached {
			errors.ErrMaxManagedOrgsReached.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// forge the managed org's on-chain account (always eager) BEFORE persisting the DB
	// row. CreateOrgAccount is idempotent and the address derives from the signer, so a
	// failure here leaves nothing to clean up and the request can be retried safely.
	infoURI := fmt.Sprintf("%s/organizations/%s", a.serverURL, dbOrg.Address.String())
	if err := a.account.CreateOrgAccount(signer, dbOrg.Address.String(), infoURI); err != nil {
		a.releaseManagedOrgSlot(integratorAddr)
		errors.ErrGenericInternalServerError.Withf("could not provision managed organization account: %v", err).Write(w)
		return
	}
	if err := a.db.SetOrganization(dbOrg); err != nil {
		a.releaseManagedOrgSlot(integratorAddr)
		if err == db.ErrAlreadyExists {
			errors.ErrInvalidOrganizationData.WithErr(err).Write(w)
			return
		}
		if err == db.ErrNotFound {
			errors.ErrMalformedBody.Withf("owner email does not correspond to an existing user").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, apicommon.OrganizationFromDB(dbOrg, nil))
}

// managedOrganizationsHandler godoc
//
//	@Summary		List organizations managed by an integrator
//	@Description	Returns a paginated list of organizations managed by the caller's integrator
//	@Description	organization (resolved from the API key's org or the user session — no address in the
//	@Description	URL). The caller must be an admin or manager of the integrator organization.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:read`).
//	@Tags			organizations
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page	query		integer	false	"Page number (default: 1)"
//	@Param			limit	query		integer	false	"Number of items per page (default: 10)"
//	@Success		200		{object}	apicommon.ListManagedOrganizations
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations [get]
func (a *API) managedOrganizationsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) && !user.HasRoleFor(integratorAddr, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the integrator organization").Write(w)
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	totalItems, orgs, err := a.db.ListManagedOrganizations(integratorAddr, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not list managed organizations: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	list := make([]*apicommon.OrganizationInfo, 0, len(orgs))
	for i := range orgs {
		list = append(list, apicommon.OrganizationFromDB(&orgs[i], nil))
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ListManagedOrganizations{
		Pagination:    pagination,
		Organizations: list,
	})
}

// integratorInfoHandler godoc
//
//	@Summary		Get integrator quota and usage
//	@Description	Returns whether the caller's organization (resolved from the API key's org or the user
//	@Description	session — no address in the URL) is enabled as an integrator, along with its effective
//	@Description	managed-resource limits and current usage. The caller must be an admin or manager of the
//	@Description	organization. When the organization is not an integrator, `enabled` is false and `limits`
//	@Description	is omitted (usage counters are still returned). A 0 in any limit field means unlimited.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `quota:read`).
//	@Tags			organizations
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.IntegratorInfoResponse
//	@Failure		400	{object}	errors.Error	"Invalid input data"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Organization not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/integrator [get]
func (a *API) integratorInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) && !user.HasRoleFor(integratorAddr, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the integrator organization").Write(w)
		return
	}
	org, err := a.db.Organization(integratorAddr)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.IntegratorInfoResponse{
		Enabled: a.subscriptions.IsIntegrator(org),
		Usage: apicommon.IntegratorUsage{
			ManagedOrgs:       org.Counters.ManagedOrgs,
			ManagedProcesses:  org.Counters.ManagedProcesses,
			ManagedCensusSize: org.Counters.ManagedCensusSize,
		},
	}
	if resp.Enabled {
		limits, err := a.subscriptions.EffectiveIntegratorLimits(org)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		resp.Limits = &limits
	}
	apicommon.HTTPWriteJSON(w, resp)
}
