package billing

import (
	"aster/billing/controllerapi"
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeServiceController06 struct {
	mu                       sync.Mutex
	view                     serviceapi.Service
	reads, sends, provisions int
	sendErr                  error
	actionStatus             string
	operationStatus          string
	lastParams               serviceapi.Params
	actions                  map[string]string
	lastRequest              controllerapi.Request
}

func (c *fakeServiceController06) Images(context.Context, string) ([]serviceapi.Image, error) {
	return []serviceapi.Image{{ID: "debian-12", Name: "Debian", Version: "12", Family: "linux", MinDiskGB: 10, SHA256: strings.Repeat("1", 64)}}, nil
}
func (c *fakeServiceController06) Service(_ context.Context, id, owner string) (serviceapi.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	v := c.view
	if id != v.ResourceID || owner != v.OwnerRef {
		return serviceapi.Service{}, serviceapi.ErrUnavailable
	}
	return v, nil
}
func (c *fakeServiceController06) Action(_ context.Context, _ string, r serviceapi.ActionRequest) (serviceapi.ActionResult, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sends++
	c.lastParams = r.Params
	if c.actions == nil {
		c.actions = map[string]string{}
	}
	c.actions[r.OperationID] = r.Action
	if c.sendErr != nil {
		return serviceapi.ActionResult{}, false, c.sendErr
	}
	status := c.actionStatus
	if status == "" {
		status = "accepted"
	}
	return serviceapi.ActionResult{OperationID: r.OperationID, Status: status, Code: "ACCEPTED"}, false, nil
}
func (c *fakeServiceController06) Operation(_ context.Context, _, _, op string) (serviceapi.Operation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := c.operationStatus
	if status == "" {
		status = "succeeded"
	}
	at := time.Now().UTC()
	return serviceapi.Operation{OperationID: op, Action: c.actions[op], Status: status, Code: "COMPLETED", FinishedAt: &at}, nil
}
func (c *fakeServiceController06) Console(context.Context, string, string) (serviceapi.Console, error) {
	return serviceapi.Console{URL: "https://console.example.test/console/#opaque-single-use-ticket", ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}
func (c *fakeServiceController06) Metrics(context.Context, string, string, string) (serviceapi.Metrics, error) {
	return serviceapi.Metrics{StepSeconds: 60, Points: []serviceapi.MetricPoint{{At: time.Now().UTC()}}}, nil
}
func (c *fakeServiceController06) Snapshots(context.Context, string, string) (serviceapi.Snapshots, error) {
	return serviceapi.Snapshots{Items: []serviceapi.Snapshot{{ID: strings.Repeat("b", 32), Name: "before-update", Status: "ready", CreatedAt: time.Now().UTC()}}, Limit: 3, RequiresStopped: true}, nil
}
func (c *fakeServiceController06) Send(_ context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.provisions++
	c.lastRequest = r
	_, digest, err := r.BytesAndDigest()
	if err != nil {
		return controllerapi.Result{}, err
	}
	return controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: digest, Status: "succeeded", ResourceID: c.view.ResourceID}, c.sendErr
}
func (c *fakeServiceController06) Lookup(_ context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, digest, err := r.BytesAndDigest()
	if err != nil {
		return controllerapi.Result{}, err
	}
	return controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: digest, Status: "succeeded", ResourceID: c.view.ResourceID}, nil
}
func serviceControllerFixture06(t *testing.T, m *MySQL, owner, sid string) (*fakeServiceController06, *ServiceControllers06) {
	t.Helper()
	controller, resource := "controller-test", ID()
	if _, err := m.DB.Exec(`UPDATE services SET controller_id=?,resource_id=?,configuration_snapshot=JSON_SET(configuration_snapshot,'$.kind','cloud_server'),status='active' WHERE id=?`, controller, resource, sid); err != nil {
		t.Fatal(err)
	}
	c := &fakeServiceController06{view: serviceapi.Service{Protocol: serviceapi.Protocol, ResourceID: resource, OwnerRef: owner, Power: "stopped", IPv4: []string{"192.0.2.2"}, IPv6: []string{}, Specs: serviceapi.Specs{CPUCores: 2, MemoryMB: 2048, DiskGB: 40, BandwidthMbps: 100}, Capabilities: serviceapi.Capabilities{Power: true, Reinstall: true, ResetPassword: true, Console: true, Rescue: true, Snapshots: true, Metrics: true, Upgrade: true}, Traffic: serviceapi.Traffic{RXBytes: 1 << 30, TXBytes: 2 << 30, PeriodStart: time.Now().Add(-time.Hour), PeriodEnd: time.Now().Add(24 * time.Hour)}}}
	runtime := &ServiceControllers06{Services: func(context.Context, string) (ServiceController06, error) { return c, nil }, Provisioning: func(context.Context, string) (ProvisioningController06, error) { return c, nil }, IDs: []string{controller}}
	return c, runtime
}
func TestMySQLCommerceControllerReadModelsAndOwnership(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	controller, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	server := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", Services: runtime}).Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	admin := client(t, server.URL, f.admin.Email, "CatalogFixture!2026")
	for _, tc := range []struct {
		method, path, template string
		input                  any
	}{
		{"GET", "", "/services/{id}", nil},
		{"POST", "/console", "/services/{id}/console", map[string]any{}},
		{"GET", "/metrics?range=1h", "/services/{id}/metrics", nil},
		{"GET", "/snapshots", "/services/{id}/snapshots", nil},
	} {
		status, body := customer.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, "")
		if status != 200 {
			t.Fatal(tc.path, status, body)
		}
		contracttest.Response(t, tc.method, tc.template, body)
		if tc.path == "" {
			data := body["data"].(map[string]any)
			if data["power"] != "stopped" || data["primary_ipv4"] != "192.0.2.2" || data["capabilities"].(map[string]any)["rescue"] != false {
				t.Fatal(data)
			}
		}
		controller.mu.Lock()
		reads := controller.reads
		controller.mu.Unlock()
		status, body = admin.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, "")
		if status != 404 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
		controller.mu.Lock()
		after := controller.reads
		controller.mu.Unlock()
		if after != reads {
			t.Fatal("foreign owner reached controller")
		}
	}
	// Feature availability comes from the controller even while billing has
	// suspended a service. Mutation state checks remain separate from capability.
	if _, err := m.DB.Exec("UPDATE services SET status='suspended' WHERE id=?", sid); err != nil {
		t.Fatal(err)
	}
	status, body := customer.request("GET", "/api/v1/services/"+sid, nil, "")
	if status != 200 {
		t.Fatal(status, body)
	}
	contracttest.Response(t, "GET", "/services/{id}", body)
	if body["data"].(map[string]any)["capabilities"].(map[string]any)["metrics"] != true {
		t.Fatal("billing status erased controller capabilities")
	}
	controller.mu.Lock()
	controller.view.Capabilities = serviceapi.Capabilities{}
	controller.mu.Unlock()
	for _, path := range []string{"/metrics", "/snapshots"} {
		status, body := customer.request("GET", "/api/v1/services/"+sid+path, nil, "")
		if status != 501 {
			t.Fatal(status, body)
		}
		contracttest.Error(t, body)
	}
	status, body = customer.request("POST", "/api/v1/services/"+sid+"/console", map[string]any{}, "")
	if status != 501 {
		t.Fatal(status, body)
	}
	contracttest.Error(t, body)
	images, _ := controller.Images(t.Context(), f.customer.ID)
	for range 2 {
		if err := m.SyncImages06(t.Context(), runtime.IDs[0], images); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var enabled bool
	var family string
	if err := m.DB.QueryRow("SELECT COUNT(*),MAX(enabled),MAX(family) FROM os_images WHERE controller_id=?", runtime.IDs[0]).Scan(&count, &enabled, &family); err != nil || count != 1 || enabled || family != "debian" {
		t.Fatal(count, enabled, family, err)
	}
	if _, err := m.DB.Exec("UPDATE os_images SET enabled=TRUE WHERE controller_id=?", runtime.IDs[0]); err != nil {
		t.Fatal(err)
	}
	images[0].SHA256 = strings.Repeat("2", 64)
	if err := m.SyncImages06(t.Context(), runtime.IDs[0], images); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.QueryRow("SELECT enabled FROM os_images WHERE controller_id=?", runtime.IDs[0]).Scan(&enabled); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
}
func TestCommerceControllerPreservesUnknownMeasurements(t *testing.T) {
	v, err := serviceMetrics06("24h", serviceapi.Metrics{StepSeconds: 60, Points: []serviceapi.MetricPoint{{At: time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "ServiceMetrics", map[string]any{"data": v})
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"cpu_percent":null`) {
		t.Fatal(string(raw))
	}
	snapshots, err := snapshots06(serviceapi.Snapshots{Items: []serviceapi.Snapshot{{ID: ID(), Name: "unknown size", Status: "ready", CreatedAt: time.Now().UTC()}}, Limit: 1, RequiresStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "SnapshotList", map[string]any{"data": snapshots})
	if snapshots.Items[0].SizeGB != nil {
		t.Fatal("unknown size became zero")
	}
	var _ api.Service = customerService06(api.AdminService{})
}
