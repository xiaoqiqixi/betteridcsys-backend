package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type invoiceRecord06 struct {
	Invoice  api.AdminInvoice
	Version  int
	Refunded int64
	Review   bool
}

// Staff can operate on different customers concurrently. Lock a durable request
// key before the customer row so same-key requests still serialize globally.
func financialRequestLock06(ctx context.Context, tx *sql.Tx, uid, operation, key, fingerprint string) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO commerce_requests(user_id,operation,request_key,fingerprint,response,created_at) VALUES(?,?,?,?,JSON_OBJECT(),UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE request_key=request_key", uid, "lock:"+operation, key, fingerprint); err != nil {
		return err
	}
	var stored string
	if err := tx.QueryRowContext(ctx, "SELECT fingerprint FROM commerce_requests WHERE user_id=? AND operation=? AND request_key=? FOR UPDATE", uid, "lock:"+operation, key).Scan(&stored); err != nil {
		return err
	}
	if stored != fingerprint {
		return ErrConflict
	}
	return nil
}

func customerInvoice06(v api.AdminInvoice) api.Invoice {
	return api.Invoice{ID: v.ID, Number: v.Number, Kind: v.Kind, Status: v.Status, Lines: v.Lines, Subtotal: v.Subtotal, CreditApplied: v.CreditApplied, Total: v.Total, AmountPaid: v.AmountPaid, BalanceDue: v.BalanceDue, IssuedAt: v.IssuedAt, DueAt: v.DueAt, PaidAt: v.PaidAt, OrderID: v.OrderID, Payments: v.Payments, Issuer: v.Issuer, BilledTo: v.BilledTo}
}
func readInvoice06(ctx context.Context, q currencyQuerier, id, owner, lock string) (invoiceRecord06, error) {
	var record invoiceRecord06
	v := &record.Invoice
	query := `SELECT i.id,i.number,i.user_id,u.email,i.order_id,i.status,i.kind,i.commerce_version,i.amount_minor,i.subtotal_minor,i.credit_applied_minor,i.amount_paid_minor,i.refunded_minor,i.currency,i.currency_snapshot,i.issuer_snapshot,i.billed_to_snapshot,i.issued_at,i.due_at,i.paid_at,i.settlement_review FROM invoices i JOIN users u ON u.id=i.user_id WHERE i.id=?`
	args := []any{id}
	if owner != "" {
		query += " AND i.user_id=?"
		args = append(args, owner)
	}
	var currency string
	var currencyRaw, issuerRaw, billedRaw []byte
	var issued, due time.Time
	var paid sql.NullTime
	err := q.QueryRowContext(ctx, query+lock, args...).Scan(&v.ID, &v.Number, &v.ClientID, &v.ClientEmail, &v.OrderID, &v.Status, &v.Kind, &record.Version, &v.Total.AmountMinor, &v.Subtotal.AmountMinor, &v.CreditApplied.AmountMinor, &v.AmountPaid.AmountMinor, &record.Refunded, &currency, &currencyRaw, &issuerRaw, &billedRaw, &issued, &due, &paid, &record.Review)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	format, err := decodeCurrencySnapshot(currencyRaw, currency)
	if err != nil {
		return record, err
	}
	if record.Version == 0 {
		v.Subtotal.AmountMinor = v.Total.AmountMinor
		if v.Status == "paid" && v.AmountPaid.AmountMinor == 0 {
			v.AmountPaid.AmountMinor = v.Total.AmountMinor - v.CreditApplied.AmountMinor
		}
	}
	v.BalanceDue.AmountMinor, err = commerce.BalanceDue(v.Total.AmountMinor, v.AmountPaid.AmountMinor, v.CreditApplied.AmountMinor)
	if err != nil {
		return record, err
	}
	for _, m := range []*api.Money{&v.Total, &v.Subtotal, &v.CreditApplied, &v.AmountPaid, &v.BalanceDue} {
		m.Currency = currency
		m.DecimalPlaces = int64(format.DecimalPlaces)
	}
	if v.Status == "void" {
		v.Status = "cancelled"
	}
	v.IssuedAt, v.DueAt = stamp06(issued), stamp06(due)
	if paid.Valid {
		p := stamp06(paid.Time)
		v.PaidAt = &p
	}
	if len(issuerRaw) > 0 {
		var identity InvoiceIdentity
		if err = json.Unmarshal(issuerRaw, &identity); err != nil {
			return record, err
		}
		v.Issuer.Name, v.Issuer.Address = identity.Name, identity.Address
	}
	if len(billedRaw) > 0 {
		if err = json.Unmarshal(billedRaw, &v.BilledTo); err != nil {
			return record, err
		}
	} else {
		profile, err := readProfile(ctx, q, v.ClientID)
		if err != nil {
			return record, err
		}
		v.BilledTo.Name, v.BilledTo.Company, v.BilledTo.Address = profile.Name, profile.Company, profileAddress06(profile)
	}
	v.Lines = []api.InvoiceLine{}
	v.Payments = []api.InvoicePayment{}
	v.Flags = []string{}
	rows, err := q.QueryContext(ctx, "SELECT description,service_id,amount_minor,period_text FROM invoice_lines WHERE invoice_id=? ORDER BY position,id", id)
	if err != nil {
		return record, err
	}
	for rows.Next() {
		var line api.InvoiceLine
		if err = rows.Scan(&line.Description, &line.ServiceID, &line.Amount.AmountMinor, &line.Period); err != nil {
			rows.Close()
			return record, err
		}
		line.Amount.Currency, line.Amount.DecimalPlaces = currency, int64(format.DecimalPlaces)
		v.Lines = append(v.Lines, line)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record, err
	}
	if len(v.Lines) == 0 && record.Version == 0 {
		description := "Historical invoice"
		if v.OrderID != nil {
			if err = q.QueryRowContext(ctx, "SELECT plan_name FROM orders WHERE id=?", *v.OrderID).Scan(&description); err != nil {
				return record, err
			}
		}
		v.Lines = append(v.Lines, api.InvoiceLine{Description: description, Amount: v.Total})
	}
	rows, err = q.QueryContext(ctx, "SELECT id,method,gateway,reference,amount_minor,status,created_at FROM invoice_payments WHERE invoice_id=? ORDER BY created_at,id", id)
	if err != nil {
		return record, err
	}
	for rows.Next() {
		var payment api.InvoicePayment
		var at time.Time
		if err = rows.Scan(&payment.ID, &payment.Method, &payment.Gateway, &payment.Reference, &payment.Amount.AmountMinor, &payment.Status, &at); err != nil {
			rows.Close()
			return record, err
		}
		if payment.Method != "credit" && payment.Method != "gateway" {
			payment.Method = "manual"
		}
		payment.Amount.Currency, payment.Amount.DecimalPlaces = currency, int64(format.DecimalPlaces)
		payment.CreatedAt = stamp06(at)
		v.Payments = append(v.Payments, payment)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record, err
	}
	rows, err = q.QueryContext(ctx, `SELECT a.id,a.provider_id,s.transaction_id,a.amount_minor,a.state,a.created_at,COALESCE(s.disposition,'') FROM payment_attempts a LEFT JOIN payment_settlements s ON s.attempt_id=a.id WHERE a.invoice_id=? AND NOT EXISTS(SELECT 1 FROM invoice_payments p WHERE p.attempt_id=a.id) ORDER BY a.created_at,a.id`, id)
	if err != nil {
		return record, err
	}
	for rows.Next() {
		var p api.InvoicePayment
		var at time.Time
		var state, disposition string
		if err = rows.Scan(&p.ID, &p.Gateway, &p.Reference, &p.Amount.AmountMinor, &state, &at, &disposition); err != nil {
			rows.Close()
			return record, err
		}
		p.Method, p.Status = "gateway", "pending"
		switch state {
		case "settled":
			p.Status = "succeeded"
		case "expired", "voided":
			p.Status = state
		}
		if disposition == "duplicate" || disposition == "cancelled" {
			p.Status = "duplicate"
		}
		p.Amount.Currency, p.Amount.DecimalPlaces = currency, int64(format.DecimalPlaces)
		p.CreatedAt = stamp06(at)
		v.Payments = append(v.Payments, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record, err
	}
	if record.Review {
		v.Flags = append(v.Flags, "duplicate_payment")
	}
	if v.Status == "unpaid" && due.Before(time.Now()) {
		v.Flags = append(v.Flags, "overdue")
	}
	if v.Kind == "manual" {
		v.Flags = append(v.Flags, "manual")
	}
	return record, nil
}
func profileAddress06(p api.Profile) string {
	parts := []string{}
	for _, s := range []string{p.AddressLine1, p.AddressLine2, p.City, p.State, p.PostalCode, p.Country} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}
func (m *MySQL) Invoice06(ctx context.Context, owner, id string) (api.AdminInvoice, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.AdminInvoice{}, err
	}
	defer tx.Rollback()
	v, err := readInvoice06(ctx, tx, id, owner, "")
	if err != nil {
		return api.AdminInvoice{}, err
	}
	return v.Invoice, tx.Commit()
}

