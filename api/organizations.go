package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/log"
)

// createOrganizationHandler handles the request to create a new organization.
// If the organization is a suborganization, the parent organization must be
// specified in the request body, and the user must be an admin of the parent.
// If the parent organization is alread a suborganization, an error is returned.
func (a *API) createOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	// get the user identifier from the HTTP header
	userID := r.Header.Get("X-User-Id")
	if userID == "" {
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
	signer, nonce, err := account.NewSigner(a.secret, userID)
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
		dbParentOrg, err := a.db.Organization(orgInfo.Parent.Address)
		if err != nil {
			if err == db.ErrNotFound {
				ErrOrganizationNotFound.Withf("parent organization not found").Write(w)
				return
			}
			ErrGenericInternalServerError.Withf("could not get parent organization: %v", err).Write(w)
			return
		}
		log.Info(dbParentOrg)
		if dbParentOrg.Parent != "" {
			ErrMalformedBody.Withf("parent organization is already a suborganization").Write(w)
			return
		}
		isAdmin, err := a.db.IsMemberOf(userID, dbParentOrg.Address, db.AdminRole)
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
		Creator:         userID,
		Nonce:           nonce,
		Type:            db.OrganizationType(orgInfo.Type),
		Description:     orgInfo.Description,
		Size:            orgInfo.Size,
		Color:           orgInfo.Color,
		Logo:            orgInfo.Logo,
		Subdomain:       orgInfo.Subdomain,
		Timezone:        orgInfo.Timezone,
		Parent:          parentOrg,
		TokensPurchased: 0,
		TokensRemaining: 0,
	}); err != nil {
		ErrGenericInternalServerError.Write(w)
		return
	}
	// send the organization back to the user
	httpWriteOK(w)
}

// organizationInfoHandler handles the request to get the information of an
// organization.
func (a *API) organizationInfoHandler(w http.ResponseWriter, r *http.Request) {
	// get the organization address from the URL
	orgAddress := a.urlParam(r, "address")
	if orgAddress == "" {
		ErrMalformedURLParam.Withf("missing organization address").Write(w)
		return
	}
	// get the organization from the database
	org, err := a.db.Organization(orgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			ErrOrganizationNotFound.Write(w)
			return
		}
		ErrGenericInternalServerError.Withf("could not get organization: %v", err).Write(w)
		return
	}
	// get the parent organization from the database if any
	var parentOrg *OrganizationInfo
	if org.Parent != "" {
		dbParentOrg, err := a.db.Organization(org.Parent)
		if err != nil {
			ErrGenericInternalServerError.Withf("could not get parent organization: %v", err).Write(w)
			return
		}
		parentOrg = &OrganizationInfo{
			Address:     dbParentOrg.Address,
			Name:        dbParentOrg.Name,
			Type:        string(dbParentOrg.Type),
			Description: dbParentOrg.Description,
			Size:        dbParentOrg.Size,
			Color:       dbParentOrg.Color,
			Logo:        dbParentOrg.Logo,
			Subdomain:   dbParentOrg.Subdomain,
			Timezone:    dbParentOrg.Timezone,
		}
	}
	// send the organization back to the user
	httpWriteJSON(w, OrganizationInfo{
		Address:     org.Address,
		Name:        org.Name,
		Type:        string(org.Type),
		Description: org.Description,
		Size:        org.Size,
		Color:       org.Color,
		Logo:        org.Logo,
		Subdomain:   org.Subdomain,
		Timezone:    org.Timezone,
		Parent:      parentOrg,
	})
}
