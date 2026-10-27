package runtimeplugins

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/smtpv1"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type Manager struct {
	root, policy        string
	rules               trust
	mu                  sync.Mutex
	failures            int
	blockedUntil        time.Time
	approvalKey         string
	paymentMu           sync.Mutex
	paymentFailures     int
	paymentBlockedUntil time.Time
	paymentApprovalKey  string
	paymentSlots        chan struct{}
}

// New never executes a file or makes a network connection. A broken plugin
// installation must not prevent the core API from starting.
func New(root, policy string) *Manager { return &Manager{root: root, policy: policy} }

type State struct {
	Status               string `json:"status"`
	ID                   string `json:"id"`
	Version              string `json:"version"`
	Protocol             string `json:"protocol"`
	PolicyRevision       uint64 `json:"policy_revision"`
	SMTPProtocol         string `json:"smtp_protocol"`
	SMTPCoreMin          string `json:"smtp_core_min"`
	SMTPCoreMaxExclusive string `json:"smtp_core_max_exclusive"`
}

func (m *Manager) State() State {
	state := State{Status: "unavailable", Protocol: pluginapi.SMTPProtocol}
	if m == nil || m.root == "" && m.policy == "" {
		state.Status = "not_installed"
		return state
	}
	a, err := loadApproval(m.root, m.policy, m.rules)
	if errors.Is(err, ErrDisabled) {
		state.Status = "disabled"
		if a != nil {
			state.ID, state.Version, state.PolicyRevision = a.Selection.ID, a.Selection.Version, a.PolicyRevision
		}
		return state
	}
	if err != nil {
		return state
	}
	defer a.Close()
	state.Status, state.ID, state.Version, state.PolicyRevision = "approved", a.Manifest.ID, a.Manifest.Version, a.PolicyRevision
	// Compatibility comes only from the pinned manifest and verified binary.
	// Disabled or unavailable selections deliberately leave these fields empty.
	state.SMTPProtocol, state.SMTPCoreMin, state.SMTPCoreMaxExclusive = a.Manifest.Protocol, a.Manifest.CoreMin, a.Manifest.CoreMaxExclusive
	return state
}

func dialApproved(ctx context.Context, selection pluginapi.Selection, rules trust) (*net.UnixConn, error) {
	if uint32(os.Geteuid()) != selection.ClientUID {
		return nil, ErrUnavailable
	}
	parent, err := protectedOpen(filepath.Dir(selection.Socket), true, trust{owner: selection.WorkerUID, systemRoot: rules.systemRoot})
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var info syscall.Stat_t
	if syscall.Lstat(selection.Socket, &info) != nil || info.Mode&syscall.S_IFMT != syscall.S_IFSOCK || info.Uid != selection.WorkerUID || info.Mode&0007 != 0 || info.Nlink != 1 {
		return nil, ErrUnavailable
	}
	dialer := net.Dialer{Timeout: time.Second}
	connection, err := dialer.DialContext(ctx, "unix", selection.Socket)
	if err != nil {
		return nil, ErrUnavailable
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		connection.Close()
		return nil, ErrUnavailable
	}
	uid, err := pluginapi.PeerUID(conn)
	if err != nil || uid != selection.WorkerUID {
		conn.Close()
		return nil, ErrUnavailable
	}
	return conn, nil
}

func exchange(ctx context.Context, client *http.Client, path string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > pluginapi.MaxRequestBytes {
		return ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, "POST", "http://plugin.local"+path, io.NopCloser(bytes.NewReader(raw)))
	if err != nil {
		return ErrUnavailable
	}
	request.ContentLength = int64(len(raw))
	request.Header.Set("Content-Type", "application/json")
	// No GetBody, idempotency header or automatic application-level retries.
	response, err := client.Do(request)
	if err != nil {
		return ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "application/json" {
		return ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, pluginapi.MaxResponseBytes+1))
	if err != nil || pluginapi.DecodeStrict(body, pluginapi.MaxResponseBytes, output) != nil {
		return ErrUnavailable
	}
	return nil
}

type connection struct {
	conn      *net.UnixConn
	transport *http.Transport
	client    *http.Client
	nonce     string
}

func (c *connection) close() { c.conn.Close(); c.transport.CloseIdleConnections() }

