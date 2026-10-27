// Package pluginserver implements the SMTP and payment v1 server boundaries. It does not
// provide database, account, filesystem, or arbitrary-command capabilities.
package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/smtpv1"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Sender interface {
	Send(context.Context, smtpv1.Config, smtpv1.Message) error
}
type sessionKey struct{}
type session struct {
	nonce string
	used  bool
}

type Server struct {
	Manifest  pluginapi.Manifest
	ClientUID uint32
	Targets   []pluginapi.Target
	Sender    Sender
}

type boundedListener struct {
	net.Listener
	clientUID uint32
	slots     chan struct{}
}
type boundedConn struct {
	*net.UnixConn
	slots chan struct{}
	once  sync.Once
}

func (c *boundedConn) Close() error {
	err := c.UnixConn.Close()
	c.once.Do(func() { <-c.slots })
	return err
}
func (l *boundedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		unix, ok := connection.(*net.UnixConn)
		if !ok {
			connection.Close()
			continue
		}
		uid, err := pluginapi.PeerUID(unix)
		if err != nil || uid != l.clientUID {
			connection.Close()
			continue
		}
		select {
		case l.slots <- struct{}{}:
			return &boundedConn{UnixConn: unix, slots: l.slots}, nil
		default:
			connection.Close()
		}
	}
}

func (s Server) Serve(ctx context.Context, listener *net.UnixListener) error {
	if s.Sender == nil || s.Manifest.Validate(s.Manifest.CoreMin) != nil || len(s.Targets) == 0 || len(s.Targets) > 8 {
		return pluginapi.ErrInvalid
	}
	server := &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096,
		ErrorLog: log.New(io.Discard, "", 0), ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, sessionKey{}, &session{})
		}}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	err := server.Serve(&boundedListener{Listener: listener, clientUID: s.ClientUID, slots: make(chan struct{}, 8)})
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if r.Method != "POST" || r.Host != "plugin.local" || r.Header.Get("Content-Type") != "application/json" || len(r.URL.RawQuery) != 0 || r.URL.ForceQuery {
		http.Error(w, "invalid request", 400)
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pluginapi.MaxRequestBytes))
	if err != nil || pluginapi.DecodeStrict(raw, pluginapi.MaxRequestBytes, target) != nil {
		http.Error(w, "invalid request", 400)
		return false
	}
	return true
}

func reply(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	state, ok := r.Context().Value(sessionKey{}).(*session)
	if !ok {
		http.Error(w, "unavailable", 503)
		return
	}
	switch r.URL.Path {
	case "/v1/handshake":
		var hello pluginapi.Hello
		if !readJSON(w, r, &hello) {
			return
		}
		if state.nonce != "" || hello.Protocol != pluginapi.SMTPProtocol || !pluginapi.ValidDigest(hello.Nonce) || s.Manifest.Validate(hello.CoreVersion) != nil {
			http.Error(w, "incompatible", 409)
			return
		}
		state.nonce = hello.Nonce
		reply(w, pluginapi.Identity{Protocol: s.Manifest.Protocol, ID: s.Manifest.ID, Version: s.Manifest.Version, SHA256: s.Manifest.SHA256, Capabilities: s.Manifest.Capabilities, Nonce: hello.Nonce})
	case "/v1/smtp/send":
		// A handshake on another connection grants no permission here.
		if state.nonce == "" || state.used {
			http.Error(w, "handshake required", 409)
			return
		}
		var request pluginapi.SendRequest
		if !readJSON(w, r, &request) {
			return
		}
		state.used = true
		if request.Protocol != pluginapi.SMTPProtocol || request.Version != s.Manifest.Version || request.RequestID != state.nonce || request.TimeoutMS < 1 || request.TimeoutMS > 12000 {
			http.Error(w, "incompatible", 409)
			return
		}
		response := pluginapi.SendResponse{Protocol: pluginapi.SMTPProtocol, RequestID: request.RequestID, Status: "failed", Code: smtpv1.CodeInvalidConfig}
		if smtpv1.ValidateConfig(request.Config) != nil || smtpv1.ValidateMessage(request.Message) != nil || !pluginapi.Allowed(s.Targets, request.Config) {
			reply(w, response)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(request.TimeoutMS)*time.Millisecond)
		defer cancel()
		// A plugin panic must not disclose its payload or be reported as a safe
		// resend. Process exits/connection loss are handled conservatively by core.
		defer func() {
			if recover() != nil {
				response.Code = smtpv1.CodeDeliveryUncertain
				response.DeliveryUncertain = true
				reply(w, response)
			}
		}()
		err := s.Sender.Send(ctx, request.Config, request.Message)
		if err == nil {
			response.Status = "accepted"
			response.Code = ""
		} else {
			response.Code = smtpv1.CodeDeliveryUncertain
			response.DeliveryUncertain = true
			var safe *smtpv1.Error
			if errors.As(err, &safe) {
				if safe.DeliveryUncertain {
					response.Code = smtpv1.CodeDeliveryUncertain
					response.DeliveryUncertain = true
				} else {
					response.Code = safeCode(safe.Code)
					response.DeliveryUncertain = response.Code == smtpv1.CodeDeliveryUncertain
				}
			}
		}
		reply(w, response)
	default:
		http.Error(w, "not found", 404)
	}
}

func safeCode(code string) string {
	switch code {
	case smtpv1.CodeInvalidConfig, smtpv1.CodeInvalidMessage, smtpv1.CodeConnection, smtpv1.CodeTLS, smtpv1.CodeAuth, smtpv1.CodeRejected, smtpv1.CodeTimeout, smtpv1.CodeCanceled, smtpv1.CodeProtocol, smtpv1.CodeInternal:
		return code
	}
	return smtpv1.CodeDeliveryUncertain
}
