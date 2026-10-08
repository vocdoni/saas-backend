package db

import (
	"context"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// nextUserID internal method returns the next available user ID by atomically
// incrementing a counter document, so concurrent inserts — including inserts
// from different service replicas — can never be handed the same ID. The
// counter is lazily seeded from the current highest user ID the first time it
// is needed (fresh database, or a wiped counters collection).
func (ms *MongoStorage) nextUserID(ctx context.Context) (uint64, error) {
	update := bson.M{"$inc": bson.M{"seq": int64(1)}}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	for {
		counter := struct {
			Seq int64 `bson:"seq"`
		}{}
		err := ms.counters.FindOneAndUpdate(ctx, bson.M{"_id": "users"}, update, opts).Decode(&counter)
		if err == nil {
			return uint64(counter.Seq), nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return 0, err
		}
		// no counter yet: seed it from the current highest user ID. A duplicate-key
		// error means another instance seeded it concurrently; retry the increment.
		var lastUser User
		findOpts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
		err = ms.users.FindOne(ctx, bson.M{}, findOpts).Decode(&lastUser)
		if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
			return 0, err
		}
		next := lastUser.ID + 1
		if _, err := ms.counters.InsertOne(ctx, bson.M{"_id": "users", "seq": next}); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				continue
			}
			return 0, err
		}
		return next, nil
	}
}

// addOrganizationToUser internal method adds the organization to the user with
// the given email. If an error occurs, it returns the error. This method must
// be called with the keysLock held.
func (ms *MongoStorage) addOrganizationToUser(ctx context.Context,
	userEmail string, address common.Address, role UserRole,
) error {
	// check if the user exists after add the organization
	filter := bson.M{"email": userEmail}
	count, err := ms.users.CountDocuments(ctx, filter)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return ErrNotFound
		}
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	// add the organization to the user
	updateDoc := bson.M{
		"$addToSet": bson.M{
			"organizations": OrganizationUser{
				Address: address,
				Role:    role,
			},
		},
	}
	if _, err := ms.users.UpdateOne(ctx, filter, updateDoc); err != nil {
		log.Warnw("error adding organization to user", "error", err)
		return err
	}
	return nil
}

func (ms *MongoStorage) fetchUserFromDB(ctx context.Context, id uint64) (*User, error) {
	// find the user in the database
	result := ms.users.FindOne(ctx, bson.M{"_id": id})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

// User method returns the user with the given ID. If the user doesn't exist, it
// returns a specific error. If other errors occur, it returns the error.
func (ms *MongoStorage) User(id uint64) (*User, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.fetchUserFromDB(ctx, id)
}

// UserByEmail method returns the user with the given email. If the user doesn't
// exist, it returns a specific error. If other errors occur, it returns the
// error.
func (ms *MongoStorage) UserByEmail(email string) (*User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	result := ms.users.FindOne(ctx, bson.M{"email": email})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

// UserByOAuthProviderExternalID returns the user that has the given OAuth provider and external ID.
// If no user is found, it returns ErrNotFound.
func (ms *MongoStorage) UserByOAuthProviderExternalID(provider, externalID string) (*User, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	fieldName := "oauth." + provider + ".externalID"
	result := ms.users.FindOne(ctx, bson.M{fieldName: externalID})
	user := &User{}
	if err := result.Decode(user); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return user, nil
}

// SetUser method creates or updates the user in the database. If the user
// already exists, it updates the fields that have changed. If the user doesn't
// exist, it creates it. If an error occurs, it returns the error.
func (ms *MongoStorage) SetUser(user *User) (uint64, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// if the user provided doesn't have organizations, create an empty slice
	if user.Organizations == nil {
		user.Organizations = []OrganizationUser{}
	}
	// check if the user exists or needs to be created
	if user.ID > 0 {
		// if the user exists, update it with the new data
		updateDoc, err := dynamicUpdateDocument(user, nil)
		if err != nil {
			return 0, err
		}
		// never write the session version from a snapshot: it is bumped atomically by the
		// credential-update methods, and a stale copy here could roll a revocation back
		if set, ok := updateDoc["$set"].(bson.M); ok {
			delete(set, "sessionVersion")
		}
		result, err := ms.users.UpdateOne(ctx, bson.M{"_id": user.ID}, updateDoc)
		if err != nil {
			return 0, err
		}
		if result.MatchedCount == 0 {
			// updating a user that does not exist
			return 0, ErrInvalidData
		}
	} else {
		// if the user doesn't exist, create it consuming the next ID from the
		// atomic counter first
		nextID, err := ms.nextUserID(ctx)
		if err != nil {
			return 0, err
		}
		user.ID = nextID
		if _, err := ms.users.InsertOne(ctx, user); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return 0, ErrAlreadyExists
			}
			return 0, err
		}
	}
	return user.ID, nil
}

