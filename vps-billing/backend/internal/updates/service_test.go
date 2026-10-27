package updates

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type catalogFixture struct {
	values  map[string][]byte
	assets  map[string][]byte
	calls   []string
	mu      sync.Mutex
	release map[string]any
}

func marshal(v any) []byte { raw, _ := json.Marshal(v); return raw }
func newFixture(component string) *catalogFixture {
	version, tag, archive := "0.4.1", "v0.4.1", "aster-0.4.1-linux-amd64.tar.gz"
	if component == "smtp" {
		version, tag, archive = "1.1.0", "smtp-v1.1.0", "aster-smtp-1.1.0-linux-amd64.tar.gz"
	}
	source := strings.Repeat("a", 40)
	build := map[string]any{"schema": 2, "release_kind": component, "release_label": version, "source_commit": source, "target": "linux/amd64", "cgo_enabled": false, "component_versions": map[string]string{"core": "0.4.1", "smtp": "1.1.0"}, "plugin_protocol": "aster.smtp/1", "smtp_core_compatibility": map[string]string{"min": "0.4.0", "max_exclusive": "0.5.0"}}
	buildBytes := marshal(build)
	archiveBytes := []byte("archive deliberately never downloaded by web")
	prov := map[string]any{"schema": 1, "repository": Repository, "repository_id": RepositoryID, "tag": tag, "version": version, "source_sha": source, "run_id": 100, "run_attempt": 2, "event": "push", "workflow_path": ".github/workflows/release-build.yml", "workflow_sha": source, "run_head_sha": source, "default_branch": "main", "default_sha": source, "artifact_name": "candidate-100-2", "trusted_blobs": map[string]string{".github/workflows/release-build.yml": strings.Repeat("d", 40)}, "assets": map[string]any{archive: map[string]any{"sha256": digestBytes(archiveBytes), "size": len(archiveBytes)}, "BUILD.json": map[string]any{"sha256": digestBytes(buildBytes), "size": len(buildBytes)}}}
	provBytes := marshal(prov)
	sums := []byte(digestBytes(buildBytes) + "  BUILD.json\n" + digestBytes(provBytes) + "  PROVENANCE.json\n" + digestBytes(archiveBytes) + "  " + archive + "\n")
	data := map[string][]byte{archive: archiveBytes, "BUILD.json": buildBytes, "PROVENANCE.json": provBytes, "SHA256SUMS": sums}
	assets := []any{}
	assetData := map[string][]byte{}
	for i, name := range []string{archive, "BUILD.json", "PROVENANCE.json", "SHA256SUMS"} {
		id := int64(200 + i)
		path := "/repos/" + Repository + "/releases/assets/" + stringNumber(id)
		assets = append(assets, map[string]any{"name": name, "id": id, "size": len(data[name]), "digest": "sha256:" + digestBytes(data[name]), "state": "uploaded", "url": "https://api.github.com" + path, "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name})
		assetData[path] = data[name]
	}
	release := map[string]any{"id": 80, "tag_name": tag, "draft": false, "prerelease": false, "html_url": officialURL(tag), "published_at": "2026-10-14T12:00:00Z", "body": "Official plain-text notes <script>never render HTML</script>", "assets": assets}
	values := map[string][]byte{
		"/repos/" + Repository:                                          marshal(map[string]any{"id": RepositoryID, "full_name": Repository}),
		"/repos/" + Repository + "/releases?per_page=20":                marshal([]any{release}),
		"/repos/" + Repository + "/git/ref/tags/" + tag:                 marshal(map[string]any{"ref": "refs/tags/" + tag, "object": map[string]string{"type": "commit", "sha": source}}),
		"/repos/" + Repository + "/actions/runs/100/attempts/2":         marshal(map[string]any{"id": 100, "run_attempt": 2, "status": "completed", "conclusion": "success", "path": ".github/workflows/release-build.yml", "repository": map[string]any{"id": RepositoryID, "full_name": Repository}, "head_repository": map[string]any{"id": RepositoryID, "full_name": Repository}, "head_sha": source, "head_branch": tag, "event": "push", "workflow_id": 7}),
		"/repos/" + Repository + "/actions/workflows/release-build.yml": marshal(map[string]any{"id": 7, "path": ".github/workflows/release-build.yml", "state": "active"}),
	}
	return &catalogFixture{values: values, assets: assetData, release: release}
}
func stringNumber(v int64) string { return strconv.FormatInt(v, 10) }
func response(status int, raw []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), ContentLength: int64(len(raw))}
}
func (f *catalogFixture) transport(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.URL.String())
	if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
		return nil, errors.New("unexpected network operation")
	}
	key := r.URL.RequestURI()
	if raw, ok := f.values[key]; ok {
		return response(200, raw), nil
	}
	if raw, ok := f.assets[key]; ok {
		if strings.HasSuffix(key, "/200") {
			return nil, errors.New("web downloaded executable archive")
		}
		return response(200, raw), nil
	}
	return response(404, []byte("private provider failure TOKEN=must-never-leak")), nil
}
func fixtureService(f *catalogFixture, current *Current) *Service {
	s := New("synthetic-read-only-token-never-logged", func() Current { return *current })
	s.client.http.Transport = roundTrip(f.transport)
	return s
}

