package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

func (ms *MongoStorage) fetchOrganizationFromDB(ctx context.Context, address common.Address) (*Organization, error) {
	// find the organization in the database by its address
	filter := bson.M{"_id": address}
	result := ms.organizations.FindOne(ctx, filter)
	org := &Organization{}
	if err := result.Decode(org); err != nil {
		// if the organization doesn't exist return a specific error
		if err == mongo.ErrNoDocuments {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// initialize Meta map if it's nil to prevent nil map assignment errors
	if org.Meta == nil {
		org.Meta = make(map[string]any)
	}
	return org, nil
}

// Organization method returns the organization with the given address.
// If the organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) Organization(address common.Address) (*Organization, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	org, err := ms.fetchOrganizationFromDB(ctx, address)
	if err != nil {
		return nil, err
	}
	return org, nil
}

// OrganizationWithParent method returns the organization with the given address
// and its parent organization if it exists. If the organization doesn't exist
// or the parent organization doesn't exist, it returns the specific error.
// If other errors occur, it returns the error.
func (ms *MongoStorage) OrganizationWithParent(address common.Address) (org *Organization, parent *Organization, err error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	org, err = ms.fetchOrganizationFromDB(ctx, address)
	if err != nil {
		return nil, nil, err
	}
	if org.Parent.Cmp(common.Address{}) == 0 {
		return org, nil, nil
	}
	// find the parent organization in the database
	parent, err = ms.fetchOrganizationFromDB(ctx, org.Parent)
	if err != nil {
		return nil, nil, err
	}
	return org, parent, nil
}

// SetOrganization method creates or updates the organization in the database.
// If the organization already exists, it updates the fields that have changed.
// If the organization doesn't exist, it creates it. If an error occurs, it
// returns the error.
//
// The subscription is only written when the organization is created. Callers
// read-modify-write the whole organization, so a write of an existing one would
// put back the subscription it read, undoing any change the billing webhooks made
// in between. Subscription changes go through SetOrganizationSubscription,
// UpdateOrganizationSubscription and SetOrganizationLastPaymentDate.
func (ms *MongoStorage) SetOrganization(org *Organization) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// prepare the document to be updated in the database modifying only the
	// fields that have changed.
	// ponytail: zero values are not persisted — no field is force-updated, so a bool can only go
	// false->true and a string can never be cleared. Force-updating is what made #625 possible (a
	// PUT omitting `active` clobbered it), so a field that must reach its zero value needs its own
	// $set here plus a pointer in the request type, not an entry in alwaysUpdateTags.
	updateDoc, err := dynamicUpdateDocument(org, nil)
	if err != nil {
		return err
	}
	// fields written only when the document is created: the subscription (see above) and the
	// signer seed, which takes part in the organization key derivation and is never rewritten.
	// Both go into one $setOnInsert map so neither can overwrite the other.
	setOnInsert := bson.M{}
	if set, ok := updateDoc["$set"].(bson.M); ok {
		if subscription, ok := set["subscription"]; ok {
			delete(set, "subscription")
			setOnInsert["subscription"] = subscription
		}
		delete(set, "signerSeed")
	}
	if org.SignerSeed != "" {
		setOnInsert["signerSeed"] = org.SignerSeed
	}
	if len(setOnInsert) > 0 {
		updateDoc["$setOnInsert"] = setOnInsert
	}
	// set upsert to true to create the document if it doesn't exist
	opts := options.UpdateOne().SetUpsert(true)
	res, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": org.Address}, updateDoc, opts)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrAlreadyExists
		}
		return err
	}
	// grant the creator the admin role only when the organization was just created. Updates
	// must not touch memberships, so they cannot re-admin a creator whose role was changed or
	// removed, and a failed grant never deletes an organization that already existed.
	if res.UpsertedCount > 0 && org.Creator != "" {
		if err := ms.addOrganizationToUser(ctx, org.Creator, org.Address, AdminRole); err != nil {
			// roll back the freshly created organization so creation stays all-or-nothing
			if _, delErr := ms.organizations.DeleteOne(ctx, bson.M{"_id": org.Address}); delErr != nil {
				return errors.Join(err, delErr)
			}
			return err
		}
	}
	return nil
}

// DelOrganization method deletes the organization from the database. If an
// error occurs, it returns the error.
func (ms *MongoStorage) DelOrganization(org *Organization) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// delete the organization from the database
	_, err := ms.organizations.DeleteOne(ctx, bson.M{"_id": org.Address})
	return err
}

