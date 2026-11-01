package billing

import (
	smtp "aster/billing/pkg/pluginapi/smtpv1"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMySQLSiteSettingsContractAndInvoiceIdentity(t *testing.T) {
	f, _, srv := accountServer(t)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	anon := client(t, srv.URL, "", "")
	settings := accountHTTP(t, owner, "GET", "/admin/site-settings", "/admin/site-settings", nil, "").(map[string]any)
	accountHTTP(t, anon, "GET", "/site-settings", "/site-settings", nil, "")
	settings["site_name"] = strings.Repeat("云", 80)
	settings["company_name"] = strings.Repeat("社", 160)
	settings["company_address"] = "International billing address"
	settings["logo"], settings["theme"], settings["accent"] = "orbit", "dark", "amber"
	settings["registration_open"], settings["email_verification_required"] = false, true
	settings["default_locale"] = "en"
	settings["enabled_locales"] = []string{"en"}
	settings["terms_url"] = "https://example.com/terms"
	settings["privacy_url"] = "https://example.com/privacy"
	accountHTTP(t, owner, "PATCH", "/admin/site-settings", "/admin/site-settings", settings, "")
	accountFailure(t, owner, "PATCH", "/admin/site-settings", settings, 409)
	public := accountHTTP(t, anon, "GET", "/site-settings", "/site-settings", nil, "").(map[string]any)
	if public["logo"] != "orbit" || public["registration_open"] != false || public["email_verification_required"] != true {
		t.Fatal(public)
	}
	legacy, err := f.Store.Settings(f.ctx)
	if err != nil || legacy.Identity().Name != settings["company_name"] {
		t.Fatal("invoice identity was not synchronized", legacy, err)
	}
	public["default_locale"] = "zh-CN"
	accountFailure(t, owner, "PATCH", "/admin/site-settings", public, 400)
	customer := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	accountFailure(t, customer, "GET", "/admin/site-settings", nil, 403)
}
func TestMySQLSecuritySettingsPolicyAndSessionLifetimes(t *testing.T) {
	f, _, srv := accountServer(t)
	m := f.Store.(*MySQL)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	customer := client(t, srv.URL, f.customer.Email, "AccountFixture!2026")
	settings := accountHTTP(t, owner, "GET", "/admin/security", "/admin/security", nil, "").(map[string]any)
	settings["password_min_length"], settings["client_session_hours"], settings["staff_session_hours"] = 20, 1, 1
	accountHTTP(t, owner, "PATCH", "/admin/security", "/admin/security", settings, "")
	accountFailure(t, owner, "PATCH", "/admin/security", settings, 409)
	var excessive int
	if err := m.DB.QueryRow("SELECT COUNT(*) FROM sessions WHERE expires_at>DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 1 HOUR)").Scan(&excessive); err != nil || excessive != 0 {
		t.Fatal("old sessions exceed policy", excessive, err)
	}
	accountFailure(t, customer, "POST", "/account/password", map[string]any{"current_password": "AccountFixture!2026", "new_password": "Too short!2026"}, 400)
	accountHTTP(t, customer, "POST", "/account/password", "/account/password", map[string]any{"current_password": "AccountFixture!2026", "new_password": "Long enough password!2026"}, "")
	accountFailure(t, customer, "GET", "/admin/security", nil, 403)
}
func TestMySQLSMTPContractSecretsAndTestSend(t *testing.T) {
	f, a, srv := accountServer(t)
	m := f.Store.(*MySQL)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	settings := accountHTTP(t, owner, "GET", "/admin/integrations/smtp", "/admin/integrations/smtp", nil, "").(map[string]any)
	delete(settings, "password_configured")
	settings["host"], settings["port"], settings["security"] = "smtp.example.com", 465, "tls"
	settings["username"], settings["password"] = "mailer@example.com", "Sensitive SMTP password!2026"
	settings["from_email"], settings["from_name"], settings["enabled"] = "mailer@example.com", "Example mail", true
	updated := accountHTTP(t, owner, "PATCH", "/admin/integrations/smtp", "/admin/integrations/smtp", settings, "").(map[string]any)
	if updated["password_configured"] != true || updated["username"] != "mailer@example.com" {
		t.Fatal(updated)
	}
	raw, _ := json.Marshal(updated)
	if bytes.Contains(raw, []byte(settings["password"].(string))) {
		t.Fatal("password returned")
	}
	var stored []byte
	if err := m.DB.QueryRow("SELECT document FROM email_settings WHERE id=1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(settings["password"].(string))) {
		t.Fatal("plaintext SMTP password stored")
	}
	delete(settings, "password")
	settings["revision"] = updated["revision"]
	settings["from_name"] = "Updated sender"
	accountHTTP(t, owner, "PATCH", "/admin/integrations/smtp", "/admin/integrations/smtp", settings, "")
	calls := 0
	a.SMTP.sender = emailSenderFunc(func(_ context.Context, cfg smtp.Config, msg smtp.Message) error {
		calls++
		if cfg.Password != "Sensitive SMTP password!2026" || msg.To != "recipient@example.com" {
			t.Fatal("saved SMTP credentials not used")
		}
		return nil
	})
	accountHTTP(t, owner, "POST", "/admin/integrations/smtp/test", "/admin/integrations/smtp/test", map[string]any{"to": "recipient@example.com"}, "")
	if calls != 1 {
		t.Fatal("test email not sent")
	}
	accountFailure(t, owner, "POST", "/admin/integrations/smtp/test", map[string]any{"to": "recipient@example.com"}, 429)
}
func TestMySQLAccountSettingsPermissionNegatives(t *testing.T) {
	f, _, srv := accountServer(t)
	owner := client(t, srv.URL, f.admin.Email, "AccountFixture!2026")
	role := accountHTTP(t, owner, "POST", "/admin/roles", "/admin/roles", map[string]any{"name": "Support only", "description": "No settings", "permissions": []string{"tickets.read"}}, "").(map[string]any)
	accountHTTP(t, owner, "PATCH", "/admin/staff/"+f.otherAdmin.ID, "/admin/staff/{id}", map[string]any{"revision": 1, "role_id": role["id"]}, "")
	limited := client(t, srv.URL, f.otherAdmin.Email, "AccountFixture!2026")
	for _, path := range []string{"/admin/site-settings", "/admin/security", "/admin/integrations/smtp"} {
		accountFailure(t, limited, "GET", path, nil, 403)
		accountFailure(t, limited, "PATCH", path, map[string]any{}, 403)
	}
	accountFailure(t, limited, "POST", "/admin/integrations/smtp/test", map[string]any{"to": "recipient@example.com"}, 403)
}
