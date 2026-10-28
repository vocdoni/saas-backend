package db

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.vocdoni.io/dvote/log"
)

// ErrInsufficientWalletBalance is returned when a debit would make the balance negative.
var ErrInsufficientWalletBalance = fmt.Errorf("insufficient wallet balance")

// ErrManagedOrgSpendCapReached is returned when a debit would take a managed organization past
// the spend cap its integrator set.
var ErrManagedOrgSpendCapReached = fmt.Errorf("managed organization spend cap reached")

// orgSpendField is the wallet document path of a managed organization's ManagedOrgSpend. A
// lowercase hex address is a safe field name: no dots, no leading '$'.
func orgSpendField(managedOrg common.Address) string {
	return "orgSpend." + strings.ToLower(managedOrg.Hex())
}

// ManagedOrgSpend returns what the managed organization may spend and has spent from w.
func (w *Wallet) ManagedOrgSpend(managedOrg common.Address) ManagedOrgSpend {
	return w.OrgSpend[strings.ToLower(managedOrg.Hex())]
}

// withinSpendCap is the aggregation expression that holds when debiting amountCents keeps the
// managed organization at or under its cap; with no cap set it always holds.
func withinSpendCap(managedOrg common.Address, amountCents pricing.Cents) bson.M {
	field := "$" + orgSpendField(managedOrg)
	return bson.M{"$lte": bson.A{
		bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{field + ".spentCents", 0}}, amountCents}},
		bson.M{"$ifNull": bson.A{field + ".capCents", int64(math.MaxInt64)}},
	}}
}

// SetManagedOrgSpendCap sets the most the managed organization may spend from the integrator's
// wallet, or lifts the cap when capCents is nil. It creates the wallet when there is none yet, so
// a cap can be set before the first top-up. What it already spent is untouched: a cap below it
// only refuses further debits.
func (ms *MongoStorage) SetManagedOrgSpendCap(integrator, managedOrg common.Address, capCents *pricing.Cents) error {
	if (integrator.Cmp(common.Address{}) == 0) || (managedOrg.Cmp(common.Address{}) == 0) ||
		(capCents != nil && *capCents < 0) {
		return ErrInvalidData
	}
	field := orgSpendField(managedOrg) + ".capCents"
	update := bson.M{
		"$set":         bson.M{"updatedAt": time.Now()},
		"$setOnInsert": bson.M{"balanceCents": pricing.Cents(0)},
	}
	if capCents == nil {
		update["$unset"] = bson.M{field: ""}
	} else {
		update["$set"].(bson.M)[field] = *capCents
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": integrator}
	_, err := ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true))
	if mongo.IsDuplicateKeyError(err) {
		// a concurrent first write created the wallet: update it
		_, err = ms.wallets.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to set managed organization spend cap: %w", err)
	}
	return nil
}

