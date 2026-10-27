package billing

import (
	"aster/billing/internal/controllers"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func readProductConfiguration(ctx context.Context, q currencyQuerier, planID, lock string) (ProductConfiguration, error) {
	c := defaultProductConfiguration(planID)
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT revision,spec,configuration_sha256 FROM product_configurations WHERE plan_id=?"+lock, planID).Scan(&c.Revision, &raw, &c.ConfigurationSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	if err != nil {
		return ProductConfiguration{}, err
	}
	c.Spec = controllers.ProductSpec{}
	if json.Unmarshal(raw, &c.Spec) != nil || !validProductConfiguration(c) {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	return c, nil
}
func (m *MySQL) ProductConfiguration(ctx context.Context, actor User, planID string) (ProductConfiguration, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProductConfiguration{}, err
	}
	defer tx.Rollback()
	if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
		return ProductConfiguration{}, err
	}
	_, revision, err := readCurrencies(ctx, tx, " FOR SHARE")
	if err != nil {
		return ProductConfiguration{}, err
	}
	if !validCatalogID(planID) {
		return ProductConfiguration{}, ErrNotFound
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM plans WHERE id=? FOR SHARE", planID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProductConfiguration{}, ErrNotFound
	}
	if err != nil {
		return ProductConfiguration{}, err
	}
	// plans.id retains its historical MySQL collation; persist only its actual ID.
	planID = id
	c, err := readProductConfiguration(ctx, tx, planID, " FOR SHARE")
	if err != nil {
		return ProductConfiguration{}, err
	}
	c.PriceRevision = revision
	if err = tx.Commit(); err != nil {
		return ProductConfiguration{}, err
	}
	return c, nil
}
func (m *MySQL) UpdateProductConfiguration(ctx context.Context, actor User, planID string, input ProductConfigurationInput) (ProductConfiguration, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProductConfiguration{}, err
	}
	defer tx.Rollback()
	admin, err := lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return ProductConfiguration{}, err
	}
	reason, ok := catalogReason(input.Reason)
	if !ok || !input.Spec.Valid() || input.Revision < 1 || input.Revision >= MaxMinor {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	_, revision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return ProductConfiguration{}, err
	}
	if revision >= MaxMinor {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	if !validCatalogID(planID) {
		return ProductConfiguration{}, ErrNotFound
	}
	var id string
	err = tx.QueryRowContext(ctx, "SELECT id FROM plans WHERE id=? FOR UPDATE", planID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProductConfiguration{}, ErrNotFound
	}
	if err != nil {
		return ProductConfiguration{}, err
	}
	planID = id
	old, err := readProductConfiguration(ctx, tx, planID, " FOR UPDATE")
	if err != nil {
		return ProductConfiguration{}, err
	}
	if old.Revision != input.Revision {
		return ProductConfiguration{}, ErrProductConfigurationConflict
	}
	hash, _ := input.Spec.Digest()
	raw, _ := json.Marshal(input.Spec)
	c := ProductConfiguration{PlanID: planID, Revision: old.Revision + 1, PriceRevision: revision + 1, Spec: input.Spec.Clone(), ConfigurationSHA256: hash}
	_, err = tx.ExecContext(ctx, "INSERT INTO product_configurations(plan_id,revision,spec,configuration_sha256,updated_at) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),spec=VALUES(spec),configuration_sha256=VALUES(configuration_sha256),updated_at=VALUES(updated_at)", planID, c.Revision, raw, hash, time.Now().UTC())
	if err != nil {
		return ProductConfiguration{}, err
	}
	// Invalidate old confirmations without rewriting amounts or the currency document.
	_, err = tx.ExecContext(ctx, "UPDATE currency_settings SET prices_revision=? WHERE id=1", c.PriceRevision)
	if err != nil {
		return ProductConfiguration{}, err
	}
	a := catalogAudit(admin, "product.configuration.updated", planID, c.Revision, reason)
	_, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	if err != nil {
		return ProductConfiguration{}, err
	}
	if err = tx.Commit(); err != nil {
		return ProductConfiguration{}, err
	}
	return c, nil
}
func insertOrderProductConfiguration(ctx context.Context, tx *sql.Tx, orderID string, c ProductConfiguration, at time.Time) error {
	if !validProductConfiguration(c) {
		return ErrProductConfiguration
	}
	raw, _ := json.Marshal(c.Spec)
	_, err := tx.ExecContext(ctx, "INSERT INTO order_product_configurations(order_id,plan_id,configuration_revision,spec,configuration_sha256,created_at) VALUES(?,?,?,?,?,?)", orderID, c.PlanID, c.Revision, raw, c.ConfigurationSHA256, at)
	return err
}
func readOrderProductConfiguration(ctx context.Context, q currencyQuerier, orderID, lock string) (orderProductConfiguration, error) {
	var s orderProductConfiguration
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT plan_id,configuration_revision,spec,configuration_sha256,created_at FROM order_product_configurations WHERE order_id=?"+lock, orderID).Scan(&s.PlanID, &s.Revision, &raw, &s.SHA256, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return s, ErrProvisioningUnavailable
	}
	if err != nil {
		return s, err
	}
	if json.Unmarshal(raw, &s.Spec) != nil || !s.valid() {
		return orderProductConfiguration{}, ErrProductConfiguration
	}
	return s, nil
}
