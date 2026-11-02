package astercloud

import (
	"aster/billing/pkg/pluginapi/serverv1"
	"aster/billing/plugins/server.aster-cloud/serviceapi"
	"encoding/json"
)

func validateInput(in serverv1.Request) error {
	allowed := map[string]bool{}
	switch in.Method {
	case "create":
		for _, key := range []string{"hostname", "image_id", "ssh_keys", "password_hash", "fields"} {
			allowed[key] = true
		}
	case "power":
		allowed["action"] = true
	case "reinstall":
		for _, key := range []string{"image_id", "ssh_keys", "password_hash"} {
			allowed[key] = true
		}
	case "unsuspend":
		allowed["start_after"] = true
	case "terminate":
		allowed["confirmation"] = true
	case "snapshot_create":
		allowed["name"] = true
	case "snapshot_restore", "snapshot_delete":
		allowed["snapshot_id"] = true
	}
	for key := range in.Input {
		if !allowed[key] {
			return serverv1.ErrConfiguration
		}
	}
	for key := range in.Secrets {
		if key != "password" {
			return serverv1.ErrConfiguration
		}
	}
	if hash := stringField(in.Input, "password_hash"); hash != "" && !serviceapi.ValidPasswordHash(hash) {
		return serverv1.ErrConfiguration
	}
	if value, ok := in.Input["fields"]; ok && value != nil {
		var fields serverv1.Fields
		convert(value, &fields)
		if len(fields) > 0 {
			return serverv1.ErrConfiguration
		}
	}
	if in.Method == "create" {
		var keys []string
		convert(in.Input["ssh_keys"], &keys)
		if len(keys) > 1 || len(keys) > 0 && (stringField(in.Input, "password_hash") != "" || stringField(in.Secrets, "password") != "") {
			return serverv1.ErrConfiguration
		}
	}
	return nil
}
func (m *Module) actionRequest(in serverv1.Request) (serviceapi.ActionRequest, error) {
	p := serviceapi.Params{Password: stringField(in.Secrets, "password")}
	action := in.Method
	switch in.Method {
	case "power":
		action = stringField(in.Input, "action")
	case "change_package":
		action = "resize"
		p.CPUCores = intField(in.Config, "cpu_cores")
		p.MemoryMB = intField(in.Config, "memory_mb")
		p.DiskGB = intField(in.Config, "disk_gb")
		p.SetBandwidth = true
		p.BandwidthMbps = intField(in.Config, "bandwidth_mbps")
		p.InboundBandwidthMbps = p.BandwidthMbps
	case "reinstall":
		p.ImageID = stringField(in.Input, "image_id")
		p.PasswordHash = stringField(in.Input, "password_hash")
		var keys []string
		convert(in.Input["ssh_keys"], &keys)
		if len(keys) > 1 {
			return serviceapi.ActionRequest{}, serverv1.ErrConfiguration
		}
		if len(keys) == 1 {
			p.SSHPublicKey = keys[0]
		}
	case "terminate":
		p.Confirmation = in.ResourceID
	case "unsuspend":
		p.StartAfter = true
	case "snapshot_create":
		p.Name = stringField(in.Input, "name")
	case "snapshot_restore", "snapshot_delete":
		p.SnapshotID = stringField(in.Input, "snapshot_id")
	}
	r := serviceapi.ActionRequest{Protocol: serviceapi.Protocol, OperationID: upstreamID(in.OperationID), OwnerRef: in.OwnerRef, Action: action, Params: p}
	if !r.Valid() {
		return r, serverv1.ErrConfiguration
	}
	return r, nil
}
func intField(fields serverv1.Fields, key string) int {
	switch n := fields[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return int(i)
		}
	}
	return 0
}
