package api

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
	"go.vocdoni.io/dvote/util"
)

// These consts define the keywords for query (?param=), url (/url/param/) and POST params.
// Note: In JS/TS acronyms like "ID" are camelCased as in "Id".
const (
	ParamPage  = "page"
	ParamLimit = "limit"
)

var (
	ErrPageNotFound    = fmt.Errorf("page not found")
	ErrCantParseNumber = fmt.Errorf("cannot parse number")
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
		// get the organization from the database with its parent
		if org, parent, err := a.db.OrganizationWithParent(common.HexToAddress(orgAddress)); err == nil {
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
//
//revive:disable:import-shadowing
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

// getLanguageFromContext extracts the language from the request context.
// Returns apicommon.DefaultLang as default if no language is found.
func (*API) getLanguageFromContext(ctx context.Context) string {
	if lang, ok := ctx.Value(apicommon.LangMetadataKey).(string); ok && lang != "" {
		return lang
	}
	return apicommon.DefaultLang // default fallback
}

// generateVerificationCodeAndLink method generates and stores in the database
// a new verification code for the target provided according to the database
// code type selected. Then it generates a verification link to the web app
// with correct web app uri and link parameters for the code type selected.
// It returns the generated verification code, the link to the web app and
// an error if the verification code could not be generated or stored in the
// database.
func (a *API) generateVerificationCodeAndLink(target any, codeType db.CodeType) (
	verificationCode string, verificationLink string, err error,
) {
	// generate verification code if the mail service is available, if not
	// the verification code will not be sent but stored in the database
	// generated with just the user email to mock the verification process
	if a.mail != nil {
		verificationCode = util.RandomHex(apicommon.VerificationCodeLength)
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
		sealedCode, err := internal.SealToken(verificationCode, user.Email, a.secret)
		if err != nil {
			return "", "", err
		}
		exp := time.Now().Add(apicommon.VerificationCodeExpiration)
		// store the verification code in the database
		if err := a.db.SetVerificationCode(&db.User{ID: user.ID}, sealedCode, codeType, exp); err != nil {
			return "", "", err
		}
		// set the web app URI and the link parameters
		webAppURI = mailtemplates.VerifyAccountNotification.WebAppURI
		if codeType == db.CodeTypePasswordReset {
			webAppURI = mailtemplates.PasswordResetNotification.WebAppURI
		}
		linkParams = map[string]any{
			"email": user.Email,
			"code":  verificationCode,
		}
	case db.CodeTypeOrgInvite:
		// the target should be a database organization invite
		invite, ok := target.(*db.OrganizationInvite)
		if !ok {
			return "", "", fmt.Errorf("invalid target type")
		}
		// set the verification code for the organization invite and the
		// expiration time
		invite.InvitationCode = verificationCode
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
	case db.CodeTypeOrgInviteUpdate:
		// when the invitation is update there is no need to store it here
		// but just return the verification code and link
		invite, ok := target.(*db.OrganizationInvite)
		if !ok {
			return "", "", fmt.Errorf("invalid target type")
		}
		// set the verification code for the organization invite and the
		// expiration time
		invite.InvitationCode = verificationCode

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
	verificationLink, err = a.buildWebAppURL(webAppURI, linkParams)
	return verificationCode, verificationLink, err
}

// generateVerificationLink method generates a verification link for the user
// provided using the code provided and the API web app configuration.
// It returns the generated verification link and an error if the link
// could not be generated.
func (a *API) generateVerificationLink(user *db.User, code string) (string, error) {
	webAppURI := mailtemplates.VerifyAccountNotification.WebAppURI
	linkParams := map[string]any{
		"email": user.Email,
		"code":  code,
	}

	return a.buildWebAppURL(webAppURI, linkParams)
}

// calculatePagination calculates PreviousPage, NextPage and LastPage.
//
// If page is negative or higher than LastPage, returns ErrPageNotFound
func calculatePagination(page, limit, totalItems int64) (*apicommon.Pagination, error) {
	lastp := int64(math.Ceil(float64(totalItems) / float64(limit)))
	if totalItems == 0 {
		lastp = 1
	}

	if page > lastp || page < 1 {
		return nil, ErrPageNotFound
	}

	var prevp, nextp *int64
	if page > 1 {
		prevPage := page - 1
		prevp = &prevPage
	}
	if page < lastp {
		nextPage := page + 1
		nextp = &nextPage
	}

	return &apicommon.Pagination{
		TotalItems:   totalItems,
		PreviousPage: prevp,
		CurrentPage:  page,
		NextPage:     nextp,
		LastPage:     lastp,
	}, nil
}

// parsePaginationParams returns a PaginationParams filled with the passed params
func parsePaginationParams(paramPage, paramLimit string) (apicommon.PaginationParams, error) {
	page, err := parsePage(paramPage)
	if err != nil {
		return apicommon.PaginationParams{}, err
	}

	limit, err := parseLimit(paramLimit)
	if err != nil {
		return apicommon.PaginationParams{}, err
	}

	return apicommon.PaginationParams{
		Page:  page,
		Limit: limit,
	}, nil
}

// parseNumber parses a string into an int64.
//
// If the string is not parseable, returns an APIerror.
//
// The empty string "" is treated specially, returns 0 with no error.
func parseNumber(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, ErrCantParseNumber
	}
	return i, nil
}

// parsePage parses a string into an int.
//
// If the resulting int is negative or 0, returns ErrNoSuchPage.
// If the string is not parseable, returns an APIerror.
//
// The empty string "" is treated specially, returns 1 with no error.
func parsePage(s string) (int64, error) {
	if s == "" {
		return 1, nil
	}
	page, err := parseNumber(s)
	if err != nil {
		return 0, err
	}
	if page < 1 {
		return 0, ErrPageNotFound
	}
	return page, nil
}

// parseLimit parses a string into an int.
//
// The empty string "" is treated specially, returns DefaultItemsPerPage with no error.
// If the resulting int is higher than MaxItemsPerPage, returns MaxItemsPerPage.
// If the resulting int is 0 or negative, returns DefaultItemsPerPage.
//
// If the string is not parseable, returns an APIerror.
func parseLimit(s string) (int64, error) {
	limit, err := parseNumber(s)
	if err != nil {
		return 0, err
	}
	if limit > apicommon.MaxItemsPerPage {
		limit = apicommon.MaxItemsPerPage
	}
	if limit <= 0 {
		limit = apicommon.DefaultItemsPerPage
	}
	return limit, nil
}
