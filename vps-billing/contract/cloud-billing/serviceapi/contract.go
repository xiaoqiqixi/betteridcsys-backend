// Package serviceapi defines the aster.service/1 billing lifecycle contract.
// Password-bearing requests are ephemeral: never persist or log them. Persist
// only the digest and a copy of Params with Password removed.
package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const Protocol = "aster.service/1"

var ErrConfiguration = errors.New("invalid service request")
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidID(id string) bool { return idPattern.MatchString(id) }

// Params is a closed action-specific union. Zero fields are omitted from the
// canonical JSON; unrelated nonzero fields are rejected. Sizes are MiB/GiB,
// matching controller provisioning resources despite the historical mb/gb keys.
type Params struct {
	SetBandwidth         bool   `json:"set_bandwidth,omitempty"`
	BandwidthMbps        int    `json:"bandwidth_mbps,omitempty"`
	InboundBandwidthMbps int    `json:"inbound_bandwidth_mbps,omitempty"`
	ImageID              string `json:"image_id,omitempty"`
	Password             string `json:"password,omitempty"`
	SSHPublicKey         string `json:"ssh_public_key,omitempty"`
	StartAfter           bool   `json:"start_after,omitempty"`
	Confirmation         string `json:"confirmation,omitempty"`
	SnapshotID           string `json:"snapshot_id,omitempty"`
	Name                 string `json:"name,omitempty"`
	CPUCores             int    `json:"cpu_cores,omitempty"`
	MemoryMB             int    `json:"memory_mb,omitempty"`
	DiskGB               int    `json:"disk_gb,omitempty"`
}

func (p Params) Redacted() Params { p.Password = ""; return p }
func validPassword(p string) bool {
	return len(p) >= 1 && len(p) <= 1024 && utf8.ValidString(p) && !strings.ContainsAny(p, "\x00\r\n")
}
func validKey(k string) bool {
	return len(k) <= 8192 && !strings.ContainsAny(k, "\x00\r\n") && (strings.HasPrefix(k, "ssh-ed25519 ") || strings.HasPrefix(k, "ssh-rsa ") || strings.HasPrefix(k, "ecdsa-sha2-nistp256 "))
}
func (p Params) Valid(action string) bool {
	rest := p
	switch action {
	case "start", "stop", "reboot", "force_off", "rescue_exit", "suspend":
	case "unsuspend":
		rest.StartAfter = false
	case "terminate":
		if !ValidID(p.Confirmation) {
			return false
		}
		rest.Confirmation = ""
	case "reinstall":
		if !imagePattern.MatchString(p.ImageID) || (p.Password == "") == (p.SSHPublicKey == "") || p.Password != "" && !validPassword(p.Password) || p.SSHPublicKey != "" && !validKey(p.SSHPublicKey) {
			return false
		}
		rest.ImageID, rest.Password, rest.SSHPublicKey = "", "", ""
	case "reset_password":
		if !validPassword(p.Password) {
			return false
		}
		rest.Password = ""
	case "rescue_enter":
		if !imagePattern.MatchString(p.ImageID) {
			return false
		}
		rest.ImageID = ""
	case "snapshot_create":
		if len(p.Name) > 80 || !utf8.ValidString(p.Name) || strings.ContainsAny(p.Name, "\x00\r\n") {
			return false
		}
		rest.Name = ""
	case "snapshot_restore", "snapshot_delete":
		if !ValidID(p.SnapshotID) {
			return false
		}
		rest.SnapshotID = ""
	case "resize":
		if p.SetBandwidth {
			if p.BandwidthMbps < 0 || p.BandwidthMbps > 100000 || p.InboundBandwidthMbps < 0 || p.InboundBandwidthMbps > 100000 {
				return false
			}
			rest.SetBandwidth = false
			rest.BandwidthMbps = 0
			rest.InboundBandwidthMbps = 0
		}
		if p.CPUCores < 1 || p.CPUCores > 4096 || p.MemoryMB < 128 || p.MemoryMB > 1073741824 || p.MemoryMB%128 != 0 || p.DiskGB < 1 || p.DiskGB > 16777216 {
			return false
		}
		rest.CPUCores, rest.MemoryMB, rest.DiskGB = 0, 0, 0
	default:
		return false
	}
	return rest == (Params{})
}

