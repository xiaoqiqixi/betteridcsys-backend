package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type orderRecord06 struct {
	Order                 api.Order
	ClientID, ClientEmail string
}
type orderItemSnapshot06 struct {
	PasswordHash string              `json:"password_hash,omitempty"`
	Item         commerce.QuotedItem `json:"item"`
	SSHKeys      []string            `json:"ssh_keys"`
	Legacy       bool                `json:"legacy,omitempty"`
}
type serviceConfiguration06 struct {
	ManualInfo   *api.ManualServiceInfo `json:"manual_info,omitempty"`
	PasswordHash string                 `json:"password_hash,omitempty"`
	Kind         string                 `json:"kind"`
	ProductID    string                 `json:"product_id"`
	NodeID       string                 `json:"node_id"`
	RegionID     string                 `json:"region_id"`
	Module       *storedProductModule06 `json:"module"`
	Input        api.CartItemInput      `json:"input"`
	SSHKeys      []string               `json:"ssh_keys"`
	UnitIndex    int64                  `json:"unit_index"`
	UnitCount    int64                  `json:"unit_count"`
}

func readOrder06(ctx context.Context, q currencyQuerier, owner, id string) (orderRecord06, error) {
	var record orderRecord06
	v := &record.Order
	query := `SELECT o.id,o.number,o.user_id,u.email,o.status,o.total_minor,o.currency,o.currency_snapshot,o.coupon_code,o.created_at,o.paid_at,o.cancelled_at,(SELECT i.id FROM invoices i WHERE i.order_id=o.id) FROM orders o JOIN users u ON u.id=o.user_id WHERE o.id=?`
	args := []any{id}
	if owner != "" {
		query += " AND o.user_id=?"
		args = append(args, owner)
	}
	var raw []byte
	var at time.Time
	var paid, cancelled sql.NullTime
	err := q.QueryRowContext(ctx, query, args...).Scan(&v.ID, &v.Number, &record.ClientID, &record.ClientEmail, &v.Status, &v.Total.AmountMinor, &v.Total.Currency, &raw, &v.CouponCode, &at, &paid, &cancelled, &v.InvoiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return record, ErrNotFound
	}
	if err != nil {
		return record, err
	}
	format, err := decodeCurrencySnapshot(raw, v.Total.Currency)
	if err != nil {
		return record, err
	}
	v.Total.DecimalPlaces = int64(format.DecimalPlaces)
	v.CreatedAt = stamp06(at)
	if paid.Valid {
		p := stamp06(paid.Time)
		v.PaidAt = &p
	}
	if cancelled.Valid {
		p := stamp06(cancelled.Time)
		v.CancelledAt = &p
	}
	v.Items = []api.OrderItem{}
	rows, err := q.QueryContext(ctx, "SELECT id,product_id,product_name,cycle,quantity,hostname,options_summary,total_minor FROM order_items WHERE order_id=? ORDER BY position,id", id)
	if err != nil {
		return record, err
	}
	itemIDs := []string{}
	for rows.Next() {
		var item api.OrderItem
		var itemID, options string
		if err = rows.Scan(&itemID, &item.ProductID, &item.ProductName, &item.Cycle, &item.Quantity, &item.Hostname, &options, &item.Amount.AmountMinor); err != nil {
			rows.Close()
			return record, err
		}
		item.OptionsSummary = []string{}
		if options != "" && json.Unmarshal([]byte(options), &item.OptionsSummary) != nil {
			item.OptionsSummary = []string{options}
		}
		item.ServiceIDs = []string{}
		item.Amount.Currency, item.Amount.DecimalPlaces = v.Total.Currency, v.Total.DecimalPlaces
		v.Items = append(v.Items, item)
		itemIDs = append(itemIDs, itemID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return record, err
	}
	for i, itemID := range itemIDs {
		rows, err = q.QueryContext(ctx, "SELECT service_id FROM service_units WHERE order_item_id=? ORDER BY unit_index", itemID)
		if err != nil {
			return record, err
		}
		for rows.Next() {
			var sid string
			if err = rows.Scan(&sid); err != nil {
				rows.Close()
				return record, err
			}
			v.Items[i].ServiceIDs = append(v.Items[i].ServiceIDs, sid)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return record, err
		}
	}
	if len(v.Items) == 0 {
		var item api.OrderItem
		var hostname string
		if err = q.QueryRowContext(ctx, "SELECT COALESCE(plan_id,''),plan_name,hostname FROM orders WHERE id=?", id).Scan(&item.ProductID, &item.ProductName, &hostname); err != nil {
			return record, err
		}
		if hostname != "" {
			item.Hostname = &hostname
		}
		item.Cycle, item.Quantity, item.Amount = "monthly", 1, v.Total
		item.OptionsSummary = []string{}
		item.ServiceIDs = []string{}
		v.Items = append(v.Items, item)
	}
	return record, nil
}
func (m *MySQL) Order06(ctx context.Context, owner, id string) (orderRecord06, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return orderRecord06{}, err
	}
	defer tx.Rollback()
	v, err := readOrder06(ctx, tx, owner, id)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (m *MySQL) OrderPage06(ctx context.Context, owner, cursor, status, search string, limit int) ([]orderRecord06, *string, error) {
	out := []orderRecord06{}
	if limit < 1 || limit > 100 || len(search) > 120 {
		return out, nil, ErrCommerceInput
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback()
	query := "SELECT id FROM orders WHERE 1=1"
	args := []any{}
	if owner != "" {
		query += " AND user_id=?"
		args = append(args, owner)
	}
	if status != "" {
		switch status {
		case "pending_payment", "paid", "active", "cancelled", "fraud":
		default:
			return out, nil, ErrCommerceInput
		}
		query += " AND status=?"
		args = append(args, status)
	}
	if search != "" {
		query += " AND (number LIKE ? OR hostname LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}
	if cursor != "" {
		if !validCancellationID(cursor) {
			return out, nil, ErrCommerceInput
		}
		var at time.Time
		lookup := "SELECT created_at FROM orders WHERE id=?"
		values := []any{cursor}
		if owner != "" {
			lookup += " AND user_id=?"
			values = append(values, owner)
		}
		if err = tx.QueryRowContext(ctx, lookup, values...).Scan(&at); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrCommerceInput
			}
			return out, nil, err
		}
		query += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, at, at, cursor)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
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
		v := ids[len(ids)-1]
		next = &v
	}
	out, err = readOrderPageRecords06(ctx, tx, ids, owner)
	if err != nil {
		return out, nil, err
	}
	return out, next, tx.Commit()
}

