package billing

import (
	"net/http/httptest"
	"testing"
)

func catalogVisibility(t *testing.T, f catalogFixture) {
	t.Helper()
	server := httptest.NewServer(NewApp(f.store, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	c := client(t, server.URL, "", "")
	read := func(id, currency string) (map[string]any, bool) {
		t.Helper()
		status, out := c.request("GET", "/api/v1/plans?currency="+currency, nil, "")
		if status != 200 {
			t.Fatal(status, out)
		}
		for _, p := range out["data"].([]any) {
			item := p.(map[string]any)
			if item["id"] == id {
				return item, true
			}
		}
		return nil, false
	}
	current := readCatalog(t, f)
	input := catalogTestInput(t, f, current.Revision)
	input.Available = false
	created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := read(created.Plan.ID, "CNY"); ok {
		t.Fatal("draft was publicly listed")
	}
	if catalogFind(t, readCatalog(t, f), created.Plan.ID).ID != created.Plan.ID {
		t.Fatal("draft disappeared from administrator catalog")
	}
	input.Available = true
	input.Revision = created.Revision
	published, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	item, ok := read(created.Plan.ID, "CNY")
	if !ok || item["available"] != true || item["price_minor"] != float64(3900) {
		t.Fatal("publication did not expose valid quote", item)
	}
	configureTestCurrency(t, f.store, f.admin, "JPY", 0, "")
	item, ok = read(created.Plan.ID, "JPY")
	if !ok || item["available"] != false || item["price_minor"] != float64(0) {
		t.Fatal("published missing currency quote was hidden or invented", item)
	}
	copied, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, created.Plan.ID, CatalogDuplicateInput{TaxonomyRevision: testReadTaxonomy(t, f).Revision, Revision: readCatalog(t, f).Revision, CurrencyRevision: readCatalog(t, f).CurrencyRevision, Reason: "Copy must remain private until published"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok = read(copied.Plan.ID, "CNY"); ok {
		t.Fatal("duplicate draft leaked to storefront")
	}
	input.Available = false
	input.Revision = copied.Revision
	input.CurrencyRevision = copied.CurrencyRevision
	if _, err = f.catalog.UpdateCatalogPlan(t.Context(), f.admin, published.Plan.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, ok = read(created.Plan.ID, "CNY"); ok {
		t.Fatal("unpublished product remained public")
	}
}
func TestCatalogPublicationVisibility(t *testing.T) { catalogVisibility(t, memoryCatalogFixture(t)) }
func TestMySQLCatalogPublicationVisibility(t *testing.T) {
	catalogVisibility(t, mysqlCatalogFixture(t))
}
