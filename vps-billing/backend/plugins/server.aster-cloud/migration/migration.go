package migration

import (
	"aster/billing/internal/modulejobs"
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	astercloud "aster/billing/plugins/server.aster-cloud"
	"aster/billing/plugins/server.aster-cloud/controllerapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
)

// MigrationPlan contains no credential material. Select exactly one old
// controller; each invocation creates one directly bound connection.
type MigrationPlan struct {
	ConnectionID string             `json:"connection_id"`
	Config       astercloud.Config  `json:"config"`
	Targets      []pluginapi.Target `json:"allowed_targets"`
	Enabled      bool               `json:"enabled"`
}

func PlanControllerMigration(raw []byte, controller, connection string) (MigrationPlan, controllerapi.Profile, error) {
	var policy controllerapi.Policy
	var out MigrationPlan
	var selected controllerapi.Profile
	if pluginapi.DecodeStrict(raw, 128<<10, &policy) != nil || policy.Schema != 1 || !pluginapi.ValidConnectionID(connection) {
		return out, selected, serverv1.ErrConfiguration
	}
	count := 0
	for _, profile := range policy.Controllers {
		if profile.ID == controller {
			selected = profile
			count++
		}
	}
	if count != 1 {
		return out, selected, serverv1.ErrConfiguration
	}
	u, err := url.Parse(selected.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return out, selected, serverv1.ErrConfiguration
	}
	port := 443
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return out, selected, serverv1.ErrConfiguration
		}
	}
	out = MigrationPlan{ConnectionID: connection, Config: astercloud.Config{Schema: 1, ControllerID: selected.ID, Endpoint: selected.Endpoint, AllowedCIDRs: selected.AllowedCIDRs, CustomCA: selected.CAFile != ""}, Targets: []pluginapi.Target{{Host: u.Hostname(), Port: port}}, Enabled: selected.Enabled}
	encoded, _ := json.Marshal(out.Config)
	if _, err = astercloud.DecodeConfig(encoded, out.Targets); err != nil {
		return MigrationPlan{}, selected, err
	}
	return out, selected, nil
}

type migratedBinding struct {
	ModuleID     string          `json:"module_id"`
	ConnectionID string          `json:"connection_id"`
	Config       serverv1.Fields `json:"config"`
	ConfigDigest string          `json:"config_digest"`
}

func migrationBinding(connection, preset string) (migratedBinding, error) {
	cfg, digest, err := astercloud.Description().ProductSchema.Normalize(serverv1.Fields{"preset_id": preset})
	if err != nil || preset == "" {
		return migratedBinding{}, serverv1.ErrConfiguration
	}
	return migratedBinding{ModuleID: astercloud.ID, ConnectionID: connection, Config: cfg, ConfigDigest: digest}, nil
}

