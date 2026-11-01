package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"errors"
	"strings"
)

func validatePaymentHistoryIndex(ctx context.Context, conn *sql.Conn) error {
	var total, valid int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(INDEX_NAME='payment_attempts_invoice_history' AND COLUMN_NAME='invoice_id' AND NON_UNIQUE=1 AND SEQ_IN_INDEX=1 AND SUB_PART IS NULL AND EXPRESSION IS NULL AND IS_VISIBLE='YES'),0) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' AND INDEX_NAME IN ('payment_attempts_invoice_history','payment_attempts_invoice')`).Scan(&total, &valid)
	if err != nil {
		return err
	}
	if total != 1 || valid != 1 {
		return errors.New("payment invoice history index differs from migration")
	}
	return paymentInvoiceUniqueCount(ctx, conn, 0)
}

// Validate every changed object before any DDL, and accept only complete
// old/new shapes when resuming MySQL's independently committed statements.
func preparePaymentResolutionMigration(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	statements := strings.Split(strings.TrimSpace(migrations.PaymentResolution), ";")
	if q == strings.TrimSpace(statements[0]) {
		var state, collation, engine string
		if err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,@@collation_database FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' AND COLUMN_NAME='state'").Scan(&state, &collation); err != nil {
			return "", false, err
		}
		if err := conn.QueryRowContext(ctx, "SELECT ENGINE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' AND TABLE_TYPE='BASE TABLE'").Scan(&engine); err != nil {
			return "", false, err
		}
		version := 13
		if state == "enum('pending','checkout','uncertain','settled','expired','voided')" {
			version = 14
		}
		if err := validatePreflightTableShape(ctx, conn, version, map[string]string{"payment_attempts": engine}, collation, true); err != nil {
			return "", false, err
		}
		if version == 13 {
			var oldTotal, oldValid int
			if err := conn.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(COLUMN_NAME='invoice_id' AND NON_UNIQUE=0 AND SEQ_IN_INDEX=1 AND SUB_PART IS NULL AND EXPRESSION IS NULL AND IS_VISIBLE='YES'),0) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' AND INDEX_NAME='payment_attempts_invoice'`).Scan(&oldTotal, &oldValid); err != nil {
				return "", false, err
			}
			if oldTotal != 1 || oldValid != 1 {
				return "", false, errors.New("original invoice reservation index differs from migration")
			}
			if err := paymentInvoiceUniqueCount(ctx, conn, 1); err != nil {
				return "", false, err
			}
			var collisions int
			if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' AND INDEX_NAME='payment_attempts_invoice_history'").Scan(&collisions); err != nil {
				return "", false, err
			}
			if collisions != 0 {
				return "", false, errors.New("colliding payment history index")
			}
		}
		return q, version == 14, nil
	}
	for _, column := range []struct{ table, name, statement string }{
		{"payment_settlements", "disposition", strings.TrimSpace(statements[1])},
		{"invoices", "settlement_review", strings.TrimSpace(statements[2])},
	} {
		if q != column.statement {
			continue
		}
		var kind, nullable, extra, generated, databaseCollation string
		var def, charset, collation sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME,@@collation_database FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?", column.table, column.name).Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation, &databaseCollation)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if !preflightColumnMatches(column.table, column.name, preflightColumns(14)[column.table][column.name], kind, nullable, extra, generated, def, charset, collation, databaseCollation) {
			return "", false, errors.New("payment resolution column differs from migration")
		}
		return q, true, nil
	}
	if q == strings.TrimSpace(statements[3]) {
		return prepareMigrationStatement(ctx, conn, 0, q)
	}
	return "", false, errors.New("unsupported payment resolution migration")
}

func paymentInvoiceUniqueCount(ctx context.Context, conn *sql.Conn, want int) error {
	var count int
	err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT INDEX_NAME FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='payment_attempts' GROUP BY INDEX_NAME HAVING COUNT(*)=1 AND MAX(NON_UNIQUE)=0 AND MAX(COLUMN_NAME)='invoice_id') AS invoice_keys`).Scan(&count)
	if err != nil {
		return err
	}
	if count != want {
		return errors.New("unexpected invoice-only payment uniqueness")
	}
	return nil
}
