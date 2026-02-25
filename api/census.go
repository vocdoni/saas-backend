package api

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/log"
)

const (
	// CensusTypeSMSOrMail is the CSP based type of census that supports both SMS and mail.
	CensusTypeSMSOrMail = "sms_or_mail"
	CensusTypeMail      = "mail"
	CensusTypeSMS       = "sms"
)

// createCensusHandler godoc
//
//	@Summary		Create a new census
//	@Description	Create a new census for an organization. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateCensusRequest	true	"Census information"
//	@Success		200		{object}	apicommon.CreateCensusResponse	"Returns the created census ID"
//	@Failure		400		{object}	errors.Error					"Invalid input data or missing required fields"
//	@Failure		401		{object}	errors.Error					"Unauthorized"
//	@Failure		500		{object}	errors.Error					"Internal server error"
//	@Router			/census [post]
func (a *API) createCensusHandler(w http.ResponseWriter, r *http.Request) {
	// Parse request
	censusInfo := &apicommon.CreateCensusRequest{}
	if err := json.NewDecoder(r.Body).Decode(&censusInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(censusInfo.OrgAddress, db.ManagerRole) && !user.HasRoleFor(censusInfo.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	census := &db.Census{
		OrgAddress:  censusInfo.OrgAddress,
		AuthFields:  censusInfo.AuthFields,
		TwoFaFields: censusInfo.TwoFaFields,
		CreatedAt:   time.Now(),
	}

	// In the regular census, members will be added later so we just create the DB entry
	censusID, err := a.db.SetCensus(census)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, apicommon.CreateCensusResponse{
		ID: censusID,
	})
}

// censusInfoHandler godoc
//
//	@Summary		Get census information
//	@Description	Retrieve census information by ID. Returns census type, organization address, and creation time.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.OrganizationCensus
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id} [get]
func (a *API) censusInfoHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}
	census, err := a.db.Census(censusID.String())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	apicommon.HTTPWriteJSON(w, apicommon.OrganizationCensusFromDB(census))
}

// addCensusParticipantsHandler godoc
//
//	@Summary		Add organization members to a census
//	@Description	Add existing organization members to a census by member ID (synchronous).
//	@Description	Members already in the census are skipped.
//	@Description	Returns number of participants added plus per-member errors in the response `errors` field.
//	@Description	Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string									true	"Census ID"
//	@Param			request	body		apicommon.AddCensusParticipantsRequest	true	"Participant member IDs to add"
//	@Success		200		{object}	apicommon.AddMembersResponse			"Added count and optional per-member errors"
//	@Failure		400		{object}	errors.Error							"Invalid input data"
//	@Failure		401		{object}	errors.Error							"Unauthorized"
//	@Failure		404		{object}	errors.Error							"Census not found"
//	@Failure		500		{object}	errors.Error							"Internal server error"
//	@Router			/census/{id} [post]
func (a *API) addCensusParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	// decode the participant IDs from the request body
	participants := &apicommon.AddCensusParticipantsRequest{}
	if err := json.NewDecoder(r.Body).Decode(participants); err != nil {
		log.Error(err)
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}

	// check if there are participants to add
	if len(participants.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{Added: 0})
		return
	}

	if err := a.subscriptions.OrgCanAddCensusParticipants(
		census.OrgAddress,
		censusID.String(),
		len(participants.MemberIDs),
	); err != nil {
		if apiErr := (errors.Error{}); errors.As(err, &apiErr) {
			apiErr.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	added, membersErrors, err := a.db.AddCensusParticipantsByMemberIDs(censusID.String(), participants.MemberIDs)
	// TODO return as error the failed memberIDs
	switch {
	case err == nil:
		apicommon.HTTPWriteJSON(w, &apicommon.AddMembersResponse{Added: uint32(added), Errors: membersErrors})
	case stderrors.Is(err, db.ErrInvalidData), stderrors.Is(err, db.ErrUpdateWouldCreateDuplicates):
		errors.ErrInvalidData.WithErr(err).Write(w)
	case stderrors.Is(err, db.ErrNotFound):
		errors.ErrCensusNotFound.Write(w)
	default:
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
	}
}

// publishCensusHandler godoc
//
//	@Summary		Publish a census for voting
//	@Description	Publish a census for voting. Requires Manager/Admin role. Returns published census with credentials.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.PublishedCensusResponse
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/publish [post]
func (a *API) publishCensusHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		errors.ErrCensusNotFound.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	if len(census.Published.Root) > 0 {
		// if the census is already published, return the censusInfo
		apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
			URI:  census.Published.URI,
			Root: census.Published.Root,
		})
		return
	}

	// if census.Type == CensusTypeSMSOrMail || census.Type == CenT {
	// build the census and store it
	cspSignerPubKey := a.account.PubKey // TODO: use a different key based on the censusID
	switch census.Type {
	case CensusTypeSMSOrMail, CensusTypeMail, CensusTypeSMS:
		census.Published.Root = cspSignerPubKey
		census.Published.URI = a.serverURL + "/process"
		census.Published.CreatedAt = time.Now()

	default:
		errors.ErrCensusTypeNotFound.Write(w)
		return
	}

	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
		URI:  census.Published.URI,
		Root: cspSignerPubKey,
	})
}

