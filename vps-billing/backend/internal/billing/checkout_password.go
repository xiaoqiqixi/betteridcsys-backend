package billing

import (
	api "aster/billing/internal/financeapi"
	"regexp"
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
		// A password and SSH keys may both be set; keep the byte-length bounds.
		if len(item.RootPassword) < 8 || len(item.RootPassword) > 72 || strings.ContainsAny(item.RootPassword, "\x00\r\n") {
			return nil, problem(400, "INVALID_GUEST_CREDENTIAL", "Use a root password of 8–72 bytes, SSH keys, or both")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(item.RootPassword), 12)
		if err != nil || !validGuestPasswordHash06(string(hash)) {
			return nil, ErrCommerceUnavailable
		}
		hashes[i] = string(hash)
	}
	return hashes, nil
}

var guestPasswordHashPattern06 = regexp.MustCompile(`^\$2[ab]\$1[2-6]\$[./A-Za-z0-9]{53}$`)

func validGuestPasswordHash06(hash string) bool { return guestPasswordHashPattern06.MatchString(hash) }
