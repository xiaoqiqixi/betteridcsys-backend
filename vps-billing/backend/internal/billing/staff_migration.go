package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"errors"
	"strings"
)

func schemaIncludes(version int, source string) bool {
	for i, s := range schemaVersions {
		if s == source {
			return version >= i+1
		}
	}
	return false
}

func prepareStaffMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	for _, table := range []string{"staff_roles", "staff_members"} {
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
			return "", false, errors.New("unexpected staff schema object")
		}
		if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	return q, false, nil
}

func staffSchemaColumns(version int, specs map[string]string) {
	if !schemaIncludes(version, migrations.StaffRoles) {
		return
	}
	specs["staff_roles"] = "id:varchar(64)! name:varchar(60)! description:varchar(240)! permissions:json! builtin:tinyint(1)! revision:bigint!"
	specs["staff_members"] = "user_id:char(32)! role_id:varchar(64)! last_login_at:datetime(6)?"
}
