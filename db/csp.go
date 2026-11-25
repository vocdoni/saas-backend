package db

//revive:disable:max-public-structs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CSPAuth is one OTP challenge in cspTokens: one code, or one auth-only grant, with every token
// answering it. Read through a token (CSPAuth, LastCSPAuth), it is that token's view: Token is the
// token it was read through, and Verified whether that token solved the code.
type CSPAuth struct {
	// ID is the document id, random. Nothing looks a token up by it.
	ID internal.HexBytes `json:"-" bson:"_id"`
	// Token is the token this view was read through. Not stored.
	Token internal.HexBytes `json:"token" bson:"-"`
	// UserID is the member ObjectID (hex) the token authenticates.
	UserID internal.HexBytes `json:"userID" bson:"userid"`
	// AnchorID is the token's anchor: the voting-process id (a process-bundle id on tokens issued
	// by the removed legacy bundle flow; the bson key keeps that name). It only binds the token
	// and gates the resend cooldown; per-election signing/consumption keys on the election id separately.
	AnchorID internal.HexBytes `json:"bundleID" bson:"bundleid"`
	// CreatedAt is when the code was issued; it expires from here.
	CreatedAt time.Time `json:"createdAt" bson:"createdat"`
	// Secret is the code's OTP secret, unset once the code is solved. It must never leave the
	// server, so it is excluded from JSON serialization.
	Secret string `json:"-" bson:"secret,omitempty"`
	// Attempts counts the verification attempts on the code, across all its tokens.
	Attempts int `json:"attempts" bson:"attempts"`
	// Resends counts the sends of the code after its first one, across all its tokens.
	Resends int `json:"resends" bson:"resends"`
	// Tokens are the tokens answering the code, the first included, oldest first.
	Tokens []CSPAuthToken `json:"-" bson:"tokens"`
	// Sends maps a destination key to when the code was last sent there; a destination missing
	// from it was never sent the code.
	Sends map[string]time.Time `json:"-" bson:"sends,omitempty"`
	// VerifiedBy is the token that solved the code: set means verified, and only that token signs.
	VerifiedBy internal.HexBytes `json:"-" bson:"verifiedby,omitempty"`
	// VerifiedAt is when the code was solved.
	VerifiedAt time.Time `json:"verifiedAt" bson:"verifiedat"`
	// Verified reports whether Token solved the code. Not stored.
	Verified bool `json:"verified" bson:"-"`
}

// CSPAuthToken is one token answering a challenge.
type CSPAuthToken struct {
	ID        internal.HexBytes `json:"-" bson:"id"`
	CreatedAt time.Time         `json:"-" bson:"createdat"`
}

// view sets the per-token fields of a challenge read through token.
func (a *CSPAuth) view(token internal.HexBytes) *CSPAuth {
	a.Token = token
	a.Verified = len(a.VerifiedBy) > 0 && bytes.Equal(a.VerifiedBy, token)
	return a
}

// LastSend returns when the code was last sent to any destination, zero if never.
func (a *CSPAuth) LastSend() time.Time {
	var last time.Time
	for _, at := range a.Sends {
		if at.After(last) {
			last = at
		}
	}
	return last
}

// CSPChallengeRequest is a new code for NewCSPChallenge.
type CSPChallengeRequest struct {
	// Token is the code's first token.
	Token    internal.HexBytes
	UserID   internal.HexBytes
	AnchorID internal.HexBytes
	// Secret is the code's OTP secret.
	Secret string
	// Dest is the destination key the code's first send goes to.
	Dest string
}

// CSPTokenRequest adds Token to the pending code of challenge ChallengeID (AddCSPAuthToken).
type CSPTokenRequest struct {
	ChallengeID internal.HexBytes
	Token       internal.HexBytes
	// MaxTokens caps the code's tokens, the first included.
	MaxTokens int
	// MaxAttempts is the attempt count that locks the code.
	MaxAttempts int
	// TTL is how long the code lives from its creation.
	TTL time.Duration
}

