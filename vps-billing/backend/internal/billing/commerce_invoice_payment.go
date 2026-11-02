package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"aster/billing/internal/integrations"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

func invoicePayFingerprint06(id, method, gateway string) string {
	raw, _ := json.Marshal([]string{id, method, gateway})
	return digest(string(raw))
}
func reserveInvoicePayKey06(ctx context.Context, tx *sql.Tx, uid, id, key, gateway string) error {
	fp := invoicePayFingerprint06(id, "gateway", gateway)
	var receipt map[string]any
	replay, err := requestReplay06(ctx, tx, uid, "invoice:pay", key, fp, &receipt)
	if errors.Is(err, ErrConflict) {
		return ErrPaymentConflict
	}
	if err != nil || replay {
		return err
	}
	return recordRequest06(ctx, tx, uid, "invoice:pay", key, fp, map[string]any{})
}

func invoiceOrderReference06(v api.AdminInvoice) string {
	if v.OrderID != nil {
		return *v.OrderID
	}
	return v.ID
}
func expectation06(v api.AdminInvoice, provider, merchant string) integrations.SettlementExpectation {
	return integrations.SettlementExpectation{ProviderID: provider, MerchantAccountID: merchant, AccountID: v.ClientID, InvoiceID: v.ID, OrderID: invoiceOrderReference06(v), AmountMinor: v.BalanceDue.AmountMinor, Currency: v.Total.Currency, CurrencyDecimalPlaces: int(v.Total.DecimalPlaces)}
}
func attemptInvoice06(a PaymentAttempt, v api.AdminInvoice) error {
	want := expectation06(v, a.Expectation.ProviderID, a.Expectation.MerchantAccountID)
	want.AmountMinor = a.Expectation.AmountMinor
	if want != a.Expectation || want.AmountMinor <= 0 || want.AmountMinor > v.Total.AmountMinor {
		return ErrPaymentConflict
	}
	return nil
}
func (m *MySQL) invoiceUsesCommerce06(ctx context.Context, id string) (bool, error) {
	var version int
	err := m.DB.QueryRowContext(ctx, "SELECT commerce_version FROM invoices WHERE id=?", id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return version != 0, err
}
func (m *MySQL) attemptUsesCommerce06(ctx context.Context, id string) (bool, error) {
	var version int
	err := m.DB.QueryRowContext(ctx, "SELECT i.commerce_version FROM invoices i JOIN payment_attempts a ON a.invoice_id=i.id WHERE a.id=?", id).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return version != 0, err
}
func (m *MySQL) invoicePaymentAttempt06(ctx context.Context, actor User, id string) (PaymentAttempt, error) {
	return retryPayment(ctx, func() (PaymentAttempt, error) {
		var out PaymentAttempt
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		initial, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if err != nil {
			return out, err
		}
		owner := actor.ID
		if actor.Role == "admin" {
			owner = ""
		}
		record, err := lockInvoice06(ctx, tx, owner, initial.Expectation.InvoiceID, false)
		if err != nil {
			return out, err
		}
		if err = expirePaymentsSQL(ctx, tx, record.Invoice.ID); err != nil {
			return out, err
		}
		out, err = readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if err != nil {
			return out, err
		}
		if err = attemptInvoice06(out, record.Invoice); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}
func (m *MySQL) voidInvoiceAttempt06(ctx context.Context, actor User, id, reason string) (PaymentAttempt, error) {
	reason, err := voidPaymentInput(actor, id, reason)
	if err != nil {
		return PaymentAttempt{}, err
	}
	return retryPayment(ctx, func() (PaymentAttempt, error) {
		actor := actor // Reset authorization input for every transaction attempt.
		var out PaymentAttempt
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		initial, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, "", initial.Expectation.InvoiceID, false)
		if err != nil {
			return out, err
		}
		out, err = readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if err != nil {
			return out, err
		}
		if err = attemptInvoice06(out, record.Invoice); err != nil {
			return out, err
		}
		if !paymentPending(out) {
			return out, ErrPaymentState
		}
		out.State, out.UpdatedAt = "voided", time.Now().UTC().Truncate(time.Microsecond)
		if _, err = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='voided',updated_at=? WHERE id=?", out.UpdatedAt, id); err != nil {
			return out, err
		}
		if err = insertCurrencyAudit(ctx, tx, voidPaymentAudit(actor, out, reason)); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}
func reserveInvoicePayment06(ctx context.Context, m *MySQL, actor User, id, key, provider, merchant string) (PaymentAttempt, bool, error) {
	type result struct {
		attempt PaymentAttempt
		replay  bool
	}
	out, err := retryPayment(ctx, func() (result, error) {
		var out result
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		owner := actor.ID
		if actor.Role == "admin" {
			owner = ""
		}
		record, err := lockInvoice06(ctx, tx, owner, id, true)
		if err != nil {
			return out, err
		}
		v := record.Invoice
		if err = reserveInvoicePayKey06(ctx, tx, v.ClientID, id, key, provider); err != nil {
			return out, err
		}
		if err = expirePaymentsSQL(ctx, tx, id); err != nil {
			return out, err
		}
		attempt, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE user_id=? AND request_key=? FOR UPDATE", v.ClientID, key))
		if err == nil {
			if attempt.RequestKey != key || attempt.Expectation.ProviderID != provider || attempt.Expectation.MerchantAccountID != merchant || attemptInvoice06(attempt, v) != nil {
				return out, ErrPaymentConflict
			}
			return result{attempt, true}, tx.Commit()
		}
		if !errors.Is(err, ErrNotFound) {
			return out, err
		}
		if v.Status != "unpaid" || v.BalanceDue.AmountMinor <= 0 {
			return out, ErrPaymentState
		}
		if payable, err := serviceInvoicePayable06(ctx, tx, v); err != nil {
			return out, err
		} else if !payable {
			return out, ErrPaymentState
		}
		if err = invoiceAttemptBarrier06(ctx, tx, id, false); err != nil {
			return out, err
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		attempt = PaymentAttempt{ID: ID(), RequestKey: key, Expectation: expectation06(v, provider, merchant), State: "pending", CreatedAt: now, UpdatedAt: now}
		_, err = tx.ExecContext(ctx, "INSERT INTO payment_attempts(id,request_key,user_id,invoice_id,order_id,provider_id,merchant_account_id,amount_minor,currency,currency_decimal_places,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,'pending',?,?)", attempt.ID, key, v.ClientID, id, v.OrderID, provider, merchant, attempt.Expectation.AmountMinor, v.Total.Currency, v.Total.DecimalPlaces, now, now)
		if err != nil {
			return out, err
		}
		return result{attempt, false}, tx.Commit()
	})
	return out.attempt, out.replay, err
}

// The parent invoice is locked. Manual confirmation can complete offline
// instructions, but online or uncertain attempts remain a payment barrier.
func invoiceAttemptBarrier06(ctx context.Context, tx *sql.Tx, id string, offline bool) error {
	if err := expirePaymentsSQL(ctx, tx, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE invoice_id=? AND state IN ('pending','checkout','uncertain') FOR UPDATE", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		attempt, err := readPaymentAttempt(rows)
		if err != nil {
			return err
		}
		if !offline || attempt.State != "checkout" || attempt.Checkout == nil || attempt.Checkout.Flow != "instructions" {
			return ErrPaymentConflict
		}
	}
	return rows.Err()
}
func invoicePayment06(ctx context.Context, tx *sql.Tx, record invoiceRecord06, payment api.InvoicePayment, method string, attemptID *string, at time.Time) (api.AdminInvoice, error) {
	v := record.Invoice
	if v.Status != "unpaid" || payment.Amount.AmountMinor <= 0 || payment.Amount.AmountMinor > v.BalanceDue.AmountMinor {
		return v, ErrPaymentState
	}
	if payable, err := serviceInvoicePayable06(ctx, tx, v); err != nil {
		return v, err
	} else if !payable {
		return v, ErrPaymentState
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO invoice_payments(id,invoice_id,attempt_id,method,gateway,reference,amount_minor,refunded_minor,status,created_at) VALUES(?,?,?,?,?,?,?,0,'succeeded',?)", payment.ID, v.ID, attemptID, method, payment.Gateway, payment.Reference, payment.Amount.AmountMinor, at); err != nil {
		return v, err
	}
	column := "amount_paid_minor"
	if method == "credit" {
		column = "credit_applied_minor"
		v.CreditApplied.AmountMinor += payment.Amount.AmountMinor
	} else {
		v.AmountPaid.AmountMinor += payment.Amount.AmountMinor
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invoices SET "+column+"=? WHERE id=?", func() int64 {
		if method == "credit" {
			return v.CreditApplied.AmountMinor
		}
		return v.AmountPaid.AmountMinor
	}(), v.ID); err != nil {
		return v, err
	}
	v.BalanceDue.AmountMinor -= payment.Amount.AmountMinor
	payment.Status, payment.CreatedAt = "succeeded", stamp06(at)
	payment.Amount.Currency, payment.Amount.DecimalPlaces = v.Total.Currency, v.Total.DecimalPlaces
	v.Payments = append(v.Payments, payment)
	record.Invoice = v
	notice := v
	notice.Total = payment.Amount
	if err := financialEmail06(ctx, tx, notice, "payment_received"); err != nil {
		return v, err
	}
	if v.BalanceDue.AmountMinor == 0 {
		return completeInvoice06(ctx, tx, record, at)
	}
	return v, nil
}
func (m *MySQL) AddInvoicePayment06(ctx context.Context, actor User, id string, input api.AdminAddPaymentInput, key string) (api.AdminInvoice, error) {
	raw, _ := json.Marshal(input)
	if api.ValidateInput("adminAddPayment", raw) != nil || !keyPattern.MatchString(key) {
		return api.AdminInvoice{}, ErrCommerceInput
	}
	fp := digest(id + ":" + string(raw))
	received, _ := time.Parse(time.RFC3339Nano, input.ReceivedAt)
	if received.After(time.Now().Add(5 * time.Minute)) {
		return api.AdminInvoice{}, ErrCommerceInput
	}
	return retryPayment(ctx, func() (api.AdminInvoice, error) {
		actor := actor // Reset authorization input for every transaction attempt.
		var out api.AdminInvoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		if err = financialRequestLock06(ctx, tx, actor.ID, "invoice:payment", key, fp); err != nil {
			return out, err
		}
		actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
		if err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, "", id, false)
		if err != nil {
			return out, err
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "invoice:payment", key, fp, &out)
		if err != nil || replay {
			return out, err
		}
		if err = invoiceAttemptBarrier06(ctx, tx, id, true); err != nil {
			return out, err
		}
		out, err = invoicePayment06(ctx, tx, record, api.InvoicePayment{ID: ID(), Method: "manual", Reference: &input.Reference, Amount: commerce.Money(record.Invoice.Total.Currency, record.Invoice.Total.DecimalPlaces, input.AmountMinor)}, input.Method, nil, received)
		if err != nil {
			return out, err
		}
		if out.Status == "paid" {
			if _, err = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='voided',updated_at=UTC_TIMESTAMP(6) WHERE invoice_id=? AND state='checkout'", id); err != nil {
				return out, err
			}
		}
		if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "invoice.payment_recorded", id, 0, "Manual payment recorded")); err != nil {
			return out, err
		}
		if err = recordRequest06(ctx, tx, actor.ID, "invoice:payment", key, fp, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}
func applyInvoiceCredit06(ctx context.Context, tx *sql.Tx, record invoiceRecord06, full bool) (api.AdminInvoice, error) {
	v := record.Invoice
	if v.Kind == "top_up" || v.Status != "unpaid" {
		return v, ErrPaymentState
	}
	if err := invoiceAttemptBarrier06(ctx, tx, v.ID, false); err != nil {
		return v, err
	}
	money, err := accountMoney06(ctx, tx, v.ClientID, "")
	if err != nil {
		return v, err
	}
	if money.Currency != v.Total.Currency || money.DecimalPlaces != v.Total.DecimalPlaces {
		return v, ErrCurrency
	}
	balance, _, err := creditBalance06(ctx, tx, v.ClientID, money)
	if err != nil {
		return v, err
	}
	if full && balance.AmountMinor < v.BalanceDue.AmountMinor {
		return v, ErrInsufficientCredit
	}
	amount := min(balance.AmountMinor, v.BalanceDue.AmountMinor)
	if amount == 0 {
		return v, nil
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	entry, err := appendCredit06(ctx, tx, v.ClientID, "invoice_payment", -amount, &v.ID, "Credit applied to "+v.Number, now)
	if err != nil {
		return v, err
	}
	return invoicePayment06(ctx, tx, record, api.InvoicePayment{ID: entry.ID, Method: "credit", Amount: commerce.Money(money.Currency, money.DecimalPlaces, amount)}, "credit", nil, now)
}
func (m *MySQL) PayCredit06(ctx context.Context, actor User, id, key string) (api.Invoice, error) {
	if !keyPattern.MatchString(key) {
		return api.Invoice{}, ErrCommerceInput
	}
	fp := invoicePayFingerprint06(id, "credit", "")
	return retryPayment(ctx, func() (api.Invoice, error) {
		var out api.Invoice
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		record, err := lockInvoice06(ctx, tx, actor.ID, id, true)
		if err != nil {
			return out, err
		}
		replay, err := requestReplay06(ctx, tx, actor.ID, "invoice:pay", key, fp, &out)
		if err != nil || replay {
			return out, err
		}
		v, err := applyInvoiceCredit06(ctx, tx, record, true)
		if err != nil {
			return out, err
		}
		out = customerInvoice06(v)
		if err = recordRequest06(ctx, tx, actor.ID, "invoice:pay", key, fp, out); err != nil {
			return out, err
		}
		return out, tx.Commit()
	})
}

func settlementResult06(record invoiceRecord06, attempt PaymentAttempt, disposition string, replay bool) PaymentSettlement {
	v := record.Invoice
	format := CurrencyFormat{Code: v.Total.Currency, DecimalPlaces: int(v.Total.DecimalPlaces)}
	i := Invoice{ID: v.ID, Number: v.Number, UserID: v.ClientID, Status: v.Status, AmountMinor: v.Total.AmountMinor, Currency: v.Total.Currency, CurrencyFormat: format, SettlementReview: record.Review}
	o := Order{ID: invoiceOrderReference06(v), UserID: v.ClientID, Currency: v.Total.Currency, CurrencyFormat: format, TotalMinor: v.Total.AmountMinor, Status: "pending_payment"}
	if v.OrderID != nil {
		i.OrderID = *v.OrderID
	}
	if v.PaidAt != nil {
		at, _ := time.Parse(time.RFC3339Nano, *v.PaidAt)
		i.PaidAt = &at
		o.Status = "paid"
	}
	if v.Status == "cancelled" {
		i.Status = "void"
		o.Status = "cancelled"
	}
	return PaymentSettlement{AttemptID: attempt.ID, Invoice: i, Order: o, Disposition: disposition, Replay: replay}
}
func (m *MySQL) settleInvoicePayment06(ctx context.Context, id string, payment integrations.VerifiedPayment) (PaymentSettlement, error) {
	return retryPayment(ctx, func() (PaymentSettlement, error) {
		var out PaymentSettlement
		tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return out, err
		}
		defer tx.Rollback()
		initial, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=?", id))
		if err != nil {
			return out, err
		}
		if err = validatePayment(initial, payment); err != nil {
			return out, err
		}
		record, err := lockInvoice06(ctx, tx, initial.Expectation.AccountID, initial.Expectation.InvoiceID, false)
		if err != nil {
			return out, err
		}
		attempt, err := readPaymentAttempt(tx.QueryRowContext(ctx, "SELECT "+paymentAttemptColumns+" FROM payment_attempts WHERE id=? FOR UPDATE", id))
		if err != nil {
			return out, err
		}
		if err = validatePayment(attempt, payment); err != nil {
			return out, err
		}
		if err = attemptInvoice06(attempt, record.Invoice); err != nil {
			return out, err
		}
		rows, err := tx.QueryContext(ctx, "SELECT attempt_id,provider_id,event_id,transaction_id,settled_at,received_at,disposition FROM payment_settlements WHERE attempt_id=? OR (provider_id=? AND (event_id=? OR transaction_id=?)) FOR UPDATE", id, payment.ProviderID, payment.EventID, payment.TransactionID)
		if err != nil {
			return out, err
		}
		replay := false
		disposition := "settled"
		for rows.Next() {
			var receipt paymentReceipt
			if err = rows.Scan(&receipt.AttemptID, &receipt.ProviderID, &receipt.EventID, &receipt.TransactionID, &receipt.SettledAt, &receipt.ReceivedAt, &receipt.Disposition); err != nil {
				rows.Close()
				return out, err
			}
			if replay || !samePaymentReceipt(receipt, attempt, payment) || attempt.State != "settled" {
				rows.Close()
				return out, ErrPaymentConflict
			}
			replay = true
			disposition = receipt.Disposition
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
		if replay {
			return settlementResult06(record, attempt, disposition, true), tx.Commit()
		}
		if attempt.State == "settled" {
			return out, ErrPaymentState
		}
		v := record.Invoice
		if v.Status == "cancelled" {
			disposition = "cancelled"
		} else if v.Status != "unpaid" || attempt.Expectation.AmountMinor > v.BalanceDue.AmountMinor {
			disposition = "duplicate"
		}
		if disposition == "settled" {
			payable, err := serviceInvoicePayable06(ctx, tx, v)
			if err != nil {
				return out, err
			}
			if !payable {
				disposition = "duplicate"
			}
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		paid := payment.SettledAt.UTC().Truncate(time.Microsecond)
		if _, err = tx.ExecContext(ctx, "INSERT INTO payment_settlements(attempt_id,provider_id,event_id,transaction_id,settled_at,received_at,disposition) VALUES(?,?,?,?,?,?,?)", id, payment.ProviderID, payment.EventID, payment.TransactionID, paid, now, disposition); err != nil {
			return out, err
		}
		if disposition == "settled" {
			record.Invoice, err = invoicePayment06(ctx, tx, record, api.InvoicePayment{ID: id, Method: "gateway", Gateway: &payment.ProviderID, Reference: &payment.TransactionID, Amount: commerce.Money(v.Total.Currency, v.Total.DecimalPlaces, payment.AmountMinor)}, "gateway", &id, paid)
			if err != nil {
				return out, err
			}
		} else {
			if _, err = tx.ExecContext(ctx, "INSERT INTO invoice_payments(id,invoice_id,attempt_id,method,gateway,reference,amount_minor,refunded_minor,status,created_at) VALUES(?,?,?,'gateway',?,?,?,0,'duplicate',?)", id, v.ID, id, payment.ProviderID, payment.TransactionID, payment.AmountMinor, paid); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE invoices SET settlement_review=TRUE WHERE id=?", v.ID); err != nil {
				return out, err
			}
			record.Review = true
		}
		if _, err = tx.ExecContext(ctx, "UPDATE payment_attempts SET state='settled',updated_at=? WHERE id=?", now, id); err != nil {
			return out, err
		}
		audit := Audit{ID: ID(), UserID: v.ClientID, Action: "payment." + disposition, Target: v.ID, CreatedAt: now, ActorEmail: "payment:" + payment.ProviderID, Detail: "Verified payment event recorded"}
		if err = insertCurrencyAudit(ctx, tx, audit); err != nil {
			return out, err
		}
		return settlementResult06(record, attempt, disposition, false), tx.Commit()
	})
}