// DropManagedOrgSpend removes the managed organization's whole spend entry (cap and spending)
// from the integrator's wallet, once the organization is torn down: every wallet read loads the
// document, so entries of deleted organizations must not pile up on it.
func (ms *MongoStorage) DropManagedOrgSpend(integrator, managedOrg common.Address) error {
	if (integrator.Cmp(common.Address{}) == 0) || (managedOrg.Cmp(common.Address{}) == 0) {
		return ErrInvalidData
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	if _, err := ms.wallets.UpdateOne(ctx, bson.M{"_id": integrator},
		bson.M{"$unset": bson.M{orgSpendField(managedOrg): ""}}); err != nil {
		return fmt.Errorf("failed to drop managed organization spend: %w", err)
	}
	return nil
}

// A money movement (a credit, a debit) is one conditional write on the wallet document that
// moves the balance, checks the cap, records the idempotency key and pushes the movement's ledger
// entry onto Wallet.Unposted, all at once (W1). The walletLedger row is a projection of it: the
// movement inserts it (W2, a duplicate is ignored) and pulls it from Unposted (W3), and so does
// every later touch of the wallet (drainWallet) and the startup sweep (DrainUnpostedWalletEntries).
// After a crash at any point a movement is either wholly visible — balance and record — or not
// applied at all, and every reader drains first.

// walletAppliedKeysWindow bounds Wallet.AppliedKeys below the 16 MB document limit. Replays of
// any age are caught by the ledger and Unposted (walletKeyApplied); the window only guards the
// race between that lookup and the write of a concurrent twin of the same movement.
const walletAppliedKeysWindow = 5000

// appliedKeysPush is the $push that records an applied key and keeps the array bounded.
func appliedKeysPush(idempotencyKey string) bson.M {
	return bson.M{
		"$each":  bson.A{idempotencyKey},
		"$slice": -walletAppliedKeysWindow,
	}
}

// walletStep names the writes of a movement that follow its balance write, for walletStepHook.
type walletStep int

const (
	// walletStepPost is the walletLedger insert (W2).
	walletStepPost walletStep = iota + 1
	// walletStepPull is the pull of the posted entry from Wallet.Unposted (W3).
	walletStepPull
)

// walletStepHook, when set (tests only), runs before W2 and W3 of every movement; an error it
// returns stops the movement there, as a crash would.
var walletStepHook func(step walletStep, idempotencyKey string) error

// walletKeyApplied reports whether the wallet already applied the key: its ledger row exists.
// The caller drains the wallet first, so an applied movement still in Unposted is in the ledger.
func (ms *MongoStorage) walletKeyApplied(orgAddress common.Address, idempotencyKey string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	err := ms.walletLedger.FindOne(ctx,
		bson.M{"orgAddress": orgAddress, "idempotencyKey": idempotencyKey}).Err()
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		return false, nil
	default:
		return false, fmt.Errorf("failed to look up wallet ledger entry: %w", err)
	}
}

// drainWallet posts every movement of orgAddress's wallet still in Unposted to the ledger (a row
// already there is a duplicate and ignored) and pulls them. Idempotent and safe to race: each
// replica pulls only the keys it read.
func (ms *MongoStorage) drainWallet(orgAddress common.Address) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var wallet struct {
		Unposted []WalletLedgerEntry `bson:"unposted"`
	}
	err := ms.wallets.FindOne(ctx, bson.M{"_id": orgAddress},
		options.FindOne().SetProjection(bson.M{"unposted": 1})).Decode(&wallet)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to read unposted wallet movements: %w", err)
	}
	if len(wallet.Unposted) == 0 {
		return nil
	}
	keys := make(bson.A, 0, len(wallet.Unposted))
	for i := range wallet.Unposted {
		if _, err := ms.walletLedger.InsertOne(ctx, &wallet.Unposted[i]); err != nil && !mongo.IsDuplicateKeyError(err) {
			return fmt.Errorf("failed to post wallet movement: %w", err)
		}
		keys = append(keys, wallet.Unposted[i].IdempotencyKey)
	}
	if _, err := ms.wallets.UpdateOne(ctx, bson.M{"_id": orgAddress},
		bson.M{"$pull": bson.M{"unposted": bson.M{"idempotencyKey": bson.M{"$in": keys}}}}); err != nil {
		return fmt.Errorf("failed to pull posted wallet movements: %w", err)
	}
	return nil
}

// drainWalletsWhere drains every wallet matching filter (a condition on its unposted entries).
func (ms *MongoStorage) drainWalletsWhere(filter bson.M) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.wallets.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return fmt.Errorf("failed to find wallets with unposted movements: %w", err)
	}
	var wallets []struct {
		ID common.Address `bson:"_id"`
	}
	if err := cursor.All(ctx, &wallets); err != nil {
		return fmt.Errorf("failed to decode wallets with unposted movements: %w", err)
	}
	var errs []error
	for _, w := range wallets {
		errs = append(errs, ms.drainWallet(w.ID))
	}
	return errors.Join(errs...)
}

