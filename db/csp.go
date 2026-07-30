package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// CSPAuth represents a user authentication information for a bundle of processes
type CSPAuth struct {
	Token internal.HexBytes `json:"token" bson:"_id"`
	// UserID is the member ObjectID (hex) the token authenticates.
	UserID internal.HexBytes `json:"userID" bson:"userid"`
	// BundleID is the token's anchor: a process-bundle id in the legacy bundle flow, or a
	// voting-process id in the new /processes flow. It only binds the token and gates the
	// resend cooldown; per-election signing/consumption keys on the election id separately.
	BundleID  internal.HexBytes `json:"bundleID" bson:"bundleid"`
	CreatedAt time.Time         `json:"createdAt" bson:"createdat"`
	// Secret is the per-token OTP challenge secret. It must never leave the
	// server, so it is excluded from JSON serialization.
	Secret   string `json:"-" bson:"secret,omitempty"`
	Attempts int    `json:"attempts" bson:"attempts"`
	// LastResendAt is the timestamp of the most recent OTP resend for this token.
	// It gates the resend cooldown independently of CreatedAt, so successive
	// resends of the same token are spaced apart. Zero until the first resend.
	LastResendAt time.Time `json:"lastResendAt" bson:"lastresendat"`
	Verified     bool      `json:"verified" bson:"verified"`
	VerifiedAt   time.Time `json:"verifiedAt" bson:"verifiedat"`
}

// CSPProcess is the status of a process in a bundle of processes for a user
type CSPProcess struct {
	ID          internal.HexBytes `json:"id" bson:"_id"` // hash(userID + processID)
	UserID      internal.HexBytes `json:"userID" bson:"userid"`
	ProcessID   internal.HexBytes `json:"processID" bson:"processid"`
	Used        bool              `json:"used" bson:"consumed"`
	UsedToken   internal.HexBytes `json:"usedToken" bson:"consumedtoken"`
	UsedAt      time.Time         `json:"usedAt" bson:"consumedat"`
	UsedAddress internal.HexBytes `json:"usedAddress" bson:"consumedaddress"`
	TimesVoted  int               `json:"timesVoted" bson:"timesVoted"`
}

