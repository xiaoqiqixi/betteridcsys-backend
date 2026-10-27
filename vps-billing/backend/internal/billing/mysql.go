package billing

import (
	"aster/billing/migrations"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type MySQL struct {
	DB                  *sql.DB
	transport, database string
}

func mysqlConfiguration(dsn string, allowInsecure bool) (*mysql.Config, error) {
	cfg, e := mysql.ParseDSN(dsn)
	if e != nil {
		return nil, errors.New("invalid MySQL configuration")
	}
	// The driver sends arbitrary Params as a raw SET statement during
	// connection setup, before Ping or any read-only inspection. Charset also
	// causes SET NAMES. Core has no need for DSN-supplied server/session SQL;
	// reject it for every command so preflight cannot mutate a privileged DB.
	// Clear identity/transport before inspecting normalized options: '?' and
	// '/' are legal inside passwords/socket paths and must not change parsing.
	probe := cfg.Clone()
	probe.User, probe.Passwd, probe.Net, probe.Addr, probe.DBName = "", "", "tcp", "127.0.0.1:3306", ""
	_, query, _ := strings.Cut(probe.FormatDSN(), "?")
	options, queryErr := url.ParseQuery(query)
	_, setsCharset := options["charset"]
	if len(cfg.Params) != 0 || setsCharset || cfg.Collation != "" || queryErr != nil {
		return nil, errors.New("MySQL DSN server variables, charset and collation overrides are not supported")
	}
	if allowInsecure {
		if cfg.Net != "unix" || !filepath.IsAbs(cfg.Addr) || strings.ContainsAny(cfg.Addr, "\x00\r\n") {
			return nil, errors.New("the insecure MySQL exception requires an absolute local Unix socket")
		}
	} else if cfg.TLSConfig != "true" || cfg.AllowFallbackToPlaintext {
		return nil, errors.New("MySQL TLS is required; use tls=true or the explicit local Unix socket exception")
	}
	// Neither transport may silently downgrade certificate verification.
	if cfg.AllowFallbackToPlaintext || cfg.TLSConfig == "skip-verify" || cfg.TLSConfig == "preferred" {
		return nil, errors.New("MySQL TLS verification cannot be weakened")
	}
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.MultiStatements = false
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	return cfg, nil
}

func OpenMySQL(ctx context.Context, dsn string, allowInsecure bool) (*MySQL, error) {
	cfg, e := mysqlConfiguration(dsn, allowInsecure)
	if e != nil {
		return nil, e
	}
	db, e := sql.Open("mysql", cfg.FormatDSN())
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		return nil, errors.New("database connection failed")
	}
	transport := "tls"
	if cfg.Net == "unix" {
		transport = "local-unix-socket"
	}
	return &MySQL{DB: db, transport: transport, database: cfg.DBName}, nil
}

var schemaVersions = []string{migrations.Initial, migrations.SiteSettings, migrations.Currencies, migrations.BillingSnapshots, migrations.ExactCurrencyCodes, migrations.EmailSettings, migrations.CurrencyRegistry, migrations.PaymentRuntime, migrations.AuditQueryIndexes, migrations.CustomerAccounts, migrations.CatalogTaxonomy, migrations.CatalogNodes, migrations.ProductConfigurations}

