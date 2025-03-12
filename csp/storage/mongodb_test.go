package storage

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/google/uuid"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/test"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var (
	testDB         *MongoStorage
	testUserID     = []byte("userID")
	testProcessID  = internal.HexBytes("processID")
	testUserBundle = BundleData{
		ID: []byte("bundleID"),
		Processes: map[string]ProcessData{
			testProcessID.String(): {
				ID: testProcessID,
			},
		},
		LastAttempt: nil,
	}
	testUserExtraData = "extraData"
	testUserPhone     = "+346787878"
	testUserMail      = "test@user.com"
	testToken         = internal.HexBytes(uuid.New().String())
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	// start a MongoDB container for testing
	dbContainer, err := test.StartMongoContainer(ctx)
	if err != nil {
		panic(fmt.Sprintf("failed to start MongoDB container: %v", err))
	}
	// get the MongoDB connection string
	mongoURI, err := dbContainer.Endpoint(ctx, "mongodb")
	if err != nil {
		panic(fmt.Sprintf("failed to get MongoDB endpoint: %v", err))
	}
	// preparing connection
	opts := options.Client()
	opts.ApplyURI(mongoURI)
	opts.SetMaxConnecting(200)
	timeout := time.Second * 10
	opts.ConnectTimeout = &timeout
	// create a new client with the connection options
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		panic(fmt.Errorf("cannot connect to mongodb: %w", err))
	}
	// check if the connection is successful
	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	// try to ping the database
	if err = client.Ping(ctx, readpref.Primary()); err != nil {
		panic(fmt.Errorf("cannot ping to mongodb: %w", err))
	}
	testDB = new(MongoStorage)
	if err := testDB.Init(&MongoConfig{Client: client}); err != nil {
		panic(fmt.Sprintf("failed to create new MongoDB connection: %v", err))
	}
	// reset the database
	if err := testDB.Reset(); err != nil {
		panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
	}
	// ensure the database is reset when the test finishes
	defer func() {
		if err := testDB.Reset(); err != nil {
			panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.Disconnect(ctx); err != nil {
			panic(fmt.Sprintf("failed to close MongoDB connection: %v", err))
		}
		if err := dbContainer.Terminate(ctx); err != nil {
			panic(fmt.Sprintf("failed to terminate MongoDB container: %v", err))
		}
	}()
	os.Exit(m.Run())
}

func resetDB(c *qt.C) {
	c.Assert(testDB.Reset(), qt.IsNil)
}

func TestUserSetUser(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)

	testUserData := UserData{
		ID:        testUserID,
		Bundles:   map[string]BundleData{},
		ExtraData: testUserExtraData,
		Phone:     testUserPhone,
		Mail:      testUserMail,
	}

	// user not found
	_, err := testDB.User(testUserData.ID)
	c.Assert(err, qt.ErrorIs, ErrUserNotFound)

	// set user
	err = testDB.SetUser(testUserData)
	c.Assert(err, qt.IsNil)
	// get user
	user, err := testDB.User(testUserData.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Bundles, qt.HasLen, 0)
	c.Assert(user.ExtraData, qt.Equals, testUserData.ExtraData)
	c.Assert(user.Phone, qt.Equals, testUserData.Phone)
	c.Assert(user.Mail, qt.Equals, testUserData.Mail)
	// update user phone
	testUserData.Phone = "+346575757"
	err = testDB.SetUser(testUserData)
	c.Assert(err, qt.IsNil)
	// get user
	user, err = testDB.User(testUserData.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Bundles, qt.HasLen, 0)
	c.Assert(user.ExtraData, qt.Equals, testUserData.ExtraData)
	c.Assert(user.Phone, qt.Equals, testUserData.Phone)
	c.Assert(user.Mail, qt.Equals, testUserData.Mail)
	// add bundle
	testUserData.Bundles[testUserBundle.ID.String()] = testUserBundle
	err = testDB.SetUser(testUserData)
	c.Assert(err, qt.IsNil)
	// get user
	user, err = testDB.User(testUserData.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Bundles, qt.HasLen, 1)
	c.Assert(user.Bundles[testUserBundle.ID.String()].ID, qt.DeepEquals, testUserBundle.ID)
	c.Assert(user.Bundles[testUserBundle.ID.String()].Processes, qt.HasLen, 1)
	c.Assert(user.Bundles[testUserBundle.ID.String()].Processes[testProcessID.String()].ID, qt.DeepEquals, testProcessID)
	c.Assert(user.Bundles[testUserBundle.ID.String()].LastAttempt, qt.IsNil)
}

