package billing

import (
	"context"
	"database/sql"
	"strings"
)

var automationColumns = map[string]string{
	"automation_settings": "id:int! document:json!",
	"automation_runs":     "id:char(32)! job:varchar(32)! scheduled_at:datetime(6)! status:varchar(16)! started_at:datetime(6)! finished_at:datetime(6)? processed:bigint! failed:bigint! message:varchar(500)?",
}

func prepareAutomationMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	if strings.HasPrefix(q, "INSERT IGNORE INTO automation_settings(id,document) VALUES(1,") {
		return q, false, nil
	}
	return prepareCommerceTables(ctx, conn, version, q, automationColumns)
}
