package updates

import (
	"aster/billing/pkg/pluginapi"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

const PairedDistribution = "paired-bundles-v1"
const releaseWorkflow = ".github/workflows/release.yml"
const frontendRepository = "xiaoqiqixi/betteridcsys-frontend"
const frontendRepositoryID int64 = 1435344023

func pairedLayout(version, frontendVersion string) distributionLayout {
	return distributionLayout{Format: PairedDistribution, Assets: map[string]bool{
		"BUILD.json": true, "BACKEND.json": true, "FRONTEND.json": true, "PROVENANCE.json": true, "SHA256SUMS": true,
		"aster-finance-backend-" + version + "-linux-amd64.tar.gz": true,
		"aster-finance-frontend-" + frontendVersion + ".tar.gz":    true,
	}}
}

func frontendAssetVersion(assets []Asset) string {
	version := ""
	for _, asset := range assets {
		if strings.HasPrefix(asset.Name, "aster-finance-frontend-") && strings.HasSuffix(asset.Name, ".tar.gz") {
			next := strings.TrimSuffix(strings.TrimPrefix(asset.Name, "aster-finance-frontend-"), ".tar.gz")
			if version != "" || !pluginapi.ValidVersion(next) {
				return ""
			}
			version = next
		}
	}
	return version
}

func validPair(pin map[string]any, version string) bool {
	if !exactKeys(pin, "schema", "repository", "repository_id", "source_sha", "frontend_version", "core_version", "compatibility_sha256", "archive_sha256", "manifest_sha256", "artifact_sha256", "artifact_id", "run_id", "run_attempt") || number(pin, "schema") != 1 || text(pin, "repository") != frontendRepository || number(pin, "repository_id") != frontendRepositoryID || !commitPattern.MatchString(text(pin, "source_sha")) || !pluginapi.ValidVersion(text(pin, "frontend_version")) || text(pin, "core_version") != version {
		return false
	}
	for _, key := range []string{"compatibility_sha256", "archive_sha256", "manifest_sha256", "artifact_sha256"} {
		if !hashPattern.MatchString(text(pin, key)) {
			return false
		}
	}
	for _, key := range []string{"artifact_id", "run_id", "run_attempt"} {
		if number(pin, key) <= 0 {
			return false
		}
	}
	return true
}

func equalJSON(a, b any) bool {
	x, e1 := json.Marshal(a)
	y, e2 := json.Marshal(b)
	return e1 == nil && e2 == nil && bytes.Equal(x, y)
}

// The web client checks immutable metadata only. Host preparation independently
// verifies every artifact's Sigstore signature before assembling any runtime.
func (g *githubClient) verifyPaired(ctx context.Context, release map[string]any, current Current) (Candidate, error) {
	tag := text(release, "tag_name")
	version := strings.TrimPrefix(tag, "v")
	assets := []Asset{}
	seen := map[int64]bool{}
	metadata := map[string][]byte{}
	for _, value := range array(release, "assets") {
		item, ok := value.(map[string]any)
		if !ok {
			return Candidate{}, ErrUnavailable
		}
		id, size, name := number(item, "id"), number(item, "size"), text(item, "name")
		hash := strings.TrimPrefix(text(item, "digest"), "sha256:")
		if id <= 0 || seen[id] || size <= 0 || size > 80<<20 || !strings.HasPrefix(text(item, "digest"), "sha256:") || !hashPattern.MatchString(hash) || text(item, "state") != "uploaded" || text(item, "url") != apiRoot+"/releases/assets/"+strconv.FormatInt(id, 10) || text(item, "browser_download_url") != "https://github.com/"+Repository+"/releases/download/"+tag+"/"+name {
			return Candidate{}, ErrUnavailable
		}
		seen[id] = true
		assets = append(assets, Asset{Name: name, ID: id, Size: size, SHA256: hash})
	}
	if !ValidDistributionAssets("core", version, PairedDistribution, assets) {
		return Candidate{}, ErrUnavailable
	}
	for _, asset := range assets {
		if strings.HasSuffix(asset.Name, ".tar.gz") {
			continue
		}
		raw, err := g.fetch(ctx, "/releases/assets/"+strconv.FormatInt(asset.ID, 10), true, maxJSON)
		if err != nil || int64(len(raw)) != asset.Size || digestBytes(raw) != asset.SHA256 {
			return Candidate{}, ErrUnavailable
		}
		metadata[asset.Name] = raw
	}
	objects := map[string]map[string]any{}
	for _, name := range []string{"BUILD.json", "BACKEND.json", "FRONTEND.json", "PROVENANCE.json"} {
		decoded, err := strictJSON(metadata[name])
		value, ok := decoded.(map[string]any)
		if err != nil || !ok {
			return Candidate{}, ErrUnavailable
		}
		objects[name] = value
	}
	build, back, front, proof := objects["BUILD.json"], objects["BACKEND.json"], objects["FRONTEND.json"], objects["PROVENANCE.json"]
	source := text(proof, "source_sha")
	pin := obj(proof, "frontend_pair")
	runID, attempt := number(proof, "run_id"), number(proof, "run_attempt")
	if !commitPattern.MatchString(source) || runID <= 0 || attempt <= 0 || !validPair(pin, version) || !equalJSON(pin, obj(build, "frontend_pair")) || !equalJSON(pin, obj(back, "frontend_pair")) || text(release, "target_commitish") != source {
		return Candidate{}, ErrUnavailable
	}
	if !exactKeys(proof, "schema", "repository", "repository_id", "source_sha", "workflow_path", "workflow_sha", "job", "event", "tag", "version", "run_id", "run_attempt", "dry_run", "frontend_pair", "assets") || number(proof, "schema") != 2 || text(proof, "repository") != Repository || number(proof, "repository_id") != RepositoryID || text(proof, "workflow_path") != releaseWorkflow || text(proof, "workflow_sha") != source || text(proof, "job") != "release" || text(proof, "event") != "push" || !isFalse(proof, "dry_run") || text(proof, "tag") != tag || text(proof, "version") != version {
		return Candidate{}, ErrUnavailable
	}
	if number(build, "schema") != 4 || text(build, "distribution_format") != PairedDistribution || text(build, "release_kind") != "core" || text(build, "release_label") != version || text(build, "source_commit") != source || text(build, "target") != "linux/amd64" || !isFalse(build, "cgo_enabled") || text(obj(build, "component_versions"), "core") != version {
		return Candidate{}, ErrUnavailable
	}
	if number(back, "schema") != 1 || text(back, "repository") != Repository || number(back, "repository_id") != RepositoryID || text(back, "format") != "backend-tar-v1" || text(back, "source_commit") != source || text(back, "version") != version || number(front, "schema") != 1 || text(front, "repository") != frontendRepository || number(front, "repository_id") != frontendRepositoryID || text(front, "format") != "static-site-tar-v1" || text(front, "source_commit") != text(pin, "source_sha") || text(front, "version") != text(pin, "frontend_version") || text(front, "core_version") != version || digestBytes(metadata["FRONTEND.json"]) != text(pin, "manifest_sha256") || text(obj(front, "asset"), "sha256") != text(pin, "archive_sha256") {
		return Candidate{}, ErrUnavailable
	}
	inventory := map[string]Asset{}
	for _, asset := range assets {
		inventory[asset.Name] = asset
	}
	for _, manifest := range []map[string]any{back, front} {
		asset := obj(manifest, "asset")
		actual, ok := inventory[text(asset, "name")]
		if !ok || actual.SHA256 != text(asset, "sha256") || actual.Size != number(asset, "size") {
			return Candidate{}, ErrUnavailable
		}
	}
	if len(obj(proof, "assets")) != len(assets)-2 {
		return Candidate{}, ErrUnavailable
	}
	for _, asset := range assets {
		if asset.Name == "SHA256SUMS" || asset.Name == "PROVENANCE.json" {
			continue
		}
		value := obj(obj(proof, "assets"), asset.Name)
		if !exactKeys(value, "sha256", "size") || text(value, "sha256") != asset.SHA256 || number(value, "size") != asset.Size {
			return Candidate{}, ErrUnavailable
		}
	}
	sums := strings.Split(strings.TrimSuffix(string(metadata["SHA256SUMS"]), "\n"), "\n")
	counted := map[string]bool{}
	if len(sums) != len(assets)-1 {
		return Candidate{}, ErrUnavailable
	}
	for _, line := range sums {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 || counted[parts[1]] || parts[1] == "SHA256SUMS" || inventory[parts[1]].SHA256 != parts[0] {
			return Candidate{}, ErrUnavailable
		}
		counted[parts[1]] = true
	}
	commit, err := g.tagCommit(ctx, tag)
	if err != nil || commit != source {
		return Candidate{}, ErrUnavailable
	}
	ancestry, err := g.object(ctx, "/compare/"+source+"...main")
	if err != nil || (text(ancestry, "status") != "ahead" && text(ancestry, "status") != "identical") || text(obj(ancestry, "merge_base_commit"), "sha") != source {
		return Candidate{}, ErrUnavailable
	}
	sourcePin, err := g.object(ctx, "/contents/FRONTEND_PAIR.json?ref="+source)
	if err != nil || text(sourcePin, "type") != "file" || text(sourcePin, "encoding") != "base64" || number(sourcePin, "size") > 65536 {
		return Candidate{}, ErrUnavailable
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(text(sourcePin, "content"), "\n", ""))
	if err != nil || int64(len(raw)) != number(sourcePin, "size") {
		return Candidate{}, ErrUnavailable
	}
	blob := sha1.Sum(append([]byte("blob "+strconv.Itoa(len(raw))+"\x00"), raw...))
	decoded, err := strictJSON(raw)
	if err != nil || hex.EncodeToString(blob[:]) != text(sourcePin, "sha") || !equalJSON(decoded, pin) {
		return Candidate{}, ErrUnavailable
	}
	prefix := "/actions/runs/" + strconv.FormatInt(runID, 10) + "/attempts/" + strconv.FormatInt(attempt, 10)
	run, err := g.object(ctx, prefix)
	if err != nil || number(run, "id") != runID || number(run, "run_attempt") != attempt || text(run, "status") != "completed" || text(run, "conclusion") != "success" || text(run, "path") != releaseWorkflow || text(run, "head_sha") != source || text(run, "event") != "push" || text(run, "head_branch") != tag {
		return Candidate{}, ErrUnavailable
	}
	for _, key := range []string{"repository", "head_repository"} {
		if number(obj(run, key), "id") != RepositoryID || text(obj(run, key), "full_name") != Repository {
			return Candidate{}, ErrUnavailable
		}
	}
	workflow, err := g.object(ctx, "/actions/workflows/release.yml")
	if err != nil || number(workflow, "id") <= 0 || number(workflow, "id") != number(run, "workflow_id") || text(workflow, "path") != releaseWorkflow || text(workflow, "state") != "active" {
		return Candidate{}, ErrUnavailable
	}
	jobs, err := g.object(ctx, prefix+"/jobs?per_page=100")
	if err != nil || number(jobs, "total_count") != int64(len(array(jobs, "jobs"))) || len(array(jobs, "jobs")) > 100 {
		return Candidate{}, ErrUnavailable
	}
	matched := 0
	for _, value := range array(jobs, "jobs") {
		job, ok := value.(map[string]any)
		if !ok {
			return Candidate{}, ErrUnavailable
		}
		if text(job, "name") == "release" {
			if text(job, "conclusion") != "success" || text(job, "status") != "completed" || text(job, "head_sha") != source || number(job, "run_id") != runID || number(job, "run_attempt") != attempt {
				return Candidate{}, ErrUnavailable
			}
			matched++
		}
	}
	if matched != 1 {
		return Candidate{}, ErrUnavailable
	}
	compatible := true
	if current.SMTPVersion != "" {
		lower, e1 := pluginapi.CompareVersion(version, current.SMTPCoreMin)
		upper, e2 := pluginapi.CompareVersion(version, current.SMTPCoreMaxExclusive)
		compatible = e1 == nil && e2 == nil && lower >= 0 && upper < 0 && current.SMTPProtocol == pluginapi.SMTPProtocol
	}
	newer, err := pluginapi.CompareVersion(version, current.CoreVersion)
	if err != nil {
		return Candidate{}, ErrUnavailable
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	candidate := Candidate{DistributionFormat: PairedDistribution, Component: "core", Tag: tag, Version: version, ReleaseURL: officialURL(tag), Notes: text(release, "body"), PublishedAt: text(release, "published_at"), SourceSHA: source, ReleaseID: number(release, "id"), RunID: runID, RunAttempt: attempt, Compatible: compatible, UpdateAvailable: newer > 0, Assets: assets, Preconditions: []string{"host_operator_required", "artifact_signature_verification_required", "exclusive_lock_required", "backup_required", "restore_test_required", "schema_review_required", "service_restart_required"}}
	candidate.CatalogSHA256 = catalogDigest(candidate)
	return candidate, nil
}
