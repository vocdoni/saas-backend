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

// UserAddressResponse is the response of the address request for a user
type UserAddressResponse struct {
	Address string `json:"address"`
}

// TransactionData is the struct that contains the data of a transaction to
// be signed, but also is used to return the signed transaction.
type TransactionData struct {
	Data string `json:"data"`
}
