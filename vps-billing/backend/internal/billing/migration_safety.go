package billing

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Verify all applied versions before performing business DDL. A valid database
// has either no history or an unchanged, contiguous prefix of this binary.
func validateAppliedMigrations(ctx context.Context, conn *sql.Conn, sources []string) error {
	rows, err := conn.QueryContext(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	applied := 0
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return err
		}
		if applied >= len(sources) || version != applied+1 || checksum != digest(sources[applied]) {
			return errors.New("migration history mismatch; expected an unchanged contiguous prefix")
		}
		applied++
	}
	return rows.Err()
}

// MySQL DDL commits independently. Resume only known statements whose existing
// definitions match exactly; a matching object name alone is insufficient.
func prepareMigrationStatement(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	if version == 13 {
		return prepareProductConfigurationMigration(ctx, conn, q)
	}
	if version == 12 {
		return prepareCatalogNodesMigration(ctx, conn, q)
	}
	if version == 11 {
		return prepareTaxonomyMigration(ctx, conn, q)
	}
	if version == 10 {
		return prepareCustomerAccountMigration(ctx, conn, q)
	}
	if version == 9 {
		return prepareAuditIndex(ctx, conn, q)
	}
	if version == 8 {
		for _, table := range []string{"payment_attempts", "payment_settlements"} {
			if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "+table+" (") {
				var kind, engine, databaseCollation string
				err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind, &engine, &databaseCollation)
				if errors.Is(err, sql.ErrNoRows) {
					return q, false, nil
				}
				if err != nil {
					return "", false, err
				}
				if kind != "BASE TABLE" || engine != "InnoDB" {
					return "", false, errors.New("existing payment object differs from migration")
				}
				if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, databaseCollation, true); err != nil {
					return "", false, err
				}
				return q, true, nil
			}
		}
		return "", false, errors.New("unsupported resumable payment migration")
	}
	if version == 3 && strings.Join(strings.Fields(q), " ") == strings.Join(strings.Fields(`INSERT IGNORE INTO plan_prices(plan_id,currency,amount_minor,available) VALUES
 ('starter','CNY',3900,FALSE),('standard','CNY',7900,FALSE),('performance','CNY',15900,FALSE)`), " ") {
		// An operator may legitimately have removed the original example plans.
		// Preserve the immutable v3 source/checksum while limiting its optional,
		// unavailable examples to plans that still exist. No FX is inferred.
		return `INSERT IGNORE INTO plan_prices(plan_id,currency,amount_minor,available)
 SELECT id,'CNY',CASE id WHEN 'starter' THEN 3900 WHEN 'standard' THEN 7900 WHEN 'performance' THEN 15900 END,FALSE
 FROM plans WHERE id IN ('starter','standard','performance')`, false, nil
	}
	issuerTable := ""
	if version == 2 && q == "ALTER TABLE invoices ADD COLUMN issuer_snapshot JSON NULL" {
		issuerTable = "invoices"
	}
	if version == 4 && q == "ALTER TABLE orders ADD COLUMN issuer_snapshot JSON NULL" {
		issuerTable = "orders"
	}
	if issuerTable != "" {
		var dataType, nullable, extra, generated string
		var defaultValue sql.NullString
		err := conn.QueryRowContext(ctx, "SELECT DATA_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND COLUMN_NAME='issuer_snapshot'", issuerTable).Scan(&dataType, &nullable, &extra, &generated, &defaultValue)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		if dataType != "json" || nullable != "YES" || extra != "" || generated != "" || defaultValue.Valid {
			return "", false, errors.New("existing issuer snapshot column differs from migration")
		}
		return q, true, nil
	}
	if strings.HasPrefix(q, "ALTER TABLE ") && strings.Contains(q, " ADD CONSTRAINT ") {
		fields := strings.Fields(q)
		checkAt := strings.Index(q, " CHECK(")
		if len(fields) < 7 || fields[3] != "ADD" || fields[4] != "CONSTRAINT" || checkAt < 0 {
			return "", false, errors.New("unsupported resumable constraint migration")
		}
		var kind, enforced, actual string
		err := conn.QueryRowContext(ctx, "SELECT tc.CONSTRAINT_TYPE,tc.ENFORCED,COALESCE(cc.CHECK_CLAUSE,'') FROM information_schema.TABLE_CONSTRAINTS tc LEFT JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.CONSTRAINT_SCHEMA=DATABASE() AND tc.TABLE_NAME=? AND tc.CONSTRAINT_NAME=?", fields[2], fields[5]).Scan(&kind, &enforced, &actual)
		if errors.Is(err, sql.ErrNoRows) {
			return q, false, nil
		}
		if err != nil {
			return "", false, err
		}
		actualNormalized, actualOK := normalizeMigrationCheck(actual)
		expectedNormalized, expectedOK := normalizeMigrationCheck(q[checkAt+len(" CHECK"):])
		if kind != "CHECK" || enforced != "YES" || !actualOK || !expectedOK || actualNormalized != expectedNormalized {
			return "", false, errors.New("existing check constraint differs from migration")
		}
		return q, true, nil
	}
	return q, false, nil
}