// ReplaceCreatorEmail method replaces the creator email in the organizations
// where it is the creator. If an error occurs, it returns the error.
func (ms *MongoStorage) ReplaceCreatorEmail(oldEmail, newEmail string) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	// update the creator email in the organizations where it is the creator. The organization
	// signer is derived from signerSeed, falling back to the creator for rows the backfill
	// migration has not stamped (see Organization.SignerSeedValue). Rewriting the creator of
	// such a row would silently re-derive a different key and strand its on-chain account, so
	// the same update first pins the seed to the current (old) creator when it is missing.
	// Within a pipeline $set stage, "$creator" still reads the value before this update.
	updateDoc := mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"signerSeed": bson.M{"$cond": bson.A{
			bson.M{"$in": bson.A{bson.M{"$ifNull": bson.A{"$signerSeed", ""}}, bson.A{""}}},
			"$creator",
			"$signerSeed",
		}},
		// $literal: inside a pipeline a string starting with "$" would be read as a field path
		"creator": bson.M{"$literal": newEmail},
	}}}}
	if _, err := ms.organizations.UpdateMany(ctx, bson.M{"creator": oldEmail}, updateDoc); err != nil {
		return fmt.Errorf("could not replace organization creator email: %w", err)
	}
	return nil
}

// OrganizationUsers method returns the users that have a role in the
// organization with the given address. If an error occurs, it returns the
// error.
func (ms *MongoStorage) OrganizationUsers(address common.Address) ([]User, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the organization in the database
	filter := bson.M{
		"organizations": bson.M{
			"$elemMatch": bson.M{
				"_id": address,
			},
		},
	}
	users := []User{}
	cursor, err := ms.users.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := cursor.Close(ctx); err != nil {
			log.Warnw("error closing cursor", "error", err)
		}
	}()
	if err := cursor.All(ctx, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// UpdateOrganizationUserRole method updates the role of the user in the
// organization with the given address.
func (ms *MongoStorage) UpdateOrganizationUserRole(address common.Address, userID uint64, newRole UserRole) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"_id":               userID,
		"organizations._id": address,
	}

	update := bson.M{
		"$set": bson.M{
			"organizations.$.role": newRole,
		},
	}

	_, err := ms.users.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	return nil
}

// RemoveOrganizationUser removes the organization from the user's organizations. It reports
// whether the user was a member, so callers only update membership counters for real removals.
func (ms *MongoStorage) RemoveOrganizationUser(address common.Address, userID uint64) (bool, error) {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":               userID,
		"organizations._id": address,
	}
	update := bson.M{
		"$pull": bson.M{
			"organizations": bson.M{
				"_id": address,
			},
		},
	}
	res, err := ms.users.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// RemoveOrganizationFromAllUsers unlinks the given organization from every user that is a
// member of it, by pulling the organization entry out of each user's organizations list.
// It generalizes RemoveOrganizationUser (which targets a single user) and is used when
// tearing down an organization so no user keeps a dangling role reference to it.
func (ms *MongoStorage) RemoveOrganizationFromAllUsers(address common.Address) error {
	if address.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	filter := bson.M{"organizations._id": address}
	update := bson.M{
		"$pull": bson.M{
			"organizations": bson.M{
				"_id": address,
			},
		},
	}
	if _, err := ms.users.UpdateMany(ctx, filter, update); err != nil {
		return fmt.Errorf("failed to unlink organization from users: %w", err)
	}
	return nil
}

