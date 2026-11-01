package billing

import (
	"aster/billing/internal/staffaccess"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type StaffRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Builtin     bool     `json:"builtin"`
	Members     int      `json:"members"`
	Revision    int64    `json:"revision"`
}
type StaffMember struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	RoleID      string     `json:"role_id"`
	Status      string     `json:"status"`
	LastLoginAt *time.Time `json:"last_login_at"`
	Revision    int64      `json:"revision"`
}
type staffRoleInput struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	Revision    *int64   `json:"revision"`
}
type staffMemberInput struct {
	RoleID   *string `json:"role_id"`
	Status   *string `json:"status"`
	Revision *int64  `json:"revision"`
}

var errBuiltinRole = errors.New("built-in role is immutable")
var errLastOwner = errors.New("last active owner is protected")

type staffStore interface {
	StaffRoles(context.Context) ([]StaffRole, error)
	StaffMembers(context.Context) ([]StaffMember, error)
	SaveStaffRole(context.Context, string, staffRoleInput) (StaffRole, error)
	UpdateStaffMember(context.Context, string, string, staffMemberInput) (StaffMember, error)
}

func (a *App) staffHTTP(w http.ResponseWriter, r *http.Request, u User, p string) {
	if p == "/admin/staff" && r.Method == "POST" {
		a.inviteStaff(w, r, u)
		return
	}
	s, ok := a.Store.(staffStore)
	if !ok {
		a.internal(w, r, ErrUnavailable)
		return
	}
	var v any
	var err error
	switch {
	case p == "/admin/roles" && r.Method == "GET":
		v, err = s.StaffRoles(r.Context())
	case p == "/admin/roles" && r.Method == "POST":
		var in staffRoleInput
		if !accountBody(w, r, "adminSaveRole", &in) {
			return
		}
		if strings.TrimSpace(in.Name) == "" || !validText(in.Name, 60, true) || !validText(in.Description, 240, false) || in.Permissions == nil || !staffaccess.ValidPermissions(in.Permissions) || (in.ID != "" && (in.Revision == nil || *in.Revision < 1 || *in.Revision >= MaxMinor)) {
			fail(w, r, 400, "INVALID_BODY", "Invalid role")
			return
		}
		v, err = s.SaveStaffRole(r.Context(), u.ID, in)
	case p == "/admin/staff" && r.Method == "GET":
		v, err = s.StaffMembers(r.Context())
	case strings.HasPrefix(p, "/admin/staff/") && r.Method == "PATCH":
		id := strings.TrimPrefix(p, "/admin/staff/")
		if !validCancellationID(id) {
			fail(w, r, 404, "NOT_FOUND", "Staff member not found")
			return
		}
		var in staffMemberInput
		if !accountBody(w, r, "adminUpdateStaff", &in) {
			return
		}
		if in.Revision == nil || *in.Revision < 1 || *in.Revision >= MaxMinor || (in.Status != nil && *in.Status != "active" && *in.Status != "disabled") || (in.RoleID != nil && (len(*in.RoleID) == 0 || len(*in.RoleID) > 64)) {
			fail(w, r, 400, "INVALID_BODY", "Invalid staff membership")
			return
		}
		v, err = s.UpdateStaffMember(r.Context(), u.ID, id, in)
	default:
		fail(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
		return
	}
	switch {
	case err == nil:
		jsonOut(w, 200, v)
	case errors.Is(err, ErrConflict):
		fail(w, r, 409, "REVISION_CONFLICT", "Record changed; reload and retry")
	case errors.Is(err, errBuiltinRole):
		fail(w, r, 409, "BUILTIN_ROLE_PROTECTED", "The owner role cannot be modified")
	case errors.Is(err, errLastOwner):
		fail(w, r, 409, "LAST_ACTIVE_OWNER", "At least one active owner is required")
	case errors.Is(err, ErrAdminRequired):
		fail(w, r, 403, "FORBIDDEN", "Permission denied")
	case errors.Is(err, ErrNotFound):
		fail(w, r, 404, "NOT_FOUND", "Record not found")
	default:
		a.internal(w, r, err)
	}
}

func (m *MySQL) StaffRoles(ctx context.Context) ([]StaffRole, error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT r.id,r.name,r.description,r.permissions,r.builtin,r.revision,COUNT(s.user_id) FROM staff_roles r LEFT JOIN staff_members s ON s.role_id=r.id GROUP BY r.id ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaffRole{}
	for rows.Next() {
		var v StaffRole
		var raw []byte
		if err = rows.Scan(&v.ID, &v.Name, &v.Description, &raw, &v.Builtin, &v.Revision, &v.Members); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &v.Permissions); err != nil {
			return nil, err
		}
		if v.ID == "owner" && v.Builtin {
			v.Permissions = staffaccess.Permissions()
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func scanStaff(row interface{ Scan(...any) error }) (StaffMember, error) {
	var v StaffMember
	var disabled bool
	err := row.Scan(&v.ID, &v.Email, &v.Name, &v.RoleID, &disabled, &v.LastLoginAt, &v.Revision)
	v.Status = "active"
	if disabled {
		v.Status = "disabled"
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return v, err
}

const staffMemberSelect = `SELECT u.id,u.email,u.name,s.role_id,u.disabled,s.last_login_at,u.revision FROM users u JOIN staff_members s ON s.user_id=u.id WHERE u.role='admin'`

func (m *MySQL) StaffMembers(ctx context.Context) ([]StaffMember, error) {
	rows, err := m.DB.QueryContext(ctx, staffMemberSelect+" ORDER BY u.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StaffMember{}
	for rows.Next() {
		v, e := scanStaff(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func staffWriteTx(ctx context.Context, tx *sql.Tx, actor string, permission ...string) error {
	// Same lock order as the legacy account writer, including last-owner checks.
	var mutex int
	if err := tx.QueryRowContext(ctx, "SELECT id FROM email_settings WHERE id=1 FOR UPDATE").Scan(&mutex); err != nil {
		return err
	}
	var p staffPolicy
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT r.id,r.builtin,r.permissions FROM users u JOIN staff_members s ON s.user_id=u.id JOIN staff_roles r ON r.id=s.role_id WHERE u.id=? AND u.role='admin' AND u.disabled=FALSE`, actor).Scan(&p.RoleID, &p.Builtin, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAdminRequired
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &p.Permissions); err != nil {
		return err
	}
	perm := "staff.write"
	if len(permission) > 0 {
		perm = permission[0]
	}
	if !staffaccess.Allows(p.RoleID, p.Builtin, p.Permissions, perm) {
		return ErrAdminRequired
	}
	return nil
}
func (m *MySQL) SaveStaffRole(ctx context.Context, actor string, in staffRoleInput) (StaffRole, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StaffRole{}, err
	}
	defer tx.Rollback()
	if err = staffWriteTx(ctx, tx, actor); err != nil {
		return StaffRole{}, err
	}
	v := StaffRole{ID: in.ID, Name: in.Name, Description: in.Description, Permissions: in.Permissions, Revision: 1}
	if v.ID != "" {
		var revision int64
		if err = tx.QueryRowContext(ctx, "SELECT builtin,revision FROM staff_roles WHERE id=? FOR UPDATE", v.ID).Scan(&v.Builtin, &revision); errors.Is(err, sql.ErrNoRows) {
			return v, ErrNotFound
		} else if err != nil {
			return v, err
		}
		if v.Builtin {
			return v, errBuiltinRole
		}
		if in.Revision == nil || revision != *in.Revision {
			return v, ErrConflict
		}
		v.Revision = revision + 1
	} else {
		v.ID = ID()
	}
	raw, _ := json.Marshal(v.Permissions)
	if _, err = tx.ExecContext(ctx, `INSERT INTO staff_roles(id,name,description,permissions,builtin,revision) VALUES(?,?,?,?,FALSE,?) ON DUPLICATE KEY UPDATE name=?,description=?,permissions=?,revision=?`, v.ID, v.Name, v.Description, raw, v.Revision, v.Name, v.Description, raw, v.Revision); err != nil {
		return v, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM staff_members WHERE role_id=?", v.ID).Scan(&v.Members); err != nil {
		return v, err
	}
	if err = staffAuditTx(ctx, tx, actor, "staff.role_updated", v.ID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func staffAuditTx(ctx context.Context, tx *sql.Tx, actor, action, target string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) SELECT ?,id,?,?,UTC_TIMESTAMP(6),email,'' FROM users WHERE id=?`, ID(), action, target, actor)
	return err
}
func (m *MySQL) UpdateStaffMember(ctx context.Context, actor, id string, in staffMemberInput) (StaffMember, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return StaffMember{}, err
	}
	defer tx.Rollback()
	if err = staffWriteTx(ctx, tx, actor); err != nil {
		return StaffMember{}, err
	}
	v, err := scanStaff(tx.QueryRowContext(ctx, staffMemberSelect+" AND u.id=? FOR UPDATE", id))
	if err != nil {
		return v, err
	}
	if in.Revision == nil || v.Revision != *in.Revision {
		return v, ErrConflict
	}
	role, status := v.RoleID, v.Status
	if in.RoleID != nil {
		role = *in.RoleID
	}
	if in.Status != nil {
		status = *in.Status
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM staff_roles WHERE id=?", role).Scan(&exists); err != nil {
		return v, err
	}
	if exists != 1 {
		return v, ErrNotFound
	}
	if v.RoleID == "owner" && v.Status == "active" && (role != "owner" || status != "active") {
		var owners int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM staff_members s JOIN users u ON u.id=s.user_id WHERE s.role_id='owner' AND u.role='admin' AND u.disabled=FALSE`).Scan(&owners); err != nil {
			return v, err
		}
		if owners <= 1 {
			return v, errLastOwner
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE staff_members SET role_id=? WHERE user_id=?", role, id); err != nil {
		return v, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE users SET disabled=?,revision=revision+1 WHERE id=?", status == "disabled", id); err != nil {
		return v, err
	}
	if role != v.RoleID || status != v.Status {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id=?", id); err != nil {
			return v, err
		}
	}
	v.RoleID, v.Status, v.Revision = role, status, v.Revision+1
	if err = staffAuditTx(ctx, tx, actor, "staff.member_updated", id); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

func (m *Memory) initStaffRolesLocked() {
	if m.staffRoles == nil {
		m.staffRoles = map[string]StaffRole{"owner": {ID: "owner", Name: "Owner", Description: "All permissions", Permissions: staffaccess.Permissions(), Builtin: true, Revision: 1}}
	}
}
func (m *Memory) StaffRoles(context.Context) ([]StaffRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initStaffRolesLocked()
	out := []StaffRole{}
	for _, v := range m.staffRoles {
		v.Permissions = append([]string{}, v.Permissions...)
		for _, p := range m.staffPolicies {
			if p.RoleID == v.ID {
				v.Members++
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func memberFromUser(u User, p staffPolicy) StaffMember {
	status := "active"
	if u.Disabled {
		status = "disabled"
	}
	return StaffMember{ID: u.ID, Email: u.Email, Name: u.Name, RoleID: p.RoleID, Status: status, Revision: normalizedUserRevision(u).Revision}
}
func (m *Memory) StaffMembers(context.Context) ([]StaffMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []StaffMember{}
	for id, u := range m.users {
		if p, ok := m.staffPolicies[id]; ok && u.Role == "admin" {
			out = append(out, memberFromUser(u, p))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *Memory) staffWriteLocked(actor string) bool {
	u, ok := m.users[actor]
	p := m.staffPolicies[actor]
	return ok && !u.Disabled && u.Role == "admin" && staffaccess.Allows(p.RoleID, p.Builtin, p.Permissions, "staff.write")
}
func (m *Memory) SaveStaffRole(_ context.Context, actor string, in staffRoleInput) (StaffRole, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initStaffRolesLocked()
	if !m.staffWriteLocked(actor) {
		return StaffRole{}, ErrAdminRequired
	}
	v := StaffRole{ID: in.ID, Name: in.Name, Description: in.Description, Permissions: append([]string{}, in.Permissions...), Revision: 1}
	if v.ID != "" {
		old, ok := m.staffRoles[v.ID]
		if !ok {
			return v, ErrNotFound
		}
		if old.Builtin {
			return v, errBuiltinRole
		}
		if in.Revision == nil || old.Revision != *in.Revision {
			return v, ErrConflict
		}
		v.Revision = old.Revision + 1
	} else {
		v.ID = ID()
	}
	m.staffRoles[v.ID] = v
	for id, p := range m.staffPolicies {
		if p.RoleID == v.ID {
			p.Permissions = append([]string{}, v.Permissions...)
			m.staffPolicies[id] = p
			v.Members++
		}
	}
	return v, nil
}
func (m *Memory) UpdateStaffMember(_ context.Context, actor, id string, in staffMemberInput) (StaffMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initStaffRolesLocked()
	if !m.staffWriteLocked(actor) {
		return StaffMember{}, ErrAdminRequired
	}
	u, ok := m.users[id]
	p, exists := m.staffPolicies[id]
	v := memberFromUser(u, p)
	if !ok || !exists || u.Role != "admin" {
		return v, ErrNotFound
	}
	if in.Revision == nil || v.Revision != *in.Revision {
		return v, ErrConflict
	}
	role, status := v.RoleID, v.Status
	if in.RoleID != nil {
		role = *in.RoleID
	}
	if in.Status != nil {
		status = *in.Status
	}
	rr, ok := m.staffRoles[role]
	if !ok {
		return v, ErrNotFound
	}
	if v.RoleID == "owner" && v.Status == "active" && (role != "owner" || status != "active") {
		owners := 0
		for uid, pu := range m.staffPolicies {
			uu := m.users[uid]
			if pu.RoleID == "owner" && uu.Role == "admin" && !uu.Disabled {
				owners++
			}
		}
		if owners <= 1 {
			return v, errLastOwner
		}
	}
	u.Disabled = status == "disabled"
	u.Revision = v.Revision + 1
	m.users[id] = u
	m.staffPolicies[id] = staffPolicy{RoleID: role, Builtin: rr.Builtin, Permissions: append([]string{}, rr.Permissions...)}
	if role != v.RoleID || status != v.Status {
		for h, s := range m.sessions {
			if s.UserID == id {
				delete(m.sessions, h)
			}
		}
	}
	return memberFromUser(u, m.staffPolicies[id]), nil
}
