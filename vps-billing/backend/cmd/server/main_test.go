package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRejectDefaultSessionSecrets(t *testing.T) {
	for _, s := range []string{"", "short", "00000000000000000000000000000000", "change-this-secret-before-production", "replace-this-with-a-strong-random-secret", "demo-session-secret-do-not-use-live"} {
		if validSessionSecret(s) {
			t.Fatalf("accepted weak/example secret %q", s)
		}
	}
	if !validSessionSecret("a928e01f683bc470d2f50911a6e0938745dcefca89b1043452ead5fac7cd3251") {
		t.Fatal("random-format secret rejected")
	}
}

func preflightTestEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"APP_MODE", "APP_ORIGIN", "SESSION_SECRET", "SESSION_SECRET_FILE", "LISTEN_ADDR", "TRUSTED_PROXY_CIDRS", "INTEGRATIONS_CONFIG_FILE", "PAYMENT_PROVIDER", "EMAIL_PROVIDER", "SMTP_ENCRYPTION_KEY_FILE", "UPDATE_RELEASE_TOKEN_FILE", "HOST_OPERATION_STATUS_FILE", "MYSQL_DSN", "MYSQL_DSN_FILE", "MYSQL_ALLOW_INSECURE"} {
		t.Setenv(name, "")
	}
	t.Setenv("APP_MODE", "production")
	t.Setenv("APP_ORIGIN", "https://billing.example")
	t.Setenv("SESSION_SECRET", "a928e01f683bc470d2f50911a6e0938745dcefca89b1043452ead5fac7cd3251")
}

func TestHostOperationStatusConfigurationIsFixedAndOptional(t *testing.T) {
	preflightTestEnv(t)
	for _, path := range []string{"", "/var/lib/aster/operations/STATUS.json"} {
		t.Setenv("HOST_OPERATION_STATUS_FILE", path)
		cfg, err := loadLocalConfiguration("127.0.0.1:18080", false)
		if err != nil || cfg.hostOperations == nil {
			t.Fatal("optional or absent receipt prevented API startup", err)
		}
	}
	for _, path := range []string{"private-relative-secret", "/var/lib/../private-secret", "/var/lib/private-secret\n"} {
		t.Setenv("HOST_OPERATION_STATUS_FILE", path)
		_, err := loadLocalConfiguration("127.0.0.1:18080", false)
		if err == nil || strings.Contains(err.Error(), "private-secret") {
			t.Fatal("unsafe receipt configuration accepted or exposed")
		}
	}
}

func TestPreflightReturnsRedactedJSONForConfigurationErrors(t *testing.T) {
	for name, value := range map[string]string{"APP_MODE": "demo", "APP_ORIGIN": "https://user:highly-secret@billing.example", "SESSION_SECRET_FILE": "/not-present/highly-secret", "LISTEN_ADDR": "0.0.0.0:8080", "TRUSTED_PROXY_CIDRS": "highly-secret", "PAYMENT_PROVIDER": "payment.highly-secret", "EMAIL_PROVIDER": "email.highly-secret", "SMTP_ENCRYPTION_KEY_FILE": "/missing/highly-secret", "UPDATE_RELEASE_TOKEN_FILE": "/missing/highly-secret", "MYSQL_DSN": "db-user:highly-secret@tcp(db.invalid:3306)/aster"} {
		t.Run(name, func(t *testing.T) {
			preflightTestEnv(t)
			t.Setenv(name, value)
			var out bytes.Buffer
			if runPreflight(&out) != 1 {
				t.Fatal("invalid configuration passed")
			}
			if strings.Contains(out.String(), "highly-secret") || strings.Contains(out.String(), "a928e01") {
				t.Fatal("secret leaked in JSON")
			}
			var result preflightResult
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.ReadOnly || result.Error == "" {
				t.Fatal("invalid JSON result", err)
			}
		})
	}
}

func TestLocalConfigurationRunsBeforeDatabaseCommands(t *testing.T) {
	preflightTestEnv(t)
	path := filepath.Join(t.TempDir(), "integration.json")
	if err := os.WriteFile(path, []byte(`{"email.disabled":{"password":"highly-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTEGRATIONS_CONFIG_FILE", path)
	if _, err := loadLocalConfiguration("127.0.0.1:18080", false); err == nil {
		t.Fatal("disabled provider accepted configuration")
	}
	t.Setenv("INTEGRATIONS_CONFIG_FILE", "")
	t.Setenv("TRUSTED_PROXY_CIDRS", "127.0.0.1/32")
	if _, err := loadLocalConfiguration("127.0.0.1:18080", false); err != nil {
		t.Fatal(err)
	}
}

func TestSecretReadIsBounded(t *testing.T) {
	preflightTestEnv(t)
	path := filepath.Join(t.TempDir(), "oversized-secret")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 16385)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSION_SECRET_FILE", path)
	if _, err := readSecret("SESSION_SECRET"); err == nil {
		t.Fatal("oversized secret file accepted")
	}
	t.Setenv("SESSION_SECRET_FILE", os.DevNull)
	if _, err := readSecret("SESSION_SECRET"); err == nil {
		t.Fatal("device accepted as secret file")
	}
}

func TestProductionOriginValidation(t *testing.T) {
	for _, origin := range []string{"https://billing.example?", "https://:443", "https://billing.example#", "https://billing.example/", "https://user:pass@billing.example", "http://billing.example", "https:billing.example", "https://billing.example:", "https://billing.example:443", "https://billing.example:0443", "https://Billing.example", "https://billing.example:99999"} {
		if validProductionOrigin(origin) {
			t.Fatal("invalid origin accepted", origin)
		}
	}
	for _, origin := range []string{"https://billing.example", "https://billing.example:8443", "https://[2001:db8::1]"} {
		if !validProductionOrigin(origin) {
			t.Fatal("valid origin rejected", origin)
		}
	}
}
