package billing

import (
	"aster/billing/internal/financeapi"
	"aster/billing/migrations"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAffiliateSelfReferralPrefixesAndCookies(t *testing.T) {
	for _, tc := range []struct {
		email, ref, ip, other string
		want                  bool
	}{
		{"same@example.com", "SAME@example.com", "192.0.2.1", "198.51.100.1", true},
		{"a@example.com", "b@example.com", "192.0.2.1", "192.0.2.254", true},
		{"a@example.com", "b@example.com", "::ffff:192.0.2.1", "192.0.2.254", true},
		{"a@example.com", "b@example.com", "2001:db8:1:2::1", "2001:db8:1:2::9", true},
		{"a@example.com", "b@example.com", "2001:db8:1:2::1", "2001:db8:1:3::9", false},
		{"a@example.com", "b@example.com", "", "", false},
	} {
		t.Run(tc.ip+tc.ref, func(t *testing.T) {
			if got := selfReferral(tc.email, tc.ref, affiliateIPPrefix(tc.ip), affiliateIPPrefix(tc.other)); got != tc.want {
				t.Fatal(got)
			}
		})
	}
	a := &App{Config: Config{Secret: strings.Repeat("s", 32)}}
	for _, tc := range []struct {
		name    string
		expires int64
		want    string
	}{{"valid", time.Now().Add(time.Hour).Unix(), "code"}, {"expired", time.Now().Add(-time.Hour).Unix(), ""}} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := a.sealAccount("affiliate-attribution", affiliateAttribution{Code: "code", Expires: tc.expires})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/auth/register", nil)
			r.AddCookie(&http.Cookie{Name: "aster_affiliate", Value: value})
			if got := a.affiliateRegistrationCode(r, "fallback"); got != tc.want {
				t.Fatal(got)
			}
		})
	}
}
func TestMySQLAffiliateSettingsRevisionPermissionsAndSnapshots(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	path := "/admin/affiliate/settings"
	accountFailure(t, customer, "GET", path, nil, 403)
	settings := accountHTTP(t, owner, "GET", path, path, nil, "").(map[string]any)
	if settings["enabled"] != false || settings["hold_days"] != float64(30) || settings["scope"] != "first_payment" {
		t.Fatal(settings)
	}
	settings["enabled"] = true
	settings["default_rate_bp"] = float64(1000)
	settings["hold_days"] = float64(1)
	saved := accountHTTP(t, owner, "PATCH", path, path, settings, "").(map[string]any)
	accountFailure(t, owner, "PATCH", path, settings, 409)
	if saved["revision"] == settings["revision"] {
		t.Fatal("missing revision increment")
	}
	activate := accountHTTP(t, customer, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	_ = activate
	other := f.customer
	other.ID = ID()
	other.Email = "snapshot-referral@example.com"
	if err := m.AddUser(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", other.ID, f.customer.ID)
	now := time.Now().UTC()
	invoice := seedAffiliateInvoice(t, m, other.ID, now.Add(-48*time.Hour))
	saved["default_rate_bp"] = float64(9000)
	saved["hold_days"] = float64(365)
	saved["scope"] = "first_months"
	accountHTTP(t, owner, "PATCH", path, path, saved, "")
	tx, err := m.DB.BeginTx(f.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = recordAffiliateCommissionTx(f.ctx, tx, invoice, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{AffiliateHoldDays: 365}, now)
	if err != nil || result.Processed != 1 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	summary, err := a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
	if err != nil || summary.Balances[0].Available.AmountMinor != 390 {
		t.Fatal(summary, err)
	}
	var bp, revision int64
	var scope string
	if err = m.DB.QueryRow("SELECT rate_bp,scope,settings_revision FROM affiliate_commission_lines WHERE commission_id=(SELECT id FROM affiliate_commissions WHERE invoice_id=?)", invoice).Scan(&bp, &scope, &revision); err != nil || bp != 1000 || scope != "first_payment" || revision != 1 {
		t.Fatal(bp, scope, revision, err)
	}
	var n int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='affiliate.settings.updated'").Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	// Reads and writes require clients.write, even where the settings are readable.
	accountFailure(t, customer, "GET", "/admin/affiliates", nil, 403)
	adminPath := "/admin/affiliates/" + f.customer.ID
	accountHTTP(t, owner, "GET", adminPath, "/admin/affiliates/{id}", nil, "")
	accountHTTP(t, owner, "PATCH", adminPath, "/admin/affiliates/{id}", map[string]any{"rate_bp": 0, "status": "suspended", "note": "Reviewed"}, "")
	accountHTTP(t, owner, "GET", "/admin/affiliates?limit=1", "/admin/affiliates", nil, "")
}
func TestMySQLAffiliateClawbackMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, policy, state     string
		wantAvailable, wantDebt int64
	}{
		{"pending all", "reverse_all", "pending", 195, 0},
		{"disabled affiliate", "reverse_all", "disabled", 195, 0},
		{"pending only", "reverse_pending", "pending", 195, 0},
		{"available all", "reverse_all", "available", 195, 0},
		{"available preserved", "reverse_pending", "available", 390, 0},
		{"withdrawn debt", "reverse_all", "withdrawn", 0, 195},
		{"withdrawn preserved", "reverse_pending", "withdrawn", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, a, srv := accountServer(t)
			m := f.Store.(*MySQL)
			setAffiliatePolicy(t, m, 1000, true)
			s, err := readAffiliateSettings(f.ctx, m.DB, "")
			if err != nil {
				t.Fatal(err)
			}
			s.Clawback = tc.policy
			raw, _ := json.Marshal(s)
			migrationExec(t, f.ctx, m.DB, "UPDATE affiliate_settings SET document=? WHERE id=1", raw)
			other := f.customer
			other.ID = ID()
			other.Email = other.ID + "@example.com"
			if err = m.AddUser(f.ctx, other); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,UTC_TIMESTAMP(6))", f.customer.ID, ID())
			migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", other.ID, f.customer.ID)
			now := time.Now().UTC()
			at := now.Add(-48 * time.Hour)
			invoice := seedAffiliateInvoice(t, m, other.ID, at)
			if tc.state == "disabled" {
				migrationExec(t, f.ctx, m.DB, "UPDATE users SET disabled=TRUE WHERE id=?", f.customer.ID)
			}
			if tc.state != "pending" {
				result, e := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{}, now)
				if e != nil || result.Failed != 0 {
					t.Fatal(result, e)
				}
			}
			if tc.state == "withdrawn" {
				c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
				accountHTTP(t, c, "POST", "/affiliate/withdraw", "/affiliate/withdraw", map[string]any{"amount_minor": 390}, "withdraw-matrix")
			}
			refundTime := now
			if tc.state == "pending" {
				refundTime = at.Add(time.Hour)
			}
			seedAffiliateRefund(t, m, invoice, 1950, refundTime)
			for range 2 {
				result, e := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{}, now.Add(time.Second))
				if e != nil || result.Failed != 0 {
					t.Fatal(result, e)
				}
			}
			out, err := a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
			if err != nil || out.Balances[0].Available.AmountMinor != tc.wantAvailable || out.Balances[0].Debt.AmountMinor != tc.wantDebt {
				t.Fatal(out, err)
			}
			if tc.policy == "reverse_all" || tc.state == "pending" {
				var auditCount int
				if err = m.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE user_id=? AND action='affiliate.reversed' AND actor_email='system'", f.customer.ID).Scan(&auditCount); err != nil || auditCount != 1 {
					t.Fatal(auditCount, err)
				}
			}
			if tc.wantDebt > 0 {
				seedAffiliateInvoice(t, m, other.ID, at)
				result, e := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{}, now)
				if e != nil || result.Failed != 0 {
					t.Fatal(result, e)
				}
				out, err = a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
				if err != nil || out.Balances[0].Available.AmountMinor != 195 || out.Balances[0].Debt.AmountMinor != 0 {
					t.Fatal("future earnings did not offset debt", out, err)
				}
			}
		})
	}
}

