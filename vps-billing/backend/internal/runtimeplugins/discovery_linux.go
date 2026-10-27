package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/smtpv1"
	"errors"
	"io"
	"path/filepath"
	"sort"
)

type Discovered struct {
	ID       string `json:"id"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
	Status   string `json:"status"`
}

// Discover only reads bounded, protected manifests. No executable is launched,
// no --version subprocess runs, and no socket is contacted during discovery.
func (m *Manager) Discover() ([]Discovered, error) {
	items := []Discovered{}
	if m == nil || m.root == "" {
		return items, nil
	}
	root, err := protectedOpen(m.root, true, m.rules)
	if err != nil {
		return items, err
	}
	defer root.Close()
	ids, err := root.ReadDir(33)
	if err != nil && !errors.Is(err, io.EOF) {
		return items, ErrUnavailable
	}
	if len(ids) > 32 {
		return items, ErrUnavailable
	}
	for _, id := range ids {
		if !pluginapi.ValidID(id.Name()) || !id.IsDir() {
			continue
		}
		directory, err := protectedOpen(filepath.Join(m.root, id.Name()), true, m.rules)
		if err != nil {
			continue
		}
		versions, err := directory.ReadDir(17)
		directory.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return items, ErrUnavailable
		}
		if len(versions) > 16 {
			return items, ErrUnavailable
		}
		for _, version := range versions {
			if !pluginapi.ValidVersion(version.Name()) || !version.IsDir() {
				continue
			}
			item := Discovered{ID: id.Name(), Version: version.Name(), Status: "invalid"}
			raw, err := readProtected(filepath.Join(m.root, id.Name(), version.Name(), "manifest.json"), pluginapi.MaxManifestBytes, m.rules)
			var manifest pluginapi.Manifest
			if err == nil && pluginapi.DecodeStrict(raw, pluginapi.MaxManifestBytes, &manifest) == nil && manifest.ID == item.ID && manifest.Version == item.Version && manifest.Validate(pluginapi.CoreVersion) == nil {
				item.Status = "discovered"
				item.Protocol = manifest.Protocol
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].ID == items[j].ID {
			return items[i].Version < items[j].Version
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func (m *Manager) Allows(cfg smtpv1.Config) bool {
	if m == nil {
		return false
	}
	a, err := loadApproval(m.root, m.policy, m.rules)
	if err != nil {
		return false
	}
	defer a.Close()
	return pluginapi.Allowed(a.Selection.AllowedTargets, cfg)
}
