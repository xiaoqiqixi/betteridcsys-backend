package billing

import (
	api "aster/billing/internal/financeapi"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestMySQLCheckoutPasswordSurvivesPaymentAndBootstrap(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	controllerID, preset, imageID := "controller-test", "approved-preset", ID()
	if _, err := m.DB.Exec(`INSERT INTO os_images(id,family,name,version,min_disk_gb,enabled,controller_id,remote_image_id,sha256) VALUES(?,'ubuntu','Ubuntu','24.04',4,TRUE,?,'ubuntu-24',?)`, imageID, controllerID, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	product := product06Fixture(t, f)
	product.Kind, product.ControllerID, product.RemoteProductID = "cloud_server", &controllerID, &preset
	product.OSImageIDs = []string{imageID}
	product.Options = []api.ProductOption{{ID: ID(), Key: "os", Kind: "os_image", Label: api.LocalizedText{"en": "Operating system"}, Required: true, Choices: []api.OptionChoice{}}}
	p, err := m.SaveProduct06(t.Context(), f.admin, "", product, ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	password := "CustomerCheckoutNeverPersisted!2026"
	input := api.CreateOrderInput{Currency: "CNY", Items: []api.CartItemInput{{ProductID: p.ID, Cycle: "monthly", Quantity: 1, Options: map[string]any{"os": imageID}, Hostname: "checkout.example", RootPassword: password}}}
	quote, err := m.Quote06(t.Context(), f.customer, api.CreateQuoteInput{Currency: input.Currency, Items: input.Items}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	input.QuoteDigest = quote.Quote.QuoteDigest
	key := ID()
	order, err := m.CreateOrder06(t.Context(), f.customer, input, key)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := m.CreateOrder06(t.Context(), f.customer, input, key); err != nil || replay.Order.ID != order.Order.ID {
		t.Fatal("password-bearing checkout replay changed its order", err)
	}
	if _, err = m.AddInvoicePayment06(t.Context(), f.admin, order.Invoice.ID, invoice06Payment(order.Invoice.Total.AmountMinor, ID()), ID()); err != nil {
		t.Fatal(err)
	}
	stored, err := m.Order06(t.Context(), f.customer.ID, order.Order.ID)
	if err != nil || len(stored.Order.Items[0].ServiceIDs) != 1 {
		t.Fatal("payment did not create exactly one service", err)
	}
	sid := stored.Order.Items[0].ServiceIDs[0]
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	if _, err := m.DB.Exec("UPDATE services SET status='provisioning',resource_id=NULL WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	// A fresh app has no in-memory credential from the checkout request.
	app := NewApp(m, Config{Services: runtime})
	settings, err := m.AutomationSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.AutoProvision = true
	for tick := range 3 {
		if result, err := app.Automation.jobs["dispatch_provisioning"](t.Context(), settings, time.Now().Add(time.Duration(tick)*time.Minute)); err != nil || result.Failed != 0 {
			t.Fatal("bootstrap dispatch failed", result, err)
		}
	}
	controller.mu.Lock()
	params := controller.lastParams
	controller.mu.Unlock()
	if params.Password != "" || params.ImageID != "ubuntu-24" || bcrypt.CompareHashAndPassword([]byte(params.PasswordHash), []byte(password)) != nil {
		t.Fatal("customer credential was replaced or sent as plaintext")
	}
	service, err := m.Service06(t.Context(), f.customer.ID, sid)
	if err != nil || service.Status != "active" {
		t.Fatal("bootstrap did not activate service", err)
	}
	for _, query := range []string{"SELECT CAST(snapshot AS CHAR) FROM order_items WHERE order_id=?", "SELECT CAST(configuration_snapshot AS CHAR) FROM services WHERE id=?", "SELECT CAST(payload AS CHAR) FROM service_tasks WHERE service_id=?"} {
		id := sid
		if strings.Contains(query, "order_items") {
			id = order.Order.ID
		}
		rows, err := m.DB.Query(query, id)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil || strings.Contains(raw, password) || strings.Contains(raw, `"root_password"`) {
				rows.Close()
				t.Fatal("plaintext checkout credential persisted", err)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
}
