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
  balance moves only when the verified webhook credits it. `GET /wallet/checkout/{sessionId}`
  reports the session's Stripe state to the organization's admins; the money is there once
  `GET /organizations/{orgAddress}/wallet` shows the `topup` row, so poll that. A
  delayed payment (SEPA) shows nothing until it is paid; one that fails or expires never shows a
  row, and the client opens a new top-up.
- A managed organization has no wallet: both routes answer **403** (40181,
  `ErrManagedOrgHasNoWallet`). The path-less `/wallet` routes and
  `GET /organizations/{orgAddress}/payments` are gone; the wallet history replaces the latter.

`history` is the `walletLedger`, newest first, with `amountCents` signed as applied:

| Kind | Sign | What |
|---|---|---|
| `topup` | + | a verified Stripe top-up, net of VAT |
| `payment` | − | a process publish or census headroom (`processId`, and `processOrgAddress`: the managed organization that spent it) |
| `creditBack` | + | a payment returned to the wallet: a paid draft deleted, a publish that failed for good, or an organization teardown returning an add-on price nobody received (naming the process that withheld it). `withheldCents` and `withheldAddOns` are what it kept back for add-ons another process relies on; a delete whose withheld add-ons are all the draft paid for shows `amountCents: 0` |
| `refund` | − | a top-up refunded by hand in the Stripe dashboard |

Each row carries `kind`, `amountCents`, `createdAt`, and where they apply `processId`,
`processOrgAddress`, `withheldCents` and `withheldAddOns`. The idempotency keys the service builds
(`refund:<process id>`, `publishfail:<process id>:<attempt>`, `teardownaddon:<org>:<addOn>:<process id>`,
`striperefund:<refund id>`, the session id of a top-up) are internal and never in the response: a client
tells credit-backs of one process apart by `withheldCents`/`withheldAddOns`, amount and `createdAt`, and finds
its top-up by amount and `createdAt` (or reads the session with `GET /wallet/checkout/{sessionId}`).

**The ledger is the source of truth** for money. Every movement — a top-up, a process payment, a
credit-back, a Stripe refund or dispute and their reversals — is one conditional update of the
wallet document that moves the balance, checks the idempotency key (`appliedKeys $ne <key>`), for
a debit the balance (`balanceCents $gte`) and a managed organization's spend cap, and pushes the
movement's ledger entry onto the wallet's own `unposted` list, all at once. The `walletLedger`
row is a projection of that entry: the movement inserts it and pulls it from `unposted`, and so
does every later read or write of the wallet and a sweep at startup (`DrainUnpostedWalletEntries`,
also run by `cmd/userdel`). After a crash at any point a movement is either wholly there — the
balance and its row — or not applied at all, so the balance always equals the sum of the ledger
once drained. `GET /organizations/{orgAddress}/wallet` may therefore write (it posts what is
waiting) before it reads. Keys are per wallet: the ledger's unique index on (`orgAddress`,
`idempotencyKey`) and `unposted` catch a replay of any age; `appliedKeys`, bounded to the 5 000 most
recent keys (`walletAppliedKeysWindow`), only guards two identical movements racing. No double
spend across replicas, no transactions.

What a process paid is never stored: it is read off its payer's ledger
(`db.ProcessLedgerState`): the sum of its `payment` rows less every `creditBack` row and what it
withheld (a withheld add-on stays the organization's stamp, not the process's), the number of
`publishfail` credit-backs (the attempt of its next debit), the once-per-organization add-ons its
live attempt paid for, whether a delete refunded it, and an add-on release the last credit-back
still owes. A published process adds a base it paid outside the wallet (`baseCents`, below).
Every decision about a process's money — the publish gate, the debit, census growth, a
credit-back and what it withholds — reads that state under the payer's wallet lock. That lock is
in-memory, like the organization tx lock: the money movements are safe across replicas, but the
decisions assume one API instance (a database lease would be the upgrade path).

Every in-memory lock a request waits on — the wallet, census growth, draft, write-seat and
unfunded managed-organization locks — is waited for at most 10 s (or until the request ends),
then the request answers **503** (50302, retry later) with nothing changed. Every 503 carries
`Retry-After: 10` unless the handler set its own delay (`errors.Error.Write`). Only a failed
publish's credit-back, which no request waits on, waits as long as it takes.

