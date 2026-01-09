package api

import (
	"context"
	"encoding/json"
	"net/http"
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

// MembersImportCompletionData represents the data structure for members import completion email template
type MembersImportCompletionData struct {
	OrganizationName string
	UserName         string
	Link             string
	TotalMembers     int
	AddedMembers     int
	ErrorCount       int
	Errors           []string
	CompletedAt      time.Time
}

// sendMembersImportCompletionEmail sends an email notification when members import is completed
func (a *API) sendMembersImportCompletionEmail(userEmail, userName string, org *db.Organization, progress *db.BulkOrgMembersJob) {
	if a.mail == nil {
		return // Email service not configured
	}

	link, err := a.buildWebAppURL("/admin/memberbase/", nil)
	if err != nil {
		log.Errorf("failed to build web app URL for members import completion email: %v", err)
	}

	// Import the mailtemplates package dynamically to avoid import issues
	// We'll use the sendMail method which handles the template execution
	data := MembersImportCompletionData{
		UserName:     userName,
		TotalMembers: progress.Total,
		AddedMembers: progress.Added,
		Link:         link,
		ErrorCount:   len(progress.Errors),
		Errors:       progress.ErrorsAsStrings(),
		CompletedAt:  time.Now(),
	}

	// Create a background context for email sending
	ctx := context.Background()

	// We need to import mailtemplates here to use the template
	// For now, let's create a simple notification structure
	if err := a.sendMail(ctx, userEmail, mailtemplates.MembersImportCompletionNotification, data); err != nil {
		log.Errorf("failed to send members import completion email to %s for org %s: %v", userEmail, org.Address, err)
		return
	}

	log.Infow("members import completion email sent",
		"user", userEmail,
		"org", org.Address,
		"added", progress.Added,
		"total", progress.Total,
		"errors", len(progress.Errors))
}

// organizationMembersHandler godoc
//
//	@Summary		Get organization members
//	@Description	Retrieve all members of an organization with pagination support
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string	true	"Organization address"
//	@Param			page	query		integer	false	"Page number (default: 1)"
//	@Param			limit	query		integer	false	"Number of items per page (default: 10)"
//	@Param			search	query		string	false	"Search term for member properties"
//	@Success		200		{object}	apicommon.OrganizationMembersResponse
//	@Failure		400		{object}	errors.Error	"Invalid input"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
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

	search := "" // Default search term
	if searchStr := r.URL.Query().Get("search"); searchStr != "" {
		search = searchStr
	}

	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
	}

	totalItems, members, err := a.db.OrgMembers(org.Address, params.Page, params.Limit, search)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get org members: %v", err).Write(w)
		return
	}
	// convert the orgMembers to the response format
	membersResponse := make([]apicommon.OrgMember, 0, len(members))
	for _, p := range members {
		membersResponse = append(membersResponse, apicommon.OrgMemberFromDb(*p))
	}

	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.OrganizationMembersResponse{
		Pagination: pagination,
		Members:    membersResponse,
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
	async := r.URL.Query().Get("async") == "true" //nolint:goconst

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
	progressChan, err := a.db.AddBulkOrgMembers(org, members.ToDB(), passwordSalt)
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

		// Return the number of members added
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{
			Added:  uint32(lastProgress.Added),
			Errors: lastProgress.ErrorsAsStrings(),
		})
		return
	}

	// if async create a new job identifier
	jobID := internal.HexBytes(util.RandomBytes(16))

	// Create persistent job record
	if err := a.db.CreateJob(jobID.String(), db.JobTypeOrgMembers, org.Address, len(members.Members)); err != nil {
		log.Warnw("failed to create persistent job record", "error", err, "jobId", jobID.String())
		// Continue with in-memory only (fallback)
	}

	// Capture user and org info for the async goroutine
	userEmail := user.Email
	userName := user.FirstName + " " + user.LastName

	go func() {
		var lastProgress *db.BulkOrgMembersJob
		for p := range progressChan {
			lastProgress = p
			// Store progress updates in a map that is read by another endpoint to check a job status
			addMembersToOrgWorkers.Store(jobID.String(), p)

			// When job completes, persist final results
			if p.Progress == 100 {
				if err := a.db.CompleteJob(jobID.String(), p.Added, p.ErrorsAsStrings()); err != nil {
					log.Warnw("failed to persist job completion", "error", err, "jobId", jobID.String())
				}
			}
		}

		// Send completion email notification when async job is done
		if lastProgress != nil {
			a.sendMembersImportCompletionEmail(userEmail, userName, org, lastProgress)
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
func (a *API) addOrganizationMembersJobStatusHandler(w http.ResponseWriter, r *http.Request) {
	jobID := internal.HexBytes{}
	if err := jobID.ParseString(chi.URLParam(r, "jobid")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid job ID").Write(w)
		return
	}

	// First check in-memory for active jobs
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

	// If not in memory, check database for completed jobs
	job, err := a.db.Job(jobID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrJobNotFound.Withf("%s", jobID.String()).Write(w)
			return
		}
		errors.ErrGenericInternalServerError.Withf("failed to get job: %v", err).Write(w)
		return
	}

	// Return persistent job data
	apicommon.HTTPWriteJSON(w, apicommon.AddMembersJobResponse{
		Added:    uint32(job.Added),
		Errors:   job.Errors,
		Progress: 100, // Completed jobs are always 100%
		Total:    uint32(job.Total),
	})
}

// upsertOrganizationMemberHandler godoc
//
//	@Summary		Create or update an organization member
//	@Description	Create or update an organization member. Requires Manager/Admin role.
//	@Description	Automatically updates census participant hashes when member data changes.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string				true	"Organization address"
//	@Param			request	body		apicommon.OrgMember	true	"Member data to insert or update"
//	@Success		200		{object}	apicommon.OrgMember	"ID of member inserted or updated"
//	@Failure		400		{object}	errors.Error		"Invalid input data"
//	@Failure		401		{object}	errors.Error		"Unauthorized"
//	@Failure		500		{object}	errors.Error		"Internal server error"
//	@Router			/organizations/{address}/members [put]
func (a *API) upsertOrganizationMemberHandler(w http.ResponseWriter, r *http.Request) {
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

	// decode the member data from the request body
	member := &apicommon.OrgMember{}
	if err := json.NewDecoder(r.Body).Decode(member); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("invalid member data").Write(w)
		return
	}

	// upsert the member in the database
	memberID, err := a.db.UpsertOrgMemberAndCensusParticipants(org, member.ToDB(), passwordSalt)
	switch {
	case errors.Is(err, db.ErrUpdateWouldCreateDuplicates):
		errors.ErrInvalidData.WithErr(err).Write(w)
		return
	case err != nil:
		errors.ErrGenericInternalServerError.Withf("could not upsert org member: %v", err).Write(w)
		return
	default:
	}

	apicommon.HTTPWriteJSON(w, apicommon.OrgMember{ID: memberID.Hex()})
}

