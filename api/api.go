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
//	@description				Type "Bearer" followed by a space and either a JWT (user session) or a
//	@description				scoped API key (prefixed "vsk_"). API keys are accepted only on endpoints
//	@description				that opt into key auth, and only when the key carries the required scope.
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
//	@tag.name					processes
//	@tag.description			Multi-question voting process operations (create, publish, results, voter CSP flow)
//
//	@tag.name					process
//	@tag.description			Legacy voting process & bundle operations (deprecated — use processes)
//
//	@tag.name					vote
//	@tag.description			Vote relay operations
//
//	@tag.name					jobs
//	@tag.description			Async job status (member import, census publish, vote relay)
//
//	@tag.name					integrator
//	@tag.description			Integrator operations: managed organizations & API keys
//
//	@tag.name					csp
//	@tag.description			Legacy CSP voter operations (deprecated — use processes)
//
//	@tag.name					storage
//	@tag.description			Object storage operations
//
//	@tag.name					transactions
//	@tag.description			Transaction signing operations (deprecated)
//
//	@tag.name					health
//	@tag.description			Service health & info
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
	// OTPExpiry overrides the validity window for all one-time codes (account
	// verification, password reset). Zero uses notifications.DefaultOTPExpiry.
	OTPExpiry time.Duration
	// OTPCooldown overrides the anti-spam rate limit between notification
	// requests for the same account. Zero uses notifications.DefaultOTPCooldown.
	OTPCooldown time.Duration
	// NotificationsSyncDelivery makes sendMail block until the notification has
	// actually been delivered (or given up) instead of returning as soon as it
	// is enqueued. It exists to make tests deterministic — a handler that sends
	// mail then returns guarantees the mail is in the (fake) inbox by the time
	// the HTTP response is observed, restoring the happens-before that the async
	// queue otherwise breaks. Leave false in production.
	NotificationsSyncDelivery bool
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
	ctx             context.Context
	notifyQueue     *notifications.Queue
	secret          string
	webAppURL       string
	serverURL       string
	transparentMode bool
	subscriptions   *subscriptions.Subscriptions
	objectStorage   *objectstorage.Client
	csp             *csp.CSP
	oauthServiceURL string
	stripeHandlers  *StripeHandlers
	txQueue         chan txTask
	orgTxLocks      *orgTxMutex
	otpExpiry       time.Duration
	otpCooldown     time.Duration
	notifySync      bool
}

// New creates a new API HTTP server. It does not start the server. Use Start() for that.
func New(ctx context.Context, conf *Config) *API {
	if conf == nil {
		return nil
	}
	// normalize once so every storage reference built from it — and the local-reference
	// match on read — is consistent and never yields a "//storage/" prefix.
	conf.ServerURL = strings.TrimRight(conf.ServerURL, "/")
	// Set the ServerURL for the ObjectStorageClient
	if conf.ObjectStorage != nil {
		conf.ObjectStorage.ServerURL = conf.ServerURL
	}

	otpExpiry := conf.OTPExpiry
	if otpExpiry <= 0 {
		otpExpiry = notifications.DefaultOTPExpiry
	}
	otpCooldown := conf.OTPCooldown
	if otpCooldown <= 0 {
		otpCooldown = notifications.DefaultOTPCooldown
	}

	var notifyQueue *notifications.Queue
	if conf.MailService != nil || conf.SMSService != nil {
		notifyQueue = notifications.NewQueue(ctx, notifications.QueueConfig{
			MailService: conf.MailService,
			SMSService:  conf.SMSService,
		})
	}

	a := &API{
		ctx:             ctx,
		db:              conf.DB,
		auth:            jwtauth.New("HS256", []byte(conf.Secret), nil),
		host:            conf.Host,
		port:            conf.Port,
		client:          conf.Client,
		account:         conf.Account,
		mail:            conf.MailService,
		sms:             conf.SMSService,
		notifyQueue:     notifyQueue,
		secret:          conf.Secret,
		webAppURL:       conf.WebAppURL,
		serverURL:       conf.ServerURL,
		transparentMode: conf.FullTransparentMode,
		subscriptions:   conf.Subscriptions,
		objectStorage:   conf.ObjectStorage,
		csp:             conf.CSP,
		oauthServiceURL: conf.OAuthServiceURL,
		orgTxLocks:      newOrgTxMutex(),
		otpExpiry:       otpExpiry,
		otpCooldown:     otpCooldown,
		notifySync:      conf.NotificationsSyncDelivery,
	}
	a.startTxQueue()
	// clear any publishing markers stranded by a previous crash/restart so those processes are
	// publishable again (see reconcileStalePublishing).
	a.reconcileStalePublishing()
	return a
}

