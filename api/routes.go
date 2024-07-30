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
	// GET /users/address to get the address of the current user
	currentUserAddressEndpoint = "/users/address"
)
