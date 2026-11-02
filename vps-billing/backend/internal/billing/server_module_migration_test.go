package billing

import (
	"aster/billing/plugins/server.aster-cloud/migration"
	"encoding/json"
	"testing"
)

func TestMySQLCommerceControllerMigrationKeepsOneConnectionAndRefusesUncertainTasks(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	sid, p := paidService06(t, f)
	if _, err := m.DB.Exec(`UPDATE products SET controller_id='old',remote_product_id='current-plan' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.DB.Exec(`UPDATE services SET controller_id='old',configuration_snapshot=JSON_SET(configuration_snapshot,'$.controller_id','old','$.remote_product_id','purchased-plan') WHERE id=?`, sid); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"schema":1,"controllers":[{"id":"old","enabled":true,"endpoint":"https://controller.example.test/v1/provisioning","allowed_cidrs":["192.0.2.0/24"],"token_file":"/etc/aster/token","ca_file":""}]}`)
	plan, _, err := migration.PlanControllerMigration(raw, "old", "direct")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.DB.Exec(`UPDATE service_tasks SET status='uncertain' WHERE service_id=?`, sid); err != nil {
		t.Fatal(err)
	}
	if err = migration.MigrateControllerDatabase(t.Context(), m.DB, plan); err == nil {
		t.Fatal("uncertain operation was migrated")
	}
	var count int
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM server_connections").Scan(&count); err != nil || count != 0 {
		t.Fatal("partial migration", count, err)
	}
	if _, err = m.DB.Exec(`UPDATE service_tasks SET status='queued' WHERE service_id=?`, sid); err != nil {
		t.Fatal(err)
	}
	if err = migration.MigrateControllerDatabase(t.Context(), m.DB, plan); err != nil {
		t.Fatal(err)
	}
	if err = m.DB.QueryRow("SELECT COUNT(*) FROM server_connections WHERE id='direct' AND module_id='server.aster-cloud'").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	var module, connection string
	var config []byte
	if err = m.DB.QueryRow("SELECT module_id,connection_id,configuration_snapshot FROM services WHERE id=?", sid).Scan(&module, &connection, &config); err != nil {
		t.Fatal(err)
	}
	var snapshot serviceConfiguration06
	if err = json.Unmarshal(config, &snapshot); err != nil || module != "server.aster-cloud" || connection != "direct" || snapshot.Module.Config["preset_id"] != "purchased-plan" {
		t.Fatal("purchased snapshot changed", err)
	}
	if err = migration.MigrateControllerDatabase(t.Context(), m.DB, plan); err == nil {
		t.Fatal("existing connection overwritten")
	}
}
