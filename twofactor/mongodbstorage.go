package twofactor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/xlzd/gotp"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"go.vocdoni.io/dvote/log"
)

// TODO: check if authToken is unknown or invalid and userID is valid,
// the attempt should not be invalidated.
/// Only if authtoken is known the attempt should be counted!

// MongoStorage uses an external MongoDB service for stoting the user data of the smshandler.
type MongoStorage struct {
	users          *mongo.Collection
	tokenIndex     *mongo.Collection
	keysLock       sync.RWMutex
	maxSmsAttempts int
	coolDownTime   time.Duration
}

func (ms *MongoStorage) Init(dataDir string, maxAttempts int, coolDownTime time.Duration) error {
	var err error
	database := "twofactor"
	log.Infof("connecting to mongodb %s@%s", dataDir, database)
	opts := options.Client()
	opts.ApplyURI(dataDir)
	opts.SetMaxConnecting(20)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(dataDir))
	defer cancel()
	if err != nil {
		return err
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

	ms.users = client.Database(database).Collection("users")
	ms.tokenIndex = client.Database(database).Collection("tokenindex")
	ms.maxSmsAttempts = maxAttempts
	ms.coolDownTime = coolDownTime

	// If reset flag is enabled, Reset drops the database documents and recreates indexes
	// else, just createIndexes
	if reset := os.Getenv("CSP_RESET_DB"); reset != "" {
		err := ms.Reset()
		if err != nil {
			return err
		}
	} else {
		err := ms.createIndexes()
		if err != nil {
			return err
		}
	}

	return nil
}

func (ms *MongoStorage) createIndexes() error {
	// Create text index on `extraData` for finding user data
	ctx, cancel3 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel3()
	index := mongo.IndexModel{
		Keys: bson.D{
			{Key: "extradata", Value: "text"},
		},
	}
	_, err := ms.users.Indexes().CreateOne(ctx, index)
	if err != nil {
		return err
	}
	return nil
}

func (ms *MongoStorage) Reset() error {
	log.Infof("resetting database")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := ms.users.Drop(ctx); err != nil {
		return err
	}
	if err := ms.createIndexes(); err != nil {
		return err
	}
	return nil
}

func (ms *MongoStorage) MaxAttempts() int {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	return ms.maxSmsAttempts
}