func (m *MySQL) InvoicePage06(ctx context.Context, owner, cursor, status, kind, search string, limit int) ([]api.AdminInvoice, *string, error) {
	out := []api.AdminInvoice{}
	if limit < 1 || limit > 100 || len(search) > 120 {
		return out, nil, ErrCommerceInput
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback()
	query := "SELECT id FROM invoices WHERE 1=1"
	args := []any{}
	if owner != "" {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	if status != "" {
		switch status {
		case "unpaid", "paid", "refunded", "partially_refunded":
			query += " AND status=?"
			args = append(args, status)
		case "cancelled":
			query += " AND status IN ('void','cancelled')"
		default:
			return out, nil, ErrCommerceInput
		}
	}
	if kind != "" {
		switch kind {
		case "order", "renewal", "upgrade", "top_up", "manual":
			query += " AND kind=?"
			args = append(args, kind)
		default:
			return out, nil, ErrCommerceInput
		}
	}
	if search != "" {
		query += " AND number LIKE ?"
		args = append(args, "%"+strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(search)+"%")
	}
	if cursor != "" {
		if !validCancellationID(cursor) {
			return out, nil, ErrCommerceInput
		}
		var issued time.Time
		lookup := "SELECT issued_at FROM invoices WHERE id=?"
		values := []any{cursor}
		if owner != "" {
			lookup += " AND user_id=?"
			values = append(values, owner)
		}
		if err = tx.QueryRowContext(ctx, lookup, values...).Scan(&issued); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrCommerceInput
			}
			return out, nil, err
		}
		query += " AND (issued_at<? OR (issued_at=? AND id<?))"
		args = append(args, issued, issued, cursor)
	}
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query+" ORDER BY issued_at DESC,id DESC LIMIT ?", args...)
	if err != nil {
		return out, nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, nil, err
	}
	var next *string
	if len(ids) > limit {
		ids = ids[:limit]
		last := ids[len(ids)-1]
		next = &last
	}
	for _, id := range ids {
		record, err := readInvoice06(ctx, tx, id, owner, "")
		if err != nil {
			return out, nil, err
		}
		out = append(out, record.Invoice)
	}
	return out, next, tx.Commit()
}

