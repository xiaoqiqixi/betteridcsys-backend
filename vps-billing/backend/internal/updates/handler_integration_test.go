package updates_test

import (
	"aster/billing/internal/billing"
	"aster/billing/internal/updates"
	"aster/billing/migrations"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func migrationSources() []string {
	return []string{migrations.Initial, migrations.SiteSettings, migrations.Currencies, migrations.BillingSnapshots, migrations.ExactCurrencyCodes, migrations.EmailSettings}
}

type integrationClient struct {
	t    *testing.T
	base string
	http *http.Client
	csrf string
}

func (c *integrationClient) request(method, path string, value any, _ string) (int, map[string]any) {
	c.t.Helper()
	var raw bytes.Buffer
	if value != nil {
		if err := json.NewEncoder(&raw).Encode(value); err != nil {
			c.t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(method, c.base+path, &raw)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		c.t.Fatal(err)
	}
	return res.StatusCode, out
}
func newIntegrationClient(t *testing.T, base string) *integrationClient {
	jar, _ := cookiejar.New(nil)
	c := &integrationClient{t: t, base: base, http: &http.Client{Jar: jar}}
	_, out := c.request("GET", "/api/v1/session", nil, "")
	c.csrf = out["data"].(map[string]any)["csrf_token"].(string)
	status, out := c.request("POST", "/api/v1/auth/login", map[string]string{"email": "admin@aster.local", "password": "DemoAdmin!2026"}, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	c.csrf = out["data"].(map[string]any)["csrf_token"].(string)
	return c
}

type updateRoundTrip func(*http.Request) (*http.Response, error)

func (f updateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUpdateHTTPHandoffWithOfficialProtocolLoopbackFixture(t *testing.T) {
	// The production service and billing handler run unchanged. A trusted Go
	// constructor redirects their fixed HTTPS GET transport to an isolated local
	// provider fixture; there is no runtime source/URL environment override.
	encode := func(value any) []byte { raw, _ := json.Marshal(value); return raw }
	hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	source := strings.Repeat("a", 40)
	tag := "v0.4.1"
	archive := "aster-0.4.1-linux-amd64.tar.gz"
	base := "/repos/" + updates.Repository
	archiveBytes := []byte("web must not download or execute this archive")
	build := encode(map[string]any{"schema": 2, "release_kind": "core", "release_label": "0.4.1", "source_commit": source, "target": "linux/amd64", "cgo_enabled": false, "component_versions": map[string]string{"core": "0.4.1", "smtp": "1.0.0"}, "plugin_protocol": "aster.smtp/1", "smtp_core_compatibility": map[string]string{"min": "0.4.0", "max_exclusive": "0.5.0"}})
	prov := encode(map[string]any{"schema": 1, "repository": updates.Repository, "repository_id": updates.RepositoryID, "tag": tag, "version": "0.4.1", "source_sha": source, "run_id": 100, "run_attempt": 2, "event": "push", "workflow_path": ".github/workflows/release-build.yml", "workflow_sha": source, "run_head_sha": source, "default_branch": "main", "default_sha": source, "artifact_name": "candidate-100-2", "trusted_blobs": map[string]string{".github/workflows/release-build.yml": strings.Repeat("d", 40)}, "assets": map[string]any{archive: map[string]any{"sha256": hash(archiveBytes), "size": len(archiveBytes)}, "BUILD.json": map[string]any{"sha256": hash(build), "size": len(build)}}})
	sums := []byte(hash(build) + "  BUILD.json\n" + hash(prov) + "  PROVENANCE.json\n" + hash(archiveBytes) + "  " + archive + "\n")
	assetData := map[string][]byte{archive: archiveBytes, "BUILD.json": build, "PROVENANCE.json": prov, "SHA256SUMS": sums}
	assets := []any{}
	payloads := map[string][]byte{}
	for i, name := range []string{archive, "BUILD.json", "PROVENANCE.json", "SHA256SUMS"} {
		id := int64(200 + i)
		path := base + "/releases/assets/" + strconv.FormatInt(id, 10)
		assets = append(assets, map[string]any{"name": name, "id": id, "size": len(assetData[name]), "digest": "sha256:" + hash(assetData[name]), "state": "uploaded", "url": "https://api.github.com" + path, "browser_download_url": "https://github.com/" + updates.Repository + "/releases/download/" + tag + "/" + name})
		payloads[path] = assetData[name]
	}
	release := map[string]any{"id": 80, "tag_name": tag, "draft": false, "prerelease": false, "html_url": "https://github.com/" + updates.Repository + "/releases/tag/" + tag, "published_at": "2026-10-14T12:00:00Z", "body": "Synthetic official notes", "assets": assets}
	payloads[base] = encode(map[string]any{"id": updates.RepositoryID, "full_name": updates.Repository})
	payloads[base+"/releases?per_page=20"] = encode([]any{release})
	payloads[base+"/git/ref/tags/"+tag] = encode(map[string]any{"ref": "refs/tags/" + tag, "object": map[string]string{"type": "commit", "sha": source}})
	payloads[base+"/actions/runs/100/attempts/2"] = encode(map[string]any{"id": 100, "run_attempt": 2, "status": "completed", "conclusion": "success", "path": ".github/workflows/release-build.yml", "repository": map[string]any{"id": updates.RepositoryID, "full_name": updates.Repository}, "head_repository": map[string]any{"id": updates.RepositoryID, "full_name": updates.Repository}, "head_sha": source, "head_branch": tag, "event": "push", "workflow_id": 7})
	payloads[base+"/actions/workflows/release-build.yml"] = encode(map[string]any{"id": 7, "path": ".github/workflows/release-build.yml", "state": "active"})
	var fixtureMu sync.Mutex
	providerRequests := 0
	archiveRequests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixtureMu.Lock()
		defer fixtureMu.Unlock()
		providerRequests++
		if r.Method != "GET" || r.Host != "api.github.com" {
			http.Error(w, "unexpected request", 500)
			return
		}
		if r.URL.Path == base+"/releases/assets/200" {
			archiveRequests++
			http.Error(w, "web archive download forbidden", 500)
			return
		}
		raw, ok := payloads[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(raw)
	}))
	defer provider.Close()
	loopback, _ := url.Parse(provider.URL)
	transport := updateRoundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			return nil, errors.New("official source changed")
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = loopback.Scheme, loopback.Host
		clone.URL = &u
		clone.Host = "api.github.com"
		return http.DefaultTransport.RoundTrip(clone)
	})
	current := updates.Current{CoreVersion: "0.4.0", SMTPVersion: "1.0.0", SMTPStatus: "approved", SMTPProtocol: "aster.smtp/1", SMTPCoreMin: "0.4.0", SMTPCoreMaxExclusive: "0.5.0", PluginPolicyRevision: 3}
	service := updates.NewForTransportTest("synthetic-never-real-read-token", func() updates.Current { return current }, transport)
	m := billing.NewMemory()
	beforeSettings, _ := m.EmailSettings(context.Background())
	beforeSchema := migrationSources()
	marker := filepath.Join(t.TempDir(), "existing-core-binary")
	os.WriteFile(marker, []byte("unchanged core runtime bytes"), 0600)
	beforeBytes, _ := os.ReadFile(marker)
	app := billing.NewApp(m, billing.Config{Updates: service, Demo: true, Origin: "http://localhost", Secret: "synthetic-local-fixture-session-secret-0123456789"})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	admin := newIntegrationClient(t, srv.URL)
	if status, _ := admin.request("GET", "/api/v1/admin/updates", nil, ""); status != 200 {
		t.Fatal(status)
	}
	fixtureMu.Lock()
	hits := providerRequests
	fixtureMu.Unlock()
	if hits != 0 {
		t.Fatal("local status made external request")
	}
	status, out := admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, "")
	if status != 200 {
		t.Fatal(status, out)
	}
	candidate := out["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
	input := map[string]any{"component": candidate["component"], "tag": candidate["tag"], "source_sha": candidate["source_sha"], "release_id": candidate["release_id"], "catalog_sha256": candidate["catalog_sha256"], "acknowledge_manual_handoff": true}
	status, out = admin.request("POST", "/api/v1/admin/updates/handoff", input, "")
	if status != 202 || out["data"].(map[string]any)["status"] != "awaiting_host_operator" {
		t.Fatal(status, out)
	}
	audits, err := m.Audits(context.Background(), "")
	if err != nil || len(audits) != 5 {
		t.Fatal("no persisted real handoff", audits, err)
	}
	fixtureMu.Lock()
	release["id"] = 81
	payloads[base+"/releases?per_page=20"] = encode([]any{release})
	fixtureMu.Unlock()
	status, out = admin.request("POST", "/api/v1/admin/updates/handoff", input, "")
	if status != 409 || out["error"].(map[string]any)["code"] != "UPDATE_STALE" {
		t.Fatal("silently changed target", status, out)
	}
	afterAudits, _ := m.Audits(context.Background(), "")
	afterSettings, _ := m.EmailSettings(context.Background())
	afterBytes, _ := os.ReadFile(marker)
	fixtureMu.Lock()
	downloads := archiveRequests
	fixtureMu.Unlock()
	if downloads != 0 || len(afterAudits) != 5 || beforeSettings != afterSettings || hash(beforeBytes) != hash(afterBytes) || len(beforeSchema) != len(migrationSources()) {
		t.Fatal("handoff performed an installation/schema change or failed audit", downloads, len(afterAudits))
	}
	for i := range beforeSchema {
		if beforeSchema[i] != migrationSources()[i] {
			t.Fatal("schema changed")
		}
	}
	if status, _ := admin.request("GET", "/healthz", nil, ""); status != 200 {
		t.Fatal("core failed after handoff", status)
	}
}

