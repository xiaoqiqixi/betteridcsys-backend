package billing

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// SchemaStatus contains database facts, never connection/user credentials or
// SQL/server diagnostics. Status is scoped to schema compatibility, not a claim
// that a host, backup, administrator workflow or application is ready to serve.
type SchemaStatus struct {
	ReadOnly                bool              `json:"read_only"`
	Vendor                  string            `json:"vendor"`
	ServerVersion           string            `json:"server_version"`
	Database                string            `json:"database"`
	Transport               string            `json:"transport"`
	Status                  string            `json:"status"`
	EmptyTarget             bool              `json:"empty_target"`
	EmptyTargetScope        string            `json:"empty_target_scope"`
	TableVisibilityVerified bool              `json:"table_visibility_verified"`
	SchemaVersion           int               `json:"schema_version"`
	RequiredSchemaVersion   int               `json:"required_schema_version"`
	MigrationChecksums      map[string]string `json:"migration_checksums"`
	PendingMigrations       []int             `json:"pending_migrations"`
	AdminCount              *int64            `json:"admin_count"`
	Error                   string            `json:"error,omitempty"`
}

var preflightDatabaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var preflightMySQLVersion = regexp.MustCompile(`^8\.4\.[0-9]+(?:[-+][A-Za-z0-9_.-]+)?$`)
var preflightMySQLVendor = regexp.MustCompile(`^MySQL (Community|Enterprise) Server(?: - (?:GPL|Commercial|Advanced Edition))?$`)

func validPreflightDatabase(name string) bool {
	if !preflightDatabaseName.MatchString(name) {
		return false
	}
	switch strings.ToLower(name) {
	case "mysql", "sys", "information_schema", "performance_schema":
		return false
	}
	return true
}

// INFORMATION_SCHEMA filters objects by the account's privileges. An empty
// result must not turn a table-scoped grant into permission to initialize a
// supposedly empty database. Restrict this proof to direct, full SELECT grants;
// roles and partial revokes need a DBA's separate assessment, not inference.
func preflightTableVisibility(ctx context.Context, conn *sql.Conn, database string) error {
	var role string
	if err := conn.QueryRowContext(ctx, "SELECT CURRENT_ROLE()").Scan(&role); err != nil {
		return err
	}
	if role != "NONE" {
		return errors.New("active database roles require separate visibility review")
	}
	rows, err := conn.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER")
	if err != nil {
		return err
	}
	defer rows.Close()
	visible, count := false, 0
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return err
		}
		if count >= 128 || len(grant) > 8192 || strings.HasPrefix(grant, "REVOKE ") {
			return errors.New("unsupported database grant visibility")
		}
		count++
		privileges, target, ok := strings.Cut(grant, " ON ")
		if !ok || !strings.HasPrefix(privileges, "GRANT ") {
			continue
		}
		target, _, ok = strings.Cut(target, " TO ")
		if !ok {
			continue
		}
		// SHOW GRANTS may escape the database wildcard characters. Both forms
		// cover this selected database; never accept a different wildcard target.
		quoted := "`" + database + "`.*"
		escaped := "`" + strings.ReplaceAll(database, "_", `\_`) + "`.*"
		if target != "*.*" && target != quoted && target != escaped {
			continue
		}
		for _, privilege := range strings.Split(strings.TrimPrefix(privileges, "GRANT "), ",") {
			if value := strings.TrimSpace(privilege); value == "SELECT" || value == "ALL PRIVILEGES" {
				visible = true
			}
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if !visible {
		return errors.New("direct database-wide SELECT visibility is required")
	}
	return nil
}

func readPreflightHistory(ctx context.Context, conn *sql.Conn) (map[string]string, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version LIMIT 65")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checksums := map[string]string{}
	for rows.Next() {
		var version int
		var checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		index := len(checksums)
		if index >= len(schemaVersions) || version != index+1 || checksum != digest(schemaVersions[index]) {
			return nil, errors.New("migration history is not an unchanged known prefix")
		}
		checksums[strconv.Itoa(version)] = checksum
	}
	return checksums, rows.Err()
}