// SetOrganizationSubscription method adds the provided subscription to
// the organization with the given address
func (ms *MongoStorage) SetOrganizationSubscription(address common.Address, orgSubscription *OrganizationSubscription) error {
	if _, err := ms.Plan(orgSubscription.PlanID); err != nil {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// prepare the document to be updated in the database
	filter := bson.M{"_id": address}
	updateDoc := bson.M{"$set": bson.M{"subscription": orgSubscription}}
	// update the organization in the database
	if _, err := ms.organizations.UpdateOne(ctx, filter, updateDoc); err != nil {
		return err
	}
	return nil
}

// UpdateOrganizationSubscription sets every field of the organization's subscription
// except lastPaymentDate, which has its own writer (SetOrganizationLastPaymentDate), so
// the subscription and payment webhooks never overwrite each other's field.
func (ms *MongoStorage) UpdateOrganizationSubscription(address common.Address, sub *OrganizationSubscription) error {
	if _, err := ms.Plan(sub.PlanID); err != nil {
		return ErrInvalidData
	}
	return ms.updateOrganizationFields(address, bson.M{
		"subscription.planID":               sub.PlanID,
		"subscription.stripeSubscriptionID": sub.StripeSubscriptionID,
		"subscription.billingPeriod":        sub.BillingPeriod,
		"subscription.startDate":            sub.StartDate,
		"subscription.renewalDate":          sub.RenewalDate,
		"subscription.active":               sub.Active,
		"subscription.email":                sub.Email,
	})
}

// SetOrganizationLastPaymentDate sets only the last payment date of the organization's
// subscription.
func (ms *MongoStorage) SetOrganizationLastPaymentDate(address common.Address, date time.Time) error {
	return ms.updateOrganizationFields(address, bson.M{"subscription.lastPaymentDate": date})
}

// updateOrganizationFields $sets the given fields of an existing organization, returning
// ErrNotFound if there is no organization with that address.
func (ms *MongoStorage) updateOrganizationFields(address common.Address, fields bson.M) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": address}, bson.M{"$set": fields})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// SetOrganizationMeta method sets the metadata for the organization with the
// given address. If the organization doesn't exist, it returns an error.
func (ms *MongoStorage) AddOrganizationMeta(address common.Address, meta map[string]any) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// prepare the document to be updated in the database
	filter := bson.M{"_id": address}
	updateDoc := bson.M{"$set": bson.M{"meta": meta}}
	// update the organization in the database
	if _, err := ms.organizations.UpdateOne(ctx, filter, updateDoc); err != nil {
		return err
	}
	return nil
}

// ReplaceOrganizationMetaKeeping replaces the 'meta' object of an organization with meta, except for
// the keepKeys: those keep their stored values, read by the update itself so a concurrent write to
// them is not lost.
func (ms *MongoStorage) ReplaceOrganizationMetaKeeping(address common.Address, meta map[string]any, keepKeys []string) error {
	replaced := bson.M{}
	for k, v := range meta {
		if !slices.Contains(keepKeys, k) {
			replaced[k] = v
		}
	}
	keep := bson.M{}
	for _, k := range keepKeys {
		keep[k] = "$meta." + k
	}
	// a stored field that is missing is left out of the keep object, so it stays missing; $literal
	// stops user values starting with "$" from being read as field paths
	merged := bson.M{"$mergeObjects": bson.A{bson.M{"$literal": replaced}, keep}}
	pipeline := bson.A{bson.M{"$set": bson.M{"meta": merged}}}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if _, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": address}, pipeline); err != nil {
		return fmt.Errorf("failed to replace meta: %w", err)
	}
	return nil
}

// UpdateOrganizationMeta updates individual keys in the 'meta' subdocument
// Has only one layer o depth, if a second layer document is provided, for example meta.doc = [a,b,c] it will
// all the docment will be updated
func (ms *MongoStorage) UpdateOrganizationMeta(address common.Address, metaUpdates map[string]any) error {
	updateFields := bson.M{}

	// Construct dot notation keys like "meta.region": "EU"
	for k, v := range metaUpdates {
		updateFields["meta."+k] = v
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": address}
	update := bson.M{"$set": updateFields}

	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		if isMetaPathError(err) {
			return fmt.Errorf("%w: %w", ErrInvalidMetaPath, err)
		}
		return fmt.Errorf("failed to update eta: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// isMetaPathError reports whether MongoDB rejected a meta update path: PathNotViable (28) when a
// sub-key is written into a value that is not an object, ConflictingUpdateOperators (40) when two
// paths overlap.
func isMetaPathError(err error) bool {
	var srvErr mongo.ServerError
	return errors.As(err, &srvErr) && (srvErr.HasErrorCode(28) || srvErr.HasErrorCode(40))
}

// DeleteOrganizationMetaKeys deletes specific keys from the 'meta' object of a given Organization
func (ms *MongoStorage) DeleteOrganizationMetaKeys(address common.Address, keysToDelete []string) error {
	unsetFields := bson.M{}

	// Build the unset document: { "meta.key1": 1, "meta.key2": 1 }
	// In MongoDB, the value in $unset doesn't matter, but it's conventional to use 1
	for _, key := range keysToDelete {
		unsetFields["meta."+key] = 1
	}

	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": address}
	update := bson.M{"$unset": unsetFields}

	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		if isMetaPathError(err) {
			return fmt.Errorf("%w: %w", ErrInvalidMetaPath, err)
		}
		return fmt.Errorf("failed to unset meta fields: %w", err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no organization found with address: %s", address)
	}

	return nil
}

// IncrementOrganizationUsersCounter atomically increments the users counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationUsersCounter(address common.Address) error {
	// we acquire a lock to check the counter limit without racing with other calls to increment this same counter.
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	org, plan, err := ms.fetchOrganizationAndPlan(address)
	if err != nil {
		return fmt.Errorf("could not get organization: %v", err)
	}

	if org.Counters.Users >= plan.Organization.Users {
		return fmt.Errorf("%w (%d >= %d)", ErrMaxUsersReached, org.Counters.Users, plan.Organization.Users)
	}

	return ms.addToOrganizationCounter(address, "users", 1)
}

// DecrementOrganizationUsersCounter atomically decrements the users counter for the organization with the given address.
func (ms *MongoStorage) DecrementOrganizationUsersCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "users", -1)
}