func TestDistributionHTTPCheckAndAuditedSchema2Handoff(t *testing.T) {
	for _, component := range []string{"core", "smtp"} {
		for _, format := range []string{updates.ArchiveDistribution, updates.RawDistribution} {
			t.Run(component+"/"+format, func(t *testing.T) {
				payloads, forbidden := updates.DistributionHTTPFixtureForTest(component, format)
				var mu sync.Mutex
				programFetches := 0
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					defer mu.Unlock()
					if r.Method != "GET" || r.Host != "api.github.com" {
						http.Error(w, "unapproved provider request", 500)
						return
					}
					if forbidden[r.URL.RequestURI()] {
						programFetches++
						http.Error(w, "program and resource downloads forbidden", 500)
						return
					}
					raw, ok := payloads[r.URL.RequestURI()]
					if !ok {
						http.NotFound(w, r)
						return
					}
					w.Write(raw)
				}))
				defer provider.Close()
				loopback, _ := url.Parse(provider.URL)
				transport := updateRoundTrip(func(r *http.Request) (*http.Response, error) {
					if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
						return nil, errors.New("official source changed")
					}
					clone := r.Clone(r.Context())
					u := *r.URL
					u.Scheme, u.Host = loopback.Scheme, loopback.Host
					clone.URL = &u
					clone.Host = "api.github.com"
					return http.DefaultTransport.RoundTrip(clone)
				})
				current := updates.Current{CoreVersion: "0.4.0", SMTPVersion: "1.0.0", SMTPStatus: "approved", SMTPProtocol: "aster.smtp/1", SMTPCoreMin: "0.4.0", SMTPCoreMaxExclusive: "0.6.0", PluginPolicyRevision: 3}
				service := updates.NewForTransportTest("synthetic-read-credential-never-real", func() updates.Current { return current }, transport)
				store := billing.NewMemory()
				beforeEmail, _ := store.EmailSettings(context.Background())
				app := billing.NewApp(store, billing.Config{Updates: service, Demo: true, Origin: "http://localhost", Secret: "synthetic-local-fixture-session-secret-0123456789"})
				srv := httptest.NewServer(app.Handler())
				defer srv.Close()
				admin := newIntegrationClient(t, srv.URL)
				status, out := admin.request("POST", "/api/v1/admin/updates/check", map[string]any{}, "")
				if status != 200 {
					t.Fatal(status, out)
				}
				candidate := out["data"].(map[string]any)["items"].([]any)[0].(map[string]any)
				if candidate["distribution_format"] != format {
					t.Fatal("distribution lost", candidate)
				}
				input := map[string]any{"component": candidate["component"], "tag": candidate["tag"], "source_sha": candidate["source_sha"], "release_id": candidate["release_id"], "catalog_sha256": candidate["catalog_sha256"], "acknowledge_manual_handoff": true}
				status, out = admin.request("POST", "/api/v1/admin/updates/handoff", input, "")
				if status != 202 {
					t.Fatal(status, out)
				}
				handoff := out["data"].(map[string]any)
				intent := handoff["intent"].(map[string]any)
				expected := 4
				if format == updates.RawDistribution {
					expected = 6
					if component == "smtp" {
						expected = 5
					}
				}
				if handoff["schema"] != float64(2) || intent["schema"] != float64(2) || intent["distribution_format"] != format || handoff["status"] != "awaiting_host_operator" || len(intent["assets"].([]any)) != expected {
					t.Fatal(handoff)
				}
				audits, err := store.Audits(context.Background(), "")
				if err != nil || len(audits) != expected+1 {
					t.Fatal("audit schema/layout mismatch", len(audits), err)
				}
				afterEmail, _ := store.EmailSettings(context.Background())
				mu.Lock()
				fetched := programFetches
				mu.Unlock()
				if beforeEmail != afterEmail || fetched != 0 {
					t.Fatal("update handoff performed an external email or program download")
				}
				if status, _ := admin.request("GET", "/healthz", nil, ""); status != 200 {
					t.Fatal("core not healthy after handoff")
				}
			})
		}
	}
}
