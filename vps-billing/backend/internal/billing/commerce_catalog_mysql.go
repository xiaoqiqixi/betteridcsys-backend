package billing

import (
	api "aster/billing/internal/financeapi"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const product06Columns = `p.id,p.slug,p.kind,r.type_id,n.region_id,p.node_id,p.name_i18n,p.description_i18n,p.badges,p.specs,p.stock,p.visibility,p.module,p.order_schema,p.sort_order,p.revision,(n.enabled AND r.enabled AND t.enabled AND (p.module IS NULL OR JSON_TYPE(p.module)='NULL' OR EXISTS(SELECT 1 FROM server_connections c WHERE c.id=JSON_UNQUOTE(JSON_EXTRACT(p.module,'$.connection_id')) AND c.module_id=JSON_UNQUOTE(JSON_EXTRACT(p.module,'$.module_id')) AND c.enabled=TRUE))),(SELECT COUNT(*) FROM services s WHERE s.product_id=p.id AND s.status NOT IN ('terminated','cancelled'))`
const product06Joins = ` FROM products p JOIN catalog_nodes n ON n.id=p.node_id JOIN catalog_regions r ON r.id=n.region_id JOIN catalog_types t ON t.id=r.type_id`

// Materialize service counts once for a catalog list. The detail/locking path
// retains its existing query and lock scope.
var productList06Columns = strings.Replace(product06Columns, "(SELECT COUNT(*) FROM services s WHERE s.product_id=p.id AND s.status NOT IN ('terminated','cancelled'))", "COALESCE(service_counts.active_services,0)", 1)

const productList06Joins = product06Joins + ` LEFT JOIN (SELECT product_id,COUNT(*) active_services FROM services WHERE status NOT IN ('terminated','cancelled') GROUP BY product_id) service_counts ON service_counts.product_id=p.id`

func readProducts06(ctx context.Context, q currencyQuerier, admin bool, id, currency, typeID, regionID, lock string) ([]api.AdminProduct, error) {
	query := "SELECT " + product06Columns + product06Joins + " WHERE 1=1"
	batched := id == "" && lock == ""
	if batched {
		query = "SELECT " + productList06Columns + productList06Joins + " WHERE 1=1"
	}
	args := []any{}
	if !admin {
		query += " AND p.visibility='public' AND n.enabled=TRUE AND r.enabled=TRUE AND t.enabled=TRUE"
	}
	for _, filter := range []struct{ column, value string }{{"p.id", id}, {"r.type_id", typeID}, {"n.region_id", regionID}} {
		if filter.value != "" {
			query += " AND BINARY " + filter.column + "=BINARY ?"
			args = append(args, filter.value)
		}
	}
	query += " ORDER BY p.sort_order,p.id" + lock
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	out := []api.AdminProduct{}
	for rows.Next() {
		var p api.AdminProduct
		var name, description, badges, specs, module, orderSchema []byte
		var visible bool
		if err = rows.Scan(&p.ID, &p.Slug, &p.Kind, &p.TypeID, &p.RegionID, &p.NodeID, &name, &description, &badges, &specs, &p.Stock, &p.Visibility, &module, &orderSchema, &p.SortOrder, &p.Revision, &visible, &p.ActiveServices); err != nil {
			rows.Close()
			return nil, err
		}
		if len(module) == 0 {
			module = []byte("null")
		}
		if len(orderSchema) == 0 {
			orderSchema = []byte("null")
		}
		for _, v := range []struct {
			raw    []byte
			target any
		}{{name, &p.Name}, {description, &p.Description}, {badges, &p.Badges}, {specs, &p.Specs}, {module, &p.Module}, {orderSchema, &p.OrderSchema}} {
			if err = json.Unmarshal(v.raw, v.target); err != nil {
				rows.Close()
				return nil, err
			}
		}
		p.Prices = []api.PricePoint{}
		p.Options = []api.ProductOption{}
		p.OSImages = []api.OsImage{}
		p.Available = visible && p.Visibility == "public" && (p.Stock == nil || *p.Stock > 0)
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	config, _, err := readCurrencies(ctx, q, "")
	if err != nil {
		return nil, err
	}
	if currency != "" {
		if c, ok := config.find(currency); !ok || !c.Enabled {
			return nil, ErrCommerceInput
		}
	}
	if batched {
		return readProductListChildren06(ctx, q, out, currency, config, admin)
	}
	for i := range out {
		p := &out[i]
		prices, err := readPrices06(ctx, q, p.ID, "", "", currency, config, admin)
		if err != nil {
			return nil, err
		}
		p.Prices = prices
		p.Available = p.Available && len(p.Prices) > 0
		rows, err = q.QueryContext(ctx, "SELECT id,option_key,kind,label_i18n,required,min_quantity,max_quantity FROM product_options WHERE product_id=? ORDER BY sort_order,id", p.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o api.ProductOption
			var raw []byte
			var minq, maxq sql.NullInt64
			if err = rows.Scan(&o.ID, &o.Key, &o.Kind, &raw, &o.Required, &minq, &maxq); err != nil {
				rows.Close()
				return nil, err
			}
			if err = json.Unmarshal(raw, &o.Label); err != nil {
				rows.Close()
				return nil, err
			}
			o.Min, o.Max = minq.Int64, maxq.Int64
			o.Choices = []api.OptionChoice{}
			p.Options = append(p.Options, o)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		for j := range p.Options {
			o := &p.Options[j]
			if o.Kind == "quantity" {
				o.UnitPrices, err = readPrices06(ctx, q, p.ID, o.ID, "", currency, config, admin)
				if err != nil {
					return nil, err
				}
			}
			rows, err = q.QueryContext(ctx, "SELECT id,label_i18n FROM product_option_choices WHERE product_id=? AND option_id=? ORDER BY sort_order,id", p.ID, o.ID)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var c api.OptionChoice
				var raw []byte
				if err = rows.Scan(&c.ID, &raw); err != nil {
					rows.Close()
					return nil, err
				}
				if err = json.Unmarshal(raw, &c.Label); err != nil {
					rows.Close()
					return nil, err
				}
				o.Choices = append(o.Choices, c)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			for k := range o.Choices {
				o.Choices[k].Prices, err = readPrices06(ctx, q, p.ID, o.ID, o.Choices[k].ID, currency, config, admin)
				if err != nil {
					return nil, err
				}
			}
		}
		rows, err = q.QueryContext(ctx, "SELECT i.id,i.family,i.name,i.version,i.min_disk_gb FROM os_images i JOIN product_os_images pi ON pi.image_id=i.id WHERE pi.product_id=? AND (? OR i.enabled=TRUE) ORDER BY i.id", p.ID, admin)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v api.OsImage
			if err = rows.Scan(&v.ID, &v.Family, &v.Name, &v.Version, &v.MinDiskGB); err != nil {
				rows.Close()
				return nil, err
			}
			p.OSImages = append(p.OSImages, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func readPrices06(ctx context.Context, q currencyQuerier, pid, oid, cid, currency string, config CurrencyConfiguration, admin bool) ([]api.PricePoint, error) {
	query := "SELECT cycle,currency,recurring_minor,setup_minor,decimal_places FROM product_prices WHERE product_id=?"
	args := []any{pid}
	if oid != "" {
		query = "SELECT cycle,currency,recurring_minor,setup_minor,decimal_places FROM option_prices WHERE product_id=? AND option_id=? AND choice_id=?"
		args = append(args, oid, cid)
	}
	if currency != "" {
		query += " AND BINARY currency=BINARY ?"
		args = append(args, currency)
	}
	rows, err := q.QueryContext(ctx, query+" ORDER BY currency,cycle", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.PricePoint{}
	for rows.Next() {
		var p api.PricePoint
		if err = rows.Scan(&p.Cycle, &p.Currency, &p.RecurringMinor, &p.SetupMinor, &p.DecimalPlaces); err != nil {
			return nil, err
		}
		c, ok := config.find(p.Currency)
		if admin || (ok && c.Enabled) {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}
func (m *MySQL) Products06(ctx context.Context, admin bool, currency, typeID, regionID string) ([]api.AdminProduct, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := readProducts06(ctx, tx, admin, "", currency, typeID, regionID, "")
	if err != nil {
		return nil, err
	}
	return out, tx.Commit()
}
func (m *MySQL) Product06(ctx context.Context, admin bool, id string) (api.AdminProduct, error) {
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return api.AdminProduct{}, err
	}
	defer tx.Rollback()
	out, err := readProducts06(ctx, tx, admin, id, "", "", "", "")
	if err != nil {
		return api.AdminProduct{}, err
	}
	if len(out) != 1 {
		return api.AdminProduct{}, ErrNotFound
	}
	return out[0], tx.Commit()
}
func requestReplay06(ctx context.Context, tx *sql.Tx, uid, operation, key, fingerprint string, out any) (bool, error) {
	if key == "" {
		return false, nil
	}
	var stored string
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT fingerprint,response FROM commerce_requests WHERE user_id=? AND operation=? AND request_key=?", uid, operation, key).Scan(&stored, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stored != fingerprint {
		return false, ErrConflict
	}
	return true, json.Unmarshal(raw, out)
}
func recordRequest06(ctx context.Context, tx *sql.Tx, uid, operation, key, fingerprint string, out any) error {
	if key == "" {
		return nil
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO commerce_requests(user_id,operation,request_key,fingerprint,response,created_at) VALUES(?,?,?,?,?,?)", uid, operation, key, fingerprint, raw, time.Now().UTC())
	return err
}
func productInput06(p api.AdminProduct) api.AdminCreateProductInput {
	ids := []string{}
	for _, i := range p.OSImages {
		ids = append(ids, i.ID)
	}
	return api.AdminCreateProductInput{Revision: p.Revision, Slug: p.Slug, Kind: p.Kind, NodeID: p.NodeID, Name: p.Name, Description: p.Description, Badges: p.Badges, Specs: p.Specs, Prices: p.Prices, Options: p.Options, OSImageIDs: ids, Stock: p.Stock, Visibility: p.Visibility, Module: productBinding06(p.Module), SortOrder: p.SortOrder}
}
func (m *MySQL) SaveProduct06(ctx context.Context, actor User, id string, in api.AdminCreateProductInput, key string, duplicate bool) (api.AdminProduct, error) {
	var out api.AdminProduct
	raw, _ := json.Marshal(in)
	fingerprint := digest(string(raw))
	operation := "products:create"
	if id != "" {
		operation = "products:" + id
	}
	if duplicate {
		operation += "/duplicate"
		fingerprint = digest("{}")
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	actor, err = lockActiveAdministrator(ctx, tx, actor.ID)
	if err != nil {
		return out, err
	}
	config, priceRevision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return out, err
	}
	if priceRevision >= MaxMinor {
		return out, ErrRevisionConflict
	}
	replay, err := requestReplay06(ctx, tx, actor.ID, operation, key, fingerprint, &out)
	if err != nil || replay {
		return out, err
	}
	taxonomy, err := readTaxonomy(ctx, tx, " FOR SHARE")
	if err != nil {
		return out, err
	}
	revision := int64(1)
	create := id == "" || duplicate
	if id != "" {
		old, err := readProducts06(ctx, tx, true, id, "", "", "", " FOR UPDATE")
		if err != nil {
			return out, err
		}
		if len(old) != 1 {
			return out, ErrNotFound
		}
		if duplicate {
			in = productInput06(old[0])
			in.Slug = "copy-" + ID()
			in.Visibility = "hidden"
		} else {
			if in.Revision != old[0].Revision || in.Revision >= MaxMinor {
				return out, ErrRevisionConflict
			}
			revision = in.Revision + 1
		}
	}
	if err = validateProduct06(in, config, taxonomy); err != nil {
		return out, err
	}
	module, orderSchema, err := normalizeProductModule06(ctx, tx, in.Module)
	if err != nil {
		return out, err
	}
	if create {
		id = ID()
	}
	for _, iid := range in.OSImageIDs {
		var minDisk int64
		var enabled bool
		var connection string
		err = tx.QueryRowContext(ctx, "SELECT min_disk_gb,enabled,connection_id FROM os_images WHERE BINARY id=BINARY ? FOR SHARE", iid).Scan(&minDisk, &enabled, &connection)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (minDisk > in.Specs.DiskGB || in.Visibility == "public" && !enabled || in.Module != nil && connection != in.Module.ConnectionID) {
			return out, ErrCommerceInput
		}
		if err != nil {
			return out, err
		}
	}
	name, _ := json.Marshal(in.Name)
	description, _ := json.Marshal(in.Description)
	specs, _ := json.Marshal(in.Specs)
	badges, _ := json.Marshal(in.Badges)
	if create {
		_, err = tx.ExecContext(ctx, "INSERT INTO products(id,slug,kind,node_id,name_i18n,description_i18n,specs,badges,stock,visibility,module,order_schema,sort_order,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", id, in.Slug, in.Kind, in.NodeID, name, description, specs, badges, in.Stock, in.Visibility, jsonValue06(module), jsonValue06(orderSchema), in.SortOrder, revision)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE products SET slug=?,kind=?,node_id=?,name_i18n=?,description_i18n=?,specs=?,badges=?,stock=?,visibility=?,module=?,order_schema=?,sort_order=?,revision=? WHERE id=?", in.Slug, in.Kind, in.NodeID, name, description, specs, badges, in.Stock, in.Visibility, jsonValue06(module), jsonValue06(orderSchema), in.SortOrder, revision, id)
	}
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return out, ErrCommerceInput
		}
		return out, err
	}
	for _, table := range []string{"option_prices", "product_option_choices", "product_options", "product_prices", "product_os_images"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE product_id=?", id); err != nil {
			return out, err
		}
	}
	writePrice := func(oid, cid string, p api.PricePoint) error {
		for i := range config.Currencies {
			if config.Currencies[i].Code == p.Currency {
				config.Currencies[i].PrecisionLocked = true
			}
		}
		if oid == "" {
			_, err = tx.ExecContext(ctx, "INSERT INTO product_prices(product_id,currency,cycle,recurring_minor,setup_minor,decimal_places) VALUES(?,?,?,?,?,?)", id, p.Currency, p.Cycle, p.RecurringMinor, p.SetupMinor, p.DecimalPlaces)
		} else {
			_, err = tx.ExecContext(ctx, "INSERT INTO option_prices(product_id,option_id,choice_id,currency,cycle,recurring_minor,setup_minor,decimal_places) VALUES(?,?,?,?,?,?,?,?)", id, oid, cid, p.Currency, p.Cycle, p.RecurringMinor, p.SetupMinor, p.DecimalPlaces)
		}
		return err
	}
	for _, p := range in.Prices {
		if err = writePrice("", "", p); err != nil {
			return out, err
		}
	}
	for pos, o := range in.Options {
		label, _ := json.Marshal(o.Label)
		if _, err = tx.ExecContext(ctx, "INSERT INTO product_options(product_id,id,option_key,kind,label_i18n,required,min_quantity,max_quantity,sort_order) VALUES(?,?,?,?,?,?,?,?,?)", id, o.ID, o.Key, o.Kind, label, o.Required, o.Min, o.Max, pos); err != nil {
			return out, err
		}
		for _, p := range o.UnitPrices {
			if err = writePrice(o.ID, "", p); err != nil {
				return out, err
			}
		}
		for n, c := range o.Choices {
			label, _ = json.Marshal(c.Label)
			if _, err = tx.ExecContext(ctx, "INSERT INTO product_option_choices(product_id,option_id,id,label_i18n,sort_order) VALUES(?,?,?,?,?)", id, o.ID, c.ID, label, n); err != nil {
				return out, err
			}
			for _, p := range c.Prices {
				if err = writePrice(o.ID, c.ID, p); err != nil {
					return out, err
				}
			}
		}
	}
	for _, iid := range in.OSImageIDs {
		if _, err = tx.ExecContext(ctx, "INSERT INTO product_os_images(product_id,image_id) VALUES(?,?)", id, iid); err != nil {
			return out, err
		}
	}
	if err = writeCurrencies(ctx, tx, config, priceRevision+1); err != nil {
		return out, err
	}
	result, err := readProducts06(ctx, tx, true, id, "", "", "", "")
	if err != nil {
		return out, err
	}
	if len(result) != 1 {
		return out, ErrNotFound
	}
	out = result[0]
	if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "product.saved", id, revision, "Catalog product saved")); err != nil {
		return out, err
	}
	if err = recordRequest06(ctx, tx, actor.ID, operation, key, fingerprint, out); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (m *MySQL) Images06(ctx context.Context) ([]api.AdminOsImage, error) {
	return readImages06(ctx, m.DB)
}
func readImages06(ctx context.Context, q currencyQuerier) ([]api.AdminOsImage, error) {
	rows, err := q.QueryContext(ctx, "SELECT id,family,name,version,min_disk_gb,enabled,connection_id,remote_image_id,sha256 FROM os_images ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.AdminOsImage{}
	for rows.Next() {
		var i api.AdminOsImage
		if err = rows.Scan(&i.ID, &i.Family, &i.Name, &i.Version, &i.MinDiskGB, &i.Enabled, &i.ConnectionID, &i.RemoteImageID, &i.SHA256); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
func (m *MySQL) PatchImage06(ctx context.Context, actor User, id string, name *string, enabled *bool) (api.AdminOsImage, error) {
	var out api.AdminOsImage
	if !validCatalogID(id) {
		return out, ErrNotFound
	}
	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	if _, err = lockActiveAdministrator(ctx, tx, actor.ID); err != nil {
		return out, err
	}
	err = tx.QueryRowContext(ctx, "SELECT id,family,name,version,min_disk_gb,enabled,connection_id,remote_image_id,sha256 FROM os_images WHERE BINARY id=BINARY ? FOR UPDATE", id).Scan(&out.ID, &out.Family, &out.Name, &out.Version, &out.MinDiskGB, &out.Enabled, &out.ConnectionID, &out.RemoteImageID, &out.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if name != nil {
		out.Name = *name
	}
	if enabled != nil {
		out.Enabled = *enabled
	}
	if strings.TrimSpace(out.Name) == "" {
		return out, ErrCommerceInput
	}
	if _, err = tx.ExecContext(ctx, "UPDATE os_images SET name=?,enabled=? WHERE id=?", out.Name, out.Enabled, id); err != nil {
		return out, err
	}
	if err = insertCurrencyAudit(ctx, tx, catalogAudit(actor, "os_image.updated", id, 0, fmt.Sprintf("enabled=%t", out.Enabled))); err != nil {
		return out, err
	}
	return out, tx.Commit()
}
