package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vocdoni/saas-backend/twofactor/internal"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.vocdoni.io/dvote/log"
)

// AuthTokenIndex is used to index a token with its userID
type AuthTokenIndex struct {
	AuthToken internal.AuthToken `bson:"_id"`
	UserID    internal.UserID    `bson:"userid"`
}

// MongoDBStorage implements the Storage interface using MongoDB
type MongoDBStorage struct {
	users          *mongo.Collection
	tokenIndex     *mongo.Collection
	mutex          sync.RWMutex
	maxAttempts    int
	cooldownPeriod time.Duration
	client         *mongo.Client
}

// NewMongoDBStorage creates a new MongoDBStorage instance
func NewMongoDBStorage() *MongoDBStorage {
	return &MongoDBStorage{}
}

// Initialize initializes the storage
func (s *MongoDBStorage) Initialize(dataDir string, maxAttempts int, cooldownPeriod time.Duration) error {
	database := "twofactor"

	// Ensure the MongoDB URI has the proper scheme
	mongoURI := dataDir
	if !strings.HasPrefix(mongoURI, "mongodb://") && !strings.HasPrefix(mongoURI, "mongodb+srv://") {
		// If no scheme is provided, assume mongodb://
		mongoURI = "mongodb://" + mongoURI
	}

	log.Infof("connecting to mongodb %s (database: %s)", mongoURI, database)

	opts := options.Client()
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	client, err := mongo.Connect(ctx, opts.ApplyURI(mongoURI).SetMaxConnecting(20))
	defer cancel()
	if err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Shutdown database connection when SIGTERM received
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		<-c
		log.Warnf("received SIGTERM, disconnecting mongo database")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := client.Disconnect(ctx)
		if err != nil {
			log.Warn(err)
		}
		cancel()
	}()

	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	err = client.Ping(ctx, readpref.Primary())
	if err != nil {
		return fmt.Errorf("cannot connect to mongodb: %w", err)
	}

	s.client = client
	s.users = client.Database(database).Collection("users")
	s.tokenIndex = client.Database(database).Collection("tokenindex")
	s.maxAttempts = maxAttempts
	s.cooldownPeriod = cooldownPeriod

	// If reset flag is enabled, Reset drops the database documents and recreates indexes
	// else, just createIndexes
	if reset := os.Getenv("CSP_RESET_DB"); reset != "" {
		err := s.Reset()
		if err != nil {
			return fmt.Errorf("failed to reset database: %w", err)
		}
	} else {
		err := s.createIndexes()
		if err != nil {
			return fmt.Errorf("failed to create indexes: %w", err)
		}
	}

	return nil
}

// createIndexes creates the necessary indexes for the MongoDB collections
func (s *MongoDBStorage) createIndexes() error {
	// Create text index on `extraData` for finding user data
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	index := mongo.IndexModel{
		Keys: bson.D{
			{Key: "extradata", Value: "text"},
		},
	}

	_, err := s.users.Indexes().CreateOne(ctx, index)
	if err != nil {
		return fmt.Errorf("failed to create index: %w", err)
	}

	return nil
}

// Reset clears all data in the storage
func (s *MongoDBStorage) Reset() error {
	log.Infof("resetting database")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.users.Drop(ctx); err != nil {
		return fmt.Errorf("failed to drop users collection: %w", err)
	}

	if err := s.tokenIndex.Drop(ctx); err != nil {
		return fmt.Errorf("failed to drop token index collection: %w", err)
	}

	if err := s.createIndexes(); err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	return nil
}

// AddUser adds a new user to the storage
func (s *MongoDBStorage) AddUser(
	userID internal.UserID,
	electionIDs []internal.ElectionID,
	email, phone, extraData string,
) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.users.InsertOne(ctx, user)
	if err != nil {
		return fmt.Errorf("failed to insert user: %w", err)
	}

	return nil
}

