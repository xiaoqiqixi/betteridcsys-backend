package billing

import (
	"aster/billing/internal/commerce"
	api "aster/billing/internal/financeapi"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type commerceCouponStore interface {
	Coupons06(context.Context) ([]api.Coupon, error)
	SaveCoupon06(context.Context, User, string, api.AdminCreateCouponInput) (api.Coupon, error)
	Quote06(context.Context, User, api.CreateQuoteInput, time.Time) (commerce.CalculatedQuote, error)
}

func (a *App) quote06HTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var in api.CreateQuoteInput
	if !commerceBody(w, r, "createQuote", &in) {
		return
	}
	actor, err := a.current(r)
	if err != nil && !errors.Is(err, ErrNotFound) {
		a.internal(w, r, err)
		return
	}
	s, ok := a.Store.(commerceCouponStore)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Quote storage is unavailable")
		return
	}
	out, err := s.Quote06(r.Context(), actor, in, time.Now().UTC())
	if err != nil {
		a.commerceError(w, r, err)
		return
	}
	jsonOut(w, 200, out.Quote)
}
func (a *App) coupons06HTTP(w http.ResponseWriter, r *http.Request, actor User, p string) bool {
	if p != "/admin/coupons" && !strings.HasPrefix(p, "/admin/coupons/") {
		return false
	}
	s, ok := a.Store.(commerceCouponStore)
	if !ok {
		fail(w, r, 503, "FEATURE_UNAVAILABLE", "Coupon storage is unavailable")
		return true
	}
	id := ""
	if p != "/admin/coupons" {
		id = strings.TrimPrefix(p, "/admin/coupons/")
		if !validCancellationID(id) {
			fail(w, r, 404, "NOT_FOUND", "Coupon not found")
			return true
		}
	}
	if id == "" && r.Method == "GET" {
		out, err := s.Coupons06(r.Context())
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	if id == "" && r.Method == "POST" || id != "" && r.Method == "PATCH" {
		op := "adminCreateCoupon"
		if id != "" {
			op = "adminUpdateCoupon"
		}
		var in api.AdminCreateCouponInput
		if !commerceBody(w, r, op, &in) {
			return true
		}
		out, err := s.SaveCoupon06(r.Context(), actor, id, in)
		if err != nil {
			a.commerceError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return true
	}
	fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	return true
}
func (a *App) paymentGateways06HTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	out := []api.PaymentGateway{}
	manifest, _, err := a.paymentSelection()
	if err == nil {
		currencies := []string{}
		for _, c := range manifest.Payment.Currencies {
			currencies = append(currencies, c.Code)
		}
		name := text06(manifest.Payment.Name)
		if name == "" {
			name = manifest.ID
		}
		out = append(out, api.PaymentGateway{ID: manifest.ID, Name: name, Description: manifest.Payment.Description, Icon: "card", Currencies: currencies})
	}
	jsonOut(w, 200, out)
}