// IncrementOrganizationProcessesCounter atomically increments the processes counter for the organization with the
// given address. The plan limit is enforced upstream by HasTxPermission; this uses an atomic $inc so concurrent
// publishes cannot lose an update.
func (ms *MongoStorage) IncrementOrganizationProcessesCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "processes", 1)
}

// IncrementOrganizationSubOrgsCounter atomically increments the suborgs counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationSubOrgsCounter(address common.Address) error {
	// we acquire a lock to check the counter limit without racing with other calls to increment this same counter.
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	org, plan, err := ms.fetchOrganizationAndPlan(address)
	if err != nil {
		return fmt.Errorf("could not get organization: %v", err)
	}

	if org.Counters.SubOrgs >= plan.Organization.SubOrgs {
		return fmt.Errorf("%w (%d >= %d)", ErrMaxSubOrgsReached, org.Counters.SubOrgs, plan.Organization.SubOrgs)
	}

	return ms.addToOrganizationCounter(address, "subOrgs", 1)
}

// DecrementOrganizationSubOrgsCounter atomically decrements the suborgs counter for the organization with the given address.
func (ms *MongoStorage) DecrementOrganizationSubOrgsCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "subOrgs", -1)
}

// IncrementOrganizationSentEmailsCounter atomically increments the emails counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationSentEmailsCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "sentEmails", 1)
}

// IncrementOrganizationSentSMSCounter atomically increments the SMS counter for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationSentSMSCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "sentSMS", 1)
}

// IncrementOrganizationSentVotesCounter atomically increments the relayed-votes counter
// for the organization with the given address.
func (ms *MongoStorage) IncrementOrganizationSentVotesCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "sentVotes", 1)
}

// IncrementOrganizationManagedOrgsCounterWithLimit atomically increments the managed
// organizations counter only if it stays within limit, re-reading the counter under
// keysLock so two concurrent creates cannot both pass a stale check and exceed the cap
// (each managed org provisions a faucet-funded on-chain account, so over-provisioning is
// a real cost vector). Returns ErrManagedQuotaReached when the limit is already reached.
func (ms *MongoStorage) IncrementOrganizationManagedOrgsCounterWithLimit(address common.Address, limit int) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()

	org, err := ms.Organization(address)
	if err != nil {
		return fmt.Errorf("could not get organization: %w", err)
	}
	if org.Counters.ManagedOrgs >= limit {
		return ErrManagedQuotaReached
	}
	return ms.addToOrganizationCounter(address, "managedOrgs", 1)
}

// DecrementOrganizationManagedOrgsCounter atomically decrements the managed organizations counter.
func (ms *MongoStorage) DecrementOrganizationManagedOrgsCounter(address common.Address) error {
	return ms.addToOrganizationCounter(address, "managedOrgs", -1)
}

