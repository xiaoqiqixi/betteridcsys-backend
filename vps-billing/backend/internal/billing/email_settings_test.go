package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/plugins/smtp"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func emailTestCipher(t *testing.T) *SMTPCipher {
	t.Helper()
	key, err := newSMTPCipher(bytes.Repeat([]byte{0x37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func emailTestMemory() *Memory {
	return &Memory{emailSettings: DefaultEmailSettings(), audits: []Audit{}}
}
func emailTestUpdate() EmailSettingsUpdate {
	return EmailSettingsUpdate{Revision: 1, Enabled: true, Host: "localhost", Port: 465, TLSMode: smtp.ImplicitTLS, FromAddress: "sender@example.invalid", FromName: "SMTP Test", Username: "synthetic-account", Password: "synthetic-password"}
}

var emailTestActor = User{ID: "smtp-admin", Email: "admin@example.invalid", Role: "admin"}

func TestSMTPCredentialCipherAndKeyFile(t *testing.T) {
	c := emailTestCipher(t)
	creds := emailCredentials{Username: strings.Repeat("<", 254), Password: strings.Repeat("&", 1024)}
	a, err := c.encrypt(creds)
	if err != nil || !validSMTPEnvelope(a) {
		t.Fatal("envelope", err)
	}
	b, _ := c.encrypt(creds)
	if a == b {
		t.Fatal("nonce reused")
	}
	got, err := c.decrypt(a)
	if err != nil || got != creds {
		t.Fatal("roundtrip", err)
	}
	other, _ := newSMTPCipher(bytes.Repeat([]byte{0x38}, 32))
	if _, err = other.decrypt(a); err != ErrSMTPKey {
		t.Fatal("wrong key accepted")
	}
	for _, tamper := range []string{"", a[:len(a)-1], "v2:" + a[3:], a[:20] + "!" + a[21:]} {
		if _, err = c.decrypt(tamper); err != ErrSMTPKey {
			t.Fatal("tamper accepted")
		}
	}
	if value, e := LoadSMTPKey(""); value != nil || e != nil {
		t.Fatal("unset key")
	}
	// The managed cloud /tmp may have a foreign owner; never weaken production
	// ancestor checks to accommodate it. Use a private owned checkout directory.
	dir, err := os.MkdirTemp(".", ".smtp-key-test-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "smtp.key")
	if err = os.WriteFile(path, bytes.Repeat([]byte{0x39}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(path); err != nil {
		t.Fatal("private key", err)
	}
	if err = os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(path); err != ErrSMTPKey {
		t.Fatal("group-readable key accepted")
	}
	os.Chmod(path, 0600)
	if err = os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(path); err != ErrSMTPKey {
		t.Fatal("executable key accepted")
	}
	os.Chmod(path, 0600)
	hardlink := filepath.Join(dir, "hardlink")
	if err = os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(path); err != ErrSMTPKey {
		t.Fatal("multiply-linked key accepted")
	}
	os.Remove(hardlink)
	link := filepath.Join(dir, "link")
	if err = os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(link); err != ErrSMTPKey {
		t.Fatal("symlink key accepted")
	}
	parentLink := filepath.Join(dir, "parent-link")
	if err = os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSMTPKey(filepath.Join(parentLink, "smtp.key")); err != ErrSMTPKey {
		t.Fatal("symlink ancestor accepted")
	}
	os.WriteFile(path, []byte("short"), 0600)
	if _, err = LoadSMTPKey(path); err != ErrSMTPKey {
		t.Fatal("short key accepted")
	}
	os.WriteFile(path, bytes.Repeat([]byte{0x39}, 32), 0600)
	os.Chmod(dir, 0777)
	if _, err = LoadSMTPKey(path); err != ErrSMTPKey {
		t.Fatal("writable parent accepted")
	}
	os.Chmod(dir, 0700)
}

func TestSMTPSettingsEncryptionRevisionAndRecovery(t *testing.T) {
	ctx := context.Background()
	m := emailTestMemory()
	service := newSMTPService(m, emailTestCipher(t))
	input := emailTestUpdate()
	view, err := service.Update(ctx, emailTestActor, input)
	if err != nil || view.Revision != 2 || !view.UsernameConfigured || !view.PasswordConfigured || !view.KeyConfigured {
		t.Fatal(view, err)
	}
	raw, _ := json.Marshal(m.emailSettings)
	for _, secret := range []string{input.Username, input.Password} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext persisted")
		}
	}
	api, _ := json.Marshal(view)
	if bytes.Contains(api, []byte("ciphertext")) || bytes.Contains(api, []byte(input.Username)) || bytes.Contains(api, []byte(input.Password)) {
		t.Fatal("credentials in view")
	}
	if _, err = service.Update(ctx, emailTestActor, input); err != ErrSettingsConflict {
		t.Fatal("stale update", err)
	}
	input.Revision = 2
	input.Username = ""
	input.Password = ""
	input.FromName = "Changed"
	before := m.emailSettings.CredentialsCiphertext
	if _, err = service.Update(ctx, emailTestActor, input); err != nil || m.emailSettings.CredentialsCiphertext != before {
		t.Fatal("blank altered stored credential", err)
	}
	input.Revision = 3
	input.Host = "new.example.invalid"
	if _, err = service.Update(ctx, emailTestActor, input); err != ErrEmailSettings {
		t.Fatal("retained credentials disclosed to new host", err)
	}
	input.Enabled = false
	input.ClearUsername = true
	if _, err = service.Update(ctx, emailTestActor, input); err != ErrEmailSettings {
		t.Fatal("partial clear accepted", err)
	}
	input.ClearPassword = true
	if _, err = service.Update(ctx, emailTestActor, input); err != nil || m.emailSettings.CredentialsCiphertext != "" {
		t.Fatal("clear both", err)
	}
	input = emailTestUpdate()
	input.Revision = 4
	if _, err = service.Update(ctx, emailTestActor, input); err != nil {
		t.Fatal(err)
	}
	wrong, _ := newSMTPCipher(bytes.Repeat([]byte{0x38}, 32))
	service.cipher = wrong
	view, err = service.Settings(ctx)
	if err != nil || !view.KeyConfigured {
		t.Fatal("loaded key presence should permit explicit recovery", err)
	}
	summary, err := service.Summary(ctx)
	if err != nil || summary.Enabled || summary.Status != "unavailable" {
		t.Fatal(summary, err)
	}
	input.Revision = 5
	input.Username = ""
	input.Password = ""
	if _, err = service.Update(ctx, emailTestActor, input); err != ErrSMTPKey {
		t.Fatal("enabled with undecryptable creds", err)
	}
	input.Username = "replacement-account"
	input.Password = "replacement-password"
	if _, err = service.Update(ctx, emailTestActor, input); err != nil {
		t.Fatal("explicit key rotation recovery", err)
	}
	service.cipher = nil
	input.Revision = 6
	input.Username = ""
	input.Password = ""
	input.Enabled = false
	if _, err = service.Update(ctx, emailTestActor, input); err != nil {
		t.Fatal("disable without key", err)
	}
	for _, a := range m.audits {
		if strings.Contains(a.Detail, "password") || strings.Contains(a.Detail, "account") || strings.Contains(a.Detail, "localhost") {
			t.Fatal("secret audit", a)
		}
	}
}

type emailSenderFunc func(context.Context, smtp.Config, smtp.Message) error

func (f emailSenderFunc) Send(ctx context.Context, c smtp.Config, m smtp.Message) error {
	return f(ctx, c, m)
}

type emailAuditFailureStore struct {
	Store
	failStart bool
}

func (s emailAuditFailureStore) RecordEmailTest(ctx context.Context, u User, r int64, outcome string) error {
	if (outcome == "test_requested") == s.failStart {
		return errors.New("synthetic audit failure")
	}
	return s.Store.RecordEmailTest(ctx, u, r, outcome)
}

func TestSMTPTestLifecycleAndAcceptedAuditFailure(t *testing.T) {
	ctx := context.Background()
	m := emailTestMemory()
	service := newSMTPService(m, emailTestCipher(t))
	input := emailTestUpdate()
	if _, err := service.Update(ctx, emailTestActor, input); err != nil {
		t.Fatal(err)
	}
	registry := integrations.Default().WithManagedEmail(service)
	calls := 0
	service.sender = emailSenderFunc(func(ctx context.Context, c smtp.Config, msg smtp.Message) error {
		calls++
		if c.Username != input.Username || c.Password != input.Password || msg.To != "recipient@example.invalid" {
			t.Fatal("incorrect saved snapshot")
		}
		return nil
	})
	if err := service.Test(ctx, registry, emailTestActor, 1, "recipient@example.invalid"); err != ErrSettingsConflict {
		t.Fatal(err)
	}
	if err := service.Test(ctx, registry, emailTestActor, 2, "a@example.invalid,b@example.invalid"); err != ErrEmailSettings {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("invalid test sent")
	}
	service.store = emailAuditFailureStore{Store: m, failStart: true}
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err == nil || calls != 0 {
		t.Fatal("sent without start audit")
	}
	service.lastTest = time.Time{}
	service.store = emailAuditFailureStore{Store: m}
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != nil || calls != 1 {
		t.Fatal("acceptance downgraded by final audit", err)
	}
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != ErrSMTPRate || calls != 1 {
		t.Fatal("rate limit", err)
	}
	service.lastTest = time.Time{}
	service.store = m
	service.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error {
		return &smtp.Error{Code: smtp.CodeDeliveryUncertain, DeliveryUncertain: true}
	})
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != ErrSMTPUncertain {
		t.Fatal("uncertain mapped unsafely", err)
	}
	if m.audits[len(m.audits)-1].Action != "email.test_uncertain" {
		t.Fatal("uncertain audit missing")
	}
	service.lastTest = time.Time{}
	cancelCtx, cancel := context.WithCancel(ctx)
	service.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error { cancel(); return nil })
	if err := service.Test(cancelCtx, registry, emailTestActor, 2, "recipient@example.invalid"); err != nil {
		t.Fatal("acceptance downgraded by deadline race", err)
	}
}

func TestSMTPAdminHTTPGuardsAndRedaction(t *testing.T) {
	m := NewMemory()
	app := NewApp(m, Config{Demo: true, Origin: "http://localhost", Secret: ID() + ID(), SMTPKey: emailTestCipher(t)})
	srv := httptest.NewServer(app.Handler())
	defer srv.Close()
	anonymous := client(t, srv.URL, "", "")
	customer := client(t, srv.URL, "demo@aster.local", "DemoCustomer!2026")
	admin := client(t, srv.URL, "admin@aster.local", "DemoAdmin!2026")
	for _, test := range []struct {
		c    *testClient
		want int
	}{{anonymous, 401}, {customer, 403}} {
		for _, method := range []string{"GET", "POST"} {
			if status, _ := test.c.request(method, "/api/v1/admin/email-settings", emailTestUpdate(), ""); status != test.want {
				t.Fatal("permission", method, status)
			}
		}
	}
	old := admin.csrf
	admin.csrf = "invalid"
	if status, _ := admin.request("POST", "/api/v1/admin/email-settings", emailTestUpdate(), ""); status != 403 {
		t.Fatal("CSRF", status)
	}
	admin.csrf = old
	status, data := admin.request("POST", "/api/v1/admin/email-settings", emailTestUpdate(), "")
	if status != 200 {
		t.Fatal(status, data)
	}
	serialized, _ := json.Marshal(data)
	if bytes.Contains(serialized, []byte("synthetic-")) || bytes.Contains(serialized, []byte("ciphertext")) {
		t.Fatal("credential leak")
	}
	for _, raw := range []string{`{"revision":2,"to":"a@example.invalid","to":"b@example.invalid"}`, `{"revision":2,"Revision":2,"to":"a@example.invalid"}`, `{"revision":2,"reviſion":2,"to":"a@example.invalid"}`, `{"revision":2,"to":null}`, `{"revision":2,"to":"a@example.invalid","host":"evil.invalid"}`} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/admin/email-settings/test", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", admin.csrf)
		res, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Fatal("ambiguous JSON accepted", res.StatusCode)
		}
	}
	called := false
	app.SMTP.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error {
		called = true
		return &smtp.Error{Code: smtp.CodeDeliveryUncertain, DeliveryUncertain: true}
	})
	status, data = admin.request("POST", "/api/v1/admin/email-settings/test", map[string]any{"revision": 2, "to": "recipient@example.invalid"}, "")
	if status != 502 || data["error"].(map[string]any)["code"] != "SMTP_DELIVERY_UNCERTAIN" || !called {
		t.Fatal(status, data)
	}
	status, data = admin.request("GET", "/api/v1/admin/integrations", nil, "")
	if status != 200 {
		t.Fatal(status, data)
	}
	found := false
	for _, item := range data["data"].(map[string]any)["items"].([]any) {
		v := item.(map[string]any)
		if v["id"] == "email.smtp" {
			// Injected test transport is not evidence of an installed runtime plugin.
			found = v["enabled"] == false && v["configured"] == true && v["status"] == "unavailable" && v["configuration_mode"] == "admin_api"
		}
	}
	if !found {
		t.Fatal("managed SMTP registry absent")
	}
}

