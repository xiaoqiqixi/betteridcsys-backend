package billing

import (
	"aster/billing/internal/financeapi"
	smtp "aster/billing/pkg/pluginapi/smtpv1"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"text/template"
	"text/template/parse"
	"time"
)

var emailTemplateKeys = []string{"welcome", "email_verification", "password_reset", "invoice_created", "invoice_reminder", "invoice_overdue", "payment_received", "service_active", "service_suspended", "service_terminated", "ticket_reply", "ticket_opened"}

func securityEmail(key string) bool { return key == "email_verification" || key == "password_reset" }
func emailVariables(key string) []string {
	vars := []string{"site_name", "name", "email", "url"}
	switch {
	case strings.HasPrefix(key, "invoice_") || key == "payment_received":
		vars = append(vars, "invoice_number", "amount", "currency", "due_at")
	case strings.HasPrefix(key, "service_"):
		vars = append(vars, "service_name", "hostname", "reason")
	case strings.HasPrefix(key, "ticket_"):
		vars = append(vars, "ticket_number", "ticket_subject", "body")
	}
	return vars
}
func defaultEmailTemplate(key string) financeapi.EmailTemplate {
	subjects := map[string][2]string{
		"welcome": {"Welcome to {{.site_name}}", "欢迎加入 {{.site_name}}"}, "email_verification": {"Verify your email", "验证您的邮箱"}, "password_reset": {"Set your password", "设置您的密码"},
		"invoice_created": {"Invoice {{.invoice_number}}", "账单 {{.invoice_number}}"}, "invoice_reminder": {"Invoice payment reminder", "账单付款提醒"}, "invoice_overdue": {"Overdue invoice", "账单已逾期"}, "payment_received": {"Payment received", "付款已收到"},
		"service_active": {"Your service is active", "您的服务已开通"}, "service_suspended": {"Your service is suspended", "您的服务已暂停"}, "service_terminated": {"Your service has ended", "您的服务已终止"},
		"ticket_reply": {"Reply to ticket {{.ticket_number}}", "工单 {{.ticket_number}} 有新回复"}, "ticket_opened": {"Ticket {{.ticket_number}} opened", "工单 {{.ticket_number}} 已创建"}}
	subject := subjects[key]
	en, zh := "Hello {{.name}},\n\n{{.url}}\n\n{{.site_name}}", "您好，{{.name}}：\n\n{{.url}}\n\n{{.site_name}}"
	if securityEmail(key) {
		en = "Hello {{.name}},\n\nUse this link within 30 minutes:\n{{.url}}\n\nIf you did not request this email, you can ignore it.\n{{.site_name}}"
		zh = "您好，{{.name}}：\n\n请在 30 分钟内使用此链接：\n{{.url}}\n\n如果您未发起此请求，可忽略此邮件。\n{{.site_name}}"
	}
	return financeapi.EmailTemplate{Key: key, Subject: map[string]string{"en": subject[0], "zh-CN": subject[1]}, Body: map[string]string{"en": en, "zh-CN": zh}, Enabled: true, Variables: emailVariables(key), Revision: 1}
}
func validLocalizedText(v map[string]string, required bool) bool {
	if required && len(v) == 0 {
		return false
	}
	for k, s := range v {
		if !validLocale(k) || !accountText(s, 4000) {
			return false
		}
	}
	return true
}
func parseEmailText(s string, variables []string) (*template.Template, error) {
	t, err := template.New("email").Option("missingkey=error").Parse(s)
	if err != nil {
		return nil, err
	}
	if len(t.Templates()) != 1 {
		return nil, errors.New("template definitions are not allowed")
	}
	for _, node := range t.Tree.Root.Nodes {
		switch n := node.(type) {
		case *parse.TextNode:
		case *parse.ActionNode:
			if len(n.Pipe.Decl) != 0 || len(n.Pipe.Cmds) != 1 || len(n.Pipe.Cmds[0].Args) != 1 {
				return nil, errors.New("only allow-listed variables are allowed")
			}
			f, ok := n.Pipe.Cmds[0].Args[0].(*parse.FieldNode)
			if !ok || len(f.Ident) != 1 || !slices.Contains(variables, f.Ident[0]) {
				return nil, errors.New("unknown template variable")
			}
		default:
			return nil, errors.New("template control flow is not allowed")
		}
	}
	return t, nil
}
func validateEmailTemplate(v financeapi.EmailTemplate) bool {
	if !slices.Contains(emailTemplateKeys, v.Key) || !validLocalizedText(v.Subject, true) || !validLocalizedText(v.Body, true) {
		return false
	}
	for _, texts := range []map[string]string{v.Subject, v.Body} {
		for _, s := range texts {
			if _, err := parseEmailText(s, emailVariables(v.Key)); err != nil {
				return false
			}
		}
	}
	for _, s := range v.Subject {
		if strings.ContainsAny(s, "\r\n") {
			return false
		}
	}
	return true
}
func loadEmailTemplate(ctx context.Context, q accountQueryer, key string) (financeapi.EmailTemplate, error) {
	v := defaultEmailTemplate(key)
	var subject, body []byte
	err := q.QueryRowContext(ctx, "SELECT subject,body,enabled,revision FROM email_templates WHERE template_key=?", key).Scan(&subject, &body, &v.Enabled, &v.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err = json.Unmarshal(subject, &v.Subject); err != nil {
		return v, err
	}
	err = json.Unmarshal(body, &v.Body)
	return v, err
}
func (a *App) queueEmailTx(ctx context.Context, tx *sql.Tx, userID, key string, variables map[string]string, protected bool) error {
	if !slices.Contains(emailTemplateKeys, key) {
		return errors.New("unknown email template")
	}
	if variables == nil {
		variables = map[string]string{}
	}
	id := ID()
	raw, _ := json.Marshal(variables)
	var sealed any
	if protected {
		value, err := a.sealAccount("email:"+id, variables)
		if err != nil {
			return err
		}
		sealed = value
		raw = []byte(`{}`)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO email_outbox(id,user_id,template_key,variables,protected_variables,attempts,next_attempt_at,last_error,created_at) VALUES(?,?,?,?,?,0,UTC_TIMESTAMP(6),'',UTC_TIMESTAMP(6))`, id, userID, key, raw, sealed)
	return err
}
func defaultNotificationPreferences() financeapi.NotificationPreferences {
	return financeapi.NotificationPreferences{InvoiceCreated: true, InvoiceReminder: true, ServiceEvents: true, TicketReplies: true, Announcements: true}
}
func readNotificationPreferences(ctx context.Context, q accountQueryer, id string) (financeapi.NotificationPreferences, error) {
	v := defaultNotificationPreferences()
	var raw []byte
	err := q.QueryRowContext(ctx, "SELECT document FROM notification_prefs WHERE user_id=?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return v, nil
	}
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return v, err
}
func notificationAllowed(p financeapi.NotificationPreferences, key string) bool {
	switch key {
	case "invoice_created":
		return p.InvoiceCreated
	case "invoice_reminder", "invoice_overdue":
		return p.InvoiceReminder
	case "service_active", "service_suspended", "service_terminated":
		return p.ServiceEvents
	case "ticket_reply", "ticket_opened":
		return p.TicketReplies
	}
	return true
}
func localizedEmail(v map[string]string, locale string) string {
	if s, ok := v[locale]; ok {
		return s
	}
	if s, ok := v["en"]; ok {
		return s
	}
	return v["zh-CN"]
}
func renderEmail(t financeapi.EmailTemplate, p financeapi.Profile, site string, variables map[string]string) (smtp.Message, error) {
	data := map[string]string{}
	for _, key := range emailVariables(t.Key) {
		data[key] = ""
	}
	for key, value := range variables {
		if _, ok := data[key]; ok {
			data[key] = value
		}
	}
	data["name"], data["email"], data["site_name"] = p.Name, p.Email, site
	var rendered [2]string
	for i, s := range []string{localizedEmail(t.Subject, p.Locale), localizedEmail(t.Body, p.Locale)} {
		tpl, err := parseEmailText(s, emailVariables(t.Key))
		if err != nil {
			return smtp.Message{}, err
		}
		var out bytes.Buffer
		if err = tpl.Execute(&out, data); err != nil {
			return smtp.Message{}, err
		}
		if out.Len() > 128<<10 {
			return smtp.Message{}, errors.New("rendered email too large")
		}
		rendered[i] = out.String()
	}
	if strings.ContainsAny(rendered[0], "\r\n") {
		return smtp.Message{}, errors.New("invalid email subject")
	}
	return smtp.Message{To: p.Email, Subject: rendered[0], Text: rendered[1]}, nil
}

type queuedEmail struct {
	id, user, key string
	variables     []byte
	protected     sql.NullString
	attempt       int
}

func (a *App) deliverQueuedEmail(ctx context.Context, m *MySQL, item queuedEmail) error {
	p, err := readProfile(ctx, m.DB, item.user)
	if err != nil {
		return err
	}
	t, err := loadEmailTemplate(ctx, m.DB, item.key)
	if err != nil {
		return err
	}
	prefs, err := readNotificationPreferences(ctx, m.DB, item.user)
	if err != nil {
		return err
	}
	if !securityEmail(item.key) && (!t.Enabled || !notificationAllowed(prefs, item.key)) {
		return nil
	}
	var vars map[string]string
	if err = json.Unmarshal(item.variables, &vars); err != nil {
		return err
	}
	if item.protected.Valid {
		if err = a.openAccount("email:"+item.id, item.protected.String, &vars); err != nil {
			return err
		}
	}
	site, err := a.readSiteSettings6(ctx, m.DB)
	if err != nil {
		return err
	}
	msg, err := renderEmail(t, p, site.SiteName, vars)
	if err != nil {
		return err
	}
	settings, err := a.Store.EmailSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled {
		return ErrSMTPDisabled
	}
	credentials, err := a.SMTP.credentials(settings)
	if err != nil {
		return err
	}
	return a.SMTP.sender.Send(ctx, settings.config(credentials), msg)
}

// DrainEmailOutbox reserves a bounded batch with durable leases. Attempts are
// recorded before SMTP, so a crashed worker cannot reset the retry budget.
func (a *App) DrainEmailOutbox(ctx context.Context) (processed, failed int, err error) {
	m, e := a.accountDB()
	if e != nil {
		return 0, 0, e
	}
	settings, e := a.Store.EmailSettings(ctx)
	if e != nil {
		return 0, 0, e
	}
	if !settings.Enabled {
		return 0, 0, nil
	}
	// Finalize a last attempt whose worker died before recording its outcome.
	rows, e := m.DB.QueryContext(ctx, "SELECT id,user_id FROM email_outbox WHERE attempts>=5 AND sent_at IS NULL AND failed_at IS NULL AND next_attempt_at<=UTC_TIMESTAMP(6) LIMIT 50")
	if e != nil {
		return 0, 0, e
	}
	var abandoned [][2]string
	for rows.Next() {
		var row [2]string
		if e = rows.Scan(&row[0], &row[1]); e != nil {
			rows.Close()
			return 0, 0, e
		}
		abandoned = append(abandoned, row)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return 0, 0, e
	}
	for _, row := range abandoned {
		if e = a.finishQueuedEmail(ctx, m, queuedEmail{id: row[0], user: row[1], attempt: 5}, errors.New("delivery lease expired")); e != nil {
			return processed, failed, e
		}
		failed++
	}
	for range 50 {
		tx, e := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if e != nil {
			return processed, failed, e
		}
		var item queuedEmail
		e = tx.QueryRowContext(ctx, `SELECT id,user_id,template_key,variables,protected_variables,attempts FROM email_outbox WHERE sent_at IS NULL AND failed_at IS NULL AND attempts<5 AND next_attempt_at<=UTC_TIMESTAMP(6) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&item.id, &item.user, &item.key, &item.variables, &item.protected, &item.attempt)
		if errors.Is(e, sql.ErrNoRows) {
			tx.Rollback()
			return processed, failed, nil
		}
		if e != nil {
			tx.Rollback()
			return processed, failed, e
		}
		item.attempt++
		_, e = tx.ExecContext(ctx, "UPDATE email_outbox SET attempts=?,next_attempt_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 5 MINUTE) WHERE id=?", item.attempt, item.id)
		if e != nil {
			tx.Rollback()
			return processed, failed, e
		}
		if e = tx.Commit(); e != nil {
			return processed, failed, e
		}
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		deliveryErr := a.deliverQueuedEmail(sendCtx, m, item)
		cancel()
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		e = a.finishQueuedEmail(finishCtx, m, item, deliveryErr)
		finishCancel()
		if e != nil {
			return processed, failed, e
		}
		processed++
		if deliveryErr != nil {
			failed++
		}
	}
	return processed, failed, nil
}
func (a *App) finishQueuedEmail(ctx context.Context, m *MySQL, item queuedEmail, deliveryErr error) error {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var result sql.Result
	if deliveryErr == nil {
		result, err = tx.ExecContext(ctx, "UPDATE email_outbox SET sent_at=UTC_TIMESTAMP(6),protected_variables=NULL,last_error='' WHERE id=? AND attempts=? AND sent_at IS NULL AND failed_at IS NULL", item.id, item.attempt)
	} else if item.attempt < 5 {
		result, err = tx.ExecContext(ctx, "UPDATE email_outbox SET next_attempt_at=DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? SECOND),last_error='DELIVERY_FAILED' WHERE id=? AND attempts=? AND sent_at IS NULL AND failed_at IS NULL", 60*(1<<(item.attempt-1)), item.id, item.attempt)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE email_outbox SET failed_at=UTC_TIMESTAMP(6),protected_variables=NULL,last_error='DELIVERY_FAILED' WHERE id=? AND attempts=? AND sent_at IS NULL AND failed_at IS NULL", item.id, item.attempt)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 && deliveryErr != nil && item.attempt >= 5 {
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,'email.delivery_failed',?,UTC_TIMESTAMP(6),'system','Delivery failed after five attempts')`, ID(), item.user, item.id)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (a *App) RunAccountJobs(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			jobCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
			_, _, err := a.DrainEmailOutbox(jobCtx)
			cancel()
			if err != nil {
				a.Config.Logger.Error("email outbox job failed", "event", "email.outbox_failed")
			}
		}
	}
}

func (a *App) emailTemplatesHTTP(w http.ResponseWriter, r *http.Request, u User, path string) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	if path == "/admin/email-templates" && r.Method == "GET" {
		out := []financeapi.EmailTemplate{}
		for _, key := range emailTemplateKeys {
			v, e := loadEmailTemplate(r.Context(), m.DB, key)
			if e != nil {
				a.internal(w, r, e)
				return
			}
			out = append(out, v)
		}
		jsonOut(w, 200, out)
		return
	}
	key := strings.TrimPrefix(path, "/admin/email-templates/")
	if r.Method != "PATCH" || !slices.Contains(emailTemplateKeys, key) {
		fail(w, r, 404, "NOT_FOUND", "Template not found")
		return
	}
	var in financeapi.AdminUpdateEmailTemplateInput
	if !accountBody(w, r, "adminUpdateEmailTemplate", &in) {
		return
	}
	next := financeapi.EmailTemplate{Key: key, Subject: in.Subject, Body: in.Body, Enabled: in.Enabled, Variables: emailVariables(key), Revision: in.Revision}
	if next.Revision < 1 || next.Revision >= MaxMinor || !validateEmailTemplate(next) {
		fail(w, r, 400, "INVALID_TEMPLATE", "Use only the listed template variables")
		return
	}
	v, err := a.accountTransaction(r, u.ID, "email-template", in, false, func(tx *sql.Tx) (any, error) {
		def := defaultEmailTemplate(key)
		subject, _ := json.Marshal(def.Subject)
		body, _ := json.Marshal(def.Body)
		if _, err := tx.ExecContext(r.Context(), "INSERT IGNORE INTO email_templates(template_key,subject,body,enabled,revision) VALUES(?,?,?,TRUE,1)", key, subject, body); err != nil {
			return nil, err
		}
		var revision int64
		if err := tx.QueryRowContext(r.Context(), "SELECT revision FROM email_templates WHERE template_key=? FOR UPDATE", key).Scan(&revision); err != nil {
			return nil, err
		}
		if revision != in.Revision {
			return nil, ErrConflict
		}
		subject, _ = json.Marshal(next.Subject)
		body, _ = json.Marshal(next.Body)
		next.Revision++
		if _, err := tx.ExecContext(r.Context(), "UPDATE email_templates SET subject=?,body=?,enabled=?,revision=? WHERE template_key=?", subject, body, next.Enabled, next.Revision, key); err != nil {
			return nil, err
		}
		return next, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "email.template_updated", key)
	})
	a.accountResult(w, r, v, err)
}
