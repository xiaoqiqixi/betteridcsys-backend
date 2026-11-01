package commerce

import (
	api "aster/billing/internal/financeapi"
	"testing"
)

func TestAutomationSettingsBoundaries(t *testing.T) {
	valid := api.AutomationSettings{InvoiceDaysBeforeDue: 7, ReminderDaysBeforeDue: []int64{1, 3, 7}, OverdueReminderDays: []int64{1, 3}, SuspendAfterDays: 3, TerminateAfterDays: 30, CancelUnpaidOrdersAfterDays: 3, AffiliateHoldDays: 30, TicketAutocloseHours: 72, Revision: 1}
	for _, tc := range []struct {
		name   string
		change func(*api.AutomationSettings)
		valid  bool
	}{
		{"valid", func(v *api.AutomationSettings) {}, true},
		{"same suspension and termination", func(v *api.AutomationSettings) { v.TerminateAfterDays = 3 }, false},
		{"reversed suspension and termination", func(v *api.AutomationSettings) { v.TerminateAfterDays = 2 }, false},
		{"repeated reminder", func(v *api.AutomationSettings) { v.ReminderDaysBeforeDue = []int64{1, 1} }, false},
		{"unsorted overdue reminders", func(v *api.AutomationSettings) { v.OverdueReminderDays = []int64{3, 1} }, false},
		{"no reminders", func(v *api.AutomationSettings) {
			v.ReminderDaysBeforeDue = []int64{}
			v.OverdueReminderDays = []int64{}
		}, true},
		{"zero day suspension", func(v *api.AutomationSettings) { v.SuspendAfterDays = 0 }, true},
		{"revision overflow", func(v *api.AutomationSettings) { v.Revision = MaxMinor }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			tc.change(&v)
			if err := ValidateAutomation(v); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
}
