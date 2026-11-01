package commerce

import (
	api "aster/billing/internal/financeapi"
	"encoding/json"
)

func ValidateAutomation(v api.AutomationSettings) error {
	raw, err := json.Marshal(v)
	if err != nil || api.ValidateInput("adminUpdateAutomation", raw) != nil || v.SuspendAfterDays >= v.TerminateAfterDays || v.Revision >= MaxMinor {
		return ErrInput
	}
	for _, days := range [][]int64{v.ReminderDaysBeforeDue, v.OverdueReminderDays} {
		for i := 1; i < len(days); i++ {
			if days[i] <= days[i-1] {
				return ErrInput
			}
		}
	}
	return nil
}
