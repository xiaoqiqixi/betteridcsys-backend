// Package astercloud translates the platform-neutral module protocol into the
// Aster controller and service protocols. It has no finance storage access.
package astercloud

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/plugins/server.aster-cloud/controllerapi"
	"aster/billing/plugins/server.aster-cloud/serviceapi"
	"context"
	"net/url"
	"strconv"
)

const ID = "server.aster-cloud"
const Version = "1.0.0"

type Config struct {
	Schema       int      `json:"schema"`
	ControllerID string   `json:"controller_id"`
	Endpoint     string   `json:"endpoint"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	CustomCA     bool     `json:"custom_ca"`
}

func DecodeConfig(raw []byte, targets []pluginapi.Target) (Config, error) {
	var c Config
	if pluginapi.DecodeStrict(raw, 64<<10, &c) != nil || c.Schema != 1 {
		return c, serverv1.ErrConfiguration
	}
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return c, serverv1.ErrConfiguration
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return c, serverv1.ErrConfiguration
		}
	}
	allowed := false
	for _, t := range targets {
		allowed = allowed || t.Host == u.Hostname() && t.Port == port
	}
	if !allowed {
		return c, serverv1.ErrConfiguration
	}
	return c, nil
}
func Open(ctx context.Context, c Config, token, ca []byte, state string) (*Module, error) {
	if c.CustomCA != (len(ca) > 0) {
		return nil, serverv1.ErrConfiguration
	}
	profile := controllerapi.Profile{ID: c.ControllerID, Enabled: true, Endpoint: c.Endpoint, AllowedCIDRs: c.AllowedCIDRs}
	provision, err := controllerapi.NewCredentialTransport(ctx, profile, token, ca)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	service, err := serviceapi.NewCredentialTransport(ctx, profile, token, ca)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	return New(c.ControllerID, provision, service, state)
}
func number(n float64) *float64 { return &n }
func Description() serverv1.Description {
	schema := serverv1.EmptySchema()
	schema.Properties = map[string]serverv1.Field{
		"preset_id":      {Type: "string", Title: "Preset", I18n: map[string]serverv1.FieldText{"en": {Title: "Controller preset"}, "zh-CN": {Title: "主控套餐标识"}}},
		"cpu_cores":      {Type: "integer", Minimum: number(1), Maximum: number(4096), I18n: map[string]serverv1.FieldText{"en": {Title: "CPU cores for plan changes"}, "zh-CN": {Title: "变更套餐时的 CPU 核数"}}},
		"memory_mb":      {Type: "integer", Minimum: number(128), Maximum: number(1073741824), I18n: map[string]serverv1.FieldText{"en": {Title: "Memory (MiB) for plan changes"}, "zh-CN": {Title: "变更套餐时的内存 (MiB)"}}},
		"disk_gb":        {Type: "integer", Minimum: number(1), Maximum: number(16777216), I18n: map[string]serverv1.FieldText{"en": {Title: "Disk (GiB) for plan changes"}, "zh-CN": {Title: "变更套餐时的磁盘 (GiB)"}}},
		"bandwidth_mbps": {Type: "integer", Minimum: number(0), Maximum: number(100000), I18n: map[string]serverv1.FieldText{"en": {Title: "Bandwidth (Mbps) for plan changes"}, "zh-CN": {Title: "变更套餐时的带宽 (Mbps)"}}},
	}
	schema.Required = []string{"preset_id"}
	return serverv1.Description{ID: ID, Name: serverv1.Text{"en": "Aster Cloud", "zh-CN": "星屿云"}, Version: Version, Capabilities: []string{"create", "suspend", "unsuspend", "terminate", "change_package", "power", "reinstall", "reset_password", "snapshot_create", "snapshot_restore", "snapshot_delete", "status", "operation_status", "console", "metrics", "snapshots", "images"}, ProductSchema: schema, OrderSchema: serverv1.EmptySchema(), CustomActions: []serverv1.CustomAction{}}
}

func description() serverv1.Description { return Description() }
