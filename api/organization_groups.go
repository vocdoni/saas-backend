package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// organizationMemberGroupsHandler godoc
//
//	@Summary		Get organization member groups
//	@Description	Get the list of groups and their info of the organization
//	@Description	Does not return the members of the groups, only the groups themselves.
//	@Description	Needs admin or manager role
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address		path		string	true	"Organization address"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			pageSize	query		integer	false	"Number of items per page (default: 10)"
//	@Success		200			{object}	apicommon.OrganizationMemberGroupsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Organization not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/groups [get]
func (a *API) organizationMemberGroupsHandler(w http.ResponseWriter, r *http.Request) {
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// Parse pagination parameters from query string
	page := 1      // Default page number
	pageSize := 10 // Default page size

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if pageVal, err := strconv.Atoi(pageStr); err == nil && pageVal > 0 {
			page = pageVal
		}
	}

	if pageSizeStr := r.URL.Query().Get("pageSize"); pageSizeStr != "" {
		if pageSizeVal, err := strconv.Atoi(pageSizeStr); err == nil && pageSizeVal > 0 {
			pageSize = pageSizeVal
		}
	}

	// send the organization back to the user
	pages, groups, err := a.db.OrganizationMemberGroups(org.Address, page, pageSize)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get organization members: %v", err).Write(w)
		return
	}
	memberGroups := apicommon.OrganizationMemberGroupsResponse{
		TotalPages:  pages,
		CurrentPage: page,
		Groups:      make([]*apicommon.OrganizationMemberGroupInfo, 0, len(groups)),
	}
	for _, group := range groups {
		memberGroups.Groups = append(memberGroups.Groups, &apicommon.OrganizationMemberGroupInfo{
			ID:           group.ID.Hex(),
			Title:        group.Title,
			Description:  group.Description,
			CreatedAt:    group.CreatedAt,
			UpdatedAt:    group.UpdatedAt,
			CensusIDs:    group.CensusIDs,
			MembersCount: len(group.MemberIDs),
		})
	}
	apicommon.HTTPWriteJSON(w, memberGroups)
}

// organizationMemberGroupHandler godoc
//
//	@Summary		Get the information of an organization member group
//	@Description	Get the information of an organization member group by its ID
//	@Description	Needs admin or manager role
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			groupID	path		string	true	"Group ID"
//	@Success		200		{object}	apicommon.OrganizationMemberGroupInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization or group not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/groups/{groupID} [get]
func (a *API) organizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupID")
	if groupID == "" {
		errors.ErrInvalidData.Withf("group ID is required").Write(w)
		return
	}
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	group, err := a.db.OrganizationMemberGroup(groupID, org.Address)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get organization member group: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationMemberGroupInfo{
		ID:          group.ID.Hex(),
		Title:       group.Title,
		Description: group.Description,
		MemberIDs:   group.MemberIDs,
		CensusIDs:   group.CensusIDs,
		CreatedAt:   group.CreatedAt,
		UpdatedAt:   group.UpdatedAt,
	})
}

// createOrganizationMemberGroupHandler godoc
//
//	@Summary		Create an organization member group
//	@Description	Create an organization member group with the given members
//	@Description	Needs admin or manager role
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string											true	"Organization address"
//	@Param			group	body		apicommon.CreateOrganizationMemberGroupRequest	true	"Group info to create"
//	@Success		200		{object}	apicommon.OrganizationMemberGroupInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/groups [post]
func (a *API) createOrganizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	var toCreate apicommon.CreateOrganizationMemberGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&toCreate); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	newMemberGroup := &db.OrganizationMemberGroup{
		Title:       toCreate.Title,
		Description: toCreate.Description,
		MemberIDs:   toCreate.MemberIDs,
		OrgAddress:  org.Address,
	}

	groupID, err := a.db.CreateOrganizationMemberGroup(newMemberGroup)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("organization not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not create organization member group: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationMemberGroupInfo{
		ID: groupID,
	})
}

// updateOrganizationMemberGroupHandler godoc
//
//	@Summary		Update an organization member group
//	@Description	Update an organization member group changing the info, and adding or removing members
//	@Description	Needs admin or manager role
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string											true	"Organization address"
//	@Param			groupID	path		string											true	"Group ID"
//	@Param			group	body		apicommon.UpdateOrganizationMemberGroupsRequest	true	"Group info to update"
//	@Success		200		{string}	string											"OK"
//	@Failure		400		{object}	errors.Error									"Invalid input data"
//	@Failure		401		{object}	errors.Error									"Unauthorized"
//	@Failure		404		{object}	errors.Error									"Organization or group not found"
//	@Failure		500		{object}	errors.Error									"Internal server error"
//	@Router			/organizations/{address}/groups/{groupID} [put]
func (a *API) updateOrganizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupID")
	if groupID == "" {
		errors.ErrInvalidData.Withf("group ID is required").Write(w)
		return
	}
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	var toUpdate apicommon.UpdateOrganizationMemberGroupsRequest
	if err := json.NewDecoder(r.Body).Decode(&toUpdate); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	err := a.db.UpdateOrganizationMemberGroup(
		groupID,
		org.Address,
		toUpdate.Title,
		toUpdate.Description,
		toUpdate.AddMembers,
		toUpdate.RemoveMembers,
	)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not update organization member group: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// deleteOrganizationMemberGroupHandler godoc
