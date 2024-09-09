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
	// POST /auth/verify to verify the user
	verifyUserEndpoint = "/auth/verify"

	// user routes

	// POST /users to register a new user
	usersEndpoint = "/users"
	// GET /users/me to get the current user information
	// PUT /users/me to update the current user information
	myUsersEndpoint = "/users/me"
	// PUT /users/me/password to update the current user password
	myUsersPasswordEndpoint = "/users/me/password"

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
)
