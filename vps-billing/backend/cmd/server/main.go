package main

import (
	"aster/billing/internal/billing"
	"aster/billing/internal/integrations"
	"aster/billing/internal/runtimeplugins"
	"aster/billing/internal/updates"
	"aster/billing/pkg/pluginapi"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/crypto/bcrypt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func secret(name string) string {
	v, e := readSecret(name)
	if e != nil {
		fatal("cannot read secret file")
	}
	return v
}
func readSecret(name string) (string, error) {
	v := os.Getenv(name)
	if f := os.Getenv(name + "_FILE"); f != "" {
		info, e := os.Stat(f)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 16384 {
			return "", errors.New("secret file must be a regular file of at most 16 KiB")
		}
		file, e := os.Open(f)
		if e != nil {
			return "", errors.New("cannot read secret file")
		}
		defer file.Close()
		if info, e = file.Stat(); e != nil || !info.Mode().IsRegular() {
			return "", errors.New("secret file must remain regular")
		}
		b, e := io.ReadAll(io.LimitReader(file, 16385))
		if e != nil || len(b) > 16384 {
			return "", errors.New("secret file is unavailable or exceeds 16 KiB")
		}
		v = strings.TrimSpace(string(b))
	}
	if len(v) > 16384 {
		return "", errors.New("secret setting exceeds 16 KiB")
	}
	return v, nil
}
func fatal(msg string) { slog.Error(msg); os.Exit(1) }
func validSessionSecret(key string) bool {
	if len(key) < 32 {
		return false
	}
	lower := strings.ToLower(key)
	for _, bad := range []string{"change", "example", "demo", "replace", "password", "your-secret"} {
		if strings.Contains(lower, bad) {
			return false
		}
	}
	seen := map[byte]bool{}
	for i := 0; i < len(key); i++ {
		seen[key[i]] = true
	}
	return len(seen) >= 8
}
func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("service", "aster-api", "core_version", pluginapi.CoreVersion))
	version := flag.Bool("version", false, "Print core version")
	migrate := flag.Bool("migrate", false, "Apply embedded MySQL schema; seeds unavailable plans only")
	create := flag.Bool("create-user", false, "Create user from BOOTSTRAP_* environment")
	preflight := flag.Bool("preflight", false, "Inspect configuration and MySQL schema read-only; print redacted JSON")
	schemaStatus := flag.Bool("schema-status", false, "Alias for -preflight (never applies migrations)")
	dispatch := flag.String("dispatch-provisioning", "", "Explicit root host command: send one paid order's prepared provisioning job once")
	reconcile := flag.String("reconcile-provisioning", "", "Explicit root host command: query a provisioning result without repeating the mutation")
	flag.Parse()
	dispatchSelected, reconcileSelected := provisioningFlagsSelected(flag.CommandLine)
	commands := 0
	for _, selected := range []bool{*version, *migrate, *create, *preflight, *schemaStatus} {
		if selected {
			commands++
		}
	}
	if dispatchSelected {
		commands++
	}
	if reconcileSelected {
		commands++
	}
	if commands > 1 || flag.NArg() != 0 {
		fatal("select one command and no positional arguments")
	}
	if *version {
		fmt.Println("Aster Core " + pluginapi.CoreVersion)
		return
	}
	if *preflight || *schemaStatus {
		os.Exit(runPreflight(os.Stdout))
	}
	if dispatchSelected || reconcileSelected {
		id := *dispatch
		if reconcileSelected {
			id = *reconcile
		}
		os.Exit(runHostProvisioning(os.Stdout, id, reconcileSelected))
	}
	mode := os.Getenv("APP_MODE")
	demo := mode == "demo"
	if mode != "" && mode != "production" && !demo {
		fatal("APP_MODE must be production or demo")
	}
	origin := os.Getenv("APP_ORIGIN")
	key := secret("SESSION_SECRET")
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if demo {
		if key == "" {
			key = billing.ID() + billing.ID()
		}
		if origin == "" {
			origin = "http://localhost:5173"
		}
		slog.Warn("DEMO MODE: in-memory data and public example credentials; never expose publicly")
	} else {
		if !validSessionSecret(key) {
			fatal("SESSION_SECRET must be a unique cryptographically random value of at least 32 bytes")
		}
		if !validProductionOrigin(origin) {
			fatal("APP_ORIGIN must be the canonical HTTPS origin without a path")
		}
	}
	local, localErr := loadLocalConfiguration(listen, demo)
	if localErr != nil {
		fatal(localErr.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var store billing.Store
	if demo {
		if *migrate || *create {
			fatal("database commands require production mode")
		}
		store = billing.NewMemory()
	} else {
		dsn := secret("MYSQL_DSN")
		if dsn == "" {
			fatal("MYSQL_DSN is required")
		}
		db, e := billing.OpenMySQL(ctx, dsn, os.Getenv("MYSQL_ALLOW_INSECURE") == "true")
		if e != nil {
			fatal("cannot initialize database; check configuration, TLS and connectivity")
		}
		defer db.DB.Close()
		store = db
		if *migrate {
			if e = db.Migrate(ctx); e != nil {
				fatal("migration failed; inspect database permissions and schema")
			}
			slog.Info("migration completed; default plans remain unavailable until reviewed")
			return
		}
		if *create {
			email := strings.ToLower(strings.TrimSpace(os.Getenv("BOOTSTRAP_EMAIL")))
			parsed, e := mail.ParseAddress(email)
			if e != nil || parsed.Address != email || strings.HasSuffix(email, "@aster.local") {
				fatal("BOOTSTRAP_EMAIL must be a valid non-demo email")
			}
			password := secret("BOOTSTRAP_PASSWORD")
			if len(password) < 16 || len(password) > 72 || strings.Contains(strings.ToLower(password), "demo") || strings.Contains(strings.ToLower(password), "password") {
				fatal("bootstrap password must be unique, 16–72 bytes, and not an example password")
			}
			role := os.Getenv("BOOTSTRAP_ROLE")
			if role == "" {
				role = "customer"
			}
			if role != "customer" && role != "admin" {
				fatal("BOOTSTRAP_ROLE must be customer or admin")
			}
			name := strings.TrimSpace(os.Getenv("BOOTSTRAP_NAME"))
			if name == "" || len(name) > 120 {
				fatal("BOOTSTRAP_NAME is required, at most 120 bytes")
			}
			h, e := bcrypt.GenerateFromPassword([]byte(password), 12)
			if e != nil {
				fatal("password hashing failed")
			}
			if e = db.AddUser(ctx, billing.User{ID: billing.ID(), Email: email, Name: name, Role: role, PasswordHash: string(h)}); e != nil {
				fatal("user creation failed; email may already exist")
			}
			slog.Info("user created")
			return
		}
	}
	// Legacy built-ins remain disabled. SMTP transport is an approved runtime
	// process accessed through a typed Unix-socket protocol, not linked here.
	registry, integrationErr := integrations.New(context.Background(), nil, integrations.Options{
		PaymentID: os.Getenv("PAYMENT_PROVIDER"), EmailID: os.Getenv("EMAIL_PROVIDER"), Configs: local.integrations,
	})
	if integrationErr != nil {
		fatal("invalid integration configuration; check installed provider IDs and required settings")
	}
	plugins := runtimeplugins.New(os.Getenv("PLUGIN_DIRECTORY"), os.Getenv("PLUGIN_POLICY_FILE"))
	updateService := updates.New(local.releaseToken, func() updates.Current {
		state := plugins.State()
		return updates.Current{CoreVersion: pluginapi.CoreVersion, SMTPVersion: state.Version, SMTPStatus: state.Status, PluginPolicyRevision: state.PolicyRevision, SMTPProtocol: state.SMTPProtocol, SMTPCoreMin: state.SMTPCoreMin, SMTPCoreMaxExclusive: state.SMTPCoreMaxExclusive}
	})
	app := billing.NewAppWithIntegrations(store, billing.Config{Updates: updateService, HostOperations: local.hostOperations, Plugins: plugins, SMTPKey: local.smtpKey, Demo: demo, Origin: origin, Secret: key, SecureCookie: !demo, TrustedProxyCIDRs: local.proxyCIDRs}, registry)
	server := &http.Server{Addr: listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: log.New(transportLogWriter{slog.Default()}, "", 0)}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		fatal("HTTP listener failed")
	}
	stop, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSignals()
	slog.Info("billing API listening", "event", "server.started", "address", listen, "demo_mode", demo)
	// Wait for active handlers before main's deferred DB.Close. Serve returning
	// ErrServerClosed only means listeners closed, not that Shutdown has drained.
	if err := runHTTPServer(stop, server, listener, 10*time.Second, slog.Default()); err != nil {
		fatal("HTTP server stopped without a complete drain")
	}
}

func validProductionOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(origin, "#") || u.User != nil {
		return false
	}
	// Browser Origin uses lowercase ASCII hosts and omits HTTPS's default port.
	hostname := u.Hostname()
	if hostname != strings.ToLower(hostname) {
		return false
	}
	for _, r := range hostname {
		if r > 127 {
			return false
		}
	}
	port := u.Port()
	expectedHost := hostname
	if ip := net.ParseIP(hostname); ip != nil {
		if hostname != ip.String() {
			return false
		}
		if strings.Contains(hostname, ":") {
			expectedHost = "[" + hostname + "]"
		}
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || number == 443 || strconv.Itoa(number) != port {
			return false
		}
		expectedHost = net.JoinHostPort(hostname, port)
	}
	return u.Host == expectedHost
}

type localConfiguration struct {
	proxyCIDRs     []string
	integrations   map[string]integrations.Config
	smtpKey        *billing.SMTPCipher
	releaseToken   string
	hostOperations *updates.OperationReader
}

// Only local validation belongs here: no provider construction, plugin RPC,
// release lookup, socket bind, user creation or migration. All production
// commands run this before any database mutation.
func loadLocalConfiguration(listen string, demo bool) (cfg localConfiguration, err error) {
	host, port, e := net.SplitHostPort(listen)
	number, portErr := strconv.Atoi(port)
	if e != nil || portErr != nil || number < 1 || number > 65535 || (!demo && (net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback())) {
		return cfg, errors.New("LISTEN_ADDR must be a valid loopback IP and port in production")
	}
	cfg.proxyCIDRs = strings.FieldsFunc(os.Getenv("TRUSTED_PROXY_CIDRS"), func(r rune) bool { return r == ',' })
	for _, cidr := range cfg.proxyCIDRs {
		if _, _, e := net.ParseCIDR(cidr); e != nil {
			return cfg, errors.New("TRUSTED_PROXY_CIDRS must contain valid comma-separated CIDRs")
		}
	}
	cfg.integrations, e = integrations.LoadConfigFile(os.Getenv("INTEGRATIONS_CONFIG_FILE"))
	if e != nil {
		return cfg, errors.New("invalid integration configuration file")
	}
	// Core installs only these disabled built-ins. Validating the selection here
	// avoids constructing any integration adapter during read-only inspection.
	for name, expected := range map[string]string{"PAYMENT_PROVIDER": "payment.disabled", "EMAIL_PROVIDER": "email.disabled"} {
		if value := os.Getenv(name); value != "" && value != expected {
			return cfg, errors.New("invalid integration configuration; check installed provider IDs and required settings")
		}
	}
	for id, config := range cfg.integrations {
		if (id != "payment.disabled" && id != "email.disabled") || len(config) != 0 {
			return cfg, errors.New("invalid disabled integration configuration")
		}
	}
	cfg.smtpKey, e = billing.LoadSMTPKey(os.Getenv("SMTP_ENCRYPTION_KEY_FILE"))
	if e != nil {
		return cfg, errors.New("invalid SMTP encryption key file")
	}
	cfg.releaseToken, e = updates.LoadTokenFile(os.Getenv("UPDATE_RELEASE_TOKEN_FILE"))
	if e != nil {
		return cfg, errors.New("invalid private release credential file")
	}
	cfg.hostOperations, e = updates.NewOperationReader(os.Getenv("HOST_OPERATION_STATUS_FILE"))
	if e != nil {
		return cfg, errors.New("invalid host operation status configuration")
	}
	return cfg, nil
}

