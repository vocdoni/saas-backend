package db

import "fmt"

var (
	ErrNotFound      = fmt.Errorf("not found")
	ErrInvalidData   = fmt.Errorf("invalid data provided")
	ErrAlreadyExists = fmt.Errorf("already exists")
	// ErrTokenNotFound is returned if the token is not found in the database
	ErrTokenNotFound = fmt.Errorf("token not found")
	// ErrPrepareDocument is returned if the update document cannot be created
	ErrPrepareDocument = fmt.Errorf("cannot create update document")
	// ErrStoreToken is returned if the token cannot be created or updated
	ErrStoreToken = fmt.Errorf("cannot set token")
	// ErrProcessNotFound is returned if the process is not found in the user data
	ErrProcessNotFound = fmt.Errorf("process not found")
	// ErrBadInputs is returned if the inputs provided to the function are invalid
	ErrBadInputs = fmt.Errorf("bad inputs")
	// ErrProcessAlreadyConsumed is returned if the process has already been consumed by the user
	ErrProcessAlreadyConsumed = fmt.Errorf("token already consumed")
	// ErrTokenNotVerified is returned if the token has not been verified
	ErrTokenNotVerified = fmt.Errorf("token not verified")
	// ErrInvalidConfig is returned if the configuration provided is invalid
	ErrInvalidConfig = fmt.Errorf("invalid configuration")
	// ErrUpdateWouldCreateDuplicates is returned when trying to update an OrgMember
	ErrUpdateWouldCreateDuplicates = fmt.Errorf("update would create duplicates")
)
