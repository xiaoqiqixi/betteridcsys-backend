package billing

import (
	"aster/billing/internal/staffaccess"
	"aster/billing/pkg/pluginapi/paymentv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"time"
)

type GatewaySettings struct {
	Enabled      bool              `json:"enabled"`
	Name         map[string]string `json:"name,omitempty"`
	DisplayOrder int64             `json:"display_order"`
	Revision     int64             `json:"revision"`
}
type GatewayPatch struct {
	Revision     *int64            `json:"revision"`
	Enabled      *bool             `json:"enabled,omitempty"`
	Name         map[string]string `json:"name,omitempty"`
	DisplayOrder *int64            `json:"display_order,omitempty"`
}
type gatewayStore interface {
	GatewaySettings(context.Context, string) (GatewaySettings, error)
	UpdateGateway(context.Context, User, string, GatewayPatch) (GatewaySettings, error)
}

func defaultGatewaySettings() GatewaySettings { return GatewaySettings{Enabled: true, Revision: 1} }
func patchGateway(s GatewaySettings, p GatewayPatch) (GatewaySettings, error) {
	if p.Revision == nil || *p.Revision < 1 || *p.Revision >= MaxMinor || paymentv1.ValidateLocalized(p.Name, 4000) != nil || p.Name != nil && len(p.Name) == 0 || p.DisplayOrder != nil && (*p.DisplayOrder < -MaxMinor || *p.DisplayOrder > MaxMinor) {
		return s, ErrPaymentInput
	}
	if *p.Revision != s.Revision {
		return s, ErrRevisionConflict
	}
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	if p.Name != nil {
		s.Name = maps.Clone(p.Name)
	}
	if p.DisplayOrder != nil {
		s.DisplayOrder = *p.DisplayOrder
	}
	s.Revision++
	return s, nil
}
func (m *Memory) GatewaySettings(_ context.Context, id string) (GatewaySettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.gatewaySettings[id]
	if !ok {
		s = defaultGatewaySettings()
	}
	s.Name = maps.Clone(s.Name)
	return s, nil
}
func (m *Memory) UpdateGateway(_ context.Context, u User, id string, p GatewayPatch) (GatewaySettings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.users[u.ID]
	policy := m.staffPolicies[u.ID]
	if !ok || current.Disabled || current.Role != "admin" || !staffaccess.Allows(policy.RoleID, policy.Builtin, policy.Permissions, "integrations.write") {
		return GatewaySettings{}, ErrAdminRequired
	}
	if !paymentv1.ValidGatewayID(id) {
		return GatewaySettings{}, ErrPaymentInput
	}
	s, ok := m.gatewaySettings[id]
	if !ok {
		s = defaultGatewaySettings()
	}
	s, err := patchGateway(s, p)
	if err != nil {
		return s, err
	}
	if m.gatewaySettings == nil {
		m.gatewaySettings = map[string]GatewaySettings{}
	}
	m.gatewaySettings[id] = s
	m.audits = append(m.audits, Audit{ID: ID(), UserID: u.ID, ActorEmail: current.Email, Action: "gateway.updated", Target: id, Detail: "Updated gateway visibility and display settings", CreatedAt: time.Now().UTC()})
	s.Name = maps.Clone(s.Name)
	return s, nil
}
func (m *MySQL) GatewaySettings(ctx context.Context, id string) (GatewaySettings, error) {
	s := defaultGatewaySettings()
	var raw []byte
	err := m.DB.QueryRowContext(ctx, "SELECT document FROM payment_gateway_settings WHERE id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(raw, &s)
	return s, err
}
func (m *MySQL) UpdateGateway(ctx context.Context, u User, id string, p GatewayPatch) (GatewaySettings, error) {
	if !paymentv1.ValidGatewayID(id) {
		return GatewaySettings{}, ErrPaymentInput
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return GatewaySettings{}, err
	}
	defer tx.Rollback()
	if err = staffWriteTx(ctx, tx, u.ID, "integrations.write"); err != nil {
		return GatewaySettings{}, err
	}
	s := defaultGatewaySettings()
	raw, _ := json.Marshal(s)
	if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO payment_gateway_settings(id,document) VALUES(?,?)", id, raw); err != nil {
		return s, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT document FROM payment_gateway_settings WHERE id=? FOR UPDATE", id).Scan(&raw); err != nil {
		return s, err
	}
	if err = json.Unmarshal(raw, &s); err != nil {
		return s, err
	}
	s, err = patchGateway(s, p)
	if err != nil {
		return s, err
	}
	raw, err = json.Marshal(s)
	if err != nil {
		return s, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE payment_gateway_settings SET document=? WHERE id=?", raw, id); err != nil {
		return s, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", ID(), u.ID, "gateway.updated", id, time.Now().UTC(), u.Email, "Updated gateway visibility and display settings"); err != nil {
		return s, err
	}
	return s, tx.Commit()
}
