package main

import (
	"context"
	"testing"
)

func TestProvisioningMySQLTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Acceptable configurations reach connection setup without network IO.
	for _, tt := range []struct{ dsn, code string }{
		{"user@tcp(db.invalid:3306)/aster?tls=true", "PROVISIONING_MYSQL_CONNECTION_FAILED"},
		{"user@unix(/run/mysql.sock)/aster", "PROVISIONING_MYSQL_CONNECTION_FAILED"},
		{"user@tcp(db.invalid:3306)/aster", "PROVISIONING_MYSQL_CONFIGURATION_INVALID"},
		{"user@tcp(db.invalid:3306)/aster?tls=skip-verify", "PROVISIONING_MYSQL_CONFIGURATION_INVALID"},
		{"user@tcp(db.invalid:3306)/aster?tls=preferred", "PROVISIONING_MYSQL_CONFIGURATION_INVALID"},
		{"user@tcp(db.invalid:3306)/aster?tls=true&allowFallbackToPlaintext=true", "PROVISIONING_MYSQL_CONFIGURATION_INVALID"},
		{"user@unix(relative.sock)/aster", "PROVISIONING_MYSQL_CONFIGURATION_INVALID"},
	} {
		_, err := openProvisioningMySQL(ctx, tt.dsn)
		if err == nil || provisioningMySQLErrorCode(err) != tt.code {
			t.Fatalf("%s: %v", tt.dsn, err)
		}
	}
}
