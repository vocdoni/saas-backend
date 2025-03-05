package storage

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/vocdoni/saas-backend/twofactor/internal"
	"github.com/xlzd/gotp"
	"go.vocdoni.io/dvote/db"
	"go.vocdoni.io/dvote/db/metadb"
	"go.vocdoni.io/dvote/log"
)

const (
	userPrefix           = "u_"
	authTokenIndexPrefix = "a_"
)

// JSONStorage implements the Storage interface using a local key-value database
type JSONStorage struct {
	db             db.Database
	mutex          sync.RWMutex
	maxAttempts    int
	cooldownPeriod time.Duration
}

// NewJSONStorage creates a new JSONStorage instance
func NewJSONStorage() *JSONStorage {
	return &JSONStorage{}
}

// Initialize initializes the storage
func (s *JSONStorage) Initialize(dataDir string, maxAttempts int, cooldownPeriod time.Duration) error {
	var err error
	s.db, err = metadb.New(db.TypePebble, filepath.Clean(dataDir))
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	s.maxAttempts = maxAttempts
	s.cooldownPeriod = cooldownPeriod
	return nil
}

// Reset does nothing for JSON storage
func (s *JSONStorage) Reset() error {
	return nil
}

// userKey creates a database key for a user ID
func userKey(userID internal.UserID) []byte {
	return append([]byte(userPrefix), userID...)
}

// keyToUserID extracts a user ID from a database key
func keyToUserID(key []byte) internal.UserID {
	return internal.UserID(key[len(userPrefix):])
}

// AddUser adds a new user to the storage
func (s *JSONStorage) AddUser(userID internal.UserID, electionIDs []internal.ElectionID, email, phone, extraData string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	user := internal.User{
		ID:        userID,
		Email:     email,
		Phone:     phone,
		ExtraData: extraData,
		Elections: make(map[string]internal.Election),
	}

	// Add elections to the user
	for _, electionID := range electionIDs {
		election := internal.Election{
			ID:                electionID,
			RemainingAttempts: s.maxAttempts,
			Verified:          false,
		}
		user.Elections[electionID.String()] = election
	}

	// Serialize and store the user
	userData, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := tx.Set(userKey(userID), userData); err != nil {
		return fmt.Errorf("failed to store user data: %w", err)
	}

	return tx.Commit()
}

// GetUser retrieves a user from the storage
func (s *JSONStorage) GetUser(userID internal.UserID) (*internal.User, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	userData, err := s.db.Get(userKey(userID))
	if err != nil {
		return nil, internal.ErrUserNotFound
	}

	var user internal.User
	if err := json.Unmarshal(userData, &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	return &user, nil
}

// UpdateUser updates a user in the storage
func (s *JSONStorage) UpdateUser(user *internal.User) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	if user.ID == nil {
		return internal.ErrUserNotFound
	}

	userData, err := json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := tx.Set(userKey(user.ID), userData); err != nil {
		return fmt.Errorf("failed to store user data: %w", err)
	}

	return tx.Commit()
}

// BulkAddUser adds multiple users to the storage in a single transaction
func (s *JSONStorage) BulkAddUser(users []*internal.User) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	for _, user := range users {
		// Ensure the user has elections map initialized
		if user.Elections == nil {
			user.Elections = make(map[string]internal.Election)
		}

		// Set default remaining attempts for each election
		for id, election := range user.Elections {
			if election.RemainingAttempts == 0 {
				election.RemainingAttempts = s.maxAttempts
				user.Elections[id] = election
			}
		}

		// Serialize and store the user
		userData, err := json.Marshal(user)
		if err != nil {
			return fmt.Errorf("failed to marshal user data: %w", err)
		}

		if err := tx.Set(userKey(user.ID), userData); err != nil {
			return fmt.Errorf("failed to store user data: %w", err)
		}
	}

	return tx.Commit()
}

// DeleteUser removes a user from the storage
func (s *JSONStorage) DeleteUser(userID internal.UserID) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	if err := tx.Delete(userKey(userID)); err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	return tx.Commit()
}