// DrainUnpostedWalletEntries posts every movement still waiting in a wallet's Unposted: the
// startup sweep for movements whose own write sequence was cut short by a crash. Any later touch
// of the wallet does the same for that wallet.
func (ms *MongoStorage) DrainUnpostedWalletEntries() error {
	return ms.drainWalletsWhere(bson.M{"unposted.0": bson.M{"$exists": true}})
}

// Wallet returns a payer's prepaid wallet, empty if it was never credited.
func (ms *MongoStorage) Wallet(orgAddress common.Address) (*Wallet, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return nil, ErrInvalidData
	}
	if err := ms.drainWallet(orgAddress); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	wallet := &Wallet{}
	// appliedKeys is only used in update filters
	opts := options.FindOne().SetProjection(bson.M{"appliedKeys": 0})
	if err := ms.wallets.FindOne(ctx, bson.M{"_id": orgAddress}, opts).Decode(wallet); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return &Wallet{OrgAddress: orgAddress}, nil
		}
		return nil, fmt.Errorf("failed to get wallet: %w", err)
	}
	return wallet, nil
}

// WalletCredit adds money to a wallet: a top-up (Kind WalletEntryTopUp, the default; key: the checkout
// session id, with the payment intent and the VAT-included amount a refund needs) or a
// credit-back (WalletEntryCreditBack with the process, key: RefundCreditKey for a deleted draft,
// PublishFailCreditKey for a publish that failed for good).
type WalletCredit struct {
	OrgAddress        common.Address
	AmountCents       pricing.Cents
	IdempotencyKey    string
	Kind              string
	ProcessID         bson.ObjectID
	ProcessOrgAddress common.Address
	// SpentBy is the managed organization whose process the credit returns money for; its
	// spending goes down by the credit, freeing room under its cap. Zero for a top-up, and for a
	// credit-back to a payer that is the process's own organization (no cap applies).
	SpentBy         common.Address
	PaymentIntentID string
	GrossCents      pricing.Cents
	// Withheld is what a credit-back keeps back of the payment it returns, shown on its ledger row.
	Withheld RefundWithholding
	// ReleaseAddOns are the once-per-organization add-ons a credit-back gives back to the
	// organization (ProcessLedgerState.OwedRelease).
	ReleaseAddOns []OrgAddOn
}

// RefundWithholding is what a credit-back keeps back: the net cents of the once-per-organization
// add-ons another process relies on, and which add-ons they are.
type RefundWithholding struct {
	Cents  pricing.Cents
	AddOns []OrgAddOn
}

// RefundCreditKey is the idempotency key of the credit-back of a deleted draft: once per process.
func RefundCreditKey(processID bson.ObjectID) string { return "refund:" + processID.Hex() }

// PublishFailCreditKey is the idempotency key of the credit-back of a publish that failed for
// good at attempt (ProcessLedgerState.Attempt when it was credited back).
func PublishFailCreditKey(processID bson.ObjectID, attempt int) string {
	return fmt.Sprintf("publishfail:%s:%d", processID.Hex(), attempt)
}

// teardownAddOnKeyPrefix starts TeardownAddOnCreditKey.
const teardownAddOnKeyPrefix = "teardownaddon:"

// TeardownAddOnCreditKey is the idempotency key of an organization teardown's credit-back of a
// once-per-organization add-on an earlier credit-back of processID withheld and no process
// received: once per withholding, since each withheld price is withheld by one row (an add-on
// sold again after a teardown cut cleared the stamp, and withheld again, is a second price). Its
// row names the withholding process, but is not part of that process's ledger
// (ProcessLedgerState): the process's envelope settled the withheld price already.
func TeardownAddOnCreditKey(orgAddress common.Address, addOn OrgAddOn, processID bson.ObjectID) string {
	return teardownAddOnKeyPrefix + strings.ToLower(orgAddress.Hex()) + ":" + string(addOn) + ":" + processID.Hex()
}