// Financial mutations lock currency, customer, order (if any), then invoice and
// attempts. Owner lookup is repeated under these locks before any mutation.
func lockInvoice06(ctx context.Context, tx *sql.Tx, owner, id string, enabled bool) (invoiceRecord06, error) {
	var zero invoiceRecord06
	config, _, err := readCurrencies(ctx, tx, " FOR SHARE")
	if err != nil {
		return zero, err
	}
	var uid string
	var oid *string
	query := "SELECT user_id,order_id FROM invoices WHERE id=?"
	args := []any{id}
	if owner != "" {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	err = tx.QueryRowContext(ctx, query, args...).Scan(&uid, &oid)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, ErrNotFound
	}
	if err != nil {
		return zero, err
	}
	var locked string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", uid).Scan(&locked); err != nil {
		return zero, err
	}
	if oid != nil {
		if err = tx.QueryRowContext(ctx, "SELECT id FROM orders WHERE id=? FOR UPDATE", *oid).Scan(&locked); err != nil {
			return zero, err
		}
	}
	out, err := readInvoice06(ctx, tx, id, owner, " FOR UPDATE")
	if err != nil {
		return zero, err
	}
	if oid != nil && out.Invoice.Kind == "order" {
		var orderOwner, currency, status string
		var total int64
		if err = tx.QueryRowContext(ctx, "SELECT user_id,currency,total_minor,status FROM orders WHERE id=?", *oid).Scan(&orderOwner, &currency, &total, &status); err != nil {
			return zero, err
		}
		v := out.Invoice
		consistent := v.Status == "unpaid" && status == "pending_payment" || (v.Status == "paid" || v.Status == "refunded" || v.Status == "partially_refunded") && (status == "paid" || status == "active") || v.Status == "cancelled" && status == "cancelled"
		if orderOwner != uid || currency != v.Total.Currency || total != v.Total.AmountMinor || !consistent {
			return zero, ErrPaymentState
		}
	}
	c, ok := config.find(out.Invoice.Total.Currency)
	if !ok || int64(c.DecimalPlaces) != out.Invoice.Total.DecimalPlaces {
		return zero, ErrPaymentState
	}
	if enabled && !c.Enabled {
		return zero, ErrCurrency
	}
	if out.Version == 0 {
		// A legacy invoice enters the partial-payment model on its first 0.6
		// mutation. Existing attempts keep their immutable amount snapshots.
		if _, err = tx.ExecContext(ctx, "UPDATE invoices SET commerce_version=1,subtotal_minor=?,amount_paid_minor=? WHERE id=?", out.Invoice.Subtotal.AmountMinor, out.Invoice.AmountPaid.AmountMinor, id); err != nil {
			return zero, err
		}
		for position, line := range out.Invoice.Lines {
			if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_lines(id,invoice_id,position,description,amount_minor) SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM invoice_lines WHERE invoice_id=? AND position=?)", ID(), id, position, line.Description, line.Amount.AmountMinor, id, position); err != nil {
				return zero, err
			}
		}
		out.Version = 1
	}
	return out, nil
}
func financialEmail06(ctx context.Context, tx *sql.Tx, v api.AdminInvoice, key string) error {
	variables := map[string]string{"invoice_number": v.Number, "amount": formatMinor06(v.Total.AmountMinor, int(v.Total.DecimalPlaces)), "currency": v.Total.Currency, "due_at": v.DueAt}
	raw, _ := json.Marshal(variables)
	_, err := tx.ExecContext(ctx, "INSERT INTO email_outbox(id,user_id,template_key,variables,attempts,next_attempt_at,last_error,created_at) VALUES(?,?,?,?,0,UTC_TIMESTAMP(6),'',UTC_TIMESTAMP(6))", ID(), v.ClientID, key, raw)
	return err
}
func formatMinor06(amount int64, decimals int) string {
	if decimals == 0 {
		return fmt.Sprint(amount)
	}
	scale := int64(1)
	for range decimals {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d", amount/scale, decimals, amount%scale)
}
func newInvoice06(ctx context.Context, tx *sql.Tx, uid, kind string, orderID *string, lines []api.InvoiceLine, due time.Time, now time.Time) (api.AdminInvoice, error) {
	var out api.AdminInvoice
	config, revision, err := readCurrencies(ctx, tx, "")
	if err != nil {
		return out, err
	}
	profile, err := readProfile(ctx, tx, uid)
	if err != nil {
		return out, err
	}
	currency, ok := config.find(profile.Currency)
	if !ok || !currency.Enabled {
		return out, ErrCurrency
	}
	total := int64(0)
	for _, line := range lines {
		total, err = commerce.Add(total, line.Amount.AmountMinor)
		if err != nil {
			return out, err
		}
	}
	if total < 0 || len(lines) == 0 || due.IsZero() {
		return out, ErrCommerceInput
	}
	var raw []byte
	var old SiteSettings
	if err = tx.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&raw); err != nil {
		return out, err
	}
	if err = json.Unmarshal(raw, &old); err != nil {
		return out, err
	}
	issuer := old.Identity()
	var site api.SiteSettings
	err = tx.QueryRowContext(ctx, "SELECT document FROM account_settings WHERE name='site'").Scan(&raw)
	if err == nil {
		if err = json.Unmarshal(raw, &site); err != nil {
			return out, err
		}
		issuer = &InvoiceIdentity{Name: site.CompanyName, Address: site.CompanyAddress, SupportEmail: site.SupportEmail}
		if issuer.Name == "" {
			issuer.Name = site.SiteName
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.ID, out.Number, out.Kind, out.ClientID, out.ClientEmail = ID(), "INV-"+strings.ToUpper(ID()[:16]), kind, uid, profile.Email
	out.OrderID = orderID
	out.Total = commerce.Money(profile.Currency, int64(currency.DecimalPlaces), total)
	out.Subtotal = out.Total
	out.BalanceDue = out.Total
	out.CreditApplied, out.AmountPaid = commerce.Money(profile.Currency, int64(currency.DecimalPlaces), 0), commerce.Money(profile.Currency, int64(currency.DecimalPlaces), 0)
	out.Status = "unpaid"
	out.IssuedAt, out.DueAt = stamp06(now), stamp06(due)
	out.Payments = []api.InvoicePayment{}
	out.Lines = []api.InvoiceLine{}
	out.Flags = []string{}
	if kind == "manual" {
		out.Flags = append(out.Flags, "manual")
	}
	out.BilledTo.Name, out.BilledTo.Company, out.BilledTo.Address = profile.Name, profile.Company, profileAddress06(profile)
	out.Issuer.Name, out.Issuer.Address = issuer.Name, issuer.Address
	issuerRaw, _ := json.Marshal(issuer)
	billedRaw, _ := json.Marshal(out.BilledTo)
	currencyRaw, _ := json.Marshal(currency.CurrencyFormat)
	_, err = tx.ExecContext(ctx, `INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot,currency_snapshot,kind,commerce_version,subtotal_minor,billed_to_snapshot) VALUES(?,?,?,?,'unpaid',?,?,?,?,?,?,?,1,?,?)`, out.ID, out.Number, uid, orderID, total, profile.Currency, now, due, issuerRaw, currencyRaw, kind, total, billedRaw)
	if err != nil {
		return out, err
	}
	for n, line := range lines {
		line.Amount = commerce.Money(profile.Currency, int64(currency.DecimalPlaces), line.Amount.AmountMinor)
		out.Lines = append(out.Lines, line)
		if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_lines(id,invoice_id,position,service_id,description,amount_minor,period_text) VALUES(?,?,?,?,?,?,?)", ID(), out.ID, n, line.ServiceID, line.Description, line.Amount.AmountMinor, line.Period); err != nil {
			return out, err
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET currency_locked=TRUE WHERE id=?", uid); err != nil {
		return out, err
	}
	for i := range config.Currencies {
		if config.Currencies[i].Code == profile.Currency {
			config.Currencies[i].PrecisionLocked = true
		}
	}
	// Invoice creation takes the registry FOR UPDATE before calling this helper.
	if err = writeCurrencies(ctx, tx, config, revision); err != nil {
		return out, err
	}
	if err = financialEmail06(ctx, tx, out, "invoice_created"); err != nil {
		return out, err
	}
	return out, nil
}

func (m *MySQL) CreateInvoice06(ctx context.Context, actor User, input api.AdminCreateInvoiceInput, key string) (api.AdminInvoice, error) {
	raw, _ := json.Marshal(input)
	if api.ValidateInput("adminCreateInvoice", raw) != nil || !keyPattern.MatchString(key) {
		return api.AdminInvoice{}, ErrCommerceInput
	}
	fingerprint := digest(string(raw))
	due, _ := time.Parse(time.RFC3339Nano, input.DueAt)
	return retryPayment(ctx, func() (api.AdminInvoice, error) {
		var out api.AdminInvoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		if _, _, err = readCurrencies(ctx, tx, " FOR UPDATE"); err != nil {
			return out, err
		}
		if _, err = accountMoney06(ctx, tx, input.ClientID, " FOR UPDATE"); err != nil {
			return out, err
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "invoices:create", key, fingerprint, &out)
		if err != nil || replay {
			return out, err
		}
		lines := []api.InvoiceLine{}
		for _, line := range input.Lines {
			lines = append(lines, api.InvoiceLine{Description: line.Description, Amount: api.Money{AmountMinor: line.AmountMinor}})
		}
		out, err = newInvoice06(ctx, tx, input.ClientID, "manual", nil, lines, due, time.Now().UTC().Truncate(time.Microsecond))
		if err != nil {
			return out, err
		}
		if out.Total.AmountMinor == 0 {
			out, err = completeInvoice06(ctx, tx, invoiceRecord06{Invoice: out, Version: 1}, time.Now().UTC())
			if err != nil {
				return out, err
			}
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "invoice.created", out.ID, 0, "Manual invoice created")); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "invoices:create", key, fingerprint, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}
func (m *MySQL) TopUp06(ctx context.Context, actor User, amount int64, key string) (api.Invoice, error) {
	if amount <= 0 || amount > MaxMinor || !keyPattern.MatchString(key) {
		return api.Invoice{}, ErrCommerceInput
	}
	fingerprint := digest(fmt.Sprint(amount))
	return retryPayment(ctx, func() (api.Invoice, error) {
		var out api.Invoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if _, _, err = readCurrencies(ctx, tx, " FOR UPDATE"); err != nil {
			return out, err
		}
		if _, err = lockAccountUser(ctx, tx, actor.ID); err != nil {
			return out, err
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "credit:top-up", key, fingerprint, &out)
		if err != nil || replay {
			return out, err
		}
		summary, err := creditSummary06(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		if amount < summary.TopUp.Min.AmountMinor || amount > summary.TopUp.Max.AmountMinor {
			return out, ErrCommerceInput
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		invoice, err := newInvoice06(ctx, tx, actor.ID, "top_up", nil, []api.InvoiceLine{{Description: "Account credit top-up", Amount: api.Money{AmountMinor: amount}}}, now.Add(72*time.Hour), now)
		if err != nil {
			return out, err
		}
		out = customerInvoice06(invoice)
		if err = recordRequest06(ctx, tx, actor.ID, "credit:top-up", key, fingerprint, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

func completeInvoice06(ctx context.Context, tx *sql.Tx, record invoiceRecord06, paid time.Time) (api.AdminInvoice, error) {
	v := record.Invoice
	if v.BalanceDue.AmountMinor != 0 || v.Status != "unpaid" {
		return v, ErrPaymentState
	}
	if v.Kind != "manual" && v.Kind != "top_up" {
		if v.Kind != "order" || v.OrderID == nil {
			return v, ErrCommerceUnavailable
		}
		var modernItems int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM order_items WHERE order_id=? AND COALESCE(JSON_EXTRACT(snapshot,'$.legacy'),JSON_EXTRACT('false','$'))<>JSON_EXTRACT('true','$')", *v.OrderID).Scan(&modernItems); err != nil {
			return v, err
		}
		if modernItems != 0 {
			return v, ErrCommerceUnavailable
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invoices SET status='paid',paid_at=? WHERE id=? AND status='unpaid'", paid, v.ID); err != nil {
		return v, err
	}
	if err := recordAffiliateCommissionTx(ctx, tx, v.ID, time.Now().UTC()); err != nil {
		return v, err
	}
	if v.OrderID != nil {
		if _, err := tx.ExecContext(ctx, "UPDATE orders SET status='paid',paid_at=? WHERE id=? AND status='pending_payment'", paid, *v.OrderID); err != nil {
			return v, err
		}
		payload, _ := json.Marshal(map[string]string{"order_id": *v.OrderID, "invoice_id": v.ID, "status": "paid"})
		if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'payment.settled',?,?)", ID(), *v.OrderID, payload, paid); err != nil {
			return v, err
		}
	}
	if v.Kind == "top_up" {
		if _, err := appendCredit06(ctx, tx, v.ClientID, "top_up", v.Total.AmountMinor, &v.ID, "Credit top-up paid", paid); err != nil {
			return v, err
		}
	}
	v.Status = "paid"
	at := stamp06(paid)
	v.PaidAt = &at
	return v, nil
}
