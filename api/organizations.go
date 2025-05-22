package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/subscriptions"
)

// createOrganizationHandler godoc
//
//	@Summary		Create a new organization
//	@Description	Create a new organization. If the organization is a suborganization, the parent organization must be
//	@Description	specified in the request body, and the user must be an admin of the parent. If the parent organization
//	@Description	is already a suborganization, an error is returned.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.OrganizationInfo	true	"Organization information"
//	@Success		200		{object}	apicommon.OrganizationInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Parent organization not found"
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
	orgInfo := &apicommon.OrganizationInfo{}
	if err := json.NewDecoder(r.Body).Decode(orgInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// create the organization signer to store the address and the nonce
	signer, nonce, err := account.NewSigner(a.secret, user.Email) // TODO: replace email with something else such as user ID
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not create organization signer: %v", err).Write(w)
		return
	}
	// check if the organization type is valid
	if !db.IsOrganizationTypeValid(orgInfo.Type) {
		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
		return
	}
	parentOrg := ""
	var dbParentOrg *db.Organization
	if orgInfo.Parent != nil {
		// check if the org has permission to create suborganizations
		hasPermission, err := a.subscriptions.HasDBPersmission(user.Email, orgInfo.Parent.Address, subscriptions.CreateSubOrg)
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
		if dbParentOrg.Parent != "" {
			errors.ErrMalformedBody.Withf("parent organization is already a suborganization").Write(w)
			return
		}
		isAdmin, err := a.db.IsMemberOf(user.Email, dbParentOrg.Address, db.AdminRole)
		if err != nil {
			errors.ErrGenericInternalServerError.Withf("could not check if user is admin of parent organization: %v", err).Write(w)
			return
		}
		if !isAdmin {
			errors.ErrUnauthorized.Withf("user is not admin of parent organization").Write(w)
			return
		}
		parentOrg = orgInfo.Parent.Address
	}
	// find default plan
	defaultPlan, err := a.db.DefaultPlan()
	if err != nil || defaultPlan == nil {
		errors.ErrNoDefaultPlan.WithErr((err)).Write(w)
		return
	}
	subscription := &db.OrganizationSubscription{
		PlanID:        defaultPlan.ID,
		StartDate:     time.Now(),
		Active:        true,
		MaxCensusSize: defaultPlan.Organization.MaxCensus,
	}
	// create the organization
	dbOrg := &db.Organization{
		Address:         signer.AddressString(),
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
		TokensPurchased: 0,
		TokensRemaining: 0,
		Parent:          parentOrg,
		Subscription:    *subscription,
	}
	if err := a.db.SetOrganization(dbOrg); err != nil {
		if err == db.ErrAlreadyExists {
			errors.ErrInvalidOrganizationData.WithErr(err).Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Write(w)
		return
	}

	// update the parent organization counter
	if orgInfo.Parent != nil {
		dbParentOrg.Counters.SubOrgs++
		if err := a.db.SetOrganization(dbParentOrg); err != nil {
			errors.ErrGenericInternalServerError.Withf("could not update parent organization: %v", err).Write(w)
			return
		}
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
//	@Description	Update the information of an organization. Only the admin of the organization can update the information.
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
		errors.ErrNoOrganizationSubscription.Write(w)
		return
	}
	// get the subscription from the database
	plan, err := a.db.Plan(org.Subscription.PlanID)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get subscription: %v", err).Write(w)
		return
	}
	info := &apicommon.OrganizationSubscriptionInfo{
		SubcriptionDetails: apicommon.SubscriptionDetailsFromDB(&org.Subscription),
		Usage:              apicommon.SubscriptionUsageFromDB(&org.Counters),
		Plan:               apicommon.SubscriptionPlanFromDB(plan),
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
//	@Description	Create a new ticket for an organization. The user must be a member of the organization with any role.
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
	if !user.HasRoleFor(org.Address, db.AnyRole) {
		errors.ErrUnauthorized.Withf("user is not a member of organization").Write(w)
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
	notification, err := mailtemplates.SupportNotification.ExecTemplate(
		struct {
			Type         string
			Organization string
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

	// send an email to the support destination
	if err := a.mail.SendNotification(r.Context(), notification); err != nil {
		log.Warnw("could not send ticket notification email", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
