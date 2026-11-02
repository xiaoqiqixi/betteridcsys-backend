package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"strings"
)

var serverModuleColumns = map[string]string{"server_connections": "id:varchar(64)! module_id:varchar(64)! name:varchar(80)! enabled:tinyint(1)! status:varchar(16)! description:json? last_checked_at:datetime(6)? revision:bigint!"}

func serverModulesSchema(version int, specs map[string]string) {
	if !schemaIncludes(version, migrations.ServerModules) {
		return
	}
	for k, v := range serverModuleColumns {
		specs[k] = v
	}
	specs["products"] += " module:json? order_schema:json?"
	specs["services"] += " module_id:varchar(64)? connection_id:varchar(64)?"
	specs["os_images"] += " connection_id:varchar(64)!"
}
func prepareServerModulesMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	if strings.HasPrefix(q, "ALTER TABLE ") {
		return prepareCommerceColumn(ctx, conn, version, q)
	}
	if strings.HasPrefix(q, "UPDATE os_images SET connection_id=") {
		return q, false, nil
	}
	return prepareCommerceTables(ctx, conn, version, q, serverModuleColumns)
}
