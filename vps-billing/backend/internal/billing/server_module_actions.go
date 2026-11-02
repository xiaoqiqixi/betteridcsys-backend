package billing

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"encoding/json"
)

// These are standard finance API inputs, independent of provider-specific schemas.
// Password and custom Secrets are excluded at every persistence boundary.
type serviceParams06 struct {
	PasswordHash string          `json:"password_hash,omitempty"`
	ImageID      string          `json:"image_id,omitempty"`
	Password     string          `json:"-"`
	SSHKeys      []string        `json:"ssh_keys,omitempty"`
	StartAfter   bool            `json:"start_after,omitempty"`
	Confirmation string          `json:"confirmation,omitempty"`
	SnapshotID   string          `json:"snapshot_id,omitempty"`
	Name         string          `json:"name,omitempty"`
	ActionID     string          `json:"action_id,omitempty"`
	Input        serverv1.Fields `json:"input,omitempty"`
	Secrets      serverv1.Fields `json:"-"`
}

func (p serviceParams06) Redacted() serviceParams06 { p.Password = ""; p.Secrets = nil; return p }
func (p serviceParams06) Valid(action string) bool {
	switch action {
	case "reinstall":
		return p.ImageID != "" && (p.Password != "" || len(p.SSHKeys) > 0)
	case "reset_password":
		return p.Password != ""
	case "rescue_enter":
		return p.Password != ""
	case "snapshot_restore", "snapshot_delete":
		return serverv1.ValidID(p.SnapshotID)
	case "action":
		return serverv1.ValidID(p.ActionID)
	default:
		return true
	}
}
func serviceRequest06(s serviceRecord06, id, action string, params serviceParams06) (serverv1.Request, error) {
	r := serverv1.Request{Protocol: serverv1.Protocol, Method: action, OperationID: id, ServiceID: s.View.ID, OwnerRef: s.View.ClientID, Input: serverv1.Fields{}, Secrets: serverv1.Fields{}}
	if s.Configuration.Module == nil {
		return r, serverv1.ErrUnavailable
	}
	r.Config = s.Configuration.Module.Config
	if s.View.ResourceID != nil {
		r.ResourceID = *s.View.ResourceID
	}
	switch action {
	case "provision":
		r.Method = "create"
		if s.View.Hostname != nil {
			r.Input["hostname"] = *s.View.Hostname
		}
		r.Input["ssh_keys"] = s.Configuration.SSHKeys
		if s.Configuration.PasswordHash != "" {
			r.Input["password_hash"] = s.Configuration.PasswordHash
		}
		r.Input["fields"] = s.Configuration.Input.ModuleFields
	case "resize":
		r.Method = "change_package"
	case "start", "stop", "reboot", "force_off":
		r.Method = "power"
		r.Input["action"] = action
	case "action":
		r.ActionID = params.ActionID
		r.Input = params.Input
		r.Secrets = params.Secrets
	default:
		raw, _ := json.Marshal(params.Redacted())
		_ = json.Unmarshal(raw, &r.Input)
	}
	if params.Password != "" {
		if r.Secrets == nil {
			r.Secrets = serverv1.Fields{}
		}
		r.Secrets["password"] = params.Password
	}
	err := r.Seal()
	return r, err
}
