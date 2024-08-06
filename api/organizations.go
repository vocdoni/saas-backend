package api

import (
	"encoding/json"
	"net/http"

	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
)

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