func TestCatalogAndExplicitPinnedHandoff(t *testing.T) {
	f := newFixture("core")
	current := Current{CoreVersion: "0.4.0", SMTPVersion: "1.0.0", SMTPStatus: "approved", SMTPProtocol: "aster.smtp/1", SMTPCoreMin: "0.4.0", SMTPCoreMaxExclusive: "0.5.0", PluginPolicyRevision: 3}
	s := fixtureService(f, &current)
	if snapshot := s.Snapshot(); snapshot.Source.Status != "not_checked" || len(f.calls) != 0 {
		t.Fatal("snapshot triggered network", snapshot)
	}
	v, err := s.Check(context.Background())
	if err != nil || v.Source.Status != "available" || len(v.Items) != 1 {
		t.Fatal(v, err)
	}
	c := v.Items[0]
	if !c.Compatible || !c.UpdateAvailable || len(c.Assets) != 4 || !hashPattern.MatchString(c.CatalogSHA256) {
		t.Fatal(c)
	}
	h, err := s.Prepare(context.Background(), strings.Repeat("b", 32), c.Component, c.Tag, c.SourceSHA, c.ReleaseID, c.CatalogSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if h.Status != "awaiting_host_operator" || h.Intent.SourceSHA != c.SourceSHA || h.Intent.PluginPolicyRevision != 3 || len(h.Intent.Assets) != 4 {
		t.Fatal(h)
	}
	issued, _ := time.Parse(time.RFC3339, h.Intent.CreatedAt)
	expires, _ := time.Parse(time.RFC3339, h.Intent.ExpiresAt)
	if expires.Sub(issued) != 30*time.Minute {
		t.Fatal("handoff lifetime")
	}
	if _, err := s.Prepare(context.Background(), strings.Repeat("b", 32), c.Component, c.Tag, strings.Repeat("c", 40), c.ReleaseID, c.CatalogSHA256); !errors.Is(err, ErrStale) {
		t.Fatal("source changed", err)
	}
	if _, err := s.Prepare(context.Background(), strings.Repeat("b", 32), c.Component, c.Tag, c.SourceSHA, c.ReleaseID, strings.Repeat("0", 64)); !errors.Is(err, ErrStale) {
		t.Fatal("assets changed", err)
	}
	current.PluginPolicyRevision++
	if v := s.Snapshot(); v.Source.Status != "not_checked" || len(v.Items) != 0 || v.CheckedAt != "" {
		t.Fatal("stale prechecks exposed", v)
	}
}

func TestOfficialCatalogRejectsUntrustedFacts(t *testing.T) {
	for _, name := range []string{"repository", "private", "foreign_url", "extra_asset", "duplicate_asset", "digest", "checksum", "provenance", "tag_moved", "failed_run", "foreign_run", "workflow", "draft", "prerelease", "bad_version"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture("core")
			base := "/repos/" + Repository
			current := Current{CoreVersion: "0.4.0", SMTPStatus: "not_installed"}
			switch name {
			case "repository":
				f.values[base] = marshal(map[string]any{"id": RepositoryID + 1, "full_name": Repository})
			case "private":
				delete(f.values, base)
			case "foreign_url":
				f.release["html_url"] = "https://github.com/attacker/repo/releases/tag/v0.4.1"
			case "extra_asset":
				f.release["assets"] = append(f.release["assets"].([]any), map[string]any{"name": "install.sh"})
			case "duplicate_asset":
				assets := f.release["assets"].([]any)
				assets[3] = assets[2]
			case "digest":
				f.release["assets"].([]any)[1].(map[string]any)["digest"] = "sha256:" + strings.Repeat("0", 64)
			case "checksum":
				f.assets[base+"/releases/assets/203"] = []byte("bad sums")
			case "provenance":
				f.assets[base+"/releases/assets/202"] = []byte(`{"repository":"attacker/repo"}`)
			case "tag_moved":
				f.values[base+"/git/ref/tags/v0.4.1"] = marshal(map[string]any{"ref": "refs/tags/v0.4.1", "object": map[string]string{"type": "commit", "sha": strings.Repeat("f", 40)}})
			case "failed_run", "foreign_run", "workflow":
				key := base + "/actions/runs/100/attempts/2"
				var value map[string]any
				json.Unmarshal(f.values[key], &value)
				if name == "failed_run" {
					value["conclusion"] = "failure"
				}
				if name == "foreign_run" {
					value["head_repository"] = map[string]any{"id": 12, "full_name": "attacker/repo"}
				}
				if name == "workflow" {
					value["workflow_id"] = 999
				}
				f.values[key] = marshal(value)
			case "draft":
				f.release["draft"] = true
			case "prerelease":
				f.release["prerelease"] = true
			case "bad_version":
				f.release["tag_name"] = "v0.4.1;echo-bad"
			}
			f.values[base+"/releases?per_page=20"] = marshal([]any{f.release})
			s := fixtureService(f, &current)
			v, err := s.Check(context.Background())
			if name == "draft" || name == "prerelease" || name == "bad_version" {
				if err != nil || len(v.Items) != 0 {
					t.Fatal("nonstable release selected", v, err)
				}
				return
			}
			if !errors.Is(err, ErrUnavailable) || v.Source.Status != "unavailable" || len(v.Items) != 0 {
				t.Fatal(v, err)
			}
			raw := string(marshal(v))
			if strings.Contains(raw, "TOKEN") || strings.Contains(raw, "provider failure") {
				t.Fatal("provider payload leaked")
			}
		})
	}
}

