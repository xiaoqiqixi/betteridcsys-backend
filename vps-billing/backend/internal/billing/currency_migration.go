package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"aster/billing/migrations"
)

var currencyMigrationTables = []string{"plans", "plan_prices", "orders", "invoices", "services"}
var currencySnapshotTables = []string{"orders", "invoices", "services"}

type currencyMigrationCheck struct {
	kind, enforced, clause string
}

func legacyCurrencyChecks(table string) map[string]string {
	switch table {
	case "plans":
		return map[string]string{"plans_chk_2": "currency = 'USD'"}
	case "plan_prices":
		return map[string]string{"plan_prices_chk_2": "currency IN ('CNY','USD')"}
	default:
		return map[string]string{
			table + "_currency":       "currency IN ('CNY','USD')",
			table + "_currency_exact": "CAST(currency AS BINARY) IN ('CNY','USD')",
		}
	}
}

func currencyMigrationChecks(ctx context.Context, conn *sql.Conn, table string) (map[string]currencyMigrationCheck, error) {
	rows, err := conn.QueryContext(ctx, "SELECT tc.CONSTRAINT_NAME,tc.CONSTRAINT_TYPE,COALESCE(tc.ENFORCED,''),COALESCE(cc.CHECK_CLAUSE,'') FROM information_schema.TABLE_CONSTRAINTS tc LEFT JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.CONSTRAINT_SCHEMA=DATABASE() AND tc.TABLE_NAME=?", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checks := make(map[string]currencyMigrationCheck)
	for rows.Next() {
		var name string
		var check currencyMigrationCheck
		if err = rows.Scan(&name, &check.kind, &check.enforced, &check.clause); err != nil {
			return nil, err
		}
		checks[name] = check
	}
	return checks, rows.Err()
}

func currencyMigrationCheckMatches(check currencyMigrationCheck, expression string) bool {
	if check.kind != "CHECK" || check.enforced != "YES" {
		return false
	}
	actual, actualOK := normalizeMigrationCheck(check.clause)
	expected, expectedOK := normalizeMigrationCheck(expression)
	if !actualOK || !expectedOK {
		return false
	}
	if expression == currencyCodeMigrationCheck() {
		return currencyMigrationConjunction(actual, []string{
			"OCTET_LENGTH(currency) = 3",
			"ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90",
			"ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90",
			"ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90",
		})
	}
	if expression == "prices_revision >= 1 AND prices_revision <= 9007199254740991" {
		return currencyMigrationConjunction(actual, []string{"prices_revision >= 1", "prices_revision <= 9007199254740991"})
	}
	// These are the two exact bounded conjunctions shipped by migration11.
	// MySQL renders operand parentheses; arbitrary expressions are not accepted.
	if expression == "sort_order >= 0 AND sort_order <= 1000000" {
		return currencyMigrationConjunction(actual, []string{"sort_order >= 0", "sort_order <= 1000000"})
	}
	if expression == "revision >= 1 AND revision <= 9007199254740991" {
		return currencyMigrationConjunction(actual, []string{"revision >= 1", "revision <= 9007199254740991"})
	}

	return actual == expected
}

// MySQL adds parentheses around individual AND operands and may print SUBSTR
// or LENGTH for their exact synonyms. Parse only conjunctions of these complete
// known atoms: discarding arbitrary parentheses would accept weaker checks.
func currencyMigrationConjunction(actual string, expressions []string) bool {
	actual = strings.ReplaceAll(actual, "substr (", "substring (")
	actual = strings.ReplaceAll(actual, "octet_length (", "length (")
	tokens := strings.Fields(actual)
	atoms := make([][]string, len(expressions))
	for i, expression := range expressions {
		normalized, ok := normalizeMigrationCheck(expression)
		if !ok {
			return false
		}
		atoms[i] = strings.Fields(strings.ReplaceAll(normalized, "octet_length (", "length ("))
	}
	position := 0
	var seen []int
	var expression func() bool
	var term func() bool
	term = func() bool {
		for index, atom := range atoms {
			if len(tokens)-position >= len(atom) && reflect.DeepEqual(tokens[position:position+len(atom)], atom) {
				position += len(atom)
				seen = append(seen, index)
				return true
			}
		}
		if position >= len(tokens) || tokens[position] != "(" {
			return false
		}
		position++
		if !expression() || position >= len(tokens) || tokens[position] != ")" {
			return false
		}
		position++
		return true
	}
	expression = func() bool {
		if !term() {
			return false
		}
		for position < len(tokens) && tokens[position] == "and" {
			position++
			if !term() {
				return false
			}
		}
		return true
	}
	if !expression() || position != len(tokens) || len(seen) != len(atoms) {
		return false
	}
	for i, index := range seen {
		if i != index {
			return false
		}
	}
	return true
}

// The migration always installs this exact byte-oriented check before removing
// either historical allowlist. Keeping the inherited column collation is safe:
// only three uppercase ASCII bytes can be stored after this check succeeds.
func currencyCodeMigrationCheck() string {
	return "OCTET_LENGTH(currency) = 3 AND ASCII(SUBSTRING(currency,1,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,2,1)) BETWEEN 65 AND 90 AND ASCII(SUBSTRING(currency,3,1)) BETWEEN 65 AND 90"
}

func currencySnapshotColumn(ctx context.Context, conn *sql.Conn, table string) (bool, error) {
	return currencyMigrationSnapshotColumn(ctx, conn, table, "currency_snapshot")
}

func currencyMigrationSnapshotColumn(ctx context.Context, conn *sql.Conn, table, column string) (bool, error) {
	var dataType, nullable, extra, generated string
	var defaultValue sql.NullString
	err := conn.QueryRowContext(ctx, "SELECT DATA_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME=?", table, column).Scan(&dataType, &nullable, &extra, &generated, &defaultValue)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if dataType != "json" || nullable != "YES" || extra != "" || generated != "" || defaultValue.Valid {
		return false, fmt.Errorf("migration 7: %s.%s differs from nullable JSON; restore the expected schema before retrying", table, column)
	}
	return true, nil
}

// An interrupted CREATE is accepted only when the complete new table matches.
// CREATE IF NOT EXISTS alone would silently accept an unrelated operator table.
func validateCurrencySettingsTable(ctx context.Context, conn *sql.Conn) (bool, error) {
	var engine, tableType string
	err := conn.QueryRowContext(ctx, "SELECT ENGINE,TABLE_TYPE FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='currency_settings'").Scan(&engine, &tableType)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if engine != "InnoDB" || tableType != "BASE TABLE" {
		return false, errors.New("migration 7: currency_settings must be the expected InnoDB table")
	}
	rows, err := conn.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='currency_settings' ORDER BY ORDINAL_POSITION")
	if err != nil {
		return false, err
	}
	type column struct {
		name, kind string
		def        sql.NullString
	}
	want := []column{{"id", "tinyint", sql.NullString{}}, {"document", "json", sql.NullString{}}, {"prices_revision", "bigint", sql.NullString{String: "1", Valid: true}}}
	index := 0
	for rows.Next() {
		var got column
		var nullable, extra, generated string
		if err = rows.Scan(&got.name, &got.kind, &nullable, &extra, &generated, &got.def); err != nil {
			rows.Close()
			return false, err
		}
		if index >= len(want) || got != want[index] || nullable != "NO" || extra != "" || generated != "" {
			rows.Close()
			return false, errors.New("migration 7: currency_settings columns differ from the expected schema")
		}
		index++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if index != len(want) {
		return false, errors.New("migration 7: currency_settings columns are incomplete")
	}
	checks, err := currencyMigrationChecks(ctx, conn, "currency_settings")
	if err != nil {
		return false, err
	}
	if len(checks) != 3 || checks["PRIMARY"].kind != "PRIMARY KEY" || !currencyMigrationCheckMatches(checks["currency_settings_singleton"], "id = 1") || !currencyMigrationCheckMatches(checks["currency_settings_prices_revision"], "prices_revision >= 1 AND prices_revision <= 9007199254740991") {
		return false, errors.New("migration 7: currency_settings constraints differ from the expected schema")
	}
	rows, err = conn.QueryContext(ctx, "SELECT INDEX_NAME,COLUMN_NAME,NON_UNIQUE,SEQ_IN_INDEX,SUB_PART,EXPRESSION FROM information_schema.STATISTICS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='currency_settings'")
	if err != nil {
		return false, err
	}
	index = 0
	for rows.Next() {
		var name, col string
		var nonUnique, sequence int
		var subPart sql.NullInt64
		var expression sql.NullString
		if err = rows.Scan(&name, &col, &nonUnique, &sequence, &subPart, &expression); err != nil {
			rows.Close()
			return false, err
		}
		if name != "PRIMARY" || col != "id" || nonUnique != 0 || sequence != 1 || subPart.Valid || expression.Valid {
			rows.Close()
			return false, errors.New("migration 7: currency_settings primary key differs from the expected schema")
		}
		index++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if index != 1 {
		return false, errors.New("migration 7: currency_settings requires exactly the singleton primary key")
	}
	return true, nil
}

func decodeCurrencyMigrationJSON(raw []byte, into any) error {
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func initialCurrencyMigrationConfiguration(ctx context.Context, conn *sql.Conn) (CurrencyConfiguration, error) {
	config := DefaultCurrencyConfiguration()
	var settingsRaw []byte
	if err := conn.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&settingsRaw); err != nil {
		return config, err
	}
	var settings struct {
		DefaultCurrency string `json:"default_currency"`
	}
	if err := json.Unmarshal(settingsRaw, &settings); err != nil || (settings.DefaultCurrency != "CNY" && settings.DefaultCurrency != "USD") {
		return config, errors.New("migration 7: legacy site default must be CNY or USD; review settings before retrying")
	}
	used := map[string]bool{}
	for _, table := range currencyMigrationTables {
		rows, err := conn.QueryContext(ctx, "SELECT DISTINCT CAST(currency AS BINARY) FROM "+table)
		if err != nil {
			return config, err
		}
		for rows.Next() {
			var code string
			if err = rows.Scan(&code); err != nil {
				rows.Close()
				return config, err
			}
			if code != "CNY" && code != "USD" {
				rows.Close()
				return config, fmt.Errorf("migration 7: %s has an unknown legacy currency; preserve the database and review its monetary precision before retrying", table)
			}
			if table != "plans" {
				used[code] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return config, err
		}
	}
	// New installs use zero-valued deprecated USD plan fields. Only genuine
	// positive legacy prices are evidence of prior USD operation. In particular,
	// an empty billing history does not imply that an older installation is new.
	rows, err := conn.QueryContext(ctx, "SELECT DISTINCT CAST(currency AS BINARY) FROM plans WHERE price_minor>0")
	if err != nil {
		return config, err
	}
	for rows.Next() {
		var code string
		if err = rows.Scan(&code); err != nil {
			rows.Close()
			return config, err
		}
		used[code] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return config, err
	}
	config.DefaultCurrency = settings.DefaultCurrency
	config.Currencies = []Currency{{CurrencyFormat: DefaultCurrencyFormat("CNY"), Enabled: true, SortOrder: 0, PrecisionLocked: used["CNY"]}}
	if used["USD"] || settings.DefaultCurrency == "USD" {
		config.Currencies = append(config.Currencies, Currency{CurrencyFormat: DefaultCurrencyFormat("USD"), Enabled: true, SortOrder: 1, PrecisionLocked: used["USD"]})
	}
	return config, nil
}

func validateCurrencyMigrationSnapshots(ctx context.Context, conn *sql.Conn, table string) error {
	rows, err := conn.QueryContext(ctx, "SELECT currency,currency_snapshot FROM "+table+" WHERE currency_snapshot IS NOT NULL")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var raw []byte
		if err = rows.Scan(&code, &raw); err != nil {
			return err
		}
		var snapshot CurrencyFormat
		if err = decodeCurrencyMigrationJSON(raw, &snapshot); err != nil || snapshot != DefaultCurrencyFormat(code) {
			return fmt.Errorf("migration 7: %s has an unexpected existing currency snapshot; preserve it and investigate before retrying", table)
		}
	}
	return rows.Err()
}

func validateCurrencyMigrationDocument(ctx context.Context, conn *sql.Conn, config CurrencyConfiguration) error {
	rows, err := conn.QueryContext(ctx, "SELECT id,document,prices_revision FROM currency_settings")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var raw []byte
		var revision int64
		if err = rows.Scan(&id, &raw, &revision); err != nil {
			return err
		}
		stored, decodeErr := decodeStoredCurrencies(raw)
		if decodeErr != nil || id != 1 || revision != 1 || !reflect.DeepEqual(stored, config) {
			return errors.New("migration 7: existing currency configuration differs from the expected upgrade state; preserve it and investigate before retrying")
		}
	}
	return rows.Err()
}

// migrateCurrencyRegistry runs under the migrator's connection-scoped advisory
// lock. Stop older application writers for the migration maintenance window.
// MySQL DDL commits independently, so all legacy data and partial definitions
// are checked before any DDL and each completed statement is recognized exactly.
func migrateCurrencyRegistry(ctx context.Context, conn *sql.Conn) error {
	config, err := initialCurrencyMigrationConfiguration(ctx, conn)
	if err != nil {
		return err
	}
	tableExists, err := validateCurrencySettingsTable(ctx, conn)
	if err != nil {
		return err
	}
	if tableExists {
		if err = validateCurrencyMigrationDocument(ctx, conn, config); err != nil {
			return err
		}
	}
	columns := make(map[string]bool)
	for _, table := range currencySnapshotTables {
		exists, err := currencySnapshotColumn(ctx, conn, table)
		if err != nil {
			return err
		}
		columns[table+".currency_snapshot"] = exists
		if exists {
			if err = validateCurrencyMigrationSnapshots(ctx, conn, table); err != nil {
				return err
			}
		}
	}
	planSnapshotExists, err := currencyMigrationSnapshotColumn(ctx, conn, "orders", "plan_snapshot")
	if err != nil {
		return err
	}
	columns["orders.plan_snapshot"] = planSnapshotExists
	if planSnapshotExists {
		var count int
		if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE plan_snapshot IS NOT NULL").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New("migration 7: unexpected historical plan snapshot; preserve it and investigate before retrying")
		}
	}
	checksByTable := make(map[string]map[string]currencyMigrationCheck)
	for _, table := range currencyMigrationTables {
		checks, err := currencyMigrationChecks(ctx, conn, table)
		if err != nil {
			return err
		}
		checksByTable[table] = checks
		newCheck, hasNewCheck := checks[table+"_currency_code"]
		if hasNewCheck && !currencyMigrationCheckMatches(newCheck, currencyCodeMigrationCheck()) {
			return fmt.Errorf("migration 7: %s currency code check differs from the expected definition", table)
		}
		for name, expression := range legacyCurrencyChecks(table) {
			check, exists := checks[name]
			if (exists && !currencyMigrationCheckMatches(check, expression)) || (!exists && !hasNewCheck) {
				return fmt.Errorf("migration 7: legacy constraint %s is changed or missing without its replacement; restore the expected definition before retrying", name)
			}
		}
	}

	for _, statement := range strings.Split(migrations.CurrencyRegistry, ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		fields := strings.Fields(statement)
		skip := false
		switch {
		case strings.HasPrefix(statement, "CREATE TABLE IF NOT EXISTS currency_settings "):
			skip = tableExists
		case len(fields) == 8 && fields[0] == "ALTER" && fields[3] == "ADD" && fields[4] == "COLUMN":
			skip = columns[fields[2]+"."+fields[5]]
		case len(fields) >= 7 && fields[0] == "ALTER" && fields[3] == "ADD" && fields[4] == "CONSTRAINT":
			_, skip = checksByTable[fields[2]][fields[5]]
		case len(fields) == 6 && fields[0] == "ALTER" && fields[3] == "DROP" && fields[4] == "CHECK":
			_, exists := checksByTable[fields[2]][fields[5]]
			skip = !exists
		default:
			return errors.New("migration 7: unrecognized currency migration statement")
		}
		if !skip {
			if _, err = conn.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("migration 7 currency DDL failed: %w", err)
			}
		}
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO currency_settings(id,document,prices_revision) VALUES(1,?,1) ON DUPLICATE KEY UPDATE id=id", raw); err != nil {
		return err
	}
	for _, table := range currencySnapshotTables {
		for _, code := range []string{"CNY", "USD"} {
			snapshot, err := json.Marshal(DefaultCurrencyFormat(code))
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE "+table+" SET currency_snapshot=? WHERE currency_snapshot IS NULL AND CAST(currency AS BINARY)=CAST(? AS BINARY)", snapshot, code); err != nil {
				return err
			}
		}
		var missing int64
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE currency_snapshot IS NULL").Scan(&missing); err != nil {
			return err
		}
		if missing != 0 {
			return errors.New("migration 7: new legacy writes appeared during snapshot backfill; stop application writers before retrying")
		}
	}
	return tx.Commit()
}
