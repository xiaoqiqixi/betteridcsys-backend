package astercloud

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/pkg/pluginapi/serverv1/conformance"
	"aster/billing/plugins/server.aster-cloud/controllerapi"
	"aster/billing/plugins/server.aster-cloud/serviceapi"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type providerFixture struct {
	mu             sync.Mutex
	sends, creates int
	uncertain      bool
	createStatus   string
	actionStatus   string
	last           serviceapi.ActionRequest
	actions        map[string]serviceapi.ActionRequest
}

const resource = "99999999999999999999999999999999"

func (p *providerFixture) Send(_ context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.creates++
	return p.result(r), p.err()
}
func (p *providerFixture) Lookup(_ context.Context, r controllerapi.Request) (controllerapi.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result(r), nil
}
func (p *providerFixture) result(r controllerapi.Request) controllerapi.Result {
	_, hash, _ := r.BytesAndDigest()
	status := p.createStatus
	if status == "" {
		status = "succeeded"
	}
	return controllerapi.Result{Protocol: controllerapi.Protocol, OperationID: r.OperationID, OrderID: r.OrderID, RequestSHA256: hash, Status: status, ResourceID: resource}
}
func (p *providerFixture) err() error {
	if p.uncertain {
		return serverv1.ErrUncertain
	}
	return nil
}
func (p *providerFixture) Images(context.Context, string) ([]serviceapi.Image, error) {
	return []serviceapi.Image{{ID: "debian-12", Family: "linux", Name: "Debian", Version: "12", MinDiskGB: 1, SHA256: strings.Repeat("a", 64)}}, nil
}
func (p *providerFixture) Service(_ context.Context, id, owner string) (serviceapi.Service, error) {
	return serviceapi.Service{Protocol: serviceapi.Protocol, ResourceID: id, OwnerRef: owner, Power: "stopped", IPv4: []string{}, IPv6: []string{}, Specs: serviceapi.Specs{CPUCores: 1, MemoryMB: 128, DiskGB: 10}, Capabilities: serviceapi.Capabilities{Power: true, Reinstall: true, ResetPassword: true, Console: true, Metrics: true, Snapshots: true, Upgrade: true}}, nil
}
func (p *providerFixture) Action(_ context.Context, _ string, r serviceapi.ActionRequest) (serviceapi.ActionResult, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sends++
	p.last = r
	if p.actions == nil {
		p.actions = map[string]serviceapi.ActionRequest{}
	}
	p.actions[r.OperationID] = r
	return serviceapi.ActionResult{OperationID: r.OperationID, Status: "accepted"}, false, p.err()
}
func (p *providerFixture) Operation(_ context.Context, _, _, id string) (serviceapi.Operation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	action, ok := p.actions[id]
	if !ok {
		return serviceapi.Operation{}, serverv1.ErrUncertain
	}
	status := p.actionStatus
	if status == "" {
		status = "succeeded"
	}
	return serviceapi.Operation{OperationID: id, Action: action.Action, Status: status}, nil
}
func (p *providerFixture) Console(context.Context, string, string) (serviceapi.Console, error) {
	return serviceapi.Console{URL: "https://example.test/console/#one-time", ExpiresAt: time.Now().Add(time.Minute)}, nil
}
func (p *providerFixture) Metrics(context.Context, string, string, string) (serviceapi.Metrics, error) {
	return serviceapi.Metrics{StepSeconds: 60, Points: []serviceapi.MetricPoint{}}, nil
}
func (p *providerFixture) Snapshots(context.Context, string, string) (serviceapi.Snapshots, error) {
	return serviceapi.Snapshots{Items: []serviceapi.Snapshot{}, Limit: 3, RequiresStopped: true}, nil
}
func fixture(t *testing.T) (*Module, *providerFixture, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	p := &providerFixture{}
	m, err := New("test", p, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m, p, dir
}
func product() serverv1.Fields {
	return serverv1.Fields{"preset_id": "standard", "cpu_cores": 2, "memory_mb": 2048, "disk_gb": 40, "bandwidth_mbps": 100}
}
func request(method string) serverv1.Request {
	return serverv1.Request{Protocol: serverv1.Protocol, Method: method, OperationID: strings.Repeat("1", 32), ServiceID: strings.Repeat("2", 32), OwnerRef: strings.Repeat("3", 32), Config: product(), Input: serverv1.Fields{"hostname": "example.test"}}
}
func TestConformance(t *testing.T) {
	m, _, _ := fixture(t)
	conformance.Test(t, conformance.Suite{Processor: m, Config: product(), Input: map[string]serverv1.Fields{"create": {"hostname": "conformance.example"}, "power": {"action": "stop"}}})
}
func TestUncertainReceiptSurvivesRestartWithoutResend(t *testing.T) {
	m, p, dir := fixture(t)
	p.uncertain = true
	r := request("reset_password")
	r.ResourceID = resource
	r.Input = nil
	r.Secrets = serverv1.Fields{"password": "NeverPersistThisPassword!2026"}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	result, err := m.Call(t.Context(), r)
	if err != nil || result.State != "uncertain" {
		t.Fatal(result, err)
	}
	m.Close()
	restarted, err := New("test", p, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	for range 2 {
		result, err = restarted.Call(t.Context(), r)
		if err != nil || result.State != "uncertain" {
			t.Fatal(result, err)
		}
	}
	lookup := serverv1.Request{Protocol: serverv1.Protocol, Method: "operation_status", OperationID: r.OperationID, Digest: r.Digest}
	result, err = restarted.Call(t.Context(), lookup)
	if err != nil || result.State != "done" || p.sends != 1 {
		t.Fatal(result, err, p.sends)
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		raw, _ := os.ReadFile(filepath.Join(dir, f.Name()))
		if strings.Contains(string(raw), "NeverPersistThisPassword") {
			t.Fatal("plaintext persisted")
		}
	}
	bad := r
	bad.Secrets = serverv1.Fields{"password": "DifferentPassword!2026"}
	_ = bad.Seal()
	if _, err = restarted.Call(t.Context(), bad); err == nil {
		t.Fatal("digest collision accepted")
	}
}
func TestCreateSetupProgressIsReadOnlyUntilWorkerAdvances(t *testing.T) {
	m, p, dir := fixture(t)
	p.createStatus = "accepted"
	r := request("create")
	r.Input["image_id"] = "debian-12"
	_ = r.Seal()
	result, err := m.Call(t.Context(), r)
	if err != nil || result.State != "accepted" {
		t.Fatal(result, err)
	}
	p.createStatus = "succeeded"
	lookup := serverv1.Request{Protocol: serverv1.Protocol, Method: "operation_status", OperationID: r.OperationID, Digest: r.Digest}
	result, err = m.Call(t.Context(), lookup)
	if err != nil || result.State != "accepted" || p.sends != 0 {
		t.Fatal("read-only reconciliation sent a mutation", result, err, p.sends)
	}
	m.Close()
	restarted, err := New("test", p, p, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err = restarted.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.last.Params.ImageID != "debian-12" || len(p.last.Params.Password) < 32 {
		t.Fatal("setup did not use selected image and ephemeral credential")
	}
	password := p.last.Params.Password
	if err = restarted.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err = restarted.Call(t.Context(), lookup)
	if err != nil || result.State != "done" || result.ResourceID != resource || p.creates != 1 || p.sends != 1 {
		t.Fatal(result, err, p.creates, p.sends)
	}
	raw, err := os.ReadFile(restarted.path(r.OperationID))
	if err != nil || strings.Contains(string(raw), password) || strings.Contains(string(raw), `"password"`) {
		t.Fatal("setup password persisted", err)
	}
	var stored receipt
	if json.Unmarshal(raw, &stored) != nil || stored.Phase != "terminal" {
		t.Fatal("missing durable terminal state")
	}
}
func TestFailedSetupNeverCreatesAnotherResource(t *testing.T) {
	m, p, _ := fixture(t)
	p.actionStatus = "failed"
	r := request("create")
	r.Input["image_id"] = "debian-12"
	_ = r.Seal()
	if _, err := m.Call(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := m.Advance(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	out, err := m.Call(t.Context(), r)
	if err != nil || out.State != "failed" || p.creates != 1 || p.sends != 1 {
		t.Fatal(out, err, p.creates, p.sends)
	}
	if out.ResourceID != resource {
		t.Fatal("partial allocation reference lost")
	}
	p.actionStatus = "succeeded"
	r.OperationID = strings.Repeat("4", 32)
	r.ResourceID = out.ResourceID
	_ = r.Seal()
	if _, err = m.Call(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err = m.Advance(t.Context()); err != nil {
		t.Fatal(err)
	}
	out, err = m.Call(t.Context(), r)
	if err != nil || out.State != "done" || p.creates != 1 || p.sends != 2 {
		t.Fatal("explicit setup retry created a resource", out, err, p.creates, p.sends)
	}
}
