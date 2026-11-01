package updates

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func newPairedFixture() *catalogFixture {
	f := newFixture("core")
	source := strings.Repeat("a", 40)
	version := "0.5.10"
	tag := "v" + version
	frontArchive := []byte("frontend archive not fetched by web")
	backArchive := []byte("backend archive not fetched by web")
	pin := map[string]any{"schema": 1, "repository": frontendRepository, "repository_id": frontendRepositoryID, "source_sha": strings.Repeat("b", 40), "core_version": version, "frontend_version": "0.5.9", "compatibility_sha256": strings.Repeat("c", 64), "artifact_sha256": strings.Repeat("d", 64), "archive_sha256": digestBytes(frontArchive), "artifact_id": 10, "run_id": 11, "run_attempt": 1}
	bn := "aster-finance-backend-" + version + "-linux-amd64.tar.gz"
	fn := "aster-finance-frontend-0.5.9.tar.gz"
	front := map[string]any{"schema": 1, "repository": frontendRepository, "repository_id": frontendRepositoryID, "source_commit": pin["source_sha"], "format": "static-site-tar-v1", "version": "0.5.9", "core_version": version, "asset": map[string]any{"name": fn, "size": len(frontArchive), "sha256": digestBytes(frontArchive)}}
	frontBytes := marshal(front)
	pin["manifest_sha256"] = digestBytes(frontBytes)
	back := map[string]any{"schema": 1, "repository": Repository, "repository_id": RepositoryID, "source_commit": source, "format": "backend-tar-v1", "version": version, "frontend_pair": pin, "asset": map[string]any{"name": bn, "size": len(backArchive), "sha256": digestBytes(backArchive)}}
	build := map[string]any{"schema": 4, "distribution_format": PairedDistribution, "release_kind": "core", "release_label": version, "source_commit": source, "target": "linux/amd64", "cgo_enabled": false, "frontend_pair": pin, "component_versions": map[string]any{"core": version}}
	files := map[string][]byte{bn: backArchive, fn: frontArchive, "BUILD.json": marshal(build), "BACKEND.json": marshal(back), "FRONTEND.json": frontBytes}
	inventory := map[string]any{}
	for name, raw := range files {
		inventory[name] = map[string]any{"size": len(raw), "sha256": digestBytes(raw)}
	}
	proof := map[string]any{"schema": 2, "repository": Repository, "repository_id": RepositoryID, "source_sha": source, "workflow_path": releaseWorkflow, "workflow_sha": source, "job": "release", "event": "push", "dry_run": false, "tag": tag, "version": version, "run_id": 100, "run_attempt": 2, "frontend_pair": pin, "assets": inventory}
	files["PROVENANCE.json"] = marshal(proof)
	names := []string{}
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	sums := ""
	for _, name := range names {
		sums += digestBytes(files[name]) + "  " + name + "\n"
	}
	files["SHA256SUMS"] = []byte(sums)
	names = append(names, "SHA256SUMS")
	assets := []any{}
	f.assets = map[string][]byte{}
	for i, name := range names {
		id := int64(300 + i)
		path := "/repos/" + Repository + "/releases/assets/" + stringNumber(id)
		raw := files[name]
		assets = append(assets, map[string]any{"id": id, "name": name, "size": len(raw), "digest": "sha256:" + digestBytes(raw), "state": "uploaded", "url": "https://api.github.com" + path, "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name})
		if !strings.HasSuffix(name, ".tar.gz") {
			f.assets[path] = raw
		}
	}
	f.release["tag_name"] = tag
	f.release["html_url"] = officialURL(tag)
	f.release["target_commitish"] = source
	f.release["assets"] = assets
	prefix := "/repos/" + Repository
	f.values[prefix+"/releases?per_page=20"] = marshal([]any{f.release})
	f.values[prefix+"/git/ref/tags/"+tag] = marshal(map[string]any{"ref": "refs/tags/" + tag, "object": map[string]any{"type": "commit", "sha": source}})
	f.values[prefix+"/compare/"+source+"...main"] = marshal(map[string]any{"status": "identical", "merge_base_commit": map[string]any{"sha": source}})
	pinBytes := marshal(pin)
	blob := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(pinBytes))), pinBytes...))
	f.values[prefix+"/contents/FRONTEND_PAIR.json?ref="+source] = marshal(map[string]any{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString(pinBytes), "size": len(pinBytes), "sha": hex.EncodeToString(blob[:])})
	path := prefix + "/actions/runs/100/attempts/2"
	decoded, _ := strictJSON(f.values[path])
	run := decoded.(map[string]any)
	run["head_branch"] = tag
	f.values[path] = marshal(run)
	f.values[path+"/jobs?per_page=100"] = marshal(map[string]any{"total_count": 1, "jobs": []any{map[string]any{"name": "release", "run_id": 100, "run_attempt": 2, "head_sha": source, "status": "completed", "conclusion": "success"}}})
	return f
}

