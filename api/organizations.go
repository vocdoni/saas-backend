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
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/log"
)

// createOrganizationHandler godoc
//
//	@Summary		Create a new organization
//	@Description	Create a new organization. If the organization is a suborganization, the parent organization must be
//	@Description	specified in the request body, and the user must be an admin of the parent. If the parent organization
//	@Description	is already a suborganization, an error is returned.
//	@Description
//	@Description	Two optional opt-in fields support the chain-abstracted flow (both default false, preserving the legacy
//	@Description	behavior): `provisionAccount` forges the organization's on-chain account eagerly at creation time
//	@Description	(idempotent) instead of leaving it to the legacy SDK createAccount path; `integrator` subscribes the
//	@Description	new organization to the free integrator plan so it becomes an integrator on the free tier with no
//	@Description	checkout.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateOrganizationRequest	true	"Organization info (+ optional provisionAccount / integrator)"
//	@Success		200		{object}	apicommon.OrganizationInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Parent org not found, or free integrator plan unavailable (integrator=true)"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations [post]
func (a *API) createOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request body
	orgInfo := &apicommon.CreateOrganizationRequest{}
	if err := json.NewDecoder(r.Body).Decode(orgInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// create the organization signer to store the address and the nonce
	// TODO: replace email with something else such as user ID
	signer, nonce, err := account.NewSigner(a.secret, user.Email)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not create organization signer: %v", err).Write(w)
		return
	}
	// check if the organization type is valid
	if !db.IsOrganizationTypeValid(orgInfo.Type) {
		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
		return
	}
	// find default plan
	defaultPlan, err := a.db.DefaultPlan()
	if err != nil || defaultPlan == nil {
		errors.ErrNoDefaultPlan.WithErr(err).Write(w)
		return
	}
	// integrator portal opt-in: subscribe the new org to the free integrator plan so it
	// becomes an integrator on the free tier with no checkout (see Subscriptions.IsIntegrator).
	selectedPlan := defaultPlan
	if orgInfo.Integrator {
		freePlan, err := a.db.FreeIntegratorPlan()
		if err != nil || freePlan == nil {
			errors.ErrPlanNotFound.Withf("free integrator plan not available").Write(w)
			return
		}
		selectedPlan = freePlan
	}
	// check if the user has permission to create new organizations
	hasPermission, err := a.subscriptions.HasDBPermission(user.Email, common.Address{}, subscriptions.CreateOrg)
	if !hasPermission || err != nil {
		errors.ErrUnauthorized.WithErr(err).Write(w)
		return
	}
	parentOrg := common.Address{}
	var dbParentOrg *db.Organization
	if orgInfo.Parent != nil {
		// check if the org has permission to create suborganizations
		hasPermission, err := a.subscriptions.HasDBPermission(user.Email, orgInfo.Parent.Address, subscriptions.CreateSubOrg)
		if !hasPermission || err != nil {
			errors.ErrUnauthorized.Withf("user does not have permission to create suborganizations: %v", err).Write(w)
			return
		}

		dbParentOrg, err = a.db.Organization(orgInfo.Parent.Address)
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrOrganizationNotFound.Withf("parent organization not found").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.Withf("could not get parent organization: %v", err).Write(w)
			return
		}
		if len(dbParentOrg.Parent) > 0 {
			errors.ErrMalformedBody.Withf("parent organization is already a suborganization").Write(w)
			return
		}
		isAdmin, err := a.db.UserHasRoleInOrg(user.Email, dbParentOrg.Address, db.AdminRole)
		if err != nil {
			errors.ErrGenericInternalServerError.
				Withf("could not check if user is admin of parent organization: %v", err).
				Write(w)
			return
		}
		if !isAdmin {
			errors.ErrUnauthorized.Withf("user is not admin of parent organization").Write(w)
			return
		}
		parentOrg = orgInfo.Parent.Address
		// update the parent organization counter
		if err := a.db.IncrementOrganizationSubOrgsCounter(parentOrg); err != nil {
			errors.ErrGenericInternalServerError.Withf("increment suborgs: %v", err).Write(w)
			return
		}
	}
	// create the organization
	dbOrg := &db.Organization{
		Address:         signer.Address(),
		Website:         orgInfo.Website,
		Creator:         user.Email,
		CreatedAt:       time.Now(),
		Nonce:           nonce,
		Type:            db.OrganizationType(orgInfo.Type),
		Size:            orgInfo.Size,
		Color:           orgInfo.Color,
		Country:         orgInfo.Country,
		Subdomain:       orgInfo.Subdomain,
		Timezone:        orgInfo.Timezone,
		Active:          true,
		Communications:  orgInfo.Communications,
		Meta:            apicommon.BuildOrgMeta(nil, orgInfo.Name, orgInfo.Logo, orgInfo.Description, orgInfo.Meta),
		TokensPurchased: 0,
		TokensRemaining: 0,
		Parent:          parentOrg,
		Subscription: db.OrganizationSubscription{
			PlanID:    selectedPlan.ID,
			StartDate: time.Now(),
			Active:    true,
		},
	}
	// optionally provision the organization's on-chain account (opt-in, eager) BEFORE
	// persisting the DB row. CreateOrgAccount is idempotent, so doing it first means a
	// provisioning failure leaves no orphaned organization (and no creator/parent
	// links) behind. When provisionAccount is false this block is skipped and behavior
	// is identical to the legacy flow (DB row only; account created later by the SDK).
	if orgInfo.ProvisionAccount {
		infoURI := fmt.Sprintf("%s/organizations/%s", a.serverURL, dbOrg.Address.String())
		displayName := apicommon.OrgDisplayName(dbOrg.Meta, dbOrg.Address.String())
		if err := a.account.CreateOrgAccount(signer, displayName, infoURI); err != nil {
			if orgInfo.Parent != nil {
				if err := a.db.DecrementOrganizationSubOrgsCounter(parentOrg); err != nil {
					log.Errorf("decrement suborgs: %v", err)
				}
			}
			errors.ErrGenericInternalServerError.
				Withf("could not provision organization account: %v", err).Write(w)
			return
		}
	}
	if err := a.db.SetOrganization(dbOrg); err != nil {
		if orgInfo.Parent != nil {
			if err := a.db.DecrementOrganizationSubOrgsCounter(parentOrg); err != nil {
				log.Errorf("decrement suborgs: %v", err)
			}
		}
		if err == db.ErrAlreadyExists {
			errors.ErrInvalidOrganizationData.WithErr(err).Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	// send the organization back to the user
	apicommon.HTTPWriteJSON(w, apicommon.OrganizationFromDB(dbOrg, dbParentOrg))
}

// organizationInfoHandler godoc
//
//	@Summary		Get organization information
//	@Description	Get information about an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Param			address	path		string	true	"Organization address"
//	@Success		200		{object}	apicommon.OrganizationInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address} [get]
func (a *API) organizationInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, parent, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// send the organization back to the user
	apicommon.HTTPWriteJSON(w, apicommon.OrganizationFromDB(org, parent))
}

