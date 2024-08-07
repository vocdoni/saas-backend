package api

import (
	"context"
	"net/http"

	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/db"
)

// MetadataKey is a type to define the key for the metadata stored in the
// context.
type MetadataKey string

// userMetadataKey is the key used to store the user in the context.
const userMetadataKey MetadataKey = "user"

// authenticator is a middleware that authenticates the user and returns a JWT
// token. If successful, the decodes the user identifier (its email) from the
// JWT token and gets the user information from the database, then adds the user
// data to the request context and passes it to the next handler.
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
		// retrieve the `userId` from the claims and add it to the HTTP header
		userEmail := claims["userId"].(string)
		// get the user from the database
		user, err := a.db.UserByEmail(userEmail)
		if err != nil {
			if err == db.ErrNotFound {
				ErrUnauthorized.Withf("user not found").Write(w)
				return
			}
			ErrGenericInternalServerError.Withf("could not retrieve user from database: %v", err).Write(w)
			return
		}
		// add the user to the context
		ctx := context.WithValue(r.Context(), userMetadataKey, *user)
		// token is authenticated, pass it through with the new context with the
		// user information
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// userFromContext retrieves the user from the context provided, expected to be
// the context of a request handled by the authenticator middleware.
func userFromContext(ctx context.Context) (*db.User, bool) {
	rawUser, ok := ctx.Value(userMetadataKey).(db.User)
	if ok {
		return &rawUser, ok
	}
	return nil, false
}
