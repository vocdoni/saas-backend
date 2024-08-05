package api

const (
	// auth routes

	// POST /auth/refresh to refresh the JWT token
	authRefresTokenEndpoint = "/auth/refresh"
	// POST /auth/login to login and get a JWT token
	authLoginEndpoint = "/auth/login"

	// user routes

	// POST /users to register a new user
	usersEndpoint = "/users"
	// GET /users/me to get the user information
	myUsersEndpoint = "/users/me"

	// signer routes
	// POST /transactions to sign a transaction
	signTxEndpoint = "/transactions"
	// POST /transactions/message to sign a message
	signMessageEndpoint = "/transactions/message"
)
