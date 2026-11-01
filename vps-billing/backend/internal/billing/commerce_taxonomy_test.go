package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestMySQLCommerceTaxonomyContractVisibilityAndLegacyCompatibility(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	product, err := m.SaveProduct06(t.Context(), f.admin, "", product06Fixture(t, f), ID(), false)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6"}).Handler())
	defer server.Close()
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	guest := client(t, server.URL, "", "")
	status, body := guest.request("GET", "/api/v1/catalog-taxonomy", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/catalog-taxonomy", body)
	status, body = admin.request("GET", "/api/v1/admin/catalog-taxonomy", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/admin/catalog-taxonomy", body)
	raw, _ := json.Marshal(body["data"])
	var tax api.CatalogTaxonomy
	if err = json.Unmarshal(raw, &tax); err != nil {
		t.Fatal(err)
	}
	for i := range tax.Types {
		tax.Types[i].Name = api.LocalizedText{"en": "Compute", "zh-CN": "计算资源"}
		tax.Types[i].SortOrder = -9007199254740991
	}
	for i := range tax.Regions {
		tax.Regions[i].Country = "SG"
		tax.Regions[i].City = "Singapore"
	}
	for i := range tax.Nodes {
		tax.Nodes[i].NetworkLabel = "Global transit"
		tax.Nodes[i].TestIP = "203.0.113.10"
		if tax.Nodes[i].ID == product.NodeID {
			tax.Nodes[i].Visible = false
		}
	}
	for _, method := range []string{"GET", "POST"} {
		status, body = customer.request(method, "/api/v1/admin/catalog-taxonomy", tax, "")
		if status != 403 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	status, body = admin.request("POST", "/api/v1/admin/catalog-taxonomy", tax, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/catalog-taxonomy", body)
	status, body = admin.request("POST", "/api/v1/admin/catalog-taxonomy", tax, "")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	status, body = guest.request("GET", "/api/v1/catalog-taxonomy", nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/catalog-taxonomy", body)
	for _, raw := range body["data"].(map[string]any)["nodes"].([]any) {
		if raw.(map[string]any)["id"] == product.NodeID {
			t.Fatal("hidden node was public")
		}
	}
	current, err := m.Taxonomy06(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if current.Types[0].Name["zh-CN"] != "计算资源" || current.Types[0].SortOrder != -9007199254740991 {
		t.Fatal(current)
	}
	legacy, err := m.CatalogTaxonomy(t.Context(), true)
	if err != nil || legacy.Types[0].Name != "Compute" || legacy.Revision != current.Revision {
		t.Fatal(legacy, err)
	}
	filtered := []api.CatalogNode{}
	for _, n := range current.Nodes {
		if n.ID != product.NodeID {
			filtered = append(filtered, n)
		}
	}
	current.Nodes = filtered
	status, body = admin.request("POST", "/api/v1/admin/catalog-taxonomy", current, "")
	if status != 409 {
		t.Fatal("deleted referenced node", status, body)
	}
	contracttest.Error(t, body)
}