// updateOrganizationHandler godoc
//
//	@Summary		Update organization information
//	@Description	Update the information of an organization.
//	@Description	Only the admin of the organization can update the information.
//	@Description	Only certain fields can be updated, and they will be updated only if they are not empty.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string						true	"Organization address"
//	@Param			request	body		apicommon.OrganizationInfo	true	"Organization information to update"
//	@Success		200		{string}	string						"OK"
//	@Failure		400		{object}	errors.Error				"Invalid input data"
//	@Failure		401		{object}	errors.Error				"Unauthorized"
//	@Failure		404		{object}	errors.Error				"Organization not found"
//	@Failure		500		{object}	errors.Error				"Internal server error"
//	@Router			/organizations/{address} [put]
func (a *API) updateOrganizationHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// get the organization info from the request body
	newOrgInfo := &apicommon.OrganizationInfo{}
	if err := json.NewDecoder(r.Body).Decode(newOrgInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// update just the fields that can be updated and are not empty
	updateOrg := false
	if newOrgInfo.Website != "" {
		org.Website = newOrgInfo.Website
		updateOrg = true
	}
	if newOrgInfo.Size != "" {
		org.Size = newOrgInfo.Size
		updateOrg = true
	}
	if newOrgInfo.Color != "" {
		org.Color = newOrgInfo.Color
		updateOrg = true
	}
	if newOrgInfo.Subdomain != "" {
		org.Subdomain = newOrgInfo.Subdomain
		updateOrg = true
	}
	if newOrgInfo.Country != "" {
		org.Country = newOrgInfo.Country
		updateOrg = true
	}
	if newOrgInfo.Timezone != "" {
		org.Timezone = newOrgInfo.Timezone
		updateOrg = true
	}
	if newOrgInfo.Active != org.Active {
		org.Active = newOrgInfo.Active
		updateOrg = true
	}
	if newOrgInfo.Name != nil || newOrgInfo.Logo != nil || newOrgInfo.Description != nil || len(newOrgInfo.Meta) > 0 {
		org.Meta = apicommon.BuildOrgMeta(org.Meta, newOrgInfo.Name, newOrgInfo.Logo, newOrgInfo.Description, newOrgInfo.Meta)
		updateOrg = true
	}
	// update the organization if any field was changed
	if updateOrg {
		if err := a.db.SetOrganization(org); err != nil {
			errors.ErrGenericInternalServerError.Withf("could not update organization: %v", err).Write(w)
			return
		}
	}
	apicommon.HTTPWriteOK(w)
}