// CreditWallet adds a credit (Kind defaults to WalletEntryTopUp), creating the wallet on first
// use. An already applied key is a successful no-op. A credit-back of 0 is valid when it withholds
// something: it settles a payment its withheld add-ons account for entirely.
func (ms *MongoStorage) CreditWallet(c WalletCredit) error {
	kind := c.Kind
	if kind == "" {
		kind = WalletEntryTopUp
	}
	settlesWithheld := kind == WalletEntryCreditBack && c.AmountCents == 0 && c.Withheld.Cents > 0
	if (c.AmountCents <= 0 && !settlesWithheld) || (kind != WalletEntryTopUp && kind != WalletEntryCreditBack) {
		return ErrInvalidData
	}
	m := walletMovement{
		entry: &WalletLedgerEntry{
			OrgAddress:        c.OrgAddress,
			AmountCents:       c.AmountCents,
			Kind:              kind,
			IdempotencyKey:    c.IdempotencyKey,
			ProcessID:         c.ProcessID,
			ProcessOrgAddress: c.ProcessOrgAddress,
			PaymentIntentID:   c.PaymentIntentID,
			GrossCents:        c.GrossCents,
			WithheldCents:     c.Withheld.Cents,
			WithheldAddOns:    c.Withheld.AddOns,
			ReleaseAddOns:     c.ReleaseAddOns,
		},
		upsert: true,
	}
	if c.SpentBy != (common.Address{}) {
		m.inc = bson.M{orgSpendField(c.SpentBy) + ".spentCents": -c.AmountCents}
	}
	return ms.applyWalletEntry(m)
}

// WalletRefund is money a top-up's payer got back through Stripe — a refund made in the
// dashboard, or a dispute's withdrawn funds — debited from the wallet the top-up credited.
type WalletRefund struct {
	OrgAddress  common.Address
	AmountCents pricing.Cents // net of VAT, positive
	// Key is the idempotency key: "striperefund:<refund id>" or "dispute:<dispute id>:<n>"
	Key string
}

// DebitWalletForRefund takes a refunded or disputed top-up back out of the wallet, in full even
// below zero: the money already left through Stripe, so this only records it. A negative balance
// refuses every DebitWalletForProcess until a top-up covers it. Keyed by r.Key, so a replay or a
// later update of the same refund or dispute debits nothing.
func (ms *MongoStorage) DebitWalletForRefund(r WalletRefund) error {
	if r.AmountCents <= 0 || r.Key == "" {
		return ErrInvalidData
	}
	return ms.applyWalletEntry(walletMovement{
		entry: &WalletLedgerEntry{
			OrgAddress:     r.OrgAddress,
			AmountCents:    -r.AmountCents,
			Kind:           WalletEntryRefund,
			IdempotencyKey: r.Key,
		},
		upsert: true,
	})
}

// ReverseWalletRefund credits back a refund debit that Stripe undid afterwards — a refund that
// failed or was canceled once debited, a dispute whose funds were reinstated. It credits exactly
// the ledger row keyed debitKey, as a positive refund row keyed creditKey, so the history shows
// the debit and its reversal as two refund rows netting to zero; a replay credits nothing. It
// reports false, changing nothing, when orgAddress's wallet recorded no such debit.
func (ms *MongoStorage) ReverseWalletRefund(orgAddress common.Address, debitKey, creditKey string) (bool, error) {
	if (orgAddress == common.Address{}) || debitKey == "" || creditKey == "" {
		return false, ErrInvalidData
	}
	if err := ms.drainWallet(orgAddress); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	var debit WalletLedgerEntry
	err := ms.walletLedger.FindOne(ctx, bson.M{
		"orgAddress": orgAddress, "idempotencyKey": debitKey, "kind": WalletEntryRefund,
	}).Decode(&debit)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to look up refund debit %s: %w", debitKey, err)
	}
	return true, ms.applyWalletEntry(walletMovement{
		entry: &WalletLedgerEntry{
			OrgAddress:     debit.OrgAddress,
			AmountCents:    -debit.AmountCents,
			Kind:           WalletEntryRefund,
			IdempotencyKey: creditKey,
		},
		upsert: true,
	})
}