// GetUser retrieves a user from the storage
func (s *MongoDBStorage) GetUser(userID internal.UserID) (*internal.User, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := s.users.FindOne(ctx, bson.M{"_id": userID})
	if result.Err() != nil {
		return nil, internal.ErrUserNotFound
	}

	var user internal.User
	if err := result.Decode(&user); err != nil {
		return nil, fmt.Errorf("failed to decode user: %w", err)
	}

	return &user, nil
}

// UpdateUser updates a user in the storage
func (s *MongoDBStorage) UpdateUser(user *internal.User) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	opts := options.ReplaceOptions{}
	opts.SetUpsert(true)

	_, err := s.users.ReplaceOne(ctx, bson.M{"_id": user.ID}, user, &opts)
	if err != nil {
		return fmt.Errorf("failed to update user: %w", err)
	}

	return nil
}

// BulkAddUser adds multiple users to the storage in batches
func (s *MongoDBStorage) BulkAddUser(users []*internal.User) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if len(users) == 0 {
		return nil
	}

	// Process users in batches of 1000
	const batchSize = 1000
	totalUsers := len(users)
	totalBatches := (totalUsers + batchSize - 1) / batchSize // Ceiling division

	log.Infow("starting bulk user insertion", "totalUsers", totalUsers, "batchSize", batchSize, "totalBatches", totalBatches)

	for batchIndex := 0; batchIndex < totalBatches; batchIndex++ {
		// Calculate start and end indices for this batch
		startIdx := batchIndex * batchSize
		endIdx := startIdx + batchSize
		if endIdx > totalUsers {
			endIdx = totalUsers
		}

		batchUsers := users[startIdx:endIdx]
		log.Infow("processing user batch", "batchIndex", batchIndex+1, "batchSize", len(batchUsers))

		// Prepare documents for bulk insert
		documents := make([]any, 0, len(batchUsers))
		for _, user := range batchUsers {
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

			documents = append(documents, user)
		}

		// Perform bulk insert for this batch
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := s.users.InsertMany(ctx, documents)
		cancel()

		if err != nil {
			return fmt.Errorf("failed to bulk insert users batch %d/%d: %w",
				batchIndex+1, totalBatches, err)
		}

		log.Infow("successfully inserted user batch",
			"batchIndex", batchIndex+1,
			"totalBatches", totalBatches,
			"usersInserted", len(batchUsers))
	}

	log.Infow("completed bulk user insertion", "totalUsers", totalUsers)
	return nil
}

// DeleteUser removes a user from the storage
func (s *MongoDBStorage) DeleteUser(userID internal.UserID) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.users.DeleteOne(ctx, bson.M{"_id": userID})
	if err != nil {
		return fmt.Errorf("failed to delete user: %w", err)
	}

	return nil
}

// ListUsers returns a list of all users
func (s *MongoDBStorage) ListUsers() ([]internal.UserID, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	opts := options.FindOptions{}
	opts.SetProjection(bson.M{"_id": true})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.users.Find(ctx, bson.M{}, &opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find users: %w", err)
	}

	var users []internal.UserID
	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	for cursor.Next(ctx) {
		var user internal.User
		if err := cursor.Decode(&user); err != nil {
			log.Warnf("failed to decode user: %v", err)
			continue
		}
		users = append(users, user.ID)
	}

	return users, nil
}

