package billing

import (
	"aster/billing/internal/staffaccess"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type staffPolicy struct {
	RoleID      string
	Builtin     bool
	Permissions []string
}

type staffPolicyStore interface {
	StaffPolicy(context.Context, string) (staffPolicy, error)
}

// staff resolves membership and permissions afresh on every request. It never
// treats the session's role or a previous successful request as authority.
func (a *App) staff(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			fail(w, r, 403, "FORBIDDEN", "Staff access requires a browser session")
			return
		}
		u, err := a.current(r)
		if err != nil || u.Role != "admin" || u.Disabled {
			if err != nil && !errors.Is(err, ErrNotFound) {
				a.internal(w, r, err)
				return
			}
			fail(w, r, 403, "FORBIDDEN", "Permission denied")
			return
		}
		s, ok := a.Store.(staffPolicyStore)
		if !ok {
			fail(w, r, 403, "FORBIDDEN", "Permission denied")
			return
		}
		p, err := s.StaffPolicy(r.Context(), u.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			a.internal(w, r, err)
			return
		}
		if err != nil || !staffaccess.Allows(p.RoleID, p.Builtin, p.Permissions, perm) {
			fail(w, r, 403, "FORBIDDEN", "Permission denied")
			return
		}
		h(w, r)
	}
}

func (m *MySQL) StaffPolicy(ctx context.Context, id string) (staffPolicy, error) {
	var p staffPolicy
	var raw []byte
	err := m.DB.QueryRowContext(ctx, `SELECT r.id,r.builtin,r.permissions FROM users u
	 JOIN staff_members s ON s.user_id=u.id JOIN staff_roles r ON r.id=s.role_id
	 WHERE u.id=? AND u.role='admin' AND u.disabled=FALSE`, id).Scan(&p.RoleID, &p.Builtin, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal(raw, &p.Permissions)
	}
	return p, err
}

func (m *Memory) StaffPolicy(_ context.Context, id string) (staffPolicy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	p, exists := m.staffPolicies[id]
	if !ok || !exists || u.Role != "admin" || u.Disabled {
		return staffPolicy{}, ErrNotFound
	}
	p.Permissions = append([]string{}, p.Permissions...)
	return p, nil
}

func ownerPolicy() staffPolicy {
	return staffPolicy{RoleID: "owner", Builtin: true, Permissions: staffaccess.Permissions()}
}

// Legacy admin routes remain available for one version, with explicit 0.6
// permissions. New routes must also be covered here or use staff directly.
func adminPermission(path, method string) string {
	read := method == http.MethodGet
	parts := strings.Split(strings.TrimPrefix(path, "/admin/"), "/")
	if len(parts) == 0 {
		return ""
	}
	switch parts[0] {
	case "overview":
		return "overview.read"
	case "audit-events":
		return "audit.read"
	case "roles", "staff", "customer-accounts":
		return "staff.write"
	case "customers", "clients":
		if len(parts) > 2 && parts[2] == "credit" && !read {
			return "clients.credit"
		}
		if read {
			return "clients.read"
		}
		return "clients.write"
	case "catalog", "catalog-taxonomy", "plan-prices", "products", "os-images":
		if read {
			return "products.read"
		}
		return "products.write"
	case "coupons":
		if read {
			return "products.read"
		}
		return "promotions.write"
	case "orders":
		if len(parts) > 2 && parts[2] == "provisioning-jobs" {
			return "services.write"
		}
		if read {
			return "orders.read"
		}
		return "orders.write"
	case "provisioning-jobs":
		return "services.write"
	case "invoices", "payment-attempts":
		if len(parts) > 2 && parts[2] == "refunds" {
			return "invoices.refund"
		}
		if read {
			return "invoices.read"
		}
		return "invoices.write"
	case "services":
		if read {
			return "services.read"
		}
		return "services.write"
	case "tickets", "departments":
		if read {
			return "tickets.read"
		}
		return "tickets.write"
	case "announcements":
		return "content.write"
	case "automation":
		return "automation.write"
	case "site-settings", "security", "currencies", "email-templates":
		return "settings.write"
	case "email-settings", "integrations", "plugins":
		return "integrations.write"
	case "updates":
		return "updates.write"
	}
	return ""
}
