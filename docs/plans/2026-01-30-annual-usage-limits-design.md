# Annual Usage Limits (Processes, SMS, Emails)

Date: 2026-01-30

## Goal
Implement annual (12‑month, anniversary‑based) usage limits for processes, sent SMS, and sent emails while preserving lifetime counters. Annual usage should reset on each anniversary period, regardless of monthly billing periods. Enforcement remains at existing points:
- Processes: at transaction permission check (process creation).
- SMS/Email: at group census publish.

## Non‑Goals (for now)
- Per‑send enforcement for SMS/email.
- Users/Suborgs annual limits.
- Changing lifetime counters or their increments.

## Summary of Approach
Keep lifetime counters in `Organization.Counters` unchanged. Add a new collection to store **period snapshots** (baselines) per organization and per annual period start. Period usage is computed at read time as `lifetime - baseline`. Snapshots are immutable and created idempotently when a new period is detected.

## Data Model
### New collection: `org_usage_snapshots`
Document fields:
- `orgAddress` (address)
- `periodStart` (time)
- `periodEnd` (time)
- `billingPeriod` (string: `year` or `month`)
- `baseline` (object)
  - `processes` (int)
  - `sentSMS` (int)
  - `sentEmails` (int)
- `createdAt` (time)
- `updatedAt` (time)

Indexes:
- Unique index on `(orgAddress, periodStart)` for idempotent upserts.
- Optional index on `(orgAddress, periodStart)` for querying history.

## Period Calculation
### Annual plans
Use Stripe `current_period_start/end` as the annual period boundaries.

### Monthly plans
Use **subscription start** as the annual anchor. Compute anniversary‑based periods:
- `periodStart = subscription.StartDate`
- `periodEnd = periodStart + 1 year`
- If `now >= periodEnd`, advance by whole‑year increments until `now < periodEnd`.

This allows a single annual usage window even on monthly billing.

## Snapshot Creation
### When
- On Stripe `customer.subscription.created` and `customer.subscription.updated` for `active` subscriptions.
- For monthly plans, use `subscription.StartDate` to compute the annual period.

### How
- Compute `periodStart`/`periodEnd` for the applicable annual window.
- Upsert snapshot by `(orgAddress, periodStart)` if it does not exist.
- Baseline values are taken from **current lifetime counters** at creation time.
- Once created, the snapshot is immutable (do not update baseline).

### Idempotency
Repeated webhook events for the same period should result in no changes.

## Usage Computation
Period usage is computed as:
- `periodProcesses = lifetime.Processes - baseline.Processes`
- `periodSentSMS = lifetime.SentSMS - baseline.SentSMS`
- `periodSentEmails = lifetime.SentEmails - baseline.SentEmails`

If snapshot is missing for the computed period, create it lazily and treat current period usage as 0 at that moment.

## Enforcement
Enforcement remains at existing points, but use **period usage** when available:
- `subscriptions.HasTxPermission`: compare `periodProcesses` to plan `MaxProcesses`.
- `subscriptions.OrgCanPublishGroupCensus`: compare `periodSentEmails/SMS` to plan `MaxSentEmails/MaxSentSMS`.

For non‑annual plans or missing snapshot, fall back to lifetime counters.

## API/Reporting
Expose both lifetime counters and computed period usage. History is obtained by listing snapshots for an org.

## Error Handling
- If billing period or subscription data is missing, skip snapshot creation and fall back to lifetime logic.
- If snapshot upsert fails due to duplicate key, re‑fetch and proceed.

## Testing
- Snapshot upsert is idempotent and uses correct baselines.
- Annual period rollovers create new snapshots.
- Monthly plan uses subscription start for annual periods.
- Enforcement uses period usage and blocks when limits are exceeded.
- Lifetime behavior unchanged for non‑annual plans.
