package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// organizationMemberGroupsHandler godoc
//
//	@Summary		Get organization member groups
//	@Description	Get the list of groups and their info of the organization
//	@Description	Does not return the members of the groups, only the groups themselves.
//	@Description	Needs admin or manager role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			limit		query		integer	false	"Number of items per page (default: 10)"
//	@Success		200			{object}	apicommon.OrganizationMemberGroupsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data, or organization not found"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{orgAddress}/groups [get]
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
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	// send the organization back to the user
	totalItems, groups, err := a.db.OrganizationMemberGroups(org.Address, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get organization members: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	memberGroups := apicommon.OrganizationMemberGroupsResponse{
		Pagination: pagination,
		Groups:     make([]*apicommon.OrganizationMemberGroupInfo, 0, len(groups)),
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
			IsAutoGroup:  group.IsAutoGroup,
		})
	}
	apicommon.HTTPWriteJSON(w, memberGroups)
}

// organizationMemberGroupHandler godoc
//
//	@Summary		Get the information of an organization member group
//	@Description	Get the information of an organization member group by its ID
//	@Description	Needs admin or manager role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Param			groupId		path		string	true	"Group ID"
//	@Success		200			{object}	apicommon.OrganizationMemberGroupInfo
//	@Failure		400			{object}	errors.Error	"Invalid input data, or organization/group not found"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{orgAddress}/groups/{groupId} [get]
func (a *API) organizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupId")
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

	info := &apicommon.OrganizationMemberGroupInfo{
		ID:          group.ID.Hex(),
		Title:       group.Title,
		Description: group.Description,
		CensusIDs:   group.CensusIDs,
		CreatedAt:   group.CreatedAt,
		UpdatedAt:   group.UpdatedAt,
		IsAutoGroup: group.IsAutoGroup,
	}
	// Auto groups store no member IDs in the document; expose the live count
	// instead, consistent with the groups-listing response.
	if group.IsAutoGroup {
		count, err := a.db.CountOrgMembers(org.Address)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		info.MembersCount = int(count)
	} else {
		info.MemberIDs = group.MemberIDs
	}
	apicommon.HTTPWriteJSON(w, info)
}