// SchemaStatus uses SELECT only. In particular it does not call Migrate, create
// schema_migrations, acquire migration locks, or call integrations/services.
// Metadata visibility is limited to the supplied account; operators must keep
// migrations stopped while interpreting this point-in-time inspection.
func (m *MySQL) SchemaStatus(ctx context.Context) (report SchemaStatus, err error) {
	report = SchemaStatus{ReadOnly: true, Vendor: "unknown", Transport: m.transport, Status: "incompatible", EmptyTargetScope: "base_tables", RequiredSchemaVersion: len(schemaVersions), MigrationChecksums: map[string]string{}, PendingMigrations: []int{}}
	defer func() {
		if err != nil {
			report.Status = "incompatible"
			report.Error = "database inspection failed; verify target, MySQL 8.4, read privileges and unchanged schema"
		}
	}()
	if !validPreflightDatabase(m.database) {
		return report, errors.New("explicit non-system database required")
	}
	conn, err := m.DB.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer conn.Close()
	var selected sql.NullString
	var comment, charset, collation string
	if err = conn.QueryRowContext(ctx, "SELECT VERSION(),@@version_comment,DATABASE(),@@character_set_database,@@collation_database").Scan(&report.ServerVersion, &comment, &selected, &charset, &collation); err != nil {
		return report, err
	}
	if !selected.Valid || selected.String != m.database || !validPreflightDatabase(selected.String) {
		return report, errors.New("selected database mismatch")
	}
	report.Database = selected.String
	if strings.Contains(strings.ToLower(report.ServerVersion+" "+comment), "mariadb") || strings.Contains(strings.ToLower(report.ServerVersion+" "+comment), "percona") || !preflightMySQLVersion.MatchString(report.ServerVersion) || !preflightMySQLVendor.MatchString(comment) {
		return report, errors.New("supported MySQL 8.4 required")
	}
	report.Vendor = "MySQL"
	if charset != "utf8mb4" {
		return report, errors.New("utf8mb4 database required")
	}
	if err = preflightTableVisibility(ctx, conn, m.database); err != nil {
		return report, err
	}
	report.TableVisibilityVerified = true
	rows, err := conn.QueryContext(ctx, "SELECT TABLE_NAME,TABLE_TYPE,COALESCE(ENGINE,'') FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() ORDER BY TABLE_NAME LIMIT 33")
	if err != nil {
		return report, err
	}
	tables := map[string]string{}
	for rows.Next() {
		var table, kind, engine string
		if err = rows.Scan(&table, &kind, &engine); err != nil {
			rows.Close()
			return report, err
		}
		if len(tables) >= 32 || kind != "BASE TABLE" || engine != "InnoDB" {
			rows.Close()
			return report, errors.New("unexpected database objects")
		}
		tables[table] = engine
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	var customObjects int
	if err = conn.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM information_schema.TRIGGERS WHERE TRIGGER_SCHEMA=DATABASE())+(SELECT COUNT(*) FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA=DATABASE())+(SELECT COUNT(*) FROM information_schema.EVENTS WHERE EVENT_SCHEMA=DATABASE())").Scan(&customObjects); err != nil {
		return report, err
	}
	if customObjects != 0 {
		return report, errors.New("custom database objects require independent review")
	}
	if len(tables) == 0 {
		report.Status = "empty"
		report.EmptyTarget = true
		for version := range schemaVersions {
			report.PendingMigrations = append(report.PendingMigrations, version+1)
		}
		return report, nil
	}
	if tables["schema_migrations"] == "" {
		return report, errors.New("nonempty database without migration history")
	}
	report.MigrationChecksums, err = readPreflightHistory(ctx, conn)
	if err != nil {
		return report, err
	}
	report.SchemaVersion = len(report.MigrationChecksums)
	if report.SchemaVersion == 0 {
		return report, errors.New("partial database without applied migration")
	}
	if err = validatePreflightShape(ctx, conn, report.SchemaVersion, tables, collation); err != nil {
		return report, err
	}
	var admins int64
	if err = conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role='admin'").Scan(&admins); err != nil {
		return report, err
	}
	report.AdminCount = &admins
	// Detect concurrent history changes without acquiring a database lock.
	after, historyErr := readPreflightHistory(ctx, conn)
	if historyErr != nil || len(after) != len(report.MigrationChecksums) {
		return report, errors.New("migration history changed during inspection")
	}
	for version, checksum := range after {
		if report.MigrationChecksums[version] != checksum {
			return report, errors.New("migration history changed during inspection")
		}
	}
	for version := report.SchemaVersion + 1; version <= len(schemaVersions); version++ {
		report.PendingMigrations = append(report.PendingMigrations, version)
	}
	report.Status = "pending_migrations"
	if len(report.PendingMigrations) == 0 {
		report.Status = "current"
	}
	return report, nil
}
