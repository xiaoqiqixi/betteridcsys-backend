package billing

import (
	"aster/billing/internal/financeapi"
	smtp "aster/billing/pkg/pluginapi/smtpv1"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

func boundedRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func siteSettingsFromLegacy(old SiteSettings) financeapi.SiteSettings {
	v := financeapi.SiteSettings{SiteName: boundedRunes(old.SiteName, 80), Logo: old.Logo, Theme: "system", Accent: "blue", DefaultLocale: old.DefaultLocale, EnabledLocales: []string{"zh-CN", "en"}, SupportEmail: old.SupportEmail, CompanyName: boundedRunes(old.CompanyName, 160), CompanyAddress: boundedRunes(old.InvoiceAddress, 400), RegistrationOpen: true, Revision: old.Revision}
	if old.Theme == "violet" {
		v.Accent = "violet"
	} else if old.Theme == "teal" {
		v.Accent = "emerald"
	}
	return v
}
func (a *App) siteSettings6(ctx context.Context) (financeapi.SiteSettings, error) {
	if m, ok := a.Store.(*MySQL); ok {
		return a.readSiteSettings6(ctx, m.DB)
	}
	old, err := a.Store.Settings(ctx)
	return siteSettingsFromLegacy(old), err
}
func (a *App) accountSettingsHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	switch path {
	case "/site-settings", "/admin/site-settings":
		if r.Method == "GET" {
			v, err := a.siteSettings6(r.Context())
			a.accountResult(w, r, v, err)
		} else if path == "/admin/site-settings" && r.Method == "PATCH" {
			a.updateSiteSettings6(w, r, u)
		} else if path == "/admin/site-settings" && r.Method == "POST" {
			return false
		} else {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	case "/admin/security":
		if r.Method == "GET" {
			v, err := a.securitySettings(r.Context())
			a.accountResult(w, r, v, err)
		} else if r.Method == "PATCH" {
			a.updateSecuritySettings(w, r, u)
		} else {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	case "/admin/integrations/smtp", "/admin/integrations/smtp/test":
		a.smtpSettings6HTTP(w, r, u, path)
		return true
	}
	return false
}
func (a *App) updateSiteSettings6(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.SiteSettings
	if !accountBody(w, r, "adminUpdateSiteSettings", &in) {
		return
	}
	if !slices.Contains(in.EnabledLocales, in.DefaultLocale) || (in.SupportEmail != "" && !validAccountEmail(in.SupportEmail)) {
		fail(w, r, 400, "INVALID_SETTINGS", "Use an enabled default language and a valid support email")
		return
	}
	for _, link := range []string{in.TermsURL, in.PrivacyURL, in.Social.Telegram, in.Social.Discord, in.Social.X} {
		if !validDisplayURL(link, false) {
			fail(w, r, 400, "INVALID_SETTINGS", "Display links must use HTTPS without credentials")
			return
		}
	}
	v, err := a.accountTransaction(r, u.ID, "site-settings", in, false, func(tx *sql.Tx) (any, error) {
		// Serialize with the legacy settings writer and preserve invoice identities.
		var oldRaw []byte
		var legacy SiteSettings
		if err := tx.QueryRowContext(r.Context(), "SELECT document FROM site_settings WHERE id=1 FOR UPDATE").Scan(&oldRaw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(oldRaw, &legacy); err != nil {
			return nil, err
		}
		old, err := a.readSiteSettings6(r.Context(), tx)
		if err != nil {
			return nil, err
		}
		if old.Revision != in.Revision || in.Revision >= MaxMinor || legacy.Revision >= MaxMinor {
			return nil, ErrConflict
		}
		next := in
		next.Revision++
		raw, _ := json.Marshal(next)
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO account_settings(name,document) VALUES('site',?) ON DUPLICATE KEY UPDATE document=?", raw, raw); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.SiteName) != "" {
			legacy.SiteName = in.SiteName
		}
		legacy.CompanyName, legacy.InvoiceAddress, legacy.SupportEmail, legacy.DefaultLocale = in.CompanyName, in.CompanyAddress, in.SupportEmail, in.DefaultLocale
		if strings.TrimSpace(in.CompanyName) != "" {
			legacy.InvoiceIssuer = in.CompanyName
		} else if strings.TrimSpace(in.SiteName) != "" {
			legacy.InvoiceIssuer = in.SiteName
		}
		if in.Logo != "orbit" {
			legacy.Logo = in.Logo
		}
		switch in.Accent {
		case "violet":
			legacy.Theme = "violet"
		case "emerald":
			legacy.Theme = "teal"
		default:
			legacy.Theme = "blue"
		}
		if !ValidateSettings(legacy) {
			return nil, problem(400, "INVALID_SETTINGS", "Invalid company identity or display text")
		}
		legacy.Revision++
		raw, _ = json.Marshal(legacy)
		if _, err = tx.ExecContext(r.Context(), "UPDATE site_settings SET document=? WHERE id=1", raw); err != nil {
			return nil, err
		}
		return next, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "site.settings.updated", "site")
	})
	a.accountResult(w, r, v, err)
}
func (a *App) updateSecuritySettings(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.SecuritySettings
	if !accountBody(w, r, "adminUpdateSecurity", &in) {
		return
	}
	v, err := a.accountTransaction(r, u.ID, "security-settings", in, false, func(tx *sql.Tx) (any, error) {
		var raw []byte
		if err := tx.QueryRowContext(r.Context(), "SELECT document FROM account_settings WHERE name='security' FOR UPDATE").Scan(&raw); err != nil {
			return nil, err
		}
		var old financeapi.SecuritySettings
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, err
		}
		if old.Revision != in.Revision || in.Revision >= MaxMinor {
			return nil, ErrConflict
		}
		next := in
		next.Revision++
		raw, _ = json.Marshal(next)
		if _, err := tx.ExecContext(r.Context(), "UPDATE account_settings SET document=? WHERE name='security'", raw); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE sessions s JOIN users u ON u.id=s.user_id SET s.expires_at=LEAST(s.expires_at,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL IF(u.role='admin',?,?) HOUR))", in.StaffSessionHours, in.ClientSessionHours); err != nil {
			return nil, err
		}
		return next, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "security.settings_updated", "security")
	})
	a.accountResult(w, r, v, err)
}

