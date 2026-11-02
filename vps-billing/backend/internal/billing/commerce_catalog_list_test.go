package billing

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestMySQLCommerceCatalogBatchesMatchDetailReads(t *testing.T) {
	f := mysqlCatalogFixture(t)
	m := f.store.(*MySQL)
	input := product06Fixture(t, f)
	p, err := m.SaveProduct06(t.Context(), f.admin, "", input, "batch-product-fixture", false)
	if err != nil {
		t.Fatal(err)
	}
	// Cross the batch boundary and retain mixed hidden/public products. Detail
	// reads remain the independent oracle for ordering, prices and availability.
	for i := range 101 {
		id := fmt.Sprintf("batch-product-%03d", i)
		migrationExec(t, t.Context(), m.DB, `INSERT INTO products(id,slug,kind,node_id,name_i18n,description_i18n,specs,badges,stock,visibility,sort_order,revision)
 SELECT ?,?,kind,node_id,name_i18n,description_i18n,specs,badges,stock,IF(?=100,'hidden','public'),?,1 FROM products WHERE id=?`, id, id, i, i, p.ID)
		migrationExec(t, t.Context(), m.DB, `INSERT INTO product_prices SELECT ?,currency,cycle,recurring_minor,setup_minor,decimal_places FROM product_prices WHERE product_id=?`, id, p.ID)
	}
	// Case-insensitive child keys must still attach to canonical parent IDs.
	pid := strings.ToUpper(p.ID)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO product_options VALUES(?, 'Storage','storage','quantity',JSON_OBJECT('en','Storage'),FALSE,0,10,0),(?,'Size','size','select',JSON_OBJECT('en','Size'),TRUE,NULL,NULL,1)`, pid, pid)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO product_option_choices VALUES(?,'SIZE','small',JSON_OBJECT('en','Small'),1),(?,'size','large',JSON_OBJECT('en','Large'),0)`, pid, pid)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO option_prices VALUES(?,'STORAGE','','CNY','monthly',11,1,2),(?,'size','SMALL','CNY','monthly',22,2,2),(?,'SIZE','LARGE','CNY','monthly',33,3,2),(?,'storage','','ZZZ','monthly',44,4,3)`, pid, pid, pid, pid)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO product_prices VALUES(?,'ZZZ','monthly',100,1,3)`, pid)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO os_images VALUES('batch-image','ubuntu','Ubuntu','24.04',10,TRUE,'test','image',REPEAT('a',64)),('batch-disabled','debian','Debian','12',10,FALSE,'test','image',REPEAT('b',64))`)
	migrationExec(t, t.Context(), m.DB, `INSERT INTO product_os_images VALUES(?,'batch-image'),(?,'batch-disabled')`, pid, pid)
	for i, status := range []string{"active", "suspended", "terminated", "cancelled"} {
		migrationExec(t, t.Context(), m.DB, `INSERT INTO services(id,user_id,name,plan_name,region,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,product_id)
 VALUES(?,?,'batch.invalid','Test','test',?,UTC_TIMESTAMP(6),999,'CNY',1,1,20,?)`, fmt.Sprintf("%032d", i+1), f.customer.ID, status, pid)
	}
	for _, admin := range []bool{false, true} {
		for _, currency := range []string{"", "CNY"} {
			got, err := m.Products06(t.Context(), admin, currency, "", "")
			if err != nil || len(got) < 100 {
				t.Fatal("list fixture did not cross batch boundary", len(got), err)
			}
			for _, product := range got {
				want, err := readProducts06(t.Context(), m.DB, admin, product.ID, currency, "", "", "")
				if err != nil || len(want) != 1 || !reflect.DeepEqual(product, want[0]) {
					t.Fatalf("batch/detail mismatch for %s admin=%v currency=%s: %v\ngot: %#v\nwant: %#v", product.ID, admin, currency, err, product, want)
				}
				if product.ID == p.ID && product.ActiveServices != 2 {
					t.Fatal("service counts changed", product.ActiveServices)
				}
			}
		}
	}
	if out, err := m.Products06(t.Context(), false, "CNY", "missing-type", ""); err != nil || len(out) != 0 {
		t.Fatal("empty filtered catalog", len(out), err)
	}
}
