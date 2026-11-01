package billing

import (
	"aster/billing/migrations"
	"context"
	"database/sql"
	"errors"
	"strings"
)

var accountSchema = map[string]string{
	"affiliates":          "user_id:char(32)! code:varchar(64)! created_at:datetime(6)!",
	"affiliate_referrals": "client_id:char(32)! affiliate_id:char(32)! created_at:datetime(6)! converted:tinyint(1)!",
	"account_profiles":    "user_id:char(32)! document:json! locale:varchar(5)! email_verified:tinyint(1)! notes:text! closed:tinyint(1)! referrer_id:char(32)?",
	"account_sessions":    "id:char(32)! token_hash:char(64)! ip:varchar(45)! user_agent:varchar(512)! created_at:datetime(6)! last_seen_at:datetime(6)!",
	"email_tokens":        "token_hash:char(64)! user_id:char(32)! purpose:varchar(32)! expires_at:datetime(6)! created_at:datetime(6)!",
	"login_events":        "id:char(32)! user_id:char(32)? email_hash:char(64)! ip:varchar(45)! user_agent:varchar(512)! result:varchar(7)! created_at:datetime(6)!",
	"notification_prefs":  "user_id:char(32)! document:json!",
	"account_settings":    "name:varchar(32)! document:json!",
	"account_requests":    "principal:char(64)! operation:varchar(128)! request_key:varchar(128)! fingerprint:char(64)! response:mediumtext? created_at:datetime(6)!",
	"email_templates":     "template_key:varchar(32)! subject:json! body:json! enabled:tinyint(1)! revision:bigint!",
	"email_outbox":        "id:char(32)! user_id:char(32)! template_key:varchar(32)! variables:json! protected_variables:mediumtext? attempts:int! next_attempt_at:datetime(6)! sent_at:datetime(6)? failed_at:datetime(6)? last_error:varchar(64)! created_at:datetime(6)!",
}

var accountFeatureSchema = map[string]string{
	"ssh_keys":            "id:char(32)! user_id:char(32)! name:varchar(80)! public_key:text! algorithm:varchar(40)! fingerprint:varchar(100)! created_at:datetime(6)!",
	"api_tokens":          "id:char(32)! user_id:char(32)! token_hash:char(64)! name:varchar(60)! prefix:varchar(16)! scopes:json! ip_allowlist:json! created_at:datetime(6)! last_used_at:datetime(6)? expires_at:datetime(6)?",
	"support_departments": "id:varchar(64)! document:json! visible:tinyint(1)! sort_order:bigint!",
	"tickets":             "id:char(32)! number:varchar(64)! user_id:char(32)! department_id:varchar(64)! subject:varchar(200)! status:varchar(20)! priority:varchar(6)! service_id:char(32)? assignee_id:char(32)? created_at:datetime(6)! last_reply_at:datetime(6)!",
	"ticket_messages":     "id:char(32)! ticket_id:char(32)! author_id:char(32)? author_type:varchar(6)! author_name:varchar(120)! body:mediumtext! internal:tinyint(1)! created_at:datetime(6)!",
	"announcements":       "id:char(32)! title:json! body:json! pinned:tinyint(1)! published:tinyint(1)! published_at:datetime(6)!",
}
var accountFeatureKeys = []string{"ssh_keys:id", "ssh_keys:user_id,fingerprint", "api_tokens:id", "api_tokens:token_hash", "support_departments:id", "tickets:id", "tickets:number", "ticket_messages:id", "announcements:id"}
var accountFeatureFKs = []string{"ssh_keys:user_id:users:id:CASCADE", "api_tokens:user_id:users:id:CASCADE", "tickets:user_id:users:id:RESTRICT", "tickets:department_id:support_departments:id:RESTRICT", "tickets:service_id:services:id:RESTRICT", "tickets:assignee_id:users:id:RESTRICT", "ticket_messages:ticket_id:tickets:id:CASCADE", "ticket_messages:author_id:users:id:RESTRICT"}

var affiliateColumns = map[string]string{"affiliate_commissions": "id:char(32)! affiliate_id:char(32)! client_id:char(32)! invoice_id:char(32)! currency:char(3)! decimal_places:int! basis_points:int! amount_minor:bigint! withdrawn_minor:bigint! status:enum('pending','available','withdrawn','cancelled')! created_at:datetime(6)! released_at:datetime(6)?"}

func accountSchemaColumns(version int, specs map[string]string) {
	if schemaIncludes(version, migrations.AffiliateCommissions) {
		for k, v := range affiliateColumns {
			specs[k] = v
		}
	}
	if schemaIncludes(version, migrations.AccountFeatures) {
		for k, v := range accountFeatureSchema {
			specs[k] = v
		}
	}
	if !schemaIncludes(version, migrations.AccountSecurity) {
		return
	}
	for k, v := range accountSchema {
		specs[k] = v
	}
	specs["users"] += " currency:char(3)! currency_locked:tinyint(1)!"
}
func prepareAccountMigration(ctx context.Context, conn *sql.Conn, version int, q string) (string, bool, error) {
	tables := accountSchema
	if version > 0 && version <= len(schemaVersions) && schemaVersions[version-1] == migrations.AccountFeatures {
		tables = accountFeatureSchema
	}
	for table := range tables {
		if strings.HasPrefix(q, "CREATE TABLE IF NOT EXISTS "+table+" (") {
			var kind, engine, collation string
			err := conn.QueryRowContext(ctx, "SELECT TABLE_TYPE,COALESCE(ENGINE,''),@@collation_database FROM information_schema.TABLES WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME=?", table).Scan(&kind, &engine, &collation)
			if errors.Is(err, sql.ErrNoRows) {
				return q, false, nil
			}
			if err != nil {
				return "", false, err
			}
			if kind != "BASE TABLE" || engine != "InnoDB" {
				return "", false, errors.New("invalid account schema object")
			}
			if err = validatePreflightTableShape(ctx, conn, version, map[string]string{table: engine}, collation, true); err != nil {
				return "", false, err
			}
			return q, true, nil
		}
	}
	for _, field := range []string{"currency", "currency_locked"} {
		if strings.HasPrefix(q, "ALTER TABLE users ADD COLUMN "+field+" ") {
			var kind, nullable, extra, generated, dbCollation string
			var def, charset, collation sql.NullString
			err := conn.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,EXTRA,GENERATION_EXPRESSION,COLUMN_DEFAULT,CHARACTER_SET_NAME,COLLATION_NAME,@@collation_database FROM information_schema.COLUMNS WHERE TABLE_SCHEMA=DATABASE() AND TABLE_NAME='users' AND COLUMN_NAME=?", field).Scan(&kind, &nullable, &extra, &generated, &def, &charset, &collation, &dbCollation)
			if errors.Is(err, sql.ErrNoRows) {
				return q, false, nil
			}
			if err != nil {
				return "", false, err
			}
			spec := preflightColumns(version)["users"][field]
			if !preflightColumnMatches("users", field, spec, kind, nullable, extra, generated, def, charset, collation, dbCollation) {
				return "", false, errors.New("invalid account user column")
			}
			return q, true, nil
		}
	}
	if strings.HasPrefix(q, "ALTER TABLE users ADD CONSTRAINT ") {
		return prepareMigrationStatement(ctx, conn, 0, q)
	}
	return q, false, nil
}