// walletMovement is one movement applyWalletEntry applies: its ledger entry (signed amount and
// key), the conditions of its balance write besides the key, the other counters it moves, whether
// it creates the wallet, and why a balance write that matched nothing was refused.
type walletMovement struct {
	entry   *WalletLedgerEntry
	filter  bson.M
	inc     bson.M
	upsert  bool
	refused func(ctx context.Context) error
}

// applyWalletEntry applies m: drain the wallet, return at once when the key is already applied,
// then W1 (balance, cap, key and the entry pushed onto Unposted, one conditional write), W2 (the
// ledger row) and W3 (pull it from Unposted). A crash after W1 is healed by the next drain.
func (ms *MongoStorage) applyWalletEntry(m walletMovement) error {
	entry := m.entry
	orgAddress, idempotencyKey := entry.OrgAddress, entry.IdempotencyKey
	if (orgAddress.Cmp(common.Address{}) == 0) || idempotencyKey == "" {
		return ErrInvalidData
	}
	if err := ms.drainWallet(orgAddress); err != nil {
		return err
	}
	applied, err := ms.walletKeyApplied(orgAddress, idempotencyKey)
	if err != nil {
		return err
	}
	if applied {
		return nil
	}
	// set before W1, so every replica that posts the entry writes the same row, and its time is
	// the movement's
	entry.ID = bson.NewObjectID()
	entry.CreatedAt = time.Now().Truncate(time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	filter := bson.M{"_id": orgAddress, "appliedKeys": bson.M{"$ne": idempotencyKey}}
	maps.Copy(filter, m.filter)
	inc := bson.M{"balanceCents": entry.AmountCents}
	maps.Copy(inc, m.inc)
	update := bson.M{
		"$inc":  inc,
		"$push": bson.M{"appliedKeys": appliedKeysPush(idempotencyKey), "unposted": entry},
		"$set":  bson.M{"updatedAt": time.Now()},
	}
	res, err := ms.wallets.UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(m.upsert))
	if m.upsert && mongo.IsDuplicateKeyError(err) {
		// the wallet exists: either the key was applied already, or a concurrent first write
		// created it. Retrying without upsert applies only in the second case.
		res, err = ms.wallets.UpdateOne(ctx, filter, update)
	}
	if err != nil {
		return fmt.Errorf("failed to update wallet: %w", err)
	}
	if res.MatchedCount == 0 && res.UpsertedCount == 0 {
		// applied by a concurrent twin, or refused (balance, cap)
		n, err := ms.wallets.CountDocuments(ctx, bson.M{"_id": orgAddress, "$or": bson.A{
			bson.M{"appliedKeys": idempotencyKey}, bson.M{"unposted.idempotencyKey": idempotencyKey},
		}})
		if err != nil {
			return fmt.Errorf("failed to check wallet movement: %w", err)
		}
		if n > 0 {
			return ms.drainWallet(orgAddress)
		}
		if m.refused != nil {
			return m.refused(ctx)
		}
		return fmt.Errorf("wallet %s refused movement %s", orgAddress.Hex(), idempotencyKey)
	}
	if walletStepHook != nil {
		if err := walletStepHook(walletStepPost, idempotencyKey); err != nil {
			return err
		}
	}
	if _, err := ms.walletLedger.InsertOne(ctx, entry); err != nil && !mongo.IsDuplicateKeyError(err) {
		return fmt.Errorf("failed to append wallet ledger entry: %w", err)
	}
	if walletStepHook != nil {
		if err := walletStepHook(walletStepPull, idempotencyKey); err != nil {
			return err
		}
	}
	if _, err := ms.wallets.UpdateOne(ctx, bson.M{"_id": orgAddress},
		bson.M{"$pull": bson.M{"unposted": bson.M{"idempotencyKey": idempotencyKey}}}); err != nil {
		return fmt.Errorf("failed to pull posted wallet movement: %w", err)
	}
	return nil
}

