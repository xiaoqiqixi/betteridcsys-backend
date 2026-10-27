package billing

import (
	"aster/billing/internal/controllers"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func productInputJSON(t *testing.T, spec controllers.ProductSpec) string {
	t.Helper()
	raw, err := json.Marshal(ProductConfigurationInput{Revision: 1, Spec: spec, Reason: "Operator reviewed product configuration"})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
func TestProductConfigurationHTTPParserStrictExactNullableShape(t *testing.T) {
	if reflect.TypeOf(controllers.Resources{}).NumField() != 17 {
		t.Fatal("resource field contract changed; review exact shape fixtures")
	}
	custom := productInputJSON(t, customProductSpec())
	preset := productInputJSON(t, presetProductSpec())
	disabled := productInputJSON(t, controllers.DisabledSpec())
	for _, raw := range []string{custom, preset, disabled, custom + " \n\t"} {
		r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		w := httptest.NewRecorder()
		var in ProductConfigurationInput
		if !productConfigurationBody(w, r, &in) || !in.Spec.Valid() {
			t.Fatal("valid complete schema refused", raw, w.Code, w.Body.String())
		}
	}
	invalid := []struct{ name, raw string }{
		{"null_root", "null"}, {"null_spec", strings.Replace(preset, `"spec":{`, `"spec":null,"ignored":{`, 1)},
		{"missing_root_revision", strings.Replace(preset, `"revision":1,`, "", 1)},
		{"missing_root_reason", strings.Replace(preset, `,"reason":"Operator reviewed product configuration"`, "", 1)},
		{"missing_resources", strings.Replace(preset, `,"resources":null`, "", 1)},
		{"missing_custom_zero_field", strings.Replace(custom, `"data_disk_gib":0,`, "", 1)},
		{"extra_root", strings.Replace(preset, `"revision":1`, `"revision":1,"endpoint":"https://forbidden.invalid"`, 1)},
		{"extra_spec", strings.Replace(preset, `"mode":"preset"`, `"mode":"preset","credentials":"not-supported"`, 1)},
		{"extra_resource", strings.Replace(custom, `"cpu_cores":2`, `"cpu_cores":2,"extra":0`, 1)},
		{"duplicate_root", strings.Replace(preset, `"revision":1`, `"revision":1,"revision":1`, 1)},
		{"duplicate_spec", strings.Replace(preset, `"mode":"preset"`, `"mode":"preset","mode":"disabled"`, 1)},
		{"duplicate_resource", strings.Replace(custom, `"cpu_cores":2`, `"cpu_cores":2,"cpu_cores":4`, 1)},
		{"case_root", strings.Replace(preset, `"revision":1`, `"Revision":1`, 1)},
		{"case_spec", strings.Replace(preset, `"controller_id":`, `"Controller_ID":`, 1)},
		{"case_resource", strings.Replace(custom, `"cpu_cores":2`, `"CPU_CORES":2`, 1)},
		{"case_alias_extra", strings.Replace(custom, `"cpu_cores":2`, `"cpu_cores":2,"CPU_CORES":2`, 1)},
		{"unicode_key", strings.Replace(custom, `"image_id":`, `"image_іd":`, 1)},
		{"null_revision", strings.Replace(preset, `"revision":1`, `"revision":null`, 1)},
		{"null_reason", strings.Replace(preset, `"reason":"Operator reviewed product configuration"`, `"reason":null`, 1)},
		{"null_controller", strings.Replace(preset, `"controller_id":"local-test"`, `"controller_id":null`, 1)},
		{"null_numeric_resource", strings.Replace(custom, `"data_disk_gib":0`, `"data_disk_gib":null`, 1)},
		{"null_string_resource", strings.Replace(custom, `"system_storage_id":""`, `"system_storage_id":null`, 1)},
		{"array_resources", strings.Replace(preset, `"resources":null`, `"resources":[]`, 1)},
		{"empty_resource_object", strings.Replace(preset, `"resources":null`, `"resources":{}`, 1)},
		{"array_root", "[]"}, {"wrong_revision_type", strings.Replace(preset, `"revision":1`, `"revision":"1"`, 1)},
		{"fractional_integer", strings.Replace(custom, `"cpu_cores":2`, `"cpu_cores":2.5`, 1)},
		{"overflow_integer", strings.Replace(custom, `"cpu_cores":2`, `"cpu_cores":9223372036854775808`, 1)},
		{"trailing_object", preset + ` {}`}, {"trailing_null", preset + ` null`}, {"trailing_garbage", preset + `x`},
		{"invalid_utf8", strings.Replace(preset, "Operator", string([]byte{0xff}), 1)},
		{"oversize", strings.Replace(preset, "Operator reviewed product configuration", strings.Repeat("x", 17<<10), 1)},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(tc.raw))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			var in ProductConfigurationInput
			if productConfigurationBody(w, r, &in) || w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"INVALID_PRODUCT_CONFIGURATION"`) {
				t.Fatal("ambiguous/incomplete module JSON accepted", tc.raw, w.Code, w.Body.String())
			}
		})
	}
	for _, media := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(preset))
		r.Header.Set("Content-Type", media)
		w := httptest.NewRecorder()
		var in ProductConfigurationInput
		if productConfigurationBody(w, r, &in) || w.Code != 415 {
			t.Fatal("non-JSON accepted", media, w.Code)
		}
	}
}
func productHTTPRaw(t *testing.T, c *testClient, method, path, raw string) (int, http.Header, map[string]any) {
	t.Helper()
	r, err := http.NewRequest(method, c.base+path, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", c.csrf)
	response, err := c.c.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if json.Unmarshal(data, &out) != nil {
		t.Fatal("not JSON", string(data))
	}
	return response.StatusCode, response.Header, out
}
func TestProductConfigurationHTTPPermissionsCSRFAndPublicPrivacy(t *testing.T) {
	f, pid, config := productConfigurationFixture(t, memoryCatalogFixture)
	m := f.store.(*Memory)
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	admin := client(t, server.URL, "admin@aster.local", "DemoAdmin!2026")
	customer := client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")
	anonymous := client(t, server.URL, "", "")
	path := "/api/v1/admin/catalog/" + pid + "/product-configuration"
	input := productInputJSON(t, presetProductSpec())
	for _, tc := range []struct {
		c    *testClient
		want int
	}{{anonymous, 401}, {customer, 403}} {
		for _, method := range []string{"GET", "POST"} {
			if status, _, out := productHTTPRaw(t, tc.c, method, path, input); status != tc.want {
				t.Fatal("module administration exposed", method, status, out)
			}
		}
	}
	if status, out := admin.request("GET", path, nil, ""); status != 200 || out["data"].(map[string]any)["revision"] != float64(1) {
		t.Fatal(status, out)
	}
	old := admin.csrf
	admin.csrf = "invalid"
	if status, _, out := productHTTPRaw(t, admin, "POST", path, input); status != 403 || out["error"].(map[string]any)["code"] != "CSRF_INVALID" {
		t.Fatal("missing CSRF allowed save", status, out)
	}
	admin.csrf = old
	request, err := http.NewRequest("POST", server.URL+path, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", admin.csrf)
	request.Header.Set("Origin", "https://foreign.invalid")
	response, err := admin.c.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("foreign origin allowed module save", response.StatusCode)
	}
	status, _, out := productHTTPRaw(t, admin, "POST", path, input)
	if status != 200 || out["data"].(map[string]any)["revision"] != float64(2) {
		t.Fatal(status, out)
	}
	if status, _, out = productHTTPRaw(t, admin, "POST", path, input); status != 409 || out["error"].(map[string]any)["code"] != "PRODUCT_CONFIGURATION_CONFLICT" {
		t.Fatal("stale config save accepted", status, out)
	}
	// Null resources is valid only for preset/disabled, not a custom resource order.
	current, err := config.ProductConfiguration(t.Context(), f.admin, pid)
	if err != nil {
		t.Fatal(err)
	}
	bad := ProductConfigurationInput{Revision: current.Revision, Spec: controllers.ProductSpec{Mode: "custom", ControllerID: "local-test"}, Reason: "invalid custom"}
	if status, out = admin.request("POST", path, bad, ""); status != 400 || out["error"].(map[string]any)["code"] != "INVALID_PRODUCT_CONFIGURATION" {
		t.Fatal("custom nil resources accepted", status, out)
	}
	invalid := ProductConfigurationInput{Revision: current.Revision, Spec: controllers.ProductSpec{Mode: "preset", ControllerID: "https://forbidden.invalid", RemoteProductID: "x"}, Reason: "invalid controller"}
	if status, out = admin.request("POST", path, invalid, ""); status != 400 {
		t.Fatal("URL injected as controller ID", status, out)
	}
	order := provisioningOrder(t, f, pid, false)
	for _, tc := range []struct {
		c    *testClient
		path string
	}{{anonymous, "/api/v1/plans?currency=CNY"}, {customer, "/api/v1/orders"}, {customer, "/api/v1/orders/" + order.ID}, {customer, "/api/v1/invoices"}, {admin, "/api/v1/admin/catalog"}} {
		status, out := tc.c.request("GET", tc.path, nil, "")
		if status != 200 {
			t.Fatal(tc.path, status, out)
		}
		raw, _ := json.Marshal(out)
		for _, private := range []string{"controller_id", "remote_product_id", "configuration_sha256", "compute_node_id", "configuration_revision"} {
			if strings.Contains(string(raw), private) {
				t.Fatal("private module configuration leaked", tc.path, string(raw))
			}
		}
	}
	m.mu.Lock()
	u := m.users[f.admin.ID]
	u.Role = "customer"
	m.users[u.ID] = u
	m.mu.Unlock()
	if status, out = admin.request("GET", path, nil, ""); status != 403 {
		t.Fatal("persisted demotion left config access", status, out)
	}
}
func TestProvisioningJobsHTTPManualPaidQueueNoDispatchRoutes(t *testing.T) {
	f, pid, config := productConfigurationFixture(t, memoryCatalogFixture)
	ctx := t.Context()
	m := f.store.(*Memory)
	if _, err := config.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "manual"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer server.Close()
	admin := client(t, server.URL, "admin@aster.local", "DemoAdmin!2026")
	customer := client(t, server.URL, "demo@aster.local", "DemoCustomer!2026")
	unpaid := provisioningOrder(t, f, pid, false)
	unpaidPath := "/api/v1/admin/orders/" + unpaid.ID + "/provisioning-jobs"
	if status, out := admin.request("POST", unpaidPath, map[string]string{"reason": "manual"}, ""); status != 409 || out["error"].(map[string]any)["code"] != "PROVISIONING_STATE_CONFLICT" {
		t.Fatal("unpaid queued", status, out)
	}
	paid := provisioningOrder(t, f, pid, true)
	queuePath := "/api/v1/admin/orders/" + paid.ID + "/provisioning-jobs"
	if status, out := customer.request("POST", queuePath, map[string]string{"reason": "manual"}, ""); status != 403 {
		t.Fatal(status, out)
	}
	csrf := admin.csrf
	admin.csrf = "bad"
	if status, out := admin.request("POST", queuePath, map[string]string{"reason": "manual"}, ""); status != 403 {
		t.Fatal(status, out)
	}
	admin.csrf = csrf
	beforeEvents, beforeServices := len(m.orderEvents), len(m.services)
	status, _, out := productHTTPRaw(t, admin, "POST", queuePath, `{"reason":"Operator reviewed paid order"}`)
	if status != 201 {
		t.Fatal(status, out)
	}
	id := out["data"].(map[string]any)["id"].(string)
	jobPath := "/api/v1/admin/provisioning-jobs/" + id
	status, header, replayed := productHTTPRaw(t, admin, "POST", queuePath, `{"reason":"Operator reviewed paid order"}`)
	if status != 200 || header.Get("Idempotent-Replay") != "true" || replayed["data"].(map[string]any)["id"] != id {
		t.Fatal("duplicate manual operation", status, header, replayed)
	}
	if status, out = admin.request("GET", jobPath, nil, ""); status != 200 || out["data"].(map[string]any)["state"] != "queued" {
		t.Fatal(status, out)
	}
	for _, path := range []string{jobPath} {
		if status, out = customer.request("GET", path, nil, ""); status != 403 {
			t.Fatal("customer job read", status, out)
		}
	}
	if status, out = admin.request("POST", jobPath, map[string]string{"state": "succeeded"}, ""); status != 405 {
		t.Fatal("browser rewrote worker result", status, out)
	}
	for _, suffix := range []string{"/dispatch", "/claim", "/complete", "/reconcile"} {
		for _, method := range []string{"GET", "POST"} {
			if status, out = admin.request(method, jobPath+suffix, map[string]string{"status": "succeeded"}, ""); status != 404 {
				t.Fatal("browser worker control route exposed", method, suffix, status, out)
			}
		}
	}
	if status, out = admin.request("GET", queuePath, nil, ""); status != 405 {
		t.Fatal("unexpected queue method", status, out)
	}
	m.mu.Lock()
	r := m.provisioningJobs[id]
	unchanged := len(m.orderEvents) == beforeEvents && len(m.services) == beforeServices && len(m.provisioningJobs) == 1
	m.mu.Unlock()
	if !unchanged || r.Attempts != 0 || r.Job.State != "queued" || r.ClaimToken != "" || r.Job.Result != nil {
		t.Fatal("HTTP queue dispatched or changed core state", r.Job, unchanged)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "claim_token") {
		t.Fatal("worker token exposed")
	}
}
func TestProductConfigurationRoutesObservabilityUsesFixedTemplates(t *testing.T) {
	id := ID()
	for _, tc := range []struct{ path, want string }{
		{"/api/v1/admin/catalog/product-PRIVATE/product-configuration", "/api/v1/admin/catalog/{plan_id}/product-configuration"},
		{"/api/v1/admin/orders/" + id + "/provisioning-jobs", "/api/v1/admin/orders/{order_id}/provisioning-jobs"},
		{"/api/v1/admin/provisioning-jobs/" + id, "/api/v1/admin/provisioning-jobs/{job_id}"},
		{"/api/v1/admin/provisioning-jobs/" + id + "/reconcile", "unmatched"},
	} {
		if got := loggedRoute(tc.path); got != tc.want {
			t.Fatal(tc.path, got, tc.want)
		}
		app, log := observedApp(NewMemory())
		request := httptest.NewRequest("GET", "http://localhost"+tc.path+"?token=PRIVATE_QUERY", strings.NewReader("PRIVATE_PAYLOAD"))
		request.Header.Set("Authorization", "Bearer PRIVATE_AUTH")
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, request)
		event := onlyObservation(t, log)
		if event["route"] != tc.want {
			t.Fatal(event)
		}
		assertLogDoesNotContain(t, log, id, "product-PRIVATE", "PRIVATE_QUERY", "PRIVATE_PAYLOAD", "PRIVATE_AUTH")
	}
}
