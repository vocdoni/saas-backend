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
	// ErrAutoGroupCannotBeDeleted is returned when trying to delete the auto-generated "All members" group
	ErrAutoGroupCannotBeDeleted = fmt.Errorf("auto-generated group cannot be deleted")
	// ErrAutoGroupMembersCannotBeModified is returned when trying to manually add/remove members from the auto group
	ErrAutoGroupMembersCannotBeModified = fmt.Errorf("auto-generated group membership cannot be manually modified")
	// ErrManagedQuotaReached is returned when an atomic integrator-quota reservation would
	// exceed the integrator's managed-orgs, managed-processes or managed-census-size limit.
	ErrManagedQuotaReached = fmt.Errorf("integrator managed quota reached")
)

// CensusInUseByPublishedProcessError reports that an operation would tear down a census that a
// published process is still voting on, naming the processes that block it. Deleting a group is a
// metadata operation for every other purpose, so it must not double as a way to wipe a live
// electorate; the caller removes members explicitly instead.
type CensusInUseByPublishedProcessError struct {
	ProcessIDs []string
}

func (e *CensusInUseByPublishedProcessError) Error() string {
	return fmt.Sprintf("census is in use by %d published process(es)", len(e.ProcessIDs))
}

// MembersAlreadyVotedError reports the members an operation refuses to strip of eligibility because
// they have already cast a ballot. Carrying the ids lets the caller name them back to the client.
type MembersAlreadyVotedError struct {
	MemberIDs []string
}

func (e *MembersAlreadyVotedError) Error() string {
	return fmt.Sprintf("%d member(s) already signed for", len(e.MemberIDs))
}

// errorsAsStrings converts a slice of errors to a slice of strings
func errorsAsStrings(errs []error) []string {
	s := []string{}
	for _, err := range errs {
		s = append(s, err.Error())
	}
	return s
}
