# Final report add-on after publish: design

Status: audited (REWORK folded in)   Class: architectural (payments, public API)
Owner decisions so far (2026-11-26):
- a wallet that falls short answers 402; the client tops up through the existing wallet top-up and
  retries (no new checkout, no webhook change);
- an explicit, server-computed `signedCertificatePaid` field tells whether the report is paid;
- the amount due for the add-on of a published process is readable on `GET /processes/{processId}/price`;
- processes published before this release pay 3900 like any other (no migration);
- quote-only published processes can buy it at the flat price.

## Problem
The paid final report (`addOns.signedCertificate`, €39, `pricing.signedCertPrice`) can only be
selected on a draft (`PUT /processes/{processId}`, `api/processes.go:566`). Once a process is
published or ended there is no way to buy it, and no read says whether a process paid for it, so
vocdoni-app lets every closed process download the PDF report for free (issue #866).

## Goals
1. An org admin can buy the report add-on for a published (open or ended) process, debited from
   the payer's wallet, at most once.
2. A read says, computed by the server, whether the report of a process is paid.
3. A read gives the exact amount the purchase would debit for a published process that lacks it.

## Non-goals
- Generating, signing or gating the PDF server side (it is built in the browser,
  vocdoni-app `src/components/Process/VotingReportPdf/`).
- A Stripe checkout or webhook path for this purchase.
- Selling it to drafts: a draft already selects it with `PUT` and pays it at publish.
- Removing the add-on, or refunding it, once bought.

## Current state
- `processQuoteInput` (`api/processes_price.go:24`) prices `SignedCert` from `vp.AddOns.SignedCertificate`.
- Census headroom for a paid or published process (`createProcessCensusCheckoutHandler`,
  `api/processes_census_payment.go:240`) is the template: `paidProcessProjection` (grandfathers a
  published process that has no recorded base), `quoteProcessAtCensusSize` (prices org add-ons as
  they were charged), then `chargeProcessWallet` (`api/processes_publish_payment.go:369`) with
  `fromCents`, which debits only the delta under the payer's wallet lock, checks the spend cap,
  refuses an open checkout (409), a refunded process, a quote-only census (422), and returns
  `walletShortError` (402: 40178 with the shortfall, 40175 for a managed organization).
- A published process's `addOns.signedCertificate` was priced into what it paid at publish, or
  into its grandfathered base, so published + flag already means paid.
- The growth guard re-quotes a published process from `vp.AddOns` (`quoteProcessAtCensusSize`), so a
  flag set without the matching payment would make the next growth check charge it.
- `SetVotingProcessEndDate` (`db/voting_processes.go:506`) is the pattern for a targeted write on a
  published process.

## Design
### Record: a ledger row of its own
The purchase is one `WalletEntryPayment` row on the payer's wallet ledger for the process:
`amountCents: -3900`, idempotency key `signedcert:<processId>`, and a new field
`WalletLedgerEntry.SignedCertificate bool` (`bson:"signedCertificate,omitempty"`). It is written
by a new db debit with the same conditional write as `DebitWalletForProcess` (balance, managed
organization spend cap, unique key, one `applyWalletEntry`), but no `PriceCents`/`Attempt`.
`foldProcessLedger` folds it into `PaidCents` like any payment and into a new
`ProcessLedgerState.SignedCertificateCents` (the report is paid when it is above 0). It is not an
`OrgAddOn`: `reliedOnOrgAddOns` prices every `paid.AddOns` entry with `orgAddOnCents`.

The row is both the debit and the record that the report is paid, so no reader ever sees the money
without the report. `addOns.signedCertificate` on the process is written afterwards as a display
echo (`db.SetVotingProcessSignedCertificate(id)`: `$set addOns.signedCertificate: true`, filter
`published: true`); a crash before it leaves the report paid (read from the ledger) and the next
call writes it.

`pricing.SignedCertCents` exports `signedCertPrice`. The price is flat: never re-quoted from the
census, the headroom held or the minimum-charge line.

### Pricing published processes
- `quoteProcessAtCensusSize` prices `SignedCert` as `vp.AddOns.SignedCertificate ||
  p.paid.SignedCertificateCents > 0`, as it already takes branding and custom URL from the ledger,
  so growth checks see the 3900 as the report's, never as headroom.
- `grandfatherProcessPayment` decides "the wallet paid nothing" on `PaidCents -
  SignedCertificateCents == 0`.
- Invariant: the base of a published process is written, from the stored `vp`, before any
  signed-certificate debit (the handler calls `paidProcessProjection(vp, census, nil)` with the
  stored process first, only for that write).

### `POST /processes/{processId}/signed-certificate`
No body. Admin role. Route constant `processesSignedCertificateEndpoint` in `api/routes.go`,
registered in the protected group next to `processesCensusCheckoutEndpoint`, and `"POST " +
processesSignedCertificateEndpoint: ScopeVotingWrite` in the API key allowlist (`api/apikeys.go`).
1. Load the process (404 unknown, 401 not admin). Not published → 400 `ErrMalformedBody` ("the
   process is a draft: select the add-on with PUT /processes/{processId}"); a partially published
   process counts as a draft. Refunded → 409 40177 (`refuseRefundedDraft`).
2. `paidProcessProjection` with the stored process (grandfather base write).
3. Under the payer's wallet lock: `refuseTearingDown`; read the ledger state; refunded → 409; an
   open top-up checkout → 409 40177; already paid (`vp.AddOns.SignedCertificate` or
   `SignedCertificateCents > 0`) → debit nothing. Otherwise debit 3900:
   - spend cap → 402 40183 (`spendCapError`);
   - short wallet, managed organization → 402 40175 (`requiredCents`, `availableCents`);
   - short wallet, any other → 402 40178 with `ProcessPaymentRequired{lines: [signedCertificate
     3900], totalCents: 3900, balanceCents, shortfallCents: 3900 - balanceCents}` and a message
     naming the report. No checkout is opened: the client tops up at least `shortfallCents`
     through the wallet top-up and calls again.
4. Echo the flag and refresh the payment row's display cache (`refreshPaymentCache` at the new paid amount).
5. 200 `apicommon.ProcessCheckoutResponse{AmountCents: debited (3900 or 0), Currency: "eur"}`.
Quote-only and ended processes buy it like any other published process.

### `GET /processes/{processId}/price`
`apicommon.ProcessPriceResponse` gains:
- `signedCertificatePaid bool`: `vp.Published && (vp.AddOns.SignedCertificate ||
  SignedCertificateCents > 0)`; false for a draft.
- `signedCertificateDueCents *pricing.Cents` (`omitempty`): 3900 for a published process whose
  report is not paid, absent otherwise. This read writes nothing (no `paidProcessProjection`).

## Alternatives considered
| Option | Why not |
|---|---|
| Re-quote the census with the add-on and charge the delta | A census that shrank after the vote makes the report free; unpriced growth of an ended process or unused headroom misprices it. |
| Debit, then flag as the record | Two writes: between them the 3900 reads as census headroom. |
| Record it as an `OrgAddOn` | Org add-ons carry claims, stamps and withholding; refunds would price it as branding. |
| Stripe checkout for the shortfall | Owner chose 402 + top-up; avoids webhook fulfillment changes. |
| Derive "paid" client-side | Owner chose an explicit server field. |

## Compatibility and rollout
Additive: one route, two fields on `ProcessPriceResponse`, one field on the wallet ledger entry
(absent on old rows, read as false). Observable on existing reads after a purchase:
`addOns.signedCertificate` becomes true on a published process, `GET /price` lists the
`signedCertificate` line, and the checkout status `amountCents` rises by 3900. No migration.
Processes published before this release (owner, 2026-11-26) pay 3900 like any other: none has the
flag, so they read `signedCertificatePaid: false`. `signedCertificatePaid` is readable by
Manager/Admin only; the app gates the download in the admin dashboard.
Rollout: backend first, then vocdoni-app gating on `signedCertificatePaid`.
Rollback: the rows stay; older code folds them as plain payments and prices the report from
`addOns.signedCertificate`, so a purchase whose echo was written stays consistent.
Follow-ups suggested in the PR: vocdoni.io docs (stage) and vocdoni-integrator-sdk
(`ProcessPriceResponse`, new endpoint).

## Failure modes and security
- Concurrent purchases: serialized by the payer's wallet lock and, across instances, by the
  `signedcert:<processId>` key; the second finds the marker, debits nothing and answers 200 with
  `amountCents: 0`.
- Purchase racing census growth: the price is flat and the guard reads the marker from the same
  ledger, so neither misprices the other.
- Crash after the debit: the ledger says paid; the retry debits 0 and writes the echo.
- Org teardown: refused under the wallet lock (`refuseTearingDown`); a published process's report
  goes with it unrefunded, like the rest of what it paid.
- Authorization: Admin of the process's organization, or a `voting:write` API key.

## Acceptance criteria
- [ ] AC1: buying for a published process with enough balance debits 3900, writes the
  `signedcert:` row and the flag (api test).
- [ ] AC2: a second call debits 0 and answers 200 (api test).
- [ ] AC3: short wallet → 402 40178 with the specified payload; nothing debited, report unpaid (api test).
- [ ] AC4: a draft → 400; non-admin → 401; unknown → 404 (api test).
- [ ] AC5: `GET /price` of a published process without the report: `signedCertificatePaid: false`,
  `signedCertificateDueCents: 3900`; after buying: `true`, no due, `signedCertificate` line (api test).
- [ ] AC6: after buying, a census growth check charges only the growth (api test).
- [ ] AC7: the purchase debits exactly 3900 when the census shrank below or grew past the size it
  paid for, and when it holds unused headroom (api test).
- [ ] AC8: a published process with no base gets its base without the report, then pays 3900; a
  later growth check charges only the growth (api test).
- [ ] AC9: with `moneyStepHook` failing after the debit, `GET /price` reads `signedCertificatePaid:
  true`, growth gets no free headroom, and the retry debits 0 and writes the flag (api test).
- [ ] AC10: two concurrent purchases debit 3900 once; both answer 200 (api test).
- [ ] AC11: a managed organization debits its integrator's wallet: 40175 when short, 40183 past
  the cap, spend counter +3900 (api test).
- [ ] AC12: an ended process and a quote-only one can buy it; a `voting:write` API key can call it
  and a key without that scope cannot (api test).
- [ ] AC13: `foldProcessLedger` folds the row into `PaidCents` and `SignedCertificateCents` (db unit test).
- [ ] AC14: `make swagger`, golangci-lint (new-from-rev) and `scripts/check-qt-patterns.sh` clean.

## Constraints
AGENTS.md conventions (`fmt.Errorf`, lowercase messages, gofumpt, lll 130, qt matchers, godoc on
exported names); run `make swagger`; unsigned commits (`git -c commit.gpgsign=false`); no client
names in public text.

## Open questions
None.
