package runtimeplugins

import (
	"aster/billing/plugins/server.aster-cloud/legacy/pluginapi"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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
	root, policy string
	rules        trust
}

func New(root, policy string) *Manager { return &Manager{root: root, policy: policy} }

type State struct {
	Status, Protocol, ID, Version string
	PolicyRevision                uint64
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
