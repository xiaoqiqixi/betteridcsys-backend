package billing

import (
	"errors"
	"reflect"
	"testing"
)

func TestAdminCatalogCurrencyPrecisionRevisionMemory(t *testing.T) {
	runCatalogCurrencyRevision(t, memoryCatalogFixture(t))
}
func TestMySQLAdminCatalogCurrencyPrecisionRevision(t *testing.T) {
	runCatalogCurrencyRevision(t, mysqlCatalogFixture(t))
}
func runCatalogCurrencyRevision(t *testing.T, f catalogFixture) {
	configureTestCurrency(t, f.store, f.admin, "JPY", 0, "")
	oldRegistry, err := f.store.Currencies(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	oldView := readCatalog(t, f)
	if oldView.CurrencyRevision != oldRegistry.Revision {
		t.Fatal("view does not record its currency snapshot")
	}
	oldJPY, _ := oldRegistry.find("JPY")
	if oldJPY.PrecisionLocked || oldJPY.DecimalPlaces != 0 {
		t.Fatal("fixture is not a new unquoted zero-decimal currency")
	}
	editRegistry := currencyUpdateFrom(oldRegistry)
	for n := range editRegistry.Currencies {
		if editRegistry.Currencies[n].Code == "JPY" {
			editRegistry.Currencies[n].DecimalPlaces = 2
		}
	}
	newRegistry, err := f.store.UpdateCurrencies(t.Context(), f.admin, editRegistry)
	if err != nil {
		t.Fatal(err)
	}
	afterPrecision := readCatalog(t, f)
	if afterPrecision.Revision != oldView.Revision || afterPrecision.CurrencyRevision != newRegistry.Revision {
		t.Fatal("catalog fix unexpectedly changed existing financial revision protocol")
	}
	oldAmount := catalogTestInput(t, f, oldView.Revision)
	oldAmount.CurrencyRevision = oldRegistry.Revision
	oldAmount.Name = "One hundred JPY at old precision zero"
	oldAmount.Prices = []CatalogPrice{{Currency: "JPY", AmountMinor: 100, Available: true}}
	assertRejected := func(input CatalogInput, want error) {
		t.Helper()
		before := readCatalog(t, f)
		configBefore, e := f.store.Currencies(t.Context(), true)
		if e != nil {
			t.Fatal(e)
		}
		auditsBefore, e := f.store.Audits(t.Context(), "")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.catalog.CreateCatalogPlan(t.Context(), f.admin, input); !errors.Is(e, want) {
			t.Fatal("create accepted stale/invalid currency precision", e)
		}
		if _, e = f.catalog.UpdateCatalogPlan(t.Context(), f.admin, "starter", input); !errors.Is(e, want) {
			t.Fatal("edit accepted stale/invalid currency precision", e)
		}
		if _, e = f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, "starter", CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: input.Revision, CurrencyRevision: input.CurrencyRevision, Reason: input.Reason}); !errors.Is(e, want) {
			t.Fatal("copy accepted stale/invalid currency precision", e)
		}
		after := readCatalog(t, f)
		configAfter, e := f.store.Currencies(t.Context(), true)
		if e != nil {
			t.Fatal(e)
		}
		auditsAfter, e := f.store.Audits(t.Context(), "")
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(configBefore, configAfter) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
			t.Fatal("rejected precision revision changed product, quotation, lock, revision or audit")
		}
	}
	// The old global revision is still current, but minor100 must NOT silently
	// become 1.00 JPY after a legitimate unused-currency precision0 -> 2 change.
	assertRejected(oldAmount, ErrCatalogConflict)
	// Model separately completed concurrent GETs: the catalog/global version is
	// new while /currencies still contains the old precision used by the author.
	standard := catalogFind(t, afterPrecision, "standard")
	unrelated := catalogTestEditInput(t, f, standard, afterPrecision.Revision, newRegistry.Revision, "Unrelated catalog metadata mutation")
	unrelated.Name = "A newer catalog snapshot"
	latest, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, standard.ID, unrelated)
	if err != nil {
		t.Fatal(err)
	}
	mixed := oldAmount
	mixed.Revision = latest.Revision
	if mixed.Revision <= oldView.Revision {
		t.Fatal("mixed-read fixture did not advance the catalog")
	}
	assertRejected(mixed, ErrCatalogConflict)
	for _, revision := range []int64{0, -1, MaxMinor + 1} {
		invalid := mixed
		invalid.CurrencyRevision = revision
		assertRejected(invalid, ErrCatalog)
	}
	// Re-reading and converting with the actual new precision is an explicit new
	// submission. The stored quote must be 100.00 JPY, not the stale minor100.
	valid := mixed
	valid.CurrencyRevision = newRegistry.Revision
	valid.Prices = []CatalogPrice{{Currency: "JPY", AmountMinor: 10000, Available: true}}
	created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, valid)
	if err != nil {
		t.Fatal(err)
	}
	if created.CurrencyRevision != newRegistry.Revision+1 || created.Plan.Prices[0].AmountMinor != 10000 {
		t.Fatal(created)
	}
	persisted, err := f.store.Currencies(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	jpy, _ := persisted.find("JPY")
	if !jpy.PrecisionLocked || jpy.DecimalPlaces != 2 || persisted.Revision != created.CurrencyRevision {
		t.Fatal("new precision was not atomically locked", jpy)
	}
	order, _, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "explicit-new-precision", "new-precision-key", "JPY")
	if err != nil || order.TotalMinor != 10000 || order.CurrencyFormat.DecimalPlaces != 2 {
		t.Fatal("explicit new precision was not preserved", order, err)
	}
}
