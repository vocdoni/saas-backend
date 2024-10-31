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
	// GET /organizations/roles to get the available organization member roles
	organizationRolesEndpoint = "/organizations/roles"
)