// WalletDebit takes AmountCents now towards a process price of PriceCents; (process, attempt,
// price) is the idempotency key, so a retry at the same price charges nothing. Attempt is the
// process's ProcessLedgerState.Attempt: a publish after a credit-back pays again under a new key.
// ProcessOrgAddress is the organization owning the process, recorded on the ledger, and AddOns
// the once-per-organization add-ons this debit prices in (recorded on its row, INV-5).
type WalletDebit struct {
	OrgAddress        common.Address
	ProcessID         bson.ObjectID
	ProcessOrgAddress common.Address
	AmountCents       pricing.Cents
	PriceCents        pricing.Cents
	Attempt           int
	AddOns            []OrgAddOn
	// ManagedOrg is the managed organization the process belongs to: the debit counts towards
	// its spending and is refused past its cap. Zero skips the cap (a standard organization
	// paying from its own wallet). ProcessOrgAddress is set either way, for the history.
	ManagedOrg common.Address
}

// walletDebitKey is the idempotency key of a process debit. Attempt 0 keeps the "<process>:<price>"
// shape of a first debit; later attempts insert the attempt, a third segment no attempt-0 key has.
func walletDebitKey(processID bson.ObjectID, attempt int, priceCents pricing.Cents) string {
	if attempt == 0 {
		return fmt.Sprintf("%s:%d", processID.Hex(), priceCents)
	}
	return fmt.Sprintf("%s:%d:%d", processID.Hex(), attempt, priceCents)
}

// DebitWalletForProcess debits the payer's wallet at most once per (process, attempt, price), or
// returns ErrInsufficientWalletBalance or ErrManagedOrgSpendCapReached without touching it. The
// balance, the managed organization's cap, the idempotency key and the movement's record are one
// write, so the cap holds under concurrent debits exactly as the balance does.
func (ms *MongoStorage) DebitWalletForProcess(d WalletDebit) error {
	if (d.OrgAddress.Cmp(common.Address{}) == 0) || d.AmountCents <= 0 ||
		d.PriceCents < d.AmountCents || d.ProcessID == bson.NilObjectID || d.Attempt < 0 {
		return ErrInvalidData
	}
	orgAddress, amountCents := d.OrgAddress, d.AmountCents
	m := walletMovement{
		entry: &WalletLedgerEntry{
			OrgAddress:        orgAddress,
			AmountCents:       -amountCents,
			Kind:              WalletEntryPayment,
			IdempotencyKey:    walletDebitKey(d.ProcessID, d.Attempt, d.PriceCents),
			ProcessID:         d.ProcessID,
			ProcessOrgAddress: d.ProcessOrgAddress,
			PriceCents:        d.PriceCents,
			Attempt:           d.Attempt,
			AddOns:            d.AddOns,
		},
		filter: bson.M{"balanceCents": bson.M{"$gte": amountCents}},
		refused: func(ctx context.Context) error {
			if d.ManagedOrg != (common.Address{}) {
				over, err := ms.wallets.CountDocuments(ctx, bson.M{
					"_id": orgAddress, "$expr": bson.M{"$not": bson.A{withinSpendCap(d.ManagedOrg, amountCents)}},
				})
				if err != nil {
					return fmt.Errorf("failed to check managed organization spend cap: %w", err)
				}
				if over > 0 {
					return ErrManagedOrgSpendCapReached
				}
			}
			return ErrInsufficientWalletBalance
		},
	}
	if d.ManagedOrg != (common.Address{}) {
		m.filter["$expr"] = withinSpendCap(d.ManagedOrg, amountCents)
		m.inc = bson.M{orgSpendField(d.ManagedOrg) + ".spentCents": amountCents}
	}
	return ms.applyWalletEntry(m)
}