func TestSMTPCompatibilityAndNoDowngrade(t *testing.T) {
	for _, tc := range []struct {
		core, installed   string
		compatible, newer bool
	}{{"0.4.0", "1.0.0", true, true}, {"0.5.0", "1.0.0", false, true}, {"0.4.0", "1.1.0", true, false}, {"0.4.0", "1.2.0", true, false}, {"0.4.0", "", true, true}} {
		f := newFixture("smtp")
		current := Current{CoreVersion: tc.core, SMTPVersion: tc.installed, SMTPStatus: "approved"}
		s := fixtureService(f, &current)
		v, err := s.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		c := v.Items[0]
		if c.Compatible != tc.compatible || c.UpdateAvailable != tc.newer {
			t.Fatal(tc, c)
		}
		if !tc.compatible || !tc.newer {
			if _, err := s.Prepare(context.Background(), strings.Repeat("b", 32), c.Component, c.Tag, c.SourceSHA, c.ReleaseID, c.CatalogSHA256); !errors.Is(err, ErrIncompatible) {
				t.Fatal(err)
			}
		}
	}
}

func TestMetadataRedirectsAreSingleOfficialCredentialFreeHop(t *testing.T) {
	for _, tc := range []struct {
		location string
		second   bool
		want     bool
	}{{"https://release-assets.githubusercontent.com/path?synthetic=1", false, true}, {"https://release-assets.githubusercontent.com/path", true, false}, {"http://release-assets.githubusercontent.com/path", false, false}, {"https://evil.invalid/path", false, false}, {"https://user@release-assets.githubusercontent.com/path", false, false}, {"https://release-assets.githubusercontent.com:8443/path", false, false}, {"https://release-assets.githubusercontent.com/path#bad", false, false}} {
		g := newGitHubClient("synthetic-read-credential")
		calls := 0
		g.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				res := response(302, nil)
				res.Header.Set("Location", tc.location)
				res.Request = r
				return res, nil
			}
			if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("credential forwarded")
			}
			if tc.second {
				res := response(302, nil)
				res.Header.Set("Location", "https://release-assets.githubusercontent.com/second")
				return res, nil
			}
			return response(200, []byte("meta")), nil
		})
		_, err := g.fetch(context.Background(), "/releases/assets/1", true, 64)
		if (err == nil) != tc.want {
			t.Fatal(tc, err)
		}
		if calls > 2 {
			t.Fatal("redirect loop")
		}
	}
	g := newGitHubClient("")
	calls := 0
	g.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		res := response(302, nil)
		res.Header.Set("Location", "https://release-assets.githubusercontent.com/meta")
		return res, nil
	})
	if _, err := g.fetch(context.Background(), "", false, 64); err == nil || calls != 1 {
		t.Fatal("catalog redirected", err, calls)
	}
}

