package updates

import (
	"aster/billing/pkg/pluginapi"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const apiRoot = "https://api.github.com/repos/" + Repository
const maxJSON = 2 << 20

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var actorPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type githubClient struct {
	http  *http.Client
	token string
}

func newGitHubClient(token string) *githubClient {
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 2, MaxIdleConnsPerHost: 2, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	return &githubClient{http: &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, token: token}
}
func (g *githubClient) fetch(ctx context.Context, path string, binary bool, bound int64) ([]byte, error) {
	// Callers construct every path from validated IDs/tags, never user URLs.
	if (path != "" && !strings.HasPrefix(path, "/")) || strings.ContainsAny(path, "\\\r\n#") {
		return nil, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, "GET", apiRoot+path, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if binary {
		req.Header.Set("Accept", "application/octet-stream")
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Aster-release-check/1")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	res, err := g.http.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer res.Body.Close()
	if binary && res.StatusCode == http.StatusFound {
		// GitHub's one official release CDN hop is credential-free. Source, run
		// and catalog responses never redirect, and a second hop is rejected.
		location, err := res.Location()
		if err != nil || location.Scheme != "https" || location.Hostname() != "release-assets.githubusercontent.com" || (location.Port() != "" && location.Port() != "443") || location.User != nil || location.Fragment != "" || len(location.String()) > 8192 {
			return nil, ErrUnavailable
		}
		res.Body.Close()
		cdn, err := http.NewRequestWithContext(ctx, "GET", location.String(), nil)
		if err != nil {
			return nil, ErrUnavailable
		}
		cdn.Header.Set("Accept", "application/octet-stream")
		cdn.Header.Set("User-Agent", "Aster-release-check/1")
		res, err = g.http.Do(cdn)
		if err != nil {
			return nil, ErrUnavailable
		}
		defer res.Body.Close()
	}
	if res.StatusCode != http.StatusOK || (res.ContentLength > bound) {
		return nil, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, bound+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > bound {
		return nil, ErrUnavailable
	}
	return raw, nil
}
func (g *githubClient) object(ctx context.Context, path string) (map[string]any, error) {
	raw, err := g.fetch(ctx, path, false, maxJSON)
	if err != nil {
		return nil, err
	}
	value, err := strictJSON(raw)
	object, ok := value.(map[string]any)
	if err != nil || !ok {
		return nil, ErrUnavailable
	}
	return object, nil
}
func strictJSON(raw []byte) (any, error) {
	if len(raw) == 0 || len(raw) > maxJSON || !utf8.Valid(raw) {
		return nil, ErrUnavailable
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) (any, error)
	walk = func(depth int) (any, error) {
		if depth > 16 {
			return nil, ErrUnavailable
		}
		token, err := d.Token()
		if err != nil {
			return nil, ErrUnavailable
		}
		switch token {
		case json.Delim('{'):
			object := map[string]any{}
			for d.More() {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || len(name) > 128 {
					return nil, ErrUnavailable
				}
				if _, exists := object[name]; exists {
					return nil, ErrUnavailable
				}
				value, err := walk(depth + 1)
				if err != nil {
					return nil, err
				}
				object[name] = value
				if len(object) > 128 {
					return nil, ErrUnavailable
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return nil, ErrUnavailable
			}
			return object, nil
		case json.Delim('['):
			array := []any{}
			for d.More() {
				value, err := walk(depth + 1)
				if err != nil {
					return nil, err
				}
				array = append(array, value)
				if len(array) > 128 {
					return nil, ErrUnavailable
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return nil, ErrUnavailable
			}
			return array, nil
		}
		if _, isDelim := token.(json.Delim); isDelim {
			return nil, ErrUnavailable
		}
		return token, nil
	}
	value, err := walk(0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, ErrUnavailable
	}
	return value, nil
}
func text(m map[string]any, key string) string { value, _ := m[key].(string); return value }
func number(m map[string]any, key string) int64 {
	value, ok := m[key].(json.Number)
	if !ok {
		return -1
	}
	n, err := strconv.ParseInt(string(value), 10, 64)
	if err != nil || n > 9007199254740991 {
		return -1
	}
	return n
}
func obj(m map[string]any, key string) map[string]any {
	value, _ := m[key].(map[string]any)
	return value
}
func array(m map[string]any, key string) []any  { value, _ := m[key].([]any); return value }
func isFalse(m map[string]any, key string) bool { value, ok := m[key].(bool); return ok && !value }
func validTag(tag, component string) bool {
	prefix := "v"
	if component == "smtp" {
		prefix = "smtp-v"
	}
	return strings.HasPrefix(tag, prefix) && pluginapi.ValidVersion(strings.TrimPrefix(tag, prefix))
}
func officialURL(tag string) string {
	return "https://github.com/" + Repository + "/releases/tag/" + tag
}
func exactKeys(m map[string]any, keys ...string) bool {
	if len(m) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := m[key]; !ok {
			return false
		}
	}
	return true
}
func digestBytes(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }

func (g *githubClient) catalog(ctx context.Context, current Current) ([]Candidate, error) {
	repo, err := g.object(ctx, "")
	if err != nil || number(repo, "id") != RepositoryID || text(repo, "full_name") != Repository {
		return nil, ErrUnavailable
	}
	raw, err := g.fetch(ctx, "/releases?per_page=20", false, maxJSON)
	if err != nil {
		return nil, err
	}
	decoded, err := strictJSON(raw)
	releases, ok := decoded.([]any)
	if err != nil || !ok || len(releases) > 20 {
		return nil, ErrUnavailable
	}
	selected := map[string]map[string]any{}
	for page := 1; page <= 5; page++ {
		for _, value := range releases {
			release, ok := value.(map[string]any)
			if !ok {
				return nil, ErrUnavailable
			}
			if !isFalse(release, "draft") || !isFalse(release, "prerelease") {
				continue
			}
			tag := text(release, "tag_name")
			component := "core"
			if strings.HasPrefix(tag, "smtp-v") {
				component = "smtp"
			}
			if !validTag(tag, component) {
				continue
			}
			prefix := "v"
			if component == "smtp" {
				prefix = "smtp-v"
			}
			if previous, ok := selected[component]; ok {
				cmp, _ := pluginapi.CompareVersion(strings.TrimPrefix(tag, prefix), strings.TrimPrefix(text(previous, "tag_name"), prefix))
				if cmp <= 0 {
					continue
				}
			}
			selected[component] = release
		}
		if len(releases) < 20 {
			break
		}
		if page == 5 {
			return nil, ErrUnavailable
		}
		raw, err := g.fetch(ctx, "/releases?per_page=20&page="+strconv.Itoa(page+1), false, maxJSON)
		if err != nil {
			return nil, ErrUnavailable
		}
		decoded, err := strictJSON(raw)
		var ok bool
		releases, ok = decoded.([]any)
		if err != nil || !ok || len(releases) > 20 {
			return nil, ErrUnavailable
		}
	}
	items := []Candidate{}
	for _, component := range []string{"core", "smtp"} {
		if release, ok := selected[component]; ok {
			candidate, err := g.verify(ctx, release, component, current)
			if err != nil {
				return nil, err
			}
			items = append(items, candidate)
		}
	}
	return items, nil
}
func (g *githubClient) tagCommit(ctx context.Context, tag string) (string, error) {
	ref, err := g.object(ctx, "/git/ref/tags/"+url.PathEscape(tag))
	if err != nil || text(ref, "ref") != "refs/tags/"+tag {
		return "", ErrUnavailable
	}
	object := obj(ref, "object")
	for n := 0; n < 3; n++ {
		sha := text(object, "sha")
		if !commitPattern.MatchString(sha) {
			return "", ErrUnavailable
		}
		if text(object, "type") == "commit" {
			return sha, nil
		}
		if text(object, "type") != "tag" {
			return "", ErrUnavailable
		}
		annotated, err := g.object(ctx, "/git/tags/"+sha)
		if err != nil || text(annotated, "sha") != sha {
			return "", ErrUnavailable
		}
		object = obj(annotated, "object")
	}
	return "", ErrUnavailable
}
func (g *githubClient) verify(ctx context.Context, release map[string]any, component string, current Current) (Candidate, error) {
	tag := text(release, "tag_name")
	prefix := "v"
	if component == "smtp" {
		prefix = "smtp-v"
	}
	version := strings.TrimPrefix(tag, prefix)
	if !validTag(tag, component) || !isFalse(release, "draft") || !isFalse(release, "prerelease") || number(release, "id") <= 0 || text(release, "html_url") != officialURL(tag) {
		return Candidate{}, ErrUnavailable
	}
	published := text(release, "published_at")
	if _, err := time.Parse(time.RFC3339, published); err != nil {
		return Candidate{}, ErrUnavailable
	}
	notes := text(release, "body")
	if len(notes) > 16<<10 || !utf8.ValidString(notes) || strings.IndexByte(notes, 0) >= 0 {
		return Candidate{}, ErrUnavailable
	}
	legacyLayout, err := releaseLayout(component, version, ArchiveDistribution)
	if err != nil {
		return Candidate{}, ErrUnavailable
	}
	rawLayout, err := releaseLayout(component, version, RawDistribution)
	if err != nil {
		return Candidate{}, ErrUnavailable
	}
	allowed := map[string]bool{}
	for name := range legacyLayout.Assets {
		allowed[name] = true
	}
	for name := range rawLayout.Assets {
		allowed[name] = true
	}
	assets := []Asset{}
	metadata := map[string][]byte{}
	inventory := array(release, "assets")
	if len(inventory) != len(legacyLayout.Assets) && len(inventory) != len(rawLayout.Assets) {
		return Candidate{}, ErrUnavailable
	}
	seenIDs := map[int64]bool{}
	for _, value := range inventory {
		asset, ok := value.(map[string]any)
		if !ok {
			return Candidate{}, ErrUnavailable
		}
		name := text(asset, "name")
		if !allowed[name] {
			return Candidate{}, ErrUnavailable
		}
		delete(allowed, name)
		hash := strings.TrimPrefix(text(asset, "digest"), "sha256:")
		id := number(asset, "id")
		size := number(asset, "size")
		if !strings.HasPrefix(text(asset, "digest"), "sha256:") || !hashPattern.MatchString(hash) || id <= 0 || seenIDs[id] || size <= 0 || size > 80<<20 || text(asset, "state") != "uploaded" || text(asset, "url") != apiRoot+"/releases/assets/"+strconv.FormatInt(id, 10) || text(asset, "browser_download_url") != "https://github.com/"+Repository+"/releases/download/"+tag+"/"+name {
			return Candidate{}, ErrUnavailable
		}
		seenIDs[id] = true
		for _, binary := range rawLayout.BinaryAssets {
			if name == binary && size > 64<<20 {
				return Candidate{}, ErrUnavailable
			}
		}
		assets = append(assets, Asset{Name: name, ID: id, Size: size, SHA256: hash})
		// This exact metadata allowlist is independent of distribution format.
		// Neither raw executables nor either type of resource/archive is fetched.
		if name == "BUILD.json" || name == "PROVENANCE.json" || name == "SHA256SUMS" {
			bound := int64(128 << 10)
			if name == "BUILD.json" {
				bound = maxJSON
			}
			if name == "SHA256SUMS" {
				bound = 1024
			}
			if size > bound {
				return Candidate{}, ErrUnavailable
			}
			raw, err := g.fetch(ctx, "/releases/assets/"+strconv.FormatInt(id, 10), true, bound)
			if err != nil || int64(len(raw)) != size || digestBytes(raw) != hash {
				return Candidate{}, ErrUnavailable
			}
			metadata[name] = raw
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	decodedBuild, err := strictJSON(metadata["BUILD.json"])
	build, ok := decodedBuild.(map[string]any)
	if err != nil || !ok {
		return Candidate{}, ErrUnavailable
	}
	format, err := buildDistribution(build)
	if err != nil {
		return Candidate{}, ErrUnavailable
	}
	layout, err := releaseLayout(component, version, format)
	if err != nil || len(assets) != len(layout.Assets) {
		return Candidate{}, ErrUnavailable
	}
	for _, asset := range assets {
		if !layout.Assets[asset.Name] {
			return Candidate{}, ErrUnavailable
		}
	}
	checksums := strings.Split(strings.TrimSuffix(string(metadata["SHA256SUMS"]), "\n"), "\n")
	if len(checksums) != len(assets)-1 {
		return Candidate{}, ErrUnavailable
	}
	seen := map[string]bool{}
	for _, line := range checksums {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 || !hashPattern.MatchString(parts[0]) || seen[parts[1]] || parts[1] == "SHA256SUMS" {
			return Candidate{}, ErrUnavailable
		}
		seen[parts[1]] = true
		found := false
		for _, asset := range assets {
			if asset.Name == parts[1] && asset.SHA256 == parts[0] {
				found = true
			}
		}
		if !found {
			return Candidate{}, ErrUnavailable
		}
	}
	decodedProv, err := strictJSON(metadata["PROVENANCE.json"])
	prov, ok := decodedProv.(map[string]any)
	if err != nil || !ok {
		return Candidate{}, ErrUnavailable
	}
	source := text(prov, "source_sha")
	runID := number(prov, "run_id")
	attempt := number(prov, "run_attempt")
	versions := obj(build, "component_versions")
	compat := obj(build, "smtp_core_compatibility")
	if text(build, "release_kind") != component || text(build, "release_label") != version || text(build, "source_commit") != source || text(build, "target") != "linux/amd64" || !isFalse(build, "cgo_enabled") || !exactKeys(versions, "core", "smtp") || text(versions, component) != version || !pluginapi.ValidVersion(text(versions, "core")) || !pluginapi.ValidVersion(text(versions, "smtp")) || text(build, "plugin_protocol") != pluginapi.SMTPProtocol || !exactKeys(compat, "min", "max_exclusive") || text(compat, "min") != "0.4.0" {
		return Candidate{}, ErrUnavailable
	}
	max := "0.5.0"
	if format == RawDistribution {
		max = "0.6.0"
	}
	if text(compat, "max_exclusive") != max {
		return Candidate{}, ErrUnavailable
	}
	if format == RawDistribution && !validateRawBinaryMetadata(build, layout, assets) {
		return Candidate{}, ErrUnavailable
	}

	if !exactKeys(prov, "schema", "repository", "repository_id", "run_id", "run_attempt", "event", "tag", "version", "source_sha", "run_head_sha", "workflow_sha", "workflow_path", "default_branch", "default_sha", "trusted_blobs", "artifact_name", "assets") || number(prov, "schema") != 1 || number(prov, "repository_id") != RepositoryID || text(prov, "repository") != Repository || text(prov, "tag") != tag || text(prov, "version") != version || !commitPattern.MatchString(source) || runID <= 0 || attempt <= 0 || text(prov, "workflow_path") != ".github/workflows/release-build.yml" || text(prov, "workflow_sha") != text(prov, "run_head_sha") || !commitPattern.MatchString(text(prov, "workflow_sha")) || text(prov, "default_branch") != "main" || !commitPattern.MatchString(text(prov, "default_sha")) || text(prov, "artifact_name") != "candidate-"+strconv.FormatInt(runID, 10)+"-"+strconv.FormatInt(attempt, 10) {
		return Candidate{}, ErrUnavailable
	}
	trusted := obj(prov, "trusted_blobs")
	if len(trusted) == 0 || len(trusted) > 64 {
		return Candidate{}, ErrUnavailable
	}
	for path, value := range trusted {
		sha, ok := value.(string)
		if !ok || !commitPattern.MatchString(sha) || len(path) > 256 || strings.Contains(path, "..") || strings.ContainsAny(path, "\\\r\n\x00") || !(strings.HasPrefix(path, ".github/workflows/") || strings.HasPrefix(path, "vps-billing/")) {
			return Candidate{}, ErrUnavailable
		}
	}
	provAssets := obj(prov, "assets")
	if len(provAssets) != len(assets)-2 {
		return Candidate{}, ErrUnavailable
	}
	for _, asset := range assets {
		if asset.Name == "PROVENANCE.json" || asset.Name == "SHA256SUMS" {
			continue
		}
		meta := obj(provAssets, asset.Name)
		if !exactKeys(meta, "sha256", "size") || text(meta, "sha256") != asset.SHA256 || number(meta, "size") != asset.Size {
			return Candidate{}, ErrUnavailable
		}
	}

	actual, err := g.tagCommit(ctx, tag)
	if err != nil || actual != source {
		return Candidate{}, ErrUnavailable
	}
	run, err := g.object(ctx, "/actions/runs/"+strconv.FormatInt(runID, 10)+"/attempts/"+strconv.FormatInt(attempt, 10))
	if err != nil || number(run, "id") != runID || number(run, "run_attempt") != attempt || text(run, "status") != "completed" || text(run, "conclusion") != "success" || text(run, "path") != ".github/workflows/release-build.yml" || number(obj(run, "repository"), "id") != RepositoryID || text(obj(run, "repository"), "full_name") != Repository || number(obj(run, "head_repository"), "id") != RepositoryID || text(obj(run, "head_repository"), "full_name") != Repository || text(run, "head_sha") != text(prov, "run_head_sha") || text(run, "event") != text(prov, "event") {
		return Candidate{}, ErrUnavailable
	}
	if text(run, "event") == "push" {
		if text(run, "head_sha") != source || text(run, "head_branch") != tag {
			return Candidate{}, ErrUnavailable
		}
	} else if text(run, "event") != "workflow_dispatch" || text(run, "head_branch") != "main" {
		return Candidate{}, ErrUnavailable
	}
	workflow, err := g.object(ctx, "/actions/workflows/release-build.yml")
	if err != nil || number(workflow, "id") != number(run, "workflow_id") || text(workflow, "path") != ".github/workflows/release-build.yml" || text(workflow, "state") != "active" {
		return Candidate{}, ErrUnavailable
	}
	min := text(compat, "min")
	low, e1 := pluginapi.CompareVersion(min, max)
	if e1 != nil || low >= 0 {
		return Candidate{}, ErrUnavailable
	}
	compatibleVersion := version
	if component == "smtp" {
		compatibleVersion = current.CoreVersion
	}
	lower, e1 := pluginapi.CompareVersion(compatibleVersion, min)
	upper, e2 := pluginapi.CompareVersion(compatibleVersion, max)
	compatible := e1 == nil && e2 == nil && lower >= 0 && upper < 0
	// A selected plugin constrains Core updates by its actual validated manifest,
	// not by the plugin version number or the candidate's bundled SMTP version.
	if component == "core" && current.SMTPVersion != "" {
		validRange := current.SMTPProtocol == pluginapi.SMTPProtocol && pluginapi.ValidVersion(current.SMTPCoreMin) && pluginapi.ValidVersion(current.SMTPCoreMaxExclusive)
		lower, errLower := pluginapi.CompareVersion(version, current.SMTPCoreMin)
		upper, errUpper := pluginapi.CompareVersion(version, current.SMTPCoreMaxExclusive)
		order, errOrder := pluginapi.CompareVersion(current.SMTPCoreMin, current.SMTPCoreMaxExclusive)
		compatible = compatible && validRange && errLower == nil && errUpper == nil && errOrder == nil && order < 0 && lower >= 0 && upper < 0
	}

	installed := current.CoreVersion
	if component == "smtp" {
		installed = current.SMTPVersion
	}
	newer := installed == ""
	if installed != "" {
		cmp, err := pluginapi.CompareVersion(version, installed)
		newer = err == nil && cmp > 0
	}
	preconditions := []string{"host_operator_required", "exclusive_lock_required", "backup_required", "restore_test_required", "schema_review_required", "service_restart_required"}
	if component == "smtp" {
		preconditions = append(preconditions, "plugin_approval_required")
	}
	candidate := Candidate{DistributionFormat: format, Component: component, Tag: tag, Version: version, ReleaseURL: officialURL(tag), Notes: notes, PublishedAt: published, SourceSHA: source, ReleaseID: number(release, "id"), RunID: runID, RunAttempt: attempt, Compatible: compatible, UpdateAvailable: newer, Preconditions: preconditions, Assets: assets}
	candidate.CatalogSHA256 = catalogDigest(candidate)
	return candidate, nil
}
