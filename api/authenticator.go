package api

import (
	"context"
	"crypto/sha256"
	"net/http"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// authHandler is a handler that authenticates the user and returns a JWT token.
// If successful, the user identifier is added to the HTTP header as `X-User-Id`,
// so that it can be used by the next handlers.
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

// makeToken creates a JWT token for the given user identifier.
// The token is signed with the API secret, following the JWT specification.
// The token is valid for the period specified on jwtExpiration constant.
func (a *API) makeToken(id string) (*LoginResponse, error) {
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

func hashPassword(password string) []byte {
	return sha256.New().Sum([]byte(passwordSalt + password))
}