The balance is negative only after a Stripe refund of credit already spent
([Stripe refunds of top-ups](#stripe-refunds-of-top-ups)): every debit is then refused until a
top-up covers the debt, and a shortfall (due − balance) already counts it.

## Payment state

The payment row lives in its own `processPayments` collection keyed by the process id —
deliberately **not** embedded in `votingProcesses`, whose write paths replace whole
documents. It holds the state of a top-up checkout and a display of what the process paid; the
money itself is the wallet ledger. Every payment is a wallet debit; the card only ever fills a
wallet.

```
 (none) ──POST /checkout──► pending ──webhook credits the top-up──► pending (consumed)
   │                          │                                         │
   │   expired / async failed │                                         │ publish debits
   │   / cancelled: row goes ◄┘                                         ▼
   ├──────────── publish debits the wallet (no checkout) ──────────► paid ──draft deleted──► (row goes)
   │                                                                  │  ▲     (credit back)
   │                                       failed publish credited ◄──┘  │ publish again
   │                                       back: failed ─────────────────┘
   └── published with nothing to pay: €0 base, paid ────────────────────►
```

- `pending`: a wallet top-up checkout is open for the process (`checkoutSessionId`,
  `amountCents` = the price it was opened for, `topUpCents` = what it charges, `requestedBy`,
  and the add-ons it priced in). The row holds the once-per-organization add-on claims across
  the checkout. `consumed` is set when the webhook credits that top-up: the session is spent and
  the money is in the wallet, so a consumed row no longer blocks a manual publish, and is
  replaced by a new checkout or the wallet payment, or dropped on cancel/delete without any
  Stripe call. This is the one state machine of the row: every transition of it is a
  compare-and-set on the status, the session id and `consumed`.
- `paid`, `failed`, `refunded` are a **display** of the ledger, refreshed by every money path
  under the payer's wallet lock, with `amountCents` = what the process paid (`failed`: what the
  reverted attempt had paid). After a crash between a movement and that refresh they can lag
  until the next money path; nothing reads them for money. `paid`: debited from the payer's
  wallet; publishing again is free, and every publish path re-prices against what was paid. The
  draft stays **editable** (an edit can only repair a draft that failed publish preflight — the
  debit re-prices it, so it cannot under-charge), and its census grows freely — no member
  change is refused for a draft; the publish debit charges whatever its census now costs beyond
  what was paid (headroom can also be bought ahead,
  [Buying census headroom](#buying-census-headroom)). `failed`: a publish that failed for good
  was credited back ([Publish retries and credit-backs](#publish-retries-and-credit-backs)); the
  draft is unpaid again. `refunded`: a delete credited it back
  ([Deleting a paid draft](#deleting-a-paid-draft)); the row goes with the draft.
- `baseCents`: written once when a process is published, what it paid outside the wallet — €0
  for a process published with nothing left to pay, the census price for one published before
  pay-per-process ([Buying census headroom](#buying-census-headroom)).

The top-up's session id is part of the filter of every transition the webhook makes, so a stale
or replayed session loses its compare-and-set instead of acting twice.

## Publication gate and the debit

0. `GET /pricing?voters=…&emailTwoFA=…` — the public calculator over the same formula (no auth,
   no draft; for the pricing page). Branding and the custom URL are charged as requested — it
   has no organization context.
1. `GET /processes/{id}/price` — the server-side quote for a draft (line breakdown, flags,
   current payment status). The price shown before payment is always a server calculation.
2. `POST /processes/{id}/publish` runs the gate (`paymentDueForPublish`), which only reads:
   quote-only → **422** (40176); a once-per-organization add-on the draft selects that another
   draft's payment holds → **409** (40177, naming that draft; `GET .../price` reports it in
   `addOnsHeldBy`); a `pending` row whose top-up is still open → **409** (40177): pay
   it, or cancel it with `DELETE /processes/{id}/checkout`, then publish — when the draft's price
   changed since the checkout was opened, the message says so and points to calling
   `POST /processes/{id}/checkout` again, which replaces the session with one for the whole
   shortfall; otherwise
   `due = total − paid`, and when the payer's balance does not cover it → **402**. A standard
   organization gets 40178 with `{lines, totalCents, …, balanceCents, shortfallCents}` (the
   quote, flattened, plus what the wallet lacks). A draft that already paid part of its price
   (its census grew since) also gets `paidCents` and `censusSize`, and its message points to
   `POST /processes/{id}/census/checkout` for that `censusSize`: a process checkout answers 409
   for a paid draft. A managed one gets 40175 with
   `{requiredCents, availableCents}` of its integrator's wallet. The `GET .../validation`
   dry-run lists the same refusals.
3. **The debit** happens in exactly one place: inside `startProcessPublish`, after the publish
   claim, so two concurrent publishes cannot both debit and a delete cannot credit back between
   the debit and the publish. It claims the once-per-organization add-ons, re-prices, and debits
   `total − paid` from the payer's wallet under the payer's wallet lock, keyed
   `<process id>:<price>` for a payment's first attempt and `<process id>:<attempt>:<price>` after
   a credit-back: a retry at the same price debits nothing, a retry of a draft that grew debits
   only the difference. What was paid is read off the ledger in the same lock hold, so a retry
   after a debit whose request then failed pays only what is still due. The balance can move
   between gate and debit; the debit's own refusal (402, claims released) is the authoritative
   answer. The debit's ledger row is the record of the payment, carrying the price it brought the
   process to, its attempt and the once-per-organization add-ons it priced in; an open checkout,
   or a draft a delete refunded, is refused (409) before any money moves. What follows the debit
   (the add-on stamps, the row's display) is redone by a retry, which charges nothing again.
   Anything failing after that is retried or credited back, below.

Free processes (net €0) publish directly; publication records the €0 base.

### Publish retries and credit-backs

A publish answers **202** with its job even when the tx queue is full. A transient failure — the
queue full, the chain not answering or not confirming a batch — is retried by the service
itself, up to 5 times with a 5 s → 80 s backoff (about 2.5 minutes in all). The job stays
`pending` meanwhile, and the backoff is not its whole duration: each of the 6 attempts runs up
to 3 rounds, and each round waits up to 40 s per question for its election to be mined. On a
stalled chain the worst case is 6 × 3 × 40 s × questions + 2.5 minutes — about 15 minutes for a
one-question process, over 3 hours for one with 15 — so clients poll `GET /jobs/{jobId}` until it
is `completed` or `failed` rather than giving up after a fixed time. The publish claim is held and refreshed throughout, while
the organization's tx lock is released between publish attempts, not held across the backoff.

A publish that then fails for good — retries exhausted, a permanent failure (the draft cannot be
built, permission, a panic), the publish request failing after the debit, a worker that died
with the service (the stale-marker sweep at startup and again `PublishStaleAfter` later), or a
worker that stood down because its claim went stale under it (job error `publish claim lost`) — is
**credited back** when none of its elections was mined, for every payer alike. "Mined" is what the
service recorded: a submitted tx that timed out unconfirmed can still mine after the credit-back,
leaving an unrecorded election nothing can vote in (known limit, issue #822). A worker that
stands down after any of its attempts has already submitted elections, or holding mined
elections it could not record, credits nothing back: what it submitted may be on chain. Nothing
credits it back afterwards except a restart's stale sweep; its stale claim blocks nothing, so publish
again (it charges nothing and resumes or completes the publish) or delete the draft (which credits
it back, or answers 40904 once elections were recorded). Under the payer's
wallet lock the reversal reads what the draft paid off the ledger and credits it to the payer's
wallet (the wallet that was debited: a process's payer never changes) as one `creditBack` row
keyed `publishfail:<process id>:<attempt>`. That one movement makes the draft unpaid, moves its
attempt on and records the add-ons it gives back; the release of those add-ons (pinned to claims
taken before the credit-back) and the row's `failed` display follow, and whatever a crash leaves
of them undone is redone by the next touch of the draft: another reversal, a publish, a census
headroom charge, a new checkout, or the draft's delete. A retry finds nothing paid and credits
nothing. Publishing again charges again, under the next attempt's key, whether from the wallet
or through a new top-up checkout. Once-per-organization add-ons another process already relies
on are withheld from the credit-back, as in a delete's, and stay the organization's. A payment
the withheld add-ons account for entirely gives nothing back and stays `paid`: publishing again
charges nothing. A publish that mined some elections keeps the debit: those elections exist, and
publishing again resumes the rest for free.

A top-up webhook redelivered after its publish was credited back finds the row `failed` and
stops: the credit is not repeated (session key), nothing is debited and nothing is published.

### Submitted elections are settled on chain first

A NEW_PROCESS tx can mine after its publish gave up on it: the 40 s wait ran out while the tx sat
in the mempool, or the service died between the submit and recording the election. So before a
round submits anything, the worker flags the process (`publishSubmitted`) and records each tx on
its question (`publishTxs`: hash, account nonce, metadata URL and the election id the node
predicts). Every tx ever submitted for a question carries the same nonce until the question is
settled, so at most one of them can mine and a question never gets two elections.

Every credit-back (the worker's abandon, the stale-marker sweep, a failure after the debit), a
delete, a teardown and an edit first settle those records against the chain, under the publish
claim:

- **mined** (its hash is in a block): the election is adopted — recorded on its question as a
  publish that confirmed it would. The draft then counts as partially published: it keeps its
  payment, and publishing again resumes it with no second charge and no second election.
- **dead** (no hash of the question is in a block and the organization's nonce moved past the
  records'): no tx of the question can ever mine. The records are dropped, and once none is left
  the process behaves exactly as before: credit-back, edit and delete as usual. A question whose
  nonce never moves is dead too once the chain is 180 blocks (the node mempool's 60-block TTL plus
  a 120-block margin) past the height its records were submitted at, which every record carries
  (`submittedHeight`; one without it never expires this way).
- **unresolved** (anything else, including a chain that does not answer): nothing is credited
  back, and an edit or a delete answers **409** (40903). The draft stays paid — the state a
  delete refunds and a publish reuses — and the next touch settles it again. A publish is never
  blocked: its next round resubmits the question at the same nonce. An operator can also settle a
  draft whose nonce never moves by publishing it on the customer's behalf; there is no support tool
  for it yet.

`cmd/userdel` has no chain client, so a draft still flagged stops the erasure with 409 (40903):
run the service, which settles it, or delete the draft through the API first.

Deployment assumption: the nonce and the tx are read from the same node. `VOCDONI_VOCDONIAPI` must
not round-robin across gateways at different heights, or a tx another gateway has not indexed yet
could be taken for dead.

The election metadata document each question publishes carries `meta.questionId` (the question's
id), which is what makes its content-addressed URL unique per question and lets an election be
matched back to its question.

## Topping up for a process

`POST /processes/{id}/checkout {returnURL, locale}` (Admin, JWT only) is how a standard
organization pays a draft its wallet does not cover. It quotes the draft, claims the add-ons,
and opens a wallet top-up for the **shortfall** (`total − balance`, at least €1) carrying
`wallet_topup_org` and `voting_process_id` metadata; it stores the `pending` row.

- 400 when the process is free or the wallet already covers it (publish directly; an open
  checkout of the draft is expired and dropped first, so the publish is not refused for it); 403 (40181)
  for a managed organization; 422 above 50 000 voters; 409 (40177) when the draft is already
  paid (a paid draft whose census outgrew its price buys the difference with
  `POST /processes/{id}/census/checkout`), a delete refunded it and did not finish (delete it
  again, as publish answers too), or its last checkout completed and is being credited.
- An open session is **reused** while both the price it was opened for and what it charges
  still equal the current total and shortfall; otherwise it is **expired first**, then replaced —
  an expiry failure refuses the checkout rather than leave two payable sessions.
- `GET /processes/{id}/checkout` reports the row with the live session state, for polling only.
  `status` displays the ledger and can lag it briefly after a failure; a paid process's
  `amountCents` is read off the ledger (plus what it paid outside the wallet) when served.
  `status: paid` means the process is published or its publish job is running; `jobId` names
  the latest publish job of the process, the webhook's included, so poll `GET /jobs/{jobId}`
  (every publish job carries the process in `result.processId`, and a failed one says why in
  `errors`). `status: failed` means that publish failed after the payment and was credited back;
  a `failed` row may carry no `jobId` when the failure came before the job existed (the publish
  response said why). `status: paid` with a `failed` job means the publish failed but the
  payment was kept: either some of its elections were mined — the draft can then only be
  published again, which resumes the remaining questions, and cannot be edited or deleted
  (40904); the job's `errors` and the questions' `upstreamId` in `GET /processes/{id}` show it —
  or its add-ons are in use by another process. Either way publish again: it charges nothing.
  A job failed with `publish claim lost` stood down because its publish claim went stale (its
  renewals kept failing for `PublishStaleAfter`, typically a storage outage): `status: failed`
  when it had submitted nothing yet and was credited back, so publish again (it charges again);
  `status: paid` when it had: nothing credits it back except a restart's stale sweep, so publish
  again (it charges nothing and resumes or completes the publish) or delete the draft, as above.
  The one exception is a job failed as interrupted by a service restart (its error says so;
  the supervisor fails an orphaned job after about 2 minutes): the dead worker's publish claim is
  reclaimed only once it is `PublishStaleAfter` (15 minutes, counted from its last renewal) old,
  so until then `POST /publish` answers 409 (40903, "publish already in progress"). After that,
  whichever comes first wins: the stale sweep (at most `PublishStaleAfter` after the restart)
  credits the debit back, `status` turns `failed` and publishing again charges again; or a
  publish takes the claim first, charges nothing and resumes the publish.
  A draft with no row answers 404 with 40179 (no payment or checkout: none was opened, or an
  expired or failed one was dropped — open a new one); 404 with 40038 means the draft is gone.
- `DELETE /processes/{id}/checkout` cancels a pending row: an open session is expired (a
  completed one is left: its money lands in the wallet anyway), the row goes and its add-on
  claims are released. 200 `"OK"`; a repeat is 404 (40179); 409 (40177) when the row is not
  pending (paid, failed), or when the checkout's top-up was already spent on the process's publish
  (its ledger shows it paid): publish again, it charges nothing.
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
debited more than the customer agreed to (the debit prices the census again under the census
growth lock and refuses past that price, recorded like the price-rose refusal). The credit
**always stays in the wallet**; nothing is ever refunded to the card.

| Webhook outcome | Answer |
|---|---|
| credited and published (or already published) | 200 |
| no row, another session's row, refunded, deleted | 200 — the credit stays for a manual publish |
| price rose, wallet short, draft changed or failing preflight, an add-on another draft holds, a census that cannot be priced, requesting user gone or no longer an admin | 200 — the credit stays for a manual publish; the refusal is recorded on the row |
| a failure after the debit, before the job exists (census, signer, CSP key, job) | 200 — the debit is credited back (row `failed`) and the refusal recorded; publish manually |
| busy organization tx lock or census growth lock | 500 — Stripe redelivers; nothing was debited |
| a read error, another publish holding the claim | 500 — Stripe redelivers, the credit is not repeated (session key) |

`GET /processes/{id}/checkout` tells these apart: `consumed: true` with no `refusal` while the row
is `pending` means the publish is being redelivered — the credit is already in the wallet, so
`POST /processes/{id}/publish` may be called at once instead of waiting for Stripe's backoff, and
the redelivery then finds the process published; a `refusal` (`code`, `message`) means the
credit is in the wallet and the process is published by hand. The next payment record of the row
(a publish, a new checkout) drops the refusal.

## Webhook handling and idempotency

`stripe/webhook.go` routes the one-time payment events:

- `checkout.session.completed` / `checkout.session.async_payment_succeeded`: dispatched on
  `wallet_topup_org` **first** — every payment session carries it; a session without it (a checkout of the retired
  subscription model) is ignored. A session whose `payment_status`
  is not `paid` (a delayed method still clearing) credits nothing yet. The net paid amount
  (subtotal − discount, VAT excluded) is credited under the session id, with the payment intent
  and the VAT-included total a later refund needs; only then is `voting_process_id` (and the
  headroom metadata) parsed, so metadata that names no process still keeps the credit.
- `checkout.session.async_payment_failed` / `checkout.session.expired`: nothing was credited;
  a top-up opened for a process drops its pending row.
- `refund.created` / `refund.updated`: [Stripe refunds of top-ups](#stripe-refunds-of-top-ups).
- `charge.dispute.funds_withdrawn` / `charge.dispute.funds_reinstated`: disputes on top-ups, in
  the same section.

The webhook endpoint must be subscribed to these four refund and dispute events on deploy.

There is **no durable event store**. Idempotency is a property of every side effect:

| Side effect | Guard |
|---|---|
| wallet credit, debit, credit-back, refund debit | `appliedKeys $ne` in the same atomic wallet update, after a ledger lookup |
| ledger row | carried by that update (`unposted`), then inserted under the unique index on the key |
| consume a pending row | compare-and-set on status, session id and `consumed` |
| publish | the publish claim; publishing a published process is a no-op |

A replayed event (Stripe retry, restart, second replica) loses every guard and is a no-op. An
event that can never become valid — an organization that is not an address, a process id that
is not an ObjectID — is answered **200** and dropped (`errPermanentEvent`): retrying it for
three days would only bury the events that can still succeed.

## Integrators (managed organizations)

An integrator is an organization flagged `integrator`, opted in at creation
(`POST /organizations` with `integrator: true`). It has no quota of its own: what it spends is
bounded by its wallet balance and, optionally, the spend cap it sets on each managed organization
(below). Each managed organization is an on-chain account the service funds, so until the
integrator has put money into its wallet and kept it — its `topup` rows less the Stripe `refund`
rows (refunds and disputes, net of their reversals) above zero — it creates **one**:
`POST /integrator/organizations` refuses the next with **402 (40184)**, data
`{managedOrgs, managedOrgsCreated, netTopUpCents}`. Creations are counted, not survivors: a
deleted managed organization's account stays funded, so deleting it frees nothing. The count is
`managedOrgsCreated` on the integrator's organization document, only ever incremented (`$inc`,
never written back by `SetOrganization`); an integrator that created organizations before the
counter existed reads 0, so the cap counts at least the managed organizations it has now. That
means **an integrator migrated from before pay-per-process with any managed organization is
under the cap**: its next creation answers 40184 until it tops up. Once the wallet kept a top-up
it creates any number, whatever its current balance: spending the wallet down to zero keeps
that. `GET /integrator` reports `managedOrgsCreated`, `netTopUpCents` and `canCreateManagedOrg`,
so a dashboard can ask for a top-up before the refusal. For an unfunded integrator the count and
the creation run under a per-integrator lock (a funded one takes none), so two parallel
creations cannot both pass it. The cap is per integrator organization, not per user: a user may
create integrator organizations up to the per-user organization limit (15), each with its one
unfunded managed organization, so one verified account can have the service fund at most 15
managed accounts before any top-up. Its own organization holds no processes (403, 40174); to
run its own votes it creates a managed organization of itself.

The migration that introduced the flag (0034) flagged the existing integrators: the unmanaged
organizations with an `integratorLimits` override granting managed organizations, or, with no
override, an active subscription to a non-default plan that granted them — the previous
integrator check, override first (an override of 0 revoked the status), except for the default
plan, which granted managed organizations to every organization. Holding API keys or managing
organizations was not enough on its own, since the default plan allowed both. The migration
leaves `integratorLimits` and the managed counters in place for a rollback; migration 0036
archives and removes them (see "Legacy subscriptions" below). Subscribing to an integrator plan in Stripe no longer grants integrator status:
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
  otherwise), and a negative cap is 400. The key is required and must be the only one: `{}` or a
  misspelt key is 400 rather than a silently lifted cap. The publish gate and
  `GET /processes/{processId}/validation` check the cap too, so they answer 40183 before the
  debit would. The cap is stored on the integrator's wallet document as
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
one. An add-on another draft holds stays priced in, never priced out — the holder may never pay
for it, and the draft would publish with it for free: `GET .../price` names the holder in
`addOnsHeldBy`, and every paying path (the gate, the validation dry-run, the debit, checkout, the
top-up webhook) refuses with **409** (40177) until the holder's payment completes (the stamp then
makes the add-on free) or is cancelled. A paying attempt that loses a claim between its quote and
its claim gets the same 409 and gives back the claims it took. A stale claim whose claimant paid
for the add-on is the stamp that was never written (that write only logs a failure): the next
quote writes it and prices the add-on out. Validity is re-derived
from the claimant's payment, so a claim self-heals: once it is older than
`db.OrgAddOnClaimStaleAfter` (2 minutes), it is releasable when that payment is absent,
`failed`, `refunded`, or no longer charges that add-on (the draft was re-priced without it).
A fresh claim is never taken over — the cutoff is part of the claim's filter — and every
paying attempt refreshes its own claims before storing its payment, so a sibling cannot read a
half-written retry and steal an add-on. The cost is that an abandoned checkout keeps its
add-ons from the organization for as long as its pending payment still charges them, since a
pending checkout is a claimant that is still paying: the claim is freed only when Stripe's
`checkout.session.expired` arrives (up to 24 hours) or the checkout is cancelled with
`DELETE /processes/{id}/checkout`. The two minutes only gate takeover of claims no payment
backs. An attempt refused before any money
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

The reverse holds too: a process paid **without** one of them stays priced without it, since
nothing sells an add-on to an already-paid process. An edit turning branding or the custom URL on for such a draft is refused (409, code
40177, "the draft is paid without …, which cannot be added after payment") unless the
organization has already paid that add-on, in which case it changes no price. The publish checks
again, under the payer's wallet lock: such an add-on is free only while the organization is
stamped for it, so if the stamp was credited back since the edit, the publish answers **409**
(40177) until the add-on is deselected or another process pays for it.

## Buying census headroom

What a process paid (its ledger, plus `baseCents` once published) is its envelope: a published
paid process may grow its census as long as the **projected price at the new size still fits
inside it**. On graduated bands every voter above the free tier is priced, so growth past the
census that was paid for costs the difference unless headroom was bought beforehand. A process
that published for free (inside the free tier) records a €0 base at publish, so its envelope is
€0 and growing it out of the free tier is sold like any other growth. A process published before
pay-per-process has neither a base nor a ledger row: its first growth check records its base at
the price of the census it holds, so what it already had stays free (a published process whose
own base write failed gets €0, since its ledger already holds what it paid).
`PUT /processes/{processId}/census` applies the guard, and so do the memberbase changes that
grow a group-backed census (member create and import, group update), naming the process in
the 402 so the client knows which one to top up before retrying. Only processes with a question
taking votes are guarded — the elections the change resizes on chain: a draft, paid or not, is
never a reason to refuse a member, since its publish debit prices the census it publishes and
charges the difference from what it paid. The check and the census write it allows run under
one per-organization lock, so parallel requests cannot each pass against the same size; a bulk
import holds it until its members are propagated. A publish takes the same lock (before the
organization's tx lock) from reading the census it prices through storing it, so a member added
meanwhile is priced, stored and in the election's `maxCensusSize`; a busy lock answers 503
(50302). From then until the process's publish claim is released — when its publish job ends
(completed or failed, retries included), or up to 15 minutes after a service restart
(`PublishStaleAfter`), as for `POST /publish` above — those memberbase changes answer **409**
(40903, "a publish of process … is in progress; retry once it completes") and write nothing
(except a member upsert whose edit turned into a creation, below): the
elections still to mine are built from the census already stored, and nothing would resize them.
`POST` and `DELETE /processes/{id}/checkout` and an eligibility update of the draft take the
same claim for the length of their request, so a member change racing one of them gets the same
short-lived 409. Once the claim is released, a member added is growth of a published process,
priced by the guard above and resized on chain. The resize itself prices each process again at
the recounted size, and a question whose census outgrew what its process paid is left at its size
and reported in the response's `errors`, never put on chain unpaid (and logged: an async import,
`?async=true` or one forced async, reports no propagation errors, see issue #628). To recover:

1. re-send a member who is already a participant to `PUT /processes/{id}/census`: it answers
   402 (40178) with the `censusSize` and `dueCents` the process still owes. A managed organization
   may get 402 40175 (the integrator wallet is short) or 40183 (spend cap reached) first, with no
   `censusSize`: top up the integrator wallet or raise the cap, then repeat this step;
2. buy that headroom with `POST /processes/{id}/census/checkout {censusSize}`, passing the
   `censusSize` from the 402;
3. re-send the same `PUT /processes/{id}/census`: with no growth left to pay for, it pushes the
   census's size on chain.

The checkout alone only debits; it resizes nothing.

A member upsert (`PUT /organizations/{org}/members`) whose edit turned into a creation, because
the member was deleted concurrently, is guarded like a create: when that growth is refused (409,
402, 422 or a busy lock) the member is still written, the answer is a 200 with its `id` and an
`errors` entry "member created but not added to its censuses", and it is in no census. Re-sending
the upsert does not add it, since it now exists: add it with `PUT /processes/{id}/census
{memberIds: [id]}` for a published process, or by editing the draft's census with
`PUT /processes/{id}`.

Growth past that price answers **402** (40178) with a `ProcessCensusGrowthQuote`
(`{lines, totalCents, paidCents, dueCents, censusSize, currency, balanceCents, shortfallCents}`:
`shortfallCents` is what the payer's wallet lacks of `dueCents`) rather than a flat refusal, and
`POST /processes/{processId}/census/checkout {censusSize, returnURL, locale}` sells exactly that
difference, for every payer, from the wallet:

- **The wallet covers it** — the difference is debited at once (`chargeProcessWallet`, the same
  debit a re-publish would use, under the payer's wallet lock, re-reading what was paid inside it
  and applying only while that is still what the difference was priced from); the response
  carries no session and `amountCents` = what was debited. A headroom debit prices in no add-on of
  its own, so it never stamps a new one. This is the path an API key may take.
- **A managed organization's integrator wallet falls short** — 402 (40175), as at publish.
- **A standard organization's wallet falls short** — a wallet top-up checkout for
  `max(shortfall, €1)`, carrying the target envelope in `process_topup_cents` and the envelope
  it was priced from in `process_topup_from_cents`. No row is stored, so an abandoned top-up
  leaves nothing to reconcile. Once the webhook credits it, it debits the headroom only while the
  process still paid exactly `process_topup_from_cents`; a replay finds the envelope raised and
  debits nothing, and any refusal (the process deleted or refunded, the envelope moved — a failed
  publish credited back meanwhile) leaves the credit in the
  wallet, where a new `census/checkout` spends it. This checkout has no status of its own: poll
  `GET /processes/{id}/checkout`, whose `amountCents` reaches the new price once the headroom is
  debited, and the wallet history (`GET /organizations/{orgAddress}/wallet`), which shows the
  `topup` credit and then a `payment` row for the process. A `topup` with no `payment` after it
  means the debit was refused: for a published process, retry the growth, and on its 402
  (`shortfallCents: 0`) call `census/checkout` again to debit the wallet; for a draft, publish it
  again, and the publish debits the difference from the wallet. Until then a published process's
  census stays refused; a draft's only refusal is its publish (402).

The growth guard itself never charges: it can only project an **upper bound** (an id naming
no member still counts as growth), and money must never move against a projection. The
caller of `census/checkout` names the size, which is what makes charging there correct.
The projection also ignores ids that are already participants, so re-sending a batch that
already landed is not refused as growth it is not.

## Deleting a draft

`DELETE /processes/{processId}` on a draft with a `pending` row expires its open top-up first
(a consumed row, or a completed session, needs no Stripe call: that money lands in the wallet
whatever happens to the draft); if Stripe cannot confirm an open session expired, the delete
fails closed rather than leave a payable session for a process that no longer exists. Then what
the draft paid is credited back (below), the row goes with the draft and its add-on claims are
released. A draft whose failed publish submitted an election the chain has not settled yet answers
409 (40903) instead, and one whose submitted election mined is adopted and answers 40904
([Submitted elections are settled on chain first](#submitted-elections-are-settled-on-chain-first)).

### Deleting a paid draft

A paid draft **credits its money back first, then deletes**, in one hold of the payer's wallet
lock, which every charge of that payer takes too: it reads what the draft paid off the ledger
(census headroom included, whatever the payment row says), decides what to withhold, and credits
the rest to the wallet that paid it as one `creditBack` row under `refund:<process id>`, for
standard and managed organizations alike. Money never goes back to a card. The row records what
it kept back (`withheldCents`, `withheldAddOns`) and the add-ons it gives back, so the history
explains a credit smaller than the payment, and every payment of a deleted draft is settled by a
row: one the withheld add-ons cover entirely is a credit-back of 0 that withholds it all. A
census headroom charge racing the delete either lands first and is credited back with the rest,
or finds the draft refunded and is refused.

Then every once-per-organization add-on the payment charged and the credit-back does not withhold
has its organization claim and `<addOn>PaidAt` stamp released (so the organization's next process
is quoted it again), pinned to claims taken before the credit-back; then the payment row and the
draft are removed. The ledger is the record of what the draft paid and got back. A delete that
failed half-way is retried from the ledger: once credited, the draft paid nothing more and only
the owed release is left to apply. Until then the draft can only be deleted again: publish,
checkout and census headroom refuse it with **409**, since the debit's key is still applied and a
re-publish would otherwise go on chain unpaid.

**Add-ons already relied on.** Branding and the custom URL are paid once per organization, so
a sibling process may already carry one of them for free on the strength of this payment — it
is published, its ledger shows it paid or its top-up checkout is `pending`
(`db.OrganizationAddOnReliedOn`, per add-on), or it is being published (it holds the publish
claim) or has elections mined already: a free draft gets its payment row only once published. A
pending checkout counts because it was priced without the add-on and its row pins that price:
once paid it would publish with it and nobody having paid for it. A sibling checkout that reads
the stamp before the release but stores its payment after the check still slips through; closing
it would take a re-check on both sides. The publish claim is also taken by a checkout, a checkout
cancel and a draft delete, so a branded sibling inside one of those requests at that instant
counts as being published, and the add-on is withheld: no money leaves the system (the
organization keeps the stamp and its next process rides on it free), and the window is one
request. Each relied-on add-on is in use and does not come back: the organization keeps its
`<addOn>PaidAt` (the sibling is not re-priced) and the credit withholds its list price (€149
branding, €99 custom URL), while an add-on nobody relies on is released and credited back with
the rest. The release follows the credit-back as its own write, which the credit-back's ledger row
records as owed (`releaseAddOns`) until a payment of that process follows. A release cut short
(a failed write, a crash) is applied by the next touch of that process, by every quote of a
sibling — pricing reads the claimant's ledger of each claimed add-on before it trusts the stamp,
so a sibling pays for an add-on the organization was credited back — and by a startup sweep.
Every one is pinned to the claimant and to a claim taken before the credit-back, so a late one
never undoes a claim taken since.

## Deleting an organization

Tearing an organization down never strands money. `DELETE /integrator/organizations/{orgAddress}`
first deletes every draft of the managed organization exactly as `DELETE /processes/{processId}`
does — paid drafts credited back to their payer's wallet, open checkouts expired, owed add-on
releases applied — and stops at the first draft it cannot delete (a publish in flight, a
credit-back or Stripe call that failed), answering with that draft's error (its message names the draft) and removing nothing
else; a retry resumes. One difference: a teardown credits back **every once-per-organization
add-on price that no published or mined process of the organization received** to the wallet that
paid it. Before it drops anything it records, under the payer's wallet lock, which add-ons a
published process or a draft with elections mined carries (`db.RecordOrganizationAddOnsDelivered`,
kept on the organization so a retry still knows them); every decision of the sweep uses that set:

- a draft's add-on is withheld from its credit-back only when it is in the set; paid drafts,
  pending checkouts and drafts being published go in the same sweep and never get to use the
  stamp, so they do not count;
- once every draft is gone, each add-on an earlier credit-back withheld (a `creditBack` row with
  `withheldCents`/`withheldAddOns`, whose price stayed the organization's stamp) that is not in the
  set is credited back too, once per withholding row (key
  `teardownaddon:<org>:<addOn>:<process id>`), as a `creditBack` row naming the withholding
  process. The stamp goes first, in the one organization write that records that row's credit as
  owed (`addOnsOwedCredit`, `db.OweOrganizationAddOnCredit`; a stamp set after the withholding was
  paid by a process of its own and stays), so a teardown cut before the credit leaves no stamp a
  branded publish could ride for free. A retry credits every owed price whatever the organization
  received since, then settles the record; it credits nothing twice and finishes the rest. An
  add-on bought again after such a cut and withheld again is a second price with its own row, so
  the retry credits both.

While the teardown runs, the organization takes no new draft, publish or wallet debit (census
headroom included): each answers **409** (40901), so nothing is paid behind the draft sweep.

So what comes back depends neither on the order the drafts are deleted in nor on what a crashed
attempt already removed. Only then are the remaining (published) voting processes and their
questions deleted with the rest of the organization. The wallet ledger is kept as the audit
trail.

GDPR erasure (`cmd/userdel`) does the same for every organization it tears down: it deletes their
drafts through `api.DeleteOrgDrafts` before erasing anything (withholding add-ons the same
way), with no Stripe secret: an open top-up checkout is dropped with its draft and left to
Stripe's own expiry (paying it meanwhile only credits the kept wallet). `db.EraseUser` refuses, erasing nothing, while such an
organization still has drafts. Wallets and their history are kept; the command prints the
balance of each torn-down organization's own wallet, to be returned by hand, and a positive
balance never blocks the erasure. The command's wallet lock is its own, not the running
server's: a census headroom charge landing between a draft's refund read and its credit stays
debited for the deleted draft (logged as an error), so erasures run against no live traffic of
the organizations they tear down.

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
with a log line.

A refund already debited that Stripe later moves to `failed` or `canceled` is credited back:
the exact amount of its `striperefund:` row in the top-up's wallet, as a positive `refund` row
keyed `refundfail:<refund id>`, so the history shows the debit and its reversal as two `refund`
rows netting to zero (`creditBack` stays reserved for payments of processes). A failed refund
never debited changes nothing but a log line. Events can arrive out of order, so a `succeeded`
event re-reads the refund from Stripe and debits only while it is still `succeeded`: a `failed`
update handled first leaves nothing to debit later. A refund (or dispute) handled before its
top-up's `checkout.session.completed` — the endpoint was failing and Stripe redelivers in any
order — finds no `topup` row: the payment intent's top-up checkout session is then listed from
Stripe (`payment_intent` filter, `wallet_topup_org` metadata) and credited first under its
session key, before the debit. The `completed` event credits nothing when it lands (the key is
applied) and its process follow-up meets the debited wallet, so the two net to zero whatever the
order. A payment intent no top-up session made is still ignored.

A dispute (chargeback) on a top-up is mirrored the same way: `charge.dispute.funds_withdrawn`
debits the disputed amount net of VAT, found through the top-up's payment intent, as a `refund`
row keyed `dispute:<dispute id>:<n>`, even below zero; `charge.dispute.funds_reinstated` (a won
dispute) credits that debit back keyed `disputereinstated:<dispute id>:<n>`. `n` numbers the
movement among the dispute's balance transactions of the same direction, so funds withdrawn
again after a reinstatement (pre-arbitration) are debited again, and the n-th reinstatement
credits back the n-th withdrawal. Either event re-reads the dispute from Stripe and applies every
movement it lists now: a withdrawal already reinstated is credited back if it was debited and
never debited otherwise, so a reinstatement handled before the withdrawal it undoes (whose first
delivery failed) leaves nothing debited, and any delivery order or replay ends the same. Stripe's
dispute fee is not the payer's money and is not mirrored.

The webhook accepts bodies up to the public route group's limit (over 800 KB), not 64 KB: a
dispute's evidence can exceed that, and an event refused for its size is lost once Stripe stops
retrying it.

Every payment session copies its metadata onto the payment intent
(`payment_intent_data.metadata`), so a human in the dashboard sees which wallet a charge topped
up.

## Error codes

| Code | HTTP | Meaning |
|---|---|---|
| 40178 | 402 | the organization's wallet does not cover a publish (quote, `balanceCents`, `shortfallCents` in data) |
| 40178 | 402 | census growth beyond the paid price (`ProcessCensusGrowthQuote` in data) |
| 40175 | 402 | the integrator wallet does not cover a managed organization's debit (`requiredCents`/`availableCents` in data) |
| 40176 | 422 | census above 50 000 — custom quote only, refused on every payment path |
| 40177 | 409 | a top-up checkout is open (publish), the draft is already paid (`/checkout`), a checkout is completing, the paid amount moved since a headroom price was computed, a delete refunded the draft, or a checkout cancel whose top-up was already spent on the process's publish (publish again), or an add-on the draft's payment does not include and the organization has not paid (publish, or an edit that turns it on) |
| 40903 | 409 | the process holds its publish claim — a publish in progress, or briefly a checkout, a checkout cancel or an eligibility update of the draft: a second publish, a checkout, cancel, edit, delete or eligibility update of that draft, or a member create, import or group update that would grow its census. Retry once the claim is released: when the publish job ends, or up to 15 minutes after a service restart (`PublishStaleAfter`). Also: an election a failed publish submitted is not settled on chain yet — edit, delete and teardown refuse the draft until it is (publish the draft again, at no new charge, or retry later) |
| 40901 | 409 | the organization is being deleted (a managed-organization teardown is running): `POST /processes`, publish and `census/checkout` refuse |
| 40183 | 402 | a managed organization's spend cap would be passed (`capCents`, `spentCents`, `requiredCents` in data) |
| 40184 | 402 | an integrator that never kept a top-up already manages its one organization (`managedOrgs`, `managedOrgsCreated`, `netTopUpCents` in data) |
| 40181 | 403 | a managed organization has no wallet (wallet routes, `/checkout`) |
| 40174 | 403 | an integrator's own organization cannot own processes |
| 40182 | 400 | the organization already has 5 admins/managers (pending invitations included) |

## Storage summary (migration 0035)

- `processPayments` — one document per process (`_id` = process id): the top-up checkout's
  state (`status: pending`, `checkoutSessionId`, `consumed`, `topUpCents`, `requestedBy`, the
  price it was opened for in `amountCents`, and `branding`/`customUrl`: the add-ons it priced
  in), the display of what the process paid (`status` paid/failed/refunded and `amountCents`),
  `baseCents` (written once at publish: what it paid outside the wallet), `publishJobId` and the
  webhook's `topUpRefusal`. No money is read off it, and no card state: no payment intent, charge
  amounts, refund id or quote hash.
- `wallets` — one document per payer (`_id` = organization address): the balance, the bounded
  `appliedKeys` window and `unposted` (movements whose ledger row is not written yet). An
  integrator's per-managed-org spend caps and spending live in the optional `orgSpend` map
  (absent means unlimited, 0).
- `walletLedger` — append-only and the source of truth, unique index on (`orgAddress`,
  `idempotencyKey`), a sparse index on `paymentIntentId` (a top-up's, which a Stripe refund
  names) and a partial index on (`orgAddress`, `processId`) for the per-process fold; rows carry
  `kind`, the signed `amountCents`, `processId`/`processOrgAddress`, a top-up's `grossCents`, a
  payment's `priceCents`, `attempt` and `addOns`, and a credit-back's `withheldCents`,
  `withheldAddOns` and `releaseAddOns`.
- `votingProcesses` gains `addOns`; `organizations` gains `brandingPaidAt`,
  `brandingClaimedBy` and `brandingClaimedAt`, and `customUrlPaidAt`, `customUrlClaimedBy` and
  `customUrlClaimedAt` (the once-per-organization custom URL, no migration: absent means unpaid).

## Legacy subscriptions (migration 0036)

The plan and subscription model is gone. Removed routes:

- `GET /plans` (removed)
- `POST /subscriptions/checkout` (removed)
- `GET /subscriptions/{orgAddress}/portal` (removed)
- `GET /organizations/{orgAddress}/subscription` (removed)

The `subscription` and `counters` fields were removed from the organization responses; `GET /integrator/organizations` keeps `counters` (`sentSMS`, `sentEmails`, `sentVotes` and the live number of published `processes`) on each managed organization. The Stripe webhook moved to `POST /payments/webhook` and the top-up session read to
`GET /wallet/checkout/{sessionId}`; the webhook acknowledges and ignores subscription, invoice and
product events. The team updates the webhook endpoint URL in Stripe after the deploy.

Migration 0036, per organization holding legacy billing fields:

- archives into `legacySubscriptions` (keyed by the organization address) the `subscription`,
  `counters`, `integratorLimits`, `tokensPurchased`, `tokensRemaining` and `managedBy` fields as
  they were, the `plan` document the organization was subscribed to, and `archivedAt`. The write
  uses `$setOnInsert`, so a re-run keeps the first run's archive;
- then unsets `subscription`, `integratorLimits`, `tokensPurchased`, `tokensRemaining` and the
  `users`, `processes`, `subOrgs`, `managedOrgs` and `managedProcesses` counters. `counters.sentSMS`,
  `sentEmails` and `sentVotes` stay: `GET /integrator` sums them and `GET /integrator/organizations`
  (and the `PUT` response) returns them per managed organization, with the live `processes` count;
- after the loop, drops `plans`, and drops the unused `subscriptions` collection when it is empty
  (when it holds documents it logs a warning with the count and leaves it).

It grants nothing: the team credits wallets and cancels the Stripe subscriptions by hand.

The archived `subscription` is copied without its billing `email`, so the archive holds no
personal data.

## Testing the flow

Three layers, from hermetic to manual:

1. **Signed-webhook e2e (CI, no network, no cards)** —
   `api/stripe_checkout_webhook_test.go` and `api/stripe_refund_webhook_test.go` sign event
   payloads with the real Stripe v1 signature scheme (`stripe-go/webhook.GenerateTestSignedPayload`)
   and POST them to `/payments/webhook`, driving signature validation, event parsing, the
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
   `stripe listen --forward-to localhost:8080/payments/webhook`,
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
- VAT treatment of wallet top-ups (single- vs multi-purpose voucher) needs accountant
  sign-off before launch; `automatic_tax` is on for top-up sessions.
- The spec's "publication retries keep the debit, debits are never silently reversed" is
  superseded: a publish that fails for good before mining anything is credited back, with a
  ledger row. The HackMD spec needs the same update.
- An open top-up checkout of an organization erased by `cmd/userdel` stays payable until Stripe
  expires it, and paying it credits the kept wallet.
- Legacy subscriptions are not converted by code: migration 0036 archives them and the team
  credits wallets by hand (see "Legacy subscriptions").
