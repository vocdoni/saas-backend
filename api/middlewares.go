package api

import (
	"context"
	"net/http"

	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
)

// authenticator is a middleware that authenticates the user and returns a JWT
// token. If successful, the decodes the user identifier (its email) from the
// JWT token and gets the user information from the database, then adds the user
// data to the request context and passes it to the next handler.
func (a *API) authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, claims, err := jwtauth.FromContext(r.Context())
		if err != nil {
			errors.ErrUnauthorized.Write(w)
			return
		}
		if token == nil || jwt.Validate(token, jwt.WithRequiredClaim("userId")) != nil {
			errors.ErrUnauthorized.Withf("userId claim not found in JWT token").Write(w)
			return
		}
		// retrieve the `userId` from the claims and add it to the HTTP header
		userEmail := claims["userId"].(string)
		// get the user from the database
		user, err := a.db.UserByEmail(userEmail)
		if err != nil {
			if err == db.ErrNotFound {
				errors.ErrUnauthorized.Withf("user not found").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.Withf("could not retrieve user from database: %v", err).Write(w)
			return
		}
		// check if the user is already verified
		if !user.Verified {
			errors.ErrUserNoVerified.With("user account not verified").Write(w)
			return
		}
		// add the user to the context
		ctx := context.WithValue(r.Context(), apicommon.UserMetadataKey, *user)
		// token is authenticated, pass it through with the new context with the
		// user information
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
