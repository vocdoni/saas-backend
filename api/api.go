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
	"github.com/vocdoni/saas-backend/objectstorage"
	"github.com/vocdoni/saas-backend/stripe"
	"github.com/vocdoni/saas-backend/subscriptions"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/dvote/log"
)

const (
	jwtExpiration = 360 * time.Hour // 15 days
	passwordSalt  = "vocdoni365"    // salt for password hashing
)

type APIConfig struct {
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
	// Stripe secrets
	StripeClient *stripe.StripeClient
	// Subscriptions permissions manager
	Subscriptions *subscriptions.Subscriptions
	// Object storage
	ObjectStorage *objectstorage.ObjectStorageClient
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
	stripe          *stripe.StripeClient
	subscriptions   *subscriptions.Subscriptions
	objectStorage   *objectstorage.ObjectStorageClient
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
		sms:             conf.SMSService,
		secret:          conf.Secret,
		webAppURL:       conf.WebAppURL,
		serverURL:       conf.ServerURL,
		transparentMode: conf.FullTransparentMode,
		stripe:          conf.StripeClient,
		subscriptions:   conf.Subscriptions,
		objectStorage:   conf.ObjectStorage,
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
		// get organization members
		log.Infow("new route", "method", "GET", "path", organizationMembersEndpoint)
		r.Get(organizationMembersEndpoint, a.organizationMembersHandler)
		// get organization subscription
		log.Infow("new route", "method", "GET", "path", organizationSubscriptionEndpoint)
		r.Get(organizationSubscriptionEndpoint, a.getOrganizationSubscriptionHandler)
		// invite a new admin member to the organization
		log.Infow("new route", "method", "POST", "path", organizationAddMemberEndpoint)
		r.Post(organizationAddMemberEndpoint, a.inviteOrganizationMemberHandler)
		// get organization censuses
		log.Infow("new route", "method", "GET", "path", organizationCensusesEndpoint)
		r.Get(organizationCensusesEndpoint, a.organizationCensusesHandler)
		// get organization participants
		log.Infow("new route", "method", "GET", "path", organizationParticipantsEndpoint)
		r.Get(organizationParticipantsEndpoint, a.organizationParticipantsHandler)
		// pending organization invitations
		log.Infow("new route", "method", "GET", "path", organizationPendingMembersEndpoint)
		r.Get(organizationPendingMembersEndpoint, a.pendingOrganizationMembersHandler)
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
		r.Post(objectStorageUploadTypedEndpoint, a.uploadImageWithFormHandler)
		// CENSUS ROUTES
		// create census
		log.Infow("new route", "method", "POST", "path", censusEndpoint)
		r.Post(censusEndpoint, a.createCensusHandler)
		// add census participants
		log.Infow("new route", "method", "POST", "path", censusIDEndpoint)
		r.Post(censusIDEndpoint, a.addParticipantsHandler)
		// publish census
		log.Infow("new route", "method", "POST", "path", censusPublishEndpoint)
		r.Post(censusPublishEndpoint, a.publishCensusHandler)
		// census participants
		log.Infow("new route", "method", "GET", "path", censusParticipantsEndpoint)
		r.Get(censusParticipantsEndpoint, a.censusParticipantsHandler)
		// PROCESS ROUTES
		log.Infow("new route", "method", "POST", "path", processEndpoint)
		r.Post(processEndpoint, a.createProcessHandler)
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
		log.Infow("new route", "method", "POST", "path", organizationAcceptMemberEndpoint)
		r.Post(organizationAcceptMemberEndpoint, a.acceptOrganizationMemberInvitationHandler)
		// get organization roles
		log.Infow("new route", "method", "GET", "path", organizationRolesEndpoint)
		r.Get(organizationRolesEndpoint, a.organizationsMembersRolesHandler)
		// get organization types
		log.Infow("new route", "method", "GET", "path", organizationTypesEndpoint)
		r.Get(organizationTypesEndpoint, a.organizationsTypesHandler)
		// get subscriptions
		log.Infow("new route", "method", "GET", "path", plansEndpoint)
		r.Get(plansEndpoint, a.getPlansHandler)
		// get subscription info
		log.Infow("new route", "method", "GET", "path", planInfoEndpoint)
		r.Get(planInfoEndpoint, a.planInfoHandler)
		// handle stripe webhook
		log.Infow("new route", "method", "POST", "path", subscriptionsWebhook)
		r.Post(subscriptionsWebhook, a.handleWebhook)
		// upload an image to the object storage
		log.Infow("new route", "method", "GET", "path", objectStorageDownloadTypedEndpoint)
		r.Get(objectStorageDownloadTypedEndpoint, a.downloadImageInlineHandler)
		// get census info
		log.Infow("new route", "method", "GET", "path", censusIDEndpoint)
		r.Get(censusIDEndpoint, a.censusInfoHandler)
		// PROCESS HANDLERS
		// process info handler
		log.Infow("new route", "method", "GET", "path", processEndpoint)
		r.Get(processEndpoint, a.processInfoHandler)
		// process auth handler
		log.Infow("new route", "method", "POST", "path", processAuthEndpoint)
		r.Post(processAuthEndpoint, a.processAuthHandler)
		// process auth routes (currently not implemented)
		// process auth step 0
		// log.Infow("new route", "method", "POST", "path", processAuthInitEndpoint)
		// r.Post(processAuthInitEndpoint, a.initiateAuthHandler)
		// process auth step 1
		// log.Infow("new route", "method", "POST", "path", processAuthVerifyEndpoint)
		// r.Post(processAuthVerifyEndpoint, a.verifyAuthCodeHandler)
		// get process proof
		// log.Infow("new route", "method", "POST", "path", processProofEndpoint)
		// r.Post(processProofEndpoint, a.generateProofHandler)
	})
	a.router = r
	return r
}