// organizationsTypesHandler godoc
//
//	@Summary		Get available organization types
//	@Description	Get the list of available organization types that can be assigned to an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	apicommon.OrganizationTypeList
//	@Router			/organizations/types [get]
func (*API) organizationsTypesHandler(w http.ResponseWriter, _ *http.Request) {
	organizationTypes := []*apicommon.OrganizationType{}
	for orgType, name := range db.OrganizationTypesNames {
		organizationTypes = append(organizationTypes, &apicommon.OrganizationType{
			Type: string(orgType),
			Name: name,
		})
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationTypeList{Types: organizationTypes})
}

// organizationSubscriptionHandler godoc
//
//	@Summary		Get organization subscription
//	@Description	Get the subscription information for an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Success		200		{object}	apicommon.OrganizationSubscriptionInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found or no subscription"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/subscription [get]
func (a *API) organizationSubscriptionHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	if org.Subscription == (db.OrganizationSubscription{}) {
		errors.ErrOrganizationHasNoSubscription.Write(w)
		return
	}
	// get the subscription from the database
	plan, err := a.db.Plan(org.Subscription.PlanID)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get subscription: %v", err).Write(w)
		return
	}
	info := &apicommon.OrganizationSubscriptionInfo{
		SubscriptionDetails: apicommon.SubscriptionDetailsFromDB(&org.Subscription),
		Usage:               apicommon.SubscriptionUsageFromDB(&org.Counters),
		Plan:                apicommon.SubscriptionPlanFromDB(plan),
	}
	apicommon.HTTPWriteJSON(w, info)
}

// organizationCensusesHandler godoc
//
//	@Summary		Get organization censuses
//	@Description	Get the list of censuses for an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Success		200		{object}	apicommon.OrganizationCensuses
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/censuses [get]
func (a *API) organizationCensusesHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// get the censuses from the database
	censuses, err := a.db.CensusesByOrg(org.Address)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get censuses: %v", err).Write(w)
		return
	}
	// decode the censuses from the database
	result := apicommon.OrganizationCensuses{
		Censuses: []apicommon.OrganizationCensus{},
	}
	for _, census := range censuses {
		result.Censuses = append(result.Censuses, apicommon.OrganizationCensusFromDB(census))
	}
	apicommon.HTTPWriteJSON(w, result)
}

// organizationCreateTicket godoc
//
//	@Summary		Create a new ticket for an organization
//	@Description	Create a new ticket for an organization. The user must have some role in the organization (any role).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string										true	"Organization address"
//	@Param			request	body		apicommon.CreateOrganizationTicketRequest	true	"Ticket request information"
//	@Success		200		{string}	string										"OK"
//	@Failure		400		{object}	errors.Error								"Invalid input data"
//	@Failure		401		{object}	errors.Error								"Unauthorized"
//	@Failure		404		{object}	errors.Error								"Organization not found"
//	@Failure		500		{object}	errors.Error								"Internal server error"
//	@Router			/organizations/{address}/ticket [post]
func (a *API) organizationCreateTicket(w http.ResponseWriter, r *http.Request) {
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
	// check if the new user already has a role in the organization
	if hasAnyRole, err := a.db.UserHasAnyRoleInOrg(user.Email, org.Address); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	} else if !hasAnyRole {
		errors.ErrUnauthorized.Withf("user has no role in the organization").Write(w)
		return
	}

	// get the ticket request from the request body
	ticketReq := &apicommon.CreateOrganizationTicketRequest{}
	if err := json.NewDecoder(r.Body).Decode(ticketReq); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// validate the ticket request
	if ticketReq.Title == "" || ticketReq.Description == "" {
		errors.ErrMalformedBody.With("title and description are required").Write(w)
		return
	}

	if !internal.ValidEmail(user.Email) {
		errors.ErrEmailMalformed.With("invalid user email address").Write(w)
		return
	}
	lang := a.getLanguageFromContext(r.Context())
	notification, err := mailtemplates.SupportNotification.Localized(lang).ExecTemplate(
		struct {
			Type         string
			Organization common.Address
			Title        string
			Description  string
			Email        string
		}{ticketReq.TicketType, org.Address, ticketReq.Title, ticketReq.Description, user.Email},
	)
	if err != nil {
		log.Warnw("could not execute support notification template", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}

	notification.ToAddress = apicommon.SupportEmail
	notification.ReplyTo = user.Email
	notification.CCAddress = user.Email

	if a.notifyQueue == nil {
		errors.ErrGenericInternalServerError.With("no notification queue configured").Write(w)
		return
	}
	if err := a.notifyQueue.Push(&notifications.QueueItem{
		Notification: notification,
		Type:         notifications.Email,
		Label:        user.Email,
	}); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}

