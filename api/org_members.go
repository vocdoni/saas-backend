package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/util"
)

// addMembersToOrgWorkers is a map of job identifiers to the progress of adding members to a census.
// This is used to check the progress of the job.
var addMembersToOrgWorkers sync.Map

// MemberImportCompletionData represents the data structure for member import completion email template
type MemberImportCompletionData struct {
	OrganizationName string
	UserName         string
	TotalMembers     int
	AddedMembers     int
	ErrorCount       int
	Errors           []string
	CompletedAt      time.Time
}

// sendMemberImportCompletionEmail sends an email notification when member import is completed
func (a *API) sendMemberImportCompletionEmail(userEmail, userName, orgName string, progress *db.BulkOrgMembersJob) {
	if a.mail == nil {
		return // Email service not configured
	}

	// Import the mailtemplates package dynamically to avoid import issues
	// We'll use the sendMail method which handles the template execution
	data := MemberImportCompletionData{
		OrganizationName: orgName,
		UserName:         userName,
		TotalMembers:     progress.Total,
		AddedMembers:     progress.Added,
		ErrorCount:       len(progress.Errors),
		Errors:           progress.ErrorsAsStrings(),
		CompletedAt:      time.Now(),
	}

	// Create a background context for email sending
	ctx := context.Background()

	// We need to import mailtemplates here to use the template
	// For now, let's create a simple notification structure
	if err := a.sendMail(ctx, userEmail, mailtemplates.MemberImportCompletionNotification, data); err != nil {
		log.Errorf("failed to send member import completion email to %s for org %s: %v", userEmail, orgName, err)
	} else {
		log.Infow("member import completion email sent",
			"user", userEmail,
			"org", orgName,
			"added", progress.Added,
			"total", progress.Total,
			"errors", len(progress.Errors))
	}
}

// organizationMembersHandler godoc
//
//	@Summary		Get organization members
//	@Description	Retrieve all members of an organization with pagination support
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address		path		string	true	"Organization address"
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			pageSize	query		integer	false	"Number of items per page (default: 10)"
//	@Param			search		query		string	false	"Search term for member properties"
//	@Success		200			{object}	apicommon.OrganizationMembersResponse
//	@Failure		400			{object}	errors.Error	"Invalid input"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members [get]
func (a *API) organizationMembersHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// Parse pagination parameters from query string
	page := 1      // Default page number
	pageSize := 10 // Default page size
	search := ""   // Default search term

	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if pageVal, err := strconv.Atoi(pageStr); err == nil && pageVal > 0 {
			page = pageVal
		}
	}

	if pageSizeStr := r.URL.Query().Get("pageSize"); pageSizeStr != "" {
		if pageSizeVal, err := strconv.Atoi(pageSizeStr); err == nil && pageSizeVal >= 0 {
			pageSize = pageSizeVal
		}
	}

	if searchStr := r.URL.Query().Get("search"); searchStr != "" {
		search = searchStr
	}

	// retrieve the orgMembers with pagination
	pages, members, err := a.db.OrgMembers(org.Address, page, pageSize, search)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get org members: %v", err).Write(w)
		return
	}

	// convert the orgMembers to the response format
	membersResponse := make([]apicommon.OrgMember, 0, len(members))
	for _, p := range members {
		membersResponse = append(membersResponse, apicommon.OrgMemberFromDb(p))
	}

	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationMembersResponse{
		Pages:   pages,
		Page:    page,
		Members: membersResponse,
	})
}

