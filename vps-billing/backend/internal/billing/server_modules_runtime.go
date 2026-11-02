package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/internal/runtimeplugins"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"encoding/json"
)

// Factories return an already approved isolated connection. Catalog metadata
// never supplies a socket, executable, network endpoint, or credential.
type ServerModules06 struct {
	Selections func() ([]pluginapi.Selection, error)
	Prepare    func(context.Context, string) (serverv1.Processor, error)
}

func NewServerModules06(manager *runtimeplugins.Manager) *ServerModules06 {
	return &ServerModules06{Selections: manager.ServerSelections, Prepare: func(ctx context.Context, id string) (serverv1.Processor, error) {
		return manager.PrepareServer(ctx, id)
	}}
}
func (c *ServerModules06) client(ctx context.Context, v api.AdminService) (*moduleService06, error) {
	if c == nil || c.Prepare == nil || v.ConnectionID == nil || v.ModuleID == nil || v.ResourceID == nil || !serverv1.ValidID(*v.ResourceID) {
		return nil, serverv1.ErrUnavailable
	}
	p, err := c.processor(ctx, *v.ConnectionID, *v.ModuleID)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	return &moduleService06{Processor: p, ServiceID: v.ID}, nil
}
func (c *ServerModules06) InspectService(ctx context.Context, v api.AdminService) (api.Service, error) {
	out := customerService06(v)
	p, err := c.client(ctx, v)
	if err != nil {
		return out, err
	}
	remote, err := p.Service(ctx, *v.ResourceID, v.ClientID)
	if err != nil {
		return out, err
	}
	return serviceProjection06(v, remote)
}

type moduleService06 struct {
	serverv1.Processor
	ServiceID string
}

func (c *moduleService06) read(ctx context.Context, method, resource, owner, period string) (serverv1.Response, error) {
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: method, ServiceID: c.ServiceID, ResourceID: resource, OwnerRef: owner, Range: period}
	out, err := c.Call(ctx, request)
	if err != nil || !out.Matches(request) {
		return serverv1.Response{}, serverv1.ErrUncertain
	}
	if out.State != "done" {
		return serverv1.Response{}, serverv1.ErrUnavailable
	}
	return out, nil
}
func (c *moduleService06) Service(ctx context.Context, id, owner string) (serverv1.Service, error) {
	r, e := c.read(ctx, "status", id, owner, "")
	if e != nil {
		return serverv1.Service{}, e
	}
	if r.Service == nil {
		return serverv1.Service{}, serverv1.ErrUncertain
	}
	check := serverv1.Description{ID: "server.validation", Version: "1", Name: serverv1.Text{"en": "Validation"}, ProductSchema: serverv1.EmptySchema(), OrderSchema: serverv1.EmptySchema(), CustomActions: r.Service.CustomActions}
	if check.Validate() != nil {
		return serverv1.Service{}, serverv1.ErrUncertain
	}
	return *r.Service, nil
}
func (c *moduleService06) Console(ctx context.Context, id, owner string) (serverv1.Console, error) {
	r, e := c.read(ctx, "console", id, owner, "")
	if e != nil {
		return serverv1.Console{}, e
	}
	if r.Console == nil {
		return serverv1.Console{}, serverv1.ErrUncertain
	}
	return *r.Console, nil
}
func (c *moduleService06) Metrics(ctx context.Context, id, owner, period string) (serverv1.Metrics, error) {
	r, e := c.read(ctx, "metrics", id, owner, period)
	if e != nil {
		return serverv1.Metrics{}, e
	}
	if r.Metrics == nil {
		return serverv1.Metrics{}, serverv1.ErrUncertain
	}
	return *r.Metrics, nil
}
func (c *moduleService06) Snapshots(ctx context.Context, id, owner string) (serverv1.Snapshots, error) {
	r, e := c.read(ctx, "snapshots", id, owner, "")
	if e != nil {
		return serverv1.Snapshots{}, e
	}
	if r.Snapshots == nil {
		return serverv1.Snapshots{}, serverv1.ErrUncertain
	}
	return *r.Snapshots, nil
}
func moduleActions06(actions []serverv1.CustomAction) ([]api.ServerCustomAction, error) {
	out := []api.ServerCustomAction{}
	err := json.Unmarshal(jsonValue06(actions), &out)
	if out == nil {
		out = []api.ServerCustomAction{}
	}
	return out, err
}

func (c *ServerModules06) processor(ctx context.Context, id, module string) (serverv1.Processor, error) {
	if c == nil || c.Prepare == nil {
		return nil, serverv1.ErrUnavailable
	}
	p, err := c.Prepare(ctx, id)
	if err != nil {
		return nil, serverv1.ErrUnavailable
	}
	request := serverv1.Request{Protocol: serverv1.Protocol, Method: "describe"}
	r, err := p.Call(ctx, request)
	if err != nil || !r.Matches(request) || r.State != "done" || r.Description == nil || r.Description.ID != module || r.Description.Validate() != nil {
		return nil, serverv1.ErrUnavailable
	}
	return p, nil
}
