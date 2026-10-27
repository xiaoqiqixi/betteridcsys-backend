package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// These bounded descriptions are the physical columns shipped by migrations
// 1–13, not an SQL interpreter. A history row alone cannot detect interrupted
// DDL or a later manual edit. Compatible extra indexes/checks are permitted.
func preflightColumns(version int) map[string]map[string]string {
	specs := map[string]string{
		"schema_migrations": "version:int! checksum:char(64)! applied_at:datetime(6)!",
		"users":             "id:char(32)! email:varchar(254)! name:varchar(120)! role:enum('customer','admin')! password_hash:varchar(100)! created_at:datetime(6)!",
		"sessions":          "token_hash:char(64)! user_id:char(32)! expires_at:datetime(6)!",
		"plans":             "id:varchar(64)! name:varchar(120)! description:varchar(500)! cpu:int! memory_gb:int! disk_gb:int! transfer_tb:int! region:varchar(120)! price_minor:bigint! currency:char(3)! available:tinyint(1)!",
		"orders":            "id:char(32)! number:varchar(64)! user_id:char(32)! plan_id:varchar(64)! plan_name:varchar(120)! hostname:varchar(253)! status:enum('pending_payment','paid','cancelled')! total_minor:bigint! currency:char(3)! created_at:datetime(6)!",
		"invoices":          "id:char(32)! number:varchar(64)! user_id:char(32)! order_id:char(32)! status:enum('unpaid','paid','void')! amount_minor:bigint! currency:char(3)! issued_at:datetime(6)! due_at:datetime(6)! paid_at:datetime(6)?",
		"order_requests":    "user_id:char(32)! request_key:varchar(128)! fingerprint:char(64)! order_id:char(32)? created_at:datetime(6)!",
		"services":          "id:char(32)! user_id:char(32)! name:varchar(253)! plan_name:varchar(120)! region:varchar(120)! ipv4:varchar(45)! status:enum('pending','active','suspended','terminated')! next_due_at:datetime(6)! price_minor:bigint! currency:char(3)! cpu:int! memory_gb:int! disk_gb:int!",
		"audit_events":      "id:char(32)! user_id:char(32)! action:varchar(64)! target:varchar(128)! created_at:datetime(6)! actor_email:varchar(254)! detail:varchar(1000)!",
		"outbox_events":     "id:char(32)! aggregate_id:char(32)! event_type:varchar(64)! payload:json! created_at:datetime(6)! processed_at:datetime(6)? attempts:int!",
	}
	if version >= 2 {
		specs["site_settings"] = "id:tinyint! document:json!"
		specs["invoices"] += " issuer_snapshot:json?"
	}
	if version >= 3 {
		specs["plan_prices"] = "plan_id:varchar(64)! currency:char(3)! amount_minor:bigint! available:tinyint(1)!"
	}
	if version >= 4 {
		specs["orders"] += " issuer_snapshot:json?"
	}
	if version >= 6 {
		specs["email_settings"] = "id:tinyint! document:json!"
	}
	if version >= 7 {
		specs["currency_settings"] = "id:tinyint! document:json! prices_revision:bigint!"
		specs["orders"] += " currency_snapshot:json? plan_snapshot:json?"
		specs["invoices"] += " currency_snapshot:json?"
		specs["services"] += " currency_snapshot:json?"
	}
	if version >= 8 {
		specs["payment_attempts"] = "id:char(32)! request_key:varchar(128)! user_id:char(32)! invoice_id:char(32)! order_id:char(32)! provider_id:varchar(64)! merchant_account_id:varchar(128)! amount_minor:bigint! currency:char(3)! currency_decimal_places:int! state:enum('pending','checkout','uncertain','settled')! checkout:json? created_at:datetime(6)! updated_at:datetime(6)!"
		specs["payment_settlements"] = "attempt_id:char(32)! provider_id:varchar(64)! event_id:varchar(128)! transaction_id:varchar(128)! settled_at:datetime(6)! received_at:datetime(6)!"
	}
	if version >= 10 {
		specs["users"] += " disabled:tinyint(1)! revision:bigint!"
	}
	if version >= 11 {
		specs["plans"] += " type_id:varchar(64)! region_id:varchar(64)!"
		specs["catalog_types"] = "id:varchar(64)! name:varchar(120)! sort_order:int! enabled:tinyint(1)!"
		specs["catalog_regions"] = "id:varchar(64)! type_id:varchar(64)! name:varchar(120)! sort_order:int! enabled:tinyint(1)!"
		specs["catalog_taxonomy_settings"] = "id:tinyint! revision:bigint!"
	}
	if version >= 12 {
		specs["plans"] += " node_id:varchar(64)!"
		specs["catalog_nodes"] = "id:varchar(64)! region_id:varchar(64)! name:varchar(120)! sort_order:int! enabled:tinyint(1)!"
	}
	if version >= 13 {
		specs["product_configurations"] = "plan_id:varchar(64)! revision:bigint! spec:json! configuration_sha256:char(64)! updated_at:datetime(6)!"
		specs["order_product_configurations"] = "order_id:char(32)! plan_id:varchar(64)! configuration_revision:bigint! spec:json! configuration_sha256:char(64)! created_at:datetime(6)!"
		specs["provisioning_jobs"] = "id:char(32)! order_id:char(32)! state:enum('queued','sending','accepted','succeeded','rejected','uncertain')! request:json! request_sha256:char(64)! claim_token:char(32)? lease_expires_at:datetime(6)? result:json? attempts:int! created_at:datetime(6)! updated_at:datetime(6)!"
	}
	result := make(map[string]map[string]string, len(specs))
	for table, spec := range specs {
		result[table] = map[string]string{}
		for _, field := range strings.Fields(spec) {
			name, kind, _ := strings.Cut(field, ":")
			result[table][name] = kind
		}
	}
	return result
}

