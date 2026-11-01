package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

func prepareGatewayMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	for _, table := range []string{"payment_gateway_settings"} {
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
			return "", false, errors.New("unexpected gateway settings object")
		}
		if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	return q, false, nil
}