func TestStrictProviderJSONAndBounds(t *testing.T) {
	for _, raw := range []string{`{"id":1,"id":2}`, `{} {}`, `{"x":[[[[[[[[[[[[[[[[[[[]]]]]]]]]]]]]]]]]]]]}`, strings.Repeat("x", maxJSON+1), string([]byte{0xff})} {
		if _, err := strictJSON([]byte(raw)); err == nil {
			t.Fatal("ambiguous provider accepted")
		}
	}
	g := newGitHubClient("")
	g.http.Transport = roundTrip(func(*http.Request) (*http.Response, error) {
		return response(200, []byte(strings.Repeat("x", 65))), nil
	})
	if _, err := g.fetch(context.Background(), "/releases/assets/1", true, 64); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestCheckExclusiveAndCancellation(t *testing.T) {
	current := Current{CoreVersion: "0.4.0", SMTPStatus: "not_installed"}
	s := New("", func() Current { return current })
	entered := make(chan struct{})
	s.client.http.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Check(ctx); done <- err }()
	<-entered
	if _, err := s.Check(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatal("parallel check allowed", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}

func TestCredentialPrivateNoSymlinkHardlinkOrPayloadLeak(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "read-token")
	os.WriteFile(file, []byte("synthetic-read-only-token-012345"), 0400)
	// The cloud executor maps / to nobody; production requires UID 0. Verify
	// rejection here rather than adding any production trust bypass.
	root, _ := os.Stat("/tmp")
	if root.Sys().(*syscall.Stat_t).Uid == 0 {
		if token, err := LoadTokenFile(file); err != nil || token != "synthetic-read-only-token-012345" {
			t.Fatal("protected credential rejected", err)
		}
	} else if _, err := LoadTokenFile(file); err == nil {
		t.Fatal("untrusted cloud ancestor accepted")
	}
	if token, err := LoadTokenFile(""); err != nil || token != "" {
		t.Fatal(token, err)
	}
	for _, path := range []string{"relative", file + "/../read-token", file + "\n"} {
		if _, err := LoadTokenFile(path); err == nil {
			t.Fatal("bad credential path accepted")
		}
	}
}
