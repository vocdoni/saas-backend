package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/jwtauth/v5"

	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

const (
	jwtExpiration = 360 * time.Hour // 15 days
	passwordSalt  = "vocdoni365"    // salt for password hashing
)

// API type represents the API HTTP server with JWT authentication capabilities.
type API struct {
	Router *chi.Mux
	auth   *jwtauth.JWTAuth
	client *apiclient.HTTPclient
}

// New creates a new API HTTP server. It does not start the server. Use Start() for that.
func New(secret string, client *apiclient.HTTPclient) *API {
	return &API{
		auth:   jwtauth.New("HS256", []byte(secret), nil),
		client: client,
	}
}

// Start starts the API HTTP server (non blocking).
func (a *API) Start(host string, port int) {
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf("%s:%d", host, port), a.router()); err != nil {
			log.Fatalf("failed to start the API server: %v", err)
		}
	}()
}

// router creates the router with all the routes and middleware.
func (a *API) router() http.Handler {
	// Create the router with a basic middleware stack
	r := chi.NewRouter()
	r.Use(cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: true,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}).Handler)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Throttle(100))
	r.Use(middleware.ThrottleBacklog(5000, 40000, 60*time.Second))
	r.Use(middleware.Timeout(45 * time.Second))

	// Protected routes
	r.Group(func(r chi.Router) {
		// Seek, verify and validate JWT tokens
		r.Use(jwtauth.Verifier(a.auth))
		// Handle valid JWT tokens.
		r.Use(a.authenticator)
		// Refresh the token
		log.Infow("new route", "method", "POST", "path", authRefresTokenEndpoint)
		r.Post(authRefresTokenEndpoint, a.refreshTokenHandler)
		// Get the address
		log.Infow("new route", "method", "GET", "path", currentUserAddressEndpoint)
		r.Get(currentUserAddressEndpoint, a.addressHandler)
		// Sign a payload
		log.Infow("new route", "method", "POST", "path", signTxEndpoint)
		r.Post(signTxEndpoint, a.signTxHandler)
	})

	// Public routes
	r.Group(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte(".")); err != nil {
				log.Warnw("failed to write ping response", "error", err)
			}
		})
		// Register new users
		log.Infow("new route", "method", "POST", "path", usersEndpoint)
		r.Post(usersEndpoint, a.registerHandler)
		// Login
		log.Infow("new route", "method", "POST", "path", authLoginEndpoint)
		r.Post(authLoginEndpoint, a.authLoginHandler)
	})
	return r
}
