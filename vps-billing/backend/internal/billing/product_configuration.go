package billing

import (
	"aster/billing/internal/controllers"
	"context"
	"errors"
	"time"
)

var ErrProductConfiguration = errors.New("invalid product configuration")
var ErrProductConfigurationConflict = errors.New("product configuration revision changed")
var ErrProvisioningState = errors.New("provisioning state does not permit operation")
var ErrProvisioningUnavailable = errors.New("provisioning configuration unavailable")

// ProductConfiguration is administrator-only, independent of public financial
// PlanSnapshot. Controller endpoints and credentials never belong in this data.
type ProductConfiguration struct {
	PlanID              string                  `json:"plan_id"`
	Revision            int64                   `json:"revision"`
	PriceRevision       int64                   `json:"price_revision"`
	Spec                controllers.ProductSpec `json:"spec"`
	ConfigurationSHA256 string                  `json:"configuration_sha256"`
}
type ProductConfigurationInput struct {
	Revision int64                   `json:"revision"`
	Spec     controllers.ProductSpec `json:"spec"`
	Reason   string                  `json:"reason"`
}
type ProductConfigurationStore interface {
	ProductConfiguration(context.Context, User, string) (ProductConfiguration, error)
	UpdateProductConfiguration(context.Context, User, string, ProductConfigurationInput) (ProductConfiguration, error)
}
type orderProductConfiguration struct {
	PlanID    string
	Revision  int64
	Spec      controllers.ProductSpec
	SHA256    string
	CreatedAt time.Time
}

func defaultProductConfiguration(planID string) ProductConfiguration {
	spec := controllers.DisabledSpec()
	hash, _ := spec.Digest()
	return ProductConfiguration{PlanID: planID, Revision: 1, Spec: spec, ConfigurationSHA256: hash}
}
func validProductConfiguration(c ProductConfiguration) bool {
	hash, err := c.Spec.Digest()
	return validCatalogID(c.PlanID) && c.Revision >= 1 && c.Revision <= MaxMinor && err == nil && hash == c.ConfigurationSHA256
}
func cloneProductConfiguration(c ProductConfiguration) ProductConfiguration {
	c.Spec = c.Spec.Clone()
	return c
}
func productSnapshot(c ProductConfiguration, at time.Time) orderProductConfiguration {
	return orderProductConfiguration{PlanID: c.PlanID, Revision: c.Revision, Spec: c.Spec.Clone(), SHA256: c.ConfigurationSHA256, CreatedAt: at}
}
func (s orderProductConfiguration) valid() bool {
	return validProductConfiguration(ProductConfiguration{PlanID: s.PlanID, Revision: s.Revision, Spec: s.Spec, ConfigurationSHA256: s.SHA256})
}
func (m *Memory) productConfigurationLocked(planID string) (ProductConfiguration, error) {
	c, ok := m.productConfigurations[planID]
	if !ok {
		c = defaultProductConfiguration(planID)
	}
	if !validProductConfiguration(c) {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	c.PriceRevision = m.pricesRevision
	return cloneProductConfiguration(c), nil
}
func (m *Memory) ProductConfiguration(ctx context.Context, actor User, planID string) (ProductConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return ProductConfiguration{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.activeAdministratorLocked(actor.ID); err != nil {
		return ProductConfiguration{}, err
	}
	found := false
	for _, p := range m.plans {
		if p.ID == planID {
			found = true
			break
		}
	}
	if !found || !validCatalogID(planID) {
		return ProductConfiguration{}, ErrNotFound
	}
	return m.productConfigurationLocked(planID)
}
func (m *Memory) UpdateProductConfiguration(ctx context.Context, actor User, planID string, input ProductConfigurationInput) (ProductConfiguration, error) {
	if err := ctx.Err(); err != nil {
		return ProductConfiguration{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	admin, err := m.activeAdministratorLocked(actor.ID)
	if err != nil {
		return ProductConfiguration{}, err
	}
	reason, ok := catalogReason(input.Reason)
	if !ok || !input.Spec.Valid() || input.Revision < 1 || input.Revision >= MaxMinor {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	found := false
	for _, p := range m.plans {
		if p.ID == planID {
			found = true
			break
		}
	}
	if !found || !validCatalogID(planID) {
		return ProductConfiguration{}, ErrNotFound
	}
	old, err := m.productConfigurationLocked(planID)
	if err != nil {
		return ProductConfiguration{}, err
	}
	if old.Revision != input.Revision {
		return ProductConfiguration{}, ErrProductConfigurationConflict
	}
	if m.pricesRevision >= MaxMinor {
		return ProductConfiguration{}, ErrProductConfiguration
	}
	hash, _ := input.Spec.Digest()
	c := ProductConfiguration{PlanID: planID, Revision: old.Revision + 1, PriceRevision: m.pricesRevision + 1, Spec: input.Spec.Clone(), ConfigurationSHA256: hash}
	if m.productConfigurations == nil {
		m.productConfigurations = map[string]ProductConfiguration{}
	}
	m.productConfigurations[planID] = c
	m.pricesRevision++
	m.audits = append(m.audits, catalogAudit(admin, "product.configuration.updated", planID, c.Revision, reason))
	return cloneProductConfiguration(c), nil
}
