package api

const (
	// ping route
	// GET /ping to check the server status
	pingEndpoint = "/ping"
	// GET /info to get service version, build and chain information
	infoEndpoint = "/info"

	// auth routes

	// POST /auth/refresh to refresh the JWT token
	authRefresTokenEndpoint = "/auth/refresh"
	// POST /auth/login to login and get a JWT token
	authLoginEndpoint = "/auth/login"
	// POST /oauth/login to login verifying OAuth parameters and get a JWT token
	oauthLoginEndpoint = "/oauth/login"
	// POST /auth/oauth to link an OAuth provider to the authenticated account
	oauthLinkEndpoint = "/auth/oauth"
	// DELETE /auth/oauth/{provider} to unlink an OAuth provider from the authenticated account
	oauthUnlinkEndpoint = "/auth/oauth/{provider}"
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

	// async job routes
	// GET /jobs/{jobId} to poll the status/result of an async transaction job (public; the
	// 32-byte job id is the capability — results carry only public on-chain data)
	jobStatusEndpoint = "/jobs/{jobId}"
	// GET /jobs?orgAddress=&page=&limit= to list an organization's jobs (protected, Manager/Admin)
	jobsEndpoint = "/jobs"

	// organization routes
	// POST /organizations to create a new organization
	organizationsEndpoint = "/organizations"
	// GET /organizations/{orgAddress} to get the organization information
	organizationEndpoint = "/organizations/{orgAddress}"
	// GET /organizations/{orgAddress}/users to get the organization users
	organizationUsersEndpoint = "/organizations/{orgAddress}/users"
	// POST /organizations/{orgAddress}/users/invite to add a new user
	organizationAddUserEndpoint = "/organizations/{orgAddress}/users"
	// POST /organizations/{orgAddress}/users/invite/accept to accept the invitation
	organizationAcceptUserEndpoint = "/organizations/{orgAddress}/users/accept"
	// GET /organizations/{orgAddress}/users/pending to get the pending users
	organizationPendingUsersEndpoint = "/organizations/{orgAddress}/users/pending"
	// DELETE /organizations/{orgAddress}/users/pending/{userId} to delete the a pending invitation
	organizationHandlePendingInvitationEndpoint = "/organizations/{orgAddress}/users/pending/{invitationId}"
	// PUT /organizations/{orgAddress}/users/{userId} to update the role of an  organization user
	organizationUpdateUserEndpoint = "/organizations/{orgAddress}/users/{userId}"
	// DELETE /organizations/{orgAddress}/users/{userId} to remove user from  an organization
	organizationDeleteUserEndpoint = "/organizations/{orgAddress}/users/{userId}"
	// GET /organizations/roles to get the available organization user roles
	organizationRolesEndpoint = "/organizations/roles"
	// GET /organizations/types to get the available organization types
	organizationTypesEndpoint = "/organizations/types"
	// GET /organizations/{orgAddress}/subscription to get the organization subscription
	organizationSubscriptionEndpoint = "/organizations/{orgAddress}/subscription"
	// GET /organizations/{orgAddress}/censuses to get the organization censuses
	organizationCensusesEndpoint = "/organizations/{orgAddress}/censuses"
	// GET /organizations/{orgAddress}/processes/drafts to get the organization draft processes
	organizationListProcessDraftsEndpoint = "/organizations/{orgAddress}/processes/drafts"

	// GET /organizations/{orgAddress}/members to get the organization members
	organizationMembersEndpoint = "/organizations/{orgAddress}/members"
	// POST /organizations/{orgAddress}/members to add new members
	organizationAddMembersEndpoint = "/organizations/{orgAddress}/members"
	// PUT /organizations/{orgAddress}/members to create or update an organization member
	organizationUpsertMemberEndpoint = "/organizations/{orgAddress}/members"
	// DELETE /organizations/{orgAddress}/members to delete members
	organizationDeleteMembersEndpoint = "/organizations/{orgAddress}/members"
	// POST/PUT/GET/DELETE /organizations/{orgAddress}/meta to add/set/get/delete the organization metadata
	organizationMetaEndpoint = "/organizations/{orgAddress}/meta"
	// POST /organizations/{orgAddress}/ticket to create a new ticket to our support system
	organizationCreateTicketEndpoint = "/organizations/{orgAddress}/ticket"
	// POST/GET /organizations/{orgAddress}/groups to create a new organization member group or get the
	// list of groups of an organization
	organizationGroupsEndpoint = "/organizations/{orgAddress}/groups"
	// PUT/DELETE /organizations/{orgAddress}/groups/{groupId} to update or delete an organization member group
	organizationGroupEndpoint = "/organizations/{orgAddress}/groups/{groupId}"
	// GET /organizations/{orgAddress}/groups/{groupId}/members to get the members of an organization member group
	organizationGroupMembersEndpoint = "/organizations/{orgAddress}/groups/{groupId}/members"
	// POST /organizations/{orgAddress}/groups/{groupId}/validate to validate the member data of an organization member group
	organizationGroupValidateEndpoint = "/organizations/{orgAddress}/groups/{groupId}/validate"
	// GET /organizations/{orgAddress}/processes to get the organization bundle processes
	organizationBundlesEndpoint = "/organizations/{orgAddress}/processes"
	// GET /integrator to get integrator quota and usage for the caller's own integrator org.
	// Path-less: the integrator org is resolved from the API key (its org) or the user session.
	integratorEndpoint = "/integrator"
	// POST /integrator/organizations to create a managed org; GET to list them, for the caller's
	// own integrator org. Path-less: the integrator is resolved from the API key or the user
	// session, so no organization address is passed in the URL.
	managedOrganizationsEndpoint = "/integrator/organizations"
	// DELETE /integrator/organizations/{orgAddress} to delete a managed org and all its data.
	managedOrganizationEndpoint = "/integrator/organizations/{orgAddress}"
	// POST /integrator/organizations/{orgAddress}/apikeys to create an API key; GET to list them
	integratorOrgAPIKeysEndpoint = "/integrator/organizations/{orgAddress}/apikeys"
	// DELETE /integrator/organizations/{orgAddress}/apikeys/{keyId} to revoke an API key
	integratorOrgAPIKeyEndpoint = "/integrator/organizations/{orgAddress}/apikeys/{keyId}"

	// subscription routes
	// GET /plans to get the public catalog of subscription plans
	plansEndpoint = "/plans"
	// POST /subscriptions/webhook to receive the subscription webhook from stripe
	subscriptionsWebhook = "/subscriptions/webhook"
	// POST /subscriptions/checkout to create a new subscription
	subscriptionsCheckout = "/subscriptions/checkout"
	// GET /subscriptions/checkout/{sessionId} to get the checkout session information
	subscriptionsCheckoutSession = "/subscriptions/checkout/{sessionId}"
	// GET /subscriptions/portal to get the stripe subscription portal URL
	subscriptionsPortal = "/subscriptions/{orgAddress}/portal"
	// object storage routes
	// POST /storage/{origin} to upload an image to the object storage
	objectStorageUploadTypedEndpoint = "/storage"
	// GET /storage/{origin}/{filename} to download an image from the object storage
	objectStorageDownloadTypedEndpoint = "/storage/{objectName}"

	// census routes
	// POST /census to create a new census
	censusEndpoint = "/census"
	// GET /census/{id} to get census information
	// POST /census/{id} to add organization members to census by member ID
	censusIDEndpoint = "/census/{id}"
	// POST /census/{id}/publish to publish a census
	censusPublishEndpoint = "/census/{id}/publish"
	// POST /census/{id}/group/{groupId}/publish to publish a group census
	censusGroupPublishEndpoint = "/census/{id}/group/{groupId}/publish"
	// GET /census/{id}/participants to get the census participants
	censusParticipantsEndpoint = "/census/{id}/participants"

	// process routes
	// POST /process/{processId} to create a new process
	processCreateEndpoint = "/process"
	// GET /process/{processId} to get process information
	processEndpoint = "/process/{processId}"
	// POST /process/{processId}/auth to check if the voter is authorized
	// processAuthEndpoint = "/process/{processId}/auth"
	// POST /process/{processId}/sign-info to get the sign info for the process.
	// {processId} accepts the 24-hex ProcessID (preferred) or, for backwards
	// compatibility, the 64-hex on-chain election id.
	processSignInfoEndpoint = "/process/{processId}/sign-info"

	// POST /process/{processId}/publish to publish a draft process as an on-chain election
	processPublishEndpoint = "/process/{processId}/publish"

	// PUT /process/{processId}/status to change an on-chain election status.
	// {processId} is the 24-hex ProcessID (not the on-chain election id).
	processStatusEndpoint = "/process/{processId}/status"

	// POST /vote to relay an already-signed vote (public). The target process is taken
	// from the signed vote envelope itself, so no process id appears in the path.
	voteEndpoint = "/vote"

	// GET /process/{processId}/results to get the trimmed on-chain election results (public).
	// {processId} is the 24-hex ProcessID (not the on-chain election id).
	processResultsEndpoint = "/process/{processId}/results"

	// GET /process/{processId}/metadata to get the election metadata JSON (public).
	// {processId} is the 24-hex ProcessID (not the on-chain election id).
	processMetadataEndpoint = "/process/{processId}/metadata"

	// two-factor process bundle routes
	// POST /process/bundle to create a new bundle
	processBundleEndpoint = "/process/bundle"
	// PUT /process/bundle/{bundleId} to add new processes to the bundle
	processBundleUpdateEndpoint = "/process/bundle/{bundleId}"
	// GET /process/bundle/{bundleId} to get the bundle information
	processBundleInfoEndpoint = "/process/bundle/{bundleId}"
	// POST /process/bundle/{bundleId}/auth/{step} to check if the voter is authorized
	processBundleAuthEndpoint = "/process/bundle/{bundleId}/auth/{step}"
	// POST /process/bundle/{bundleId}/auth/resend to resend the auth challenge
	processBundleAuthResendEndpoint = "/process/bundle/{bundleId}/auth/resend"
	// POST /process/bundle/{bundleId}/weight to get the voter weight for the bundle
	processBundleWeightEndpoint = "/process/bundle/{bundleId}/weight"
	// POST /process/bundle/{bundleId}/sign to sign with two-factor authentication
	processBundleSignEndpoint = "/process/bundle/{bundleId}/sign"
	// POST /process/bundle/{bundleId}/check to check census membership for a CSP auth token
	processBundleCheckEndpoint = "/process/bundle/{bundleId}/check"
	// GET /process/bundle/{bundleId}/{participantId} to get the process information
	processBundleMemberEndpoint = "/process/bundle/{bundleId}/{participantId}"
	// POST /process/bundle/{bundleId}/participants/check to check whether an org member is a
	// participant of the bundle's census. Manager/Admin only.
	processBundleParticipantsCheckEndpoint = "/process/bundle/{bundleId}/participants/check"

	// multi-question voting-process routes (new /processes API, plural namespace)
	// POST /processes to create a draft; GET /processes to list (paginated, filterable)
	processesCreateEndpoint = "/processes"
	// PUT /processes/{processId} to update a draft; GET /processes/{processId} to read one (full)
	processesEndpoint = "/processes/{processId}"
	// POST /processes/census/validation validates a census spec (duplicates/missing fields) before create
	processesCensusValidateEndpoint = "/processes/census/validation"
	// PUT /processes/{processId}/census adds members to a published process's census (+ maxCensusSize bump)
	processesCensusEndpoint = "/processes/{processId}/census"
	// GET /processes/{processId}/validation publish-readiness dry-run (protected)
	processesValidateEndpoint = "/processes/{processId}/validation"
	// POST /processes/{processId}/check voter eligibility/status (public CSP)
	processesCheckEndpoint = "/processes/{processId}/check"
	// POST /processes/{processId}/publish to publish the process (one election per question)
	processesPublishEndpoint = "/processes/{processId}/publish"
	// PUT /processes/{processId}/questions/status to change many questions' on-chain status
	processesQuestionsStatusEndpoint = "/processes/{processId}/questions/status"
	// PUT /processes/{processId}/questions/{questionId}/status to change one question's status
	processesQuestionStatusEndpoint = "/processes/{processId}/questions/{questionId}/status"
	// GET /processes/{processId}/questions/{questionId} to read one question (public voter read)
	processesQuestionEndpoint = "/processes/{processId}/questions/{questionId}"
	// GET /processes/{processId}/participants/{participantId} for a single participant's info (public)
	processesParticipantEndpoint = "/processes/{processId}/participants/{participantId}"
	// GET /processes/{processId}/results for the per-question on-chain results (public)
	processesResultsEndpoint = "/processes/{processId}/results"
	// GET /processes/{processId}/results/memos for per-question raw voter memos of questions in
	// RESULTS status (manager/admin only)
	processesMemosEndpoint = "/processes/{processId}/results/memos"
	// GET /processes/{processId}/participants?field=&value= — Manager/Admin lookup of org members by
	// field intersected with the census, with per-question voted status (protected)
	processesParticipantsEndpoint = "/processes/{processId}/participants"
	// POST /processes/{processId}/sign-info — voter's per-question consumed address/nullifier (public)
	processesSignInfoEndpoint = "/processes/{processId}/sign-info"
	// CSP voter routes for a voting process (public)
	processesAuthEndpoint       = "/processes/{processId}/auth/{step}"
	processesAuthResendEndpoint = "/processes/{processId}/auth/resend"
	processesSignEndpoint       = "/processes/{processId}/sign"
	processesWeightEndpoint     = "/processes/{processId}/weight"

	// // census auth routes (currently not implemented)
	// // POST /process/{processId}/auth/0 to initiate auth
	// processAuthInitEndpoint = "/process/{processId}/auth/0"
	// // POST /process/{processId}/auth/1 to verify auth code
	// processAuthVerifyEndpoint = "/process/{processId}/auth/1"
	// // POST /process/{processId}/proof to generate proof
	// processProofEndpoint = "/process/{processId}/proof"
)
