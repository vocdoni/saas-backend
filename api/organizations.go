package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/log"
)

// createOrganizationHandler handles the request to create a new organization.
// If the organization is a suborganization, the parent organization must be
// specified in the request body, and the user must be an admin of the parent.
// If the parent organization is alread a suborganization, an error is returned.
func (a *API) createOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request body
	orgInfo := &OrganizationInfo{}
	if err := json.NewDecoder(r.Body).Decode(orgInfo); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// create the organization signer to store the address and the nonce
	signer, nonce, err := account.NewSigner(a.secret, user.Email)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not create organization signer: %v", err).Write(w)
		return
	}
	// check if the organization type is valid
	if !db.IsOrganizationTypeValid(orgInfo.Type) {
		ErrMalformedBody.Withf("invalid organization type").Write(w)
		return
	}
	parentOrg := ""
	if orgInfo.Parent != nil {
		dbParentOrg, _, err := a.db.Organization(orgInfo.Parent.Address, false)
		if err != nil {
			if err == db.ErrNotFound {
				ErrOrganizationNotFound.Withf("parent organization not found").Write(w)
				return
			}
			ErrGenericInternalServerError.Withf("could not get parent organization: %v", err).Write(w)
			return
		}
		if dbParentOrg.Parent != "" {
			ErrMalformedBody.Withf("parent organization is already a suborganization").Write(w)
			return
		}
		isAdmin, err := a.db.IsMemberOf(user.Email, dbParentOrg.Address, db.AdminRole)
		if err != nil {
			ErrGenericInternalServerError.Withf("could not check if user is admin of parent organization: %v", err).Write(w)
			return
		}
		if !isAdmin {
			ErrUnauthorized.Withf("user is not admin of parent organization").Write(w)
			return
		}
		parentOrg = orgInfo.Parent.Address
	}
	// create the organization
	if err := a.db.SetOrganization(&db.Organization{
		Address:         signer.AddressString(),
		Creator:         user.Email,
		CreatedAt:       time.Now(),
		Nonce:           nonce,
		Type:            db.OrganizationType(orgInfo.Type),
		Size:            orgInfo.Size,
		Color:           orgInfo.Color,
		Subdomain:       orgInfo.Subdomain,
		Timezone:        orgInfo.Timezone,
		Active:          true,
		TokensPurchased: 0,
		TokensRemaining: 0,
		Parent:          parentOrg,
	}); err != nil {
		if err == db.ErrAlreadyExists {
			ErrInvalidOrganizationData.WithErr(err).Write(w)
			return
		}
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the organization back to the user
	httpWriteOK(w)
}

// organizationInfoHandler handles the request to get the information of an
// organization.
func (a *API) organizationInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, parent, ok := a.organizationFromRequest(r)
	if !ok {
		ErrNoOrganizationProvided.Write(w)
		return
	}
	// send the organization back to the user
	httpWriteJSON(w, organizationFromDB(org, parent))
}

// organizationMembersHandler handles the request to get the members of an
// organization. It returns the list of members with their role in the
// organization with the address provided in the request.
func (a *API) organizationMembersHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		ErrNoOrganizationProvided.Write(w)
		return
	}
	// send the organization back to the user
	members, err := a.db.OrganizationsMembers(org.Address)
	if err != nil {
		ErrGenericInternalServerError.Withf("could not get organization members: %v", err).Write(w)
		return
	}
	orgMembers := OrganizationMembers{
		Members: make([]*OrganizationMember, 0, len(members)),
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
		orgMembers.Members = append(orgMembers.Members, &OrganizationMember{
			Info: &UserInfo{
				Email:     member.Email,
				FirstName: member.FirstName,
				LastName:  member.LastName,
			},
			Role: role,
		})
	}
	httpWriteJSON(w, orgMembers)
}

// updateOrganizationHandler handles the request to update the information of an
// organization. Only the admin of the organization can update the information.
// Only certain fields can be updated, and they will be updated only if they are
// not empty.
func (a *API) updateOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		ErrNoOrganizationProvided.Write(w)
		return
	}
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// get the organization info from the request body
	newOrgInfo := &OrganizationInfo{}
	if err := json.NewDecoder(r.Body).Decode(newOrgInfo); err != nil {
		ErrMalformedBody.Write(w)
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
			ErrGenericInternalServerError.Withf("could not update organization: %v", err).Write(w)
			return
		}
	}
	httpWriteOK(w)
}

