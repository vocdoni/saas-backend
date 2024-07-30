package api

import (
	"time"
)

// Register is the request to register a new user.
type Register struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login is the request to login a user.
type Login struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the response of the login request which includes the JWT token
type LoginResponse struct {
	Token    string    `json:"token"`
	Expirity time.Time `json:"expirity"`
}
