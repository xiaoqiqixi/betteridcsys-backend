package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Deliberately separate from PaymentAttempt: checkout URLs, merchant identities
// and replay keys must never become a read-only order-detail response.
type OrderPaymentAttempt struct {
	ID                    string    `json:"id"`
	ProviderID            string    `json:"provider_id"`
	AmountMinor           int64     `json:"amount_minor"`
	Currency              string    `json:"currency"`
	CurrencyDecimalPlaces int       `json:"currency_decimal_places"`
	State                 string    `json:"state"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type OrderDetail struct {
	Order                  Order                 `json:"order"`
	Customer               *User                 `json:"customer,omitempty"`
	Invoice                *Invoice              `json:"invoice"`
	Service                *Service              `json:"service"`
	PaymentAttempts        []OrderPaymentAttempt `json:"payment_attempts"`
	Audit                  []Audit               `json:"audit"`
	AuditHasMore           bool                  `json:"audit_has_more"`
	PaymentAttemptsHasMore bool                  `json:"payment_attempts_has_more"`
}

type orderDetailStore interface {
	OrderDetail(context.Context, string, string) (OrderDetail, error)
}

func orderDetailPath(path string, admin bool) (string, bool) {
	prefix := "/orders/"
	if admin {
		prefix = "/admin/orders/"
	}
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(path, prefix)
	return id, !strings.Contains(id, "/")
}

func (a *App) orderDetailHTTP(w http.ResponseWriter, r *http.Request, uid, id string) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	if !validCancellationID(id) {
		fail(w, r, 404, "NOT_FOUND", "未找到此订单")
		return
	}
	store, ok := a.Store.(orderDetailStore)
	if !ok {
		a.internal(w, r, errors.New("order detail store unavailable"))
		return
	}
	out, err := store.OrderDetail(r.Context(), uid, id)
	if errors.Is(err, ErrNotFound) {
		fail(w, r, 404, "NOT_FOUND", "未找到此订单")
		return
	}
	a.result(w, r, out, err)
}

func safeOrderAttempt(a PaymentAttempt) OrderPaymentAttempt {
	return OrderPaymentAttempt{ID: a.ID, ProviderID: a.Expectation.ProviderID, AmountMinor: a.Expectation.AmountMinor, Currency: a.Expectation.Currency, CurrencyDecimalPlaces: a.Expectation.CurrencyDecimalPlaces, State: a.State, CreatedAt: a.CreatedAt, UpdatedAt: a.UpdatedAt}
}

func (m *Memory) OrderDetail(ctx context.Context, uid, id string) (OrderDetail, error) {
	if err := ctx.Err(); err != nil {
		return OrderDetail{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := OrderDetail{PaymentAttempts: []OrderPaymentAttempt{}, Audit: []Audit{}}
	found := false
	for _, o := range m.orders {
		if o.ID == id && (uid == "" || o.UserID == uid) {
			out.Order = cloneOrder(o)
			found = true
			break
		}
	}
	if !found {
		return out, ErrNotFound
	}
	owner := out.Order.UserID
	if uid == "" {
		if u, ok := m.users[owner]; ok {
			u.PasswordHash = ""
			out.Customer = &u
		}
	}
	targets := map[string]bool{id: true}
	for _, i := range m.invoices {
		if i.OrderID == id && i.UserID == owner {
			v := cloneInvoice(i)
			out.Invoice = &v
			targets[i.ID] = true
			break
		}
	}
	// Existing MySQL records have no order→service foreign key. Never guess the
	// relationship from hostname, price or plan name; an absent link is null.
	if out.Order.ServiceID != "" {
		for _, s := range m.services {
			if s.ID == out.Order.ServiceID && s.UserID == owner {
				v := s
				out.Service = &v
				break
			}
		}
	}
	for _, a := range m.paymentAttempts {
		if a.Expectation.OrderID == id && a.Expectation.AccountID == owner {
			out.PaymentAttempts = append(out.PaymentAttempts, safeOrderAttempt(a))
			targets[a.ID] = true
		}
	}
	sort.Slice(out.PaymentAttempts, func(i, j int) bool {
		a, b := out.PaymentAttempts[i], out.PaymentAttempts[j]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if len(out.PaymentAttempts) > 100 {
		out.PaymentAttemptsHasMore = true
		out.PaymentAttempts = out.PaymentAttempts[:100]
	}
	for _, a := range m.audits {
		if a.UserID == owner && targets[a.Target] {
			out.Audit = append(out.Audit, a)
		}
	}
	sort.Slice(out.Audit, func(i, j int) bool {
		a, b := out.Audit[i], out.Audit[j]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	if len(out.Audit) > 100 {
		out.AuditHasMore = true
		out.Audit = out.Audit[:100]
	}
	return out, ctx.Err()
}

func (m *MySQL) OrderDetail(ctx context.Context, uid, id string) (OrderDetail, error) {
	out := OrderDetail{PaymentAttempts: []OrderPaymentAttempt{}, Audit: []Audit{}}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	query := "SELECT " + orderColumns + " FROM orders WHERE id=?"
	args := []any{id}
	if uid != "" {
		query += " AND user_id=?"
		args = append(args, uid)
	}
	out.Order, err = scanOrder(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	owner := out.Order.UserID
	if uid == "" {
		var u User
		err = tx.QueryRowContext(ctx, "SELECT id,email,name,role FROM users WHERE id=?", owner).Scan(&u.ID, &u.Email, &u.Name, &u.Role)
		if err != nil {
			return out, err
		}
		out.Customer = &u
	}
	var invoice Invoice
	var issuerRaw, currencyRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot FROM invoices WHERE order_id=? AND user_id=?", id, owner).Scan(&invoice.ID, &invoice.Number, &invoice.UserID, &invoice.OrderID, &invoice.Status, &invoice.AmountMinor, &invoice.Currency, &invoice.IssuedAt, &invoice.DueAt, &invoice.PaidAt, &issuerRaw, &currencyRaw)
	if err == nil {
		if len(issuerRaw) > 0 {
			if err = json.Unmarshal(issuerRaw, &invoice.Issuer); err != nil {
				return out, err
			}
		}
		invoice.CurrencyFormat, err = decodeCurrencySnapshot(currencyRaw, invoice.Currency)
		if err != nil {
			return out, err
		}
		out.Invoice = &invoice
	} else if !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,provider_id,amount_minor,currency,currency_decimal_places,state,created_at,updated_at FROM payment_attempts WHERE order_id=? AND user_id=? ORDER BY created_at DESC,id DESC LIMIT 101", id, owner)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a OrderPaymentAttempt
		if err = rows.Scan(&a.ID, &a.ProviderID, &a.AmountMinor, &a.Currency, &a.CurrencyDecimalPlaces, &a.State, &a.CreatedAt, &a.UpdatedAt); err != nil {
			rows.Close()
			return out, err
		}
		out.PaymentAttempts = append(out.PaymentAttempts, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.PaymentAttempts) > 100 {
		out.PaymentAttemptsHasMore = true
		out.PaymentAttempts = out.PaymentAttempts[:100]
	}
	invoiceID := ""
	if out.Invoice != nil {
		invoiceID = out.Invoice.ID
	}
	rows, err = tx.QueryContext(ctx, "SELECT e.id,e.user_id,e.action,e.target,e.created_at,e.actor_email,e.detail FROM audit_events e WHERE e.user_id=? AND (e.target=? OR (?<>'' AND e.target=?) OR EXISTS (SELECT 1 FROM payment_attempts p WHERE p.id=e.target AND p.order_id=? AND p.user_id=?)) ORDER BY e.created_at DESC,e.id DESC LIMIT 101", owner, id, invoiceID, invoiceID, id, owner)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a Audit
		if err = rows.Scan(&a.ID, &a.UserID, &a.Action, &a.Target, &a.CreatedAt, &a.ActorEmail, &a.Detail); err != nil {
			rows.Close()
			return out, err
		}
		out.Audit = append(out.Audit, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(out.Audit) > 100 {
		out.AuditHasMore = true
		out.Audit = out.Audit[:100]
	}
	return out, tx.Commit()
}