// organizationJobsHandler godoc
//
//	@Summary		Get organization jobs
//	@Description	Get the list of import jobs for an organization with pagination support
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			page	query		integer	false	"Page number (default: 1)"
//	@Param			limit	query		integer	false	"Number of items per page (default: 10)"
//	@Param			type	query		string	false	"Filter by job type (org_members or census_participants)"
//	@Success		200		{object}	apicommon.JobsResponse
//	@Failure		400		{object}	errors.Error	"Invalid input"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/jobs [get]
func (a *API) organizationJobsHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of organization").Write(w)
		return
	}

	// Parse job type filter
	var jobType *db.JobType
	if typeStr := r.URL.Query().Get("type"); typeStr != "" {
		switch typeStr {
		case string(db.JobTypeOrgMembers):
			t := db.JobTypeOrgMembers
			jobType = &t
		case string(db.JobTypeCensusParticipants):
			t := db.JobTypeCensusParticipants
			jobType = &t
		default:
			errors.ErrMalformedURLParam.Withf("invalid job type: %s", typeStr).Write(w)
			return
		}
	}

	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	totalItems, jobs, err := a.db.Jobs(org.Address, params.Page, params.Limit, jobType)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get jobs: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	// convert the jobs to the response format
	jobsResponse := make([]apicommon.JobInfo, 0, len(jobs))
	for _, job := range jobs {
		jobsResponse = append(jobsResponse, apicommon.JobFromDB(&job))
	}

	apicommon.HTTPWriteJSON(w, &apicommon.JobsResponse{
		Pagination: pagination,
		Jobs:       jobsResponse,
	})
}

// organizationBundlesHandler godoc
//
//	@Summary		List organization bundles and their main process
//	@Description	Returns a paginated list of process bundles for an organization.
//	@Description	Each entry contains the bundle ID, the primary vochain process ID
//	@Description	(first element of Processes when present), and all process IDs in the bundle.
//	@Description	Requires the user to be part of the organization (any role).
//	@Tags			process
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			page	query		int		false	"Page number (default 1)"
//	@Param			limit	query		int		false	"Items per page (default 10, max 100)"
//	@Success		200		{object}	apicommon.ListOrganizationBundles
//	@Failure		400		{object}	errors.Error	"Invalid request or organization not found"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Deprecated
//	@Router	/organizations/{address}/processes [get]
func (a *API) organizationBundlesHandler(w http.ResponseWriter, r *http.Request) {
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
	// check if the user belongs to the organization
	if _, ok := user.RoleFor(org.Address); !ok {
		errors.ErrUnauthorized.Withf("user does not belong to the organization").Write(w)
		return
	}
	// parse pagination params
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	// get paginated results
	totalItems, bundles, err := a.db.ListOrganizationBundles(org.Address, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get organization bundles: %v", err).Write(w)
		return
	}
	// calculate pagination
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	// parse db bundles to get the primary process ID (the first one in the
	// array) and the list of processes IDs
	finalBundles := []apicommon.OrganizationBundle{}
	for _, bundle := range bundles {
		// skip bundles with no processes
		if len(bundle.Processes) == 0 {
			continue
		}
		// get processes IDs
		processes := make([]string, 0, len(bundle.Processes))
		for _, process := range bundle.Processes {
			processes = append(processes, process.String())
		}
		// add bundle with the primary process and all processes IDs
		finalBundles = append(finalBundles, apicommon.OrganizationBundle{
			BundleID:         bundle.ID.Hex(),
			PrimaryProcessID: bundle.Processes[0].String(),
			Processes:        processes,
		})
	}
	// write response
	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationBundles{
		Pagination: pagination,
		Bundles:    finalBundles,
	})
}
