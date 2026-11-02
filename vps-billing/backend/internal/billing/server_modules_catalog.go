package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"encoding/json"
)

type productModuleInput06 = struct {
	ModuleID     string         `json:"module_id"`
	ConnectionID api.Id         `json:"connection_id"`
	Config       map[string]any `json:"config"`
}
type storedProductModule06 = struct {
	ModuleID     string         `json:"module_id"`
	ConnectionID api.Id         `json:"connection_id"`
	Config       map[string]any `json:"config"`
	ConfigDigest string         `json:"config_digest"`
}

func productBinding06(p *storedProductModule06) *productModuleInput06 {
	if p == nil {
		return nil
	}
	return &productModuleInput06{p.ModuleID, p.ConnectionID, p.Config}
}
func moduleID06(p *storedProductModule06) any {
	if p == nil {
		return nil
	}
	return p.ModuleID
}
func connectionID06(p *storedProductModule06) any {
	if p == nil {
		return nil
	}
	return p.ConnectionID
}
func jsonValue06(v any) []byte { raw, _ := json.Marshal(v); return raw }
func normalizeModuleDescription06(in *productModuleInput06, descriptions map[string]serverv1.Description) (*storedProductModule06, *api.ModuleSchema, error) {
	if in == nil {
		return nil, nil, nil
	}
	d, ok := descriptions[in.ConnectionID]
	if !ok || d.ID != in.ModuleID || d.Validate() != nil {
		return nil, nil, ErrCommerceUnavailable
	}
	config, hash, err := d.ProductSchema.Normalize(in.Config)
	if err != nil {
		return nil, nil, ErrCommerceInput
	}
	var schema api.ModuleSchema
	if err = json.Unmarshal(jsonValue06(d.OrderSchema), &schema); err != nil {
		return nil, nil, err
	}
	return &storedProductModule06{in.ModuleID, in.ConnectionID, config, hash}, &schema, nil
}
func normalizeProductModule06(ctx context.Context, q currencyQuerier, in *productModuleInput06) (*storedProductModule06, *api.ModuleSchema, error) {
	if in == nil {
		return nil, nil, nil
	}
	var raw []byte
	var module string
	var enabled bool
	if err := q.QueryRowContext(ctx, "SELECT module_id,enabled,description FROM server_connections WHERE BINARY id=BINARY ? FOR SHARE", in.ConnectionID).Scan(&module, &enabled, &raw); err != nil || !enabled || module != in.ModuleID {
		return nil, nil, ErrCommerceUnavailable
	}
	var d serverv1.Description
	if json.Unmarshal(raw, &d) != nil {
		return nil, nil, ErrCommerceUnavailable
	}
	return normalizeModuleDescription06(in, map[string]serverv1.Description{in.ConnectionID: d})
}

func (a *App) validateProductModule06(ctx context.Context, in *productModuleInput06) error {
	if err := a.syncModuleSlots06(ctx); err != nil {
		return err
	}
	m, ok := a.Store.(*MySQL)
	if !ok {
		return serverv1.ErrUnavailable
	}
	list, err := m.connections06(ctx, in.ConnectionID)
	if err != nil || len(list) != 1 || !list[0].Enabled || list[0].ModuleID != in.ModuleID {
		return ErrCommerceUnavailable
	}
	d, err := a.moduleDescription06(ctx, list[0])
	if err != nil {
		return err
	}
	config, _, err := d.ProductSchema.Normalize(in.Config)
	if err != nil {
		return ErrCommerceInput
	}
	runtime := a.Config.Services.(*ServerModules06)
	p, err := runtime.processor(ctx, in.ConnectionID, in.ModuleID)
	if err != nil {
		return err
	}
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: "validate_product", Config: config}
	result, err := p.Call(ctx, request)
	if err != nil || !result.Matches(request) || result.State != "done" {
		return serverv1.ErrUnavailable
	}
	normalized, hash, err := d.ProductSchema.Normalize(result.Config)
	if err != nil || hash != result.ConfigDigest {
		return serverv1.ErrUncertain
	}
	in.Config = normalized
	return nil
}
