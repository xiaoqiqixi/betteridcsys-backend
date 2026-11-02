package billing

import (
	"aster/billing/internal/contracttest"
	"aster/billing/internal/financeapi"
	"bytes"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

func setAffiliatePolicy(t *testing.T, m *MySQL, bp int, recurring bool) {
	t.Helper()
	s := financeapi.AffiliateSettings{Enabled: true, DefaultRateBp: int64(bp), Scope: "first_payment", ScopeMonths: 12, HoldDays: 1, AttributionDays: 30, MinWithdraw: []financeapi.AffiliateCurrencyAmount{}, MaxPerReferral: []financeapi.AffiliateCurrencyAmount{}, AllowSelfReferral: true, Clawback: "reverse_all"}
	_ = json.Unmarshal([]byte(`[]`), &s.ProductOverrides)
	if recurring {
		s.Scope = "all_payments"
	}
	raw, _ := json.Marshal(s)
	if _, err := m.DB.Exec("UPDATE affiliate_settings SET document=? WHERE id=1", raw); err != nil {
		t.Fatal(err)
	}
}

func seedAffiliateInvoice(t *testing.T, m *MySQL, uid string, at time.Time) string {
	return seedAffiliateInvoiceKind(t, m, uid, at, "order")
}
func seedAffiliateInvoiceKind(t *testing.T, m *MySQL, uid string, at time.Time, kind string) string {
	t.Helper()
	id := ID()
	raw, _ := json.Marshal(DefaultCurrencyFormat("CNY"))
	tx, err := m.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO invoices(id,number,user_id,status,amount_minor,currency,issued_at,due_at,paid_at,currency_snapshot,amount_paid_minor,subtotal_minor,commerce_version) VALUES(?,?,?,'paid',3900,'CNY',?,?,?,?,3900,3900,6)", id, "INV-"+id, uid, at, at, at, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE invoices SET kind=? WHERE id=?", kind, id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor) VALUES(?,?,0,'Affiliate fixture',3900)", ID(), id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("INSERT INTO invoice_payments(id,invoice_id,method,amount_minor,refunded_minor,status,created_at) VALUES(?,?,'manual',3900,0,'succeeded',?)", ID(), id, at); err != nil {
		t.Fatal(err)
	}
	if err = recordAffiliateCommissionTx(t.Context(), tx, id, at); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMySQLAffiliateOwnershipHoldAndAtomicWithdrawals(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	v := accountHTTP(t, c, "GET", "/affiliate", "/affiliate", nil, "").(map[string]any)
	if v["enabled"] != false || v["code"] != nil || v["rate_bp"] != float64(0) {
		t.Fatal(v)
	}
	v = accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	again := accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	if v["code"] != again["code"] {
		t.Fatal("activation changed referral code")
	}
	setAffiliatePolicy(t, m, 0, false)
	ref := client(t, srv.URL, "", "")
	registered := accountHTTP(t, ref, "POST", "/auth/register", "/auth/register", map[string]any{"email": "referred-person@example.com", "password": "Referred password!2026", "name": "Referred", "country": "DE", "currency": "CNY", "locale": "en", "accept_terms": true, "referral_code": v["code"]}, "affiliate-signup").(map[string]any)
	uid := registered["user"].(map[string]any)["id"].(string)
	setAffiliatePolicy(t, m, 1000, false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedAffiliateInvoice(t, m, uid, now.Add(-48*time.Hour))
	seedAffiliateInvoiceKind(t, m, uid, now.Add(-47*time.Hour), "renewal")
	v = accountHTTP(t, c, "GET", "/affiliate", "/affiliate", nil, "").(map[string]any)
	if v["balances"].([]any)[0].(map[string]any)["pending"].(map[string]any)["amount_minor"] != float64(390) {
		t.Fatal("first invoice policy", v)
	}
	refs := accountHTTP(t, c, "GET", "/affiliate/referrals?limit=1", "/affiliate/referrals", nil, "").(map[string]any)
	raw, _ := json.Marshal(refs)
	if bytes.Contains(raw, []byte("referred-person@example.com")) || !bytes.Contains(raw, []byte("r***@example.com")) {
		t.Fatal("referral email privacy", string(raw))
	}
	foreign := accountHTTP(t, ref, "GET", "/affiliate/referrals", "/affiliate/referrals", nil, "").(map[string]any)
	if len(foreign["items"].([]any)) != 0 {
		t.Fatal("cross-account referrals returned")
	}
	accountFailure(t, c, "GET", "/affiliate/referrals?client_id="+uid, nil, 400)
	status, out := c.request("POST", "/api/v1/affiliate/withdraw", map[string]any{"amount_minor": 1}, "held-withdrawal")
	if status != 409 {
		t.Fatal(status, out)
	}
	contracttest.Error(t, out)
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 3}, now.Add(-36*time.Hour))
	if err != nil || result.Processed != 0 {
		t.Fatal("hold shortened", result, err)
	}
	result, err = a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 1}, now)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	for range 2 {
		v = accountHTTP(t, c, "POST", "/affiliate/withdraw", "/affiliate/withdraw", map[string]any{"amount_minor": 300}, "affiliate-payout-1").(map[string]any)
		if v["balances"].([]any)[0].(map[string]any)["paid_out"].(map[string]any)["amount_minor"] != float64(300) {
			t.Fatal(v)
		}
	}
	var wg sync.WaitGroup
	type response struct {
		status int
		out    map[string]any
	}
	results := make(chan response, 2)
	for _, key := range []string{"affiliate-payout-2", "affiliate-payout-3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := c.request("POST", "/api/v1/affiliate/withdraw", map[string]any{"amount_minor": 90}, key)
			results <- response{status, out}
		}()
	}
	wg.Wait()
	close(results)
	wins := 0
	for r := range results {
		if r.status == 200 {
			wins++
			contracttest.Response(t, "POST", "/affiliate/withdraw", r.out)
		} else if r.status != 409 {
			t.Fatal(r)
		} else {
			contracttest.Error(t, r.out)
		}
	}
	if wins != 1 {
		t.Fatal("concurrent overdraw", wins)
	}
	credit := accountHTTP(t, c, "GET", "/credit", "/credit", nil, "").(map[string]any)
	if credit["balance"].(map[string]any)["amount_minor"] != float64(390) {
		t.Fatal("duplicate credit", credit)
	}
	v = accountHTTP(t, ref, "GET", "/affiliate", "/affiliate", nil, "").(map[string]any)
	if v["balances"].([]any)[0].(map[string]any)["paid_out"].(map[string]any)["amount_minor"] != float64(0) {
		t.Fatal("foreign commission returned", v)
	}
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	accountFailure(t, owner, "POST", "/affiliate/activate", map[string]any{}, 403)
}