// CSPSendClaimRequest claims a re-send of the code of challenge ChallengeID to destination Dest
// (ClaimCSPAuthSend).
type CSPSendClaimRequest struct {
	ChallengeID internal.HexBytes
	Dest        string
	// CoolDown spaces the sends to one destination.
	CoolDown time.Duration
	// TTL is how long the code lives from its creation.
	TTL time.Duration
	// MaxAttempts is the attempt count that locks the code.
	MaxAttempts int
	// MaxResends caps the code's re-sends.
	MaxResends int
}

// CSPSendClaim is a send ClaimCSPAuthSend claimed.
type CSPSendClaim struct {
	// At is the send time the claim stored for the destination, truncated to the millisecond
	// Mongo stores, so a rollback can match it exactly.
	At time.Time
	// Previous is the destination's send time before the claim, zero when it had none.
	Previous time.Time
}

// CSPSendRollback undoes a send claimed on challenge ChallengeID (RollbackCSPAuthSend).
type CSPSendRollback struct {
	ChallengeID internal.HexBytes
	Dest        string
	// ClaimedAt is the send time the claim stored; a later send stored another and is left alone.
	ClaimedAt time.Time
	// Previous is restored as the destination's send time; zero removes the destination.
	Previous time.Time
	// Resend reports whether the claim spent one of the code's resends, given back too.
	Resend bool
}

// newChallengeDoc is a challenge document with a fresh random id and token as its only token.
func newChallengeDoc(token, userID, anchorID internal.HexBytes, now time.Time) *CSPAuth {
	return &CSPAuth{
		ID:        internal.HexBytes(internal.RandomBytes(16)),
		UserID:    userID,
		AnchorID:  anchorID,
		CreatedAt: now,
		Tokens:    []CSPAuthToken{{ID: token, CreatedAt: now}},
	}
}

