package billing

import "testing"

// Historical migration helpers deliberately use version zero to invoke the
// generic statement handler without redispatching into their own migration.
func TestCommerceMigrationDispatchPreservesGenericVersionZero(t *testing.T) {
	statement, skip, err := prepareMigrationStatement(t.Context(), nil, 0, "SELECT 1")
	if err != nil || skip || statement != "SELECT 1" {
		t.Fatal(statement, skip, err)
	}
}
