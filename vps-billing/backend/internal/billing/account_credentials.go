package billing

import (
	"aster/billing/internal/financeapi"
	"context"
	"crypto/rsa"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type bearerContextKey struct{}

func bearerUser(r *http.Request) (User, bool) {
	u, ok := r.Context().Value(bearerContextKey{}).(User)
	return u, ok
}
func bearerClientPath(path string) bool {
	if path == "/account" || path == "/account/notifications" {
		return true
	}
	root := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
	return slices.Contains([]string{"overview", "products", "cart", "orders", "invoices", "services", "credit", "tickets", "support", "affiliate", "announcements", "currencies", "catalog-taxonomy", "payment-gateways", "site-settings"}, root)
}
func (a *App) bearerRequest(w http.ResponseWriter, r *http.Request, path string) (*http.Request, bool) {
	if !bearerClientPath(path) {
		fail(w, r, 403, "FORBIDDEN", "API tokens cannot access staff or account-security endpoints")
		return r, false
	}
	values := r.Header.Values("Authorization")
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(values) != 1 || len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || !strings.HasPrefix(parts[1], "ast_") {
		fail(w, r, 401, "INVALID_API_TOKEN", "Invalid API token")
		return r, false
	}
	secret := strings.TrimPrefix(parts[1], "ast_")
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(raw) != 32 {
		fail(w, r, 401, "INVALID_API_TOKEN", "Invalid API token")
		return r, false
	}
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return r, false
	}
	var id, uid string
	var scopesRaw, ipsRaw []byte
	hash := digest(parts[1])
	err = m.DB.QueryRowContext(r.Context(), "SELECT id,user_id,scopes,ip_allowlist FROM api_tokens WHERE token_hash=? AND (expires_at IS NULL OR expires_at>UTC_TIMESTAMP(6))", hash).Scan(&id, &uid, &scopesRaw, &ipsRaw)
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, r, 401, "INVALID_API_TOKEN", "Invalid or expired API token")
		return r, false
	}
	if err != nil {
		a.internal(w, r, err)
		return r, false
	}
	var scopes, ips []string
	if json.Unmarshal(scopesRaw, &scopes) != nil || json.Unmarshal(ipsRaw, &ips) != nil {
		a.internal(w, r, errors.New("invalid stored API token policy"))
		return r, false
	}
	scope := "read"
	if r.Method != "GET" {
		scope = "write"
	}
	if !slices.Contains(scopes, scope) {
		fail(w, r, 403, "FORBIDDEN", "API token scope does not allow this operation")
		return r, false
	}
	address, parseErr := netip.ParseAddr(a.clientIP(r))
	allowed := len(ips) == 0
	if parseErr == nil {
		address = address.Unmap()
		for _, value := range ips {
			prefix, e := netip.ParsePrefix(value)
			if e == nil && prefix.Contains(address) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		fail(w, r, 403, "FORBIDDEN", "API token does not allow this IP address")
		return r, false
	}
	u, err := m.UserByID(r.Context(), uid)
	if errors.Is(err, ErrNotFound) || u.Disabled {
		fail(w, r, 401, "INVALID_API_TOKEN", "API token is unavailable")
		return r, false
	}
	if err != nil {
		a.internal(w, r, err)
		return r, false
	}
	result, err := m.DB.ExecContext(r.Context(), "UPDATE api_tokens SET last_used_at=UTC_TIMESTAMP(6) WHERE id=? AND token_hash=? AND (expires_at IS NULL OR expires_at>UTC_TIMESTAMP(6))", id, hash)
	if err != nil {
		a.internal(w, r, err)
		return r, false
	}
	n, err := result.RowsAffected()
	if err != nil {
		a.internal(w, r, err)
		return r, false
	}
	if n != 1 {
		fail(w, r, 401, "INVALID_API_TOKEN", "API token was revoked")
		return r, false
	}
	// Even a staff member's personal token acts only as that member's client.
	u.Role = "customer"
	observeActor(r.Context(), u)
	return r.WithContext(context.WithValue(r.Context(), bearerContextKey{}, u)), true
}