// Start starts the API HTTP server and the notification queue (non blocking).
func (a *API) Start() {
	if a.notifyQueue != nil {
		a.notifyQueue.Start()
		// Drain the Done channel so workers never block on a full channel.
		// The API does not need per-item outcome tracking; CSP has its own
		// forwardResults() goroutine that drains the inner queue it owns.
		go func() {
			for {
				select {
				case <-a.ctx.Done():
					return
				case item, ok := <-a.notifyQueue.Done:
					if !ok {
						return
					}
					if item != nil && !item.Success {
						log.Warnw("notification delivery failed", "label", item.Label, "retries", item.Retries)
					}
				}
			}
		}()
	}
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
		handle(r, http.MethodPost, oauthLinkEndpoint, a.oauthLinkHandler)
		handle(r, http.MethodDelete, oauthUnlinkEndpoint, a.oauthUnlinkHandler)
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
		handle(r, http.MethodGet, organizationBundlesEndpoint, a.organizationBundlesHandler)
		handle(r, http.MethodPost, managedOrganizationsEndpoint, a.createManagedOrganizationHandler)
		handle(r, http.MethodGet, managedOrganizationsEndpoint, a.managedOrganizationsHandler)
		handle(r, http.MethodDelete, managedOrganizationEndpoint, a.deleteManagedOrganizationHandler)
		handle(r, http.MethodGet, integratorEndpoint, a.integratorInfoHandler)
		handle(r, http.MethodPost, organizationAPIKeysEndpoint, a.createAPIKeyHandler)
		handle(r, http.MethodGet, organizationAPIKeysEndpoint, a.apiKeysHandler)
		handle(r, http.MethodDelete, organizationAPIKeyEndpoint, a.revokeAPIKeyHandler)
		handle(r, http.MethodPost, subscriptionsCheckout, a.stripeHandlers.CreateSubscriptionCheckout)
		handle(r, http.MethodGet, subscriptionsCheckoutSession, a.stripeHandlers.GetCheckoutSession)
		handle(r, http.MethodGet, subscriptionsPortal, func(w http.ResponseWriter, r *http.Request) {
			a.stripeHandlers.CreateSubscriptionPortalSession(w, r, a)
		})
		handle(r, http.MethodPost, objectStorageUploadTypedEndpoint, a.objectStorage.UploadImageWithFormHandler)
		handle(r, http.MethodPost, censusEndpoint, a.createCensusHandler)
		handle(r, http.MethodPost, censusIDEndpoint, a.addCensusParticipantsHandler)
		handle(r, http.MethodPost, censusPublishEndpoint, a.publishCensusHandler)
		handle(r, http.MethodPost, censusGroupPublishEndpoint, a.publishCensusGroupHandler)
		handle(r, http.MethodGet, censusParticipantsEndpoint, a.censusParticipantsHandler)
		handle(r, http.MethodPost, processCreateEndpoint, a.createProcessHandler)
		handle(r, http.MethodPut, processEndpoint, a.updateProcessHandler)
		handle(r, http.MethodDelete, processEndpoint, a.deleteProcessHandler)
		handle(r, http.MethodPost, processPublishEndpoint, a.publishProcessHandler)
		handle(r, http.MethodPut, processStatusEndpoint, a.setProcessStatusHandler)
		handle(r, http.MethodPost, processBundleEndpoint, a.createProcessBundleHandler)
		handle(r, http.MethodPut, processBundleUpdateEndpoint, a.updateProcessBundleHandler)
		handle(r, http.MethodPost, processBundleParticipantsCheckEndpoint, a.checkProcessBundleVotedParticipantsHandler)
		// multi-question voting processes: authoring + protected reads
		handle(r, http.MethodPost, processesCreateEndpoint, a.createVotingProcessHandler)
		handle(r, http.MethodGet, processesCreateEndpoint, a.listVotingProcessesHandler)
		handle(r, http.MethodPut, processesEndpoint, a.updateVotingProcessHandler)
		handle(r, http.MethodGet, processesEndpoint, a.votingProcessInfoHandler)
		handle(r, http.MethodGet, processesValidateEndpoint, a.validateVotingProcessHandler)
		handle(r, http.MethodPost, processesPublishEndpoint, a.publishVotingProcessHandler)
		handle(r, http.MethodPut, processesQuestionsStatusEndpoint, a.setVotingProcessQuestionsStatusHandler)
		handle(r, http.MethodPut, processesQuestionStatusEndpoint, a.setVotingProcessQuestionStatusHandler)
		handle(r, http.MethodDelete, processesEndpoint, a.deleteVotingProcessHandler)
		handle(r, http.MethodGet, processesParticipantsEndpoint, a.votingProcessParticipantsHandler)
	})

	// Public routes
	r.Group(func(r chi.Router) {
		handle(r, http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte(".")); err != nil {
				log.Warnw("failed to write ping response", "error", err)
			}
		})
		handle(r, http.MethodGet, infoEndpoint, a.InfoHandler)

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
		handle(r, http.MethodPost, subscriptionsWebhook, a.stripeHandlers.HandleWebhook)
		handle(r, http.MethodGet, objectStorageDownloadTypedEndpoint, a.objectStorage.DownloadImageInlineHandler)
		handle(r, http.MethodGet, censusIDEndpoint, a.censusInfoHandler)
		handle(r, http.MethodGet, jobStatusEndpoint, a.jobStatusHandler)
		handle(r, http.MethodGet, processEndpoint, a.processInfoHandler)
		handle(r, http.MethodPost, voteEndpoint, a.relayVoteHandler)
		handle(r, http.MethodGet, processResultsEndpoint, a.processResultsHandler)
		handle(r, http.MethodGet, processMetadataEndpoint, a.processMetadataHandler)
		handle(r, http.MethodPost, processSignInfoEndpoint, cspHandlers.ConsumedAddressHandler)
		handle(r, http.MethodGet, processBundleInfoEndpoint, a.processBundleInfoHandler)
		handle(r, http.MethodPost, processBundleWeightEndpoint, cspHandlers.UserWeightHandler)
		handle(r, http.MethodPost, processBundleAuthEndpoint, cspHandlers.BundleAuthHandler)
		handle(r, http.MethodPost, processBundleAuthResendEndpoint, cspHandlers.BundleAuthResendHandler)
		handle(r, http.MethodPost, processBundleSignEndpoint, cspHandlers.BundleSignHandler)
		handle(r, http.MethodPost, processBundleCheckEndpoint, cspHandlers.BundleCheckHandler)
		handle(r, http.MethodGet, processBundleMemberEndpoint, a.processBundleParticipantInfoHandler)
		// multi-question voting processes: public voter reads + CSP
		handle(r, http.MethodGet, processesQuestionEndpoint, a.votingProcessQuestionHandler)
		handle(r, http.MethodGet, processesParticipantEndpoint, a.votingProcessParticipantHandler)
		handle(r, http.MethodGet, processesResultsEndpoint, a.votingProcessResultsHandler)
		handle(r, http.MethodPost, processesCheckEndpoint, cspHandlers.ProcessCheckHandler)
		handle(r, http.MethodPost, processesAuthEndpoint, cspHandlers.ProcessAuthHandler)
		handle(r, http.MethodPost, processesAuthResendEndpoint, cspHandlers.ProcessAuthResendHandler)
		handle(r, http.MethodPost, processesSignEndpoint, cspHandlers.ProcessSignHandler)
		handle(r, http.MethodPost, processesWeightEndpoint, cspHandlers.ProcessWeightHandler)
		handle(r, http.MethodPost, processesSignInfoEndpoint, cspHandlers.ProcessSignInfoHandler)
	})
	a.router = r
	return r
}