// SearchUsers searches for users with the given term in their extra data
func (s *MongoDBStorage) SearchUsers(term string) ([]internal.UserID, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	opts := options.FindOptions{}
	opts.SetProjection(bson.M{"_id": true})

	filter := bson.M{"$text": bson.M{"$search": term}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.users.Find(ctx, filter, &opts)
	if err != nil {
		return nil, fmt.Errorf("failed to find users: %w", err)
	}

	var users []internal.UserID
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	for cursor.Next(ctx) {
		var user internal.User
		if err := cursor.Decode(&user); err != nil {
			log.Warnf("failed to decode user: %v", err)
			continue
		}
		users = append(users, user.ID)
	}

	return users, nil
}

// IsUserInElection checks if a user is in an election
func (s *MongoDBStorage) IsUserInElection(userID internal.UserID, electionID internal.ElectionID) (bool, error) {
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
func (s *MongoDBStorage) IsUserVerified(userID internal.UserID, electionID internal.ElectionID) (bool, error) {
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
func (s *MongoDBStorage) UpdateAttempts(userID internal.UserID, electionID internal.ElectionID, delta int) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	user, err := s.GetUser(userID)
	if err != nil {
		return err
	}

	election, ok := user.Elections[electionID.String()]
	if !ok {
		return internal.ErrUserNotInElection
	}

	election.RemainingAttempts += delta
	user.Elections[electionID.String()] = election

	return s.UpdateUser(user)
}

// CreateChallenge creates a new challenge for a user in an election
func (s *MongoDBStorage) CreateChallenge(
	userID internal.UserID,
	electionID internal.ElectionID,
	secret string,
	token internal.AuthToken,
) (string, string, int, error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	user, err := s.GetUser(userID)
	if err != nil {
		return "", "", 0, err
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

	if err := s.UpdateUser(user); err != nil {
		return "", "", attemptNo, err
	}

	// Save the token as index for finding the userID
	atindex := AuthTokenIndex{
		AuthToken: token,
		UserID:    user.ID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = s.tokenIndex.InsertOne(ctx, atindex)
	if err != nil {
		return "", "", attemptNo, fmt.Errorf("failed to insert token index: %w", err)
	}

	return user.Phone, user.Email, attemptNo, nil
}

// VerifyChallenge verifies a challenge response
func (s *MongoDBStorage) VerifyChallenge(electionID internal.ElectionID, token internal.AuthToken, solution string) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Fetch the user ID by token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := s.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	if result.Err() != nil {
		return internal.ErrInvalidToken
	}

	var atIndex AuthTokenIndex
	if err := result.Decode(&atIndex); err != nil {
		return fmt.Errorf("failed to decode token index: %w", err)
	}

	// With the user ID fetch the user data
	user, err := s.GetUser(atIndex.UserID)
	if err != nil {
		return err
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
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if _, err := s.tokenIndex.DeleteOne(ctx, bson.M{"_id": token}); err != nil {
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
	if err := s.UpdateUser(user); err != nil {
		return err
	}

	// Return error if the solution does not match the challenge
	if challengeData != solution {
		return internal.ErrChallengeFailed
	}

	return nil
}

// GetUserByToken retrieves a user by an authentication token
func (s *MongoDBStorage) GetUserByToken(token internal.AuthToken) (*internal.User, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result := s.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	if result.Err() != nil {
		return nil, internal.ErrInvalidToken
	}

	var atIndex AuthTokenIndex
	if err := result.Decode(&atIndex); err != nil {
		return nil, fmt.Errorf("failed to decode token index: %w", err)
	}

	// With the user ID fetch the user data
	user, err := s.GetUser(atIndex.UserID)
	if err != nil {
		return nil, err
	}

	return user, nil
}

// ImportData imports data into the storage
func (s *MongoDBStorage) ImportData(data []byte) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	var users map[string]internal.User
	if err := json.Unmarshal(data, &users); err != nil {
		return fmt.Errorf("failed to unmarshal data: %w", err)
	}

	for _, user := range users {
		if err := s.UpdateUser(&user); err != nil {
			log.Warnf("failed to update user %s: %v", user.ID, err)
		}
	}

	return nil
}

// ExportData exports data from the storage
func (s *MongoDBStorage) ExportData() ([]byte, error) {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cursor, err := s.users.Find(ctx, bson.D{{}})
	if err != nil {
		return nil, fmt.Errorf("failed to find users: %w", err)
	}

	users := make(map[string]internal.User)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()

	for cursor.Next(ctx2) {
		var user internal.User
		if err := cursor.Decode(&user); err != nil {
			log.Warnf("failed to decode user: %v", err)
			continue
		}
		users[user.ID.String()] = user
	}

	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal users: %w", err)
	}

	return data, nil
}