func preflightChecks(version int) map[string]map[string]string {
	checks := map[string]map[string]string{
		"plans":    {"plans_chk_1": "price_minor >= 0"},
		"orders":   {"orders_chk_1": "total_minor >= 0"},
		"invoices": {"invoices_chk_1": "amount_minor >= 0"},
		"services": {"services_chk_1": "price_minor >= 0"},
	}
	if version < 7 {
		checks["plans"]["plans_chk_2"] = "currency = 'USD'"
	}
	if version < 3 {
		checks["orders"]["orders_chk_2"] = "currency = 'USD'"
		checks["invoices"]["invoices_chk_2"] = "currency = 'USD'"
	}
	if version >= 2 {
		checks["site_settings"] = map[string]string{"site_settings_chk_1": "id = 1"}
	}
	if version >= 3 {
		checks["plan_prices"] = map[string]string{"plan_prices_chk_1": "amount_minor > 0"}
		if version < 7 {
			checks["plan_prices"]["plan_prices_chk_2"] = "currency IN ('CNY','USD')"
			for _, table := range []string{"orders", "invoices"} {
				checks[table][table+"_currency"] = "currency IN ('CNY','USD')"
			}
		}
	}
	if version >= 4 {
		if version < 7 {
			checks["services"]["services_currency"] = "currency IN ('CNY','USD')"
		}
		for table, column := range map[string]string{"plan_prices": "amount_minor", "orders": "total_minor", "invoices": "amount_minor", "services": "price_minor"} {
			checks[table][table+"_exact_minor"] = column + " <= 9007199254740991"
		}
	}
	if version >= 5 && version < 7 {
		for _, table := range []string{"orders", "invoices", "services"} {
			checks[table][table+"_currency_exact"] = "CAST(currency AS BINARY) IN ('CNY','USD')"
		}
	}
	if version >= 6 {
		checks["email_settings"] = map[string]string{"email_settings_singleton": "id = 1"}
	}
	if version >= 7 {
		checks["currency_settings"] = map[string]string{"currency_settings_singleton": "id = 1", "currency_settings_prices_revision": "prices_revision >= 1 AND prices_revision <= 9007199254740991"}
		for _, table := range currencyMigrationTables {
			checks[table][table+"_currency_code"] = currencyCodeMigrationCheck()
		}
	}
	if version >= 8 {
		checks["payment_attempts"] = map[string]string{
			"payment_attempts_positive_minor": "amount_minor > 0", "payment_attempts_exact_minor": "amount_minor <= 9007199254740991",
			"payment_attempts_decimal_places_min": "currency_decimal_places >= 0", "payment_attempts_decimal_places_max": "currency_decimal_places <= 6",
			"payment_attempts_currency_code": currencyCodeMigrationCheck(),
		}
	}
	if version >= 10 {
		checks["users"] = map[string]string{"users_disabled_boolean": "disabled IN (0,1)", "users_revision_min": "revision >= 1", "users_revision_max": "revision <= 9007199254740991"}
	}
	if version >= 11 {
		checks["catalog_types"] = map[string]string{"catalog_types_sort": "sort_order >= 0 AND sort_order <= 1000000", "catalog_types_enabled": "enabled IN (0,1)"}
		checks["catalog_regions"] = map[string]string{"catalog_regions_sort": "sort_order >= 0 AND sort_order <= 1000000", "catalog_regions_enabled": "enabled IN (0,1)"}
		checks["catalog_taxonomy_settings"] = map[string]string{"catalog_taxonomy_singleton": "id = 1", "catalog_taxonomy_revision": "revision >= 1 AND revision <= 9007199254740991"}
	}
	if version >= 12 {
		checks["catalog_nodes"] = map[string]string{"catalog_nodes_sort": "sort_order >= 0 AND sort_order <= 1000000", "catalog_nodes_enabled": "enabled IN (0,1)"}
	}
	if version >= 13 {
		checks["product_configurations"] = map[string]string{"product_configuration_revision": "revision >= 1 AND revision <= 9007199254740991"}
		checks["order_product_configurations"] = map[string]string{"order_product_configuration_revision": "configuration_revision >= 1 AND configuration_revision <= 9007199254740991"}
		checks["provisioning_jobs"] = map[string]string{"provisioning_job_attempts": "attempts >= 0 AND attempts <= 1"}
	}
	return checks
}

