package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"errors"
	"strings"
)

var commerceCatalogColumns = map[string]string{
	"products":               "id:varchar(64)! slug:varchar(64)! kind:enum('cloud_server','dedicated_server','generic')! node_id:varchar(64)! name_i18n:json! description_i18n:json! specs:json! badges:json! stock:bigint? visibility:enum('public','hidden','retired')! controller_id:varchar(64)? remote_product_id:varchar(128)? sort_order:bigint! revision:bigint!",
	"product_prices":         "product_id:varchar(64)! currency:char(3)! cycle:varchar(16)! recurring_minor:bigint! setup_minor:bigint! decimal_places:int!",
	"product_options":        "product_id:varchar(64)! id:varchar(64)! option_key:varchar(48)! kind:enum('select','quantity','os_image')! label_i18n:json! required:tinyint(1)! min_quantity:bigint? max_quantity:bigint? sort_order:int!",
	"product_option_choices": "product_id:varchar(64)! option_id:varchar(64)! id:varchar(64)! label_i18n:json! sort_order:int!",
	"option_prices":          "product_id:varchar(64)! option_id:varchar(64)! choice_id:varchar(64)! currency:char(3)! cycle:varchar(16)! recurring_minor:bigint! setup_minor:bigint! decimal_places:int!",
	"os_images":              "id:varchar(64)! family:varchar(16)! name:varchar(80)! version:varchar(40)! min_disk_gb:bigint! enabled:tinyint(1)! controller_id:varchar(64)! remote_image_id:varchar(128)! sha256:varchar(64)!",
	"product_os_images":      "product_id:varchar(64)! image_id:varchar(64)!",
	"commerce_requests":      "user_id:char(32)! operation:varchar(128)! request_key:varchar(128)! fingerprint:char(64)! response:json! created_at:datetime(6)!",
	"commerce_taxonomy":      "id:tinyint! document:json!",
}
var commerceCatalogKeys = []string{"products:id", "products:slug", "product_prices:product_id,currency,cycle", "product_options:product_id,id", "product_options:product_id,option_key", "product_option_choices:product_id,option_id,id", "option_prices:product_id,option_id,choice_id,currency,cycle", "os_images:id", "product_os_images:product_id,image_id", "commerce_requests:user_id,operation,request_key", "commerce_taxonomy:id"}
var commerceCatalogFKs = []string{"products:node_id:catalog_nodes:id:RESTRICT", "product_prices:product_id:products:id:RESTRICT", "product_options:product_id:products:id:RESTRICT", "product_option_choices:product_id:products:id:RESTRICT", "option_prices:product_id:products:id:RESTRICT", "product_os_images:product_id:products:id:RESTRICT", "product_os_images:image_id:os_images:id:RESTRICT", "commerce_requests:user_id:users:id:CASCADE"}

func commerceSchemaColumns(version int, specs map[string]string) {
	if !schemaIncludes(version, migrations.CommerceCatalog) {
		return
	}
	for table, spec := range commerceCatalogColumns {
		specs[table] = spec
	}
}
func commerceSchemaChecks(version int, checks map[string]map[string]string) {
	if !schemaIncludes(version, migrations.CommerceCatalog) {
		return
	}
	checks["products"] = map[string]string{"product_stock_min": "stock >= 0", "product_stock_max": "stock <= 9007199254740991", "product_revision_min": "revision >= 1", "product_revision_max": "revision <= 9007199254740991"}
	for table, prefix := range map[string]string{"product_prices": "product_price", "option_prices": "option_price"} {
		checks[table] = map[string]string{prefix + "_recurring_min": "recurring_minor >= 0", prefix + "_recurring_max": "recurring_minor <= 9007199254740991", prefix + "_setup_min": "setup_minor >= 0", prefix + "_setup_max": "setup_minor <= 9007199254740991", prefix + "_precision_min": "decimal_places >= 0", prefix + "_precision_max": "decimal_places <= 6"}
	}
	checks["os_images"] = map[string]string{"os_image_min_disk": "min_disk_gb >= 1"}
	checks["commerce_taxonomy"] = map[string]string{"commerce_taxonomy_singleton": "id = 1"}
}
func prepareCommerceCatalogMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	for table := range commerceCatalogColumns {
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
			return "", false, errors.New("unexpected commerce catalog object")
		}
		if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
			return "", false, err
		}
		return q, true, nil
	}
	// Backfills are INSERT ... SELECT guarded by NOT EXISTS, with no mutation of
	// already migrated product records. No historical services are synthesized.
	if strings.HasPrefix(q, "INSERT INTO products(") || strings.HasPrefix(q, "INSERT INTO product_prices(") {
		return q, false, nil
	}
	return "", false, errors.New("unsupported commerce catalog migration")
}
