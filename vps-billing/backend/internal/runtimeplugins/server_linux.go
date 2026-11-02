package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"encoding/json"
	"errors"
	"time"
)

type ServerClient struct {
	manager   *Manager
	selection pluginapi.Selection
}

func (m *Manager) ServerSelections() ([]pluginapi.Selection, error) {
	if m == nil || m.root == "" && m.policy == "" {
		return []pluginapi.Selection{}, nil
	}
	raw, e := readProtected(m.policy, pluginapi.MaxPolicyBytes, m.rules)
	if e != nil {
		return nil, e
	}
	var p pluginapi.Policy
	if pluginapi.DecodeStrict(raw, pluginapi.MaxPolicyBytes, &p) != nil {
		return nil, ErrUnavailable
	}
	if e = validatePolicyKind(p, "server:inspection", m.rules); e != nil && !errors.Is(e, ErrDisabled) {
		return nil, e
	}
	return p.Servers, nil
}
func (m *Manager) PrepareServer(ctx context.Context, id string) (*ServerClient, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || !pluginapi.ValidConnectionID(id) {
		return nil, ErrUnavailable
	}
	a, e := loadApprovalKind(m.root, m.policy, "server:"+id, m.rules)
	if e != nil {
		return nil, e
	}
	defer a.Close()
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, _, e := handshake(check, a, m.rules)
	if e != nil {
		return nil, e
	}
	c.close()
	return &ServerClient{manager: m, selection: a.Selection}, nil
}
func (c *ServerClient) Call(ctx context.Context, in serverv1.Request) (serverv1.Response, error) {
	var out serverv1.Response
	if c == nil || c.manager == nil || in.Verify() != nil {
		return out, serverv1.ErrConfiguration
	}
	a, e := loadApprovalKind(c.manager.root, c.manager.policy, "server:"+c.selection.Slot, c.manager.rules)
	if e != nil {
		return out, serverv1.ErrUncertain
	}
	defer a.Close()
	before, _ := json.Marshal(c.selection)
	now, _ := json.Marshal(a.Selection)
	if string(before) != string(now) {
		return out, serverv1.ErrUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	wire, _, e := handshake(ctx, a, c.manager.rules)
	if e != nil {
		return out, serverv1.ErrUncertain
	}
	defer wire.close()
	end, _ := ctx.Deadline()
	_ = wire.conn.SetDeadline(end)
	payload, e := json.Marshal(in)
	if e != nil {
		return out, serverv1.ErrConfiguration
	}
	defer clear(payload)
	request := pluginapi.ServerRequest{Protocol: pluginapi.ServerProtocol, Version: a.Manifest.Version, RequestID: wire.nonce, TimeoutMS: time.Until(end).Milliseconds(), ConnectionID: c.selection.Slot, Payload: payload}
	var response pluginapi.ServerResponse
	if request.TimeoutMS < 1 || exchange(ctx, wire.client, "/v1/server/call", request, &response) != nil || response.Protocol != pluginapi.ServerProtocol || response.RequestID != wire.nonce || response.Status != "confirmed" || response.Code != "" || serverv1.Decode(response.Payload, &out) != nil || !out.Matches(in) {
		return serverv1.Response{}, serverv1.ErrUncertain
	}
	return out, nil
}