// ProcessLedgerState folds payer's ledger rows of processID, in the order they were applied, into
// what the process paid and is owed (see ProcessLedgerState). It drains the wallet first, so a
// movement whose row was not posted yet counts. Served by the {orgAddress, processId} index.
func (ms *MongoStorage) ProcessLedgerState(payer common.Address, processID bson.ObjectID) (*ProcessLedgerState, error) {
	if (payer.Cmp(common.Address{}) == 0) || processID == bson.NilObjectID {
		return nil, ErrInvalidData
	}
	if err := ms.drainWallet(payer); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.walletLedger.Find(ctx, bson.M{"orgAddress": payer, "processId": processID},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("failed to read process wallet movements: %w", err)
	}
	var rows []WalletLedgerEntry
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, fmt.Errorf("failed to decode process wallet movements: %w", err)
	}
	state := foldProcessLedger(rows)
	state.Payer = payer
	if state.PaidCents < 0 {
		log.Errorw(fmt.Errorf("process %s nets %d eur cents credited to %s", processID.Hex(), -state.PaidCents, payer.Hex()),
			"process ledger credits exceed its payments; taken as paid nothing")
		state.PaidCents = 0
	}
	return state, nil
}

// foldProcessLedger is ProcessLedgerState's fold over rows in the order they were applied.
func foldProcessLedger(rows []WalletLedgerEntry) *ProcessLedgerState {
	state := &ProcessLedgerState{}
	for i := range rows {
		row := &rows[i]
		switch row.Kind {
		case WalletEntryPayment:
			state.PaidCents -= row.AmountCents
			for _, addOn := range row.AddOns {
				if !slices.Contains(state.AddOns, addOn) {
					state.AddOns = append(state.AddOns, addOn)
				}
			}
			state.OwedRelease, state.OwedSince = nil, time.Time{}
		case WalletEntryCreditBack:
			if strings.HasPrefix(row.IdempotencyKey, teardownAddOnKeyPrefix) {
				continue // settled by the withholding row already (TeardownAddOnCreditKey)
			}
			state.PaidCents -= row.AmountCents + row.WithheldCents
			switch {
			case strings.HasPrefix(row.IdempotencyKey, "publishfail:"):
				state.Attempt++
				state.AddOns = nil
			case strings.HasPrefix(row.IdempotencyKey, "refund:"):
				state.Refunded = true
			default:
			}
			state.OwedRelease, state.OwedSince = row.ReleaseAddOns, row.CreatedAt
			state.CreditedBackAt = row.CreatedAt
		default:
		}
	}
	return state
}

// WalletEntry returns orgAddress's ledger entry keyed idempotencyKey, or ErrNotFound when the
// wallet never applied it. It drains the wallet first, so a movement not posted yet is found.
func (ms *MongoStorage) WalletEntry(orgAddress common.Address, idempotencyKey string) (*WalletLedgerEntry, error) {
	if (orgAddress.Cmp(common.Address{}) == 0) || idempotencyKey == "" {
		return nil, ErrInvalidData
	}
	if err := ms.drainWallet(orgAddress); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	entry := &WalletLedgerEntry{}
	err := ms.walletLedger.FindOne(ctx,
		bson.M{"orgAddress": orgAddress, "idempotencyKey": idempotencyKey}).Decode(entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find wallet ledger entry: %w", err)
	}
	return entry, nil
}

