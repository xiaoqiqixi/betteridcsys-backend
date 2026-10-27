package billing

import (
	"aster/billing/internal/updates"
	"aster/billing/pkg/pluginapi"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeUpdates struct {
	checks, prepares int
	err              error
	view             updates.View
	handoff          updates.Handoff
}

type fakeHostOperations struct {
	reads int
	view  updates.OperationView
}

func (f *fakeHostOperations) Snapshot() updates.OperationView {
	f.reads++
	return f.view
}

func TestHostOperationReceiptIsAdminOnlyReadOnlyAndIndependent(t *testing.T) {
	f := updateFixture()
	host := &fakeHostOperations{view: updates.OperationView{State: "absent"}}
	m := NewMemory()
	app := NewApp(m, Config{Updates: f, HostOperations: host, Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	const path = "/api/v1/admin/updates/operation"
	for _, tc := range []struct {
		client *testClient
		want   int
	}{{anonymous, 401}, {customer, 403}} {
		if status, _ := tc.client.request("GET", path, nil, ""); status != tc.want {
			t.Fatal("receipt authorization", status, tc.want)
		}
	}
	if host.reads != 0 {
		t.Fatal("unauthorized request reached receipt source")
	}
	// A body/query may not supply another receipt path or an operation command.
	status, out := admin.request("GET", path+"?path=/etc/private-secret&action=install", map[string]any{"path": "/etc/private-secret", "command": "id"}, "")
	if status != 200 || out["data"].(map[string]any)["state"] != "absent" || host.reads != 1 {
		t.Fatal("fixed read-only source", status, out)
	}
	if status, _ := admin.request("POST", path, map[string]any{"action": "upgrade"}, ""); status != 405 || host.reads != 1 {
		t.Fatal("receipt endpoint allowed mutation", status)
	}
	if f.checks != 0 || f.prepares != 0 {
		t.Fatal("receipt triggered release or handoff work")
	}
	audits, err := m.Audits(context.Background(), "")
	if err != nil || len(audits) != 0 {
		t.Fatal("receipt read created operation audit", len(audits), err)
	}
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	res, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("operation status may be cached")
	}
	for _, state := range []string{"unconfigured", "absent", "unavailable"} {
		host.view = updates.OperationView{State: state}
		status, out = admin.request("GET", path, nil, "")
		data := out["data"].(map[string]any)
		if status != 200 || data["state"] != state || data["operation"] != nil || len(data) != 1 {
			t.Fatal("receipt state confused with execution", status, out)
		}
	}
	app.Config.HostOperations = nil
	status, out = admin.request("GET", path, nil, "")
	if status != 200 || out["data"].(map[string]any)["state"] != "unconfigured" {
		t.Fatal("default receipt configuration", status, out)
	}
}

func (f *fakeUpdates) Snapshot() updates.View                      { return f.view }
func (f *fakeUpdates) Check(context.Context) (updates.View, error) { f.checks++; return f.view, f.err }
func (f *fakeUpdates) Prepare(_ context.Context, actor, component, tag, source string, id int64, catalog string) (updates.Handoff, error) {
	f.prepares++
	h := f.handoff
	h.Intent.ActorID = actor
	return h, f.err
}
func updateFixture() *fakeUpdates {
	assets := []updates.Asset{{Name: "BUILD.json", ID: 201, Size: 100, SHA256: strings.Repeat("a", 64)}, {Name: "PROVENANCE.json", ID: 202, Size: 100, SHA256: strings.Repeat("b", 64)}, {Name: "SHA256SUMS", ID: 203, Size: 300, SHA256: strings.Repeat("c", 64)}, {Name: "aster-0.4.1-linux-amd64.tar.gz", ID: 200, Size: 1024, SHA256: strings.Repeat("d", 64)}}
	now := time.Now().UTC().Truncate(time.Second)
	current := updates.Current{CoreVersion: "0.4.0", SMTPVersion: "1.0.0", SMTPStatus: "approved", PluginPolicyRevision: 3}
	return &fakeUpdates{view: updates.View{Current: current, Source: updates.Source{Repository: updates.Repository, Status: "available"}, CheckedAt: now.Format(time.RFC3339), Mode: "manual_host_handoff", Items: []updates.Candidate{}}, handoff: updates.Handoff{Schema: 2, Status: "awaiting_host_operator", Intent: updates.Intent{Schema: 2, DistributionFormat: updates.ArchiveDistribution, RequestID: ID(), CreatedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Repository: updates.Repository, RepositoryID: updates.RepositoryID, Component: "core", Tag: "v0.4.1", Version: "0.4.1", SourceSHA: strings.Repeat("a", 40), ReleaseID: 80, RunID: 100, RunAttempt: 2, CurrentCoreVersion: current.CoreVersion, CurrentPluginVersion: current.SMTPVersion, PluginPolicyRevision: current.PluginPolicyRevision, Assets: assets}}}
}
func updateRequest() map[string]any {
	return map[string]any{"component": "core", "tag": "v0.4.1", "source_sha": strings.Repeat("a", 40), "release_id": 80, "catalog_sha256": strings.Repeat("f", 64), "acknowledge_manual_handoff": true}
}

func TestUpdateAdminAuthorizationCSRFStrictBodyAndNoSilentWork(t *testing.T) {
	f := updateFixture()
	m := NewMemory()
	app := NewApp(m, Config{Updates: f, Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	for _, tc := range []struct {
		client *testClient
		want   int
	}{{anonymous, 401}, {customer, 403}} {
		for _, route := range []struct{ method, path string }{{"GET", "/api/v1/admin/updates"}, {"POST", "/api/v1/admin/updates/check"}, {"POST", "/api/v1/admin/updates/handoff"}} {
			if status, _ := tc.client.request(route.method, route.path, updateRequest(), ""); status != tc.want {
				t.Fatal("authorization", status, tc.want)
			}
		}
	}
	if f.checks != 0 || f.prepares != 0 {
		t.Fatal("unauthorized provider work")
	}
	if status, _ := admin.request("GET", "/api/v1/admin/updates", nil, ""); status != 200 || f.checks != 0 {
		t.Fatal("GET caused check", status)
	}
	for _, path := range []string{"/api/v1/admin/updates/check", "/api/v1/admin/updates/handoff"} {
		if status, _ := admin.request("GET", path, nil, ""); status != 405 {
			t.Fatal("unsafe method", status)
		}
	}
	old := admin.csrf
	admin.csrf = "invalid"
	if status, _ := admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, ""); status != 403 {
		t.Fatal("csrf", status)
	}
	admin.csrf = old
	for _, raw := range []string{`null`, `[]`, `{"url":"https://evil.invalid"}`, `{} {}`, `{"tag":"v0.4.1","tag":"v0.4.2"}`, `{"Tag":"v0.4.1"}`, `{"acknowledge_manual_handoff":null}`, `{"script":{"command":"id"}}`} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/updates/handoff", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("ambiguous body", raw, res.StatusCode)
		}
	}
	input := updateRequest()
	input["acknowledge_manual_handoff"] = false
	if status, _ := admin.request("POST", "/api/v1/admin/updates/handoff", input, ""); status != 400 || f.prepares != 0 {
		t.Fatal("implicit handoff", status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, ""); status != 200 || f.checks != 1 {
		t.Fatal("explicit check", status)
	}
	status, out := admin.request("POST", "/api/v1/admin/updates/handoff", updateRequest(), "")
	if status != 202 || out["data"].(map[string]any)["status"] != "awaiting_host_operator" {
		t.Fatal(status, out)
	}
	audits, err := m.Audits(context.Background(), "")
	if err != nil || len(audits) != 5 {
		t.Fatal("durable request/asset audits", len(audits), err)
	}
	found := false
	for _, audit := range audits {
		if len(audit.Detail) > 1000 {
			t.Fatal("audit schema exceeded")
		}
		if audit.Action == "update.handoff_requested" {
			found = true
			if audit.ID != f.handoff.Intent.RequestID || !strings.Contains(audit.Detail, `"plugin_policy_revision":3`) {
				t.Fatal(audit)
			}
		} else if audit.Action != "update.asset_verified" || audit.Target != f.handoff.Intent.RequestID {
			t.Fatal("asset not bound", audit)
		}
	}
	if !found {
		t.Fatal("missing request audit")
	}
	if status, _ := admin.request("GET", "/healthz", nil, ""); status != 200 {
		t.Fatal("core failed after handoff")
	}
}

type failedUpdateAudit struct{ Store }

func (failedUpdateAudit) RecordUpdateHandoff(context.Context, User, updates.Intent) error {
	return errors.New("database unavailable SECRET-MUST-NOT-LEAK")
}
func TestUpdateAuditFailureWithholdsHandoffAndRedactsProvider(t *testing.T) {
	f := updateFixture()
	app := NewApp(failedUpdateAudit{NewMemory()}, Config{Updates: f, Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	status, out := admin.request("POST", "/api/v1/admin/updates/handoff", updateRequest(), "")
	raw, _ := json.Marshal(out)
	if status != 500 || out["data"] != nil || strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "awaiting_host_operator") {
		t.Fatal(status, out)
	}
	f.err = errors.New("provider response token=SECRET-MUST-NOT-LEAK")
	status, out = admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, "")
	raw, _ = json.Marshal(out)
	if status != 503 || strings.Contains(string(raw), "SECRET") || out["error"].(map[string]any)["code"] != "UPDATE_UNAVAILABLE" {
		t.Fatal(status, out)
	}
	for _, tc := range []struct {
		err  error
		code string
	}{{updates.ErrBusy, "UPDATE_BUSY"}, {updates.ErrStale, "UPDATE_STALE"}, {updates.ErrIncompatible, "UPDATE_INCOMPATIBLE"}} {
		f.err = tc.err
		status, out = admin.request("POST", "/api/v1/admin/updates/handoff", updateRequest(), "")
		if status != 409 || out["error"].(map[string]any)["code"] != tc.code {
			t.Fatal(status, out)
		}
	}
}

func TestUpdateUnavailableDefaultDoesNotBreakCore(t *testing.T) {
	app := NewApp(NewMemory(), Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	status, out := admin.request("GET", "/api/v1/admin/updates", nil, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	view := out["data"].(map[string]any)
	if view["source"].(map[string]any)["status"] != "unavailable" || view["current"].(map[string]any)["core_version"] != pluginapi.CoreVersion {
		t.Fatal(out)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, ""); status != 503 {
		t.Fatal(status)
	}
}

func TestMySQLUpdateAuditAtomicWithinExistingSchema(t *testing.T) {
	m, ctx := migrationTestDatabase(t)
	if err := m.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	actor := User{ID: ID(), Email: ID() + "@updates.invalid", Role: "admin", Name: "Update fixture", PasswordHash: "unused"}
	if err := m.AddUser(ctx, actor); err != nil {
		t.Fatal(err)
	}
	intent := updateFixture().handoff.Intent
	intent.ActorID = actor.ID
	if err := m.RecordUpdateHandoff(ctx, actor, intent); err != nil {
		t.Fatal("real SQL audit", err)
	}
	audits, err := m.Audits(ctx, actor.ID)
	if err != nil || len(audits) != 5 {
		t.Fatal(len(audits), err)
	}
	if err := m.RecordUpdateHandoff(ctx, actor, intent); err == nil {
		t.Fatal("duplicate request committed")
	}
	audits, err = m.Audits(ctx, actor.ID)
	if err != nil || len(audits) != 5 {
		t.Fatal("duplicate left partial audits", len(audits), err)
	}
	invalid := actor
	invalid.ID = ID()
	intent.RequestID = ID()
	intent.ActorID = invalid.ID
	if err := m.RecordUpdateHandoff(ctx, invalid, intent); err == nil {
		t.Fatal("missing actor committed")
	}
	var count int
	if err := m.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE target=? OR id=?", intent.RequestID, intent.RequestID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed actor left partial audit", count, err)
	}
}

func TestUpdateAuditAcceptsOnlySchema2ExactDistributionLayouts(t *testing.T) {
	actor := User{ID: ID(), Role: "admin", Email: "update-layout@example.invalid"}
	for _, component := range []string{"core", "smtp"} {
		intent := updateFixture().handoff.Intent
		intent.ActorID = actor.ID
		intent.Component = component
		intent.DistributionFormat = updates.RawDistribution
		intent.Version = "0.5.0"
		intent.Tag = "v0.5.0"
		names := []string{"aster-api-0.5.0-linux-amd64", "aster-plugin-host-0.5.0-linux-amd64", "aster-core-0.5.0-linux-amd64-resources.tar.gz", "BUILD.json", "PROVENANCE.json", "SHA256SUMS"}
		if component == "smtp" {
			intent.Version = "1.1.0"
			intent.Tag = "smtp-v1.1.0"
			names = []string{"aster-smtp-1.1.0-linux-amd64", "aster-smtp-1.1.0-linux-amd64-resources.tar.gz", "BUILD.json", "PROVENANCE.json", "SHA256SUMS"}
		}
		intent.Assets = []updates.Asset{}
		for i, name := range names {
			intent.Assets = append(intent.Assets, updates.Asset{Name: name, ID: int64(200 + i), Size: 100, SHA256: strings.Repeat("a", 64)})
		}
		audits, err := updateAudits(actor, intent)
		if err != nil || len(audits) != len(names)+1 || !strings.Contains(audits[0].Detail, `"distribution_format":"raw-binaries-v1"`) {
			t.Fatal(component, audits, err)
		}
		for _, defect := range []string{"schema1", "format_archive", "format_unknown", "missing", "extra", "name", "hash", "duplicate_id"} {
			changed := intent
			changed.Assets = append([]updates.Asset{}, intent.Assets...)
			switch defect {
			case "schema1":
				changed.Schema = 1
			case "format_archive":
				changed.DistributionFormat = updates.ArchiveDistribution
			case "format_unknown":
				changed.DistributionFormat = "run-script-v1"
			case "missing":
				changed.Assets = changed.Assets[1:]
			case "extra":
				changed.Assets = append(changed.Assets, updates.Asset{Name: "project-source.tar.gz", ID: 900, Size: 100, SHA256: strings.Repeat("a", 64)})
			case "name":
				changed.Assets[0].Name = "arbitrary-program"
			case "hash":
				changed.Assets[0].SHA256 = "invalid"
			case "duplicate_id":
				changed.Assets[0].ID = changed.Assets[1].ID
			}
			if _, err := updateAudits(actor, changed); err == nil {
				t.Fatal("invalid update request persisted", component, defect)
			}
		}
	}
}