func TestSMTPConcurrentTestDoesNotDuplicateSend(t *testing.T) {
	ctx := context.Background()
	memory := emailTestMemory()
	service := newSMTPService(memory, emailTestCipher(t))
	if _, err := service.Update(ctx, emailTestActor, emailTestUpdate()); err != nil {
		t.Fatal(err)
	}
	registry := integrations.Default().WithManagedEmail(service)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	service.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error {
		calls.Add(1)
		close(started)
		<-release
		return &smtp.Error{Code: smtp.CodeConnection}
	})
	done := make(chan error, 1)
	go func() { done <- service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid") }()
	<-started
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != ErrSMTPRate {
		t.Fatal("concurrent send was not rate limited", err)
	}
	close(release)
	if err := <-done; err != ErrSMTPUnavailable {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate sender invocation")
	}
	service.mu.Lock()
	inFlight := service.inFlight
	service.lastTest = time.Time{}
	service.mu.Unlock()
	if inFlight {
		t.Fatal("failed send left busy flag")
	}
	service.sender = emailSenderFunc(func(context.Context, smtp.Config, smtp.Message) error { calls.Add(1); return nil })
	if err := service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != nil || calls.Load() != 2 {
		t.Fatal("later explicit retry blocked", err)
	}
}

func TestSMTPMissingKeyAndDisabledDefaults(t *testing.T) {
	ctx := context.Background()
	memory := emailTestMemory()
	service := newSMTPService(memory, nil)
	view, err := service.Settings(ctx)
	if err != nil || view.Enabled || view.KeyConfigured || view.UsernameConfigured || view.PasswordConfigured || view.Revision != 1 {
		t.Fatal(view, err)
	}
	input := emailTestUpdate()
	if _, err = service.Update(ctx, emailTestActor, input); err != ErrSMTPKey {
		t.Fatal("enable without key", err)
	}
	input.Enabled = false
	input.Username = ""
	input.Password = ""
	if _, err = service.Update(ctx, emailTestActor, input); err != nil {
		t.Fatal("disabled metadata draft", err)
	}
	registry := integrations.Default().WithManagedEmail(service)
	if err = service.Test(ctx, registry, emailTestActor, 2, "recipient@example.invalid"); err != ErrSMTPDisabled {
		t.Fatal("disabled test", err)
	}
}
