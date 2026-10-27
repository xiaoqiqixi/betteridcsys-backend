package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/internal/runtimeplugins"
	"aster/billing/pkg/pluginapi"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Logger            *slog.Logger
	Updates           UpdateService
	HostOperations    HostOperationSource
	Plugins           *runtimeplugins.Manager
	PaymentRuntime    PaymentRuntime
	SMTPKey           *SMTPCipher
	Demo              bool
	Origin            string
	Secret            string
	SecureCookie      bool
	TrustedProxyCIDRs []string
}
type bucket struct {
	Count int
	Start time.Time
}
type App struct {
	SMTP         *SMTPService
	Integrations *integrations.Registry
	Store        Store
	Config       Config
	limiterMu    sync.Mutex
	attempts     map[string]bucket
	dummyHash    []byte
}

func NewApp(s Store, c Config) *App {
	return NewAppWithIntegrations(s, c, integrations.Default())
}

// NewAppWithIntegrations injects a validated immutable registry. Nil fails closed.
func NewAppWithIntegrations(s Store, c Config, registry *integrations.Registry) *App {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if registry == nil {
		registry = integrations.Default()
	}
	h, _ := bcrypt.GenerateFromPassword([]byte(ID()), bcrypt.DefaultCost)
	smtpService := newSMTPService(s, c.SMTPKey)
	if c.PaymentRuntime == nil && c.Plugins != nil {
		c.PaymentRuntime = c.Plugins
	}
	if c.Plugins != nil {
		smtpService.runtime = c.Plugins
		smtpService.sender = c.Plugins
	}
	return &App{SMTP: smtpService, Integrations: registry.WithManagedEmail(smtpService), Store: s, Config: c, attempts: map[string]bucket{}, dummyHash: h}
}
func (a *App) cookieName() string {
	if a.Config.SecureCookie {
		return "__Host-aster_session"
	}
	return "aster_demo_session"
}
func digest(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func (a *App) csrf(v string) string {
	h := hmac.New(sha256.New, []byte(a.Config.Secret))
	h.Write([]byte("csrf:" + v))
	return hex.EncodeToString(h.Sum(nil))
}
func (a *App) setCookie(w http.ResponseWriter, v string) {
	http.SetCookie(w, &http.Cookie{Name: a.cookieName(), Value: v, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookie, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
}
func (a *App) token(r *http.Request) string {
	c, e := r.Cookie(a.cookieName())
	if e != nil || len(c.Value) != 64 {
		return ""
	}
	return c.Value
}
func (a *App) current(r *http.Request) (User, error) {
	t := a.token(r)
	if t == "" {
		return User{}, ErrNotFound
	}
	s, e := a.Store.Session(r.Context(), digest(t))
	if e != nil {
		return User{}, e
	}
	u, err := a.Store.UserByID(r.Context(), s.UserID)
	if err == nil && u.Disabled {
		return User{}, ErrNotFound
	}
	if err == nil {
		observeActor(r.Context(), u)
	}
	return u, err
}
func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}
func fail(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	observeError(r.Context(), code, "")
	locale := requestLocale(r.Header.Get("Accept-Language"))
	w.Header().Set("Content-Language", locale)
	if locale == "en" {
		if translated, ok := englishErrors[code]; ok {
			msg = translated
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg, "request_id": w.Header().Get("X-Request-ID")}})
}
func (a *App) internal(w http.ResponseWriter, r *http.Request, e error) {
	observeError(r.Context(), "INTERNAL_ERROR", errorKind(e))
	fail(w, r, 500, "INTERNAL_ERROR", "暂时无法完成请求，请稍后重试")
}
func body(w http.ResponseWriter, r *http.Request, v any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "请使用 JSON 请求")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		fail(w, r, 400, "INVALID_BODY", "请求格式不正确或包含不支持的字段")
		return false
	}
	if e := d.Decode(&struct{}{}); e != io.EOF {
		fail(w, r, 400, "INVALID_BODY", "仅允许一个 JSON 对象")
		return false
	}
	return true
}
func (a *App) Handler() http.Handler {
	return a.observeHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Accept-Language")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if a.Config.SecureCookie {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		a.serve(w, r)
	}))
}
func (a *App) serve(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	if p != "/healthz" && p != "/readyz" {
		origin, _ := url.Parse(a.Config.Origin)
		allowed := strings.EqualFold(r.Host, origin.Host)
		if a.Config.Demo {
			host := r.Host
			if h, _, e := net.SplitHostPort(host); e == nil {
				host = h
			}
			ip := net.ParseIP(host)
			allowed = allowed || strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
		}
		if !allowed {
			fail(w, r, 421, "INVALID_HOST", "请求主机不匹配")
			return
		}
	}

	if p == "/healthz" && r.Method == "GET" {
		jsonOut(w, 200, map[string]string{"status": "ok"})
		return
	}
	if p == "/readyz" && r.Method == "GET" {
		if e := a.Store.Ping(r.Context()); e != nil {
			observeError(r.Context(), "NOT_READY", errorKind(e))
			fail(w, r, 503, "NOT_READY", "服务尚未就绪")
		} else {
			jsonOut(w, 200, map[string]string{"status": "ready"})
		}
		return
	}
	if !strings.HasPrefix(p, "/api/v1/") {
		fail(w, r, 404, "NOT_FOUND", "未找到此资源")
		return
	}
	p = strings.TrimPrefix(p, "/api/v1")
	if r.Method != "GET" && r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
		return
	}
	// Only the dedicated provider callback bypasses browser CSRF. It still
	// requires canonical Host, bounded input and plugin signature verification.
	if strings.HasPrefix(p, "/payment-callbacks/") {
		a.paymentCallbackHTTP(w, r, strings.TrimPrefix(p, "/payment-callbacks/"))
		return
	}
	if r.Method == "POST" {
		t := a.token(r)
		origin := r.Header.Get("Origin")
		if t == "" || !hmac.Equal([]byte(r.Header.Get("X-CSRF-Token")), []byte(a.csrf(t))) || (origin != "" && origin != a.Config.Origin) {
			fail(w, r, 403, "CSRF_INVALID", "安全令牌无效，请刷新页面后重试")
			return
		}
	}
	if p == "/session" && r.Method == "GET" {
		t := a.token(r)
		var user any
		u, e := a.current(r)
		if e == nil {
			user = u
		} else if !errors.Is(e, ErrNotFound) {
			a.internal(w, r, e)
			return
		}
		if t == "" {
			t = ID() + ID()
			a.setCookie(w, t)
		}
		jsonOut(w, 200, map[string]any{"user": user, "csrf_token": a.csrf(t), "demo_mode": a.Config.Demo})
		return
	}
	if p == "/auth/login" && r.Method == "POST" {
		a.login(w, r)
		return
	}
	if p == "/auth/logout" && r.Method == "POST" {
		if e := a.Store.DeleteSession(r.Context(), digest(a.token(r))); e != nil {
			a.internal(w, r, e)
			return
		}
		t := ID() + ID()
		a.setCookie(w, t)
		jsonOut(w, 200, map[string]any{"user": nil, "csrf_token": a.csrf(t)})
		return
	}
	if p == "/catalog-taxonomy" && r.Method == "GET" {
		a.taxonomyHTTP(w, r, User{}, false)
		return
	}
	if p == "/currencies" && r.Method == "GET" {
		a.currenciesHTTP(w, r, User{}, false)
		return
	}
	if p == "/plans" && r.Method == "GET" {
		currency, ok := a.queryCurrency(w, r)
		if !ok {
			return
		}
		v, e := a.Store.Plans(r.Context(), currency)
		if e != nil {
			a.internal(w, r, e)
		} else {
			if a.Config.Demo && requestLocale(r.Header.Get("Accept-Language")) == "en" {
				localizeDemoPlans(v)
			}
			jsonOut(w, 200, v)
		}
		return
	}
	if p == "/site-settings" && r.Method == "GET" {
		settings, err := a.Store.Settings(r.Context())
		if err != nil {
			a.internal(w, r, err)
			return
		}
		jsonOut(w, 200, settings.Public())
		return
	}
	u, e := a.current(r)
	if errors.Is(e, ErrNotFound) {
		fail(w, r, 401, "AUTH_REQUIRED", "请先登录")
		return
	}
	if e != nil {
		a.internal(w, r, e)
		return
	}
	admin := strings.HasPrefix(p, "/admin/")
	if admin && u.Role != "admin" {
		fail(w, r, 403, "FORBIDDEN", "没有权限执行此操作")
		return
	}
	uid := u.ID
	if admin {
		uid = ""
	}
	if r.Method == "POST" && p == "/orders" {
		a.createOrder(w, r, u)
		return
	}
	if planID, matched := productConfigurationPath(p); matched {
		a.productConfigurationHTTP(w, r, u, planID)
		return
	}
	if orderID, matched := provisioningOrderPath(p); matched {
		a.provisioningHTTP(w, r, u, orderID, true)
		return
	}
	if jobID, matched := provisioningJobPath(p); matched {
		a.provisioningHTTP(w, r, u, jobID, false)
		return
	}
	if cancellationID, matched := cancellationPath(p, admin); matched {
		if !admin {
			// The customer path always retains ownership filtering, even for an
			// administrator using their own customer-facing console.
			u.Role = "customer"
		}
		a.cancelOrderHTTP(w, r, u, cancellationID)
		return
	}
	if orderID, matched := orderDetailPath(p, admin); matched {
		a.orderDetailHTTP(w, r, uid, orderID)
		return
	}
	if p == "/admin/customer-accounts" || strings.HasPrefix(p, "/admin/customer-accounts/") {
		a.adminCustomerAccountsHTTP(w, r, u, p)
		return
	}
	if p == "/admin/catalog" || strings.HasPrefix(p, "/admin/catalog/") {
		a.adminCatalogHTTP(w, r, u, p)
		return
	}
	if p == "/admin/catalog-taxonomy" {
		a.taxonomyHTTP(w, r, u, true)
		return
	}
	if p == "/admin/currencies" {
		a.currenciesHTTP(w, r, u, true)
		return
	}
	if p == "/admin/plan-prices" {
		a.planPricesHTTP(w, r, u)
		return
	}
	if p == "/admin/site-settings" {
		if r.Method == "GET" {
			settings, err := a.Store.Settings(r.Context())
			a.result(w, r, settings, err)
			return
		}
		var settings SiteSettings
		if !body(w, r, &settings) {
			return
		}
		if !ValidateSettings(settings) {
			fail(w, r, 400, "INVALID_SETTINGS", "请检查必填项、语言、币种、联系方式和 HTTPS 地址")
			return
		}
		updated, err := a.Store.UpdateSettings(r.Context(), u, settings)
		if errors.Is(err, ErrSettingsConflict) {
			fail(w, r, 409, "SETTINGS_CONFLICT", "设置已被其他管理员修改，请重新读取后再编辑")
			return
		}
		if err != nil {
			a.currencyError(w, r, err)
		} else {
			jsonOut(w, 200, updated)
		}
		return
	}
	if p == "/admin/email-settings" || p == "/admin/email-settings/test" {
		a.emailSettingsHTTP(w, r, u, p == "/admin/email-settings/test")
		return
	}
	if p == "/admin/updates" || p == "/admin/updates/check" || p == "/admin/updates/handoff" || p == "/admin/updates/operation" {
		a.updateHTTP(w, r, u, p)
		return
	}
	if p == "/admin/plugins" || p == "/admin/plugins/check" || p == "/admin/plugins/payment/check" {
		if p == "/admin/plugins/check" || p == "/admin/plugins/payment/check" {
			if r.Method != "POST" {
				fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
				return
			}
			var input struct{}
			if !emailBody(w, r, &input) {
				return
			}
			var identity pluginapi.Identity
			var err error
			if p == "/admin/plugins/payment/check" {
				identity, err = a.Config.Plugins.PaymentCheck(r.Context())
			} else {
				identity, err = a.Config.Plugins.Check(r.Context())
			}
			if err != nil {
				fail(w, r, 503, "PLUGIN_UNAVAILABLE", "插件不可用，请检查安装、启用状态及兼容版本")
				return
			}
			jsonOut(w, 200, identity)
			return
		}
		if r.Method != "GET" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
			return
		}
		items, err := a.Config.Plugins.Discover()
		if err != nil {
			fail(w, r, 503, "PLUGIN_UNAVAILABLE", "插件不可用，请检查安装、启用状态及兼容版本")
			return
		}
		jsonOut(w, 200, map[string]any{"items": items, "smtp": a.Config.Plugins.State(), "payment": a.Config.Plugins.PaymentState()})
		return
	}
	if strings.HasPrefix(p, "/invoices/") || strings.HasPrefix(p, "/admin/invoices/") {
		invoicePath := "/invoices/"
		if admin {
			invoicePath = "/admin/invoices/"
		}
		id := strings.TrimPrefix(p, invoicePath)
		checkout := strings.HasSuffix(id, "/checkout")
		if checkout {
			id = strings.TrimSuffix(id, "/checkout")
		}
		if (checkout && (admin || r.Method != "POST")) || (!checkout && r.Method != "GET") {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "不支持此请求方式")
			return
		}
		invoice, e := a.Store.Invoice(r.Context(), uid, id)
		if errors.Is(e, ErrNotFound) {
			fail(w, r, 404, "NOT_FOUND", "未找到此账单")
			return
		}
		if e != nil {
			a.internal(w, r, e)
			return
		}
		if checkout {
			a.checkout(w, r, u, invoice)
		} else {
			jsonOut(w, 200, invoice)
		}
		return
	}
	if r.Method != "GET" {
		fail(w, r, 404, "NOT_FOUND", "未找到此资源")
		return
	}
	switch p {
	case "/admin/integrations":
		summaries, err := a.Integrations.SummariesContext(r.Context())
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if requestLocale(r.Header.Get("Accept-Language")) == "en" {
			for n := range summaries {
				if summaries[n].ID == "payment.disabled" || summaries[n].ID == "email.disabled" {
					if summaries[n].ID == "payment.disabled" {
						summaries[n].Name = "Payments not connected"
					} else {
						summaries[n].Name = "Disabled email adapter"
					}
					summaries[n].Description = "Disabled adapter; no external operations"
					summaries[n].Limitations = []string{"This disabled adapter performs no external operations or billing state changes."}
				}
				if summaries[n].ID == "email.smtp" {
					summaries[n].Name = "Official SMTP email"
					summaries[n].Description = "Send administrator test messages using saved TLS SMTP settings"
					summaries[n].Limitations = []string{"Administrator test sending only; notifications, email verification and password recovery are not connected.", "Accepted means the SMTP server accepted the message; inbox delivery is not guaranteed."}
				}
			}
		}
		jsonOut(w, 200, map[string]any{"items": summaries})
	case "/services":
		v, e := a.Store.Services(r.Context(), uid)
		a.result(w, r, v, e)
	case "/admin/services":
		a.adminServicesHTTP(w, r)
	case "/orders", "/admin/orders":
		v, e := a.Store.Orders(r.Context(), uid)
		a.result(w, r, v, e)
	case "/invoices", "/admin/invoices":
		v, e := a.Store.Invoices(r.Context(), uid)
		a.result(w, r, v, e)
	case "/audit-events", "/admin/audit-events":
		a.auditEventsHTTP(w, r, u, uid)
	case "/audit":
		if u.Role == "admin" {
			uid = ""
		}
		v, e := a.Store.Audits(r.Context(), uid)
		a.result(w, r, v, e)
	case "/admin/customers":
		v, e := a.Store.Users(r.Context())
		out := []User{}
		for _, x := range v {
			if x.Role == "customer" {
				out = append(out, x)
			}
		}
		a.result(w, r, out, e)
	case "/overview", "/admin/overview":
		a.overview(w, r, uid, admin)
	default:
		fail(w, r, 404, "NOT_FOUND", "未找到此资源")
	}
}
func (a *App) result(w http.ResponseWriter, r *http.Request, v any, e error) {
	if e != nil {
		a.internal(w, r, e)
	} else {
		jsonOut(w, 200, v)
	}
}
func (a *App) allowLogin(key string) bool {
	a.limiterMu.Lock()
	defer a.limiterMu.Unlock()
	now := time.Now()
	if len(a.attempts) > 10000 {
		for k, v := range a.attempts {
			if now.Sub(v.Start) > 15*time.Minute {
				delete(a.attempts, k)
			}
		}
		if len(a.attempts) > 10000 {
			return false
		}
	}
	b := a.attempts[key]
	if now.Sub(b.Start) > 15*time.Minute {
		b = bucket{Start: now}
	}
	b.Count++
	a.attempts[key] = b
	return b.Count <= 20
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !body(w, r, &v) {
		return
	}
	v.Email = strings.ToLower(strings.TrimSpace(v.Email))
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if parsed := net.ParseIP(ip); parsed != nil {
		ip = parsed.String()
	}
	for _, cidr := range a.Config.TrustedProxyCIDRs {
		_, network, e := net.ParseCIDR(cidr)
		if e == nil && network.Contains(net.ParseIP(ip)) {
			forwarded := strings.TrimSpace(r.Header.Get("X-Real-IP"))
			if net.ParseIP(forwarded) != nil {
				ip = net.ParseIP(forwarded).String()
			}
			break
		}
	}
	if !a.allowLogin("ip:"+ip) || !a.allowLogin("email:"+v.Email) {
		w.Header().Set("Retry-After", "900")
		fail(w, r, 429, "RATE_LIMITED", "尝试次数过多，请稍后再试")
		return
	}
	if len(v.Email) > 254 || len(v.Password) > 72 {
		fail(w, r, 401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
		return
	}
	u, e := a.Store.UserByEmail(r.Context(), v.Email)
	hash := a.dummyHash
	if e == nil {
		hash = []byte(u.PasswordHash)
	} else if !errors.Is(e, ErrNotFound) {
		a.internal(w, r, e)
		return
	}
	valid := bcrypt.CompareHashAndPassword(hash, []byte(v.Password)) == nil
	if !valid || e != nil || u.Disabled {
		fail(w, r, 401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
		return
	}
	token := ID() + ID()
	if e = a.Store.PutSession(r.Context(), Session{Revision: u.Revision, Hash: digest(token), UserID: u.ID, Expires: time.Now().UTC().Add(8 * time.Hour)}); e != nil {
		if errors.Is(e, ErrNotFound) || errors.Is(e, ErrConflict) {
			fail(w, r, 401, "INVALID_CREDENTIALS", "邮箱或密码不正确")
			return
		}
		a.internal(w, r, e)
		return
	}
	if e = a.Store.DeleteSession(r.Context(), digest(a.token(r))); e != nil {
		_ = a.Store.DeleteSession(r.Context(), digest(token))
		a.internal(w, r, e)
		return
	}
	observeActor(r.Context(), u)
	a.setCookie(w, token)
	jsonOut(w, 200, map[string]any{"user": u, "csrf_token": a.csrf(token), "demo_mode": a.Config.Demo})
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`)

func (a *App) createOrder(w http.ResponseWriter, r *http.Request, u User) {
	var v struct {
		PlanID   string          `json:"plan_id"`
		Hostname string          `json:"hostname"`
		Currency json.RawMessage `json:"currency"`
		Quote    json.RawMessage `json:"quote"`
	}
	if !body(w, r, &v) {
		return
	}
	currency := ""
	if v.Currency != nil {
		if err := json.Unmarshal(v.Currency, &currency); err != nil || !SupportedCurrency(currency) {
			fail(w, r, 400, "INVALID_CURRENCY", "币种代码必须是三个大写英文字母")
			return
		}
	}
	var quote *PriceQuote
	if v.Quote != nil {
		var err error
		quote, err = decodePriceQuote(v.Quote)
		if err != nil || currency == "" {
			a.currencyError(w, r, ErrInvalidQuote)
			return
		}
	}
	v.Hostname = strings.ToLower(strings.TrimSpace(v.Hostname))
	if !hostnamePattern.MatchString(v.Hostname) || len(v.PlanID) > 64 || v.PlanID == "" {
		fail(w, r, 400, "VALIDATION_ERROR", "请选择套餐，并填写 1–63 位字母、数字或连字符主机名")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !keyPattern.MatchString(key) {
		fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "请提供 8–128 位幂等请求标识")
		return
	}
	o, replayed, e := a.Store.CreateQuotedOrder(r.Context(), u, v.PlanID, v.Hostname, key, currency, quote)
	if errors.Is(e, ErrConflict) {
		fail(w, r, 409, "IDEMPOTENCY_CONFLICT", "该请求标识已用于不同订单")
		return
	}
	if errors.Is(e, ErrCurrency) || errors.Is(e, ErrCurrencyDisabled) || errors.Is(e, ErrPriceChanged) || errors.Is(e, ErrInvalidQuote) {
		a.currencyError(w, r, e)
		return
	}
	if errors.Is(e, ErrUnavailable) {
		fail(w, r, 409, "PLAN_UNAVAILABLE", "该套餐当前不可订购")
		return
	}
	if e != nil {
		a.internal(w, r, e)
		return
	}
	status := 201
	if replayed {
		status = 200
		w.Header().Set("Idempotency-Replayed", "true")
	}
	jsonOut(w, status, o)
}
func (a *App) overview(w http.ResponseWriter, r *http.Request, uid string, admin bool) {
	currency, ok := a.queryCurrency(w, r)
	if !ok {
		return
	}
	s, e := a.Store.Summary(r.Context(), uid, currency)
	if e != nil {
		a.internal(w, r, e)
		return
	}
	config, err := a.Store.Currencies(r.Context(), false)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	cur, known := config.find(currency)
	if !known {
		a.currencyError(w, r, ErrCurrency)
		return
	}
	if admin {
		jsonOut(w, 200, map[string]any{"currency_format": cur.CurrencyFormat, "customers": s.Customers, "total_services": s.TotalServices, "unpaid_invoices": s.PendingInvoices, "revenue_minor": s.Revenue, "unpaid_total_minor": s.UnpaidTotal, "currency": currency, "count_scope": "currency", "customers_scope": "all"})
		return
	}
	os, e := a.Store.RecentOrders(r.Context(), uid)
	if e != nil {
		a.internal(w, r, e)
		return
	}
	jsonOut(w, 200, map[string]any{"currency_format": cur.CurrencyFormat, "active_services": s.ActiveServices, "pending_invoices": s.PendingInvoices, "monthly_total_minor": s.MonthlyTotal, "unpaid_total_minor": s.UnpaidTotal, "currency": currency, "count_scope": "currency", "recent_orders": os, "recent_orders_scope": "all"})
}
