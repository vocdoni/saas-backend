package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/util"
)

// organizationFromRequest helper function allows to get the organization info
// related to the request provided. It gets the organization address from the
// URL parameters and retrieves the organization from the database. If the
// organization is a suborganization, it also retrieves the parent organization.
func (a *API) organizationFromRequest(r *http.Request) (org *db.Organization, parent *db.Organization, found bool) {
	orgAddress := chi.URLParam(r, "address")
	// if the organization address is not empty, get the organization from
	// the database and add it to the context
	if orgAddress != "" {
		// get the organization from the database
		if org, parent, err := a.db.Organization(orgAddress, true); err == nil {
			return org, parent, true
		}
	}
	return nil, nil, false
}

// buildLoginResponse creates a JWT token for the given user identifier.
// The token is signed with the API secret, following the JWT specification.
// The token is valid for the period specified on jwtExpiration constant.
func (a *API) buildLoginResponse(id string) (*apicommon.LoginResponse, error) {
	j := jwt.New()
	if err := j.Set("userId", id); err != nil {
		return nil, err
	}
	if err := j.Set(jwt.ExpirationKey, time.Now().Add(jwtExpiration).UnixNano()); err != nil {
		return nil, err
	}
	lr := apicommon.LoginResponse{}
	lr.Expirity = time.Now().Add(jwtExpiration)
	jmap, err := j.AsMap(context.Background())
	if err != nil {
		return nil, err
	}
	_, lr.Token, _ = a.auth.Encode(jmap)
	return &lr, nil
}

// buildWebAppURL method allows to build a URL for the web application using
// the path and the parameters provided. It returns the URL as a string and an
// error if the URL could not be built. It encodes the parameters in the query
// string of the URL to prevent any issues with special characters. It returns
// the URL as a string and an error if the URL could not be built.
func (a *API) buildWebAppURL(path string, params map[string]any) (string, error) {
	// parse the web app URL with the path provided
	url, err := url.Parse(a.webAppURL + path)
	if err != nil {
		return "", err
	}
	// encode the parameters in the query string of the URL
	q := url.Query()
	for k, v := range params {
		q.Set(k, fmt.Sprint(v))
	}
	// include the encoded query string in the URL
	url.RawQuery = q.Encode()
	return url.String(), nil
}

// generateVerificationCodeAndLink method generates and stores in the database
// a new verification code for the target provided according to the database
// code type selected. Then it generates a verification link to the web app
// with correct web app uri and link parameters for the code type selected.
// It returns the generated verification code, the link to the web app and
// an error if the verification code could not be generated or stored in the
// database.
func (a *API) generateVerificationCodeAndLink(target any, codeType db.CodeType) (string, string, error) {
	// generate verification code if the mail service is available, if not
	// the verification code will not be sent but stored in the database
	// generated with just the user email to mock the verification process
	var code string
	if a.mail != nil {
		code = util.RandomHex(apicommon.VerificationCodeLength)
	}
	var webAppURI string
	var linkParams map[string]any
	switch codeType {
	case db.CodeTypeVerifyAccount, db.CodeTypePasswordReset:
		// the target should be a database user
		user, ok := target.(*db.User)
		if !ok {
			return "", "", fmt.Errorf("invalid target type")
		}
		// generate the verification code for the user and the expiration time
		hashCode := internal.HashVerificationCode(user.Email, code)
		exp := time.Now().Add(apicommon.VerificationCodeExpiration)
		// store the verification code in the database
		if err := a.db.SetVerificationCode(&db.User{ID: user.ID}, hashCode, codeType, exp); err != nil {
			return "", "", err
		}
		// set the web app URI and the link parameters
		webAppURI = mailtemplates.VerifyAccountNotification.WebAppURI
		if codeType == db.CodeTypePasswordReset {
			webAppURI = mailtemplates.PasswordResetNotification.WebAppURI
		}
		linkParams = map[string]any{
			"email": user.Email,
			"code":  code,
		}
	case db.CodeTypeOrgInvite:
		// the target should be a database organization invite
		invite, ok := target.(*db.OrganizationInvite)
		if !ok {
			return "", "", fmt.Errorf("invalid target type")
		}
		// set the verification code for the organization invite and the
		// expiration time
		invite.InvitationCode = code
		invite.Expiration = time.Now().Add(apicommon.InvitationExpiration)
		// store the organization invite in the database
		if err := a.db.CreateInvitation(invite); err != nil {
			return "", "", err
		}
		// set the web app URI and the link parameters
		webAppURI = mailtemplates.InviteNotification.WebAppURI
		linkParams = map[string]any{
			"email":   invite.NewUserEmail,
			"code":    invite.InvitationCode,
			"address": invite.OrganizationAddress,
		}
	default:
		return "", "", fmt.Errorf("invalid code type")
	}
	// generate the verification link to the web app with the selected uri
	// and the link parameters
	verificationLink, err := a.buildWebAppURL(webAppURI, linkParams)
	return code, verificationLink, err
}
