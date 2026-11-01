package billing

import (
	"context"
	"testing"
)

func TestMigrationDispatchKeepsInternalZeroVersionOutOfSourceLookup(t *testing.T) {
	// Specialized migration validators delegate generic statements with the
	// internal version-zero sentinel. It must not index schemaVersions[-1].
	const statement = "SELECT 1"
	query, skip, err := prepareMigrationStatement(context.Background(), nil, 0, statement)
	if err != nil || skip || query != statement {
		t.Fatalf("generic migration dispatch changed: query=%q skip=%v err=%v", query, skip, err)
	}
}
