package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var commerceLedgerColumns = map[string]string{
	"credit_ledger": "user_id:char(32)! sequence:bigint! id:char(32)! kind:varchar(24)! amount_minor:bigint! balance_after_minor:bigint! currency:char(3)! decimal_places:int! invoice_id:char(32)? description:varchar(500)! created_at:datetime(6)!",
	"credit_limits": "currency:char(3)! min_minor:bigint! max_minor:bigint! presets:json!",
}

// New commerce CREATE statements use the same exact inventory validator as the
// upgrade preflight, including keys, checks and foreign keys on a resumed DDL.
func prepareCommerceTables(ctx context.Context, conn *sql.Conn, version int, q string, tables map[string]string) (string, bool, error) {
	for table := range tables {
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
			return "", false, errors.New("unexpected commerce schema object")
		}
		if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	return "", false, errors.New("unsupported commerce table migration")
}