// insertChallenge stores a new challenge document.
func (ms *MongoStorage) insertChallenge(doc *CSPAuth) error {
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspTokens.InsertOne(ctx, doc); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// cspNow is the current time at the millisecond precision Mongo stores, so a time written and
// later matched exactly (a send claim) compares equal.
func cspNow() time.Time {
	return time.Now().Truncate(time.Millisecond)
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
	// VotedOnChain caches that the chain has a vote under this row's nullifier. It is set once a
	// lookup finds the vote and never cleared (a vote cannot leave the chain), so a row that has
	// it needs no further chain read. A missing field reads false: there is no migration.
	VotedOnChain bool `json:"votedOnChain" bson:"votedonchain,omitempty"`
	// BlindSecret holds the CSP's one-time blind-signature nonce k between the two rounds of a
	// blind (OFF_CHAIN_CA_V2) signature: armed in round 1 and atomically claimed in round 2.
	// Never leaves the server; absent for the non-anonymous ECDSA flow and once claimed.
	BlindSecret internal.HexBytes `json:"-" bson:"blindsecret,omitempty"`
	// BlindR is the blind point R issued alongside BlindSecret in round 1, kept so a repeated
	// round-1 call is idempotent (returns the same R rather than clobbering the armed nonce).
	BlindR internal.HexBytes `json:"-" bson:"blindr,omitempty"`
	// BlindWeight is the CSP-authorized weight captured at round 1 and used to salt the round-2
	// signature, so a mid-flow weight change cannot desync the salt from the client's bundle.
	BlindWeight internal.HexBytes `json:"-" bson:"blindweight,omitempty"`
}

// SetCSPAuth stores a new challenge for a user and anchor whose only token is token, holding the
// OTP secret (none for a challenge verified without a code). It returns ErrBadInputs if the token,
// user ID or anchor ID are nil.
func (ms *MongoStorage) SetCSPAuth(token, userID, anchorID internal.HexBytes, secret string) error {
	if token == nil || userID == nil || anchorID == nil {
		return ErrBadInputs
	}
	doc := newChallengeDoc(token, userID, anchorID, cspNow())
	doc.Secret = secret
	return ms.insertChallenge(doc)
}

// SetVerifiedCSPAuth stores an auth-only grant: a challenge with no code, verified by its only token.
func (ms *MongoStorage) SetVerifiedCSPAuth(token, userID, anchorID internal.HexBytes) error {
	if token == nil || userID == nil || anchorID == nil {
		return ErrBadInputs
	}
	now := cspNow()
	doc := newChallengeDoc(token, userID, anchorID, now)
	doc.VerifiedBy = token
	doc.VerifiedAt = now
	return ms.insertChallenge(doc)
}

// NewCSPChallenge stores a new code with its first token and its first send, to Dest, both at the
// creation time. It returns the stored challenge viewed through req.Token.
func (ms *MongoStorage) NewCSPChallenge(req CSPChallengeRequest) (*CSPAuth, error) {
	if req.Token == nil || req.UserID == nil || req.AnchorID == nil || req.Secret == "" || req.Dest == "" {
		return nil, ErrBadInputs
	}
	now := cspNow()
	doc := newChallengeDoc(req.Token, req.UserID, req.AnchorID, now)
	doc.Secret = req.Secret
	doc.Sends = map[string]time.Time{req.Dest: now}
	if err := ms.insertChallenge(doc); err != nil {
		return nil, err
	}
	return doc.view(req.Token), nil
}

// AddCSPAuthToken adds a token to a pending code in one conditional update: the code must still
// have its secret, be unlocked, unverified and unexpired, and hold fewer than MaxTokens tokens. It
// reports whether the token was added.
func (ms *MongoStorage) AddCSPAuthToken(req CSPTokenRequest) (bool, error) {
	if req.ChallengeID == nil || req.Token == nil || req.MaxTokens < 1 {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := cspNow()
	res, err := ms.cspTokens.UpdateOne(ctx, bson.M{
		"_id":        req.ChallengeID,
		"secret":     bson.M{"$exists": true},
		"attempts":   bson.M{"$lt": req.MaxAttempts},
		"verifiedby": bson.M{"$exists": false},
		"createdat":  bson.M{"$gt": now.Add(-req.TTL)},
		// a token at the last allowed position means the code is full
		fmt.Sprintf("tokens.%d", req.MaxTokens-1): bson.M{"$exists": false},
	}, bson.M{"$push": bson.M{"tokens": CSPAuthToken{ID: req.Token, CreatedAt: now}}})
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	return res.MatchedCount == 1, nil
}

// RemoveCSPAuthToken drops a token from a code that is not verified, for a token whose send
// failed. It is a no-op once the code is verified, so it never drops the token that solved it.
func (ms *MongoStorage) RemoveCSPAuthToken(challengeID, token internal.HexBytes) error {
	if challengeID == nil || token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspTokens.UpdateOne(ctx,
		bson.M{"_id": challengeID, "verifiedby": bson.M{"$exists": false}},
		bson.M{"$pull": bson.M{"tokens": bson.M{"id": token}}}); err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	return nil
}

// ClaimCSPAuthSend claims a re-send of a pending code to one destination in one conditional
// update: the code must still have its secret, be unlocked and unexpired, have resends left, and
// not have been sent to that destination within the cooldown. The update stores the send time and
// spends a resend. claimed is false, with nothing written, when any condition fails; of concurrent
// claims for one destination exactly one succeeds.
func (ms *MongoStorage) ClaimCSPAuthSend(req CSPSendClaimRequest) (claim CSPSendClaim, claimed bool, err error) {
	if req.ChallengeID == nil || req.Dest == "" {
		return CSPSendClaim{}, false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	now := cspNow()
	field := "sends." + req.Dest
	filter := bson.M{
		"_id":       req.ChallengeID,
		"secret":    bson.M{"$exists": true},
		"attempts":  bson.M{"$lt": req.MaxAttempts},
		"createdat": bson.M{"$gt": now.Add(-req.TTL)},
		"resends":   bson.M{"$not": bson.M{"$gte": req.MaxResends}},
		"$or": bson.A{
			bson.M{field: bson.M{"$exists": false}},
			bson.M{field: bson.M{"$lte": now.Add(-req.CoolDown)}},
		},
	}
	update := bson.M{"$set": bson.M{field: now}, "$inc": bson.M{"resends": 1}}
	var before struct {
		Sends map[string]time.Time `bson:"sends"`
	}
	err = ms.cspTokens.FindOneAndUpdate(ctx, filter, update,
		options.FindOneAndUpdate().SetReturnDocument(options.Before).SetProjection(bson.M{field: 1})).Decode(&before)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return CSPSendClaim{}, false, nil
	}
	if err != nil {
		return CSPSendClaim{}, false, errors.Join(ErrStoreToken, err)
	}
	return CSPSendClaim{At: now, Previous: before.Sends[req.Dest]}, true, nil
}

// RollbackCSPAuthSend undoes a send claim whose message never reached the voter: it restores the
// destination's previous send time (or removes it) and, for a re-send, gives the resend back. It
// only matches while the destination still holds the claim's own send time, so it never undoes a
// later send; rolledBack is false when it did not match.
func (ms *MongoStorage) RollbackCSPAuthSend(r CSPSendRollback) (rolledBack bool, err error) {
	if r.ChallengeID == nil || r.Dest == "" || r.ClaimedAt.IsZero() {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	field := "sends." + r.Dest
	filter := bson.M{"_id": r.ChallengeID, field: r.ClaimedAt}
	update := bson.M{}
	if r.Previous.IsZero() {
		update["$unset"] = bson.M{field: ""}
	} else {
		update["$set"] = bson.M{field: r.Previous}
	}
	if r.Resend {
		filter["resends"] = bson.M{"$gt": 0}
		update["$inc"] = bson.M{"resends": -1}
	}
	res, err := ms.cspTokens.UpdateOne(ctx, filter, update)
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	return res.MatchedCount == 1, nil
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

// LastCSPAuth returns the user's latest challenge on the anchor, viewed through its newest token.
// It returns an error if the user ID or anchor ID are nil or there is no challenge.
func (ms *MongoStorage) LastCSPAuth(userID, anchorID internal.HexBytes) (*CSPAuth, error) {
	if userID == nil || anchorID == nil {
		return nil, ErrBadInputs
	}
	// create a context with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	// generate filter and options to find the last token for the user and
	// anchor
	filter := bson.M{"userid": userID, "bundleid": anchorID}
	opts := options.FindOne().SetSort(bson.M{"createdat": -1})
	tokenData := new(CSPAuth)
	// find the last token
	if err := ms.cspTokens.FindOne(ctx, filter, opts).Decode(tokenData); err != nil {
		if err == mongo.ErrNoDocuments {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	// the view of the latest code is its newest token
	var last internal.HexBytes
	if n := len(tokenData.Tokens); n > 0 {
		last = tokenData.Tokens[n-1].ID
	}
	return tokenData.view(last), nil
}

// VerifyCSPAuth marks the token's code as solved by that token, in one update that also clears the
// code's secret, so the code cannot be solved again through any of its tokens. A code issued with
// a secret only verifies while it still has it. It returns ErrChallengeConsumed when the code is
// already solved, and ErrTokenNotFound when no code has the token.
func (ms *MongoStorage) VerifyCSPAuth(token internal.HexBytes) error {
	if token == nil {
		return ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return err
	}
	filter := bson.M{"_id": tokenData.ID, "verifiedby": bson.M{"$exists": false}}
	if tokenData.Secret != "" {
		filter["secret"] = bson.M{"$exists": true}
	}
	res, err := ms.cspTokens.UpdateOne(ctx, filter, bson.M{
		"$unset": bson.M{"secret": ""},
		"$set":   bson.M{"verifiedby": token, "verifiedat": time.Now()},
	})
	if err != nil {
		return errors.Join(ErrStoreToken, err)
	}
	if res.MatchedCount == 0 {
		return ErrChallengeConsumed
	}
	return nil
}

// IncrementCSPAuthAttempts atomically records a verification attempt for
// the code the given token answers, across all its tokens, but only while the stored attempt count is still below
// maxAttempts. It returns recorded=true when the attempt was counted, and
// recorded=false when the cap had already been reached (no increment performed)
// — this is done in a single conditional update so concurrent verifications
// cannot push the counter past maxAttempts. It returns ErrTokenNotFound if the
// token does not exist and ErrBadInputs if the token is nil. It returns
// ErrChallengeConsumed when the code was solved meanwhile.
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
		bson.M{"tokens.id": token, "secret": bson.M{"$exists": true}, "attempts": bson.M{"$lt": maxAttempts}},
		bson.M{"$inc": bson.M{"attempts": 1}})
	if err != nil {
		return false, errors.Join(ErrStoreToken, err)
	}
	if res.MatchedCount == 1 {
		return true, nil
	}
	// no document matched: the token is unknown, its code was solved meanwhile, or the cap is reached
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return false, err
	}
	if tokenData.Secret == "" {
		return false, ErrChallengeConsumed
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
	// Check the recorded token is verified. It can be gone rather than unverified: the census
	// revocation cascade deletes a member's auth sessions by userid while deliberately keeping
	// this consumption row, so a dangling UsedToken is a fact of a past revocation, not an
	// inconsistency. The row itself is only ever written on a signature, which requires a
	// verified token, so the row is evidence enough — erroring here would refuse the member's
	// remaining vote overwrites for the pinned address in every election they were signed for.
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, currentStatus.UsedToken)
	switch {
	case err == nil:
		if !tokenData.Verified {
			return false, ErrTokenNotVerified
		}
	case errors.Is(err, ErrTokenNotFound):
		// revoked session; the consumption row stands on its own
	default:
		return false, err
	}
	return currentStatus.TimesVoted >= MaxCSPSignaturesPerElection, nil
}

// ConsumeCSPProcess method consumes a CSP process for a user. It returns an
// error if the token, processID or address are nil. It returns an error if
// the token does not exist, the process has already been consumed or thecspAuthTokenStatusID(
// token is not verified.
func (ms *MongoStorage) ConsumeCSPProcess(token, processID, address internal.HexBytes) error {
	// an empty address would be stored as an empty string, which the address pin below can never
	// match again, so it is refused like a missing one
	if token == nil || processID == nil || len(address) == 0 {
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
	// calculate the status id
	id := cspAuthTokenStatusID(tokenData.UserID, processID)
	// consume in one conditional upsert, so a concurrent caller — also from another
	// service replica — can never push the row past the overwrite budget or move it
	// to another address: the vote budget and the address pin are part of the filter,
	// and the first consumption creates the row atomically. A row that fails the
	// filter makes the upsert hit the unique _id and return a duplicate-key error,
	// which we resolve by reading the row to name the refusal.
	filter := bson.M{
		"_id":             id,
		"timesVoted":      bson.M{"$not": bson.M{"$gte": MaxCSPSignaturesPerElection}},
		"consumedaddress": bson.M{"$in": bson.A{nil, address}},
	}
	update := bson.M{
		"$inc": bson.M{"timesVoted": 1},
		"$set": bson.M{
			"consumed":        true,
			"consumedat":      time.Now(),
			"consumedtoken":   token,
			"consumedaddress": address,
		},
		"$setOnInsert": bson.M{
			"userid":    tokenData.UserID,
			"processid": processID,
		},
	}
	opts := options.UpdateOne().SetUpsert(true)
	for range consumeCSPProcessAttempts {
		_, err := ms.cspTokensStatus.UpdateOne(ctx, filter, update, opts)
		if err == nil {
			return nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return errors.Join(ErrStoreToken, err)
		}
		existing, ferr := ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
		if ferr != nil {
			return ferr
		}
		if existing.TimesVoted >= MaxCSPSignaturesPerElection {
			return ErrProcessAlreadyConsumed
		}
		// check if the address is the same as the previous one used to vote
		if existing.UsedAddress != nil && !existing.UsedAddress.Equals(address) {
			return ErrInvalidData
		}
		// two first consumptions raced on the row creation: the loser lands here
		// with a row that still passes the filter, so retry the update against it
	}
	// the row keeps failing the filter while looking valid once decoded (e.g. a stored
	// address the pin cannot match): refuse rather than spin against the database
	return fmt.Errorf("%w: consumption row %s fails the consume filter", ErrStoreToken, id)
}

// consumeCSPProcessAttempts bounds ConsumeCSPProcess's conditional upsert retries. One retry is all a
// lost row-creation race needs; more only guard against a row the filter and the decoded checks
// disagree on, which must not turn into a busy loop.
const consumeCSPProcessAttempts = 3

// ArmCSPProcessBlind arms the round-1 half of a blind (OFF_CHAIN_CA_V2) signature: it atomically
// stores the one-time nonce k, its blind point R and the CSP-authorized weight for (token's user,
// processID), but only while the election is not already signed (consumed) and no nonce is armed.
// It is idempotent: a repeated round-1 call for an already-armed, unconsumed election returns the
// existing R and weight instead of clobbering the nonce (so a client that lost the first R and a
// concurrent retry both blind against the same R). It returns ErrProcessAlreadyConsumed once the
// election has been signed — a blind census allows exactly one signature per (user, election).
func (ms *MongoStorage) ArmCSPProcessBlind(
	token, processID, secretK, blindR, weight internal.HexBytes,
) (r, w internal.HexBytes, err error) {
	if token == nil || processID == nil || secretK == nil || blindR == nil {
		return nil, nil, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tokenData, err := ms.fetchCSPAuthFromDB(ctx, token)
	if err != nil {
		return nil, nil, err
	}
	id := cspAuthTokenStatusID(tokenData.UserID, processID)
	// arm only if not yet consumed and not yet armed. upsert makes the first round-1 create the
	// row atomically; a row that already carries a nonce (or is consumed) fails the filter and,
	// because the filter pins _id, the upsert then hits the unique _id and returns a duplicate-key
	// error — which we resolve by reading the existing row (idempotent return, or consumed).
	filter := bson.M{"_id": id, "consumed": bson.M{"$ne": true}, "blindsecret": bson.M{"$exists": false}}
	update := bson.M{"$set": bson.M{
		"userid":      tokenData.UserID,
		"processid":   processID,
		"blindsecret": secretK,
		"blindr":      blindR,
		"blindweight": weight,
	}}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).SetReturnDocument(options.After)
	armed := new(CSPProcess)
	if err := ms.cspTokensStatus.FindOneAndUpdate(ctx, filter, update, opts).Decode(armed); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// a nonce is already armed (or the election is consumed): return the existing state
			existing, ferr := ms.fetchCSPProcessFromDB(ctx, tokenData.UserID, processID)
			if ferr != nil {
				return nil, nil, ferr
			}
			if existing.Used {
				return nil, nil, ErrProcessAlreadyConsumed
			}
			return existing.BlindR, existing.BlindWeight, nil
		}
		return nil, nil, errors.Join(ErrStoreToken, err)
	}
	return armed.BlindR, armed.BlindWeight, nil
}

// ClaimCSPProcessBlind atomically claims the round-2 half of a blind (OFF_CHAIN_CA_V2) signature for
// the token's user: in a single conditional update it clears the one-time nonce and marks the
// election consumed, so exactly one concurrent caller ever obtains the nonce (blind-signature nonce
// reuse is catastrophic — it leaks the salted signing key). It returns the pre-claim document, which
// still carries the claimed BlindSecret, BlindR and BlindWeight to sign with. A blind census is
// address-less, so there is no address to pin; exactly-one-signature replaces the ECDSA overwrite
// budget. Returns ErrBlindRequestNotFound if nothing is armed and ErrProcessAlreadyConsumed if the
// election was already signed.
func (ms *MongoStorage) ClaimCSPProcessBlind(userID, processID, token internal.HexBytes) (*CSPProcess, error) {
	if userID == nil || processID == nil || token == nil {
		return nil, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	id := cspAuthTokenStatusID(userID, processID)
	filter := bson.M{"_id": id, "blindsecret": bson.M{"$exists": true}, "consumed": bson.M{"$ne": true}}
	update := bson.M{
		"$set":   bson.M{"consumed": true, "consumedat": time.Now(), "consumedtoken": token, "timesVoted": 1},
		"$unset": bson.M{"blindsecret": ""},
	}
	// return the document as it was BEFORE the claim, so the caller gets the nonce it just claimed
	opts := options.FindOneAndUpdate().SetReturnDocument(options.Before)
	claimed := new(CSPProcess)
	if err := ms.cspTokensStatus.FindOneAndUpdate(ctx, filter, update, opts).Decode(claimed); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// nothing matched: either no nonce is armed, or the election is already consumed
			existing, ferr := ms.fetchCSPProcessFromDB(ctx, userID, processID)
			if ferr != nil {
				if errors.Is(ferr, ErrTokenNotFound) {
					return nil, ErrBlindNonceNotFound
				}
				return nil, ferr
			}
			if existing.Used {
				return nil, ErrProcessAlreadyConsumed
			}
			return nil, ErrBlindNonceNotFound
		}
		return nil, errors.Join(ErrStoreToken, err)
	}
	return claimed, nil
}

// fetchCSPAuthFromDB returns the challenge the token answers, viewed through the token. Every
// db-internal token resolution goes through it, so a token that did not solve its code never reads
// as verified.
func (ms *MongoStorage) fetchCSPAuthFromDB(ctx context.Context, token internal.HexBytes) (*CSPAuth, error) {
	tokenData := new(CSPAuth)
	if err := ms.cspTokens.FindOne(ctx, bson.M{"tokens.id": token}).Decode(tokenData); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrTokenNotFound
		}
		return nil, err
	}
	return tokenData.view(token), nil
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

