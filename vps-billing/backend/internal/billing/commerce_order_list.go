package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Hydrate one bounded page with three queries, preserving the caller's snapshot
// and cursor order. Detail and mutation reads keep their existing lock behavior.
func readOrderPageRecords06(ctx context.Context, q currencyQuerier, ids []string, owner string) ([]orderRecord06, error) {
	out := make([]orderRecord06, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	marks, args := make([]string, len(ids)), make([]any, len(ids))
	for i, id := range ids {
		marks[i], args[i] = "?", id
	}
	where := " WHERE o.id IN (" + strings.Join(marks, ",") + ")"
	rootWhere, rootArgs := where, args
	if owner != "" {
		rootWhere += " AND o.user_id=?"
		rootArgs = append(append([]any{}, args...), owner)
	}
	read := func(query string, values []any, scan func(*sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query, values...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			if err := scan(rows); err != nil {
				return err
			}
		}
		return rows.Err()
	}
	records := make(map[string]*orderRecord06, len(ids))
	fallback := make(map[string]api.OrderItem, len(ids))
	var memo currencySnapshotMemo
	if err := read(`SELECT o.id,o.number,o.user_id,u.email,o.status,o.total_minor,o.currency,o.currency_snapshot,o.coupon_code,o.created_at,o.paid_at,o.cancelled_at,(SELECT i.id FROM invoices i WHERE i.order_id=o.id),COALESCE(o.plan_id,''),o.plan_name,o.hostname
 FROM orders o JOIN users u ON u.id=o.user_id`+rootWhere, rootArgs, func(rows *sql.Rows) error {
		record := &orderRecord06{}
		v := &record.Order
		var raw []byte
		var at time.Time
		var paid, cancelled sql.NullTime
		var legacy api.OrderItem
		var hostname string
		if err := rows.Scan(&v.ID, &v.Number, &record.ClientID, &record.ClientEmail, &v.Status, &v.Total.AmountMinor, &v.Total.Currency, &raw, &v.CouponCode, &at, &paid, &cancelled, &v.InvoiceID, &legacy.ProductID, &legacy.ProductName, &hostname); err != nil {
			return err
		}
		format, err := memo.decode(raw, v.Total.Currency)
		if err != nil {
			return err
		}
		v.Total.DecimalPlaces = int64(format.DecimalPlaces)
		v.CreatedAt = stamp06(at)
		if paid.Valid {
			at := stamp06(paid.Time)
			v.PaidAt = &at
		}
		if cancelled.Valid {
			at := stamp06(cancelled.Time)
			v.CancelledAt = &at
		}
		v.Items = []api.OrderItem{}
		if hostname != "" {
			legacy.Hostname = &hostname
		}
		legacy.Cycle, legacy.Quantity, legacy.Amount = "monthly", 1, v.Total
		legacy.OptionsSummary, legacy.ServiceIDs = []string{}, []string{}
		records[v.ID], fallback[v.ID] = record, legacy
		return nil
	}); err != nil {
		return out, err
	}
	if len(records) != len(ids) {
		return out, ErrNotFound
	}
	type itemLocation struct {
		order string
		index int
	}
	items := map[string]itemLocation{}
	// Parent joins preserve MySQL's case-insensitive foreign-key matching while
	// returning canonical IDs for the in-memory association maps.
	if err := read(`SELECT o.id,i.id,i.product_id,i.product_name,i.cycle,i.quantity,i.hostname,i.options_summary,i.total_minor
 FROM orders o JOIN order_items i ON i.order_id=o.id`+where+" ORDER BY o.id,i.position,i.id", args, func(rows *sql.Rows) error {
		var oid, iid, options string
		var item api.OrderItem
		if err := rows.Scan(&oid, &iid, &item.ProductID, &item.ProductName, &item.Cycle, &item.Quantity, &item.Hostname, &options, &item.Amount.AmountMinor); err != nil {
			return err
		}
		v := &records[oid].Order
		item.OptionsSummary, item.ServiceIDs = []string{}, []string{}
		if options != "" && json.Unmarshal([]byte(options), &item.OptionsSummary) != nil {
			item.OptionsSummary = []string{options}
		}
		item.Amount.Currency, item.Amount.DecimalPlaces = v.Total.Currency, v.Total.DecimalPlaces
		items[iid] = itemLocation{oid, len(v.Items)}
		v.Items = append(v.Items, item)
		return nil
	}); err != nil {
		return out, err
	}
	if len(items) > 0 {
		if err := read(`SELECT i.id,u.service_id FROM orders o JOIN order_items i ON i.order_id=o.id
 JOIN service_units u ON u.order_item_id=i.id`+where+" ORDER BY i.id,u.unit_index", args, func(rows *sql.Rows) error {
			var iid, sid string
			if err := rows.Scan(&iid, &sid); err != nil {
				return err
			}
			location := items[iid]
			item := &records[location.order].Order.Items[location.index]
			item.ServiceIDs = append(item.ServiceIDs, sid)
			return nil
		}); err != nil {
			return out, err
		}
	}
	for _, id := range ids {
		record := records[id]
		if len(record.Order.Items) == 0 {
			record.Order.Items = append(record.Order.Items, fallback[id])
		}
		out = append(out, *record)
	}
	return out, nil
}
