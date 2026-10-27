package billing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrAdminRequired     = errors.New("active administrator required")
	ErrAccountInput      = errors.New("invalid account input")
	ErrAccountQuery      = errors.New("invalid account query")
	ErrAccountExists     = errors.New("account already exists")
	ErrAccountConflict   = errors.New("account revision changed")
	ErrSelfAccountChange = errors.New("cannot demote or disable own account")
	ErrLastActiveAdmin   = errors.New("last active administrator")
)

// Shared with catalog management: persistence, not the caller's cached role,
// determines authorization. SHARE precedes business-resource locks; identity
// writes first serialize on their singleton and fence the original session.
func lockActiveAdministrator(ctx context.Context, tx *sql.Tx, actorID string) (User, error) {
	actor, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=? FOR SHARE", actorID))
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrAdminRequired
	}
	if err != nil {
		return User{}, err
	}
	if actor.Disabled || actor.Role != "admin" {
		return User{}, ErrAdminRequired
	}
	return actor, nil
}
func (m *Memory) activeAdministratorLocked(actorID string) (User, error) {
	actor, ok := m.users[actorID]
	if !ok || actor.Disabled || actor.Role != "admin" {
		return User{}, ErrAdminRequired
	}
	return normalizedUserRevision(actor), nil
}
func normalizedUserRevision(user User) User {
	// Legacy in-memory fixtures predate revisions. SQL migration 10 sets them to 1.
	if user.Revision == 0 {
		user.Revision = 1
	}
	return user
}

type customerAccount struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Disabled bool   `json:"disabled"`
	Revision int64  `json:"revision"`
}

func accountDTO(u User) customerAccount {
	return customerAccount{u.ID, u.Email, u.Name, u.Role, u.Disabled, u.Revision}
}

type accountQuery struct {
	Limit                   int
	Q, Role, Status, Before string
}
type accountPage struct {
	Items   []User
	HasMore bool
}
type accountWrite struct {
	Operation, ID, Name, Email, Role, Password, Reason string
	Disabled                                           bool
	Revision                                           int64
}
type adminCustomerStore interface {
	AdminCustomerAccounts(context.Context, string, accountQuery) (accountPage, error)
	AdminCustomerAccount(context.Context, string, string) (User, error)
	WriteCustomerAccount(context.Context, string, accountWrite) (User, error)
}

