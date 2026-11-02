package billing

import (
	"aster/billing/controllerapi"
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/netip"
	"strings"
)

// Factories are installed from host-approved configuration at startup. Neither
// a catalog field nor a request may supply an endpoint or controller credential.
type ServiceController06 interface {
	Images(context.Context, string) ([]serviceapi.Image, error)
	Service(context.Context, string, string) (serviceapi.Service, error)
	Action(context.Context, string, serviceapi.ActionRequest) (serviceapi.ActionResult, bool, error)
	Operation(context.Context, string, string, string) (serviceapi.Operation, error)
	Console(context.Context, string, string) (serviceapi.Console, error)
	Metrics(context.Context, string, string, string) (serviceapi.Metrics, error)
	Snapshots(context.Context, string, string) (serviceapi.Snapshots, error)
}
type ProvisioningController06 interface {
	Send(context.Context, controllerapi.Request) (controllerapi.Result, error)
	Lookup(context.Context, controllerapi.Request) (controllerapi.Result, error)
}
type ServiceControllers06 struct {
	Services     func(context.Context, string) (ServiceController06, error)
	Provisioning func(context.Context, string) (ProvisioningController06, error)
	IDs          []string
}

func (c *ServiceControllers06) client(ctx context.Context, v api.AdminService) (ServiceController06, error) {
	if c == nil || c.Services == nil || v.Kind != "cloud_server" || v.ControllerID == nil || v.ResourceID == nil || !serviceapi.ValidID(*v.ResourceID) {
		return nil, serviceapi.ErrUnavailable
	}
	return c.Services(ctx, *v.ControllerID)
}
func (c *ServiceControllers06) InspectService(ctx context.Context, v api.AdminService) (api.Service, error) {
	out := customerService06(v)
	client, err := c.client(ctx, v)
	if err != nil {
		return out, err
	}
	remote, err := client.Service(ctx, *v.ResourceID, v.ClientID)
	if err != nil {
		return out, err
	}
	return serviceProjection06(v, remote)
}
func serviceProjection06(v api.AdminService, remote serviceapi.Service) (api.Service, error) {
	out := customerService06(v)
	if v.ResourceID == nil || remote.ResourceID != *v.ResourceID || remote.OwnerRef != v.ClientID || remote.Protocol != serviceapi.Protocol || remote.Specs.CPUCores < 0 || remote.Specs.MemoryMB < 0 || remote.Specs.DiskGB < 0 || remote.Specs.BandwidthMbps < 0 {
		return out, serviceapi.ErrUncertain
	}
	switch remote.Power {
	case "running", "stopped", "starting", "stopping", "rebooting", "reinstalling", "unknown":
	default:
		return out, serviceapi.ErrUncertain
	}
	for family, ips := range [][]string{remote.IPv4, remote.IPv6} {
		for _, ip := range ips {
			addr, err := netip.ParseAddr(ip)
			if err != nil || addr.Zone() != "" || addr.Is4() != (family == 0) || addr.Is4In6() {
				return out, serviceapi.ErrUncertain
			}
		}
	}
	raw, _ := json.Marshal(remote.Capabilities)
	_ = json.Unmarshal(raw, &out.Capabilities)
	// This SDK cannot carry the rescue root password required by the public API.
	// Advertising rescue would promise a credential the controller never receives.
	out.Capabilities.Rescue = false
	if v.Status != "active" {
		out.Capabilities = api.ServiceCapabilities{}
	}
	out.Power = &remote.Power
	out.IPv4 = append([]string{}, remote.IPv4...)
	out.IPv6 = append([]string{}, remote.IPv6...)
	out.PrimaryIPv4 = nil
	if len(out.IPv4) > 0 {
		out.PrimaryIPv4 = &out.IPv4[0]
	}
	out.Specs.CPUCores = int64(remote.Specs.CPUCores)
	out.Specs.MemoryMB = int64(remote.Specs.MemoryMB)
	out.Specs.DiskGB = int64(remote.Specs.DiskGB)
	out.Specs.BandwidthMbps = int64(remote.Specs.BandwidthMbps)
	out.Specs.IPv4 = int64(len(remote.IPv4))
	out.Specs.IPv6 = len(remote.IPv6) > 0
	if !remote.Traffic.PeriodStart.IsZero() && remote.Traffic.PeriodEnd.After(remote.Traffic.PeriodStart) {
		out.Traffic = &struct {
			UsedGB   float64 `json:"used_gb"`
			LimitGB  *int64  `json:"limit_gb"`
			ResetsAt string  `json:"resets_at"`
		}{UsedGB: float64(remote.Traffic.RXBytes)/(1<<30) + float64(remote.Traffic.TXBytes)/(1<<30), ResetsAt: stamp06(remote.Traffic.PeriodEnd)}
		if remote.Traffic.LimitBytes != nil {
			limit := int64(*remote.Traffic.LimitBytes / (1 << 30))
			if *remote.Traffic.LimitBytes%(1<<30) != 0 {
				limit++
			}
			out.Traffic.LimitGB = &limit
		}
	}
	return out, nil
}
func mergeServiceProjection06(v *api.AdminService, remote api.Service) {
	v.Capabilities = remote.Capabilities
	v.Power = remote.Power
	v.IPv4 = remote.IPv4
	v.IPv6 = remote.IPv6
	v.PrimaryIPv4 = remote.PrimaryIPv4
	v.Specs = remote.Specs
	v.Traffic = remote.Traffic
	v.Rescue = remote.Rescue
}
func serviceMetrics06(period string, raw serviceapi.Metrics) (api.ServiceMetrics, error) {
	out := api.ServiceMetrics{Range: period, StepSeconds: int64(raw.StepSeconds)}
	out.Points = make([]struct {
		At           string   `json:"at"`
		CPUPercent   *float64 `json:"cpu_percent"`
		NetInBPS     *float64 `json:"net_in_bps"`
		NetOutBPS    *float64 `json:"net_out_bps"`
		DiskReadBPS  *float64 `json:"disk_read_bps"`
		DiskWriteBPS *float64 `json:"disk_write_bps"`
	}, 0, len(raw.Points))
	if raw.StepSeconds < 1 {
		return out, serviceapi.ErrUncertain
	}
	for _, p := range raw.Points {
		if p.At.IsZero() {
			return out, serviceapi.ErrUncertain
		}
		for i, n := range []*float64{p.CPUPercent, p.NetInBPS, p.NetOutBPS, p.DiskReadBPS, p.DiskWriteBPS} {
			if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0 || i == 0 && *n > 100) {
				return out, serviceapi.ErrUncertain
			}
		}
		point := struct {
			At           string   `json:"at"`
			CPUPercent   *float64 `json:"cpu_percent"`
			NetInBPS     *float64 `json:"net_in_bps"`
			NetOutBPS    *float64 `json:"net_out_bps"`
			DiskReadBPS  *float64 `json:"disk_read_bps"`
			DiskWriteBPS *float64 `json:"disk_write_bps"`
		}{stamp06(p.At), p.CPUPercent, p.NetInBPS, p.NetOutBPS, p.DiskReadBPS, p.DiskWriteBPS}
		out.Points = append(out.Points, point)
	}
	return out, nil
}
func snapshots06(raw serviceapi.Snapshots) (api.SnapshotList, error) {
	out := api.SnapshotList{Items: []api.Snapshot{}, Limit: int64(raw.Limit), RequiresStopped: raw.RequiresStopped}
	if raw.Limit < 0 || !raw.RequiresStopped {
		return out, serviceapi.ErrUncertain
	}
	for _, s := range raw.Items {
		if !serviceapi.ValidID(s.ID) || s.CreatedAt.IsZero() || len([]rune(s.Name)) > 60 {
			return out, serviceapi.ErrUncertain
		}
		switch s.Status {
		case "creating", "ready", "restoring", "failed":
		default:
			return out, serviceapi.ErrUncertain
		}
		v := api.Snapshot{ID: s.ID, Name: s.Name, Status: s.Status, CreatedAt: stamp06(s.CreatedAt)}
		if s.SizeBytes != nil {
			size := float64(*s.SizeBytes) / (1 << 30)
			v.SizeGB = &size
		}
		out.Items = append(out.Items, v)
	}
	return out, nil
}
func imageFamily06(im serviceapi.Image) string {
	if im.Family == "windows" {
		return "windows"
	}
	name := strings.ToLower(im.ID + " " + im.Name)
	for _, family := range []string{"ubuntu", "debian", "centos", "rocky", "alma", "fedora", "alpine", "arch"} {
		if strings.Contains(name, family) {
			return family
		}
	}
	return "other"
}

