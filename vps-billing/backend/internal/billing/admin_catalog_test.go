package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

type catalogFixture struct {
	store           Store
	catalog         adminCatalogStore
	admin, customer User
	changeActor     func(User)
}

func memoryCatalogFixture(t *testing.T) catalogFixture {
	t.Helper()
	m := NewMemory()
	admin, _ := m.UserByEmail(t.Context(), "admin@aster.local")
	customer, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	return catalogFixture{m, m, admin, customer, func(u User) { m.mu.Lock(); defer m.mu.Unlock(); m.users[u.ID] = u }}
}
func mysqlCatalogFixture(t *testing.T) catalogFixture {
	t.Helper()
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("CatalogFixture!2026"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	admin := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Catalog administrator", Role: "admin", PasswordHash: string(hash)}
	customer := User{ID: ID(), Email: ID() + "@test.invalid", Name: "Catalog customer", Role: "customer", PasswordHash: string(hash)}
	for _, u := range []User{admin, customer} {
		if err = m.AddUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	return catalogFixture{m, m, admin, customer, func(u User) {
		migrationExec(t, ctx, m.DB, "UPDATE users SET role=?,disabled=? WHERE id=?", u.Role, u.Disabled, u.ID)
	}}
}
func catalogTestInput(t *testing.T, f catalogFixture, revision int64) CatalogInput {
	t.Helper()
	config, err := f.store.Currencies(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	taxonomy := testReadTaxonomy(t, f)
	if len(taxonomy.Regions) == 0 {
		t.Fatal("missing fixture taxonomy region")
	}
	region := taxonomy.Regions[0]
	nodeID := ""
	for _, node := range taxonomy.Nodes {
		if node.RegionID == region.ID {
			nodeID = node.ID
			break
		}
	}
	if nodeID == "" {
		t.Fatal("missing fixture taxonomy node")
	}
	return CatalogInput{TaxonomyRevision: taxonomy.Revision, TypeID: region.TypeID, RegionID: region.ID, NodeID: nodeID, Revision: revision, CurrencyRevision: config.Revision, Reason: "Operator reviewed specifications and independent prices", Name: "Catalog fixture", Description: "A real product\nNo automatic provisioning", CPU: 2, MemoryGB: 4, DiskGB: 80, TransferTB: 2, Region: region.Name, Available: true, Prices: []CatalogPrice{{"CNY", 3900, true}}}
}
func readCatalog(t *testing.T, f catalogFixture) CatalogView {
	t.Helper()
	out, err := f.catalog.AdminCatalog(t.Context(), f.admin)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func catalogFind(t *testing.T, view CatalogView, id string) CatalogPlan {
	t.Helper()
	for _, p := range view.Items {
		if p.ID == id {
			return p
		}
	}
	t.Fatal("missing catalog product", id)
	return CatalogPlan{}
}
func catalogHistory(t *testing.T, f catalogFixture) ([]Order, []Invoice, []Service) {
	t.Helper()
	orders, err := f.store.Orders(t.Context(), f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	invoices, err := f.store.Invoices(t.Context(), f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	services, err := f.store.Services(t.Context(), f.customer.ID)
	if err != nil {
		t.Fatal(err)
	}
	return orders, invoices, services
}
func TestAdminCatalogMemory(t *testing.T) { runCatalogSuite(t, memoryCatalogFixture) }
func TestMySQLAdminCatalog(t *testing.T)  { runCatalogSuite(t, mysqlCatalogFixture) }
func runCatalogSuite(t *testing.T, makeFixture func(*testing.T) catalogFixture) {
	t.Run("draft_publish_duplicate_history_and_replay", func(t *testing.T) {
		f := makeFixture(t)
		for _, c := range []struct {
			code      string
			precision int
		}{{"USD", 2}, {"JPY", 0}, {"KWD", 3}} {
			configureTestCurrency(t, f.store, f.admin, c.code, c.precision, "")
		}
		before := readCatalog(t, f)
		input := catalogTestInput(t, f, before.Revision)
		input.Available = false
		input.Prices = []CatalogPrice{}
		draft, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, input)
		if err != nil || draft.Revision != before.Revision+1 || draft.Plan.Available || draft.Plan.ID == "" || draft.Plan.ID == "starter" || len(draft.Plan.Prices) != 0 {
			t.Fatal(draft, err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, draft.Plan.ID, "draft", "draft-key", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("draft was purchasable", err)
		}
		input = catalogTestInput(t, f, draft.Revision)
		input.Prices = append(input.Prices, CatalogPrice{"USD", 600, true}, CatalogPrice{"JPY", MaxMinor, true}, CatalogPrice{"KWD", 1234, true})
		published, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, draft.Plan.ID, input)
		if err != nil || !published.Plan.Available || published.Revision != draft.Revision+1 || len(published.Plan.Prices) != 4 {
			t.Fatal(published, err)
		}
		config, err := f.store.Currencies(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		for _, cur := range config.Currencies {
			if !cur.PrecisionLocked {
				t.Fatal("price precision not locked", cur.Code)
			}
		}
		quoted := f.store.(interface {
			CreateQuotedOrder(context.Context, User, string, string, string, string, *PriceQuote) (Order, bool, error)
		})
		quote := &PriceQuote{3900, 2, published.Revision}
		original, replay, err := quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "history", "history-key", "CNY", quote)
		if err != nil || replay || original.PlanSnapshot == nil || original.PlanSnapshot.Name != input.Name {
			t.Fatal(original, replay, err)
		}
		kwd, _, err := quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "three-decimals", "kwd-key", "KWD", &PriceQuote{1234, 3, published.Revision})
		if err != nil || kwd.TotalMinor != 1234 || kwd.CurrencyFormat.DecimalPlaces != 3 {
			t.Fatal(kwd, err)
		}
		jpy, _, err := quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "max-minor", "jpy-key", "JPY", &PriceQuote{MaxMinor, 0, published.Revision})
		if err != nil || jpy.TotalMinor != MaxMinor || jpy.CurrencyFormat.DecimalPlaces != 0 {
			t.Fatal(jpy, err)
		}
		ordersBefore, invoicesBefore, servicesBefore := catalogHistory(t, f)
		// Compare persisted records on both sides: MySQL DATETIME(6) truncates
		// the nanoseconds of the initial in-process CreateOrder result.
		for _, stored := range ordersBefore {
			if stored.ID == original.ID {
				original = stored
			}
		}
		if len(ordersBefore) != 3 || len(invoicesBefore) != 3 {
			t.Fatal("incomplete history fixture")
		}
		edit := catalogTestEditInput(t, f, published.Plan, published.Revision, published.CurrencyRevision, "Rename without rewriting history")
		edit.Name, edit.CPU = "Updated catalog name", 8
		edited, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, draft.Plan.ID, edit)
		if err != nil || edited.Revision != published.Revision+1 {
			t.Fatal(edited, err)
		}
		if _, _, err = quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "stale", "stale-key", "CNY", quote); !errors.Is(err, ErrPriceChanged) {
			t.Fatal("metadata did not invalidate stale quote", err)
		}
		copied, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, draft.Plan.ID, CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: edited.Revision, CurrencyRevision: edited.CurrencyRevision, Reason: "New draft from existing product"})
		if err != nil || copied.Plan.ID == draft.Plan.ID || copied.Plan.Available || !reflect.DeepEqual(copied.Plan.Prices, edited.Plan.Prices) {
			t.Fatal(copied, err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, copied.Plan.ID, "copy", "copy-key", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("copy was published", err)
		}
		unpublish := catalogTestEditInput(t, f, edited.Plan, copied.Revision, copied.CurrencyRevision, "Take product off sale")
		unpublish.Available = false
		stopped, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, draft.Plan.ID, unpublish)
		if err != nil || stopped.Plan.Available {
			t.Fatal(stopped, err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, draft.Plan.ID, "new-after-stop", "new-after-stop", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("off-sale product was purchasable", err)
		}
		got, replayed, err := quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "history", "history-key", "CNY", quote)
		if err != nil || !replayed || !reflect.DeepEqual(got, original) {
			t.Fatal("existing key did not replay original order", got, replayed, err)
		}
		if _, _, err = quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "different-host", "history-key", "CNY", quote); !errors.Is(err, ErrConflict) {
			t.Fatal("catalog update loosened idempotency fingerprint", err)
		}
		ordersAfter, invoicesAfter, servicesAfter := catalogHistory(t, f)
		if !reflect.DeepEqual(ordersBefore, ordersAfter) || !reflect.DeepEqual(invoicesBefore, invoicesAfter) || !reflect.DeepEqual(servicesBefore, servicesAfter) {
			t.Fatal("catalog mutation rewrote historical business records")
		}
		republish := catalogTestEditInput(t, f, stopped.Plan, stopped.Revision, stopped.CurrencyRevision, "Publish changed product again")
		republish.Available = true
		republish.Prices = []CatalogPrice{{"CNY", 6000, true}, {"USD", 999, true}}
		fresh, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, draft.Plan.ID, republish)
		if err != nil {
			t.Fatal(err)
		}
		current, _, err := quoted.CreateQuotedOrder(t.Context(), f.customer, draft.Plan.ID, "fresh", "fresh-key", "CNY", &PriceQuote{6000, 2, fresh.Revision})
		if err != nil || current.TotalMinor != 6000 || current.PlanSnapshot.Name != edit.Name || current.PlanSnapshot.CPU != 8 || current.PlanSnapshot.Region != edit.Region {
			t.Fatal(current, err)
		}
		config, _ = f.store.Currencies(t.Context(), true)
		for _, code := range []string{"JPY", "KWD"} {
			cur, _ := config.find(code)
			if !cur.PrecisionLocked {
				t.Fatal("removing quote unlocked historic precision", code)
			}
		}
		view := readCatalog(t, f)
		for n, p := range view.Items {
			if n > 0 && view.Items[n-1].ID >= p.ID {
				t.Fatal("catalog is not sorted")
			}
			for i, price := range p.Prices {
				if i > 0 && p.Prices[i-1].Currency >= price.Currency {
					t.Fatal("quotes are not sorted")
				}
			}
		}
	})
	t.Run("strict_validation_and_no_side_effects", func(t *testing.T) {
		f := makeFixture(t)
		configureTestCurrency(t, f.store, f.admin, "USD", 2, "")
		config, _ := f.store.Currencies(t.Context(), true)
		change := currencyUpdateFrom(config)
		for i := range change.Currencies {
			if change.Currencies[i].Code == "USD" {
				change.Currencies[i].Enabled = false
			}
		}
		if _, err := f.store.UpdateCurrencies(t.Context(), f.admin, change); err != nil {
			t.Fatal(err)
		}
		before := readCatalog(t, f)
		audits, _ := f.store.Audits(t.Context(), "")
		cases := []struct {
			name   string
			change func(*CatalogInput)
		}{
			{"empty_name", func(p *CatalogInput) { p.Name = " \n " }}, {"utf8_byte_limit", func(p *CatalogInput) { p.Name = strings.Repeat("中", 41) }},
			{"invalid_utf8", func(p *CatalogInput) { p.Name = string([]byte{0xff}) }}, {"html", func(p *CatalogInput) { p.Description = "<script>" }},
			{"control", func(p *CatalogInput) { p.Description = "description\x00" }}, {"format_control", func(p *CatalogInput) { p.Name = "bad\u202e" }},
			{"empty_reason", func(p *CatalogInput) { p.Reason = " " }}, {"long_reason", func(p *CatalogInput) { p.Reason = strings.Repeat("r", 501) }},
			{"cpu_zero", func(p *CatalogInput) { p.CPU = 0 }}, {"cpu_large", func(p *CatalogInput) { p.CPU = 4097 }},
			{"memory_large", func(p *CatalogInput) { p.MemoryGB = 1048577 }}, {"disk_large", func(p *CatalogInput) { p.DiskGB = 16777217 }}, {"transfer_large", func(p *CatalogInput) { p.TransferTB = 1048577 }},
			{"nil_prices", func(p *CatalogInput) { p.Prices = nil }}, {"empty_publish", func(p *CatalogInput) { p.Prices = []CatalogPrice{} }},
			{"zero_price", func(p *CatalogInput) { p.Prices[0].AmountMinor = 0 }}, {"overflow_price", func(p *CatalogInput) { p.Prices[0].AmountMinor = MaxMinor + 1 }}, {"negative_price", func(p *CatalogInput) { p.Prices[0].AmountMinor = -1 }},
			{"unknown_currency", func(p *CatalogInput) { p.Prices[0].Currency = "EUR" }}, {"folded_currency", func(p *CatalogInput) { p.Prices[0].Currency = "cny" }},
			{"duplicate_currency", func(p *CatalogInput) { p.Prices = append(p.Prices, p.Prices[0]) }}, {"no_active_quote", func(p *CatalogInput) { p.Prices[0].Available = false }},
			{"disabled_currency", func(p *CatalogInput) { p.Prices = []CatalogPrice{{"USD", 600, true}} }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				in := catalogTestInput(t, f, before.Revision)
				tc.change(&in)
				if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, in); !errors.Is(err, ErrCatalog) {
					t.Fatal("invalid catalog accepted", err)
				}
			})
		}
		if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, before.Revision+1)); !errors.Is(err, ErrCatalogConflict) {
			t.Fatal(err)
		}
		if _, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, "missing", catalogTestInput(t, f, before.Revision)); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, "missing", CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: before.Revision, CurrencyRevision: before.CurrencyRevision, Reason: "Copy"}); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, before.Items[0].ID, CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: before.Revision, CurrencyRevision: before.CurrencyRevision, Reason: " "}); !errors.Is(err, ErrCatalog) {
			t.Fatal(err)
		}
		after := readCatalog(t, f)
		afterAudits, _ := f.store.Audits(t.Context(), "")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(audits, afterAudits) {
			t.Fatal("rejected catalog operation had side effects")
		}
		in := catalogTestInput(t, f, before.Revision)
		in.Available = false
		in.Prices = []CatalogPrice{{"USD", 600, true}}
		result, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, in)
		if err != nil || result.Plan.Available {
			t.Fatal(result, err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, result.Plan.ID, "disabled", "disabled-key", "USD"); !errors.Is(err, ErrCurrencyDisabled) {
			t.Fatal(err)
		}
	})
	t.Run("persisted_actor_not_request_role", func(t *testing.T) {
		f := makeFixture(t)
		before := readCatalog(t, f)
		for _, actor := range []User{{ID: ID(), Role: "admin"}, {ID: f.customer.ID, Role: "admin", Email: "forged@test.invalid"}} {
			if _, err := f.catalog.AdminCatalog(t.Context(), actor); !errors.Is(err, ErrAdminRequired) {
				t.Fatal(err)
			}
			if _, err := f.catalog.CreateCatalogPlan(t.Context(), actor, catalogTestInput(t, f, before.Revision)); !errors.Is(err, ErrAdminRequired) {
				t.Fatal(err)
			}
			if _, err := f.catalog.UpdateCatalogPlan(t.Context(), actor, before.Items[0].ID, catalogTestInput(t, f, before.Revision)); !errors.Is(err, ErrAdminRequired) {
				t.Fatal(err)
			}
			if _, err := f.catalog.DuplicateCatalogPlan(t.Context(), actor, before.Items[0].ID, CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: before.Revision, CurrencyRevision: before.CurrencyRevision, Reason: "Copy"}); !errors.Is(err, ErrAdminRequired) {
				t.Fatal(err)
			}
		}
		for _, change := range []func(*User){func(u *User) { u.Role = "customer" }, func(u *User) { u.Disabled = true }} {
			stale := f.admin
			change(&stale)
			f.changeActor(stale)
			if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, before.Revision)); !errors.Is(err, ErrAdminRequired) {
				t.Fatal("cached admin authorized after persisted change", err)
			}
			if _, err := f.catalog.AdminCatalog(t.Context(), f.admin); !errors.Is(err, ErrAdminRequired) {
				t.Fatal(err)
			}
			f.changeActor(f.admin)
		}
		if !reflect.DeepEqual(before, readCatalog(t, f)) {
			t.Fatal("authorization denial changed catalog")
		}
		forged := f.admin
		forged.Role = "customer"
		forged.Email = "spoofed@test.invalid"
		if _, err := f.catalog.CreateCatalogPlan(t.Context(), forged, catalogTestInput(t, f, before.Revision)); err != nil {
			t.Fatal("persisted admin not authoritative", err)
		}
		audits, _ := f.store.Audits(t.Context(), "")
		found := false
		for _, audit := range audits {
			if audit.Action == "catalog.created" {
				found = true
				if audit.ActorEmail != f.admin.Email || audit.UserID != f.admin.ID {
					t.Fatal("spoofed audit identity", audit)
				}
			}
		}
		if !found {
			t.Fatal("missing catalog audit")
		}
	})
	t.Run("concurrent_revision_and_order_snapshot", func(t *testing.T) {
		f := makeFixture(t)
		view := readCatalog(t, f)
		created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, view.Revision))
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				<-start
				in := catalogTestEditInput(t, f, created.Plan, created.Revision, created.CurrencyRevision, "Concurrent metadata update")
				in.Name = fmt.Sprintf("winner-%d", n)
				_, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, in)
				results <- err
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflicts := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrCatalogConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflicts != 1 {
			t.Fatal("same revision had multiple winners", success, conflicts)
		}
		view = readCatalog(t, f)
		plan := catalogFind(t, view, created.Plan.ID)
		start = make(chan struct{})
		results = make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, plan.ID, catalogTestEditInput(t, f, plan, view.Revision, view.CurrencyRevision, "Compete with financial editor"))
			results <- err
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := f.store.UpdatePlanPrices(t.Context(), f.admin, PlanPricesUpdate{Revision: view.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: plan.ID, Amount: "42.00", Available: true, PlanAvailable: true}}})
			results <- err
		}()
		close(start)
		wg.Wait()
		close(results)
		success, conflicts = 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrCatalogConflict) || errors.Is(err, ErrPriceConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflicts != 1 {
			t.Fatal("catalog and price editor did not share revision", success, conflicts)
		}
		view = readCatalog(t, f)
		old := catalogFind(t, view, plan.ID)
		input := catalogTestEditInput(t, f, old, view.Revision, view.CurrencyRevision, "Atomic metadata and quote edit")
		input.Name = "After atomic edit"
		input.CPU = 11
		input.Prices = []CatalogPrice{{"CNY", 7777, true}}
		orders := make(chan Order, 20)
		errs := make(chan error, 21)
		start = make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, plan.ID, input)
			if err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			for n := 0; n < 20; n++ {
				o, _, err := f.store.CreateOrder(t.Context(), f.customer, plan.ID, fmt.Sprintf("atomic-%d", n), fmt.Sprintf("atomic-key-%d", n), "CNY")
				if err != nil {
					errs <- err
				} else {
					orders <- o
				}
			}
		}()
		close(start)
		wg.Wait()
		close(orders)
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		oldAmount := int64(0)
		for _, price := range old.Prices {
			if price.Currency == "CNY" {
				oldAmount = price.AmountMinor
			}
		}
		count := 0
		for o := range orders {
			count++
			if o.PlanSnapshot == nil {
				t.Fatal("missing snapshot")
			}
			oldMatch := o.PlanSnapshot.Name == old.Name && o.PlanSnapshot.CPU == old.CPU && o.TotalMinor == oldAmount
			newMatch := o.PlanSnapshot.Name == input.Name && o.PlanSnapshot.CPU == input.CPU && o.TotalMinor == 7777
			if !oldMatch && !newMatch {
				t.Fatal("torn order metadata/amount snapshot", o)
			}
		}
		if count != 20 {
			t.Fatal("missing concurrent orders", count)
		}
	})
}

