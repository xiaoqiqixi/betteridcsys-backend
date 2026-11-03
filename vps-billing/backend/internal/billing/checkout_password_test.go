package billing

import (
	api "aster/billing/internal/financeapi"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestCheckoutPasswordHashesPreserveCredentialsWithoutPlaintext(t *testing.T) {
	password := "Customer-checkout-secret!2026"
	items := []api.CartItemInput{{RootPassword: password}, {SSHKeyIDs: []string{"key"}}}
	hashes, err := checkoutPasswordHashes06(items)
	if err != nil || len(hashes) != 2 || hashes[1] != "" || !validGuestPasswordHash06(hashes[0]) {
		t.Fatal("checkout credential derivation failed", err)
	}
	if strings.Contains(hashes[0], password) || bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte(password)) != nil {
		t.Fatal("stored value does not authenticate the selected password")
	}
	// A password combined with SSH keys is now valid; the hash is still derived.
	if both, err := checkoutPasswordHashes06([]api.CartItemInput{{RootPassword: password, SSHKeyIDs: []string{"key"}}}); err != nil || !validGuestPasswordHash06(both[0]) {
		t.Fatal("password combined with SSH keys rejected", err)
	}
	for _, item := range []api.CartItemInput{{RootPassword: "short"}, {RootPassword: strings.Repeat("a", 73)}, {RootPassword: password + "\n"}} {
		if _, err := checkoutPasswordHashes06([]api.CartItemInput{item}); err == nil {
			t.Fatal("unsafe guest credential accepted")
		}
	}
}