func (m *MySQL) Ping(ctx context.Context) error {
	rows, err := m.DB.QueryContext(ctx, "SELECT version,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var version int
		var checksum string
		if err = rows.Scan(&version, &checksum); err != nil {
			return err
		}
		if version != n+1 || n >= len(schemaVersions) || checksum != digest(schemaVersions[n]) {
			return errors.New("schema version mismatch")
		}
		n++
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if n != len(schemaVersions) {
		return errors.New("schema upgrade required")
	}
	return nil
}

func (m *MySQL) Migrate(ctx context.Context) error {
	conn, err := m.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK('aster_schema_migration',30)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("could not obtain migration lock")
	}
	defer conn.ExecContext(context.Background(), "SELECT RELEASE_LOCK('aster_schema_migration')")
	if _, err = conn.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_migrations(version INT PRIMARY KEY, checksum CHAR(64) NOT NULL, applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)) ENGINE=InnoDB"); err != nil {
		return err
	}
	if err = validateAppliedMigrations(ctx, conn, schemaVersions); err != nil {
		return err
	}
	for index, source := range schemaVersions {
		version := index + 1
		var checksum string
		err = conn.QueryRowContext(ctx, "SELECT checksum FROM schema_migrations WHERE version=?", version).Scan(&checksum)
		if err == nil {
			if checksum != digest(source) {
				return errors.New("migration checksum mismatch; never edit applied migrations")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if version == 3 {
			// MySQL generated names depend on the original schema. Identify only legacy USD checks.
			rows, e := conn.QueryContext(ctx, "SELECT tc.TABLE_NAME,tc.CONSTRAINT_NAME,cc.CHECK_CLAUSE FROM information_schema.TABLE_CONSTRAINTS tc JOIN information_schema.CHECK_CONSTRAINTS cc ON tc.CONSTRAINT_SCHEMA=cc.CONSTRAINT_SCHEMA AND tc.CONSTRAINT_NAME=cc.CONSTRAINT_NAME WHERE tc.CONSTRAINT_SCHEMA=DATABASE() AND tc.TABLE_NAME IN ('orders','invoices') AND tc.CONSTRAINT_TYPE='CHECK'")
			if e != nil {
				return e
			}
			var drop []string
			for rows.Next() {
				var table, name, clause string
				if e = rows.Scan(&table, &name, &clause); e != nil {
					rows.Close()
					return e
				}
				normalized, ok := normalizeMigrationCheck(clause)
				legacy, _ := normalizeMigrationCheck("currency = 'USD'")
				// Only the generated legacy USD checks from migration 1 may be removed.
				if ok && normalized == legacy && name == table+"_chk_2" {
					drop = append(drop, "ALTER TABLE `"+table+"` DROP CHECK `"+strings.ReplaceAll(name, "`", "``")+"`")
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, q := range drop {
				if _, e = conn.ExecContext(ctx, q); e != nil {
					return e
				}
			}
		}
		if version == 7 {
			if err = migrateCurrencyRegistry(ctx, conn); err != nil {
				return fmt.Errorf("migration 7 failed: %w", err)
			}
		}
		if version == 8 {
			// Validate every existing payment object before creating either new
			// table. A colliding second object must not cause partial new DDL.
			for _, q := range strings.Split(source, ";") {
				q = strings.TrimSpace(q)
				if q == "" {
					continue
				}
				if _, _, err = prepareMigrationStatement(ctx, conn, version, q); err != nil {
					return fmt.Errorf("migration 8 preparation failed: %w", err)
				}
			}
		}
		if version == 9 {
			// Validate every colliding index before creating any new object.
			for _, q := range strings.Split(source, ";") {
				if q = strings.TrimSpace(q); q == "" {
					continue
				}
				if _, _, err = prepareAuditIndex(ctx, conn, q); err != nil {
					return fmt.Errorf("migration 9 preparation failed: %w", err)
				}
			}
		}
		if version == 13 {
			for _, q := range strings.Split(source, ";") {
				if q = strings.TrimSpace(q); q != "" {
					if _, _, err = prepareProductConfigurationMigration(ctx, conn, q); err != nil {
						return fmt.Errorf("migration 13 preparation failed: %w", err)
					}
				}
			}
		}
		if version == 12 {
			if err = prevalidateCatalogNodesMigration(ctx, conn, source); err != nil {
				return fmt.Errorf("migration 12 preparation failed: %w", err)
			}
		}
		if version == 11 {
			if err = prevalidateTaxonomyMigration(ctx, conn, source); err != nil {
				return fmt.Errorf("migration 11 preparation failed: %w", err)
			}
		}
		if version == 10 {
			if err = prevalidateCustomerAccountMigration(ctx, conn, source); err != nil {
				return fmt.Errorf("migration 10 preparation failed: %w", err)
			}
		}
		for _, q := range strings.Split(source, ";") {
			if version == 7 {
				break
			}
			q = strings.TrimSpace(q)
			if q == "" {
				continue
			}
			var skip bool
			q, skip, err = prepareMigrationStatement(ctx, conn, version, q)
			if err != nil {
				return fmt.Errorf("migration %d preparation failed: %w", version, err)
			}
			if skip {
				continue
			}
			if _, err = conn.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("migration %d failed: %w", version, err)
			}
		}
		if version == 1 {
			for _, p := range Catalog() {
				_, err = conn.ExecContext(ctx, "INSERT IGNORE INTO plans(id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available) VALUES(?,?,?,?,?,?,?,?,?,?,?)", p.ID, p.Name, p.Description, p.CPU, p.MemoryGB, p.DiskGB, p.TransferTB, p.Region, 0, "USD", false)
				if err != nil {
					return err
				}
			}
		}
		if version == 2 {
			raw, _ := json.Marshal(DefaultSettings())
			if _, err = conn.ExecContext(ctx, "INSERT IGNORE INTO site_settings(id,document) VALUES(1,?)", raw); err != nil {
				return err
			}
		}
		if version == 6 {
			if err = initializeEmailSettings(ctx, conn); err != nil {
				return err
			}
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO schema_migrations(version,checksum) VALUES(?,?)", version, digest(source)); err != nil {
			return err
		}
	}
	return nil
}

const userColumns = "id,email,name,role,password_hash,disabled,revision"

func scanUser(s interface{ Scan(...any) error }) (User, error) {
	var u User
	e := s.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.PasswordHash, &u.Disabled, &u.Revision)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return u, e
}
func (m *MySQL) UserByEmail(c context.Context, e string) (User, error) {
	return scanUser(m.DB.QueryRowContext(c, "SELECT "+userColumns+" FROM users WHERE email=?", e))
}
func (m *MySQL) UserByID(c context.Context, id string) (User, error) {
	return scanUser(m.DB.QueryRowContext(c, "SELECT "+userColumns+" FROM users WHERE id=?", id))
}
func (m *MySQL) AddUser(c context.Context, u User) error {
	_, e := m.DB.ExecContext(c, "INSERT INTO users(id,email,name,role,password_hash,disabled,revision) VALUES(?,?,?,?,?,?,?)", u.ID, u.Email, u.Name, u.Role, u.PasswordHash, u.Disabled, normalizedUserRevision(u).Revision)
	return e
}
func (m *MySQL) Users(c context.Context) ([]User, error) {
	r, e := m.DB.QueryContext(c, "SELECT "+userColumns+" FROM users ORDER BY created_at DESC LIMIT 1000")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []User{}
	for r.Next() {
		u, e := scanUser(r)
		if e != nil {
			return nil, e
		}
		out = append(out, u)
	}
	return out, r.Err()
}
func (m *MySQL) PutSession(c context.Context, s Session) error {
	tx, err := m.DB.BeginTx(c, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	u, err := scanUser(tx.QueryRowContext(c, "SELECT "+userColumns+" FROM users WHERE id=? FOR SHARE", s.UserID))
	if err != nil {
		return err
	}
	if u.Disabled || s.Revision != 0 && s.Revision != u.Revision {
		return ErrConflict
	}
	if _, err = tx.ExecContext(c, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", s.Hash, s.UserID, s.Expires); err != nil {
		return err
	}
	return tx.Commit()
}
func (m *MySQL) Session(c context.Context, k string) (Session, error) {
	var s Session
	e := m.DB.QueryRowContext(c, "SELECT token_hash,user_id,expires_at FROM sessions WHERE token_hash=? AND expires_at>UTC_TIMESTAMP(6)", k).Scan(&s.Hash, &s.UserID, &s.Expires)
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return s, e
}
func (m *MySQL) DeleteSession(c context.Context, k string) error {
	_, e := m.DB.ExecContext(c, "DELETE FROM sessions WHERE token_hash=?", k)
	return e
}
func scanPlan(s interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	e := s.Scan(&p.ID, &p.Name, &p.Description, &p.CPU, &p.MemoryGB, &p.DiskGB, &p.TransferTB, &p.Region, &p.PriceMinor, &p.Currency, &p.Available, &p.TypeID, &p.RegionID, &p.NodeID)
	return p, e
}

const planColumns = "id,name,description,cpu,memory_gb,disk_gb,transfer_tb,region,price_minor,currency,available,type_id,region_id,node_id"

func (m *MySQL) Plans(c context.Context, currency string) ([]Plan, error) {
	tx, err := m.DB.BeginTx(c, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	config, priceRevision, err := readCurrencies(c, tx, "")
	if err != nil {
		return nil, err
	}
	taxonomy, err := readTaxonomy(c, tx, "")
	if err != nil {
		return nil, err
	}
	cur, known := config.find(currency)
	if !known {
		return nil, ErrCurrency
	}
	rows, err := tx.QueryContext(c, "SELECT p.id,p.name,p.description,p.cpu,p.memory_gb,p.disk_gb,p.transfer_tb,p.region,COALESCE(pp.amount_minor,0),?,(p.available AND COALESCE(pp.available,FALSE) AND COALESCE(pp.amount_minor,0)>0),p.type_id,p.region_id,p.node_id FROM plans p LEFT JOIN plan_prices pp ON pp.plan_id=p.id AND pp.currency=? WHERE p.available=TRUE AND EXISTS (SELECT 1 FROM catalog_nodes n JOIN catalog_regions r ON r.id=n.region_id JOIN catalog_types t ON t.id=r.type_id WHERE n.id=p.node_id AND r.id=p.region_id AND t.id=p.type_id AND n.enabled=TRUE AND r.enabled=TRUE AND t.enabled=TRUE) ORDER BY COALESCE(pp.amount_minor,0),p.id", currency, currency)
	if err != nil {
		return nil, err
	}
	out := []Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		if p.PriceMinor < 0 || p.PriceMinor > MaxMinor {
			rows.Close()
			return nil, ErrPlanPrices
		}
		p.CurrencyFormat = cur.CurrencyFormat
		p.PriceRevision = priceRevision
		p.TaxonomyRevision = taxonomy.Revision
		p.Available = p.Available && cur.Enabled && validAmount(p.PriceMinor)
		out = append(out, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	return out, err
}

const orderColumns = "id,number,user_id,plan_name,status,total_minor,currency,created_at,hostname,issuer_snapshot,currency_snapshot,plan_snapshot"

// A memo belongs to one Orders call and retains only its last successful
// currency decode. Snapshot bytes and the record's currency both identify it.
type currencySnapshotMemo struct {
	hasValid bool
	code     string
	raw      []byte
	format   CurrencyFormat
}

func (m *currencySnapshotMemo) decode(raw []byte, code string) (CurrencyFormat, error) {
	if m.hasValid && m.code == code && bytes.Equal(m.raw, raw) {
		return m.format, nil
	}
	format, err := decodeCurrencySnapshot(raw, code)
	if err != nil {
		return format, err
	}
	m.raw = append(m.raw[:0], raw...)
	m.hasValid, m.code, m.format = true, code, format
	return format, nil
}

func scanOrder(s interface{ Scan(...any) error }) (Order, error) {
	return scanOrderWithCurrencyMemo(s, nil)
}

func scanOrderWithCurrencyMemo(s interface{ Scan(...any) error }, memo *currencySnapshotMemo) (Order, error) {
	var o Order
	var raw, currencyRaw, planRaw []byte
	e := s.Scan(&o.ID, &o.Number, &o.UserID, &o.PlanName, &o.Status, &o.TotalMinor, &o.Currency, &o.CreatedAt, &o.Hostname, &raw, &currencyRaw, &planRaw)
	if e == nil && len(raw) > 0 {
		e = json.Unmarshal(raw, &o.Issuer)
	}
	if e == nil {
		if memo == nil {
			o.CurrencyFormat, e = decodeCurrencySnapshot(currencyRaw, o.Currency)
		} else {
			o.CurrencyFormat, e = memo.decode(currencyRaw, o.Currency)
		}
	}
	if e == nil && len(planRaw) > 0 {
		e = json.Unmarshal(planRaw, &o.PlanSnapshot)
	}
	return o, e
}
func filter(uid string) (string, []any) {
	if uid == "" {
		return "", nil
	}
	return " WHERE user_id=?", []any{uid}
}
func (m *MySQL) Orders(c context.Context, uid string) ([]Order, error) {
	w, a := filter(uid)
	r, e := m.DB.QueryContext(c, "SELECT "+orderColumns+" FROM orders"+w+" ORDER BY created_at DESC LIMIT 1000", a...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Order{}
	var memo currencySnapshotMemo
	for r.Next() {
		o, e := scanOrderWithCurrencyMemo(r, &memo)
		if e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, r.Err()
}

func (m *MySQL) RecentOrders(ctx context.Context, uid string) ([]Order, error) {
	if uid == "" {
		return nil, ErrNotFound
	}
	rows, err := m.DB.QueryContext(ctx, "SELECT "+orderColumns+" FROM orders WHERE user_id=? ORDER BY created_at DESC,id DESC LIMIT 5", uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Order, 0, recentOrderLimit)
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, rows.Err()
}

func (m *MySQL) Invoices(c context.Context, uid string) ([]Invoice, error) {
	w, a := filter(uid)
	r, e := m.DB.QueryContext(c, "SELECT id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot FROM invoices"+w+" ORDER BY issued_at DESC LIMIT 1000", a...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Invoice{}
	for r.Next() {
		var i Invoice
		var issuerRaw, currencyRaw []byte
		e = r.Scan(&i.ID, &i.Number, &i.UserID, &i.OrderID, &i.Status, &i.AmountMinor, &i.Currency, &i.IssuedAt, &i.DueAt, &i.PaidAt, &issuerRaw, &currencyRaw)
		if e != nil {
			return nil, e
		}
		if len(issuerRaw) > 0 {
			if e = json.Unmarshal(issuerRaw, &i.Issuer); e != nil {
				return nil, e
			}
		}
		i.CurrencyFormat, e = decodeCurrencySnapshot(currencyRaw, i.Currency)
		if e != nil {
			return nil, e
		}
		out = append(out, i)
	}
	return out, r.Err()
}
func (m *MySQL) Services(c context.Context, uid string) ([]Service, error) {
	w, a := filter(uid)
	r, e := m.DB.QueryContext(c, "SELECT id,user_id,name,plan_name,region,ipv4,status,next_due_at,price_minor,currency,cpu,memory_gb,disk_gb,currency_snapshot FROM services"+w+" ORDER BY next_due_at LIMIT 1000", a...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Service{}
	for r.Next() {
		var s Service
		var currencyRaw []byte
		e = r.Scan(&s.ID, &s.UserID, &s.Name, &s.PlanName, &s.Region, &s.IPv4, &s.Status, &s.NextDueAt, &s.PriceMinor, &s.Currency, &s.CPU, &s.MemoryGB, &s.DiskGB, &currencyRaw)
		if e == nil {
			s.CurrencyFormat, e = decodeCurrencySnapshot(currencyRaw, s.Currency)
		}
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, r.Err()
}
func (m *MySQL) Audits(c context.Context, uid string) ([]Audit, error) {
	w, a := filter(uid)
	r, e := m.DB.QueryContext(c, "SELECT id,user_id,action,target,created_at,actor_email,detail FROM audit_events"+w+" ORDER BY created_at DESC LIMIT 1000", a...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []Audit{}
	for r.Next() {
		var v Audit
		e = r.Scan(&v.ID, &v.UserID, &v.Action, &v.Target, &v.CreatedAt, &v.ActorEmail, &v.Detail)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, r.Err()
}
func (m *MySQL) createOrderOnce(c context.Context, u User, pid, host, key, currency, fp string, quote *PriceQuote) (Order, bool, error) {
	tx, e := m.DB.BeginTx(c, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return Order{}, false, e
	}
	defer tx.Rollback()
	_, e = tx.ExecContext(c, "INSERT INTO order_requests(user_id,request_key,fingerprint) VALUES(?,?,?) ON DUPLICATE KEY UPDATE request_key=request_key", u.ID, key, fp)
	if e != nil {
		return Order{}, false, e
	}
	var oldFP string
	var oid sql.NullString
	e = tx.QueryRowContext(c, "SELECT fingerprint,order_id FROM order_requests WHERE user_id=? AND request_key=? FOR UPDATE", u.ID, key).Scan(&oldFP, &oid)
	if e != nil {
		return Order{}, false, e
	}
	if fp != oldFP {
		return Order{}, false, ErrConflict
	}
	if oid.Valid {
		o, e := scanOrder(tx.QueryRowContext(c, "SELECT "+orderColumns+" FROM orders WHERE id=? AND user_id=?", oid.String, u.ID))
		if e != nil {
			return o, false, e
		}
		e = tx.Commit()
		return o, true, e
	}
	config, priceRevision, e := readCurrencies(c, tx, " FOR SHARE")
	if e != nil {
		return Order{}, false, e
	}
	var settingsRaw []byte
	if e = tx.QueryRowContext(c, "SELECT document FROM site_settings WHERE id=1 FOR SHARE").Scan(&settingsRaw); e != nil {
		return Order{}, false, e
	}
	var settings SiteSettings
	if e = json.Unmarshal(settingsRaw, &settings); e != nil {
		return Order{}, false, e
	}
	if !ValidateSettings(settings) || settings.DefaultCurrency != config.DefaultCurrency {
		return Order{}, false, errors.New("invalid stored site settings")
	}
	if currency == "" {
		currency = settings.DefaultCurrency
	}
	cur, known := config.find(currency)
	if !known {
		return Order{}, false, ErrCurrency
	}
	if !cur.Enabled {
		return Order{}, false, ErrCurrencyDisabled
	}
	taxonomy, e := readTaxonomy(c, tx, " FOR SHARE")
	if e != nil {
		return Order{}, false, e
	}
	p, e := scanPlan(tx.QueryRowContext(c, "SELECT p.id,p.name,p.description,p.cpu,p.memory_gb,p.disk_gb,p.transfer_tb,p.region,pp.amount_minor,pp.currency,(p.available AND pp.available),p.type_id,p.region_id,p.node_id FROM plans p JOIN plan_prices pp ON pp.plan_id=p.id AND pp.currency=? WHERE p.id=? FOR SHARE", currency, pid))
	_, classificationLive := taxonomyNode(taxonomy, p.TypeID, p.RegionID, p.NodeID, true)
	if errors.Is(e, sql.ErrNoRows) || (e == nil && (!p.Available || !classificationLive || !validAmount(p.PriceMinor))) {
		return Order{}, false, ErrUnavailable
	}
	if e != nil {
		return Order{}, false, e
	}
	if quote != nil && !quote.matches(p.PriceMinor, cur.DecimalPlaces, priceRevision) {
		return Order{}, false, ErrPriceChanged
	}
	configuration, e := readProductConfiguration(c, tx, p.ID, " FOR SHARE")
	if e != nil {
		return Order{}, false, e
	}
	p.CurrencyFormat = cur.CurrencyFormat
	o, i, a := newRecords(u, p, host)
	i.Issuer = settings.Identity()
	o.Issuer = settings.Identity()
	issuerRaw, _ := json.Marshal(i.Issuer)
	currencyRaw, _ := json.Marshal(cur.CurrencyFormat)
	planRaw, _ := json.Marshal(o.PlanSnapshot)
	_, e = tx.ExecContext(c, "INSERT INTO orders(id,number,user_id,plan_id,plan_name,hostname,status,total_minor,currency,created_at,issuer_snapshot,currency_snapshot,plan_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", o.ID, o.Number, u.ID, pid, p.Name, host, o.Status, o.TotalMinor, o.Currency, o.CreatedAt, issuerRaw, currencyRaw, planRaw)
	if e != nil {
		return Order{}, false, e
	}
	if e = insertOrderProductConfiguration(c, tx, o.ID, configuration, o.CreatedAt); e != nil {
		return Order{}, false, e
	}
	_, e = tx.ExecContext(c, "INSERT INTO invoices(id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,issuer_snapshot,currency_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?)", i.ID, i.Number, u.ID, o.ID, i.Status, i.AmountMinor, i.Currency, i.IssuedAt, i.DueAt, issuerRaw, currencyRaw)
	if e != nil {
		return Order{}, false, e
	}
	_, e = tx.ExecContext(c, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, u.ID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail)
	if e != nil {
		return Order{}, false, e
	}
	payload, _ := json.Marshal(map[string]string{"order_id": o.ID, "invoice_id": i.ID, "status": "pending_payment"})
	_, e = tx.ExecContext(c, "INSERT INTO outbox_events(id,aggregate_id,event_type,payload,created_at) VALUES(?,?,?,?,?)", ID(), o.ID, "order.created", payload, o.CreatedAt)
	if e != nil {
		return Order{}, false, e
	}
	_, e = tx.ExecContext(c, "UPDATE order_requests SET order_id=? WHERE user_id=? AND request_key=?", o.ID, u.ID, key)
	if e != nil {
		return Order{}, false, e
	}
	e = tx.Commit()
	return o, false, e
}

func (m *MySQL) Invoice(c context.Context, uid, id string) (Invoice, error) {
	var i Invoice
	var issuerRaw, currencyRaw []byte
	query := "SELECT id,number,user_id,order_id,status,amount_minor,currency,issued_at,due_at,paid_at,issuer_snapshot,currency_snapshot FROM invoices WHERE id=?"
	args := []any{id}
	if uid != "" {
		query += " AND user_id=?"
		args = append(args, uid)
	}
	e := m.DB.QueryRowContext(c, query, args...).Scan(&i.ID, &i.Number, &i.UserID, &i.OrderID, &i.Status, &i.AmountMinor, &i.Currency, &i.IssuedAt, &i.DueAt, &i.PaidAt, &issuerRaw, &currencyRaw)
	if e == nil && len(issuerRaw) > 0 {
		e = json.Unmarshal(issuerRaw, &i.Issuer)
	}
	if e == nil {
		i.CurrencyFormat, e = decodeCurrencySnapshot(currencyRaw, i.Currency)
	}
	if errors.Is(e, sql.ErrNoRows) {
		e = ErrNotFound
	}
	return i, e
}
func (m *MySQL) Summary(c context.Context, uid, currency string) (Summary, error) {
	var s Summary
	config, err := m.Currencies(c, false)
	if err != nil {
		return s, err
	}
	if _, known := config.find(currency); !known {
		return s, ErrCurrency
	}
	tx, e := m.DB.BeginTx(c, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if e != nil {
		return s, e
	}
	defer tx.Rollback()
	w, a := " WHERE currency=?", []any{currency}
	if uid != "" {
		w += " AND user_id=?"
		a = append(a, uid)
	}
	e = tx.QueryRowContext(c, "SELECT COUNT(*),COALESCE(SUM(status='active'),0),COALESCE(SUM(CASE WHEN status='active' THEN price_minor ELSE 0 END),0) FROM services"+w, a...).Scan(&s.TotalServices, &s.ActiveServices, &s.MonthlyTotal)
	if e != nil {
		return s, e
	}
	e = tx.QueryRowContext(c, "SELECT COALESCE(SUM(status='unpaid'),0),COALESCE(SUM(CASE WHEN status='paid' THEN amount_minor ELSE 0 END),0),COALESCE(SUM(CASE WHEN status='unpaid' THEN amount_minor ELSE 0 END),0) FROM invoices"+w, a...).Scan(&s.PendingInvoices, &s.Revenue, &s.UnpaidTotal)
	if e != nil {
		return s, e
	}
	if uid == "" {
		e = tx.QueryRowContext(c, "SELECT COUNT(*) FROM users WHERE role='customer'").Scan(&s.Customers)
		if e != nil {
			return s, e
		}
	}
	if s.Revenue < 0 || s.Revenue > MaxMinor || s.MonthlyTotal < 0 || s.MonthlyTotal > MaxMinor || s.UnpaidTotal < 0 || s.UnpaidTotal > MaxMinor {
		return Summary{}, errors.New("amount overflow")
	}
	e = tx.Commit()
	return s, e
}

func (m *MySQL) CreateOrder(c context.Context, u User, pid, host, key, currency string) (Order, bool, error) {
	return m.CreateQuotedOrder(c, u, pid, host, key, currency, nil)
}
func (m *MySQL) CreateQuotedOrder(c context.Context, u User, pid, host, key, currency string, quote *PriceQuote) (Order, bool, error) {
	if currency != "" && !SupportedCurrency(currency) {
		return Order{}, false, ErrCurrency
	}
	if quote != nil && (currency == "" || !quote.valid()) {
		return Order{}, false, ErrInvalidQuote
	}
	fp := orderFingerprint(pid, host, currency)
	for n := 0; n < 3; n++ {
		o, replay, e := m.createOrderOnce(c, u, pid, host, key, currency, fp, quote)
		var me *mysql.MySQLError
		if !errors.As(e, &me) || (me.Number != 1213 && me.Number != 1205) {
			return o, replay, e
		}
		select {
		case <-c.Done():
			return Order{}, false, c.Err()
		case <-time.After(time.Duration(n+1) * 30 * time.Millisecond):
		}
	}
	return Order{}, false, errors.New("transaction retry exhausted")
}

func (m *MySQL) Settings(ctx context.Context) (SiteSettings, error) {
	var raw []byte
	var s SiteSettings
	if err := m.DB.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1").Scan(&raw); err != nil {
		return s, err
	}
	err := json.Unmarshal(raw, &s)
	if err == nil && !ValidateSettings(s) {
		err = errors.New("invalid stored site settings")
	}
	return s, err
}

func (m *MySQL) UpdateSettings(ctx context.Context, actor User, settings SiteSettings) (SiteSettings, error) {
	if actor.Role != "admin" || !ValidateSettings(settings) {
		return SiteSettings{}, errors.New("invalid settings update")
	}
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return SiteSettings{}, err
	}
	defer tx.Rollback()
	config, priceRevision, err := readCurrencies(ctx, tx, " FOR UPDATE")
	if err != nil {
		return SiteSettings{}, err
	}
	cur, known := config.find(settings.DefaultCurrency)
	if !known || !cur.Enabled {
		return SiteSettings{}, ErrCurrency
	}
	var raw []byte
	var old SiteSettings
	if err = tx.QueryRowContext(ctx, "SELECT document FROM site_settings WHERE id=1 FOR UPDATE").Scan(&raw); err != nil {
		return SiteSettings{}, err
	}
	if err = json.Unmarshal(raw, &old); err != nil {
		return SiteSettings{}, err
	}
	if settings.Revision != old.Revision || settings.Revision == MaxMinor {
		return SiteSettings{}, ErrSettingsConflict
	}
	if settings.DefaultCurrency != config.DefaultCurrency {
		if config.Revision == MaxMinor {
			return SiteSettings{}, ErrCurrencyConflict
		}
		config.DefaultCurrency = settings.DefaultCurrency
		config.Revision++
		if err = writeCurrencies(ctx, tx, config, priceRevision); err != nil {
			return SiteSettings{}, err
		}
	}
	settings.Revision++
	raw, _ = json.Marshal(settings)
	if _, err = tx.ExecContext(ctx, "UPDATE site_settings SET document=? WHERE id=1", raw); err != nil {
		return SiteSettings{}, err
	}
	a := settingsAudit(actor)
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit_events(id,user_id,action,target,created_at,actor_email,detail) VALUES(?,?,?,?,?,?,?)", a.ID, a.UserID, a.Action, a.Target, a.CreatedAt, a.ActorEmail, a.Detail); err != nil {
		return SiteSettings{}, err
	}
	err = tx.Commit()
	return settings, err
}
