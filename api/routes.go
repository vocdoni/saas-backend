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
	// POST /oauth/login to login verifying OAuth parameters and get a JWT token
	oauthLoginEndpoint = "/oauth/login"
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
	// GET /organizations/{address}/users to get the organization users
	organizationUsersEndpoint = "/organizations/{address}/users"
	// POST /organizations/{address}/users/invite to add a new user
	organizationAddUserEndpoint = "/organizations/{address}/users"
	// POST /organizations/{address}/users/invite/accept to accept the invitation
	organizationAcceptUserEndpoint = "/organizations/{address}/users/accept"
	// GET /organizations/{address}/users/pending to get the pending users
	organizationPendingUsersEndpoint = "/organizations/{address}/users/pending"
	// DELETE /organizations/{address}/users/pending/{userid} to delete the a pending invitation
	organizationHandlePendingInvitationEndpoint = "/organizations/{address}/users/pending/{invitationID}"
	// PUT /organizations/{address}/users/{userid} to update the role of an  organization user
	organizationUpdateUserEndpoint = "/organizations/{address}/users/{userid}"
	// DELETE /organizations/{address}/users/{userid} to remove user from  an organization
	organizationDeleteUserEndpoint = "/organizations/{address}/users/{userid}"
	// GET /organizations/roles to get the available organization user roles
	organizationRolesEndpoint = "/organizations/roles"
	// GET /organizations/types to get the available organization types
	organizationTypesEndpoint = "/organizations/types"
	// GET /organizations/{address}/subscription to get the organization subscription
	organizationSubscriptionEndpoint = "/organizations/{address}/subscription"
	// GET /organizations/{address}/censuses to get the organization censuses
	organizationCensusesEndpoint = "/organizations/{address}/censuses"
	// GET /organizations/{address}/members to get the organization members
	organizationMembersEndpoint = "/organizations/{address}/members"
	// POST /organizations/{address}/members to add new members
	organizationAddMembersEndpoint = "/organizations/{address}/members"
	// GET /organizations/{address}/members/check/{jobid} to check the status of the add members job
	organizationAddMembersJobStatusEndpoint = "/organizations/{address}/members/job/{jobid}"
	// DELETE /organizations/{address}/members to delete members
	organizationDeleteMembersEndpoint = "/organizations/{address}/members"
	// POST/PUT/GET/DELETE /organizations/{address}/meta to add/set/get/delete the organization metadata
	organizationMetaEndpoint = "/organizations/{address}/meta"
	// POST /organizations/{address}/ticket to create a new ticket to our support system
	organizationCreateTicketEndpoint = "/organizations/{address}/ticket"
	// POST/GET /organizations/{address}/groups to create a new organization member group or get the
	// list of groups of an organization
	organizationGroupsEndpoint = "/organizations/{address}/groups"
	// PUT/DELETE /organizations/{address}/groups/{groupID} to update or delete an organization member group
	organizationGroupEndpoint = "/organizations/{address}/groups/{groupID}"
	// GET /organizations/{address}/groups/{groupID}/members to get the members of an organization member group
	organizationGroupMembersEndpoint = "/organizations/{address}/groups/{groupID}/members"

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
	// POST /census/{id} to add members to census
	censusIDEndpoint = "/census/{id}"
	// GET /census/check/{jobid} to check the status of the add members job
	censusAddMembersJobStatusEndpoint = "/census/job/{jobid}"
	// POST /census/{id}/publish to publish a census
	censusPublishEndpoint = "/census/{id}/publish"

	// process routes
	// POST /process/{processId} to create a new process
	// GET /process/{processId} to get process information
	processEndpoint = "/process/{processId}"
	// POST /process/{processId}/auth to check if the voter is authorized
	// processAuthEndpoint = "/process/{processId}/auth"
	// POST /process/{processId}/sign-info to get the sign info for the process
	processSignInfoEndpoint = "/process/{processId}/sign-info"

	// two-factor process bundle routes
	// POST /process/bundle to create a new bundle
	processBundleEndpoint = "/process/bundle"
	// PUT /process/bundle/{bundleId} to add new processes to the bundle
	processBundleUpdateEndpoint = "/process/bundle/{bundleId}"
	// GET /process/bundle/{bundleId} to get the bundle information
	processBundleInfoEndpoint = "/process/bundle/{bundleId}"
	// POST /process/bundle/{bundleId}/auth/{step} to check if the voter is authorized
	processBundleAuthEndpoint = "/process/bundle/{bundleId}/auth/{step}"
	// POST /process/bundle/{bundleId}/sign to sign with two-factor authentication
	processBundleSignEndpoint = "/process/bundle/{bundleId}/sign"
	// GET /process/bundle/{bundleId}/{memberId} to get the process information
	processBundleMemberEndpoint = "/process/bundle/{bundleId}/{memberId}"

	// // census auth routes (currently not implemented)
	// // POST /process/{processId}/auth/0 to initiate auth
	// processAuthInitEndpoint = "/process/{processId}/auth/0"
	// // POST /process/{processId}/auth/1 to verify auth code
	// processAuthVerifyEndpoint = "/process/{processId}/auth/1"
	// // POST /process/{processId}/proof to generate proof
	// processProofEndpoint = "/process/{processId}/proof"
)
