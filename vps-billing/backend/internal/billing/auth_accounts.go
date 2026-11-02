package billing

import (
	"aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func (a *App) clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	ip = parsed.String()
	for _, cidr := range a.Config.TrustedProxyCIDRs {
		_, network, e := net.ParseCIDR(cidr)
		if e == nil && network.Contains(parsed) {
			forwarded := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP")))
			if forwarded != nil {
				ip = forwarded.String()
			}
			break
		}
	}
	return ip
}
func (a *App) recordLogin(r *http.Request, u User, email string, success bool) error {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return nil
	}
	var owner any
	if u.ID != "" {
		owner = u.ID
	}
	result := "failed"
	if success {
		result = "success"
	}
	_, err := m.DB.ExecContext(r.Context(), "INSERT INTO login_events(id,user_id,email_hash,ip,user_agent,result,created_at) VALUES(?,?,?,?,?,?,UTC_TIMESTAMP(6))", ID(), owner, digest(email), a.clientIP(r), shortUserAgent(r), result)
	if err == nil && success && u.Role == "admin" {
		_, err = m.DB.ExecContext(r.Context(), "UPDATE staff_members SET last_login_at=UTC_TIMESTAMP(6) WHERE user_id=?", u.ID)
	}
	return err
}
func (a *App) authAccountsHTTP(w http.ResponseWriter, r *http.Request, path string) bool {
	if !strings.HasPrefix(path, "/auth/") || path == "/auth/login" || path == "/auth/logout" {
		return false
	}
	if r.Method != "POST" {
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return true
	}
	switch path {
	case "/auth/register":
		a.registerAccount(w, r)
	case "/auth/password/forgot":
		a.forgotPassword(w, r)
	case "/auth/password/reset":
		a.consumeEmailToken(w, r, "password_reset")
	case "/auth/email/verify":
		a.consumeEmailToken(w, r, "email_verification")
	case "/auth/email/resend":
		u, err := a.current(r)
		if err != nil {
			fail(w, r, 401, "AUTH_REQUIRED", "Sign in first")
			return true
		}
		var in struct{}
		if !accountBody(w, r, "resendVerification", &in) {
			return true
		}
		if !a.allowLogin("ip:" + a.clientIP(r)) {
			fail(w, r, 429, "RATE_LIMITED", "Try again later")
			return true
		}
		v, err := a.accountTransaction(r, u.ID, path, in, false, func(tx *sql.Tx) (any, error) {
			if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
				return nil, err
			}
			p, err := readProfile(r.Context(), tx, u.ID)
			if err != nil {
				return nil, err
			}
			if !p.EmailVerified {
				err = a.issueEmailTokenTx(r.Context(), tx, u.ID, "email_verification")
			}
			return okData(), err
		})
		a.accountResult(w, r, v, err)
	default:
		fail(w, r, 404, "NOT_FOUND", "Endpoint not found")
	}
	return true
}
func (a *App) issueEmailTokenTx(ctx context.Context, tx *sql.Tx, userID, purpose string) error {
	// Callers lock the user first. Reissuing invalidates all previous links.
	if _, err := tx.ExecContext(ctx, "DELETE FROM email_tokens WHERE user_id=? AND purpose=?", userID, purpose); err != nil {
		return err
	}
	token := randomSecret()
	if _, err := tx.ExecContext(ctx, `INSERT INTO email_tokens(token_hash,user_id,purpose,expires_at,created_at) VALUES(?,?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL 30 MINUTE),UTC_TIMESTAMP(6))`, digest(token), userID, purpose); err != nil {
		return err
	}
	path := "/reset-password"
	if purpose == "email_verification" {
		path = "/verify-email"
	}
	return a.queueEmailTx(ctx, tx, userID, purpose, map[string]string{"url": a.Config.Origin + path + "?token=" + url.QueryEscape(token)}, true)
}
func (a *App) forgotPassword(w http.ResponseWriter, r *http.Request) {
	var in financeapi.ForgotPasswordInput
	if !accountBody(w, r, "forgotPassword", &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	// The response is independent of account existence, disabled state and rate
	// exhaustion; only source-IP limits govern whether another email is queued.
	if !validAccountEmail(in.Email) || !a.allowLogin("ip:"+a.clientIP(r)) {
		jsonOut(w, 200, okData())
		return
	}
	_, err := a.accountTransaction(r, "anonymous", "forgot", in, false, func(tx *sql.Tx) (any, error) {
		var id string
		err := tx.QueryRowContext(r.Context(), "SELECT id FROM users WHERE email=? AND disabled=FALSE FOR UPDATE", in.Email).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return okData(), nil
		}
		if err != nil {
			return nil, err
		}
		return okData(), a.issueEmailTokenTx(r.Context(), tx, id, "password_reset")
	})
	if err != nil {
		a.Config.Logger.Error("password recovery queue failed", "event", "auth.recovery_failed")
	}
	jsonOut(w, 200, okData())
}
func (a *App) consumeEmailToken(w http.ResponseWriter, r *http.Request, purpose string) {
	var in financeapi.ResetPasswordInput
	if purpose == "email_verification" {
		var x financeapi.VerifyEmailInput
		if !accountBody(w, r, "verifyEmail", &x) {
			return
		}
		in.Token = x.Token
	} else {
		if !accountBody(w, r, "resetPassword", &in) {
			return
		}
	}
	if len(in.Token) < 16 || len(in.Token) > 256 {
		fail(w, r, 400, "INVALID_TOKEN", "This link is invalid or has expired")
		return
	}
	if !a.allowLogin("ip:" + a.clientIP(r)) {
		fail(w, r, 429, "RATE_LIMITED", "Try again later")
		return
	}
	hash := ""
	if purpose == "password_reset" {
		security, err := a.securitySettings(r.Context())
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if !validPassword(in.Password, security.PasswordMinLength) {
			fail(w, r, 400, "PASSWORD_TOO_SHORT", "Password does not meet the site policy")
			return
		}
		hash, err = hashPassword(in.Password)
		if err != nil {
			a.internal(w, r, err)
			return
		}
	}
	v, err := a.accountTransaction(r, "anonymous", purpose, in, false, func(tx *sql.Tx) (any, error) {
		var id string
		err := tx.QueryRowContext(r.Context(), "SELECT user_id FROM email_tokens WHERE token_hash=? AND purpose=?", digest(in.Token), purpose).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, problem(400, "INVALID_TOKEN", "This link is invalid or has expired")
		}
		if err != nil {
			return nil, err
		}
		if _, err = lockAccountUser(r.Context(), tx, id); err != nil {
			return nil, err
		}
		result, err := tx.ExecContext(r.Context(), "DELETE FROM email_tokens WHERE token_hash=? AND purpose=? AND expires_at>UTC_TIMESTAMP(6)", digest(in.Token), purpose)
		if err != nil {
			return nil, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if n != 1 {
			return nil, problem(400, "INVALID_TOKEN", "This link is invalid or has expired")
		}
		if purpose == "password_reset" {
			if _, err = tx.ExecContext(r.Context(), "UPDATE users SET password_hash=?,revision=revision+1 WHERE id=?", hash, id); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(r.Context(), "DELETE FROM sessions WHERE user_id=?", id); err != nil {
				return nil, err
			}
		} else {
			p, err := readProfile(r.Context(), tx, id)
			if err != nil {
				return nil, err
			}
			p.EmailVerified = true
			if err = saveProfileTx(r.Context(), tx, p); err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(r.Context(), "UPDATE users SET revision=revision+1 WHERE id=?", id); err != nil {
				return nil, err
			}
		}
		if err = insertAccountAudit(r.Context(), tx, id, id, "account."+purpose, id); err != nil {
			return nil, err
		}
		return okData(), nil
	})
	a.accountResult(w, r, v, err)
}