func (a *App) smtpSettings6(ctx context.Context) (financeapi.SmtpSettings, error) {
	record, err := a.Store.EmailSettings(ctx)
	if err != nil {
		return financeapi.SmtpSettings{}, err
	}
	creds, err := a.SMTP.credentials(record)
	if err != nil {
		return financeapi.SmtpSettings{}, err
	}
	security := "starttls"
	if record.TLSMode == smtp.ImplicitTLS {
		security = "tls"
	}
	return financeapi.SmtpSettings{Host: record.Host, Port: int64(record.Port), Security: security, Username: creds.Username, PasswordConfigured: creds.Password != "", FromEmail: record.FromAddress, FromName: record.FromName, Enabled: record.Enabled, Revision: record.Revision}, nil
}
func (a *App) smtpSettings6HTTP(w http.ResponseWriter, r *http.Request, u User, path string) {
	if path == "/admin/integrations/smtp/test" {
		if r.Method != "POST" {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return
		}
		var in financeapi.AdminTestSmtpInput
		if !accountBody(w, r, "adminTestSmtp", &in) {
			return
		}
		current, err := a.Store.EmailSettings(r.Context())
		if err == nil {
			err = a.SMTP.Test(r.Context(), a.Integrations, u, current.Revision, in.To)
		}
		if err != nil {
			a.emailError(w, r, err)
		} else {
			jsonOut(w, 200, okData())
		}
		return
	}
	if r.Method == "PATCH" {
		var in financeapi.AdminUpdateSmtpInput
		if !accountBody(w, r, "adminUpdateSmtp", &in) {
			return
		}
		mode := smtp.STARTTLS
		if in.Security == "tls" {
			mode = smtp.ImplicitTLS
		}
		input := EmailSettingsUpdate{Revision: in.Revision, Enabled: in.Enabled, Host: in.Host, Port: int(in.Port), TLSMode: mode, FromAddress: in.FromEmail, FromName: in.FromName, Username: in.Username, Password: in.Password}
		if in.Username == "" && !in.Enabled {
			input.ClearUsername, input.ClearPassword = true, true
		}
		if _, err := a.SMTP.Update(r.Context(), u, input); err != nil {
			a.emailError(w, r, err)
			return
		}
	} else if r.Method != "GET" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	v, err := a.smtpSettings6(r.Context())
	if err != nil {
		a.emailError(w, r, err)
	} else {
		jsonOut(w, 200, v)
	}
}