// inviteOrganizationMemberHandler handles the request to invite a new admin
// member to an organization. Only the admin of the organization can invite a
// new member. It stores the invitation in the database and sends an email to
// the new member with the invitation code.
func (a *API) inviteOrganizationMemberHandler(w http.ResponseWriter, r *http.Request) {
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		ErrNoOrganizationProvided.Write(w)
		return
	}
	if !user.HasRoleFor(org.Address, db.AdminRole) {
		ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// check if the user is already verified
	if !user.Verified {
		ErrUserNoVerified.With("user account not verified").Write(w)
		return
	}
	// get new admin info from the request body
	invite := &OrganizationInvite{}
	if err := json.NewDecoder(r.Body).Decode(invite); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the email is correct format
	if !internal.ValidEmail(invite.Email) {
		ErrEmailMalformed.Write(w)
		return
	}
	// check the role is valid
	if valid := db.IsValidUserRole(db.UserRole(invite.Role)); !valid {
		ErrInvalidUserData.Withf("invalid role").Write(w)
		return
	}
	// check if the new user is already a member of the organization
	if _, err := a.db.IsMemberOf(invite.Email, org.Address, db.AdminRole); err == nil {
		ErrDuplicateConflict.With("user is already admin of organization").Write(w)
		return
	}
	// create new invitation
	inviteCode := internal.RandomHex(VerificationCodeLength)
	if err := a.db.CreateInvitation(&db.OrganizationInvite{
		InvitationCode:      inviteCode,
		OrganizationAddress: org.Address,
		NewUserEmail:        invite.Email,
		Role:                db.UserRole(invite.Role),
		CurrentUserID:       user.ID,
		Expiration:          time.Now().Add(InvitationExpiration),
	}); err != nil {
		if err == db.ErrAlreadyExists {
			ErrDuplicateConflict.With("user is already invited to the organization").Write(w)
			return
		}
		ErrGenericInternalServerError.Withf("could not create invitation: %v", err).Write(w)
		return
	}
	// send the invitation email
	ctx, cancel := context.WithTimeout(r.Context(), time.Second*10)
	defer cancel()
	// send the verification code via email if the mail service is available
	if a.mail != nil {
		if err := a.mail.SendNotification(ctx, &notifications.Notification{
			ToName:    fmt.Sprintf("%s %s", user.FirstName, user.LastName),
			ToAddress: invite.Email,
			Subject:   InvitationEmailSubject,
			Body:      fmt.Sprintf(InvitationTextBody, org.Address, inviteCode),
		}); err != nil {
			ErrGenericInternalServerError.Withf("could not send verification code: %v", err).Write(w)
			return
		}
	}
	httpWriteOK(w)
}

// acceptOrganizationMemberInvitationHandler handles the request to accept an
// invitation to an organization. It checks if the invitation is valid and not
// expired, and if the user is not already a member of the organization. If the
// user does not exist, it creates a new user with the provided information.
// If the user already exists and is verified, it adds the organization to the
// user.
func (a *API) acceptOrganizationMemberInvitationHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		ErrNoOrganizationProvided.Write(w)
		return
	}
	// get new member info from the request body
	invitationReq := &AcceptOrganizationInvitation{}
	if err := json.NewDecoder(r.Body).Decode(invitationReq); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// get the invitation from the database
	invitation, err := a.db.Invitation(invitationReq.Code)
	if err != nil {
		ErrUnauthorized.Withf("could not get invitation: %v", err).Write(w)
		return
	}
	// check if the organization is correct
	if invitation.OrganizationAddress != org.Address {
		ErrUnauthorized.Withf("invitation is not for this organization").Write(w)
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
		ErrInvitationExpired.Write(w)
		return
	}
	// try to get the user from the database
	dbUser, err := a.db.UserByEmail(invitation.NewUserEmail)
	if err != nil {
		// if the user does not exist, create it
		if err != db.ErrNotFound {
			ErrGenericInternalServerError.Withf("could not get user: %v", err).Write(w)
			return
		}
		// check if the user info is provided
		if invitationReq.User == nil {
			ErrMalformedBody.With("user info not provided").Write(w)
			return
		}
		// check the email is correct
		if invitationReq.User.Email != invitation.NewUserEmail {
			ErrInvalidUserData.With("email does not match").Write(w)
			return
		}
		// create the new user and move on to include the organization
		hPassword := internal.HexHashPassword(passwordSalt, invitationReq.User.Password)
		dbUser = &db.User{
			Email:     invitationReq.User.Email,
			Password:  hPassword,
			FirstName: invitationReq.User.FirstName,
			LastName:  invitationReq.User.LastName,
			Verified:  true,
		}
	} else {
		// if it does, check if the user is already verified
		if !dbUser.Verified {
			ErrUserNoVerified.With("user already exists but is not verified").Write(w)
			return
		}
		// check if the user is already a member of the organization
		if _, err := a.db.IsMemberOf(invitation.NewUserEmail, org.Address, invitation.Role); err == nil {
			go removeInvitation()
			ErrDuplicateConflict.With("user is already admin of organization").Write(w)
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
		ErrGenericInternalServerError.Withf("could not set user: %v", err).Write(w)
		return
	}
	// delete the invitation
	go removeInvitation()
	httpWriteOK(w)
}

// memberRolesHandler returns the available roles that can be assigned to a
// member of an organization.
func (a *API) organizationsMembersRolesHandler(w http.ResponseWriter, _ *http.Request) {
	availableRoles := []*OrganizationRole{}
	for role, name := range db.UserRolesNames {
		availableRoles = append(availableRoles, &OrganizationRole{
			Role:            string(role),
			Name:            name,
			WritePermission: db.HasWriteAccess(role),
		})
	}
	httpWriteJSON(w, &OrganizationRoleList{Roles: availableRoles})
}

// organizationsTypesHandler returns the available organization types that can be
// assigned to an organization.
func (a *API) organizationsTypesHandler(w http.ResponseWriter, _ *http.Request) {
	organizationTypes := []*OrganizationType{}
	for orgType, name := range db.OrganizationTypesNames {
		organizationTypes = append(organizationTypes, &OrganizationType{
			Type: string(orgType),
			Name: name,
		})
	}
	httpWriteJSON(w, &OrganizationTypeList{Types: organizationTypes})
}