func TestMySQLAffiliateSettlementReplayAndRollback(t *testing.T) {
	f := newCancellationSQLFixture(t)
	m := f.m
	p := paymentFixture{store: m, owner: f.owner, other: f.other, ctx: f.ctx}
	if _, err := m.DB.Exec("INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6))", f.other.ID, ID()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec("INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", f.owner.ID, f.other.ID); err != nil {
		t.Fatal(err)
	}
	setAffiliatePolicy(t, m, 1250, true)
	o, i := p.order(t)
	attempt := p.reserve(t, i)
	paid := syntheticPayment(attempt)
	// Failure after commission insertion must roll back both money and commission.
	migrationExec(t, f.ctx, m.DB, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'payment.settled','{}',UTC_TIMESTAMP(6))", ID(), o.ID)
	if _, err := m.SettlePayment(f.ctx, attempt.ID, paid); err == nil {
		t.Fatal("expected late settlement failure")
	}
	var n int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_commissions").Scan(&n); err != nil || n != 0 {
		t.Fatal("commission escaped rollback", n, err)
	}
	migrationExec(t, f.ctx, m.DB, "DELETE FROM outbox_events WHERE aggregate_id=? AND event_type='payment.settled'", o.ID)
	for range 2 {
		if _, err := m.SettlePayment(f.ctx, attempt.ID, paid); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_commissions").Scan(&n); err != nil || n != 1 {
		t.Fatal("commission duplicated", n, err)
	}
	if err := m.Migrate(f.ctx); err != nil {
		t.Fatal("affiliate schema replay", err)
	}
	if status, err := m.SchemaStatus(f.ctx); err != nil || status.Status != "current" {
		t.Fatal(status, err)
	}
}

func TestMySQLAffiliateRefundDuringHoldAndRecurringPolicy(t *testing.T) {
	f, a, _ := accountServer(t)
	m := f.Store.(*MySQL)
	other := f.customer
	other.ID = ID()
	other.Email = "affiliate-other@example.com"
	if err := m.AddUser(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6))", f.customer.ID, ID())
	migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", other.ID, f.customer.ID)
	setAffiliatePolicy(t, m, 1000, true)
	now := time.Now().UTC()
	id := seedAffiliateInvoice(t, m, other.ID, now.Add(-48*time.Hour))
	seedAffiliateInvoice(t, m, other.ID, now.Add(-48*time.Hour))
	seedAffiliateRefund(t, m, id, 3900, now.Add(-47*time.Hour))
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 1}, now)
	if err != nil || result.Processed != 2 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	v, err := a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
	if err != nil || v.Balances[0].Available.AmountMinor != 390 || v.Balances[0].Pending.AmountMinor != 0 {
		t.Fatal(v, err)
	}
	if a.Automation.jobs["release_affiliate"] == nil {
		t.Fatal("release job not registered")
	}
}