func TestMySQLAffiliateCurrenciesPayoutsMinimumAndSuspension(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	setAffiliatePolicy(t, m, 1000, true)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "")
	now := time.Now().UTC()
	at := now.Add(-48 * time.Hour)
	for _, currency := range []string{"CNY", "USD"} {
		other := f.customer
		other.ID = ID()
		other.Email = other.ID + "@example.com"
		if err := m.AddUser(f.ctx, other); err != nil {
			t.Fatal(err)
		}
		migrationExec(t, f.ctx, m.DB, "UPDATE users SET currency=? WHERE id=?", currency, other.ID)
		migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", other.ID, f.customer.ID)
		id := ID()
		format, _ := json.Marshal(DefaultCurrencyFormat(currency))
		tx, err := m.DB.BeginTx(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec("INSERT INTO invoices(id,number,user_id,status,amount_minor,currency,issued_at,due_at,paid_at,currency_snapshot,amount_paid_minor,subtotal_minor,commerce_version) VALUES(?,?,?,'paid',3900,?,?,?,?,?,3900,3900,6)", id, "INV-"+id, other.ID, currency, at, at, at, format)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor) VALUES(?,?,0,'Currency fixture',3900)", ID(), id); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec("INSERT INTO invoice_payments(id,invoice_id,method,amount_minor,refunded_minor,status,created_at) VALUES(?,?,'cash',3900,0,'succeeded',?)", ID(), id, at); err != nil {
			t.Fatal(err)
		}
		if err = recordAffiliateCommissionTx(f.ctx, tx, id, at); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	result, err := a.releaseAffiliate(f.ctx, financeapi.AutomationSettings{}, now)
	if err != nil || result.Processed != 2 || result.Failed != 0 {
		t.Fatal(result, err)
	}
	summary, err := a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
	if err != nil || len(summary.Balances) != 2 {
		t.Fatal(summary, err)
	}
	for _, b := range summary.Balances {
		if b.Available.AmountMinor != 390 {
			t.Fatal(b)
		}
	}
	path := "/admin/affiliate/settings"
	s := accountHTTP(t, owner, "GET", path, path, nil, "").(map[string]any)
	s["enabled"] = false
	s["min_withdraw"] = []any{map[string]any{"currency": "CNY", "amount_minor": 100}}
	accountHTTP(t, owner, "PATCH", path, path, s, "")
	if status, out := c.request("POST", "/api/v1/affiliate/withdraw", map[string]any{"amount_minor": 99}, "minimum-denied"); status != 409 {
		t.Fatal(status, out)
	}
	accountHTTP(t, c, "POST", "/affiliate/withdraw", "/affiliate/withdraw", map[string]any{"amount_minor": 390}, "currency-credit-payout")
	if status, out := c.request("POST", "/api/v1/affiliate/withdraw", map[string]any{"amount_minor": 1}, "currency-denied"); status != 409 {
		t.Fatal(status, out)
	}
	payoutPath := "/admin/affiliates/" + f.customer.ID + "/payouts"
	input := map[string]any{"currency": "USD", "amount_minor": 390, "reference": "wire-2026-001"}
	for range 2 {
		accountHTTP(t, owner, "POST", payoutPath, "/admin/affiliates/{id}/payouts", input, "currency-manual-payout")
	}
	summary, err = a.affiliateSummary(f.ctx, m.DB, f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range summary.Balances {
		if b.Available.AmountMinor != 0 || b.PaidOut.AmountMinor != 390 {
			t.Fatal(b)
		}
	}
	var count int
	var detail string
	if err = m.DB.QueryRow("SELECT COUNT(*),MAX(detail) FROM audit_events WHERE action='affiliate.payout' AND detail LIKE '%wire-2026-001%'").Scan(&count, &detail); err != nil || count != 1 || !strings.Contains(detail, "USD") {
		t.Fatal(count, detail, err)
	}
	credit, err := m.Credit06(f.ctx, f.customer.ID)
	if err != nil || credit.Balance.AmountMinor != 390 || credit.Balance.Currency != "CNY" {
		t.Fatal(credit, err)
	}
	// A role that can edit settings still cannot read or settle affiliates.
	role := accountHTTP(t, owner, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "Affiliate settings only", "description": "", "permissions": []string{"settings.write"}}, "").(map[string]any)
	accountHTTP(t, owner, "PATCH", "/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": role["id"]}, "")
	limited := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	accountHTTP(t, limited, "GET", path, path, nil, "")
	for _, p := range []string{"/admin/affiliates", "/admin/affiliates/" + f.customer.ID} {
		accountFailure(t, limited, "GET", p, nil, 403)
	}
	accountFailure(t, limited, "PATCH", "/admin/affiliates/"+f.customer.ID, map[string]any{"rate_bp": nil, "status": "active", "note": ""}, 403)
	accountFailure(t, limited, "POST", payoutPath, input, 403)
	// Suspension and explicit zero overrides both prevent new earnings.
	setAffiliatePolicy(t, m, 1000, true)
	for _, patch := range []map[string]any{{"rate_bp": 1000, "status": "suspended", "note": "Paused"}, {"rate_bp": 0, "status": "active", "note": "Zero override"}} {
		accountHTTP(t, owner, "PATCH", "/admin/affiliates/"+f.customer.ID, "/admin/affiliates/{id}", patch, "")
		var referred string
		if err = m.DB.QueryRow("SELECT client_id FROM affiliate_referrals WHERE affiliate_id=? LIMIT 1", f.customer.ID).Scan(&referred); err != nil {
			t.Fatal(err)
		}
		invoice := seedAffiliateInvoice(t, m, referred, at)
		var amount int64
		if err = m.DB.QueryRow("SELECT amount_minor FROM affiliate_commissions WHERE invoice_id=?", invoice).Scan(&amount); err != nil || amount != 0 {
			t.Fatal(amount, err)
		}
	}
}