// SetCSPAuth method stores a new CSP authentication token for a user and a
// bundle of processes. It returns an error if the token, user ID or bundle
// ID are nil.
func (ms *MongoStorage) SetCSPAuth(token, userID, bundleID internal.HexBytes, secret string) error {
	if token == nil || userID == nil || bundleID == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// insert the token
	if _, err := ms.cspTokens.InsertOne(ctx, CSPAuth{
		Token:     token,
		UserID:    userID,
		BundleID:  bundleID,
		CreatedAt: time.Now(),
		Secret:    secret,
		Verified:  false,
	}); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// CSPAuth method returns the CSP authentication data for a given token. It
// returns an error if the token is nil or the token does not exist.
func (ms *MongoStorage) CSPAuth(token internal.HexBytes) (*CSPAuth, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// find the token
	return ms.fetchCSPAuthFromDB(ctx, token)
}

// LastCSPAuth method returns the last CSP authentication data for a given
// user and bundle of processes. It returns an error if the user ID or bundle
// ID are nil or the token does not exist.
func (ms *MongoStorage) LastCSPAuth(userID, bundleID internal.HexBytes) (*CSPAuth, error) {
	if userID == nil || bundleID == nil {
		return nil, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// generate filter and options to find the last token for the user and
	// bundle
	filter := bson.M{"userid": userID, "bundleid": bundleID}
	opts := options.FindOne().SetSort(bson.M{"createdat": -1})
	tokenData := new(CSPAuth)
	// find the last token
	if err := ms.cspTokens.FindOne(ctx, filter, opts).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

// TouchCSPAuthResend atomically enforces the resend cooldown for a token. It
// updates the token's lastresendat to now only if at least cooldown has elapsed
// since its previous resend, and reports whether the resend is allowed. The
// first resend of a token (zero lastresendat) is always allowed so a voter who
// did not receive the initial code can retry immediately; only successive
// resends are spaced, which is what caps notification flooding. Doing the check
// and the timestamp bump in a single conditional update makes it race-safe:
// concurrent resends cannot both pass. The token must already exist (callers
// fetch it first); a false result for an existing token means the cooldown has
// not yet elapsed.
func (ms *MongoStorage) TouchCSPAuthResend(token internal.HexBytes, cooldown time.Duration) (bool, error) {
	if token == nil {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// only match (and update) the token when its last resend is older than the
	// cooldown. A never-resent token has the zero time in lastresendat, which is
	// always older than the threshold, so the first resend passes immediately.
	threshold := time.Now().Add(-cooldown)
	filter := bson.M{
		"_id":          token,
		"lastresendat": bson.M{"$lte": threshold},
	}
	update := bson.M{"$set": bson.M{"lastresendat": time.Now()}}
	res, err := ms.cspTokens.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	return res.ModifiedCount > 0, nil
}

// VerifyCSPAuth method verifies a CSP authentication token. It returns an
// error if the token is nil or the token does not exist.
func (ms *MongoStorage) VerifyCSPAuth(token internal.HexBytes) error {
	if token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// ensure that the token exists
	if _, err := ms.fetchCSPAuthFromDB(ctx, token); err != nil {
		return err
	}
	// update the token: mark verified and clear the secret so the code cannot be reused
	filter := bson.M{"_id": token}
	updateDoc := bson.M{"$set": bson.M{"verified": true, "verifiedat": time.Now()}, "$unset": bson.M{"secret": ""}}
	if _, err := ms.cspTokens.UpdateOne(ctx, filter, updateDoc, nil); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// IncrementCSPAuthAttempts atomically records a failed verification attempt for
// the given token, but only while the stored attempt count is still below
// maxAttempts. It returns recorded=true when the attempt was counted, and
// recorded=false when the cap had already been reached (no increment performed)
// — this is done in a single conditional update so concurrent verifications
// cannot push the counter past maxAttempts. It returns ErrTokenNotFound if the
// token does not exist and ErrBadInputs if the token is nil.
func (ms *MongoStorage) IncrementCSPAuthAttempts(token internal.HexBytes, maxAttempts int) (recorded bool, err error) {
	if token == nil {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// conditional increment: only bump attempts while still below the cap
	res, err := ms.cspTokens.UpdateOne(ctx,
		bson.M{"_id": token, "attempts": bson.M{"$lt": maxAttempts}},
		bson.M{"$inc": bson.M{"attempts": 1}})
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	// no document matched: either the token does not exist or the cap is already
	// reached. Distinguish the two so callers can react correctly.
	if _, err := ms.fetchCSPAuthFromDB(ctx, token); err != nil {
		return false, err
	}
	return false, nil
}

// CSPProcess returns the CSPProcess for the given token and processID.
// It returns an error if the token or processID are nil.
func (ms *MongoStorage) CSPProcess(token, processID internal.HexBytes) (*CSPProcess, error) {
	if token == nil || processID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// get the token data
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return nil, err
	}
	// find the token status by id
	return ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
}

// IsCSPProcessConsumed method checks if a CSP process has been consumed by a
// user. It returns an error if the userID or processID are nil. It returns
// true if the process has been consumed, false if it has not been consumed and
// an error if the process does not exist or the token is not verified.
func (ms *MongoStorage) IsCSPProcessConsumed(userID, processID internal.HexBytes) (bool, error) {
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// try to find the token status by id
	currentStatus, err := ms.fetchCSPProcessFromDB(ctx, userID, processID)
	if err != nil {
		if err == ErrTokenNotFound {
			return false, nil
		}
		return false, err
	}
	// check if the token is verified
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, currentStatus.UsedToken)
	if err != nil {
		return false, err
	}
	if !tokenData.Verified {
		return false, ErrTokenNotVerified
	}
	return currentStatus.TimesVoted > MaxVoteOverwritesPerProcess, nil
}

// ConsumeCSPProcess method consumes a CSP process for a user. It returns an
// error if the token, processID or address are nil. It returns an error if
// the token does not exist, the process has already been consumed or thecspAuthTokenStatusID(
// token is not verified.
func (ms *MongoStorage) ConsumeCSPProcess(token, processID, address internal.HexBytes) error {
	if token == nil || processID == nil || address == nil {
		return ErrBadInputs
	}
	// lock the keys
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// check if the token exists
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return err
	}
	// get the token status
	tokenStatus, err := ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
	if err != nil && !errors.Is(err, ErrTokenNotFound) {
		return err
	}
	// check if the token is already consumed
	if tokenStatus != nil && tokenStatus.TimesVoted > MaxVoteOverwritesPerProcess {
		return ErrProcessAlreadyConsumed
	}
	timesVoted := 1
	if tokenStatus != nil {
		timesVoted = tokenStatus.TimesVoted + 1
		// check if the address is the same as the previous one used to vote
		if tokenStatus.UsedAddress != nil && !tokenStatus.UsedAddress.Equals(address) {
			return ErrInvalidData
		}
	}
	// calculate the status id
	id := cspAuthTokenStatusID(tokenData.UserID, processID)
	// prepare the document to update
	updateDoc, err := dynamicUpdateDocument(CSPProcess{
		ID:          id,
		UserID:      tokenData.UserID,
		ProcessID:   processID,
		Used:        true,
		UsedAt:      time.Now(),
		UsedToken:   token,
		UsedAddress: address,
		TimesVoted:  timesVoted,
	}, nil)
	if err != nil {
		return errors.Join(ErrPrepareDocument, err)
	}
	// set the filter and update options to create the document if it does not
	// exist
	filter := bson.M{"_id": id}
	opts := options.Update().SetUpsert(true)
	// update the token status
	if _, err = ms.cspTokensStatus.UpdateOne(ctx, filter, updateDoc, opts); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

func (ms *MongoStorage) fetchCSPAuthFromDB(ctx context.Context, token internal.HexBytes) (*CSPAuth, error) {
	tokenData := new(CSPAuth)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"_id": token}).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData, nil
}

func (ms *MongoStorage) fetchCSPProcessFromDB(ctx context.Context, userID, processID internal.HexBytes) (*CSPProcess, error) {
	// calculate the status ID
	id := cspAuthTokenStatusID(userID, processID)
	// find the token status
	tokenStatus := new(CSPProcess)
	if err := ms.cspTokensStatus.FindOne(ctx, bson.M{"_id": id}).Decode(tokenStatus); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenStatus, nil
}

func cspAuthTokenStatusID(userID, processID internal.HexBytes) internal.HexBytes {
	hash := sha256.Sum256(append(userID, processID...))
	return internal.HexBytes(hash[:])
}

// CountCSPAuthByBundle counts the total number of CSP authentication tokens
// for a given bundle ID. Returns an error if the bundleID is nil.
func (ms *MongoStorage) CountCSPAuthByBundle(bundleID internal.HexBytes) (int64, error) {
	if bundleID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the bundle ID
	filter := bson.M{"bundleid": bundleID}
	distinctValues, err := ms.cspTokens.Distinct(ctx, "userid", filter)
	if err != nil {
		return 0, err
	}
	return int64(len(distinctValues)), nil
}

// CountCSPAuthVerifiedByBundle counts the number of verified CSP authentication
// tokens for a given bundle ID. Returns an error if the bundleID is nil.
func (ms *MongoStorage) CountCSPAuthVerifiedByBundle(bundleID internal.HexBytes) (int64, error) {
	if bundleID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the bundle ID and verified status
	filter := bson.M{"bundleid": bundleID, "verified": true}
	distinctValues, err := ms.cspTokens.Distinct(ctx, "userid", filter)
	if err != nil {
		return 0, err
	}
	return int64(len(distinctValues)), nil
}

// CountCSPProcessConsumedByProcess counts the number of consumed CSP processes
// for a given process ID. Returns an error if the processID is nil.
func (ms *MongoStorage) CountCSPProcessConsumedByProcess(processID internal.HexBytes) (int64, error) {
	if processID == nil {
		return 0, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// count documents matching the process ID and consumed status
	filter := bson.M{"processid": processID, "consumed": true}
	distinctValues, err := ms.cspTokensStatus.Distinct(ctx, "userid", filter)
	if err != nil {
		return 0, err
	}
	return int64(len(distinctValues)), nil
}

// CSPProcessByUserAndProcess retrieves the CSP process status for a given
// user and process. Returns an error if the userID or processID are nil.
func (ms *MongoStorage) CSPProcessByUserAndProcess(userID, processID internal.HexBytes) (*CSPProcess, error) {
	if userID == nil || processID == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.RLock()
	defer ms.keysLock.RUnlock()
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// fetch the process status
	return ms.fetchCSPProcessFromDB(ctx, userID, processID)
}

func (ms *MongoStorage) distinctCSPProcessVotersByProcess(processID internal.HexBytes) ([]string, error) {
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// prepare the filter
	filter := bson.M{"processid": processID, "consumed": true}
	// execute the distinct operation
	var results []string
	distinctValues, err := ms.cspTokensStatus.Distinct(ctx, "userid", filter)
	if err != nil {
		return nil, fmt.Errorf("failed to execute distinct query: %w", err)
	}
	// convert results to []internal.HexBytes
	for _, v := range distinctValues {
		b, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("unexpected type in distinct results")
		}
		results = append(results, b)
	}
	return results, nil
}

func (ms *MongoStorage) GetOrgMembersByProcess(orgAddress common.Address, processID internal.HexBytes) ([]*OrgMember, error) {
	userids, err := ms.distinctCSPProcessVotersByProcess(processID)
	if err != nil {
		return nil, err
	}
	_, orgMembers, err := ms.orgMembersByIDs(orgAddress, userids, 0, 0)
	if err != nil {
		return nil, err
	}
	return orgMembers, nil
}

// MembersWithUsedCSPProcess returns the subset of the given memberIDs (each the
// hex of an OrgMember ObjectID) that have already cast a ballot in the given
// process. The returned map is keyed by the memberID hex string and only
// contains entries set to true; members without a CSP process for the given
// process, or with one that has not been used, are simply absent from the map.
//
// A member is considered to have voted when a CSPProcess exists for
// (memberID, processID) and its Used field is true — i.e. the member consumed
// the process to cast a ballot.
func (ms *MongoStorage) MembersWithUsedCSPProcess(
	processID internal.HexBytes,
	memberIDs []string,
) (map[string]bool, error) {
	if processID == nil {
		return nil, ErrBadInputs
	}

	result := make(map[string]bool, len(memberIDs))
	for _, id := range memberIDs {
		userID := internal.HexBytesFromString(id)
		proc, err := ms.CSPProcessByUserAndProcess(userID, processID)
		if err != nil {
			if errors.Is(err, ErrTokenNotFound) {
				continue
			}
			return nil, fmt.Errorf("failed to query CSP process status: %w", err)
		}
		if proc.Used {
			result[id] = true
		}
	}
	return result, nil
}

// DeleteCSPAuthByBundle removes every CSP authentication token tied to the given bundle.
// It is a best-effort cleanup used when tearing down an organization (the bundle's
// processes share a common census/auth flow). Returns the number of deleted tokens.
func (ms *MongoStorage) DeleteCSPAuthByBundle(bundleID internal.HexBytes) (int64, error) {
	if bundleID == nil {
		return 0, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.cspTokens.DeleteMany(ctx, bson.M{"bundleid": bundleID})
	if err != nil {
		return 0, fmt.Errorf("failed to delete CSP auth tokens by bundle: %w", err)
	}
	return res.DeletedCount, nil
}

// DeleteCSPProcessByProcess removes every CSP process-status record tied to the given
// on-chain process id. Best-effort cleanup used when tearing down an organization's
// published processes. Returns the number of deleted status rows.
func (ms *MongoStorage) DeleteCSPProcessByProcess(processID internal.HexBytes) (int64, error) {
	if processID == nil {
		return 0, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.cspTokensStatus.DeleteMany(ctx, bson.M{"processid": processID})
	if err != nil {
		return 0, fmt.Errorf("failed to delete CSP process status by process: %w", err)
	}
	return res.DeletedCount, nil
}