func (a *App) accountCredentialsHTTP(w http.ResponseWriter, r *http.Request, u User, path string) bool {
	keys := path == "/account/ssh-keys" || strings.HasPrefix(path, "/account/ssh-keys/")
	tokens := path == "/account/api-tokens" || strings.HasPrefix(path, "/account/api-tokens/")
	if !keys && !tokens {
		return false
	}
	base := "/account/api-tokens"
	table := "api_tokens"
	if keys {
		base = "/account/ssh-keys"
		table = "ssh_keys"
	}
	switch {
	case path == base && r.Method == "GET":
		a.listAccountCredentials(w, r, u, keys)
	case path == base && r.Method == "POST":
		if keys {
			a.addSSHKey(w, r, u)
		} else {
			a.createAPIToken(w, r, u)
		}
	case strings.HasPrefix(path, base+"/") && r.Method == "DELETE":
		id := strings.TrimPrefix(path, base+"/")
		v, err := a.accountTransaction(r, u.ID, "revoke-"+table, nil, false, func(tx *sql.Tx) (any, error) {
			if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
				return nil, err
			}
			result, err := tx.ExecContext(r.Context(), "DELETE FROM "+table+" WHERE id=? AND user_id=?", id, u.ID)
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
			return okData(), insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account."+table+"_revoked", id)
		})
		a.accountResult(w, r, v, err)
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
	}
	return true
}
func (a *App) listAccountCredentials(w http.ResponseWriter, r *http.Request, u User, keys bool) {
	m, err := a.accountDB()
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	query := "SELECT id,name,prefix,scopes,ip_allowlist,last_used_at,expires_at,created_at FROM api_tokens WHERE user_id=? ORDER BY created_at DESC,id"
	if keys {
		query = "SELECT id,name,fingerprint,algorithm,created_at FROM ssh_keys WHERE user_id=? ORDER BY created_at DESC,id"
	}
	rows, err := m.DB.QueryContext(r.Context(), query, u.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var created time.Time
		if keys {
			var v financeapi.SshKey
			err = rows.Scan(&v.ID, &v.Name, &v.Fingerprint, &v.Algorithm, &created)
			v.CreatedAt = stamp(created)
			out = append(out, v)
		} else {
			var v financeapi.ApiToken
			var scopes, ips []byte
			var last, expiry sql.NullTime
			err = rows.Scan(&v.ID, &v.Name, &v.Prefix, &scopes, &ips, &last, &expiry, &created)
			if err == nil {
				err = json.Unmarshal(scopes, &v.Scopes)
			}
			if err == nil {
				err = json.Unmarshal(ips, &v.IPAllowlist)
			}
			v.CreatedAt = stamp(created)
			if last.Valid {
				s := stamp(last.Time)
				v.LastUsedAt = &s
			}
			if expiry.Valid {
				s := stamp(expiry.Time)
				v.ExpiresAt = &s
			}
			out = append(out, v)
		}
		if err != nil {
			a.internal(w, r, err)
			return
		}
	}
	a.accountResult(w, r, out, rows.Err())
}
func validatedSSHKey(value string) (ssh.PublicKey, error) {
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
	if err != nil || len(options) > 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, problem(400, "INVALID_SSH_KEY", "Provide one supported public SSH key without options")
	}
	switch key.Type() {
	case ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256:
	case ssh.KeyAlgoRSA:
		public, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return nil, problem(400, "INVALID_SSH_KEY", "Invalid RSA key")
		}
		rsaKey, ok := public.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || rsaKey.N.BitLen() < 3072 {
			return nil, problem(400, "INVALID_SSH_KEY", "RSA keys must contain at least 3072 bits")
		}
	default:
		return nil, problem(400, "INVALID_SSH_KEY", "Use ed25519, ecdsa-p256 or RSA with at least 3072 bits")
	}
	return key, nil
}
func (a *App) addSSHKey(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.AddSshKeyInput
	if !accountBody(w, r, "addSshKey", &in) {
		return
	}
	key, err := validatedSSHKey(in.PublicKey)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	v, err := a.accountTransaction(r, u.ID, "ssh-key", in, false, func(tx *sql.Tx) (any, error) {
		if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
			return nil, err
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM ssh_keys WHERE user_id=?", u.ID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 100 {
			return nil, problem(409, "KEY_LIMIT", "Remove an unused SSH key first")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		v := financeapi.SshKey{ID: ID(), Name: in.Name, Fingerprint: ssh.FingerprintSHA256(key), Algorithm: key.Type(), CreatedAt: stamp(now)}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO ssh_keys(id,user_id,name,public_key,algorithm,fingerprint,created_at) VALUES(?,?,?,?,?,?,?)", v.ID, u.ID, v.Name, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), v.Algorithm, v.Fingerprint, now)
		if err != nil {
			return nil, err
		}
		return v, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account.ssh_key_added", v.ID)
	})
	a.accountResult(w, r, v, err)
}
func normalizeTokenIPs(values []string) ([]string, error) {
	out := []string{}
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			address, e := netip.ParseAddr(value)
			if e != nil || address.Zone() != "" {
				return nil, problem(400, "INVALID_IP_ALLOWLIST", "Use IP addresses or CIDR prefixes")
			}
			address = address.Unmap()
			prefix = netip.PrefixFrom(address, address.BitLen())
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, problem(400, "INVALID_IP_ALLOWLIST", "Invalid mapped IPv4 prefix")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
		}
		value = prefix.Masked().String()
		if !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out, nil
}
func (a *App) createAPIToken(w http.ResponseWriter, r *http.Request, u User) {
	var in financeapi.CreateApiTokenInput
	if !accountBody(w, r, "createApiToken", &in) {
		return
	}
	ips, err := normalizeTokenIPs(in.IPAllowlist)
	if err != nil {
		a.accountResult(w, r, nil, err)
		return
	}
	scopes := []string{}
	for _, scope := range in.Scopes {
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	v, err := a.accountTransaction(r, u.ID, "create-api-token", in, true, func(tx *sql.Tx) (any, error) {
		if _, err := lockAccountUser(r.Context(), tx, u.ID); err != nil {
			return nil, err
		}
		var count int
		if err := tx.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM api_tokens WHERE user_id=?", u.ID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 100 {
			return nil, problem(409, "TOKEN_LIMIT", "Remove an unused API token first")
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		secret := "ast_" + randomSecret()
		token := financeapi.ApiToken{ID: ID(), Name: in.Name, Prefix: secret[:12], Scopes: scopes, IPAllowlist: ips, CreatedAt: stamp(now)}
		var expiry any
		if in.ExpiresInDays != nil {
			until := now.Add(time.Duration(*in.ExpiresInDays) * 24 * time.Hour)
			expiry = until
			s := stamp(until)
			token.ExpiresAt = &s
		}
		scopeRaw, _ := json.Marshal(scopes)
		ipsRaw, _ := json.Marshal(ips)
		if _, err := tx.ExecContext(r.Context(), "INSERT INTO api_tokens(id,user_id,token_hash,name,prefix,scopes,ip_allowlist,created_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?)", token.ID, u.ID, digest(secret), token.Name, token.Prefix, scopeRaw, ipsRaw, now, expiry); err != nil {
			return nil, err
		}
		return financeapi.ApiTokenCreated{Token: token, Secret: secret}, insertAccountAudit(r.Context(), tx, u.ID, u.ID, "account.api_token_created", token.ID)
	})
	a.accountResult(w, r, v, err)
}
