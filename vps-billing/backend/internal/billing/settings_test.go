package billing

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSettingsAuthorizationSnapshotsAndAudit(t *testing.T) {
	m := NewMemory()
	srv := httptest.NewServer(NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID()}).Handler())
	defer srv.Close()
	anon := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	settings, _ := m.Settings(t.Context())
	settings.SiteName = "测试云"
	settings.InvoiceIssuer = "测试公司"
	settings.EmailSignature = "内部署名"
	for _, c := range []*testClient{anon, customer} {
		status, _ := c.request("POST", "/api/v1/admin/site-settings", settings, "")
		want := 403
		if c == anon {
			want = 401
		}
		if status != want {
			t.Fatalf("settings write %d want %d", status, want)
		}
	}
	oldCSRF := admin.csrf
	admin.csrf = "invalid"
	if status, _ := admin.request("POST", "/api/v1/admin/site-settings", settings, ""); status != 403 {
		t.Fatal(status)
	}
	admin.csrf = oldCSRF
	if status, _ := admin.request("POST", "/api/v1/admin/site-settings", settings, ""); status != 200 {
		t.Fatal(status)
	}
	if status, _ := admin.request("POST", "/api/v1/admin/site-settings", settings, ""); status != 409 {
		t.Fatal("stale revision", status)
	}
	_, public := anon.request("GET", "/api/v1/site-settings", nil, "")
	raw, _ := json.Marshal(public)
	if strings.Contains(string(raw), "内部署名") || strings.Contains(string(raw), "email_signature") {
		t.Fatal("private template exposed")
	}
	if status, _ := customer.request("POST", "/api/v1/orders", map[string]string{"plan_id": "starter", "hostname": "identity-test"}, "identity-key"); status != 201 {
		t.Fatal(status)
	}
	settings, _ = m.Settings(t.Context())
	settings.InvoiceIssuer = "后来公司"
	if status, _ := admin.request("POST", "/api/v1/admin/site-settings", settings, ""); status != 200 {
		t.Fatal(status)
	}
	u, _ := m.UserByEmail(t.Context(), "demo@aster.local")
	invoices, _ := m.Invoices(t.Context(), u.ID)
	if len(invoices) != 1 || invoices[0].Issuer == nil || invoices[0].Issuer.Name != "测试公司" {
		t.Fatal("historical invoice identity changed", invoices)
	}
	events, _ := m.Audits(t.Context(), "")
	count := 0
	for _, event := range events {
		if event.Action == "site.settings.updated" {
			count++
			if strings.Contains(event.Detail, "内部署名") {
				t.Fatal("settings values logged")
			}
		}
	}
	if count != 2 {
		t.Fatal("settings audit count", count)
	}
}

func TestSettingsValidation(t *testing.T) {
	good := DefaultSettings()
	if !ValidateSettings(good) {
		t.Fatal("invalid default")
	}
	for _, mutate := range []func(*SiteSettings){
		func(s *SiteSettings) { s.DefaultLocale = "fr" }, func(s *SiteSettings) { s.DefaultCurrency = "eur" },
		func(s *SiteSettings) { s.SupportURL = "javascript:alert(1)" }, func(s *SiteSettings) { s.SupportURL = "https://user:secret@example.com" },
		func(s *SiteSettings) { s.DisplayURL = "https://example.com/path" }, func(s *SiteSettings) { s.Logo = "https://remote.example/logo.svg" },
		func(s *SiteSettings) { s.Theme = "body {color:red}" }, func(s *SiteSettings) { s.SupportEmail = "Name <test@example.com>" },
		func(s *SiteSettings) { s.SiteName = "bad\nname" }, func(s *SiteSettings) { s.InvoiceIssuer = "" },
		func(s *SiteSettings) { s.SupportURL = "https://127.0.0.1" }, func(s *SiteSettings) { s.SupportURL = "https://[::1]" },
		func(s *SiteSettings) { s.SupportURL = "https://localhost" }, func(s *SiteSettings) { s.SupportURL = "https://support.example:8443" },
		func(s *SiteSettings) { s.SupportURL = "https://invalid_host.example" }, func(s *SiteSettings) { s.DisplayURL = "https://example.com?" },
		func(s *SiteSettings) { s.SiteName = "<script>alert(1)</script>" }, func(s *SiteSettings) { s.SiteName = "company\u202ename" },
		func(s *SiteSettings) { s.Revision = MaxMinor + 1 },
	} {
		bad := good
		mutate(&bad)
		if ValidateSettings(bad) {
			t.Fatalf("accepted invalid settings %+v", bad)
		}
	}
}

func TestRejectTLSDowngradeBeforeConnecting(t *testing.T) {
	_, err := OpenMySQL(context.Background(), "user@tcp(127.0.0.1:1)/aster?tls=true&allowFallbackToPlaintext=true", false)
	if err == nil || !strings.Contains(err.Error(), "TLS is required") {
		t.Fatal("did not reject plaintext fallback", err)
	}
}

func TestProxyIPv6AliasesShareLoginLimit(t *testing.T) {
	app := NewApp(NewMemory(), Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), TrustedProxyCIDRs: []string{"127.0.0.1/32"}})
	app.attempts["ip:2001:db8::1"] = bucket{Count: 20, Start: time.Now()}
	token := ID() + ID()
	for _, ip := range []string{"2001:db8::1", "2001:0db8:0000:0000:0000:0000:0000:0001"} {
		r := httptest.NewRequest("POST", "http://localhost/api/v1/auth/login", strings.NewReader(`{"email":"missing@example.com","password":"wrong"}`))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Real-IP", ip)
		r.Header.Set("Cookie", app.cookieName()+"="+token)
		r.Header.Set("X-CSRF-Token", app.csrf(token))
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		if w.Code != 429 {
			t.Fatalf("alias %s bypassed limit: %d", ip, w.Code)
		}
	}
}

func TestJSONMediaType(t *testing.T) {
	for _, kind := range []string{"application/json-fake", "application/json;broken"} {
		r := httptest.NewRequest("POST", "http://localhost", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", kind)
		w := httptest.NewRecorder()
		var v map[string]any
		if body(w, r, &v) || w.Code != 415 {
			t.Fatal("invalid media type accepted", kind, w.Code)
		}
	}
}
