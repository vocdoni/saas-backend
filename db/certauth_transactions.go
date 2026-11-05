package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Statuses of a certificate sign-in transaction, in the only order it moves through them.
const (
	// CertAuthStatusCreated is an attempt the create endpoint stored, waiting for the auth listener.
	CertAuthStatusCreated = "created"
	// CertAuthStatusAuthenticated is an attempt the auth listener consumed: it holds a code and the
	// presented chain, waiting for the redeem.
	CertAuthStatusAuthenticated = "authenticated"
	// CertAuthStatusConsumed is an attempt whose code was redeemed. Its chain is gone.
	CertAuthStatusConsumed = "consumed"
)

// CertAuthTransaction is one certificate sign-in attempt (contract-cert-auth.md section 1). No field
// ever holds a DNI/NIE, a name or anything derived from the identity: the presented DER chain is its
// only personal data, removed at redeem and bounded by ExpiresAt otherwise.
type CertAuthTransaction struct {
	ID bson.ObjectID `bson:"_id"`
	// State is 32 random bytes, base64url (43 characters); unique.
	State string `bson:"state"`
	// Nonce is 16 random bytes, base32 lower case (26 characters): the label of the attempt's auth host.
	Nonce string `bson:"nonce"`
	// Challenge is the S256 PKCE challenge of the create request.
	Challenge string        `bson:"challenge"`
	ProcessID bson.ObjectID `bson:"processId"`
	// ReturnURL is the validated return URL of the create request.
	ReturnURL string `bson:"returnUrl"`
	// Purpose is "vote" or "check".
	Purpose string `bson:"purpose"`
	Status  string `bson:"status"`
	// Code is 32 random bytes, base64url, set by the auth listener; unique among the attempts that have one.
	Code string `bson:"code,omitempty"`
	// Chain is the presented DER certificates, leaf first. Empty with ChainPresented false means the
	// browser sent no certificate; empty with ChainPresented true means it sent one too large to keep.
	Chain          [][]byte `bson:"chain,omitempty"`
	ChainPresented bool     `bson:"chainPresented"`

	CreatedAt       time.Time `bson:"createdAt"`
	AuthenticatedAt time.Time `bson:"authenticatedAt,omitempty"`
	// ExpiresAt backs the TTL index: create + state TTL, shortened to authenticate + code TTL. The TTL
	// monitor only collects garbage: every consume re-checks its own deadline.
	ExpiresAt time.Time `bson:"expiresAt"`
}

// CertAuthStateConsume is what the auth listener stores when it consumes a state.
type CertAuthStateConsume struct {
	State string
	// Nonce is the label of the host the state was presented on: a state only matches on its own host.
	Nonce string
	// Code is the fresh code to issue.
	Code           string
	Chain          [][]byte
	ChainPresented bool
	// CreatedAfter is now minus the state TTL: an older attempt has expired.
	CreatedAfter time.Time
	// Now is the authentication time.
	Now time.Time
	// ExpiresAt is now plus the code TTL.
	ExpiresAt time.Time
}

// InsertCertAuthTransaction stores a new attempt, setting its id when it has none.
func (ms *MongoStorage) InsertCertAuthTransaction(tx *CertAuthTransaction) error {
	if tx == nil || tx.State == "" || tx.Nonce == "" || tx.ProcessID.IsZero() {
		return ErrBadInputs
	}
	if tx.ID.IsZero() {
		tx.ID = bson.NewObjectID()
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.certAuthTransactions.InsertOne(ctx, tx); err != nil {
		return fmt.Errorf("failed to insert certificate auth transaction: %w", err)
	}
	return nil
}

// ConsumeCertAuthState atomically moves the created, unexpired attempt holding the state on the
// nonce's host to authenticated, storing the code and the chain, and shortening its expiry to the
// code TTL. It returns the attempt as it was before, or ErrNotFound when no attempt matches: the
// state is unknown, expired, already consumed or presented on another host. Of two concurrent
// consumes of one state exactly one succeeds.
func (ms *MongoStorage) ConsumeCertAuthState(in CertAuthStateConsume) (*CertAuthTransaction, error) {
	if in.State == "" || in.Nonce == "" || in.Code == "" {
		return nil, ErrBadInputs
	}
	filter := bson.M{
		"state":     in.State,
		"nonce":     in.Nonce,
		"status":    CertAuthStatusCreated,
		"createdAt": bson.M{"$gt": in.CreatedAfter},
	}
	set := bson.M{
		"status":          CertAuthStatusAuthenticated,
		"code":            in.Code,
		"chainPresented":  in.ChainPresented,
		"authenticatedAt": in.Now,
		"expiresAt":       in.ExpiresAt,
	}
	if len(in.Chain) > 0 {
		set["chain"] = in.Chain
	}
	return ms.consumeCertAuthTransaction(filter, bson.M{"$set": set})
}

// ConsumeCertAuthCode atomically moves the authenticated attempt holding the code, authenticated
// after authenticatedAfter (now minus the code TTL), to consumed and removes its chain. It returns
// the attempt as it was before, chain included, or ErrNotFound when no attempt matches: the code is
// unknown, expired or already redeemed (CertAuthTransactionByCode tells them apart). Of two
// concurrent redeems of one code exactly one succeeds.
func (ms *MongoStorage) ConsumeCertAuthCode(code string, authenticatedAfter time.Time) (*CertAuthTransaction, error) {
	if code == "" {
		return nil, ErrBadInputs
	}
	filter := bson.M{
		"code":            code,
		"status":          CertAuthStatusAuthenticated,
		"authenticatedAt": bson.M{"$gt": authenticatedAfter},
	}
	update := bson.M{"$set": bson.M{"status": CertAuthStatusConsumed}, "$unset": bson.M{"chain": ""}}
	return ms.consumeCertAuthTransaction(filter, update)
}

// consumeCertAuthTransaction applies update to the one attempt matching filter, returning its
// pre-image, or ErrNotFound.
func (ms *MongoStorage) consumeCertAuthTransaction(filter, update bson.M) (*CertAuthTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	opts := options.FindOneAndUpdate().SetReturnDocument(options.Before)
	tx := &CertAuthTransaction{}
	if err := ms.certAuthTransactions.FindOneAndUpdate(ctx, filter, update, opts).Decode(tx); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to consume certificate auth transaction: %w", err)
	}
	return tx, nil
}

// CertAuthTransactionByState returns the attempt holding the state on the nonce's host, whatever its
// status or age, or ErrNotFound.
func (ms *MongoStorage) CertAuthTransactionByState(state, nonce string) (*CertAuthTransaction, error) {
	if state == "" || nonce == "" {
		return nil, ErrBadInputs
	}
	return ms.certAuthTransaction(bson.M{"state": state, "nonce": nonce})
}

// CertAuthTransactionByCode returns the attempt holding the code, whatever its status or age, or
// ErrNotFound.
func (ms *MongoStorage) CertAuthTransactionByCode(code string) (*CertAuthTransaction, error) {
	if code == "" {
		return nil, ErrBadInputs
	}
	return ms.certAuthTransaction(bson.M{"code": code})
}

// certAuthTransaction reads the one attempt matching filter, or ErrNotFound.
func (ms *MongoStorage) certAuthTransaction(filter bson.M) (*CertAuthTransaction, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	tx := &CertAuthTransaction{}
	if err := ms.certAuthTransactions.FindOne(ctx, filter).Decode(tx); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read certificate auth transaction: %w", err)
	}
	return tx, nil
}
