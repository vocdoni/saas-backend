package api

const (
	// ping route
	// GET /ping to check the server status
	pingEndpoint = "/ping"

	// auth routes

	// POST /auth/refresh to refresh the JWT token
	authRefresTokenEndpoint = "/auth/refresh"
	// POST /auth/login to login and get a JWT token
	authLoginEndpoint = "/auth/login"
	// GET /auth/addresses to get the writable organization addresses
	authAddressesEndpoint = "/auth/addresses"

	// user routes

	// POST /users to register a new user
	usersEndpoint = "/users"
	// POST /users/verify to verify the user
	verifyUserEndpoint = "/users/verify"
	// GET /users/verify/code to get the user verification code information
	// POST /users/verify/code to try to resend the user verification code
	verifyUserCodeEndpoint = "/users/verify/code"
	// GET /users/me to get the current user information
	// PUT /users/me to update the current user information
	usersMeEndpoint = "/users/me"
	// PUT /users/me/password to update the current user password
	usersPasswordEndpoint = "/users/password"
	// POST /users/password/recovery to recover the user password
	usersRecoveryPasswordEndpoint = "/users/password/recovery"
	// POST /users/password/reset to reset the user password
	usersResetPasswordEndpoint = "/users/password/reset"

	// signer routes
	// POST /transactions to sign a transaction
	signTxEndpoint = "/transactions"
	// POST /transactions/message to sign a message
	signMessageEndpoint = "/transactions/message"

	// organization routes
	// POST /organizations to create a new organization
	organizationsEndpoint = "/organizations"
	// GET /organizations/{address} to get the organization information
	organizationEndpoint = "/organizations/{address}"
	// GET /organizations/{address}/members to get the organization members
	organizationMembersEndpoint = "/organizations/{address}/members"
	// POST /organizations/{address}/members/invite to add a new member
	organizationAddMemberEndpoint = "/organizations/{address}/members"
	// POST /organizations/{address}/members/invite/accept to accept the invitation
	organizationAcceptMemberEndpoint = "/organizations/{address}/members/accept"
	// GET /organizations/{address}/members/pending to get the pending members
	organizationPendingMembersEndpoint = "/organizations/{address}/members/pending"
	// GET /organizations/roles to get the available organization member roles
	organizationRolesEndpoint = "/organizations/roles"
	// GET /organizations/types to get the available organization types
	organizationTypesEndpoint = "/organizations/types"
	// GET /organizations/{address}/subscription to get the organization subscription
	organizationSubscriptionEndpoint = "/organizations/{address}/subscription"
	// GET /organizations/{address}/censuses to get the organization censuses
	organizationCensusesEndpoint = "/organizations/{address}/censuses"
	// GET /organizations/{address}/participants?limit=50&offset=0 to list the
	// organization participants
	organizationParticipantsEndpoint = "/organizations/{address}/participants"

	// subscription routes
	// GET /subscriptions to get the subscriptions of an organization
	plansEndpoint = "/plans"
	// GET /subscriptions/{planID} to get the plan information
	planInfoEndpoint = "/plans/{planID}"
	// POST /subscriptions/webhook to receive the subscription webhook from stripe
	subscriptionsWebhook = "/subscriptions/webhook"
	// POST /subscriptions/checkout to create a new subscription
	subscriptionsCheckout = "/subscriptions/checkout"
	// GET /subscriptions/checkout/{sessionID} to get the checkout session information
	subscriptionsCheckoutSession = "/subscriptions/checkout/{sessionID}"
	// GET /subscriptions/portal to get the stripe subscription portal URL
	subscriptionsPortal = "/subscriptions/{address}/portal"
	// object storage routes
	// POST /storage/{origin} to upload an image to the object storage
	objectStorageUploadTypedEndpoint = "/storage"
	// GET /storage/{origin}/{filename} to download an image from the object storage
	objectStorageDownloadTypedEndpoint = "/storage/{objectName}"

	// census routes
	// POST /census to create a new census
	censusEndpoint = "/census"
	// GET /census/{id} to get census information
	// POST /census/{id} to add participants to census
	censusIDEndpoint = "/census/{id}"
	// POST /census/{id}/publish to publish a census
	censusPublishEndpoint = "/census/{id}/publish"
	// GET /census/{id}/participants?limit=50&offset=0 to list census
	// participants paginated
	censusParticipantsEndpoint = "/census/{id}/participants"

	// process routes
	// POST /process/{processId} to create a new process
	// GET /process/{processId} to get process information
	processEndpoint = "/process/{processId}"
	// POST /process/{processId}/auth to check if the voter is authorized
	processAuthEndpoint = "/process/{processId}/auth"

	// // census auth routes (currently not implemented)
	// // POST /process/{processId}/auth/0 to initiate auth
	// processAuthInitEndpoint = "/process/{processId}/auth/0"
	// // POST /process/{processId}/auth/1 to verify auth code
	// processAuthVerifyEndpoint = "/process/{processId}/auth/1"
	// // POST /process/{processId}/proof to generate proof
	// processProofEndpoint = "/process/{processId}/proof"
)
