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
	raw, _ := json.Marshal(map[string]any{"commission_basis_points": bp, "recurring": recurring})
	if _, err := m.DB.Exec("UPDATE account_settings SET document=? WHERE name='affiliate_policy'", raw); err != nil {
		t.Fatal(err)
	}
}

func seedAffiliateInvoice(t *testing.T, m *MySQL, uid string, at time.Time) string {
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
	if v["enabled"] != false || v["code"] != nil || v["commission_percent"] != float64(0) {
		t.Fatal(v)
	}
	v = accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	again := accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	if v["code"] != again["code"] {
		t.Fatal("activation changed referral code")
	}
	ref := client(t, srv.URL, "", "")
	registered := accountHTTP(t, ref, "POST", "/auth/register", "/auth/register", map[string]any{"email": "referred-person@example.com", "password": "Referred password!2026", "name": "Referred", "country": "DE", "currency": "CNY", "locale": "en", "accept_terms": true, "referral_code": v["code"]}, "affiliate-signup").(map[string]any)
	uid := registered["user"].(map[string]any)["id"].(string)
	setAffiliatePolicy(t, m, 1000, false)
	now := time.Now().UTC().Truncate(time.Microsecond)
	seedAffiliateInvoice(t, m, uid, now.Add(-48*time.Hour))
	seedAffiliateInvoice(t, m, uid, now.Add(-47*time.Hour))
	v = accountHTTP(t, c, "GET", "/affiliate", "/affiliate", nil, "").(map[string]any)
	if v["pending"].(map[string]any)["amount_minor"] != float64(390) {
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
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 3}, now)
	if err != nil || result.Processed != 0 {
		t.Fatal("hold shortened", result, err)
	}
	result, err = a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 1}, now)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	for range 2 {
		v = accountHTTP(t, c, "POST", "/affiliate/withdraw", "/affiliate/withdraw", map[string]any{"amount_minor": 300}, "affiliate-payout-1").(map[string]any)
		if v["paid_out"].(map[string]any)["amount_minor"] != float64(300) {
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
	if v["paid_out"].(map[string]any)["amount_minor"] != float64(0) {
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
	migrationExec(t, f.ctx, m.DB, "UPDATE invoices SET refunded_minor=3900,status='refunded' WHERE id=?", id)
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 1}, now)
	if err != nil || result.Processed != 2 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	v, err := a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
	if err != nil || v.Available.AmountMinor != 390 || v.Pending.AmountMinor != 0 {
		t.Fatal(v, err)
	}
	if a.Automation.jobs["release_affiliate"] == nil {
		t.Fatal("release job not registered")
	}
}