func TestMySQLAffiliateCommercePartialSettlementAndCreditRollback(t *testing.T) {
	f := newCancellationSQLFixture(t)
	m := f.m
	p := paymentFixture{store: m, owner: f.owner, other: f.other, ctx: f.ctx}
	migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6))", f.other.ID, ID())
	migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", f.owner.ID, f.other.ID)
	setAffiliatePolicy(t, m, 1000, true)
	_, invoice := p.order(t)
	partial, err := m.AddInvoicePayment06(f.ctx, f.admin, invoice.ID, invoice06Payment(1000, "affiliate-partial"), "affiliate-partial-1")
	if err != nil || partial.Status != "unpaid" {
		t.Fatal(partial, err)
	}
	var n int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_commissions").Scan(&n); err != nil || n != 0 {
		t.Fatal("partial invoice earned commission", n, err)
	}
	remaining := invoice.AmountMinor - 1000
	if _, err = m.AdjustCredit06(f.ctx, f.admin, f.owner.ID, remaining, "Opening credit", "affiliate-credit-seed"); err != nil {
		t.Fatal(err)
	}
	setAffiliatePolicy(t, m, 10001, true)
	if _, err = m.PayCredit06(f.ctx, f.owner, invoice.ID, "affiliate-credit-pay"); err == nil {
		t.Fatal("invalid commission policy accepted")
	}
	v, err := m.Invoice06(f.ctx, f.owner.ID, invoice.ID)
	if err != nil || v.Status != "unpaid" || v.CreditApplied.AmountMinor != 0 || len(v.Payments) != 1 {
		t.Fatal("failed commission escaped settlement transaction", v, err)
	}
	credit, err := m.Credit06(f.ctx, f.owner.ID)
	if err != nil || credit.Balance.AmountMinor != remaining {
		t.Fatal("failed commission consumed credit", credit, err)
	}
	setAffiliatePolicy(t, m, 1000, true)
	for range 2 {
		if _, err = m.PayCredit06(f.ctx, f.owner, invoice.ID, "affiliate-credit-pay"); err != nil {
			t.Fatal(err)
		}
	}
	var commission int64
	if err = m.DB.QueryRow("SELECT amount_minor FROM affiliate_commissions WHERE invoice_id=?", invoice.ID).Scan(&commission); err != nil || commission != 100 {
		t.Fatal(commission, err)
	}
	// A second invoice enters commerce through a partial manual payment and
	// completes through a gateway. Both completion routes must use the hook.
	_, second := p.order(t)
	if _, err = m.AddInvoicePayment06(f.ctx, f.admin, second.ID, invoice06Payment(1000, "affiliate-partial-two"), "affiliate-partial-2"); err != nil {
		t.Fatal(err)
	}
	attempt := p.reserve(t, second)
	paid := syntheticPayment(attempt)
	for range 2 {
		if _, err = m.SettlePayment(f.ctx, attempt.ID, paid); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_commissions").Scan(&n); err != nil || n != 2 {
		t.Fatal("commerce settlement replay", n, err)
	}
}

func seedAffiliateRefund(t *testing.T, m *MySQL, invoice string, amount int64, at time.Time) string {
	t.Helper()
	var payment string
	if err := m.DB.QueryRow("SELECT id FROM invoice_payments WHERE invoice_id=? AND method IN ('manual','gateway') ORDER BY id LIMIT 1", invoice).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	id := ID()
	migrationExec(t, t.Context(), m.DB, "INSERT INTO invoice_refunds(id,invoice_id,payment_id,amount_minor,destination,status,reason,created_at,updated_at) VALUES(?,?,?,?,'credit','succeeded','Affiliate fixture refund',?,?)", id, invoice, payment, amount, at, at)
	migrationExec(t, t.Context(), m.DB, "UPDATE invoice_payments SET refunded_minor=refunded_minor+?,status='refunded' WHERE id=?", amount, payment)
	migrationExec(t, t.Context(), m.DB, "UPDATE invoices SET refunded_minor=refunded_minor+?,status='partially_refunded' WHERE id=?", amount, invoice)
	return id
}