// SyncImages06 preserves administrator enable/disable decisions, maps opaque
// remote IDs to stable local IDs, and never removes images used by old services.
func (m *MySQL) SyncImages06(ctx context.Context, controller string, images []serviceapi.Image) error {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, im := range images {
		if !im.Valid() {
			return serviceapi.ErrUncertain
		}
		id := "image-" + digest(controller + ":" + im.ID)[:48]
		var existing string
		err = tx.QueryRowContext(ctx, "SELECT id FROM os_images WHERE controller_id=? AND remote_image_id=? ORDER BY id LIMIT 1 FOR UPDATE", controller, im.ID).Scan(&existing)
		if err == nil {
			id = existing
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		name, version := []rune(im.Name), []rune(im.Version)
		if len(name) > 80 {
			name = name[:80]
		}
		if len(version) > 40 {
			version = version[:40]
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO os_images(id,family,name,version,min_disk_gb,enabled,controller_id,remote_image_id,sha256) VALUES(?,?,?,?,?,FALSE,?,?,?) ON DUPLICATE KEY UPDATE family=VALUES(family),name=VALUES(name),version=VALUES(version),min_disk_gb=VALUES(min_disk_gb),sha256=VALUES(sha256)`, id, imageFamily06(im), string(name), string(version), im.MinDiskGB, controller, im.ID, im.SHA256)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
