package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// No historical orders are backfilled. Existing colliding objects are checked
// in full before the first new table is created, and after interrupted DDL.
func prepareProductConfigurationMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	for _, table := range []string{"product_configurations", "order_product_configurations", "provisioning_jobs"} {
		if !strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			continue
		}
		var kind, engine, collation string
		err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind, &engine, &collation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if kind != "BASE TABLE" || engine != "InnoDB" {
			return "", false, errors.New("product configuration object differs from migration")
		}
		if err = validatePreflightTableShape(ctx, conn, 13, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	return "", false, errors.New("unsupported product configuration migration")
}

// Match only the three shipped schema13 checks. MySQL adds parentheses around
// each comparison; parsing these complete atoms does not accept OR, changed
// bounds, duplicate operands, comments or a check with a different identity.
func productConfigurationMigrationCheckMatches(table, name string, check currencyMigrationCheck, expected string) bool {
	var atoms []string
	switch table + "." + name {
	case "product_configurations.product_configuration_revision":
		atoms = []string{"revision >= 1", "revision <= 9007199254740991"}
	case "order_product_configurations.order_product_configuration_revision":
		atoms = []string{"configuration_revision >= 1", "configuration_revision <= 9007199254740991"}
	case "provisioning_jobs.provisioning_job_attempts":
		atoms = []string{"attempts >= 0", "attempts <= 1"}
	default:
		return false
	}
	if check.kind != "CHECK" || check.enforced != "YES" || expected != strings.Join(atoms, " AND ") {
		return false
	}
	actual, ok := normalizeMigrationCheck(check.clause)
	return ok && currencyMigrationConjunction(actual, atoms)
}