// addOrganizationMembersHandler godoc
//
//	@Summary		Add members to an organization
//	@Description	Add multiple members to an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string						true	"Organization address"
//	@Param			async	query		boolean						false	"Process asynchronously and return job ID"
//	@Param			request	body		apicommon.AddMembersRequest	true	"Members to add"
//	@Success		200		{object}	apicommon.AddMembersResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/members [post]
func (a *API) addOrganizationMembersHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// get the async flag
	async := r.URL.Query().Get("async") == "true"

	// retrieve census
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// decode the members from the request body
	members := &apicommon.AddMembersRequest{}
	if err := json.NewDecoder(r.Body).Decode(members); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("missing members").Write(w)
		return
	}
	// check if there are members to add
	if len(members.Members) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{Added: 0})
		return
	}
	// add the org members to the database
	progressChan, err := a.db.SetBulkOrgMembers(
		org.Address,
		passwordSalt,
		members.DbOrgMembers(org.Address),
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if !async {
		// Wait for the channel to be closed (100% completion)
		var lastProgress *db.BulkOrgMembersJob
		for p := range progressChan {
			lastProgress = p
			// Just drain the channel until it's closed
			log.Debugw("org add members",
				"org", org.Address,
				"progress", p.Progress,
				"added", p.Added,
				"total", p.Total,
				"errors", len(p.Errors))
		}

		// Send completion email notification
		orgName := org.Subdomain
		if orgName == "" {
			orgName = org.Address.Hex()
		}
		go a.sendMemberImportCompletionEmail(user.Email, user.FirstName+" "+user.LastName, orgName, lastProgress)

		// Return the number of members added
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{
			Added:  uint32(lastProgress.Added),
			Errors: lastProgress.ErrorsAsStrings(),
		})
		return
	}

	// if async create a new job identifier
	jobID := internal.HexBytes(util.RandomBytes(16))

	// Capture user and org info for the async goroutine
	userEmail := user.Email
	userName := user.FirstName + " " + user.LastName
	orgName := org.Subdomain
	if orgName == "" {
		orgName = org.Address.Hex()
	}

	go func() {
		var lastProgress *db.BulkOrgMembersJob
		for p := range progressChan {
			lastProgress = p
			// Store progress updates in a map that is read by another endpoint to check a job status
			addMembersToOrgWorkers.Store(jobID.String(), p)
		}

		// Send completion email notification when async job is done
		if lastProgress != nil {
			a.sendMemberImportCompletionEmail(userEmail, userName, orgName, lastProgress)
		}
	}()

	apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{JobID: jobID})
}

// addOrganizationMembersJobStatusHandler godoc
//
//	@Summary		Check the progress of adding members
//	@Description	Check the progress of a job to add members to an organization. Returns the progress of the job.
//	@Description	If the job is completed, the job is deleted after 60 seconds.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			jobid	path		string	true	"Job ID"
//	@Success		200		{object}	apicommon.AddMembersJobResponse
//	@Failure		400		{object}	errors.Error	"Invalid job ID"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Job not found"
//	@Router			/organizations/{address}/members/job/{jobid} [get]
func (*API) addOrganizationMembersJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobid")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	if v, ok := addMembersToOrgWorkers.Load(jobID.String()); ok {
		p, ok := v.(*db.BulkOrgMembersJob)
		if !ok {
			errors.ErrGenericInternalServerError.Withf("invalid job status type").Write(w)
			return
		}
		if p.Progress == 100 {
			go func() {
				// Schedule the deletion of the job after 60 seconds
				time.Sleep(60 * time.Second)
				addMembersToOrgWorkers.Delete(jobID.String())
			}()
		}
		apicommon.HTTPWriteJSON(w, apicommon.AddMembersJobResponse{
			Added:    uint32(p.Added),
			Errors:   p.ErrorsAsStrings(),
			Progress: uint32(p.Progress),
			Total:    uint32(p.Total),
		})
		return
	}

	errors.ErrJobNotFound.Withf("%s", jobID.String()).Write(w)
}

// deleteOrganizationMembersHandler godoc
//
//	@Summary		Delete organization members
//	@Description	Delete multiple members from an organization. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string							true	"Organization address"
//	@Param			request	body		apicommon.DeleteMembersRequest	true	"Member IDs to delete"
//	@Success		200		{object}	apicommon.DeleteMembersResponse
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{address}/member [delete]
func (a *API) deleteOrganizationMembersHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization info from the request context
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	// check the user has the necessary permissions
	if !user.HasRoleFor(org.Address, db.ManagerRole) && !user.HasRoleFor(org.Address, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}
	// get memberIds from the request body
	members := &apicommon.DeleteMembersRequest{}
	if err := json.NewDecoder(r.Body).Decode(members); err != nil {
		errors.ErrMalformedBody.Withf("error decoding member IDs").Write(w)
		return
	}
	// check if there are member IDs to delete
	if len(members.IDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.DeleteMembersResponse{Count: 0})
		return
	}
	// delete the org members from the database
	deleted, err := a.db.DeleteOrgMembers(org.Address, members.IDs)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not delete org members: %v", err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, &apicommon.DeleteMembersResponse{Count: deleted})
}