// ReserveManagedPublish atomically reserves one managed process slot for the integrator:
// the quota check is part of the increment's filter, so concurrent publishes — also from
// different service replicas — cannot each pass a stale check and exceed the integrator's
// aggregate process quota. Returns ErrManagedQuotaReached if the limit would be exceeded.
// Roll back with AddOrganizationManagedProcesses(-1) if the publish later fails.
func (ms *MongoStorage) ReserveManagedPublish(address common.Address, processLimit int) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{
		"_id": address,
		// $not also matches a missing counter, unlike $lt
		"counters.managedProcesses": bson.M{"$not": bson.M{"$gte": processLimit}},
	}
	update := bson.M{"$inc": bson.M{"counters.managedProcesses": 1}}
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("could not reserve managed publish slot: %w", err)
	}
	if result.MatchedCount == 0 {
		// either the organization does not exist or its quota is already full
		if _, err := ms.Organization(address); err != nil {
			return fmt.Errorf("could not get organization: %w", err)
		}
		return ErrManagedQuotaReached
	}
	return nil
}

// AddOrganizationManagedProcesses atomically adds delta to the managed processes counter.
func (ms *MongoStorage) AddOrganizationManagedProcesses(address common.Address, delta int64) error {
	return ms.addToOrganizationCounter(address, "managedProcesses", delta)
}

// ListManagedOrganizations returns a paginated list of organizations managed by
// the integrator at the given address.
func (ms *MongoStorage) ListManagedOrganizations(
	managerAddress common.Address, page, limit int64,
) (int64, []Organization, error) {
	filter := bson.M{"managedBy": managerAddress}
	return paginatedDocuments[Organization](ms.organizations, page, limit, filter, options.Find())
}

// managedOrgAddresses returns the addresses of all organizations managed by
// integratorAddr, projected to just their _id to keep the read lightweight.
func (ms *MongoStorage) managedOrgAddresses(ctx context.Context, integratorAddr common.Address) ([]common.Address, error) {
	if integratorAddr.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	filter := bson.M{"managedBy": integratorAddr}
	opts := options.Find().SetProjection(bson.M{"_id": 1})
	cursor, err := ms.organizations.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("could not list managed organizations: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var orgs []Organization
	if err := cursor.All(ctx, &orgs); err != nil {
		return nil, fmt.Errorf("could not decode managed organizations: %w", err)
	}
	addrs := make([]common.Address, len(orgs))
	for i := range orgs {
		addrs[i] = orgs[i].Address
	}
	return addrs, nil
}

// OrgAddressesManagedBy returns, out of the candidate addresses, those that belong to an
// organization managed by owner, projected to just their _id to keep the read lightweight.
// Used to confine an API key's reach to its own organization and the ones it manages.
func (ms *MongoStorage) OrgAddressesManagedBy(owner common.Address, candidates []common.Address) ([]common.Address, error) {
	if owner.Cmp(common.Address{}) == 0 || len(candidates) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": bson.M{"$in": candidates}, "managedBy": owner}
	opts := options.Find().SetProjection(bson.M{"_id": 1})
	cursor, err := ms.organizations.Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("could not list managed organizations: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var orgs []Organization
	if err := cursor.All(ctx, &orgs); err != nil {
		return nil, fmt.Errorf("could not decode managed organizations: %w", err)
	}
	addrs := make([]common.Address, len(orgs))
	for i := range orgs {
		addrs[i] = orgs[i].Address
	}
	return addrs, nil
}

// sumManagedCounter returns the sum of the given counters subfield (e.g. "sentEmails")
// across all organizations managed by integratorAddr, computed server-side so only the
// total crosses the wire.
func (ms *MongoStorage) sumManagedCounter(integratorAddr common.Address, field string) (int, error) {
	if integratorAddr.Cmp(common.Address{}) == 0 {
		return 0, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"managedBy": integratorAddr}}},
		{{Key: "$group", Value: bson.M{"_id": nil, "total": bson.M{"$sum": "$counters." + field}}}},
	}
	cursor, err := ms.organizations.Aggregate(ctx, pipeline)
	if err != nil {
		return 0, fmt.Errorf("could not aggregate managed counter %q: %w", field, err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []struct {
		Total int `bson:"total"`
	}
	if err := cursor.All(ctx, &results); err != nil {
		return 0, fmt.Errorf("could not decode managed counter %q: %w", field, err)
	}
	if len(results) == 0 {
		return 0, nil
	}
	return results[0].Total, nil
}

// CountMembersManagedBy returns the total org-member count across all organizations
// managed by integratorAddr. It is the shared-pool denominator for the member limit
// of an integrator's managed orgs.
func (ms *MongoStorage) CountMembersManagedBy(integratorAddr common.Address) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	addrs, err := ms.managedOrgAddresses(ctx, integratorAddr)
	if err != nil {
		return 0, err
	}
	if len(addrs) == 0 {
		return 0, nil
	}
	// single query over all managed orgs rather than one CountOrgMembers per org
	return ms.orgMembers.CountDocuments(ctx, bson.M{"orgAddress": bson.M{"$in": addrs}}) //nolint:goconst
}