// deleteOrganizationMembersHandler godoc
//
//	@Summary		Delete organization members
//	@Description	Delete multiple members from an organization or all members. Requires Manager/Admin role.
//	@Tags			organizations
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			address	path		string							true	"Organization address"
//	@Param			request	body		apicommon.DeleteMembersRequest	true	"Member IDs to delete or all flag"
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
		errors.ErrMalformedBody.Withf("error decoding member request").Write(w)
		return
	}

	var deleted int
	var err error

	// check if we should delete all members
	if members.All {
		// delete all org members from the database
		deleted, err = a.db.DeleteAllOrgMembers(org.Address)
		if err != nil {
			errors.ErrGenericInternalServerError.Withf("could not delete all org members: %v", err).Write(w)
			return
		}
		log.Infow("deleted all organization members",
			"org", org.Address.Hex(),
			"count", deleted,
			"user", user.Email)
	} else {
		// check if there are member IDs to delete
		if len(members.IDs) == 0 {
			apicommon.HTTPWriteJSON(w, &apicommon.DeleteMembersResponse{Count: 0})
			return
		}
		// delete specific org members from the database
		deleted, err = a.db.DeleteOrgMembers(org.Address, members.IDs)
		if err != nil {
			errors.ErrGenericInternalServerError.Withf("could not delete org members: %v", err).Write(w)
			return
		}
	}

	apicommon.HTTPWriteJSON(w, &apicommon.DeleteMembersResponse{Count: deleted})
}
