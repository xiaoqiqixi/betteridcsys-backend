package billing

import (
	"aster/billing/internal/financeapi"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

type apiProblem struct {
	status        int
	code, message string
}

func (e *apiProblem) Error() string                  { return e.code }
func problem(status int, code, message string) error { return &apiProblem{status, code, message} }
func (a *App) accountResult(w http.ResponseWriter, r *http.Request, v any, err error) {
	if err == nil {
		jsonOut(w, 200, v)
		return
	}
	var p *apiProblem
	var me *mysql.MySQLError
	switch {
	case errors.As(err, &p):
		fail(w, r, p.status, p.code, p.message)
	case errors.Is(err, ErrNotFound), errors.Is(err, sql.ErrNoRows):
		fail(w, r, 404, "NOT_FOUND", "Record not found")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "Permission denied")
	case errors.Is(err, ErrConflict):
		fail(w, r, 409, "REVISION_CONFLICT", "Record changed; reload and retry")
	case errors.As(err, &me) && me.Number == 1062:
		fail(w, r, 409, "ALREADY_EXISTS", "Record already exists")
	default:
		a.internal(w, r, err)
	}
}
func okData() financeapi.Ok    { return financeapi.Ok{Ok: true} }
func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func (a *App) accountDB() (*MySQL, error) {
	m, ok := a.Store.(*MySQL)
	if !ok {
		return nil, problem(503, "FEATURE_UNAVAILABLE", "This operation requires persistent storage")
	}
	return m, nil
}

type accountQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSecurity(ctx context.Context, q accountQueryer) (financeapi.SecuritySettings, error) {
	var raw []byte
	var v financeapi.SecuritySettings
	err := q.QueryRowContext(ctx, "SELECT document FROM account_settings WHERE name='security'").Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return v, err
}
func (a *App) securitySettings(ctx context.Context) (financeapi.SecuritySettings, error) {
	if m, ok := a.Store.(*MySQL); ok {
		return readSecurity(ctx, m.DB)
	}
	return financeapi.SecuritySettings{ClientSessionHours: 8, StaffSessionHours: 8, PasswordMinLength: 10, Revision: 1}, nil
}

func hashPassword(password string) (string, error) {
	sum := sha256.Sum256([]byte(password))
	raw, err := bcrypt.GenerateFromPassword([]byte(base64.RawStdEncoding.EncodeToString(sum[:])), bcrypt.DefaultCost)
	return "bcrypt-sha256:" + string(raw), err
}
func checkPassword(hash, password string) bool {
	if strings.HasPrefix(hash, "bcrypt-sha256:") {
		sum := sha256.Sum256([]byte(password))
		password = base64.RawStdEncoding.EncodeToString(sum[:])
		hash = strings.TrimPrefix(hash, "bcrypt-sha256:")
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
func validPassword(s string, min int64) bool {
	return utf8.ValidString(s) && int64(utf8.RuneCountInString(s)) >= min && utf8.RuneCountInString(s) <= 256 && len(s) <= 1024
}

var accountEmailPattern = regexp.MustCompile("^(?:[A-Za-z0-9_'+\\-]+\\.)*[A-Za-z0-9_'+\\-]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9\\-]*\\.)+[A-Za-z]{2,}$")
var accountIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var countryCodePattern = regexp.MustCompile(`^[A-Z]{2}$`)

func validAccountEmail(s string) bool { return len(s) <= 254 && accountEmailPattern.MatchString(s) }
func validLocale(s string) bool       { return s == "en" || s == "zh-CN" }
func accountText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && !strings.ContainsRune(s, 0)
}
func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// Replay payloads and security-email variables are encrypted. Token tables only
// hold hashes, and plaintext credentials never enter audit or error messages.
func (a *App) accountCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("aster:accounts:v1:" + a.Config.Secret))
	b, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}
