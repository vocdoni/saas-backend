// Package api provides the HTTP API for the Vocdoni SaaS Backend
//
//	@title						Vocdoni SaaS API
//	@version					1.0
//	@description				API for Vocdoni SaaS Backend
//	@termsOfService				http://swagger.io/terms/
//
//	@contact.name				API Support
//	@contact.url				https://vocdoni.io
//	@contact.email				info@vocdoni.io
//
//	@license.name				Apache 2.0
//	@license.url				http://www.apache.org/licenses/LICENSE-2.0.html
//
//	@host						localhost:8080
//	@BasePath					/
//	@schemes					http https
//
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Type "Bearer" followed by a space and the JWT token.
//
//	@tag.name					auth
//	@tag.description			Authentication operations
//
//	@tag.name					users
//	@tag.description			User management operations
//
//	@tag.name					organizations
//	@tag.description			Organization management operations
//
//	@tag.name					plans
//	@tag.description			Subscription plans operations
//
//	@tag.name					census
//	@tag.description			Census management operations
//
//	@tag.name					process
//	@tag.description			Voting process operations
//
//	@tag.name					storage
//	@tag.description			Object storage operations
//
//	@tag.name					transactions
//	@tag.description			Transaction signing operations
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
	"github.com/vocdoni/saas-backend/csp"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/notifications"
	"github.com/vocdoni/saas-backend/objectstorage"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

const (
	jwtExpiration = 360 * time.Hour // 15 days
	passwordSalt  = "vocdoni365"    // salt for password hashing
)

type Config struct {
	Host        string
	Port        int
	Secret      string
	Chain       string
	DB          *db.MongoStorage
	Client      *apiclient.HTTPclient
	Account     *account.Account
	MailService notifications.NotificationService
	SMSService  notifications.NotificationService
	WebAppURL   string
	ServerURL   string
	// FullTransparentMode if true allows signing all transactions and does not
	// modify any of them.
	FullTransparentMode bool
	// Subscriptions permissions manager
	Subscriptions *subscriptions.Subscriptions
	// Object storage
	ObjectStorage *objectstorage.Client
	CSP           *csp.CSP
	// OAuth service URL
	OAuthServiceURL string
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
	sms             notifications.NotificationService
	secret          string
	webAppURL       string
	serverURL       string
	transparentMode bool
	subscriptions   *subscriptions.Subscriptions
	objectStorage   *objectstorage.Client
	csp             *csp.CSP
	oauthServiceURL string
	stripeHandlers  *StripeHandlers
}

// New creates a new API HTTP server. It does not start the server. Use Start() for that.
func New(conf *Config) *API {
	if conf == nil {
		return nil
	}
	// Set the ServerURL for the ObjectStorageClient
	if conf.ObjectStorage != nil {
		conf.ObjectStorage.ServerURL = conf.ServerURL
	}

	return &API{
		db:              conf.DB,
		auth:            jwtauth.New("HS256", []byte(conf.Secret), nil),
		host:            conf.Host,
		port:            conf.Port,
		client:          conf.Client,
		account:         conf.Account,
		mail:            conf.MailService,
		sms:             conf.SMSService,
		secret:          conf.Secret,
		webAppURL:       conf.WebAppURL,
		serverURL:       conf.ServerURL,
		transparentMode: conf.FullTransparentMode,
		subscriptions:   conf.Subscriptions,
		objectStorage:   conf.ObjectStorage,
		csp:             conf.CSP,
		oauthServiceURL: conf.OAuthServiceURL,
	}
}

// Start starts the API HTTP server (non blocking).
func (a *API) Start() {
	go func() {
		if err := http.ListenAndServe(fmt.Sprintf("%s:%d", a.host, a.port), a.initRouter()); err != nil {
			log.Fatalf("failed to start the API server: %v", err) //revive:disable:deep-exit
		}
	}()
}

