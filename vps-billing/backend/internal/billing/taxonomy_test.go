package billing

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type taxonomyTestStore interface {
	CatalogTaxonomy(context.Context, bool) (TaxonomyConfiguration, error)
	UpdateCatalogTaxonomy(context.Context, User, TaxonomyUpdate) (TaxonomyConfiguration, error)
}

func testReadTaxonomy(t *testing.T, f catalogFixture) TaxonomyConfiguration {
	t.Helper()
	out, err := f.store.(taxonomyTestStore).CatalogTaxonomy(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func taxonomyUpdateFrom(v TaxonomyConfiguration) TaxonomyUpdate {
	return TaxonomyUpdate{Revision: v.Revision, Reason: "Reviewed server catalog classification", Types: append([]CatalogType{}, v.Types...), Regions: append([]CatalogRegion{}, v.Regions...), Nodes: append([]CatalogNode{}, v.Nodes...)}
}
func catalogTestEditInput(t *testing.T, f catalogFixture, p CatalogPlan, revision, currencyRevision int64, reason string) CatalogInput {
	t.Helper()
	out := catalogInputFrom(p, revision, currencyRevision, reason)
	out.TaxonomyRevision = testReadTaxonomy(t, f).Revision
	return out
}
func taxonomyAddFixture(t *testing.T, f catalogFixture) TaxonomyConfiguration {
	t.Helper()
	input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
	input.Types = append(input.Types, CatalogType{ID: "compute", Name: "云服务器", SortOrder: 20, Enabled: true}, CatalogType{ID: "baremetal", Name: "独立服务器", SortOrder: 10, Enabled: true})
	input.Regions = append(input.Regions, CatalogRegion{ID: "compute-hkg", TypeID: "compute", Name: "香港", SortOrder: 20, Enabled: true}, CatalogRegion{ID: "compute-nrt", TypeID: "compute", Name: "东京", SortOrder: 10, Enabled: true}, CatalogRegion{ID: "metal-hkg", TypeID: "baremetal", Name: "香港", SortOrder: 10, Enabled: true})
	input.Nodes = append(input.Nodes,
		CatalogNode{ID: "compute-hkg-a", RegionID: "compute-hkg", Name: "香港 A", SortOrder: 20, Enabled: true},
		CatalogNode{ID: "compute-nrt-a", RegionID: "compute-nrt", Name: "东京 A", SortOrder: 10, Enabled: true},
		CatalogNode{ID: "metal-hkg-a", RegionID: "metal-hkg", Name: "独服 A", SortOrder: 10, Enabled: true})
	out, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func taxonomyChangeType(input *TaxonomyUpdate, id string, mutate func(*CatalogType)) {
	for n := range input.Types {
		if input.Types[n].ID == id {
			mutate(&input.Types[n])
			return
		}
	}
}
func taxonomyChangeRegion(input *TaxonomyUpdate, id string, mutate func(*CatalogRegion)) {
	for n := range input.Regions {
		if input.Regions[n].ID == id {
			mutate(&input.Regions[n])
			return
		}
	}
}
func taxonomyPlanInput(t *testing.T, f catalogFixture, available bool) CatalogInput {
	t.Helper()
	input := catalogTestInput(t, f, readCatalog(t, f).Revision)
	input.TypeID, input.RegionID, input.NodeID, input.Region, input.Available = "compute", "compute-hkg", "compute-hkg-a", "香港", available
	return input
}
func taxonomyContainsRegion(c TaxonomyConfiguration, id string) bool {
	for _, region := range c.Regions {
		if region.ID == id {
			return true
		}
	}
	return false
}
func taxonomyContainsType(c TaxonomyConfiguration, id string) bool {
	for _, typ := range c.Types {
		if typ.ID == id {
			return true
		}
	}
	return false
}
func TestCatalogTaxonomyMemory(t *testing.T) { runTaxonomySuite(t, memoryCatalogFixture) }
func TestMySQLCatalogTaxonomy(t *testing.T)  { runTaxonomySuite(t, mysqlCatalogFixture) }
func runTaxonomySuite(t *testing.T, fixture func(*testing.T) catalogFixture) {
	t.Run("real_ids_ordering_visibility_and_copy_isolation", func(t *testing.T) {
		f := fixture(t)
		before := testReadTaxonomy(t, f)
		if before.Revision != 1 || !taxonomyContainsType(before, "generic") || len(before.Regions) == 0 {
			t.Fatal("missing migrated classification", before)
		}
		updated := taxonomyAddFixture(t, f)
		if updated.Revision != before.Revision+1 {
			t.Fatal("revision did not advance exactly once", updated)
		}
		for n, typ := range updated.Types {
			if n > 0 {
				prev := updated.Types[n-1]
				if prev.SortOrder > typ.SortOrder || (prev.SortOrder == typ.SortOrder && prev.ID >= typ.ID) {
					t.Fatal("type order is unstable", updated.Types)
				}
			}
		}
		// Regions are globally ordered by sort_order, then ID; ownership is explicit.
		for n, region := range updated.Regions {
			if n > 0 {
				prev := updated.Regions[n-1]
				if prev.SortOrder > region.SortOrder || (prev.SortOrder == region.SortOrder && prev.ID >= region.ID) {
					t.Fatal("region order is unstable", updated.Regions)
				}
			}
		}
		input := taxonomyUpdateFrom(updated)
		taxonomyChangeType(&input, "compute", func(v *CatalogType) { v.Enabled = false })
		disabled, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		public, err := f.store.(taxonomyTestStore).CatalogTaxonomy(t.Context(), false)
		if err != nil || taxonomyContainsType(public, "compute") || taxonomyContainsRegion(public, "compute-hkg") || taxonomyContainsRegion(public, "compute-nrt") {
			t.Fatal("disabled ancestor leaked to storefront", public, err)
		}
		if !taxonomyContainsType(disabled, "compute") || !taxonomyContainsRegion(disabled, "compute-hkg") {
			t.Fatal("disabled entries disappeared from admin registry")
		}
		// Returned and submitted slices must not alias the stored configuration.
		disabled.Types[0].Name = "returned mutation"
		input.Types[0].Name = "input mutation"
		again := testReadTaxonomy(t, f)
		if again.Types[0].Name == "returned mutation" || again.Types[0].Name == "input mutation" {
			t.Fatal("taxonomy aliases its API arguments")
		}
		audits, err := f.store.Audits(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, audit := range audits {
			if audit.Action == "catalog.taxonomy_updated" {
				count++
				if audit.ActorEmail != f.admin.Email || !strings.Contains(audit.Detail, "Reviewed server catalog classification") {
					t.Fatal("incomplete taxonomy audit", audit)
				}
			}
		}
		if count != 2 {
			t.Fatal("taxonomy write not audited once", count)
		}
	})
	t.Run("unused_entries_can_be_disabled_and_deleted", func(t *testing.T) {
		f := fixture(t)
		before := taxonomyAddFixture(t, f)
		input := taxonomyUpdateFrom(before)
		taxonomyChangeRegion(&input, "compute-nrt", func(v *CatalogRegion) { v.Enabled = false })
		disabled, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		public, err := f.store.(taxonomyTestStore).CatalogTaxonomy(t.Context(), false)
		if err != nil || taxonomyContainsRegion(public, "compute-nrt") || !taxonomyContainsRegion(public, "compute-hkg") {
			t.Fatal("disabled region was not isolated", public, err)
		}
		input = taxonomyUpdateFrom(disabled)
		input.Types = []CatalogType{}
		input.Regions = []CatalogRegion{}
		input.Nodes = []CatalogNode{}
		for _, node := range disabled.Nodes {
			if node.RegionID != "compute-hkg" && node.RegionID != "compute-nrt" {
				input.Nodes = append(input.Nodes, node)
			}
		}
		for _, typ := range disabled.Types {
			if typ.ID != "compute" {
				input.Types = append(input.Types, typ)
			}
		}
		for _, region := range disabled.Regions {
			if region.TypeID != "compute" {
				input.Regions = append(input.Regions, region)
			}
		}
		deleted, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil || taxonomyContainsType(deleted, "compute") || taxonomyContainsRegion(deleted, "compute-hkg") || taxonomyContainsRegion(deleted, "compute-nrt") {
			t.Fatal("unused classification could not be deleted", deleted, err)
		}
	})
	t.Run("rename_synchronizes_current_labels_not_historical_snapshots", func(t *testing.T) {
		f := fixture(t)
		taxonomyAddFixture(t, f)
		created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, taxonomyPlanInput(t, f, true))
		if err != nil {
			t.Fatal(err)
		}
		old, _, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "before-rename", "taxonomy-before-rename", "CNY")
		if err != nil {
			t.Fatal(err)
		}
		historyBefore, invoicesBefore, servicesBefore := catalogHistory(t, f)
		input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
		taxonomyChangeRegion(&input, "compute-hkg", func(v *CatalogRegion) { v.Name = "香港二区" })
		renamed, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		view := readCatalog(t, f)
		current := catalogFind(t, view, created.Plan.ID)
		if current.Region != "香港二区" || current.RegionID != created.Plan.RegionID || current.TypeID != created.Plan.TypeID || view.Revision != created.Revision+1 || view.TaxonomyRevision != renamed.Revision || !reflect.DeepEqual(current.Prices, created.Plan.Prices) {
			t.Fatal("rename lost current binding or changed quotation", current, view)
		}
		plans, err := f.store.Plans(t.Context(), "CNY")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range plans {
			if p.ID == current.ID {
				found = true
				if p.TypeID != current.TypeID || p.RegionID != current.RegionID || p.Region != current.Region || p.TaxonomyRevision != renamed.Revision {
					t.Fatal("public plan and taxonomy revisions/bindings disagree", p)
				}
			}
		}
		if !found {
			t.Fatal("renamed published plan disappeared")
		}
		quoted := f.store.(interface {
			CreateQuotedOrder(context.Context, User, string, string, string, string, *PriceQuote) (Order, bool, error)
		})
		if _, _, err = quoted.CreateQuotedOrder(t.Context(), f.customer, current.ID, "stale-label", "stale-taxonomy-label", "CNY", &PriceQuote{PriceMinor: 3900, DecimalPlaces: 2, PriceRevision: created.Revision}); !errors.Is(err, ErrPriceChanged) {
			t.Fatal("rename accepted a stale configuration quote", err)
		}
		historyAfter, invoicesAfter, servicesAfter := catalogHistory(t, f)
		if !reflect.DeepEqual(historyBefore, historyAfter) || !reflect.DeepEqual(invoicesBefore, invoicesAfter) || !reflect.DeepEqual(servicesBefore, servicesAfter) {
			t.Fatal("current label rename rewrote old snapshots")
		}
		fresh, _, err := f.store.CreateOrder(t.Context(), f.customer, current.ID, "after-rename", "taxonomy-after-rename", "CNY")
		if err != nil || fresh.TotalMinor != old.TotalMinor || fresh.PlanSnapshot.Region != "香港二区" || old.PlanSnapshot.Region != "香港" {
			t.Fatal("new configuration label not snapshotted independently", fresh, old, err)
		}
	})
	t.Run("strict_registry_validation_no_side_effects", func(t *testing.T) {
		f := fixture(t)
		before := taxonomyAddFixture(t, f)
		catalogBefore := readCatalog(t, f)
		currenciesBefore, _ := f.store.Currencies(t.Context(), true)
		auditsBefore, _ := f.store.Audits(t.Context(), "")
		cases := []struct {
			name   string
			change func(*TaxonomyUpdate)
		}{
			{"zero_revision", func(v *TaxonomyUpdate) { v.Revision = 0 }},
			{"negative_revision", func(v *TaxonomyUpdate) { v.Revision = -1 }},
			{"unsafe_revision", func(v *TaxonomyUpdate) { v.Revision = MaxMinor + 1 }},
			{"empty_reason", func(v *TaxonomyUpdate) { v.Reason = " \n " }},
			{"long_reason", func(v *TaxonomyUpdate) { v.Reason = strings.Repeat("r", 501) }},
			{"nil_types", func(v *TaxonomyUpdate) { v.Types = nil }},
			{"nil_regions", func(v *TaxonomyUpdate) { v.Regions = nil }},
			{"too_many_types", func(v *TaxonomyUpdate) {
				for len(v.Types) <= 128 {
					v.Types = append(v.Types, CatalogType{ID: "limit-" + ID(), Name: "类型", Enabled: true})
				}
			}},
			{"too_many_regions", func(v *TaxonomyUpdate) {
				for len(v.Regions) <= 512 {
					v.Regions = append(v.Regions, CatalogRegion{ID: "limit-" + ID(), TypeID: "generic", Name: "地区", Enabled: true})
				}
			}},
			{"duplicate_type_id", func(v *TaxonomyUpdate) { v.Types = append(v.Types, v.Types[0]) }},
			{"duplicate_region_id", func(v *TaxonomyUpdate) { v.Regions = append(v.Regions, v.Regions[0]) }},
			{"empty_type_id", func(v *TaxonomyUpdate) { v.Types[0].ID = "" }},
			{"invalid_type_id", func(v *TaxonomyUpdate) { v.Types[0].ID = "bad/type" }},
			{"oversize_type_id", func(v *TaxonomyUpdate) { v.Types[0].ID = strings.Repeat("a", 65) }},
			{"invalid_region_id", func(v *TaxonomyUpdate) { v.Regions[0].ID = ".." }},
			{"invalid_parent_id", func(v *TaxonomyUpdate) { v.Regions[0].TypeID = "missing" }},
			{"empty_type_name", func(v *TaxonomyUpdate) { v.Types[0].Name = " " }},
			{"empty_region_name", func(v *TaxonomyUpdate) { v.Regions[0].Name = " " }},
			{"type_name_byte_limit", func(v *TaxonomyUpdate) { v.Types[0].Name = strings.Repeat("中", 41) }},
			{"region_name_byte_limit", func(v *TaxonomyUpdate) { v.Regions[0].Name = strings.Repeat("中", 121) }},
			{"invalid_utf8", func(v *TaxonomyUpdate) { v.Types[0].Name = string([]byte{0xff}) }},
			{"control", func(v *TaxonomyUpdate) { v.Regions[0].Name = "region\x00" }},
			{"negative_type_order", func(v *TaxonomyUpdate) { v.Types[0].SortOrder = -1 }},
			{"oversize_region_order", func(v *TaxonomyUpdate) { v.Regions[0].SortOrder = 1000001 }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				input := taxonomyUpdateFrom(before)
				tc.change(&input)
				if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input); !errors.Is(err, ErrTaxonomy) {
					t.Fatal("invalid registry accepted", err)
				}
			})
		}
		stale := taxonomyUpdateFrom(before)
		stale.Revision++
		if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, stale); !errors.Is(err, ErrTaxonomyConflict) {
			t.Fatal("stale registry accepted", err)
		}
		after := testReadTaxonomy(t, f)
		currenciesAfter, _ := f.store.Currencies(t.Context(), true)
		auditsAfter, _ := f.store.Audits(t.Context(), "")
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(catalogBefore, readCatalog(t, f)) || !reflect.DeepEqual(currenciesBefore, currenciesAfter) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
			t.Fatal("rejected taxonomy change mutated business data")
		}
	})
	t.Run("catalog_ownership_publication_references_and_history", func(t *testing.T) {
		f := fixture(t)
		taxonomyAddFixture(t, f)
		before := testReadTaxonomy(t, f)
		for _, tc := range []struct {
			name   string
			change func(*CatalogInput)
			want   error
		}{
			{"missing_type", func(v *CatalogInput) { v.TypeID = "" }, ErrTaxonomy},
			{"missing_region", func(v *CatalogInput) { v.RegionID = "" }, ErrTaxonomy},
			{"unknown_type", func(v *CatalogInput) { v.TypeID = "no-such-type" }, ErrTaxonomy},
			{"unknown_region", func(v *CatalogInput) { v.RegionID = "no-such-region" }, ErrTaxonomy},
			{"cross_type_region", func(v *CatalogInput) { v.TypeID = "baremetal" }, ErrTaxonomy},
			{"forged_region_name", func(v *CatalogInput) { v.Region = "东京" }, ErrTaxonomy},
			{"zero_taxonomy_revision", func(v *CatalogInput) { v.TaxonomyRevision = 0 }, ErrTaxonomy},
			{"stale_taxonomy_revision", func(v *CatalogInput) { v.TaxonomyRevision = before.Revision - 1 }, ErrTaxonomyConflict},
		} {
			t.Run(tc.name, func(t *testing.T) {
				input := taxonomyPlanInput(t, f, true)
				tc.change(&input)
				if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, input); !errors.Is(err, tc.want) {
					t.Fatal("invalid plan classification accepted", err)
				}
				if _, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, "starter", input); !errors.Is(err, tc.want) {
					t.Fatal("invalid edited plan classification accepted", err)
				}
				if strings.HasSuffix(tc.name, "taxonomy_revision") {
					_, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, "starter", CatalogDuplicateInput{Revision: input.Revision, CurrencyRevision: input.CurrencyRevision, TaxonomyRevision: input.TaxonomyRevision, Reason: input.Reason})
					if !errors.Is(err, tc.want) {
						t.Fatal("invalid taxonomy revision accepted while duplicating", err)
					}
				}
			})
		}
		created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, taxonomyPlanInput(t, f, true))
		if err != nil || created.Plan.TypeID != "compute" || created.Plan.RegionID != "compute-hkg" || created.TaxonomyRevision != before.Revision {
			t.Fatal(created, err)
		}
		order, _, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "history-host", "taxonomy-history", "CNY")
		if err != nil {
			t.Fatal(err)
		}
		ordersBefore, invoicesBefore, servicesBefore := catalogHistory(t, f)
		copy, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, created.Plan.ID, CatalogDuplicateInput{Revision: created.Revision, CurrencyRevision: created.CurrencyRevision, TaxonomyRevision: created.TaxonomyRevision, Reason: "Copy preserves real classification"})
		if err != nil || copy.Plan.Available || copy.Plan.TypeID != created.Plan.TypeID || copy.Plan.RegionID != created.Plan.RegionID || !reflect.DeepEqual(copy.Plan.Prices, created.Plan.Prices) {
			t.Fatal("copy lost classification or changed quotations", copy, err)
		}
		for _, target := range []string{"type", "region"} {
			input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
			if target == "type" {
				taxonomyChangeType(&input, "compute", func(v *CatalogType) { v.Enabled = false })
			} else {
				taxonomyChangeRegion(&input, "compute-hkg", func(v *CatalogRegion) { v.Enabled = false })
			}
			if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input); !errors.Is(err, ErrTaxonomyInUse) {
				t.Fatal("disabled a classification with published products", target, err)
			}
		}
		unpublish := catalogTestEditInput(t, f, created.Plan, copy.Revision, copy.CurrencyRevision, "Take product off sale before disabling classification")
		unpublish.Available = false
		stopped, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, unpublish)
		if err != nil {
			t.Fatal(err)
		}
		input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
		taxonomyChangeRegion(&input, "compute-hkg", func(v *CatalogRegion) { v.Enabled = false })
		disabled, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		// Draft references still protect deletion and moving a region to another type.
		moved := taxonomyUpdateFrom(disabled)
		taxonomyChangeRegion(&moved, "compute-hkg", func(v *CatalogRegion) { v.TypeID = "baremetal" })
		if _, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, moved); !errors.Is(err, ErrTaxonomyInUse) {
			t.Fatal("referenced region was reparented", err)
		}
		removed := taxonomyUpdateFrom(disabled)
		removed.Regions = nil
		removed.Nodes = []CatalogNode{}
		for _, node := range disabled.Nodes {
			if node.RegionID != "compute-hkg" {
				removed.Nodes = append(removed.Nodes, node)
			}
		}
		for _, r := range disabled.Regions {
			if r.ID != "compute-hkg" {
				removed.Regions = append(removed.Regions, r)
			}
		}
		if _, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, removed); !errors.Is(err, ErrTaxonomyInUse) {
			t.Fatal("draft reference failed to protect deleted region", err)
		}
		republish := catalogTestEditInput(t, f, stopped.Plan, readCatalog(t, f).Revision, stopped.CurrencyRevision, "Cannot publish into disabled region")
		republish.Available = true
		if _, err = f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, republish); !errors.Is(err, ErrTaxonomy) {
			t.Fatal("published into disabled classification", err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "new-host", "new-after-disable", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("off-sale taxonomy allowed new order", err)
		}
		replay, replayed, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "history-host", "taxonomy-history", "CNY")
		if err != nil || !replayed || replay.ID != order.ID || replay.TotalMinor != order.TotalMinor || !reflect.DeepEqual(replay.PlanSnapshot, order.PlanSnapshot) {
			t.Fatal("taxonomy broke original idempotent snapshot", replay, replayed, err)
		}
		ordersAfter, invoicesAfter, servicesAfter := catalogHistory(t, f)
		if !reflect.DeepEqual(ordersBefore, ordersAfter) || !reflect.DeepEqual(invoicesBefore, invoicesAfter) || !reflect.DeepEqual(servicesBefore, servicesAfter) {
			t.Fatal("taxonomy rewrote historical records")
		}
	})
	t.Run("price_editor_cannot_republish_retired_classification", func(t *testing.T) {
		for _, target := range []string{"type", "region"} {
			t.Run(target, func(t *testing.T) {
				f := fixture(t)
				taxonomyAddFixture(t, f)
				created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, taxonomyPlanInput(t, f, false))
				if err != nil {
					t.Fatal(err)
				}
				input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
				if target == "type" {
					taxonomyChangeType(&input, "compute", func(v *CatalogType) { v.Enabled = false })
				} else {
					taxonomyChangeRegion(&input, "compute-hkg", func(v *CatalogRegion) { v.Enabled = false })
				}
				if _, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input); err != nil {
					t.Fatal(err)
				}
				before := readCatalog(t, f)
				currenciesBefore, _ := f.store.Currencies(t.Context(), true)
				auditsBefore, _ := f.store.Audits(t.Context(), "")
				prices, err := f.store.PlanPrices(t.Context(), "CNY")
				if err != nil {
					t.Fatal(err)
				}
				// The valid first edit must also roll back when a later row tries
				// to bypass retirement using the older price-management endpoint.
				update := PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: "starter", Amount: "999.99", Available: true, PlanAvailable: true}, {PlanID: created.Plan.ID, Amount: "39.00", Available: true, PlanAvailable: true}}}
				if _, err = f.store.UpdatePlanPrices(t.Context(), f.admin, update); !errors.Is(err, ErrTaxonomy) {
					t.Fatal("price editor republished retired classification", err)
				}
				currenciesAfter, _ := f.store.Currencies(t.Context(), true)
				auditsAfter, _ := f.store.Audits(t.Context(), "")
				if !reflect.DeepEqual(before, readCatalog(t, f)) || !reflect.DeepEqual(currenciesBefore, currenciesAfter) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
					t.Fatal("rejected price batch partially mutated catalog")
				}
				update.Prices = []PlanPriceInput{{PlanID: created.Plan.ID, Amount: "40.00", Available: true, PlanAvailable: false}}
				if _, err = f.store.UpdatePlanPrices(t.Context(), f.admin, update); err != nil {
					t.Fatal("retired draft cannot retain an independent editable price", err)
				}
				if _, _, err = f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "retired", "price-edit-retired", "CNY"); !errors.Is(err, ErrUnavailable) {
					t.Fatal("editing draft price made it purchasable", err)
				}
			})
		}
	})
	t.Run("persisted_admin_authority_and_concurrent_revision", func(t *testing.T) {
		f := fixture(t)
		before := testReadTaxonomy(t, f)
		input := taxonomyUpdateFrom(before)
		for _, actor := range []User{{ID: ID(), Role: "admin"}, {ID: f.customer.ID, Role: "admin"}} {
			if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), actor, input); !errors.Is(err, ErrAdminRequired) {
				t.Fatal("unpersisted administrator accepted", err)
			}
		}
		disabled := f.admin
		disabled.Disabled = true
		f.changeActor(disabled)
		if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input); !errors.Is(err, ErrAdminRequired) {
			t.Fatal("stale disabled administrator accepted", err)
		}
		f.changeActor(f.admin)
		input.Reason = "Concurrent taxonomy review"
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
				results <- err
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		success, conflict := 0, 0
		for err := range results {
			if err == nil {
				success++
			} else if errors.Is(err, ErrTaxonomyConflict) {
				conflict++
			} else {
				t.Fatal(err)
			}
		}
		if success != 1 || conflict != 1 || testReadTaxonomy(t, f).Revision != before.Revision+1 {
			t.Fatal("optimistic taxonomy write not atomic", success, conflict)
		}
	})
}

