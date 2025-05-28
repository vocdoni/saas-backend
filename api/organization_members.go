package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/log"
)

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
				ID:        member.ID,
				Email:     member.Email,
				FirstName: member.FirstName,
				LastName:  member.LastName,
			},
			Role: role,
		})
	}
	apicommon.HTTPWriteJSON(w, orgMembers)
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
//	@Description	Accept an invitation to an organization. It checks if the invitation is valid and not expired,
//	@Description	and if the user is not already a member of the organization.
//	@Description	user is not already a member of the organization. If the user does not exist, it creates a new user with
//	@Description	the provided information. If the user already exists and is verified,
//	@Description	it adds the organization to the user.
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
	invitation, err := a.db.InvitationByCode(invitationReq.Code)
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
		if err := a.db.DeleteInvitationByCode(invitationReq.Code); err != nil {
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

// updatePendingMemberInvitationHandler godoc
//
//	@Summary		Update a pending invitation to an organization
//	@Description	Update the code, link and expiration time of a pending invitation to an organization by email.
//	@Description	Resend the invitation email.
//	@Description	Only the admin of the organization can update an invitation.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address			path		string			true	"Organization address"
//	@Param			invitationID	path		string			true	"Invitation ID"
//	@Success		200				{string}	string			"OK"
//	@Failure		400				{object}	errors.Error	"Invalid input data"
//	@Failure		401				{object}	errors.Error	"Unauthorized"
//	@Failure		400				{object}	errors.Error	"Invalid data - invitation not found"
//	@Failure		500				{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members/pending/{invitationID} [put]
func (a *API) updatePendingMemberInvitationHandler(w http.ResponseWriter, r *http.Request) {
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

	invitationID := chi.URLParam(r, "invitationID")
	if invitationID == "" {
		errors.ErrMalformedBody.With("invitation ID not provided").Write(w)
		return
	}
	// get the invitation from the database
	invitation, err := a.db.Invitation(invitationID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.With("invitation not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get invitation: %v", err).Write(w)
		return
	}

	// create the updated invitation
	orgInvite := &db.OrganizationInvite{
		ID:                  invitation.ID,
		OrganizationAddress: org.Address,
		NewUserEmail:        invitation.NewUserEmail,
		Role:                db.UserRole(invitation.Role),
		CurrentUserID:       user.ID,
	}
	// generate the verification code and the verification link
	code, link, err := a.generateVerificationCodeAndLink(orgInvite, db.CodeTypeOrgInviteUpdate)
	if err != nil {
		if err == db.ErrAlreadyExists {
			errors.ErrDuplicateConflict.With("user is already invited to the organization").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not create the invite: %v", err).Write(w)
		return
	}

	orgInvite.InvitationCode = code
	orgInvite.Expiration = time.Now().Add(apicommon.InvitationExpiration)
	// store the updated invitation in the database
	if err := a.db.UpdateInvitation(orgInvite); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not update invitation: %v", err).Write(w)
		return
	}

	// send the invitation mail to invited user email with the invite code and
	// the invite link
	if err := a.sendMail(r.Context(), orgInvite.NewUserEmail, mailtemplates.InviteNotification,
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
	apicommon.HTTPWriteOK(w)
}

// deletePendingMemberInvitationHandler godoc
//
//	@Summary		Delete a pending invitation to an organization
//	@Description	Delete a pending invitation to an organization by email.
//	@Description	Only the admin of the organization can delete an invitation.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address			path		string			true	"Organization address"
//	@Param			invitationID	path		string			true	"Invitation ID"
//	@Success		200				{string}	string			"OK"
//	@Failure		400				{object}	errors.Error	"Invalid input data"
//	@Failure		401				{object}	errors.Error	"Unauthorized"
//	@Failure		400				{object}	errors.Error	"Invalid data - invitation not found"
//	@Failure		500				{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members/pending/{invitationID} [delete]
func (a *API) deletePendingMemberInvitationHandler(w http.ResponseWriter, r *http.Request) {
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

	invitationID := chi.URLParam(r, "invitationID")
	if invitationID == "" {
		errors.ErrMalformedBody.With("invitation ID not provided").Write(w)
		return
	}
	// get the invitation from the database
	invitation, err := a.db.Invitation(invitationID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.With("invitation not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get invitation: %v", err).Write(w)
		return
	}
	// check if the organization is correct
	if invitation.OrganizationAddress != org.Address {
		errors.ErrUnauthorized.Withf("invitation is not for this organization").Write(w)
		return
	}

	if err := a.db.DeleteInvitation(invitationID); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get invitation: %v", err).Write(w)
		return
	}

	// update the org members counter
	org.Counters.Members--
	if err := a.db.SetOrganization(org); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not update organization: %v", err).Write(w)
		return
	}
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
			ID:         invitation.ID.Hex(),
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
func (*API) organizationsMembersRolesHandler(w http.ResponseWriter, _ *http.Request) {
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

// updateOrganizationMemberRoleHandler godoc
//
//	@Summary		Update organization member role
//	@Description	Update the role of a member in an organization. Only the admin of the organization can update the role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string											true	"Organization address"
//	@Param			userid	path		string											true	"User ID"
//	@Param			request	body		apicommon.UpdateOrganizationMemberRoleRequest	true	"Update member role information"
//	@Success		200		{string}	string											"OK"
//	@Failure		400		{object}	errors.Error									"Invalid input data"
//	@Failure		401		{object}	errors.Error									"Unauthorized"
//	@Failure		404		{object}	errors.Error									"Organization not found"
//
// Note: The implementation returns 200 OK even for non-existent members
//
//	@Failure		500		{object}	errors.Error									"Internal server error"
//	@Router			/organizations/{address}/members/{userid} [put]
func (a *API) updateOrganizationMemberRoleHandler(w http.ResponseWriter, r *http.Request) {
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
	// get the member ID from the request path
	userID := chi.URLParam(r, "userid")
	if userID == "" {
		errors.ErrMalformedBody.With("member ID not provided").Write(w)
		return
	}
	// convert the user ID to the correct type
	userIDInt, err := strconv.Atoi(userID)
	if err != nil {
		errors.ErrMalformedBody.Withf("invalid user ID: %v", err).Write(w)
		return
	}

	// get the new role from the request body
	update := &apicommon.UpdateOrganizationMemberRoleRequest{}
	if err := json.NewDecoder(r.Body).Decode(update); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if update.Role == "" {
		errors.ErrMalformedBody.With("role not provided").Write(w)
		return
	}
	if valid := db.IsValidUserRole(db.UserRole(update.Role)); !valid {
		errors.ErrInvalidUserData.Withf("invalid role").Write(w)
		return
	}
	if err := a.db.UpdateOrganizationMemberRole(org.Address, uint64(userIDInt), db.UserRole(update.Role)); err != nil {
		errors.ErrInvalidUserData.Withf("member not found: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// removeOrganizationMemberHandler godoc
//
//	@Summary		Remove a user from the organization members
//	@Description	Remove a user from the organization members. Only the admin of the organization can remove a member.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string			true	"Organization address"
//	@Param			userid	path		string			true	"User ID"
//	@Success		200		{string}	string			"OK"
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//
// Note: The implementation returns 200 OK even for non-existent members
//
//	@Failure		400		{object}	errors.Error	"Invalid input data - User cannot remove itself"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members/{userid} [delete]
func (a *API) removeOrganizationMemberHandler(w http.ResponseWriter, r *http.Request) {
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
	// get the member ID from the request path
	userID := chi.URLParam(r, "userid")
	if userID == "" {
		errors.ErrMalformedBody.With("member ID not provided").Write(w)
		return
	}
	// convert the user ID to the correct type
	userIDInt, err := strconv.Atoi(userID)
	if err != nil {
		errors.ErrMalformedBody.Withf("invalid user ID: %v", err).Write(w)
		return
	}
	if uint64(userIDInt) == user.ID {
		errors.ErrInvalidUserData.With("user cannot remove itself from the organization").Write(w)
		return
	}
	if err := a.db.RemoveOrganizationMember(org.Address, uint64(userIDInt)); err != nil {
		errors.ErrInvalidUserData.Withf("member not found: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}
