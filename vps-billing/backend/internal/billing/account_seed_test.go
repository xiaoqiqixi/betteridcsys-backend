package billing

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestAccountSeedHashesRetainCostAndInstanceIsolation(t *testing.T) {
	const count = 8
	ready := make(chan *App, count)
	for range count {
		go func() { ready <- NewApp(NewMemory(), Config{Demo: true}) }()
	}
	apps := make([]*App, 0, count)
	ids := map[string]bool{}
	for range count {
		a := <-ready
		apps = append(apps, a)
		cost, err := bcrypt.Cost(a.dummyHash)
		if err != nil || cost != bcrypt.DefaultCost {
			t.Fatal("unknown-account password work was weakened", cost, err)
		}
		users, err := a.Store.Users(t.Context())
		if err != nil || len(users) != 3 {
			t.Fatal("demo users missing", users, err)
		}
		for _, u := range users {
			if u.ID == "" || ids[u.ID] {
				t.Fatal("demo identity shared between stores")
			}
			ids[u.ID] = true
			cost, err := bcrypt.Cost([]byte(u.PasswordHash))
			if err != nil || cost != bcrypt.DefaultCost {
				t.Fatal("demo password cost changed", cost, err)
			}
		}
	}
	for email, password := range map[string]string{"demo@aster.local": "DemoCustomer!2026", "admin@aster.local": "DemoAdmin!2026", "other@aster.local": "DemoOther!2026"} {
		u, err := apps[0].Store.UserByEmail(t.Context(), email)
		if err != nil || !checkPassword(u.PasswordHash, password) {
			t.Fatal("public demo credential stopped working", email, err)
		}
	}
	first := apps[0].Store.(*Memory)
	u, err := first.UserByEmail(t.Context(), "demo@aster.local")
	if err != nil {
		t.Fatal(err)
	}
	first.mu.Lock()
	u.PasswordHash = "changed only in this store"
	first.users[u.ID] = u
	first.mu.Unlock()
	other, err := apps[1].Store.UserByEmail(t.Context(), u.Email)
	if err != nil || !checkPassword(other.PasswordHash, "DemoCustomer!2026") {
		t.Fatal("password changes escaped the owning demo store", err)
	}
	before := string(apps[1].dummyHash)
	apps[0].dummyHash[0] ^= 0xff
	if string(apps[1].dummyHash) != before {
		t.Fatal("applications share mutable dummy password bytes")
	}
	if checkPassword(before, "invalid-account-password") {
		t.Fatal("dummy credential matched an invalid login")
	}
}