type catalogHTTPStoreSpy struct {
	Store
	catalog adminCatalogStore
	calls   atomic.Int64
}

func (s *catalogHTTPStoreSpy) AdminCatalog(ctx context.Context, u User) (CatalogView, error) {
	s.calls.Add(1)
	return s.catalog.AdminCatalog(ctx, u)
}
func (s *catalogHTTPStoreSpy) CreateCatalogPlan(ctx context.Context, u User, p CatalogInput) (CatalogResult, error) {
	s.calls.Add(1)
	return s.catalog.CreateCatalogPlan(ctx, u, p)
}
func (s *catalogHTTPStoreSpy) UpdateCatalogPlan(ctx context.Context, u User, id string, p CatalogInput) (CatalogResult, error) {
	s.calls.Add(1)
	return s.catalog.UpdateCatalogPlan(ctx, u, id, p)
}
func (s *catalogHTTPStoreSpy) DuplicateCatalogPlan(ctx context.Context, u User, id string, p CatalogDuplicateInput) (CatalogResult, error) {
	s.calls.Add(1)
	return s.catalog.DuplicateCatalogPlan(ctx, u, id, p)
}
func TestAdminCatalogHTTP(t *testing.T) {
	runCatalogHTTP(t, memoryCatalogFixture(t), "DemoAdmin!2026", "DemoCustomer!2026")
}
func TestMySQLAdminCatalogHTTP(t *testing.T) {
	runCatalogHTTP(t, mysqlCatalogFixture(t), "CatalogFixture!2026", "CatalogFixture!2026")
}
func runCatalogHTTP(t *testing.T, f catalogFixture, adminPassword, customerPassword string) {
	spy := &catalogHTTPStoreSpy{Store: f.store, catalog: f.catalog}
	srv := httptest.NewServer(NewApp(spy, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, f.customer.Email, customerPassword)
	admin := client(t, srv.URL, f.admin.Email, adminPassword)
	before := readCatalog(t, f)
	input := catalogTestInput(t, f, before.Revision)
	for _, c := range []struct {
		client *testClient
		status int
	}{{anonymous, 401}, {customer, 403}} {
		for _, path := range []string{"/api/v1/admin/catalog", "/api/v1/admin/catalog/starter", "/api/v1/admin/catalog/starter/duplicate"} {
			if status, body := c.client.request("POST", path, input, ""); status != c.status {
				t.Fatal(status, body)
			}
		}
		if status, body := c.client.request("GET", "/api/v1/admin/catalog?role=admin", nil, ""); status != c.status {
			t.Fatal(status, body)
		}
	}
	if spy.calls.Load() != 0 {
		t.Fatal("authorization denial called catalog store")
	}
	saved := admin.csrf
	admin.csrf = "invalid"
	if status, body := admin.request("POST", "/api/v1/admin/catalog", input, ""); status != 403 {
		t.Fatal(status, body)
	}
	admin.csrf = saved
	if spy.calls.Load() != 0 {
		t.Fatal("CSRF denial called catalog store")
	}
	raw, _ := json.Marshal(input)
	var missingNode map[string]json.RawMessage
	_ = json.Unmarshal(raw, &missingNode)
	delete(missingNode, "node_id")
	withoutNode, _ := json.Marshal(missingNode)
	bad := []string{string(withoutNode),
		strings.Replace(string(raw), `"available":true,`, "", 1), strings.Replace(string(raw), `"revision":1,`, "", 1), strings.Replace(string(raw), `"currency_revision":1,`, "", 1),
		strings.Replace(string(raw), `"reason":`, `"reason":null,"unused":`, 1), strings.Replace(string(raw), `"name":`, `"name":"duplicate","name":`, 1), strings.Replace(string(raw), `"name":`, `"Name":`, 1),
		strings.TrimSuffix(string(raw), "}") + `,"id":"attacker"}`, strings.Replace(string(raw), `"amount_minor":3900`, `"amount_minor":39.5`, 1),
		strings.Replace(string(raw), `"currency_revision":1`, `"currency_revision":1.5`, 1),
		strings.Replace(string(raw), `"currency_revision":1`, `"currency_revision":null`, 1), strings.Replace(string(raw), `"prices":[`, `"prices":null,"extra":[`, 1), string(raw) + `{}`,
	}
	for _, body := range bad {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/catalog", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatalf("malformed JSON accepted with status %d", res.StatusCode)
		}
	}
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/catalog", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-CSRF-Token", admin.csrf)
	res, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatal(res.StatusCode)
	}
	if spy.calls.Load() != 0 {
		t.Fatal("invalid body called catalog store")
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if status, _ := admin.request(method, "/api/v1/admin/catalog", input, ""); status != 405 {
			t.Fatal(method, status)
		}
	}
	if status, _ := admin.request("GET", "/api/v1/admin/catalog?role=admin", nil, ""); status != 400 {
		t.Fatal(status)
	}
	status, body := admin.request("POST", "/api/v1/admin/catalog", input, "")
	if status != 201 {
		t.Fatal(status, body)
	}
	data := body["data"].(map[string]any)
	plan := data["plan"].(map[string]any)
	id := plan["id"].(string)
	if data["revision"] != float64(before.Revision+1) || id == "" {
		t.Fatal(body)
	}
	status, body = admin.request("POST", "/api/v1/admin/catalog", input, "")
	if status != 409 || body["error"].(map[string]any)["code"] != "CATALOG_CONFLICT" {
		t.Fatal(status, body)
	}
	status, body = admin.request("POST", "/api/v1/admin/catalog/"+id+"/duplicate", CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: before.Revision + 1, CurrencyRevision: before.CurrencyRevision, Reason: "Duplicate through real endpoint"}, "")
	if status != 201 || body["data"].(map[string]any)["plan"].(map[string]any)["available"] != false {
		t.Fatal(status, body)
	}
	view := readCatalog(t, f)
	edit := catalogTestEditInput(t, f, catalogFind(t, view, id), view.Revision, view.CurrencyRevision, "Edit through real endpoint")
	edit.Available = false
	if status, body = admin.request("POST", "/api/v1/admin/catalog/"+id, edit, ""); status != 200 {
		t.Fatal(status, body)
	}
	if status, body = admin.request("GET", "/api/v1/admin/catalog", nil, ""); status != 200 {
		t.Fatal(status, body)
	}
	raw, _ = json.Marshal(body)
	for _, secret := range []string{"password_hash", f.admin.PasswordHash, f.customer.ID, "actor_id", "user_id"} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("catalog leaked private account data")
		}
	}
	downgraded := f.admin
	downgraded.Role = "customer"
	f.changeActor(downgraded)
	count := spy.calls.Load()
	if status, body = admin.request("GET", "/api/v1/admin/catalog", nil, ""); status != 403 {
		t.Fatal(status, body)
	}
	if status, body = admin.request("POST", "/api/v1/admin/catalog", input, ""); status != 403 {
		t.Fatal(status, body)
	}
	if spy.calls.Load() != count {
		t.Fatal("downgraded cookie reached catalog store")
	}
	f.changeActor(f.admin)
	configureTestCurrency(t, f.store, f.admin, "JPY", 0, "")
	prior := readCatalog(t, f)
	priorRegistry, err := f.store.Currencies(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	changed := currencyUpdateFrom(priorRegistry)
	for n := range changed.Currencies {
		if changed.Currencies[n].Code == "JPY" {
			changed.Currencies[n].DecimalPlaces = 2
		}
	}
	if _, err = f.store.UpdateCurrencies(t.Context(), f.admin, changed); err != nil {
		t.Fatal(err)
	}
	stale := catalogTestInput(t, f, prior.Revision)
	stale.CurrencyRevision = priorRegistry.Revision
	stale.Prices = []CatalogPrice{{"JPY", 100, true}}
	for _, operation := range []struct {
		path string
		body any
	}{
		{"/api/v1/admin/catalog", stale},
		{"/api/v1/admin/catalog/" + id, stale},
		{"/api/v1/admin/catalog/" + id + "/duplicate", CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: prior.Revision, CurrencyRevision: priorRegistry.Revision, Reason: "Stale precision must be rejected"}},
	} {
		if status, body := admin.request("POST", operation.path, operation.body, ""); status != 409 || body["error"].(map[string]any)["code"] != "CATALOG_CONFLICT" {
			t.Fatal("HTTP precision revision conflict", status, body)
		}
	}
	for _, revision := range []int64{0, -1, MaxMinor + 1} {
		invalid := stale
		invalid.CurrencyRevision = revision
		if status, body := admin.request("POST", "/api/v1/admin/catalog", invalid, ""); status != 400 || body["error"].(map[string]any)["code"] != "INVALID_CATALOG" {
			t.Fatal("HTTP precision revision range", status, body)
		}
	}
	if after := readCatalog(t, f); !reflect.DeepEqual(prior.Items, after.Items) || prior.Revision != after.Revision {
		t.Fatal("rejected HTTP precision changed catalog")
	}
}
func TestMySQLAdminCatalogRollback(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	ctx := t.Context()
	configureTestCurrency(t, f.store, f.admin, "EUR", 2, "")
	before := readCatalog(t, f)
	configBefore, _ := f.store.Currencies(ctx, true)
	auditsBefore, _ := f.store.Audits(ctx, "")
	migrationExec(t, ctx, m.DB, "CREATE TRIGGER catalog_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='fixture failure'")
	t.Cleanup(func() { migrationExec(t, context.Background(), m.DB, "DROP TRIGGER IF EXISTS catalog_reject_audit") })
	input := catalogTestInput(t, f, before.Revision)
	input.Prices = []CatalogPrice{{"EUR", 1234, true}}
	if _, err := f.catalog.CreateCatalogPlan(ctx, f.admin, input); err == nil {
		t.Fatal("late create failure was accepted")
	}
	input.Name = "Should roll back"
	if _, err := f.catalog.UpdateCatalogPlan(ctx, f.admin, before.Items[0].ID, input); err == nil {
		t.Fatal("late edit failure was accepted")
	}
	if _, err := f.catalog.DuplicateCatalogPlan(ctx, f.admin, before.Items[0].ID, CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: before.Revision, CurrencyRevision: before.CurrencyRevision, Reason: "Copy should roll back"}); err == nil {
		t.Fatal("late duplicate failure was accepted")
	}
	after := readCatalog(t, f)
	configAfter, _ := f.store.Currencies(ctx, true)
	auditsAfter, _ := f.store.Audits(ctx, "")
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(configBefore, configAfter) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
		t.Fatal("late audit failure leaked plan, quote, precision lock, revision or audit mutation")
	}
}
func TestAdminCatalogNoSilentLimitOrAliasing(t *testing.T) {
	f := memoryCatalogFixture(t)
	m := f.store.(*Memory)
	seed := m.plans[0]
	m.mu.Lock()
	for i := 0; i < 1005; i++ {
		m.plans = append(m.plans, Plan{ID: fmt.Sprintf("bulk-%04d", i), Name: "Fixture", CPU: 1, MemoryGB: 1, DiskGB: 1, TransferTB: 1, Region: seed.Region, TypeID: seed.TypeID, RegionID: seed.RegionID, NodeID: seed.NodeID})
	}
	m.mu.Unlock()
	view := readCatalog(t, f)
	if len(view.Items) != 1008 {
		t.Fatal("catalog silently truncated", len(view.Items))
	}
	p := catalogFind(t, view, "starter")
	p.Prices[0].AmountMinor = 1
	if again := catalogFind(t, readCatalog(t, f), "starter"); again.Prices[0].AmountMinor == 1 {
		t.Fatal("catalog DTO aliases stored prices")
	}
}

