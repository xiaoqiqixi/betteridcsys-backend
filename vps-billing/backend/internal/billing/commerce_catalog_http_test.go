package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
)

func product06Fixture(t *testing.T, f catalogFixture) api.AdminCreateProductInput {
	t.Helper()
	tax := testReadTaxonomy(t, f)
	return api.AdminCreateProductInput{Slug: "test-product", Kind: "generic", NodeID: tax.Nodes[0].ID, Name: api.LocalizedText{"en": "Test product", "zh-CN": "测试商品"}, Description: api.LocalizedText{"en": "Independent cycle pricing"}, Badges: []string{}, Specs: api.ProductSpecs{CPUCores: 2, MemoryMB: 2048, DiskGB: 40, IPv4: 1}, Prices: []api.PricePoint{{Cycle: "monthly", Currency: "CNY", RecurringMinor: 999, SetupMinor: 100, DecimalPlaces: 2}, {Cycle: "annually", Currency: "CNY", RecurringMinor: 9999, DecimalPlaces: 2}}, Options: []api.ProductOption{}, OSImageIDs: []string{}, Visibility: "public"}
}
func decodeProduct06(t *testing.T, v any) api.AdminProduct {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out api.AdminProduct
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMySQLCommerceCatalogHandlerContractAndPermissions(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	admin := client(t, srv.URL, f.admin.Email, "CatalogFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "CatalogFixture!2026")
	guest := client(t, srv.URL, "", "")
	input := product06Fixture(t, f)
	status, body := admin.request("POST", "/api/v1/admin/products", input, "catalog-create-key")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/products", body)
	p := decodeProduct06(t, body["data"])
	status, replay := admin.request("POST", "/api/v1/admin/products", input, "catalog-create-key")
	if status != 200 || decodeProduct06(t, replay["data"]).ID != p.ID {
		t.Fatal("idempotent replay", status, replay)
	}
	contracttest.Data(t, "AdminProduct", replay)
	changed := input
	changed.Slug = "different"
	status, body = admin.request("POST", "/api/v1/admin/products", changed, "catalog-create-key")
	if status != 409 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	for _, tc := range []struct {
		path, template string
		public         bool
	}{{"/products", "/products", true}, {"/products/" + p.ID, "/products/{id}", true}, {"/admin/products", "/admin/products", false}, {"/admin/products/" + p.ID, "/admin/products/{id}", false}, {"/admin/os-images", "/admin/os-images", false}} {
		caller := admin
		if tc.public {
			caller = guest
		}
		status, body = caller.request("GET", "/api/v1"+tc.path, nil, "")
		if status != 200 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Response(t, "GET", tc.template, body)
		if !tc.public {
			status, body = customer.request("GET", "/api/v1"+tc.path, nil, "")
			if status != 403 {
				t.Fatal(tc.path, status, body)
			}
			contracttest.Error(t, body)
		}
	}
	input.Revision = p.Revision
	input.Visibility = "hidden"
	status, body = admin.request("PATCH", "/api/v1/admin/products/"+p.ID, input, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/products/{id}", body)
	status, body = admin.request("PATCH", "/api/v1/admin/products/"+p.ID, input, "")
	if status != 409 {
		t.Fatal("stale revision", status, body)
	}
	contracttest.Error(t, body)
	status, body = guest.request("GET", "/api/v1/products/"+p.ID, nil, "")
	if status != 404 {
		t.Fatal("hidden product leaked", status, body)
	}
	contracttest.Error(t, body)
	status, body = admin.request("POST", "/api/v1/admin/products/"+p.ID+"/duplicate", map[string]any{}, "catalog-copy-key")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "POST", "/admin/products/{id}/duplicate", body)
	duplicate := decodeProduct06(t, body["data"])
	if duplicate.ID == p.ID || duplicate.Visibility != "hidden" || duplicate.Revision != 1 {
		t.Fatal(duplicate)
	}
	migrationExec(t, t.Context(), m.DB, "INSERT INTO os_images(id,family,name,version,min_disk_gb,enabled,controller_id,connection_id,remote_image_id,sha256) VALUES('image1','ubuntu','Ubuntu','24.04',10,FALSE,'controller','controller','remote',REPEAT('a',64))")
	status, body = admin.request("PATCH", "/api/v1/admin/os-images/image1", map[string]any{"enabled": true, "name": "Ubuntu LTS"}, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "PATCH", "/admin/os-images/{id}", body)
	if image, ok := body["data"].(map[string]any); !ok || image["connection_id"] != "controller" {
		t.Fatal("image update changed its server connection binding", body)
	}
	for _, tc := range []struct {
		method, path string
		input        any
	}{{"POST", "/admin/products", input}, {"PATCH", "/admin/products/" + p.ID, input}, {"POST", "/admin/products/" + p.ID + "/duplicate", map[string]any{}}, {"PATCH", "/admin/os-images/image1", map[string]any{"enabled": false}}} {
		status, body = customer.request(tc.method, "/api/v1"+tc.path, tc.input, "denied-catalog-key")
		if status != 403 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Error(t, body)
		csrf := admin.csrf
		admin.csrf = "wrong"
		status, body = admin.request(tc.method, "/api/v1"+tc.path, tc.input, "csrf-catalog-key")
		admin.csrf = csrf
		if status != 403 {
			t.Fatal("CSRF", tc.path, status, body)
		}
		contracttest.Error(t, body)
	}
	// The live permission check applies to every new admin route, including reads.
	migrationExec(t, t.Context(), m.DB, "INSERT INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES('catalog-none','None','No product permissions',JSON_ARRAY(),FALSE,1)")
	migrationExec(t, t.Context(), m.DB, "UPDATE staff_members SET role_id='catalog-none' WHERE user_id=?", f.admin.ID)
	status, body = admin.request("GET", "/api/v1/admin/products", nil, "")
	if status != 403 {
		t.Fatal("revoked permission", status, body)
	}
	contracttest.Error(t, body)
}

func TestMySQLCommerceProductConcurrentIdempotency(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	input := product06Fixture(t, f)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for n := 0; n < 12; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := m.SaveProduct06(t.Context(), f.admin, "", input, "concurrent-product", false)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- p.ID
		}()
	}
	wg.Wait()
	close(ids)
	first := ""
	count := 0
	for id := range ids {
		count++
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate product")
		}
	}
	if count != 12 {
		t.Fatal("missing results", count)
	}
	var products, requests int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM products WHERE slug='test-product'").Scan(&products); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM commerce_requests WHERE request_key='concurrent-product'").Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if products != 1 || requests != 1 {
		t.Fatal(products, requests)
	}
}
