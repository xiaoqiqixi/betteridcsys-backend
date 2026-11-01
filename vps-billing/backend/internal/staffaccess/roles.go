package staffaccess

import "slices"

// Permissions is the Core 0.6 contract catalog. The version-one snapshot
// catalog remains immutable for consumers of its persisted seals.
func Permissions() []string {
	return []string{"overview.read", "clients.read", "clients.write", "clients.credit", "orders.read", "orders.write", "invoices.read", "invoices.write", "invoices.refund", "services.read", "services.write", "services.power", "services.terminate", "products.read", "products.write", "promotions.write", "tickets.read", "tickets.write", "content.write", "automation.write", "integrations.write", "staff.write", "settings.write", "audit.read", "updates.write"}
}

func ValidPermissions(grants []string) bool {
	seen := map[string]bool{}
	for _, p := range grants {
		if !slices.Contains(Permissions(), p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// Allows accepts current database policy, never a session's cached grants.
// Even the owner must request a known permission: unknown routes fail closed.
func Allows(roleID string, builtin bool, grants []string, permission string) bool {
	return slices.Contains(Permissions(), permission) && ValidPermissions(grants) &&
		((roleID == "owner" && builtin) || slices.Contains(grants, permission))
}