//
//	@Summary		Delete an organization member group
//	@Description	Delete an organization member group by its ID
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string			true	"Organization address"
//	@Param			groupID	path		string			true	"Group ID"
//	@Success		200		{string}	string			"OK"
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization or group not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/groups/{groupID} [delete]
func (a *API) deleteOrganizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the member ID from the request path
	groupID := chi.URLParam(r, "groupID")
	if groupID == "" {
		errors.ErrInvalidData.Withf("group ID is required").Write(w)
		return
	}
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	if err := a.db.DeleteOrganizationMemberGroup(groupID, org.Address); err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not delete organization member group: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// listOrganizationMemberGroupsHandler godoc
//
//	@Summary		Get the list of members with details of an organization member group
//	@Description	Get the list of members with details of an organization member group
//	@Description	Needs admin or manager role
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address		path		string	true	"Organization address"
//	@Param			groupID		path		string	true	"Group ID"
//	@Param			page		query		int		false	"Page number for pagination"
//	@Param			pageSize	query		int		false	"Number of items per page"
//	@Success		200			{object}	apicommon.ListOrganizationMemberGroupResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Organization or group not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/groups/{groupID}/members [get]
func (a *API) listOrganizationMemberGroupsHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupID")
	if groupID == "" {
		errors.ErrInvalidData.Withf("group ID is required").Write(w)
		return
	}
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// Parse pagination parameters from query string
	page := 1      // Default page number
	pageSize := 10 // Default page size

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if pageVal, err := strconv.Atoi(pageStr); err == nil && pageVal > 0 {
			page = pageVal
		}
	}

	if pageSizeStr := r.URL.Query().Get("pageSize"); pageSizeStr != "" {
		if pageSizeVal, err := strconv.Atoi(pageSizeStr); err == nil && pageSizeVal > 0 {
			pageSize = pageSizeVal
		}
	}

	totalPages, members, err := a.db.ListOrganizationMemberGroup(groupID, org.Address, int64(page), int64(pageSize))
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get organization member group members: %v", err).Write(w)
		return
	}
	if totalPages == 0 {
		// If no members are found, return an empty response
		apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationMemberGroupResponse{
			TotalPages:  totalPages,
			CurrentPage: 0,
			Members:     []apicommon.OrgMember{},
		})
	}
	// convert the members to the response format
	membersResponse := make([]apicommon.OrgMember, 0, len(members))
	for _, m := range members {
		membersResponse = append(membersResponse, apicommon.OrgMemberFromDb(*m))
	}

	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationMemberGroupResponse{
		TotalPages:  totalPages,
		CurrentPage: page,
		Members:     membersResponse,
	})
}

// organizationMemberGroupValidateHandler godoc
//
//	@Summary		Validate organization group members data
//	@Description	Checks the AuthFields for duplicates or empty fields and the TwoFaFields for empty ones.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string								true	"Organization address"
//	@Param			groupID	path		string								true	"Group ID"
//	@Param			members	body		apicommon.ValidateMemberGroupRequest	true	"Members validation request"
//	@Success		200		{string}	string			"OK"
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Organization or group not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//
// @Router			/organizations/{address}/groups/{groupID}/validate [post]
func (a *API) organizationMemberGroupValidateHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupID")
	if groupID == "" {
		errors.ErrInvalidData.Withf("group ID is required").Write(w)
		return
	}
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
		// if the user is not admin or manager of the organization, return an error
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	var membersRequest apicommon.ValidateMemberGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&membersRequest); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if len(membersRequest.AuthFields) == 0 && len(membersRequest.TwoFaFields) == 0 {
		errors.ErrInvalidData.Withf("missing both AuthFields and TwoFaFields").Write(w)
		return
	}

	// check the org members to veriy tha the OrgMemberAuthFields can be used for authentication
	aggregationResults, err := a.db.CheckGroupMembersFields(
		org.Address,
		groupID,
		membersRequest.AuthFields,
		membersRequest.TwoFaFields,
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(aggregationResults.Duplicates) > 0 || len(aggregationResults.MissingData) > 0 {
		// if there are incorrect members, return an error with the IDs of the incorrect members
		errors.ErrInvalidData.WithData(aggregationResults).Write(w)
		return
	}

	apicommon.HTTPWriteOK(w)
}
