// Package resourcebridge is the explicitly installed Aster controller worker.
// It cannot read billing storage or mutate financial records.
package resourcebridge

import (
	"aster/billing/controllerapi"
	"aster/billing/pkg/pluginapi"
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

const ID = "resource.aster-cloud"
const Version = "0.1.1"

type Config struct {
	Schema       int      `json:"schema"`
	ControllerID string   `json:"controller_id"`
	Endpoint     string   `json:"endpoint"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	CustomCA     bool     `json:"custom_ca"`
}

func DecodeConfig(raw []byte, id string, targets []pluginapi.Target) (Config, error) {
	var cfg Config
	if pluginapi.DecodeStrict(raw, 64<<10, &cfg) != nil || cfg.Schema != 1 || cfg.ControllerID != id || !pluginapi.ValidControllerID(id) || len(targets) < 1 || len(targets) > 8 {
		return Config{}, pluginapi.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 5 {
		return Config{}, pluginapi.ErrInvalid
	}
	for _, k := range []string{"schema", "controller_id", "endpoint", "allowed_cidrs", "custom_ca"} {
		if _, ok := fields[k]; !ok {
			return Config{}, pluginapi.ErrInvalid
		}
	}
	u, e := url.Parse(cfg.Endpoint)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return Config{}, pluginapi.ErrInvalid
	}
	port := 443
	if u.Port() != "" {
		port, e = strconv.Atoi(u.Port())
		if e != nil {
			return Config{}, pluginapi.ErrInvalid
		}
	}
	approved := false
	for _, t := range targets {
		approved = approved || t.Host == u.Hostname() && t.Port == port
	}
	if !approved {
		return Config{}, pluginapi.ErrInvalid
	}
	return cfg, nil
}
func New(ctx context.Context, cfg Config, token, ca []byte) (*controllerapi.Client, error) {
	if cfg.Schema != 1 || !pluginapi.ValidControllerID(cfg.ControllerID) || cfg.CustomCA != (len(ca) > 0) {
		return nil, controllerapi.ErrUnavailable
	}
	return controllerapi.NewCredentialTransport(ctx, controllerapi.Profile{ID: cfg.ControllerID, Enabled: true, Endpoint: cfg.Endpoint, AllowedCIDRs: cfg.AllowedCIDRs}, token, ca)
}
