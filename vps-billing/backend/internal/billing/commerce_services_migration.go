package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"strings"
)

var commerceServiceColumns = map[string]string{
	"service_units":           "order_item_id:char(32)! unit_index:int! service_id:char(32)!",
	"service_tasks":           "id:char(32)! service_id:char(32)! action:varchar(32)! status:enum('queued','running','succeeded','failed','uncertain')! operation_id:varchar(128)? message:varchar(500)? payload:json! created_at:datetime(6)! updated_at:datetime(6)! finished_at:datetime(6)?",
	"invoice_service_changes": "invoice_id:char(32)! service_id:char(32)! kind:enum('renewal','upgrade')! period_start:datetime(6)? period_end:datetime(6)? payload:json!",
	"invoice_notifications":   "invoice_id:char(32)! stage:varchar(64)! created_at:datetime(6)!",
}
var commerceServiceKeys = []string{"service_units:order_item_id,unit_index", "service_units:service_id", "service_tasks:id", "service_tasks:operation_id", "invoice_service_changes:invoice_id", "invoice_service_changes:service_id,kind,period_start", "invoice_notifications:invoice_id,stage"}
var commerceServiceFKs = []string{"service_units:order_item_id:order_items:id:RESTRICT", "service_units:service_id:services:id:RESTRICT", "service_tasks:service_id:services:id:RESTRICT", "invoice_service_changes:invoice_id:invoices:id:RESTRICT", "invoice_service_changes:service_id:services:id:RESTRICT", "invoice_notifications:invoice_id:invoices:id:RESTRICT"}

func commerceServicesSchema(version int, specs map[string]string) {
	if !schemaIncludes(version, migrations.CommerceServices) {
		return
	}
	for k, v := range commerceServiceColumns {
		specs[k] = v
	}
	specs["services"] = strings.ReplaceAll(specs["services"], "status:enum('pending','active','suspended','terminated')!", "status:enum('pending','active','suspended','terminated','provisioning','cancelled','failed')!")
	specs["services"] = strings.ReplaceAll(specs["services"], "plan_name:varchar(120)!", "plan_name:varchar(4000)!")
	specs["services"] += " product_id:varchar(64)? cycle:varchar(16)! recurring_minor:bigint! price_override_minor:bigint? next_invoice_at:datetime(6)? auto_renew:tinyint(1)! label:varchar(80)! controller_id:varchar(64)? resource_id:varchar(128)? suspension_reason:varchar(500)? cancellation_type:varchar(16)? cancellation_reason:varchar(500)? cancellation_requested_at:datetime(6)? terminates_at:datetime(6)? specs_snapshot:json? os_snapshot:json? configuration_snapshot:json? created_at:datetime(6)? revision:bigint! anchor_day:int! last_change_at:datetime(6)?"
}
func commerceServicesChecks(version int, checks map[string]map[string]string) {
	if !schemaIncludes(version, migrations.CommerceServices) {
		return
	}
	for k, v := range map[string]string{"service_recurring_money": "recurring_minor >= 0 AND recurring_minor <= 9007199254740991", "service_override_money": "price_override_minor >= 0 AND price_override_minor <= 9007199254740991", "service_revision": "revision >= 1 AND revision <= 9007199254740991", "service_anchor_day": "anchor_day >= 1 AND anchor_day <= 31", "service_auto_renew": "auto_renew IN (0,1)"} {
		checks["services"][k] = v
	}
	checks["service_units"] = map[string]string{"service_unit_index": "unit_index >= 0 AND unit_index < 20"}
}
func prepareCommerceServicesMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	if strings.HasPrefix(q, "ALTER TABLE services ") {
		if strings.Contains(q, " ADD CONSTRAINT ") {
			return prepareCommerceCheck(ctx, conn, q)
		}
		return prepareCommerceColumn(ctx, conn, version, q)
	}
	if strings.HasPrefix(q, "UPDATE services SET recurring_minor=") {
		return q, false, nil
	}
	return prepareCommerceTables(ctx, conn, version, q, commerceServiceColumns)
}
