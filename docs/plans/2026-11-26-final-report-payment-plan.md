# Final report add-on after publish: implementation plan

Spec: docs/specs/2026-11-26-final-report-payment-design.md
Class: architectural
Repos and branches: /home/lucasmenendez/saas-backend/.worktrees/feature-final-report-payment → feature/final-report-payment (base 9bdf27a9)

Every task's code is an exact diff, identical to its patch in
`.delivery/2026-11-26-final-report-payment/patches/task-<N>.patch` (apply with `git am` or
`git apply`). The patches were built and run, in order, in the scratch worktree
`~/.cache/saas-backend/scratch/final-report-payment` (branch `scratch/final-report-payment`).
Run every command from the repo root.

## Global Constraints
- Commits: Conventional Commits `type(scope): lowercase summary`, header ≤100 chars, no AI attribution, no Co-Authored-By trailers. Never sign: always `git -c commit.gpgsign=false commit ...` (and for any merge/cherry-pick/rebase).
- Never push; commit with explicit paths only (`git commit -m ... -- <paths>`), never `git add -A`.
- Build output under ~/.cache/saas-backend/, never /tmp.
- AGENTS.md conventions: fmt.Errorf (not errors.New), wrap with context, lowercase log/error messages, gofumpt, lll 130, godoc on exported names, quicktest matchers (qt.HasLen, qt.IsNil, ...).
- Code comments: 1-3 lines, carry the why only.
- No client/integrator names anywhere.
- Behaviour that must stay unchanged: publish payment, census headroom purchase, refunds/credit-backs, org add-on claims; the api suite is slow, always narrow with -run.

## Review Focus
- `db/wallets.go` `DebitWalletForSignedCertificate` and the shared `debitWallet` it now has with
  `DebitWalletForProcess` (same balance filter, spend-cap `$expr`/`$inc` and refusal mapping;
  key `signedcert:<processId>`, no `priceCents`/`attempt`). The refactor must leave
  `DebitWalletForProcess` byte-for-byte equivalent in effect.
- `db/wallets.go` `foldProcessLedger`: a `signedCertificate` payment row adds to `PaidCents` and
  to `SignedCertificateCents`; it adds no add-on and is never reset (only drafts get credit-backs).
- `api/processes_census_payment.go` `quoteProcessAtCensusSize`: `SignedCert` priced as
  `vp.AddOns.SignedCertificate || paid.SignedCertificateCents > 0`, so a debit whose echo did not
  land never reads as census headroom (pinned by `TestSignedCertificateCutShort`).
- `api/processes_census_payment.go` `grandfatherProcessPayment`: "wallet paid nothing" is
  `PaidCents - SignedCertificateCents == 0`; the handler writes the base (`paidProcessProjection`
  on the stored process) before it takes the wallet lock and debits.
- `api/processes_signed_certificate.go` `buySignedCertificate`: lock → `refuseTearingDown` →
  ledger read → refunded 409 → open checkout 409 → already paid (flag or ledger) debits 0 →
  debit → `moneyStepSignedCert` → echo → payment cache. Refusals via `walletDebitError`.
