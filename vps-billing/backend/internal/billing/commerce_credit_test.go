package billing

import (
	"aster/billing/internal/contracttest"
	"aster/billing/migrations"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestMySQLCreditLedgerConcurrencyAndReplay(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	seed, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 10, "Opening balance", "credit-opening")
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "CreditEntry", map[string]any{"data": seed})
	replay, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 10, "Opening balance", "credit-opening")
	if err != nil || replay.ID != seed.ID {
		t.Fatal("replay", replay, err)
	}
	if _, err = m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 11, "Opening balance", "credit-opening"); !errors.Is(err, ErrConflict) {
		t.Fatal("key reuse", err)
	}
	if _, err = m.AdjustCredit06(t.Context(), f.customer, f.customer.ID, 10, "Invalid actor", "credit-invalid"); !errors.Is(err, ErrAdminRequired) {
		t.Fatal("customer adjustment", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, -1, "Concurrent debit", fmt.Sprintf("credit-debit-%02d", i))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	accepted, rejected := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrInsufficientCredit) {
			rejected++
		} else {
			t.Fatal(err)
		}
	}
	if accepted != 10 || rejected != 10 {
		t.Fatal("overspend", accepted, rejected)
	}
	summary, err := m.Credit06(t.Context(), f.customer.ID)
	if err != nil || summary.Balance.AmountMinor != 0 {
		t.Fatal(summary, err)
	}
	contracttest.Data(t, "CreditSummary", map[string]any{"data": summary})
	var count, sequence, total, minimum int64
	if err = m.DB.QueryRow("SELECT COUNT(*),MAX(sequence),SUM(amount_minor),MIN(balance_after_minor) FROM credit_ledger WHERE user_id=?", f.customer.ID).Scan(&count, &sequence, &total, &minimum); err != nil || count != 11 || sequence != 11 || total != 0 || minimum != 0 {
		t.Fatal(count, sequence, total, minimum, err)
	}
}

func TestMySQLCreditSnapshotsEverySupportedPrecision(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	for _, tc := range []struct {
		currency string
		decimals int
		amount   string
	}{{"JPY", 0, "1"}, {"EUR", 2, "1.23"}, {"KWD", 3, "1.234"}, {"XTS", 6, "1.234567"}} {
		t.Run(tc.currency, func(t *testing.T) {
			configureTestCurrency(t, m, f.admin, tc.currency, tc.decimals, tc.amount)
			u := User{ID: ID(), Email: ID() + "@credit.test", Name: "Credit customer", Role: "customer", PasswordHash: "unused"}
			if err := m.AddUser(t.Context(), u); err != nil {
				t.Fatal(err)
			}
			migrationExec(t, t.Context(), m.DB, "UPDATE users SET currency=? WHERE id=?", tc.currency, u.ID)
			entry, err := m.AdjustCredit06(t.Context(), f.admin, u.ID, 123, "Precision snapshot", "credit-precision-"+tc.currency)
			if err != nil || entry.Amount.DecimalPlaces != int64(tc.decimals) || entry.Amount.Currency != tc.currency {
				t.Fatal(entry, err)
			}
			contracttest.Data(t, "CreditEntry", map[string]any{"data": entry})
		})
	}
}