// Normalize only the small expression grammar used by the shipped checks.
// Identifier/keyword case and MySQL's rendering of charset introducers are
// irrelevant; literal case and expression grouping must remain significant.
func normalizeMigrationCheck(expression string) (string, bool) {
	// CHECK_CLAUSE on MySQL 8.4 escapes the quote delimiters of charset
	// literals. Shipped checks contain only unescaped ASCII currency literals.
	expression = strings.ReplaceAll(expression, `\'`, `'`)
	var tokens []string
	for i := 0; i < len(expression); {
		c := expression[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			i++
			continue
		}
		if c == '`' {
			end := strings.IndexByte(expression[i+1:], '`')
			if end < 0 {
				return "", false
			}
			name := expression[i+1 : i+1+end]
			if name == "" {
				return "", false
			}
			for _, n := range name {
				if !((n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') || n == '_') {
					return "", false
				}
			}
			tokens = append(tokens, strings.ToLower(name))
			i += end + 2
			continue
		}
		if c == '\'' {
			end := strings.IndexByte(expression[i+1:], '\'')
			if end < 0 {
				return "", false
			}
			literal := expression[i+1 : i+1+end]
			if literal != "USD" && literal != "CNY" {
				return "", false
			}
			tokens = append(tokens, "'"+literal+"'")
			i += end + 2
			continue
		}
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
			end := i + 1
			for end < len(expression) {
				n := expression[end]
				if !((n >= 'a' && n <= 'z') || (n >= 'A' && n <= 'Z') || (n >= '0' && n <= '9') || n == '_') {
					break
				}
				end++
			}
			word := strings.ToLower(expression[i:end])
			if word == "_utf8mb4" || word == "_ascii" {
				if end >= len(expression) || expression[end] != '\'' {
					return "", false
				}
			} else {
				tokens = append(tokens, word)
			}
			i = end
			continue
		}
		if c >= '0' && c <= '9' {
			end := i + 1
			for end < len(expression) && expression[end] >= '0' && expression[end] <= '9' {
				end++
			}
			tokens = append(tokens, expression[i:end])
			i = end
			continue
		}
		if c == '(' || c == ')' || c == ',' || c == '=' || c == '<' || c == '>' {
			token := string(c)
			i++
			if (c == '<' || c == '>') && i < len(expression) && expression[i] == '=' {
				token += "="
				i++
			}
			tokens = append(tokens, token)
			continue
		}
		return "", false
	}
	// MySQL renders CAST(currency AS BINARY) as CAST(currency AS CHAR CHARSET
	// BINARY). Recognize only this exact currency expression; other charsets,
	// explicit lengths and different operands must not gain binary semantics.
	for i := 0; i+7 < len(tokens); i++ {
		if strings.Join(tokens[i:i+8], " ") == "cast ( currency as char charset binary )" {
			tokens = append(tokens[:i+4], tokens[i+6:]...)
		}
	}
	for len(tokens) >= 2 && tokens[0] == "(" && tokens[len(tokens)-1] == ")" {
		depth := 0
		outer := true
		for i, token := range tokens {
			if token == "(" {
				depth++
			} else if token == ")" {
				depth--
			}
			if depth < 0 || (depth == 0 && i != len(tokens)-1) {
				outer = false
				break
			}
		}
		if !outer || depth != 0 {
			break
		}
		tokens = tokens[1 : len(tokens)-1]
	}
	return strings.Join(tokens, " "), len(tokens) != 0
}
