package billing

import (
	"aster/billing/internal/financeapi"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func (a *App) accountProfileHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	switch {
	case path == "/account":
		m, err := a.accountDB()
		if err != nil {
			a.accountResult(w, r, nil, err)
			return true
		}
		if r.Method == "GET" {
			v, err := readProfile(r.Context(), m.DB, u.ID)
			a.accountResult(w, r, v, err)
		} else if r.Method == "PATCH" {
			a.updateProfile(w, r, u)
		} else {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
	case path == "/account/password" && r.Method == "POST":
		a.changeAccountPassword(w, r, u)
	case path == "/account/sessions" && r.Method == "GET":
		a.accountSessions(w, r, u)
	case strings.HasPrefix(path, "/account/sessions/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, "/account/sessions/")
		v, err := a.accountTransaction(r, u.ID, "revoke-session", nil, false, func(tx *sql.Tx) (any, error) {
			result, err := tx.ExecContext(r.Context(), `DELETE s FROM sessions s JOIN account_sessions a ON a.token_hash=s.token_hash WHERE a.id=? AND s.user_id=?`, id, u.ID)
			if err != nil {
				return nil, err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return nil, err
			}
			if n != 1 {
				return nil, ErrNotFound
			}
			return okData(), insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account.session_revoked", id)
		})
		a.accountResult(w, r, v, err)
	case path == "/account/login-history" && r.Method == "GET":
		a.accountHistory(w, r, u, false)
	case path == "/account/activity" && r.Method == "GET":
		a.accountHistory(w, r, u, true)
	case path == "/account/notifications":
		a.notificationPreferencesHTTP(w, r, u)
	default:
		return false
	}
	return true
}
func (a *App) updateProfile(w http.ResponseWriter, r *http.Request, u User) {
	var in map[string]json.RawMessage
	if !accountBody(w, r, "updateProfile", &in) {
		return
	}
	var revision int64
	if raw, ok := in["revision"]; !ok || json.Unmarshal(raw, &revision) != nil || revision < 1 || revision >= MaxMinor {
		fail(w, r, 400, "INVALID_BODY", "Revision is required")
		return
	}
	textLimits := map[string]int{"name": 120, "company": 160, "phone": 40, "country": 2, "address_line1": 200, "address_line2": 200, "city": 120, "state": 120, "postal_code": 32, "locale": 5, "currency": 3}
	for k, raw := range in {
		if string(raw) == "null" {
			fail(w, r, 400, "INVALID_BODY", "Null profile fields are not supported")
			return
		}
		if k == "revision" {
			continue
		}
		if k == "marketing_emails" {
			var b bool
			if json.Unmarshal(raw, &b) != nil {
				fail(w, r, 400, "INVALID_BODY", "Invalid preference")
				return
			}
			continue
		}
		limit, ok := textLimits[k]
		var value string
		if !ok || json.Unmarshal(raw, &value) != nil || !accountText(value, limit) || (k == "country" && !countryCodePattern.MatchString(value)) || (k == "locale" && !validLocale(value)) || (k == "currency" && !validCurrencyCode(value)) {
			fail(w, r, 400, "INVALID_BODY", "Invalid profile field")
			return
		}
	}
	v, err := a.accountTransaction(r, u.ID, "profile", in, false, func(tx *sql.Tx) (any, error) {
		// Currency registry precedes the account lock, as in checkout.
		config, _, err := readCurrencies(r.Context(), tx, " FOR SHARE")
		if err != nil {
			return nil, err
		}
		owner, err := lockAccountUser(r.Context(), tx, u.ID)
		if err != nil {
			return nil, err
		}
		if owner.Revision != revision {
			return nil, ErrConflict
		}
		p, err := readProfile(r.Context(), tx, u.ID)
		if err != nil {
			return nil, err
		}
		oldCurrency := p.Currency
		raw, _ := json.Marshal(p)
		var merged map[string]json.RawMessage
		if err = json.Unmarshal(raw, &merged); err != nil {
			return nil, err
		}
		for k, value := range in {
			merged[k] = value
		}
		raw, _ = json.Marshal(merged)
		if err = json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.Currency != oldCurrency {
			if p.CurrencyLocked {
				return nil, problem(409, "CURRENCY_LOCKED", "Currency is locked after the first invoice")
			}
			cur, ok := config.find(p.Currency)
			if !ok || !cur.Enabled {
				return nil, problem(400, "INVALID_CURRENCY", "Select an enabled currency")
			}
		}
		p.Revision++
		if _, err = tx.ExecContext(r.Context(), "UPDATE users SET name=?,currency=?,revision=? WHERE id=?", p.Name, p.Currency, p.Revision, u.ID); err != nil {
			return nil, err
		}
		if err = saveProfileTx(r.Context(), tx, p); err != nil {
			return nil, err
		}
		return p, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account.profile_updated", u.ID)
	})
	a.accountResult(w, r, v, err)
}
func (a *App) changeAccountPassword(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.ChangePasswordInput
	if !accountBody(w, r, "changePassword", &in) {
		return
	}
	security, err := a.securitySettings(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if !validPassword(in.NewPassword, security.PasswordMinLength) {
		fail(w, r, 400, "PASSWORD_TOO_SHORT", "Password does not meet the site policy")
		return
	}
	if !a.allowLogin("ip:" + a.clientIP(r)) {
		fail(w, r, 429, "RATE_LIMITED", "Try again later")
		return
	}
	hash, err := hashPassword(in.NewPassword)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	v, err := a.accountTransaction(r, u.ID, "change-password", nil, false, func(tx *sql.Tx) (any, error) {
		current, err := lockAccountUser(r.Context(), tx, u.ID)
		if err != nil {
			return nil, err
		}
		if !checkPassword(current.PasswordHash, in.CurrentPassword) {
			return nil, problem(400, "INVALID_PASSWORD", "Current password is incorrect")
		}
		if current.Revision >= MaxMinor {
			return nil, ErrConflict
		}
		// Recheck the original session after the user lock, so an in-flight request
		// cannot retain access after another password change or reset commits.
		var valid bool
		if err = tx.QueryRowContext(r.Context(), "SELECT EXISTS(SELECT 1 FROM sessions WHERE token_hash=? AND user_id=? AND expires_at>UTC_TIMESTAMP(6))", digest(a.token(r)), u.ID).Scan(&valid); err != nil {
			return nil, err
		}
		if !valid {
			return nil, problem(401, "AUTH_REQUIRED", "Sign in again")
		}
		if _, err = tx.ExecContext(r.Context(), "UPDATE users SET password_hash=?,revision=revision+1 WHERE id=?", hash, u.ID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=? AND token_hash<>?", u.ID, digest(a.token(r))); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(r.Context(), "DELETE FROM email_tokens WHERE user_id=? AND purpose='password_reset'", u.ID); err != nil {
			return nil, err
		}
		return okData(), insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account.password_changed", u.ID)
	})
	a.accountResult(w, r, v, err)
}
func (a *App) accountSessions(w http.ResponseWriter, r *http.Request, u User) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	rows, err := m.DB.QueryContext(r.Context(), `SELECT a.id,s.token_hash,a.ip,a.user_agent,a.created_at,a.last_seen_at FROM sessions s JOIN account_sessions a ON a.token_hash=s.token_hash WHERE s.user_id=? AND s.expires_at>UTC_TIMESTAMP(6) ORDER BY a.created_at DESC,a.id LIMIT 1000`, u.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []financeapi.AccountSession{}
	for rows.Next() {
		var v financeapi.AccountSession
		var hash string
		var created, seen time.Time
		if err = rows.Scan(&v.ID, &hash, &v.IP, &v.UserAgent, &created, &seen); err != nil {
			a.internal(w, r, err)
			return
		}
		v.Current = hash == digest(a.token(r))
		v.CreatedAt, v.LastSeenAt = stamp(created), stamp(seen)
		out = append(out, v)
	}
	a.accountResult(w, r, out, rows.Err())
}

type accountCursor struct {
	At time.Time `json:"at"`
	ID string    `json:"id"`
}

func (a *App) cursorSignature(r *http.Request, uid, value string) string {
	q := r.URL.Query()
	q.Del("cursor")
	mac := hmac.New(sha256.New, []byte(a.Config.Secret))
	mac.Write([]byte("account-page:" + uid + ":" + r.URL.Path + ":" + q.Encode() + ":" + value))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *App) makeAccountCursor(r *http.Request, uid string, at time.Time, id string) string {
	raw, _ := json.Marshal(accountCursor{at, id})
	value := base64.RawURLEncoding.EncodeToString(raw)
	return value + "." + a.cursorSignature(r, uid, value)
}
func (a *App) readAccountCursor(r *http.Request, uid string) (accountCursor, error) {
	var v accountCursor
	cursor := r.URL.Query().Get("cursor")
	if cursor == "" {
		return v, nil
	}
	parts := strings.Split(cursor, ".")
	if len(cursor) > 512 || len(parts) != 2 || !hmac.Equal([]byte(parts[1]), []byte(a.cursorSignature(r, uid, parts[0]))) {
		return v, problem(400, "INVALID_CURSOR", "Invalid page cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &v) != nil || v.At.IsZero() || !validCancellationID(v.ID) {
		return v, problem(400, "INVALID_CURSOR", "Invalid page cursor")
	}
	return v, nil
}
func (a *App) accountHistory(w http.ResponseWriter, r *http.Request, u User, activity bool) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	limit, err := queryLimit(r)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	cursor, err := a.readAccountCursor(r, u.ID)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	table, columns := "login_events", "id,created_at,ip,user_agent,result"
	if activity {
		table, columns = "audit_events", "id,created_at,action,target,actor_email"
	}
	where := " WHERE user_id=?"
	args := []any{u.ID}
	if !cursor.At.IsZero() {
		where += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(r.Context(), "SELECT "+columns+" FROM "+table+where+" ORDER BY created_at DESC,id DESC LIMIT ?", args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	items := []any{}
	var lastAt time.Time
	var lastID string
	var next *string
	for rows.Next() {
		var id, x, y, z string
		var at time.Time
		if err = rows.Scan(&id, &at, &x, &y, &z); err != nil {
			a.internal(w, r, err)
			return
		}
		if len(items) == limit {
			value := a.makeAccountCursor(r, u.ID, lastAt, lastID)
			next = &value
			break
		}
		if activity {
			actor := "staff"
			if z == u.Email {
				actor = "self"
			} else if z == "system" || strings.HasPrefix(x, "payment.") || strings.HasPrefix(x, "email.") {
				actor = "system"
			}
			subject := y
			items = append(items, financeapi.ActivityEvent{ID: id, At: stamp(at), Action: x, ActorType: actor, Subject: &subject})
		} else {
			items = append(items, financeapi.LoginEvent{ID: id, At: stamp(at), IP: x, UserAgent: y, Result: z})
		}
		lastAt, lastID = at, id
	}
	a.accountResult(w, r, map[string]any{"items": items, "next_cursor": next}, rows.Err())
}
func (a *App) notificationPreferencesHTTP(w http.ResponseWriter, r *http.Request, u User) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	if r.Method == "GET" {
		v, err := readNotificationPreferences(r.Context(), m.DB, u.ID)
		a.accountResult(w, r, v, err)
		return
	}
	if r.Method != "PATCH" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	var in map[string]bool
	if !accountBody(w, r, "updateNotificationPreferences", &in) {
		return
	}
	for k := range in {
		if k != "invoice_created" && k != "invoice_reminder" && k != "service_events" && k != "ticket_replies" && k != "announcements" {
			fail(w, r, 400, "INVALID_BODY", "Unknown preference")
			return
		}
	}
	v, err := a.accountTransaction(r, u.ID, "notifications", in, false, func(tx *sql.Tx) (any, error) {
		if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
			return nil, err
		}
		prefs, err := readNotificationPreferences(r.Context(), tx, u.ID)
		if err != nil {
			return nil, err
		}
		raw, _ := json.Marshal(prefs)
		var values map[string]bool
		json.Unmarshal(raw, &values)
		for k, b := range in {
			values[k] = b
		}
		raw, _ = json.Marshal(values)
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO notification_prefs(user_id,document) VALUES(?,?) ON DUPLICATE KEY UPDATE document=?", u.ID, raw, raw); err != nil {
			return nil, err
		}
		return values, nil
	})
	a.accountResult(w, r, v, err)
}
