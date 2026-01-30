package db

import "time"

func ComputeAnnualPeriod(sub OrganizationSubscription, billingPeriod BillingPeriod, now time.Time,
) (start time.Time, end time.Time, ok bool) {
	if billingPeriod == BillingPeriodAnnual {
		if sub.StartDate.IsZero() || sub.RenewalDate.IsZero() {
			return time.Time{}, time.Time{}, false
		}
		return sub.StartDate, sub.RenewalDate, true
	}

	if billingPeriod == BillingPeriodMonthly {
		if sub.StartDate.IsZero() {
			return time.Time{}, time.Time{}, false
		}
		start := sub.StartDate
		end := start.AddDate(1, 0, 0)
		for !now.Before(end) {
			start = start.AddDate(1, 0, 0)
			end = end.AddDate(1, 0, 0)
		}
		return start, end, true
	}

	return time.Time{}, time.Time{}, false
}
