package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

var regexpEmail = regexp.MustCompile(`^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$`)

// isEmailValid helper function allows to validate an email address.
func isEmailValid(email string) bool {
	return regexpEmail.MatchString(email)
}

// hashPassword helper function allows to hash a password using a salt.
func hashPassword(password string) []byte {
	return sha256.New().Sum([]byte(passwordSalt + password))
}

// authHandler is a middleware that authenticates the user and returns a JWT
// token. If successful, the user identifier is added to the HTTP header as
// `X-User-Id`, so that it can be used by the next handlers.
func (a *API) authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, claims, err := jwtauth.FromContext(r.Context())
		if err != nil {
			ErrUnauthorized.Write(w)
			return
		}
		if token == nil || jwt.Validate(token, jwt.WithRequiredClaim("userId")) != nil {
			ErrUnauthorized.Withf("userId claim not found in JWT token").Write(w)
			return
		}
		// Retrieve the `userId` from the claims and add it to the HTTP header
		r.Header.Add("X-User-Id", claims["userId"].(string))
		// Token is authenticated, pass it through
		next.ServeHTTP(w, r)
	})
}

// buildLoginResponse creates a JWT token for the given user identifier.
// The token is signed with the API secret, following the JWT specification.
// The token is valid for the period specified on jwtExpiration constant.
func (a *API) buildLoginResponse(id string) (*LoginResponse, error) {
	j := jwt.New()
	if err := j.Set("userId", id); err != nil {
		return nil, err
	}
	if err := j.Set(jwt.ExpirationKey, time.Now().Add(jwtExpiration).UnixNano()); err != nil {
		return nil, err
	}
	lr := LoginResponse{}
	lr.Expirity = time.Now().Add(jwtExpiration)
	jmap, err := j.AsMap(context.Background())
	if err != nil {
		return nil, err
	}
	_, lr.Token, _ = a.auth.Encode(jmap)
	return &lr, nil
}

func (a *API) organizationSignerForUser(address, userEmail string) (*ethereum.SignKeys, error) {
	// get the user from the database
	dbUser, err := a.db.UserByEmail(userEmail)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, fmt.Errorf("user not found")
		}
		return nil, fmt.Errorf("could not retrieve user from database: %v", err)
	}
	// get the organization from the database
	dbOrg, err := a.db.Organization(address)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, fmt.Errorf("organization not found")
		}
		return nil, fmt.Errorf("could not retrieve organization from database: %v", err)
	}
	// check if the user is part of the organization
	isUserOrg := false
	for _, userOrgs := range dbUser.Organizations {
		if userOrgs.Address == dbOrg.Address {
			isUserOrg = true
			break
		}
	}
	if !isUserOrg {
		return nil, fmt.Errorf("user is not part of the organization")
	}
	// get the user signer from the user identifier
	organizationSigner, err := account.OrganizationSigner(a.secret, userEmail, dbOrg.Nonce)
	if err != nil {
		return nil, fmt.Errorf("could not restore the signer of the organization: %v", err)
	}
	return organizationSigner, nil
}

// httpWriteJSON helper function allows to write a JSON response.
func httpWriteJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}

// httpWriteOK helper function allows to write an OK response.
func httpWriteOK(w http.ResponseWriter) {
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte("\n")); err != nil {
		log.Warnw("failed to write on response", "error", err)
	}
}
