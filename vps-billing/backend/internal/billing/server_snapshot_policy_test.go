package billing

import (
	"aster/billing/internal/contracttest"
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommerceModuleSnapshotPolicy(t *testing.T) {
	for _, requiresStopped := range []bool{false, true} {
		list := serverv1.Snapshots{Items: []serverv1.Snapshot{}, Limit: 3, RequiresStopped: requiresStopped}
		projected, err := snapshots06(list)
		if err != nil || projected.RequiresStopped != requiresStopped {
			t.Fatal("module snapshot policy was not preserved", err)
		}
		contracttest.Data(t, "SnapshotList", map[string]any{"data": projected})
		for _, power := range []string{"running", "stopped"} {
			remote := serverv1.Service{Power: power}
			for _, action := range []string{"snapshot_create", "snapshot_restore", "snapshot_delete"} {
				if err := serviceRemoteState06(remote, action); err != nil {
					t.Fatal("generic state check overrode module policy", action, err)
				}
			}
			if err := serviceSnapshotState06(remote, list); (err != nil) != (requiresStopped && power != "stopped") {
				t.Fatal("module snapshot policy was not enforced", requiresStopped, power, err)
			}
		}
	}
	if err := serviceSnapshotState06(serverv1.Service{Power: "stopped"}, serverv1.Snapshots{Limit: -1}); !errors.Is(err, serverv1.ErrUncertain) {
		t.Fatal("invalid snapshot response was accepted", err)
	}
}

func TestMySQLCommerceModuleLiveSnapshotActions(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, _ := paidService06(t, f)
	activateServiceFixture06(t, m, sid)
	fake, runtime := serviceControllerFixture06(t, m, f.customer.ID, sid)
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), CommerceContractVersion: "0.6", Services: runtime})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	customer := client(t, server.URL, f.customer.Email, "CatalogFixture!2026")
	for _, tc := range []struct {
		method, path string
		input        any
	}{
		{"POST", "/snapshots", api.CreateSnapshotInput{Name: "online"}},
		{"POST", "/snapshots/" + strings.Repeat("b", 32) + "/restore", api.RestoreSnapshotInput{Confirm: sid}},
		{"DELETE", "/snapshots/" + strings.Repeat("b", 32), nil},
	} {
		fake.mu.Lock()
		fake.view.Power = "running"
		fake.liveSnapshots = false
		before := fake.sends
		fake.mu.Unlock()
		status, body := customer.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, ID())
		if status != 409 {
			t.Fatal("stopped-only module accepted a running snapshot operation", status, body)
		}
		fake.mu.Lock()
		if fake.sends != before {
			fake.mu.Unlock()
			t.Fatal("rejected snapshot operation reached module")
		}
		fake.liveSnapshots = true
		fake.mu.Unlock()
		status, body = customer.request("GET", "/api/v1/services/"+sid+"/snapshots", nil, "")
		if status != 200 || body["data"].(map[string]any)["requires_stopped"] != false {
			t.Fatal("live snapshot policy was not exposed", status, body)
		}
		contracttest.Response(t, "GET", "/services/{id}/snapshots", body)
		status, body = customer.request(tc.method, "/api/v1/services/"+sid+tc.path, tc.input, ID())
		if status != 200 {
			t.Fatal("live snapshot module rejected running service", status, body)
		}
		id := body["data"].(map[string]any)["id"].(string)
		if task, err := app.DispatchServiceTask06(t.Context(), id, true, true); err != nil || task.Status != "succeeded" {
			t.Fatal(task, err)
		}
		fake.mu.Lock()
		after := fake.sends
		fake.mu.Unlock()
		if after != before+1 {
			t.Fatal("live snapshot operation was not sent exactly once", before, after)
		}
	}
}
