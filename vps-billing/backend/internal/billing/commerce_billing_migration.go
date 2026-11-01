package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"errors"
	"strings"
)

var commerceBillingColumns = map[string]string{
	"order_items":      "id:char(32)! order_id:char(32)! position:int! product_id:varchar(64)! product_name:varchar(4000)! cycle:varchar(16)! quantity:int! hostname:varchar(253)? options_summary:text! recurring_minor:bigint! total_minor:bigint! stock_reserved:int! snapshot:json!",
	"invoice_lines":    "id:char(32)! invoice_id:char(32)! position:int! service_id:char(32)? description:text! amount_minor:bigint! period_text:varchar(120)? period_start:datetime(6)?",
	"invoice_payments": "id:char(32)! invoice_id:char(32)! attempt_id:char(32)? method:varchar(24)! gateway:varchar(64)? reference:varchar(128)? amount_minor:bigint! refunded_minor:bigint! status:varchar(24)! created_at:datetime(6)!",
	"invoice_refunds":  "id:char(32)! invoice_id:char(32)! payment_id:char(32)! amount_minor:bigint! destination:varchar(16)! status:varchar(16)! reason:varchar(500)! provider_reference:varchar(128)? created_at:datetime(6)! updated_at:datetime(6)!",
}

func commerceBillingSchema(version int, specs map[string]string) {
	if !schemaIncludes(version, migrations.CommerceBilling) {
		return
	}
	for k, v := range commerceBillingColumns {
		specs[k] = v
	}
	specs["orders"] = strings.ReplaceAll(specs["orders"], "plan_id:varchar(64)!", "plan_id:varchar(64)?")
	specs["orders"] = strings.ReplaceAll(specs["orders"], "status:enum('pending_payment','paid','cancelled')!", "status:enum('pending_payment','paid','cancelled','active','fraud')!")
	specs["orders"] += " coupon_code:varchar(64)? paid_at:datetime(6)? cancelled_at:datetime(6)?"
	specs["invoices"] = strings.ReplaceAll(specs["invoices"], "order_id:char(32)!", "order_id:char(32)?")
	specs["invoices"] = strings.ReplaceAll(specs["invoices"], "status:enum('unpaid','paid','void')!", "status:enum('unpaid','paid','void','cancelled','refunded','partially_refunded')!")
	specs["invoices"] += " kind:enum('order','renewal','upgrade','top_up','manual')! commerce_version:int! subtotal_minor:bigint! credit_applied_minor:bigint! amount_paid_minor:bigint! refunded_minor:bigint! billed_to_snapshot:json?"
	specs["payment_attempts"] = strings.ReplaceAll(specs["payment_attempts"], "order_id:char(32)!", "order_id:char(32)?")
}
func commerceBillingChecks(version int, checks map[string]map[string]string) {
	if !schemaIncludes(version, migrations.CommerceBilling) {
		return
	}
	checks["invoices"]["invoice_commerce_money"] = "subtotal_minor >= 0 AND subtotal_minor <= 9007199254740991 AND credit_applied_minor >= 0 AND amount_paid_minor >= 0 AND refunded_minor >= 0 AND refunded_minor <= credit_applied_minor + amount_paid_minor AND credit_applied_minor + amount_paid_minor <= amount_minor"
	checks["order_items"] = map[string]string{"order_item_quantity": "quantity >= 1 AND quantity <= 20 AND stock_reserved >= 0 AND stock_reserved <= quantity", "order_item_money": "recurring_minor >= 0 AND recurring_minor <= 9007199254740991 AND total_minor >= 0 AND total_minor <= 9007199254740991"}
	checks["invoice_lines"] = map[string]string{"invoice_line_money": "amount_minor >= -9007199254740991 AND amount_minor <= 9007199254740991"}
	checks["invoice_payments"] = map[string]string{"invoice_payment_money": "amount_minor >= 1 AND amount_minor <= 9007199254740991 AND refunded_minor >= 0 AND refunded_minor <= amount_minor"}
	checks["invoice_refunds"] = map[string]string{"invoice_refund_money": "amount_minor >= 1 AND amount_minor <= 9007199254740991"}
}
func prepareCommerceColumn(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	fields := strings.Fields(q)
	if len(fields) < 7 || fields[0] != "ALTER" || fields[1] != "TABLE" || (fields[3] != "ADD" && fields[3] != "MODIFY") || fields[4] != "COLUMN" {
		return "", false, errors.New("unsupported commerce column migration")
	}
	table, column := fields[2], fields[5]
	var kind, nullable, extra, generated, dbCollation string
	var def, charset, collation sql.NullString
	err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME,@@collation_database FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?", table, column).Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation, &dbCollation)
	if errors.Is(err, sql.ErrNoRows) && fields[3] == "ADD" {
		return q, false, nil
	}
	if err != nil {
		return "", false, err
	}
	match := func(spec string) bool {
		return spec != "" && preflightColumnMatches(table, column, spec, kind, nullable, extra, generated, def, charset, collation, dbCollation)
	}
	if match(preflightColumns(version)[table][column]) {
		return q, true, nil
	}
	if fields[3] == "MODIFY" && match(preflightColumns(version - 1)[table][column]) {
		return q, false, nil
	}
	return "", false, errors.New("commerce column differs from its migration")
}
func prepareCommerceBillingMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	if strings.HasPrefix(q, "ALTER TABLE ") {
		if strings.Contains(q, " ADD CONSTRAINT ") {
			return prepareCommerceCheck(ctx, conn, q)
		}
		return prepareCommerceColumn(ctx, conn, version, q)
	}
	if strings.HasPrefix(q, "UPDATE invoices SET ") || strings.HasPrefix(q, "UPDATE orders o JOIN ") || strings.HasPrefix(q, "INSERT IGNORE INTO order_items(") || strings.HasPrefix(q, "INSERT IGNORE INTO invoice_lines(") {
		return q, false, nil
	}
	return prepareCommerceTables(ctx, conn, version, q, commerceBillingColumns)
}

func prepareCommerceCheck(ctx context.Context, conn *sql.Conn, q string) (string, bool, error) {
	fields := strings.Fields(q)
	at := strings.Index(q, " CHECK(")
	if len(fields) < 7 || at < 0 {
		return "", false, errors.New("invalid commerce constraint statement")
	}
	var check currencyMigrationCheck
	err := conn.QueryRowContext(ctx, "SELECT tc.CONSTRAINT_TYPE,tc.ENFORCED,cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.TABLE_NAME=? AND tc.CONSTRAINT_NAME=?", fields[2], fields[5]).Scan(&check.kind, &check.enforced, &check.clause)
	if errors.Is(err, sql.ErrNoRows) {
		return q, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !commerceCheckMatches(check, q[at+len(" CHECK"):]) {
		return "", false, errors.New("commerce constraint differs from migration")
	}
	return q, true, nil
}
