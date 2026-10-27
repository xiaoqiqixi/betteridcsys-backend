package runtimeplugins

import (
	"aster/billing/controllerapi"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/resourcev1"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// ResourceClient pins the complete root-approved selection before a durable
// job is claimed. Neither construction nor calls execute a process. Only the
// independently sandboxed service launcher may execute its approved inode.
type ResourceClient struct {
	manager   *Manager
	selection pluginapi.Selection
}

func (m *Manager) ResourceState() State {
	s := State{Status: "unavailable", Protocol: pluginapi.ResourceProtocol}
	if m == nil || m.root == "" && m.policy == "" {
		s.Status = "not_installed"
		return s
	}
	a, e := loadApprovalKind(m.root, m.policy, "resource", m.rules)
	if errors.Is(e, ErrDisabled) {
		s.Status = "disabled"
		if a != nil {
			s.ID, s.Version, s.PolicyRevision = a.Selection.ID, a.Selection.Version, a.PolicyRevision
		}
		return s
	}
	if e != nil {
		return s
	}
	defer a.Close()
	s.Status, s.ID, s.Version, s.PolicyRevision = "approved", a.Manifest.ID, a.Manifest.Version, a.PolicyRevision
	return s
}
func (m *Manager) PrepareResource(ctx context.Context, id string) (*ResourceClient, error) {
	if m == nil || ctx == nil || ctx.Err() != nil || !pluginapi.ValidControllerID(id) {
		return nil, ErrUnavailable
	}
	a, e := loadApprovalKind(m.root, m.policy, "resource", m.rules)
	if e != nil {
		return nil, e
	}
	defer a.Close()
	if id != a.Selection.ControllerID {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, _, e := handshake(ctx, a, m.rules)
	if e != nil {
		return nil, e
	}
	c.close()
	return &ResourceClient{manager: m, selection: a.Selection}, nil
}
func (c *ResourceClient) Send(ctx context.Context, in controllerapi.Request) (controllerapi.Result, error) {
	return c.call(ctx, in, false)
}
func (c *ResourceClient) Lookup(ctx context.Context, in controllerapi.Request) (controllerapi.Result, error) {
	return c.call(ctx, in, true)
}
func (c *ResourceClient) call(ctx context.Context, in controllerapi.Request, lookup bool) (controllerapi.Result, error) {
	if c == nil || c.manager == nil || ctx == nil || ctx.Err() != nil {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	payload, _, e := in.BytesAndDigest()
	if e != nil || in.Product.ControllerID != c.selection.ControllerID {
		return controllerapi.Result{}, controllerapi.ErrConfiguration
	}
	// Re-read and rehash every operation; policy rotation cannot silently change
	// the selected worker after the caller's durable claim.
	a, e := loadApprovalKind(c.manager.root, c.manager.policy, "resource", c.manager.rules)
	if e != nil {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	defer a.Close()
	before, _ := json.Marshal(c.selection)
	now, _ := json.Marshal(a.Selection)
	if string(before) != string(now) {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	connection, _, e := handshake(ctx, a, c.manager.rules)
	if e != nil {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	defer connection.close()
	end, _ := ctx.Deadline()
	_ = connection.conn.SetDeadline(end)
	envelope := pluginapi.ResourceRequest{Protocol: pluginapi.ResourceProtocol, Version: a.Manifest.Version, RequestID: connection.nonce, TimeoutMS: time.Until(end).Milliseconds(), ControllerID: c.selection.ControllerID, Payload: payload}
	path := "/v1/resource/send"
	if lookup {
		path = "/v1/resource/lookup"
	}
	var out pluginapi.ResourceResponse
	if envelope.TimeoutMS < 1 || exchange(ctx, connection.client, path, envelope, &out) != nil || out.Protocol != pluginapi.ResourceProtocol || out.RequestID != connection.nonce || out.Status != "confirmed" || out.Code != "" {
		return controllerapi.Result{}, controllerapi.ErrUncertain
	}
	return resourcev1.DecodeResult(out.Payload, in)
}
