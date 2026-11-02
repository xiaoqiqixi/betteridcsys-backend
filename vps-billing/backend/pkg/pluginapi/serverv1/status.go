package serverv1

import "time"

type Image struct {
	ID        string `json:"id"`
	Family    string `json:"family"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	MinDiskGB int    `json:"min_disk_gb"`
	SHA256    string `json:"sha256"`
}

func (i Image) Valid() bool {
	return ValidID(i.ID) && i.Name != "" && i.MinDiskGB >= 1 && (i.SHA256 == "" || ValidDigest(i.SHA256))
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
	Protocol      string         `json:"protocol"`
	ResourceID    string         `json:"resource_id"`
	OwnerRef      string         `json:"owner_ref"`
	Power         string         `json:"power"`
	Suspended     bool           `json:"suspended"`
	RescueActive  bool           `json:"rescue_active"`
	IPv4          []string       `json:"ipv4"`
	IPv6          []string       `json:"ipv6"`
	OSImageID     string         `json:"os_image_id"`
	Specs         Specs          `json:"specs"`
	Traffic       Traffic        `json:"traffic"`
	Capabilities  Capabilities   `json:"capabilities"`
	CustomActions []CustomAction `json:"custom_actions"`
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