func TestCommerceCheckNormalizationPreservesLiteralSemantics(t *testing.T) {
	for _, expression := range []string{"kind IN ('top_up','invoice_payment','refund','adjustment','affiliate_payout','overpayment')", "status IN ('running','succeeded','failed','uncertain')"} {
		if !commerceCheckMatches(currencyMigrationCheck{"CHECK", "YES", expression}, expression) {
			t.Fatal("unrecognized commerce check", expression)
		}
	}
	for _, test := range []struct {
		actual, expected string
		valid            bool
	}{
		{"((amount_minor >= -(9007199254740991)) and (amount_minor <= 9007199254740991))", "amount_minor >= -9007199254740991 AND amount_minor <= 9007199254740991", true},
		{"((refunded_minor <= (credit_applied_minor + amount_paid_minor)))", "refunded_minor <= credit_applied_minor + amount_paid_minor", true},
		{"amount_minor >= -9007199254740990", "amount_minor >= -9007199254740991", false},
		{"(amount_minor >= 0) OR (amount_minor <= 9007199254740991)", "amount_minor >= 0 AND amount_minor <= 9007199254740991", false},
	} {
		if commerceCheckMatches(currencyMigrationCheck{"CHECK", "YES", test.actual}, test.expected) != test.valid {
			t.Fatal("changed commerce check", test)
		}
	}
	if _, ok := normalizeMigrationCheck("kind IN ('TOP_UP')"); ok {
		t.Fatal("changed literal case accepted")
	}
	if _, ok := normalizeMigrationCheck("status='running' OR 1=1 -- ignored"); ok {
		t.Fatal("SQL comment accepted")
	}
}

func TestMySQLCreditHandlersOwnershipAndContract(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	if _, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 400, "First adjustment", "credit-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AdjustCredit06(t.Context(), f.admin, f.customer.ID, 100, "Second adjustment", "credit-second"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	guest := client(t, srv.URL, "", "")
	for _, tc := range []struct{ path, template string }{{"/credit", "/credit"}, {"/credit/entries", "/credit/entries"}} {
		status, body := customer.request("GET", "/api/v1"+tc.path, nil, "")
		if status != 200 {
			t.Fatal(status, body)
		}
		contracttest.Response(t, "GET", tc.template, body)
		status, body = guest.request("GET", "/api/v1"+tc.path, nil, "")
		if status != 401 {
			t.Fatal("guest", status, body)
		}
		contracttest.Error(t, body)
	}
	status, body := admin.request("GET", "/api/v1/credit", nil, "")
	if status != 200 || body["data"].(map[string]any)["balance"].(map[string]any)["amount_minor"] != float64(0) {
		t.Fatal("other account leaked", status, body)
	}
	contracttest.Data(t, "CreditSummary", body)
	status, body = admin.request("GET", "/api/v1/admin/clients/"+f.customer.ID+"/credit?limit=1", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/admin/clients/{id}/credit", body)
	data := body["data"].(map[string]any)
	cursor := data["next_cursor"].(string)
	if len(data["items"].([]any)) != 1 {
		t.Fatal(data)
	}
	status, body = customer.request("GET", "/api/v1/credit/entries?limit=1&cursor="+cursor, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/credit/entries", body)
	data = body["data"].(map[string]any)
	if data["next_cursor"] != nil || data["items"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] != float64(400) {
		t.Fatal("keyset pagination", data)
	}
	status, body = customer.request("GET", "/api/v1/admin/clients/"+f.admin.ID+"/credit", nil, "")
	if status != 403 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = customer.request("GET", "/api/v1/credit/entries?cursor=-1", nil, "")
	if status != 400 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
}

func TestMySQLCreditMigrationResumesAndRejectsCollision(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprint(collision), func(t *testing.T) {
			m, ctx := migrationTestDatabase(t)
			prefix := 0
			for i, s := range schemaVersions {
				if s == migrations.CommerceLedger {
					prefix = i
					break
				}
			}
			if prefix == 0 {
				t.Fatal("missing credit migration")
			}
			migrationHistoricalPrefix(t, ctx, m, prefix)
			if collision {
				migrationExec(t, ctx, m.DB, "CREATE TABLE credit_limits(currency VARCHAR(3) PRIMARY KEY) ENGINE=InnoDB")
				if err := m.Migrate(ctx); err == nil {
					t.Fatal("adopted incompatible table")
				}
				var n int
				if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='credit_ledger'").Scan(&n); err != nil || n != 0 {
					t.Fatal("mutated before preflight", n, err)
				}
				return
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if err := m.Migrate(ctx); err != nil {
				t.Fatal("repeat", err)
			}
			if status, err := m.SchemaStatus(ctx); err != nil || status.Status != "current" {
				t.Fatal(status, err)
			}
		})
	}
}
