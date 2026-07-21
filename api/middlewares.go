package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/jwtauth/v5"
	"github.com/lestrrat-go/jwx/v2/jwt"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/log"
)

// bearerToken extracts the bearer credential from the Authorization header, or "" if absent.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}

// optionalUser resolves the authenticated user from an optional JWT bearer token, or nil when the
// request is anonymous or the token is absent/invalid/unverified. Unlike authenticator it never
// writes a response — it lets an otherwise-public handler reveal extra data to an authenticated
// user without requiring auth. JWT sessions only (API keys, which require a per-route scope, are
// not resolved here).
func (a *API) optionalUser(r *http.Request) *db.User {
	raw := bearerToken(r)
	if raw == "" || looksLikeAPIKey(raw) {
		return nil
	}
	token, err := jwtauth.VerifyToken(a.auth, raw)
	if err != nil || token == nil {
		return nil
	}
	claim, ok := token.Get("userId") // the userId claim carries the user's email (see authenticator)
	if !ok {
		return nil
	}
	email, ok := claim.(string)
	if !ok {
		return nil
	}
	user, err := a.db.UserByEmail(email)
	if err != nil || !user.Verified {
		return nil
	}
	return user
}

// authenticator is a middleware that authenticates the user and returns a JWT
// token. If successful, the decodes the user identifier (its email) from the
// JWT token and gets the user information from the database, then adds the user
// data to the request context and passes it to the next handler.
//
// It also accepts API keys: a Bearer credential with the API-key prefix is resolved against the
// apiKeys collection (instead of being parsed as a JWT), enforcing the per-route allowlist and
// the key's scopes. On success the owning user is placed in the context exactly as for a JWT, so
// downstream role checks are unchanged.
func (a *API) authenticator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw := bearerToken(r); looksLikeAPIKey(raw) {
			a.authenticateAPIKey(w, r, next, raw)
			return
		}
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
		userIDValue, ok := claims["userId"]
		if !ok {
			errors.ErrUnauthorized.Withf("userId claim not found in JWT token").Write(w)
			return
		}
		userEmail, ok := userIDValue.(string)
		if !ok {
			errors.ErrUnauthorized.Withf("userId claim is not a string").Write(w)
			return
		}
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

// authenticateAPIKey resolves a request authenticated with an API key. It validates the key,
// enforces the per-route allowlist and the key's scopes, then loads the key's owning user into
// the context (so existing role checks work) before delegating to the next handler.
func (a *API) authenticateAPIKey(w http.ResponseWriter, r *http.Request, next http.Handler, raw string) {
	key, err := a.db.APIKeyByHash(hashAPIKey(raw))
	if err != nil {
		errors.ErrInvalidAPIKey.Write(w)
		return
	}
	// deny-by-default: the endpoint must opt into API-key access, and the key must hold its scope
	pattern := chi.RouteContext(r.Context()).RoutePattern()
	scope, allowed := requiredScopeForRoute(r.Method, pattern)
	if !allowed {
		errors.ErrAPIKeyNotAllowed.Write(w)
		return
	}
	if !slices.Contains(key.Scopes, scope) {
		errors.ErrInsufficientAPIKeyScope.Withf("endpoint requires the %q scope", scope).Write(w)
		return
	}
	// the key acts as its creating user, reusing the existing per-organization role checks
	user, err := a.db.UserByEmail(key.CreatedBy)
	if err != nil {
		errors.ErrInvalidAPIKey.Withf("key owner no longer exists").Write(w)
		return
	}
	if !user.Verified {
		errors.ErrUserNoVerified.With("user account not verified").Write(w)
		return
	}
	// best-effort last-used tracking; never block the request on it
	if err := a.db.TouchAPIKey(key.ID, time.Now()); err != nil {
		log.Warnw("could not update API key last-used time", "keyID", key.ID, "error", err)
	}
	ctx := context.WithValue(r.Context(), apicommon.UserMetadataKey, *user)
	ctx = context.WithValue(ctx, apicommon.APIKeyMetadataKey, *key)
	next.ServeHTTP(w, r.WithContext(ctx))
}

// setLang is a middleware that sets the lang parameter in the request context
// and passes it to the next handler.
func (*API) setLang(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		// get the lang from URL params
		if lang := chi.URLParam(r, string(apicommon.LangMetadataKey)); lang != "" {
			ctx = context.WithValue(r.Context(), apicommon.LangMetadataKey, lang)
		}
		// get the lang from query params
		if lang := r.URL.Query().Get(string(apicommon.LangMetadataKey)); lang != "" {
			ctx = context.WithValue(r.Context(), apicommon.LangMetadataKey, lang)
		}
		// add lang to the context
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
