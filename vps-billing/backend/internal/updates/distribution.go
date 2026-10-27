package updates

import (
	"aster/billing/pkg/pluginapi"
	"strings"
)

const (
	ArchiveDistribution = "archive-v1"
	RawDistribution     = "raw-binaries-v1"
)

type distributionLayout struct {
	Format         string
	Assets         map[string]bool
	BinaryAssets   map[string]string
	ResourcesAsset string
}

// Asset names are derived from validated component/version facts, never URLs or
// commands supplied by an administrator. Resource archives are not fetched by
// the Web client, just like separately downloadable raw executable assets.
func releaseLayout(component, version, format string) (distributionLayout, error) {
	if (component != "core" && component != "smtp") || !pluginapi.ValidVersion(version) {
		return distributionLayout{}, ErrUnavailable
	}
	layout := distributionLayout{Format: format, Assets: map[string]bool{"BUILD.json": true, "PROVENANCE.json": true, "SHA256SUMS": true}, BinaryAssets: map[string]string{}}
	switch format {
	case ArchiveDistribution:
		prefix := "aster-"
		if component == "smtp" {
			prefix = "aster-smtp-"
		}
		layout.Assets[prefix+version+"-linux-amd64.tar.gz"] = true
	case RawDistribution:
		if component == "core" {
			layout.BinaryAssets = map[string]string{"bin/aster-api": "aster-api-" + version + "-linux-amd64", "bin/aster-plugin-host": "aster-plugin-host-" + version + "-linux-amd64"}
			layout.ResourcesAsset = "aster-core-" + version + "-linux-amd64-resources.tar.gz"
		} else {
			layout.BinaryAssets = map[string]string{"plugin": "aster-smtp-" + version + "-linux-amd64"}
			layout.ResourcesAsset = "aster-smtp-" + version + "-linux-amd64-resources.tar.gz"
		}
		for _, name := range layout.BinaryAssets {
			layout.Assets[name] = true
		}
		layout.Assets[layout.ResourcesAsset] = true
	default:
		return distributionLayout{}, ErrUnavailable
	}
	return layout, nil
}

func buildDistribution(build map[string]any) (string, error) {
	switch number(build, "schema") {
	case 2:
		if value, exists := build["distribution_format"]; exists && value != ArchiveDistribution {
			return "", ErrUnavailable
		}
		if _, exists := build["binary_assets"]; exists {
			return "", ErrUnavailable
		}
		if _, exists := build["resources_asset"]; exists {
			return "", ErrUnavailable
		}
		return ArchiveDistribution, nil
	case 3:
		if text(build, "distribution_format") != RawDistribution {
			return "", ErrUnavailable
		}
		// BUILD is copied into resources. Including a resource archive digest or
		// size here creates a circular identity and is not a supported schema.
		for key := range build {
			if key != "resources_asset" && (strings.HasPrefix(key, "resources_") || strings.HasPrefix(key, "resource_")) {
				return "", ErrUnavailable
			}
		}
		return RawDistribution, nil
	default:
		return "", ErrUnavailable
	}
}

// ValidDistributionAssets is shared with the billing audit guard. It validates
// format/name/count/type bounds only; authenticity still requires the independent
// official metadata checks and the host operator's artifact verification.
func ValidDistributionAssets(component, version, format string, assets []Asset) bool {
	layout, err := releaseLayout(component, version, format)
	if err != nil || len(assets) != len(layout.Assets) {
		return false
	}
	ids := map[int64]bool{}
	for _, asset := range assets {
		if !layout.Assets[asset.Name] || asset.ID <= 0 || asset.ID > 9007199254740991 || ids[asset.ID] || asset.Size <= 0 || asset.Size > 80<<20 || !hashPattern.MatchString(asset.SHA256) {
			return false
		}
		limit := int64(80 << 20)
		switch asset.Name {
		case "BUILD.json":
			limit = maxJSON
		case "PROVENANCE.json":
			limit = 128 << 10
		case "SHA256SUMS":
			limit = 1024
		}
		for _, name := range layout.BinaryAssets {
			if asset.Name == name {
				limit = 64 << 20
			}
		}
		if asset.Size > limit {
			return false
		}
		delete(layout.Assets, asset.Name)
		ids[asset.ID] = true
	}
	return len(layout.Assets) == 0
}

func validateRawBinaryMetadata(build map[string]any, layout distributionLayout, assets []Asset) bool {
	if text(build, "resources_asset") != layout.ResourcesAsset {
		return false
	}
	mapping := obj(build, "binary_assets")
	hashes := obj(build, "binary_sha256")
	if len(mapping) != len(layout.BinaryAssets) || len(hashes) != len(layout.BinaryAssets) {
		return false
	}
	for logical, name := range layout.BinaryAssets {
		if text(mapping, logical) != name || !hashPattern.MatchString(text(hashes, logical)) {
			return false
		}
		matched := false
		for _, asset := range assets {
			if asset.Name == name && asset.SHA256 == text(hashes, logical) {
				matched = true
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
