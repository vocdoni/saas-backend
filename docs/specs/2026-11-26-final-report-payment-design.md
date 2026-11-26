# Final report add-on after publish: design

Status: draft   Class: architectural (payments, public API)
Owner decisions so far (2026-11-26):
- a wallet that falls short answers 402; the client tops up through the existing wallet top-up and
  retries (no new checkout, no webhook change);
- an explicit, server-computed `signedCertificatePaid` field tells whether the report is paid;
- the amount due for the add-on of a published process is readable on `GET /processes/{processId}/price`.

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
  `api/processes_census_payment.go:225`) is the template: `paidProcessProjection` (grandfathers a
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
### `POST /processes/{processId}/signed-certificate`
No body. Admin role; API key scope `voting:write`. Steps, mirroring census headroom:
1. Load the process; 400 when it is not published (a draft selects it with `PUT`); refuse a
   refunded process as census headroom does.
2. When `vp.AddOns.SignedCertificate` is already set: 200 with `amountCents: 0` (idempotent).
3. `paidProcessProjection(vp, census, nil)`; then quote with
   `quoteProcessAtCensusSize` on a copy of `vp` whose `AddOns.SignedCertificate` is true, at the
   current `census.Size`. Quote-only → 422.
4. `chargeProcessWallet(processCharge{vp, org, quote, fromCents: &paidCents})`. Errors pass through
   unchanged (402 40178 / 40175 / 40183, 409, 422, 503).
5. After a successful (or covered, 0) charge, `db.SetVotingProcessSignedCertificate(id)`: `$set
   addOns.signedCertificate: true` filtered on `published: true`.
6. 200 `apicommon.ProcessCheckoutResponse{AmountCents: debited, Currency: "eur"}`.

Order matters: debit first, flag second. A crash between them leaves the money debited and the
flag unset; retrying finds the quote with the add-on already covered (`chargeProcessWallet`
returns 0) and sets the flag, so it never charges twice. The reverse order would expose a paid
flag nothing paid for.

### `GET /processes/{processId}/price`
`apicommon.ProcessPriceResponse` gains:
- `signedCertificatePaid bool` — `vp.Published && vp.AddOns.SignedCertificate`.
- `signedCertificateDueCents *pricing.Cents` (`omitempty`) — only for a published process without
  the add-on whose census is not quote-only: the quote of step 3 minus `paid.cents()`, i.e. what
  the POST would debit now (normally 3900).

## Alternatives considered
| Option | Why not |
|---|---|
| Record the add-on in the ledger debit (like `OrgAddOn`) | Org add-ons carry claims, stamps and withholding; the report is per-process and needs none. |
| Flag first, then debit | A failed debit would read as paid. |
| Stripe checkout for the shortfall | Owner chose 402 + top-up; avoids webhook fulfillment changes. |
| Derive "paid" client-side from `published` + `addOns` | Owner chose an explicit server field. |

## Compatibility and rollout
Additive: one new route, two new response fields. No migration, no stored-format change.
Swagger regenerated. Follow-ups: vocdoni.io docs (pricing/processes pages) and
vocdoni-integrator-sdk (`ProcessPriceResponse` type, new endpoint), both suggested in the PR.

## Failure modes and security
- Concurrent purchases: serialized by the payer's wallet lock in `chargeProcessWallet`; the second
  finds the price covered and debits 0.
- Purchase racing census growth: `fromCents` makes a charge priced from a stale paid amount fail
  with 409; the client retries.
- Org teardown: `chargeProcessWallet` refuses (`refuseTearingDown`).
- Authorization: Admin of the process's organization only, like census headroom.
- Grandfathering on `GET /price`: `paidProcessProjection` may write the base of a pre-PAYG
  published process on this read, as the growth guard already does; idempotent.

## Acceptance criteria
- [ ] AC1: buying for a published process with enough balance debits 3900, sets the flag, and the
  wallet history shows a payment row (api test).
- [ ] AC2: a second call debits 0 and returns 200 (api test).
- [ ] AC3: wallet short → 402 40178 with the shortfall; nothing is debited and the flag stays unset (api test).
- [ ] AC4: a draft → 400; non-admin → 401 (api test).
- [ ] AC5: `GET /price` of a published process without the add-on returns
  `signedCertificatePaid: false`, `signedCertificateDueCents: 3900`; after buying, `true` and no due (api test).
- [ ] AC6: after buying, a census growth check does not charge the add-on again (api test).
- [ ] AC7: `make swagger`, golangci-lint (new-from-rev) and `scripts/check-qt-patterns.sh` clean.

## Constraints
AGENTS.md conventions (`fmt.Errorf`, lowercase messages, gofumpt, lll 130, qt matchers, godoc on
exported names); run `make swagger`; unsigned commits (`git -c commit.gpgsign=false`); no client
names in public text.

## Open questions
None.
