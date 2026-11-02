package pluginserver

import (
	"aster/billing/pkg/pluginapi"
	"aster/billing/pkg/pluginapi/serverv1"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"time"
)

// ServerServer authenticates a separate connection worker and bounds each RPC.
// Neither request payloads nor untrusted processor errors are logged.
type ServerServer struct {
	Manifest     pluginapi.Manifest
	ClientUID    uint32
	ConnectionID string
	Processor    serverv1.Processor
	operations   chan struct{}
}

func (s ServerServer) Serve(ctx context.Context, listener *net.UnixListener) error {
	if ctx == nil || listener == nil || s.Processor == nil || !pluginapi.ValidConnectionID(s.ConnectionID) || s.Manifest.Protocol != pluginapi.ServerProtocol || s.Manifest.Validate(s.Manifest.CoreMin) != nil {
		return pluginapi.ErrInvalid
	}
	s.operations = make(chan struct{}, 8)
	server := &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 4096, ErrorLog: log.New(io.Discard, "", 0), ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
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
func (s ServerServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	state, ok := r.Context().Value(sessionKey{}).(*session)
	if !ok {
		http.Error(w, "unavailable", 503)
		return
	}
	if r.URL.Path == "/v1/handshake" {
		var h pluginapi.Hello
		if !readJSON(w, r, &h) {
			return
		}
		if state.nonce != "" || h.Protocol != pluginapi.ServerProtocol || !pluginapi.ValidDigest(h.Nonce) || s.Manifest.Validate(h.CoreVersion) != nil {
			http.Error(w, "incompatible", 409)
			return
		}
		state.nonce = h.Nonce
		reply(w, pluginapi.Identity{Protocol: s.Manifest.Protocol, ID: s.Manifest.ID, Version: s.Manifest.Version, SHA256: s.Manifest.SHA256, Capabilities: s.Manifest.Capabilities, Nonce: h.Nonce})
		return
	}
	if r.URL.Path != "/v1/server/call" {
		http.Error(w, "not found", 404)
		return
	}
	if state.nonce == "" || state.used {
		http.Error(w, "handshake required", 409)
		return
	}
	state.used = true // An invalid operation consumes the connection too.
	var envelope pluginapi.ServerRequest
	if !readJSON(w, r, &envelope) {
		return
	}
	if envelope.Protocol != pluginapi.ServerProtocol || envelope.Version != s.Manifest.Version || envelope.RequestID != state.nonce || envelope.ConnectionID != s.ConnectionID || envelope.TimeoutMS < 1 || envelope.TimeoutMS > 12000 {
		http.Error(w, "incompatible", 409)
		return
	}
	var in serverv1.Request
	err := serverv1.Decode(envelope.Payload, &in)
	clear(envelope.Payload)
	if err != nil || in.Verify() != nil {
		http.Error(w, "incompatible", 409)
		return
	}
	select {
	case s.operations <- struct{}{}:
		defer func() { <-s.operations }()
	default:
		http.Error(w, "unavailable", 503)
		return
	}
	out := pluginapi.ServerResponse{Protocol: pluginapi.ServerProtocol, RequestID: envelope.RequestID, Status: "uncertain", Code: "unavailable", Payload: []byte{}}
	defer func() {
		if recover() != nil {
			reply(w, out)
		}
	}()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(envelope.TimeoutMS)*time.Millisecond)
	defer cancel()
	result, err := s.Processor.Call(ctx, in)
	if err == nil && ctx.Err() == nil && result.Matches(in) {
		payload, e := json.Marshal(result)
		if e == nil {
			out.Status = "confirmed"
			out.Code = ""
			out.Payload = payload
		}
	}
	reply(w, out)
}