// publishCensusGroupHandler godoc
//
//	@Summary		Publish a group-based census for voting
//	@Description	Publish a census based on a specific organization members group for voting. Requires Manager/Admin role.
//	@Description	Returns published census with credentials.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id		path		string								true	"Census ID"
//	@Param			groupId	path		string								true	"Group ID"
//	@Param			request	body		apicommon.PublishCensusGroupRequest	true	"Census authentication configuration"
//	@Success		200		{object}	apicommon.PublishedCensusResponse
//	@Failure		400		{object}	errors.Error	"Invalid census ID or group ID"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		404		{object}	errors.Error	"Census not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/group/{groupid}/publish [post]
func (a *API) publishCensusGroupHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	groupID := internal.HexBytes{}
	if err := groupID.ParseString(chi.URLParam(r, "groupid")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong group ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrCensusNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	// Parse request
	publishInfo := &apicommon.PublishCensusGroupRequest{}
	if err := json.NewDecoder(r.Body).Decode(&publishInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	census.AuthFields = publishInfo.AuthFields
	census.TwoFaFields = publishInfo.TwoFaFields
	census.Weighted = publishInfo.Weighted

	if len(census.Published.Root) > 0 {
		// if the census is already published, return the censusInfo
		apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
			URI:  census.Published.URI,
			Root: census.Published.Root,
		})
		return
	}

	if err := a.subscriptions.OrgCanPublishGroupCensus(census, groupID.String()); err != nil {
		if apiErr := (errors.Error{}); errors.As(err, &apiErr) {
			apiErr.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	inserted, err := a.db.PopulateGroupCensus(census, groupID.String())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// build the census and store it
	cspSignerPubKey, err := a.csp.PubKey()
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("failed to get CSP public key").Write(w)
		return
	}
	var rootHex internal.HexBytes
	if err := rootHex.ParseString(cspSignerPubKey.String()); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(census.TwoFaFields) == 0 && len(census.AuthFields) == 0 {
		// non CSP censuses
		errors.ErrCensusTypeNotFound.Write(w)
		return
	}

	census.Published.Root = rootHex
	census.Published.URI = a.serverURL + "/process"
	census.Published.CreatedAt = time.Now()

	if _, err := a.db.SetCensus(census); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	apicommon.HTTPWriteJSON(w, &apicommon.PublishedCensusResponse{
		URI:  census.Published.URI,
		Root: rootHex,
		Size: inserted,
	})
}

// censusParticipantsHandler godoc
//
//	@Summary		Get census participants
//	@Description	Retrieve participants of a census by ID. Requires Manager/Admin role.
//	@Tags			census
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			id	path		string	true	"Census ID"
//	@Success		200	{object}	apicommon.CensusParticipantsResponse
//	@Failure		400	{object}	errors.Error	"Invalid census ID"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Census not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/census/{id}/participants [get]
func (a *API) censusParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	censusID := internal.HexBytes{}
	if err := censusID.ParseString(chi.URLParam(r, "id")); err != nil {
		errors.ErrMalformedURLParam.Withf("wrong census ID").Write(w)
		return
	}

	// retrieve census
	census, err := a.db.Census(censusID.String())
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrCensusNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user does not have the necessary permissions in the organization").Write(w)
		return
	}

	participants, err := a.db.CensusParticipants(censusID.String())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	participantMemberIDs := make([]string, len(participants))
	for i, p := range participants {
		participantMemberIDs[i] = p.ParticipantID
	}

	apicommon.HTTPWriteJSON(w, &apicommon.CensusParticipantsResponse{
		CensusID:  censusID.String(),
		MemberIDs: participantMemberIDs,
	})
}
