#!/usr/bin/env bash
# End-to-end smoke test of the pay-per-process flow against the local dev stack.
#
# Runs every step a user would (register -> org -> draft -> quote -> gate ->
# checkout -> pay -> publish) and prints each with a PASS/FAIL line. The payment
# step is performed by signing a checkout.session.completed webhook with the same
# HMAC scheme Stripe uses (VOCDONI_STRIPEWEBHOOKSECRET from .env) — so it needs no
# browser and no Stripe account, yet drives the real backend path: checkout
# creation, signature verification, fulfillment CAS, and server-side publication.
# The only thing it does NOT touch is Stripe's own card UI (validated manually).
#
# Usage:  bash docs/tmp/test-payg-flow.sh        # from the repo root, stack up
set -uo pipefail

API=${API:-http://localhost:8080}
ENVFILE=${ENVFILE:-.env}
PASS=0; FAIL=0
g(){ printf '\033[32m%s\033[0m' "$1"; }; r(){ printf '\033[31m%s\033[0m' "$1"; }
b(){ printf '\033[1m%s\033[0m' "$1"; }
step(){ printf '\n\033[1m== %s ==\033[0m\n' "$1"; }
ok(){ printf '  [%s] %s\n' "$(g PASS)" "$1"; PASS=$((PASS+1)); }
no(){ printf '  [%s] %s\n' "$(r FAIL)" "$1"; FAIL=$((FAIL+1)); }
die(){ printf '\n%s %s\n' "$(r 'ABORT:')" "$1"; exit 1; }

for dep in curl jq openssl docker; do command -v $dep >/dev/null || die "missing dependency: $dep"; done
[ -f "$ENVFILE" ] || die "no $ENVFILE (run from the repo root)"
SECRET=$(grep -E '^VOCDONI_STRIPEWEBHOOKSECRET=' "$ENVFILE" | cut -d= -f2-)
[ -n "${SECRET:-}" ] && [ "$SECRET" != "whsec_your_stripe_webhook_secret" ] \
  || die "set a real VOCDONI_STRIPEWEBHOOKSECRET in $ENVFILE (any whsec_… value; it only has to match this script)"

step "0. stack reachable"
curl -sf "$API/ping" >/dev/null && ok "GET /ping" || die "API not reachable at $API — is the stack up?"

step "1. register + verify + login"
EMAIL="payg$(date +%s)@example.com"; PWORD="password1234"
printf '  user: %s\n' "$(b "$EMAIL")"
SINCE=$(date +%s)
curl -sf "$API/users" -d "{\"email\":\"$EMAIL\",\"password\":\"$PWORD\",\"firstName\":\"Payg\",\"lastName\":\"Test\"}" >/dev/null \
  && ok "POST /users (register)" || no "register"
sleep 2
CODE=$(docker compose logs --since "${SINCE}s" fakesmtp 2>/dev/null | grep -o 'code=[A-Za-z0-9]*' | tail -1 | cut -d= -f2)
[ -n "${CODE:-}" ] && ok "verification code from fakesmtp: $CODE" || die "no verification code in fakesmtp logs (is the local-smtp profile up and SMTP creds blank?)"
curl -sf "$API/users/verify" -d "{\"email\":\"$EMAIL\",\"code\":\"$CODE\"}" >/dev/null \
  && ok "POST /users/verify" || no "verify"
TOKEN=$(curl -sf "$API/auth/login" -d "{\"email\":\"$EMAIL\",\"password\":\"$PWORD\"}" | jq -r .token)
[ -n "${TOKEN:-}" ] && [ "$TOKEN" != null ] && ok "POST /auth/login (JWT acquired)" || die "login failed"
AUTH="Authorization: Bearer $TOKEN"

step "2. organization + members + draft"
ORG=$(curl -sf "$API/organizations" -H "$AUTH" \
  -d '{"type":"company","website":"https://payg-'"$(date +%s)"'.example","provisionAccount":true}' | jq -r .address)
[ -n "${ORG:-}" ] && [ "$ORG" != null ] && ok "POST /organizations (provisioned): $ORG" || die "org creation failed"
curl -sf "$API/organizations/$ORG/members" -H "$AUTH" -d '{"members":[
  {"memberNumber":"P001","name":"Alice","surname":"One","email":"alice@example.com"},
  {"memberNumber":"P002","name":"Bob","surname":"Two","email":"bob@example.com"}]}' >/dev/null \
  && ok "POST /organizations/{org}/members (2 members)" || no "add members"
