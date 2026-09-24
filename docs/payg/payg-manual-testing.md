# Manually testing the pay-per-process flow

How to walk the whole flow — draft → quote → checkout → test card → webhook →
server-side publication — as a real user, against the **local dev stack** and **Stripe's
test environment**, without touching vocdoni-app. Stripe test mode is a full sandbox:
real sessions, real signed webhooks, standard test cards, no real money.

The only piece Stripe does not host for us is the payment page: our sessions use
`ui_mode: "custom"` (embedded, as the app will render them), so the test card has to be
entered into a Payment Element mounted by Stripe.js. Step 6 embeds a single-file page
for that — copy it anywhere local (e.g. `/tmp/checkout.html`); the backend's CORS is
open, so it talks to the API directly.

**Two ways to test:**

- **Automated smoke test** ([§0](#0-fast-path-automated-smoke-test)) — one script runs
  the whole backend flow (register → draft → quote → gate → checkout → pay → publish)
  and prints a PASS/FAIL line per step. It signs the payment webhook itself, so it needs
  **no browser and no Stripe account** — ideal for a quick "is the flow healthy" check.
- **Manual walkthrough** ([§1](#1-prerequisites-one-time-free) onward) — the same flow by
  hand, paying with a real Stripe **test card** in the browser. Use this to exercise the
  actual Payment Element and Stripe Tax UI, the one thing the script cannot.

## 0. Fast path: automated smoke test

The repo ships [`test-payg-flow.sh`](test-payg-flow.sh) beside this doc. It does
everything §3–§8 do, end to end, with clear output — and performs the payment by signing
a `checkout.session.completed` webhook with the same HMAC scheme Stripe uses (the secret
from your `.env`), so it drives the real backend path (checkout creation, signature
verification, fulfillment, on-chain publication) without a card or a Stripe account.

Prerequisites: only the stack ([§2](#2-configure-and-start-the-stack)) running, with a
**non-placeholder** `VOCDONI_STRIPEWEBHOOKSECRET` in `.env` — for the script that value
is arbitrary (any `whsec_…` string), it only has to be the same one the script reads. No
`stripe listen` needed.

```bash
# from the repo root, stack up
bash docs/payg/test-payg-flow.sh
```

Each run provisions a fresh user/org/draft (unique email, so it never collides) and ends
with a summary:

```
== summary ==
  17 passed, 0 failed
  process: 6aab…f4d
  org:     0x794f…50bd
  user:    payg1789646632@example.com

✔ the whole pay-per-process flow works end to end.
```

A non-zero exit and a red `FAIL` line point at the exact step that broke. Override the
target with `API=… ENVFILE=… bash docs/payg/test-payg-flow.sh` if your stack is not on
`localhost:8080` / `./.env`.

For the real card UI, continue with the manual walkthrough below.

## 1. Prerequisites (one-time, free)

- A [Stripe account](https://dashboard.stripe.com) in **test mode**: grab the test keys
  (`sk_test_…` secret, `pk_test_…` publishable).
- **Configure the test-mode tax origin address** (Dashboard → Settings → Tax): our
  sessions enable `automatic_tax`, and Stripe refuses to create them until the test
  account has an origin address.
- The [Stripe CLI](https://docs.stripe.com/stripe-cli) (`brew install stripe/stripe-cli/stripe`,
  then `stripe login`).

## 2. Configure and start the stack

```bash
cp example.env .env
```

Edit `.env`:

```bash
VOCDONI_STRIPEAPISECRET=sk_test_…            # your test secret key
VOCDONI_STRIPEWEBHOOKSECRET=whsec_…          # printed by `stripe listen` below
VOCDONI_VOCDONIAPI=http://vocone:9090/v2     # the local voconed chain
VOCDONI_SMTPSERVER=localhost                 # the fake SMTP capture server
VOCDONI_SMTPPORT=1025
VOCDONI_SMTPUSERNAME=                        # MUST be blank: credentials make the API
VOCDONI_SMTPPASSWORD=                        # attempt AUTH, which the fake server lacks
```

`VOCDONI_PRIVATEKEY` must be a real hex key (the example placeholder is not one and the
API refuses to start): `VOCDONI_PRIVATEKEY=$(openssl rand -hex 32)` — the `fundaccount`
service funds it on the local chain.

```bash
# local chain + faucet funding + fake SMTP + API + Mongo (+ mongo-express on :8081)
docker compose --profile with-vocone --profile local-smtp up -d
# seed the default plan (organization creation requires one)
docker compose --profile with-ui run --rm defaultplan
# forward real signed webhooks from Stripe to the local API — keep this running;
# it prints the whsec_… that must be in .env (restart the api after setting it)
stripe listen --forward-to localhost:8080/subscriptions/webhook
```

The pinned `stripe-go` (v86, API version `2026-08-26.dahlia`) matches the current Stripe
account version, so events validate with no extra flags. If Stripe advances your
account's API version past the SDK's release train, bump `stripe-go` to the matching
major — the webhook check compares the release train, so staying on the same train is
enough.

## 3. User, organization, members, draft (curl)

```bash
API=http://localhost:8080

# register (any email works — mail lands in the fake SMTP capture)
curl -s $API/users -d '{"email":"tester@example.com","password":"password1234","firstName":"Pay","lastName":"Tester"}'
# the verification mail lands in the fake SMTP capture; the code travels as code=… :
docker compose logs fakesmtp | grep -o 'code=[A-Za-z0-9]*' | tail -1
curl -s $API/users/verify -d '{"email":"tester@example.com","code":"<CODE>"}'
TOKEN=$(curl -s $API/auth/login -d '{"email":"tester@example.com","password":"password1234"}' | jq -r .token)
AUTH="Authorization: Bearer $TOKEN"

# organization with its on-chain account provisioned eagerly (publish needs it)
ORG=$(curl -s $API/organizations -H "$AUTH" \
  -d '{"type":"company","website":"https://payg-test.example","provisionAccount":true}' | jq -r .address)

# two members — with the certificate add-on that is already a priced (€49) process,
# no need to import 11+ voters to get past the free tier
curl -s $API/organizations/$ORG/members -H "$AUTH" -d '{"members":[
  {"memberNumber":"P001","name":"Alice","surname":"One","email":"alice@example.com"},
  {"memberNumber":"P002","name":"Bob","surname":"Two","email":"bob@example.com"}]}'
MIDS=$(curl -s "$API/organizations/$ORG/members?limit=10" -H "$AUTH" | jq '[.members[].id]')

# draft: email-2FA census over both members, one yes/no question, certificate add-on
PID=$(curl -s $API/processes -H "$AUTH" -d '{
  "orgAddress":"'${ORG#0x}'",
  "census":{"twoFaFields":["email"],"memberIds":'$MIDS'},
  "title":{"default":"Manual payment test"},
  "startDate":"'$(date -u -v+1H +%Y-%m-%dT%H:%M:%SZ)'",
  "endDate":"'$(date -u -v+48H +%Y-%m-%dT%H:%M:%SZ)'",
  "addOns":{"signedCertificate":true},
  "questions":[{"title":{"default":"Approve?"},"type":"singlechoice",
    "typeSetup":{"minChoices":1,"maxChoices":1},
    "choices":[{"title":{"default":"Yes"},"value":0},{"title":{"default":"No"},"value":1}]}]
}' | jq -r .processId)
```

(GNU date: use `date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ`.)

## 4. Quote and gate

```bash
curl -s "$API/pricing?voters=2&signedCertificate=true" | jq          # public calculator: 4900
curl -s $API/processes/$PID/price -H "$AUTH" | jq                    # the draft's quote: 4900
curl -s -X POST $API/processes/$PID/publish -H "$AUTH" | jq          # 402: publication requires payment
```

## 5. Open the checkout

```bash
curl -s -X POST $API/processes/$PID/checkout -H "$AUTH" \
  -d '{"returnURL":"http://localhost:8000/checkout.html"}' | jq
# → { clientSecret, sessionId, amountCents: 4900, currency: "eur" }
```

Repeating the call returns the **same session** while the price is unchanged; editing
the draft first (e.g. toggle an add-on) makes the next call expire it and open a
replacement — watch it happen under Dashboard → Payments → Checkout sessions.

## 6. Pay with a Stripe test card

Save this as `/tmp/checkout.html`, serve it (`python3 -m http.server 8000 -d /tmp`),
open `http://localhost:8000/checkout.html`, paste your `pk_test_…` key and the
`clientSecret` from step 5:

```html
<!doctype html>
<meta charset="utf-8"><title>PAYG checkout harness</title>
<!-- initCheckout needs the Basil release; the legacy /v3/ pin throws IntegrationError -->
<script src="https://js.stripe.com/basil/stripe.js"></script>
<style>body{font:14px sans-serif;max-width:480px;margin:2em auto}input,button{width:100%;margin:.3em 0;padding:.5em}</style>
<body>
  <h3>Pay-per-process checkout harness</h3>
  <input id="pk" placeholder="pk_test_…">
  <input id="cs" placeholder="clientSecret from POST /processes/{id}/checkout">
  <button onclick="mount()">Mount payment form</button>
  <div id="billing-element"></div>
  <div id="payment-element"></div>
  <button id="pay" style="display:none" onclick="pay()">Pay</button>
  <pre id="out"></pre>
<script>
let checkout;
async function mount() {
  const stripe = Stripe(document.getElementById('pk').value.trim());
  checkout = await stripe.initCheckout({
    fetchClientSecret: async () => document.getElementById('cs').value.trim(),
  });
  checkout.createBillingAddressElement().mount('#billing-element');
  checkout.createPaymentElement().mount('#payment-element');
  document.getElementById('pay').style.display = 'block';
}
async function pay() {
  // no updateEmail: the backend already sets the payer's email on the session
  // (CustomerEmail from the JWT user, or the organization's reused Stripe customer)
  const result = await checkout.confirm(); // redirects to the returnURL on success
  document.getElementById('out').textContent = JSON.stringify(result, null, 2);
}
</script>
</body>
```

(The page uses Stripe.js's custom-checkout API, the same one the app's embedded flow
uses; if Stripe evolves it, https://docs.stripe.com/checkout/custom is the reference.)

Test data (any **future expiry**, any CVC, any postcode — full list:
https://docs.stripe.com/testing):

| Input | Behavior |
|---|---|
| `4242 4242 4242 4242` | immediate success |
| `4000 0025 0000 3155` | 3DS challenge, then success |
| `4000 0000 0000 9995` | declined (insufficient funds) — the session stays open, retry in the form |
| SEPA IBAN `DE89370400440532013000` | delayed method: `processing`, succeeds after a few seconds |
| SEPA IBAN `DE62370400440532013001` | delayed method: fails → payment `failed`, checkout reopens |

## 7. Watch the backend finish without you

The browser's part ended at `confirm()` — close it if you like. In the `stripe listen`
terminal the `checkout.session.completed` event flows through; then:

```bash
curl -s $API/processes/$PID/checkout -H "$AUTH" | jq   # status: paid
curl -s $API/processes/$PID -H "$AUTH" | jq .published # true (publication ran as you,
                                                       #  triggered by the webhook)
```

Payment truth only ever comes from the webhook — the redirect back to `returnURL` is
never what marks anything paid.

## 8. More paths worth exercising

- **Replay**: `stripe events resend <evt_…>` (id from `stripe listen`) → 200, no second
  publication, wallet not re-credited.
- **Failed then retry**: after the failing SEPA IBAN, `GET …/checkout` shows `failed`;
  `POST …/checkout` opens a fresh session and the card flow works again.
- **Paid draft is editable**: once paid, `PUT /processes/{id}` still works — the publish
  gate re-prices whatever the edit produced, so an edit can only repair the draft. What is
  refused (409) is editing or deleting while a payment is `processing`, or deleting one
  whose Stripe session you just completed but whose webhook has not landed.
- **Census headroom**: after publishing, `PUT /processes/{id}/census` with a few extra
  members goes through (€5 rounding absorbs it); add enough to cross the next €5 step and
  it answers 402 with `{totalCents, paidCents, dueCents, censusSize}`. Buy the difference
  with `POST /processes/{id}/census/checkout -d '{"censusSize":<target>,"returnURL":"http://localhost:8000/checkout.html"}'`,
  pay it with a test card, and the same `PUT` then lands. For a managed organization the
  same call debits the integrator wallet instead and takes effect immediately.
- **Refund on delete**: `DELETE /processes/{id}` on a paid *draft* refunds it first — the
  Stripe dashboard shows the refund with VAT included, the payment row survives as
  `refunded`, and a branded organization can be charged for branding again afterwards. A
  wallet-paid draft credits the integrator wallet instead, visible as a `refund` row in
  `GET /wallet`. Note that a paid process normally publishes itself straight away, so a
  paid draft only exists when that automatic publication failed preflight (the org hitting
  its plan's process limit between paying and publishing is the realistic way in). The
  assertions live in `TestDeletePaidDraftRefunds` and
  `TestDeleteWalletPaidDraftCreditsIntegratorWallet`; the dashboard is what only a browser
  can show you.
- **Integrator wallet**: make your org an integrator
  (`go run ./cmd/cli --setIntegrator --orgAddress $ORG --maxManagedOrgs 5 --mongoURL mongodb://root:vocdoni@localhost:27017/saasdb`),
  then `POST /wallet/topup -d '{"amountCents":10000,"returnURL":"http://localhost:8000/checkout.html"}'`,
  pay it with a test card, and `GET /wallet` shows the credited balance and ledger; a
  managed organization's process then publishes by debiting it (no checkout involved).

## Notes

- Everything here is local: staging/production get these endpoints once the pay-per-process PRs deploy.
  Keep the deployed `stripe-go` on the same API-version release train as the Stripe
  account (currently `dahlia`); bump the SDK major when Stripe advances the account to a
  new train, or webhook signature validation will start rejecting events.
- The automated suite already covers the webhook contract with signed payloads
  (`api/stripe_checkout_webhook_test.go`); this runbook is for what only a browser can
  exercise — the Payment Element, Stripe Tax collecting the billing address, and the
  dashboard's view of sessions being reused, expired and replaced.
