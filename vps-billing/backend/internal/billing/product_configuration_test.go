package billing

import (
	"aster/billing/internal/controllers"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func productConfigurationFixture(t *testing.T, makeFixture func(*testing.T) catalogFixture) (catalogFixture, string, ProductConfigurationStore) {
	t.Helper()
	f := makeFixture(t)
	view := readCatalog(t, f)
	out, err := f.catalog.CreateCatalogPlan(t.Context(), f.admin, catalogTestInput(t, f, view.Revision))
	if err != nil {
		t.Fatal(err)
	}
	return f, out.Plan.ID, f.store.(ProductConfigurationStore)
}
func presetProductSpec() controllers.ProductSpec {
	return controllers.ProductSpec{Mode: "preset", ControllerID: "local-test", RemoteProductID: "plan-123"}
}
func customProductSpec() controllers.ProductSpec {
	return controllers.ProductSpec{Mode: "custom", ControllerID: "local-test", Resources: &controllers.Resources{ComputeNodeID: "node-12", ImageID: "image-1", CPUCores: 2, MemoryMiB: 2048, SystemDiskGiB: 40, NetworkType: "normal", TrafficDirection: "all", IPv4Count: 1}}
}
func privateOrderConfiguration(t *testing.T, f catalogFixture, id string) (orderProductConfiguration, bool) {
	t.Helper()
	switch m := f.store.(type) {
	case *Memory:
		m.mu.Lock()
		defer m.mu.Unlock()
		s, ok := m.orderProductConfigurations[id]
		s.Spec = s.Spec.Clone()
		return s, ok
	case *MySQL:
		s, err := readOrderProductConfiguration(t.Context(), m.DB, id, "")
		if errors.Is(err, ErrProvisioningUnavailable) {
			return s, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return s, true
	}
	t.Fatal("unknown fixture")
	return orderProductConfiguration{}, false
}
func deletePrivateOrderConfiguration(t *testing.T, f catalogFixture, id string) {
	t.Helper()
	switch m := f.store.(type) {
	case *Memory:
		m.mu.Lock()
		delete(m.orderProductConfigurations, id)
		m.mu.Unlock()
	case *MySQL:
		migrationExec(t, t.Context(), m.DB, "DELETE FROM order_product_configurations WHERE order_id=?", id)
	}
}
func TestProductConfigurationMemory(t *testing.T) {
	runProductConfigurationSuite(t, memoryCatalogFixture)
}
func TestMySQLProductConfiguration(t *testing.T) {
	runProductConfigurationSuite(t, mysqlCatalogFixture)
}
func runProductConfigurationSuite(t *testing.T, makeFixture func(*testing.T) catalogFixture) {
	t.Run("private_versioned_configuration_stales_quotes_not_money", func(t *testing.T) {
		f, pid, store := productConfigurationFixture(t, makeFixture)
		ctx := t.Context()
		initial, err := store.ProductConfiguration(ctx, f.admin, pid)
		if err != nil || initial.Revision != 1 || initial.Spec.Mode != "disabled" || !validProductConfiguration(initial) {
			t.Fatal(initial, err)
		}
		plans, err := f.store.Plans(ctx, "CNY")
		if err != nil {
			t.Fatal(err)
		}
		var p Plan
		for _, v := range plans {
			if v.ID == pid {
				p = v
			}
		}
		quote := PriceQuote{p.PriceMinor, p.CurrencyFormat.DecimalPlaces, p.PriceRevision}
		beforeOrder, replay, err := f.store.(interface {
			CreateQuotedOrder(ctx context.Context, u User, pid, host, key, currency string, quote *PriceQuote) (Order, bool, error)
		}).CreateQuotedOrder(ctx, f.customer, pid, "before-config", "before-config-key", "CNY", &quote)
		if err != nil || replay {
			t.Fatal(beforeOrder, replay, err)
		}
		old, ok := privateOrderConfiguration(t, f, beforeOrder.ID)
		if !ok || old.Spec.Mode != "disabled" || old.Revision != 1 {
			t.Fatal(old, ok)
		}
		orders, invoices, services := catalogHistory(t, f)
		// The replay contract returns persisted data. MySQL DATETIME(6) has
		// microsecond precision; compare every other field with the creation
		// response and permit only its exact truncation/rounding at that scale.
		foundStored := false
		for _, persisted := range orders {
			if persisted.ID != beforeOrder.ID {
				continue
			}
			expected := cloneOrder(beforeOrder)
			if _, mysqlStore := f.store.(*MySQL); mysqlStore {
				if !persisted.CreatedAt.Equal(beforeOrder.CreatedAt.Truncate(time.Microsecond)) && !persisted.CreatedAt.Equal(beforeOrder.CreatedAt.Round(time.Microsecond)) {
					t.Fatal("stored creation time differs beyond DATETIME(6) precision", beforeOrder.CreatedAt, persisted.CreatedAt)
				}
				expected.CreatedAt = persisted.CreatedAt
			}
			if !reflect.DeepEqual(expected, persisted) {
				t.Fatal("persisted creation changed non-time order fields", expected, persisted)
			}
			beforeOrder = persisted
			foundStored = true
			break
		}
		if !foundStored {
			t.Fatal("created order missing from persisted history")
		}
		// Read after creating the initial order: admin currency usage includes
		// that new obligation. Only the following configuration save is under test.
		currencies, err := f.store.Currencies(ctx, true)
		if err != nil {
			t.Fatal(err)
		}
		input := ProductConfigurationInput{Revision: 1, Spec: customProductSpec(), Reason: "Operator reviewed provisioning parameters"}
		saved, err := store.UpdateProductConfiguration(ctx, f.admin, pid, input)
		if err != nil || saved.Revision != 2 || saved.PriceRevision != initial.PriceRevision+1 || saved.Spec.Mode != "custom" {
			t.Fatal(saved, err)
		}
		input.Spec.Resources.CPUCores = 99
		saved.Spec.Resources.CPUCores = 88
		got, err := store.ProductConfiguration(ctx, f.admin, pid)
		if err != nil || got.Spec.Resources.CPUCores != 2 {
			t.Fatal("mutable pointer escaped", got, err)
		}
		newCurrencies, err := f.store.Currencies(ctx, true)
		if err != nil || !reflect.DeepEqual(currencies, newCurrencies) {
			t.Fatal("configuration changed currency registry", err)
		}
		afterOrders, afterInvoices, afterServices := catalogHistory(t, f)
		if !reflect.DeepEqual(orders, afterOrders) || !reflect.DeepEqual(invoices, afterInvoices) || !reflect.DeepEqual(services, afterServices) {
			t.Fatal("configuration changed financial history")
		}
		qs := f.store.(interface {
			CreateQuotedOrder(context.Context, User, string, string, string, string, *PriceQuote) (Order, bool, error)
		})
		if _, _, err = qs.CreateQuotedOrder(ctx, f.customer, pid, "stale", "stale-config-key", "CNY", &quote); !errors.Is(err, ErrPriceChanged) {
			t.Fatal("stale confirmation bought new resources", err)
		}
		existing, replay, err := qs.CreateQuotedOrder(ctx, f.customer, pid, "before-config", "before-config-key", "CNY", &quote)
		if err != nil || !replay || !reflect.DeepEqual(existing, beforeOrder) {
			t.Fatal("historical key replay changed", existing, replay, err)
		}
		frozen, _ := privateOrderConfiguration(t, f, beforeOrder.ID)
		if !reflect.DeepEqual(old, frozen) {
			t.Fatal("prior private snapshot changed")
		}
		next, _, err := f.store.CreateOrder(ctx, f.customer, pid, "after-config", "after-config-key", "CNY")
		if err != nil {
			t.Fatal(err)
		}
		snapshot, ok := privateOrderConfiguration(t, f, next.ID)
		if !ok || snapshot.Revision != 2 || snapshot.Spec.Resources.CPUCores != 2 || snapshot.SHA256 != got.ConfigurationSHA256 {
			t.Fatal("new order did not freeze module parameters", snapshot, ok)
		}
		raw, _ := json.Marshal(next)
		for _, secret := range []string{"controller_id", "configuration_sha256", "remote_product_id", "compute_node_id"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("private snapshot leaked into Order", string(raw))
			}
		}
		_, err = store.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 2, Spec: controllers.DisabledSpec(), Reason: "Disable future dispatch"})
		if err != nil {
			t.Fatal(err)
		}
		unchanged, _ := privateOrderConfiguration(t, f, next.ID)
		if !reflect.DeepEqual(snapshot, unchanged) {
			t.Fatal("catalog save changed ordered parameters")
		}
		deletePrivateOrderConfiguration(t, f, beforeOrder.ID)
		_, replay, err = qs.CreateQuotedOrder(ctx, f.customer, pid, "before-config", "before-config-key", "CNY", &quote)
		if err != nil || !replay {
			t.Fatal("legacy replay rejected", replay, err)
		}
		if _, ok = privateOrderConfiguration(t, f, beforeOrder.ID); ok {
			t.Fatal("old order snapshot was backfilled")
		}
	})

	t.Run("duplicate_product_defaults_to_disabled_module", func(t *testing.T) {
		f, pid, store := productConfigurationFixture(t, makeFixture)
		ctx := t.Context()
		original, err := store.UpdateProductConfiguration(ctx, f.admin, pid, ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "Explicit original module configuration"})
		if err != nil {
			t.Fatal(err)
		}
		view := readCatalog(t, f)
		copied, err := f.catalog.DuplicateCatalogPlan(ctx, f.admin, pid, CatalogDuplicateInput{Revision: view.Revision, CurrencyRevision: view.CurrencyRevision, TaxonomyRevision: view.TaxonomyRevision, Reason: "Duplicate catalog specification only"})
		if err != nil {
			t.Fatal(err)
		}
		module, err := store.ProductConfiguration(ctx, f.admin, copied.Plan.ID)
		if err != nil || module.Revision != 1 || module.Spec.Mode != "disabled" || !validProductConfiguration(module) {
			t.Fatal("duplicate inherited potentially live module parameters", module, err)
		}
		unchanged, err := store.ProductConfiguration(ctx, f.admin, pid)
		if err != nil || unchanged.Revision != original.Revision || unchanged.ConfigurationSHA256 != original.ConfigurationSHA256 || !reflect.DeepEqual(unchanged.Spec, original.Spec) {
			t.Fatal("duplicate changed source module", unchanged, err)
		}
	})
	t.Run("persisted_permissions_revisions_and_invalid_input", func(t *testing.T) {
		f, pid, store := productConfigurationFixture(t, makeFixture)
		ctx := t.Context()
		in := ProductConfigurationInput{Revision: 1, Spec: presetProductSpec(), Reason: "Configure product"}
		for _, actor := range []User{f.customer, {ID: ID(), Role: "admin"}, {ID: f.customer.ID, Role: "admin"}} {
			if _, err := store.ProductConfiguration(ctx, actor, pid); !errors.Is(err, ErrAdminRequired) {
				t.Fatal("read used caller role", err)
			}
			if _, err := store.UpdateProductConfiguration(ctx, actor, pid, in); !errors.Is(err, ErrAdminRequired) {
				t.Fatal("save used caller role", err)
			}
		}
		disabled := f.admin
		disabled.Disabled = true
		f.changeActor(disabled)
		if _, err := store.UpdateProductConfiguration(ctx, f.admin, pid, in); !errors.Is(err, ErrAdminRequired) {
			t.Fatal("stale admin survived disable", err)
		}
		f.changeActor(f.admin)
		for _, bad := range []ProductConfigurationInput{{Revision: 0, Spec: presetProductSpec(), Reason: "x"}, {Revision: MaxMinor, Spec: presetProductSpec(), Reason: "x"}, {Revision: 1, Spec: presetProductSpec(), Reason: " "}, {Revision: 1, Spec: controllers.ProductSpec{Mode: "preset", ControllerID: "https://secret.invalid", RemoteProductID: "x"}, Reason: "x"}, {Revision: 1, Spec: controllers.ProductSpec{Mode: "disabled", RemoteProductID: "x"}, Reason: "x"}} {
			if _, err := store.UpdateProductConfiguration(ctx, f.admin, pid, bad); !errors.Is(err, ErrProductConfiguration) {
				t.Fatal("bad module params accepted", bad, err)
			}
		}
		if _, err := store.ProductConfiguration(ctx, f.admin, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := store.UpdateProductConfiguration(ctx, f.admin, "missing", in); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := store.UpdateProductConfiguration(ctx, f.admin, pid, in)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		successes, conflicts := 0, 0
		for err := range errs {
			if err == nil {
				successes++
			} else if errors.Is(err, ErrProductConfigurationConflict) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		if successes != 1 || conflicts != 1 {
			t.Fatal("optimistic concurrency lost", successes, conflicts)
		}
	})
}
