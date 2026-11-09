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
	// the signer seed is written only when the document is created: it takes part in the
	// organization key derivation and is never rewritten
	setOnInsert := bson.M{}
	if set, ok := updateDoc["$set"].(bson.M); ok {
		delete(set, "signerSeed")
		// monotonic, written only by $inc: a stale read written back must not lower it
		delete(set, "managedOrgsCreated")
		// grows only through RecordOrganizationAddOnsDelivered
		delete(set, "addOnsDelivered")
		// written only by OweOrganizationAddOnCredit and SettleOrganizationAddOnCredit
		delete(set, "addOnsOwedCredit")
		// the once-per-organization add-on fields are written only by their conditional methods: a
		// stale read written back would resurrect a released claim or a refunded add-on
		for _, addOn := range OrgAddOns {
			delete(set, addOn.paidAtField())
			delete(set, addOn.claimedByField())
			delete(set, addOn.claimedAtField())
		}
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

// OrgAddOnClaimStaleAfter is how long a claim its payment no longer backs is still respected.
// Paying attempts refresh the claim first, so an older one is abandoned. A var for tests.
var OrgAddOnClaimStaleAfter = 2 * time.Minute

// OrgAddOnClaim names one process's claim on a once-per-organization add-on.
type OrgAddOnClaim struct {
	OrgAddress common.Address
	AddOn      OrgAddOn
	ProcessID  bson.ObjectID
	// Observed is the stale claimant ClaimOrganizationAddOn may take over (zero if none).
	Observed bson.ObjectID
	// ClaimedAt pins ReleaseOrganizationAddOnClaim to the exact claim that was taken.
	ClaimedAt time.Time
	// ClaimedBefore, when set, pins ReleaseOrganizationAddOn to a claim taken strictly earlier.
	ClaimedBefore time.Time
}

// ClaimOrganizationAddOn takes the organization's claim on c.AddOn for c.ProcessID and returns
// the stored claim time, or zero when another process holds it (re-price without the add-on).
// c.Observed is the stale claim the caller judged releasable (zero if none); it is in the filter,
// so a claim taken or refreshed meanwhile is never overwritten. A paid add-on is never claimed
// again.
func (ms *MongoStorage) ClaimOrganizationAddOn(c OrgAddOnClaim) (time.Time, error) {
	if (c.OrgAddress.Cmp(common.Address{}) == 0) || c.ProcessID == bson.NilObjectID || !c.AddOn.IsValid() {
		return time.Time{}, ErrInvalidData
	}
	claimedBy, claimedAt := c.AddOn.claimedByField(), c.AddOn.claimedAtField()
	// unclaimed, already ours (a retry of the same draft), or the releasable claim the
	// caller observed
	allowed := bson.A{
		bson.M{claimedBy: bson.M{"$exists": false}},
		bson.M{claimedBy: c.ProcessID},
	}
	if c.Observed != bson.NilObjectID && c.Observed != c.ProcessID {
		allowed = append(allowed, bson.M{
			claimedBy: c.Observed,
			claimedAt: bson.M{"$lt": time.Now().Add(-OrgAddOnClaimStaleAfter)},
		})
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{
		"_id":                 c.OrgAddress,
		c.AddOn.paidAtField(): bson.M{"$exists": false},
		"$or":                 allowed,
	}
	now := time.Now().Truncate(time.Millisecond)
	res, err := ms.organizations.UpdateOne(ctx, filter, bson.M{"$set": bson.M{claimedBy: c.ProcessID, claimedAt: now}})
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to claim organization %s: %w", c.AddOn, err)
	}
	// MatchedCount: a same-millisecond re-claim modifies nothing but still wins
	if res.MatchedCount != 1 {
		return time.Time{}, nil
	}
	return now, nil
}

// ReleaseOrganizationAddOnClaim removes the claim c.ProcessID took at c.ClaimedAt, abandoned
// before any money moved. Pinned to that exact claim; never touches the paid stamp.
func (ms *MongoStorage) ReleaseOrganizationAddOnClaim(c OrgAddOnClaim) error {
	if (c.OrgAddress.Cmp(common.Address{}) == 0) || c.ProcessID == bson.NilObjectID || c.ClaimedAt.IsZero() ||
		!c.AddOn.IsValid() {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.organizations.UpdateOne(ctx,
		bson.M{
			"_id":                    c.OrgAddress,
			c.AddOn.claimedByField(): c.ProcessID,
			c.AddOn.claimedAtField(): c.ClaimedAt,
			c.AddOn.paidAtField():    bson.M{"$exists": false},
		},
		bson.M{"$unset": bson.M{c.AddOn.claimedByField(): "", c.AddOn.claimedAtField(): ""}})
	if err != nil {
		return fmt.Errorf("failed to release organization %s claim: %w", c.AddOn, err)
	}
	return nil
}

// UpdateOrganizationProfile $sets only the profile fields that are not zero on the existing
// organization with profile's address. Unlike SetOrganization it never upserts, and it leaves
// every other field (counters, add-ons, memberships...) alone, so a write based on
// an earlier read cannot undo changes made since. Returns ErrNotFound if there is no organization
// with that address.
func (ms *MongoStorage) UpdateOrganizationProfile(profile *Organization) error {
	fields := bson.M{}
	if profile.Website != "" {
		fields["website"] = profile.Website
	}
	if profile.Size != "" {
		fields["size"] = profile.Size
	}
	if profile.Color != "" {
		fields["color"] = profile.Color
	}
	if profile.Subdomain != "" {
		fields["subdomain"] = profile.Subdomain
	}
	if profile.Country != "" {
		fields["country"] = profile.Country
	}
	if profile.Timezone != "" {
		fields["timezone"] = profile.Timezone
	}
	if profile.DefaultLang != "" {
		fields["defaultLang"] = profile.DefaultLang
	}
	if profile.Meta != nil {
		fields["meta"] = profile.Meta
	}
	// nothing to write, the organization is left as it is
	if len(fields) == 0 {
		return nil
	}
	return ms.updateOrganizationFields(profile.Address, fields)
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

// ReleaseOrganizationAddOn gives c.AddOn of c.OrgAddress back, so the next process can be
// quoted (and charged) for it: both the claim and the paid stamp are removed. Only the claimant
// releases it — the filter pins the claim to c.ProcessID — so a refund for a process that no
// longer holds the claim leaves another process's add-on alone. A non-zero c.ClaimedBefore also
// pins it to a claim taken strictly earlier: the credit-back that owes the release, so a late
// release never undoes a claim the process took again since (and a payment that followed it). A
// no-op when the claim moved on.
func (ms *MongoStorage) ReleaseOrganizationAddOn(c OrgAddOnClaim) error {
	orgAddress, addOn := c.OrgAddress, c.AddOn
	if (orgAddress.Cmp(common.Address{}) == 0) || c.ProcessID == bson.NilObjectID || !addOn.IsValid() {
		return ErrInvalidData
	}
	filter := bson.M{"_id": orgAddress, addOn.claimedByField(): c.ProcessID}
	if !c.ClaimedBefore.IsZero() {
		filter[addOn.claimedAtField()] = bson.M{"$lt": c.ClaimedBefore}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	_, err := ms.organizations.UpdateOne(ctx, filter,
		bson.M{"$unset": bson.M{addOn.claimedByField(): "", addOn.claimedAtField(): "", addOn.paidAtField(): ""}})
	if err != nil {
		return fmt.Errorf("failed to release organization %s: %w", addOn, err)
	}
	return nil
}

// OweOrganizationAddOnCredit records, in one write, that an organization teardown owes the
// credit-back of the addOn price the withholding credit-back row withheld (its
// TeardownAddOnCreditKey in AddOnsOwedCredit), and removes the organization's stamp and claim on
// addOn when the stamp is that withheld price: set no later than the row. A stamp set since was
// paid by a process of its own and stays. The
// write is pinned to the stamp it read, so a stamp set in between is never cleared (an error: the
// teardown is retried). Once it lands, no process can ride the withheld price, and the credit it
// records is owed whatever the organization's processes receive afterwards.
func (ms *MongoStorage) OweOrganizationAddOnCredit(orgAddress common.Address, addOn OrgAddOn,
	withholding *WalletLedgerEntry,
) error {
	if (orgAddress.Cmp(common.Address{}) == 0) || !addOn.IsValid() || withholding == nil || withholding.ProcessID.IsZero() {
		return ErrInvalidData
	}
	withheldAt := withholding.CreatedAt
	org, err := ms.Organization(orgAddress)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": orgAddress}
	update := bson.M{"$addToSet": bson.M{"addOnsOwedCredit": TeardownAddOnCreditKey(orgAddress, addOn, withholding.ProcessID)}}
	if paidAt := org.AddOnPaidAt(addOn); paidAt.IsZero() {
		filter[addOn.paidAtField()] = bson.M{"$exists": false}
	} else {
		filter[addOn.paidAtField()] = paidAt
		if !paidAt.After(withheldAt) {
			update["$unset"] = bson.M{addOn.claimedByField(): "", addOn.claimedAtField(): "", addOn.paidAtField(): ""}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.organizations.UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("failed to record the organization's owed %s credit: %w", addOn, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("the organization's %s stamp changed while recording its owed credit; retry", addOn)
	}
	return nil
}

// SettleOrganizationAddOnCredit removes the owed credit of the addOn price processID's
// credit-back withheld from the organization's AddOnsOwedCredit once it is applied. Idempotent.
func (ms *MongoStorage) SettleOrganizationAddOnCredit(orgAddress common.Address, addOn OrgAddOn, processID bson.ObjectID) error {
	if (orgAddress.Cmp(common.Address{}) == 0) || !addOn.IsValid() || processID.IsZero() {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": orgAddress},
		bson.M{"$pull": bson.M{"addOnsOwedCredit": TeardownAddOnCreditKey(orgAddress, addOn, processID)}}); err != nil {
		return fmt.Errorf("failed to settle the organization's owed %s credit: %w", addOn, err)
	}
	return nil
}

// RecordOrganizationAddOnsDelivered adds to the organization's AddOnsDelivered every
// once-per-organization add-on a published or mined process of it carries now
// (OrganizationAddOnDelivered), and returns them all, those recorded earlier included. An
// organization teardown calls it before it drops anything, so what it decides from the result
// does not depend on which drafts or published processes a crashed attempt already removed.
func (ms *MongoStorage) RecordOrganizationAddOnsDelivered(orgAddress common.Address) ([]OrgAddOn, error) {
	if (orgAddress.Cmp(common.Address{}) == 0) {
		return nil, ErrInvalidData
	}
	delivered := bson.A{}
	for _, addOn := range OrgAddOns {
		ok, err := ms.OrganizationAddOnDelivered(orgAddress, addOn, bson.NilObjectID)
		if err != nil {
			return nil, err
		}
		if ok {
			delivered = append(delivered, addOn)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	org := &Organization{}
	err := ms.organizations.FindOneAndUpdate(ctx, bson.M{"_id": orgAddress},
		bson.M{"$addToSet": bson.M{"addOnsDelivered": bson.M{"$each": delivered}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).
			SetProjection(bson.M{"addOnsDelivered": 1})).Decode(org)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to record the organization's delivered add-ons: %w", err)
	}
	return org.AddOnsDelivered, nil
}

// SetOrganizationAddOnPaid stamps a once-per-organization add-on as paid once, reporting
// whether this call did.
func (ms *MongoStorage) SetOrganizationAddOnPaid(address common.Address, addOn OrgAddOn, when time.Time) (bool, error) {
	if (address.Cmp(common.Address{}) == 0) || when.IsZero() || !addOn.IsValid() {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": address, addOn.paidAtField(): bson.M{"$exists": false}}
	res, err := ms.organizations.UpdateOne(ctx, filter, bson.M{"$set": bson.M{addOn.paidAtField(): when}})
	if err != nil {
		return false, fmt.Errorf("failed to set organization %s paid: %w", addOn, err)
	}
	return res.MatchedCount == 1, nil
}

// SetOrganizationAddOnPaidForClaim stamps a once-per-organization add-on as paid for the claim
// c names: only while c.ProcessID still holds the exact claim taken at c.ClaimedAt and nothing
// stamped it yet, reporting whether this call did. A stamp derived from that claimant's payment
// is then a no-op once its credit-back released the claim, or once it claimed again since, so it
// never leaves a stamp with no claimant nothing could undo.
func (ms *MongoStorage) SetOrganizationAddOnPaidForClaim(c OrgAddOnClaim) (bool, error) {
	if (c.OrgAddress.Cmp(common.Address{}) == 0) || c.ProcessID == bson.NilObjectID || c.ClaimedAt.IsZero() ||
		!c.AddOn.IsValid() {
		return false, ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.organizations.UpdateOne(ctx,
		bson.M{
			"_id":                    c.OrgAddress,
			c.AddOn.claimedByField(): c.ProcessID,
			c.AddOn.claimedAtField(): c.ClaimedAt,
			c.AddOn.paidAtField():    bson.M{"$exists": false},
		},
		bson.M{"$set": bson.M{c.AddOn.paidAtField(): time.Now()}})
	if err != nil {
		return false, fmt.Errorf("failed to set organization %s paid: %w", c.AddOn, err)
	}
	return res.MatchedCount == 1, nil
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

// IncrementManagedOrgsCreated counts one more managed organization created by the integrator at
// address. Never decremented: the unfunded integrator cap counts creations, not survivors.
func (ms *MongoStorage) IncrementManagedOrgsCreated(address common.Address) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.organizations.UpdateOne(ctx, bson.M{"_id": address}, bson.M{"$inc": bson.M{"managedOrgsCreated": 1}})
	if err != nil {
		return fmt.Errorf("failed to count the managed organization created: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ListManagedOrganizations returns a paginated list of organizations managed by
// the integrator at the given address.
func (ms *MongoStorage) ListManagedOrganizations(
	managerAddress common.Address, page, limit int64,
) (int64, []Organization, error) {
	filter := bson.M{"managedBy": managerAddress}
	return paginatedDocuments[Organization](ms.organizations, page, limit, filter, options.Find())
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

// ManagedCounters is an integrator's usage summed across all of its managed organizations:
// how many it manages, votes relayed and 2FA SMS/emails sent. bson tags match the $group
// output of SumManagedCounters.
type ManagedCounters struct {
	ManagedOrgs int `bson:"managedOrgs"`
	SentVotes   int `bson:"sentVotes"`
	SentSMS     int `bson:"sentSMS"`
	SentEmails  int `bson:"sentEmails"`
}

// SumManagedCounters returns the managed organization count and their vote/SMS/email totals
// in a single aggregation, so the /integrator dashboard reads them in one round trip.
//
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
			"_id":         nil,
			"managedOrgs": bson.M{"$sum": 1},
			"sentVotes":   bson.M{"$sum": "$counters.sentVotes"},
			"sentSMS":     bson.M{"$sum": "$counters.sentSMS"},
			"sentEmails":  bson.M{"$sum": "$counters.sentEmails"},
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
