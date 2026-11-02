package billing

import (
	api "aster/billing/internal/financeapi"
	"aster/billing/serviceapi"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestCheckoutPasswordHashesPreserveCredentialsWithoutPlaintext(t *testing.T) {
	password := "Customer-checkout-secret!2026"
	items := []api.CartItemInput{{RootPassword: password}, {SSHKeyIDs: []string{"key"}}}
	hashes, err := checkoutPasswordHashes06(items)
	if err != nil || len(hashes) != 2 || hashes[1] != "" || !serviceapi.ValidPasswordHash(hashes[0]) {
		t.Fatal("checkout credential derivation failed", err)
	}
	if strings.Contains(hashes[0], password) || bcrypt.CompareHashAndPassword([]byte(hashes[0]), []byte(password)) != nil {
		t.Fatal("stored value does not authenticate the selected password")
	}
	for _, item := range []api.CartItemInput{{RootPassword: password, SSHKeyIDs: []string{"key"}}, {RootPassword: "short"}, {RootPassword: strings.Repeat("a", 73)}, {RootPassword: password + "\n"}} {
		if _, err := checkoutPasswordHashes06([]api.CartItemInput{item}); err == nil {
			t.Fatal("ambiguous or unsafe guest credential accepted")
		}
	}
}