func TestAdminCatalogMemoryBoundaries(t *testing.T) {
	t.Run("revision_exhaustion_is_atomic", func(t *testing.T) {
		f := memoryCatalogFixture(t)
		m := f.store.(*Memory)
		m.mu.Lock()
		m.pricesRevision = MaxMinor
		m.mu.Unlock()
		before := readCatalog(t, f)
		if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, MaxMinor)); !errors.Is(err, ErrCatalogConflict) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, readCatalog(t, f)) {
			t.Fatal("exhausted revision mutated catalog")
		}
	})
	t.Run("new_precision_lock_is_atomic", func(t *testing.T) {
		f := memoryCatalogFixture(t)
		m := f.store.(*Memory)
		configureTestCurrency(t, m, f.admin, "JPY", 0, "")
		m.mu.Lock()
		m.currencies.Revision = MaxMinor
		m.mu.Unlock()
		before := readCatalog(t, f)
		input := catalogTestInput(t, f, before.Revision)
		input.Prices = []CatalogPrice{{"JPY", 100, true}}
		if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, input); !errors.Is(err, ErrCurrencyConflict) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, readCatalog(t, f)) {
			t.Fatal("exhausted precision revision mutated catalog")
		}
		config, err := m.Currencies(t.Context(), true)
		if err != nil {
			t.Fatal(err)
		}
		cur, _ := config.find("JPY")
		if cur.PrecisionLocked {
			t.Fatal("failed precision lock leaked")
		}
	})
	t.Run("cancelled_context_has_no_mutation", func(t *testing.T) {
		f := memoryCatalogFixture(t)
		before := readCatalog(t, f)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := f.catalog.CreateCatalogPlan(ctx, f.admin, catalogTestInput(t, f, before.Revision)); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, readCatalog(t, f)) {
			t.Fatal("cancelled catalog write committed")
		}
	})
	t.Run("unsafe_stored_minor_fails_closed", func(t *testing.T) {
		f := memoryCatalogFixture(t)
		m := f.store.(*Memory)
		m.mu.Lock()
		m.prices["starter"]["CNY"] = PlanPrice{MaxMinor + 1, true}
		m.mu.Unlock()
		if _, err := m.AdminCatalog(t.Context(), f.admin); !errors.Is(err, errStoredCatalog) {
			t.Fatal("unsafe stored number reached browser DTO", err)
		}
	})
}