MIDS=$(curl -sf "$API/organizations/$ORG/members?limit=10" -H "$AUTH" | jq -c '[.members[].id]')
[ "$(echo "$MIDS" | jq 'length')" = 2 ] && ok "GET members -> 2 ids" || no "member list"
START=$(date -u -v+1H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+1 hour' +%Y-%m-%dT%H:%M:%SZ)
END=$(date -u -v+48H +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d '+48 hours' +%Y-%m-%dT%H:%M:%SZ)
PID=$(curl -sf "$API/processes" -H "$AUTH" -d '{
  "orgAddress":"'"${ORG#0x}"'",
  "census":{"twoFaFields":["email"],"memberIds":'"$MIDS"'},
  "title":{"default":"Automated payment test"},
  "startDate":"'"$START"'","endDate":"'"$END"'",
  "addOns":{"signedCertificate":true},
  "questions":[{"title":{"default":"Approve?"},"type":"singlechoice",
    "typeSetup":{"minChoices":1,"maxChoices":1},
    "choices":[{"title":{"default":"Yes"},"value":0},{"title":{"default":"No"},"value":1}]}]
}' | jq -r .processId)
[ -n "${PID:-}" ] && [ "$PID" != null ] && ok "POST /processes (draft): $PID" || die "draft creation failed"

step "3. quote"
PUB=$(curl -sf "$API/pricing?voters=2&signedCertificate=true" | jq -r .totalCents)
[ "$PUB" = 4900 ] && ok "GET /pricing -> 4900 cents (public calculator)" || no "public pricing (got $PUB)"
DQ=$(curl -sf "$API/processes/$PID/price" -H "$AUTH" | jq -r .totalCents)
[ "$DQ" = 4900 ] && ok "GET /processes/{id}/price -> 4900 cents" || no "draft quote (got $DQ)"

step "4. publication is gated while unpaid"
GATE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/processes/$PID/publish" -H "$AUTH")
[ "$GATE" = 402 ] && ok "POST /publish -> 402 Payment Required" || no "expected 402, got $GATE"

step "5. open checkout"
CO=$(curl -sf -X POST "$API/processes/$PID/checkout" -H "$AUTH" -d '{"returnURL":"http://localhost/none"}')
SESSION=$(echo "$CO" | jq -r .sessionId); AMT=$(echo "$CO" | jq -r .amountCents)
[ -n "${SESSION:-}" ] && [ "$SESSION" != null ] && ok "POST /checkout -> session $SESSION ($AMT cents)" || die "checkout failed: $CO"

step "6. pay (self-signed checkout.session.completed webhook)"
PAYLOAD=$(jq -cn --arg id "evt_test_$(date +%s)" --arg sess "$SESSION" --arg pid "$PID" --arg email "$EMAIL" '{
  id:$id, object:"event", api_version:"2026-08-26.dahlia", type:"checkout.session.completed",
  data:{object:{id:$sess, object:"checkout.session", mode:"payment", status:"complete",
    payment_status:"paid", amount_subtotal:4900, amount_total:4900,
    metadata:{voting_process_id:$pid, requested_by:$email}}}}')
TS=$(date +%s)
SIG=$(printf '%s' "$TS.$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" | sed 's/^.* //')
WCODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$API/subscriptions/webhook" \
  -H "Content-Type: application/json" -H "Stripe-Signature: t=$TS,v1=$SIG" --data-binary "$PAYLOAD")
[ "$WCODE" = 200 ] && ok "POST /subscriptions/webhook (signed) -> 200" || die "webhook rejected ($WCODE) — check the secret matches .env"

step "7. observe paid + published"
PSTATUS=$(curl -sf "$API/processes/$PID/checkout" -H "$AUTH" | jq -r .status)
[ "$PSTATUS" = paid ] && ok "GET /checkout -> status: paid" || no "payment status: $PSTATUS"
printf '  waiting for on-chain publication'
PUBLISHED=false
for _ in $(seq 1 30); do
  if [ "$(curl -sf "$API/processes/$PID" -H "$AUTH" | jq -r .published)" = true ]; then PUBLISHED=true; break; fi
  printf '.'; sleep 2
done; printf '\n'
if $PUBLISHED; then
  ok "GET /processes/{id} -> published: true"
  QST=$(curl -sf "$API/processes/$PID" -H "$AUTH" | jq -c '[.questions[].status]')
  ok "question status: $QST"
else no "process did not publish within 60s"; fi

step "summary"
printf '  %s passed, %s\n' "$(g "$PASS")" "$( [ "$FAIL" -eq 0 ] && g '0 failed' || r "$FAIL failed" )"
printf '  process: %s\n  org:     %s\n  user:    %s\n' "$PID" "$ORG" "$EMAIL"
[ "$FAIL" -eq 0 ] && printf '\n%s the whole pay-per-process flow works end to end.\n' "$(g '✔')" || exit 1
