package billing

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func cancellationUsers(t *testing.T, m *Memory) (User, User, User) {
	t.Helper()
	owner, err := m.UserByEmail(t.Context(), "demo@aster.local")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := m.UserByEmail(t.Context(), "other@aster.local")
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	return owner, other, admin
}

func cancellationOrder(t *testing.T, m *Memory, owner User) (Order, Invoice, string) {
	t.Helper()
	key := ID()
	o, _, err := m.CreateOrder(t.Context(), owner, "starter", "cancel-"+key[:8], key, "CNY")
	if err != nil {
		t.Fatal(err)
	}
	invoices, _ := m.Invoices(t.Context(), owner.ID)
	for _, i := range invoices {
		if i.OrderID == o.ID {
			return o, i, key
		}
	}
	t.Fatal("missing invoice")
	return Order{}, Invoice{}, ""
}

func TestCancellationPreservesHistoryAndReplay(t *testing.T) {
	m := NewMemory()
	owner, _, admin := cancellationUsers(t, m)
	o, i, key := cancellationOrder(t, m, owner)
	creation := m.orderEvents[0]
	out, err := m.CancelOrder(t.Context(), admin, o.ID, "  customer requested cancellation  ")
	if err != nil {
		t.Fatal(err)
	}
	expectedOrder, expectedInvoice := cloneOrder(o), cloneInvoice(i)
	expectedOrder.Status, expectedInvoice.Status = "cancelled", "void"
	if !reflect.DeepEqual(out.Order, expectedOrder) || !reflect.DeepEqual(out.Invoice, expectedInvoice) {
		t.Fatal("cancellation changed immutable history", out)
	}
	if len(m.audits) != 2 || len(m.orderEvents) != 2 || !reflect.DeepEqual(m.orderEvents[0], creation) {
		t.Fatal("creation history lost or cancellation not recorded once")
	}
	audit := m.audits[1]
	if audit.Action != "order.cancelled" || audit.UserID != owner.ID || audit.ActorEmail != admin.Email || audit.Target != o.ID || !strings.HasSuffix(audit.Detail, "customer requested cancellation") {
		t.Fatal("missing owner/actor/reason evidence", audit)
	}
	if !validCancellationEventPayload(m.orderEvents[1].Payload, out.Order, out.Invoice, true) {
		t.Fatal("invalid cancellation event")
	}
	repeated, err := m.CancelOrder(t.Context(), owner, o.ID, "different retry reason")
	if err != nil || !reflect.DeepEqual(repeated, out) || len(m.audits) != 2 || len(m.orderEvents) != 2 {
		t.Fatal("retry changed history", repeated, err)
	}
	// An old creation key continues to resolve the same current order even when
	// today's price, display settings and confirmation revision have changed.
	m.mu.Lock()
	m.prices["starter"]["CNY"] = PlanPrice{AmountMinor: 5000, Available: false}
	m.pricesRevision++
	m.currencies.Currencies[0].Name = "Current name"
	m.mu.Unlock()
	replayed, replay, err := m.CreateQuotedOrder(t.Context(), owner, "starter", o.Hostname, key, "CNY", &PriceQuote{PriceMinor: o.TotalMinor, DecimalPlaces: 2, PriceRevision: 1})
	if err != nil || !replay || !reflect.DeepEqual(replayed, expectedOrder) || len(m.orders) != 1 {
		t.Fatal("cancelled order creation replay changed identity or history", replayed, replay, err)
	}
	summary, err := m.Summary(t.Context(), owner.ID, "CNY")
	if err != nil || summary.PendingInvoices != 0 || summary.UnpaidTotal != 0 || summary.Revenue != 0 {
		t.Fatal("void invoice counted as obligation or revenue", summary, err)
	}
	config, _ := m.Currencies(t.Context(), true)
	if config.Currencies[0].Usage.PendingOrders != 0 || config.Currencies[0].Usage.UnpaidInvoices != 0 || !config.Currencies[0].PrecisionLocked {
		t.Fatal("obligation not removed or precision unlocked", config)
	}
	out.Order.PlanSnapshot.Name = "caller mutation"
	out.Invoice.Issuer.Name = "caller mutation"
	stored, _ := m.Orders(t.Context(), owner.ID)
	invoice, _ := m.Invoice(t.Context(), owner.ID, i.ID)
	if !reflect.DeepEqual(stored[0], expectedOrder) || !reflect.DeepEqual(invoice, expectedInvoice) {
		t.Fatal("response aliases stored history")
	}
}