// updateUserFields private method applies the given update document to the user
// with the given ID, returning ErrNotFound when the user does not exist. It must
// be called with the keysLock held.
func (ms *MongoStorage) updateUserFields(ctx context.Context, userID uint64, update bson.M) error {
	res, err := ms.users.UpdateOne(ctx, bson.M{"_id": userID}, update)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrAlreadyExists
		}
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserProfile method updates only the profile fields of the user with the
// given ID: non-empty first and last names are written with a field-specific
// $set, so a stale user snapshot can never overwrite anything else (memberships,
// password, email). If both names are empty it does nothing.
func (ms *MongoStorage) UpdateUserProfile(userID uint64, firstName, lastName string) error {
	set := bson.M{}
	if firstName != "" {
		set["firstName"] = firstName
	}
	if lastName != "" {
		set["lastName"] = lastName
	}
	if len(set) == 0 {
		return nil
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.updateUserFields(ctx, userID, bson.M{"$set": set})
}

// UpdateUserPassword method sets the password of the user with the given ID and,
// in the same atomic update, bumps the session version so every existing JWT
// session is revoked. Nothing else on the user document is touched.
func (ms *MongoStorage) UpdateUserPassword(userID uint64, hashedPassword string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.updateUserFields(ctx, userID, bson.M{
		"$set": bson.M{"password": hashedPassword},
		"$inc": bson.M{"sessionVersion": 1},
	})
}

// UpdateUserEmail method sets the email of the user with the given ID and, in the
// same atomic update, bumps the session version so every existing JWT session is
// revoked. It returns ErrAlreadyExists when the email is already taken (unique
// index) and ErrNotFound when the user does not exist.
func (ms *MongoStorage) UpdateUserEmail(userID uint64, email string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.updateUserFields(ctx, userID, bson.M{
		"$set": bson.M{"email": email},
		"$inc": bson.M{"sessionVersion": 1},
	})
}

// SetUserOAuthProvider method sets (links or refreshes) a single OAuth provider
// entry of the user with the given ID, without writing any other field, so a
// stale user snapshot can never overwrite concurrent changes.
func (ms *MongoStorage) SetUserOAuthProvider(userID uint64, provider string, p OAuthProvider) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.updateUserFields(ctx, userID, bson.M{"$set": bson.M{"oauth." + provider: p}})
}

// DeleteUserOAuthProvider method unlinks a single OAuth provider from the user
// with the given ID, without writing any other field.
func (ms *MongoStorage) DeleteUserOAuthProvider(userID uint64, provider string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	return ms.updateUserFields(ctx, userID, bson.M{"$unset": bson.M{"oauth." + provider: ""}})
}

// DelUser method deletes the user from the database. If an error occurs, it
// returns the error.
func (ms *MongoStorage) DelUser(user *User) error {
	// check if the user is valid (has an ID or an email)
	if user.ID == 0 && user.Email == "" {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// delete the user from the database using the ID or the email
	filter := bson.M{"_id": user.ID}
	if user.ID == 0 {
		filter = bson.M{"email": user.Email}
	}
	_, err := ms.users.DeleteOne(ctx, filter)
	return err
}

// VerifyUserAccount method verifies the user provided, modifying the user to
// mark as verified and removing the verification code. If an error occurs, it
// returns the error.
func (ms *MongoStorage) VerifyUserAccount(user *User) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// try to get the user to ensure it exists
	if _, err := ms.fetchUserFromDB(ctx, user.ID); err != nil {
		return err
	}
	// update the user to mark as verified
	filter := bson.M{"_id": user.ID}
	if _, err := ms.users.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"verified": true}}); err != nil {
		return err
	}
	// remove the verification code
	return ms.delVerificationCode(ctx, user.ID, CodeTypeVerifyAccount)
}

// UserHasRoleInOrg method checks if the user with the given email has a specific role in the
// organization with the given address. If the user has the role, it
// returns true. If the user doesn't have that role, it returns false. If an error
// occurs, it returns the error.
func (ms *MongoStorage) UserHasRoleInOrg(userEmail string, organizationAddress common.Address, role UserRole) (bool, error) {
	user, err := ms.UserByEmail(userEmail)
	if err != nil {
		return false, err
	}
	for _, org := range user.Organizations {
		if org.Address == organizationAddress {
			return org.Role == role, nil
		}
	}
	return false, ErrNotFound
}

// UserHasAnyRoleInOrg method checks if the user with the given email has any role in the
// organization with the given address. If the user has any role, it
// returns true. If the user doesn't have any role, it returns false. If an error
// occurs, it returns the error.
func (ms *MongoStorage) UserHasAnyRoleInOrg(userEmail string, organizationAddress common.Address) (bool, error) {
	user, err := ms.UserByEmail(userEmail)
	if err != nil {
		return false, err
	}
	for _, org := range user.Organizations {
		if org.Address == organizationAddress {
			return true, nil
		}
	}
	return false, nil
}
