package billing

import (
	"context"
	"database/sql"
	"errors"
)

func prepareCreditPrecisionMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	var check currencyMigrationCheck
	err := conn.QueryRowContext(ctx, "SELECT tc.CONSTRAINT_TYPE,tc.ENFORCED,cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.TABLE_NAME='credit_ledger' AND tc.CONSTRAINT_NAME='credit_precision'").Scan(&check.kind, &check.enforced, &check.clause)
	if err != nil {
		return "", false, err
	}
	if commerceCheckMatches(check, "decimal_places >= 0 AND decimal_places <= 6") {
		return q, true, nil
	}
	if !commerceCheckMatches(check, "decimal_places >= 0 AND decimal_places <= 3") {
		return "", false, errors.New("unexpected credit precision check")
	}
	return q, false, nil
}
