package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var auditIndexColumns = map[string][]string{
	"ix_audit_date_id":        {"created_at", "id"},
	"ix_audit_actor_date_id":  {"actor_email", "created_at", "id"},
	"ix_audit_action_date_id": {"action", "created_at", "id"},
}

func inspectAuditIndex(ctx context.Context, conn *sql.Conn, name string) (bool, error) {
	columns, ok := auditIndexColumns[name]
	if !ok {
		return false, errors.New("unknown audit index")
	}
	rows, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME,NON_UNIQUE,SEQ_IN_INDEX,SUB_PART,EXPRESSION,IS_VISIBLE,INDEX_TYPE,COLLATION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='audit_events' AND INDEX_NAME=? ORDER BY SEQ_IN_INDEX LIMIT 5", name)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var column, expression, collation sql.NullString
		var prefix sql.NullInt64
		var unique, sequence int
		var visible, kind string
		if err := rows.Scan(&column, &unique, &sequence, &prefix, &expression, &visible, &kind, &collation); err != nil {
			return false, err
		}
		if count >= len(columns) || !column.Valid || column.String != columns[count] || unique != 1 || sequence != count+1 || prefix.Valid || expression.Valid || visible != "YES" || kind != "BTREE" || !collation.Valid || collation.String != "A" {
			return false, errors.New("existing audit index differs from migration")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if count != 0 && count != len(columns) {
		return false, errors.New("existing audit index has an unexpected column count")
	}
	return count != 0, nil
}

func prepareAuditIndex(ctx context.Context, conn *sql.Conn, statement string) (string, bool, error) {
	for name, columns := range auditIndexColumns {
		if statement != "CREATE INDEX "+name+" ON audit_events("+strings.Join(columns, ",")+")" {
			continue
		}
		exists, err := inspectAuditIndex(ctx, conn, name)
		return statement, exists, err
	}
	return "", false, errors.New("unsupported audit index migration")
}

func validateAuditIndexes(ctx context.Context, conn *sql.Conn) error {
	for name := range auditIndexColumns {
		exists, err := inspectAuditIndex(ctx, conn, name)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("required audit query index is missing")
		}
	}
	return nil
}