- `api/processes_publish_payment.go` `walletDebitError`: extracted verbatim from
  `chargeProcessWallet` (spend cap measured on `s.org.Address`, which every caller passes as the
  process's organization); `walletShort.message` only replaces the 40178 message.
- `api/processes_price.go`: `signedCertificatePaid`/`signedCertificateDueCents` only for a
  published process; the read writes nothing.

## Task order and parallelism
| Task | Depends on | Repo/files | May run in parallel with |
|---|---|---|---|
| 1 | — | `db/types.go`, `db/wallets.go`, `db/voting_processes.go`, `pricing/pricing.go`, db tests | — |
| 2 | 1 | `api/processes_signed_certificate*.go`, `api/processes_census_payment.go`, `api/processes_publish_payment.go`, `api/routes.go`, `api/api.go`, `api/apikeys.go` | — |
| 3 | 2 | `api/apicommon/voting_processes.go`, `api/processes_price.go`, `api/processes_signed_certificate_test.go` | — |
| 4 | 3 | `docs/swagger.yaml` | — |

Strictly sequential: each task builds on the previous one's files.

## Tasks

### Task 1: ledger record, fold and db writes
Model: standard   Repo: /home/lucasmenendez/saas-backend/.worktrees/feature-final-report-payment   Files: db/types.go, db/wallets.go, db/voting_processes.go, pricing/pricing.go, db/wallet_ledger_test.go, db/voting_processes_test.go
Goal: the `signedcert:<processId>` wallet debit, its fold into `ProcessLedgerState.SignedCertificateCents`, the flag echo write and `pricing.SignedCertCents` (AC13; the base for AC1-AC12).
Patch: .delivery/2026-11-26-final-report-payment/patches/task-1.patch

Step 1: write the failing test
```diff
diff --git a/db/voting_processes_test.go b/db/voting_processes_test.go
index b0b2b627..eed9afe5 100644
--- a/db/voting_processes_test.go
+++ b/db/voting_processes_test.go
@@ -628,3 +628,25 @@ func TestSetVotingProcessEndDate(t *testing.T) {
 	c.Assert(testDB.SetVotingProcessEndDate(id, time.Time{}), qt.Equals, ErrInvalidData)
 	c.Assert(JobTypeSetProcessEndDate.IsValid(), qt.IsTrue)
 }
+
+// TestSetVotingProcessSignedCertificate pins that the certificate echo is only written on a
+// published process: a draft selects the add-on through PUT /processes.
+func TestSetVotingProcessSignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	org := common.Address{0x12, 0xe1}
+	setupVotingProcessOrg(c, org)
+	id, err := testDB.SetVotingProcess(&VotingProcess{OrgAddress: org})
+	c.Assert(err, qt.IsNil)
+
+	c.Assert(testDB.SetVotingProcessSignedCertificate(id), qt.Equals, ErrNotFound)
+	got, err := testDB.VotingProcess(id)
+	c.Assert(err, qt.IsNil)
+	c.Assert(got.AddOns.SignedCertificate, qt.IsFalse)
+
+	c.Assert(testDB.SetVotingProcessPublished(id, "", time.Now()), qt.IsNil)
+	c.Assert(testDB.SetVotingProcessSignedCertificate(id), qt.IsNil)
+	got, err = testDB.VotingProcess(id)
+	c.Assert(err, qt.IsNil)
+	c.Assert(got.AddOns.SignedCertificate, qt.IsTrue)
+	c.Assert(testDB.SetVotingProcessSignedCertificate(bson.NilObjectID), qt.Equals, ErrInvalidData)
+}
diff --git a/db/wallet_ledger_test.go b/db/wallet_ledger_test.go
index e2fd20c0..77bea60c 100644
--- a/db/wallet_ledger_test.go
+++ b/db/wallet_ledger_test.go
@@ -538,3 +538,51 @@ func TestReleaseOwedOrgAddOns(t *testing.T) {
 		c.Assert(claimant, qt.Equals, repaid)
 	}
 }
+
+// TestSignedCertificateDebit: the certificate bought after publish is one payment per process,
+// folded into what the process paid and, apart, into SignedCertificateCents; it is refused past
+// the balance or a managed organization's spend cap, which it counts.
+func TestSignedCertificateDebit(t *testing.T) {
+	c := qt.New(t)
+	c.Cleanup(func() { c.Assert(testDB.DeleteAllDocuments(), qt.IsNil) })
+	processID, managed := bson.NewObjectID(), common.Address{0x5c}
+	cert := WalletDebit{
+		OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: pricing.SignedCertCents, ManagedOrg: managed,
+	}
+	c.Assert(testDB.DebitWalletForSignedCertificate(WalletDebit{ProcessID: processID, AmountCents: 1}),
+		qt.Equals, ErrInvalidData)
+	c.Assert(testDB.DebitWalletForSignedCertificate(cert), qt.ErrorIs, ErrInsufficientWalletBalance)
+	c.Assert(topUpTestWallet(10_000, "cs_signedcert"), qt.IsNil)
+	capCents := pricing.SignedCertCents - 1
+	c.Assert(testDB.SetManagedOrgSpendCap(testOrgAddress, managed, &capCents), qt.IsNil)
+	c.Assert(testDB.DebitWalletForSignedCertificate(cert), qt.ErrorIs, ErrManagedOrgSpendCapReached)
+	c.Assert(testDB.SetManagedOrgSpendCap(testOrgAddress, managed, nil), qt.IsNil)
+
+	// the publish, then the certificate twice: debited once
+	c.Assert(testDB.DebitWalletForProcess(WalletDebit{
+		OrgAddress: testOrgAddress, ProcessID: processID, AmountCents: 2_000, PriceCents: 2_000,
+	}), qt.IsNil)
+	c.Assert(testDB.DebitWalletForSignedCertificate(cert), qt.IsNil)
+	c.Assert(testDB.DebitWalletForSignedCertificate(cert), qt.IsNil)
+	s := processState(c, testOrgAddress, processID)
+	c.Assert(s.PaidCents, qt.Equals, 2_000+pricing.SignedCertCents)
+	c.Assert(s.SignedCertificateCents, qt.Equals, pricing.SignedCertCents)
+	c.Assert(s.AddOns, qt.HasLen, 0)
+	wallet, err := testDB.Wallet(testOrgAddress)
+	c.Assert(err, qt.IsNil)
+	c.Assert(wallet.BalanceCents, qt.Equals, 10_000-2_000-pricing.SignedCertCents)
+	c.Assert(wallet.ManagedOrgSpend(managed).SpentCents, qt.Equals, pricing.SignedCertCents)
+	row, err := testDB.WalletEntry(testOrgAddress, SignedCertificateDebitKey(processID))
+	c.Assert(err, qt.IsNil)
+	c.Assert(row.SignedCertificate, qt.IsTrue)
+	c.Assert(row.Kind, qt.Equals, WalletEntryPayment)
+	c.Assert(row.AmountCents, qt.Equals, -pricing.SignedCertCents)
+
+	// the fold over rows as stored: only the marked payment counts as the certificate
+	folded := foldProcessLedger([]WalletLedgerEntry{
+		{Kind: WalletEntryPayment, AmountCents: -2_000},
+		{Kind: WalletEntryPayment, AmountCents: -pricing.SignedCertCents, SignedCertificate: true},
+	})
+	c.Assert(folded.PaidCents, qt.Equals, 2_000+pricing.SignedCertCents)
+	c.Assert(folded.SignedCertificateCents, qt.Equals, pricing.SignedCertCents)
+}
```
Run: `go vet ./db/`
Expected: `db/voting_processes_test.go:641:18: testDB.SetVotingProcessSignedCertificate undefined (type *MongoStorage has no field or method SetVotingProcessSignedCertificate)` (and the same for `DebitWalletForSignedCertificate`, `SignedCertificateDebitKey`, `pricing.SignedCertCents`).

Step 2: implement
```diff
diff --git a/db/types.go b/db/types.go
index 6f73172a..050f6db3 100644
--- a/db/types.go
+++ b/db/types.go
@@ -1030,6 +1030,9 @@ type WalletLedgerEntry struct {
 	Attempt    int           `json:"-" bson:"attempt,omitempty"`
 	// AddOns are the once-per-organization add-ons a payment priced in and stamped.
 	AddOns []OrgAddOn `json:"-" bson:"addOns,omitempty"`
+	// SignedCertificate marks the payment that bought a published process's signed results
+	// certificate (SignedCertificateDebitKey): the record that it is paid.
+	SignedCertificate bool `json:"-" bson:"signedCertificate,omitempty"`
 	// ReleaseAddOns are the add-ons a creditBack gives back to the organization (charged and not
 	// withheld): released by whatever touches the process next until a later payment follows.
 	ReleaseAddOns []OrgAddOn `json:"-" bson:"releaseAddOns,omitempty"`
@@ -1048,6 +1051,9 @@ type ProcessLedgerState struct {
 	// creditBack rows. A credit-back settles what it returned and what it withheld, since a
 	// withheld add-on stays the organization's stamp, not the process's.
 	PaidCents pricing.Cents
+	// SignedCertificateCents is the part of PaidCents that bought the signed results certificate
+	// after publish: the certificate is paid when it is above 0.
+	SignedCertificateCents pricing.Cents
 	// Attempt is the number of publishfail credit-backs: the attempt segment of the next debit key.
 	Attempt int
 	// AddOns are the once-per-organization add-ons the live attempt's payments priced in.
diff --git a/db/voting_processes.go b/db/voting_processes.go
index 93e12cd7..e5c6b96a 100644
--- a/db/voting_processes.go
+++ b/db/voting_processes.go
@@ -520,3 +520,24 @@ func (ms *MongoStorage) SetVotingProcessEndDate(id bson.ObjectID, endDate time.T
 	}
 	return nil
 }
+
+// SetVotingProcessSignedCertificate turns the signed results certificate add-on on for a published
+// process, once its payment is in the ledger (a display echo of it). It returns ErrNotFound when
+// no published process has that id.
+func (ms *MongoStorage) SetVotingProcessSignedCertificate(id bson.ObjectID) error {
+	if id == bson.NilObjectID {
+		return ErrInvalidData
+	}
+	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
+	defer cancel()
+	res, err := ms.votingProcesses.UpdateOne(ctx,
+		bson.M{"_id": id, "published": true},
+		bson.M{"$set": bson.M{"addOns.signedCertificate": true, "updatedAt": time.Now()}})
+	if err != nil {
+		return fmt.Errorf("failed to set voting process signed certificate: %w", err)
+	}
+	if res.MatchedCount == 0 {
+		return ErrNotFound
+	}
+	return nil
+}
diff --git a/db/wallets.go b/db/wallets.go
index 007dbfe2..a3eb2436 100644
--- a/db/wallets.go
+++ b/db/wallets.go
@@ -512,24 +512,54 @@ func (ms *MongoStorage) DebitWalletForProcess(d WalletDebit) error {
 		d.PriceCents < d.AmountCents || d.ProcessID == bson.NilObjectID || d.Attempt < 0 {
 		return ErrInvalidData
 	}
-	orgAddress, amountCents := d.OrgAddress, d.AmountCents
+	return ms.debitWallet(&WalletLedgerEntry{
+		OrgAddress:        d.OrgAddress,
+		AmountCents:       -d.AmountCents,
+		Kind:              WalletEntryPayment,
+		IdempotencyKey:    walletDebitKey(d.ProcessID, d.Attempt, d.PriceCents),
+		ProcessID:         d.ProcessID,
+		ProcessOrgAddress: d.ProcessOrgAddress,
+		PriceCents:        d.PriceCents,
+		Attempt:           d.Attempt,
+		AddOns:            d.AddOns,
+	}, d.ManagedOrg)
+}
+
+// SignedCertificateDebitKey is the idempotency key of the signed results certificate bought for
+// a published process: one per process, whatever its attempt or price.
+func SignedCertificateDebitKey(processID bson.ObjectID) string {
+	return "signedcert:" + processID.Hex()
+}
+
+// DebitWalletForSignedCertificate debits d.AmountCents for the signed results certificate of a
+// published process, at most once per process (SignedCertificateDebitKey), refused as
+// DebitWalletForProcess refuses. d.PriceCents, d.Attempt and d.AddOns are ignored.
+func (ms *MongoStorage) DebitWalletForSignedCertificate(d WalletDebit) error {
+	if (d.OrgAddress.Cmp(common.Address{}) == 0) || d.AmountCents <= 0 || d.ProcessID == bson.NilObjectID {
+		return ErrInvalidData
+	}
+	return ms.debitWallet(&WalletLedgerEntry{
+		OrgAddress:        d.OrgAddress,
+		AmountCents:       -d.AmountCents,
+		Kind:              WalletEntryPayment,
+		IdempotencyKey:    SignedCertificateDebitKey(d.ProcessID),
+		ProcessID:         d.ProcessID,
+		ProcessOrgAddress: d.ProcessOrgAddress,
+		SignedCertificate: true,
+	}, d.ManagedOrg)
+}
+
+// debitWallet applies the payment entry (negative AmountCents) when the balance covers it and, for
+// a non-zero managedOrg, when it stays within that organization's spend cap, which it counts.
+func (ms *MongoStorage) debitWallet(entry *WalletLedgerEntry, managedOrg common.Address) error {
+	orgAddress, amountCents := entry.OrgAddress, -entry.AmountCents
 	m := walletMovement{
-		entry: &WalletLedgerEntry{
-			OrgAddress:        orgAddress,
-			AmountCents:       -amountCents,
-			Kind:              WalletEntryPayment,
-			IdempotencyKey:    walletDebitKey(d.ProcessID, d.Attempt, d.PriceCents),
-			ProcessID:         d.ProcessID,
-			ProcessOrgAddress: d.ProcessOrgAddress,
-			PriceCents:        d.PriceCents,
-			Attempt:           d.Attempt,
-			AddOns:            d.AddOns,
-		},
+		entry:  entry,
 		filter: bson.M{"balanceCents": bson.M{"$gte": amountCents}},
 		refused: func(ctx context.Context) error {
-			if d.ManagedOrg != (common.Address{}) {
+			if managedOrg != (common.Address{}) {
 				over, err := ms.wallets.CountDocuments(ctx, bson.M{
-					"_id": orgAddress, "$expr": bson.M{"$not": bson.A{withinSpendCap(d.ManagedOrg, amountCents)}},
+					"_id": orgAddress, "$expr": bson.M{"$not": bson.A{withinSpendCap(managedOrg, amountCents)}},
 				})
 				if err != nil {
 					return fmt.Errorf("failed to check managed organization spend cap: %w", err)
@@ -541,9 +571,9 @@ func (ms *MongoStorage) DebitWalletForProcess(d WalletDebit) error {
 			return ErrInsufficientWalletBalance
 		},
 	}
-	if d.ManagedOrg != (common.Address{}) {
-		m.filter["$expr"] = withinSpendCap(d.ManagedOrg, amountCents)
-		m.inc = bson.M{orgSpendField(d.ManagedOrg) + ".spentCents": amountCents}
+	if managedOrg != (common.Address{}) {
+		m.filter["$expr"] = withinSpendCap(managedOrg, amountCents)
+		m.inc = bson.M{orgSpendField(managedOrg) + ".spentCents": amountCents}
 	}
 	return ms.applyWalletEntry(m)
 }
@@ -587,6 +617,9 @@ func foldProcessLedger(rows []WalletLedgerEntry) *ProcessLedgerState {
 		switch row.Kind {
 		case WalletEntryPayment:
 			state.PaidCents -= row.AmountCents
+			if row.SignedCertificate {
+				state.SignedCertificateCents -= row.AmountCents
+			}
 			for _, addOn := range row.AddOns {
 				if !slices.Contains(state.AddOns, addOn) {
 					state.AddOns = append(state.AddOns, addOn)
diff --git a/pricing/pricing.go b/pricing/pricing.go
index 1b0c7644..91bdb704 100644
--- a/pricing/pricing.go
+++ b/pricing/pricing.go
@@ -44,8 +44,10 @@ const (
 const (
 	// email 2FA is free for standard organizations; integrators pay it per eligible voter
 	integratorEmailTwoFAPerVoter = 1 * Cent
-	smsTwoFAPerVoter             = 5 * Cent  // per eligible voter
-	signedCertPrice              = 39 * Euro // per process
+	smsTwoFAPerVoter             = 5 * Cent // per eligible voter
+	// SignedCertCents is the signed results certificate, per process, exported for its purchase
+	// after publish, which is never re-quoted from the census.
+	SignedCertCents = 39 * Euro
 	// BrandingCents and CustomURLCents are the once-per-organization add-ons, exported for
 	// refunds, which withhold them when another process relies on them.
 	BrandingCents  = 149 * Euro
@@ -149,7 +151,7 @@ func Compute(in QuoteInput) (Quote, error) {
 		addLine(LineSMSTwoFA, "SMS 2FA", Cents(in.CensusSize)*smsTwoFAPerVoter)
 	}
 	if in.SignedCert {
-		addLine(LineSignedCert, "signed results certificate", signedCertPrice)
+		addLine(LineSignedCert, "signed results certificate", SignedCertCents)
 	}
 	if in.CustomURL {
 		addLine(LineCustomURL, "custom URL", CustomURLCents)
```
Run: `go build ./... && go test ./db/ ./pricing/ -count=1`
Expected: `ok  	github.com/vocdoni/saas-backend/db` and `ok  	github.com/vocdoni/saas-backend/pricing`
(narrowed: `go test ./db/ -run 'TestSignedCertificateDebit|TestSetVotingProcessSignedCertificate|TestProcessLedgerStateFold|TestWallet' -count=1 -v` → 16 `--- PASS`, 0 FAIL).

Step 3: commit
`git -c commit.gpgsign=false commit -m "feat(db): signed certificate wallet debit and ledger fold" -- db/types.go db/wallets.go db/voting_processes.go pricing/pricing.go db/wallet_ledger_test.go db/voting_processes_test.go`

Interfaces this task provides to later tasks:
- `func (ms *MongoStorage) DebitWalletForSignedCertificate(d WalletDebit) error` (uses `OrgAddress`, `ProcessID`, `ProcessOrgAddress`, `AmountCents`, `ManagedOrg`; returns `ErrInsufficientWalletBalance`, `ErrManagedOrgSpendCapReached`, `ErrInvalidData`)
- `func SignedCertificateDebitKey(processID bson.ObjectID) string` → `"signedcert:<hex>"`
- `func (ms *MongoStorage) SetVotingProcessSignedCertificate(id bson.ObjectID) error` (ErrNotFound unless published)
- `ProcessLedgerState.SignedCertificateCents pricing.Cents`, `WalletLedgerEntry.SignedCertificate bool` (`bson:"signedCertificate,omitempty"`, `json:"-"`)
- `pricing.SignedCertCents` (3900)

### Task 2: POST /processes/{processId}/signed-certificate
Model: strong   Repo: /home/lucasmenendez/saas-backend/.worktrees/feature-final-report-payment   Files: api/processes_signed_certificate.go, api/processes_signed_certificate_test.go, api/processes_census_payment.go, api/processes_publish_payment.go, api/routes.go, api/api.go, api/apikeys.go
Goal: the purchase endpoint, its pricing guards (quote, grandfather) and refusals (AC1-AC4, AC6-AC8, AC10-AC12).
Patch: .delivery/2026-11-26-final-report-payment/patches/task-2.patch

Step 1: write the failing test
```diff
diff --git a/api/processes_signed_certificate_test.go b/api/processes_signed_certificate_test.go
new file mode 100644
index 00000000..e8787dab
--- /dev/null
+++ b/api/processes_signed_certificate_test.go
@@ -0,0 +1,276 @@
+package api
+
+import (
+	"context"
+	"encoding/json"
+	"net/http"
+	"sync"
+	"testing"
+	"time"
+
+	"github.com/ethereum/go-ethereum/common"
+	qt "github.com/frankban/quicktest"
+	"github.com/vocdoni/saas-backend/api/apicommon"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/errors"
+	"github.com/vocdoni/saas-backend/pricing"
+	"go.mongodb.org/mongo-driver/v2/bson"
+)
+
+// newPublishedPaidProcess creates a process of growthBaseVoters fresh members without 2FA in
+// orgAddress, pays it from the wallet and marks it published with a €0 base, the state its publish
+// leaves, without the chain. It returns the process.
+func newPublishedPaidProcess(t *testing.T, token string, orgAddress common.Address) bson.ObjectID {
+	t.Helper()
+	c := qt.New(t)
+	req := newVotingProcessRequest(orgAddress, postNewOrgMembers(t, token, orgAddress, growthBaseVoters))
+	req.Census.TwoFaFields = nil
+	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
+	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
+		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
+	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthBaseVoters))
+	oid := objectID(c, pid)
+	c.Assert(testDB.SetVotingProcessPublished(oid, "", time.Now()), qt.IsNil)
+	_, err := testDB.SetProcessPaymentBase(oid, orgAddress, 0)
+	c.Assert(err, qt.IsNil)
+	return oid
+}
+
+// buyCert calls POST /processes/{processId}/signed-certificate and expects a 200.
+func buyCert(t *testing.T, token string, oid bson.ObjectID) pricing.Cents {
+	t.Helper()
+	return requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token, nil,
+		"processes", oid.Hex(), "signed-certificate").AmountCents
+}
+
+// growthRefusal is the 402 data the growth guard answers for growing oid's census to size.
+func growthRefusal(c *qt.C, oid bson.ObjectID, size int64) apicommon.ProcessCensusGrowthQuote {
+	vp, err := testDB.VotingProcess(oid)
+	c.Assert(err, qt.IsNil)
+	census, err := testDB.Census(vp.CensusID.Hex())
+	c.Assert(err, qt.IsNil)
+	err = testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, size: size})
+	var apiErr errors.Error
+	c.Assert(errors.As(err, &apiErr), qt.IsTrue, qt.Commentf("error: %v", err))
+	quote, ok := apiErr.Data.(apicommon.ProcessCensusGrowthQuote)
+	c.Assert(ok, qt.IsTrue)
+	return quote
+}
+
+// setCensusSize overwrites the stored size of oid's census, as a path the growth guard does not
+// see would leave it.
+func setCensusSize(c *qt.C, oid bson.ObjectID, size int64) {
+	vp, err := testDB.VotingProcess(oid)
+	c.Assert(err, qt.IsNil)
+	_, err = testDB.DBClient.Database(testDBName).Collection("census").UpdateOne(context.Background(),
+		bson.M{"_id": vp.CensusID}, bson.M{"$set": bson.M{"size": size}})
+	c.Assert(err, qt.IsNil)
+}
+
+// assertBalance asserts wallet's balance.
+func assertBalance(c *qt.C, wallet common.Address, cents pricing.Cents) {
+	got, err := testDB.Wallet(wallet)
+	c.Assert(err, qt.IsNil)
+	c.Assert(got.BalanceCents, qt.Equals, cents)
+}
+
+// TestBuySignedCertificate: a published process buys the certificate once, from its wallet; a
+// short wallet gets the shortfall to top up and nothing moves; the 3900 never reads as headroom.
+func TestBuySignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcert12345")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	oid := newPublishedPaidProcess(t, token, orgAddress)
+	paidCents := growthCents(t, growthBaseVoters)
+
+	// a draft selects it with PUT; another organization's user and an unknown process are refused
+	draft := newPricedVotingProcess(t, token, orgAddress)
+	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token, nil, "processes", draft, "signed-certificate")
+	other := testCreateUser(t, "signedcertother1")
+	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, other, nil,
+		"processes", oid.Hex(), "signed-certificate")
+	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodPost, token, nil,
+		"processes", bson.NewObjectID().Hex(), "signed-certificate")
+
+	// an empty wallet: 402 with what to top up, nothing debited
+	short := requestAndParseWithAssertCode[struct {
+		Code    int                              `json:"code"`
+		Message string                           `json:"error"`
+		Data    apicommon.ProcessPaymentRequired `json:"data"`
+	}](http.StatusPaymentRequired, t, http.MethodPost, token, nil, "processes", oid.Hex(), "signed-certificate")
+	c.Assert(short.Code, qt.Equals, errors.ErrPaymentRequired.Code)
+	c.Assert(short.Message, qt.Contains, "signed results certificate")
+	c.Assert(short.Data.Lines, qt.DeepEquals, []pricing.QuoteLine{{
+		Kind: pricing.LineSignedCert, Description: "signed results certificate", AmountCents: pricing.SignedCertCents,
+	}})
+	c.Assert(short.Data.TotalCents, qt.Equals, pricing.SignedCertCents)
+	c.Assert(short.Data.BalanceCents, qt.Equals, pricing.Cents(0))
+	c.Assert(short.Data.ShortfallCents, qt.Equals, pricing.SignedCertCents)
+	_, err := testDB.WalletEntry(orgAddress, db.SignedCertificateDebitKey(oid))
+	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
+
+	// topped up: debited once, recorded on the ledger and echoed on the process
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
+	assertBalance(c, orgAddress, 0)
+	row, err := testDB.WalletEntry(orgAddress, db.SignedCertificateDebitKey(oid))
+	c.Assert(err, qt.IsNil)
+	c.Assert(row.SignedCertificate, qt.IsTrue)
+	c.Assert(row.AmountCents, qt.Equals, -pricing.SignedCertCents)
+	vp, err := testDB.VotingProcess(oid)
+	c.Assert(err, qt.IsNil)
+	c.Assert(vp.AddOns.SignedCertificate, qt.IsTrue)
+	payment, err := testDB.ProcessPayment(oid)
+	c.Assert(err, qt.IsNil)
+	c.Assert(payment.AmountCents, qt.Equals, paidCents+pricing.SignedCertCents)
+
+	// a second call debits nothing
+	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.Cents(0))
+	assertBalance(c, orgAddress, 0)
+
+	// growth is charged the growth only
+	refusal := growthRefusal(c, oid, growthTierEnd)
+	c.Assert(refusal.PaidCents, qt.Equals, paidCents+pricing.SignedCertCents)
+	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-paidCents)
+}
+
+// TestSignedCertificateFlatPrice: the certificate costs 3900 whatever the census did after the
+// publish: unused headroom, a census that shrank, or one that grew past what was paid.
+func TestSignedCertificateFlatPrice(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertflat12")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	paidCents := growthCents(t, growthBaseVoters)
+
+	headroom := newPublishedPaidProcess(t, token, orgAddress)
+	buyHeadroom(t, headroom, paidCents)
+	shrunk := newPublishedPaidProcess(t, token, orgAddress)
+	setCensusSize(c, shrunk, 1)
+	grown := newPublishedPaidProcess(t, token, orgAddress)
+	setCensusSize(c, grown, growthTotalVoters)
+
+	for _, oid := range []bson.ObjectID{headroom, shrunk, grown} {
+		fundWallet(t, orgAddress, pricing.SignedCertCents)
+		c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
+		assertBalance(c, orgAddress, 0)
+	}
+}
+
+// TestGrandfatheredSignedCertificate: a published process with no base gets it from its census
+// without the certificate, then pays the 3900; later growth is charged the growth only.
+func TestGrandfatheredSignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertgrand1")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	oid := newPublishedPaidProcess(t, token, orgAddress)
+	// what a process published before pay-per-process looks like: no payment row, no movements
+	database := testDB.DBClient.Database(testDBName)
+	_, err := database.Collection("processPayments").DeleteOne(context.Background(), bson.M{"_id": oid})
+	c.Assert(err, qt.IsNil)
+	_, err = database.Collection("walletLedger").DeleteMany(context.Background(), bson.M{"processId": oid})
+	c.Assert(err, qt.IsNil)
+
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
+	baseCents := growthCents(t, growthBaseVoters)
+	payment, err := testDB.ProcessPayment(oid)
+	c.Assert(err, qt.IsNil)
+	c.Assert(payment.BaseCents, qt.DeepEquals, &baseCents)
+
+	refusal := growthRefusal(c, oid, growthTierEnd)
+	c.Assert(refusal.PaidCents, qt.Equals, baseCents+pricing.SignedCertCents)
+	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-baseCents)
+}
+
+// TestConcurrentSignedCertificate: two purchases racing debit the certificate once; both succeed.
+func TestConcurrentSignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertrace12")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	oid := newPublishedPaidProcess(t, token, orgAddress)
+	fundWallet(t, orgAddress, 2*pricing.SignedCertCents)
+
+	debited := make(chan pricing.Cents, 2)
+	var wg sync.WaitGroup
+	for range 2 {
+		wg.Go(func() { debited <- buyCert(t, token, oid) })
+	}
+	wg.Wait()
+	close(debited)
+	var total pricing.Cents
+	for cents := range debited {
+		total += cents
+	}
+	c.Assert(total, qt.Equals, pricing.SignedCertCents)
+	assertBalance(c, orgAddress, pricing.SignedCertCents)
+}
+
+// TestManagedSignedCertificate: a managed organization's certificate is debited from its
+// integrator's wallet: 40175 when short, 40183 past its spend cap, which it counts towards. An API
+// key buys it with voting:write only.
+func TestManagedSignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	installFakePaymentGW(t)
+	token := testCreateUser(t, "signedcertmanage")
+	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token)
+	oid := newPublishedPaidProcess(t, token, managedAddr)
+
+	short := requestAndParseWithAssertCode[struct {
+		Code int                      `json:"code"`
+		Data map[string]pricing.Cents `json:"data"`
+	}](http.StatusPaymentRequired, t, http.MethodPost, token, nil, "processes", oid.Hex(), "signed-certificate")
+	c.Assert(short.Code, qt.Equals, errors.ErrInsufficientWalletBalance.Code)
+	c.Assert(short.Data, qt.DeepEquals, map[string]pricing.Cents{
+		"requiredCents": pricing.SignedCertCents, "availableCents": 0,
+	})
+
+	fundWallet(t, integratorAddr, pricing.SignedCertCents)
+	wallet, err := testDB.Wallet(integratorAddr)
+	c.Assert(err, qt.IsNil)
+	spent := wallet.ManagedOrgSpend(managedAddr).SpentCents
+	capCents := spent + pricing.SignedCertCents - 1
+	c.Assert(testDB.SetManagedOrgSpendCap(integratorAddr, managedAddr, &capCents), qt.IsNil)
+	requestAndAssertError(errors.ErrManagedOrgSpendCapReached, t, http.MethodPost, token, nil,
+		"processes", oid.Hex(), "signed-certificate")
+	c.Assert(testDB.SetManagedOrgSpendCap(integratorAddr, managedAddr, nil), qt.IsNil)
+
+	mintKey := func(scopes ...string) string {
+		data, code := testRequest(t, http.MethodPost, token,
+			&apicommon.CreateAPIKeyRequest{Label: "cert", Scopes: scopes},
+			"integrator", "organizations", integratorAddr.String(), "apikeys")
+		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
+		var created apicommon.CreateAPIKeyResponse
+		c.Assert(json.Unmarshal(data, &created), qt.IsNil)
+		return created.Secret
+	}
+	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, mintKey(ScopeQuotaRead), nil,
+		"processes", oid.Hex(), "signed-certificate")
+	c.Assert(buyCert(t, mintKey(ScopeVotingWrite), oid), qt.Equals, pricing.SignedCertCents)
+	wallet, err = testDB.Wallet(integratorAddr)
+	c.Assert(err, qt.IsNil)
+	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
+	c.Assert(wallet.ManagedOrgSpend(managedAddr).SpentCents, qt.Equals, spent+pricing.SignedCertCents)
+}
+
+// TestSignedCertificateEndedAndQuoteOnly: an ended process and a quote-only one (published
+// outside the wallet, so grandfathered at a €0 base) buy the certificate like any other.
+func TestSignedCertificateEndedAndQuoteOnly(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertended1")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+
+	ended := newPublishedPaidProcess(t, token, orgAddress)
+	c.Assert(testDB.SetVotingProcessEndDate(ended, time.Now().Add(-time.Hour)), qt.IsNil)
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	c.Assert(buyCert(t, token, ended), qt.Equals, pricing.SignedCertCents)
+
+	quoteOnly := objectID(c, newPricedVotingProcess(t, token, orgAddress))
+	c.Assert(testDB.SetVotingProcessPublished(quoteOnly, "", time.Now()), qt.IsNil)
+	setCensusSize(c, quoteOnly, 50_001)
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	c.Assert(buyCert(t, token, quoteOnly), qt.Equals, pricing.SignedCertCents)
+	payment, err := testDB.ProcessPayment(quoteOnly)
+	c.Assert(err, qt.IsNil)
+	c.Assert(payment.BaseCents, qt.DeepEquals, new(pricing.Cents))
+	assertBalance(c, orgAddress, 0)
+}
```
Run: `go test ./api/ -run TestBuySignedCertificate -count=1 2>&1 | grep -E '^(--- |FAIL)|got:' -A1`
Expected: `--- FAIL: TestBuySignedCertificate` with `got: int(404)` / `want: int(400)` (the route is not registered).

Step 2: implement
```diff
diff --git a/api/api.go b/api/api.go
index c8a119b8..c1f33d83 100644
--- a/api/api.go
+++ b/api/api.go
@@ -539,6 +539,7 @@ func (a *API) initRouter() http.Handler {
 		handle(r, http.MethodGet, processesCheckoutEndpoint, a.processCheckoutStatusHandler)
 		handle(r, http.MethodDelete, processesCheckoutEndpoint, a.cancelProcessCheckoutHandler)
 		handle(r, http.MethodPost, processesCensusCheckoutEndpoint, a.createProcessCensusCheckoutHandler)
+		handle(r, http.MethodPost, processesSignedCertificateEndpoint, a.buyProcessSignedCertificateHandler)
 		handle(r, http.MethodPut, processesQuestionsStatusEndpoint, a.setVotingProcessQuestionsStatusHandler)
 		handle(r, http.MethodPut, processesQuestionStatusEndpoint, a.setVotingProcessQuestionStatusHandler)
 		handle(r, http.MethodPut, processesEndDateEndpoint, a.setVotingProcessEndDateHandler)
diff --git a/api/apikeys.go b/api/apikeys.go
index eadc2bec..78009b88 100644
--- a/api/apikeys.go
+++ b/api/apikeys.go
@@ -98,6 +98,8 @@ var apiKeyAllowlist = map[string]string{
 	// managed organization never opens a top-up, so the top-up branch is never reached with one;
 	// the per-organization role check still gates it.
 	"POST " + processesCensusCheckoutEndpoint: ScopeVotingWrite,
+	// debited from the wallet, never a checkout
+	"POST " + processesSignedCertificateEndpoint: ScopeVotingWrite,
 }
 
 // requiredScopeForRoute returns the scope required to call (method, pattern) with an API key and
diff --git a/api/processes_census_payment.go b/api/processes_census_payment.go
index 6684246d..6e43f151 100644
--- a/api/processes_census_payment.go
+++ b/api/processes_census_payment.go
@@ -26,6 +26,8 @@ func quoteProcessAtCensusSize(p processCensusProjection, size int64) (pricing.Qu
 	input := processQuoteInput(p.vp, &grown, p.org)
 	input.Branding = p.paid.Charged(db.OrgAddOnBranding)
 	input.CustomURL = p.paid.Charged(db.OrgAddOnCustomURL)
+	// a certificate bought after publish is in the ledger before its flag is echoed
+	input.SignedCert = input.SignedCert || p.paid.SignedCertificateCents > 0
 	quote, err := pricing.Compute(input)
 	if err != nil {
 		return pricing.Quote{}, errors.ErrMalformedBody.WithErr(err)
@@ -84,7 +86,8 @@ func (a *API) paidProcessProjection(
 func (a *API) grandfatherProcessPayment(p processCensusProjection) (processPaid, error) {
 	vp, census, org, paid := p.vp, p.census, p.org, p.paid
 	var baseCents pricing.Cents
-	if paid.PaidCents == 0 {
+	// a certificate bought after publish is no payment for the census
+	if paid.PaidCents-paid.SignedCertificateCents == 0 {
 		quote, err := quoteProcessAtCensusSize(processCensusProjection{
 			vp: vp, census: census, org: org, paid: processPaid{ProcessLedgerState: &db.ProcessLedgerState{}},
 		}, census.Size)
diff --git a/api/processes_publish_payment.go b/api/processes_publish_payment.go
index c3392e5a..81a89853 100644
--- a/api/processes_publish_payment.go
+++ b/api/processes_publish_payment.go
@@ -41,6 +41,9 @@ func walletShortError(s walletShort) error {
 		BalanceCents:   s.balanceCents,
 		ShortfallCents: s.dueCents - s.balanceCents,
 	}}
+	if s.message != "" {
+		return errors.ErrPaymentRequired.Withf("%s", s.message).WithData(required)
+	}
 	if s.paidCents > 0 {
 		// a paid draft that outgrew its price has no process checkout to open (409): it buys the
 		// difference for the census it has now
@@ -63,6 +66,32 @@ type walletShort struct {
 	// is priced at, which census/checkout takes
 	paidCents  pricing.Cents
 	censusSize int64
+	// message, when set, replaces the default message of the 40178
+	message string
+}
+
+// walletDebitError maps a refused debit of s.dueCents from s.org's payer's wallet to the answer
+// the client acts on: spendCapError (402, 40183) or walletShortError at the balance it reads now.
+func (a *API) walletDebitError(err error, s walletShort) error {
+	payer := walletPayer(s.org)
+	switch {
+	case errors.Is(err, db.ErrManagedOrgSpendCapReached):
+		if wallet, werr := a.db.Wallet(payer); werr == nil {
+			if capErr := spendCapError(wallet.ManagedOrgSpend(s.org.Address), s.dueCents); capErr != nil {
+				return capErr
+			}
+		}
+		return errors.ErrManagedOrgSpendCapReached
+	case errors.Is(err, db.ErrInsufficientWalletBalance):
+		wallet, werr := a.db.Wallet(payer)
+		if werr != nil {
+			return fmt.Errorf("wallet short, and its balance could not be read: %w", werr)
+		}
+		s.balanceCents = wallet.BalanceCents
+		return walletShortError(s)
+	default:
+		return fmt.Errorf("failed to debit wallet: %w", err)
+	}
 }
 
 // processPaid is what a process has paid (ProcessLedgerState, its payer's wallet ledger folded)
@@ -441,24 +470,7 @@ func (a *API) chargeProcessWallet(c processCharge) (pricing.Cents, error) {
 		AddOns:            addOns,
 		ManagedOrg:        managedOrg,
 	}); err != nil {
-		if errors.Is(err, db.ErrManagedOrgSpendCapReached) {
-			if wallet, werr := a.db.Wallet(payer); werr == nil {
-				if capErr := spendCapError(wallet.ManagedOrgSpend(c.vp.OrgAddress), dueCents); capErr != nil {
-					return 0, capErr
-				}
-			}
-			return 0, errors.ErrManagedOrgSpendCapReached
-		}
-		if errors.Is(err, db.ErrInsufficientWalletBalance) {
-			wallet, werr := a.db.Wallet(payer)
-			if werr != nil {
-				return 0, fmt.Errorf("wallet short, and its balance could not be read: %w", werr)
-			}
-			return 0, walletShortError(walletShort{
-				org: c.org, quote: c.quote, dueCents: dueCents, balanceCents: wallet.BalanceCents,
-			})
-		}
-		return 0, fmt.Errorf("failed to debit wallet: %w", err)
+		return 0, a.walletDebitError(err, walletShort{org: c.org, quote: c.quote, dueCents: dueCents})
 	}
 	// the add-ons the process paid before are stamped again too (idempotent)
 	return dueCents, a.completeProcessCharge(c.vp, slices.Concat(paid.AddOns, addOns), alreadyCents+dueCents)
@@ -512,6 +524,8 @@ const (
 	moneyStepPaymentDrop
 	// moneyStepDraftDelete: a delete drops the draft.
 	moneyStepDraftDelete
+	// moneyStepSignedCert: a certificate purchase echoes the add-on on the process.
+	moneyStepSignedCert
 )
 
 // moneyStepHook, when set (tests only), runs before each step of a money path that follows its
diff --git a/api/processes_signed_certificate.go b/api/processes_signed_certificate.go
new file mode 100644
index 00000000..bce5aeed
--- /dev/null
+++ b/api/processes_signed_certificate.go
@@ -0,0 +1,145 @@
+package api
+
+import (
+	"context"
+	"fmt"
+	"net/http"
+
+	"github.com/ethereum/go-ethereum/common"
+	"github.com/vocdoni/saas-backend/api/apicommon"
+	"github.com/vocdoni/saas-backend/db"
+	"github.com/vocdoni/saas-backend/errors"
+	"github.com/vocdoni/saas-backend/pricing"
+	"go.vocdoni.io/dvote/log"
+)
+
+// buyProcessSignedCertificateHandler godoc
+//
+//	@Summary		Buy the signed results certificate of a published process
+//	@Description	Debits the signed results certificate add-on (€39, flat: never re-priced from the census)
+//	@Description	from the payer's wallet for a published process, open or ended, quote-only included, and
+//	@Description	turns addOns.signedCertificate on. A process that already has it is debited nothing
+//	@Description	(amountCents 0). A draft selects the add-on with PUT /processes/{processId} instead (400).
+//	@Description	No checkout is opened: when the wallet falls short a standard organization gets 402 (40178)
+//	@Description	with the shortfall, tops its wallet up by at least shortfallCents and calls again; a managed
+//	@Description	organization gets 402 (40175) or, past its spend cap, 402 (40183).
+//	@Description	GET /processes/{processId}/price tells whether it is paid (signedCertificatePaid).
+//	@Description	Requires Admin role. Also callable with a scoped API key (scope: `voting:write`).
+//	@Tags			processes
+//	@Produce		json
+//	@Security		BearerAuth
+//	@Param			processId	path		string	true	"Process ID"
+//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
+//	@Failure		400			{object}	errors.Error	"The process is a draft"
+//	@Failure		401			{object}	errors.Error
+//	@Failure		402			{object}	errors.Error	"Wallet short (40178, 40175), or spend cap reached (40183)"
+//	@Failure		404			{object}	errors.Error
+//	@Failure		409			{object}	errors.Error	"Open checkout or refunded (40177), or organization being deleted (40901)"
+//	@Failure		500			{object}	errors.Error
+//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
+//	@Router			/processes/{processId}/signed-certificate [post]
+func (a *API) buyProcessSignedCertificateHandler(w http.ResponseWriter, r *http.Request) {
+	oid, ok := a.votingProcessID(w, r)
+	if !ok {
+		return
+	}
+	user, ok := apicommon.UserFromContext(r.Context())
+	if !ok {
+		errors.ErrUnauthorized.Write(w)
+		return
+	}
+	vp, ok := a.loadVotingProcess(w, oid)
+	if !ok {
+		return
+	}
+	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
+		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
+		return
+	}
+	if !vp.Published {
+		errors.ErrMalformedBody.Withf("the process is a draft: select the add-on with PUT /processes/{processId}").Write(w)
+		return
+	}
+	census, err := a.db.Census(vp.CensusID.Hex())
+	if err != nil {
+		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
+		return
+	}
+	// the base is written from the stored process before any certificate debit, so a
+	// grandfathered base never prices the certificate in
+	p, projected, err := a.paidProcessProjection(vp, census, nil)
+	if err == nil && !projected {
+		err = errors.ErrGenericInternalServerError.Withf("published process %s has no payment base", oid.Hex())
+	}
+	if err != nil {
+		writeAPIError(w, err)
+		return
+	}
+	debited, err := a.buySignedCertificate(vp, p.org)
+	if err != nil {
+		writeAPIError(w, err)
+		return
+	}
+	log.Infow("signed certificate bought", "processId", oid.Hex(), "debitedCents", debited)
+	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{AmountCents: debited, Currency: "eur"})
+}
+
+// buySignedCertificate debits the certificate of the published vp from org's payer's wallet unless
+// it is paid already, and returns what it debited. The ledger row is the record that it is paid,
+// so it is read and written under the payer's wallet lock; the flag is its echo, redone by a retry.
+func (a *API) buySignedCertificate(vp *db.VotingProcess, org *db.Organization) (pricing.Cents, error) {
+	payer := walletPayer(org)
+	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
+	if err != nil {
+		return 0, err
+	}
+	defer walletLock.Unlock()
+	if err := a.refuseTearingDown(vp.OrgAddress); err != nil {
+		return 0, err
+	}
+	paid, err := a.processPaidState(vp.ID, payer)
+	if err != nil {
+		return 0, err
+	}
+	if paid.Refunded {
+		return 0, errRefundedDraft
+	}
+	if paid.openCheckout() {
+		return 0, errors.ErrPaymentSessionConflict.Withf(
+			"process %s has an open top-up checkout the wallet cannot pay for", vp.ID.Hex())
+	}
+	var debited pricing.Cents
+	if !vp.AddOns.SignedCertificate && paid.SignedCertificateCents == 0 {
+		var managedOrg common.Address
+		if org.ManagedBy != (common.Address{}) {
+			managedOrg = vp.OrgAddress
+		}
+		if err := a.db.DebitWalletForSignedCertificate(db.WalletDebit{
+			OrgAddress:        payer,
+			ProcessID:         vp.ID,
+			ProcessOrgAddress: vp.OrgAddress,
+			AmountCents:       pricing.SignedCertCents,
+			ManagedOrg:        managedOrg,
+		}); err != nil {
+			line := pricing.QuoteLine{
+				Kind: pricing.LineSignedCert, Description: "signed results certificate", AmountCents: pricing.SignedCertCents,
+			}
+			return 0, a.walletDebitError(err, walletShort{
+				org:      org,
+				quote:    pricing.Quote{Lines: []pricing.QuoteLine{line}, TotalCents: pricing.SignedCertCents},
+				dueCents: pricing.SignedCertCents,
+				message: "the wallet does not cover the signed results certificate; " +
+					"top it up by at least shortfallCents and call again",
+			})
+		}
+		debited = pricing.SignedCertCents
+	}
+	if err := atMoneyStep(moneyStepSignedCert, vp.ID); err != nil {
+		return 0, err
+	}
+	if err := a.db.SetVotingProcessSignedCertificate(vp.ID); err != nil {
+		return 0, fmt.Errorf("failed to set the signed certificate add-on: %w", err)
+	}
+	a.refreshPaymentCache(vp, db.ProcessPaymentPaid, paid.cents()+debited)
+	return debited, nil
+}
diff --git a/api/routes.go b/api/routes.go
index f503dc33..8cf7c6dc 100644
--- a/api/routes.go
+++ b/api/routes.go
@@ -178,6 +178,8 @@ const (
 	processesCheckoutEndpoint = "/processes/{processId}/checkout"
 	// POST buys census headroom for an already-paid process from the wallet (raises the paid envelope)
 	processesCensusCheckoutEndpoint = "/processes/{processId}/census/checkout"
+	// POST buys the signed results certificate of a published process from the wallet
+	processesSignedCertificateEndpoint = "/processes/{processId}/signed-certificate"
 	// POST /processes/{processId}/check voter eligibility/status (public CSP)
 	processesCheckEndpoint = "/processes/{processId}/check"
 	// POST /processes/{processId}/publish to publish the process (one election per question)
```
Run: `go build ./... && go vet ./api/ && go test ./api/ -run 'TestBuySignedCertificate|TestSignedCertificateFlatPrice|TestGrandfatheredSignedCertificate|TestConcurrentSignedCertificate|TestManagedSignedCertificate|TestSignedCertificateEndedAndQuoteOnly' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: 6 `--- PASS` (TestBuySignedCertificate, TestSignedCertificateFlatPrice, TestGrandfatheredSignedCertificate, TestConcurrentSignedCertificate, TestManagedSignedCertificate, TestSignedCertificateEndedAndQuoteOnly), `ok  	github.com/vocdoni/saas-backend/api`.

Regression for the `walletDebitError` extraction and the quote change (unchanged behaviour):
Run: `go test ./api/ -run 'TestManagedOrgSpendCap|TestSpendCapRefusalReleasesAddOnClaims|TestManagedCensusGrowthChargesIntegratorWallet|TestManagedProcessWalletPublish|TestGrandfatheredProcessCensusGrowth|TestPublishedCensusGrowthWithinPaidPrice|TestCensusCheckoutChargesTheMinimum|TestCensusCheckoutRefusesQuoteOnlySize|TestVotingProcessPublishPaymentGate|TestIntegratorAPIKeyReadsPrice' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: 10 `--- PASS`, `ok  	github.com/vocdoni/saas-backend/api` (about 25 s).

Run: `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=9bdf27a9 ./... && ./scripts/check-qt-patterns.sh`
Expected: `0 issues.` and `✅ No qt anti-patterns found!`

Step 3: commit
`git add -- api/processes_signed_certificate.go api/processes_signed_certificate_test.go && git -c commit.gpgsign=false commit -m "feat(processes): buy the signed certificate of a published process" -- api/processes_signed_certificate.go api/processes_signed_certificate_test.go api/processes_census_payment.go api/processes_publish_payment.go api/routes.go api/api.go api/apikeys.go`

Interfaces this task provides to later tasks:
- route constant `processesSignedCertificateEndpoint = "/processes/{processId}/signed-certificate"`, handler `buyProcessSignedCertificateHandler`
- `moneyStepSignedCert` (test hook step before the echo)
- `func (a *API) walletDebitError(err error, s walletShort) error`; `walletShort.message`
- test helpers in `api/processes_signed_certificate_test.go`: `newPublishedPaidProcess`, `buyCert`, `growthRefusal`, `setCensusSize`, `assertBalance`

### Task 3: GET /processes/{processId}/price reads the certificate
Model: standard   Repo: /home/lucasmenendez/saas-backend/.worktrees/feature-final-report-payment   Files: api/apicommon/voting_processes.go, api/processes_price.go, api/processes_signed_certificate_test.go
Goal: `signedCertificatePaid` and `signedCertificateDueCents` on `ProcessPriceResponse`, read off the ledger (AC5, AC9).
Patch: .delivery/2026-11-26-final-report-payment/patches/task-3.patch

Step 1: write the failing test
```diff
diff --git a/api/processes_signed_certificate_test.go b/api/processes_signed_certificate_test.go
index e8787dab..49ae571a 100644
--- a/api/processes_signed_certificate_test.go
+++ b/api/processes_signed_certificate_test.go
@@ -274,3 +274,66 @@ func TestSignedCertificateEndedAndQuoteOnly(t *testing.T) {
 	c.Assert(payment.BaseCents, qt.DeepEquals, new(pricing.Cents))
 	assertBalance(c, orgAddress, 0)
 }
+
+// processPrice reads GET /processes/{processId}/price.
+func processPrice(t *testing.T, token string, oid bson.ObjectID) apicommon.ProcessPriceResponse {
+	t.Helper()
+	return requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, token, nil, "processes", oid.Hex(), "price")
+}
+
+// TestPriceReadsSignedCertificate: GET /price says whether the certificate of a published process
+// is paid and, until it is, what buying it debits; a draft reads unpaid with nothing due.
+func TestPriceReadsSignedCertificate(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertprice1")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	draft := objectID(c, newPricedVotingProcess(t, token, orgAddress))
+	price := processPrice(t, token, draft)
+	c.Assert(price.SignedCertificatePaid, qt.IsFalse)
+	c.Assert(price.SignedCertificateDueCents, qt.IsNil)
+
+	oid := newPublishedPaidProcess(t, token, orgAddress)
+	price = processPrice(t, token, oid)
+	c.Assert(price.SignedCertificatePaid, qt.IsFalse)
+	c.Assert(price.SignedCertificateDueCents, qt.DeepEquals, new(pricing.SignedCertCents))
+
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
+	price = processPrice(t, token, oid)
+	c.Assert(price.SignedCertificatePaid, qt.IsTrue)
+	c.Assert(price.SignedCertificateDueCents, qt.IsNil)
+	kinds := make([]string, 0, len(price.Lines))
+	for _, line := range price.Lines {
+		kinds = append(kinds, line.Kind)
+	}
+	c.Assert(kinds, qt.Contains, pricing.LineSignedCert)
+}
+
+// TestSignedCertificateCutShort: a purchase stopped after its debit, before the echo, is paid all
+// the same: GET /price reads it paid, growth gets no headroom out of it, and the retry debits
+// nothing and writes the echo.
+func TestSignedCertificateCutShort(t *testing.T) {
+	c := qt.New(t)
+	token := testCreateUser(t, "signedcertcrash1")
+	orgAddress := testCreateProvisionedOrganization(t, token)
+	oid := newPublishedPaidProcess(t, token, orgAddress)
+	paidCents := growthCents(t, growthBaseVoters)
+
+	fundWallet(t, orgAddress, pricing.SignedCertCents)
+	failMoneyStepOnce(t, moneyStepSignedCert, oid)
+	requestAndAssertCode(http.StatusInternalServerError, t, http.MethodPost, token, nil,
+		"processes", oid.Hex(), "signed-certificate")
+	assertBalance(c, orgAddress, 0)
+	vp, err := testDB.VotingProcess(oid)
+	c.Assert(err, qt.IsNil)
+	c.Assert(vp.AddOns.SignedCertificate, qt.IsFalse)
+	c.Assert(processPrice(t, token, oid).SignedCertificatePaid, qt.IsTrue)
+	refusal := growthRefusal(c, oid, growthTierEnd)
+	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-paidCents)
+
+	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.Cents(0))
+	vp, err = testDB.VotingProcess(oid)
+	c.Assert(err, qt.IsNil)
+	c.Assert(vp.AddOns.SignedCertificate, qt.IsTrue)
+	assertBalance(c, orgAddress, 0)
+}
```
Run: `go vet ./api/`
Expected: `api/processes_signed_certificate_test.go:292:17: price.SignedCertificatePaid undefined (type apicommon.ProcessPriceResponse has no field or method SignedCertificatePaid)`

Step 2: implement
```diff
diff --git a/api/apicommon/voting_processes.go b/api/apicommon/voting_processes.go
index 0a262ad1..1528c421 100644
--- a/api/apicommon/voting_processes.go
+++ b/api/apicommon/voting_processes.go
@@ -207,6 +207,12 @@ type ProcessPriceResponse struct {
 	// customUrl), the other process of the organization whose payment holds it. It is priced in,
 	// and publish and checkout answer 409 (40177) until that payment completes or is cancelled.
 	AddOnsHeldBy map[string]string `json:"addOnsHeldBy,omitempty"`
+	// SignedCertificatePaid tells whether a published process paid for the signed results
+	// certificate, at publish or bought after it; false for a draft.
+	SignedCertificatePaid bool `json:"signedCertificatePaid"`
+	// SignedCertificateDueCents is what POST /processes/{processId}/signed-certificate would debit:
+	// set only for a published process whose certificate is not paid.
+	SignedCertificateDueCents *pricing.Cents `json:"signedCertificateDueCents,omitempty"`
 }
 
 // ProcessCheckoutRequest starts (or resumes) the wallet top-up checkout of a voting process.
diff --git a/api/processes_price.go b/api/processes_price.go
index c5776970..771c1a17 100644
--- a/api/processes_price.go
+++ b/api/processes_price.go
@@ -420,7 +420,10 @@ func (*API) pricingHandler(w http.ResponseWriter, r *http.Request) {
 //	@Description	payment completes (the add-on is then free) or is cancelled. Above 15 000 voters a custom
 //	@Description	quote is recommended; above 50 000 the process is quote-only: no price (empty
 //	@Description	lines, totalCents 0, quoteRequired true), and publish and checkout answer 422
-//	@Description	(40176). Requires Manager/Admin of the organization.
+//	@Description	(40176). For a published process, signedCertificatePaid tells whether the signed results
+//	@Description	certificate is paid and, when it is not, signedCertificateDueCents is what
+//	@Description	POST /processes/{processId}/signed-certificate would debit. Requires Manager/Admin of the
+//	@Description	organization.
 //	@Description
 //	@Description	Also callable with a scoped API key (scope: `voting:write`).
 //	@Tags			processes
@@ -468,6 +471,18 @@ func (a *API) processPriceHandler(w http.ResponseWriter, r *http.Request) {
 		}
 		resp.AddOnsHeldBy[string(addOn)] = holder.Hex()
 	}
+	if vp.Published {
+		// read off the ledger: a purchase cut short before its echo is paid all the same
+		paid, err := a.processPaidState(vp.ID, walletPayer(quoted.org))
+		if err != nil {
+			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
+			return
+		}
+		resp.SignedCertificatePaid = vp.AddOns.SignedCertificate || paid.SignedCertificateCents > 0
+		if !resp.SignedCertificatePaid {
+			resp.SignedCertificateDueCents = new(pricing.SignedCertCents)
+		}
+	}
 	payment, err := a.db.ProcessPayment(oid)
 	switch {
 	case err == nil:
```
Run: `go vet ./api/ && go test ./api/ -run 'TestPriceReadsSignedCertificate|TestSignedCertificateCutShort|TestIntegratorAPIKeyReadsPrice' -count=1 -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: 3 `--- PASS` (TestIntegratorAPIKeyReadsPrice, TestPriceReadsSignedCertificate, TestSignedCertificateCutShort), `ok  	github.com/vocdoni/saas-backend/api`.
Mutation check done while drafting: with the `quoteProcessAtCensusSize` line from Task 2 reverted,
`TestSignedCertificateCutShort` fails (growth would get the 3900 as headroom).

Run: `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=9bdf27a9 ./... && ./scripts/check-qt-patterns.sh`
Expected: `0 issues.` and `✅ No qt anti-patterns found!`

Step 3: commit
`git -c commit.gpgsign=false commit -m "feat(processes): tell from the price whether the signed certificate is paid" -- api/apicommon/voting_processes.go api/processes_price.go api/processes_signed_certificate_test.go`

Interfaces this task provides to later tasks: `apicommon.ProcessPriceResponse.SignedCertificatePaid bool` (`json:"signedCertificatePaid"`), `SignedCertificateDueCents *pricing.Cents` (`json:"signedCertificateDueCents,omitempty"`).

### Task 4: regenerate the swagger spec
Model: fast   Repo: /home/lucasmenendez/saas-backend/.worktrees/feature-final-report-payment   Files: docs/swagger.yaml
Goal: the spec matches the new route and fields (AC14).
Patch: .delivery/2026-11-26-final-report-payment/patches/task-4.patch

Step 1: no test; the generated file is the check.

Step 2: generate
Run: `PATH=$(go env GOPATH)/bin:$PATH make swagger && git status --short`
Expected: `Swagger documentation generated successfully at docs/swagger.yaml` and only ` M docs/swagger.yaml`, with exactly this diff:
```diff
diff --git a/docs/swagger.yaml b/docs/swagger.yaml
index 64858d89..c865d5bc 100644
--- a/docs/swagger.yaml
+++ b/docs/swagger.yaml
@@ -1355,6 +1355,16 @@ definitions:
           QuoteRequired: census above 50 000 voters — quote-only: no price (no lines, totalCents 0,
           which is not free) and no self-service payment.
         type: boolean
+      signedCertificateDueCents:
+        description: |-
+          SignedCertificateDueCents is what POST /processes/{processId}/signed-certificate would debit:
+          set only for a published process whose certificate is not paid.
+        type: integer
+      signedCertificatePaid:
+        description: |-
+          SignedCertificatePaid tells whether a published process paid for the signed results
+          certificate, at publish or bought after it; false for a draft.
+        type: boolean
       totalCents:
         type: integer
     type: object
@@ -6420,7 +6430,10 @@ paths:
         payment completes (the add-on is then free) or is cancelled. Above 15 000 voters a custom
         quote is recommended; above 50 000 the process is quote-only: no price (empty
         lines, totalCents 0, quoteRequired true), and publish and checkout answer 422
-        (40176). Requires Manager/Admin of the organization.
+        (40176). For a published process, signedCertificatePaid tells whether the signed results
+        certificate is paid and, when it is not, signedCertificateDueCents is what
+        POST /processes/{processId}/signed-certificate would debit. Requires Manager/Admin of the
+        organization.
 
         Also callable with a scoped API key (scope: `voting:write`).
       parameters:
@@ -6974,6 +6987,65 @@ paths:
       summary: Get a voter's consumed sign info for a voting process
       tags:
       - processes
+  /processes/{processId}/signed-certificate:
+    post:
+      description: |-
+        Debits the signed results certificate add-on (€39, flat: never re-priced from the census)
+        from the payer's wallet for a published process, open or ended, quote-only included, and
+        turns addOns.signedCertificate on. A process that already has it is debited nothing
+        (amountCents 0). A draft selects the add-on with PUT /processes/{processId} instead (400).
+        No checkout is opened: when the wallet falls short a standard organization gets 402 (40178)
+        with the shortfall, tops its wallet up by at least shortfallCents and calls again; a managed
+        organization gets 402 (40175) or, past its spend cap, 402 (40183).
+        GET /processes/{processId}/price tells whether it is paid (signedCertificatePaid).
+        Requires Admin role. Also callable with a scoped API key (scope: `voting:write`).
+      parameters:
+      - description: Process ID
+        in: path
+        name: processId
+        required: true
+        type: string
+      produces:
+      - application/json
+      responses:
+        "200":
+          description: OK
+          schema:
+            $ref: '#/definitions/apicommon.ProcessCheckoutResponse'
+        "400":
+          description: The process is a draft
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "401":
+          description: Unauthorized
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "402":
+          description: Wallet short (40178, 40175), or spend cap reached (40183)
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "404":
+          description: Not Found
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "409":
+          description: Open checkout or refunded (40177), or organization being deleted
+            (40901)
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "500":
+          description: Internal Server Error
+          schema:
+            $ref: '#/definitions/errors.Error'
+        "503":
+          description: Busy (50302), retry after Retry-After
+          schema:
+            $ref: '#/definitions/errors.Error'
+      security:
+      - BearerAuth: []
+      summary: Buy the signed results certificate of a published process
+      tags:
+      - processes
   /processes/{processId}/validation:
     get:
       description: |-
```

Step 3: commit
`git -c commit.gpgsign=false commit -m "docs(swagger): regenerate for the signed certificate purchase" -- docs/swagger.yaml`

Interfaces this task provides to later tasks: none.

## Merge and release
1. Full gates on the branch: `go build ./... && go vet ./...`; `go test ./db/ ./pricing/ -count=1` → `ok` for both;
   the new and regression api tests from Tasks 2-3 → 19 `--- PASS`; `~/.cache/saas-backend/bin/golangci-lint run --new-from-rev=9bdf27a9 ./... && ./scripts/check-qt-patterns.sh` → `0 issues.`;
   `go mod tidy && git diff --exit-code go.mod go.sum` → no diff; `PATH=$(go env GOPATH)/bin:$PATH make swagger` → no diff.
   The whole `./api` suite runs in CI only (`go test -failfast -timeout=45m ./...`).
2. Merge order: feature/final-report-payment → main, through a PR (single repo).
3. Release: none here; it ships with the next `main → stage → lts` promotion.
4. **Owner OK required before:** pushing feature/final-report-payment; opening the PR against main.
5. Post-release checks: CI green on the PR (lint, qt gate, tidy, tests, swagger diff comment).
   The PR description records the docs/SDK impact as pending follow-ups to suggest to the owner:
   - vocdoni/vocdoni.io (`stage` branch, `content/developers/docs/en/`): voting processes / quotas
     pages, the new `POST /processes/{processId}/signed-certificate` (402 40178/40175/40183, no
     checkout) and the two `GET /processes/{processId}/price` fields; port to `main` once it reaches `lts`.
   - vocdoni/vocdoni-integrator-sdk: `@vocdoni/api-types` `ProcessPriceResponse` gains
     `signedCertificatePaid`, `signedCertificateDueCents`; `@vocdoni/api-client` gains the purchase call.
   - vocdoni-app: gate the PDF download on `signedCertificatePaid` (issue #866), after the backend deploy.
