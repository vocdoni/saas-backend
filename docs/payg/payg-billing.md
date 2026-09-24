# Pay-per-process billing

How the SaaS backend charges for voting processes: each process is priced individually
from its eligible voters and selected add-ons, paid through a one-time Stripe Checkout
session (or an integrator's prepaid wallet), and **publication is the fulfillment of a
verified payment**. Drafts are free; small processes are free; a browser redirect is
never proof of payment.

Spec: [VocdoniApp new pricing model](https://hackmd.io/@vocdoni/vocdoni-app-new-pricing-model-feature).
Code map: `pricing/` (the formula), `db/process_payments.go` + `db/wallets.go` (state),
`stripe/payments.go` (checkout + webhook fulfillment), `api/processes_price.go`,
`api/processes_checkout.go` + `api/wallet.go` (endpoints), `api/processes_publish.go` +
`api/processes_publish_payment.go` (the gate).

## Pricing

`pricing.Compute` is a pure function over `QuoteInput{CensusSize, EmailTwoFA, SMSTwoFA,
SignedCert, CustomURL, Branding, Payer}`. All amounts are **EUR cents, VAT excluded** —
Stripe Tax adds VAT at checkout from the billing details the customer enters.

Base price per eligible voter count `v`, rounded to the nearest €5 (`R5`):

| Voters | Formula |
|---|---|
| 1–10 | free |
| 11–800 | `R5(1.5·v^0.824)` |
| 801–5 000 | `R5(33·v^0.36)` |
| 5 001–15 000 | `R5(0.142·v)` |
| >15 000 | `R5(2130 + 0.11·(v−15000))` |

Add-ons: email 2FA €0.01/voter, SMS 2FA €0.03/voter (both derive from the census
`twoFaFields`, not a separate flag), signed results certificate €49/process, custom URL
€89/process, branding €149 **once per organization** — the line is suppressed once
`Organization.BrandingPaidAt` is stamped, and while another process holds the claim
described in [Branding is claimed, not just checked](#branding-is-claimed-not-just-checked).

Worked examples (pinned in `pricing/pricing_test.go`): 10 voters → free; 600 → €290;
10 000 → €1 420; 10 000 + email 2FA + certificate → €1 569.

Two deliberate refinements over the spec text:

- **Free tier zeroes per-voter add-ons.** At ≤10 voters, 2FA would price at €0.10–€0.30 —
  below any amount Stripe can charge — and such test-sized processes are already exempt
  from process counters (`db.TestMaxCensusSize`). Flat add-ons (certificate, custom URL,
  branding) stay billable at any census size.
- **Payer-type policy seam.** The bracket function is selected by payer type
  (`PayerStandard` / `PayerIntegrator`). Both are identical today; integrator pricing can
  diverge later without touching the payment flow.

Thresholds: above 15 000 voters the price response sets `quoteRecommended` (the app
should suggest a custom quote); above 50 000, `quoteRequired` — self-service checkout is
refused (422, code 40176) and sales handles it.

`pricing.QuoteHash` fingerprints the priced inputs + total. It is stored with the
payment and is how an open checkout session is detected as **obsolete** after a draft
edit changes the price.

## Payment state

Payment state lives in its own `processPayments` collection keyed by the process id —
deliberately **not** embedded in `votingProcesses`, whose write paths replace whole
documents and must never be able to wipe a live session id or a paid state.

```
 (none) ──checkout──► pending ──delayed method──► processing
   │                    │  ▲                          │
   │ free (net €0)      │  └── new session ── failed ◄─┴── payment failed
   │                    │                        ▲
   ▼                    │                        └── session expired
 publish            webhook paid ──► paid
                                       ▲
                         wallet debit ─┘
```

- `pending`: a checkout session is open. Draft edits are allowed; the session is expired
  and replaced at the next checkout if the price changed. A session Stripe expires on its
  own (`checkout.session.expired`) moves the payment to `failed`, which matters for
  branding: an abandoned checkout must not hold the organization's claim forever.
- `processing`: checkout completed with a delayed payment method (e.g. SEPA debit) and
  the outcome is unknown. No new charge may start; the draft is locked.
- `failed`: the payment failed; the process is payable again with a fresh session.
- `paid`: verified by webhook or debited from a wallet. Publication retries never
  re-charge, and every publish path re-prices against `amountCents`. The draft stays
  **editable** (an edit can only repair a draft that failed publish preflight — the gate
  re-prices it, so it cannot under-charge) but **not deletable** ([Deleting a
  draft](#deleting-a-draft)).

Every transition is a single conditional Mongo write whose **filter is the state
machine** (`db/process_payments.go`), in the style of `ClaimVotingProcessForPublish`:
matching the filter is winning the transition, `MatchedCount` reports it, and a
duplicate webhook resolves to a lost CAS instead of a second side effect.

## Checkout flow (standard organizations)

0. `GET /pricing?voters=…&emailTwoFA=…` — the public calculator over the same formula
   (no auth, no draft needed; for the pricing page). Branding is charged as requested
   and credits are not applied — it has no organization context.
1. `GET /processes/{id}/price` — the server-side quote for a draft (line breakdown,
   flags, current payment status). The price shown to the user before payment is always
   a server calculation, never client-side math.
2. `POST /processes/{id}/checkout` (Admin, JWT-only) — reconciles before charging:
   - `processing`/`paid` → 409 (40177), never a second charge;
   - open session with a matching quote hash → **reused** (same client secret);
   - open session with a stale hash → **expired first**, then replaced — an expiry
     failure refuses the checkout rather than risking two payable sessions;
   - otherwise a new `mode: payment` session with server-calculated `price_data`
     (`tax_behavior: exclusive`), `invoice_creation` enabled, and the process id +
     requesting user in **session metadata**.
3. The app confirms with the embedded Payment Element; Stripe computes VAT.
4. The webhook fulfills (below). `GET /processes/{id}/checkout` is for polling only.

Free processes (net €0) skip all of this and publish directly.

## Publication gate

`POST /processes/{id}/publish` refuses a priced unpaid process with **402** (40174,
quote in the error data); the `GET .../validation` dry-run lists the missing payment
too. Once paid, publish re-enters the normal pipeline (preflight → atomic claim →
signer → async job). Publishing after paid is free forever — a failed publication
retries without touching the payment.

Webhook-triggered publication: the fulfillment that wins the paid CAS fires
`stripe.Service.OnProcessPaid` → `api.publishPaidProcess`, which re-runs preflight and
publishes **acting as the user who requested the checkout** (persisted on the payment).
If that user is gone or preflight fails, the process stays paid and any admin publishes
manually later, free. So payment completes even if the buyer closes the browser.

## Webhook fulfillment and idempotency

`stripe/webhook.go` handles `checkout.session.completed` and
`checkout.session.async_payment_succeeded` (fulfill only when the session's
`payment_status` is `"paid"`; a completed-but-unpaid session only advances to
`processing`), `checkout.session.async_payment_failed` and `checkout.session.expired`
(→ `failed`). Sessions from
the legacy subscription flow carry none of our metadata keys and are ignored.

There is **no durable event store**. Idempotency is property of every side effect:

| Side effect | Guard |
|---|---|
| mark process paid | payment CAS (status + session id in the filter) |
| enqueue publication | fired only by the CAS winner |
| wallet credit / debit | `appliedKeys $ne` in the same atomic wallet update |
| ledger audit row | unique index on the operation's idempotency key |

A replayed event (Stripe retry, restart, second replica) loses every guard and is a
no-op. Money arriving for a session the process no longer references is logged as an
error for manual reconciliation — never silently dropped.

An event that can never become valid — metadata naming no process, an id that is not an
ObjectID — is answered **200** and dropped (`errPermanentEvent`): retrying it for three days
would only bury the events that can still succeed.

## Integrator wallet (managed organizations)

Integrators publish on behalf of managed organizations, so the payer is not present at
publish time. Each integrator holds a prepaid **EUR balance** (money, not resource
units — unit pools would clash with the non-linear base price):

- **Top-up**: `POST /wallet/topup` (JWT, Admin of the integrator org) opens the same
  one-time checkout flow; the balance is credited only by the verified webhook, keyed by
  the session id. `GET /wallet` (also `quota:read` API key) reads balance + ledger.
- **Debit at publish**: a managed org's process skips the 402 gate; inside the publish
  pipeline (after the claim, so concurrent publishes cannot both debit) the wallet is
  debited by one conditional update carrying both the balance condition
  (`balanceCents $gte`) and the idempotency condition (`appliedKeys $ne <processId:price>`)
  in a single filter — the no-double-spend guarantee across replicas, with no transactions.
  Insufficient balance → 402 (40175, required + available cents), claim released,
  retryable. The key carries the price, so a retry at the **same** price is a no-op while a
  retry of a draft that grew debits only the difference and raises the payment to match.
- **Ledger**: `walletLedger` is the append-only audit trail (`topup` and `debit`
  rows) and the authority on what happened; the wallet document is authoritative for the
  balance. `appliedKeys` is bounded to the most recent 5 000 keys
  (`walletAppliedKeysWindow`), so a long-lived integrator cannot grow the document without
  limit; an operation older than the window is caught by the ledger's unique index on
  `idempotencyKey` instead.

Managed orgs are refused at `POST /processes/{id}/checkout` — their payment provenance is
the wallet, and pricing selects the integrator policy for them.

## Branding is claimed, not just checked

Branding is the one add-on that can only be sold once per organization, so two processes
paying at the same time must not both be charged for it — and neither may quietly lose it.
The claim is a conditional write on the **organization** document
(`brandingClaimedBy` + `brandingClaimedAt`, `db.ClaimOrganizationBranding`), taken at the
two moments money moves: opening a card checkout, and debiting the integrator wallet.

Read paths (`GET .../price`, the publish gate) only *read* the claim, so quoting never takes
one. A caller that loses the claim has `Branding` dropped from its input and the quote
recomputed **before** it is charged. Validity is re-derived from the claimant's payment, so
the claim self-heals: once it is older than `db.BrandingClaimStaleAfter` (2 minutes), it is
releasable when that payment is absent, `failed`, `refunded`, or no longer carries branding
(the draft was re-priced without it). A fresh claim is never taken over — the cutoff is part
of the claim's filter — and every paying attempt refreshes its own claim before storing its
payment, so a sibling cannot read a half-written retry and steal the add-on. The cost is
that an abandoned branded checkout frees branding for the organization only after two
minutes. Fulfillment stamps `brandingPaidAt`. A process that paid for branding keeps it in
its own price after the stamp, so re-pricing it never drops the €149 it already paid.

## Deleting a draft

`DELETE /processes/{processId}` refuses a `paid` draft with **409** (40177): deleting it
would keep the money for a process that no longer exists. A `processing` payment refuses
deletion too, and so does a `pending` one whose session the customer just completed — both
are retryable once they settle. A `pending` session that is still open is expired first; if
Stripe cannot confirm it expired, the delete fails closed rather than leave a payable session
for a process that no longer exists. A `failed` or never-checked-out payment goes with the
draft.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| 40174 | 402 | publication requires payment (quote in data) |
| 40175 | 402 | insufficient integrator wallet balance (required/available in data) |
| 40176 | 422 | census above 50 000 — custom quote only |
| 40177 | 409 | a payment is already processing or completed, or a session is completing |

## Storage summary (migration 0023)

- `processPayments` — one document per process payment (`_id` = process id).
- `wallets` — one document per integrator (`_id` = org address).
- `walletLedger` — append-only, unique index on `idempotencyKey`.
- `votingProcesses` gains `addOns`; `organizations` gains `brandingPaidAt`,
  `brandingClaimedBy` and `brandingClaimedAt`.
- `processPayments` also records `paymentIntentId` and `chargeSubtotalCents`/
  `chargeTotalCents` (the charge net and gross of VAT) at fulfillment.

## Testing the flow

Three layers, from hermetic to manual:

1. **Signed-webhook e2e (CI, no network, no cards)** —
   `api/stripe_checkout_webhook_test.go` signs event payloads with the real Stripe v1
   signature scheme (`stripe-go/webhook.GenerateTestSignedPayload`) and POSTs them to
   `/subscriptions/webhook`, driving signature validation, event parsing, fulfillment
   and server-side publication through the production code path. Card numbers never
   appear here: the card only matters inside Stripe's browser step, and the webhook is
   Stripe's signed statement of the outcome.
2. **Live test-mode session lifecycle (opt-in, network)** — set
   `STRIPE_TEST_SECRET_KEY=sk_test_…` to run `stripe/payments_livetest_test.go`, which
   creates, reads and expires a real test-mode session: Stripe itself validates the
   `price_data` + `automatic_tax` + `invoice_creation` wire shape. It refuses non-test
   keys and never charges anything.
3. **Manual browser checklist (Stripe test mode)** — the only layer that exercises the
   embedded Payment Element and Stripe Tax UI. The full step-by-step runbook (stack
   setup, curl sequence, a copy-paste payment-page harness) is
   [`payg-manual-testing.md`](payg-manual-testing.md). Run the stack with test-mode
   keys, forward webhooks with
   `stripe listen --forward-to localhost:8080/subscriptions/webhook`,
   and use Stripe's standard test data (authoritative list:
   https://docs.stripe.com/testing — any future expiry, any CVC, any postcode):

   | Test data | Behavior to verify |
   |---|---|
   | Card `4242 4242 4242 4242` | immediate success → webhook marks paid → process auto-publishes |
   | Card `4000 0025 0000 3155` | 3DS challenge, then success |
   | Card `4000 0000 0000 9995` | declined (insufficient funds) → payment `failed` → retry opens a new session |
   | SEPA IBAN `DE89370400440532013000` | delayed method: `processing`, then `async_payment_succeeded` → publishes |
   | SEPA IBAN `DE62370400440532013001` | delayed failure: `async_payment_failed` → payable again |
   | `stripe events resend <evt_…>` | replayed fulfillment is a no-op (no double publish/credit) |

   Also verify: VAT appears on top of the net line items once a billing address is
   entered; the invoice is generated (invoice_creation); a wallet top-up credits the
   net amount exactly once.

## Open questions / follow-ups

- Custom URL is priced per process but the underlying subdomain remains org-level.
- Chargebacks are out of scope: a disputed payment is not reverted automatically.
- VAT treatment of wallet top-ups (single- vs multi-purpose voucher) needs accountant
  sign-off before launch; `automatic_tax` is on for top-up sessions.
- Legacy subscriptions convert to process credits and the subscription system is
  removed in the follow-up PR (see the spec's "Legacy plans transition").
