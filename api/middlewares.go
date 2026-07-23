package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
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

// Sentinel errors returned by userFromToken so callers can map them to the right response (or, for
// the optional path, treat any of them as "anonymous").
var (
	errInvalidUserClaim = fmt.Errorf("invalid or missing userId claim")
	errUserNotVerified  = fmt.Errorf("user account not verified")
)

// userFromToken resolves and validates the user referenced by an already signature/temporal-verified
// JWT (the userId claim carries the user's email). It is shared by authenticator (required auth) and
// optionalUser (optional auth): the callers verify the token first (via the Verifier middleware or
// jwtauth.VerifyToken), and this does the claim → user → verified checks in one place so the two
// paths cannot drift. Returns a nil user with a typed error the caller maps to a response.
func (a *API) userFromToken(token jwt.Token) (*db.User, error) {
	if token == nil || jwt.Validate(token, jwt.WithRequiredClaim("userId")) != nil {
		return nil, errInvalidUserClaim
	}
	claim, _ := token.Get("userId")
	email, ok := claim.(string)
	if !ok {
		return nil, errInvalidUserClaim
	}
	user, err := a.db.UserByEmail(email)
	if err != nil {
		return nil, err // db.ErrNotFound or an unexpected DB error
	}
	if !user.Verified {
		return nil, errUserNotVerified
	}
	return user, nil
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
	token, err := jwtauth.VerifyToken(a.auth, raw) // verifies signature + temporal claims (exp/nbf/iat)
	if err != nil {
		return nil
	}
	user, err := a.userFromToken(token) // any error → treat as anonymous
	if err != nil {
		return nil
	}
	return user
}

// apiKeyActingUser loads the verified db.User an API key acts as (its CreatedBy owner), or nil if that
// user is missing or unverified. Single source of truth for "which user does a key act as", shared by
// authenticateAPIKey (required auth) and userFromAPIKey (optional public-read auth) so the two cannot
// drift — a future hardening check on the acting user (e.g. a suspended-user flag) lives here once.
func (a *API) apiKeyActingUser(key *db.APIKey) *db.User {
	user, err := a.db.UserByEmail(key.CreatedBy)
	if err != nil || !user.Verified {
		return nil
	}
	return user
}

// userFromAPIKey resolves the db.User an API key acts as, but only when the key is valid (not
// revoked/expired) and holds requiredScope. Returns nil otherwise. Like authenticateAPIKey, the key
// acts as its creating user, so org authorization still flows through that user's roles. Used by the
// optional-auth path of public handlers, so it never writes a response and skips last-used tracking.
func (a *API) userFromAPIKey(raw, requiredScope string) *db.User {
	key, err := a.db.APIKeyByHash(hashAPIKey(raw))
	if err != nil || !slices.Contains(key.Scopes, requiredScope) {
		return nil
	}
	return a.apiKeyActingUser(key)
}

// optionalCallerUser resolves the acting db.User from either a JWT bearer session or a voting:write
// scoped API key (which acts as its creating user), or nil when the request is anonymous or the
// credential is invalid/insufficient. Never writes a response — for public handlers that reveal extra
// data to an authorized caller.
func (a *API) optionalCallerUser(r *http.Request) *db.User {
	if raw := bearerToken(r); looksLikeAPIKey(raw) {
		return a.userFromAPIKey(raw, ScopeVotingWrite)
	}
	return a.optionalUser(r)
}

// optionalManager reports whether the request's optional credential (JWT or scoped API key) resolves
// to a user with Manager or Admin role for orgAddress. Anonymous or insufficient callers get false —
// the public view. Never writes a response.
func (a *API) optionalManager(r *http.Request, orgAddress common.Address) bool {
	user := a.optionalCallerUser(r)
	return user != nil &&
		(user.HasRoleFor(orgAddress, db.ManagerRole) || user.HasRoleFor(orgAddress, db.AdminRole))
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
		token, _, err := jwtauth.FromContext(r.Context())
		if err != nil {
			errors.ErrUnauthorized.Write(w)
			return
		}
		// resolve+validate the user from the verified token (shared with optionalUser); map the
		// typed failures back to the same responses this middleware has always returned.
		user, err := a.userFromToken(token)
		if err != nil {
			switch err {
			case errUserNotVerified:
				errors.ErrUserNoVerified.With("user account not verified").Write(w)
			case db.ErrNotFound:
				errors.ErrUnauthorized.Withf("user not found").Write(w)
			case errInvalidUserClaim:
				errors.ErrUnauthorized.Withf("invalid or missing userId claim").Write(w)
			default:
				errors.ErrGenericInternalServerError.Withf("could not retrieve user from database: %v", err).Write(w)
			}
			return
		}
		// add the user to the context and pass the authenticated request through
		ctx := context.WithValue(r.Context(), apicommon.UserMetadataKey, *user)
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
	// the key acts as its creating user (shared resolver, so this and the public-read path can't drift),
	// reusing the existing per-organization role checks
	user := a.apiKeyActingUser(key)
	if user == nil {
		errors.ErrInvalidAPIKey.Withf("key owner missing or unverified").Write(w)
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