type ActionRequest struct {
	Protocol    string `json:"protocol"`
	OperationID string `json:"operation_id"`
	OwnerRef    string `json:"owner_ref"`
	Action      string `json:"action"`
	Params      Params `json:"params"`
}

func (r ActionRequest) Valid() bool {
	return r.Protocol == Protocol && ValidID(r.OperationID) && ValidID(r.OwnerRef) && r.Params.Valid(r.Action)
}

// BytesAndDigest uses encoding/json's deterministic struct field ordering,
// compact encoding and HTML escaping, exactly like controllerapi.Request.
func (r ActionRequest) BytesAndDigest() ([]byte, string, error) {
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

type ActionResult struct {
	OperationID string `json:"operation_id"`
	Status      string `json:"status"` // accepted or rejected; immutable first receipt
	Code        string `json:"code"`
}
type Operation struct {
	OperationID string     `json:"operation_id"`
	Action      string     `json:"action"`
	Status      string     `json:"status"` // accepted, running, succeeded, failed, uncertain
	Code        string     `json:"code"`
	FinishedAt  *time.Time `json:"finished_at"`
}
type Image struct {
	ID        string `json:"id"`
	Family    string `json:"family"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	MinDiskGB int    `json:"min_disk_gb"`
	SHA256    string `json:"sha256"`
}

func (i Image) Valid() bool {
	return imagePattern.MatchString(i.ID) && (i.Family == "linux" || i.Family == "windows") && i.Name != "" && len(i.Name) <= 128 && i.Version != "" && len(i.Version) <= 128 && i.MinDiskGB >= 1 && shaPattern.MatchString(i.SHA256)
}

type Specs struct {
	CPUCores      int `json:"cpu_cores"`
	MemoryMB      int `json:"memory_mb"`
	DiskGB        int `json:"disk_gb"`
	BandwidthMbps int `json:"bandwidth_mbps"`
}
type Traffic struct {
	RXBytes     uint64    `json:"rx_bytes"`
	TXBytes     uint64    `json:"tx_bytes"`
	LimitBytes  *uint64   `json:"limit_bytes"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
}
type Capabilities struct {
	Power         bool `json:"power"`
	Reinstall     bool `json:"reinstall"`
	ResetPassword bool `json:"reset_password"`
	Console       bool `json:"console"`
	Rescue        bool `json:"rescue"`
	Snapshots     bool `json:"snapshots"`
	Metrics       bool `json:"metrics"`
	Upgrade       bool `json:"upgrade"`
}
type Service struct {
	Protocol     string       `json:"protocol"`
	ResourceID   string       `json:"resource_id"`
	OwnerRef     string       `json:"owner_ref"`
	Power        string       `json:"power"`
	Suspended    bool         `json:"suspended"`
	RescueActive bool         `json:"rescue_active"`
	IPv4         []string     `json:"ipv4"`
	IPv6         []string     `json:"ipv6"`
	OSImageID    string       `json:"os_image_id"`
	Specs        Specs        `json:"specs"`
	Traffic      Traffic      `json:"traffic"`
	Capabilities Capabilities `json:"capabilities"`
}
type Console struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
type MetricPoint struct {
	At           time.Time `json:"at"`
	CPUPercent   *float64  `json:"cpu_percent"`
	NetInBPS     *float64  `json:"net_in_bps"`
	NetOutBPS    *float64  `json:"net_out_bps"`
	DiskReadBPS  *float64  `json:"disk_read_bps"`
	DiskWriteBPS *float64  `json:"disk_write_bps"`
}
type Metrics struct {
	StepSeconds int           `json:"step_seconds"`
	Points      []MetricPoint `json:"points"`
}
type Snapshot struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	SizeBytes *uint64   `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}
type Snapshots struct {
	Items           []Snapshot `json:"items"`
	Limit           int        `json:"limit"`
	RequiresStopped bool       `json:"requires_stopped"`
}

func ValidRange(r string) bool { return r == "1h" || r == "24h" || r == "7d" || r == "30d" }
