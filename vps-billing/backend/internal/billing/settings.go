package billing

import (
	"errors"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrSettingsConflict = errors.New("site settings changed")

// Display settings never alter authentication origins, DNS, TLS or provider configuration.
type SiteSettings struct {
	Revision        int64  `json:"revision"`
	DefaultLocale   string `json:"default_locale"`
	DefaultCurrency string `json:"default_currency"`
	SiteName        string `json:"site_name"`
	CompanyName     string `json:"company_name"`
	Logo            string `json:"logo"`
	Theme           string `json:"theme"`
	SupportEmail    string `json:"support_email"`
	SupportURL      string `json:"support_url"`
	DisplayURL      string `json:"display_url"`
	EmailSignature  string `json:"email_signature"`
	InvoiceIssuer   string `json:"invoice_issuer"`
	InvoiceAddress  string `json:"invoice_address"`
}

type InvoiceIdentity struct {
	Name         string `json:"name"`
	Address      string `json:"address"`
	SupportEmail string `json:"support_email"`
}

func DefaultSettings() SiteSettings {
	return SiteSettings{Revision: 1, DefaultLocale: "zh-CN", DefaultCurrency: "CNY", SiteName: "星屿云", Logo: "star", Theme: "blue", InvoiceIssuer: "星屿云"}
}

func (s SiteSettings) Public() map[string]any {
	return map[string]any{"default_locale": s.DefaultLocale, "default_currency": s.DefaultCurrency, "site_name": s.SiteName, "company_name": s.CompanyName, "logo": s.Logo, "theme": s.Theme, "support_email": s.SupportEmail, "support_url": s.SupportURL, "display_url": s.DisplayURL}
}

func (s SiteSettings) Identity() *InvoiceIdentity {
	return &InvoiceIdentity{Name: s.InvoiceIssuer, Address: s.InvoiceAddress, SupportEmail: s.SupportEmail}
}

func validText(s string, max int, multiline bool) bool {
	if !utf8.ValidString(s) || len(s) > max {
		return false
	}
	for _, r := range s {
		if r == '<' || r == '>' || unicode.Is(unicode.Cf, r) || (unicode.IsControl(r) && !(multiline && r == '\n')) {
			return false
		}
	}
	return true
}

var dnsLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

// These URLs are display links only. The server never resolves or fetches them.
// Restrict them to HTTPS public-style DNS names, no embedded credentials or ports.
func validDisplayURL(value string, originOnly bool) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.Host == "" || u.User != nil || u.Fragment != "" || strings.Contains(u.Host, ":") || net.ParseIP(u.Hostname()) != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if len(host) > 253 || !strings.Contains(host, ".") || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return !originOnly || (u.Path == "" && u.RawQuery == "" && !u.ForceQuery)
}

func ValidateSettings(s SiteSettings) bool {
	if (s.DefaultLocale != "zh-CN" && s.DefaultLocale != "en") || !SupportedCurrency(s.DefaultCurrency) {
		return false
	}
	if s.Revision < 1 || s.Revision > MaxMinor || strings.TrimSpace(s.SiteName) == "" || strings.TrimSpace(s.InvoiceIssuer) == "" {
		return false
	}
	for _, v := range []struct {
		value     string
		max       int
		multiline bool
	}{
		{s.SiteName, 120, false}, {s.CompanyName, 240, false}, {s.SupportEmail, 254, false},
		{s.SupportURL, 500, false}, {s.DisplayURL, 300, false}, {s.EmailSignature, 1000, true},
		{s.InvoiceIssuer, 240, false}, {s.InvoiceAddress, 1000, true},
	} {
		if !validText(v.value, v.max, v.multiline) {
			return false
		}
	}
	if s.Logo != "star" && s.Logo != "cloud" && s.Logo != "server" {
		return false
	}
	if s.Theme != "blue" && s.Theme != "teal" && s.Theme != "violet" {
		return false
	}
	if s.SupportEmail != "" {
		address, err := mail.ParseAddress(s.SupportEmail)
		if err != nil || address.Address != s.SupportEmail {
			return false
		}
	}
	return validDisplayURL(s.SupportURL, false) && validDisplayURL(s.DisplayURL, true)
}

// SupportedCurrency validates syntax only; the store registry authorizes actual currencies.
func SupportedCurrency(c string) bool { return validCurrencyCode(c) }

func settingsAudit(actor User) Audit {
	return Audit{ID: ID(), UserID: actor.ID, Action: "site.settings.updated", Target: "site", CreatedAt: time.Now().UTC(), ActorEmail: actor.Email, Detail: "站点设置已更新；历史账单身份和金额未改变"}
}
