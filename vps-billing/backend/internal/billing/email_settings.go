package billing

import (
	"aster/billing/internal/integrations"
	"aster/billing/internal/runtimeplugins"
	smtp "aster/billing/pkg/pluginapi/smtpv1"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
)

var (
	ErrEmailSettings   = errors.New("invalid email settings")
	ErrSMTPKey         = errors.New("SMTP encryption key unavailable")
	ErrSMTPDisabled    = errors.New("SMTP disabled")
	ErrSMTPRate        = errors.New("SMTP test rate limited")
	ErrSMTPUnavailable = errors.New("SMTP unavailable")
	ErrSMTPUncertain   = errors.New("SMTP delivery uncertain")
)

// EmailSettingsRecord is internal persistence data. Never serialize it to an API.
// Both authentication fields live exclusively inside CredentialsCiphertext.
type EmailSettingsRecord struct {
	Revision              int64     `json:"revision"`
	Enabled               bool      `json:"enabled"`
	Host                  string    `json:"host"`
	Port                  int       `json:"port"`
	TLSMode               string    `json:"tls_mode"`
	FromAddress           string    `json:"from_address"`
	FromName              string    `json:"from_name"`
	CredentialsCiphertext string    `json:"credentials_ciphertext"`
	UpdatedAt             time.Time `json:"updated_at"`
}

type EmailSettingsView struct {
	PluginAvailable    bool      `json:"plugin_available"`
	PluginStatus       string    `json:"plugin_status"`
	PluginVersion      string    `json:"plugin_version"`
	DestinationAllowed bool      `json:"destination_allowed"`
	Revision           int64     `json:"revision"`
	Enabled            bool      `json:"enabled"`
	Host               string    `json:"host"`
	Port               int       `json:"port"`
	TLSMode            string    `json:"tls_mode"`
	FromAddress        string    `json:"from_address"`
	FromName           string    `json:"from_name"`
	UsernameConfigured bool      `json:"username_configured"`
	PasswordConfigured bool      `json:"password_configured"`
	KeyConfigured      bool      `json:"key_configured"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type EmailSettingsUpdate struct {
	Revision      int64  `json:"revision"`
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	Port          int    `json:"port"`
	TLSMode       string `json:"tls_mode"`
	FromAddress   string `json:"from_address"`
	FromName      string `json:"from_name"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	ClearUsername bool   `json:"clear_username,omitempty"`
	ClearPassword bool   `json:"clear_password,omitempty"`
}

type emailCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func DefaultEmailSettings() EmailSettingsRecord {
	return EmailSettingsRecord{Revision: 1, Port: 587, TLSMode: smtp.STARTTLS}
}

func (s EmailSettingsRecord) config(c emailCredentials) smtp.Config {
	return smtp.Config{Host: s.Host, Port: s.Port, TLSMode: s.TLSMode, FromAddress: s.FromAddress, FromName: s.FromName, Username: c.Username, Password: c.Password}
}

func validEmailRecord(s EmailSettingsRecord) bool {
	if s.Revision < 1 || s.Revision > MaxMinor || s.Port < 1 || s.Port > 65535 || (s.TLSMode != smtp.STARTTLS && s.TLSMode != smtp.ImplicitTLS) || len(s.Host) > 253 || len(s.FromAddress) > 254 || !validText(s.FromName, 120, false) || len(s.CredentialsCiphertext) > 4096 {
		return false
	}
	// Validate optional draft fields with harmless placeholders, using the same
	// grammar as the transport. Never normalize authentication bytes.
	c := s.config(emailCredentials{Username: "validation", Password: "validation"})
	if c.Host == "" {
		c.Host = "localhost"
	}
	if c.FromAddress == "" {
		c.FromAddress = "validation@example.invalid"
	}
	if smtp.ValidateConfig(c) != nil {
		return false
	}
	if s.Enabled && (s.Host == "" || s.FromAddress == "" || s.CredentialsCiphertext == "") {
		return false
	}
	return s.CredentialsCiphertext == "" || validSMTPEnvelope(s.CredentialsCiphertext)
}

func decodeEmailRecord(raw []byte) (EmailSettingsRecord, error) {
	var s EmailSettingsRecord
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 8192 || d.Decode(&s) != nil || !validEmailRecord(s) {
		return s, ErrEmailSettings
	}
	return s, nil
}

func emailAudit(actor User, revision int64, outcome string) Audit {
	return Audit{ID: ID(), UserID: actor.ID, ActorEmail: actor.Email, Action: "email." + outcome, Target: "email.smtp", CreatedAt: time.Now().UTC(), Detail: fmt.Sprintf("SMTP settings revision %d; %s", revision, outcome)}
}

func validEmailOutcome(outcome string) bool {
	return outcome == "test_requested" || outcome == "test_accepted" || outcome == "test_failed" || outcome == "test_uncertain"
}

type smtpSender interface {
	Send(context.Context, smtp.Config, smtp.Message) error
}
type emailTestAccess struct {
	Actor    User
	Revision int64
}
type emailTestContextKey struct{}

