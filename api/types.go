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

// EncodedSignedTxResponse is the response of the sign a transaction request.
// It includes the signed transaction encoded in base64.
type EncodedSignedTxResponse struct {
	Data string `json:"data"`
}
