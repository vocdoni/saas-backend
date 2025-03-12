package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi"
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
)

type cspHandlers struct {
	csp    *csp.CSP
	mainDB *db.MongoStorage
}

func New(c *csp.CSP, db *db.MongoStorage) *cspHandlers {
	return &cspHandlers{
		csp:    c,
		mainDB: db,
	}
}

func (c *cspHandlers) BundleAuthHandler(w http.ResponseWriter, r *http.Request) {
	bundleID := new(internal.HexBytes)
	if err := bundleID.ParseString(chi.URLParam(r, "bundleId")); err != nil {
		errors.ErrMalformedURLParam.Withf("invalid bundle ID").Write(w)
		return
	}

	stepString := chi.URLParam(r, "step")
	step, err := strconv.Atoi(stepString)
	if err != nil || (step != 0 && step != 1) {
		errors.ErrMalformedURLParam.Withf("wrong step ID").Write(w)
		return
	}

	bundle, err := c.mainDB.ProcessBundle(*bundleID)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrMalformedURLParam.Withf("bundle not found").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	if len(bundle.Processes) == 0 {
		errors.ErrInvalidOrganizationData.Withf("bundle has no processes").Write(w)
		return
	}

	switch step {
	case 0:
		// authToken, err := a.initiateBundleAuthRequest(r, bundleID.String(), bundle.Census.ID.Hex())
		// if err != nil {
		// 	errors.ErrUnauthorized.WithErr(err).Write(w)
		// 	return
		// }
		// httpWriteJSON(w, &twofactorResponse{AuthToken: authToken})
		return
	case 1:
		// var req AuthRequest
		// if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// 	errors.ErrMalformedBody.Write(w)
		// 	return
		// }
		// authResp := a.twofactor.Auth(bundleID.Hex(), req.AuthToken, req.AuthData)
		// if !authResp.Success {
		// 	errors.ErrUnauthorized.WithErr(stderrors.New(authResp.Error)).Write(w)
		// 	return
		// }
		// httpWriteJSON(w, &twofactorResponse{AuthToken: authResp.AuthToken})
		return
	}
}