func TestSetUserBundle(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)
	// try to add a bundle to a non-existing user
	err := testDB.SetUserBundle(testUserID, testUserBundle.ID, []internal.HexBytes{testProcessID}...)
	c.Assert(err, qt.ErrorIs, ErrUserNotFound)
	// add user
	c.Assert(testDB.SetUser(UserData{
		ID:        testUserID,
		Bundles:   map[string]BundleData{},
		ExtraData: testUserExtraData,
		Phone:     testUserPhone,
		Mail:      testUserMail,
	}), qt.IsNil)
	user, err := testDB.User(testUserID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Bundles, qt.HasLen, 0)
	// add bundle
	err = testDB.SetUserBundle(testUserID, testUserBundle.ID, []internal.HexBytes{testProcessID}...)
	c.Assert(err, qt.IsNil)
	// get user bundles
	user, err = testDB.User(testUserID)
	c.Assert(err, qt.IsNil)
	c.Assert(user.Bundles, qt.HasLen, 1)
	c.Assert(user.Bundles[testUserBundle.ID.String()].ID, qt.DeepEquals, testUserBundle.ID)
	c.Assert(user.Bundles[testUserBundle.ID.String()].Processes, qt.HasLen, 1)
	c.Assert(user.Bundles[testUserBundle.ID.String()].Processes[testProcessID.String()], qt.DeepEquals, testUserBundle.Processes[testProcessID.String()])
	c.Assert(user.Bundles[testUserBundle.ID.String()].LastAttempt, qt.IsNil)
}

func TestAddUsers(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)
	users := testUsersBulk(10000)
	err := testDB.AddUsers(users)
	c.Assert(err, qt.IsNil)
	for i := range len(users) {
		user, err := testDB.User(users[i].ID)
		c.Assert(err, qt.IsNil)
		c.Assert(user.ExtraData, qt.Equals, users[i].ExtraData)
		c.Assert(user.Phone, qt.Equals, users[i].Phone)
		c.Assert(user.Mail, qt.Equals, users[i].Mail)
	}
}

func testUsersBulk(n int) []UserData {
	users := make([]UserData, n)
	for i := 0; i < n; i++ {
		users[i] = UserData{
			ID:        []byte(fmt.Sprintf("user%dID", i)),
			Bundles:   map[string]BundleData{},
			ExtraData: fmt.Sprintf("extraData%d", i),
			Phone:     fmt.Sprintf("+346%08d", rand.Int63n(10000000)),
			Mail:      fmt.Sprintf("user%d@test.com", i),
		}
	}
	return users
}

func TestUserAuthToken(t *testing.T) {
	c := qt.New(t)
	defer resetDB(c)
	// get token of a non-existing token
	_, _, err := testDB.UserAuthToken(testToken)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
	// try to add the token to the index of a non-existing user
	err = testDB.IndexAuthToken(testUserID, testUserBundle.ID, testToken)
	c.Assert(err, qt.ErrorIs, ErrUserNotFound)
	// add user with no bundles
	c.Assert(testDB.SetUser(UserData{
		ID:        testUserID,
		Bundles:   map[string]BundleData{},
		ExtraData: testUserExtraData,
		Phone:     testUserPhone,
		Mail:      testUserMail,
	}), qt.IsNil)
	// try to add the token to the index of a non-existing bundle
	err = testDB.IndexAuthToken(testUserID, testUserBundle.ID, testToken)
	c.Assert(err, qt.ErrorIs, ErrBundleNotFound)
	// add bundle
	c.Assert(testDB.SetUserBundle(testUserID, testUserBundle.ID, []internal.HexBytes{testProcessID}...), qt.IsNil)
	// add token
	err = testDB.IndexAuthToken(testUserID, testUserBundle.ID, testToken)
	c.Assert(err, qt.IsNil)
	// get token
	token, userData, err := testDB.UserAuthToken(testToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Token.String(), qt.Equals, testToken.String())
	c.Assert(token.UserID.Bytes(), qt.DeepEquals, testUserID)
	c.Assert(token.BundleID.Bytes(), qt.DeepEquals, testUserBundle.ID.Bytes())
	c.Assert(token.Verified, qt.Equals, false)
	c.Assert(userData.ID.Bytes(), qt.DeepEquals, testUserID)
	c.Assert(userData.Bundles, qt.HasLen, 1)
	c.Assert([]byte(userData.Bundles[testUserBundle.ID.String()].ID), qt.DeepEquals, testUserBundle.ID.Bytes())
	c.Assert(userData.Bundles[testUserBundle.ID.String()].Processes, qt.HasLen, 1)
	c.Assert(userData.Bundles[testUserBundle.ID.String()].Processes[testProcessID.String()], qt.DeepEquals, testUserBundle.Processes[testProcessID.String()])
	c.Assert(userData.Bundles[testUserBundle.ID.String()].LastAttempt, qt.IsNil)
	// verify token
	err = testDB.VerifyAuthToken(testToken)
	c.Assert(err, qt.IsNil)
	// get token
	token, _, err = testDB.UserAuthToken(testToken)
	c.Assert(err, qt.IsNil)
	c.Assert(token.Verified, qt.Equals, true)
	// try to verify a non-existing token
	nonExistingToken := internal.HexBytes(uuid.New().String())
	err = testDB.VerifyAuthToken(nonExistingToken)
	c.Assert(err, qt.ErrorIs, ErrTokenNotFound)
}
