package billing

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type affiliateAttribution struct {
	Code    string
	Expires int64
}

func affiliateIPPrefix(ip string) string {
	address, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	address = address.Unmap()
	bits := 64
	if address.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(address, bits).Masked().String()
}
func selfReferral(email, referrerEmail, prefix, referrerPrefix string) bool {
	return strings.EqualFold(strings.TrimSpace(email), strings.TrimSpace(referrerEmail)) || (prefix != "" && prefix == referrerPrefix)
}
func (a *App) captureAffiliate(w http.ResponseWriter, r *http.Request) error {
	code := r.URL.Query().Get("referral_code")
	if code == "" {
		return nil
	}
	if len(code) > 64 {
		return ErrCommerceInput
	}
	m, err := a.accountDB()
	if err != nil {
		return err
	}
	var id string
	err = m.DB.QueryRowContext(r.Context(), "SELECT user_id FROM affiliates WHERE code=?", code).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	s, err := readAffiliateSettings(r.Context(), m.DB, "")
	if err != nil {
		return err
	}
	lifetime := time.Duration(s.AttributionDays) * 24 * time.Hour
	expires := time.Now().Add(lifetime)
	value, err := a.sealAccount("affiliate-attribution", affiliateAttribution{Code: code, Expires: expires.Unix()})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: "aster_affiliate", Value: value, Path: "/", HttpOnly: true, Secure: a.Config.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: int(lifetime.Seconds()), Expires: expires})
	return nil
}
func (a *App) affiliateRegistrationCode(r *http.Request, input string) string {
	cookie, err := r.Cookie("aster_affiliate")
	if err != nil {
		return input
	}
	var claim affiliateAttribution
	if a.openAccount("affiliate-attribution", cookie.Value, &claim) != nil || claim.Expires <= time.Now().Unix() {
		return ""
	}
	return claim.Code
}
func attributeAffiliateTx(ctx context.Context, tx *sql.Tx, id, email, ip, code string) error {
	prefix := affiliateIPPrefix(ip)
	if _, err := tx.ExecContext(ctx, "INSERT INTO affiliate_registration_ips(user_id,ip_prefix) VALUES(?,?)", id, prefix); err != nil {
		return err
	}
	if code == "" {
		return nil
	}
	var referrer, refEmail, refPrefix string
	err := tx.QueryRowContext(ctx, `SELECT a.user_id,u.email,COALESCE(ip.ip_prefix,'') FROM affiliates a JOIN users u ON u.id=a.user_id LEFT JOIN affiliate_registration_ips ip ON ip.user_id=a.user_id WHERE a.code=?`, code).Scan(&referrer, &refEmail, &refPrefix)
	if errors.Is(err, sql.ErrNoRows) {
		return problem(400, "INVALID_REFERRAL", "Referral code not found")
	}
	if err != nil {
		return err
	}
	// Older accounts have no registration snapshot; use their oldest known session.
	if refPrefix == "" {
		var oldIP string
		err = tx.QueryRowContext(ctx, "SELECT ip FROM account_sessions WHERE token_hash IN (SELECT token_hash FROM sessions WHERE user_id=?) ORDER BY created_at,id LIMIT 1", referrer).Scan(&oldIP)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		refPrefix = affiliateIPPrefix(oldIP)
	}
	s, err := readAffiliateSettings(ctx, tx, "")
	if err != nil {
		return err
	}
	if !s.AllowSelfReferral && selfReferral(email, refEmail, prefix, refPrefix) {
		return nil
	}
	// Attribution is written once, in the registration transaction. There is no
	// account/profile update endpoint that can change this relationship later.
	if _, err = tx.ExecContext(ctx, "UPDATE account_profiles SET referrer_id=? WHERE user_id=?", referrer, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO affiliate_referrals(client_id,affiliate_id,created_at,converted) VALUES(?,?,UTC_TIMESTAMP(6),FALSE)", id, referrer); err != nil {
		return err
	}
	return insertAccountAudit(ctx, tx, referrer, id, "affiliate.attributed", id)
}
