# Pay-per-process billing

How the SaaS backend charges for voting processes: each process is priced individually
from its eligible voters and selected add-ons and **paid from a prepaid credit wallet**
(1 credit = €1, VAT excluded, stored as cents). Every organization that is not managed has one
wallet, a standard organization and an integrator alike; a managed organization's processes are
paid from its integrator's. Stripe does one thing: top a wallet up. Publishing and buying census
headroom only ever debit a wallet, money given back is credited to the wallet it came from, and
a refund made by hand in the Stripe dashboard is mirrored in the wallet. Drafts are free; small
processes without add-ons are free; a browser redirect is never proof of payment.

Spec: [VocdoniApp new pricing model](https://hackmd.io/@vocdoni/vocdoni-app-new-pricing-model-feature).
Code map: `pricing/` (the formula), `db/process_payments.go` + `db/wallets.go` (state),
`stripe/payments.go` (top-up sessions, the checkout webhook, refund sync), `api/wallet.go`
(wallet endpoints, every top-up session), `api/processes_price.go`, `api/processes_checkout.go`
(the shortfall checkout), `api/processes_publish.go` + `api/processes_publish_payment.go` (the
gate, the debit, the webhook follow-up), `api/processes_census_payment.go` (census headroom),
`api/processes_refund.go` (credit back on delete).

## Pricing

`pricing.Compute` is a pure function over `QuoteInput{CensusSize, EmailTwoFA, SMSTwoFA,
SignedCert, CustomURL, Branding, Payer}`. All amounts are **EUR cents, VAT excluded** —
Stripe Tax adds VAT at checkout from the billing details the customer enters.

The base price is **graduated**: up to 25 eligible voters it is free; above that every voter
is priced, starting from the first band, and each rate applies only to the voters inside its
band (`basePriceCents`, one `switch` falling through from the top band down).

| Voters | Per voter | Cumulative at the top of the band |
|---|---|---|
| 1–25 | free (whole census) | €0 |
| 1–100 | €0.660 | €66 |
| 101–500 | €0.430 | €238 |
| 501–1 000 | €0.240 | €358 |
| 1 001–5 000 | €0.125 | €858 |
| 5 001–15 000 | €0.100 | €1 858 |
| 15 001–50 000 | €0.090 | €5 008 |
| above 50 000 | quote only | — |

The rates are exact in hundredths of a cent (€0.125 = 1 250), so the bands are summed exactly
as integers and rounded **half-up to the cent once**, at the end (1 001 voters → €358.125 →
€358.13).

Add-ons are charged at **any** census size, a free base price included: SMS 2FA €0.05/voter;
email 2FA free for standard organizations and €0.01/voter for integrators (both derive from
the census `twoFaFields`, not a separate flag); signed results certificate €39/process;
custom URL €99 and branding €149, both **once per organization** — each line is suppressed
once the organization's `<addOn>PaidAt` is stamped, and while another process holds that
add-on's claim, as described in
[Once-per-organization add-ons are claimed, not just checked](#once-per-organization-add-ons-are-claimed-not-just-checked).

**Minimum charge.** A priced total strictly between €0 and €1 is raised to €1
(`pricing.MinimumChargeCents`) by an extra `minimumCharge` line, so the lines still sum to the
total; it applies to every payer. A total of €0 stays free. €1 is also the smallest top-up
(Stripe cannot charge a card less than €0.50): a free-form top-up is €1 to €100 000, and a
top-up opened for a shortfall charges `max(shortfall, €1)`, the rest staying in the wallet
([Topping up for a process](#topping-up-for-a-process),
[Buying census headroom](#buying-census-headroom)).

Worked examples (pinned in `pricing/pricing_test.go` and `api/pricing_test.go`): 25 voters →
free; 26 → €17.16; 40 → €26.40; 600 → €262; 1 200 → €383; 3 000 → €608; 10 000 → €1 358;
10 000 + SMS 2FA + certificate → €1 897; 900 + SMS 2FA + certificate → €418; 10 voters + SMS
2FA → €0.50, charged €1 (minimum); 50 000 → €5 008; 10 000 + email 2FA → €1 358 standard,
€1 458 for an integrator.

- **Payer-type policy.** The payer type (`PayerStandard` / `PayerIntegrator`, the latter for a
  managed organization) selects the policy. The bands are identical; email 2FA is free for
  standard organizations and billed per voter to integrators. Integrator pricing can diverge
  further without touching the payment flow.

Thresholds: above 15 000 voters the price response sets `quoteRecommended` (the app
should suggest a custom quote) and still carries a price that can be paid. Above 50 000 the
process is **quote-only**: the quote has no price at all — `lines: []`, `totalCents: 0`,
`quoteRequired: true`, add-ons not priced either — and a zero total there is never "free".
Every payment path refuses it with **422** (40176) before it could read the zero as free or
as already covered: the publish gate (so the `validation` dry-run lists it too), the wallet
debit, the process top-up checkout, census-headroom checkout, and the census growth guard. A
draft whose census crossed 50 000 after its top-up was credited is not published by the webhook
(it answers 200, not a retry loop): the credit stays in the wallet for sales to settle.

## Wallets

A wallet is keyed by its **payer**: the organization itself, or `org.ManagedBy` for a managed
organization, whose processes its integrator pays. It is created on its first credit.

- `GET /organizations/{orgAddress}/wallet` (Admin or Manager; also a `quota:read` API key **of
  that same organization**) returns `{balanceCents, currency, history[], pagination}`. A key acts
  as the user who created it, who may administer other organizations too, so the handler pins
  it to `key.OrgAddress` (403 `ErrAPIKeyNotAllowed` otherwise).
- `POST /organizations/{orgAddress}/wallet/topup {amountCents, returnURL, locale}` (Admin, JWT
  only) opens a `mode: payment` checkout for €1–€100 000 net; Stripe Tax adds VAT on top. The
  balance moves only when the verified webhook credits it.
- A managed organization has no wallet: both routes answer **403** (40181,
  `ErrManagedOrgHasNoWallet`). The path-less `/wallet` routes and
  `GET /organizations/{orgAddress}/payments` are gone; the wallet history replaces the latter.

`history` is the `walletLedger`, newest first, with `amountCents` signed as applied:

| Kind | Sign | What |
|---|---|---|
| `topup` | + | a verified Stripe top-up, net of VAT (key: the session id) |
| `payment` | − | a process publish or census headroom (`processId`, and `processOrgAddress`: the managed organization that spent it) |
| `creditBack` | + | a payment returned to the wallet: a paid draft deleted (`refund:<process id>`), or a publish that failed for good (`publishfail:<process id>:<attempt>`) |
| `refund` | − | a top-up refunded by hand in the Stripe dashboard (`striperefund:<refund id>`) |

The ledger is the append-only audit trail; the wallet document is authoritative for the
balance. Every write is one conditional update that carries its idempotency condition
(`appliedKeys $ne <key>`) and, for a debit, the balance condition (`balanceCents $gte`) in the
same filter (all but a Stripe refund's debit, see below): no double spend across replicas, no transactions. `appliedKeys` is bounded to the
5 000 most recent keys (`walletAppliedKeysWindow`); an operation older than that is caught by the
ledger's unique index on `idempotencyKey`. Charges to one wallet are also serialized by an
in-memory lock (single API instance).

The balance is negative only after a Stripe refund of credit already spent
([Stripe refunds of top-ups](#stripe-refunds-of-top-ups)): every debit is then refused until a
top-up covers the debt, and a shortfall (due − balance) already counts it.

## Payment state

Payment state lives in its own `processPayments` collection keyed by the process id —
deliberately **not** embedded in `votingProcesses`, whose write paths replace whole
documents. Every payment is a wallet debit; the card only ever fills a wallet.

```
 (none) ──POST /checkout──► pending ──webhook credits the top-up──► pending (consumed)
   │                          │                                         │
   │   expired / async failed │                                         │ publish debits
   │   / cancelled: row goes ◄┘                                         ▼
   ├──────────── publish debits the wallet (no checkout) ──────────► paid ──draft deleted──► refunded
   │                                                                     ▲   (credit back)
   └── free (net €0): publish records a €0 paid envelope ────────────────┘
```

- `pending`: a wallet top-up checkout is open for the process (`checkoutSessionId`,
  `amountCents` = the price it was opened for, `topUpCents` = what it charges, `requestedBy`).
  The row holds the once-per-organization add-on claims across the checkout. `consumed` is set
  when the webhook credits that top-up: the session is spent and the money is in the wallet, so
  a consumed row no longer blocks a manual publish, and is replaced by a new checkout, the wallet
  payment or the €0 envelope, or dropped on cancel/delete without any Stripe call.
- `paid`: debited from the payer's wallet (`payer`). `amountCents` is the envelope; it keeps the
  `checkoutSessionId` of the top-up that funded it, if any. Publishing again is free, and every
  publish path re-prices against `amountCents`. The draft stays **editable** (an edit can only
  repair a draft that failed publish preflight — the debit re-prices it, so it cannot
  under-charge), its census may grow while the projected price fits inside what was paid, and
  the envelope can be raised ([Buying census headroom](#buying-census-headroom)).
- `refunded`: the draft was deleted and its payment credited back to `payer`
  ([Deleting a paid draft](#deleting-a-paid-draft)). Terminal; kept as the audit trail.
- `failed`: a wallet payment that a publish failing for good reverted
  ([Publish retries and credit-backs](#publish-retries-and-credit-backs)): `walletAttempt` moved
  on, and `creditBackCents` is what it owes `payer`. The draft is unpaid again; a publish, a new
  checkout or the €0 envelope replaces the row, keeping its `walletAttempt`.

Every transition is a single conditional Mongo write whose **filter is the state
machine** (`db/process_payments.go`): matching the filter is winning the transition,
`MatchedCount` reports it, and the top-up's session id is part of the filter of every
transition the webhook makes, so a stale or replayed session loses its compare-and-set instead
of acting twice.

## Publication gate and the debit

0. `GET /pricing?voters=…&emailTwoFA=…` — the public calculator over the same formula (no auth,
   no draft; for the pricing page). Branding and the custom URL are charged as requested — it
   has no organization context.
1. `GET /processes/{id}/price` — the server-side quote for a draft (line breakdown, flags,
   current payment status). The price shown before payment is always a server calculation.
2. `POST /processes/{id}/publish` runs the gate (`paymentDueForPublish`), which only reads:
   quote-only → **422** (40176); a `pending` row whose top-up is still open → **409** (40177): pay
   it, or cancel it with `DELETE /processes/{id}/checkout`, then publish; otherwise
   `due = total − paid`, and when the payer's balance does not cover it → **402**. A standard
   organization gets 40178 with `{lines, totalCents, …, balanceCents, shortfallCents}` (the
   quote, flattened, plus what the wallet lacks); a managed one gets 40175 with
   `{requiredCents, availableCents}` of its integrator's wallet. The `GET .../validation`
   dry-run lists the same refusals.
3. **The debit** happens in exactly one place: inside `startProcessPublish`, after the publish
   claim, so two concurrent publishes cannot both debit and a delete cannot credit back between
   the debit and the publish. It claims the once-per-organization add-ons, re-prices, and debits
   `total − paid` from the payer's wallet under the payer's wallet lock, keyed
   `<process id>:<price>` for a payment's first attempt and `<process id>:<attempt>:<price>` after
   a credit-back: a retry at the same price debits nothing, a retry of a draft that grew debits
   only the difference. The balance can move between gate and debit; the debit's own
   refusal (402, claims released) is the authoritative answer. The paid record (`payer`, the
   new envelope) is written right after the debit and is not optional: a payment the wallet may
   not write over — an open checkout, a planned refund — is refused (409) before any money moves,
   and a record write that still fails is a 500 a retry heals without charging again. Anything
   failing after that is retried or credited back, below.

Free processes (net €0) publish directly and record a €0 `paid` envelope.

### Publish retries and credit-backs

A publish answers **202** with its job even when the tx queue is full. A transient failure — the
queue full, the chain not answering or not confirming a batch — is retried by the service
itself, up to 5 times with a 5 s → 80 s backoff (about 2.5 minutes in all). The job stays
`pending` meanwhile, so clients polling `GET /jobs/{jobId}` should wait at least 3 minutes
before treating a publish as stuck. The publish claim is held and refreshed throughout, while
the organization's tx lock is released between publish attempts, not held across the backoff.

A publish that then fails for good — retries exhausted, a permanent failure (the draft cannot be
built, permission, a panic), the publish request failing after the debit, or a worker that died
with the service (the stale-marker sweep at startup and again `PublishStaleAfter` later) — is
**credited back** when none of its elections was mined, for every payer alike: the payment
becomes `failed` with `walletAttempt` incremented and the amount owed in `creditBackCents`, then
`payment.payer`'s wallet (never an organization re-resolved at that time) is credited with a
`creditBack` row keyed `publishfail:<process id>:<attempt>`. Publishing again charges again,
under the next attempt's key, whether from the wallet or through a new top-up checkout (which
keeps the attempt). The revert comes before the credit so that a crash in between leaves the
payer owed, never a draft both credited and still `paid`. An owed credit that has not yet
posted is applied the next time the draft is touched: a publish, a census headroom charge, a
new checkout, or the draft's delete (or another reversal). Once-per-organization add-ons
another process already relies on are withheld from the credit-back, as in a delete's. A
publish that mined some elections keeps the debit: those elections exist, and publishing again
resumes the rest for free.

A top-up webhook redelivered after its publish was credited back finds the row `failed` and
stops: the credit is not repeated (session key), nothing is debited and nothing is published.

## Topping up for a process

`POST /processes/{id}/checkout {returnURL, locale}` (Admin, JWT only) is how a standard
organization pays a draft its wallet does not cover. It quotes the draft, claims the add-ons,
and opens a wallet top-up for the **shortfall** (`total − balance`, at least €1) carrying
`wallet_topup_org` and `voting_process_id` metadata; it stores the `pending` row.

- 400 when the process is free or the wallet already covers it (publish directly); 403 (40181)
  for a managed organization; 422 above 50 000 voters; 409 (40177) when the draft is already
  paid (a paid draft whose census outgrew its price buys the difference with
  `POST /processes/{id}/census/checkout`) or its last checkout completed and is being credited.
- An open session is **reused** while both the price it was opened for and what it charges
  still equal the current total and shortfall; otherwise it is **expired first**, then replaced —
  an expiry failure refuses the checkout rather than leave two payable sessions.
- `GET /processes/{id}/checkout` reports the row with the live session state, for polling only.
- `DELETE /processes/{id}/checkout` cancels a pending row: an open session is expired (a
  completed one is left: its money lands in the wallet anyway), the row goes and its add-on
  claims are released. 200 `"OK"`; a repeat is 404 (no payment); 409 for a paid or refunded one.
- `checkout.session.expired` / `async_payment_failed` drop the row holding that session and
  release its claims; a newer checkout of the same draft, and a row whose top-up was already
  credited (consumed), are left alone.

When the top-up is paid the webhook credits the wallet and then follows up
(`applyProcessTopUp`): it acts only on the row of **this** session, consumes it (a
compare-and-set on the session id) and publishes as the requesting user
(`publishPaidProcess`, which re-prices, re-runs preflight and calls `startProcessPublish`: the
debit is the one inside it). A row already consumed (a crash between credit and publish) is
published by the redelivery; a row paid by this session publishes again for free. It publishes
only at the price the checkout was opened for or less: a draft whose price rose since is never
debited more than the customer agreed to. The credit **always stays in the wallet**; nothing is
ever refunded to the card.

| Webhook outcome | Answer |
|---|---|
| credited and published (or already published) | 200 |
| no row, another session's row, refunded, price rose, wallet short, draft changed, deleted or failing preflight, requesting user gone | 200 — the credit stays for a manual publish |
| a read error, another publish holding the claim | 500 — Stripe redelivers, the credit is not repeated (session key) |

## Webhook handling and idempotency

`stripe/webhook.go` routes the one-time payment events:

- `checkout.session.completed` / `checkout.session.async_payment_succeeded`: dispatched on
  `wallet_topup_org` **first** — every payment session carries it; a session without it is a
  subscription checkout, owned by the subscription webhooks. A session whose `payment_status`
  is not `paid` (a delayed method still clearing) credits nothing yet. The net paid amount
  (subtotal − discount, VAT excluded) is credited under the session id, with the payment intent
  and the VAT-included total a later refund needs; only then is `voting_process_id` (and the
  headroom metadata) parsed, so metadata that names no process still keeps the credit.
- `checkout.session.async_payment_failed` / `checkout.session.expired`: nothing was credited;
  a top-up opened for a process drops its pending row.
- `refund.created` / `refund.updated`: [Stripe refunds of top-ups](#stripe-refunds-of-top-ups).
  The webhook endpoint must be subscribed to them.

There is **no durable event store**. Idempotency is a property of every side effect:

| Side effect | Guard |
|---|---|
| wallet credit, debit, credit-back, refund debit | `appliedKeys $ne` in the same atomic wallet update |
| ledger audit row | unique index on the operation's idempotency key |
| consume a pending row | compare-and-set on status, session id and `consumed` |
| publish | the publish claim; publishing a published process is a no-op |

A replayed event (Stripe retry, restart, second replica) loses every guard and is a no-op. An
event that can never become valid — an organization that is not an address, a process id that
is not an ObjectID — is answered **200** and dropped (`errPermanentEvent`): retrying it for
three days would only bury the events that can still succeed.

## Integrators (managed organizations)

An integrator is an organization flagged `integrator`, opted in at creation
(`POST /organizations` with `integrator: true`). It manages any number of organizations and has no
quota of its own: what it spends is bounded by its wallet balance and, optionally, the spend cap it
sets on each managed organization (below). Its own organization holds no processes (403, 40174); to
run its own votes it creates a managed organization of itself.

The migration that introduced the flag (0035) also flagged organizations on an active non-default
integrator plan. Subscribing to an integrator plan in Stripe no longer grants integrator status:
the organization has to be created with `integrator: true`. There is no command to flag an
existing organization later.

Integrators publish on behalf of managed organizations, so the payer is not present at publish
time: a managed organization's process is debited from the integrator's wallet by the same
debit as any other, and a short wallet answers 402 (40175, `{requiredCents, availableCents}`),
claims released, retryable. A managed organization cannot open a top-up (`/checkout` and the
wallet routes answer 403, 40181); the integrator tops up its own wallet. The integrator's
history names, in `processOrgAddress`, the managed organization each `payment` was for. The
one payment route a managed organization does use is `POST /processes/{id}/census/checkout`,
which debits the integrator's wallet at once (see below); it is also the only payment route
callable with an API key, because integrators drive census growth with their keys. With an API
key an integrator also reads a draft's price (`GET /processes/{processId}/price`,
`voting:write`) and sets a managed organization's spend cap (`PUT`, `managed:write`).

### Spend cap per managed organization

The spend cap applies to managed debits only: a standard organization paying from its own
wallet has none, and its debits and credit-backs never touch `orgSpend`.

- **Cap**: `PUT /integrator/organizations/{orgAddress}` with `{"spendCapCents": n}` (EUR cents,
  VAT excluded; `null` = unlimited, the default). Callable with an integrator Admin JWT or a
  `managed:write` API key. The target must be managed by the caller's integrator (404
  otherwise), and a negative cap is 400. The cap is stored on the integrator's wallet document as
  `orgSpend.<lowercase managed address>.capCents`, so it is one more condition in the debit's
  filter (`$expr`) next to the balance and the idempotency key: concurrent debits cannot pass it,
  and the check and the charge are one atomic write. A debit past the cap is refused before
  anything is charged with **402 (40183)**, data `{capCents, spentCents, requiredCents}`; as for
  40175, the once-per-organization add-on claims taken for that attempt are released. The same
  applies to census headroom bought from the wallet. A cap below what is already spent only
  refuses further debits; it does not claw anything back.
- **Spent**: `orgSpend.<managed address>.spentCents` is what that organization's processes hold
  debited from the wallet. Each managed debit adds its amount; a deleted draft's credit-back and
  a failed publish's credit-back take their credited amount off again, so an add-on withheld
  from a credit-back stays counted.
- **Listing**: `GET /integrator/organizations` items and the `PUT` response carry
  `spendCapCents` (`null` = unlimited) and `spentCents` alongside the organization's fields.

## Once-per-organization add-ons are claimed, not just checked

Branding and the custom URL are the add-ons sold only once per organization, so two processes
paying at the same time must not both be charged for one — and neither may quietly lose it.
Both run on one mechanism keyed by `db.OrgAddOn` (`"branding"`, `"customUrl"`: the bson name
of the add-on in `addOns` and in the payment, and the prefix of the organization fields
`<addOn>PaidAt`, `<addOn>ClaimedBy`, `<addOn>ClaimedAt` — `brandingPaidAt`…,
`customUrlPaidAt`…). Each add-on has its own claim and stamp, so claiming, stamping or
releasing one never touches the other. The claim is a conditional write on the
**organization** document (`db.ClaimOrganizationAddOn`), taken for every priced, unstamped
add-on at the two moments a price is fixed: opening a top-up checkout for a process, and the
wallet debit.

Read paths (`GET .../price`, the publish gate) only *read* the claims, so quoting never takes
one. A caller that loses a claim has that add-on dropped from its input and the quote
recomputed **before** it is charged; the add-ons it did claim stay. Validity is re-derived
from the claimant's payment, so a claim self-heals: once it is older than
`db.OrgAddOnClaimStaleAfter` (2 minutes), it is releasable when that payment is absent,
`failed`, `refunded`, or no longer charges that add-on (the draft was re-priced without it).
A fresh claim is never taken over — the cutoff is part of the claim's filter — and every
paying attempt refreshes its own claims before storing its payment, so a sibling cannot read a
half-written retry and steal an add-on. The cost is that an abandoned checkout frees its
add-ons for the organization only after two minutes. An attempt refused before any money
moves — a quote-required price, a wallet that does not cover it, a Stripe or payment-state
failure at checkout — does not wait for that: it gives back the claims it just took
(`db.ReleaseOrganizationAddOnClaim`, pinned to the exact claim it wrote, so a concurrent
retry's refresh is kept), never one its draft held before. A pending checkout dropped on
purpose — cancelled with `DELETE /processes/{id}/checkout`, or deleted with its draft — gives
its claims back too, pinned to the claims read before dropping it. Cancel and checkout both
take the draft's publish claim, so a cancel never releases the claims of a session that a
concurrent checkout of the same draft is storing.
The wallet debit stamps `<addOn>PaidAt` for each add-on the payment charged
(`db.SetOrganizationAddOnPaid`); a credit-back on delete releases both the stamp and the claim
(`db.ReleaseOrganizationAddOn`). A process that paid for an add-on keeps it in its own price
after the stamp (the payment records `branding` / `customUrl`), so its census growth is never
re-quoted without the €149 or €99 it already paid.

The reverse holds too: a process paid **without** one of them stays priced without it, even
once the claim that kept it out frees up, since nothing sells an add-on to an already-paid
process. An edit turning branding or the custom URL on for such a draft is refused (409, code
40177, "the draft is paid without …, which cannot be added after payment") unless the
organization has already paid that add-on, in which case it changes no price.

## Buying census headroom

`ProcessPayment.AmountCents` is the envelope: a published paid process may grow its census
as long as the **projected price at the new size still fits inside it**. On graduated bands
every voter above the free tier is priced, so growth past the census that was paid for costs
the difference unless headroom was bought beforehand. A process that
published for free (inside the free tier) records a €0 `paid` payment at publish, so its
envelope is €0 and growing it out of the free tier is sold like any other growth. A process
published before pay-per-process has no payment at all: its first growth check grandfathers it
with a `paid` payment at the price of the census it holds, so what it already had stays free.
`PUT /processes/{processId}/census` applies the guard, and so do the memberbase changes that
grow a group-backed census (member create and import, group update), naming the process in
the 402 so the client knows which one to top up before retrying. The check and the census
write it allows run under one per-organization lock, so parallel requests cannot each pass
against the same size; a bulk import holds it until its members are propagated. The publish
gate re-prices as a backstop.

Growth past that price answers **402** (40178) with a `ProcessCensusGrowthQuote`
(`{lines, totalCents, paidCents, dueCents, censusSize, currency, balanceCents, shortfallCents}`:
`shortfallCents` is what the payer's wallet lacks of `dueCents`) rather than a flat refusal, and
`POST /processes/{processId}/census/checkout {censusSize, returnURL, locale}` sells exactly that
difference, for every payer, from the wallet:

- **The wallet covers it** — the difference is debited at once (`chargeProcessWallet`, the same
  debit a re-publish would use, under the payer's wallet lock, re-reading the envelope inside
  it) and the envelope raised; the response carries no session and `amountCents` = what was
  debited. This is the path an API key may take.
- **A managed organization's integrator wallet falls short** — 402 (40175), as at publish.
- **A standard organization's wallet falls short** — a wallet top-up checkout for
  `max(shortfall, €1)`, carrying the target envelope in `process_topup_cents` and the envelope
  it was priced from in `process_topup_from_cents`. No row is stored (the payment is `paid` and
  keeps its envelope), so an abandoned top-up leaves nothing to reconcile. Once the webhook
  credits it, it debits the headroom only while the process still paid exactly
  `process_topup_from_cents`; a replay finds the envelope raised and debits nothing, and any
  refusal (the process deleted, the envelope moved, a planned refund) leaves the credit in the
  wallet, where a new `census/checkout` spends it. Opening this checkout is refused to API keys
  (403).

The growth guard itself never charges: it can only project an **upper bound** (an id naming
no member still counts as growth), and money must never move against a projection. The
caller of `census/checkout` names the size, which is what makes charging there correct.
The projection also ignores ids that are already participants, so re-sending a batch that
already landed is not refused as growth it is not.

## Deleting a draft

`DELETE /processes/{processId}` on a draft with a `pending` row expires its open top-up first
(a consumed row, or a completed session, needs no Stripe call: that money lands in the wallet
whatever happens to the draft); if Stripe cannot confirm an open session expired, the delete
fails closed rather than leave a payable session for a process that no longer exists. The row
goes with the draft and its add-on claims are released. A `failed` row first gets the
credit-back it may still owe applied, then goes with the draft.

### Deleting a paid draft

A paid draft **credits its money back first, then deletes**: the whole envelope (census
headroom included), less any withheld add-on, is credited to the wallet that paid it
(`payment.payer`, never an organization re-resolved at delete time) as a `creditBack` row under
`refund:<process id>`, for standard and managed organizations alike. Money never goes back to a
card. A €0 envelope, or one the withheld add-ons cover entirely, credits nothing.

Before any money moves, the delete fixes a **refund plan** on the payment
(`refundWithheldCents`: what it keeps back, 0 unless an add-on is relied on — see below — and
`refundWithheldAddOns`: which add-ons that is, written in the same update), conditional on the
payment still being `paid` at the amount the delete read, and under the payer's wallet lock,
which a headroom charge holds from its debit to its record, so the debit is never left between
a credited envelope and a refused record. A retried delete reuses the plan, so it credits the
same amount under the same key even if a sibling started or stopped relying on an add-on
meanwhile. A planned payment is locked: census headroom and publish refuse it with **409**, and
a headroom top-up landing later only credits the wallet. A delete that failed half-way leaves a
draft that can only be deleted again.

Then every once-per-organization add-on the payment charged and the plan does not withhold has
its organization claim and `<addOn>PaidAt` stamp released (so the organization's next process is
quoted it again) — before the payment is marked, so a release that fails fails the delete and a
retry runs it again — the payment becomes `refunded`, its document is **kept** as the audit
trail, and only then is the draft removed. If the credit fails, nothing else is written and the
draft stays. The reverse — credited, but the draft not removed — leaves a draft that can only be
deleted again: publish refuses it with **409**, since the debit's key is
still applied and a re-publish would otherwise go on chain unpaid.

**Add-ons already relied on.** Branding and the custom URL are paid once per organization, so
a sibling process may already carry one of them for free on the strength of this payment — it
is published, or its own payment is `paid` or `pending` (`db.OrganizationAddOnReliedOn`, per
add-on). A pending checkout counts because it was priced without the add-on and its row pins
that price: once paid it would publish with it and nobody having paid for it. A sibling checkout
that reads the stamp before the release but stores its payment after the check still slips
through; closing it would take a re-check on both sides. Each relied-on add-on is in use and does
not come back: the organization keeps its `<addOn>PaidAt` (the sibling is not re-priced) and the
credit withholds its list price (€149 branding, €99 custom URL), while an add-on nobody relies
on is released and credited back with the rest.

## Deleting an organization

Tearing an organization down never strands money. `DELETE /integrator/organizations/{orgAddress}`
first deletes every draft of the managed organization exactly as `DELETE /processes/{processId}`
does — paid drafts credited back to their payer's wallet, open checkouts expired, owed
credit-backs applied — and stops at the first draft it cannot delete (a publish in flight, a
credit-back or Stripe call that failed), answering with that draft's error and removing nothing
else; a retry resumes. Only then are the remaining (published) voting processes and their
questions deleted with the rest of the organization. Payment rows are kept as the audit trail.

GDPR erasure (`cmd/userdel`) does the same for every organization it tears down: it deletes their
drafts through `api.DeleteOrgDrafts` before erasing anything, with no Stripe secret: an open
top-up checkout is dropped with its draft and left to Stripe's own expiry (paying it meanwhile
only credits the kept wallet). `db.EraseUser` refuses, erasing nothing, while such an
organization still has drafts. Wallets and their history are kept; the command prints the
balance of each torn-down organization's own wallet, to be returned by hand, and a positive
balance never blocks the erasure.

## Stripe refunds of top-ups

The service never refunds. A refund made by hand in the Stripe dashboard is mirrored in the
wallet: on `refund.created` and `refund.updated` with `status: succeeded`, a refund of a payment
intent that credited a wallet top-up — found through the top-up's ledger row, which records
`paymentIntentId` (sparse index) and the VAT-included `grossCents` — is debited from that wallet
net of VAT at the top-up's own ratio, `refund.amount × net credited / amount_total`, rounded
down, as a `refund` row keyed `striperefund:<refund id>`, so a replay, or the `updated` event of
a refund already counted when created, debits nothing; a partial refund and a later one each
debit once. It is debited in full even below zero: the money already left through Stripe, so
this only records it, and it is the only debit that skips the balance check. A refund of
anything else (a subscription invoice, a payment intent that credited no top-up) is ignored
with a log line. Every payment session copies its metadata onto the payment intent
(`payment_intent_data.metadata`), so a human in the dashboard sees which wallet a charge topped
up.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| 40178 | 402 | the organization's wallet does not cover a publish (quote, `balanceCents`, `shortfallCents` in data) |
| 40178 | 402 | census growth beyond the paid price (`ProcessCensusGrowthQuote` in data) |
| 40175 | 402 | the integrator wallet does not cover a managed organization's debit (`requiredCents`/`availableCents` in data) |
| 40176 | 422 | census above 50 000 — custom quote only, refused on every payment path |
| 40177 | 409 | a top-up checkout is open (publish), the draft is already paid (`/checkout`), a checkout is completing, or the payment changed or is being refunded |
| 40183 | 402 | a managed organization's spend cap would be passed (`capCents`, `spentCents`, `requiredCents` in data) |
| 40181 | 403 | a managed organization has no wallet (wallet routes, `/checkout`) |
| 40174 | 403 | an integrator's own organization cannot own processes |
| 40182 | 400 | the organization already has 5 admins/managers (pending invitations included) |

## Storage summary (migration 0034)

- `processPayments` — one document per process payment (`_id` = process id): `status`,
  `payer` (the wallet debited), `amountCents` (the envelope), `checkoutSessionId` (the open
  top-up while pending, the one that funded the debit once paid), `consumed`, `topUpCents`,
  `requestedBy`, `branding`/`customUrl` (which once-per-organization add-ons the payment
  charged), a delete's refund plan (`refundWithheldCents`, `refundWithheldAddOns`),
  `walletAttempt` (how many times a failed publish credited the wallet payment back; part of the
  debit and credit-back keys) and `creditBackCents` (what a reverted wallet payment still owes
  its payer). No card state: no payment intent, charge amounts, refund id or quote hash.
- `wallets` — one document per payer (`_id` = organization address). An integrator's per-managed-org
  spend caps and spending live in the optional `orgSpend` map (absent means unlimited, 0).
- `walletLedger` — append-only, unique index on `idempotencyKey` and a sparse index on
  `paymentIntentId` (a top-up's, which a Stripe refund names); rows carry `kind`, the signed
  `amountCents`, `processId`/`processOrgAddress`, and a top-up's `grossCents`.
- `votingProcesses` gains `addOns`; `organizations` gains `brandingPaidAt`,
  `brandingClaimedBy` and `brandingClaimedAt`, and `customUrlPaidAt`, `customUrlClaimedBy` and
  `customUrlClaimedAt` (the once-per-organization custom URL, no migration: absent means unpaid).

## Testing the flow

Three layers, from hermetic to manual:

1. **Signed-webhook e2e (CI, no network, no cards)** —
   `api/stripe_checkout_webhook_test.go` and `api/stripe_refund_webhook_test.go` sign event
   payloads with the real Stripe v1 signature scheme (`stripe-go/webhook.GenerateTestSignedPayload`)
   and POST them to `/subscriptions/webhook`, driving signature validation, event parsing, the
   wallet credit, server-side publication and refund sync through the production code path.
   Card numbers never appear here: the card only matters inside Stripe's browser step, and the
   webhook is Stripe's signed statement of the outcome.
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
   | Card `4242 4242 4242 4242` | immediate success → webhook credits the wallet → the process auto-publishes from it |
   | Card `4000 0025 0000 3155` | 3DS challenge, then success |
   | Card `4000 0000 0000 9995` | declined (insufficient funds) — the session stays open, retry in the form |
   | SEPA IBAN `DE89370400440532013000` | delayed method: nothing credited until `async_payment_succeeded`, which credits and publishes |
   | SEPA IBAN `DE62370400440532013001` | delayed failure: `async_payment_failed` → the pending row goes, a new checkout opens a new session |
   | `stripe events resend <evt_…>` | replayed event is a no-op (no second credit, debit or publish) |
   | Dashboard refund of a top-up | `refund` row in the wallet history, net of VAT; the balance may go negative |

   Also verify: VAT appears on top of the net line once a billing address is entered; the
   invoice is generated (invoice_creation); a top-up credits the net amount exactly once.

## Open questions / follow-ups

- Census growth of a published process is governed by its envelope, which `census/checkout`
  can raise; subscription plans no longer limit it.
- Payments recorded before the custom URL became once per organization priced it per process
  and do not record `customUrl`: they neither stamp the organization nor withhold or release it
  on refund, and their growth quotes leave the €89 they paid out of the projection, so that much
  census growth fits the envelope for free (accepted, stage data only).
- Chargebacks are out of scope: a disputed top-up is not mirrored in the wallet automatically.
- A top-up refunded in the dashboard before its completed event was credited is ignored (no
  ledger row names its payment intent yet); a human cannot realistically do that.
- VAT treatment of wallet top-ups (single- vs multi-purpose voucher) needs accountant
  sign-off before launch; `automatic_tax` is on for top-up sessions.
- The spec's "publication retries keep the debit, debits are never silently reversed" is
  superseded: a publish that fails for good before mining anything is credited back, with a
  ledger row. The HackMD spec needs the same update.
- An open top-up checkout of an organization erased by `cmd/userdel` stays payable until Stripe
  expires it, and paying it credits the kept wallet.
- Legacy subscriptions convert to process credits and the subscription system is
  removed in the follow-up PR (see the spec's "Legacy plans transition").