var preflightDefaults = map[string]string{
	"provisioning_jobs.attempts": "0",
	"users.disabled":             "0", "users.revision": "1",
	"schema_migrations.applied_at": "current_timestamp(6)", "users.created_at": "current_timestamp(6)", "order_requests.created_at": "current_timestamp(6)",
	"plans.available": "0", "plan_prices.available": "0", "services.ipv4": "", "outbox_events.attempts": "0", "currency_settings.prices_revision": "1",
}

func preflightColumnMatches(table, name, spec, kind, nullable, extra, generated string, def, charset, collation sql.NullString, databaseCollation string) bool {
	if kind != spec[:len(spec)-1] || (nullable == "YES") != strings.HasSuffix(spec, "?") || generated != "" {
		return false
	}
	wantDefault, hasDefault := preflightDefaults[table+"."+name]
	if def.Valid != hasDefault || (hasDefault && strings.ToLower(def.String) != wantDefault) {
		return false
	}
	wantExtra := ""
	if strings.HasPrefix(wantDefault, "current_timestamp") {
		wantExtra = "DEFAULT_GENERATED"
	}
	if extra != wantExtra {
		return false
	}
	if strings.HasPrefix(kind, "char(") || strings.HasPrefix(kind, "varchar(") || strings.HasPrefix(kind, "enum(") {
		wantCharset, wantCollation := "utf8mb4", databaseCollation
		switch table + "." + name {
		case "users.email":
			wantCharset, wantCollation = "ascii", "ascii_general_ci"
		case "order_requests.request_key", "plan_prices.currency", "product_configurations.configuration_sha256", "order_product_configurations.configuration_sha256", "provisioning_jobs.request_sha256", "provisioning_jobs.claim_token":
			wantCharset, wantCollation = "ascii", "ascii_bin"
		}
		if (table == "catalog_types" && name == "id") || (table == "catalog_regions" && (name == "id" || name == "type_id")) || (table == "plans" && (name == "type_id" || name == "region_id" || name == "node_id")) || (table == "catalog_nodes" && (name == "id" || name == "region_id")) {
			wantCharset, wantCollation = "ascii", "ascii_bin"
		}
		if (table == "payment_attempts" && name != "user_id" && name != "invoice_id" && name != "order_id" && name != "state") || table == "payment_settlements" {
			wantCharset, wantCollation = "ascii", "ascii_bin"
		}
		return charset.Valid && charset.String == wantCharset && collation.Valid && collation.String == wantCollation
	}
	return !charset.Valid && !collation.Valid
}

