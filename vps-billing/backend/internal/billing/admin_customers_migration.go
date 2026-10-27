package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// DDL commits individually on MySQL. Inspect all colliding objects before any
// statement is applied, then resume only byte-for-byte known logical shapes.
func prepareCustomerAccountMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	columns := map[string]struct{ statement, kind, def string }{
		"disabled": {"ALTER TABLE users ADD COLUMN disabled BOOLEAN NOT NULL DEFAULT FALSE", "tinyint(1)", "0"},
		"revision": {"ALTER TABLE users ADD COLUMN revision BIGINT NOT NULL DEFAULT 1", "bigint", "1"},
	}
	for name, want := range columns {
		if q != want.statement {
			continue
		}
		var kind, nullable, extra, generated string
		var def, charset, collation sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME=?", name).Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if kind != want.kind || nullable != "NO" || extra != "" || generated != "" || !def.Valid || def.String != want.def || charset.Valid || collation.Valid {
			return "", false, errors.New("existing customer account column differs from migration")
		}
		return q, true, nil
	}
	for _, check := range []string{"ALTER TABLE users ADD CONSTRAINT users_disabled_boolean CHECK(disabled IN (0,1))", "ALTER TABLE users ADD CONSTRAINT users_revision_min CHECK(revision >= 1)", "ALTER TABLE users ADD CONSTRAINT users_revision_max CHECK(revision <= 9007199254740991)"} {
		if q == check {
			return prepareMigrationStatement(ctx, conn, 0, q)
		}
	}
	return "", false, errors.New("unsupported customer account migration")
}
func prevalidateCustomerAccountMigration(ctx context.Context, conn *sql.Conn, source string) error {
	for _, q := range strings.Split(source, ";") {
		if q = strings.TrimSpace(q); q != "" {
			if _, _, err := prepareCustomerAccountMigration(ctx, conn, q); err != nil {
				return err
			}
		}
	}
	return nil
}
