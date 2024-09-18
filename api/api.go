package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/jwtauth/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

const (
	jwtExpiration = 360 * time.Hour // 15 days
	passwordSalt  = "vocdoni365"    // salt for password hashing
)

type APIConfig struct {
	Host          string
	Port          int
	Secret        string
	Chain         string
	DB            *db.MongoStorage
	Client        *apiclient.HTTPclient
	Account       *account.Account
	MailTemplates map[notifications.MailTemplate]string
	MailService   notifications.NotificationService
	// FullTransparentMode if true allows signing all transactions and does not
	// modify any of them.
	FullTransparentMode bool
}

// API type represents the API HTTP server with JWT authentication capabilities.
type API struct {
	db              *db.MongoStorage
	auth            *jwtauth.JWTAuth
	host            string
	port            int
	router          *chi.Mux
	client          *apiclient.HTTPclient
	account         *account.Account
	mail            notifications.NotificationService
	mailTemplates   map[notifications.MailTemplate]string
	secret          string
	transparentMode bool
}

// New creates a new API HTTP server. It does not start the server. Use Start() for that.
func New(conf *APIConfig) *API {
	if conf == nil {
		return nil
	}
	return &API{
		db:              conf.DB,
		auth:            jwtauth.New("HS256", []byte(conf.Secret), nil),
		host:            conf.Host,
		port:            conf.Port,
		client:          conf.Client,
		account:         conf.Account,
		mail:            conf.MailService,
		mailTemplates:   conf.MailTemplates,
		secret:          conf.Secret,
		transparentMode: conf.FullTransparentMode,
	}
}

// Start starts the API HTTP server (non blocking).
func (a *API) Start() {
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf("%s:%d", a.host, a.port), a.initRouter()); err != nil {
			log.Fatalf("failed to start the API server: %v", err)
		}
	}()
}

// router creates the router with all the routes and middleware.
func (a *API) initRouter() http.Handler {
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

	// protected routes
	r.Group(func(r chi.Router) {
		// seek, verify and validate JWT tokens
		r.Use(jwtauth.Verifier(a.auth))
		// handle valid JWT tokens
		r.Use(a.authenticator)
		// refresh the token
		log.Infow("new route", "method", "POST", "path", authRefresTokenEndpoint)
		r.Post(authRefresTokenEndpoint, a.refreshTokenHandler)
		// writable organization addresses
		log.Infow("new route", "method", "GET", "path", authAddressesEndpoint)
		r.Get(authAddressesEndpoint, a.writableOrganizationAddressesHandler)
		// get user information
		log.Infow("new route", "method", "GET", "path", usersMeEndpoint)
		r.Get(usersMeEndpoint, a.userInfoHandler)
		// update user information
		log.Infow("new route", "method", "PUT", "path", usersMeEndpoint)
		r.Put(usersMeEndpoint, a.updateUserInfoHandler)
		// update user password
		log.Infow("new route", "method", "PUT", "path", usersPasswordEndpoint)
		r.Put(usersPasswordEndpoint, a.updateUserPasswordHandler)
		// sign a payload
		log.Infow("new route", "method", "POST", "path", signTxEndpoint)
		r.Post(signTxEndpoint, a.signTxHandler)
		// sign a message
		log.Infow("new route", "method", "POST", "path", signMessageEndpoint)
		r.Post(signMessageEndpoint, a.signMessageHandler)
		// create an organization
		log.Infow("new route", "method", "POST", "path", organizationsEndpoint)
		r.Post(organizationsEndpoint, a.createOrganizationHandler)
		// create a route for those endpoints that include the organization
		// address to get the organization data from the database
		// update the organization
		log.Infow("new route", "method", "PUT", "path", organizationEndpoint)
		r.Put(organizationEndpoint, a.updateOrganizationHandler)
	})

	// Public routes
	r.Group(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
			if _, err := w.Write([]byte(".")); err != nil {
				log.Warnw("failed to write ping response", "error", err)
			}
		})
		// login
		log.Infow("new route", "method", "POST", "path", authLoginEndpoint)
		r.Post(authLoginEndpoint, a.authLoginHandler)
		// register user
		log.Infow("new route", "method", "POST", "path", usersEndpoint)
		r.Post(usersEndpoint, a.registerHandler)
		// verify user
		log.Infow("new route", "method", "POST", "path", verifyUserEndpoint)
		r.Post(verifyUserEndpoint, a.verifyUserAccountHandler)
		// request user password recovery
		log.Infow("new route", "method", "POST", "path", usersRecoveryPasswordEndpoint)
		r.Post(usersRecoveryPasswordEndpoint, a.recoverUserPasswordHandler)
		// reset user password
		log.Infow("new route", "method", "POST", "path", usersResetPasswordEndpoint)
		r.Post(usersResetPasswordEndpoint, a.resetUserPasswordHandler)
		// get organization information
		log.Infow("new route", "method", "GET", "path", organizationEndpoint)
		r.Get(organizationEndpoint, a.organizationInfoHandler)
		// get organization members
		log.Infow("new route", "method", "GET", "path", organizationMembersEndpoint)
		r.Get(organizationMembersEndpoint, a.organizationMembersHandler)
	})
	a.router = r
	return r
}