type taxonomyHTTPStoreSpy struct {
	Store
	taxonomy taxonomyTestStore
	calls    atomic.Int64
}

func (s *taxonomyHTTPStoreSpy) CatalogTaxonomy(ctx context.Context, admin bool) (TaxonomyConfiguration, error) {
	s.calls.Add(1)
	return s.taxonomy.CatalogTaxonomy(ctx, admin)
}
func (s *taxonomyHTTPStoreSpy) UpdateCatalogTaxonomy(ctx context.Context, u User, input TaxonomyUpdate) (TaxonomyConfiguration, error) {
	s.calls.Add(1)
	return s.taxonomy.UpdateCatalogTaxonomy(ctx, u, input)
}
func TestCatalogTaxonomyHTTP(t *testing.T) {
	runTaxonomyHTTP(t, memoryCatalogFixture(t), "DemoAdmin!2026", "DemoCustomer!2026")
}
func TestMySQLCatalogTaxonomyHTTP(t *testing.T) {
	runTaxonomyHTTP(t, mysqlCatalogFixture(t), "CatalogFixture!2026", "CatalogFixture!2026")
}
func runTaxonomyHTTP(t *testing.T, f catalogFixture, adminPassword, customerPassword string) {
	spy := &taxonomyHTTPStoreSpy{Store: f.store, taxonomy: f.store.(taxonomyTestStore)}
	server := httptest.NewServer(NewApp(spy, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	anonymous := client(t, server.URL, "", "")
	customer := client(t, server.URL, f.customer.Email, customerPassword)
	admin := client(t, server.URL, f.admin.Email, adminPassword)
	input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
	for _, entry := range []struct {
		client *testClient
		status int
	}{{anonymous, 401}, {customer, 403}} {
		for _, method := range []string{"GET", "POST"} {
			if status, body := entry.client.request(method, "/api/v1/admin/catalog-taxonomy", input, ""); status != entry.status {
				t.Fatal("admin taxonomy authorization", method, status, body)
			}
		}
	}
	if spy.calls.Load() != 0 {
		t.Fatal("unauthorized request reached taxonomy store")
	}
	saved := admin.csrf
	admin.csrf = "invalid"
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", input, ""); status != 403 {
		t.Fatal("missing CSRF rejection", status, body)
	}
	admin.csrf = saved
	if spy.calls.Load() != 0 {
		t.Fatal("CSRF denial reached taxonomy store")
	}
	raw, _ := json.Marshal(input)
	var missingNodes map[string]json.RawMessage
	_ = json.Unmarshal(raw, &missingNodes)
	delete(missingNodes, "nodes")
	withoutNodes, _ := json.Marshal(missingNodes)
	bad := []string{string(withoutNodes),
		strings.Replace(string(raw), `"revision":1,`, "", 1),
		strings.Replace(string(raw), `"revision":1`, `"revision":null`, 1),
		strings.Replace(string(raw), `"reason":`, `"Reason":`, 1),
		strings.Replace(string(raw), `"reason":`, `"reason":"duplicate","reason":`, 1),
		strings.Replace(string(raw), `,"enabled":true`, "", 1),
		strings.TrimSuffix(string(raw), "}") + `,"zones":[]}`,
		strings.Replace(string(raw), `"sort_order":0`, `"sort_order":0.5`, 1),
		string(raw) + `{}`,
	}
	for _, body := range bad {
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/admin/catalog-taxonomy", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("malformed taxonomy JSON accepted", res.StatusCode, body)
		}
	}
	if spy.calls.Load() != 0 {
		t.Fatal("malformed JSON reached taxonomy store")
	}
	req, _ := http.NewRequest("POST", server.URL+"/api/v1/admin/catalog-taxonomy", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("X-CSRF-Token", admin.csrf)
	res, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 415 {
		t.Fatal("non-JSON taxonomy accepted", res.StatusCode)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if status, _ := admin.request(method, "/api/v1/admin/catalog-taxonomy", input, ""); status != 405 {
			t.Fatal(method, status)
		}
	}
	if status, body := anonymous.request("GET", "/api/v1/catalog-taxonomy", nil, ""); status != 200 {
		t.Fatal("public classification requires login", status, body)
	}
	if status, body := admin.request("GET", "/api/v1/admin/catalog-taxonomy?role=admin", nil, ""); status != 400 {
		t.Fatal("unknown query accepted", status, body)
	}
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", input, ""); status != 200 {
		t.Fatal(status, body)
	}
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", input, ""); status != 409 || body["error"].(map[string]any)["code"] != "TAXONOMY_CONFLICT" {
		t.Fatal("stale taxonomy HTTP response", status, body)
	}
	view := testReadTaxonomy(t, f)
	invalid := taxonomyUpdateFrom(view)
	invalid.Reason = " "
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", invalid, ""); status != 400 || body["error"].(map[string]any)["code"] != "INVALID_TAXONOMY" {
		t.Fatal("invalid taxonomy HTTP response", status, body)
	}
	published, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, readCatalog(t, f).Revision))
	if err != nil {
		t.Fatal(err)
	}
	inUse := taxonomyUpdateFrom(view)
	taxonomyChangeRegion(&inUse, published.Plan.RegionID, func(v *CatalogRegion) { v.Enabled = false })
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", inUse, ""); status != 409 || body["error"].(map[string]any)["code"] != "TAXONOMY_IN_USE" {
		t.Fatal("referenced taxonomy HTTP response", status, body)
	}
	downgraded := f.admin
	downgraded.Role = "customer"
	f.changeActor(downgraded)
	calls := spy.calls.Load()
	if status, body := admin.request("POST", "/api/v1/admin/catalog-taxonomy", taxonomyUpdateFrom(view), ""); status != 403 {
		t.Fatal("stale administrator cookie accepted", status, body)
	}
	if spy.calls.Load() != calls {
		t.Fatal("revoked role reached taxonomy store")
	}
}