// CSPProcessesByMembers returns, in one query, every CSP process row of the given members
// (memberIDs are OrgMember ObjectID hex strings; ids that are not hex are skipped) on any of the
// given elections. Rows exist only for (member, election) pairs the CSP has touched, so a missing
// pair means "not signed".
func (ms *MongoStorage) CSPProcessesByMembers(processIDs []internal.HexBytes, memberIDs []string) ([]CSPProcess, error) {
	userIDs := normalizeCSPUserIDs(memberIDs)
	if len(processIDs) == 0 || len(userIDs) == 0 {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	// covered by the userid+processid index on cspTokensStatus
	cursor, err := ms.cspTokensStatus.Find(ctx, bson.M{
		"processid": bson.M{"$in": processIDs},
		"userid":    bson.M{"$in": userIDs},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query CSP processes of members: %w", err)
	}
	var rows []CSPProcess
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode CSP processes of members: %w", err)
	}
	return rows, nil
}

// MarkCSPProcessVotedOnChain sets the VotedOnChain cache flag on the CSP process row of
// (userID, processID), and on nothing else. A missing row is not an error: there is nothing to
// cache on.
func (ms *MongoStorage) MarkCSPProcessVotedOnChain(userID, processID internal.HexBytes) error {
	if userID == nil || processID == nil {
		return ErrBadInputs
	}
	return ms.markVotedOnChain(bson.M{"_id": cspAuthTokenStatusID(userID, processID)})
}

// MarkCSPProcessVotedOnChainByAddress caches that the vote cast by address on processID is on
// chain, on the CSP process row that consumed that address: the relay knows the voter's address
// and the election, not the member. No row matches a blind-census vote (its row holds no address),
// which is not an error. The lookup is served by the cspTokensStatus processid+consumedaddress
// index (migration 32).
func (ms *MongoStorage) MarkCSPProcessVotedOnChainByAddress(processID, address internal.HexBytes) error {
	if len(processID) == 0 || len(address) == 0 {
		return ErrBadInputs
	}
	return ms.markVotedOnChain(bson.M{"processid": processID, "consumedaddress": address})
}

// markVotedOnChain sets the votedonchain cache on the CSP process row filter matches. A filter that
// matches no row has nothing to cache on, which is not an error.
func (ms *MongoStorage) markVotedOnChain(filter bson.M) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.cspTokensStatus.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"votedonchain": true}}); err != nil {
		return fmt.Errorf("failed to mark CSP process voted on chain: %w", err)
	}
	return nil
}