// SumSentEmailsManagedBy returns the total 2FA emails sent across all organizations
// managed by integratorAddr (the integrator's shared 2FA-email pool consumption).
func (ms *MongoStorage) SumSentEmailsManagedBy(integratorAddr common.Address) (int, error) {
	return ms.sumManagedCounter(integratorAddr, "sentEmails")
}

// SumSentSMSManagedBy returns the total 2FA SMS sent across all organizations managed
// by integratorAddr (the integrator's shared 2FA-SMS pool consumption).
func (ms *MongoStorage) SumSentSMSManagedBy(integratorAddr common.Address) (int, error) {
	return ms.sumManagedCounter(integratorAddr, "sentSMS")
}

// SumSentVotesManagedBy returns the total votes relayed across all organizations managed
// by integratorAddr (the integrator's shared vote pool consumption).
func (ms *MongoStorage) SumSentVotesManagedBy(integratorAddr common.Address) (int, error) {
	return ms.sumManagedCounter(integratorAddr, "sentVotes")
}

// ManagedCounters is an integrator's shared-pool usage summed across all of its managed
// organizations: votes relayed and 2FA SMS/emails sent. bson tags match the $group output
// of SumManagedCounters.
type ManagedCounters struct {
	SentVotes  int `bson:"sentVotes"`
	SentSMS    int `bson:"sentSMS"`
	SentEmails int `bson:"sentEmails"`
}

// SumManagedCounters returns the vote/SMS/email totals across all organizations managed by
// integratorAddr in a single aggregation, so the frequently-hit /integrator dashboard reads
// the whole shared-pool usage in one round trip instead of three separate sumManagedCounter
// calls.
//
// one $match+$group over the managedBy set the other integrator paths already scan.
// If /integrator ever gets hot, denormalise these onto the integrator's own Counters at
// increment time and drop the aggregation.
func (ms *MongoStorage) SumManagedCounters(integratorAddr common.Address) (ManagedCounters, error) {
	if integratorAddr.Cmp(common.Address{}) == 0 {
		return ManagedCounters{}, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"managedBy": integratorAddr}}},
		{{Key: "$group", Value: bson.M{
			"_id":        nil,
			"sentVotes":  bson.M{"$sum": "$counters.sentVotes"},
			"sentSMS":    bson.M{"$sum": "$counters.sentSMS"},
			"sentEmails": bson.M{"$sum": "$counters.sentEmails"},
		}}},
	}
	cursor, err := ms.organizations.Aggregate(ctx, pipeline)
	if err != nil {
		return ManagedCounters{}, fmt.Errorf("could not aggregate managed counters: %w", err)
	}
	defer func() { _ = cursor.Close(ctx) }()

	var results []ManagedCounters
	if err := cursor.All(ctx, &results); err != nil {
		return ManagedCounters{}, fmt.Errorf("could not decode managed counters: %w", err)
	}
	if len(results) == 0 {
		return ManagedCounters{}, nil
	}
	return results[0], nil
}

func (ms *MongoStorage) fetchOrganizationAndPlan(orgAddress common.Address) (*Organization, *Plan, error) {
	org, err := ms.Organization(orgAddress)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get organization: %v", err)
	}
	if org.Subscription.PlanID == "" {
		return nil, nil, fmt.Errorf("organization has no subscription plan")
	}
	plan, err := ms.Plan(org.Subscription.PlanID)
	if err != nil {
		return nil, nil, fmt.Errorf("could not get subscription plan: %v", err)
	}
	return org, plan, nil
}

// addToOrganizationCounter atomically increments (or decrements) a counter by delta.
// Pass delta=1 to increment, delta=-1 to decrement, etc.
func (ms *MongoStorage) addToOrganizationCounter(orgAddress common.Address, counter string, delta int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	filter := bson.M{"_id": orgAddress}
	field := strings.Join([]string{"counters", counter}, ".")
	update := bson.M{"$inc": bson.M{field: delta}}
	result, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to add %d to counter %q: %w", delta, counter, err)
	}

	if result.MatchedCount == 0 {
		return fmt.Errorf("no counter matched %s", orgAddress)
	}
	return nil
}
