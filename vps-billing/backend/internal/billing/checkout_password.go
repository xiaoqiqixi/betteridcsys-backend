package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// Checkout may precede payment by days. Persist a Linux crypt hash, never the
// password or a recoverable credential. Hash once outside transaction retries.
func checkoutPasswordHashes06(items []api.CartItemInput) ([]string, error) {
	hashes := make([]string, len(items))
	for i, item := range items {
		if item.RootPassword == "" {
			continue
		}
		if len(item.SSHKeyIDs) != 0 || len(item.RootPassword) < 8 || len(item.RootPassword) > 72 || strings.ContainsAny(item.RootPassword, "\x00\r\n") {
			return nil, problem(400, "INVALID_GUEST_CREDENTIAL", "Use either an SSH key or a root password of 8–72 bytes")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(item.RootPassword), 12)
		if err != nil || !serviceapi.ValidPasswordHash(string(hash)) {
			return nil, ErrCommerceUnavailable
		}
		hashes[i] = string(hash)
	}
	return hashes, nil
}
