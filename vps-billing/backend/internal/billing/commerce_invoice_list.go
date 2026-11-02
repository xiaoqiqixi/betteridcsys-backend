package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"strings"
	"time"
)

// List contracts contain neither lines, receipts nor billing addresses. Read
// just their financial summary within the page selector's repeatable snapshot.
// The page selector keeps the sort narrow and bounds this join to at most 100
// primary keys, including when a global chronological index is not available.
func readInvoiceSummaries06(ctx context.Context, q currencyQuerier, ids []string, owner string) ([]api.AdminInvoiceSummary, error) {
	out := make([]api.AdminInvoiceSummary, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args, marks := make([]any, len(ids)), make([]string, len(ids))
	for i, id := range ids {
		args[i], marks[i] = id, "?"
	}
	query := `SELECT i.id,i.number,i.user_id,u.email,i.kind,i.status,i.commerce_version,i.amount_minor,i.credit_applied_minor,i.amount_paid_minor,i.currency,i.currency_snapshot,i.issued_at,i.due_at,i.paid_at
 FROM invoices i JOIN users u ON u.id=i.user_id WHERE i.id IN (` + strings.Join(marks, ",") + ")"
	if owner != "" {
		query += " AND i.user_id=?"
		args = append(args, owner)
	}
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	byID := make(map[string]api.AdminInvoiceSummary, len(ids))
	var memo currencySnapshotMemo
	for rows.Next() {
		var v api.AdminInvoiceSummary
		var version int
		var credit, paid int64
		var raw []byte
		var issued, due time.Time
		var paidAt sql.NullTime
		if err := rows.Scan(&v.ID, &v.Number, &v.ClientID, &v.ClientEmail, &v.Kind, &v.Status, &version, &v.Total.AmountMinor, &credit, &paid, &v.Total.Currency, &raw, &issued, &due, &paidAt); err != nil {
			return out, err
		}
		format, err := memo.decode(raw, v.Total.Currency)
		if err != nil {
			return out, err
		}
		v.Total.DecimalPlaces = int64(format.DecimalPlaces)
		if version == 0 && v.Status == "paid" && paid == 0 {
			paid = v.Total.AmountMinor - credit
		}
		v.BalanceDue = v.Total
		v.BalanceDue.AmountMinor, err = commerce.BalanceDue(v.Total.AmountMinor, paid, credit)
		if err != nil {
			return out, err
		}
		if v.Status == "void" {
			v.Status = "cancelled"
		}
		v.IssuedAt, v.DueAt = stamp06(issued), stamp06(due)
		if paidAt.Valid {
			at := stamp06(paidAt.Time)
			v.PaidAt = &at
		}
		byID[v.ID] = v
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, id := range ids {
		v, ok := byID[id]
		if !ok {
			return out, ErrNotFound
		}
		out = append(out, v)
	}
	return out, nil
}

func customerInvoiceSummary06(v api.AdminInvoiceSummary) api.InvoiceSummary {
	return api.InvoiceSummary{ID: v.ID, Number: v.Number, Kind: v.Kind, Status: v.Status, Total: v.Total, BalanceDue: v.BalanceDue, IssuedAt: v.IssuedAt, DueAt: v.DueAt, PaidAt: v.PaidAt}
}