// SMTPService owns authorization, persistence and audit. The official transport
// receives only the validated configuration/message and never receives a Store.
type SMTPService struct {
	runtime  *runtimeplugins.Manager
	store    Store
	cipher   *SMTPCipher
	sender   smtpSender
	mu       sync.Mutex
	lastTest time.Time
	inFlight bool
}

func newSMTPService(store Store, cipher *SMTPCipher) *SMTPService {
	return &SMTPService{store: store, cipher: cipher, sender: unavailableSMTPSender{}}
}

func (s *SMTPService) credentials(record EmailSettingsRecord) (emailCredentials, error) {
	if record.CredentialsCiphertext == "" {
		return emailCredentials{}, nil
	}
	if s.cipher == nil {
		return emailCredentials{}, ErrSMTPKey
	}
	return s.cipher.decrypt(record.CredentialsCiphertext)
}

func (s *SMTPService) view(record EmailSettingsRecord) EmailSettingsView {
	c, err := s.credentials(record)
	plugin := s.runtime.State()
	// Flags describe presence without disclosing credential lengths or values.
	return EmailSettingsView{PluginAvailable: plugin.Status == "approved", PluginStatus: plugin.Status, PluginVersion: plugin.Version, DestinationAllowed: s.runtime.Allows(record.config(c)), Revision: record.Revision, Enabled: record.Enabled, Host: record.Host, Port: record.Port, TLSMode: record.TLSMode, FromAddress: record.FromAddress, FromName: record.FromName, UpdatedAt: record.UpdatedAt, UsernameConfigured: record.CredentialsCiphertext != "" && (err != nil || c.Username != ""), PasswordConfigured: record.CredentialsCiphertext != "" && (err != nil || c.Password != ""), KeyConfigured: s.cipher != nil}
}

func (s *SMTPService) Settings(ctx context.Context) (EmailSettingsView, error) {
	r, e := s.store.EmailSettings(ctx)
	if e != nil {
		return EmailSettingsView{}, e
	}
	return s.view(r), nil
}

func (s *SMTPService) Update(ctx context.Context, actor User, input EmailSettingsUpdate) (EmailSettingsView, error) {
	if actor.Role != "admin" {
		return EmailSettingsView{}, ErrEmailSettings
	}
	old, err := s.store.EmailSettings(ctx)
	if err != nil {
		return EmailSettingsView{}, err
	}
	if old.Revision != input.Revision {
		return EmailSettingsView{}, ErrSettingsConflict
	}
	if (input.ClearUsername && input.Username != "") || (input.ClearPassword && input.Password != "") {
		return EmailSettingsView{}, ErrEmailSettings
	}
	next := EmailSettingsRecord{Revision: input.Revision, Enabled: input.Enabled, Host: input.Host, Port: input.Port, TLSMode: input.TLSMode, FromAddress: input.FromAddress, FromName: input.FromName, CredentialsCiphertext: old.CredentialsCiphertext, UpdatedAt: old.UpdatedAt}
	changedDestination := old.Host != next.Host || old.Port != next.Port || old.TLSMode != next.TLSMode
	clearBoth := input.ClearUsername && input.ClearPassword && !input.Enabled
	replaceBoth := input.Username != "" && input.Password != ""
	if changedDestination && old.CredentialsCiphertext != "" && !clearBoth && !replaceBoth {
		return EmailSettingsView{}, ErrEmailSettings
	}
	secretEdit := input.Username != "" || input.Password != "" || input.ClearUsername || input.ClearPassword
	if secretEdit || input.Enabled {
		if s.cipher == nil {
			return EmailSettingsView{}, ErrSMTPKey
		}
		c, e := s.credentials(old)
		// Full replacement/clear permits recovery after an operator rotates a lost
		// key; partial replacement never discards an undecryptable credential.
		if e != nil && !replaceBoth && !clearBoth {
			return EmailSettingsView{}, ErrSMTPKey
		}
		if input.Username != "" {
			c.Username = input.Username
		}
		if input.Password != "" {
			c.Password = input.Password
		}
		if input.ClearUsername {
			c.Username = ""
		}
		if input.ClearPassword {
			c.Password = ""
		}
		// Authentication is a pair: clearing either requires clearing both while disabled.
		if (c.Username == "") != (c.Password == "") {
			return EmailSettingsView{}, ErrEmailSettings
		}
		if c.Username == "" {
			if input.Enabled {
				return EmailSettingsView{}, ErrEmailSettings
			}
			next.CredentialsCiphertext = ""
		} else {
			cfg := next.config(c)
			if cfg.Host == "" {
				cfg.Host = "localhost"
			}
			if cfg.FromAddress == "" {
				cfg.FromAddress = "validation@example.invalid"
			}
			if smtp.ValidateConfig(cfg) != nil || len(c.Username) > 254 || len(c.Password) > 1024 {
				return EmailSettingsView{}, ErrEmailSettings
			}
			if secretEdit {
				next.CredentialsCiphertext, err = s.cipher.encrypt(c)
				if err != nil {
					return EmailSettingsView{}, err
				}
			}
		}
	}
	if !validEmailRecord(next) {
		return EmailSettingsView{}, ErrEmailSettings
	}
	updated, err := s.store.UpdateEmailSettings(ctx, actor, next)
	if err != nil {
		return EmailSettingsView{}, err
	}
	return s.view(updated), nil
}