func (a *App) sealAccount(purpose string, v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	c, err := a.accountCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(c.Seal(nonce, nonce, raw, []byte(purpose))), nil
}
func (a *App) openAccount(purpose, value string, v any) error {
	raw, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return err
	}
	c, err := a.accountCipher()
	if err != nil {
		return err
	}
	if len(raw) < c.NonceSize() {
		return errors.New("invalid protected record")
	}
	plain, err := c.Open(nil, raw[:c.NonceSize()], raw[c.NonceSize():], []byte(purpose))
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// accountTransaction commits the mutation and its replay result together. Each
// principal/key is serialized by the unique key; errors roll back reservations.
func (a *App) accountTransaction(r *http.Request, principal, operation string, input any, idempotent bool, fn func(*sql.Tx) (any, error)) (any, error) {
	m, err := a.accountDB()
	if err != nil {
		return nil, err
	}
	key := r.Header.Get("Idempotency-Key")
	if idempotent && (len(key) < 8 || len(key) > 128) {
		return nil, problem(400, "IDEMPOTENCY_KEY_REQUIRED", "A valid Idempotency-Key is required")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(a.Config.Secret))
	mac.Write(raw)
	fp := hex.EncodeToString(mac.Sum(nil))
	key = digest(key)
	principal = digest(principal)
	tx, err := m.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if idempotent {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO account_requests(principal,operation,request_key,fingerprint,created_at) VALUES(?,?,?,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE principal=principal`, principal, operation, key, fp); err != nil {
			return nil, err
		}
		var old string
		var response sql.NullString
		if err = tx.QueryRowContext(r.Context(), "SELECT fingerprint,response FROM account_requests WHERE principal=? AND operation=? AND request_key=? FOR UPDATE", principal, operation, key).Scan(&old, &response); err != nil {
			return nil, err
		}
		if old != fp {
			return nil, problem(409, "IDEMPOTENCY_KEY_REUSED", "This key was used for a different request")
		}
		if response.Valid {
			var v any
			if err = a.openAccount("replay:"+principal+":"+operation+":"+key, response.String, &v); err != nil {
				return nil, err
			}
			return v, tx.Commit()
		}
	}
	v, err := fn(tx)
	if err != nil {
		return nil, err
	}
	if idempotent {
		sealed, e := a.sealAccount("replay:"+principal+":"+operation+":"+key, v)
		if e != nil {
			return nil, e
		}
		if _, err = tx.ExecContext(r.Context(), "UPDATE account_requests SET response=? WHERE principal=? AND operation=? AND request_key=?", sealed, principal, operation, key); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return v, nil
}

func defaultProfile(u User) financeapi.Profile {
	return financeapi.Profile{ID: u.ID, Email: u.Email, Name: u.Name, Country: "ZZ", Locale: "zh-CN", Currency: "CNY", Revision: normalizedUserRevision(u).Revision, CreatedAt: stamp(time.Unix(0, 0))}
}
func readProfile(ctx context.Context, q accountQueryer, id string) (financeapi.Profile, error) {
	var raw []byte
	var u User
	var currency string
	var locked bool
	var created time.Time
	var locale sql.NullString
	var verified sql.NullBool
	err := q.QueryRowContext(ctx, `SELECT u.id,u.email,u.name,u.currency,(u.currency_locked OR EXISTS(SELECT 1 FROM invoices i WHERE i.user_id=u.id)),u.revision,u.created_at,p.document,p.locale,p.email_verified FROM users u LEFT JOIN account_profiles p ON p.user_id=u.id WHERE u.id=?`, id).Scan(&u.ID, &u.Email, &u.Name, &currency, &locked, &u.Revision, &created, &raw, &locale, &verified)
	if err != nil {
		return financeapi.Profile{}, err
	}
	p := defaultProfile(u)
	if raw != nil {
		if err = json.Unmarshal(raw, &p); err != nil {
			return p, err
		}
	}
	p.ID, p.Email, p.Name, p.Currency, p.CurrencyLocked, p.Revision, p.CreatedAt = u.ID, u.Email, u.Name, currency, locked, u.Revision, stamp(created)
	if locale.Valid {
		p.Locale = locale.String
	}
	p.EmailVerified = verified.Bool
	return p, nil
}
func saveProfileTx(ctx context.Context, tx *sql.Tx, p financeapi.Profile) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_profiles(user_id,document,locale,email_verified,notes,closed) VALUES(?,?,?,?,'',FALSE) ON DUPLICATE KEY UPDATE document=?,locale=?,email_verified=?`, p.ID, raw, p.Locale, p.EmailVerified, raw, p.Locale, p.EmailVerified)
	return err
}
func (a *App) sessionUser(ctx context.Context, u User) (financeapi.SessionUser, error) {
	p := defaultProfile(u)
	var err error
	if m, ok := a.Store.(*MySQL); ok {
		p, err = readProfile(ctx, m.DB, u.ID)
		if err != nil {
			return financeapi.SessionUser{}, err
		}
	}
	v := financeapi.SessionUser{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Locale: p.Locale, Currency: p.Currency, EmailVerified: p.EmailVerified}
	if u.Role == "admin" {
		policy, e := a.Store.StaffPolicy(ctx, u.ID)
		if e != nil {
			return v, e
		}
		v.Permissions = policy.Permissions
	}
	return v, nil
}
func (a *App) writeSession(w http.ResponseWriter, r *http.Request, u *User, token string) {
	var v financeapi.SessionUser
	var err error
	var user *financeapi.SessionUser
	if u != nil {
		v, err = a.sessionUser(r.Context(), *u)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		user = &v
	}
	jsonOut(w, 200, financeapi.Session{User: user, CSRFToken: a.csrf(token), DemoMode: a.Config.Demo})
}

func shortUserAgent(r *http.Request) string {
	s := r.UserAgent()
	if len(s) > 512 {
		s = s[:512]
	}
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[:len(s)-1]
	}
	return s
}
func lockAccountUser(ctx context.Context, tx *sql.Tx, id string) (User, error) {
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=? FOR UPDATE", id))
	if err == nil && u.Disabled {
		err = problem(403, "FORBIDDEN", "Account is disabled")
	}
	return u, err
}
func insertAccountAudit(ctx context.Context, tx *sql.Tx, owner, actor, action, target string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) SELECT ?,?,?,?,UTC_TIMESTAMP(6),email,'' FROM users WHERE id=?`, ID(), owner, action, target, actor)
	return err
}

func accountBody(w http.ResponseWriter, r *http.Request, operation string, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		fail(w, r, 415, "JSON_REQUIRED", "Use JSON")
		return false
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil || uniqueJSON(raw) != nil || financeapi.ValidateInput(operation, raw) != nil {
		fail(w, r, 400, "INVALID_BODY", "Request does not match the API contract")
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		fail(w, r, 400, "INVALID_BODY", "Invalid request")
		return false
	}
	return true
}

// Reject duplicate fields, excessive nesting and trailing documents before schema
// validation; no parser may silently choose a different security-sensitive value.
func uniqueJSON(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return errors.New("JSON nesting limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, composite := t.(json.Delim)
		if !composite {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := k.(string)
				if !ok || keys[name] {
					return errors.New("duplicate JSON field")
				}
				keys[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON document")
	}
	return nil
}
func queryLimit(r *http.Request) (int, error) {
	limit := 50
	values, exists := r.URL.Query()["limit"]
	if exists {
		var err error
		if len(values) != 1 {
			return 0, problem(400, "INVALID_QUERY", "Invalid limit")
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			return 0, problem(400, "INVALID_QUERY", "Invalid limit")
		}
	}
	return limit, nil
}
