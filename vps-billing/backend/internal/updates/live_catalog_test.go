//go:build official_release_smoke

package updates

import (
	"aster/billing/pkg/pluginapi"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in smoke uses the unchanged production client against the fixed
// official API/CDN. Ordinary go test excludes it; it never calls Prepare,
// downloads executable archives, accesses a Store, or installs/restarts code.
func TestOfficialPublishedReleaseCatalogSmoke(t *testing.T) {
	if os.Getenv("ASTER_OFFICIAL_RELEASE_SMOKE") != "1" {
		t.Fatal("official release smoke requires an explicit CI opt-in")
	}
	if os.Getenv("GITHUB_TOKEN") == "" {
		t.Fatal("official release smoke requires the ephemeral read-only CI credential")
	}
	current := Current{CoreVersion: pluginapi.CoreVersion, SMTPStatus: "not_installed"}
	service := New(os.Getenv("GITHUB_TOKEN"), func() Current { return current })
	evidence := struct {
		Schema              int              `json:"schema"`
		Status              string           `json:"status"`
		Scope               string           `json:"scope"`
		Source              Source           `json:"source"`
		CheckedAt           string           `json:"checked_at"`
		Current             Current          `json:"current"`
		Components          []map[string]any `json:"components"`
		SafeReason          string           `json:"safe_reason,omitempty"`
		TransportOverridden bool             `json:"transport_overridden"`
		HandoffCreated      bool             `json:"handoff_created"`
		ProgramsDownloaded  bool             `json:"programs_downloaded"`
		Applied             bool             `json:"applied"`
	}{Schema: 1, Status: "failed", Scope: "Production fixed-source metadata read only; no host, backup, installation or migration readiness proof.", Current: current, Components: []map[string]any{}}
	defer func() {
		path := os.Getenv("ASTER_OFFICIAL_SMOKE_EVIDENCE")
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			t.Error("official release smoke requires a canonical CI evidence path")
			return
		}
		raw, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Error("cannot encode official release smoke evidence")
			return
		}
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Error("cannot create official release smoke evidence")
			return
		}
		if _, err := file.Write(append(raw, '\n')); err != nil {
			t.Error("cannot write official release smoke evidence")
		}
		if err := file.Close(); err != nil {
			t.Error("cannot close official release smoke evidence")
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	view, err := service.Check(ctx)
	evidence.Source, evidence.CheckedAt = view.Source, view.CheckedAt
	if err != nil || view.Source.Repository != Repository || view.Source.Status != "available" {
		evidence.SafeReason = "official_source_unavailable_or_unverified"
		t.Fatal("production official release metadata could not be verified")
	}
	if len(view.Items) != 2 || view.Mode != "manual_host_handoff" || view.Current != current {
		evidence.SafeReason = "incomplete_official_component_catalog"
		t.Fatal("official release catalog must contain normal core and SMTP components")
	}
	seen := map[string]bool{}
	for _, candidate := range view.Items {
		prefixTag := "v"
		if candidate.Component == "smtp" {
			prefixTag = "smtp-v"
		}
		if (candidate.Component != "core" && candidate.Component != "smtp") || seen[candidate.Component] || !validTag(candidate.Tag, candidate.Component) || !pluginapi.ValidVersion(candidate.Version) || candidate.Tag != prefixTag+candidate.Version || !commitPattern.MatchString(candidate.SourceSHA) || !hashPattern.MatchString(candidate.CatalogSHA256) || candidate.ReleaseURL != officialURL(candidate.Tag) || candidate.ReleaseID <= 0 || candidate.RunID <= 0 || candidate.RunAttempt <= 0 {
			evidence.SafeReason = "unexpected_official_release_identity"
			t.Fatal("official release identity is incomplete")
		}
		seen[candidate.Component] = true
		layout, err := releaseLayout(candidate.Component, candidate.Version, candidate.DistributionFormat)
		if err != nil || len(candidate.Assets) != len(layout.Assets) {
			evidence.SafeReason = "unexpected_official_distribution"
			t.Fatal("official release distribution is unsupported")
		}
		expected := layout.Assets

		assetIDs := map[int64]bool{}
		for _, asset := range candidate.Assets {
			if !expected[asset.Name] || asset.ID <= 0 || assetIDs[asset.ID] || asset.Size <= 0 || asset.Size > 80<<20 || !hashPattern.MatchString(asset.SHA256) {
				evidence.SafeReason = "unexpected_official_asset_inventory"
				t.Fatal("official release must contain its exact bounded program/resource/metadata assets")
			}
			delete(expected, asset.Name)
			assetIDs[asset.ID] = true
		}
		if len(expected) != 0 {
			t.Fatal("official release asset inventory is incomplete")
		}
		expectedNewer := true // No SMTP installation is claimed by this metadata smoke.
		if candidate.Component == "core" {
			comparison, err := pluginapi.CompareVersion(candidate.Version, current.CoreVersion)
			if err != nil {
				t.Fatal("official core version is invalid")
			}
			expectedNewer = comparison > 0
		}
		if candidate.UpdateAvailable != expectedNewer {
			t.Fatal("official update availability does not match declared component versions")
		}
		// Compatibility is recorded, never assumed true: newer main source can
		// precede a matching release or intentionally change the protocol range.
		evidence.Components = append(evidence.Components, map[string]any{"distribution_format": candidate.DistributionFormat, "component": candidate.Component, "tag": candidate.Tag, "version": candidate.Version, "source_sha": candidate.SourceSHA, "release_id": candidate.ReleaseID, "run_id": candidate.RunID, "run_attempt": candidate.RunAttempt, "compatible": candidate.Compatible, "update_available": candidate.UpdateAvailable, "catalog_sha256": candidate.CatalogSHA256, "assets": candidate.Assets})
	}
	evidence.Status = "passed"
	t.Log("Verified normal official core and SMTP metadata using the production client; no update was applied.")
}