// ReleaseOwedOrgAddOns applies every add-on release a credit-back still owes
// (ProcessLedgerState.OwedRelease): the startup sweep for releases a crash cut short after their
// credit-back, next to DrainUnpostedWalletEntries. Each is pinned like the release any later touch
// applies (ReleaseOrganizationAddOn before the credit-back), so it is idempotent and never undoes a
// claim taken since. A failure is returned after trying the rest.
//
// ponytail: scans every credit-back that released an add-on, each startup; track owed releases on
// the wallet document if that list grows large.
func (ms *MongoStorage) ReleaseOwedOrgAddOns() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.walletLedger.Find(ctx,
		bson.M{"kind": WalletEntryCreditBack, "releaseAddOns.0": bson.M{"$exists": true}},
		options.Find().SetProjection(bson.M{"orgAddress": 1, "processId": 1, "processOrgAddress": 1}))
	if err != nil {
		return fmt.Errorf("failed to find credit-backs releasing add-ons: %w", err)
	}
	var rows []WalletLedgerEntry
	if err := cursor.All(ctx, &rows); err != nil {
		return fmt.Errorf("failed to decode credit-backs releasing add-ons: %w", err)
	}
	seen := map[bson.ObjectID]bool{}
	var errs []error
	for i := range rows {
		row := &rows[i]
		if seen[row.ProcessID] {
			continue
		}
		seen[row.ProcessID] = true
		state, err := ms.ProcessLedgerState(row.OrgAddress, row.ProcessID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, addOn := range state.OwedRelease {
			errs = append(errs, ms.ReleaseOrganizationAddOn(OrgAddOnClaim{
				OrgAddress: row.ProcessOrgAddress, AddOn: addOn, ProcessID: row.ProcessID, ClaimedBefore: state.OwedSince,
			}))
		}
	}
	return errors.Join(errs...)
}

// WalletTopUpByPaymentIntent returns the top-up ledger entry a Stripe payment intent paid for,
// or ErrNotFound when no wallet top-up was credited from it.
func (ms *MongoStorage) WalletTopUpByPaymentIntent(paymentIntentID string) (*WalletLedgerEntry, error) {
	if paymentIntentID == "" {
		return nil, ErrInvalidData
	}
	if err := ms.drainWalletsWhere(bson.M{"unposted.paymentIntentId": paymentIntentID}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	entry := &WalletLedgerEntry{}
	err := ms.walletLedger.FindOne(ctx,
		bson.M{"paymentIntentId": paymentIntentID, "kind": WalletEntryTopUp}).Decode(entry)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to find wallet top-up: %w", err)
	}
	return entry, nil
}

// WalletNetTopUpCents is what orgAddress has put into its wallet through Stripe and kept: its
// top-ups less the refunds and disputes Stripe returned (net of their reversals). Spending and
// credit-backs do not count, so a wallet spent down to zero keeps what it once paid in.
func (ms *MongoStorage) WalletNetTopUpCents(orgAddress common.Address) (pricing.Cents, error) {
	if err := ms.drainWallet(orgAddress); err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()
	cursor, err := ms.walletLedger.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"orgAddress": orgAddress, "kind": bson.M{"$in": bson.A{WalletEntryTopUp, WalletEntryRefund}},
		}}},
		{{Key: "$group", Value: bson.M{"_id": nil, "cents": bson.M{"$sum": "$amountCents"}}}},
	})
	if err != nil {
		return 0, fmt.Errorf("failed to sum wallet top-ups: %w", err)
	}
	var sums []struct {
		Cents pricing.Cents `bson:"cents"`
	}
	if err := cursor.All(ctx, &sums); err != nil {
		return 0, fmt.Errorf("failed to decode wallet top-ups: %w", err)
	}
	if len(sums) == 0 {
		return 0, nil
	}
	return sums[0].Cents, nil
}

// WalletLedger returns a page of an organization's wallet ledger, newest first.
func (ms *MongoStorage) WalletLedger(
	orgAddress common.Address, page, limit int64,
) (int64, []WalletLedgerEntry, error) {
	if orgAddress.Cmp(common.Address{}) == 0 {
		return 0, nil, ErrInvalidData
	}
	if err := ms.drainWallet(orgAddress); err != nil {
		return 0, nil, err
	}
	return paginatedDocuments[WalletLedgerEntry](ms.walletLedger, page, limit,
		// _id breaks createdAt ties: two rows written in the same millisecond keep insertion order
		bson.M{"orgAddress": orgAddress}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}))
}