func (m *MySQL) CreateOrder06(ctx context.Context, actor User, input api.CreateOrderInput, key string) (api.CreateOrderResponse, error) {
	for _, item := range input.Items {
		if len(item.ModuleFields) > 0 {
			product, err := m.Product06(ctx, false, item.ProductID)
			if err != nil {
				return api.CreateOrderResponse{}, err
			}
			if product.OrderSchema == nil {
				return api.CreateOrderResponse{}, ErrCommerceInput
			}
			var schema serverv1.Schema
			if json.Unmarshal(jsonValue06(product.OrderSchema), &schema) != nil || schema.Validate() != nil {
				return api.CreateOrderResponse{}, ErrCommerceInput
			}
			_, secret := schema.Split(item.ModuleFields)
			if len(secret) > 0 {
				return api.CreateOrderResponse{}, problem(400, "PASSWORD_REQUIRES_DIRECT_ACTION", "Credentials must be submitted through a direct service action after payment")
			}
		}

	}
	raw, _ := json.Marshal(input)
	if !keyPattern.MatchString(key) || api.ValidateInput("createOrder", raw) != nil {
		return api.CreateOrderResponse{}, ErrCommerceInput
	}
	fp := digest(string(raw))
	passwordHashes, err := checkoutPasswordHashes06(input.Items)
	if err != nil {
		return api.CreateOrderResponse{}, err
	}
	return retryPayment(ctx, func() (api.CreateOrderResponse, error) {
		actor := actor // Reset authorization input for every transaction attempt.
		var out api.CreateOrderResponse
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		config, _, err := readCurrencies(ctx, tx, " FOR UPDATE")
		if err != nil {
			return out, err
		}
		if actor, err = lockAccountUser(ctx, tx, actor.ID); err != nil {
			return out, err
		}
		if replay, err := requestReplay06(ctx, tx, actor.ID, "orders:create", key, fp, &out); err != nil || replay {
			return out, err
		}
		var prior string
		if err = tx.QueryRowContext(ctx, "SELECT fingerprint FROM order_requests WHERE user_id=? AND request_key=? FOR UPDATE", actor.ID, key).Scan(&prior); err == nil {
			return out, ErrConflict
		} else if !errors.Is(err, sql.ErrNoRows) {
			return out, err
		}
		profile, err := readProfile(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		if profile.Currency != input.Currency {
			return out, ErrCurrency
		}
		site, err := new(App).readSiteSettings6(ctx, tx)
		if err != nil {
			return out, err
		}
		if site.EmailVerificationRequired && !profile.EmailVerified {
			return out, problem(403, "EMAIL_VERIFICATION_REQUIRED", "Verify your email address before placing an order")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		quote, err := quote06SQL(ctx, tx, actor, api.CreateQuoteInput{Items: input.Items, Coupon: input.Coupon, Currency: input.Currency}, now, " FOR UPDATE")
		if errors.Is(err, commerce.ErrProductUnavailable) {
			err = commerce.ErrQuoteChanged
		}
		if err != nil {
			return out, err
		}
		if err = commerce.MatchQuote(quote, input.QuoteDigest, now); err != nil {
			return out, err
		}
		if input.Coupon != "" && (quote.Quote.Coupon == nil || !quote.Quote.Coupon.Valid) {
			return out, problem(409, "COUPON_INVALID", "The coupon is no longer eligible")
		}
		format, ok := config.find(input.Currency)
		if !ok || !format.Enabled {
			return out, ErrCurrency
		}
		currencyRaw, _ := json.Marshal(format.CurrencyFormat)
		oid := ID()
		number := "ORD-" + strings.ToUpper(ID()[:16])
		name := text06(quote.Items[0].Product.Name)
		if len(quote.Items) > 1 {
			name = fmt.Sprintf("%s + %d", name, len(quote.Items)-1)
		}
		name = string([]rune(name)[:min(120, len([]rune(name)))])
		var couponCode *string
		if input.Coupon != "" {
			couponCode = &input.Coupon
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,currency_snapshot,coupon_code) VALUES(?,?,?,NULL,?,?,'pending_payment',?,?,?,?,?)", oid, number, actor.ID, name, input.Items[0].Hostname, quote.Quote.Total.AmountMinor, input.Currency, now, currencyRaw, couponCode)
		if err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO order_requests(user_id,request_key,fingerprint,order_id,created_at) VALUES(?,?,?,?,?)", actor.ID, key, fp, oid, now); err != nil {
			return out, err
		}
		for position, item := range quote.Items {
			if passwordHashes[position] != "" && (item.Product.Kind != "cloud_server" || item.OSImage == nil) {
				return out, problem(400, "GUEST_IMAGE_REQUIRED", "Select an operating system before setting its root password")
			}
			sshKeys := []string{}
			seen := map[string]bool{}
			for _, id := range item.Input.SSHKeyIDs {
				if seen[id] {
					return out, ErrCommerceInput
				}
				seen[id] = true
				var key string
				if err = tx.QueryRowContext(ctx, "SELECT public_key FROM ssh_keys WHERE user_id=? AND id=?", actor.ID, id).Scan(&key); err != nil {
					if errors.Is(err, sql.ErrNoRows) {
						err = ErrNotFound
					}
					return out, err
				}
				sshKeys = append(sshKeys, key)
			}
			reserved := int64(0)
			if item.Product.Stock != nil {
				result, err := tx.ExecContext(ctx, "UPDATE products SET stock=stock-? WHERE id=? AND stock>=?", item.Input.Quantity, item.Product.ID, item.Input.Quantity)
				if err != nil {
					return out, err
				}
				changed, err := result.RowsAffected()
				if err != nil {
					return out, err
				}
				if changed != 1 {
					return out, commerce.ErrQuoteChanged
				}
				reserved = item.Input.Quantity
			}
			snapshot, _ := json.Marshal(orderItemSnapshot06{Item: item, SSHKeys: sshKeys, PasswordHash: passwordHashes[position]})
			options, _ := json.Marshal(item.OptionsSummary)
			var hostname *string
			if item.Input.Hostname != "" {
				hostname = &item.Input.Hostname
			}
			_, err = tx.ExecContext(ctx, "INSERT INTO order_items(id,order_id,position,product_id,product_name,cycle,quantity,hostname,options_summary,recurring_minor,total_minor,stock_reserved,snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", ID(), oid, position, item.Product.ID, text06(item.Product.Name), item.Input.Cycle, item.Input.Quantity, hostname, options, item.RecurringMinor, item.TotalMinor, reserved, snapshot)
			if err != nil {
				return out, err
			}
		}
		if input.Coupon != "" {
			if _, err = tx.ExecContext(ctx, "INSERT INTO coupon_redemptions(id,coupon_id,user_id,order_id,created_at) SELECT ?,id,?,?,? FROM coupons WHERE code=?", ID(), actor.ID, oid, now, input.Coupon); err != nil {
				return out, err
			}
		}
		lines := []api.InvoiceLine{}
		for _, line := range quote.Quote.Lines {
			lines = append(lines, api.InvoiceLine{Description: line.Description, Amount: line.Amount})
		}
		invoice, err := newInvoice06(ctx, tx, actor.ID, "order", &oid, lines, now.Add(72*time.Hour), now)
		if err != nil {
			return out, err
		}
		// Preserve quote item attribution for every product, option and setup
		// line. Invoice line positions are not order item positions.
		for position, line := range quote.Quote.Lines {
			if line.ItemIndex == nil {
				continue
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO affiliate_invoice_lines(invoice_line_id,product_id,order_item_id) SELECT l.id,oi.product_id,oi.id FROM invoice_lines l JOIN order_items oi ON oi.order_id=? AND oi.position=? WHERE l.invoice_id=? AND l.position=?`, oid, *line.ItemIndex, invoice.ID, position); err != nil {
				return out, err
			}
		}
		invoice.Subtotal = quote.Quote.Subtotal
		if _, err = tx.ExecContext(ctx, "UPDATE invoices SET subtotal_minor=? WHERE id=?", invoice.Subtotal.AmountMinor, invoice.ID); err != nil {
			return out, err
		}
		var issuer []byte
		if err = tx.QueryRowContext(ctx, "SELECT issuer_snapshot FROM invoices WHERE id=?", invoice.ID).Scan(&issuer); err != nil {
			return out, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE orders SET issuer_snapshot=? WHERE id=?", issuer, oid); err != nil {
			return out, err
		}
		record := invoiceRecord06{Invoice: invoice, Version: 1}
		if invoice.Total.AmountMinor == 0 {
			invoice, err = completeInvoice06(ctx, tx, record, now)
		} else if input.ApplyCredit {
			invoice, err = applyInvoiceCredit06(ctx, tx, record, false)
		}
		if err != nil {
			return out, err
		}
		order, err := readOrder06(ctx, tx, actor.ID, oid)
		if err != nil {
			return out, err
		}
		out.Order, out.Invoice = order.Order, customerInvoice06(invoice)
		if err = insertAccountAudit(ctx, tx, actor.ID, actor.ID, "order.created", oid); err != nil {
			return out, err
		}
		payload, _ := json.Marshal(map[string]string{"order_id": oid, "invoice_id": invoice.ID})
		if _, err = tx.ExecContext(ctx, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'order.created',?,?)", ID(), oid, payload, now); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "orders:create", key, fp, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

// Split an aggregate recurring discount without losing a minor unit. Quantities
// are ordered permanently by unit_index, so subsequent renewals stay stable.
func unitAmount06(total, quantity, index int64) int64 {
	amount := total / quantity
	if index < total%quantity {
		amount++
	}
	return amount
}
func fulfillPaidOrder06(ctx context.Context, tx *sql.Tx, invoice api.AdminInvoice, paid time.Time) error {
	if invoice.OrderID == nil {
		return ErrPaymentState
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,snapshot FROM order_items WHERE order_id=? ORDER BY position,id FOR UPDATE", *invoice.OrderID)
	if err != nil {
		return err
	}
	type itemRow struct {
		id       string
		snapshot orderItemSnapshot06
	}
	items := []itemRow{}
	for rows.Next() {
		var item itemRow
		var raw []byte
		if err = rows.Scan(&item.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &item.snapshot); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var currencyRaw []byte
	if err = tx.QueryRowContext(ctx, "SELECT currency_snapshot FROM invoices WHERE id=?", invoice.ID).Scan(&currencyRaw); err != nil {
		return err
	}
	for _, row := range items {
		if row.snapshot.Legacy {
			continue
		}
		item := row.snapshot.Item
		if item.Input.Quantity < 1 || item.Input.Quantity > 20 || item.Input.RootPassword != "" {
			return ErrPaymentState
		}
		var region string
		if err = tx.QueryRowContext(ctx, "SELECT name FROM catalog_regions WHERE id=?", item.Product.RegionID).Scan(&region); err != nil {
			return err
		}
		for unit := int64(0); unit < item.Input.Quantity; unit++ {
			var existing string
			err = tx.QueryRowContext(ctx, "SELECT service_id FROM service_units WHERE order_item_id=? AND unit_index=?", row.id, unit).Scan(&existing)
			if err == nil {
				continue
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			sid := ID()
			due := paid
			auto := item.Input.Cycle != "one_time"
			var nextInvoice *time.Time
			if auto {
				months, e := commerce.CycleMonths(item.Input.Cycle)
				if e != nil {
					return e
				}
				due = commerce.CalendarMonths(paid, months, paid.Day())
				nextInvoice = &due
			}
			hostname := item.Input.Hostname
			if hostname == "" {
				hostname = "service-" + sid[:12]
			}
			if unit > 0 {
				suffix := fmt.Sprintf("-%d", unit+1)
				hostname = strings.TrimRight(hostname[:min(len(hostname), 63-len(suffix))], ".-") + suffix
			}
			label := hostname
			if len([]rune(label)) > 80 {
				label = string([]rune(label)[:80])
			}
			config := serviceConfiguration06{Kind: item.Product.Kind, ProductID: item.Product.ID, NodeID: item.Product.NodeID, RegionID: item.Product.RegionID, Module: item.Product.Module, Input: item.Input, SSHKeys: row.snapshot.SSHKeys, PasswordHash: row.snapshot.PasswordHash, UnitIndex: unit, UnitCount: item.Input.Quantity}
			config.Input.Quantity = 1
			config.Input.Hostname = hostname
			configRaw, _ := json.Marshal(config)
			specsRaw, _ := json.Marshal(item.Product.Specs)
			var osRaw any
			if item.OSImage != nil {
				osRaw, _ = json.Marshal(item.OSImage)
			}
			recurring := unitAmount06(item.RecurringMinor, item.Input.Quantity, unit)
			_, err = tx.ExecContext(ctx, `INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot,product_id,cycle,recurring_minor,next_invoice_at,auto_renew,label,module_id,connection_id,specs_snapshot,os_snapshot,configuration_snapshot,created_at,anchor_day) VALUES(?,?,?,?,?,'provisioning',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, sid, invoice.ClientID, hostname, text06(item.Product.Name), region, due, recurring, invoice.Total.Currency, item.Product.Specs.CPUCores, (item.Product.Specs.MemoryMB+1023)/1024, item.Product.Specs.DiskGB, currencyRaw, item.Product.ID, item.Input.Cycle, recurring, nextInvoice, auto, label, moduleID06(item.Product.Module), connectionID06(item.Product.Module), specsRaw, osRaw, configRaw, paid, paid.Day())
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO service_units(order_item_id,unit_index,service_id) VALUES(?,?,?)", row.id, unit, sid); err != nil {
				return err
			}
			if _, err = queueServiceTask06(ctx, tx, sid, "provision", map[string]any{}, paid); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE order_items SET stock_reserved=0 WHERE order_id=?", *invoice.OrderID)
	return err
}
func queueServiceTask06(ctx context.Context, tx *sql.Tx, sid, action string, payload any, now time.Time) (api.ServiceTask, error) {
	id := ID()
	raw, err := json.Marshal(payload)
	if err != nil {
		return api.ServiceTask{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO service_tasks(id,service_id,action,status,payload,created_at,updated_at) VALUES(?,?,?,'queued',?,?,?)", id, sid, action, raw, now, now)
	return api.ServiceTask{ID: id, Action: serviceTaskAction06(action), Status: "queued", CreatedAt: stamp06(now)}, err
}

func (m *MySQL) CancelOrder06(ctx context.Context, actor User, id, reason string) (api.Order, error) {
	if len([]rune(reason)) > 500 || !validCancellationID(id) {
		return api.Order{}, ErrCommerceInput
	}
	return retryPayment(ctx, func() (api.Order, error) {
		actor := actor // Reset authorization input for every transaction attempt.
		var out api.Order
		owner := actor.ID
		if actor.Role == "admin" {
			owner = ""
		}
		var version int
		query := "SELECT i.commerce_version FROM invoices i JOIN orders o ON o.id=i.order_id WHERE o.id=?"
		args := []any{id}
		if owner != "" {
			query += " AND o.user_id=?"
			args = append(args, owner)
		}
		if err := m.DB.QueryRowContext(ctx, query, args...).Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrNotFound
			}
			return out, err
		}
		if version == 0 {
			// Preserve the legacy fulfillment/outbox barrier and void state even
			// when a current client cancels a deprecated checkout.
			if _, err := m.cancelOrderOnce(ctx, actor, id, reason); err != nil {
				return out, err
			}
			order, err := m.Order06(ctx, owner, id)
			return order.Order, err
		}
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if actor.Role == "admin" {
			if actor, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
				return out, err
			}
			owner = ""
		}
		var iid string
		query = "SELECT i.id FROM invoices i JOIN orders o ON o.id=i.order_id WHERE o.id=?"
		args = []any{id}
		if owner != "" {
			query += " AND o.user_id=?"
			args = append(args, owner)
		}
		if err = tx.QueryRowContext(ctx, query, args...).Scan(&iid); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				err = ErrNotFound
			}
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, owner, iid, false)
		if err != nil {
			return out, err
		}
		v := record.Invoice
		if v.Status == "cancelled" {
			order, e := readOrder06(ctx, tx, owner, id)
			return order.Order, e
		}
		if v.Status != "unpaid" || v.AmountPaid.AmountMinor != 0 {
			return out, ErrPaymentState
		}
		if err = invoiceAttemptBarrier06(ctx, tx, iid, false); err != nil {
			return out, err
		}
		var services int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM service_units u JOIN order_items i ON i.id=u.order_item_id WHERE i.order_id=?", id).Scan(&services); err != nil {
			return out, err
		}
		if services != 0 {
			return out, ErrPaymentState
		}
		if err = releaseOrderStock06(ctx, tx, id); err != nil {
			return out, err
		}
		if v.CreditApplied.AmountMinor > 0 {
			if _, err = appendCredit06(ctx, tx, v.ClientID, "refund", v.CreditApplied.AmountMinor, &iid, "Credit released from cancelled order", time.Now().UTC()); err != nil {
				return out, err
			}
		}
		for _, statement := range []struct {
			q    string
			args []any
		}{
			{"DELETE FROM coupon_redemptions WHERE order_id=?", []any{id}},
			{"UPDATE invoice_payments SET status='refunded',refunded_minor=amount_minor WHERE invoice_id=? AND method='credit'", []any{iid}},
			{"UPDATE invoices SET status='cancelled',credit_applied_minor=0 WHERE id=?", []any{iid}},
			{"UPDATE orders SET status='cancelled',cancelled_at=UTC_TIMESTAMP(6) WHERE id=?", []any{id}},
		} {
			if _, err = tx.ExecContext(ctx, statement.q, statement.args...); err != nil {
				return out, err
			}
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "order.cancelled", id, 0, reason)); err != nil {
			return out, err
		}
		payload, _ := json.Marshal(map[string]string{"order_id": id, "invoice_id": iid})
		if _, err = tx.ExecContext(ctx, "INSERT IGNORE INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,'order.cancelled',?,UTC_TIMESTAMP(6))", ID(), id, payload); err != nil {
			return out, err
		}
		order, err := readOrder06(ctx, tx, owner, id)
		if err != nil {
			return out, err
		}
		return order.Order, tx.Commit()
	})
}
