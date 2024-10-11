package api

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
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
		Name:            orgInfo.Name,
		Creator:         user.Email,
		CreatedAt:       time.Now(),
		Nonce:           nonce,
		Type:            db.OrganizationType(orgInfo.Type),
		Description:     orgInfo.Description,
		Size:            orgInfo.Size,
		Color:           orgInfo.Color,
		Logo:            orgInfo.Logo,
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
	if newOrgInfo.Name != "" {
		org.Name = newOrgInfo.Name
		updateOrg = true
	}
	if newOrgInfo.Description != "" {
		org.Description = newOrgInfo.Description
		updateOrg = true
	}
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
	if newOrgInfo.Logo != "" {
		org.Logo = newOrgInfo.Logo
		updateOrg = true
	}
	if newOrgInfo.Header != "" {
		org.Header = newOrgInfo.Header
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
	if newOrgInfo.Language != "" {
		org.Language = newOrgInfo.Language
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

// inviteOrganizationAdminHandler handles the request to invite a new admin
// member to an organization. Only the admin of the organization can invite a
// new admin member. The new admin will be created as a verified user if it
// does not exist yet (with a random password), and an email will be sent to
// the new admin with the invitation code to change that password.
func (a *API) inviteOrganizationAdminHandler(w http.ResponseWriter, r *http.Request) {
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	if err := json.Unmarshal(body, invite); err != nil {
		ErrMalformedBody.Write(w)
		return
	}
	// check the email is correct format
	if !internal.ValidEmail(invite.User.Email) {
		ErrEmailMalformed.Write(w)
		return
	}
	// check the phone is not empty
	if invite.User.Phone == "" {
		ErrMalformedBody.Withf("phone is empty").Write(w)
		return
	}
	// check the role is valid
	if valid := db.ValidRoles[db.UserRole(invite.Role)]; !valid {
		ErrInvalidUserData.Withf("invalid role").Write(w)
		return
	}
	// check if the user already exists, if so, add it to the organization and
	// return
	admin, err := a.db.UserByEmail(invite.User.Email)
	if err == nil {
		// check if the user is already a member of the organization
		if _, err := a.db.IsMemberOf(invite.User.Email, org.Address, db.AdminRole); err == nil {
			ErrDuplicateConflict.With("user is already admin of organization").Write(w)
			return
		}
		// check if the user is already verified
		if !admin.Verified {
			ErrUserNoVerified.With("new admin account not verified").Write(w)
			return
		}
		// if the user exists and is not a member of the organization, add it
		admin.Organizations = append(admin.Organizations, db.OrganizationMember{
			Address: org.Address,
			Role:    db.UserRole(invite.Role),
		})
		// update the user info in the database
		if _, err := a.db.SetUser(admin); err != nil {
			ErrGenericInternalServerError.Withf("could not add user to organization: %v", err).Write(w)
			return
		}
		httpWriteOK(w)
		return
	}
	// if the user does not exist, create a new verified user with the desired
	// role and send an email to the user to set the password

	// check the first name is not empty
	if invite.User.FirstName == "" {
		ErrMalformedBody.Withf("first name is empty").Write(w)
		return
	}
	// check the last name is not empty
	if invite.User.LastName == "" {
		ErrMalformedBody.Withf("last name is empty").Write(w)
		return
	}
	newUser := &db.User{
		Email:     invite.User.Email,
		FirstName: invite.User.FirstName,
		LastName:  invite.User.LastName,
		Phone:     invite.User.Phone,
		Password:  internal.RandomHex(8),
		Verified:  true,
		Organizations: []db.OrganizationMember{
			{Address: org.Address, Role: db.UserRole(invite.Role)},
		},
	}
	// create the user in the database
	if newUser.ID, err = a.db.SetUser(newUser); err != nil {
		ErrGenericInternalServerError.Withf("could not create user: %v", err).Write(w)
		return
	}
	// generate a code for the user to set the password
	if err := a.sendUserCode(r.Context(), newUser, db.CodeTypePasswordReset); err != nil {
		log.Warnw("could not send verification code", "error", err)
		ErrGenericInternalServerError.Write(w)
		return
	}
	httpWriteOK(w)
}
