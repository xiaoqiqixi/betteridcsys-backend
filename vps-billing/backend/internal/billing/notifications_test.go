package billing

import (
	"aster/billing/internal/financeapi"
	smtp "aster/billing/pkg/pluginapi/smtpv1"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEmailTemplateAllowListAndLocale(t *testing.T) {
	for _, key := range emailTemplateKeys {
		v := defaultEmailTemplate(key)
		if !validateEmailTemplate(v) {
			t.Fatal("invalid default template", key)
		}
	}
	for _, body := range []string{"{{.password}}", "{{.name.Secret}}", "{{range .name}}{{.}}{{end}}", "{{printf \"%s\" .name}}", "{{template \"other\" .}}", "{{define \"other\"}}text{{end}}", "{{.name | html}}"} {
		if _, err := parseEmailText(body, emailVariables("welcome")); err == nil {
			t.Fatal("template bypass", body)
		}
	}
	v := defaultEmailTemplate("email_verification")
	msg, err := renderEmail(v, financeapi.Profile{Name: "Reader", Email: "reader@example.com", Locale: "en"}, "Example", map[string]string{"url": "https://example.com/verify-email?token=test", "email": "attacker@example.com"})
	if err != nil || msg.To != "reader@example.com" || !strings.Contains(msg.Text, "within 30 minutes") {
		t.Fatal("localized email", msg, err)
	}
	for _, locale := range []string{"en", "zh-CN"} {
		for _, key := range []string{"invoice_created", "invoice_reminder", "invoice_overdue", "payment_received"} {
			msg, err := renderEmail(defaultEmailTemplate(key), financeapi.Profile{Name: "Reader", Email: "reader@example.com", Locale: locale}, "Example", map[string]string{"invoice_number": "INV-12345", "amount": "123.45", "currency": "USD", "due_at": "2026-11-10"})
			if err != nil || !strings.Contains(msg.Text, "INV-12345") || !strings.Contains(msg.Text, "123.45 USD") {
				t.Fatal("transactional details missing", key, locale, err)
			}
		}
	}
	v.Subject["en"] = "Header\r\nInjected: bad"
	if validateEmailTemplate(v) {
		t.Fatal("subject header injection")
	}
}

func queueTestEmail(t *testing.T, a *App, m *MySQL, user, key string) string {
	t.Helper()
	tx, err := m.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = a.queueEmailTx(t.Context(), tx, user, key, map[string]string{"url": "https://example.com/link"}, securityEmail(key)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var id string
	if err = m.DB.QueryRow("SELECT id FROM email_outbox WHERE user_id=? AND template_key=? ORDER BY created_at DESC,id DESC LIMIT 1", user, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestMySQLOutboxPreferencesSecurityAndConcurrentDelivery(t *testing.T) {
	f, a, _ := accountServer(t)
	m := f.Store.(*MySQL)
	if _, err := a.SMTP.Update(f.ctx, f.admin, emailTestUpdate()); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(financeapi.NotificationPreferences{})
	if _, err := m.DB.Exec("INSERT INTO notification_prefs(user_id,document) VALUES(?,?)", f.customer.ID, raw); err != nil {
		t.Fatal(err)
	}
	def := defaultEmailTemplate("password_reset")
	subject, _ := json.Marshal(def.Subject)
	body, _ := json.Marshal(def.Body)
	if _, err := m.DB.Exec("INSERT INTO email_templates(template_key,subject,body,enabled,revision) VALUES('password_reset',?,?,FALSE,1)", subject, body); err != nil {
		t.Fatal(err)
	}
	invoice := queueTestEmail(t, a, m, f.customer.ID, "invoice_created")
	receipt := queueTestEmail(t, a, m, f.customer.ID, "payment_received")
	reset := queueTestEmail(t, a, m, f.customer.ID, "password_reset")
	var sent atomic.Int64
	a.SMTP.sender = emailSenderFunc(func(_ context.Context, _ smtp.Config, msg smtp.Message) error { sent.Add(1); return nil })
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, _, err := a.DrainEmailOutbox(f.ctx); errs <- err })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if sent.Load() != 1 {
		t.Fatal("preferences or concurrent lease failed", sent.Load())
	}
	for _, id := range []string{invoice, receipt, reset} {
		var done bool
		var protected sql.NullString
		if err := m.DB.QueryRow("SELECT sent_at IS NOT NULL,protected_variables FROM email_outbox WHERE id=?", id).Scan(&done, &protected); err != nil || !done || protected.Valid {
			t.Fatal("outbox completion", done, protected.Valid, err)
		}
	}
}
func TestMySQLOutboxFiveAttemptsBackoffAndFailureAudit(t *testing.T) {
	f, a, _ := accountServer(t)
	m := f.Store.(*MySQL)
	if _, err := a.SMTP.Update(f.ctx, f.admin, emailTestUpdate()); err != nil {
		t.Fatal(err)
	}
	id := queueTestEmail(t, a, m, f.customer.ID, "welcome")
	var sent int
	a.SMTP.sender = emailSenderFunc(func(_ context.Context, _ smtp.Config, _ smtp.Message) error {
		sent++
		return errors.New("secret provider response must not enter audit")
	})
	for attempt := 1; attempt <= 5; attempt++ {
		n, failed, err := a.DrainEmailOutbox(f.ctx)
		if err != nil || n != 1 || failed != 1 {
			t.Fatal("drain", n, failed, err)
		}
		var attempts, delay int
		var terminal bool
		var last string
		if err = m.DB.QueryRow("SELECT attempts,TIMESTAMPDIFF(SECOND,UTC_TIMESTAMP(6),next_attempt_at),failed_at IS NOT NULL,last_error FROM email_outbox WHERE id=?", id).Scan(&attempts, &delay, &terminal, &last); err != nil {
			t.Fatal(err)
		}
		if attempts != attempt || terminal != (attempt == 5) || last != "DELIVERY_FAILED" {
			t.Fatal("retry state", attempts, terminal, last)
		}
		if attempt < 5 {
			expected := 60 * (1 << (attempt - 1))
			if delay < expected-5 || delay > expected {
				t.Fatal("exponential backoff", delay, expected)
			}
			n, _, err = a.DrainEmailOutbox(f.ctx)
			if err != nil || n != 0 {
				t.Fatal("retry ignored delay", n, err)
			}
			if _, err = m.DB.Exec("UPDATE email_outbox SET next_attempt_at=UTC_TIMESTAMP(6) WHERE id=?", id); err != nil {
				t.Fatal(err)
			}
		}
	}
	n, _, err := a.DrainEmailOutbox(f.ctx)
	if err != nil || n != 0 || sent != 5 {
		t.Fatal("attempt limit", n, sent, err)
	}
	var count int
	var detail string
	if err = m.DB.QueryRow("SELECT COUNT(*),COALESCE(MAX(detail),'') FROM audit_events WHERE action='email.delivery_failed' AND target=?", id).Scan(&count, &detail); err != nil || count != 1 || strings.Contains(detail, "secret") {
		t.Fatal("failure audit", count, detail, err)
	}
	// A worker crash on the fifth attempt also terminates and audits once.
	crashed := queueTestEmail(t, a, m, f.customer.ID, "welcome")
	if _, err = m.DB.Exec("UPDATE email_outbox SET attempts=5,next_attempt_at=UTC_TIMESTAMP(6) WHERE id=?", crashed); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, _, err = a.DrainEmailOutbox(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='email.delivery_failed' AND target=?", crashed).Scan(&count); err != nil || count != 1 {
		t.Fatal("abandoned lease audit", count, err)
	}
}

func TestMySQLRegistrationReferralAndEncryptedReplay(t *testing.T) {
	f, _, srv := accountServer(t)
	m := f.Store.(*MySQL)
	if _, err := m.DB.Exec("INSERT INTO affiliates(user_id,code,created_at) VALUES(?,'referral-test',UTC_TIMESTAMP(6))", f.customer.ID); err != nil {
		t.Fatal(err)
	}
	c := client(t, srv.URL, "", "")
	input := map[string]any{"email": "referred@example.com", "password": "Sensitive phrase!2026", "name": "Referred", "country": "SG", "locale": "en", "currency": "CNY", "accept_terms": true, "referral_code": "referral-test"}
	data := accountHTTP(t, c, "POST", "/auth/register", "/auth/register", input, "referral-register").(map[string]any)
	id := data["user"].(map[string]any)["id"].(string)
	var referrer string
	if err := m.DB.QueryRow("SELECT affiliate_id FROM affiliate_referrals WHERE client_id=?", id).Scan(&referrer); err != nil || referrer != f.customer.ID {
		t.Fatal("referral attribution", referrer, err)
	}
	var response, fingerprint string
	if err := m.DB.QueryRow("SELECT response,fingerprint FROM account_requests WHERE operation='register'").Scan(&response, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response, id) || strings.Contains(response, input["password"].(string)) {
		t.Fatal("plaintext replay credentials")
	}
	raw, _ := json.Marshal(input)
	if fingerprint == digest(string(raw)) {
		t.Fatal("unkeyed password fingerprint")
	}
}