// createOrganizationMemberGroupHandler godoc
//
//	@Summary		Create an organization member group
//	@Description	Create an organization member group with the given members, or with all members when
//	@Description	`includeAllMembers` is set. Needs admin or manager role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string											true	"Organization address"
//	@Param			group		body		apicommon.CreateOrganizationMemberGroupRequest	true	"Group info to create"
//	@Success		200			{object}	apicommon.OrganizationMemberGroupInfo
//	@Failure		400			{object}	errors.Error	"Invalid input data, or organization not found"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{orgAddress}/groups [post]
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

	var memberIDs []string
	var err error

	// Check if we should include all members
	if toCreate.IncludeAllMembers {
		// Get all member IDs from the database
		memberIDs, err = a.db.GetAllOrgMemberIDs(org.Address)
		if err != nil {
			errors.ErrGenericInternalServerError.Withf("could not get all org member IDs: %v", err).Write(w)
			return
		}
		log.Infow("creating group with all organization members",
			"org", org.Address.Hex(),
			"count", len(memberIDs),
			"user", user.Email,
			"title", toCreate.Title)
	} else {
		// Use the provided member IDs
		memberIDs = toCreate.MemberIDs
	}

	newMemberGroup := &db.OrganizationMemberGroup{
		Title:       toCreate.Title,
		Description: toCreate.Description,
		MemberIDs:   memberIDs,
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
//	@Description	Update an organization member group changing the info, and adding or removing members.
//	@Description	Needs admin or manager role. The auto-generated "All members" group cannot have its
//	@Description	membership modified.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string											true	"Organization address"
//	@Param			groupId		path		string											true	"Group ID"
//	@Param			group		body		apicommon.UpdateOrganizationMemberGroupsRequest	true	"Group info to update"
//	@Success		200			{object}	apicommon.UpdateOrganizationMemberGroupResponse	"Census resize jobs and errors; bare OK when neither"
//	@Failure		400			{object}	errors.Error									"Invalid input data, or organization/group not found"
//	@Failure		401			{object}	errors.Error									"Unauthorized"
//	@Failure		403			{object}	errors.Error									"Auto-generated group membership cannot be modified"
//	@Failure		500			{object}	errors.Error									"Internal server error"
//	@Router			/organizations/{orgAddress}/groups/{groupId} [put]
func (a *API) updateOrganizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupId")
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

	group, err := a.db.OrganizationMemberGroup(groupID, org.Address)
	if err != nil {
		if stderrors.Is(err, db.ErrNotFound) {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not load organization member group: %v", err).Write(w)
		return
	}
	// members leaving the group leave its censuses too, so the refusal has to happen before any
	// write: otherwise a blocked member is dropped from the group but stays in the census. Only
	// the ids the group actually holds are considered — the rest change nothing, and matching the
	// set the DB layer revokes keeps the guard from answering 409 for a member it will not touch.
	removedInGroup := make([]string, 0, len(toUpdate.RemoveMembers))
	for _, id := range toUpdate.RemoveMembers {
		if slices.Contains(group.MemberIDs, id) {
			removedInGroup = append(removedInGroup, id)
		}
	}
	if a.refuseBlockedVoters(w, group.CensusIDs, removedInGroup) {
		return
	}
	// read-only checks that must refuse before the group is touched, so an over-quota request
	// leaves the member in neither the group nor the census.
	//
	// Counted net of the group's current members, mirroring removedInGroup above:
	// AddCensusParticipantsByMemberIDs skips anyone already in the census, so a re-submitted id
	// grows nothing and must not consume quota. Group membership is a proxy for census membership
	// rather than the same thing — a census can hold participants added by other paths — so this
	// narrows the over-count rather than eliminating it, in the direction that stops refusing
	// requests which would have fit.
	addedNotInGroup := 0
	for _, id := range toUpdate.AddMembers {
		if !slices.Contains(group.MemberIDs, id) {
			addedNotInGroup++
		}
	}
	if err := a.preflightCensusGrowth(org, group.CensusIDs, addedNotInGroup); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	emptied, err := a.db.UpdateOrganizationMemberGroup(
		groupID,
		org.Address,
		toUpdate.Title,
		toUpdate.Description,
		toUpdate.AddMembers,
		toUpdate.RemoveMembers,
	)
	if err != nil {
		switch err {
		case db.ErrNotFound, db.ErrInvalidData:
			errors.ErrInvalidData.Withf("group not found").Write(w)
		case db.ErrAutoGroupMembersCannotBeModified:
			errors.ErrAutoGroupMembersCannotBeModified.Write(w)
		default:
			errors.ErrGenericInternalServerError.Withf("could not update organization member group: %v", err).Write(w)
		}
		return
	}
	resp := apicommon.UpdateOrganizationMemberGroupResponse{}
	if jobID := a.resizeEmptiedQuestions(org.Address, emptied); jobID != "" {
		resp.CensusJobIDs = append(resp.CensusJobIDs, jobID)
	}
	// members joining the group join its censuses too, drafts included: the census tracks the
	// memberbase regardless, and it is only the on-chain resize that needs a published question.
	if len(toUpdate.AddMembers) > 0 {
		propagated := a.propagateMembersToCensuses(org.Address, group.CensusIDs, toUpdate.AddMembers)
		resp.CensusJobIDs = append(resp.CensusJobIDs, propagated.JobIDs...)
		resp.Errors = append(resp.Errors, propagated.Errors...)
	}
	// the endpoint answered a bare OK before; it still does when there is nothing to report
	if len(resp.CensusJobIDs) == 0 && len(resp.Errors) == 0 {
		apicommon.HTTPWriteOK(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &resp)
}

// deleteOrganizationMemberGroupHandler godoc
//
//	@Summary		Delete an organization member group
//	@Description	Delete an organization member group by its ID. Needs admin or manager role. The
//	@Description	auto-generated "All members" group cannot be deleted.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string											true	"Organization address"
//	@Param			groupId		path		string											true	"Group ID"
//	@Success		200			{object}	apicommon.UpdateOrganizationMemberGroupResponse	"Census resize job, if needed; bare OK otherwise"
//	@Failure		400			{object}	errors.Error									"Invalid input data, or organization/group not found"
//	@Failure		401			{object}	errors.Error									"Unauthorized"
//	@Failure		403			{object}	errors.Error									"Auto-generated group cannot be deleted"
//	@Failure		500			{object}	errors.Error									"Internal server error"
//	@Router			/organizations/{orgAddress}/groups/{groupId} [delete]
func (a *API) deleteOrganizationMemberGroupHandler(w http.ResponseWriter, r *http.Request) {
	// get the member ID from the request path
	groupID := chi.URLParam(r, "groupId")
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
	// deleting a group empties the censuses built from it, so every one of its members has to be
	// removable — checked before the delete, which is not undoable.
	group, err := a.db.OrganizationMemberGroup(groupID, org.Address)
	switch {
	case err == nil:
		if a.refuseBlockedVoters(w, group.CensusIDs, group.MemberIDs) {
			return
		}
	case stderrors.Is(err, db.ErrNotFound):
		// preserve the handler's existing behaviour: deleting a missing group succeeds
	default:
		errors.ErrGenericInternalServerError.Withf("could not load organization member group: %v", err).Write(w)
		return
	}

	emptied, err := a.db.DeleteOrganizationMemberGroup(groupID, org.Address)
	if err != nil {
		switch err {
		case db.ErrNotFound:
			errors.ErrInvalidData.Withf("group not found").Write(w)
		case db.ErrAutoGroupCannotBeDeleted:
			errors.ErrAutoGroupCannotBeDeleted.Write(w)
		default:
			errors.ErrGenericInternalServerError.Withf("could not delete organization member group: %v", err).Write(w)
		}
		return
	}
	// deleting a group can open its questions to the whole census, which needs the on-chain room —
	// reported like every other path that causes a resize. Bare OK when there is nothing to report,
	// as the group PUT already does.
	if jobID := a.resizeEmptiedQuestions(org.Address, emptied); jobID != "" {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateOrganizationMemberGroupResponse{
			CensusJobIDs: []string{jobID},
		})
		return
	}
	apicommon.HTTPWriteOK(w)
}

// listOrganizationMemberGroupsHandler godoc
//
//	@Summary		Get the list of members with details of an organization member group
//	@Description	Get the paginated list of members with details of an organization member group.
//	@Description	Needs admin or manager role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Param			groupId		path		string	true	"Group ID"
//	@Param			page		query		int		false	"Page number for pagination"
//	@Param			limit		query		int		false	"Number of items per page"
//	@Success		200			{object}	apicommon.ListOrganizationMemberGroupResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data, or organization/group not found"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{orgAddress}/groups/{groupId}/members [get]
func (a *API) listOrganizationMemberGroupsHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupId")
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

	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	totalItems, members, err := a.db.ListOrganizationMemberGroup(groupID, org.Address,
		params.Page, params.Limit)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrInvalidData.Withf("group not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("could not get organization member group members: %v", err).Write(w)
		return
	}
	// convert the members to the response format
	membersResponse := make([]apicommon.OrgMember, 0, len(members))
	for _, m := range members {
		membersResponse = append(membersResponse, apicommon.OrgMemberFromDb(*m))
	}

	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.ListOrganizationMemberGroupResponse{
		Pagination: pagination,
		Members:    membersResponse,
	})
}

// organizationMemberGroupValidateHandler godoc
//
//	@Summary		Validate organization group members data
//	@Description	Checks the AuthFields for duplicates or empty fields and the TwoFaFields for empty ones.
//	@Description	On failure the offending member IDs are returned in the error `data`. Needs admin or
//	@Description	manager role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `members:write`).
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string									true	"Organization address"
//	@Param			groupId		path		string									true	"Group ID"
//	@Param			members		body		apicommon.ValidateMemberGroupRequest	true	"Members validation request"
//	@Success		200			{string}	string									"OK"
//	@Failure		400			{object}	errors.Error							"Invalid input data, duplicate/missing fields, or organization/group not found"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//
//	@Deprecated
//	@Router	/organizations/{orgAddress}/groups/{groupId}/validate [post]
func (a *API) organizationMemberGroupValidateHandler(w http.ResponseWriter, r *http.Request) {
	// get the group ID from the request path
	groupID := chi.URLParam(r, "groupId")
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
