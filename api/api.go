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

	a.csp.PasswordSalt = passwordSalt
	cspHandlers := handlers.New(a.csp, a.db)

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
		// get organization users
		log.Infow("new route", "method", "GET", "path", organizationUsersEndpoint)
		r.Get(organizationUsersEndpoint, a.organizationUsersHandler)
		// get organization subscription
		log.Infow("new route", "method", "GET", "path", organizationSubscriptionEndpoint)
		r.Get(organizationSubscriptionEndpoint, a.organizationSubscriptionHandler)
		// invite a new user to the organization
		log.Infow("new route", "method", "POST", "path", organizationAddUserEndpoint)
		r.Post(organizationAddUserEndpoint, a.inviteOrganizationUserHandler)
		// update an organization's user role
		log.Infow("new route", "method", "PUT", "path", organizationUpdateUserEndpoint)
		r.Put(organizationUpdateUserEndpoint, a.updateOrganizationUserHandler)
		// remove a user from an organization
		log.Infow("new route", "method", "DELETE", "path", organizationDeleteUserEndpoint)
		r.Delete(organizationDeleteUserEndpoint, a.removeOrganizationUserHandler)
		// get organization censuses
		log.Infow("new route", "method", "GET", "path", organizationCensusesEndpoint)
		r.Get(organizationCensusesEndpoint, a.organizationCensusesHandler)
		// pending organization invitations
		log.Infow("new route", "method", "GET", "path", organizationPendingUsersEndpoint)
		r.Get(organizationPendingUsersEndpoint, a.pendingOrganizationUsersHandler)
		// update pending organization invitation
		log.Infow("new route", "method", "PUT", "path", organizationHandlePendingInvitationEndpoint)
		r.Put(organizationHandlePendingInvitationEndpoint, a.updatePendingUserInvitationHandler)
		// delete pending organization invitation
		log.Infow("new route", "method", "DELETE", "path", organizationHandlePendingInvitationEndpoint)
		r.Delete(organizationHandlePendingInvitationEndpoint, a.deletePendingUserInvitationHandler)
		// get organization members
		log.Infow("new route", "method", "GET", "path", organizationMembersEndpoint)
		r.Get(organizationMembersEndpoint, a.organizationMembersHandler)
		// add organization members
		log.Infow("new route", "method", "POST", "path", organizationAddMembersEndpoint)
		r.Post(organizationAddMembersEndpoint, a.addOrganizationMembersHandler)
		// check the status of the add members job
		log.Infow("new route", "method", "GET", "path", organizationAddMembersJobStatusEndpoint)
		r.Get(organizationAddMembersJobStatusEndpoint, a.addOrganizationMembersJobStatusHandler)
		// delete a set of organization members
		log.Infow("new route", "method", "DELETE", "path", organizationDeleteMembersEndpoint)
		r.Delete(organizationDeleteMembersEndpoint, a.deleteOrganizationMembersHandler)
		// add/overwrite organization meta information
		log.Infow("new route", "method", "POST", "path", organizationMetaEndpoint)
		r.Post(organizationMetaEndpoint, a.addOrganizationMetaHandler)
		// update organization meta information
		log.Infow("new route", "method", "PUT", "path", organizationMetaEndpoint)
		r.Put(organizationMetaEndpoint, a.updateOrganizationMetaHandler)
		// get organization meta information
		log.Infow("new route", "method", "GET", "path", organizationMetaEndpoint)
		r.Get(organizationMetaEndpoint, a.organizationMetaHandler)
		// delete organization meta information
		log.Infow("new route", "method", "DELETE", "path", organizationMetaEndpoint)
		r.Delete(organizationMetaEndpoint, a.deleteOrganizationMetaHandler)
		// create a new ticket for the organization
		log.Infow("new route", "method", "POST", "path", organizationCreateTicketEndpoint)
		r.Post(organizationCreateTicketEndpoint, a.organizationCreateTicket)
		// create a new organization member group
		log.Infow("new route", "method", "POST", "path", organizationGroupsEndpoint)
		r.Post(organizationGroupsEndpoint, a.createOrganizationMemberGroupHandler)
		// get organization member groups list
		log.Infow("new route", "method", "GET", "path", organizationGroupsEndpoint)
		r.Get(organizationGroupsEndpoint, a.organizationMemberGroupsHandler)
		// get details of an organization member group
		log.Infow("new route", "method", "GET", "path", organizationGroupEndpoint)
		r.Get(organizationGroupEndpoint, a.organizationMemberGroupHandler)
		// get members of an organization member group
		log.Infow("new route", "method", "GET", "path", organizationGroupMembersEndpoint)
		r.Get(organizationGroupMembersEndpoint, a.listOrganizationMemberGroupsHandler)
		// update an organization member group
		log.Infow("new route", "method", "PUT", "path", organizationGroupEndpoint)
		r.Put(organizationGroupEndpoint, a.updateOrganizationMemberGroupHandler)
		// delete an organization member group
		log.Infow("new route", "method", "DELETE", "path", organizationGroupEndpoint)
		r.Delete(organizationGroupEndpoint, a.deleteOrganizationMemberGroupHandler)
		// validate the member data of an organization member group
		log.Infow("new route", "method", "POST", "path", organizationGroupValidateEndpoint)
		r.Post(organizationGroupValidateEndpoint, a.organizationMemberGroupValidateHandler)

		// handle stripe checkout session
		log.Infow("new route", "method", "POST", "path", subscriptionsCheckout)
		r.Post(subscriptionsCheckout, a.createSubscriptionCheckoutHandler)
		// get stripe checkout session info
		log.Infow("new route", "method", "GET", "path", subscriptionsCheckoutSession)
		r.Get(subscriptionsCheckoutSession, a.checkoutSessionHandler)
		// get stripe subscription portal session info
		log.Infow("new route", "method", "GET", "path", subscriptionsPortal)
		r.Get(subscriptionsPortal, a.createSubscriptionPortalSessionHandler)
		// upload an image to the object storage
		log.Infow("new route", "method", "POST", "path", objectStorageUploadTypedEndpoint)
		r.Post(objectStorageUploadTypedEndpoint, a.objectStorage.UploadImageWithFormHandler)
		// CENSUS ROUTES
		// create census
		log.Infow("new route", "method", "POST", "path", censusEndpoint)
		r.Post(censusEndpoint, a.createCensusHandler)
		// add census participants
		log.Infow("new route", "method", "POST", "path", censusIDEndpoint)
		r.Post(censusIDEndpoint, a.addCensusParticipantsHandler)
		// get census participants job
		log.Infow("new route", "method", "GET", "path", censusAddParticipantsJobStatusEndpoint)
		r.Get(censusAddParticipantsJobStatusEndpoint, a.censusAddParticipantsJobStatusHandler)
		// publish census
		log.Infow("new route", "method", "POST", "path", censusPublishEndpoint)
		r.Post(censusPublishEndpoint, a.publishCensusHandler)
		// publish group census
		log.Infow("new route", "method", "POST", "path", censusGroupPublishEndpoint)
		r.Post(censusGroupPublishEndpoint, a.publishCensusGroupHandler)
		// get census participants
		log.Infow("new route", "method", "GET", "path", censusParticipantsEndpoint)
		r.Get(censusParticipantsEndpoint, a.censusParticipantsHandler)
		// PROCESS ROUTES
		log.Infow("new route", "method", "POST", "path", processEndpoint)
		r.Post(processEndpoint, a.createProcessHandler)
		// PROCESS BUNDLE ROUTES (private)
		log.Infow("new route", "method", "POST", "path", processBundleEndpoint)
		r.Post(processBundleEndpoint, a.createProcessBundleHandler)
		log.Infow("new route", "method", "PUT", "path", processBundleUpdateEndpoint)
		r.Put(processBundleUpdateEndpoint, a.updateProcessBundleHandler)
	})

	// Public routes
	r.Group(func(r chi.Router) {
		r.Get("/ping", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte(".")); err != nil {
				log.Warnw("failed to write ping response", "error", err)
			}
		})
		// login
		log.Infow("new route", "method", "POST", "path", authLoginEndpoint)
		r.Post(authLoginEndpoint, a.authLoginHandler)
		// oauth login
		log.Infow("new route", "method", "POST", "path", oauthLoginEndpoint)
		r.Post(oauthLoginEndpoint, a.oauthLoginHandler)
		// register user
		log.Infow("new route", "method", "POST", "path", usersEndpoint)
		r.Post(usersEndpoint, a.registerHandler)
		// verify user
		log.Infow("new route", "method", "POST", "path", verifyUserEndpoint)
		r.Post(verifyUserEndpoint, a.verifyUserAccountHandler)
		// get user verification code information
		log.Infow("new route", "method", "GET", "path", verifyUserCodeEndpoint)
		r.Get(verifyUserCodeEndpoint, a.userVerificationCodeInfoHandler)
		// resend user verification code
		log.Infow("new route", "method", "POST", "path", verifyUserCodeEndpoint)
		r.Post(verifyUserCodeEndpoint, a.resendUserVerificationCodeHandler)
		// request user password recovery
		log.Infow("new route", "method", "POST", "path", usersRecoveryPasswordEndpoint)
		r.Post(usersRecoveryPasswordEndpoint, a.recoverUserPasswordHandler)
		// reset user password
		log.Infow("new route", "method", "POST", "path", usersResetPasswordEndpoint)
		r.Post(usersResetPasswordEndpoint, a.resetUserPasswordHandler)
		// get organization information
		log.Infow("new route", "method", "GET", "path", organizationEndpoint)
		r.Get(organizationEndpoint, a.organizationInfoHandler)
		// accept organization invitation
		log.Infow("new route", "method", "POST", "path", organizationAcceptUserEndpoint)
		r.Post(organizationAcceptUserEndpoint, a.acceptOrganizationUserInvitationHandler)
		// get organization roles
		log.Infow("new route", "method", "GET", "path", organizationRolesEndpoint)
		r.Get(organizationRolesEndpoint, a.organizationRolesHandler)
		// get organization types
		log.Infow("new route", "method", "GET", "path", organizationTypesEndpoint)
		r.Get(organizationTypesEndpoint, a.organizationsTypesHandler)
		// get subscriptions
		log.Infow("new route", "method", "GET", "path", plansEndpoint)
		r.Get(plansEndpoint, a.plansHandler)
		// get subscription info
		log.Infow("new route", "method", "GET", "path", planInfoEndpoint)
		r.Get(planInfoEndpoint, a.planInfoHandler)
		// handle stripe webhook
		log.Infow("new route", "method", "POST", "path", subscriptionsWebhook)
		r.Post(subscriptionsWebhook, a.handleWebhook)
		// upload an image to the object storage
		log.Infow("new route", "method", "GET", "path", objectStorageDownloadTypedEndpoint)
		r.Get(objectStorageDownloadTypedEndpoint, a.objectStorage.DownloadImageInlineHandler)
		// get census info
		log.Infow("new route", "method", "GET", "path", censusIDEndpoint)
		r.Get(censusIDEndpoint, a.censusInfoHandler)
		// process info handler
		log.Infow("new route", "method", "GET", "path", processEndpoint)
		r.Get(processEndpoint, a.processInfoHandler)
		// process sign info handler
		log.Infow("new route", "method", "POST", "path", processSignInfoEndpoint)
		r.Post(processSignInfoEndpoint, cspHandlers.ConsumedAddressHandler)
		// process bundle info handler
		log.Infow("new route", "method", "GET", "path", processBundleInfoEndpoint)
		r.Get(processBundleInfoEndpoint, a.processBundleInfoHandler)
		// process bundle auth handler
		log.Infow("new route", "method", "POST", "path", processBundleAuthEndpoint)
		// r.Post(processBundleAuthEndpoint, a.processBundleAuthHandler)
		r.Post(processBundleAuthEndpoint, cspHandlers.BundleAuthHandler)
		// process bundle sign handler
		log.Infow("new route", "method", "POST", "path", processBundleSignEndpoint)
		// r.Post(processBundleSignEndpoint, a.processBundleSignHandler)
		r.Post(processBundleSignEndpoint, cspHandlers.BundleSignHandler)
		// process bundle member info handler
		log.Infow("new route", "method", "GET", "path", processBundleMemberEndpoint)
		r.Get(processBundleMemberEndpoint, a.processBundleParticipantInfoHandler)
	})
	a.router = r
	return r
}