func normalizeAccountQuery(q accountQuery) (accountQuery, error) {
	if q.Limit == 0 {
		q.Limit = 50
	}
	q.Q = strings.TrimSpace(q.Q)
	if q.Limit < 1 || q.Limit > 100 || !validText(q.Q, 120, false) || q.Role != "" && q.Role != "admin" && q.Role != "customer" || q.Status != "" && q.Status != "active" && q.Status != "disabled" || q.Before != "" && !auditOwner.MatchString(q.Before) {
		return q, ErrAccountQuery
	}
	return q, nil
}
func prepareAccountWrite(w accountWrite) (accountWrite, string, error) {
	w.Name = strings.TrimSpace(w.Name)
	w.Reason = strings.TrimSpace(w.Reason)
	if w.Reason == "" || !validText(w.Reason, 500, false) {
		return w, "", ErrAccountInput
	}
	if w.Operation != "create" && (!auditOwner.MatchString(w.ID) || w.Revision < 1 || w.Revision > MaxMinor) {
		return w, "", ErrAccountInput
	}
	switch w.Operation {
	case "create", "update":
		if w.Name == "" || !validText(w.Name, 120, false) || w.Role != "customer" && w.Role != "admin" {
			return w, "", ErrAccountInput
		}
	case "password", "revoke":
	default:
		return w, "", ErrAccountInput
	}
	if w.Operation == "create" {
		w.Email = strings.ToLower(strings.TrimSpace(w.Email))
		a, err := mail.ParseAddress(w.Email)
		if err != nil || a.Address != w.Email || a.Name != "" || len(w.Email) > 254 || strings.IndexFunc(w.Email, func(c rune) bool { return c > 127 }) >= 0 {
			return w, "", ErrAccountInput
		}
	}
	hash := ""
	if w.Operation == "create" || w.Operation == "password" {
		// bcrypt's limit is bytes, not characters. Passwords are never trimmed.
		if len(w.Password) < 16 || len(w.Password) > 72 || strings.TrimSpace(w.Password) == "" || strings.Contains(w.Reason, w.Password) {
			return w, "", ErrAccountInput
		}
		b, err := bcrypt.GenerateFromPassword([]byte(w.Password), 12)
		if err != nil {
			return w, "", err
		}
		hash = string(b)
	}
	w.Password = "" // Never carry plaintext into transaction/audit construction.
	return w, hash, nil
}
func accountMutation(actor, current User, w accountWrite, hash string, activeAdmins int) (User, bool, error) {
	if current.Revision != w.Revision || current.Revision >= MaxMinor {
		return User{}, false, ErrAccountConflict
	}
	next := current
	revoke := w.Operation == "password" || w.Operation == "revoke"
	if w.Operation == "update" {
		if current.ID == actor.ID && (w.Role != "admin" || w.Disabled) {
			return User{}, false, ErrSelfAccountChange
		}
		if current.Role == "admin" && !current.Disabled && (w.Role != "admin" || w.Disabled) && activeAdmins <= 1 {
			return User{}, false, ErrLastActiveAdmin
		}
		revoke = revoke || current.Role != w.Role || current.Disabled != w.Disabled
		next.Name, next.Role, next.Disabled = w.Name, w.Role, w.Disabled
	}
	if w.Operation == "password" {
		next.PasswordHash = hash
	}
	next.Revision++
	return next, revoke, nil
}
func accountAudit(actor, next User, w accountWrite) Audit {
	actions := map[string]string{"create": "account.created", "update": "account.updated", "password": "account.password_reset", "revoke": "account.sessions_revoked"}
	// Explicit fields only: never serialize w or User (which hold credentials).
	detail := fmt.Sprintf("revision=%d; role=%s; disabled=%t; reason=%s", next.Revision, next.Role, next.Disabled, w.Reason)
	return Audit{ID: ID(), UserID: next.ID, Action: actions[w.Operation], Target: next.ID, ActorEmail: actor.Email, Detail: detail, CreatedAt: time.Now().UTC()}
}
func accountMatches(u User, q accountQuery) bool {
	return (q.Before == "" || u.ID < q.Before) && (q.Role == "" || u.Role == q.Role) && (q.Status == "" || (q.Status == "disabled") == u.Disabled) && (q.Q == "" || strings.Contains(strings.ToLower(u.Name), strings.ToLower(q.Q)) || strings.Contains(strings.ToLower(u.Email), strings.ToLower(q.Q)))
}
func (m *Memory) AdminCustomerAccounts(_ context.Context, actor string, q accountQuery) (accountPage, error) {
	q, err := normalizeAccountQuery(q)
	if err != nil {
		return accountPage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err = m.activeAdministratorLocked(actor); err != nil {
		return accountPage{}, err
	}
	out := []User{}
	for _, u := range m.users {
		if accountMatches(u, q) {
			out = append(out, normalizedUserRevision(u))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return accountPage{out, more}, nil
}
func (m *Memory) AdminCustomerAccount(_ context.Context, actor, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.activeAdministratorLocked(actor); err != nil {
		return User{}, err
	}
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return normalizedUserRevision(u), nil
}
func (m *Memory) WriteCustomerAccount(ctx context.Context, actorID string, input accountWrite) (User, error) {
	sessionHash, err := accountWriteSessionHash(ctx, actorID)
	if err != nil {
		return User{}, err
	}
	w, hash, err := prepareAccountWrite(input)
	if err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return User{}, err
	}
	if err := m.accountSessionLocked(actorID, sessionHash); err != nil {
		return User{}, err
	}
	actor, err := m.activeAdministratorLocked(actorID)
	if err != nil {
		return User{}, err
	}
	var next User
	revoke := false
	if w.Operation == "create" {
		for _, u := range m.users {
			if strings.EqualFold(u.Email, w.Email) {
				return User{}, ErrAccountExists
			}
		}
		next = User{ID: ID(), Email: w.Email, Name: w.Name, Role: w.Role, PasswordHash: hash, Revision: 1}
	} else {
		current, ok := m.users[w.ID]
		if !ok {
			return User{}, ErrNotFound
		}
		admins := 0
		for _, u := range m.users {
			if !u.Disabled && u.Role == "admin" {
				admins++
			}
		}
		next, revoke, err = accountMutation(actor, normalizedUserRevision(current), w, hash, admins)
		if err != nil {
			return User{}, err
		}
	}
	m.users[next.ID] = next
	if revoke {
		for k, s := range m.sessions {
			if s.UserID == next.ID {
				delete(m.sessions, k)
			}
		}
	}
	m.audits = append(m.audits, accountAudit(actor, next, w))
	return next, nil
}
func (m *MySQL) AdminCustomerAccounts(ctx context.Context, actor string, q accountQuery) (accountPage, error) {
	q, err := normalizeAccountQuery(q)
	if err != nil {
		return accountPage{}, err
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return accountPage{}, err
	}
	defer tx.Rollback()
	if _, err = lockActiveAdministrator(ctx, tx, actor); err != nil {
		return accountPage{}, err
	}
	where := []string{"1=1"}
	args := []any{}
	if q.Before != "" {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	if q.Role != "" {
		where = append(where, "role = ?")
		args = append(args, q.Role)
	}
	if q.Status != "" {
		where = append(where, "disabled = ?")
		args = append(args, q.Status == "disabled")
	}
	if q.Q != "" {
		where = append(where, "(LOCATE(BINARY LOWER(?),BINARY LOWER(name))>0 OR LOCATE(BINARY LOWER(?),BINARY LOWER(email))>0)")
		args = append(args, q.Q, q.Q)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, "SELECT "+userColumns+" FROM users WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return accountPage{}, err
	}
	out := []User{}
	for rows.Next() {
		u, e := scanUser(rows)
		if e != nil {
			rows.Close()
			return accountPage{}, e
		}
		out = append(out, u)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return accountPage{}, err
	}
	if err = tx.Commit(); err != nil {
		return accountPage{}, err
	}
	more := len(out) > q.Limit
	if more {
		out = out[:q.Limit]
	}
	return accountPage{out, more}, nil
}
func (m *MySQL) AdminCustomerAccount(ctx context.Context, actor, id string) (User, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if _, err = lockActiveAdministrator(ctx, tx, actor); err != nil {
		return User{}, err
	}
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=?", id))
	if err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}
func (m *MySQL) WriteCustomerAccount(ctx context.Context, actorID string, input accountWrite) (User, error) {
	sessionHash, err := accountWriteSessionHash(ctx, actorID)
	if err != nil {
		return User{}, err
	}
	w, hash, err := prepareAccountWrite(input)
	if err != nil {
		return User{}, err
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	// Serialize role changes before locking users. This independent singleton
	// avoids currency->user lock inversion with order_requests' user FK lock.
	// Actor SHARE (not X) also avoids cycles with financial audit actor FK reads.
	var singleton int
	if err = tx.QueryRowContext(ctx, "SELECT id FROM email_settings WHERE id=1 FOR UPDATE").Scan(&singleton); err != nil {
		return User{}, err
	}
	if err := lockAccountSession(ctx, tx, actorID, sessionHash); err != nil {
		return User{}, err
	}
	actor, err := lockActiveAdministrator(ctx, tx, actorID)
	if err != nil {
		return User{}, err
	}
	var next User
	revoke := false
	if w.Operation == "create" {
		next = User{ID: ID(), Email: w.Email, Name: w.Name, Role: w.Role, PasswordHash: hash, Revision: 1}
		_, err = tx.ExecContext(ctx, "INSERT INTO users(id,email,name,role,password_hash,disabled,revision) VALUES(?,?,?,?,?,?,?)", next.ID, next.Email, next.Name, next.Role, next.PasswordHash, false, next.Revision)
		var duplicate *mysql.MySQLError
		if errors.As(err, &duplicate) && duplicate.Number == 1062 {
			return User{}, ErrAccountExists
		}
		if err != nil {
			return User{}, err
		}
	} else {
		current, e := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id=? FOR UPDATE", w.ID))
		if e != nil {
			return User{}, e
		}
		var admins int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=FALSE").Scan(&admins); err != nil {
			return User{}, err
		}
		next, revoke, err = accountMutation(actor, current, w, hash, admins)
		if err != nil {
			return User{}, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE users SET name=?,role=?,password_hash=?,disabled=?,revision=? WHERE id=?", next.Name, next.Role, next.PasswordHash, next.Disabled, next.Revision, next.ID)
		if err != nil {
			return User{}, err
		}
	}
	if revoke {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", next.ID); err != nil {
			return User{}, err
		}
	}
	a := accountAudit(actor, next, w)
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,actor_email,detail,created_at) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.ActorEmail, a.Detail, a.CreatedAt); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	return next, nil
}