// router creates the router with all the routes and middleware.
//
//revive:disable:function-length
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
	// set lang param in context
	r.Use(a.setLang)

	a.csp.PasswordSalt = passwordSalt
	cspHandlers := handlers.New(a.csp, a.db)

	// Initialize Stripe service
	if err := a.InitializeStripeService(); err != nil {
		log.Errorf("failed to initialize Stripe service: %v", err)
		// Don't fail completely, but log the error
	}

	handle := func(r chi.Router, method, pattern string, h http.HandlerFunc) {
		log.Infow("new route", "method", method, "path", pattern)
		switch method {
		case http.MethodGet:
			r.Get(pattern, h)
		case http.MethodPut:
			r.Put(pattern, h)
		case http.MethodPost:
			r.Post(pattern, h)
		case http.MethodDelete:
			r.Delete(pattern, h)
		default:
			log.Errorf("unsupported method %s in api initRouter", method)
		}
	}

	// protected routes
	r.Group(func(r chi.Router) {
		// seek, verify and validate JWT tokens
		r.Use(jwtauth.Verifier(a.auth))
		// handle valid JWT tokens
		r.Use(a.authenticator)

		handle(r, http.MethodPost, authRefresTokenEndpoint, a.refreshTokenHandler)
		handle(r, http.MethodGet, authAddressesEndpoint, a.organizationAddressesHandler)
		handle(r, http.MethodGet, usersMeEndpoint, a.userInfoHandler)
		handle(r, http.MethodPut, usersMeEndpoint, a.updateUserInfoHandler)
		handle(r, http.MethodPut, usersPasswordEndpoint, a.updateUserPasswordHandler)
		handle(r, http.MethodPost, signTxEndpoint, a.signTxHandler)
		handle(r, http.MethodPost, signMessageEndpoint, a.signMessageHandler)
		handle(r, http.MethodPost, organizationsEndpoint, a.createOrganizationHandler)
		handle(r, http.MethodPut, organizationEndpoint, a.updateOrganizationHandler)
		handle(r, http.MethodGet, organizationUsersEndpoint, a.organizationUsersHandler)
		handle(r, http.MethodGet, organizationSubscriptionEndpoint, a.organizationSubscriptionHandler)
		handle(r, http.MethodPost, organizationAddUserEndpoint, a.inviteOrganizationUserHandler)
		handle(r, http.MethodPut, organizationUpdateUserEndpoint, a.updateOrganizationUserHandler)
		handle(r, http.MethodDelete, organizationDeleteUserEndpoint, a.removeOrganizationUserHandler)
		handle(r, http.MethodGet, organizationCensusesEndpoint, a.organizationCensusesHandler)
		handle(r, http.MethodGet, organizationListProcessDraftsEndpoint, a.organizationListProcessDraftsHandler)
		handle(r, http.MethodGet, organizationPendingUsersEndpoint, a.pendingOrganizationUsersHandler)
		handle(r, http.MethodPut, organizationHandlePendingInvitationEndpoint, a.updatePendingUserInvitationHandler)
		handle(r, http.MethodDelete, organizationHandlePendingInvitationEndpoint, a.deletePendingUserInvitationHandler)
		handle(r, http.MethodGet, organizationMembersEndpoint, a.organizationMembersHandler)
		handle(r, http.MethodPost, organizationAddMembersEndpoint, a.addOrganizationMembersHandler)
		handle(r, http.MethodPut, organizationUpsertMemberEndpoint, a.upsertOrganizationMemberHandler)
		handle(r, http.MethodGet, organizationAddMembersJobStatusEndpoint, a.addOrganizationMembersJobStatusHandler)
		handle(r, http.MethodDelete, organizationDeleteMembersEndpoint, a.deleteOrganizationMembersHandler)
		handle(r, http.MethodPost, organizationMetaEndpoint, a.addOrganizationMetaHandler)
		handle(r, http.MethodPut, organizationMetaEndpoint, a.updateOrganizationMetaHandler)
		handle(r, http.MethodGet, organizationMetaEndpoint, a.organizationMetaHandler)
		handle(r, http.MethodDelete, organizationMetaEndpoint, a.deleteOrganizationMetaHandler)
		handle(r, http.MethodPost, organizationCreateTicketEndpoint, a.organizationCreateTicket)
		handle(r, http.MethodPost, organizationGroupsEndpoint, a.createOrganizationMemberGroupHandler)
		handle(r, http.MethodGet, organizationGroupsEndpoint, a.organizationMemberGroupsHandler)
		handle(r, http.MethodGet, organizationGroupEndpoint, a.organizationMemberGroupHandler)
		handle(r, http.MethodGet, organizationGroupMembersEndpoint, a.listOrganizationMemberGroupsHandler)
		handle(r, http.MethodPut, organizationGroupEndpoint, a.updateOrganizationMemberGroupHandler)
		handle(r, http.MethodDelete, organizationGroupEndpoint, a.deleteOrganizationMemberGroupHandler)
		handle(r, http.MethodPost, organizationGroupValidateEndpoint, a.organizationMemberGroupValidateHandler)
		handle(r, http.MethodGet, organizationJobsEndpoint, a.organizationJobsHandler)
		handle(r, http.MethodPost, subscriptionsCheckout, a.stripeHandlers.CreateSubscriptionCheckout)
		handle(r, http.MethodGet, subscriptionsCheckoutSession, a.stripeHandlers.GetCheckoutSession)
		handle(r, http.MethodGet, subscriptionsPortal, func(w http.ResponseWriter, r *http.Request) {
			a.stripeHandlers.CreateSubscriptionPortalSession(w, r, a)
		})
		handle(r, http.MethodPost, objectStorageUploadTypedEndpoint, a.objectStorage.UploadImageWithFormHandler)
		handle(r, http.MethodPost, censusEndpoint, a.createCensusHandler)
		handle(r, http.MethodPost, censusIDEndpoint, a.addCensusParticipantsHandler)
		handle(r, http.MethodGet, censusAddParticipantsJobStatusEndpoint, a.censusAddParticipantsJobStatusHandler)
		handle(r, http.MethodPost, censusPublishEndpoint, a.publishCensusHandler)
		handle(r, http.MethodPost, censusGroupPublishEndpoint, a.publishCensusGroupHandler)
		handle(r, http.MethodGet, censusParticipantsEndpoint, a.censusParticipantsHandler)
		handle(r, http.MethodPost, processCreateEndpoint, a.createProcessHandler)
		handle(r, http.MethodPut, processEndpoint, a.updateProcessHandler)
		handle(r, http.MethodDelete, processEndpoint, a.deleteProcessHandler)
		handle(r, http.MethodPost, processBundleEndpoint, a.createProcessBundleHandler)
		handle(r, http.MethodPut, processBundleUpdateEndpoint, a.updateProcessBundleHandler)
	})

	// Public routes
	r.Group(func(r chi.Router) {
		handle(r, http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte(".")); err != nil {
				log.Warnw("failed to write ping response", "error", err)
			}
		})

		handle(r, http.MethodPost, authLoginEndpoint, a.authLoginHandler)
		handle(r, http.MethodPost, oauthLoginEndpoint, a.oauthLoginHandler)
		handle(r, http.MethodPost, usersEndpoint, a.registerHandler)
		handle(r, http.MethodPost, verifyUserEndpoint, a.verifyUserAccountHandler)
		handle(r, http.MethodGet, verifyUserCodeEndpoint, a.userVerificationCodeInfoHandler)
		handle(r, http.MethodPost, verifyUserCodeEndpoint, a.resendUserVerificationCodeHandler)
		handle(r, http.MethodPost, usersRecoveryPasswordEndpoint, a.recoverUserPasswordHandler)
		handle(r, http.MethodPost, usersResetPasswordEndpoint, a.resetUserPasswordHandler)
		handle(r, http.MethodGet, organizationEndpoint, a.organizationInfoHandler)
		handle(r, http.MethodPost, organizationAcceptUserEndpoint, a.acceptOrganizationUserInvitationHandler)
		handle(r, http.MethodGet, organizationRolesEndpoint, a.organizationRolesHandler)
		handle(r, http.MethodGet, organizationTypesEndpoint, a.organizationsTypesHandler)
		handle(r, http.MethodGet, plansEndpoint, a.plansHandler)
		handle(r, http.MethodGet, planInfoEndpoint, a.planInfoHandler)
		handle(r, http.MethodPost, subscriptionsWebhook, a.stripeHandlers.HandleWebhook)
		handle(r, http.MethodGet, objectStorageDownloadTypedEndpoint, a.objectStorage.DownloadImageInlineHandler)
		handle(r, http.MethodGet, censusIDEndpoint, a.censusInfoHandler)
		handle(r, http.MethodGet, processEndpoint, a.processInfoHandler)
		handle(r, http.MethodPost, processSignInfoEndpoint, cspHandlers.ConsumedAddressHandler)
		handle(r, http.MethodGet, processBundleInfoEndpoint, a.processBundleInfoHandler)
		handle(r, http.MethodPost, processBundleAuthEndpoint, cspHandlers.BundleAuthHandler)
		handle(r, http.MethodPost, processBundleSignEndpoint, cspHandlers.BundleSignHandler)
		handle(r, http.MethodGet, processBundleMemberEndpoint, a.processBundleParticipantInfoHandler)
	})
	a.router = r
	return r
}
