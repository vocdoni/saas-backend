package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
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

		dbParentOrg, _, err = a.db.Organization(orgInfo.Parent.Address, false)
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

// organizationMembersHandler godoc
//
//	@Summary		Get organization members
//	@Description	Get the list of members with their roles in the organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Success		200		{object}	apicommon.OrganizationMembers
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members [get]
func (a *API) organizationMembersHandler(w http.ResponseWriter, r *http.Request) {
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
	// send the organization back to the user
	members, err := a.db.OrganizationsMembers(org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get organization members: %v", err).Write(w)
		return
	}
	orgMembers := apicommon.OrganizationMembers{
		Members: make([]*apicommon.OrganizationMember, 0, len(members)),
	}
	for _, member := range members {
		var role string
		for _, userOrg := range member.Organizations {
			if userOrg.Address == org.Address {
				role = string(userOrg.Role)
				break
			}
		}
		if role == "" {
			continue
		}
		orgMembers.Members = append(orgMembers.Members, &apicommon.OrganizationMember{
			Info: &apicommon.UserInfo{
				Email:     member.Email,
				FirstName: member.FirstName,
				LastName:  member.LastName,
			},
			Role: role,
		})
	}
	apicommon.HTTPWriteJSON(w, orgMembers)
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

// inviteOrganizationMemberHandler godoc
//
//	@Summary		Invite a new member to an organization
//	@Description	Invite a new member to an organization. Only the admin of the organization can invite a new member.
//	@Description	It stores the invitation in the database and sends an email to the new member with the invitation code.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string							true	"Organization address"
//	@Param			request	body		apicommon.OrganizationInvite	true	"Invitation information"
//	@Success		200		{string}	string							"OK"
//	@Failure		400		{object}	errors.Error					"Invalid input data"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		409		{object}	errors.Error					"User is already a member of the organization"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/organizations/{address}/members [post]
func (a *API) inviteOrganizationMemberHandler(w http.ResponseWriter, r *http.Request) {
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

	// check if the user/org has permission to invite members
	hasPermission, err := a.subscriptions.HasDBPersmission(user.Email, org.Address, subscriptions.InviteMember)
	if !hasPermission || err != nil {
		errors.ErrUnauthorized.Withf("user does not have permission to sign transactions: %v", err).Write(w)
		return
	}
	// get new admin info from the request body
	invite := &apicommon.OrganizationInvite{}
	if err := json.NewDecoder(r.Body).Decode(invite); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// check the email is correct format
	if !internal.ValidEmail(invite.Email) {
		errors.ErrEmailMalformed.Write(w)
		return
	}
	// check the role is valid
	if valid := db.IsValidUserRole(db.UserRole(invite.Role)); !valid {
		errors.ErrInvalidUserData.Withf("invalid role").Write(w)
		return
	}
	// check if the new user is already a member of the organization
	if _, err := a.db.IsMemberOf(invite.Email, org.Address, db.AdminRole); err == nil {
		errors.ErrDuplicateConflict.With("user is already admin of organization").Write(w)
		return
	}
	// create new invitation
	orgInvite := &db.OrganizationInvite{
		OrganizationAddress: org.Address,
		NewUserEmail:        invite.Email,
		Role:                db.UserRole(invite.Role),
		CurrentUserID:       user.ID,
	}
	// generate the verification code and the verification link
	code, link, err := a.generateVerificationCodeAndLink(orgInvite, db.CodeTypeOrgInvite)
	if err != nil {
		if err == db.ErrAlreadyExists {
			errors.ErrDuplicateConflict.With("user is already invited to the organization").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not create the invite: %v", err).Write(w)
		return
	}
	// send the invitation mail to invited user email with the invite code and
	// the invite link
	if err := a.sendMail(r.Context(), invite.Email, mailtemplates.InviteNotification,
		struct {
			Organization string
			Code         string
			Link         string
		}{org.Address, code, link},
	); err != nil {
		log.Warnw("could not send verification code email", "error", err)
		errors.ErrGenericInternalServerError.Write(w)
		return
	}

	// update the org members counter
	org.Counters.Members++
	if err := a.db.SetOrganization(org); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not update organization: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// acceptOrganizationMemberInvitationHandler godoc
//
//	@Summary		Accept an invitation to an organization
//	@Description	Accept an invitation to an organization. It checks if the invitation is valid and not expired, and if the
//	@Description	user is not already a member of the organization. If the user does not exist, it creates a new user with
//	@Description	the provided information. If the user already exists and is verified, it adds the organization to the user.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Param			address	path		string									true	"Organization address"
//	@Param			request	body		apicommon.AcceptOrganizationInvitation	true	"Invitation acceptance information"
//	@Success		200		{string}	string									"OK"
//	@Failure		400		{object}	errors.Error							"Invalid input data"
//	@Failure		401		{object}	errors.Error							"Unauthorized or invalid invitation"
//	@Failure		409		{object}	errors.Error							"User is already a member of the organization"
//	@Failure		410		{object}	errors.Error							"Invitation expired"
//	@Failure		500		{object}	errors.Error							"Internal server error"
//	@Router			/organizations/{address}/members/accept [post]
func (a *API) acceptOrganizationMemberInvitationHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get new member info from the request body
	invitationReq := &apicommon.AcceptOrganizationInvitation{}
	if err := json.NewDecoder(r.Body).Decode(invitationReq); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	// get the invitation from the database
	invitation, err := a.db.Invitation(invitationReq.Code)
	if err != nil {
		errors.ErrUnauthorized.Withf("could not get invitation: %v", err).Write(w)
		return
	}
	// check if the organization is correct
	if invitation.OrganizationAddress != org.Address {
		errors.ErrUnauthorized.Withf("invitation is not for this organization").Write(w)
		return
	}
	// create a helper function to remove the invitation from the database in
	// case of error or expiration
	removeInvitation := func() {
		if err := a.db.DeleteInvitation(invitationReq.Code); err != nil {
			log.Warnf("could not delete invitation: %v", err)
		}
	}
	// check if the invitation is expired
	if invitation.Expiration.Before(time.Now()) {
		go removeInvitation()
		errors.ErrInvitationExpired.Write(w)
		return
	}
	// try to get the user from the database
	dbUser, err := a.db.UserByEmail(invitation.NewUserEmail)
	if err != nil {
		// if the error is different from not found, return the error, if not,
		// continue to try to create the user
		if err != db.ErrNotFound {
			errors.ErrGenericInternalServerError.Withf("could not get user: %v", err).Write(w)
			return
		}
		// check if the user info is provided, at least the first name, last
		// name and the password, the email is already checked in the invitation
		if invitationReq.User == nil || invitationReq.User.FirstName == "" ||
			invitationReq.User.LastName == "" || invitationReq.User.Password == "" {
			errors.ErrMalformedBody.With("user info not provided").Write(w)
			return
		}
		// create the new user and move on to include the organization, the user
		// is verified because it is an invitation and the email is already
		// checked in the invitation so just hash the password and create the
		// user with the first name and last name provided
		hPassword := internal.HexHashPassword(passwordSalt, invitationReq.User.Password)
		dbUser = &db.User{
			Email:     invitation.NewUserEmail,
			Password:  hPassword,
			FirstName: invitationReq.User.FirstName,
			LastName:  invitationReq.User.LastName,
			Verified:  true,
		}
	} else {
		// if it does, check if the user is already verified
		if !dbUser.Verified {
			errors.ErrUserNoVerified.With("user already exists but is not verified").Write(w)
			return
		}
		// check if the user is already a member of the organization
		if _, err := a.db.IsMemberOf(invitation.NewUserEmail, org.Address, invitation.Role); err == nil {
			go removeInvitation()
			errors.ErrDuplicateConflict.With("user is already admin of organization").Write(w)
			return
		}
	}
	// include the new organization in the user
	dbUser.Organizations = append(dbUser.Organizations, db.OrganizationMember{
		Address: org.Address,
		Role:    invitation.Role,
	})
	// set the user in the database
	if _, err := a.db.SetUser(dbUser); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not set user: %v", err).Write(w)
		return
	}
	// delete the invitation
	go removeInvitation()
	apicommon.HTTPWriteOK(w)
}

// pendingOrganizationMembersHandler godoc
//
//	@Summary		Get pending organization members
//	@Description	Get the list of pending invitations for an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Success		200		{object}	apicommon.OrganizationInviteList
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members/pending [get]
func (a *API) pendingOrganizationMembersHandler(w http.ResponseWriter, r *http.Request) {
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
	// get the pending invitations
	invitations, err := a.db.PendingInvitations(org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get pending invitations: %v", err).Write(w)
		return
	}
	invitationsList := make([]*apicommon.OrganizationInvite, 0, len(invitations))
	for _, invitation := range invitations {
		invitationsList = append(invitationsList, &apicommon.OrganizationInvite{
			Email:      invitation.NewUserEmail,
			Role:       string(invitation.Role),
			Expiration: invitation.Expiration,
		})
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationInviteList{Invites: invitationsList})
}

// organizationsMembersRolesHandler godoc
//
//	@Summary		Get available organization member roles
//	@Description	Get the list of available roles that can be assigned to a member of an organization
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Success		200	{object}	apicommon.OrganizationRoleList
//	@Router			/organizations/roles [get]
func (a *API) organizationsMembersRolesHandler(w http.ResponseWriter, _ *http.Request) {
	availableRoles := []*apicommon.OrganizationRole{}
	for role, name := range db.UserRolesNames {
		availableRoles = append(availableRoles, &apicommon.OrganizationRole{
			Role:            string(role),
			Name:            name,
			WritePermission: db.HasWriteAccess(role),
		})
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationRoleList{Roles: availableRoles})
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
func (a *API) organizationsTypesHandler(w http.ResponseWriter, _ *http.Request) {
	organizationTypes := []*apicommon.OrganizationType{}
	for orgType, name := range db.OrganizationTypesNames {
		organizationTypes = append(organizationTypes, &apicommon.OrganizationType{
			Type: string(orgType),
			Name: name,
		})
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationTypeList{Types: organizationTypes})
}

// getOrganizationSubscriptionHandler godoc
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
func (a *API) getOrganizationSubscriptionHandler(w http.ResponseWriter, r *http.Request) {
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
