package billing

import (
	"aster/billing/internal/commerce"
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

const clientSelect = `SELECT u.id,u.email,u.name,u.currency,u.disabled,u.revision,u.created_at,
 p.document,p.locale,p.email_verified,p.notes,p.closed,p.referrer_id,
 (SELECT COUNT(*) FROM services s WHERE s.user_id=u.id AND s.status<>'terminated'),
 (SELECT MAX(e.created_at) FROM login_events e WHERE e.user_id=u.id AND e.result='success'),
 l.balance_after_minor,l.currency,l.decimal_places,
 (SELECT COALESCE(SUM(IF(i.commerce_version=0,IF(i.status='paid',i.amount_minor,0),i.amount_paid_minor+i.credit_applied_minor-i.refunded_minor)),0) FROM invoices i WHERE i.user_id=u.id AND i.currency=u.currency AND i.kind<>'top_up'),
 (SELECT COALESCE(SUM(i.amount_minor-i.amount_paid_minor-i.credit_applied_minor),0) FROM invoices i WHERE i.user_id=u.id AND i.currency=u.currency AND i.status='unpaid')
 FROM users u LEFT JOIN account_profiles p ON p.user_id=u.id
 LEFT JOIN credit_ledger l ON l.user_id=u.id AND l.sequence=(SELECT MAX(latest_entry.sequence) FROM credit_ledger latest_entry WHERE latest_entry.user_id=u.id)`

func scanAdminClient(row interface{ Scan(...any) error }, currencies CurrencyConfiguration) (financeapi.AdminClient, error) {
	var v financeapi.AdminClient
	var u User
	var raw []byte
	var created time.Time
	var locale, notes, ledgerCurrency sql.NullString
	var verified, closed sql.NullBool
	var last sql.NullTime
	var credit, decimals sql.NullInt64
	var paid, outstanding int64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &v.Currency, &u.Disabled, &u.Revision, &created, &raw, &locale, &verified, &notes, &closed, &v.ReferrerID, &v.Services, &last, &credit, &ledgerCurrency, &decimals, &paid, &outstanding)
	if err != nil {
		return v, err
	}
	p := defaultProfile(u)
	if raw != nil {
		if err = json.Unmarshal(raw, &p); err != nil {
			return v, err
		}
	}
	v.ID, v.Email, v.Name, v.Revision = u.ID, u.Email, u.Name, u.Revision
	v.Company, v.Country, v.Phone, v.Notes = p.Company, p.Country, p.Phone, notes.String
	v.Locale = p.Locale
	if locale.Valid {
		v.Locale = locale.String
	}
	v.EmailVerified = verified.Bool
	parts := []string{}
	for _, part := range []string{p.AddressLine1, p.AddressLine2, p.City, p.State, p.PostalCode} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	v.Address = strings.Join(parts, ", ")
	v.Status = "active"
	if u.Disabled {
		v.Status = "disabled"
	}
	if closed.Bool {
		v.Status = "closed"
	}
	v.CreatedAt = stamp(created)
	if last.Valid {
		s := stamp(last.Time)
		v.LastLoginAt = &s
	}
	currency, ok := currencies.find(v.Currency)
	if !ok {
		return v, ErrCurrency
	}
	precision := int64(currency.DecimalPlaces)
	if credit.Valid {
		if ledgerCurrency.String != v.Currency {
			return v, ErrPaymentState
		}
		precision = decimals.Int64
	}
	for _, n := range []int64{credit.Int64, paid, outstanding} {
		if n < 0 || n > MaxMinor {
			return v, errors.New("client money exceeds contract bounds")
		}
	}
	v.Credit = commerce.Money(v.Currency, precision, credit.Int64)
	v.LifetimePaid = commerce.Money(v.Currency, precision, paid)
	v.Outstanding = commerce.Money(v.Currency, precision, outstanding)
	return v, nil
}
func readAdminClient(ctx context.Context, q accountReader, id string) (financeapi.AdminClient, error) {
	currencies, _, err := readCurrencies(ctx, q, "")
	if err != nil {
		return financeapi.AdminClient{}, err
	}
	return scanAdminClient(q.QueryRowContext(ctx, clientSelect+" WHERE u.id=? AND u.role='customer'", id), currencies)
}
func clientSummary(v financeapi.AdminClient) financeapi.AdminClientSummary {
	return financeapi.AdminClientSummary{ID: v.ID, Email: v.Email, Name: v.Name, Company: v.Company, Country: v.Country, Currency: v.Currency, Status: v.Status, Services: v.Services, Credit: v.Credit, CreatedAt: v.CreatedAt, LastLoginAt: v.LastLoginAt}
}
func lockClient(ctx context.Context, tx *sql.Tx, id string) (User, error) {
	return scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=? AND role='customer' FOR UPDATE", id))
}
func (a *App) clientAdminHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	if path != "/admin/clients" && !strings.HasPrefix(path, "/admin/clients/") {
		return false
	}
	if path == "/admin/clients" {
		if r.Method == "GET" {
			a.listAdminClients(w, r, u)
		} else if r.Method == "POST" {
			a.createAdminClient(w, r, u)
		} else {
			fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		}
		return true
	}
	parts := strings.Split(strings.TrimPrefix(path, "/admin/clients/"), "/")
	id := parts[0]
	if !accountIDPattern.MatchString(id) {
		fail(w, r, 404, "NOT_FOUND", "Client not found")
		return true
	}
	if len(parts) == 2 && parts[1] == "credit" && r.Method == "GET" {
		return false
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return true
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		v, err := readAdminClient(r.Context(), m.DB, id)
		a.accountResult(w, r, v, err)
	case len(parts) == 1 && r.Method == "PATCH":
		a.updateAdminClient(w, r, u, id)
	case len(parts) == 2 && r.Method == "POST" && (parts[1] == "password-reset" || parts[1] == "revoke-sessions"):
		operation := "adminSendPasswordReset"
		if parts[1] == "revoke-sessions" {
			operation = "adminRevokeClientSessions"
		}
		var in struct{}
		if !accountBody(w, r, operation, &in) {
			return true
		}
		v, err := a.accountTransaction(r, u.ID, operation+":"+id, in, false, func(tx *sql.Tx) (any, error) {
			target, err := lockClient(r.Context(), tx, id)
			if err != nil {
				return nil, err
			}
			action := "account.sessions_revoked"
			if parts[1] == "password-reset" {
				if target.Disabled {
					return nil, problem(409, "ACCOUNT_DISABLED", "Enable this account before sending a setup link")
				}
				err = a.issueEmailTokenTx(r.Context(), tx, id, "password_reset")
				action = "account.password_reset_requested"
			} else {
				_, err = tx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=?", id)
			}
			if err != nil {
				return nil, err
			}
			return okData(), insertAccountAudit(r.Context(), tx, id, u.ID, action, id)
		})
		a.accountResult(w, r, v, err)
	case len(parts) == 2 && parts[1] == "credit" && r.Method == "POST":
		var in financeapi.AdminAdjustCreditInput
		if !accountBody(w, r, "adminAdjustCredit", &in) {
			return true
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) < 8 || len(key) > 128 {
			fail(w, r, 400, "IDEMPOTENCY_KEY_REQUIRED", "A valid Idempotency-Key is required")
			return true
		}
		// The commerce ledger owns the money transaction and its idempotency record.
		if _, err = m.AdjustCredit06(r.Context(), u, id, in.AmountMinor, in.Reason, digest(key)); err != nil {
			a.commerceError(w, r, err)
			return true
		}
		v, err := readAdminClient(r.Context(), m.DB, id)
		a.accountResult(w, r, v, err)
	default:
		fail(w, r, 404, "NOT_FOUND", "Client endpoint not found")
	}
	return true
}
func (a *App) listAdminClients(w http.ResponseWriter, r *http.Request, u User) {
	q, err := parseAccountQuery(r, "cursor", "limit", "q", "status")
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	status := q.Get("status")
	if status != "" && !slices.Contains([]string{"active", "disabled", "closed"}, status) || !accountText(q.Get("q"), 120) {
		a.accountResult(w, r, nil, problem(400, "INVALID_QUERY", "Invalid client filter"))
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
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	currencies, _, err := readCurrencies(r.Context(), m.DB, "")
	if err != nil {
		a.internal(w, r, err)
		return
	}
	where := " WHERE u.role='customer'"
	args := []any{}
	switch status {
	case "active":
		where += " AND u.disabled=FALSE AND COALESCE(p.closed,FALSE)=FALSE"
	case "disabled":
		where += " AND u.disabled=TRUE AND COALESCE(p.closed,FALSE)=FALSE"
	case "closed":
		where += " AND p.closed=TRUE"
	}
	if q.Get("q") != "" {
		where += " AND (LOCATE(?,u.email)>0 OR LOCATE(?,u.name)>0 OR LOCATE(?,COALESCE(JSON_UNQUOTE(JSON_EXTRACT(p.document,'$.company')),''))>0)"
		args = append(args, q.Get("q"), q.Get("q"), q.Get("q"))
	}
	if !cursor.At.IsZero() {
		where += " AND (u.created_at<? OR (u.created_at=? AND u.id<?))"
		args = append(args, cursor.At, cursor.At, cursor.ID)
	}
	args = append(args, limit+1)
	rows, err := m.DB.QueryContext(r.Context(), clientSelect+where+" ORDER BY u.created_at DESC,u.id DESC LIMIT ?", args...)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	items := []financeapi.AdminClientSummary{}
	var next *string
	for rows.Next() {
		v, e := scanAdminClient(rows, currencies)
		if e != nil {
			a.internal(w, r, e)
			return
		}
		if len(items) == limit {
			last := items[len(items)-1]
			at, _ := time.Parse(time.RFC3339Nano, last.CreatedAt)
			s := a.makeAccountCursor(r, u.ID, at, last.ID)
			next = &s
			break
		}
		items = append(items, clientSummary(v))
	}
	a.accountResult(w, r, map[string]any{"items": items, "next_cursor": next}, rows.Err())
}
func (a *App) createAdminClient(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.AdminCreateClientInput
	if !accountBody(w, r, "adminCreateClient", &in) {
		return
	}
	in.Email = strings.ToLower(in.Email)
	security, err := a.securitySettings(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	password := in.Password
	if password == "" {
		password = randomSecret()
	} else if !validPassword(password, security.PasswordMinLength) {
		fail(w, r, 400, "PASSWORD_TOO_SHORT", "Password does not meet site policy")
		return
	}
	hash, err := hashPassword(password)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	v, err := a.accountTransaction(r, u.ID, "admin-create-client", in, true, func(tx *sql.Tx) (any, error) {
		config, _, err := readCurrencies(r.Context(), tx, " FOR SHARE")
		if err != nil {
			return nil, err
		}
		currency, ok := config.find(in.Currency)
		if !ok || !currency.Enabled {
			return nil, problem(400, "INVALID_CURRENCY", "Select an enabled currency")
		}
		id := ID()
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO users(id,email,name,role,password_hash,currency) VALUES(?,?,?,'customer',?,?)", id, in.Email, in.Name, hash, in.Currency); err != nil {
			return nil, err
		}
		profile := defaultProfile(User{ID: id, Email: in.Email, Name: in.Name, Revision: 1})
		profile.Country, profile.Company, profile.Currency, profile.Locale = in.Country, in.Company, in.Currency, in.Locale
		if err = saveProfileTx(r.Context(), tx, profile); err != nil {
			return nil, err
		}
		if in.Password == "" {
			if err = a.issueEmailTokenTx(r.Context(), tx, id, "password_reset"); err != nil {
				return nil, err
			}
		}
		if err = a.issueEmailTokenTx(r.Context(), tx, id, "email_verification"); err != nil {
			return nil, err
		}
		if err = a.queueEmailTx(r.Context(), tx, id, "welcome", nil, false); err != nil {
			return nil, err
		}
		if err = insertAccountAudit(r.Context(), tx, id, u.ID, "client.created", id); err != nil {
			return nil, err
		}
		return readAdminClient(r.Context(), tx, id)
	})
	a.accountResult(w, r, v, err)
}
func (a *App) updateAdminClient(w http.ResponseWriter, r *http.Request, u User, id string) {
	var fields map[string]json.RawMessage
	if !accountBody(w, r, "adminUpdateClient", &fields) {
		return
	}
	raw, _ := json.Marshal(fields)
	var in financeapi.AdminUpdateClientInput
	if err := json.Unmarshal(raw, &in); err != nil {
		a.internal(w, r, err)
		return
	}
	for field := range fields {
		if !slices.Contains([]string{"revision", "name", "company", "phone", "country", "status", "notes"}, field) {
			fail(w, r, 400, "INVALID_BODY", "Unknown client field")
			return
		}
	}
	v, err := a.accountTransaction(r, u.ID, "admin-update-client:"+id, in, false, func(tx *sql.Tx) (any, error) {
		target, err := lockClient(r.Context(), tx, id)
		if err != nil {
			return nil, err
		}
		if target.Revision != in.Revision || in.Revision >= MaxMinor {
			return nil, ErrConflict
		}
		p, err := readProfile(r.Context(), tx, id)
		if err != nil {
			return nil, err
		}
		if _, set := fields["name"]; set {
			p.Name = in.Name
		}
		if _, set := fields["company"]; set {
			p.Company = in.Company
		}
		if _, set := fields["phone"]; set {
			p.Phone = in.Phone
		}
		if _, set := fields["country"]; set {
			p.Country = in.Country
		}
		if in.Status != "" {
			target.Disabled = in.Status != "active"
		}
		p.Revision++
		if _, err = tx.ExecContext(r.Context(), "UPDATE users SET name=?,disabled=?,revision=revision+1 WHERE id=?", p.Name, target.Disabled, id); err != nil {
			return nil, err
		}
		if err = saveProfileTx(r.Context(), tx, p); err != nil {
			return nil, err
		}
		if _, set := fields["notes"]; set {
			if _, err = tx.ExecContext(r.Context(), "UPDATE account_profiles SET notes=? WHERE user_id=?", in.Notes, id); err != nil {
				return nil, err
			}
		}
		if in.Status != "" {
			if _, err = tx.ExecContext(r.Context(), "UPDATE account_profiles SET closed=? WHERE user_id=?", in.Status == "closed", id); err != nil {
				return nil, err
			}
			if target.Disabled {
				for _, table := range []string{"sessions", "api_tokens", "email_tokens"} {
					if _, err = tx.ExecContext(r.Context(), "DELETE FROM "+table+" WHERE user_id=?", id); err != nil {
						return nil, err
					}
				}
			}
		}
		if err = insertAccountAudit(r.Context(), tx, id, u.ID, "client.updated", id); err != nil {
			return nil, err
		}
		return readAdminClient(r.Context(), tx, id)
	})
	a.accountResult(w, r, v, err)
}