func TestMySQLAffiliateAttributionAtRegistration(t *testing.T) {
	f, _, srv := accountServer(t)
	m := f.Store.(*MySQL)
	c := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	activated := accountHTTP(t, c, "POST", "/affiliate/activate", "/affiliate/activate", map[string]any{}, "").(map[string]any)
	for _, tc := range []struct {
		name, prefix string
		allow, want  bool
	}{{"same subnet blocked", "127.0.0.0/24", false, false}, {"same subnet allowed", "127.0.0.0/24", true, true}, {"different subnet", "198.51.100.0/24", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := readAffiliateSettings(f.ctx, m.DB, "")
			if err != nil {
				t.Fatal(err)
			}
			s.AllowSelfReferral = tc.allow
			raw, _ := json.Marshal(s)
			migrationExec(t, f.ctx, m.DB, "UPDATE affiliate_settings SET document=? WHERE id=1", raw)
			migrationExec(t, f.ctx, m.DB, "INSERT INTO affiliate_registration_ips(user_id,ip_prefix) VALUES(?,?) ON DUPLICATE KEY UPDATE ip_prefix=VALUES(ip_prefix)", f.customer.ID, tc.prefix)
			visitor := client(t, srv.URL, "", "")
			accountHTTP(t, visitor, "GET", "/session?referral_code="+activated["code"].(string), "/session", nil, "")
			input := map[string]any{"email": ID() + "@example.com", "password": "Referral password!2026", "name": "Referred", "country": "DE", "currency": "CNY", "locale": "en", "accept_terms": true}
			registered := accountHTTP(t, visitor, "POST", "/auth/register", "/auth/register", input, ID()).(map[string]any)
			id := registered["user"].(map[string]any)["id"].(string)
			var n int
			if err = m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_referrals WHERE client_id=? AND affiliate_id=?", id, f.customer.ID).Scan(&n); err != nil || (n == 1) != tc.want {
				t.Fatal(n, err)
			}
		})
	}
}
func TestMySQLAffiliateMigrationResumesAndPreservesHistory(t *testing.T) {
	statements := migrationStatements(migrations.AffiliateSettings)
	for _, cut := range []int{0, 3, len(statements) - 1} {
		t.Run(string(rune('a'+cut)), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			prefix := 0
			for i, s := range schemaVersions {
				if s == migrations.AffiliateSettings {
					prefix = i
					break
				}
			}
			if prefix == 0 {
				t.Fatal("missing affiliate settings migration")
			}
			migrationHistoricalPrefix(t, ctx, m, prefix)
			affiliateID, clientID, invoiceID, commissionID := ID(), ID(), ID(), ID()
			created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			for _, id := range []string{affiliateID, clientID} {
				migrationExec(t, ctx, m.DB, "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,?,?,'customer','unused')", id, id+"@example.com", id)
			}
			migrationExec(t, ctx, m.DB, "INSERT INTO affiliates(user_id,code,created_at) VALUES(?,?,?)", affiliateID, ID(), created)
			format, _ := json.Marshal(DefaultCurrencyFormat("CNY"))
			migrationExec(t, ctx, m.DB, "INSERT INTO invoices(id,number,user_id,status,amount_minor,currency,issued_at,due_at,paid_at,currency_snapshot,amount_paid_minor,subtotal_minor,commerce_version) VALUES(?,?,?,'paid',3900,'CNY',?,?,?,?,3900,3900,6)", invoiceID, "INV-"+invoiceID, clientID, created, created, created, format)
			migrationExec(t, ctx, m.DB, "INSERT INTO affiliate_commissions(id,affiliate_id,client_id,invoice_id,currency,decimal_places,basis_points,amount_minor,withdrawn_minor,status,created_at) VALUES(?,?,?,?,'CNY',2,1000,390,150,'available',?)", commissionID, affiliateID, clientID, invoiceID, created)
			for _, q := range statements[:cut] {
				migrationExec(t, ctx, m.DB, q)
			}
			for range 2 {
				if err := m.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if state, err := m.SchemaStatus(ctx); err != nil || state.Status != "current" {
				t.Fatal(state, err)
			}
			var amount, withdrawn int64
			if err := m.DB.QueryRow("SELECT amount_minor,withdrawn_minor FROM affiliate_commissions WHERE id=?", commissionID).Scan(&amount, &withdrawn); err != nil || amount != 390 || withdrawn != 150 {
				t.Fatal(amount, withdrawn, err)
			}
			var snapshots int
			if err := m.DB.QueryRow("SELECT COUNT(*) FROM affiliate_commission_lines WHERE commission_id=? AND rate_bp=1000", commissionID).Scan(&snapshots); err != nil || snapshots != 1 {
				t.Fatal(snapshots, err)
			}
			settings, err := readAffiliateSettings(ctx, m.DB, "")
			if err != nil || settings.Enabled || settings.DefaultRateBp != 0 || settings.Revision != 0 {
				t.Fatal(settings, err)
			}
		})
	}
}