func validatePreflightShape(ctx context.Context, conn *sql.Conn, version int, tables map[string]string, databaseCollation string) error {
	return validatePreflightTableShape(ctx, conn, version, tables, databaseCollation, false)
}

// A resumable CREATE validates just its already-existing table through the same
// rules as preflight. Other migration statements may not have run yet.
func validatePreflightTableShape(ctx context.Context, conn *sql.Conn, version int, tables map[string]string, databaseCollation string, partial bool) error {
	want := preflightColumns(version)
	if partial {
		for table := range want {
			if _, included := tables[table]; !included {
				delete(want, table)
			}
		}
	}
	if len(want) != len(tables) {
		return errors.New("schema table inventory differs from applied migrations")
	}
	for table := range want {
		if tables[table] != "InnoDB" {
			return errors.New("schema requires the expected InnoDB base tables")
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,ORDINAL_POSITION LIMIT 257")
	if err != nil {
		return err
	}
	seen := 0
	for rows.Next() {
		var table, name, kind, nullable, extra, generated string
		var def, charset, collation sql.NullString
		if err = rows.Scan(&table, &name, &kind, &nullable, &extra, &generated, &def, &charset, &collation); err != nil {
			rows.Close()
			return err
		}
		if partial && tables[table] == "" {
			continue
		}
		spec := want[table][name]
		if seen >= 256 || spec == "" || !preflightColumnMatches(table, name, spec, kind, nullable, extra, generated, def, charset, collation, databaseCollation) {
			rows.Close()
			return errors.New("schema columns differ from applied migrations")
		}
		delete(want[table], name)
		seen++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, columns := range want {
		if len(columns) != 0 {
			return errors.New("schema has missing columns")
		}
	}
	checks := preflightChecks(version)
	if partial {
		for table := range checks {
			if tables[table] == "" {
				delete(checks, table)
			}
		}
	}
	knownChecks := map[string]map[string]bool{}
	for prefix := 1; prefix <= len(schemaVersions); prefix++ {
		for table, definitions := range preflightChecks(prefix) {
			if knownChecks[table] == nil {
				knownChecks[table] = map[string]bool{}
			}
			for name := range definitions {
				knownChecks[table][name] = true
			}
		}
	}
	rows, err = conn.QueryContext(ctx, "SELECT tc.TABLE_NAME,tc.CONSTRAINT_NAME,tc.ENFORCED,cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.TABLE_SCHEMA=DATABASE() AND tc.CONSTRAINT_TYPE='CHECK' ORDER BY tc.TABLE_NAME,tc.CONSTRAINT_NAME LIMIT 257")
	if err != nil {
		return err
	}
	seen = 0
	for rows.Next() {
		var table, name, enforced, clause string
		if err = rows.Scan(&table, &name, &enforced, &clause); err != nil {
			rows.Close()
			return err
		}
		if seen >= 256 {
			rows.Close()
			return errors.New("too many schema checks")
		}
		seen++
		if partial && tables[table] == "" {
			continue
		}
		// Migration 7 removes these exact historical allowlists. Their presence
		// after a claimed completion is interrupted/manual DDL, even if the new
		// byte-oriented code check was already added.
		if version >= 7 {
			if _, removed := legacyCurrencyChecks(table)[name]; removed {
				rows.Close()
				return errors.New("schema retains a superseded currency check")
			}
		}
		if expected := checks[table][name]; expected != "" {
			check := currencyMigrationCheck{"CHECK", enforced, clause}
			matches := currencyMigrationCheckMatches(check, expected)
			if version >= 13 && (table == "product_configurations" || table == "order_product_configurations" || table == "provisioning_jobs") {
				matches = productConfigurationMigrationCheckMatches(table, name, check, expected)
			}
			if !matches {
				rows.Close()
				return errors.New("required schema check differs from applied migrations")
			}
			delete(checks[table], name)
		} else if knownChecks[table][name] {
			rows.Close()
			return errors.New("schema contains a shipped check outside its applied migration prefix")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, required := range checks {
		if len(required) != 0 {
			return errors.New("schema has missing required checks")
		}
	}
	if err := validatePreflightTableKeys(ctx, conn, version, tables, partial); err != nil {
		return err
	}
	if version >= 9 && (!partial || tables["audit_events"] != "") {
		return validateAuditIndexes(ctx, conn)
	}
	return nil
}

func validatePreflightKeys(ctx context.Context, conn *sql.Conn, version int) error {
	return validatePreflightTableKeys(ctx, conn, version, nil, false)
}

func validatePreflightTableKeys(ctx context.Context, conn *sql.Conn, version int, tables map[string]string, partial bool) error {
	// Required unique keys are correctness boundaries for auth and idempotency.
	// Extra operator indexes and check constraints do not invalidate a schema.
	required := map[string]bool{
		"schema_migrations:version": true, "users:id": true, "users:email": true, "sessions:token_hash": true,
		"plans:id": true, "orders:id": true, "orders:number": true, "invoices:id": true, "invoices:number": true,
		"invoices:order_id": true, "order_requests:user_id,request_key": true, "services:id": true,
		"audit_events:id": true, "outbox_events:id": true, "outbox_events:aggregate_id,event_type": true,
	}
	if version >= 2 {
		required["site_settings:id"] = true
	}
	if version >= 3 {
		required["plan_prices:plan_id,currency"] = true
	}
	if version >= 6 {
		required["email_settings:id"] = true
	}
	if version >= 7 {
		required["currency_settings:id"] = true
	}
	if version >= 8 {
		for _, key := range []string{"payment_attempts:id", "payment_attempts:user_id,request_key", "payment_attempts:invoice_id", "payment_settlements:attempt_id", "payment_settlements:provider_id,event_id", "payment_settlements:provider_id,transaction_id"} {
			required[key] = true
		}
	}
	if version >= 11 {
		for _, t := range []string{"catalog_types", "catalog_regions", "catalog_taxonomy_settings"} {
			required[t+":id"] = true
		}
	}
	if version >= 12 {
		required["catalog_nodes:id"] = true
	}
	if version >= 13 {
		for _, key := range []string{"product_configurations:plan_id", "order_product_configurations:order_id", "provisioning_jobs:id", "provisioning_jobs:order_id"} {
			required[key] = true
		}
	}
	if partial {
		for key := range required {
			table, _, _ := strings.Cut(key, ":")
			if tables[table] == "" {
				delete(required, key)
			}
		}
	}
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME,INDEX_NAME,COLUMN_NAME,NON_UNIQUE,SEQ_IN_INDEX,SUB_PART,EXPRESSION,IS_VISIBLE FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME,INDEX_NAME,SEQ_IN_INDEX LIMIT 513")
	if err != nil {
		return err
	}
	keys := map[string]string{}
	valid := map[string]bool{}
	count := 0
	for rows.Next() {
		var table, name, visible string
		var column, expression sql.NullString
		var prefix sql.NullInt64
		var nonUnique, sequence int
		if err = rows.Scan(&table, &name, &column, &nonUnique, &sequence, &prefix, &expression, &visible); err != nil {
			rows.Close()
			return err
		}
		if count >= 512 {
			rows.Close()
			return errors.New("too many schema indexes")
		}
		count++
		id := table + ":" + name
		if sequence == 1 {
			valid[id] = nonUnique == 0
		}
		if nonUnique != 0 || prefix.Valid || expression.Valid || !column.Valid || visible != "YES" {
			valid[id] = false
		}
		if sequence > 1 {
			keys[id] += ","
		}
		keys[id] += column.String
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, columns := range keys {
		table, _, _ := strings.Cut(id, ":")
		if valid[id] {
			delete(required, table+":"+columns)
		}
	}
	if len(required) != 0 {
		return errors.New("schema has missing required unique keys")
	}
	if version >= 8 {
		for table, column := range map[string]string{"payment_attempts": "id", "payment_settlements": "attempt_id"} {
			if partial && tables[table] == "" {
				continue
			}
			if id := table + ":PRIMARY"; !valid[id] || keys[id] != column {
				return errors.New("payment schema has an unexpected primary key")
			}
		}
	}
	if version >= 12 && (!partial || tables["catalog_nodes"] != "") {
		if id := "catalog_nodes:PRIMARY"; !valid[id] || keys[id] != "id" {
			return errors.New("catalog node schema has an unexpected primary key")
		}
	}

	if version >= 13 {
		for table, column := range map[string]string{"product_configurations": "plan_id", "order_product_configurations": "order_id", "provisioning_jobs": "id"} {
			if partial && tables[table] == "" {
				continue
			}
			if id := table + ":PRIMARY"; !valid[id] || keys[id] != column {
				return errors.New("product configuration schema has an unexpected primary key")
			}
		}
	}
	fks := map[string]bool{
		"sessions:user_id:users:id:CASCADE": true, "orders:user_id:users:id:RESTRICT": true, "orders:plan_id:plans:id:RESTRICT": true,
		"invoices:user_id:users:id:RESTRICT": true, "invoices:order_id:orders:id:RESTRICT": true,
		"order_requests:user_id:users:id:RESTRICT": true, "order_requests:order_id:orders:id:RESTRICT": true,
		"services:user_id:users:id:RESTRICT": true, "audit_events:user_id:users:id:RESTRICT": true, "outbox_events:aggregate_id:orders:id:RESTRICT": true,
	}
	if version >= 3 {
		fks["plan_prices:plan_id:plans:id:RESTRICT"] = true
	}
	if version >= 8 {
		for _, key := range []string{"payment_attempts:user_id:users:id:RESTRICT", "payment_attempts:invoice_id:invoices:id:RESTRICT", "payment_attempts:order_id:orders:id:RESTRICT", "payment_settlements:attempt_id:payment_attempts:id:RESTRICT"} {
			fks[key] = true
		}
	}
	if version >= 11 {
		for _, k := range []string{"catalog_regions:type_id:catalog_types:id:RESTRICT", "plans:type_id:catalog_types:id:RESTRICT", "plans:region_id:catalog_regions:id:RESTRICT"} {
			fks[k] = true
		}
	}
	if version >= 12 {
		fks["catalog_nodes:region_id:catalog_regions:id:RESTRICT"] = true
		fks["plans:node_id:catalog_nodes:id:RESTRICT"] = true
	}
	if version >= 13 {
		for _, key := range []string{"product_configurations:plan_id:plans:id:RESTRICT", "order_product_configurations:order_id:orders:id:RESTRICT", "order_product_configurations:plan_id:plans:id:RESTRICT", "provisioning_jobs:order_id:order_product_configurations:order_id:RESTRICT"} {
			fks[key] = true
		}
	}
	if partial {
		for key := range fks {
			table, _, _ := strings.Cut(key, ":")
			if tables[table] == "" {
				delete(fks, key)
			}
		}
	}
	rows, err = conn.QueryContext(ctx, "SELECT k.TABLE_NAME,k.COLUMN_NAME,k.REFERENCED_TABLE_NAME,k.REFERENCED_COLUMN_NAME,r.DELETE_RULE,r.UPDATE_RULE,k.REFERENCED_TABLE_SCHEMA=DATABASE(),k.ORDINAL_POSITION,k.CONSTRAINT_NAME FROM information_schema.KEY_COLUMN_USAGE k JOIN information_schema.REFERENTIAL_CONSTRAINTS r ON k.CONSTRAINT_SCHEMA=r.CONSTRAINT_SCHEMA AND k.CONSTRAINT_NAME=r.CONSTRAINT_NAME WHERE k.TABLE_SCHEMA=DATABASE() AND k.REFERENCED_TABLE_NAME IS NOT NULL ORDER BY k.TABLE_NAME,k.CONSTRAINT_NAME,k.ORDINAL_POSITION LIMIT 129")
	if err != nil {
		return err
	}
	count = 0
	foreignKeys := map[string]string{}
	foreignKeySizes := map[string]int{}
	for rows.Next() {
		var table, col, refTable, refCol, deletion, update, constraint string
		var sameDB bool
		var ordinal int
		if err = rows.Scan(&table, &col, &refTable, &refCol, &deletion, &update, &sameDB, &ordinal, &constraint); err != nil {
			rows.Close()
			return err
		}
		if count >= 128 {
			rows.Close()
			return errors.New("too many schema foreign keys")
		}
		count++
		id := table + ":" + constraint
		foreignKeySizes[id]++
		if version >= 13 && (table == "product_configurations" || table == "order_product_configurations" || table == "provisioning_jobs") && (!partial || tables[table] != "") {
			rule := deletion
			if rule == "NO ACTION" {
				rule = "RESTRICT"
			}
			signature := strings.Join([]string{table, col, refTable, refCol, rule}, ":")
			if !sameDB || ordinal != 1 || (update != "RESTRICT" && update != "NO ACTION") || !fks[signature] {
				rows.Close()
				return errors.New("product configuration schema has an unexpected foreign key")
			}
		}
		if version >= 12 && table == "catalog_nodes" && (!partial || tables[table] != "") {
			rule := deletion
			if rule == "NO ACTION" {
				rule = "RESTRICT"
			}
			signature := strings.Join([]string{table, col, refTable, refCol, rule}, ":")
			if !sameDB || ordinal != 1 || (update != "RESTRICT" && update != "NO ACTION") || !fks[signature] {
				rows.Close()
				return errors.New("catalog node schema has an unexpected foreign key")
			}
		}
		if version >= 8 && (table == "payment_attempts" || table == "payment_settlements") && (!partial || tables[table] != "") {
			deletionRule := deletion
			if deletionRule == "NO ACTION" {
				deletionRule = "RESTRICT"
			}
			signature := strings.Join([]string{table, col, refTable, refCol, deletionRule}, ":")
			if !sameDB || ordinal != 1 || (update != "RESTRICT" && update != "NO ACTION") || !fks[signature] {
				rows.Close()
				return errors.New("payment schema has an unexpected foreign key")
			}
		}
		if sameDB && ordinal == 1 && (update == "RESTRICT" || update == "NO ACTION") {
			if deletion == "NO ACTION" {
				deletion = "RESTRICT"
			}
			foreignKeys[id] = strings.Join([]string{table, col, refTable, refCol, deletion}, ":")
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, signature := range foreignKeys {
		if foreignKeySizes[id] == 1 {
			delete(fks, signature)
		}
	}
	if len(fks) != 0 {
		return errors.New("schema has missing required foreign keys")
	}
	return nil
}