func (a *App) registerAccount(w http.ResponseWriter, r *http.Request) {
	var in financeapi.RegisterInput
	if !accountBody(w, r, "register", &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !a.allowLogin("ip:" + a.clientIP(r)) {
		fail(w, r, 429, "RATE_LIMITED", "Try again later")
		return
	}
	if !validAccountEmail(in.Email) || strings.TrimSpace(in.Name) == "" || !accountText(in.Name, 120) || !countryCodePattern.MatchString(in.Country) || !validLocale(in.Locale) || !in.AcceptTerms || len(in.ReferralCode) > 64 {
		fail(w, r, 400, "INVALID_BODY", "Invalid registration details")
		return
	}
	security, err := a.securitySettings(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if !validPassword(in.Password, security.PasswordMinLength) {
		fail(w, r, 400, "PASSWORD_TOO_SHORT", "Password does not meet the site policy")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	// Bind registration replay to the server-issued anonymous cookie. Reuse it
	// for this new session; password login always rotates its session token.
	token := a.token(r)
	v, err := a.accountTransaction(r, "registration:"+digest(token), "register", in, true, func(tx *sql.Tx) (any, error) {
		site, err := a.readSiteSettings6(r.Context(), tx)
		if err != nil {
			return nil, err
		}
		if !site.RegistrationOpen {
			return nil, problem(403, "REGISTRATION_CLOSED", "Registration is closed")
		}
		config, _, err := readCurrencies(r.Context(), tx, " FOR SHARE")
		if err != nil {
			return nil, err
		}
		cur, ok := config.find(in.Currency)
		if !ok || !cur.Enabled {
			return nil, problem(400, "INVALID_CURRENCY", "Select an enabled currency")
		}
		id := ID()
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO users(id,email,name,role,password_hash,currency) VALUES(?,?,?,'customer',?,?)", id, in.Email, in.Name, hash, in.Currency); err != nil {
			return nil, err
		}
		u := User{ID: id, Email: in.Email, Name: in.Name, Role: "customer", Revision: 1}
		p := defaultProfile(u)
		p.Country, p.Locale, p.Currency = in.Country, in.Locale, in.Currency
		if err = saveProfileTx(r.Context(), tx, p); err != nil {
			return nil, err
		}
		if err = attributeAffiliateTx(r.Context(), tx, id, in.Email, a.clientIP(r), a.affiliateRegistrationCode(r, in.ReferralCode)); err != nil {
			return nil, err
		}

		if _, err = tx.ExecContext(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,DATE_ADD(UTC_TIMESTAMP(6),INTERVAL ? HOUR))", digest(token), id, security.ClientSessionHours); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(r.Context(), "INSERT INTO account_sessions(id,token_hash,ip,user_agent,created_at,last_seen_at) VALUES(?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", ID(), digest(token), a.clientIP(r), shortUserAgent(r)); err != nil {
			return nil, err
		}
		if err = a.issueEmailTokenTx(r.Context(), tx, id, "email_verification"); err != nil {
			return nil, err
		}
		if err = a.queueEmailTx(r.Context(), tx, id, "welcome", nil, false); err != nil {
			return nil, err
		}
		if err = insertAccountAudit(r.Context(), tx, id, id, "account.registered", id); err != nil {
			return nil, err
		}
		return financeapi.Session{User: &financeapi.SessionUser{ID: id, Email: in.Email, Name: in.Name, Role: "customer", Locale: in.Locale, Currency: in.Currency}, CSRFToken: a.csrf(token), DemoMode: a.Config.Demo}, nil
	})
	if err == nil {
		a.setSessionCookie(w, token, time.Duration(security.ClientSessionHours)*time.Hour, true)
	}
	a.accountResult(w, r, v, err)
}
func (a *App) setSessionCookie(w http.ResponseWriter, token string, lifetime time.Duration, remember bool) {
	c := &http.Cookie{Name: a.cookieName(), Value: token, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookie, SameSite: http.SameSiteStrictMode}
	if remember {
		c.MaxAge = int(lifetime.Seconds())
	}
	http.SetCookie(w, c)
}

func (a *App) inviteStaff(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.AdminInviteStaffInput
	if !accountBody(w, r, "adminInviteStaff", &in) {
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	if !validAccountEmail(in.Email) || strings.TrimSpace(in.Name) == "" || !accountText(in.Name, 120) || len(in.RoleID) > 64 || in.RoleID == "" {
		fail(w, r, 400, "INVALID_BODY", "Invalid invitation")
		return
	}
	// An unguessable, undisclosed password prevents login until the setup link is
	// redeemed through the same single-use password-reset path.
	hash, err := hashPassword(randomSecret())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	v, err := a.accountTransaction(r, u.ID, "invite-staff", in, true, func(tx *sql.Tx) (any, error) {
		if err := staffWriteTx(r.Context(), tx, u.ID); err != nil {
			return nil, err
		}
		var role string
		if err := tx.QueryRowContext(r.Context(), "SELECT id FROM staff_roles WHERE id=?", in.RoleID).Scan(&role); err != nil {
			return nil, err
		}
		id := ID()
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO users(id,email,name,role,password_hash) VALUES(?,?,?,'admin',?)", id, in.Email, in.Name, hash); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO staff_members(user_id,role_id) VALUES(?,?)", id, role); err != nil {
			return nil, err
		}
		if err := a.issueEmailTokenTx(r.Context(), tx, id, "password_reset"); err != nil {
			return nil, err
		}
		if err := staffAuditTx(r.Context(), tx, u.ID, "staff.invited", id); err != nil {
			return nil, err
		}
		return StaffMember{ID: id, Email: in.Email, Name: in.Name, RoleID: role, Status: "active", Revision: 1}, nil
	})
	a.accountResult(w, r, v, err)
}

func (a *App) readSiteSettings6(ctx context.Context, q accountQueryer) (financeapi.SiteSettings, error) {
	var raw []byte
	var v financeapi.SiteSettings
	err := q.QueryRowContext(ctx, "SELECT document FROM account_settings WHERE name='site'").Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
		return v, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return v, err
	}
	var old SiteSettings
	if err = q.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&raw); err != nil {
		return v, err
	}
	if err = json.Unmarshal(raw, &old); err != nil {
		return v, err
	}
	v = siteSettingsFromLegacy(old)
	return v, nil
}

func (a *App) orderEmailVerified(w http.ResponseWriter, r *http.Request, u User) bool {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return true
	} // The legacy in-memory demo has no registration policy.
	settings, err := a.readSiteSettings6(r.Context(), m.DB)
	if err != nil {
		a.internal(w, r, err)
		return false
	}
	if !settings.EmailVerificationRequired {
		return true
	}
	profile, err := readProfile(r.Context(), m.DB, u.ID)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return false
	}
	if !profile.EmailVerified {
		fail(w, r, 403, "EMAIL_VERIFICATION_REQUIRED", "Verify your email address before placing an order")
		return false
	}
	return true
}
