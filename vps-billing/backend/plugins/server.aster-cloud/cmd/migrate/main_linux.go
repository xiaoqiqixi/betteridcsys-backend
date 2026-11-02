// Explicit host migration tool; it is never linked into the plugin worker or Core.
package main

import (
	"aster/billing/plugins/server.aster-cloud/internal/controllers"
	"aster/billing/plugins/server.aster-cloud/migration"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/go-sql-driver/mysql"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "Migration stopped; no controller operation was sent.")
		os.Exit(1)
	}
}
func run() error {
	policy := flag.String("controller-policy", "", "Existing root-owned controller policy")
	controller := flag.String("controller-id", "", "Exactly one controller to migrate")
	connection := flag.String("connection-id", "", "New direct connection id")
	dsnFile := flag.String("mysql-dsn-file", "", "Root-only database credential file")
	apply := flag.Bool("apply", false, "Apply after stopping Core and completing legacy jobs")
	flag.Parse()
	if os.Geteuid() != 0 || flag.NArg() != 0 {
		return fmt.Errorf("root required")
	}
	raw, err := controllers.ReadHostFile(*policy, false, 128<<10)
	if err != nil {
		return err
	}
	plan, profile, err := migration.PlanControllerMigration(raw, *controller, *connection)
	if err != nil {
		return err
	}
	if !*apply {
		return json.NewEncoder(os.Stdout).Encode(plan)
	}
	dsn, err := controllers.ReadHostFile(*dsnFile, true, 64<<10)
	if err != nil {
		return err
	}
	defer clear(dsn)
	token, err := controllers.ReadHostFile(profile.TokenFile, true, 4096)
	if err != nil {
		return err
	}
	defer clear(token)
	var ca []byte
	if profile.CAFile != "" {
		ca, err = controllers.ReadHostFile(profile.CAFile, false, 64<<10)
		if err != nil {
			return err
		}
	}
	base := "/etc/aster/server"
	for _, path := range []string{"/etc/aster", base} {
		if err = os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, e := os.Lstat(path)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return fmt.Errorf("unsafe credential parent")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return fmt.Errorf("credential parent must belong to root")
		}
	}
	dir := filepath.Join(base, plan.ConnectionID)
	if _, err = os.Lstat(dir); !os.IsNotExist(err) {
		return fmt.Errorf("credential directory exists")
	}
	tmp, err := os.MkdirTemp(base, ".migration-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // Only the private directory created by this invocation.
	config, _ := json.Marshal(plan.Config)
	for name, value := range map[string][]byte{"config.json": config, "token": token} {
		if err = os.WriteFile(filepath.Join(tmp, name), value, 0600); err != nil {
			return err
		}
	}
	if len(ca) > 0 {
		if err = os.WriteFile(filepath.Join(tmp, "ca.pem"), ca, 0600); err != nil {
			return err
		}
	}
	cfg, err := mysql.ParseDSN(strings.TrimSpace(string(dsn)))
	if err != nil || cfg.DBName == "" || cfg.MultiStatements || cfg.AllowAllFiles || cfg.AllowCleartextPasswords || cfg.AllowOldPasswords || (cfg.Net != "unix" && cfg.TLSConfig != "true") {
		return fmt.Errorf("protected database transport required")
	}
	cfg.ParseTime, cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = true, 5*time.Second, 30*time.Second, 30*time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Publish credentials before the transaction. A failed transaction leaves a
	// prepared directory for inspection, never an enabled runtime slot.
	if err = os.Rename(tmp, dir); err != nil {
		return err
	}
	if err = migration.MigrateControllerDatabase(ctx, db, plan); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		ConnectionID string `json:"connection_id"`
		Status       string `json:"status"`
	}{plan.ConnectionID, "migrated; approve the server slot before starting Core"})
}
