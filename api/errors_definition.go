//nolint:lll
package api

import (
	"fmt"
	"net/http"
)

// The custom Error type satisfies the error interface.
// Error() returns a human-readable description of the error.
//
// Error codes in the 40001-49999 range are the user's fault,
// and they return HTTP Status 400 or 404 (or even 204), whatever is most appropriate.
//
// Error codes 50001-59999 are the server's fault
// and they return HTTP Status 500 or 503, or something else if appropriate.
//
// The initial list of errors were more or less grouped by topic, but the list grows with time in a random fashion.
// NEVER change any of the current error codes, only append new errors after the current last 4XXX or 5XXX
// If you notice there's a gap (say, error code 4010, 4011 and 4013 exist, 4012 is missing) DON'T fill in the gap,
// that code was used in the past for some error (not anymore) and shouldn't be reused.
// There's no correlation between Code and HTTP Status,
// for example the fact that Code 4045 returns HTTP Status 404 Not Found is just a coincidence
//
// Do note that HTTPstatus 204 No Content implies the response body will be empty,
// so the Code and Message will actually be discarded, never sent to the client
var (
	ErrUnauthorized            = Error{Code: 40001, HTTPstatus: http.StatusUnauthorized, Err: fmt.Errorf("user not authorized")}
	ErrEmailMalformed          = Error{Code: 40002, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("email malformed")}
	ErrPasswordTooShort        = Error{Code: 40003, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("password too short")}
	ErrMalformedBody           = Error{Code: 40004, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("malformed JSON body")}
	ErrDuplicateConflict       = Error{Code: 40901, HTTPstatus: http.StatusConflict, Err: fmt.Errorf("duplicate conflict")}
	ErrInvalidUserData         = Error{Code: 40005, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid user data")}
	ErrCouldNotSignTransaction = Error{Code: 40006, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("could not sign transaction")}
	ErrInvalidTxFormat         = Error{Code: 40007, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("invalid transaction format")}
	ErrTxTypeNotAllowed        = Error{Code: 40008, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("transaction type not allowed")}
	ErrOrganizationNotFound    = Error{Code: 40009, HTTPstatus: http.StatusNotFound, Err: fmt.Errorf("organization not found")}
	ErrMalformedURLParam       = Error{Code: 40010, HTTPstatus: http.StatusBadRequest, Err: fmt.Errorf("malformed URL parameter")}

	ErrMarshalingServerJSONFailed  = Error{Code: 50001, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("marshaling (server-side) JSON failed")}
	ErrGenericInternalServerError  = Error{Code: 50002, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("internal server error")}
	ErrCouldNotCreateFaucetPackage = Error{Code: 50003, HTTPstatus: http.StatusInternalServerError, Err: fmt.Errorf("could not create faucet package")}
)
