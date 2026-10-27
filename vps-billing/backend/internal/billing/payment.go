package billing

import (
	"aster/billing/internal/integrations"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"time"
)

var (
	ErrPaymentInput     = errors.New("invalid payment request")
	ErrPaymentState     = errors.New("payment state does not permit this operation")
	ErrPaymentConflict  = errors.New("payment identity or replay conflict")
	ErrPaymentUncertain = errors.New("payment outcome requires reconciliation")
)

// PaymentAttempt is saved before the first adapter call. Its expectation is a
// snapshot of core records and administrator-selected identity, never callback
// input. No expiry or failed network call releases this reservation: a provider
// may have accepted a checkout even when its response was lost.
type PaymentAttempt struct {
	ID          string                             `json:"id"`
	RequestKey  string                             `json:"-"`
	Expectation integrations.SettlementExpectation `json:"-"`
	State       string                             `json:"state"`
	Checkout    *integrations.Checkout             `json:"checkout,omitempty"`
	CreatedAt   time.Time                          `json:"created_at"`
	UpdatedAt   time.Time                          `json:"updated_at"`
}

type PaymentSettlement struct {
	AttemptID string  `json:"attempt_id"`
	Invoice   Invoice `json:"invoice"`
	Order     Order   `json:"order"`
	Replay    bool    `json:"replay"`
}

type paymentReceipt struct {
	AttemptID, ProviderID, EventID, TransactionID string
	SettledAt, ReceivedAt                         time.Time
}

var paymentProviderID = regexp.MustCompile(`^[a-z][a-z0-9_.-]{1,63}$`)

func paymentToken(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for _, c := range s {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

func paymentActor(actor User) bool {
	return actor.ID != "" && (actor.Role == "customer" || actor.Role == "admin")
}

func validatePaymentInput(actor User, invoiceID, key, provider, merchant string) error {
	if !paymentActor(actor) {
		return ErrNotFound
	}
	if !validCancellationID(invoiceID) || !paymentToken(key, 128) || !paymentProviderID.MatchString(provider) || provider == "payment.disabled" || !paymentToken(merchant, 128) {
		return ErrPaymentInput
	}
	return nil
}

func paymentPair(actor User, o Order, i Invoice) error {
	if !paymentActor(actor) || (actor.Role != "admin" && i.UserID != actor.ID) {
		return ErrNotFound
	}
	if !validCancellationID(o.ID) || !validCancellationID(i.ID) || o.UserID == "" || o.UserID != i.UserID || o.ID != i.OrderID || !validAmount(i.AmountMinor) || o.TotalMinor != i.AmountMinor || o.Currency != i.Currency || o.CurrencyFormat != i.CurrencyFormat || !validCurrencyFormat(i.CurrencyFormat) || i.CurrencyFormat.Code != i.Currency {
		return ErrPaymentState
	}
	return nil
}

func paymentExpectation(o Order, i Invoice, provider, merchant string) integrations.SettlementExpectation {
	return integrations.SettlementExpectation{ProviderID: provider, MerchantAccountID: merchant, AccountID: i.UserID, OrderID: o.ID, InvoiceID: i.ID, AmountMinor: i.AmountMinor, Currency: i.Currency, CurrencyDecimalPlaces: i.CurrencyFormat.DecimalPlaces}
}

func validateAttemptPair(a PaymentAttempt, o Order, i Invoice) error {
	if a.State != "pending" && a.State != "checkout" && a.State != "uncertain" && a.State != "settled" {
		return ErrPaymentState
	}
	if a.Expectation != paymentExpectation(o, i, a.Expectation.ProviderID, a.Expectation.MerchantAccountID) {
		return ErrPaymentConflict
	}
	if a.State == "settled" {
		if o.Status != "paid" || i.Status != "paid" || i.PaidAt == nil {
			return ErrPaymentState
		}
	} else if o.Status != "pending_payment" || i.Status != "unpaid" || i.PaidAt != nil {
		return ErrPaymentState
	}
	return nil
}

func clonePaymentAttempt(a PaymentAttempt) PaymentAttempt {
	if a.Checkout != nil {
		c := *a.Checkout
		a.Checkout = &c
	}
	return a
}

func validateCheckout(a PaymentAttempt, c integrations.Checkout) error {
	u, e := url.Parse(c.URL)
	if c.ProviderID != a.Expectation.ProviderID || !paymentToken(c.SessionID, 128) || e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(c.URL) > 2048 || c.ExpiresAt.IsZero() || !c.ExpiresAt.After(time.Now().UTC()) {
		return ErrPaymentInput
	}
	return nil
}

func validatePayment(a PaymentAttempt, v integrations.VerifiedPayment) error {
	if v.AttemptID != a.ID || !paymentToken(v.EventID, 128) || !paymentToken(v.TransactionID, 128) || v.SettledAt.After(time.Now().UTC().Add(5*time.Minute)) {
		return ErrPaymentConflict
	}
	if integrations.ValidateSettlement(v, a.Expectation) != nil {
		return ErrPaymentConflict
	}
	return nil
}

func samePaymentReceipt(p paymentReceipt, a PaymentAttempt, v integrations.VerifiedPayment) bool {
	return p.AttemptID == a.ID && p.ProviderID == v.ProviderID && p.EventID == v.EventID && p.TransactionID == v.TransactionID && p.SettledAt.Equal(v.SettledAt.UTC().Truncate(time.Microsecond))
}

func paidEvent(o Order, i Invoice, a PaymentAttempt) []byte {
	raw, _ := json.Marshal(map[string]string{"order_id": o.ID, "invoice_id": i.ID, "attempt_id": a.ID, "status": "paid"})
	return raw
}

func paidAudit(o Order, a PaymentAttempt, now time.Time) Audit {
	return Audit{ID: ID(), UserID: o.UserID, Action: "payment.settled", Target: o.ID, CreatedAt: now, ActorEmail: "payment:" + a.Expectation.ProviderID, Detail: "已验签的支付回调与核心账单匹配，订单与账单已原子记账；未执行资源开通"}
}
