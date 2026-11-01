package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestMySQLCommerceRenewalContractConcurrencyAndMonthEnd(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	settings, err := m.AutomationSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoApplyCredit = false
	if settings, err = m.UpdateAutomation(t.Context(), f.admin, settings); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)
	if _, err = m.DB.Exec("UPDATE services SET status='active',next_due_at=?,next_invoice_at=?,anchor_day=31 WHERE id=?", due, due, sid); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	status, body := admin.request("POST", "/api/v1/services/"+sid+"/renew", map[string]string{"cycle": "monthly"}, "renew-other-owner")
	if status != 404 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/renew", map[string]string{"cycle": "monthly"}, "renew-month-end")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/renew", body)
	raw, _ := json.Marshal(body["data"])
	var invoice api.Invoice
	if err = json.Unmarshal(raw, &invoice); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan api.Invoice, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := m.RenewService06(t.Context(), f.customer, sid, "monthly", ID(), time.Now().UTC())
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for v := range results {
		if v.ID != invoice.ID {
			t.Fatal("duplicate renewal invoice", v.ID, invoice.ID)
		}
		contracttest.Data(t, "Invoice", map[string]any{"data": v})
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, invoice.ID, invoice06Payment(400, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	if err = m.DB.QueryRow("SELECT next_due_at FROM services WHERE id=?", sid).Scan(&next); err != nil || !next.Equal(due) {
		t.Fatal("partial payment advanced period", next, err)
	}
	payment := invoice06Payment(599, ID())
	for range 2 {
		if _, err = m.AddInvoicePayment06(t.Context(), f.admin, invoice.ID, payment, "renew-payment-replay"); err != nil {
			t.Fatal(err)
		}
	}
	expect := time.Date(2027, 2, 28, 12, 0, 0, 0, time.UTC)
	if err = m.DB.QueryRow("SELECT next_due_at FROM services WHERE id=?", sid).Scan(&next); err != nil || !next.Equal(expect) {
		t.Fatal(next, err)
	}
	second, err := m.RenewService06(t.Context(), f.customer, sid, "monthly", ID(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, second.ID, invoice06Payment(999, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	expect = time.Date(2027, 3, 31, 12, 0, 0, 0, time.UTC)
	if err = m.DB.QueryRow("SELECT next_due_at FROM services WHERE id=?", sid).Scan(&next); err != nil || !next.Equal(expect) {
		t.Fatal("lost month-end anchor", next, err)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/renew", map[string]string{"cycle": "monthly"}, "renew-month-end")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/services/{id}/renew", body)
	if body["data"].(map[string]any)["id"] != invoice.ID {
		t.Fatal("idempotent replay changed invoice", body)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/renew", map[string]string{"cycle": "annually"}, "renew-month-end")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}

func TestMySQLCommerceAutomationBillingJobsAndFailureIsolation(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	broken, _ := paidService06(t, f)
	cancelled, _ := paidService06(t, f)
	settings, err := m.AutomationSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoApplyCredit = false
	settings.InvoiceDaysBeforeDue = 7
	settings.ReminderDaysBeforeDue = []int64{2}
	settings.OverdueReminderDays = []int64{1}
	settings.SuspendAfterDays = 3
	settings.TerminateAfterDays = 10
	if settings, err = m.UpdateAutomation(t.Context(), f.admin, settings); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	due := at.Add(48 * time.Hour)
	if _, err = m.DB.Exec("UPDATE services SET status='active',next_due_at=?,next_invoice_at=?", due, due); err != nil {
		t.Fatal(err)
	}
	if _, err = m.DB.Exec("UPDATE services SET configuration_snapshot=JSON_OBJECT('kind',123) WHERE id=?", broken); err != nil {
		t.Fatal(err)
	}
	if _, err = m.CancelService06(t.Context(), f.customer, cancelled, &api.RequestCancellationInput{Type: "end_of_period", Reason: "End subscription"}); err != nil {
		t.Fatal(err)
	}
	app := NewApp(m, Config{Demo: true, Secret: ID() + ID()})
	result, err := app.Automation.jobs["generate_renewals"](t.Context(), settings, at)
	if err != nil || result.Processed != 1 || result.Failed != 1 {
		t.Fatal(result, err)
	}
	var invoiceID string
	if err = m.DB.QueryRow("SELECT invoice_id FROM invoice_service_changes WHERE service_id=?", sid).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err = app.Automation.jobs["send_reminders"](t.Context(), settings, at)
		if err != nil || result.Failed != 0 {
			t.Fatal(result, err)
		}
	}
	var reminders int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM invoice_notifications WHERE invoice_id=?", invoiceID).Scan(&reminders); err != nil || reminders != 1 {
		t.Fatal("repeated reminder", reminders, err)
	}
	if _, err = m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 400, "Renewal deposit", ID()); err != nil {
		t.Fatal(err)
	}
	settings.AutoApplyCredit = true
	result, err = app.Automation.jobs["apply_credit"](t.Context(), settings, at)
	if err != nil || result.Processed != 1 {
		t.Fatal(result, err)
	}
	invoice, err := m.Invoice06(t.Context(), f.customer.ID, invoiceID)
	if err != nil || invoice.CreditApplied.AmountMinor != 400 || invoice.BalanceDue.AmountMinor != 599 {
		t.Fatal(invoice, err)
	}
	contracttest.Data(t, "AdminInvoice", map[string]any{"data": invoice})
	// Each object failure leaves later objects eligible, including cancellation.
	result, err = app.Automation.jobs["terminate_overdue"](t.Context(), settings, due.Add(time.Hour))
	if err != nil || result.Processed != 1 {
		t.Fatal(result, err)
	}
	result, err = app.Automation.jobs["suspend_overdue"](t.Context(), settings, due.Add(4*24*time.Hour))
	if err != nil || result.Processed < 1 {
		t.Fatal(result, err)
	}
	var count int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='suspend' AND status='queued'", sid).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	result, err = app.Automation.jobs["suspend_overdue"](t.Context(), settings, due.Add(4*24*time.Hour+time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='suspend' AND status='queued'", sid).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate suspend", count, err)
	}
	if _, err = m.DB.Exec("UPDATE services SET status='suspended',suspension_reason='overdue' WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, invoiceID, invoice06Payment(599, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='unsuspend' AND status='queued'", sid).Scan(&count); err != nil || count != 1 {
		t.Fatal("overdue service not scheduled for unsuspend", count, err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND action='suspend' AND status='queued'", sid).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale suspension survived payment", count, err)
	}
}
