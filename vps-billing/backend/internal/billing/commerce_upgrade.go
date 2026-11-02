package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// The controller adapter supplies capabilities. Nil keeps every capability
// unavailable; catalog configuration alone never asserts controller support.
type ServiceInspector06 interface {
	InspectService(context.Context, api.AdminService) (api.Service, error)
}

type planChange06 struct {
	Product       api.AdminProduct       `json:"product"`
	Configuration serviceConfiguration06 `json:"configuration"`
	Recurring     int64                  `json:"recurring_minor"`
	Reserved      bool                   `json:"reserved"`
}
type calculatedUpgrade06 struct {
	Quote           api.UpgradeQuote
	Change          planChange06
	ServiceRevision int64
	ServiceID       string
	Due             string
}

func upgradeDigest06(v calculatedUpgrade06) string {
	v.Quote.QuoteDigest = ""
	raw, _ := json.Marshal(v)
	return digest(string(raw))
}
func matchUpgrade06(v calculatedUpgrade06, want string, now time.Time) bool {
	for second := 1; second <= 600; second++ {
		v.Quote.ExpiresAt = stamp06(now.UTC().Truncate(time.Second).Add(time.Duration(second) * time.Second))
		if upgradeDigest06(v) == want {
			return true
		}
	}
	return false
}
func upgradeEligible06(s serviceRecord06, at time.Time) error {
	if s.View.Status != "active" || s.View.Cancellation != nil || s.ProductID == nil || s.View.Cycle == "one_time" || !s.Due.After(at) {
		return serviceConflict06()
	}
	if s.LastChange.Valid && at.Before(s.LastChange.Time.Add(10*time.Minute)) {
		return problem(409, "UPGRADE_RATE_LIMITED", "Wait ten minutes between plan changes")
	}
	return nil
}
func calculateUpgrade06(s serviceRecord06, p api.AdminProduct, now time.Time) (calculatedUpgrade06, error) {
	v := calculatedUpgrade06{ServiceID: s.View.ID, ServiceRevision: s.View.Revision, Due: stamp06(s.Due)}
	if err := upgradeEligible06(s, now); err != nil {
		return v, err
	}
	sameController := s.View.ControllerID == nil && p.ControllerID == nil || s.View.ControllerID != nil && p.ControllerID != nil && *s.View.ControllerID == *p.ControllerID
	if p.ID == *s.ProductID || p.NodeID != s.Configuration.NodeID || p.Kind != s.View.Kind || !sameController || !p.Available || p.Specs.DiskGB < s.View.Specs.DiskGB {
		return v, ErrCommerceUnavailable
	}
	input := s.Configuration.Input
	input.ProductID = p.ID
	input.Quantity = 1
	input.RootPassword = ""
	input.SSHKeyIDs = nil
	quote, err := commerce.CalculateQuote(api.CreateQuoteInput{Currency: s.View.Recurring.Currency, Items: []api.CartItemInput{input}}, map[string]api.AdminProduct{p.ID: p}, s.View.Recurring.DecimalPlaces, nil, commerce.CouponEligibility{}, now)
	if err != nil {
		return v, err
	}
	recurring := quote.Items[0].RecurringMinor
	amount, days, err := commerce.Prorate(s.View.Recurring.AmountMinor, recurring, s.View.Cycle, s.Due, now)
	if err != nil {
		return v, err
	}
	v.Quote = api.UpgradeQuote{TargetProductID: p.ID, Prorated: commerce.Money(s.View.Recurring.Currency, s.View.Recurring.DecimalPlaces, amount), DaysRemaining: days, RequiresRestart: true, ExpiresAt: stamp06(now.UTC().Truncate(time.Second).Add(10 * time.Minute))}
	config := s.Configuration
	config.ProductID = p.ID
	config.RemoteProductID = p.RemoteProductID
	config.Input = input
	v.Change = planChange06{Product: p, Configuration: config, Recurring: recurring}
	v.Quote.QuoteDigest = upgradeDigest06(v)
	return v, nil
}
func (m *MySQL) UpgradeQuote06(ctx context.Context, owner, id, target string, at time.Time) (api.UpgradeQuote, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.UpgradeQuote{}, err
	}
	defer tx.Rollback()
	s, err := readService06(ctx, tx, owner, id, "")
	if err != nil {
		return api.UpgradeQuote{}, err
	}
	products, err := readProducts06(ctx, tx, false, target, s.View.Recurring.Currency, "", "", "")
	if err != nil {
		return api.UpgradeQuote{}, err
	}
	if len(products) != 1 {
		return api.UpgradeQuote{}, ErrCommerceUnavailable
	}
	v, err := calculateUpgrade06(s, products[0], at)
	if err != nil {
		return v.Quote, err
	}
	return v.Quote, tx.Commit()
}
func (m *MySQL) UpgradeOptions06(ctx context.Context, owner, id string, at time.Time) ([]api.UpgradeOption, error) {
	out := []api.UpgradeOption{}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	s, err := readService06(ctx, tx, owner, id, "")
	if err != nil {
		return out, err
	}
	if err = upgradeEligible06(s, at); err != nil {
		return out, err
	}
	products, err := readProducts06(ctx, tx, false, "", s.View.Recurring.Currency, "", "", "")
	if err != nil {
		return out, err
	}
	for _, p := range products {
		v, err := calculateUpgrade06(s, p, at)
		if err != nil {
			continue
		}
		direction := "upgrade"
		if v.Change.Recurring < s.View.Recurring.AmountMinor {
			direction = "downgrade"
		}
		out = append(out, api.UpgradeOption{ProductID: p.ID, ProductName: text06(p.Name), Specs: p.Specs, Direction: direction, Recurring: commerce.Money(s.View.Recurring.Currency, s.View.Recurring.DecimalPlaces, v.Change.Recurring), Prorated: v.Quote.Prorated, Available: true})
	}
	return out, tx.Commit()
}
func reservePlanChange06(ctx context.Context, tx *sql.Tx, change *planChange06) error {
	if change.Product.Stock == nil {
		return nil
	}
	result, err := tx.ExecContext(ctx, "UPDATE products SET stock=stock-1 WHERE id=? AND stock>0", change.Product.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return commerce.ErrQuoteChanged
	}
	change.Reserved = true
	return nil
}
func applyPlanChange06(ctx context.Context, tx *sql.Tx, s serviceRecord06, change planChange06, at time.Time) (api.ServiceTask, error) {
	var task api.ServiceTask
	// Billing changes once in the same transaction as payment/credit. Actual
	// resource specifications remain the confirmed snapshot until resize succeeds.
	if s.ProductID != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE products SET stock=stock+1 WHERE id=? AND stock IS NOT NULL", *s.ProductID); err != nil {
			return task, err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE services SET product_id=?,plan_name=?,recurring_minor=?,price_minor=?,price_override_minor=NULL,last_change_at=?,revision=revision+1 WHERE id=?", change.Product.ID, text06(change.Product.Name), change.Recurring, change.Recurring, at, s.View.ID)
	if err != nil {
		return task, err
	}
	return queueServiceTask06(ctx, tx, s.View.ID, "resize", change, at)
}
func (m *MySQL) UpgradeService06(ctx context.Context, actor User, id string, in api.UpgradeServiceInput, key string, at time.Time) (api.UpgradeResult, error) {
	if !keyPattern.MatchString(key) {
		return nil, ErrCommerceInput
	}
	raw, _ := json.Marshal(in)
	if api.ValidateInput("upgradeService", raw) != nil {
		return nil, ErrCommerceInput
	}
	fp := digest(id + ":" + string(raw))
	return retryPayment(ctx, func() (api.UpgradeResult, error) {
		var out api.UpgradeResult
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, _, err = readCurrencies(ctx, tx, " FOR UPDATE"); err != nil {
			return nil, err
		}
		if err = lockServiceOwner06(ctx, tx, actor.ID, id); err != nil {
			return nil, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "service:upgrade", key, fp, &out); err != nil || replay {
			return out, err
		}
		s, err := readService06(ctx, tx, actor.ID, id, " FOR UPDATE")
		if err != nil {
			return nil, err
		}
		if err = upgradeEligible06(s, at); err != nil {
			return nil, err
		}
		var pending int
		if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM service_tasks WHERE service_id=? AND status IN ('queued','running','uncertain') AND action<>'provision')+(SELECT COUNT(*) FROM invoice_service_changes c JOIN invoices i ON i.id=c.invoice_id WHERE c.service_id=? AND i.status='unpaid')", id, id).Scan(&pending); err != nil {
			return nil, err
		}
		if pending != 0 {
			return nil, serviceConflict06()
		}
		products, err := readProducts06(ctx, tx, false, in.TargetProductID, s.View.Recurring.Currency, "", "", " FOR UPDATE")
		if err != nil {
			return nil, err
		}
		if len(products) != 1 {
			return nil, commerce.ErrQuoteChanged
		}
		v, err := calculateUpgrade06(s, products[0], at)
		if err != nil {
			return nil, err
		}
		if !matchUpgrade06(v, in.QuoteDigest, at) {
			return nil, commerce.ErrQuoteChanged
		}
		if err = reservePlanChange06(ctx, tx, &v.Change); err != nil {
			return nil, err
		}
		if v.Quote.Prorated.AmountMinor > 0 {
			period := at.UTC().Format("2006-01-02") + " – " + s.Due.UTC().Format("2006-01-02")
			invoice, err := newInvoice06(ctx, tx, actor.ID, "upgrade", nil, []api.InvoiceLine{{ServiceID: &id, Description: "Plan change to " + text06(v.Change.Product.Name), Amount: v.Quote.Prorated, Period: &period}}, s.Due, at)
			if err != nil {
				return nil, err
			}
			payload, _ := json.Marshal(v.Change)
			if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_service_changes(invoice_id,service_id,kind,payload) VALUES(?,?,'upgrade',?)", invoice.ID, id, payload); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE services SET last_change_at=?,revision=revision+1 WHERE id=?", at, id); err != nil {
				return nil, err
			}
			out = map[string]any{"status": "invoice", "invoice": customerInvoice06(invoice)}
		} else {
			if v.Quote.Prorated.AmountMinor < 0 {
				if _, err = appendCredit06(ctx, tx, actor.ID, "refund", -v.Quote.Prorated.AmountMinor, nil, "Prorated plan downgrade for service "+id, at); err != nil {
					return nil, err
				}
			}
			task, err := applyPlanChange06(ctx, tx, s, v.Change, at)
			if err != nil {
				return nil, err
			}
			out = map[string]any{"status": "scheduled", "task": task}
		}
		if err = insertAccountAudit(ctx, tx, actor.ID, actor.ID, "service.plan_changed", id); err != nil {
			return nil, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "service:upgrade", key, fp, out); err != nil {
			return nil, err
		}
		return out, tx.Commit()
	})
}
func fulfillUpgrade06(ctx context.Context, tx *sql.Tx, invoice api.AdminInvoice, at time.Time) error {
	var sid string
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT service_id,payload FROM invoice_service_changes WHERE invoice_id=? AND kind='upgrade'", invoice.ID).Scan(&sid, &raw); err != nil {
		return err
	}
	var change planChange06
	if err := json.Unmarshal(raw, &change); err != nil {
		return err
	}
	s, err := readService06(ctx, tx, invoice.ClientID, sid, " FOR UPDATE")
	if err != nil {
		return err
	}
	_, err = applyPlanChange06(ctx, tx, s, change, at)
	return err
}
func releaseUpgradeStock06(ctx context.Context, tx *sql.Tx, invoice api.AdminInvoice) error {
	if invoice.Kind != "upgrade" {
		return nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT payload FROM invoice_service_changes WHERE invoice_id=? AND kind='upgrade'", invoice.ID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var change planChange06
	if err = json.Unmarshal(raw, &change); err != nil {
		return err
	}
	if change.Reserved {
		_, err = tx.ExecContext(ctx, "UPDATE products SET stock=stock+1 WHERE id=? AND stock IS NOT NULL", change.Product.ID)
	}
	return err
}