type preflightResult struct {
	Schema               int                   `json:"schema"`
	CoreVersion          string                `json:"core_version"`
	ReadOnly             bool                  `json:"read_only"`
	ConfigurationChecked bool                  `json:"configuration_checked"`
	Checked              []string              `json:"checked"`
	NotChecked           []string              `json:"not_checked"`
	Database             *billing.SchemaStatus `json:"database,omitempty"`
	Error                string                `json:"error,omitempty"`
}

func runPreflight(output io.Writer) int {
	result := preflightResult{Schema: 1, CoreVersion: pluginapi.CoreVersion, ReadOnly: true,
		Checked: []string{}, NotChecked: []string{"database_account_least_privilege_and_hidden_non_table_metadata", "application_business_data", "concurrent_writes_or_migrations_quiesced", "plugin_approval_and_runtime_handshake", "provider_network_and_email", "listen_port_availability", "systemd_nginx_tls_firewall_disk_permissions", "backup_contents_and_isolated_restore", "full_service_readiness"}}
	finish := func(message string) int {
		result.Error = message
		if json.NewEncoder(output).Encode(result) != nil {
			return 1
		}
		if message != "" {
			return 1
		}
		return 0
	}
	mode := os.Getenv("APP_MODE")
	if mode != "" && mode != "production" {
		return finish("read-only database inspection requires production configuration")
	}
	key, err := readSecret("SESSION_SECRET")
	if err != nil || !validSessionSecret(key) {
		return finish("invalid SESSION_SECRET configuration")
	}
	if !validProductionOrigin(os.Getenv("APP_ORIGIN")) {
		return finish("invalid APP_ORIGIN configuration")
	}
	listen := os.Getenv("LISTEN_ADDR")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if _, err = loadLocalConfiguration(listen, false); err != nil {
		return finish(err.Error())
	}
	result.ConfigurationChecked = true
	result.Checked = []string{"production_origin_session_secret", "listen_address_syntax_and_loopback", "trusted_proxy_cidr_syntax", "disabled_integration_selection_and_local_config", "optional_smtp_key_and_release_credential_files"}
	dsn, err := readSecret("MYSQL_DSN")
	if err != nil || dsn == "" {
		return finish("invalid MYSQL_DSN configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := billing.OpenMySQL(ctx, dsn, os.Getenv("MYSQL_ALLOW_INSECURE") == "true")
	if err != nil {
		return finish("database connection failed; check target, transport, credentials and connectivity")
	}
	defer db.DB.Close()
	status, err := db.SchemaStatus(ctx)
	result.Database = &status
	if err != nil {
		return finish(status.Error)
	}
	result.Checked = append(result.Checked, "selected_database_vendor_version_transport", "direct_database_select_and_table_inventory_visibility", "applied_migration_prefix_checksums_and_required_schema_shape", "administrator_count_if_schema_initialized")
	return finish("")
}
