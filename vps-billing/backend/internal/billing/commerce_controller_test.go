package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
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
	view                     serverv1.Service
	description              *serverv1.Description
	reads, sends, provisions int
	sendErr                  error
	actionStatus             string
	operationStatus          string
	lastParams               serviceParams06
	actions                  map[string]string
	lastRequest              serverv1.Request
	liveSnapshots            bool
}

func (c *fakeServiceController06) Images(context.Context, string) ([]serverv1.Image, error) {
	return []serverv1.Image{{ID: "debian-12", Name: "Debian", Version: "12", Family: "linux", MinDiskGB: 10, SHA256: strings.Repeat("1", 64)}}, nil
}
func (c *fakeServiceController06) Service(_ context.Context, id, owner string) (serverv1.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	v := c.view
	if id != v.ResourceID || owner != v.OwnerRef {
		return serverv1.Service{}, serverv1.ErrUnavailable
	}
	return v, nil
}
func (c *fakeServiceController06) Console(context.Context, string, string) (serverv1.Console, error) {
	return serverv1.Console{URL: "https://console.example.test/console/#opaque-single-use-ticket", ExpiresAt: time.Now().UTC().Add(time.Minute)}, nil
}
func (c *fakeServiceController06) Metrics(context.Context, string, string, string) (serverv1.Metrics, error) {
	return serverv1.Metrics{StepSeconds: 60, Points: []serverv1.MetricPoint{{At: time.Now().UTC()}}}, nil
}
func (c *fakeServiceController06) Snapshots(context.Context, string, string) (serverv1.Snapshots, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return serverv1.Snapshots{Items: []serverv1.Snapshot{{ID: strings.Repeat("b", 32), Name: "before-update", Status: "ready", CreatedAt: time.Now().UTC()}}, Limit: 3, RequiresStopped: !c.liveSnapshots}, nil
}
func (c *fakeServiceController06) Call(ctx context.Context, r serverv1.Request) (serverv1.Response, error) {
	out := serverv1.Response{Protocol: serverv1.Protocol, OperationID: r.OperationID, Digest: r.Digest, State: "done"}
	switch r.Method {
	case "describe":
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.description != nil {
			out.Description = c.description
			return out, nil
		}
		d := serverv1.Description{ID: "server.fake", Name: serverv1.Text{"en": "Fake module", "zh-CN": "测试模块"}, Version: "1.0.0", Capabilities: []string{"create", "suspend", "unsuspend", "terminate", "change_package", "renew", "power", "status", "console", "metrics", "snapshots", "images", "action"}, ProductSchema: serverv1.EmptySchema(), OrderSchema: serverv1.EmptySchema(), CustomActions: []serverv1.CustomAction{}}
		d.CustomActions = c.view.CustomActions
		out.Description = &d
		return out, nil
	case "test_connection":
		return out, nil
	case "validate_product":
		out.Config, out.ConfigDigest, _ = serverv1.EmptySchema().Normalize(r.Config)
		return out, nil
	case "status":
		v, e := c.Service(ctx, r.ResourceID, r.OwnerRef)
		out.Service = &v
		return out, e
	case "console":
		v, e := c.Console(ctx, r.ResourceID, r.OwnerRef)
		out.Console = &v
		return out, e
	case "metrics":
		v, e := c.Metrics(ctx, r.ResourceID, r.OwnerRef, r.Range)
		out.Metrics = &v
		return out, e
	case "snapshots":
		v, e := c.Snapshots(ctx, r.ResourceID, r.OwnerRef)
		out.Snapshots = &v
		return out, e
	case "images":
		v, e := c.Images(ctx, r.OwnerRef)
		out.Images = v
		return out, e
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Method == "operation_status" {
		if _, ok := c.actions[r.OperationID]; !ok {
			return serverv1.Response{}, serverv1.ErrUncertain
		}
		out.State = c.operationStatus
		if out.State == "" || out.State == "succeeded" {
			out.State = "done"
		}
		if out.State == "rejected" {
			out.State = "failed"
		}
		if c.actions[r.OperationID] == "create" {
			out.ResourceID = c.view.ResourceID
		}
		return out, nil
	}
	if r.Verify() != nil {
		return serverv1.Response{}, serverv1.ErrConfiguration
	}
	if c.actions == nil {
		c.actions = map[string]string{}
	}
	if _, ok := c.actions[r.OperationID]; ok {
		return serverv1.Response{}, serverv1.ErrUncertain
	}
	c.actions[r.OperationID] = r.Method
	c.lastRequest = r
	if r.Method == "create" {
		c.provisions++
		out.ResourceID = c.view.ResourceID
		// Simulate a module that finishes image setup before reporting create done.
		if image, ok := r.Input["image_id"].(string); ok {
			c.lastParams.ImageID = image
			if hash, ok := r.Input["password_hash"].(string); ok {
				c.lastParams.PasswordHash = hash
				c.lastParams.Password = ""
			} else {
				c.lastParams.Password = "Ephemeral-Plugin-Only!"
			}
			out.State = "accepted"
		}
	} else {
		c.sends++
		_ = json.Unmarshal(jsonValue06(r.Input), &c.lastParams)
		if password, ok := r.Secrets["password"].(string); ok {
			c.lastParams.Password = password
		}
		out.State = c.actionStatus
		if out.State == "" {
			out.State = "accepted"
		}
		if out.State == "rejected" {
			out.State = "failed"
		}
	}
	return out, c.sendErr
}
func serviceControllerFixture06(t *testing.T, m *MySQL, owner, sid string) (*fakeServiceController06, *ServerModules06) {
	t.Helper()
	controller, resource := "controller-test", ID()
	if _, err := m.DB.Exec("INSERT IGNORE INTO server_connections(id,module_id,name,enabled) VALUES(?,'server.fake','Test connection',TRUE)", controller); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec(`UPDATE services SET module_id='server.fake',connection_id=?,resource_id=?,configuration_snapshot=JSON_SET(configuration_snapshot,'$.kind','cloud_server','$.module',JSON_OBJECT('module_id','server.fake','connection_id','controller-test','config',JSON_OBJECT(),'config_digest',REPEAT('a',64))),status='active' WHERE id=?`, controller, resource, sid); err != nil {
		t.Fatal(err)
	}
	c := &fakeServiceController06{view: serverv1.Service{Protocol: serverv1.Protocol, ResourceID: resource, OwnerRef: owner, Power: "stopped", IPv4: []string{"192.0.2.2"}, IPv6: []string{}, Specs: serverv1.Specs{CPUCores: 2, MemoryMB: 2048, DiskGB: 40, BandwidthMbps: 100}, Capabilities: serverv1.Capabilities{Power: true, Reinstall: true, ResetPassword: true, Console: true, Rescue: false, Snapshots: true, Metrics: true, Upgrade: true}, Traffic: serverv1.Traffic{RXBytes: 1 << 30, TXBytes: 2 << 30, PeriodStart: time.Now().Add(-time.Hour), PeriodEnd: time.Now().Add(24 * time.Hour)}}}
	runtime := &ServerModules06{Prepare: func(context.Context, string) (serverv1.Processor, error) { return c, nil }, Selections: func() ([]pluginapi.Selection, error) {
		return []pluginapi.Selection{{Slot: controller, ID: "server.fake", Version: "1.0.0", Enabled: true}}, nil
	}}
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
	controller.view.Capabilities = serverv1.Capabilities{}
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
		if err := m.SyncImages06(t.Context(), "controller-test", images); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var enabled bool
	var family string
	if err := m.DB.QueryRow("SELECT COUNT(*),MAX(enabled),MAX(family) FROM os_images WHERE connection_id=?", "controller-test").Scan(&count, &enabled, &family); err != nil || count != 1 || enabled || family != "debian" {
		t.Fatal(count, enabled, family, err)
	}
	if _, err := m.DB.Exec("UPDATE os_images SET enabled=TRUE WHERE connection_id=?", "controller-test"); err != nil {
		t.Fatal(err)
	}
	images[0].SHA256 = strings.Repeat("2", 64)
	if err := m.SyncImages06(t.Context(), "controller-test", images); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.QueryRow("SELECT enabled FROM os_images WHERE connection_id=?", "controller-test").Scan(&enabled); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
}
func TestCommerceControllerPreservesUnknownMeasurements(t *testing.T) {
	v, err := serviceMetrics06("24h", serverv1.Metrics{StepSeconds: 60, Points: []serverv1.MetricPoint{{At: time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "ServiceMetrics", map[string]any{"data": v})
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"cpu_percent":null`) {
		t.Fatal(string(raw))
	}
	snapshots, err := snapshots06(serverv1.Snapshots{Items: []serverv1.Snapshot{{ID: ID(), Name: "unknown size", Status: "ready", CreatedAt: time.Now().UTC()}}, Limit: 1, RequiresStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	contracttest.Data(t, "SnapshotList", map[string]any{"data": snapshots})
	if snapshots.Items[0].SizeGB != nil {
		t.Fatal("unknown size became zero")
	}
	var _ api.Service = customerService06(api.AdminService{})
}
