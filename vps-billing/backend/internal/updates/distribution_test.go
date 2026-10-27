package updates

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

func newRawFixture(component string) *catalogFixture {
	f := newFixture(component)
	version, tag := "0.5.1", "v0.5.1"
	if component == "smtp" {
		version, tag = "1.1.0", "smtp-v1.1.0"
	}
	layout, _ := releaseLayout(component, version, RawDistribution)
	source := strings.Repeat("a", 40)
	data := map[string][]byte{layout.ResourcesAsset: []byte("resources deliberately never fetched by Web")}
	binaries := map[string]string{}
	for logical, name := range layout.BinaryAssets {
		data[name] = []byte("raw program deliberately never fetched: " + logical)
		binaries[logical] = digestBytes(data[name])
	}
	versions := map[string]string{"core": "0.5.1", "smtp": "1.1.0"}
	build := map[string]any{"schema": 3, "distribution_format": RawDistribution, "release_kind": component, "release_label": version, "source_commit": source, "target": "linux/amd64", "cgo_enabled": false, "component_versions": versions, "plugin_protocol": "aster.smtp/1", "smtp_core_compatibility": map[string]string{"min": "0.4.0", "max_exclusive": "0.6.0"}, "binary_assets": layout.BinaryAssets, "binary_sha256": binaries, "resources_asset": layout.ResourcesAsset}
	data["BUILD.json"] = marshal(build)
	provAssets := map[string]any{}
	for name, raw := range data {
		provAssets[name] = map[string]any{"sha256": digestBytes(raw), "size": len(raw)}
	}
	prov := map[string]any{"schema": 1, "repository": Repository, "repository_id": RepositoryID, "tag": tag, "version": version, "source_sha": source, "run_id": 100, "run_attempt": 2, "event": "push", "workflow_path": ".github/workflows/release-build.yml", "workflow_sha": source, "run_head_sha": source, "default_branch": "main", "default_sha": source, "artifact_name": "candidate-100-2", "trusted_blobs": map[string]string{".github/workflows/release-build.yml": strings.Repeat("d", 40)}, "assets": provAssets}
	data["PROVENANCE.json"] = marshal(prov)
	names := []string{}
	for name := range data {
		names = append(names, name)
	}
	sort.Strings(names)
	sums := ""
	for _, name := range names {
		sums += digestBytes(data[name]) + "  " + name + "\n"
	}
	data["SHA256SUMS"] = []byte(sums)
	ordered := []string{}
	for _, name := range layout.BinaryAssets {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	ordered = append(ordered, layout.ResourcesAsset, "BUILD.json", "PROVENANCE.json", "SHA256SUMS")
	assets := []any{}
	f.assets = map[string][]byte{}
	for i, name := range ordered {
		id := int64(200 + i)
		path := "/repos/" + Repository + "/releases/assets/" + stringNumber(id)
		assets = append(assets, map[string]any{"name": name, "id": id, "size": len(data[name]), "digest": "sha256:" + digestBytes(data[name]), "state": "uploaded", "url": "https://api.github.com" + path, "browser_download_url": "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name})
		f.assets[path] = data[name]
	}
	oldTag := textFromFixture(f.release, "tag_name")
	f.release["assets"], f.release["tag_name"], f.release["html_url"] = assets, tag, officialURL(tag)
	base := "/repos/" + Repository
	delete(f.values, base+"/git/ref/tags/"+oldTag)
	f.values[base+"/git/ref/tags/"+tag] = marshal(map[string]any{"ref": "refs/tags/" + tag, "object": map[string]string{"type": "commit", "sha": source}})
	var run map[string]any
	json.Unmarshal(f.values[base+"/actions/runs/100/attempts/2"], &run)
	run["head_branch"] = tag
	f.values[base+"/actions/runs/100/attempts/2"] = marshal(run)
	f.values[base+"/releases?per_page=20"] = marshal([]any{f.release})
	return f
}

func textFromFixture(m map[string]any, key string) string { value, _ := m[key].(string); return value }
func fixtureAsset(f *catalogFixture, name string) map[string]any {
	for _, value := range f.release["assets"].([]any) {
		asset := value.(map[string]any)
		if asset["name"] == name {
			return asset
		}
	}
	return nil
}
func fixtureObject(f *catalogFixture, name string) map[string]any {
	asset := fixtureAsset(f, name)
	var value map[string]any
	json.Unmarshal(f.assets["/repos/"+Repository+"/releases/assets/"+stringNumber(int64(asset["id"].(int64)))], &value)
	return value
}
func setFixtureObject(f *catalogFixture, name string, value map[string]any) {
	asset := fixtureAsset(f, name)
	path := "/repos/" + Repository + "/releases/assets/" + stringNumber(asset["id"].(int64))
	f.assets[path] = marshal(value)
}

// Reseal all metadata so each mutation is rejected for its semantic defect,
// not merely because the first small asset has an obsolete transport digest.
func resealFixture(f *catalogFixture) {
	base := "/repos/" + Repository + "/releases/assets/"
	prov := fixtureObject(f, "PROVENANCE.json")
	bindings := prov["assets"].(map[string]any)
	build := fixtureAsset(f, "BUILD.json")
	raw := f.assets[base+stringNumber(build["id"].(int64))]
	bindings["BUILD.json"] = map[string]any{"sha256": digestBytes(raw), "size": len(raw)}
	setFixtureObject(f, "PROVENANCE.json", prov)
	lines := []string{}
	for _, value := range f.release["assets"].([]any) {
		asset := value.(map[string]any)
		name := asset["name"].(string)
		if name == "SHA256SUMS" {
			continue
		}
		raw := f.assets[base+stringNumber(asset["id"].(int64))]
		asset["size"], asset["digest"] = len(raw), "sha256:"+digestBytes(raw)
		lines = append(lines, digestBytes(raw)+"  "+name+"\n")
	}
	sort.Strings(lines)
	sums := fixtureAsset(f, "SHA256SUMS")
	raw = []byte(strings.Join(lines, ""))
	f.assets[base+stringNumber(sums["id"].(int64))] = raw
	sums["size"], sums["digest"] = len(raw), "sha256:"+digestBytes(raw)
	f.values["/repos/"+Repository+"/releases?per_page=20"] = marshal([]any{f.release})
}

func TestRawAndLegacyCatalogDistributionAndSchema2Handoff(t *testing.T) {
	for _, component := range []string{"core", "smtp"} {
		for _, format := range []string{ArchiveDistribution, RawDistribution} {
			t.Run(component+"/"+format, func(t *testing.T) {
				f := newFixture(component)
				if format == RawDistribution {
					f = newRawFixture(component)
				}
				current := Current{CoreVersion: "0.4.0", SMTPVersion: "1.0.0", SMTPStatus: "approved", SMTPProtocol: "aster.smtp/1", SMTPCoreMin: "0.4.0", SMTPCoreMaxExclusive: "0.6.0", PluginPolicyRevision: 3}
				service := fixtureService(f, &current)
				view, err := service.Check(context.Background())
				if err != nil || len(view.Items) != 1 {
					t.Fatal(view, err)
				}
				candidate := view.Items[0]
				layout, _ := releaseLayout(component, candidate.Version, format)
				if candidate.DistributionFormat != format || len(candidate.Assets) != len(layout.Assets) || !candidate.Compatible {
					t.Fatal(candidate)
				}
				handoff, err := service.Prepare(context.Background(), strings.Repeat("b", 32), component, candidate.Tag, candidate.SourceSHA, candidate.ReleaseID, candidate.CatalogSHA256)
				if err != nil || handoff.Schema != 2 || handoff.Intent.Schema != 2 || handoff.Intent.DistributionFormat != format || len(handoff.Intent.Assets) != len(layout.Assets) {
					t.Fatal(handoff, err)
				}
				for _, call := range f.calls {
					for _, asset := range candidate.Assets {
						if asset.Name == "BUILD.json" || asset.Name == "PROVENANCE.json" || asset.Name == "SHA256SUMS" {
							continue
						}
						if strings.Contains(call, "/releases/assets/"+stringNumber(asset.ID)) {
							t.Fatal("Web fetched program/resources", call)
						}
					}
				}
			})
		}
	}
}

func TestRawMetadataRejectsMixedMissingUnknownTypesAndBindings(t *testing.T) {
	for _, component := range []string{"core", "smtp"} {
		for _, defect := range []string{"format_missing", "format_archive", "format_number", "schema_bool", "schema_legacy", "binary_name", "binary_key", "binary_hash", "binary_hash_type", "resources_name", "resources_type", "circular_hash", "circular_size", "compatibility", "provenance_missing", "provenance_extra", "provenance_hash", "provenance_size_type", "duplicate_id", "asset_size_bool", "asset_missing", "asset_extra", "mixed_archive", "metadata_checksum"} {
			t.Run(component+"/"+defect, func(t *testing.T) {
				f := newRawFixture(component)
				build := fixtureObject(f, "BUILD.json")
				prov := fixtureObject(f, "PROVENANCE.json")
				reseal := true
				switch defect {
				case "format_missing":
					delete(build, "distribution_format")
				case "format_archive":
					build["distribution_format"] = ArchiveDistribution
				case "format_number":
					build["distribution_format"] = 3
				case "schema_bool":
					build["schema"] = true
				case "schema_legacy":
					build["schema"] = 2
					delete(build, "binary_assets")
					delete(build, "resources_asset")
					delete(build, "distribution_format")
				case "binary_name", "binary_key", "binary_hash", "binary_hash_type":
					mapping := build["binary_assets"].(map[string]any)
					hashes := build["binary_sha256"].(map[string]any)
					for key := range mapping {
						if defect == "binary_name" {
							mapping[key] = "unapproved-program"
						}
						if defect == "binary_key" {
							mapping["arbitrary/program"] = mapping[key]
							delete(mapping, key)
						}
						if defect == "binary_hash" {
							hashes[key] = strings.Repeat("f", 64)
						}
						if defect == "binary_hash_type" {
							hashes[key] = 99
						}
						break
					}
				case "resources_name":
					build["resources_asset"] = "arbitrary-resources.tar.gz"
				case "resources_type":
					build["resources_asset"] = map[string]any{"name": build["resources_asset"]}
				case "circular_hash":
					build["resources_sha256"] = strings.Repeat("f", 64)
				case "circular_size":
					build["resources_size"] = 100
				case "compatibility":
					build["smtp_core_compatibility"].(map[string]any)["max_exclusive"] = "0.9.0"
				case "provenance_missing":
					for key := range prov["assets"].(map[string]any) {
						if key != "BUILD.json" {
							delete(prov["assets"].(map[string]any), key)
							break
						}
					}
				case "provenance_extra":
					prov["assets"].(map[string]any)["install.sh"] = map[string]any{"sha256": strings.Repeat("f", 64), "size": 1}
				case "provenance_hash", "provenance_size_type":
					for key, value := range prov["assets"].(map[string]any) {
						if key == "BUILD.json" {
							continue
						}
						if defect == "provenance_hash" {
							value.(map[string]any)["sha256"] = strings.Repeat("f", 64)
						} else {
							value.(map[string]any)["size"] = true
						}
						break
					}
				case "duplicate_id":
					values := f.release["assets"].([]any)
					values[1].(map[string]any)["id"] = values[0].(map[string]any)["id"]
					reseal = false
				case "asset_size_bool":
					f.release["assets"].([]any)[0].(map[string]any)["size"] = true
					reseal = false
				case "asset_missing":
					f.release["assets"] = f.release["assets"].([]any)[1:]
					reseal = false
				case "asset_extra":
					f.release["assets"] = append(f.release["assets"].([]any), map[string]any{"name": "project-source.tar.gz"})
					reseal = false
				case "mixed_archive":
					f.release["assets"].([]any)[0].(map[string]any)["name"] = "aster-0.5.1-linux-amd64.tar.gz"
					reseal = false
				case "metadata_checksum":
					asset := fixtureAsset(f, "SHA256SUMS")
					path := "/repos/" + Repository + "/releases/assets/" + stringNumber(asset["id"].(int64))
					f.assets[path] = []byte(strings.Repeat("0", 64) + "  BUILD.json\n")
					asset["size"], asset["digest"] = len(f.assets[path]), "sha256:"+digestBytes(f.assets[path])
					reseal = false
				}
				if reseal {
					setFixtureObject(f, "BUILD.json", build)
					setFixtureObject(f, "PROVENANCE.json", prov)
					resealFixture(f)
				} else {
					f.values["/repos/"+Repository+"/releases?per_page=20"] = marshal([]any{f.release})
				}
				current := Current{CoreVersion: "0.4.0", SMTPStatus: "not_installed"}
				service := fixtureService(f, &current)
				view, err := service.Check(context.Background())
				if !errors.Is(err, ErrUnavailable) || view.Source.Status != "unavailable" || len(view.Items) != 0 {
					t.Fatal("untrusted distribution accepted", defect, view, err)
				}
			})
		}
	}
}

func TestCoreCompatibilityUsesSelectedManifestRange(t *testing.T) {
	for _, tc := range []struct {
		protocol, min, max string
		version            string
		want               bool
	}{{"aster.smtp/1", "0.4.0", "0.6.0", "1.1.0", true}, {"aster.smtp/1", "0.4.0", "0.5.0", "1.1.0", false}, {"", "", "", "1.1.0", false}, {"aster.smtp/2", "0.4.0", "0.6.0", "1.1.0", false}, {"aster.smtp/1", "0.6.0", "0.4.0", "1.1.0", false}, {"", "", "", "", true}} {
		f := newRawFixture("core")
		current := Current{CoreVersion: "0.4.0", SMTPVersion: tc.version, SMTPStatus: "approved", SMTPProtocol: tc.protocol, SMTPCoreMin: tc.min, SMTPCoreMaxExclusive: tc.max}
		service := fixtureService(f, &current)
		view, err := service.Check(context.Background())
		if err != nil || view.Items[0].Compatible != tc.want {
			t.Fatal(tc, view, err)
		}
	}
}

func TestCatalogFingerprintBindsDistributionAndRejectsReplay(t *testing.T) {
	f := newRawFixture("core")
	current := Current{CoreVersion: "0.4.0", SMTPStatus: "not_installed"}
	service := fixtureService(f, &current)
	view, err := service.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	candidate := view.Items[0]
	changed := candidate
	changed.DistributionFormat = ArchiveDistribution
	if catalogDigest(candidate) == catalogDigest(changed) {
		t.Fatal("format is not bound")
	}
	if _, err := service.Prepare(context.Background(), strings.Repeat("b", 32), candidate.Component, candidate.Tag, candidate.SourceSHA, candidate.ReleaseID, catalogDigest(changed)); !errors.Is(err, ErrStale) {
		t.Fatal("format fingerprint replay accepted", err)
	}
}
