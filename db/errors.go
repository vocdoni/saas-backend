package db

import "fmt"

// PasswordNeedsLookupFieldMessage is the message of ErrPasswordNeedsLookupField, for handlers that
// answer with errors.ErrInvalidData.With and must not double its prefix.
const PasswordNeedsLookupFieldMessage = "password needs another credential or a 2FA field"

var (
	ErrNotFound      = fmt.Errorf("not found")
	ErrInvalidData   = fmt.Errorf("invalid data provided")
	ErrAlreadyExists = fmt.Errorf("already exists")
	// ErrConflict is returned by the conditional writers when the stored document changed since
	// the caller read it, so the write was refused rather than applied on top of someone else's.
	ErrConflict = fmt.Errorf("document changed since it was read")
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
	// ErrBlindNonceNotFound is returned when a round-2 blind claim finds no armed nonce for the
	// (user, election): round 1 was never run, or the nonce was already claimed.
	ErrBlindNonceNotFound = fmt.Errorf("no armed blind nonce")
	// ErrTokenNotVerified is returned if the token has not been verified
	ErrTokenNotVerified = fmt.Errorf("token not verified")
	// ErrUpdateWouldCreateDuplicates is returned when trying to update an OrgMember
	ErrUpdateWouldCreateDuplicates = fmt.Errorf("update would create duplicates")
	// ErrMissingLoginData is returned when a member lacks the auth data a census requires to log in
	ErrMissingLoginData = fmt.Errorf("missing required auth data")
	// ErrPasswordNeedsLookupField is returned for a census whose only login field is the member
	// password: it never takes part in the login hash, so every member would hash alike.
	ErrPasswordNeedsLookupField = fmt.Errorf("%w: %s", ErrInvalidData, PasswordNeedsLookupFieldMessage)
	// ErrInvalidCensusAuthMethod is returned for an unknown census authMethod, or login fields the
	// method cannot use (see ValidateCensusAuthMethod).
	ErrInvalidCensusAuthMethod = fmt.Errorf("invalid census auth method")
	// ErrAutoGroupCannotBeDeleted is returned when trying to delete the auto-generated "All members" group
	ErrAutoGroupCannotBeDeleted = fmt.Errorf("auto-generated group cannot be deleted")
	// ErrAutoGroupMembersCannotBeModified is returned when trying to manually add/remove members from the auto group
	ErrAutoGroupMembersCannotBeModified = fmt.Errorf("auto-generated group membership cannot be manually modified")
	// ErrInvalidMetaPath is returned when MongoDB refuses a meta update path, such as a sub-key of a
	// meta value that is not an object, or two paths where one is a prefix of the other.
	ErrInvalidMetaPath = fmt.Errorf("invalid meta path")
)

// errorsAsStrings converts a slice of errors to a slice of strings
func errorsAsStrings(errs []error) []string {
	s := []string{}
	for _, err := range errs {
		s = append(s, err.Error())
	}
	return s
}