// ListUsers returns a list of all users
func (s *JSONStorage) ListUsers() ([]internal.UserID, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var userIDs []internal.UserID
	if err := s.db.Iterate([]byte(userPrefix), func(key, value []byte) bool {
		userIDs = append(userIDs, keyToUserID(key))
		return true
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate users: %w", err)
	}

	return userIDs, nil
}

// SearchUsers searches for users with the given term in their extra data
func (s *JSONStorage) SearchUsers(term string) ([]internal.UserID, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	var userIDs []internal.UserID
	if err := s.db.Iterate([]byte(userPrefix), func(key, value []byte) bool {
		var user internal.User
		if err := json.Unmarshal(value, &user); err != nil {
			log.Warnf("failed to unmarshal user data: %v", err)
			return true
		}

		// Check if the term is in the extra data
		if term != "" && user.ExtraData != "" {
			if json.Valid([]byte(user.ExtraData)) {
				// If extra data is JSON, search in the JSON string
				if json.Valid([]byte(term)) {
					// If term is also JSON, compare as JSON
					if user.ExtraData == term {
						userIDs = append(userIDs, keyToUserID(key))
					}
				} else {
					// If term is not JSON, search as substring
					if contains(user.ExtraData, term) {
						userIDs = append(userIDs, keyToUserID(key))
					}
				}
			} else {
				// If extra data is not JSON, search as substring
				if contains(user.ExtraData, term) {
					userIDs = append(userIDs, keyToUserID(key))
				}
			}
		}

		return true
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate users: %w", err)
	}

	return userIDs, nil
}

// contains checks if a string contains a substring
func contains(s, substr string) bool {
	return s != "" && substr != "" && s != substr && len(s) > len(substr) && s[len(s)-len(substr):] == substr
}

// IsUserInElection checks if a user is in an election
func (s *JSONStorage) IsUserInElection(userID internal.UserID, electionID internal.ElectionID) (bool, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	user, err := s.GetUser(userID)
	if err != nil {
		return false, err
	}

	_, ok := user.Elections[electionID.String()]
	return ok, nil
}

// IsUserVerified checks if a user is verified for an election
func (s *JSONStorage) IsUserVerified(userID internal.UserID, electionID internal.ElectionID) (bool, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	user, err := s.GetUser(userID)
	if err != nil {
		return false, err
	}

	election, ok := user.Elections[electionID.String()]
	if !ok {
		return false, internal.ErrUserNotInElection
	}

	return election.Verified, nil
}

// UpdateAttempts updates the remaining attempts for a user in an election
func (s *JSONStorage) UpdateAttempts(userID internal.UserID, electionID internal.ElectionID, delta int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	userData, err := tx.Get(userKey(userID))
	if err != nil {
		return internal.ErrUserNotFound
	}

	var user internal.User
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	election, ok := user.Elections[electionID.String()]
	if !ok {
		return internal.ErrUserNotInElection
	}

	election.RemainingAttempts += delta
	user.Elections[electionID.String()] = election

	userData, err = json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := tx.Set(userKey(userID), userData); err != nil {
		return fmt.Errorf("failed to store user data: %w", err)
	}

	return tx.Commit()
}

// CreateChallenge creates a new challenge for a user in an election
func (s *JSONStorage) CreateChallenge(
	userID internal.UserID,
	electionID internal.ElectionID,
	secret string,
	token internal.AuthToken,
) (string, string, int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	userData, err := tx.Get(userKey(userID))
	if err != nil {
		return "", "", 0, internal.ErrUserNotFound
	}

	var user internal.User
	if err := json.Unmarshal(userData, &user); err != nil {
		return "", "", 0, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	election, ok := user.Elections[electionID.String()]
	if !ok {
		return "", "", 0, internal.ErrUserNotInElection
	}

	attemptNo := s.maxAttempts - election.RemainingAttempts
	if election.Verified {
		return "", "", attemptNo, internal.ErrUserAlreadyVerified
	}

	if election.LastAttempt != nil {
		if time.Now().Before(election.LastAttempt.Add(s.cooldownPeriod)) {
			return "", "", attemptNo, internal.ErrCooldownPeriodNotElapsed
		}
	}

	if election.RemainingAttempts < 1 {
		return "", "", attemptNo, internal.ErrTooManyAttempts
	}

	// Save the challenge data
	election.AuthToken = &token
	election.ChallengeSecret = secret
	now := time.Now()
	election.LastAttempt = &now
	user.Elections[electionID.String()] = election

	userData, err = json.Marshal(user)
	if err != nil {
		return "", "", attemptNo, fmt.Errorf("failed to marshal user data: %w", err)
	}

	// Save the user data
	if err := tx.Set(userKey(userID), userData); err != nil {
		return "", "", attemptNo, fmt.Errorf("failed to store user data: %w", err)
	}

	// Save the token as index for finding the userID
	tokenUUID := token.ToUUID()
	if err := tx.Set([]byte(authTokenIndexPrefix+tokenUUID.String()), userID); err != nil {
		return "", "", attemptNo, fmt.Errorf("failed to store token index: %w", err)
	}

	return user.Phone, user.Email, attemptNo, tx.Commit()
}

// VerifyChallenge verifies a challenge response
func (s *JSONStorage) VerifyChallenge(electionID internal.ElectionID, token internal.AuthToken, solution string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	tx := s.db.WriteTx()
	defer tx.Discard()

	// Fetch the user ID by token
	tokenUUID := token.ToUUID()
	userID, err := tx.Get([]byte(authTokenIndexPrefix + tokenUUID.String()))
	if err != nil {
		return internal.ErrInvalidToken
	}

	// With the user ID fetch the user data
	userData, err := tx.Get(userKey(internal.UserID(userID)))
	if err != nil {
		return internal.ErrUserNotFound
	}

	var user internal.User
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	// Find the election and check the solution
	election, ok := user.Elections[electionID.String()]
	if !ok {
		return internal.ErrUserNotInElection
	}

	if election.Verified {
		return internal.ErrUserAlreadyVerified
	}

	if election.AuthToken == nil {
		return fmt.Errorf("no auth token available for this election")
	}

	if election.AuthToken.String() != token.String() {
		return internal.ErrInvalidToken
	}

	// Clean token data (we only allow 1 chance)
	election.AuthToken = nil
	if err := tx.Delete([]byte(authTokenIndexPrefix + tokenUUID.String())); err != nil {
		return fmt.Errorf("failed to delete token index: %w", err)
	}

	attemptNo := s.maxAttempts - election.RemainingAttempts - 1
	// Use the stored challenge secret to generate the OTP
	hotp := gotp.NewDefaultHOTP(election.ChallengeSecret)
	challengeData := hotp.At(attemptNo)

	// Set verified to true or false depending on the challenge solution
	election.Verified = challengeData == solution

	// Save the user data
	user.Elections[electionID.String()] = election
	userData, err = json.Marshal(user)
	if err != nil {
		return fmt.Errorf("failed to marshal user data: %w", err)
	}

	if err := tx.Set(userKey(user.ID), userData); err != nil {
		return fmt.Errorf("failed to store user data: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Return error if the solution does not match the challenge
	if challengeData != solution {
		return internal.ErrChallengeFailed
	}

	return nil
}

// GetUserByToken retrieves a user by an authentication token
func (s *JSONStorage) GetUserByToken(token internal.AuthToken) (*internal.User, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	// Fetch the user ID by token
	tokenUUID := token.ToUUID()
	userID, err := s.db.Get([]byte(authTokenIndexPrefix + tokenUUID.String()))
	if err != nil {
		return nil, internal.ErrInvalidToken
	}

	// With the user ID fetch the user data
	userData, err := s.db.Get(userKey(internal.UserID(userID)))
	if err != nil {
		return nil, internal.ErrUserNotFound
	}

	var user internal.User
	if err := json.Unmarshal(userData, &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	return &user, nil
}

// ImportData imports data into the storage
func (s *JSONStorage) ImportData(data []byte) error {
	// Not implemented for JSON storage
	return nil
}

// ExportData exports data from the storage
func (s *JSONStorage) ExportData() ([]byte, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	output := make(map[string]internal.User)
	if err := s.db.Iterate([]byte(userPrefix), func(key, value []byte) bool {
		var user internal.User
		if err := json.Unmarshal(value, &user); err != nil {
			log.Warnf("failed to unmarshal user data: %v", err)
			return true
		}
		output[user.ID.String()] = user
		return true
	}); err != nil {
		return nil, fmt.Errorf("failed to iterate users: %w", err)
	}

	outputData, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal output data: %w", err)
	}

	return outputData, nil
}
