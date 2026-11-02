package controllers

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// RuntimeCredential is loaded once from host-controlled files. It is never an
// API DTO. Profile paths are removed before a transport can be constructed.
type RuntimeCredential struct {
	Profile   Profile
	Token, CA []byte
}

// LoadRuntimeCredentials is deliberately separate from the root-only CLI
// policy. The non-secret policy must be root-owned and not shared-writable;
// token files must be private, single-link files owned by the API service UID
// (for example systemd LoadCredential files). No web request selects a path.
func LoadRuntimeCredentials(path string) ([]RuntimeCredential, error) {
	return loadRuntimeCredentials(path, 0, uint32(os.Geteuid()))
}
func loadRuntimeCredentials(path string, policyOwner, credentialOwner uint32) ([]RuntimeCredential, error) {
	raw, err := readHostFile(path, false, 64<<10, policyOwner)
	var policy Policy
	if err != nil || !strictJSON(raw, &policy) || !exactKeys(raw, "schema", "controllers") || policy.Schema != 1 || policy.Controllers == nil || len(policy.Controllers) > 64 {
		return nil, ErrUnavailable
	}
	var encoded struct {
		Controllers []json.RawMessage `json:"controllers"`
	}
	if json.Unmarshal(raw, &encoded) != nil {
		return nil, ErrUnavailable
	}
	seen := map[string]bool{}
	out := []RuntimeCredential{}
	for i, p := range policy.Controllers {
		_, _, valid := p.endpoint()
		if !valid || !controllerID.MatchString(p.ID) || seen[p.ID] || !exactKeys(encoded.Controllers[i], "id", "enabled", "endpoint", "allowed_cidrs", "token_file", "ca_file") || !filepath.IsAbs(p.TokenFile) || filepath.Clean(p.TokenFile) != p.TokenFile {
			return nil, ErrUnavailable
		}
		seen[p.ID] = true
		if !p.Enabled {
			continue
		}
		token, err := readHostFile(p.TokenFile, true, 4096, credentialOwner)
		if err != nil {
			return nil, ErrUnavailable
		}
		var ca []byte
		if p.CAFile != "" {
			ca, err = readHostFile(p.CAFile, false, 64<<10, 0)
			if err != nil && credentialOwner != 0 {
				ca, err = readHostFile(p.CAFile, false, 64<<10, credentialOwner)
			}
			if err != nil {
				return nil, ErrUnavailable
			}
		}
		p.TokenFile = ""
		p.CAFile = ""
		out = append(out, RuntimeCredential{Profile: p, Token: token, CA: ca})
	}
	return out, nil
}
