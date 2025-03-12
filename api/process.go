package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

// createProcessHandler creates a new voting process.
// Requires Manager/Admin role. Returns 201 on success.
func (a *API) createProcessHandler(w http.ResponseWriter, r *http.Request) {
	processID := internal.HexBytes{}
	if err := processID.ParseString(chi.URLParam(r, "processId")); err != nil {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	processInfo := &CreateProcessRequest{}
	if err := json.NewDecoder(r.Body).Decode(&processInfo); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}

	if processInfo.PublishedCensusRoot == nil || processInfo.CensusID == nil {
		errors.ErrMalformedBody.Withf("missing published census root or ID").Write(w)
		return
	}

	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	pubCensus, err := a.db.PublishedCensus(
		processInfo.PublishedCensusRoot.String(),
		processInfo.PublishedCensusURI,
		processInfo.CensusID.String(),
	)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("published census not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the user has the necessary permissions
	if !user.HasRoleFor(pubCensus.Census.OrgAddress, db.ManagerRole) && !user.HasRoleFor(pubCensus.Census.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of organization").Write(w)
		return
	}

	// check that the process does not exist
	if _, err := a.db.Process(processID); err == nil {
		errors.ErrDuplicateConflict.Withf("process already exists").Write(w)
		return
	}

	// get the participants
	orgParticipants, err := a.db.OrgParticipantsMemberships(
		pubCensus.Census.OrgAddress,
		pubCensus.Census.ID.Hex(),
		"",
		[]internal.HexBytes{processID},
	)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// check the census type
	switch pubCensus.Census.Type {
	case db.CensusTypeSMSorMail, db.CensusTypeMail, db.CensusTypeSMS:
		if err := a.twofactor.AddProcess(pubCensus.Census.Type, orgParticipants); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	default:
		errors.ErrNotSupported.Withf("census type not supported").Write(w)
		return
	}

	// finally create the process
	process := &db.Process{
		ID:              processID,
		PublishedCensus: *pubCensus,
		Metadata:        processInfo.Metadata,
		OrgAddress:      pubCensus.Census.OrgAddress,
	}

	if err := a.db.SetProcess(process); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteOK(w)
}

// processInfoHandler retrieves voting process information by ID.
// Returns process details including census and metadata.
func (a *API) processInfoHandler(w http.ResponseWriter, r *http.Request) {
	processID := chi.URLParam(r, "processId")
	if len(processID) == 0 {
		errors.ErrMalformedURLParam.Withf("missing process ID").Write(w)
		return
	}

	process, err := a.db.Process([]byte(processID))
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("process not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	httpWriteJSON(w, process)
}