func (s *SMTPService) Summary(ctx context.Context) (integrations.Summary, error) {
	r, err := s.store.EmailSettings(ctx)
	if err != nil {
		return integrations.Summary{}, err
	}
	c, e := s.credentials(r)
	configured := s.cipher != nil && e == nil && smtp.ValidateConfig(r.config(c)) == nil
	pluginReady := s.runtime.State().Status == "approved" && s.runtime.Allows(r.config(c))
	status := "disabled"
	if r.Enabled {
		status = "unavailable"
		if configured && pluginReady {
			status = "ready"
		}
	}
	return integrations.Summary{Manifest: integrations.Manifest{ID: "email.smtp", Kind: integrations.Email, Name: "SMTP 邮件插件", Description: "通过独立运行时插件发送管理员测试邮件", Capabilities: []string{"admin_test_email"}, ConfigurationFields: []integrations.ConfigurationField{}, SupportedCurrencies: []string{}, Limitations: []string{"需要单独安装、批准并运行兼容插件；配置保存不会执行未知文件", "仅提供管理员测试发送；未接入通知、邮箱验证或密码找回", "接受表示 SMTP 服务器接受，不保证收件箱投递"}}, ConfigurationMode: "admin_api", Enabled: r.Enabled && configured && pluginReady, Configured: configured, Status: status}, nil
}

func (s *SMTPService) Test(ctx context.Context, registry *integrations.Registry, actor User, revision int64, to string) error {
	if actor.Role != "admin" || revision < 1 || revision > MaxMinor || smtp.ValidateRecipient(to) != nil {
		return ErrEmailSettings
	}
	ctx = context.WithValue(ctx, emailTestContextKey{}, emailTestAccess{actor, revision})
	_, err := registry.SendEmail(ctx, integrations.EmailMessage{IdempotencyKey: ID(), To: to, Template: "smtp.test"})
	return err
}

func (s *SMTPService) Send(ctx context.Context, message integrations.EmailMessage) (integrations.EmailReceipt, error) {
	access, ok := ctx.Value(emailTestContextKey{}).(emailTestAccess)
	if !ok || access.Actor.Role != "admin" || message.Template != "smtp.test" || smtp.ValidateRecipient(message.To) != nil {
		return integrations.EmailReceipt{}, ErrSMTPDisabled
	}
	r, err := s.store.EmailSettings(ctx)
	if err != nil {
		return integrations.EmailReceipt{}, err
	}
	if r.Revision != access.Revision {
		return integrations.EmailReceipt{}, ErrSettingsConflict
	}
	if !r.Enabled {
		return integrations.EmailReceipt{}, ErrSMTPDisabled
	}
	c, err := s.credentials(r)
	if err != nil || s.cipher == nil {
		return integrations.EmailReceipt{}, ErrSMTPKey
	}
	if smtp.ValidateConfig(r.config(c)) != nil {
		return integrations.EmailReceipt{}, ErrEmailSettings
	}
	s.mu.Lock()
	if s.inFlight || time.Since(s.lastTest) < 30*time.Second {
		s.mu.Unlock()
		return integrations.EmailReceipt{}, ErrSMTPRate
	}
	s.lastTest = time.Now()
	s.inFlight = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inFlight = false; s.mu.Unlock() }()
	if err = s.store.RecordEmailTest(ctx, access.Actor, r.Revision, "test_requested"); err != nil {
		return integrations.EmailReceipt{}, err
	}
	err = s.sender.Send(ctx, r.config(c), smtp.Message{To: message.To, Subject: "Aster SMTP configuration test", Text: "This is an administrator-requested SMTP configuration test. SMTP server acceptance does not guarantee inbox delivery."})
	outcome := "test_accepted"
	result := error(nil)
	if err != nil {
		outcome = "test_failed"
		result = ErrSMTPUnavailable
		var transportError *smtp.Error
		if errors.As(err, &transportError) && transportError.DeliveryUncertain {
			outcome = "test_uncertain"
			result = ErrSMTPUncertain
		}
	}
	// Acceptance cannot be rolled back. Best-effort final audit has a fresh,
	// bounded context; an audit failure never converts acceptance into a resend.
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if auditErr := s.store.RecordEmailTest(auditCtx, access.Actor, r.Revision, outcome); auditErr != nil {
		recordEmailAuditFailure(auditCtx, access.Actor, r.Revision, outcome, auditErr)
	}
	if result != nil {
		return integrations.EmailReceipt{}, result
	}
	return integrations.EmailReceipt{ProviderMessageID: "smtp-accepted-" + strconv.FormatInt(r.Revision, 10) + "-" + message.IdempotencyKey}, nil
}

// No built-in SMTP fallback: an absent runtime plugin stays unavailable.
type unavailableSMTPSender struct{}

func (unavailableSMTPSender) Send(context.Context, smtp.Config, smtp.Message) error {
	return &smtp.Error{Code: smtp.CodeConnection}
}
