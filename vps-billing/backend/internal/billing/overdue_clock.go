package billing

import "time"

// A queued suspension concerns the billing period observed by its scheduler.
// Compare periods, not a different worker's wall clock: a renewal may settle
// before dispatch or while the remote operation is running.
func overdueRenewed06(current time.Time, payload serviceActionPayload06, now time.Time) bool {
	if payload.BillingDueAt != nil {
		return current.After(*payload.BillingDueAt)
	}
	return current.After(now) // Preserve recovery of tasks queued by older cores.
}