func TestCancellationRejectsUnsafeStateWithoutWrites(t *testing.T) {
	m := NewMemory()
	owner, other, _ := cancellationUsers(t, m)
	cases := []struct {
		name   string
		change func(*Memory, int, int)
	}{
		{"paid order", func(m *Memory, o, i int) { m.orders[o].Status = "paid" }},
		{"paid invoice", func(m *Memory, o, i int) { m.invoices[i].Status = "paid" }},
		{"paid timestamp", func(m *Memory, o, i int) { now := time.Now(); m.invoices[i].PaidAt = &now }},
		{"partial cancellation", func(m *Memory, o, i int) { m.orders[o].Status = "cancelled" }},
		{"manual cancelled pair", func(m *Memory, o, i int) { m.orders[o].Status, m.invoices[i].Status = "cancelled", "void" }},
		{"invoice owner", func(m *Memory, o, i int) { m.invoices[i].UserID = other.ID }},
		{"invoice amount", func(m *Memory, o, i int) { m.invoices[i].AmountMinor++ }},
		{"invoice currency", func(m *Memory, o, i int) { m.invoices[i].Currency = "USD" }},
		{"currency format", func(m *Memory, o, i int) { m.invoices[i].CurrencyFormat.Prefix = "$" }},
		{"issuer snapshot", func(m *Memory, o, i int) { m.invoices[i].Issuer = &InvoiceIdentity{Name: "Different issuer"} }},
		{"missing issuer snapshot", func(m *Memory, o, i int) { m.orders[o].Issuer = nil }},
		{"explicit service", func(m *Memory, o, i int) { m.orders[o].ServiceID = ID() }},
		{"matching service", func(m *Memory, o, i int) {
			m.services = append(m.services, Service{ID: ID(), UserID: owner.ID, Name: m.orders[o].Hostname, Status: "terminated"})
		}},
		{"missing invoice", func(m *Memory, o, i int) { m.invoices[i].OrderID = ID() }},
		{"duplicate invoice", func(m *Memory, o, i int) {
			duplicate := m.invoices[i]
			duplicate.ID = ID()
			m.invoices = append(m.invoices, duplicate)
		}},
		{"missing creation event", func(m *Memory, o, i int) { m.orderEvents = m.orderEvents[:len(m.orderEvents)-1] }},
		{"attempted creation event", func(m *Memory, o, i int) { m.orderEvents[len(m.orderEvents)-1].Attempts = 1 }},
		{"processed creation event", func(m *Memory, o, i int) { now := time.Now(); m.orderEvents[len(m.orderEvents)-1].ProcessedAt = &now }},
		{"ambiguous creation event", func(m *Memory, o, i int) { m.orderEvents = append(m.orderEvents, m.orderEvents[len(m.orderEvents)-1]) }},
		{"other lifecycle event", func(m *Memory, o, i int) {
			m.orderEvents = append(m.orderEvents, orderOutboxEvent{AggregateID: m.orders[o].ID, EventType: "order.fulfilled"})
		}},
		{"wrong creation association", func(m *Memory, o, i int) {
			m.orderEvents[len(m.orderEvents)-1].Payload = []byte(`{"order_id":"wrong","invoice_id":"wrong","status":"pending_payment"}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, _, _ := cancellationOrder(t, m, owner)
			oi, ii := len(m.orders)-1, len(m.invoices)-1
			tc.change(m, oi, ii)
			before, _ := m.Orders(t.Context(), "")
			invoices, _ := m.Invoices(t.Context(), "")
			audits, events := len(m.audits), len(m.orderEvents)
			if _, err := m.CancelOrder(t.Context(), owner, o.ID, "No longer needed"); !errors.Is(err, ErrCancellationState) {
				t.Fatal("unsafe cancellation accepted", err)
			}
			after, _ := m.Orders(t.Context(), "")
			afterInvoices, _ := m.Invoices(t.Context(), "")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(invoices, afterInvoices) || len(m.audits) != audits || len(m.orderEvents) != events {
				t.Fatal("rejected cancellation wrote data")
			}
		})
	}
}

func TestCancellationOwnershipInputsAndConcurrentRetries(t *testing.T) {
	m := NewMemory()
	owner, other, _ := cancellationUsers(t, m)
	o, _, _ := cancellationOrder(t, m, owner)
	if _, err := m.CancelOrder(t.Context(), other, o.ID, "Not mine"); !errors.Is(err, ErrNotFound) {
		t.Fatal("other customer cancellation", err)
	}
	for _, reason := range []string{"", "   ", strings.Repeat("a", 501), strings.Repeat("中", 167), "<script>", "line\nbreak", "hidden\u200btext", string([]byte{0xff})} {
		if _, err := m.CancelOrder(t.Context(), owner, o.ID, reason); !errors.Is(err, ErrCancellationInput) {
			t.Fatal("invalid reason accepted", err)
		}
	}
	if _, err := m.CancelOrder(t.Context(), owner, strings.ToUpper(o.ID), "No longer needed"); !errors.Is(err, ErrCancellationInput) {
		t.Fatal("noncanonical ID accepted", err)
	}
	// An unrelated service must not prevent cancellation of this unpaid order.
	m.services = append(m.services, Service{ID: ID(), UserID: owner.ID, Name: "another-service", Status: "active"})
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := m.CancelOrder(t.Context(), owner, o.ID, "No longer needed")
			if err != nil || out.Order.Status != "cancelled" || out.Invoice.Status != "void" {
				t.Error("concurrent cancellation", out, err)
			}
		}()
	}
	wg.Wait()
	if len(m.orders) != 1 || len(m.invoices) != 1 || len(m.audits) != 2 || len(m.orderEvents) != 2 || m.services[0].Status != "active" {
		t.Fatal("concurrent retries duplicated effects or changed service")
	}
}

func TestCancellationCurrencyObligationsAndLegacySnapshots(t *testing.T) {
	m := NewMemory()
	owner, _, admin := cancellationUsers(t, m)
	initial, _ := m.Currencies(t.Context(), true)
	if len(initial.Currencies) != 1 || initial.DefaultCurrency != "CNY" {
		t.Fatal("fresh installation currency changed", initial)
	}
	for _, tc := range []struct {
		code     string
		decimals int
		amount   string
		minor    int64
	}{{"USD", 2, "10.95", 1095}, {"KWD", 3, "1.234", 1234}, {"JPY", 0, "1250", 1250}} {
		t.Run(tc.code, func(t *testing.T) {
			configureTestCurrency(t, m, admin, tc.code, tc.decimals, tc.amount)
			o, _, err := m.CreateOrder(t.Context(), owner, "starter", "cancel-"+strings.ToLower(tc.code), ID(), tc.code)
			if err != nil {
				t.Fatal(err)
			}
			// A migrated legacy order may lack issuer and plan snapshots. Do not
			// fill those gaps with today's plan or operator identity.
			if tc.code == "USD" {
				m.mu.Lock()
				m.orders[len(m.orders)-1].PlanSnapshot = nil
				m.orders[len(m.orders)-1].Issuer = nil
				m.invoices[len(m.invoices)-1].Issuer = nil
				m.mu.Unlock()
			}
			config, _ := m.Currencies(t.Context(), true)
			disable := currencyUpdateFrom(config)
			for n := range disable.Currencies {
				if disable.Currencies[n].Code == tc.code {
					disable.Currencies[n].Enabled = false
				}
			}
			var inUse *CurrencyInUse
			if _, err := m.UpdateCurrencies(t.Context(), admin, disable); !errors.As(err, &inUse) {
				t.Fatal("pending obligations did not block disabling", err)
			}
			out, err := m.CancelOrder(t.Context(), owner, o.ID, "No longer needed")
			if err != nil || out.Order.TotalMinor != tc.minor || out.Invoice.AmountMinor != tc.minor || out.Order.Currency != tc.code || out.Order.CurrencyFormat != o.CurrencyFormat {
				t.Fatal("cancellation changed currency or integer amount", out, err)
			}
			if tc.code == "USD" && (out.Order.PlanSnapshot != nil || out.Order.Issuer != nil || out.Invoice.Issuer != nil) {
				t.Fatal("invented legacy snapshots", out)
			}
			updated, err := m.UpdateCurrencies(t.Context(), admin, disable)
			if err != nil {
				t.Fatal("cancelled obligations still blocked disabling", err)
			}
			currency, _ := updated.find(tc.code)
			if currency.Enabled || !currency.PrecisionLocked || currency.DecimalPlaces != tc.decimals {
				t.Fatal("disabling changed historical precision", currency)
			}
			if repeated, err := m.CancelOrder(t.Context(), owner, o.ID, "Retry after disabling"); err != nil || !reflect.DeepEqual(repeated, out) {
				t.Fatal("disabled historical currency prevented safe retry", repeated, err)
			}
		})
	}
}
