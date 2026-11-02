// Package controllers defines Aster's vendor-neutral provisioning bridge.
// Its wire contract is not the undocumented native API of any cloud vendor.
package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
)

const Protocol = "aster.controller/1"

var ErrConfiguration = errors.New("invalid controller product configuration")
var ErrUnavailable = errors.New("controller unavailable")
var ErrUncertain = errors.New("controller outcome uncertain")

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var controllerID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var operationID = regexp.MustCompile(`^[0-9a-f]{32}$`)
var hashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ProductSpec contains only non-secret operator-controlled module parameters.
// Logical catalog type/region/node IDs are deliberately not resource IDs.
type ProductSpec struct {
	Mode            string     `json:"mode"` // disabled, preset, custom
	ControllerID    string     `json:"controller_id"`
	RemoteProductID string     `json:"remote_product_id"`
	Resources       *Resources `json:"resources"`
}

// Resources are fixed by an administrator, not customer-editable quantities.
// Zero bandwidth/transfer values explicitly mean "controller default" in this
// bridge contract; a vendor adapter must not silently assume other semantics.
type Resources struct {
	DatacenterID         string `json:"datacenter_id"`
	ComputeNodeID        string `json:"compute_node_id"`
	NodeGroupID          string `json:"node_group_id"`
	ImageID              string `json:"image_id"`
	CPUCores             int    `json:"cpu_cores"`
	MemoryMiB            int    `json:"memory_mib"`
	SystemDiskGiB        int    `json:"system_disk_gib"`
	SystemStorageID      string `json:"system_storage_id"`
	DataDiskGiB          int    `json:"data_disk_gib"`
	DataStorageID        string `json:"data_storage_id"`
	NetworkType          string `json:"network_type"` // normal, vpc
	BandwidthMbps        int    `json:"bandwidth_mbps"`
	InboundBandwidthMbps int    `json:"inbound_bandwidth_mbps"`
	TransferGiB          int    `json:"transfer_gib"`
	TrafficDirection     string `json:"traffic_direction"` // in, out, all
	IPv4Count            int    `json:"ipv4_count"`
	IPv6Count            int    `json:"ipv6_count"`
}

func DisabledSpec() ProductSpec { return ProductSpec{Mode: "disabled"} }

func validID(value string, required bool) bool {
	return value == "" && !required || identifier.MatchString(value)
}

func (s ProductSpec) Valid() bool {
	switch s.Mode {
	case "disabled":
		return s.ControllerID == "" && s.RemoteProductID == "" && s.Resources == nil
	case "preset":
		return controllerID.MatchString(s.ControllerID) && validID(s.RemoteProductID, true) && s.Resources == nil
	case "custom":
		if !controllerID.MatchString(s.ControllerID) || s.RemoteProductID != "" || s.Resources == nil {
			return false
		}
		r := s.Resources
		for _, v := range []string{r.DatacenterID, r.ComputeNodeID, r.NodeGroupID, r.SystemStorageID, r.DataStorageID} {
			if !validID(v, false) {
				return false
			}
		}
		return (r.DatacenterID != "" || r.ComputeNodeID != "") && validID(r.ImageID, true) &&
			r.CPUCores >= 1 && r.CPUCores <= 4096 && r.MemoryMiB >= 128 && r.MemoryMiB <= 1073741824 && r.MemoryMiB%128 == 0 &&
			r.SystemDiskGiB >= 1 && r.SystemDiskGiB <= 16777216 && r.DataDiskGiB >= 0 && r.DataDiskGiB <= 16777216 && (r.DataDiskGiB > 0 || r.DataStorageID == "") &&
			(r.NetworkType == "normal" || r.NetworkType == "vpc") &&
			r.BandwidthMbps >= 0 && r.BandwidthMbps <= 10000000 && r.InboundBandwidthMbps >= 0 && r.InboundBandwidthMbps <= 10000000 && r.TransferGiB >= 0 && r.TransferGiB <= 1073741824 &&
			(r.TrafficDirection == "in" || r.TrafficDirection == "out" || r.TrafficDirection == "all") &&
			r.IPv4Count >= 0 && r.IPv4Count <= 64 && r.IPv6Count >= 0 && r.IPv6Count <= 256 && r.IPv4Count+r.IPv6Count >= 1
	}
	return false
}

func (s ProductSpec) Clone() ProductSpec {
	if s.Resources != nil {
		r := *s.Resources
		s.Resources = &r
	}
	return s
}

func (s ProductSpec) Digest() (string, error) {
	if !s.Valid() {
		return "", ErrConfiguration
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "", ErrConfiguration
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// Request intentionally excludes money, invoice data, personal details,
// account credentials and current catalog labels. It is built from an
// immutable order snapshot, never from today's product configuration.
type Request struct {
	Protocol              string      `json:"protocol"`
	OperationID           string      `json:"operation_id"`
	OrderID               string      `json:"order_id"`
	OwnerRef              string      `json:"owner_ref"`
	Hostname              string      `json:"hostname"`
	ConfigurationRevision int64       `json:"configuration_revision"`
	ConfigurationSHA256   string      `json:"configuration_sha256"`
	Product               ProductSpec `json:"product"`
}

func (r Request) Valid() bool {
	if r.Protocol != Protocol || !operationID.MatchString(r.OperationID) || !operationID.MatchString(r.OrderID) || !operationID.MatchString(r.OwnerRef) || r.ConfigurationRevision < 1 || r.ConfigurationRevision > 9007199254740991 || !hashPattern.MatchString(r.ConfigurationSHA256) || !r.Product.Valid() || r.Product.Mode == "disabled" {
		return false
	}
	if !hostnamePattern.MatchString(r.Hostname) {
		return false
	}
	h, err := r.Product.Digest()
	return err == nil && h == r.ConfigurationSHA256
}

func (r Request) BytesAndDigest() ([]byte, string, error) {
	if !r.Valid() {
		return nil, "", ErrConfiguration
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, "", ErrConfiguration
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}

// Result must bind the exact operation, order and request bytes. Accepted
// means the controller accepted a task, not that a VM is running. Even a
// succeeded result does not manufacture a local Service or paid record.
type Result struct {
	Protocol      string `json:"protocol"`
	OperationID   string `json:"operation_id"`
	OrderID       string `json:"order_id"`
	RequestSHA256 string `json:"request_sha256"`
	Status        string `json:"status"` // accepted, succeeded, rejected
	ResourceID    string `json:"resource_id"`
}

func (r Result) Matches(request Request) bool {
	_, h, err := request.BytesAndDigest()
	if err != nil || r.Protocol != Protocol || r.OperationID != request.OperationID || r.OrderID != request.OrderID || r.RequestSHA256 != h {
		return false
	}
	switch r.Status {
	case "accepted":
		return validID(r.ResourceID, false)
	case "succeeded":
		return validID(r.ResourceID, true)
	case "rejected":
		return r.ResourceID == ""
	}
	return false
}
