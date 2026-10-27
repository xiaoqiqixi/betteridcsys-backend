package billing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"unicode/utf8"
)

// Financial mutations require exact keys, every field and no duplicate/null
// semantics, including nested rows. This avoids encoding/json's case folding.
func financeTokens(d *json.Decoder, depth int) bool {
	if depth > 8 {
		return false
	}
	token, err := d.Token()
	if err != nil || token == nil {
		return false
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			t, e := d.Token()
			key, ok := t.(string)
			if e != nil || !ok || seen[key] || key == "" {
				return false
			}
			for _, c := range key {
				if !(c >= 'a' && c <= 'z' || c == '_') {
					return false
				}
			}
			seen[key] = true
			if !financeTokens(d, depth+1) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim('}')
	case json.Delim('['):
		for d.More() {
			if !financeTokens(d, depth+1) {
				return false
			}
		}
		end, e := d.Token()
		return e == nil && end == json.Delim(']')
	}
	_, delimiter := token.(json.Delim)
	return !delimiter
}
func financeShape(raw json.RawMessage, t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		return financeShape(raw, t.Elem())
	}
	if t.Kind() == reflect.Struct {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != t.NumField() {
			return false
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			value, ok := fields[f.Tag.Get("json")]
			if !ok || !financeShape(value, f.Type) {
				return false
			}
		}
	}
	if t.Kind() == reflect.Slice {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil {
			return false
		}
		for _, value := range values {
			if !financeShape(value, t.Elem()) {
				return false
			}
		}
	}
	return true
}
func financeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	return financeBodyLimit(w, r, v, 64<<10)
}
func financeBodyLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "请使用 JSON 请求")
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	valid := err == nil && utf8.Valid(raw)
	if valid {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		valid = financeTokens(d, 0)
		var extra any
		if d.Decode(&extra) != io.EOF {
			valid = false
		}
	}
	if valid {
		valid = financeShape(raw, reflect.TypeOf(v))
	}
	if valid {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		valid = d.Decode(v) == nil
	}
	if !valid {
		fail(w, r, 400, "INVALID_BODY", "请求格式不正确或包含缺失、重复、不支持的字段")
	}
	return valid
}
func (a *App) currencyError(w http.ResponseWriter, r *http.Request, err error) {
	var inUse *CurrencyInUse
	switch {
	case errors.Is(err, ErrTaxonomy), errors.Is(err, ErrTaxonomyConflict), errors.Is(err, ErrTaxonomyInUse):
		a.taxonomyError(w, r, err)
	case errors.As(err, &inUse):
		observeError(r.Context(), "CURRENCY_IN_USE", "")
		locale := requestLocale(r.Header.Get("Accept-Language"))
		message := "此币种仍有未完成的付款或服务义务"
		if locale == "en" {
			message = "This currency has outstanding payment or service obligations."
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Language", locale)
		w.WriteHeader(409)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "CURRENCY_IN_USE", "message": message, "request_id": w.Header().Get("X-Request-ID"), "details": map[string]any{"currency": inUse.Code, "usage": inUse.Usage}}})
	case errors.Is(err, ErrCurrencyConfig):
		fail(w, r, 400, "INVALID_CURRENCY_CONFIG", "请检查币种字段；默认币种必须启用，已有币种不能删除")
	case errors.Is(err, ErrCurrencyConflict):
		fail(w, r, 409, "CURRENCY_CONFLICT", "币种设置已改变，请重新读取")
	case errors.Is(err, ErrCurrencyPrecision):
		fail(w, r, 409, "CURRENCY_PRECISION_LOCKED", "已有价格或业务记录的币种不能修改小数位")
	case errors.Is(err, ErrCurrencyDisabled):
		fail(w, r, 409, "CURRENCY_DISABLED", "此币种已停用，不能创建新订单")
	case errors.Is(err, ErrCurrency):
		fail(w, r, 400, "UNSUPPORTED_CURRENCY", "未配置此币种")
	case errors.Is(err, ErrPlanPrices):
		fail(w, r, 400, "INVALID_PLAN_PRICES", "价格必须是所选币种精度内的正数，不支持汇率换算或舍入")
	case errors.Is(err, ErrPriceChanged):
		fail(w, r, 409, "PRICE_CHANGED", "报价已改变，请重新确认当前价格后下单")
	case errors.Is(err, ErrInvalidQuote):
		fail(w, r, 400, "INVALID_QUOTE", "请提供完整的报价金额、精度、版本及明确币种")
	case errors.Is(err, ErrPriceConflict):
		fail(w, r, 409, "PRICE_CONFLICT", "套餐价格已改变，请重新读取")
	default:
		a.internal(w, r, err)
	}
}
func (a *App) currenciesHTTP(w http.ResponseWriter, r *http.Request, actor User, admin bool) {
	if r.Method == "GET" {
		out, err := a.Store.Currencies(r.Context(), admin)
		if err != nil {
			a.currencyError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if !admin || r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input CurrencyUpdate
	if !financeBody(w, r, &input) {
		return
	}
	out, err := a.Store.UpdateCurrencies(r.Context(), actor, input)
	if err != nil {
		a.currencyError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
}
func (a *App) planPricesHTTP(w http.ResponseWriter, r *http.Request, actor User) {
	if r.Method == "GET" {
		code, ok := a.queryCurrency(w, r)
		if !ok {
			return
		}
		out, err := a.Store.PlanPrices(r.Context(), code)
		if err != nil {
			a.currencyError(w, r, err)
		} else {
			jsonOut(w, 200, out)
		}
		return
	}
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	var input PlanPricesUpdate
	if !financeBody(w, r, &input) {
		return
	}
	out, err := a.Store.UpdatePlanPrices(r.Context(), actor, input)
	if err != nil {
		a.currencyError(w, r, err)
	} else {
		jsonOut(w, 200, out)
	}
}

func decodePriceQuote(raw []byte) (*PriceQuote, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if !financeTokens(d, 0) || !financeShape(raw, reflect.TypeOf(PriceQuote{})) {
		return nil, ErrInvalidQuote
	}
	var q PriceQuote
	if json.Unmarshal(raw, &q) != nil || !q.valid() {
		return nil, ErrInvalidQuote
	}
	return &q, nil
}
