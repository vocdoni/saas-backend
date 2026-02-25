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
	// ErrBadInputs is returned if the inputs provided to the function are invalid
	ErrBadInputs = fmt.Errorf("bad inputs")
	// ErrProcessAlreadyConsumed is returned if the process has already been consumed by the user
	ErrProcessAlreadyConsumed = fmt.Errorf("token already consumed")
	// ErrTokenNotVerified is returned if the token has not been verified
	ErrTokenNotVerified = fmt.Errorf("token not verified")
	// ErrUpdateWouldCreateDuplicates is returned when trying to update an OrgMember
	ErrUpdateWouldCreateDuplicates = fmt.Errorf("update would create duplicates")
)

// errorsAsStrings converts a slice of errors to a slice of strings
func errorsAsStrings(errs []error) []string {
	s := []string{}
	for _, err := range errs {
		s = append(s, err.Error())
	}
	return s
}