// MigrateControllerDatabase runs after the Core schema migrations with the API
// stopped. It never sends RPCs or rewrites a dispatched request. In-flight old
// jobs block migration so their original protocol can first be reconciled.
func MigrateControllerDatabase(ctx context.Context, db *sql.DB, plan MigrationPlan) error {
	if db == nil || !pluginapi.ValidConnectionID(plan.ConnectionID) {
		return serverv1.ErrConfiguration
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var pending int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM provisioning_jobs WHERE state IN ('queued','sending','accepted','uncertain') AND JSON_UNQUOTE(JSON_EXTRACT(request,'$.product.controller_id'))=?`, plan.Config.ControllerID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("reconcile outstanding legacy provisioning jobs before migration")
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_tasks t JOIN services s ON s.id=t.service_id WHERE s.controller_id=? AND s.module_id IS NULL AND t.status IN ('running','uncertain')`, plan.Config.ControllerID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending != 0 {
		return errors.New("reconcile outstanding legacy service tasks before migration")
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT module_id FROM server_connections WHERE id=? FOR UPDATE", plan.ConnectionID).Scan(&existing)
	if err == nil {
		return errors.New("connection already exists; migration will not replace it")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	desc, _ := json.Marshal(astercloud.Description())
	if _, err = tx.ExecContext(ctx, "INSERT INTO server_connections(id,module_id,name,enabled,status,description) VALUES(?,?,?,?,?,?)", plan.ConnectionID, astercloud.ID, plan.Config.ControllerID, plan.Enabled, "unconfigured", desc); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,remote_product_id FROM products WHERE controller_id=? AND module IS NULL FOR UPDATE", plan.Config.ControllerID)
	if err != nil {
		return err
	}
	type product struct{ id, preset string }
	products := []product{}
	for rows.Next() {
		var p product
		if err = rows.Scan(&p.id, &p.preset); err != nil {
			rows.Close()
			return err
		}
		products = append(products, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	orderSchema, _ := json.Marshal(astercloud.Description().OrderSchema)
	// Core 0.5.10 catalog plans were copied into products with the same id by
	// migration 016. Carry their explicit presets across without inventing one.
	legacyRows, err := tx.QueryContext(ctx, `SELECT plan_id,spec FROM product_configurations WHERE JSON_UNQUOTE(JSON_EXTRACT(spec,'$.controller_id'))=? FOR UPDATE`, plan.Config.ControllerID)
	if err != nil {
		return err
	}
	legacy := []product{}
	for legacyRows.Next() {
		var id string
		var raw []byte
		var spec controllerapi.ProductSpec
		if err = legacyRows.Scan(&id, &raw); err != nil {
			legacyRows.Close()
			return err
		}
		if json.Unmarshal(raw, &spec) != nil || !spec.Valid() || spec.Mode != "preset" {
			legacyRows.Close()
			return errors.New("map custom legacy configurations to controller presets before migration")
		}
		legacy = append(legacy, product{id, spec.RemoteProductID})
	}
	err = legacyRows.Err()
	legacyRows.Close()
	if err != nil {
		return err
	}
	for _, p := range legacy {
		binding, err := migrationBinding(plan.ConnectionID, p.preset)
		if err != nil {
			return err
		}
		spec := modulejobs.ProductSpec{Mode: "module", ModuleID: binding.ModuleID, ConnectionID: binding.ConnectionID, Config: binding.Config}
		raw, _ := json.Marshal(spec)
		digest, err := spec.Digest()
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE product_configurations SET spec=?,configuration_sha256=?,revision=revision+1,updated_at=UTC_TIMESTAMP(6) WHERE plan_id=?", raw, digest, p.id); err != nil {
			return err
		}
		products = append(products, p)
	}
	for _, p := range products {
		binding, err := migrationBinding(plan.ConnectionID, p.preset)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(binding)
		if _, err = tx.ExecContext(ctx, "UPDATE products SET module=?,order_schema=?,revision=revision+1 WHERE id=?", raw, orderSchema, p.id); err != nil {
			return err
		}
	}
	if len(legacy) > 0 {
		if _, err = tx.ExecContext(ctx, "UPDATE currency_settings SET prices_revision=prices_revision+1 WHERE id=1"); err != nil {
			return err
		}
	}
	// Service snapshots retain their purchased preset, rather than today's plan.
	rows, err = tx.QueryContext(ctx, `SELECT id,configuration_snapshot FROM services WHERE controller_id=? AND module_id IS NULL FOR UPDATE`, plan.Config.ControllerID)
	if err != nil {
		return err
	}
	type service struct {
		id     string
		config map[string]any
	}
	services := []service{}
	for rows.Next() {
		var s service
		var raw []byte
		if err = rows.Scan(&s.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if json.Unmarshal(raw, &s.config) != nil {
			rows.Close()
			return serverv1.ErrConfiguration
		}
		services = append(services, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, s := range services {
		preset, _ := s.config["remote_product_id"].(string)
		binding, err := migrationBinding(plan.ConnectionID, preset)
		if err != nil {
			return errors.New("service lacks an immutable preset; map it explicitly before migration")
		}
		s.config["module"] = binding
		delete(s.config, "controller_id")
		delete(s.config, "remote_product_id")
		raw, _ := json.Marshal(s.config)
		if _, err = tx.ExecContext(ctx, "UPDATE services SET module_id=?,connection_id=?,configuration_snapshot=?,revision=revision+1 WHERE id=?", astercloud.ID, plan.ConnectionID, raw, s.id); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE os_images SET connection_id=? WHERE controller_id=?", plan.ConnectionID, plan.Config.ControllerID); err != nil {
		return err
	}
	return tx.Commit()
}
