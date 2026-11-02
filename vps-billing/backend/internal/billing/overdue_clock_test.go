package billing

import (
	"encoding/json"
	"testing"
	"time"
)

func TestOverdueDecisionSurvivesSchedulerClockAndRenewalRace(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	due := now.AddDate(0, 1, 0)
	raw, err := json.Marshal(serviceActionPayload06{Version: 1, Source: "automation", Reason: "overdue", BillingDueAt: &due})
	if err != nil {
		t.Fatal(err)
	}
	var persisted serviceActionPayload06
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	for _, workerClock := range []time.Time{now, due.AddDate(0, 0, 5), due.AddDate(0, 3, 0)} {
		if overdueRenewed06(due, persisted, workerClock) {
			t.Fatal("unchanged period was treated as paid because worker clock differed")
		}
		if !overdueRenewed06(due.AddDate(0, 1, 0), persisted, workerClock) {
			t.Fatal("renewal racing dispatch or completion did not supersede suspension")
		}
	}
	if !overdueRenewed06(due, serviceActionPayload06{}, now) || overdueRenewed06(now, serviceActionPayload06{}, due) {
		t.Fatal("legacy task recovery changed")
	}
}
