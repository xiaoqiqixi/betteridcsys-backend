package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func taxonomyChangeNode(input *TaxonomyUpdate, id string, mutate func(*CatalogNode)) {
	for n := range input.Nodes {
		if input.Nodes[n].ID == id {
			mutate(&input.Nodes[n])
			return
		}
	}
}
func taxonomyContainsNode(c TaxonomyConfiguration, id string) bool {
	for _, node := range c.Nodes {
		if node.ID == id {
			return true
		}
	}
	return false
}
func TestCatalogNodesMemory(t *testing.T) { runCatalogNodes(t, memoryCatalogFixture) }
func TestMySQLCatalogNodes(t *testing.T)  { runCatalogNodes(t, mysqlCatalogFixture) }
func runCatalogNodes(t *testing.T, fixture func(*testing.T) catalogFixture) {
	t.Run("default_nodes_are_real_bound_records", func(t *testing.T) {
		f := fixture(t)
		c := testReadTaxonomy(t, f)
		if len(c.Nodes) != len(c.Regions) {
			t.Fatal("migration did not create exactly one default node per region", c)
		}
		expected := map[string]string{}
		for _, r := range c.Regions {
			sum := sha256.Sum256([]byte(r.ID))
			expected[r.ID] = "n-" + hex.EncodeToString(sum[:])[:60]
		}
		for _, node := range c.Nodes {
			if node.ID != expected[node.RegionID] || node.Name != "默认节点" || !node.Enabled {
				t.Fatal("unexpected default node identity/label", node)
			}
		}
		for _, p := range readCatalog(t, f).Items {
			if p.NodeID != expected[p.RegionID] {
				t.Fatal("migrated plan is not bound to its region's real default node", p)
			}
		}
	})
	t.Run("node_validation_sorting_projection_and_no_alias", func(t *testing.T) {
		f := fixture(t)
		taxonomyAddFixture(t, f)
		input := taxonomyUpdateFrom(testReadTaxonomy(t, f))
		input.Nodes = append(input.Nodes, CatalogNode{ID: "compute-hkg-b", RegionID: "compute-hkg", Name: "香港 B", SortOrder: 5, Enabled: true})
		updated, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		for n, node := range updated.Nodes {
			if n > 0 {
				prev := updated.Nodes[n-1]
				if prev.SortOrder > node.SortOrder || (prev.SortOrder == node.SortOrder && prev.ID >= node.ID) {
					t.Fatal("node sorting not stable", updated.Nodes)
				}
			}
		}
		input = taxonomyUpdateFrom(updated)
		taxonomyChangeRegion(&input, "compute-hkg", func(v *CatalogRegion) { v.Enabled = false })
		disabled, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		public, err := f.store.(taxonomyTestStore).CatalogTaxonomy(t.Context(), false)
		if err != nil || taxonomyContainsNode(public, "compute-hkg-a") || taxonomyContainsNode(public, "compute-hkg-b") || !taxonomyContainsNode(public, "compute-nrt-a") || !taxonomyContainsNode(disabled, "compute-hkg-a") {
			t.Fatal("node projection ignores region ownership or loses admin data", public, disabled, err)
		}
		disabled.Nodes[0].Name = "returned alias"
		input.Nodes[0].Name = "input alias"
		if name := testReadTaxonomy(t, f).Nodes[0].Name; name == "returned alias" || name == "input alias" {
			t.Fatal("node slice aliases configuration")
		}
		before := testReadTaxonomy(t, f)
		catalogBefore := readCatalog(t, f)
		auditsBefore, _ := f.store.Audits(t.Context(), "")
		invalid := []struct {
			name   string
			change func(*TaxonomyUpdate)
		}{
			{"nil_nodes", func(v *TaxonomyUpdate) { v.Nodes = nil }},
			{"duplicate_node_id", func(v *TaxonomyUpdate) { v.Nodes = append(v.Nodes, v.Nodes[0]) }},
			{"empty_id", func(v *TaxonomyUpdate) { v.Nodes[0].ID = "" }},
			{"invalid_id", func(v *TaxonomyUpdate) { v.Nodes[0].ID = "bad/node" }},
			{"oversize_id", func(v *TaxonomyUpdate) { v.Nodes[0].ID = strings.Repeat("n", 65) }},
			{"missing_region", func(v *TaxonomyUpdate) { v.Nodes[0].RegionID = "missing-region" }},
			{"empty_name", func(v *TaxonomyUpdate) { v.Nodes[0].Name = " " }},
			{"untrimmed_name", func(v *TaxonomyUpdate) { v.Nodes[0].Name = " node " }},
			{"name_byte_limit", func(v *TaxonomyUpdate) { v.Nodes[0].Name = strings.Repeat("中", 41) }},
			{"name_control", func(v *TaxonomyUpdate) { v.Nodes[0].Name = "node\x00" }},
			{"invalid_utf8", func(v *TaxonomyUpdate) { v.Nodes[0].Name = string([]byte{0xff}) }},
			{"negative_sort", func(v *TaxonomyUpdate) { v.Nodes[0].SortOrder = -1 }},
			{"oversize_sort", func(v *TaxonomyUpdate) { v.Nodes[0].SortOrder = 1000001 }},
			{"too_many_nodes", func(v *TaxonomyUpdate) {
				for len(v.Nodes) <= 2048 {
					v.Nodes = append(v.Nodes, CatalogNode{ID: ID(), RegionID: "compute-hkg", Name: "节点", Enabled: true})
				}
			}},
		}
		for _, tc := range invalid {
			t.Run(tc.name, func(t *testing.T) {
				edit := taxonomyUpdateFrom(before)
				tc.change(&edit)
				if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, edit); !errors.Is(err, ErrTaxonomy) {
					t.Fatal("invalid node registry accepted", err)
				}
			})
		}
		auditsAfter, _ := f.store.Audits(t.Context(), "")
		if !reflect.DeepEqual(before, testReadTaxonomy(t, f)) || !reflect.DeepEqual(catalogBefore, readCatalog(t, f)) || !reflect.DeepEqual(auditsBefore, auditsAfter) {
			t.Fatal("invalid node updates had side effects")
		}
	})
	t.Run("four_level_plan_ownership_and_reference_protection", func(t *testing.T) {
		f := fixture(t)
		taxonomyAddFixture(t, f)
		for _, tc := range []struct{ name, nodeID string }{{"missing_node", ""}, {"unknown_node", "unknown"}, {"wrong_region_node", "compute-nrt-a"}, {"wrong_type_node", "metal-hkg-a"}} {
			t.Run(tc.name, func(t *testing.T) {
				input := taxonomyPlanInput(t, f, true)
				input.NodeID = tc.nodeID
				if _, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, input); !errors.Is(err, ErrTaxonomy) {
					t.Fatal("created plan with broken four-level relationship", err)
				}
				if _, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, "starter", input); !errors.Is(err, ErrTaxonomy) {
					t.Fatal("edited plan with broken four-level relationship", err)
				}
			})
		}
		created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, taxonomyPlanInput(t, f, true))
		if err != nil {
			t.Fatal(err)
		}
		if created.Plan.NodeID != "compute-hkg-a" {
			t.Fatal("node binding lost", created)
		}
		old, _, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "node-history", "node-history-key", "CNY")
		if err != nil {
			t.Fatal(err)
		}
		historyBefore, invoicesBefore, servicesBefore := catalogHistory(t, f)
		blocked := taxonomyUpdateFrom(testReadTaxonomy(t, f))
		taxonomyChangeNode(&blocked, created.Plan.NodeID, func(v *CatalogNode) { v.Enabled = false })
		if _, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, blocked); !errors.Is(err, ErrTaxonomyInUse) {
			t.Fatal("disabled node with published plan", err)
		}
		view := readCatalog(t, f)
		unpublish := catalogTestEditInput(t, f, catalogFind(t, view, created.Plan.ID), view.Revision, view.CurrencyRevision, "Unpublish before retiring node")
		unpublish.Available = false
		stopped, err := f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, unpublish)
		if err != nil {
			t.Fatal(err)
		}
		disabled, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, blocked)
		if err != nil {
			t.Fatal(err)
		}
		public, err := f.store.(taxonomyTestStore).CatalogTaxonomy(t.Context(), false)
		if err != nil || taxonomyContainsNode(public, created.Plan.NodeID) {
			t.Fatal("disabled node publicly selectable", public, err)
		}
		for _, operation := range []string{"remove", "reparent"} {
			input := taxonomyUpdateFrom(disabled)
			if operation == "remove" {
				input.Nodes = []CatalogNode{}
				for _, node := range disabled.Nodes {
					if node.ID != created.Plan.NodeID {
						input.Nodes = append(input.Nodes, node)
					}
				}
			} else {
				taxonomyChangeNode(&input, created.Plan.NodeID, func(v *CatalogNode) { v.RegionID = "compute-nrt" })
			}
			if _, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input); !errors.Is(err, ErrTaxonomyInUse) {
				t.Fatal("draft reference did not protect node", operation, err)
			}
		}
		draft := catalogTestEditInput(t, f, stopped.Plan, readCatalog(t, f).Revision, stopped.CurrencyRevision, "Retain retired node on draft")
		draft.Available = true
		if _, err = f.catalog.UpdateCatalogPlan(t.Context(), f.admin, created.Plan.ID, draft); !errors.Is(err, ErrTaxonomy) {
			t.Fatal("republished plan on disabled node", err)
		}
		prices, err := f.store.PlanPrices(t.Context(), "CNY")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.store.UpdatePlanPrices(t.Context(), f.admin, PlanPricesUpdate{Revision: prices.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: created.Plan.ID, Amount: "39.00", Available: true, PlanAvailable: true}}}); !errors.Is(err, ErrTaxonomy) {
			t.Fatal("old price API bypassed disabled node", err)
		}
		view = readCatalog(t, f)
		copied, err := f.catalog.DuplicateCatalogPlan(t.Context(), f.admin, created.Plan.ID, CatalogDuplicateInput{Revision: view.Revision, CurrencyRevision: view.CurrencyRevision, TaxonomyRevision: view.TaxonomyRevision, Reason: "Copy preserves retired node draft"})
		if err != nil || copied.Plan.Available || copied.Plan.NodeID != created.Plan.NodeID || copied.Plan.RegionID != created.Plan.RegionID || copied.Plan.TypeID != created.Plan.TypeID {
			t.Fatal("copy lost real four-level ownership", copied, err)
		}
		replay, ok, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "node-history", "node-history-key", "CNY")
		if err != nil || !ok || replay.ID != old.ID || replay.TotalMinor != old.TotalMinor || !reflect.DeepEqual(replay.PlanSnapshot, old.PlanSnapshot) {
			t.Fatal("retired node broke old idempotent snapshot", replay, ok, err)
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "new-node-host", "new-node-host-key", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("retired node accepted a new order", err)
		}
		historyAfter, invoicesAfter, servicesAfter := catalogHistory(t, f)
		if !reflect.DeepEqual(historyBefore, historyAfter) || !reflect.DeepEqual(invoicesBefore, invoicesAfter) || !reflect.DeepEqual(servicesBefore, servicesAfter) {
			t.Fatal("node retirement rewrote history")
		}
	})
	t.Run("stored_node_disabled_blocks_direct_plan_even_when_product_remains_published", func(t *testing.T) {
		f := fixture(t)
		taxonomyAddFixture(t, f)
		created, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, taxonomyPlanInput(t, f, true))
		if err != nil {
			t.Fatal(err)
		}
		old, _, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "direct-node-host", "direct-node-original", "CNY")
		if err != nil {
			t.Fatal(err)
		}
		// Model independently disabled persisted node state, not a product being
		// taken off sale. This proves runtime guards do not accidentally rely
		// only on plans.available or on the management write-path checks.
		switch store := f.store.(type) {
		case *Memory:
			store.mu.Lock()
			for n := range store.taxonomy.Nodes {
				if store.taxonomy.Nodes[n].ID == created.Plan.NodeID {
					store.taxonomy.Nodes[n].Enabled = false
				}
			}
			store.mu.Unlock()
		case *MySQL:
			migrationExec(t, t.Context(), store.DB, "UPDATE catalog_nodes SET enabled=FALSE WHERE id=?", created.Plan.NodeID)
		default:
			t.Fatal("unsupported fixture")
		}
		view := readCatalog(t, f)
		if !catalogFind(t, view, created.Plan.ID).Available {
			t.Fatal("test must retain a published product to isolate node enforcement")
		}
		if _, _, err = f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "direct-node-new", "direct-node-new-key", "CNY"); !errors.Is(err, ErrUnavailable) {
			t.Fatal("direct plan ID bypassed disabled node", err)
		}
		plans, err := f.store.Plans(t.Context(), "CNY")
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range plans {
			if p.ID == created.Plan.ID {
				t.Fatal("storefront exposed published product on disabled node", p)
			}
		}
		if _, err = f.store.UpdatePlanPrices(t.Context(), f.admin, PlanPricesUpdate{Revision: view.Revision, Currency: "CNY", Prices: []PlanPriceInput{{PlanID: created.Plan.ID, Amount: "39.00", Available: true, PlanAvailable: true}}}); !errors.Is(err, ErrTaxonomy) {
			t.Fatal("price API published into independently disabled node", err)
		}
		replayed, replay, err := f.store.CreateOrder(t.Context(), f.customer, created.Plan.ID, "direct-node-host", "direct-node-original", "CNY")
		if err != nil || !replay || replayed.ID != old.ID || !reflect.DeepEqual(replayed.PlanSnapshot, old.PlanSnapshot) || replayed.TotalMinor != old.TotalMinor {
			t.Fatal("disabled node blocked existing idempotent history", replayed, replay, err)
		}
	})
	t.Run("unreferenced_node_can_disable_enable_rename_delete", func(t *testing.T) {
		f := fixture(t)
		c := taxonomyAddFixture(t, f)
		input := taxonomyUpdateFrom(c)
		taxonomyChangeNode(&input, "compute-hkg-a", func(v *CatalogNode) { v.Enabled = false })
		c, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		input = taxonomyUpdateFrom(c)
		taxonomyChangeNode(&input, "compute-hkg-a", func(v *CatalogNode) { v.Enabled = true; v.Name = "香港 01"; v.SortOrder = 1000000 })
		c, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil {
			t.Fatal(err)
		}
		input = taxonomyUpdateFrom(c)
		input.Nodes = []CatalogNode{}
		for _, node := range c.Nodes {
			if node.ID != "compute-hkg-a" {
				input.Nodes = append(input.Nodes, node)
			}
		}
		c, err = f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(t.Context(), f.admin, input)
		if err != nil || taxonomyContainsNode(c, "compute-hkg-a") {
			t.Fatal("unreferenced node cannot be deleted", c, err)
		}
	})
}

func TestCatalogNodeCancelledContext(t *testing.T) {
	f := memoryCatalogFixture(t)
	before := testReadTaxonomy(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.store.(taxonomyTestStore).UpdateCatalogTaxonomy(ctx, f.admin, taxonomyUpdateFrom(before)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, testReadTaxonomy(t, f)) {
		t.Fatal("cancelled node update committed")
	}
}