// DeleteCSPChallenge removes a challenge whose first send never reached the queue, but only while
// its first token is its only one: a token a concurrent request added must keep resolving.
func (ms *MongoStorage) DeleteCSPChallenge(challengeID internal.HexBytes) (deleted bool, err error) {
	if challengeID == nil {
		return false, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	res, err := ms.cspTokens.DeleteOne(ctx, bson.M{"_id": challengeID, "tokens.1": bson.M{"$exists": false}})
	if err != nil {
		return false, fmt.Errorf("failed to delete CSP challenge: %w", err)
	}
	return res.DeletedCount == 1, nil
}

// DeleteCSPAuthByAnchor removes every CSP authentication token tied to the given bundle.
// It is a best-effort cleanup used when tearing down an organization (the bundle's
// processes share a common census/auth flow). Returns the number of deleted tokens.
func (ms *MongoStorage) DeleteCSPAuthByAnchor(anchorID internal.HexBytes) (int64, error) {
	if anchorID == nil {
		return 0, ErrBadInputs
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	res, err := ms.cspTokens.DeleteMany(ctx, bson.M{"bundleid": anchorID})
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
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	res, err := ms.cspTokensStatus.DeleteMany(ctx, bson.M{"processid": processID})
	if err != nil {
		return 0, fmt.Errorf("failed to delete CSP process status by process: %w", err)
	}
	return res.DeletedCount, nil
}

// DeleteVotingProcessCSPByOrg removes the CSP auth tokens anchored to any voting process of the
// organization (the anchor is the process ObjectID bytes, see csp/handlers parseProcessID) and the
// CSP status rows of their questions' elections (keyed by the question UpstreamID). It is a step
// of an organization teardown and must run before DeleteVotingProcessesByOrg, which drops the ids
// it reads.
func (ms *MongoStorage) DeleteVotingProcessCSPByOrg(orgAddress common.Address) error {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return ErrInvalidData
	}
	ms.keysLock.Lock()
	defer ms.keysLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), bulkTimeout)
	defer cancel()
	var processIDs []bson.ObjectID
	if err := ms.votingProcesses.Distinct(ctx, "_id", bson.M{"orgAddress": orgAddress}).Decode(&processIDs); err != nil {
		return fmt.Errorf("failed to list voting processes: %w", err)
	}
	anchors := make([]internal.HexBytes, 0, len(processIDs))
	for _, id := range processIDs {
		anchors = append(anchors, internal.HexBytes(id[:]))
	}
	if len(anchors) > 0 {
		if _, err := ms.cspTokens.DeleteMany(ctx, bson.M{"bundleid": bson.M{"$in": anchors}}); err != nil {
			return fmt.Errorf("failed to delete CSP auth tokens of voting processes: %w", err)
		}
	}
	var upstreamIDs []internal.HexBytes
	if err := ms.processesQuestions.Distinct(ctx, "upstreamId", bson.M{"orgAddress": orgAddress}).Decode(&upstreamIDs); err != nil {
		return fmt.Errorf("failed to list question elections: %w", err)
	}
	if len(upstreamIDs) > 0 {
		if _, err := ms.cspTokensStatus.DeleteMany(ctx, bson.M{"processid": bson.M{"$in": upstreamIDs}}); err != nil {
			return fmt.Errorf("failed to delete CSP status of question elections: %w", err)
		}
	}
	return nil
}