func (ms *MongoStorage) Users() (*Users, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	opts := options.FindOptions{}
	opts.SetProjection(bson.M{"_id": true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cur, err := ms.users.Find(ctx, bson.M{}, &opts)
	if err != nil {
		return nil, err
	}

	ctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	var users Users
	for cur.Next(ctx) {
		user := UserData{}
		err := cur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		users.Users = append(users.Users, user.UserID)
	}
	return &users, nil
}

func (ms *MongoStorage) AddUser(userID internal.HexBytes, processIDs []internal.HexBytes,
	mail, phone, extra string,
) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	user := UserData{
		UserID:    userID,
		ExtraData: extra,
		Phone:     phone,
		Mail:      mail,
	}
	user.Elections = make(map[string]UserElection, len(processIDs))
	for _, e := range HexBytesToElection(processIDs, ms.maxSmsAttempts) {
		user.Elections[e.ElectionID.String()] = e
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ms.users.InsertOne(ctx, user)
	return err
}

func (ms *MongoStorage) User(userID internal.HexBytes) (*UserData, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	user, err := ms.getUserData(userID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (ms *MongoStorage) getUserData(userID internal.HexBytes) (*UserData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.users.FindOne(ctx, bson.M{"_id": userID})
	var user UserData
	if err := result.Decode(&user); err != nil {
		log.Warn(err)
		return nil, ErrUserUnknown
	}
	return &user, nil
}

// updateUser makes a upsert on the user data
func (ms *MongoStorage) updateUser(user *UserData) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	opts := options.ReplaceOptions{}
	opts.Upsert = new(bool)
	*opts.Upsert = true
	_, err := ms.users.ReplaceOne(ctx, bson.M{"_id": user.UserID}, user, &opts)
	if err != nil {
		return fmt.Errorf("cannot update object: %w", err)
	}
	return nil
}

func (ms *MongoStorage) UpdateUser(udata *UserData) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	return ms.updateUser(udata)
}

func (ms *MongoStorage) BelongsToElection(userID internal.HexBytes,
	electionID internal.HexBytes,
) (bool, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	user, err := ms.getUserData(userID)
	if err != nil {
		return false, err
	}
	_, ok := user.Elections[electionID.String()]
	return ok, nil
}

func (ms *MongoStorage) SetAttempts(userID, electionID internal.HexBytes, delta int) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	user, err := ms.getUserData(userID)
	if err != nil {
		return err
	}
	election, ok := user.Elections[electionID.String()]
	if !ok {
		return ErrUserNotBelongsToElection
	}
	election.RemainingAttempts += delta
	user.Elections[electionID.String()] = election
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = ms.users.ReplaceOne(ctx, bson.M{"_id": userID}, user)
	if err != nil {
		return fmt.Errorf("cannot update object: %w", err)
	}
	return nil
}

func (ms *MongoStorage) NewAttempt(userID, electionID internal.HexBytes,
	challengeSecret string, token *uuid.UUID,
) (string, string, int, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	user, err := ms.getUserData(userID)
	if err != nil {
		return "", "", 0, err
	}

	election, ok := user.Elections[electionID.String()]
	if !ok {
		return "", "", 0, ErrUserNotBelongsToElection
	}

	attemptNo := ms.maxSmsAttempts - election.RemainingAttempts
	// Check if the CSP signature is already consumed for the user/election
	if election.Consumed {
		return "", "", attemptNo, ErrUserAlreadyVerified
	}
	// Check cool down time
	if election.LastAttempt != nil {
		if time.Now().Before(election.LastAttempt.Add(ms.coolDownTime)) {
			return "", "", attemptNo, ErrAttemptCoolDownTime
		}
	}
	// Check remaining attempts
	if election.RemainingAttempts < 1 {
		return "", "", attemptNo, ErrTooManyAttempts
	}
	// Save new data
	election.AuthToken = token
	election.ChallengeSecret = challengeSecret
	t := time.Now()
	election.LastAttempt = &t
	user.Elections[electionID.String()] = election
	if err := ms.updateUser(user); err != nil {
		return "", "", attemptNo, err
	}

	// Save the token as index for finding the userID
	atindex := AuthTokenIndex{
		AuthToken: token,
		UserID:    user.UserID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = ms.tokenIndex.InsertOne(ctx, atindex)
	if err != nil {
		return "", "", attemptNo, err
	}

	return user.Phone, user.Mail, attemptNo, nil
}

func (ms *MongoStorage) Exists(userID internal.HexBytes) bool {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	_, err := ms.getUserData(userID)
	return err == nil
}

func (ms *MongoStorage) Verified(userID, electionID internal.HexBytes) (bool, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	user, err := ms.getUserData(userID)
	if err != nil {
		return false, err
	}
	election, ok := user.Elections[electionID.String()]
	if !ok {
		return false, ErrUserNotBelongsToElection
	}
	return election.Consumed, nil
}

func (ms *MongoStorage) GetUserFromToken(token *uuid.UUID) (*UserData, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	if result.Err() != nil {
		return nil, ErrInvalidAuthToken
	}
	var atIndex AuthTokenIndex
	if err := result.Decode(&atIndex); err != nil {
		return nil, ErrInvalidAuthToken
	}
	// with the user ID fetch the user data
	user, err := ms.getUserData(atIndex.UserID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (ms *MongoStorage) VerifyChallenge(electionID internal.HexBytes,
	token *uuid.UUID, solution string,
) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	// fetch the user ID by token
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := ms.tokenIndex.FindOne(ctx, bson.M{"_id": token})
	if result.Err() != nil {
		log.Warnf("cannot fetch auth token: %v", result.Err())
		return ErrInvalidAuthToken
	}
	var atIndex AuthTokenIndex
	if err := result.Decode(&atIndex); err != nil {
		log.Warnf("cannot decode auth token: %v", err)
		return ErrInvalidAuthToken
	}

	// with the user ID fetch the user data
	user, err := ms.getUserData(atIndex.UserID)
	if err != nil {
		return err
	}

	// find the election and check the solution
	election, ok := user.Elections[electionID.String()]
	if !ok {
		return ErrUserNotBelongsToElection
	}
	if election.Consumed {
		return ErrUserAlreadyVerified
	}
	if election.AuthToken == nil {
		return fmt.Errorf("no auth token available for this election")
	}
	if election.AuthToken.String() != token.String() {
		return ErrInvalidAuthToken
	}

	// clean token data (we only allow 1 chance)
	// election.AuthToken = nil
	// ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	// defer cancel2()
	// if _, err := ms.tokenIndex.DeleteOne(ctx, bson.M{"_id": token}); err != nil {
	// 	return err
	// }

	attemptNo := ms.maxSmsAttempts - election.RemainingAttempts - 1
	// Use the stored challenge secret to generate the OTP
	hotp := gotp.NewDefaultHOTP(election.ChallengeSecret)
	challengeData := hotp.At(attemptNo)

	// set consumed to true or false depending on the challenge solution
	election.Consumed = challengeData == solution

	// save the user data
	user.Elections[electionID.String()] = election
	if err := ms.updateUser(user); err != nil {
		return err
	}

	// return error if the solution does not match the challenge
	if challengeData != solution {
		return ErrChallengeCodeFailure
	}

	return nil
}

func (ms *MongoStorage) DelUser(userID internal.HexBytes) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ms.users.DeleteOne(ctx, bson.M{"_id": userID})
	return err
}

func (ms *MongoStorage) Search(term string) (*Users, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	opts := options.FindOptions{}
	opts.SetProjection(bson.M{"_id": true})
	filter := bson.M{"$text": bson.M{"$search": term}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cur, err := ms.users.Find(ctx, filter, &opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	var users Users
	for cur.Next(ctx) {
		user := UserData{}
		err := cur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		users.Users = append(users.Users, user.UserID)
	}
	return &users, nil
}

func (ms *MongoStorage) Import(data []byte) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	var collection UserCollection
	if err := json.Unmarshal(data, &collection); err != nil {
		return err
	}
	for _, u := range collection.Users {
		if err := ms.updateUser(&u); err != nil {
			log.Warnf("cannot upsert %s", u.UserID)
		}
	}
	return nil
}

func (ms *MongoStorage) String() string {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cur, err := ms.users.Find(ctx, bson.D{{}})
	if err != nil {
		log.Warn(err)
		return "{}"
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	var collection UserCollection
	for cur.Next(ctx2) {
		var user UserData
		err := cur.Decode(&user)
		if err != nil {
			log.Warn(err)
		}
		collection.Users = append(collection.Users, user)
	}
	data, err := json.MarshalIndent(collection, "", " ")
	if err != nil {
		log.Warn(err)
	}
	return string(data)
}