func handshake(ctx context.Context, a *Approval, rules trust) (*connection, pluginapi.Identity, error) {
	var identity pluginapi.Identity
	conn, err := dialApproved(ctx, a.Selection, rules)
	if err != nil {
		return nil, identity, err
	}
	deadline := time.Now().Add(2 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	_ = conn.SetDeadline(deadline)
	var dialMu sync.Mutex
	used := false
	transport := &http.Transport{Proxy: nil, MaxConnsPerHost: 1, MaxIdleConns: 1, MaxIdleConnsPerHost: 1, MaxResponseHeaderBytes: 4096,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			dialMu.Lock()
			defer dialMu.Unlock()
			if used {
				return nil, ErrUnavailable
			}
			used = true
			return conn, nil
		}}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		conn.Close()
		return nil, identity, ErrUnavailable
	}
	c := &connection{conn: conn, transport: transport, client: client, nonce: hex.EncodeToString(nonce[:])}
	hello := pluginapi.Hello{Protocol: a.Manifest.Protocol, CoreVersion: pluginapi.CoreVersion, Nonce: c.nonce}
	if exchange(ctx, client, "/v1/handshake", hello, &identity) != nil || identity.Protocol != a.Manifest.Protocol || identity.ID != a.Manifest.ID || identity.Version != a.Manifest.Version || identity.SHA256 != a.Manifest.SHA256 || identity.Nonce != c.nonce || !pluginapi.EqualCapabilities(identity.Capabilities, a.Manifest.Capabilities) {
		c.close()
		return nil, pluginapi.Identity{}, ErrUnavailable
	}
	return c, identity, nil
}

// Check performs an explicitly requested compatibility check without credentials.
func (m *Manager) Check(ctx context.Context) (pluginapi.Identity, error) {
	if m == nil || ctx == nil {
		return pluginapi.Identity{}, ErrDisabled
	}
	a, err := loadApproval(m.root, m.policy, m.rules)
	if err != nil {
		return pluginapi.Identity{}, err
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, identity, err := handshake(ctx, a, m.rules)
	if err != nil {
		return pluginapi.Identity{}, err
	}
	c.close()
	return identity, nil
}

func (m *Manager) Send(ctx context.Context, cfg smtpv1.Config, message smtpv1.Message) error {
	failed := func(uncertain bool) error {
		return &smtpv1.Error{Code: smtpv1.CodeConnection, DeliveryUncertain: uncertain}
	}
	if m == nil || ctx == nil || smtpv1.ValidateConfig(cfg) != nil || smtpv1.ValidateMessage(message) != nil {
		return failed(false)
	}
	a, err := loadApproval(m.root, m.policy, m.rules)
	if err != nil {
		return failed(false)
	}
	defer a.Close()
	if !pluginapi.Allowed(a.Selection.AllowedTargets, cfg) {
		return failed(false)
	}
	m.mu.Lock()
	key := a.Selection.ManifestSHA256 + a.Selection.Socket
	if key != m.approvalKey {
		m.approvalKey = key
		m.failures = 0
		m.blockedUntil = time.Time{}
	}
	blocked := time.Now().Before(m.blockedUntil)
	m.mu.Unlock()
	if blocked {
		return failed(false)
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	c, _, err := handshake(ctx, a, m.rules)
	if err != nil {
		m.recordFailure()
		return failed(false)
	}
	defer c.close()
	end, _ := ctx.Deadline()
	_ = c.conn.SetDeadline(end)
	remaining := time.Until(end).Milliseconds()
	if remaining <= 0 {
		return failed(false)
	}
	request := pluginapi.SendRequest{Protocol: pluginapi.SMTPProtocol, Version: a.Manifest.Version, RequestID: c.nonce, TimeoutMS: remaining, Config: cfg, Message: message}
	var response pluginapi.SendResponse
	if exchange(ctx, c.client, "/v1/smtp/send", request, &response) != nil || response.Protocol != pluginapi.SMTPProtocol || response.RequestID != c.nonce {
		m.recordFailure()
		return failed(true)
	}
	if response.Status == "accepted" && response.Code == "" && !response.DeliveryUncertain {
		m.mu.Lock()
		m.failures = 0
		m.blockedUntil = time.Time{}
		m.mu.Unlock()
		return nil
	}
	if response.Status != "failed" || !validErrorCode(response.Code) || response.DeliveryUncertain != (response.Code == smtpv1.CodeDeliveryUncertain) {
		m.recordFailure()
		return failed(true)
	}
	return &smtpv1.Error{Code: response.Code, DeliveryUncertain: response.DeliveryUncertain}
}

func (m *Manager) recordFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures++
	if m.failures >= 3 {
		m.blockedUntil = time.Now().Add(5 * time.Second)
	}
}

func validErrorCode(code string) bool {
	switch code {
	case smtpv1.CodeInvalidConfig, smtpv1.CodeInvalidMessage, smtpv1.CodeConnection, smtpv1.CodeTLS, smtpv1.CodeAuth, smtpv1.CodeRejected, smtpv1.CodeTimeout, smtpv1.CodeCanceled, smtpv1.CodeProtocol, smtpv1.CodeInternal, smtpv1.CodeDeliveryUncertain:
		return true
	}
	return false
}