func TestPairedReleaseMetadataAndHandoff(t *testing.T) {
	f := newPairedFixture()
	current := Current{CoreVersion: "0.5.2"}
	s := fixtureService(f, &current)
	view, err := s.Check(context.Background())
	if err != nil || len(view.Items) != 1 {
		t.Fatal(view, err)
	}
	item := view.Items[0]
	if item.DistributionFormat != PairedDistribution || !item.Compatible || !item.UpdateAvailable || len(item.Assets) != 7 {
		t.Fatal(item)
	}
	handoff, err := s.Prepare(context.Background(), strings.Repeat("f", 32), "core", item.Tag, item.SourceSHA, item.ReleaseID, item.CatalogSHA256)
	if err != nil || handoff.Intent.RepositoryID != 1435344087 || !ValidDistributionAssets("core", item.Version, PairedDistribution, handoff.Intent.Assets) {
		t.Fatal(handoff, err)
	}
	for _, call := range f.calls {
		if strings.Contains(call, "/betteridcsys/") {
			t.Fatal("old origin fetched", call)
		}
	}
}

func TestPairedMetadataRejectsUnrelatedPolicyRunPinAndJob(t *testing.T) {
	prefix := "/repos/" + Repository
	cases := []struct {
		path, key string
		value     any
	}{
		{prefix, "id", 1418618420},
		{prefix + "/actions/runs/100/attempts/2", "event", "workflow_dispatch"},
		{prefix + "/actions/runs/100/attempts/2", "run_attempt", 3},
		{prefix + "/actions/runs/100/attempts/2", "path", ".github/workflows/ci.yml"},
		{prefix + "/actions/runs/100/attempts/2", "head_sha", strings.Repeat("f", 40)},
		{prefix + "/actions/runs/100/attempts/2/jobs?per_page=100", "jobs", []any{map[string]any{"name": "other", "conclusion": "success"}}},
		{prefix + "/contents/FRONTEND_PAIR.json?ref=" + strings.Repeat("a", 40), "content", base64.StdEncoding.EncodeToString([]byte("{}"))},
		{prefix + "/compare/" + strings.Repeat("a", 40) + "...main", "status", "diverged"},
	}
	for _, tc := range cases {
		t.Run(tc.key+tc.path, func(t *testing.T) {
			f := newPairedFixture()
			value, _ := strictJSON(f.values[tc.path])
			m := value.(map[string]any)
			m[tc.key] = tc.value
			f.values[tc.path] = marshal(m)
			current := Current{CoreVersion: "0.5.2"}
			_, err := fixtureService(f, &current).Check(context.Background())
			if err == nil {
				t.Fatal("invalid pair accepted")
			}
		})
	}
}

func TestPairedDistributionRejectsDuplicateOrUnboundedFrontendAssets(t *testing.T) {
	f := newPairedFixture()
	current := Current{CoreVersion: "0.5.2"}
	view, err := fixtureService(f, &current).Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assets := view.Items[0].Assets
	for i := range assets {
		bad := append([]Asset{}, assets...)
		bad[i].Name = "aster-finance-frontend-latest.tar.gz"
		if ValidDistributionAssets("core", "0.5.10", PairedDistribution, bad) {
			t.Fatal("invalid name accepted")
		}
	}
	if ValidDistributionAssets("smtp", "0.5.10", PairedDistribution, assets) {
		t.Fatal("SMTP used paired core format")
	}
}
