// Package servertemplate is an offline reference implementation for module authors.
// It deliberately simulates resources; replace the provider and durable receipts
// before deploying a real module. No credentials or customer input are logged.
package servertemplate

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"sync"
)

const ID = "server.template"

type Processor struct {
	mu       sync.Mutex
	receipts map[string]serverv1.Response
	services map[string]serverv1.Service
}

func New() *Processor {
	return &Processor{receipts: map[string]serverv1.Response{}, services: map[string]serverv1.Service{}}
}
func Description() serverv1.Description {
	return serverv1.Description{ID: ID, Version: "1.0.0", Name: serverv1.Text{"en": "Example server module", "zh-CN": "示例服务器模块"}, Capabilities: []string{"create", "suspend", "unsuspend", "terminate", "change_package", "renew", "power", "action", "status", "operation_status", "images"}, ProductSchema: serverv1.EmptySchema(), OrderSchema: serverv1.EmptySchema(), CustomActions: []serverv1.CustomAction{{ID: "ping", Name: serverv1.Text{"en": "Ping", "zh-CN": "测试"}, InputSchema: serverv1.EmptySchema()}}}
}
func (p *Processor) Call(ctx context.Context, in serverv1.Request) (serverv1.Response, error) {
	out := serverv1.Response{Protocol: serverv1.Protocol, State: "done", OperationID: in.OperationID, Digest: in.Digest}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if in.Verify() != nil {
		return out, serverv1.ErrConfiguration
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	switch in.Method {
	case "describe":
		d := Description()
		out.Description = &d
	case "test_connection":
	case "validate_product":
		var e error
		out.Config, out.ConfigDigest, e = Description().ProductSchema.Normalize(in.Config)
		if e != nil {
			return out, e
		}
	case "images":
		out.Images = []serverv1.Image{}
	case "operation_status":
		stored, ok := p.receipts[in.OperationID]
		if !ok || stored.Digest != in.Digest {
			return out, serverv1.ErrUncertain
		}
		return stored, nil
	case "status":
		s, ok := p.services[in.ResourceID]
		if !ok || s.OwnerRef != in.OwnerRef {
			return out, serverv1.ErrUnavailable
		}
		out.Service = &s
	default:
		if !serverv1.IsMutation(in.Method) {
			return out, serverv1.ErrUnavailable
		}
		if old, ok := p.receipts[in.OperationID]; ok {
			if old.Digest != in.Digest {
				return out, serverv1.ErrConfiguration
			}
			return old, nil
		}
		if !Description().Has(in.Method) {
			return out, serverv1.ErrUnavailable
		}
		resource := in.ResourceID
		if in.Method == "create" {
			resource = "example-" + in.ServiceID
			p.services[resource] = serverv1.Service{Protocol: serverv1.Protocol, ResourceID: resource, OwnerRef: in.OwnerRef, Power: "running", IPv4: []string{}, IPv6: []string{}, Capabilities: serverv1.Capabilities{Power: true, Upgrade: true}, CustomActions: Description().CustomActions}
		}
		s, ok := p.services[resource]
		if !ok || s.OwnerRef != in.OwnerRef {
			return out, serverv1.ErrUnavailable
		}
		switch in.Method {
		case "suspend":
			s.Suspended = true
		case "unsuspend":
			s.Suspended = false
		case "terminate":
			delete(p.services, resource)
		case "power":
			if in.Input["action"] == "stop" || in.Input["action"] == "force_off" {
				s.Power = "stopped"
			} else {
				s.Power = "running"
			}
		case "action":
			if in.ActionID != "ping" {
				return out, serverv1.ErrUnavailable
			}
		}
		if in.Method != "terminate" {
			p.services[resource] = s
		}
		out.ResourceID = resource
		p.receipts[in.OperationID] = out
	}
	return out, nil
}
